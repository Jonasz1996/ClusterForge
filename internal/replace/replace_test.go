package replace

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func TestReplacements(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	s := NewService(pool, ev, log, jobs.NewRunner(pool, ev, log), nil)
	var cl, noVIP, oldNode, member uuid.UUID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ('rd', 'RustDesk', 'keepalived', 'lab') RETURNING id`).Scan(&cl))
	must(pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment) VALUES ('x', 'Zonder VIP', 'keepalived', 'lab') RETURNING id`).Scan(&noVIP))
	_, err := pool.Exec(ctx, `INSERT INTO vips (cluster_id, address) VALUES ($1, '192.168.10.50')`, cl)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO nodes (hostname, primary_ip) VALUES ('rustdesk-oud', '192.168.10.20') RETURNING id`).Scan(&oldNode))
	must(pool.QueryRow(ctx, `INSERT INTO nodes (hostname, primary_ip, cluster_id) VALUES ('rd1', '192.168.10.21', $1) RETURNING id`, cl).Scan(&member))

	for _, c := range []struct {
		in   Input
		want string
	}{
		{Input{ClusterID: noVIP, OldAddress: "192.168.10.20"}, "geen VIP"},
		{Input{ClusterID: cl}, "IP-adres"},
		{Input{ClusterID: cl, OldAddress: "192.168.10.50"}, "al het VIP"},
		{Input{ClusterID: cl, OldAddress: "192.168.10.21"}, "node van dit cluster"},
		{Input{ClusterID: cl, OldNodeID: &member}, "zelf een node"},
	} {
		var ve ValidationError
		if _, err := s.Record(ctx, events.System(), nil, c.in); !errors.As(err, &ve) || !strings.Contains(ve.Msg, c.want) {
			t.Errorf("%+v: %v, wil %q", c.in, err, c.want)
		}
	}
	r, err := s.Record(ctx, events.System(), nil, Input{ClusterID: cl, OldNodeID: &oldNode, KeepDays: 3})
	must(err)
	if r.OldAddress != "192.168.10.20" || r.VIP != "192.168.10.50" || r.OldHostname != "rustdesk-oud" || r.KeepDays != 3 || r.Due || r.State != StateActive {
		t.Fatalf("%+v", r)
	}
	// Nog eens: dezelfde open vervanging.
	again, err := s.Record(ctx, events.System(), nil, Input{ClusterID: cl, OldNodeID: &oldNode})
	must(err)
	if again.ID != r.ID {
		t.Fatalf("tweede record %s", again.ID)
	}
	if ns, _ := s.Notices(ctx); len(ns) != 0 {
		t.Fatalf("te vroeg: %+v", ns)
	}
	s.Now = func() time.Time { return time.Now().Add(4 * 24 * time.Hour) }
	ns, err := s.Notices(ctx)
	must(err)
	if len(ns) != 1 || !strings.Contains(ns[0].Detail, "rustdesk-oud") {
		t.Fatalf("melding: %+v", ns)
	}
	// Houden: geen melding meer.
	if _, err := s.SetKeep(ctx, events.System(), r.ID, 3, true); err != nil {
		t.Fatal(err)
	}
	if ns, _ := s.Notices(ctx); len(ns) != 0 {
		t.Fatalf("na houden: %+v", ns)
	}
	// Opruimen vraagt de naam, en een agent of een Proxmox-gast.
	if _, err := s.StartCleanup(ctx, events.System(), r.ID, "fout", ""); err == nil || !strings.Contains(err.Error(), "rustdesk-oud") {
		t.Fatalf("zonder naam: %v", err)
	}
	if _, err := s.StartCleanup(ctx, events.System(), r.ID, "rustdesk-oud", ""); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, oldNode)
	must(err)
	j, err := s.StartCleanup(ctx, events.System(), r.ID, "192.168.10.20", "")
	must(err)
	got, _ := s.Get(ctx, r.ID)
	if got.State != StateCleanup || got.CleanupJob == nil || *got.CleanupJob != j.ID {
		t.Fatalf("na starten: %+v", got)
	}
	if _, err := s.StartCleanup(ctx, events.System(), r.ID, "192.168.10.20", ""); err == nil {
		t.Fatal("twee keer opruimen")
	}
}

// TestTakeoverScript: kiest het ctl-script met een overname.
func TestTakeoverScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("geen sh")
	}
	dir := t.TempDir()
	script := strings.ReplaceAll(TakeoverScript("vaultx", "10.0.0.9"), "/usr/local/lib/clusterforge", dir)
	run := func() string {
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	if out := run(); !strings.Contains(out, "geen-overname") {
		t.Fatalf("leeg: %s", out)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("clusterforge-ctl.sh", "case $1 in\n  overnemen) echo FOUT ;;\nesac\n")
	write("keepalived-ctl.sh", "case $1 in\n  check) ;;\nesac\n")
	write("vaultx-ctl.sh", "case $1 in\n  overnemen) echo \"overnemen van $2\" ;;\nesac\n")
	if out := run(); !strings.Contains(out, "overnemen van 10.0.0.9") || strings.Contains(out, "FOUT") {
		t.Fatalf("vaultx: %s", out)
	}
}
