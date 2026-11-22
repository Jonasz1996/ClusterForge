package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// changesQuery zijn de gecontroleerde filters van een journaalvraag.
type changesQuery struct {
	since, until time.Time
	kinds        []string
	limit        int
}

func parseChangesQuery(w http.ResponseWriter, from, to *time.Time, kind *string, limit *int) (changesQuery, bool) {
	q := changesQuery{until: time.Now(), limit: 200}
	if to != nil {
		q.until = *to
	}
	q.since = q.until.Add(-7 * 24 * time.Hour)
	if from != nil {
		q.since = *from
	}
	if !q.since.Before(q.until) {
		writeError(w, http.StatusBadRequest, "bad_request", "from moet voor to liggen")
		return q, false
	}
	if limit != nil {
		if *limit < 1 || *limit > 1000 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit moet tussen 1 en 1000 liggen")
			return q, false
		}
		q.limit = *limit
	}
	q.kinds = []string{}
	if kind != nil && strings.TrimSpace(*kind) != "" {
		for k := range strings.SplitSeq(*kind, ",") {
			k = strings.TrimSpace(k)
			if !slices.Contains(journal.Kinds, k) {
				writeError(w, http.StatusBadRequest, "bad_request", "onbekende soort "+k)
				return q, false
			}
			q.kinds = append(q.kinds, k)
		}
	}
	return q, true
}

func (s *Server) ListNodeChanges(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.ListNodeChangesParams) {
	ctx := r.Context()
	if _, err := s.q.GetNode(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	cq, ok := parseChangesQuery(w, params.From, params.To, params.Kind, params.Limit)
	if !ok {
		return
	}
	rows, err := s.q.ListNodeChanges(ctx, store.ListNodeChangesParams{
		NodeID: id, Since: cq.since, Until: cq.until, Kinds: cq.kinds, Lim: int32(cq.limit + 1),
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	changes := make([]changeRow, len(rows))
	for i, row := range rows {
		changes[i] = changeRow(row)
	}
	s.writeChanges(w, r, changes, cq.limit, store.ListNodesWithoutJournalParams{NodeID: &id, JournalSince: protocol.JournalSince})
}

func (s *Server) ListClusterChanges(w http.ResponseWriter, r *http.Request, id uuid.UUID, params gen.ListClusterChangesParams) {
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	cq, ok := parseChangesQuery(w, params.From, params.To, params.Kind, params.Limit)
	if !ok {
		return
	}
	rows, err := s.q.ListClusterChanges(ctx, store.ListClusterChangesParams{
		ClusterID: &id, Since: cq.since, Until: cq.until, Kinds: cq.kinds, Lim: int32(cq.limit + 1),
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	changes := make([]changeRow, len(rows))
	for i, row := range rows {
		changes[i] = changeRow(row)
	}
	s.writeChanges(w, r, changes, cq.limit, store.ListNodesWithoutJournalParams{ClusterID: &id, JournalSince: protocol.JournalSince})
}

// changeRow is een regel uit ListNodeChanges of ListClusterChanges; beide
// hebben dezelfde kolommen.
type changeRow store.ListNodeChangesRow

func (s *Server) writeChanges(w http.ResponseWriter, r *http.Request, rows []changeRow, limit int, old store.ListNodesWithoutJournalParams) {
	p, _ := principalFrom(r.Context())
	admin := p.User.Role == store.UserRoleAdmin
	out := gen.NodeChanges{Changes: make([]gen.NodeChange, 0, len(rows)), AgentsTooOld: []gen.ChangesNode{}}
	if len(rows) > limit {
		rows, out.HasMore = rows[:limit], true
	}
	for _, row := range rows {
		e := journal.FromRow(row.Kind, row.Subject, row.Action, row.Detail)
		summary := row.Summary
		if !admin {
			// Een viewer ziet geen IP-adressen van logins.
			summary = journal.Describe(e, true)
			delete(e.Detail, "ip")
		}
		detail := e.Detail
		for k, v := range map[string]string{"from": e.From, "to": e.To, "by": e.By} {
			if v != "" {
				detail[k] = v
			}
		}
		out.Changes = append(out.Changes, gen.NodeChange{
			Id: row.ID, NodeId: row.NodeID, Hostname: row.Hostname, Ts: row.Ts, ObservedAt: row.ObservedAt,
			Source: gen.NodeChangeSource(row.Source), Kind: row.Kind, Subject: row.Subject, Action: row.Action,
			Summary: summary, Detail: detail, JobId: nullableOf(row.JobID),
		})
	}
	nodes, err := s.q.ListNodesWithoutJournal(r.Context(), old)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, n := range nodes {
		out.AgentsTooOld = append(out.AgentsTooOld, gen.ChangesNode{NodeId: n.ID, Hostname: n.Hostname})
	}
	writeJSON(w, http.StatusOK, out)
}
