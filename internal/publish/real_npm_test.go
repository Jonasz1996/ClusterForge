package publish

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// agentBus stuurt elk webverzoek naar een echte agent, in dit proces.
type agentBus struct{ a *agent.Agent }

func (b agentBus) Web(ctx context.Context, _ uuid.UUID, req protocol.WebRequest) (protocol.WebResponse, error) {
	return b.a.WebRequest(ctx, req), nil
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func hostPort(t *testing.T, container, port string) string {
	t.Helper()
	out := docker(t, "port", container, port)
	line, _, _ := strings.Cut(out, "\n")
	_, p, err := net.SplitHostPort(line)
	if err != nil {
		t.Fatalf("docker port %s: %q", port, out)
	}
	return p
}

func listenAll(t *testing.T, h http.Handler) int {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

func npmAPI(t *testing.T, port, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://127.0.0.1:"+port+"/api"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func seedNode(t *testing.T, pool *pgxpool.Pool, name, ip string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO nodes (hostname, primary_ip) VALUES ($1, $2) RETURNING id`, name, ip).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agents (id, node_id, nkey_public, protocol_version) VALUES (gen_random_uuid(), $1, $2, $3)`,
		id, "U"+strings.ToUpper(name), protocol.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_status (node_id, heartbeat_at, addresses) VALUES ($1, now(), '{}')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestRealNPM draait de publicatie tegen een echte Nginx Proxy Manager in
// Docker, met een echte agent (snapshot, controles en terugzetten op de
// SQLite-database en de nginx-config) en een nagespeelde Authentik.
//
//	CF_TEST_NPM_DOCKER=1 CF_TEST_DATABASE_URL=... go test -run TestRealNPM ./internal/publish/
//
// Nodig: docker en sqlite3, en schrijfrechten op de map die NPM als root
// vult (in CI met sudo).
func TestRealNPM(t *testing.T) {
	if os.Getenv("CF_TEST_NPM_DOCKER") == "" {
		t.Skip("CF_TEST_NPM_DOCKER niet gezet")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatal("sqlite3 ontbreekt")
	}
	pool := storetest.DB(t)
	ctx := context.Background()
	image := os.Getenv("CF_TEST_NPM_IMAGE")
	if image == "" {
		image = "jc21/nginx-proxy-manager:2"
	}
	dir, err := os.MkdirTemp("", "cfnpm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data := filepath.Join(dir, "data")
	name := "cf-npm-test-" + strings.ToLower(rand.Text()[:6])
	// Vaste poorten: bij terugzetten stopt en start de agent NPM, en
	// willekeurige poorten van Docker veranderen dan.
	docker(t, "run", "-d", "--name", name, "-e", "DISABLE_IPV6=true",
		"-p", "127.0.0.1:"+freePort(t)+":80", "-p", "127.0.0.1:"+freePort(t)+":81", "-p", "127.0.0.1:"+freePort(t)+":443",
		"-v", data+":/data", "-v", filepath.Join(dir, "le")+":/etc/letsencrypt", image)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "60", name).CombinedOutput()
			t.Logf("logs van NPM:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	p80, p81, p443 := hostPort(t, name, "80"), hostPort(t, name, "81"), hostPort(t, name, "443")
	gateway := docker(t, "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}")

	// Wachten tot NPM antwoordt (de eerste start migreert de database).
	deadline := time.Now().Add(3 * time.Minute)
	var setup bool
	for {
		status, body := 0, []byte(nil)
		if res, err := http.Get("http://127.0.0.1:" + p81 + "/api/"); err == nil {
			body, _ = io.ReadAll(res.Body)
			_ = res.Body.Close()
			status = res.StatusCode
		}
		if status == 200 {
			var st struct {
				Setup *bool `json:"setup"`
			}
			_ = json.Unmarshal(body, &st)
			setup = st.Setup == nil || *st.Setup
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("NPM antwoordt niet")
		}
		time.Sleep(2 * time.Second)
	}
	identity, secret := "admin@example.com", "changeme"
	if !setup {
		identity, secret = "cf@example.test", "Geheim-wachtwoord-1"
		if s, b := npmAPI(t, p81, "POST", "/users", "", map[string]any{"name": "ClusterForge", "nickname": "cf", "email": identity,
			"roles": []string{"admin"}, "auth": map[string]any{"type": "password", "secret": secret}}); s >= 300 {
			t.Fatalf("eerste gebruiker: %d %s", s, b)
		}
	}

	// De dienst achter NPM en de nagespeelde Authentik, bereikbaar vanuit
	// de container via de gateway van het bridge-netwerk.
	backend := listenAll(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<title>Homepage</title>backend")
	}))
	fake := newFakeAuthentik()
	akPort := listenAll(t, fake)

	a := &agent.Agent{Log: slog.New(slog.DiscardHandler), NPMContainer: name, NPMSnapshotDir: filepath.Join(dir, "snapshots"),
		WebDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, _ := net.SplitHostPort(addr)
			if host == "127.0.0.1" {
				switch port {
				case "80":
					addr = "127.0.0.1:" + p80
				case "443":
					addr = "127.0.0.1:" + p443
				}
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}}
	log := slog.New(slog.DiscardHandler)
	ev := events.NewWriter(store.New(pool), log)
	runner := jobs.NewRunner(pool, ev, log)
	runner.Poll = 50 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runner.Run(rctx)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(pool, ev, log, runner, agentBus{a}, box)
	// NPM herlaadt nginx pas na zijn antwoord; zonder wachttijd antwoordt
	// soms nog de oude config (update naar een dichte poort gaf dan HTTP 200).
	// Dezelfde wachttijd als in productie.

	npmNode := seedNode(t, pool, "npm1", "127.0.0.1")
	akNode := seedNode(t, pool, "ak1", "127.0.0.1")
	npmPort, _ := json.Number(p81).Int64()
	defaults := DefaultDefaults()
	defaults.LetsEncrypt = false
	if _, err := s.SaveSettings(ctx, events.System(), nil, SettingsInput{Domain: "example.test", Defaults: &defaults,
		NPMNodeID: &npmNode, NPMPort: int(npmPort), NPMIdentity: identity, NPMSecret: secret,
		AuthentikNodeID: &akNode, AuthentikPort: akPort, AuthentikToken: "ak-token", AuthentikOutpost: fmt.Sprintf("http://%s:%d", gateway, akPort)}); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Test(ctx, "npm"); err != nil || res["hosts"] != 0 {
		t.Fatalf("test NPM: %v %v", res, err)
	}
	if res, err := s.Test(ctx, "authentik"); err != nil || res["outpost"] != "authentik Embedded Outpost" {
		t.Fatalf("test Authentik: %v %v", res, err)
	}

	wait := func(id uuid.UUID) (store.Job, string) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Minute)
		for {
			row, err := store.New(pool).GetJob(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			j := row.Job
			if j.Status == store.JobStatusSucceeded || j.Status == store.JobStatusFailed {
				steps, _ := store.New(pool).ListJobSteps(ctx, id)
				var b strings.Builder
				for _, st := range steps {
					fmt.Fprintf(&b, "[%s] %s\n", st.Status, st.Name)
					for _, l := range st.Log {
						fmt.Fprintf(&b, "    %s\n", l)
					}
				}
				return j, b.String()
			}
			if time.Now().After(deadline) {
				t.Fatalf("taak blijft %s", j.Status)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	run := func(act Action, want store.JobStatus) string {
		t.Helper()
		j, _, err := s.Start(ctx, events.System(), act)
		if err != nil {
			t.Fatalf("%s: %v", act.Action, err)
		}
		j, log := wait(j.ID)
		if j.Status != want {
			t.Fatalf("%s: %s (%s)\n%s", act.Action, j.Status, j.Error, log)
		}
		t.Logf("%s: %s\n%s", act.Action, j.Status, log)
		return log
	}
	f := false
	conf := func(id int) string {
		b, _ := os.ReadFile(filepath.Join(data, "nginx/proxy_host", fmt.Sprintf("%d.conf", id)))
		return string(b)
	}

	// 1. Publiceren: host gemaakt, database en config kloppen, adres antwoordt.
	run(Action{Action: ActPublish, Label: "app", ForwardHost: gateway, ForwardPort: backend, SSL: "none", Authentik: &f}, store.JobStatusSucceeded)
	s.Refresh(ctx)
	ov, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Hosts) != 1 || !ov.Hosts[0].Managed || !ov.Hosts[0].CreatedByCF || ov.Hosts[0].Domains[0] != "app.example.test" {
		t.Fatalf("hosts: %+v", ov.Hosts)
	}
	id := ov.Hosts[0].ID
	if !strings.Contains(conf(id), "app.example.test") {
		t.Fatalf("config: %s", conf(id))
	}
	if _, err := s.Plan(ctx, Action{Action: ActPublish, Label: "app", ForwardHost: gateway, ForwardPort: backend}); err == nil {
		t.Fatal("dubbele naam hoort geweigerd te worden")
	}

	// 1b. Verkeer en logins (fase 39): aanvragen via NPM, events in
	// Authentik, en een ban van CrowdSec voor het IP dat NPM ziet.
	for range 5 {
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+p80+"/", nil)
		req.Host = "app.example.test"
		if res, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
	}
	now := time.Now()
	for i := range 12 {
		fake.addEvent("login_failed", "AnonymousUser", "203.0.113.9", now.Add(-time.Duration(30-i)*time.Minute), map[string]any{"username": "admin", "geo": map[string]any{"country": "NL"}})
	}
	fake.addEvent("login", "jonas", "198.51.100.7", now.Add(-5*time.Minute), nil)
	fake.addEvent("authorize_application", "jonas", "198.51.100.7", now.Add(-4*time.Minute), map[string]any{"authorized_application": map[string]any{"name": "App"}})
	var tv *TrafficView
	deadline = time.Now().Add(30 * time.Second)
	for {
		// NPM schrijft zijn log met een kleine buffer.
		s.PullFeeds(ctx)
		tv, err = s.Traffic(ctx, "1h")
		if err != nil {
			t.Fatal(err)
		}
		if tv.Totals.Requests >= 5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if tv.Totals.Requests < 5 || len(tv.Hosts) != 1 || tv.Hosts[0].Domain != "app.example.test" || tv.Totals.Visitors < 1 || len(tv.IPs) == 0 {
		t.Fatalf("verkeer: %+v feeds %+v", tv.Totals, tv.Feeds)
	}
	if tv.Totals.Connections < 0 || tv.Totals.ConnAt == nil {
		t.Errorf("verbindingen: %+v", tv.Totals)
	}
	seenIP := tv.IPs[0].IP
	if _, err := pool.Exec(ctx, `INSERT INTO crowdsec_decisions (node_id, decision_id, origin, type, scope, value, scenario, until, first_seen)
		VALUES ($1, 1, 'crowdsec', 'ban', 'Ip', $2, 'crowdsecurity/http-probing', now() + interval '4 hours', now() - interval '3 hours'),
		       ($1, 2, 'crowdsec', 'ban', 'Range', '203.0.113.0/24', 'crowdsecurity/ssh-bf', now() + interval '4 hours', now())`, npmNode, seenIP); err != nil {
		t.Fatal(err)
	}
	// De ban begon drie uur geleden; het verkeer van nu kwam er dus nog door.
	if tv, err = s.Traffic(ctx, "24h"); err != nil || tv.Compare.BannedSeen != 1 || tv.Compare.BannedPassing != 1 || tv.IPs[0].Ban == nil || tv.IPs[0].AfterBan < 5 {
		t.Fatalf("vergelijking met CrowdSec: %v %+v %+v", err, tv.Compare, tv.IPs)
	}
	lv, err := s.Logins(ctx, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if lv.Totals.Failed != 12 || lv.Totals.Logins != 1 || len(lv.FailedIPs) != 1 || lv.FailedIPs[0].IP != "203.0.113.9" ||
		lv.FailedIPs[0].Ban == nil || lv.FailedIPs[0].Usernames[0] != "admin" || len(lv.Apps) != 1 || lv.Apps[0].App != "App" {
		t.Fatalf("logins: %+v %+v %+v", lv.Totals, lv.FailedIPs, lv.Apps)
	}
	// Nog eens ophalen: niets dubbel.
	s.PullFeeds(ctx)
	if lv2, _ := s.Logins(ctx, "24h"); lv2.Totals.Failed != 12 {
		t.Fatalf("dubbel geteld: %+v", lv2.Totals)
	}
	notices, _ := s.Notices(ctx)
	var ids []string
	for _, n := range notices {
		ids = append(ids, n.ID)
	}
	if !slices.Contains(ids, "publish-ban-passing") || slices.Contains(ids, "publish-failed-logins") {
		t.Fatalf("meldingen: %v", ids)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM crowdsec_decisions`); err != nil {
		t.Fatal(err)
	}
	if notices, _ = s.Notices(ctx); !slices.ContainsFunc(notices, func(n Notice) bool { return n.ID == "publish-failed-logins" }) {
		t.Fatalf("geen melding voor mislukte logins zonder ban: %+v", notices)
	}

	// 2. Een doel waar niets draait: de controle faalt en de host gaat terug.
	// Een poort die net vrijkwam; closed+1 kan op een runner wel bezet zijn.
	closed, _ := strconv.Atoi(freePort(t))
	logText := run(Action{Action: ActUpdate, HostID: id, ForwardPort: closed}, store.JobStatusFailed)
	if !strings.Contains(logText, "HTTP 502") || !strings.Contains(logText, "terug naar de vorige waarden") {
		t.Fatalf("terugdraaien niet gezien:\n%s", logText)
	}
	if !strings.Contains(conf(id), fmt.Sprintf("set $port           %d", backend)) && !strings.Contains(conf(id), fmt.Sprintf("%d;", backend)) {
		t.Fatalf("config niet terug op poort %d:\n%s", backend, conf(id))
	}
	out, _ := exec.Command("sqlite3", filepath.Join(data, "database.sqlite"), fmt.Sprintf("SELECT forward_port FROM proxy_host WHERE id = %d;", id)).CombinedOutput()
	if strings.TrimSpace(string(out)) != fmt.Sprint(backend) {
		t.Fatalf("database niet terug: %s", out)
	}

	// 3. Achter Authentik: provider, applicatie, outpost en forward auth;
	// zonder aanmelding stuurt NPM door naar de outpost.
	logText = run(Action{Action: ActProtect, HostID: id}, store.JobStatusSucceeded)
	if apps, provs, inOut := fake.counts(); apps != 1 || provs != 1 || inOut != 1 {
		t.Fatalf("Authentik: %d apps, %d providers, %d in outpost", apps, provs, inOut)
	}
	if !strings.Contains(logText, "outpost.goauthentik.io/start") {
		t.Fatalf("geen doorsturen naar Authentik:\n%s", logText)
	}
	s.Refresh(ctx)
	if ov, _ = s.Overview(ctx); ov.Hosts[0].Authentik != AKProtected || len(ov.Hosts[0].Drift) != 0 {
		t.Fatalf("na beschermen: %+v", ov.Hosts[0])
	}

	// 4. Drift: iemand verandert de host in NPM; herstellen zet hem terug.
	tok := loginNPM(t, p81, identity, secret)
	if st, b := npmAPI(t, p81, "PUT", fmt.Sprintf("/nginx/proxy-hosts/%d", id), tok, map[string]any{"block_exploits": false}); st >= 300 {
		t.Fatalf("drift maken: %d %s", st, b)
	}
	s.Refresh(ctx)
	if ov, _ = s.Overview(ctx); len(ov.Hosts[0].Drift) != 1 {
		t.Fatalf("drift niet gezien: %+v", ov.Hosts[0])
	}
	run(Action{Action: ActRestore, HostID: id}, store.JobStatusSucceeded)
	s.Refresh(ctx)
	if ov, _ = s.Overview(ctx); len(ov.Hosts[0].Drift) != 0 {
		t.Fatalf("drift blijft: %+v", ov.Hosts[0].Drift)
	}

	// 5. Authentik weghalen: snippet weg, wat ClusterForge maakte weg.
	run(Action{Action: ActUnprotect, HostID: id}, store.JobStatusSucceeded)
	if apps, provs, inOut := fake.counts(); apps != 0 || provs != 0 || inOut != 0 {
		t.Fatalf("Authentik na weghalen: %d apps, %d providers, %d in outpost", apps, provs, inOut)
	}
	if strings.Contains(conf(id), "goauthentik") {
		t.Fatal("snippet staat nog in de config")
	}

	// 6. De kopie terugzetten (de laatste stap van het terugdraaien): een
	// host die na de kopie verdween, komt terug, met database en config.
	snap := a.WebRequest(ctx, protocol.WebRequest{Op: protocol.WebOpNPMSnapshot})
	if snap.Error != "" {
		t.Fatal(snap.Error)
	}
	if st, b := npmAPI(t, p81, "DELETE", fmt.Sprintf("/nginx/proxy-hosts/%d", id), tok, nil); st >= 300 {
		t.Fatalf("verwijderen: %d %s", st, b)
	}
	res := a.WebRequest(ctx, protocol.WebRequest{Op: protocol.WebOpNPMRestore, NPM: &protocol.WebNPM{Snapshot: snap.NPM.Snapshot}})
	if res.Error != "" || !res.NPM.AllOK() {
		t.Fatalf("terugzetten: %s %+v", res.Error, res.NPM)
	}
	chk := a.WebRequest(ctx, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{HostID: id, Domain: "app.example.test"}})
	if !chk.NPM.AllOK() {
		t.Fatalf("na terugzetten: %+v", chk.NPM.Checks)
	}
	tok = loginNPM(t, p81, identity, secret)
	if st, _ := npmAPI(t, p81, "GET", fmt.Sprintf("/nginx/proxy-hosts/%d", id), tok, nil); st != 200 {
		t.Fatalf("host niet terug via de API: %d", st)
	}

	// 7. Hoofddomein omzetten, met de oude naam als extra naam.
	s.Refresh(ctx)
	run(Action{Action: ActRename, OldDomain: "example.test", NewDomain: "example.org", KeepOld: true}, store.JobStatusSucceeded)
	if c := conf(id); !strings.Contains(c, "app.example.org") || !strings.Contains(c, "app.example.test") {
		t.Fatalf("hernoemen: %s", c)
	}

	// 8. Verwijderen vraagt de naam; daarna is de host weg.
	s.Refresh(ctx)
	if _, _, err := s.Start(ctx, events.System(), Action{Action: ActDelete, HostID: id}); err == nil {
		t.Fatal("verwijderen zonder bevestiging")
	}
	run(Action{Action: ActDelete, HostID: id, Confirm: "app.example.org"}, store.JobStatusSucceeded)
	if _, err := os.Stat(filepath.Join(data, "nginx/proxy_host", fmt.Sprintf("%d.conf", id))); !os.IsNotExist(err) {
		t.Fatal("config bestaat nog")
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM publish_hosts`).Scan(&n)
	if n != 0 {
		t.Fatalf("publish_hosts: %d", n)
	}

	// 9. Fase 42: twee oude hosts naar een adres dat niet meer bestaat; de
	// dienst draait nu op een andere node. Voorstel op naam, en alles in één
	// keer toepassen, host voor host.
	alive := func() {
		if _, err := pool.Exec(ctx, `UPDATE node_status SET heartbeat_at = now()`); err != nil {
			t.Fatal(err)
		}
	}
	alive()
	tok = loginNPM(t, p81, identity, secret)
	var old []int
	for _, name := range []string{"webapp.example.test", "webapp2.example.test"} {
		st, b := npmAPI(t, p81, "POST", "/nginx/proxy-hosts", tok, map[string]any{"domain_names": []string{name}, "forward_scheme": "http",
			"forward_host": "10.255.255.1", "forward_port": 8080, "certificate_id": 0, "meta": map[string]any{}, "advanced_config": "", "locations": []any{}})
		var h struct {
			ID int `json:"id"`
		}
		if st >= 300 || json.Unmarshal(b, &h) != nil {
			t.Fatalf("oude host maken: %d %s", st, b)
		}
		old = append(old, h.ID)
	}
	s.Refresh(ctx)
	svcNode := seedNode(t, pool, "svc1", gateway)
	svcs, _ := json.Marshal([]protocol.WebService{{Proto: "tcp", Addr: "0.0.0.0", Port: backend, Container: "webapp", Image: "example/webapp:1", Kind: "http", Status: 200}})
	if _, err := pool.Exec(ctx, `INSERT INTO publish_services (node_id, services) VALUES ($1, $2)`, svcNode, svcs); err != nil {
		t.Fatal(err)
	}
	alive()
	ov, _ = s.Overview(ctx)
	sugg := 0
	for _, h := range ov.Hosts {
		if slices.Contains(old, h.ID) {
			if h.Suggest == nil || h.Suggest.Host != gateway || h.Suggest.Port != backend {
				t.Fatalf("voorstel voor %v: %+v", h.Domains, h.Suggest)
			}
			sugg++
		}
	}
	if sugg != 2 {
		for _, h := range ov.Hosts {
			t.Logf("host %d %v → %s:%d %+v", h.ID, h.Domains, h.ForwardHost, h.ForwardPort, h.Suggest)
		}
		for _, r := range ov.Services {
			t.Logf("dienst %s %s %s:%d web=%v", r.Key, r.Name, r.Target, r.Port, r.Web)
		}
		t.Fatalf("%d voorstellen (oud %v)", sugg, old)
	}
	plan, err := s.Plan(ctx, Action{Action: ActRetargetMany})
	if err != nil || len(plan.Hosts) != 2 {
		t.Fatalf("plan: %v %+v", err, plan)
	}
	run(Action{Action: ActRetargetMany}, store.JobStatusSucceeded)
	for _, hid := range old {
		if c := conf(hid); !strings.Contains(c, gateway) || !strings.Contains(c, fmt.Sprint(backend)) {
			t.Fatalf("host %d niet omgezet:\n%s", hid, c)
		}
	}

	// 10. Bewerken: eigen Advanced, een extra naam en de tikfout rechtzetten.
	alive()
	s.Refresh(ctx)
	adv := "add_header X-CF-Test fase42;"
	run(Action{Action: ActUpdate, HostID: old[0], AdvancedConfig: &adv, Domains: []string{"webapp.exampel.test", "extra.example.test"}}, store.JobStatusSucceeded)
	if c := conf(old[0]); !strings.Contains(c, "X-CF-Test fase42") || !strings.Contains(c, "extra.example.test") {
		t.Fatalf("bewerken:\n%s", c)
	}
	s.Refresh(ctx)
	ov, _ = s.Overview(ctx)
	for _, h := range ov.Hosts {
		if h.ID == old[0] && (h.Typo != "webapp.example.test" || h.Advanced != adv || len(h.TypoDomains) != 2) {
			t.Fatalf("tikfout of advanced: %+v", h)
		}
	}
	alive()
	run(Action{Action: ActUpdate, HostID: old[0], Domains: []string{"webapp.example.test", "extra.example.test"}}, store.JobStatusSucceeded)
	if c := conf(old[0]); !strings.Contains(c, "webapp.example.test") || strings.Contains(c, "exampel") {
		t.Fatalf("tikfout niet rechtgezet:\n%s", c)
	}
}

func loginNPM(t *testing.T, port, identity, secret string) string {
	t.Helper()
	st, b := npmAPI(t, port, "POST", "/tokens", "", map[string]string{"identity": identity, "secret": secret})
	var r struct {
		Token string `json:"token"`
	}
	if st != 200 || json.Unmarshal(b, &r) != nil {
		t.Fatalf("token: %d %s", st, b)
	}
	return r.Token
}
