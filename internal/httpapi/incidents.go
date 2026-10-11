package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/whatbroke"
)

// incidentHistory zijn de actions in de geschiedenis van een incident: wat
// er met de status gebeurde, de incidenten zelf en de tests. Nooit iets uit
// de beveiliging, zodat een viewer hem mag zien.
var incidentHistory = slices.Concat(
	events.Actions(events.CategoryStatus),
	events.Actions(events.CategoryIncident),
	[]string{"service.status_changed", "failover.fault_injected", "failover.fault_cleared", "failover.finished", "backup.verify_finished"},
)

// historyLimit is het grootste aantal regels in de geschiedenis van een
// incident.
const historyLimit = 200

func (s *Server) ListIncidents(w http.ResponseWriter, r *http.Request, params gen.ListIncidentsParams) {
	until := time.Now()
	if params.To != nil {
		until = *params.To
	}
	since := until.Add(-30 * 24 * time.Hour)
	if params.From != nil {
		since = *params.From
	}
	if !since.Before(until) {
		writeError(w, http.StatusBadRequest, "bad_request", "from moet voor to liggen")
		return
	}
	limit := 100
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 500 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit moet tussen 1 en 500 liggen")
			return
		}
		limit = *params.Limit
	}
	var status *string
	if params.Status != nil {
		if !params.Status.Valid() {
			writeError(w, http.StatusBadRequest, "bad_request", "status moet open of resolved zijn")
			return
		}
		st := string(*params.Status)
		status = &st
	}
	rows, err := s.q.ListIncidents(r.Context(), store.ListIncidentsParams{
		Status: status, IncludeTests: deref(params.IncludeTests), Since: since, Until: until, ClusterID: params.Cluster,
		Lim: int32(limit + 1),
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.IncidentList{Items: []gen.Incident{}}
	if len(rows) > limit {
		rows, out.HasMore = rows[:limit], true
	}
	out.Items, err = s.incidents(r, rows)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetIncident(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	row, err := s.q.GetIncident(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "incident niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	incs, err := s.incidents(r, []store.Incident{row})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	inc := incs[0]

	// De geschiedenis: van vijf minuten voor het begin tot een minuut na het
	// einde, over alles wat geraakt was.
	from, to := inc.StartedAt.Add(-5*time.Minute), time.Now()
	if t, err := inc.ResolvedAt.Get(); err == nil {
		to = t.Add(time.Minute)
	}
	anyOf := []uuid.UUID{inc.Id}
	for _, sub := range inc.Subjects {
		anyOf = append(anyOf, sub.Id)
		if c, err := sub.ClusterId.Get(); err == nil && !slices.Contains(anyOf, c) {
			anyOf = append(anyOf, c)
		}
	}
	history, next, err := s.audit.List(ctx, audit.Filter{
		From: &from, To: &to, AnyOf: anyOf, Actions: incidentHistory, Limit: historyLimit,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p, _ := principalFrom(ctx)
	if p.User.Role != store.UserRoleAdmin {
		for i := range history {
			history[i].IP, history[i].Session = nil, nil
		}
	}
	slices.Reverse(history)
	analysis := whatbroke.Decode(row)
	timeline, timelineMore, err := s.incidentTimeline(ctx, row, analysis)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var stored *incidentAnalysis
	if analysis != nil {
		stored = &incidentAnalysis{Analysis: analysis, AnalyzedAt: row.AnalyzedAt}
	}
	writeJSON(w, http.StatusOK, struct {
		Incident     gen.Incident      `json:"incident"`
		History      []audit.Entry     `json:"history"`
		HistoryMore  bool              `json:"history_more"`
		Analysis     *incidentAnalysis `json:"analysis"`
		Timeline     []whatbroke.Entry `json:"timeline"`
		TimelineMore bool              `json:"timeline_more"`
	}{inc, history, next != nil, stored, s.redactEntries(r, timeline), timelineMore})
}

func (s *Server) SetIncidentNote(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.IncidentNote
	if !decode(w, r, &req) {
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > 10000 {
		writeError(w, http.StatusBadRequest, "validation", "een notitie is hoogstens 10.000 tekens")
		return
	}
	ctx := r.Context()
	var row store.Incident
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.GetIncident(ctx, id)
		if err != nil {
			return err
		}
		if old.Note == note {
			row = old
			return nil
		}
		if row, err = q.SetIncidentNote(ctx, store.SetIncidentNoteParams{ID: id, Note: note}); err != nil {
			return err
		}
		// Het cluster van het eerste onderwerp, zoals in de events van de
		// detector.
		subjects, err := q.ListIncidentSubjectsOf(ctx, []uuid.UUID{id})
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
			Actor: events.User(p.User.ID), SubjectType: "incident", SubjectID: id.String(), ClusterID: cluster,
			Action: "incident.note_updated", Payload: map[string]any{"number": row.Number, "title": row.Title, "empty": note == ""},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "incident niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	incs, err := s.incidents(r, []store.Incident{row})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, incs[0])
}

// incidents zet incidenten met hun onderwerpen om naar de API.
func (s *Server) incidents(r *http.Request, rows []store.Incident) ([]gen.Incident, error) {
	out := make([]gen.Incident, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	byID := map[uuid.UUID]*gen.Incident{}
	for i, row := range rows {
		ids[i] = row.ID
		out[i] = gen.Incident{
			Id: row.ID, Number: int(row.Number), Title: row.Title, Severity: gen.IncidentSeverity(row.Severity),
			Status: gen.IncidentStatus(row.Status), StartedAt: row.StartedAt, DetectedAt: row.DetectedAt,
			ResolvedAt: nullableOf(row.ResolvedAt), CausedByTest: row.CausedByTest, TestRunId: nullableOf(row.TestRunID),
			Note: row.Note, UpdatedAt: row.UpdatedAt, Subjects: []gen.IncidentSubject{},
			Cause: nullable.NewNullNullable[gen.IncidentTopCause](),
		}
		if c := whatbroke.Decode(row).TopCause(); c != nil {
			out[i].Cause = nullable.NewNullableWithValue(gen.IncidentTopCause{Summary: c.Summary, Level: gen.IncidentTopCauseLevel(c.Level)})
		}
		byID[row.ID] = &out[i]
	}
	subjects, err := s.q.ListIncidentSubjects(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	for _, sub := range subjects {
		inc := byID[sub.IncidentID]
		inc.Subjects = append(inc.Subjects, gen.IncidentSubject{
			Type: gen.IncidentSubjectType(sub.SubjectType), Id: sub.SubjectID, Name: sub.Name,
			ClusterId: nullableOf(sub.ClusterID), FirstStatus: sub.FirstStatus, WorstStatus: sub.WorstStatus,
			LastStatus: sub.LastStatus, Reason: sub.Reason, Since: sub.Since, Until: nullableOf(sub.Until),
			Present: sub.Present,
		})
	}
	return out, nil
}
