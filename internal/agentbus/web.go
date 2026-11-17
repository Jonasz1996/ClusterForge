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

// Web stuurt een verzoek van de publicatie (fase 35) naar de agent van een
// node en wacht op het antwoord, tot ctx afloopt.
func (b *Bus) Web(ctx context.Context, nodeID uuid.UUID, req protocol.WebRequest) (protocol.WebResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.WebRequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.WebResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeWebRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.WebResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectWeb), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.WebResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.WebResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.WebResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.WebResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeWebResult {
		return protocol.WebResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.WebResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.WebResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
