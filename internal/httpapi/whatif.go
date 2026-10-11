package httpapi

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

func (s *Server) ListRiskComponents(w http.ResponseWriter, r *http.Request) {
	ww, err := s.risk.World(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": nonNil(ww.Components())})
}

func (s *Server) RiskWhatIf(w http.ResponseWriter, r *http.Request) {
	var req gen.RiskWhatIfInput
	if !decode(w, r, &req) {
		return
	}
	if len(req.Components) == 0 || len(req.Components) > 20 {
		writeError(w, http.StatusBadRequest, "bad_request", "kies 1 tot 20 onderdelen")
		return
	}
	ww, err := s.risk.World(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	comps, err := ww.Resolve(req.Components)
	var unknown risk.UnknownComponentError
	if errors.As(err, &unknown) {
		writeError(w, http.StatusBadRequest, "bad_request", unknown.Error())
		return
	}
	writeJSON(w, http.StatusOK, ww.Run(comps, false))
}

func (s *Server) ListRiskSPOFs(w http.ResponseWriter, r *http.Request) {
	ww, err := s.risk.World(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spofs": ww.SPOFs()})
}

// selfGuest is gen.SelfGuest.
type selfGuest struct {
	ConnectionID uuid.UUID `json:"connection_id"`
	Connection   string    `json:"connection"`
	VMID         int       `json:"vmid"`
	Name         string    `json:"name"`
	Host         string    `json:"host"`
	Type         string    `json:"type"`
}

// selfLocation is gen.SelfLocation.
type selfLocation struct {
	Source    string      `json:"source"`
	Guest     *selfGuest  `json:"guest"`
	VMID      *int        `json:"vmid"`
	MatchedOn string      `json:"matched_on"`
	Note      string      `json:"note"`
	Guests    []selfGuest `json:"guests"`
}

func (s *Server) GetSelfLocation(w http.ResponseWriter, r *http.Request) {
	m, err := s.topo.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := selfLocation{Source: "none", MatchedOn: m.Self.MatchedOn, Note: m.Self.Note, Guests: []selfGuest{}}
	guest := func(g topo.Guest) selfGuest {
		name := g.Name
		if g.Hostname != "" {
			name = g.Hostname
		}
		return selfGuest{ConnectionID: g.Conn, Connection: m.ConnName(g.Conn), VMID: g.VMID, Name: name, Host: g.Host, Type: g.Type}
	}
	if m.Self.Found() {
		out.Source = m.Self.Source
		vmid := m.Self.VMID
		out.VMID = &vmid
		if g, ok := m.Guest(m.Self.Conn, m.Self.VMID); ok {
			sg := guest(g)
			out.Guest = &sg
		} else {
			out.Note = "VM " + strconv.Itoa(vmid) + " staat niet meer in Proxmox. Kies de VM opnieuw of haal de keuze weg."
		}
	}
	for _, g := range m.Guests {
		if !g.Template && g.Pool != proxmox.SandboxPool {
			out.Guests = append(out.Guests, guest(g))
		}
	}
	slices.SortFunc(out.Guests, func(a, b selfGuest) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Connection), strings.ToLower(b.Connection)), cmp.Compare(a.VMID, b.VMID))
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) SetSelfLocation(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.SelfLocationInput
	if !decode(w, r, &req) {
		return
	}
	if req.Vmid < 100 || req.Vmid > 999999999 {
		writeError(w, http.StatusBadRequest, "bad_request", "een VMID ligt tussen 100 en 999999999")
		return
	}
	err := s.topo.SetSelf(r.Context(), events.User(p.User.ID), &p.User.ID, req.ConnectionId, req.Vmid)
	switch {
	case errors.Is(err, topo.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "die VM of container staat niet in Proxmox")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.risk.Kick()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteSelfLocation(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.topo.ClearSelf(r.Context(), events.User(p.User.ID)); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.risk.Kick()
	w.WriteHeader(http.StatusNoContent)
}
