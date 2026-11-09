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

// Run stuurt een runverzoek (fase 10) naar de agent van een node en wacht op het
// antwoord, tot ctx afloopt.
func (b *Bus) Run(ctx context.Context, nodeID uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.RunRequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.RunResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeRunRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.RunResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectRun), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.RunResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.RunResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.RunResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.RunResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeRunResult {
		return protocol.RunResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.RunResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.RunResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
