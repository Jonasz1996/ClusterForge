package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// Tweestapsverificatie voor prodacties (testronde 2026-11-22, B1b): per
// installatie aan of uit. Aan (standaard voor een nieuwe installatie) kan
// op prod alleen een account met tweestapsverificatie iets doen; uit is
// het intikken van de clusternaam de bescherming die blijft.

// prod2FA leest de instelling; zonder rij of bij een fout: aan.
func (s *Server) prod2FA(ctx context.Context) (bool, *time.Time) {
	if s.pool == nil {
		return true, nil
	}
	var on bool
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT prod_require_2fa, updated_at FROM security_settings WHERE id`).Scan(&on, &at)
	if err != nil {
		return true, nil
	}
	return on, at
}

// needs2FA is true als p voor een actie op prod eerst tweestapsverificatie
// moet aanzetten.
func (s *Server) needs2FA(ctx context.Context, p auth.Principal) bool {
	if p.User.TotpEnabledAt != nil {
		return false
	}
	on, _ := s.prod2FA(ctx)
	return on
}

func (s *Server) GetProd2FA(w http.ResponseWriter, r *http.Request) {
	on, at := s.prod2FA(r.Context())
	writeJSON(w, http.StatusOK, prod2FAOut(on, at))
}

func prod2FAOut(on bool, at *time.Time) gen.Prod2FA {
	out := gen.Prod2FA{Required: on, UpdatedAt: nullable.NewNullNullable[time.Time]()}
	if at != nil {
		out.UpdatedAt = nullable.NewNullableWithValue(*at)
	}
	return out
}

func (s *Server) SetProd2FA(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.Prod2FAInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	cur, _ := s.prod2FA(ctx)
	if !in.Required && cur {
		// Een bescherming uitzetten vraagt wie je bent.
		if deref(in.Password) == "" && deref(in.TotpCode) == "" {
			writeError(w, http.StatusConflict, "needs_confirmation", "bevestig met je wachtwoord (of de code van je tweestapsverificatie)")
			return
		}
		if s.authError(w, r, s.auth.Confirm(ctx, p, deref(in.Password), deref(in.TotpCode), "prod_2fa_off")) {
			return
		}
	}
	var at time.Time
	err := s.pool.QueryRow(ctx, `INSERT INTO security_settings (id, prod_require_2fa, updated_at, updated_by) VALUES (true, $1, now(), $2)
		ON CONFLICT (id) DO UPDATE SET prod_require_2fa = $1, updated_at = now(), updated_by = $2
		RETURNING updated_at`, in.Required, p.User.ID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) || err != nil {
		s.internalError(w, r, err)
		return
	}
	if cur != in.Required {
		_ = s.ev.Write(ctx, nil, events.Event{
			Actor: events.User(p.User.ID), SubjectType: "settings", SubjectID: "prod_2fa",
			Action: "security.prod_2fa_changed", Payload: map[string]any{"required": in.Required},
		})
	}
	writeJSON(w, http.StatusOK, prod2FAOut(in.Required, &at))
}
