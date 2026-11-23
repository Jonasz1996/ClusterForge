package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Testlijst 3, N32: de ingebouwde outpost van Authentik laadt een nieuwe
// provider soms pas na een herstart van de server. Elke Authentik-node heeft
// zijn eigen Redis, en het bericht "config gewijzigd" komt dan alleen bij de
// outposts van de node waar de worker de taak deed. De controle na "achter
// Authentik" wacht daarom even, en herstart als het nodig is de servers van
// het Authentik-cluster één voor één (de VIP-node als laatste).

const akNotRedirect = "stuurt niet door naar Authentik"

// akReloadScript herstart de Authentik-server op een node en wacht tot hij
// weer gezond is.
const akReloadScript = `set -e
docker restart cf-authentik-server >/dev/null
for i in $(seq 1 90); do
  if curl -fsS -m 4 -o /dev/null http://127.0.0.1:9000/-/health/ready/ 2>/dev/null; then
    echo "Authentik-server weer gezond na $((i * 2)) s"
    exit 0
  fi
  sleep 2
done
echo "Authentik-server niet gezond na 3 minuten" >&2
exit 1
`

// check is verify, met wachten en outposts herladen voor een host achter
// Authentik.
func (s *Service) check(ctx context.Context, step *jobs.Step, st Settings, node nodeRow, hostID int, ph *PlannedHost, protected bool) error {
	err := s.verify(ctx, step, st, node, hostID, ph, protected)
	if !protected {
		return err
	}
	wait := 10 * time.Second
	if s.SettleTime != nil {
		wait = *s.SettleTime
	}
	for i := 0; i < 3 && err != nil && strings.Contains(err.Error(), akNotRedirect); i++ {
		step.Logf("· de outpost kent de nieuwe provider misschien nog niet; opnieuw over %s", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		err = s.verify(ctx, step, st, node, hostID, ph, protected)
	}
	if err == nil || !strings.Contains(err.Error(), akNotRedirect) || s.Run == nil {
		return err
	}
	if rerr := s.reloadOutposts(ctx, step, st); rerr != nil {
		step.Logf("✗ outposts herladen: %v", rerr)
		return err
	}
	return s.verify(ctx, step, st, node, hostID, ph, protected)
}

// memState bewaart de state van een run in het geheugen (meerdere runs in
// één stap).
type memState struct{ v []byte }

func (m *memState) State(v any) bool {
	if m.v == nil {
		return false
	}
	return json.Unmarshal(m.v, v) == nil
}

func (m *memState) SetState(_ context.Context, v any) error {
	b, err := json.Marshal(v)
	m.v = b
	return err
}

// reloadOutposts herstart de Authentik-server op elke node van het
// Authentik-cluster, de node met het VIP als laatste.
func (s *Service) reloadOutposts(ctx context.Context, step *jobs.Step, st Settings) error {
	nodes, err := s.nodes(ctx)
	if err != nil {
		return err
	}
	var todo, last []nodeRow
	for _, n := range nodes {
		in := (st.AuthentikNodeID != nil && n.ID == *st.AuthentikNodeID) ||
			(st.AuthentikNodeID == nil && st.AuthentikClusterID != nil && n.ClusterID != nil && *n.ClusterID == *st.AuthentikClusterID)
		if !in || n.Ready() != "" {
			continue
		}
		if len(n.VIPs) > 0 {
			last = append(last, n)
		} else {
			todo = append(todo, n)
		}
	}
	todo = append(todo, last...)
	if len(todo) == 0 {
		return fmt.Errorf("geen Authentik-node bereikbaar")
	}
	step.Logf("Outposts herladen: Authentik-server herstarten op %d node(s), één voor één, de VIP-node als laatste", len(todo))
	for _, n := range todo {
		step.Logf("· %s", n.Hostname)
		res, err := s.Run.DoWith(ctx, step, &memState{}, n.ID, protocol.RunSpec{ID: "cf-akreload-" + uuid.NewString(), Kind: protocol.RunScript, Script: akReloadScript, TimeoutSeconds: 240})
		if err != nil {
			return fmt.Errorf("%s: %w", n.Hostname, err)
		}
		if res.State != protocol.RunOK {
			return fmt.Errorf("%s: herstart niet gelukt (%s)", n.Hostname, res.State)
		}
	}
	return nil
}
