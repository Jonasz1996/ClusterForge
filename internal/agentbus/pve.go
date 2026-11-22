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

// PVE stuurt een leesverzoek (fase 17) naar de agent van een Proxmox-host
// en wacht op het antwoord, tot ctx afloopt.
func (b *Bus) PVE(ctx context.Context, nodeID uuid.UUID, req protocol.PVERequest) (protocol.PVEResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(protocol.PVERequestTimeout)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.PVEResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypePVERequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.PVEResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectPVE), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.PVEResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.PVEResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.PVEResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.PVEResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypePVEResult {
		return protocol.PVEResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.PVEResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.PVEResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
