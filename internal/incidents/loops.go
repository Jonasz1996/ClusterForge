package incidents

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Testlijst 2 (23/11), punt 2.8: een unit die systemd meer dan LoopRestarts
// keer automatisch herstartte, is een incident met de echte oorzaak: de
// laatste fout uit het journaal van de node. Op 22/11 liepen zo units
// tienduizenden keren rond zonder dat iemand het zag.

// LoopRestarts is vanaf hoeveel herstarts een unit in een lus zit.
const LoopRestarts = 100

// loopWindow: zolang komt er een nieuwe herstart binnen, anders is de lus
// voorbij.
const loopWindow = 15 * time.Minute

// LoopSubject is het vaste kenmerk van een unit op een node.
func LoopSubject(nodeID uuid.UUID, unit string) uuid.UUID {
	return uuid.NewSHA1(nodeID, []byte("unit:"+unit))
}

func restartLoops(ctx context.Context, tx pgx.Tx, w *World, now time.Time) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (c.node_id, c.subject) c.node_id, n.hostname, n.cluster_id, coalesce(cl.environment::text, ''),
			c.subject, (c.detail->>'restarts')::int, c.ts,
			coalesce((SELECT f.summary FROM node_changes f WHERE f.node_id = c.node_id AND f.kind = 'service' AND f.subject = c.subject
				AND f.action = 'failed' AND f.ts > $1 ORDER BY f.ts DESC LIMIT 1), '')
		FROM node_changes c JOIN nodes n ON n.id = c.node_id LEFT JOIN clusters cl ON cl.id = n.cluster_id
		WHERE c.kind = 'service' AND c.action = 'auto_restarted' AND c.ts > $1
			AND c.detail ? 'restarts' AND (c.detail->>'restarts') ~ '^[0-9]+$' AND (c.detail->>'restarts')::int >= $2
		ORDER BY c.node_id, c.subject, c.ts DESC`, now.Add(-loopWindow), LoopRestarts)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID uuid.UUID
		var host, env, unit, failed string
		var clusterID *uuid.UUID
		var n int
		var at time.Time
		if err := rows.Scan(&nodeID, &host, &clusterID, &env, &unit, &n, &at, &failed); err != nil {
			return err
		}
		k := Key{SubjectUnit, LoopSubject(nodeID, unit)}
		reason := fmt.Sprintf("%s op %s is al %d keer automatisch herstart door systemd", unit, host, n)
		if failed != "" {
			reason += "; laatste fout: " + clip(failed, 200)
		}
		if strings.HasPrefix(unit, "cf-") {
			reason += ". Een cf-dienst die vóór de overname start of een oude installatie die zijn poort vasthoudt, is de gewone oorzaak: kijk in de logs van " + unit
		}
		p := Problem{Key: k, Name: unit + " op " + host, ClusterID: clusterID, Env: env, Status: "fail", Reason: reason, Since: at, Primary: true}
		w.Status[k] = "fail"
		w.Hosts[k] = []string{host}
		w.Problems = append(w.Problems, p)
	}
	return rows.Err()
}
