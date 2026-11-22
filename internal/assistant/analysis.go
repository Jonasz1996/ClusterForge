package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Automatisch analyseren: hoogstens zoveel per dag, alleen incidenten die
// het afgelopen uur ontdekt zijn, en pas na een korte wachttijd, zodat de
// tijdlijn en de metrics van het begin er zijn.
const (
	MaxAutoPerDay = 10
	autoWindow    = time.Hour
	autoDelay     = 2 * time.Minute
	// autoTimeout is hoe lang een automatische analyse mag duren.
	autoTimeout = 10 * time.Minute
)

var (
	// ErrIncidentNotFound is een incident dat niet bestaat.
	ErrIncidentNotFound = errors.New("incident niet gevonden")
	// ErrAdminAnalysis is een viewer die een analyse met de rechten van een
	// beheerder zou vervangen.
	ErrAdminAnalysis = errors.New("een beheerder liet dit incident al analyseren; alleen een beheerder maakt die analyse opnieuw. Je kunt wel een vraag over het incident stellen")
)

// De kopjes van een analyse, in hun volgorde.
var analysisHeadings = []string{"## In het kort", "## Waarschijnlijke oorzaak", "## Nog te controleren", "## Wat je kunt doen"}

// Analyze begint een analyse van een incident met de rechten van c. Het
// model krijgt meteen wat de tool incident geeft (het incident, de analyse
// van What Broke, de tijdlijn en de geschiedenis) en kijkt met de andere
// tools verder. Het resultaat komt bij het incident, met model en datum.
func (s *Service) Analyze(ctx context.Context, c Caller, id uuid.UUID) (*Turn, error) {
	if _, _, err := s.access(ctx, c); err != nil {
		return nil, err
	}
	inc, err := s.q.GetIncident(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrIncidentNotFound
	}
	if err != nil {
		return nil, err
	}
	// Een analyse met de rechten van een beheerder ziet een viewer niet;
	// hij vervangt ze ook niet door een met minder gegevens.
	if !c.admin() && inc.AiScope == "admin" {
		return nil, ErrAdminAnalysis
	}
	in, _ := json.Marshal(map[string]string{"incident": id.String()})
	data, isErr := s.runTool(ctx, c, "incident", in)
	if isErr {
		return nil, fmt.Errorf("gegevens van incident #%d voor de analyse: %s", inc.Number, data)
	}
	dbs, err := s.q.IncidentDatabases(ctx, id)
	if err != nil {
		return nil, err
	}
	scope := "viewer"
	if c.admin() {
		scope = "admin"
	}
	return s.Start(ctx, c, Task{
		Kind:        KindAnalysis,
		Title:       fmt.Sprintf("Analyse van incident #%d: %s", inc.Number, inc.Title),
		Question:    fmt.Sprintf("Analyseer incident #%d: %s.", inc.Number, inc.Title),
		Prompt:      analysisPrompt(inc, data, dbs),
		Effort:      EffortHigh,
		SubjectType: SubjectIncident, SubjectID: &id, IncidentID: &id, Claim: &id,
		Finish: func(ctx context.Context, r Result) error {
			return s.saveAnalysis(ctx, c, inc, scope, r)
		},
	})
}

// analysisPrompt is de opdracht voor de analyse van een incident; dbs zijn
// de clusters en nodes met een database die het raakte.
func analysisPrompt(inc store.Incident, data string, dbs []string) string {
	var db string
	if len(dbs) > 0 {
		db = fmt.Sprintf(`

Het incident raakte %s, met een database. Gaan de reden of de tijdlijn over de database, of kan ze meespelen, kijk dan met de tool database hoe ze ervoor staat: welke nodes meedoen en in welke toestand, wie het VIP heeft, de laatste herstart en de dumps; met database_clients welke apps de database gebruiken en dus mee geraakt waren; en met heal wat Zelfherstel vaststelde, welk plan het heeft en of het al herstelde. Leg in gewone woorden uit wat Galera of Patroni deed, zoals een node die het cluster verliet en met een volledige kopie (SST) terugkwam, een failover naar een andere primary, of een cluster dat zonder meerderheid stopte. Noem bij Wat je kunt doen het runbook uit het hoofdstuk Database van de documentatie (tool documentatie) en Beheer bij het onderdeel Database op de pagina van het cluster, of bij een database die ClusterForge niet uitrolde Nu herstellen onder Zelfherstel bij Databases.`, joinNames(dbs))
	}
	return fmt.Sprintf(`Analyseer incident #%d, %q. Onderaan staan de gegevens van ClusterForge over dit incident, zoals de tool incident ze geeft: het incident, de analyse van What Broke met de waarschijnlijke oorzaken, de tijdlijn en de geschiedenis. Kijk met de tools verder waar dat helpt, bijvoorbeeld naar de metrics van het venster, de wijzigingen op de nodes, de impact op afhankelijke diensten of eerdere incidenten.%s

Schrijf het resultaat in precies deze vorm, met deze vier kopjes en niets ervoor:

## In het kort
Twee of drie zinnen: wat er gebeurde, wanneer en hoe lang, wat er geraakt was, en of het voorbij is.

## Waarschijnlijke oorzaak
De oorzaak met het bewijs: tijdstippen, wijzigingen, metrics en events, met links naar de schermen. Zeg hoe zeker je bent. Wijk je af van What Broke, zeg dan waarom.

## Nog te controleren
Wat de gegevens niet laten zien en wat de gebruiker zelf moet nakijken, als korte lijst.

## Wat je kunt doen
Concrete stappen, elk met een link naar het scherm in ClusterForge waar de gebruiker het zelf doet. Je voert zelf niets uit.

Gegevens van ClusterForge (tool incident):
%s`, inc.Number, inc.Title, db, data)
}

