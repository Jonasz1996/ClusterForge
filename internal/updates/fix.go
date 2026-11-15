package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ErrNoReport: er is nog geen Docker-update van dit project op deze node.
var ErrNoReport = errors.New("er is nog geen Docker-update van dit project op deze node")

// ReadCompose leest de bestanden van een project via een korte run; geheime
// waarden zijn plaatshouders.
func (s *Service) ReadCompose(ctx context.Context, nodeID uuid.UUID, project string) ([]protocol.ComposeFile, error) {
	rt, err := s.q.GetNodeRuntime(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	switch {
	case !rt.HasAgent:
		return nil, &ConflictError{"de node heeft geen agent"}
	case rt.AgentProtocol < protocol.UpdatesSince:
		return nil, &ConflictError{fmt.Sprintf("de agent heeft protocol %d; het compose-bestand lezen vraagt %d. Werk de agent eerst bij", rt.AgentProtocol, protocol.UpdatesSince)}
	}
	spec := protocol.RunSpec{
		ID: "read-" + uuid.NewString(), Kind: protocol.RunDocker, Title: "Compose-bestanden lezen", TimeoutSeconds: 60,
		Docker: &protocol.DockerRun{Smart: true, Read: true, Projects: []string{project}},
	}
	if err := spec.Validate(); err != nil {
		return nil, &FieldError{err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := s.bus.Run(ctx, nodeID, protocol.RunRequest{Op: protocol.RunStart, Spec: &spec})
	for err == nil && res.Status != nil && !res.Status.Done() {
		select {
		case <-ctx.Done():
			return nil, errors.New("de agent las het compose-bestand niet binnen 90 s")
		case <-time.After(300 * time.Millisecond):
		}
		res, err = s.bus.Run(ctx, nodeID, protocol.RunRequest{Op: protocol.RunPoll, RunID: spec.ID})
	}
	switch {
	case err != nil:
		return nil, fmt.Errorf("de agent: %w", err)
	case res.Status == nil:
		return nil, errors.New(firstOf(res.Error, "de agent gaf geen antwoord"))
	case res.Status.State != protocol.RunOK:
		return nil, &ConflictError{firstOf(res.Status.Error, "lezen mislukte")}
	}
	return res.Status.Files, nil
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// Report is het bewaarde rapport van een project op een node.
type Report struct {
	NodeID         uuid.UUID
	JobID          *uuid.UUID
	CreatedAt      time.Time
	ConversationID *uuid.UUID
	protocol.DockerReport
}

// Reports geeft de bewaarde rapporten per node.
func (s *Service) Reports(ctx context.Context) (map[uuid.UUID][]Report, error) {
	rows, err := s.q.ListDockerReports(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]Report{}
	for _, r := range rows {
		rep := Report{NodeID: r.NodeID, JobID: r.JobID, CreatedAt: r.CreatedAt, ConversationID: r.ConversationID}
		if json.Unmarshal(r.Report, &rep.DockerReport) != nil {
			continue
		}
		out[r.NodeID] = append(out[r.NodeID], rep)
	}
	return out, nil
}

// FixTask maakt de opdracht voor de assistent: het rapport, de logs, het
// compose-bestand en de release notes van de bron.
func (s *Service) FixTask(ctx context.Context, nodeID uuid.UUID, project string) (assistant.Task, error) {
	row, err := s.q.GetDockerReport(ctx, store.GetDockerReportParams{NodeID: nodeID, Project: project})
	if errors.Is(err, pgx.ErrNoRows) {
		return assistant.Task{}, ErrNoReport
	}
	if err != nil {
		return assistant.Task{}, err
	}
	var rep protocol.DockerReport
	if err := json.Unmarshal(row.Report, &rep); err != nil {
		return assistant.Task{}, err
	}
	n, err := s.q.GetNode(ctx, nodeID)
	if err != nil {
		return assistant.Task{}, err
	}
	var b strings.Builder
	logs := rep.Logs
	rep.Logs = ""
	data, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Fprintf(&b, "## Rapport van de update\n```json\n%s\n```\n\n## docker compose logs (laatste regels)\n```\n%s\n```\n", data, assistant.Mask(logs))
	files, ferr := s.ReadCompose(ctx, nodeID, project)
	if ferr != nil {
		fmt.Fprintf(&b, "\n## Compose-bestanden\nNiet gelezen: %s\n", ferr)
	}
	for _, f := range files {
		lang := "yaml"
		if f.Env {
			lang = "sh"
		}
		fmt.Fprintf(&b, "\n## Bestand %s\n```%s\n%s\n```\n", f.Path, lang, assistant.Mask(f.Content))
	}
	seen := map[string]bool{}
	for _, sv := range rep.Services {
		if seen[sv.Image] {
			continue
		}
		seen[sv.Image] = true
		notes, errs := s.Notes.Fetch(ctx, sv.Image, sv.Source)
		for _, nt := range notes {
			fmt.Fprintf(&b, "\n## %s van %s (%s)\n%s\n", nt.Where, sv.Image, nt.URL, nt.Text)
		}
		for _, e := range errs {
			fmt.Fprintf(&b, "\n(Release notes van %s niet opgehaald: %s)\n", sv.Image, e)
		}
	}
	question := fmt.Sprintf("Stel een fix voor de Docker-update van %s op %s voor.", project, n.Node.Hostname)
	id := nodeID
	return assistant.Task{
		Kind: assistant.KindExplain, Title: "Fix voorstellen: " + project + " op " + n.Node.Hostname, Question: question,
		SubjectType: assistant.SubjectNode, SubjectID: &id, Effort: assistant.EffortHigh,
		Prompt: fmt.Sprintf(`%s

ClusterForge werkte het Docker Compose-project %s op node %s bij: docker compose pull, down en up -d, en testte daarna of elke container draait en gezond is. Het resultaat staat in het rapport (result: %s). Onderaan staan het rapport, de laatste regels van de logs, de compose-bestanden zoals ze nu op de node staan, en de release notes of documentatie van de bron van de images (GitHub of Docker Hub).

Zoek waarom de containers na de update niet goed draaien, of wat er bij een volgende update mis kan gaan. Let vooral op:
- omgevingsvariabelen (-e, environment, env_file) die hernoemd, verplicht of verwijderd zijn (env_added en env_removed in het rapport zijn de namen in het image zelf), en wat de release notes daarover zeggen;
- gewijzigde poorten, paden van volumes of configuratiebestanden, en breaking changes tussen old_version en new_version;
- de foutmeldingen in de log, letterlijk aangehaald.

Antwoord in precies deze vorm:

## In het kort
Een of twee zinnen: wat er mis is en hoe zeker je bent.

## Bewijs
De regels uit de log en de zinnen uit de release notes (met de link) die het aantonen.

## Voorgestelde fix
Wat er in het compose-bestand (of het env-bestand) moet veranderen, en daarna het volledige aangepaste bestand in één codeblok met de taal yaml (of sh voor een env-bestand), met als eerste regel een commentaar met het pad, zoals "# %s". Laat de plaatshouders «geheim-1», «geheim-2», … precies staan: ClusterForge zet daar op de node de echte waarden terug. Verander niets anders dan nodig is. Is er geen wijziging in het bestand nodig (bijvoorbeeld wachten op een nieuwe versie of terug naar de vorige tag), zeg dat dan en geef geen codeblok.

## Nakijken
Wat de gebruiker zelf moet controleren voor hij opslaat.

Je voert zelf niets uit. De gebruiker opent het voorstel in ClusterForge onder Updates, bij het project, met Compose-bestand, kijkt het na en slaat het op; ClusterForge start het project dan opnieuw, test het en zet bij een fout de vorige versie terug.

Wat ClusterForge weet:
%s`, question, project, n.Node.Hostname, rep.Result, firstConfig(rep), b.String()),
	}, nil
}

func firstConfig(rep protocol.DockerReport) string {
	if len(rep.ConfigFiles) > 0 {
		return rep.ConfigFiles[0]
	}
	return "/pad/naar/compose.yml"
}

// SetFixConversation onthoudt het gesprek met het voorstel bij het
// rapport.
func (s *Service) SetFixConversation(ctx context.Context, nodeID uuid.UUID, project string, conv uuid.UUID) error {
	return s.q.SetDockerReportConversation(ctx, store.SetDockerReportConversationParams{NodeID: nodeID, Project: project, ConversationID: &conv})
}

// AutoFix laat de assistent na een mislukte automatische update vanzelf
// een fix voorstellen, als die ingesteld is. Het loopt op de achtergrond.
func (s *Service) AutoFix(asst *assistant.Service) func(ctx context.Context, nodeID, jobID uuid.UUID, rep protocol.DockerReport) {
	return func(ctx context.Context, nodeID, _ uuid.UUID, rep protocol.DockerReport) {
		if asst == nil || !asst.Available() {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
			defer cancel()
			task, err := s.FixTask(ctx, nodeID, rep.Project)
			if err != nil {
				return
			}
			turn, err := asst.Start(ctx, assistant.SystemCaller(), task)
			if err != nil {
				return
			}
			res := turn.Run(ctx, func(assistant.Event) {})
			if res.ConversationID != uuid.Nil {
				_ = s.SetFixConversation(ctx, nodeID, rep.Project, res.ConversationID)
			}
		}()
	}
}
