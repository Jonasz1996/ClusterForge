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

var (
	// ErrAgentOffline betekent dat er geen agent luistert voor deze node.
	ErrAgentOffline = errors.New("de agent is niet verbonden")
	// ErrNoAnswer betekent dat de agent niet op tijd antwoordde. Het commando
	// kan wel aangekomen zijn; stuur het opnieuw met hetzelfde id.
	ErrNoAnswer = errors.New("de agent antwoordde niet op tijd")
)

// Command stuurt een commando naar de agent van een node en wacht op het
// antwoord, tot ctx afloopt.
func (b *Bus) Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error) {
	body, err := json.Marshal(cmd)
	if err != nil {
		return protocol.Result{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: cmd.ID, Type: protocol.TypeCommand, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.Result{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectCommands), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.Result{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.Result{}, ErrNoAnswer
	case err != nil:
		return protocol.Result{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.Result{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeResult {
		return protocol.Result{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.Result
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.Result{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	return res, nil
}
