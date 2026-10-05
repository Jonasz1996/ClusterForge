package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type newToken struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type nodeWithAgent struct {
	node
	Agent *struct {
		ID         string `json:"id"`
		Connection string `json:"connection"`
		Version    string `json:"version"`
	} `json:"agent"`
}

// fakeRoot maakt een minimale bestandssysteemwortel met een machine-id.
func fakeRoot(t *testing.T, machineID string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/machine-id"), []byte(machineID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func fakeCollector(root string, addrs ...string) *agent.Collector {
	return &agent.Collector{
		Root: root,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("geen commando's in tests")
		},
		Addresses:      func() []string { return addrs },
		PrimaryAddress: func() string { return "192.0.2.50" },
	}
}

// eventually herhaalt check tot die true geeft of de tijd om is.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestAgentEnrollHeartbeatFactsRevoke(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	hostname, _ := os.Hostname()

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "LB", "type": "keepalived", "environment": "lab"}, &cl)
	var v vip
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.99.0.10"}, &v)

	var tok newToken
	if status := c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"cluster_id": cl.ID, "max_uses": 2}, &tok); status != http.StatusCreated {
		t.Fatalf("token: %d", status)
	}
	if !strings.HasPrefix(tok.Token, "cfe_") {
		t.Fatalf("token: %+v", tok)
	}

	// Onbekend token.
	_, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: "cfe_onzin", Root: fakeRoot(t, strings.Repeat("a", 32))})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("onbekend token: %v", err)
	}

	root := fakeRoot(t, strings.Repeat("1", 32))
	cfg, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok.Token, Version: "test", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NatsCertSHA256 != e.bus.Fingerprint() || !strings.HasPrefix(cfg.NatsURL, "tls://127.0.0.1:") {
		t.Fatalf("config: %+v", cfg)
	}

	// Een andere machine met dezelfde hostname mag de node niet overnemen.
	_, err = agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok.Token, Root: fakeRoot(t, strings.Repeat("2", 32))})
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("andere machine: %v", err)
	}

	a := &agent.Agent{
		Config: cfg, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Collector: fakeCollector(root, "10.99.0.10", "192.0.2.50"), HeartbeatInterval: 100 * time.Millisecond,
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = a.Run(runCtx) }()

	var n nodeWithAgent
	eventually(t, "agent online", func() bool {
		c.do("GET", "/api/v1/nodes/"+cfg.NodeID, nil, &n)
		return n.Agent != nil && n.Agent.Connection == "online"
	})
	if n.Hostname != hostname || n.ClusterID == nil || *n.ClusterID != cl.ID {
		t.Fatalf("node: %+v", n)
	}

	eventually(t, "VIP-eigenaar", func() bool {
		var d struct {
			Vips []struct {
				OwnerHostname *string `json:"owner_hostname"`
			} `json:"vips"`
		}
		c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &d)
		return len(d.Vips) == 1 && d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == hostname
	})

	var rt struct {
		Facts *struct {
			MachineID      string `json:"machine_id"`
			PrimaryAddress string `json:"primary_address"`
		} `json:"facts"`
		Heartbeat *struct {
			Addresses []string `json:"addresses"`
		} `json:"heartbeat"`
	}
	eventually(t, "facts", func() bool {
		c.do("GET", "/api/v1/nodes/"+cfg.NodeID+"/facts", nil, &rt)
		return rt.Facts != nil && rt.Heartbeat != nil
	})
	if rt.Facts.MachineID != strings.Repeat("1", 32) || len(rt.Heartbeat.Addresses) != 2 {
		t.Fatalf("runtime: %+v %+v", rt.Facts, rt.Heartbeat)
	}
	// Het primaire IP-adres komt uit de facts, omdat de node er nog geen had.
	c.do("GET", "/api/v1/nodes/"+cfg.NodeID, nil, &n)
	if n.PrimaryIP == nil || *n.PrimaryIP != "192.0.2.50" {
		t.Fatalf("primair IP: %v", n.PrimaryIP)
	}

	// De agent mag niet luisteren op de commando's van een andere node.
	kp, _ := nkeys.FromSeed([]byte(cfg.NkeySeed))
	pub, _ := kp.PublicKey()
	errc := make(chan error, 1)
	nc, err := nats.Connect(cfg.NatsURL, nats.Nkey(pub, kp.Sign), nats.Secure(protocol.PinnedTLSConfig(cfg.NatsCertSHA256)),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errc <- err }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nc.SubscribeSync("cf.node.00000000-0000-0000-0000-000000000000.cmd"); err != nil {
		t.Fatal(err)
	}
	_ = nc.Flush()
	select {
	case err := <-errc:
		if !errors.Is(err, nats.ErrPermissionViolation) && !strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
			t.Fatalf("verwacht permissions violation, kreeg %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("geen permissions violation")
	}
	nc.Close()

	// Een vreemde sleutel komt er niet in.
	other, _ := nkeys.CreateUser()
	otherPub, _ := other.PublicKey()
	if _, err := nats.Connect(cfg.NatsURL, nats.Nkey(otherPub, other.Sign), nats.Secure(protocol.PinnedTLSConfig(cfg.NatsCertSHA256))); err == nil {
		t.Fatal("onbekende sleutel toegelaten")
	}

	// Intrekken: de node verliest zijn agent en de sleutel werkt niet meer.
	if status := c.do("POST", "/api/v1/agents/"+n.Agent.ID+"/revoke", nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke: %d", status)
	}
	c.do("GET", "/api/v1/nodes/"+cfg.NodeID, nil, &n)
	if n.Agent != nil {
		t.Fatalf("agent na intrekken: %+v", n.Agent)
	}
	if _, err := nats.Connect(cfg.NatsURL, nats.Nkey(pub, kp.Sign), nats.Secure(protocol.PinnedTLSConfig(cfg.NatsCertSHA256))); err == nil {
		t.Fatal("ingetrokken sleutel toegelaten")
	}

	var evs struct {
		Items []struct{ Action string } `json:"items"`
	}
	c.do("GET", "/api/v1/events?limit=100", nil, &evs)
	seen := map[string]bool{}
	for _, ev := range evs.Items {
		seen[ev.Action] = true
	}
	for _, want := range []string{"enrollment_token.created", "node.created", "agent.enrolled", "vip.owner_changed", "node.facts_changed", "agent.revoked"} {
		if !seen[want] {
			t.Errorf("event %s ontbreekt", want)
		}
	}
}

