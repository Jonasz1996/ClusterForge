package components

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

// TestVerifyVIPTwoOwners: na een herstart van alle nodes kiest keepalived
// even wie het VIP houdt. Twee eigenaars die er één worden, is goed; twee
// eigenaars tot na VIPTimeout is split-brain.
func TestVerifyVIPTwoOwners(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	const vip = "10.0.20.185"
	spec := Spec{VIP: &VIP{Address: vip}}
	for i, name := range []string{"nc1", "nc2"} {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), $2)`,
			id, []string{"10.0.20.18" + string(rune('6'+i)), vip}); err != nil {
			t.Fatal(err)
		}
		spec.Nodes = append(spec.Nodes, Node{NodeID: id, Hostname: name, Role: RoleData, Index: i})
		spec.VIP.Nodes = append(spec.VIP.Nodes, id)
	}
	s := &Service{q: store.New(pool), Now: time.Now, Poll: 10 * time.Millisecond, VIPTimeout: 5 * time.Second, Fresh: time.Minute}

	// nc2 laat het VIP los terwijl verifyVIP kijkt.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = pool.Exec(ctx, `UPDATE node_status SET heartbeat_at = now(), addresses = '{10.0.20.187}' WHERE node_id = $1`, spec.Nodes[1].NodeID)
	}()
	if err := s.verifyVIP(ctx, &jobs.Step{}, spec); err != nil {
		t.Fatalf("twee eigenaars die er één worden: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE node_status SET heartbeat_at = now(), addresses = $2 WHERE node_id = $1`,
		spec.Nodes[1].NodeID, []string{"10.0.20.187", vip}); err != nil {
		t.Fatal(err)
	}
	s.VIPTimeout = 100 * time.Millisecond
	err := s.verifyVIP(ctx, &jobs.Step{}, spec)
	if err == nil || !strings.Contains(err.Error(), "staat op nc1 en nc2 tegelijk (split-brain)") {
		t.Fatalf("split-brain: %v", err)
	}
}
