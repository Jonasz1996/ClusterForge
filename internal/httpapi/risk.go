package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// adminTOTPText vervangt voor een viewer de bevinding over een beheerder
// zonder tweestapsverificatie: welke beheerder dat is, is een doelwit.
const adminTOTPText = "Een beheerder heeft geen tweestapsverificatie"

func redactFinding(f *risk.Finding) {
	if f.Check == "platform.admin_totp" {
		f.Text = adminTOTPText
		f.Subject.Name = ""
	}
}

func (s *Server) GetRisk(w http.ResponseWriter, r *http.Request) {
	ov, err := s.risk.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if p, _ := principalFrom(r.Context()); p.User.Role != store.UserRoleAdmin {
		if ov.Platform != nil {
			for i := range ov.Platform.Findings {
				redactFinding(&ov.Platform.Findings[i])
			}
		}
		for i := range ov.Findings {
			redactFinding(&ov.Findings[i].Finding)
		}
	}
	writeJSON(w, http.StatusOK, ov)
}

// clusterRisk is gen.ClusterRisk; computed_at is leeg zolang de score nog
// niet berekend is.
type clusterRisk struct {
	risk.Result
	ComputedAt *time.Time `json:"computed_at"`
}

func (s *Server) GetClusterRisk(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	if _, err := s.q.GetCluster(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	st, err := s.risk.Get(ctx, "cluster", id)
	switch {
	case errors.Is(err, risk.ErrNotComputed):
		writeJSON(w, http.StatusOK, clusterRisk{Result: risk.Result{Cards: []risk.Card{}}})
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, clusterRisk{Result: st.Result, ComputedAt: &st.ComputedAt})
	}
}

func (s *Server) GetRiskHistory(w http.ResponseWriter, r *http.Request, params gen.GetRiskHistoryParams) {
	days := 30
	if params.Days != nil {
		if *params.Days < 1 || *params.Days > 365 {
			writeError(w, http.StatusBadRequest, "bad_request", "days moet tussen 1 en 365 liggen")
			return
		}
		days = *params.Days
	}
	typ, id := "fleet", risk.FleetID
	if params.Cluster != nil {
		typ, id = "cluster", *params.Cluster
	}
	points, err := s.risk.History(r.Context(), typ, id, days)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

func (s *Server) AcceptRiskFinding(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.RiskAcceptanceInput
	if !decode(w, r, &req) {
		return
	}
	var expires *time.Time
	if t, err := req.ExpiresAt.Get(); err == nil {
		expires = &t
	}
	err := s.risk.Accept(r.Context(), events.User(p.User.ID), &p.User.ID, risk.AcceptInput{
		Check: req.Check, SubjectType: req.SubjectType, SubjectID: req.SubjectId, Reason: req.Reason, ExpiresAt: expires,
	})
	if s.riskError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteRiskAcceptance(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.riskError(w, r, s.risk.Unaccept(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) riskError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve risk.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "bad_request", ve.Msg)
	case errors.Is(err, risk.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "bevinding niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}
