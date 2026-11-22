package agentbus

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kube stuurt een verzoek naar de kube-brug van de agent van een node en
// wacht op het antwoord, tot ctx afloopt. Een gecomprimeerd antwoord staat
// daarna uitgepakt in Body.
func (b *Bus) Kube(ctx context.Context, nodeID uuid.UUID, req protocol.KubeRequest) (protocol.KubeResponse, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.Deadline.IsZero() {
		d, ok := ctx.Deadline()
		if !ok {
			d = time.Now().Add(time.Minute)
		}
		req.Deadline = d
	}
	body, err := json.Marshal(req)
	if err != nil {
		return protocol.KubeResponse{}, err
	}
	data, err := json.Marshal(protocol.Envelope{
		V: protocol.Version, ID: req.ID, Type: protocol.TypeKubeRequest, TS: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return protocol.KubeResponse{}, err
	}
	msg, err := b.nc.RequestWithContext(ctx, protocol.Subject(nodeID.String(), protocol.SubjectKube), data)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return protocol.KubeResponse{}, ErrAgentOffline
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout):
		return protocol.KubeResponse{}, ErrNoAnswer
	case err != nil:
		return protocol.KubeResponse{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return protocol.KubeResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if env.Type != protocol.TypeKubeResult {
		return protocol.KubeResponse{}, fmt.Errorf("onverwacht antwoord van de agent: %s", env.Type)
	}
	var res protocol.KubeResponse
	if err := json.Unmarshal(env.Body, &res); err != nil {
		return protocol.KubeResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
	}
	if len(res.Gzip) > 0 {
		zr, err := gzip.NewReader(bytes.NewReader(res.Gzip))
		if err != nil {
			return protocol.KubeResponse{}, fmt.Errorf("ongeldig antwoord van de agent: %w", err)
		}
		out, err := io.ReadAll(io.LimitReader(zr, protocol.KubeMaxRead+1))
		if err != nil || len(out) > protocol.KubeMaxRead {
			return protocol.KubeResponse{}, errors.New("ongeldig of te groot antwoord van de agent")
		}
		res.Body, res.Gzip = out, nil
	}
	return res, nil
}
