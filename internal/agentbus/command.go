package agentbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
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
// antwoord, tot ctx afloopt. Een commando dat iets verandert, komt eerst in
// het logboek; lukt dat niet, dan gaat het niet weg. Elke poging geeft een
// regel, ook een herhaling met hetzelfde id.
func (b *Bus) Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error) {
	if changes(cmd.Action) {
		if err := b.logCommand(ctx, nodeID, cmd); err != nil {
			return protocol.Result{}, fmt.Errorf("commando niet gestuurd, want het logboek schrijven mislukte: %w", err)
		}
	}
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

// changes is true voor een commando dat iets op de node verandert. Lezen,
// zoals facts.collect, komt niet in het logboek; een nieuw leescommando
// hoort hier bij. Een onbekend commando telt als wijziging.
func changes(action string) bool {
	switch action {
	case protocol.CmdFactsCollect:
		return false
	}
	return true
}

// SetFingerprintKey zet de sleutel voor de vingerafdrukken van bestanden in
// het logboek; zonder sleutel staat er alleen het pad.
func (b *Bus) SetFingerprintKey(key []byte) { b.fpKey = key }

func (b *Bus) logCommand(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) error {
	ref, err := b.q.GetNodeRef(ctx, nodeID)
	if err != nil {
		return err
	}
	p := map[string]any{"hostname": ref.Hostname, "command": cmd.Action, "command_id": cmd.ID}
	if cmd.Reason != "" {
		p["reason"] = cmd.Reason
	}
	if cmd.DelaySeconds > 0 {
		p["delay_seconds"] = cmd.DelaySeconds
	}
	if len(cmd.Steps) > 0 {
		p["steps"] = SummarizeSteps(cmd.Steps, b.fpKey)
	}
	return b.ev.Write(ctx, nil, events.Event{
		Actor: events.System(), SubjectType: "node", SubjectID: nodeID.String(), ClusterID: ref.ClusterID,
		Action: "agent.command", Payload: p,
	})
}

// StepSummary is wat het logboek van een deploystap bewaart: soort, pad,
// unit, pakketnamen en creates. Nooit run, unless of de inhoud van een
// bestand, want die zijn met de geheimen van het cluster gerenderd; van een
// bestand staat er een vingerafdruk.
type StepSummary struct {
	Kind        string   `json:"kind"`
	Path        string   `json:"path,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Packages    []string `json:"packages,omitempty"`
	User        string   `json:"user,omitempty"`
	State       string   `json:"state,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Group       string   `json:"group,omitempty"`
	Creates     string   `json:"creates,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

// SummarizeSteps vat stappen samen voor het logboek. key is de afgeleide
// sleutel voor vingerafdrukken; nil laat ze weg.
func SummarizeSteps(steps []protocol.Step, key []byte) []StepSummary {
	out := make([]StepSummary, 0, len(steps))
	for _, st := range steps {
		s := StepSummary{Kind: st.Kind()}
		switch {
		case st.Package != nil:
			s.Packages, s.State = st.Package.Names, st.Package.State
		case st.File != nil:
			s.Path, s.Mode, s.Owner, s.Group = st.File.Path, st.File.Mode, st.File.Owner, st.File.Group
			if key != nil {
				s.Fingerprint = secrets.Fingerprint(key, []byte(st.File.Content))
			}
		case st.Service != nil:
			s.Unit, s.State, s.Enabled = st.Service.Name, st.Service.State, st.Service.Enabled
		case st.User != nil:
			s.User = st.User.Name
		case st.Directory != nil:
			s.Path, s.Mode, s.Owner, s.Group = st.Directory.Path, st.Directory.Mode, st.Directory.Owner, st.Directory.Group
		case st.Command != nil:
			s.Creates = st.Command.Creates
		}
		out = append(out, s)
	}
	return out
}
