package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/publish"
	"github.com/Jonasz1996/clusterforge/internal/replace"
)

// Fase 48: een losse installatie vervangen door een cluster, de oude later
// opruimen, en de voorstellen naar het VIP (firewall en NPM). Buiten de
// OpenAPI, zoals /self; de webinterface typt ze in web/src/lib/replace.ts.

func (s *Server) replaceError(w http.ResponseWriter, r *http.Request, err error) {
	var ve replace.ValidationError
	var pe publish.ValidationError
	var busy jobs.BusyError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.As(err, &pe):
		writeError(w, http.StatusBadRequest, "validation", pe.Msg)
	case errors.As(err, &busy):
		writeError(w, http.StatusConflict, "busy", "er loopt al een taak: "+busy.Title)
	case errors.Is(err, replace.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) replaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if s.replace == nil {
		writeError(w, http.StatusNotFound, "not_found", "niet beschikbaar")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "ongeldig id")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) listReplacements(w http.ResponseWriter, r *http.Request) {
	if s.replace == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	var cluster *uuid.UUID
	if c := r.URL.Query().Get("cluster"); c != "" {
		id, err := uuid.Parse(c)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation", "ongeldig cluster")
			return
		}
		cluster = &id
	}
	list, err := s.replace.List(r.Context(), cluster)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "default_keep_days": replace.DefaultKeepDays})
}

func (s *Server) createReplacement(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.replace == nil {
		writeError(w, http.StatusNotFound, "not_found", "niet beschikbaar")
		return
	}
	var in replace.Input
	if !decode(w, r, &in) {
		return
	}
	uid := p.User.ID
	if in.Takeover {
		j, err := s.replace.StartTakeover(r.Context(), events.User(uid), &uid, in)
		if err != nil {
			s.replaceError(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job": toAPIJob(j, &p.User.Username)})
		return
	}
	rep, err := s.replace.Record(r.Context(), events.User(uid), &uid, in)
	if err != nil {
		s.replaceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replacement": rep})
}

func (s *Server) keepReplacement(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := s.replaceID(w, r)
	if !ok {
		return
	}
	var in struct {
		KeepDays int  `json:"keep_days"`
		Keep     bool `json:"keep"`
	}
	if !decode(w, r, &in) {
		return
	}
	rep, err := s.replace.SetKeep(r.Context(), events.User(p.User.ID), id, in.KeepDays, in.Keep)
	if err != nil {
		s.replaceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) moveReplacementHosts(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := s.replaceID(w, r)
	if !ok {
		return
	}
	j, err := s.replace.MoveHosts(r.Context(), events.User(p.User.ID), id)
	if err != nil {
		s.replaceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

func (s *Server) cleanupReplacement(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, ok := s.replaceID(w, r)
	if !ok {
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
		Storage string `json:"storage"`
	}
	if !decode(w, r, &in) {
		return
	}
	j, err := s.replace.StartCleanup(r.Context(), events.User(p.User.ID), id, in.Confirm, strings.TrimSpace(in.Storage))
	if err != nil {
		s.replaceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJob(j, &p.User.Username))
}

// vipAdvice geeft voor een cluster de voorstellen naar zijn VIP: regels en
// port forwards op de firewall, en proxy hosts die nog naar een oud adres
// of een node wijzen.
func (s *Server) vipAdvice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cid, err := uuid.Parse(r.URL.Query().Get("cluster"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "ongeldig cluster")
		return
	}
	var vips []string
	rows, err := s.pool.Query(ctx, `SELECT host(address) FROM vips WHERE cluster_id = $1 ORDER BY created_at`, cid)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			vips = append(vips, v)
		}
	}
	rows.Close()
	out := map[string]any{"vips": vips, "firewall": []any{}, "hosts": []any{}}
	if len(vips) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if s.opnsense != nil {
		if fw, err := s.opnsense.VIPAdviceFor(ctx, vips); err == nil {
			out["firewall"] = fw
		} else {
			out["firewall_error"] = err.Error()
		}
	}
	// Proxy hosts die naar een oud adres of een node van het cluster wijzen.
	type hostMove struct {
		From  string   `json:"from"`
		To    string   `json:"to"`
		Why   string   `json:"why"`
		Hosts []string `json:"hosts"`
	}
	moves := []hostMove{}
	if s.publish != nil {
		addrs := map[string]string{}
		rows, err := s.pool.Query(ctx, `SELECT host(old_address), 'oude installatie' FROM replacements WHERE cluster_id = $1 AND state IN ('actief', 'opruimen')
			UNION ALL SELECT host(primary_ip), 'node ' || hostname FROM nodes WHERE cluster_id = $1 AND primary_ip IS NOT NULL`, cid)
		if err == nil {
			for rows.Next() {
				var a, why string
				if rows.Scan(&a, &why) == nil {
					if _, ok := addrs[a]; !ok {
						addrs[a] = why
					}
				}
			}
			rows.Close()
		}
		for a, why := range addrs {
			if hs, err := s.publish.HostsTo(ctx, a); err == nil && len(hs) > 0 {
				moves = append(moves, hostMove{From: a, To: vips[0], Why: why, Hosts: hs})
			}
		}
		var count int
		if hs, err := s.publish.HostsTo(ctx, vips[0]); err == nil {
			count = len(hs)
		}
		out["hosts_to_vip"] = count
	}
	out["hosts"] = moves
	writeJSON(w, http.StatusOK, out)
}
