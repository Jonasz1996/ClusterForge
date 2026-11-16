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

// Stacks stuurt een stackverzoek (fase 21) naar de agent van een node
// en wacht op het antwoord, tot ctx afloopt.
func (b *Bus) Stacks(ctx context.Context, nodeID uuid.UUID, req protocol.StacksRequest) (protocol.StacksResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.StacksRequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.StacksResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeStacksRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.StacksResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectStacks), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.StacksResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.StacksResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.StacksResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.StacksResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeStacksResult {
		return protocol.StacksResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.StacksResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.StacksResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
