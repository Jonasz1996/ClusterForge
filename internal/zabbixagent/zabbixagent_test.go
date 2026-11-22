package zabbixagent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func testConfig() Config {
	return Config{Hostname: "docker-trippel1", Server: "192.168.199.14", ServerActive: "192.168.199.14", Version: "8.0",
		HostMetadata: "Linux", PSKIdentity: Identity("docker-trippel1"), PSKHash: "abc"}
}

func TestScript(t *testing.T) {
	c := testConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	psk := strings.Repeat("ab", 32)
	s := Script(c, psk)
	for _, want := range []string{
		"Server=192.168.199.14\n", "Hostname=docker-trippel1\n", "TLSPSKIdentity=cf-docker-trippel1\n", "ZBX_VERSION=8.0\n",
		"\n" + psk + "\nCFPSK\n", "zabbix-release_latest_${ZBX_VERSION}+${ID}${VERSION_ID}_all.deb", "Plugins.Docker.Endpoint",
		"trap 'rm -f \"$0\"' EXIT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script mist %q", want)
		}
	}
	if strings.Count(s, psk) != 1 {
		t.Error("de sleutel hoort precies één keer in het script te staan (en nooit in een echo)")
	}
	if _, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "s.sh")
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
	// Een andere sleutel of Server geeft een andere hash.
	h := c.Hash()
	c2 := c
	c2.PSKHash = "def"
	c3 := c
	c3.Server = "192.168.199.11,192.168.199.12"
	if c2.Hash() == h || c3.Hash() == h {
		t.Error("hash verandert niet")
	}
}

func TestValidate(t *testing.T) {
	for _, mod := range []func(*Config){
		func(c *Config) { c.Server = "1.2.3.4\nUser=root" },
		func(c *Config) { c.ServerActive = "a b" },
		func(c *Config) { c.Version = "8" },
		func(c *Config) { c.HostMetadata = "Linux$(id)" },
		func(c *Config) { c.Hostname = "x\ny" },
	} {
		c := testConfig()
		mod(&c)
		if c.Validate() == nil {
			t.Errorf("geen fout voor %+v", c)
		}
	}
	if got := Identity("nc 1/a"); got != "cf-nc-1-a" {
		t.Errorf("Identity: %s", got)
	}
}

// fakeZabbix speelt de JSON-RPC-API van Zabbix na.
type fakeZabbix struct {
	mu      sync.Mutex
	hosts   map[string]string
	psk     map[string]string
	created []map[string]any
	calls   []string
}

func (z *fakeZabbix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	z.mu.Lock()
	defer z.mu.Unlock()
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	z.calls = append(z.calls, req.Method)
	auth := r.Header.Get("Authorization")
	if req.Method == "apiinfo.version" {
		if auth != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": -32602, "message": "Invalid params."}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "8.0.0"})
		return
	}
	if auth != "Bearer geheim-token" {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": -32602, "message": "Not authorized."}})
		return
	}
	var result any
	switch req.Method {
	case "host.get":
		out := []map[string]string{}
		if f, ok := req.Params["filter"].(map[string]any); ok {
			for _, n := range f["host"].([]any) {
				if id, ok := z.hosts[n.(string)]; ok {
					out = append(out, map[string]string{"hostid": id})
				}
			}
		} else {
			out = append(out, map[string]string{"hostid": "1"})
		}
		result = out
	case "host.update":
		z.psk[req.Params["hostid"].(string)] = req.Params["tls_psk_identity"].(string) + "=" + req.Params["tls_psk"].(string)
		result = map[string]any{"hostids": []string{req.Params["hostid"].(string)}}
	case "hostgroup.get":
		result = []map[string]string{}
	case "hostgroup.create":
		result = map[string]any{"groupids": []string{"50"}}
	case "template.get":
		result = []map[string]string{{"templateid": "10001", "host": "Linux by Zabbix agent", "name": "Linux by Zabbix agent"},
			{"templateid": "10002", "host": "Docker by Zabbix agent 2", "name": "Docker by Zabbix agent 2"}}
	case "proxy.get":
		result = []map[string]string{{"proxyid": "7"}}
	case "host.create":
		z.created = append(z.created, req.Params)
		id := "900"
		z.hosts[req.Params["host"].(string)] = id
		z.psk[id] = req.Params["tls_psk_identity"].(string) + "=" + req.Params["tls_psk"].(string)
		result = map[string]any{"hostids": []string{id}}
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": -32601, "message": "Method not found."}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": 1})
}

type fakeBus struct {
	mu      sync.Mutex
	scripts map[uuid.UUID]string
}

func (b *fakeBus) Run(_ context.Context, id uuid.UUID, req protocol.RunRequest) (protocol.RunResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req.Op == protocol.RunStart {
		b.scripts[id] = req.Spec.Script
	}
	now := time.Now()
	exit := 0
	return protocol.RunResponse{Status: &protocol.RunStatus{State: protocol.RunOK, StartedAt: now, EndedAt: &now, Exit: &exit}, Output: "klaar\n", Offset: 6}, nil
}