func TestEnrollmentTokens(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()

	var n node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db1.lab"}, &n)
	if status := c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"node_id": n.ID, "max_uses": 3}, nil); status != http.StatusBadRequest {
		t.Fatalf("node-token met meerdere keren: %d", status)
	}
	var tok newToken
	c.do("POST", "/api/v1/enrollment-tokens", map[string]any{"node_id": n.ID, "description": "db1"}, &tok)

	var list struct {
		Items []struct {
			ID           string  `json:"id"`
			NodeHostname *string `json:"node_hostname"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/enrollment-tokens", nil, &list)
	if len(list.Items) != 1 || list.Items[0].NodeHostname == nil || *list.Items[0].NodeHostname != "db1.lab" {
		t.Fatalf("lijst: %+v", list)
	}

	// Een token voor een vaste node koppelt aan die node, ook bij een andere hostname.
	cfg, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok.Token, Root: fakeRoot(t, strings.Repeat("3", 32))})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != n.ID {
		t.Fatalf("node: %s, want %s", cfg.NodeID, n.ID)
	}
	// Opgebruikt.
	if _, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok.Token, Root: fakeRoot(t, strings.Repeat("3", 32))}); err == nil {
		t.Fatal("opgebruikt token geaccepteerd")
	}
	c.do("GET", "/api/v1/enrollment-tokens", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("opgebruikt token nog in de lijst: %+v", list)
	}

	// Verlopen token.
	var tok2 newToken
	c.do("POST", "/api/v1/enrollment-tokens", map[string]any{}, &tok2)
	if _, err := e.pool.Exec(ctx, "UPDATE enrollment_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1", tok2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok2.Token, Root: fakeRoot(t, strings.Repeat("4", 32))}); err == nil {
		t.Fatal("verlopen token geaccepteerd")
	}

	var tok3 newToken
	c.do("POST", "/api/v1/enrollment-tokens", map[string]any{}, &tok3)
	if status := c.do("DELETE", "/api/v1/enrollment-tokens/"+tok3.ID, nil, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if _, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: e.srv.URL, Token: tok3.Token, Root: fakeRoot(t, strings.Repeat("5", 32))}); err == nil {
		t.Fatal("verwijderd token geaccepteerd")
	}

	// Viewers mogen geen tokens zien of maken.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if status := v.do("GET", "/api/v1/enrollment-tokens", nil, nil); status != http.StatusForbidden {
		t.Fatalf("viewer lijst: %d", status)
	}
	if status := v.do("POST", "/api/v1/enrollment-tokens", map[string]any{}, nil); status != http.StatusForbidden {
		t.Fatalf("viewer maken: %d", status)
	}
}

func TestAgentDownloads(t *testing.T) {
	e := newTestEnv(t)
	resp, err := http.Get(e.srv.URL + "/install/agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "#!/bin/sh") {
		t.Fatalf("installatiescript: %d", resp.StatusCode)
	}
	if err := os.WriteFile(filepath.Join(e.api.cfg.AgentDir, "cf-agent-linux-amd64"), []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/downloads/cf-agent-linux-amd64": 200,
		"/downloads/cf-agent-linux-arm64": 404,
		"/downloads/../../etc/passwd":     404,
		"/downloads/agent.json":           404,
	} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}
