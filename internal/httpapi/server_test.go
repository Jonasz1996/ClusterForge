package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/disaster"
	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/incidents"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/kubenodes"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/reports"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/internal/whatbroke"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// testEnv start de volledige HTTP-server tegen een echte PostgreSQL uit
// CF_TEST_DATABASE_URL. Het schema wordt per test leeggemaakt.
type testEnv struct {
	t       *testing.T
	srv     *httptest.Server
	auth    *auth.Service
	pool    *pgxpool.Pool
	api     *Server
	bus     *agentbus.Bus
	eval    *status.Evaluator
	vm      *fakeVM
	pve     *proxmox.Service
	jobs    *jobs.Runner
	life    *lifecycle.Service
	deploy  *deploy.Service
	backups *backups.Service
	drift   *drift.Service
	fo      *failover.Service
	dis     *disaster.Service
	// evalClock is hoe ver de klok van de evaluator vooruit staat.
	evalClock *atomic.Int64
	deps      *deps.Service
	ro        *rollout.Service
	git       *gitops.Service
	det       *incidents.Detector
	ana       *whatbroke.Analyzer
	conns     *testConns
	asst      *assistant.Service
	risk      *risk.Service
	topo      *topo.Service
	docs      *docs.Service
	reports   *reports.Service
	kube      *kube.Service
	dbs       *databases.Service
	crons     *cronjobs.Service
	cmps      *components.Service
	ingest    *metrics.Ingester

	// De runner heeft een eigen context, zodat een test hem kan herstarten.
	runCtx     context.Context
	runnerMu   sync.Mutex
	runnerStop context.CancelFunc
	runnerDone chan struct{}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	pool := storetest.DB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := store.New(pool)
	a, err := auth.NewService(q, events.NewWriter(q, log), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	vm := newFakeVM(t)
	ev := events.NewWriter(q, log)
	eval := status.NewEvaluator(pool, ev, log)
	// Heartbeats komen hier elke 100 ms; een wissel duurt dus veel korter.
	eval.Warmup, eval.VIPGrace = 0, time.Second
	// Een test zet de klok van de evaluator vooruit, zodat een node zonder
	// heartbeat down is zonder 90 s te wachten.
	evalClock := new(atomic.Int64)
	eval.Now = func() time.Time { return time.Now().Add(time.Duration(evalClock.Load())) }
	ingest := metrics.NewIngester(vm.srv.URL, q, log)
	bus, err := agentbus.Start(ctx, "127.0.0.1:0", pool, ev, log, agentbus.Hooks{
		Metrics: func(ctx context.Context, nodeID uuid.UUID, m protocol.Metrics) error {
			if err := ingest.Ingest(ctx, nodeID, m, time.Now()); err != nil {
				return err
			}
			ingest.Flush(ctx)
			return nil
		},
		Changed: eval.Kick,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)
	runCtx, stop := context.WithCancel(ctx)
	hub := live.NewHub(pool, log)
	runner := jobs.NewRunner(pool, ev, log)
	runner.Poll, runner.Heartbeat = 50*time.Millisecond, 50*time.Millisecond
	box, err := secrets.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	bus.SetFingerprintKey(box.Derive(secrets.PurposeFile))
	pve := proxmox.NewService(pool, ev, log, box, runner)
	pve.TaskPoll = 20 * time.Millisecond
	pve.Changed = eval.Kick
	life := lifecycle.NewService(pool, ev, log, runner, bus)
	life.Changed = eval.Kick
	life.Poll, life.PowerDelay, life.OffAfter = 20*time.Millisecond, 0, time.Second
	life.Offline, life.Stale = time.Second, time.Second
	life.MoveTimeout, life.BootTimeout, life.OffTimeout, life.ReadyTimeout = 5*time.Second, 8*time.Second, 8*time.Second, 5*time.Second
	dep := deploy.NewService(pool, ev, log, runner, box, pve, bus)
	dep.Changed = eval.Kick
	dep.Poll, dep.GuestAgentTimeout, dep.EnrollTimeout = 20*time.Millisecond, 5*time.Second, 8*time.Second
	dep.HTTPGet = func(context.Context, string) (int, error) { return http.StatusOK, nil }
	// De sync van Kubernetes loopt niet vanzelf; een test roept Sync aan.
	kub := kube.NewService(pool, bus, log)
	kub.Interval, kub.Timeout = 0, 5*time.Second
	kub.DrainTimeout, kub.DrainRetry, kub.DrainPoll = 3*time.Second, 50*time.Millisecond, 20*time.Millisecond
	kub.RestorePoll, kub.RestoreWait = 20*time.Millisecond, 3*time.Second
	kub.EnableDrain(runner, ev)
	kub.Metrics, kub.Changed = ingest, eval.Kick
	dep.Kube = kub
	life.Kube, life.KubeReadyTimeout = kub, 5*time.Second
	kinv := inventory.NewService(pool, ev)
	kinv.Disconnect = bus.Disconnect
	kn := kubenodes.NewService(pool, ev, runner, kub, pve, kinv, dep)
	kn.Changed = eval.Kick
	kn.Poll, kn.ShutdownTimeout = 20*time.Millisecond, 2*time.Second
	bk := backups.NewService(pool, ev, log, pve)
	bk.Poll, bk.ConnPoll, bk.BootTimeout, bk.NoAgentWait = 20*time.Millisecond, 20*time.Millisecond, 3*time.Second, 100*time.Millisecond
	bk.AgentTimeout, bk.LockWait, bk.VIPWait = 2*time.Second, time.Second, 3*time.Second
	bk.Running = dep.Running
	conns := &testConns{bus: bus}
	bk.EnableVerify(runner, conns)
	// Zonder Interval loopt de scanner niet; een test start hem zelf.
	drf := drift.NewService(pool, ev, log, bus, dep, box.Derive(secrets.PurposeFile))
	drf.Interval = 0
	fo := failover.NewService(pool, ev, log, runner, bus)
	fo.Changed = eval.Kick
	fo.ProbeInterval, fo.Gate.Poll, fo.Retry = 20*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	fo.ReturnTimeout, fo.Fresh, fo.EmergencyTimeout = 8*time.Second, 2*time.Second, 3*time.Second
	fo.Proxmox, fo.BootTimeout = pve, 8*time.Second
	// De resolver draait niet vanzelf; een test roept Resolve aan.
	dps := deps.NewService(pool, ev, log)
	// ClusterForge zelf staat in de tests op 192.0.2.5, met NATS en de
	// webinterface.
	dps.Self = func(context.Context) deps.Skip { return deps.SelfSkip([]string{"192.0.2.5"}, []int{4222, 8080}) }
	// De detector heeft korte tijden, zodat een test een incident ziet
	// sluiten.
	det := incidents.NewDetector(pool, ev, log)
	det.Rules = incidents.Rules{Join: 10 * time.Second, Settle: time.Second, Reopen: 2 * time.Second}
	eval.After = func(ctx context.Context) error {
		return errors.Join(dps.Evaluate(ctx), det.Detect(ctx))
	}
	ana := whatbroke.NewAnalyzer(pool, metrics.NewClient(vm.srv.URL), log)
	det.Changed = ana.Kick
	ro := rollout.NewService(pool, ev, log, runner, bus, dep, drf)
	ro.Changed = eval.Kick
	ro.Gate.Poll, ro.Retry, ro.Fresh = 20*time.Millisecond, 50*time.Millisecond, 2*time.Second
	ro.ReadyTimeout, ro.SettleTimeout, ro.CheckTimeout = 5*time.Second, 5*time.Second, 2*time.Second
	ro.Kube, ro.KubeReadyTimeout = kub, 5*time.Second
	// De GitOps-lus loopt niet vanzelf; een test start hem of roept Sync aan.
	git := gitops.NewService(pool, ev, log, box, dep, ro)
	git.Interval = 0
	// De assistent praat met een nagespeelde API die een test zelf zet.
	asst := assistant.NewService(pool, ev, log, box)
	// De health score rekent niet vanzelf; een test roept Compute aan.
	rsk := risk.NewService(pool, ev, log, metrics.NewClient(vm.srv.URL), bk)
	// De eigen adressen van de server zet een test zelf.
	tp := topo.NewService(pool, ev, log, "")
	tp.Own = func(context.Context, []string) topo.Own { return topo.Own{} }
	rsk.Topo, rsk.Deps = tp, dps
	// Rampoefeningen met korte tijden; een test zet de probe zelf.
	dis := disaster.NewService(pool, ev, log, runner, bus)
	dis.Changed, dis.Probes, dis.Proxmox, dis.World = eval.Kick, fo.ProbeFor, pve, rsk.World
	dis.ProbeInterval, dis.StatusPoll, dis.Gate.Poll, dis.Retry = 20*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	dis.ShortOutage, dis.Fresh, dis.EmergencyTimeout = 500*time.Millisecond, 2*time.Second, 3*time.Second
	dis.ReturnTimeout, dis.BootTimeout = 8*time.Second, 8*time.Second
	dis.Backups = bk
	// De documentatie schrijft niet vanzelf; een test roept GenerateAll aan.
	dcs := docs.NewService(pool, log)
	dcs.Deploy, dcs.GitOps, dcs.Backups, dcs.Risk, dcs.Box, dcs.Events = dep, git, bk, rsk, box, ev
	dcs.Opts = docs.Opts{Usernames: true}
	// De databases leest een test zelf met SyncAll.
	dbs := databases.NewService(pool, bus, log)
	dbs.Interval, dbs.Timeout = 0, 5*time.Second
	// De cronjobs ook.
	crons := cronjobs.NewService(pool, bus, ev, log)
	crons.Interval, crons.Timeout = 0, 5*time.Second
	bus.SetCronRuns(crons.HandleRuns)
	// De database als onderdeel, met korte tijden; zonder metingen van het
	// geheugen tenzij een test ze zet.
	cmps := components.NewService(pool, ev, log, runner, bus, dep)
	cmps.Changed = eval.Kick
	cmps.Poll, cmps.GateTimeout, cmps.Timeout, cmps.Retry = 20*time.Millisecond, 5*time.Second, 2*time.Second, 50*time.Millisecond
	cmps.VIPTimeout, cmps.DumpPoll, cmps.MoveDrain, cmps.PendingWait = 5*time.Second, 20*time.Millisecond, 200*time.Millisecond, 500*time.Millisecond
	cmps.Overview = dbs.Overview
	life.DB = cmps
	rsk.DB = cmps
	drf.Components = cmps
	dcs.Components = cmps
	fo.DB = cmps
	fo.WriteStart, fo.WritePoll, fo.WriteFinishTimeout, fo.DBReturnTimeout = 3*time.Second, 50*time.Millisecond, 3*time.Second, 8*time.Second
	fo.RebootDelay, fo.RestartMargin, fo.RestartPoll = 0, 15*time.Second, 50*time.Millisecond
	rep := reports.NewService(pool, log)
	rep.Backups, rep.Risk = bk, rsk
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); hub.Run(runCtx) }()
	go func() { defer wg.Done(); eval.Run(runCtx) }()
	go func() { defer wg.Done(); ana.Run(runCtx) }()
	cfg := config.Config{
		SecureCookies: false, SessionTTL: time.Hour, AgentDir: t.TempDir(), VictoriaMetricsURL: vm.srv.URL,
		GrafanaNodeURL: "https://grafana.example/d/node?var-node={hostname}",
	}
	api := New(Deps{
		Config: cfg, Log: log, Pool: pool, Auth: a, Bus: bus, Hub: hub, Proxmox: pve, Jobs: runner, Lifecycle: life,
		Deploy: dep, Backups: bk, Drift: drf, Failover: fo, Disaster: dis, Deps: dps, Rollout: ro, GitOps: git, Assistant: asst,
		Risk: rsk, Topo: tp, Docs: dcs, Reports: rep, Kube: kub, KubeNodes: kn, Databases: dbs, Cronjobs: crons, Components: cmps, Version: "test",
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	e := &testEnv{t: t, srv: srv, auth: a, pool: pool, api: api, bus: bus, eval: eval, vm: vm, pve: pve, jobs: runner, life: life, deploy: dep,
		backups: bk, drift: drf, fo: fo, dis: dis, evalClock: evalClock, deps: dps, ro: ro, git: git, det: det, ana: ana, conns: conns, asst: asst, risk: rsk, topo: tp, docs: dcs, reports: rep, kube: kub, dbs: dbs, crons: crons, cmps: cmps, ingest: ingest, runCtx: runCtx}
	e.startRunner()
	t.Cleanup(func() {
		stop()
		wg.Wait()
		e.runnerMu.Lock()
		done := e.runnerDone
		e.runnerMu.Unlock()
		<-done
	})
	return e
}

// testConns telt de verbindingen van de echte bus, plus extra: zo speelt een
// test een tweede verbinding die Bus.Check toch doorliet.
type testConns struct {
	bus   *agentbus.Bus
	extra atomic.Int32
}

func (c *testConns) Connections(nkey string) (int, error) {
	n, err := c.bus.Connections(nkey)
	return n + int(c.extra.Load()), err
}

func (e *testEnv) startRunner() {
	ctx, cancel := context.WithCancel(e.runCtx)
	done := make(chan struct{})
	go func() { defer close(done); e.jobs.Run(ctx) }()
	e.runnerMu.Lock()
	e.runnerStop, e.runnerDone = cancel, done
	e.runnerMu.Unlock()
}

// stopRunner stopt de runner tot startRunner; taken blijven in de
// wachtrij staan.
func (e *testEnv) stopRunner() {
	e.runnerMu.Lock()
	stop, done := e.runnerStop, e.runnerDone
	e.runnerMu.Unlock()
	stop()
	<-done
}

// restartRunner stopt de runner zoals bij een nette stop van de server en
// start hem opnieuw; lopende taken worden dan hervat.
func (e *testEnv) restartRunner() {
	e.runnerMu.Lock()
	stop, done := e.runnerStop, e.runnerDone
	e.runnerMu.Unlock()
	stop()
	<-done
	e.startRunner()
}

// fakeVM speelt VictoriaMetrics: het bewaart wat binnenkomt en geeft op elke
// query één vaste lijn terug.
type fakeVM struct {
	srv     *httptest.Server
	mu      sync.Mutex
	lines   []string
	queries []string
	// instant geeft het resultaat van een instant query, als JSON-lijst;
	// zonder is het leeg.
	instant func(query string) string
}

func newFakeVM(t *testing.T) *fakeVM {
	vm := &fakeVM{}
	vm.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/import/prometheus":
			body, _ := io.ReadAll(r.Body)
			vm.mu.Lock()
			vm.lines = append(vm.lines, strings.Split(strings.TrimSpace(string(body)), "\n")...)
			vm.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/query_range":
			vm.mu.Lock()
			vm.queries = append(vm.queries, r.URL.Query().Get("query"))
			vm.mu.Unlock()
			start := r.URL.Query().Get("start")
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[`+
				`{"metric":{"node":"web01","device":"eth0","mountpoint":"/"},"values":[[%s,"1.5"]]}]}}`, start)
		case "/api/v1/query":
			vm.mu.Lock()
			f := vm.instant
			vm.mu.Unlock()
			result := "[]"
			if f != nil {
				result = f(r.URL.Query().Get("query"))
			}
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(vm.srv.Close)
	return vm
}

func (vm *fakeVM) received() []string {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	return slices.Clone(vm.lines)
}

func (e *testEnv) createUser(name, password string, role store.UserRole) {
	e.t.Helper()
	if _, err := e.auth.CreateUser(context.Background(), events.System(), name, password, role); err != nil {
		e.t.Fatal(err)
	}
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func (e *testEnv) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.srv.URL, http: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type apiErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type me struct {
	CsrfToken string `json:"csrf_token"`
	User      struct {
		Username    string `json:"username"`
		Role        string `json:"role"`
		TotpEnabled bool   `json:"totp_enabled"`
	} `json:"user"`
}

func (c *client) login(user, pass, code string) (int, me, apiErr) {
	c.t.Helper()
	body := map[string]any{"username": user, "password": pass}
	if code != "" {
		body["totp_code"] = code
	}
	var raw json.RawMessage
	status := c.do("POST", "/api/v1/auth/login", body, &raw)
	var m me
	var e apiErr
	_ = json.Unmarshal(raw, &m)
	_ = json.Unmarshal(raw, &e)
	if status == http.StatusOK {
		c.csrf = m.CsrfToken
	}
	return status, m, e
}

func TestHealth(t *testing.T) {
	e := newTestEnv(t)
	var h map[string]string
	if s := e.client().do("GET", "/api/v1/health", nil, &h); s != 200 || h["database"] != "ok" {
		t.Fatalf("health: status %d, body %v", s, h)
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()

	if s := c.do("GET", "/api/v1/auth/me", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("me zonder sessie: %d", s)
	}
	if s, _, er := c.login("jonas", "verkeerd-wachtwoord", ""); s != 401 || er.Code != "invalid_credentials" {
		t.Fatalf("fout wachtwoord: %d %s", s, er.Code)
	}
	if s, _, er := c.login("niemand", "een-lang-wachtwoord", ""); s != 401 || er.Code != "invalid_credentials" {
		t.Fatalf("onbekende gebruiker: %d %s", s, er.Code)
	}
	s, m, _ := c.login("JONAS", "een-lang-wachtwoord", "")
	if s != 200 || m.User.Username != "jonas" || m.CsrfToken == "" {
		t.Fatalf("login: %d %+v", s, m)
	}
	var got me
	if s := c.do("GET", "/api/v1/auth/me", nil, &got); s != 200 || got.User.Role != "admin" {
		t.Fatalf("me: %d %+v", s, got)
	}

	// Zonder CSRF-token mag een schrijvende request niet.
	csrf := c.csrf
	c.csrf = ""
	var er apiErr
	if s := c.do("POST", "/api/v1/auth/logout", nil, &er); s != 403 || er.Code != "csrf" {
		t.Fatalf("logout zonder csrf: %d %s", s, er.Code)
	}
	c.csrf = csrf
	if s := c.do("POST", "/api/v1/auth/logout", nil, nil); s != 204 {
		t.Fatalf("logout: %d", s)
	}
	if s := c.do("GET", "/api/v1/auth/me", nil, nil); s != 401 {
		t.Fatalf("me na logout: %d", s)
	}
}

func TestTOTPFlow(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	if s, _, _ := c.login("jonas", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatalf("login: %d", s)
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	if s := c.do("POST", "/api/v1/auth/totp/setup", nil, &setup); s != 200 || setup.Secret == "" {
		t.Fatalf("totp setup: %d", s)
	}
	var er apiErr
	if s := c.do("POST", "/api/v1/auth/totp/enable", map[string]string{"code": "000000"}, &er); s != 400 || er.Code != "invalid_totp" {
		t.Fatalf("enable met foute code: %d %s", s, er.Code)
	}
	// Elke code mag maar één keer; gebruik opeenvolgende periodes binnen de
	// toegestane klokafwijking. Start niet vlak voor een periodegrens, anders
	// valt de oudste code buiten het venster.
	if rest := 30 - time.Now().Unix()%30; rest < 10 {
		time.Sleep(time.Duration(rest)*time.Second + 100*time.Millisecond)
	}
	now := time.Now()
	code, _ := totp.GenerateCode(setup.Secret, now.Add(-30*time.Second))
	if s := c.do("POST", "/api/v1/auth/totp/enable", map[string]string{"code": code}, nil); s != 204 {
		t.Fatalf("enable: %d", s)
	}
	if s := c.do("POST", "/api/v1/auth/totp/setup", nil, &er); s != 409 {
		t.Fatalf("tweede setup terwijl TOTP aan staat: %d", s)
	}

	c2 := e.client()
	if s, _, er := c2.login("jonas", "een-lang-wachtwoord", ""); s != 401 || er.Code != "totp_required" {
		t.Fatalf("login zonder code: %d %s", s, er.Code)
	}
	if s, _, er := c2.login("jonas", "een-lang-wachtwoord", "123456"); s != 401 || er.Code != "invalid_totp" {
		t.Fatalf("login met foute code: %d %s", s, er.Code)
	}
	if s, _, er := c2.login("jonas", "een-lang-wachtwoord", code); s != 401 || er.Code != "invalid_totp" {
		t.Fatalf("hergebruik van de activatiecode: %d %s", s, er.Code)
	}
	code, _ = totp.GenerateCode(setup.Secret, now)
	if s, m, _ := c2.login("jonas", "een-lang-wachtwoord", code); s != 200 || !m.User.TotpEnabled {
		t.Fatalf("login met code: %d %+v", s, m)
	}
	if s, _, er := e.client().login("jonas", "een-lang-wachtwoord", code); s != 401 || er.Code != "invalid_totp" {
		t.Fatalf("dezelfde code twee keer: %d %s", s, er.Code)
	}
	code, _ = totp.GenerateCode(setup.Secret, now.Add(30*time.Second))

	if s := c2.do("POST", "/api/v1/auth/totp/disable", map[string]string{"password": "fout-fout-fout", "code": code}, nil); s != 400 {
		t.Fatalf("disable met fout wachtwoord: %d", s)
	}
	if s := c2.do("POST", "/api/v1/auth/totp/disable", map[string]string{"password": "een-lang-wachtwoord", "code": code}, nil); s != 204 {
		t.Fatalf("disable: %d", s)
	}
	if s, _, _ := e.client().login("jonas", "een-lang-wachtwoord", ""); s != 200 {
		t.Fatalf("login na disable: %d", s)
	}
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	a, b := e.client(), e.client()
	a.login("jonas", "een-lang-wachtwoord", "")
	b.login("jonas", "een-lang-wachtwoord", "")

	var er apiErr
	if s := a.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "een-lang-wachtwoord", "new_password": "kort"}, &er); s != 400 || er.Code != "validation" {
		t.Fatalf("te kort nieuw wachtwoord: %d %s", s, er.Code)
	}
	if s := a.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "een-lang-wachtwoord", "new_password": "nog-een-lang-wachtwoord"}, nil); s != 204 {
		t.Fatalf("wachtwoord wijzigen: %d", s)
	}
	if s := a.do("GET", "/api/v1/auth/me", nil, nil); s != 200 {
		t.Fatalf("eigen sessie na wijziging: %d", s)
	}
	if s := b.do("GET", "/api/v1/auth/me", nil, nil); s != 401 {
		t.Fatalf("andere sessie na wijziging: %d", s)
	}
	if s, _, _ := e.client().login("jonas", "nog-een-lang-wachtwoord", ""); s != 200 {
		t.Fatalf("login met nieuw wachtwoord: %d", s)
	}
}

func TestEventsAdminOnly(t *testing.T) {
	e := newTestEnv(t)
	e.createUser("jonas", "een-lang-wachtwoord", store.UserRoleAdmin)
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)

	v := e.client()
	v.login("kijker", "een-lang-wachtwoord", "")
	if s := v.do("GET", "/api/v1/events", nil, nil); s != 403 {
		t.Fatalf("events als viewer: %d", s)
	}

	a := e.client()
	a.login("jonas", "een-lang-wachtwoord", "")
	var res struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	if s := a.do("GET", "/api/v1/events?limit=10", nil, &res); s != 200 {
		t.Fatalf("events als admin: %d", s)
	}
	if len(res.Items) == 0 || res.Items[0].Action != "auth.login" {
		t.Fatalf("nieuwste event moet de login zijn: %+v", res.Items)
	}
	if s := a.do("GET", "/api/v1/events?limit=500", nil, nil); s != 400 {
		t.Fatalf("limit buiten bereik: %d", s)
	}
	if _, err := e.pool.Exec(context.Background(), "DELETE FROM events"); err == nil {
		t.Fatal("events verwijderen moet geweigerd worden")
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t)
	// Een limiter zonder bijvullen binnen de test, zodat trage runs (-race)
	// niet toevallig een extra poging krijgen.
	e.api.loginLimiter = newIPLimiter(time.Hour, 10)
	c := e.client()
	var last int
	for i := 0; i < 11; i++ {
		last, _, _ = c.login("niemand", "een-lang-wachtwoord", "")
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("elfde poging: %d, verwacht 429", last)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	e := newTestEnv(t)
	var er apiErr
	if s := e.client().do("GET", "/api/v1/bestaat-niet", nil, &er); s != 404 || er.Code != "not_found" {
		t.Fatalf("onbekend pad: %d %s", s, er.Code)
	}
}
