package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/whatbroke"
)

// timelineQuery zijn de gecontroleerde filters van een tijdlijn.
type timelineQuery struct {
	from, to time.Time
	classes  []events.Class
	limit    int
}

func parseTimelineQuery(w http.ResponseWriter, from, to *time.Time, class *string, limit *int) (timelineQuery, bool) {
	q := timelineQuery{to: time.Now(), limit: 200}
	if to != nil {
		q.to = *to
	}
	q.from = q.to.Add(-24 * time.Hour)
	if from != nil {
		q.from = *from
	}
	if !q.from.Before(q.to) {
		writeError(w, http.StatusBadRequest, "bad_request", "from moet voor to liggen")
		return q, false
	}
	if q.to.Sub(q.from) > 31*24*time.Hour {
		writeError(w, http.StatusBadRequest, "bad_request", "een tijdlijn is hoogstens 31 dagen")
		return q, false
	}
	if limit != nil {
		if *limit < 1 || *limit > whatbroke.MaxEntries {
			writeError(w, http.StatusBadRequest, "bad_request", "limit moet tussen 1 en 1000 liggen")
			return q, false
		}
		q.limit = *limit
	}
	if class != nil && strings.TrimSpace(*class) != "" {
		for c := range strings.SplitSeq(*class, ",") {
			c := events.Class(strings.TrimSpace(c))
			if c != events.ClassChange && c != events.ClassSymptom && c != events.ClassInfo {
				writeError(w, http.StatusBadRequest, "bad_request", "onbekende klasse "+string(c))
				return q, false
			}
			q.classes = append(q.classes, c)
		}
	}
	return q, true
}

func (s *Server) GetNodeTimeline(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.GetNodeTimelineParams) {
	if _, err := s.q.GetNode(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	tq, ok := parseTimelineQuery(w, params.From, params.To, params.Class, params.Limit)
	if !ok {
		return
	}
	s.writeTimeline(w, r, whatbroke.Scope{Nodes: []uuid.UUID{id}}, tq)
}

func (s *Server) GetClusterTimeline(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.GetClusterTimelineParams) {
	if _, err := s.q.GetCluster(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	tq, ok := parseTimelineQuery(w, params.From, params.To, params.Class, params.Limit)
	if !ok {
		return
	}
	s.writeTimeline(w, r, whatbroke.Scope{Clusters: []uuid.UUID{id}}, tq)
}

func (s *Server) writeTimeline(w http.ResponseWriter, r *http.Request, sc whatbroke.Scope, tq timelineQuery) {
	// Met een filter op klasse eerst alles, zodat er na het filter nog
	// genoeg regels over zijn.
	load := tq.limit
	if tq.classes != nil {
		load = whatbroke.MaxEntries
	}
	entries, more, err := whatbroke.Timeline(r.Context(), s.q, s.audit, sc, tq.from, tq.to, load)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tq.classes != nil {
		entries = slices.DeleteFunc(entries, func(e whatbroke.Entry) bool { return !slices.Contains(tq.classes, e.Class) })
		if len(entries) > tq.limit {
			entries, more = entries[len(entries)-tq.limit:], true
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Entries []whatbroke.Entry `json:"entries"`
		HasMore bool              `json:"has_more"`
	}{s.redactEntries(r, entries), more})
}

// redactEntries haalt voor een viewer het IP-adres uit SSH-logins.
func (s *Server) redactEntries(r *http.Request, entries []whatbroke.Entry) []whatbroke.Entry {
	if p, _ := principalFrom(r.Context()); p.User.Role == store.UserRoleAdmin {
		return entries
	}
	for i := range entries {
		entries[i] = whatbroke.Redact(entries[i])
	}
	return entries
}

// incidentAnalysis is de opgeslagen analyse met het tijdstip erbij.
type incidentAnalysis struct {
	*whatbroke.Analysis
	AnalyzedAt *time.Time `json:"analyzed_at"`
}

// incidentTimeline is de tijdlijn die de analyse las, met de afwijkingen in
// de metrics erbij.
func (s *Server) incidentTimeline(ctx context.Context, row store.Incident, a *whatbroke.Analysis) ([]whatbroke.Entry, bool, error) {
	subjects, err := s.q.ListIncidentSubjectsOf(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return nil, false, err
	}
	world, err := whatbroke.LoadWorld(ctx, s.q)
	if err != nil {
		return nil, false, err
	}
	from, to := whatbroke.AnalysisWindow(row, time.Now())
	sc := whatbroke.ScopeFor(row.ID, whatbroke.SubjectsOf(subjects), world)
	entries, more, err := whatbroke.Timeline(ctx, s.q, s.audit, sc, from, to, whatbroke.MaxEntries)
	if err != nil {
		return nil, false, err
	}
	if a != nil {
		for _, f := range a.Anomalies {
			entries = append(entries, f.Entry())
		}
		whatbroke.Sort(entries)
	}
	return entries, more, nil
}
