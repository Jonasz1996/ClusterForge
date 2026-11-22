package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
)

// De tools voor cronjobs lezen via dezelfde GET-handlers als de pagina
// Cronjobs: de gevonden jobs op de nodes en de beheerde jobs met hun runs.
// Commando's zijn op de node al gemaskeerd; uitvoer gaat nog eens door
// assistant.Mask, en een kijker ziet de uitvoer van een job als root niet.

const (
	// cronListBudget is wat de jobs in één antwoord mogen innemen.
	cronListBudget = 24 * 1024
	// cronOutputTail is hoeveel van de uitvoer van een run meegaat.
	cronOutputTail = 4 * 1024
)

func (s *Server) cronTools() []assistant.Tool {
	return []assistant.Tool{
		{
			Name: "cronjobs",
			Description: "De cronjobs van een cluster: de beheerde jobs van ClusterForge (schema in gewone taal, waar ze draaien, " +
				"aan of uit, bij een bestandssync van welke map naar welke nodes, de laatste run per node, mislukt in 24 uur, een " +
				"gemiste run) en de jobs die ClusterForge op de nodes vond in crontabs, cron.d, cron.daily en systemd-timers, met " +
				"de nodes waarop ze staan, verschillen tussen nodes en waarschuwingen. Leeg voor alle clusters.",
			Properties: map[string]any{"cluster": strProp(clusterDesc)},
			Required:   []string{"cluster"},
			Label: func(in map[string]any) string {
				if v := arg(in, "cluster"); v != "" {
					return "Cronjobs van " + v + " bekijken"
				}
				return "Alle cronjobs bekijken"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster string `json:"cluster"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				var id string
				if in.Cluster != "" {
					cid, err := s.findCluster(ctx, c, in.Cluster)
					if err != nil {
						return nil, err
					}
					id = cid.String()
				}
				var managed, found list[map[string]any]
				if err := s.view(ctx, c, s.ListCronJobs, &managed); err != nil {
					return nil, err
				}
				if err := s.view(ctx, c, s.ListCronjobs, &found); err != nil {
					return nil, err
				}
				var jobs []any
				for _, j := range managed.Items {
					if id != "" && j["cluster_id"] != id {
						continue
					}
					if cmd, ok := j["command"].(string); ok {
						j["command"] = assistant.Mask(cmd)
					}
					jobs = append(jobs, compact(j, "cluster_slug", "node_id", "on_calendar", "revision", "created_at"))
				}
				var groups []any
				for _, g := range found.Items {
					if id != "" && g["cluster_id"] != id {
						continue
					}
					groups = append(groups, compact(g, "key", "node_id", "cluster_slug"))
				}
				jobs, cutJobs := fit(jobs, cronListBudget/2, false)
				groups, cutGroups := fit(groups, cronListBudget/2, false)
				out := map[string]any{
					"beheerd":  orNote(jobs, "geen beheerde cronjobs"),
					"gevonden": orNote(groups, "geen cronjobs gevonden op de nodes"),
				}
				if cutJobs+cutGroups > 0 {
					out["afgekapt"] = fmt.Sprintf("%d beheerde jobs en %d clusters met gevonden jobs vielen weg; vraag één cluster", cutJobs, cutGroups)
				}
				return out, nil
			},
		},
		{
			Name: "cron_runs",
			Description: "De runs van één beheerde cronjob, nieuwste eerst: node, waarom (schema, met de hand, herhaling), uitkomst, " +
				"exitcode, duur, en bij een bestandssync per doel de bestanden en bytes of waarom het doel weigerde. Met het einde van " +
				"de uitvoer van de nieuwste run die mislukte.",
			Properties: map[string]any{
				"job":     strProp("Naam of id van de beheerde cronjob, zoals de tool cronjobs hem noemt"),
				"cluster": strProp("Naam of id van het cluster van de job, of leeg als de naam uniek is"),
			},
			Required: []string{"job", "cluster"},
			Label:    func(in map[string]any) string { return "Runs van " + arg(in, "job") + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Job     string `json:"job"`
					Cluster string `json:"cluster"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				jobID, err := s.findCronJob(ctx, c, in.Job, in.Cluster)
				if err != nil {
					return nil, err
				}
				var detail struct {
					Job  map[string]any   `json:"job"`
					Runs []map[string]any `json:"runs"`
				}
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetCronJob(w, r, jobID) }, &detail); err != nil {
					return nil, err
				}
				runs := make([]any, 0, len(detail.Runs))
				var failed string
				for _, r := range detail.Runs {
					if res := r["result"]; failed == "" && (res == "failed" || res == "timeout") {
						failed, _ = r["id"].(string)
					}
					runs = append(runs, compact(r, "node_id"))
				}
				runs, cut := fit(runs, cronListBudget, false)
				out := map[string]any{
					"job":  compact(detail.Job, "cluster_slug", "node_id", "on_calendar", "revision", "nodes", "command"),
					"runs": orNote(runs, "deze job heeft nog niet gedraaid"),
				}
				if cut > 0 {
					out["afgekapt"] = fmt.Sprintf("%d oudere runs vielen weg", cut)
				}
				if failed != "" {
					runID, _ := uuid.Parse(failed)
					var run struct {
						Output       string `json:"output"`
						OutputHidden bool   `json:"output_hidden"`
					}
					if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetCronRun(w, r, runID) }, &run); err != nil {
						return nil, err
					}
					switch {
					case run.OutputHidden:
						out["uitvoer_laatste_fout"] = "verborgen: de job draait als root en de uitvoer is alleen voor beheerders"
					default:
						text := run.Output
						if len(text) > cronOutputTail {
							text = "…" + text[len(text)-cronOutputTail:]
						}
						out["uitvoer_laatste_fout"] = assistant.Mask(text)
					}
				}
				return out, nil
			},
		},
	}
}

// findCronJob zoekt een beheerde job op id of naam, eventueel binnen een
// cluster.
func (s *Server) findCronJob(ctx context.Context, c assistant.Caller, name, cluster string) (uuid.UUID, error) {
	var clusterID string
	if cluster != "" {
		id, err := s.findCluster(ctx, c, cluster)
		if err != nil {
			return uuid.Nil, err
		}
		clusterID = id.String()
	}
	var jobs list[map[string]any]
	if err := s.view(ctx, c, s.ListCronJobs, &jobs); err != nil {
		return uuid.Nil, err
	}
	var hits []string
	var names []string
	for _, j := range jobs.Items {
		if clusterID != "" && j["cluster_id"] != clusterID {
			continue
		}
		id, _ := j["id"].(string)
		n, _ := j["name"].(string)
		names = append(names, n)
		if id == name || strings.EqualFold(n, name) {
			hits = append(hits, id)
		}
	}
	switch len(hits) {
	case 1:
		return uuid.Parse(hits[0])
	case 0:
		return uuid.Nil, assistant.ToolError{Msg: "geen beheerde cronjob " + name + "; er zijn: " + known(names)}
	}
	return uuid.Nil, assistant.ToolError{Msg: "meer clusters hebben een job " + name + "; geef het cluster mee"}
}
