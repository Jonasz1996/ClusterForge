package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// testEnv start de volledige HTTP-server tegen een echte PostgreSQL uit
// CF_TEST_DATABASE_URL. Het schema wordt per test leeggemaakt.
type testEnv struct {
	t      *testing.T
	srv    *httptest.Server
	auth   *auth.Service
	pool   *pgxpool.Pool
	api    *Server
	bus    *agentbus.Bus
	eval   *status.Evaluator
	vm     *fakeVM
	pve    *proxmox.Service
	jobs   *jobs.Runner
	life   *lifecycle.Service
	deploy *deploy.Service
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
	eval.Warmup = 0
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
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); hub.Run(runCtx) }()
	go func() { defer wg.Done(); eval.Run(runCtx) }()
	go func() { defer wg.Done(); runner.Run(runCtx) }()
	t.Cleanup(func() { stop(); wg.Wait() })
	cfg := config.Config{
		SecureCookies: false, SessionTTL: time.Hour, AgentDir: t.TempDir(), VictoriaMetricsURL: vm.srv.URL,
		GrafanaNodeURL: "https://grafana.example/d/node?var-node={hostname}",
	}
	api := New(Deps{
		Config: cfg, Log: log, Pool: pool, Auth: a, Bus: bus, Hub: hub, Proxmox: pve, Jobs: runner, Lifecycle: life,
		Deploy: dep, Version: "test",
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &testEnv{t: t, srv: srv, auth: a, pool: pool, api: api, bus: bus, eval: eval, vm: vm, pve: pve, jobs: runner, life: life, deploy: dep}
}

// fakeVM speelt VictoriaMetrics: het bewaart wat binnenkomt en geeft op elke
// query één vaste lijn terug.
type fakeVM struct {
	srv     *httptest.Server
	mu      sync.Mutex
	lines   []string
	queries []string
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
