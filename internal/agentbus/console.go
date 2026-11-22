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

// OpenConsole vraagt de agent een consolesessie te openen (fase 26 M7). De
// uitvoer komt op protocol.ConsoleOut binnen; zet die subscription vóór
// deze aanroep (SubscribeConsole), anders gaan de eerste bytes verloren.
func (b *Bus) OpenConsole(ctx context.Context, nodeID uuid.UUID, req protocol.ConsoleRequest) (protocol.ConsoleResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		req.Deadline = time.Now().Add(protocol.ConsoleRequestTimeout)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.ConsoleResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeConsoleRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.ConsoleResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectConsole), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.ConsoleResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.ConsoleResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.ConsoleResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil || env.Type != protocol.TypeConsoleResult {
		return protocol.ConsoleResponse{}, fmt.Errorf("ongeldig antwoord van de agent")
	}
	var res protocol.ConsoleResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.ConsoleResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}

// SubscribeConsole luistert naar de uitvoer van een sessie. fn krijgt de
// frames in volgorde, vanuit één goroutine.
func (b *Bus) SubscribeConsole(nodeID uuid.UUID, session string, fn func([]byte)) (func(), error) {
	sub, err := b.nc.Subscribe(protocol.ConsoleOut(nodeID.String(), session), func(m *nats.Msg) { fn(m.Data) })
	if err != nil {
		return nil, err
	}
	// Ruim: een scherm stuurt veel. Wat er toch niet in past, valt weg en
	// sluit de sessie aan de kant van de server (zie pvehost).
	_ = sub.SetPendingLimits(4096, 64<<20)
	if err := b.nc.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// SendConsole stuurt een frame naar de agent.
func (b *Bus) SendConsole(nodeID uuid.UUID, session string, frame []byte) error {
	return b.nc.Publish(protocol.ConsoleIn(nodeID.String(), session), frame)
}
