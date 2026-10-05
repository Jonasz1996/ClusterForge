package httpapi

import (
	"context"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// De logboekhandlers schrijven audit.Entry en audit.Info rechtstreeks: hun
// JSON is precies AuditEntry en AuditInfo uit de API-beschrijving.

func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, params gen.ListAuditParams) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	f, msg := auditFilter(params.From, params.To, params.User, params.Cluster, params.Node, params.Job,
		(*string)(params.ActorType), params.Category, params.Q)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	f.Before = params.Before
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > audit.MaxLimit {
			writeError(w, http.StatusBadRequest, "bad_request", "limit moet tussen 1 en 200 liggen")
			return
		}
		f.Limit = *params.Limit
	}
	items, next, err := s.audit.List(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []audit.Entry `json:"items"`
		NextBefore *int64        `json:"next_before"`
	}{items, next})
}

func (s *Server) GetAuditInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	info, err := s.audit.Info(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) ExportAudit(w http.ResponseWriter, r *http.Request, params gen.ExportAuditParams) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	f, msg := auditFilter(params.From, params.To, params.User, params.Cluster, params.Node, params.Job,
		(*string)(params.ActorType), params.Category, params.Q)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	rc := http.NewResponseController(w)
	// Een grote export duurt langer dan de schrijftimeout van de server; elke
	// portie verlengt hem.
	extend := func() { _ = rc.SetWriteDeadline(time.Now().Add(time.Minute)) }
	extend()
	name := "clusterforge-logboek-" + time.Now().Format("20060102-1504") + ".ndjson"
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.WriteHeader(http.StatusOK)
	count, err := s.audit.Export(r.Context(), f, w, func() error {
		extend()
		return rc.Flush()
	})
	if err != nil {
		// De status is al verstuurd; de export houdt halverwege op.
		s.log.WarnContext(r.Context(), "export van het logboek afgebroken", "regels", count, "err", err)
	}
	filters := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 && v[0] != "" {
			filters[k] = v[0]
		}
	}
	_ = s.ev.Write(context.WithoutCancel(r.Context()), nil, events.Event{
		Actor: events.User(p.User.ID), SubjectType: "audit", Action: "audit.exported",
		Payload: map[string]any{"format": "ndjson", "count": count, "filters": filters, "complete": err == nil},
	})
}

// auditFilter controleert de filters die lijst en export delen. Een niet-lege
// melding is een fout in de request.
func auditFilter(from, to *time.Time, user, cluster, node, job *uuid.UUID, actorType, category, q *string) (audit.Filter, string) {
	f := audit.Filter{From: from, To: to, User: user, Cluster: cluster, Node: node, Job: job}
	if actorType != nil {
		f.ActorType = store.ActorType(*actorType)
	}
	if category != nil && *category != "" {
		if !events.ValidCategory(*category) {
			return f, "onbekende soort " + *category
		}
		f.Category = events.Category(*category)
	}
	if q != nil {
		if utf8.RuneCountInString(*q) > 200 {
			return f, "zoektekst is te lang (hoogstens 200 tekens)"
		}
		f.Query = *q
	}
	if from != nil && to != nil && !from.Before(*to) {
		return f, "het begin van de periode moet voor het einde liggen"
	}
	return f, ""
}