// joinNames schrijft a, b en c.
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
}

// AnalysisText is het resultaat van een analyse zonder wat het model voor
// het eerste kopje schreef, zoals 'ik kijk eerst naar de metrics'.
func AnalysisText(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, analysisHeadings[0]); i > 0 {
		return raw[i:]
	}
	return raw
}

// MissingHeadings zijn de kopjes die een analyse mist.
func MissingHeadings(text string) []string {
	var out []string
	for _, h := range analysisHeadings {
		if !strings.Contains(text, h) {
			out = append(out, strings.TrimPrefix(h, "## "))
		}
	}
	return out
}

func (s *Service) saveAnalysis(ctx context.Context, c Caller, inc store.Incident, scope string, r Result) error {
	text := AnalysisText(r.Text)
	if missing := MissingHeadings(text); len(missing) > 0 {
		s.log.WarnContext(ctx, "analyse van de assistent mist kopjes", "incident", inc.Number, "missing", missing)
	}
	now := s.Now()
	model := strings.Join(r.Models, ", ")
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.SetIncidentAI(ctx, store.SetIncidentAIParams{
			ID: inc.ID, AiAnalysis: &text, AiModel: model, AiAt: &now, AiBy: c.user(), AiAutomatic: c.System, AiScope: scope,
			AiConversationID: &r.ConversationID, AiCostMicrousd: r.CostMicro,
		}); err != nil {
			return err
		}
		subjects, err := q.ListIncidentSubjectsOf(ctx, []uuid.UUID{inc.ID})
		if err != nil {
			return err
		}
		var cluster *uuid.UUID
		for _, sub := range subjects {
			if sub.ClusterID != nil {
				cluster = sub.ClusterID
				break
			}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: c.actor(), SubjectType: "incident", SubjectID: inc.ID.String(), ClusterID: cluster, Action: "incident.analyzed",
			Payload: map[string]any{
				"number": inc.Number, "title": inc.Title, "model": model, "cost_usd": USD(r.CostMicro), "automatic": c.System,
				"conversation_id": r.ConversationID.String(),
			},
		})
	})
}

// AutoAnalyze analyseert nieuwe prod-incidenten met ernst hoog of kritiek,
// als een beheerder dat aanzette: elk incident één keer, hoogstens tien per
// dag, binnen het budget. De analyses lopen op de achtergrond. Het is een
// taak van de planner.
func (s *Service) AutoAnalyze(ctx context.Context, now time.Time) error {
	if !s.Available() {
		return nil
	}
	cur, err := s.current(ctx)
	if err != nil || cur == nil || !cur.Enabled || !cur.AutoAnalyze {
		return err
	}
	rows, err := s.q.ListAutoAnalyzeCandidates(ctx, store.ListAutoAnalyzeCandidatesParams{
		Since: now.Add(-autoWindow), Until: now.Add(-autoDelay),
	})
	if err != nil || len(rows) == 0 {
		return err
	}
	y, m, d := now.Date()
	done, err := s.q.CountSystemConversationsSince(ctx, store.CountSystemConversationsSinceParams{
		Kind: KindAnalysis, Since: time.Date(y, m, d, 0, 0, 0, 0, now.Location()),
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		if done >= MaxAutoPerDay {
			s.log.InfoContext(ctx, "automatisch analyseren: het maximum van vandaag is bereikt", "incident", r.Number, "max", MaxAutoPerDay)
			return nil
		}
		turn, err := s.Analyze(ctx, SystemCaller(), r.ID)
		var le LimitError
		switch {
		case errors.As(err, &le):
			s.log.InfoContext(ctx, "automatisch analyseren: "+le.Msg, "incident", r.Number)
			return nil
		case errors.Is(err, ErrRunning):
			continue
		case err != nil:
			s.log.WarnContext(ctx, "automatisch analyseren", "incident", r.Number, "err", err)
			continue
		}
		done++
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoTimeout)
			defer cancel()
			if res := turn.Run(ctx, func(Event) {}); !res.Complete {
				s.log.WarnContext(ctx, "automatische analyse gaf geen resultaat", "incident", r.Number)
			}
		}()
	}
	return nil
}

// Wait wacht tot de automatische analyses klaar zijn.
func (s *Service) Wait() { s.wg.Wait() }
