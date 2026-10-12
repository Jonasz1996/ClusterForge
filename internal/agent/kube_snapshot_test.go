package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// snapshotAgent is een agent op een k3s-server met de config die de
// template schrijft; init is de eerste server, met cluster-init.
func snapshotAgent(t *testing.T, init bool) (*Agent, *agenttest.Host, *[]string) {
	t.Helper()
	a, h := applyAgent(t)
	cfg := "token: \"geheim\"\nnode-ip: 10.0.40.11\n"
	if init {
		cfg += "cluster-init: true\n"
	} else {
		cfg += "server: https://10.0.40.10:6443\n"
	}
	for p, body := range map[string]string{k3sConfig: cfg, k3sServerUnit: "[Service]\n", protocol.KubeSnapshotDir + "/etcd-snapshot-leren-server-01-1760000000": "snapshot"} {
		if err := os.MkdirAll(filepath.Join(a.Root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.Root, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a.Root, protocol.KubeDBDir, "etcd-member"), []byte("lid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.Root, protocol.KubeDBDir, "etcd"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.SetUnit("k3s", agenttest.Unit{Enabled: true, Active: true})
	var k3s []string
	h.K3s = func(args []string) ([]byte, error) {
		k3s = append(k3s, strings.Join(args, " "))
		if len(args) > 1 && args[0] == "etcd-snapshot" {
			return []byte("INFO[0000] Snapshot " + args[3] + "-leren-server-01-1760001234 saved.\n"), nil
		}
		return []byte("INFO[0001] Managed etcd cluster membership has been reset, restart without --cluster-reset flag now.\n"), nil
	}
	return a, h, &k3s
}

func snapshotSend(t *testing.T, a *Agent, s protocol.KubeSnapshotRequest) (protocol.KubeSnapshotResult, string) {
	t.Helper()
	res := kubeSend(t, a, protocol.KubeRequest{ID: "s", Op: protocol.KubeSnapshot, Snapshot: &s})
	if res.Error != "" {
		return protocol.KubeSnapshotResult{}, res.Error
	}
	var out protocol.KubeSnapshotResult
	if res.Status != http.StatusOK || json.Unmarshal(res.Body, &out) != nil {
		t.Fatalf("%s: %+v", s.Action, res)
	}
	return out, ""
}

// Een snapshot maken is k3s etcd-snapshot save met een naam die de agent
// controleert; een ongeldige naam komt nooit bij k3s.
func TestKubeSnapshotSave(t *testing.T) {
	a, _, k3s := snapshotAgent(t, true)
	for _, name := range []string{"", "Clusterforge", "../x", "a b", "-x", strings.Repeat("a", 40)} {
		if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotSave, Name: name}); !strings.Contains(msg, "ongeldige naam") {
			t.Errorf("naam %q: %q", name, msg)
		}
	}
	if len(*k3s) != 0 {
		t.Fatalf("k3s: %v", *k3s)
	}
	out, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotSave, Name: "clusterforge"})
	if msg != "" || len(out.Output) != 1 || !strings.Contains(out.Output[0], "clusterforge-leren-server-01-1760001234 saved") {
		t.Fatalf("save: %+v %q", out, msg)
	}
	if !slices.Equal(*k3s, []string{"etcd-snapshot save --name clusterforge"}) {
		t.Fatalf("k3s: %v", *k3s)
	}
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: "delete"}); !strings.Contains(msg, "onbekende snapshotstap") {
		t.Fatalf("onbekend: %q", msg)
	}
	if f := a.kubeFacts(context.Background(), "leren-server-01"); f == nil || !f.Init {
		t.Fatalf("facts: %+v", f)
	}
	if out, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotCheck}); msg != "" || !out.Init || out.Join || out.Active != "active" {
		t.Fatalf("check: %+v %q", out, msg)
	}

	// Een worker is geen server.
	w, _ := applyAgent(t)
	if _, msg := snapshotSend(t, w, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotSave, Name: "clusterforge"}); !strings.Contains(msg, "geen k3s-server") {
		t.Fatalf("worker: %q", msg)
	}
}

