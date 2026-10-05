package events

import (
	"context"
	"encoding/hex"

	"github.com/google/uuid"
)

// Origin is de herkomst van een actie: vanaf welk IP en welke sessie, en
// binnen welke taak. De Writer zet hem onder payload.origin, zodat geen
// handler dat zelf hoeft te doen.
type Origin struct {
	IP string
	// Session is het begin van de hash van de sessie: genoeg om regels van
	// dezelfde sessie te herkennen, te weinig om er iets mee te doen.
	Session string
	JobID   *uuid.UUID
}

type originKey struct{}

// WithRequest legt het IP en de sessie van een request vast. sessionID is de
// hash van het sessietoken; nil als er (nog) geen sessie is.
func WithRequest(ctx context.Context, ip string, sessionID []byte) context.Context {
	o := OriginFrom(ctx)
	o.IP = ip
	o.Session = ""
	if len(sessionID) >= 4 {
		o.Session = hex.EncodeToString(sessionID[:4])
	}
	return context.WithValue(ctx, originKey{}, o)
}

// WithJob legt vast dat alles wat in ctx gebeurt bij een taak hoort.
func WithJob(ctx context.Context, id uuid.UUID) context.Context {
	o := OriginFrom(ctx)
	o.JobID = &id
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom geeft de herkomst uit ctx; leeg als er geen is.
func OriginFrom(ctx context.Context) Origin {
	o, _ := ctx.Value(originKey{}).(Origin)
	return o
}

func (o Origin) payload() map[string]any {
	out := map[string]any{}
	if o.IP != "" {
		out["ip"] = o.IP
	}
	if o.Session != "" {
		out["session"] = o.Session
	}
	if o.JobID != nil {
		out["job_id"] = o.JobID.String()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
