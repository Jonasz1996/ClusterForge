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

// Logs stuurt een logverzoek (fase 23) naar de agent van een node
// en wacht op het antwoord, tot ctx afloopt.
func (b *Bus) Logs(ctx context.Context, nodeID uuid.UUID, req protocol.LogsRequest) (protocol.LogsResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.LogsRequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.LogsResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeLogsRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.LogsResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectLogs), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.LogsResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.LogsResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.LogsResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.LogsResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeLogsResult {
		return protocol.LogsResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.LogsResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.LogsResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
