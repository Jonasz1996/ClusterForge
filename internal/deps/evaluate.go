package deps

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Evaluate rekent na een ronde van de evaluator de eigen status en de
// doorgegeven uitval van elke dienst uit en slaat ze op. Hij draait na de
// commit van de evaluator, in een eigen transactie met dezelfde advisory
// lock, zodat een fout hier de status van nodes en clusters en de
// VIP-eigenaar nooit tegenhoudt. Alleen als de status of de impact van een
// bevestigde dienst verandert, komt er één service.status_changed met de
// oorzaak en het pad. De eerste berekening van een dienst geeft geen event.
func (s *Service) Evaluate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", statusLock); err != nil {
			return err
		}
		q := store.New(tx)
		g, rows, err := s.load(ctx, q)
		if err != nil {
			return err
		}
		now := s.Now()
		for _, row := range rows {
			if err := s.saveStatus(ctx, q, g, row, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) saveStatus(ctx context.Context, q *store.Queries, g *Graph, row store.Service, now time.Time) error {
	own, e := g.Own(row.ID), g.Effect(row.ID)
	reason, path := g.ImpactText(row.ID)
	st := string(own.Status)
	next := store.SetServiceStatusParams{
		ID: row.ID, Status: &st, StatusReason: own.Reason, Impact: string(e.Impact), ImpactReason: reason,
	}
	if e.Impact != ImpactNone {
		cause, since := e.Cause, now
		if row.ImpactSince != nil {
			since = *row.ImpactSince
		}
		next.ImpactCauseID, next.ImpactSince = &cause, &since
	}
	if row.Status != nil && *row.Status == st && row.StatusReason == next.StatusReason && row.Impact == next.Impact &&
		row.ImpactReason == next.ImpactReason && sameID(row.ImpactCauseID, next.ImpactCauseID) {
		return nil
	}
	if err := q.SetServiceStatus(ctx, next); err != nil {
		return err
	}
	if row.Status == nil || row.State != StateConfirmed || (*row.Status == st && row.Impact == next.Impact) {
		return nil
	}
	sv, _ := g.Service(row.ID)
	scope := g.GroupOf(sv).Name
	if sv.External() {
		scope = "extern"
	}
	payload := map[string]any{
		"name": row.Name, "kind": row.Kind, "scope": scope,
		"from": *row.Status, "to": st, "reason": own.Reason,
		"impact_from": row.Impact, "impact": next.Impact, "impact_reason": reason, "path": path,
	}
	if e.Impact != ImpactNone {
		payload["cause"], payload["cause_id"] = g.labelFrom(row.ID, e.Cause), e.Cause.String()
	}
	return s.ev.Write(ctx, q, events.Event{
		Actor: events.System(), SubjectType: "service", SubjectID: row.ID.String(), ClusterID: row.ClusterID,
		Action: "service.status_changed", Payload: payload,
	})
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
