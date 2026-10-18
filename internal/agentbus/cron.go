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

// Cron stuurt een cronverzoek naar de agent van een node en wacht op het
// antwoord, tot ctx afloopt.
func (b *Bus) Cron(ctx context.Context, nodeID uuid.UUID, req protocol.CronRequest) (protocol.CronResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.CronTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.CronResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeCronRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.CronResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectCron), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.CronResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.CronResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.CronResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.CronResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeCronResult {
		return protocol.CronResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.CronResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.CronResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