// Terugzetten op de eerste server: alleen als k3s stilstaat, alleen een
// bestand in de map met snapshots, en alleen op de server met
// cluster-init.
func TestKubeSnapshotReset(t *testing.T) {
	a, h, k3s := snapshotAgent(t, true)
	file := "etcd-snapshot-leren-server-01-1760000000"
	reset := func(file string) string {
		t.Helper()
		_, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotReset, File: file})
		return msg
	}
	if msg := reset(file); !strings.Contains(msg, "k3s staat niet stil") {
		t.Fatalf("draait: %q", msg)
	}
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStop}); msg != "" {
		t.Fatal(msg)
	}
	if u, _ := h.Unit("k3s"); u.Active {
		t.Fatal("k3s draait nog")
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(a.Root, protocol.KubeSnapshotDir, "link")); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{
		"../../../../etc/shadow": "ongeldige naam", "": "ongeldige naam", ".hidden": "ongeldige naam",
		"bestaat-niet": "bestaat niet", "link": "geen gewoon bestand",
	} {
		if msg := reset(f); !strings.Contains(msg, want) {
			t.Errorf("bestand %q: %q", f, msg)
		}
	}
	if len(*k3s) != 0 {
		t.Fatalf("k3s: %v", *k3s)
	}
	if msg := reset(file); msg != "" {
		t.Fatal(msg)
	}
	if want := "server --cluster-reset --cluster-reset-restore-path=" + protocol.KubeSnapshotDir + "/" + file; !slices.Equal(*k3s, []string{want}) {
		t.Fatalf("k3s: %v", *k3s)
	}
	// Op de eerste server blijft de database staan.
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotRejoin}); !strings.Contains(msg, "begon het cluster") {
		t.Fatalf("rejoin op de eerste: %q", msg)
	}
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStart}); msg != "" {
		t.Fatal(msg)
	}
	if u, _ := h.Unit("k3s"); !u.Active {
		t.Fatal("k3s draait niet")
	}

	// Mislukt k3s, dan staat de laatste regel in de fout.
	h.Fail("k3s server", "FATA[0002] etcd: snapshot file is corrupt")
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStop}); msg != "" {
		t.Fatal(msg)
	}
	if msg := reset(file); !strings.Contains(msg, "k3s server mislukte") || !strings.Contains(msg, "snapshot file is corrupt") {
		t.Fatalf("fout: %q", msg)
	}
}

// Op een andere server gaat de database van etcd opzij, zodat hij zich
// opnieuw aanmeldt; de snapshots blijven. Terugzetten kan daar niet.
func TestKubeSnapshotRejoin(t *testing.T) {
	a, _, k3s := snapshotAgent(t, false)
	rejoin := func() (protocol.KubeSnapshotResult, string) {
		t.Helper()
		return snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotRejoin})
	}
	if _, msg := rejoin(); !strings.Contains(msg, "k3s staat niet stil") {
		t.Fatalf("draait: %q", msg)
	}
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStop}); msg != "" {
		t.Fatal(msg)
	}
	if _, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotReset, File: "etcd-snapshot-leren-server-01-1760000000"}); !strings.Contains(msg, "alleen op de server die het cluster begon") {
		t.Fatalf("reset op een andere: %q", msg)
	}
	out, msg := rejoin()
	if msg != "" || !strings.HasPrefix(out.Moved, protocol.KubeDBDir+"-voor-terugzetten-") {
		t.Fatalf("rejoin: %+v %q", out, msg)
	}
	if b, err := os.ReadFile(filepath.Join(a.Root, out.Moved, "etcd-member")); err != nil || string(b) != "lid" {
		t.Fatalf("oude database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, protocol.KubeDBDir, "etcd")); !os.IsNotExist(err) {
		t.Fatalf("etcd staat er nog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, protocol.KubeSnapshotDir, "etcd-snapshot-leren-server-01-1760000000")); err != nil {
		t.Fatalf("snapshot weg: %v", err)
	}
	// Nog eens doet niets: er is geen database meer.
	if out, msg := rejoin(); msg != "" || out.Moved != "" || len(out.Output) != 1 {
		t.Fatalf("tweede keer: %+v %q", out, msg)
	}
	if len(*k3s) != 0 {
		t.Fatalf("k3s: %v", *k3s)
	}
	if f := a.kubeFacts(context.Background(), "leren-server-02"); f == nil || f.Init {
		t.Fatalf("facts: %+v", f)
	}
	if out, msg := snapshotSend(t, a, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotCheck}); msg != "" || out.Init || !out.Join || out.Active != "inactive" {
		t.Fatalf("check: %+v %q", out, msg)
	}
}
