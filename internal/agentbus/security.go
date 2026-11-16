package agentbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Security stuurt een beveiligingsverzoek (fase 27) naar de agent van een node
// en wacht op het antwoord, tot ctx afloopt.
func (b *Bus) Security(ctx context.Context, nodeID uuid.UUID, req protocol.SecurityRequest) (protocol.SecurityResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.SecurityRequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.SecurityResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeSecurityRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.SecurityResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectSecurity), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.SecurityResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.SecurityResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.SecurityResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.SecurityResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeSecurityResult {
		return protocol.SecurityResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.SecurityResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.SecurityResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