func seed(t *testing.T, pool *pgxpool.Pool, host, ip string, cluster *uuid.UUID, docker bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, cluster_id, primary_ip) VALUES ($1, $2, $3) RETURNING id`, host, cluster, ip).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(host), protocol.RunSince); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	f := protocol.Facts{Hostname: host}
	if docker {
		f.Docker = &protocol.Docker{}
	}
	b, _ := json.Marshal(f)
	if _, err := pool.Exec(ctx, `INSERT INTO node_facts (node_id, hash, facts, collected_at) VALUES ($1, 'x', $2, now())`, id, b); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServiceFlow(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	runner := jobs.NewRunner(pool, ev, log)
	runner.Poll = 50 * time.Millisecond
	bus := &fakeBus{scripts: map[uuid.UUID]string{}}
	zbx := &fakeZabbix{hosts: map[string]string{"docker-trippel1": "101"}, psk: map[string]string{}}
	srv := httptest.NewServer(zbx)
	defer srv.Close()
	s := NewService(pool, ev, runner, bus, log)
	s.Run.Poll = 10 * time.Millisecond
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.Box = box
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runner.Run(rctx)

	var prod uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (slug, name, type, environment, template_name) VALUES ('db', 'DB', 'generic', 'prod', 'mariadb-galera') RETURNING id`).Scan(&prod); err != nil {
		t.Fatal(err)
	}
	a := seed(t, pool, "docker-trippel1", "192.168.105.11", nil, true)
	b := seed(t, pool, "nieuw1", "192.168.105.12", nil, true)
	p := seed(t, pool, "db1", "192.168.10.191", &prod, false)

	// Zonder instellingen gebeurt er niets.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Node(ctx, a); n.Enabled {
		t.Fatal("aangezet zonder instellingen")
	}
	tok := "geheim-token"
	set, err := s.UpdateSettings(ctx, events.System(), nil, SettingsInput{Settings: Settings{
		AutoApply: true, Server: " 192.168.199.14 ", Version: "8.0", HostMetadata: "Linux", APIURL: srv.URL + "/zabbix",
		HostGroup: "ClusterForge", Templates: []string{"Linux by Zabbix agent"}, DockerTemplate: "Docker by Zabbix agent 2", Proxy: "zabbix-proxy-01",
	}, Token: &tok})
	if err != nil {
		t.Fatal(err)
	}
	if set.ServerActive != "192.168.199.14" || !set.TokenSet || set.APIURL != srv.URL+"/zabbix/api_jsonrpc.php" || !set.Ready() {
		t.Fatalf("instellingen: %+v", set)
	}
	s.API = func(_, t string) *Client { return &Client{URL: srv.URL, Token: t} }
	if v, err := s.Test(ctx); err != nil || v != "8.0.0" {
		t.Fatalf("test: %s %v", v, err)
	}

	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	wait := func(id uuid.UUID) Node {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			n, err := s.Node(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if n.AppliedAt != nil && !n.Pending && !n.ApplyRunning {
				return n
			}
			if n.ApplyError != "" {
				t.Fatalf("%s: %s", n.Hostname, n.ApplyError)
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s niet toegepast: %+v", n.Hostname, n)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	na, nb := wait(a), wait(b)
	if na.ZabbixHostID != "101" || nb.ZabbixHostID != "900" {
		t.Errorf("hostids %q %q", na.ZabbixHostID, nb.ZabbixHostID)
	}
	if np, _ := s.Node(ctx, p); np.Enabled {
		t.Error("prod kreeg de agent zonder Ook productie")
	}
	zbx.mu.Lock()
	pskA := zbx.psk["101"]
	pskB := zbx.psk["900"]
	created := zbx.created
	zbx.mu.Unlock()
	if !strings.HasPrefix(pskA, "cf-docker-trippel1=") || !strings.HasPrefix(pskB, "cf-nieuw1=") || pskA[len("cf-docker-trippel1="):] == pskB[len("cf-nieuw1="):] {
		t.Errorf("PSK's in Zabbix: %q %q", pskA, pskB)
	}
	if !strings.Contains(bus.scripts[a], "\n"+pskA[len("cf-docker-trippel1="):]+"\n") {
		t.Error("de sleutel op de node is niet die in Zabbix")
	}
	if len(created) != 1 || created[0]["proxyid"] != "7" || len(created[0]["templates"].([]any)) != 2 {
		t.Errorf("aangemaakt: %+v", created)
	}

	// Een nieuwe sleutel: de volgende ronde past opnieuw toe.
	if _, err := s.Rotate(ctx, events.System(), a); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Node(ctx, a); !n.Pending {
		t.Fatal("na een nieuwe sleutel niet nog toe te passen")
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		zbx.mu.Lock()
		cur := zbx.psk["101"]
		zbx.mu.Unlock()
		n, _ := s.Node(ctx, a)
		if cur != pskA && !n.Pending && !n.ApplyRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nieuwe sleutel niet in Zabbix")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Niet meer beheren: geen automatische toepassing meer.
	if err := s.Disable(ctx, events.System(), []uuid.UUID{b}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE zabbix_agent_settings SET host_metadata = 'Linux docker'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Node(ctx, b); n.ApplyRunning || !n.Pending {
		t.Errorf("niet beheerde node: %+v", n)
	}
}

// Het script leest /etc/os-release, dat zelf VERSION zet ("13 (trixie)"):
// de versie van Zabbix mag daar niet door overschreven worden.
func TestScriptKeepsZabbixVersion(t *testing.T) {
	if _, err := os.Stat("/etc/os-release"); err != nil {
		t.Skip("geen /etc/os-release")
	}
	s := Script(testConfig(), strings.Repeat("ab", 32))
	head := s[:strings.Index(s, "case \"$ID\" in")]
	head = strings.Replace(head, "trap 'rm -f \"$0\"' EXIT\n", "", 1)
	f := filepath.Join(t.TempDir(), "s.sh")
	if err := os.WriteFile(f, []byte(head+"echo \"$ZBX_VERSION|$ID|$VERSION_ID\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", f).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "8.0|") {
		t.Errorf("uitvoer %q", out)
	}
}
