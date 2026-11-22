// Package events schrijft regels naar de append-only eventtabel. Elke
// wijziging in ClusterForge hoort hier een event achter te laten.
package events

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type Actor struct {
	Type store.ActorType
	ID   string
}

func System() Actor { return Actor{Type: store.ActorTypeSystem} }

func User(id uuid.UUID) Actor { return Actor{Type: store.ActorTypeUser, ID: id.String()} }

type Event struct {
	Actor       Actor
	SubjectType string
	SubjectID   string
	ClusterID   *uuid.UUID
	Action      string
	Payload     map[string]any
}

type Writer struct {
	q   *store.Queries
	log *slog.Logger
}

func NewWriter(q *store.Queries, log *slog.Logger) *Writer {
	return &Writer{q: q, log: log}
}

// Write slaat een event op. q kan een transactie zijn, zodat het event samen
// met de wijziging zelf wordt vastgelegd; nil gebruikt de standaardverbinding.
// De herkomst uit ctx (IP, sessie, taak) komt onder payload.origin, en de
// waarde van een veld als password of *_secret wordt "[verborgen]".
func (w *Writer) Write(ctx context.Context, q *store.Queries, e Event) error {
	if q == nil {
		q = w.q
	}
	payload, err := encodePayload(e.Payload, OriginFrom(ctx))
	if err != nil {
		w.log.ErrorContext(ctx, "event schrijven mislukt", "action", e.Action, "err", err)
		return err
	}
	err = q.InsertEvent(ctx, store.InsertEventParams{
		ActorType:   e.Actor.Type,
		ActorID:     e.Actor.ID,
		SubjectType: e.SubjectType,
		SubjectID:   e.SubjectID,
		ClusterID:   e.ClusterID,
		Action:      e.Action,
		Payload:     payload,
	})
	if err != nil {
		w.log.ErrorContext(ctx, "event schrijven mislukt", "action", e.Action, "err", err)
	}
	return err
}
