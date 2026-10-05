package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) GetHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	h := gen.Health{Status: gen.HealthStatusOk, Version: s.version, Database: gen.HealthDatabaseOk}
	status := http.StatusOK
	if err := s.pool.Ping(ctx); err != nil {
		h.Status, h.Database, status = gen.HealthStatusDegraded, gen.HealthDatabaseUnreachable, http.StatusServiceUnavailable
	}
	writeJSON(w, status, h)
}

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.loginLimiter.allow(ip) {
		w.Header().Set("Retry-After", "6")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "te veel inlogpogingen, probeer het straks opnieuw")
		return
	}
	var req gen.LoginRequest
	if !decode(w, r, &req) {
		return
	}
	in := auth.LoginInput{Username: req.Username, Password: req.Password, IP: ip, UserAgent: r.UserAgent()}
	if req.TotpCode != nil {
		in.TOTPCode = *req.TotpCode
	}
	sess, err := s.auth.Login(r.Context(), in)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
		return
	case errors.Is(err, auth.ErrTOTPRequired):
		writeError(w, http.StatusUnauthorized, "totp_required", err.Error())
		return
	case errors.Is(err, auth.ErrInvalidTOTP):
		writeError(w, http.StatusUnauthorized, "invalid_totp", err.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, gen.Me{
		User:             toAPIUser(sess.User),
		CsrfToken:        sess.CSRFToken,
		SessionExpiresAt: sess.ExpiresAt,
	})
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	if err := s.auth.Logout(r.Context(), p); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, gen.Me{
		User:             toAPIUser(p.User),
		CsrfToken:        p.CSRFToken,
		SessionExpiresAt: p.ExpiresAt,
	})
}

func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req gen.ChangePasswordRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.auth.ChangePassword(r.Context(), p, req.CurrentPassword, req.NewPassword)
	if s.authError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) BeginTotpSetup(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	secret, url, err := s.auth.BeginTOTPSetup(r.Context(), p)
	if s.authError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, gen.TotpSetup{Secret: secret, OtpauthUrl: url})
}

func (s *Server) EnableTotp(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req gen.TotpCodeRequest
	if !decode(w, r, &req) {
		return
	}
	if s.authError(w, r, s.auth.EnableTOTP(r.Context(), p, req.Code)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DisableTotp(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req gen.TotpDisableRequest
	if !decode(w, r, &req) {
		return
	}
	if s.authError(w, r, s.auth.DisableTOTP(r.Context(), p, req.Password, req.Code)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListEvents(w http.ResponseWriter, r *http.Request, params gen.ListEventsParams) {
	p, _ := principalFrom(r.Context())
	if p.User.Role != store.UserRoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "alleen voor beheerders")
		return
	}
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 200 {
		writeError(w, http.StatusBadRequest, "bad_request", "limit moet tussen 1 en 200 liggen")
		return
	}
	rows, err := s.q.ListRecentEvents(r.Context(), int32(limit))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.Event, 0, len(rows))
	for _, e := range rows {
		payload := map[string]any{}
		_ = json.Unmarshal(e.Payload, &payload)
		items = append(items, gen.Event{
			Id:          e.ID,
			Ts:          e.Ts,
			ActorType:   gen.EventActorType(e.ActorType),
			ActorId:     e.ActorID,
			SubjectType: e.SubjectType,
			SubjectId:   e.SubjectID,
			ClusterId:   nullableOf(e.ClusterID),
			Action:      e.Action,
			Payload:     payload,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Items []gen.Event `json:"items"`
	}{items})
}

// authError schrijft een passende foutrespons en geeft true terug als err
// niet nil was.
func (s *Server) authError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ve auth.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "validation", ve.Msg)
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusBadRequest, "invalid_credentials", "huidig wachtwoord klopt niet")
	case errors.Is(err, auth.ErrInvalidTOTP):
		writeError(w, http.StatusBadRequest, "invalid_totp", err.Error())
	case errors.Is(err, auth.ErrTOTPAlreadyEnabled):
		writeError(w, http.StatusConflict, "totp_already_enabled", err.Error())
	case errors.Is(err, auth.ErrTOTPNotPending):
		writeError(w, http.StatusConflict, "totp_not_pending", err.Error())
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "interne fout", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "interne fout")
}

func toAPIUser(u store.User) gen.User {
	return gen.User{
		Id:          u.ID,
		Username:    u.Username,
		Role:        gen.Role(u.Role),
		TotpEnabled: u.TotpEnabledAt != nil,
		CreatedAt:   u.CreatedAt,
	}
}
