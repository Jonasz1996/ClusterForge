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

// Docker stuurt een leesverzoek (fase 28) naar de agent van een node met
// Docker en wacht op het antwoord, tot ctx afloopt.
func (b *Bus) Docker(ctx context.Context, nodeID uuid.UUID, req protocol.DockerRequest) (protocol.DockerResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.DockerRequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.DockerResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeDockerRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.DockerResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectDocker), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.DockerResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.DockerResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.DockerResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.DockerResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeDockerResult {
		return protocol.DockerResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.DockerResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.DockerResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
