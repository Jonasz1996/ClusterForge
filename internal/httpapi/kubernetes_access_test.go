package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type kubeAccessView struct {
	ID           string     `json:"id"`
	User         string     `json:"user"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	Account      string     `json:"account"`
	State        string     `json:"state"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedBy    string     `json:"revoked_by"`
	RevokeReason string     `json:"revoke_reason"`
	RemovedAt    *time.Time `json:"removed_at"`
	RemoveError  string     `json:"remove_error"`
}

type kubeAccessIssued struct {
	Access     kubeAccessView `json:"access"`
	Kubeconfig string         `json:"kubeconfig"`
	Filename   string         `json:"filename"`
	Server     string         `json:"server"`
}

// kubeconfigFile is wat kubectl uit een kubeconfig leest.
type kubeconfigFile struct {
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
			CA     string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token string `yaml:"token"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	CurrentContext string `yaml:"current-context"`
}

// TestKubernetesAccess is de acceptatie van mijlpaal 9 van fase 4: een
// kubeconfig downloaden werkt zoals kubectl hem gebruikt, een viewer kan
// niets wijzigen, en na intrekken weigert het cluster het token. Een
// verlopen kubeconfig, of een van een gebruiker die uitgeschakeld is of
// geen admin meer is, ruimt de sync op.
func TestKubernetesAccess(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	fake := kubefake.New()
	fake.Start()
	t.Cleanup(fake.Close)
	fake.SeedK3s(kubefake.NodeSpec{Name: "leren-server-01", IP: "10.0.40.11", Server: true})
	fake.Add(
		kubefake.Namespace("demo"),
		kubefake.Deployment(kubefake.WorkloadSpec{Namespace: "demo", Name: "web", Image: "nginx:1.29", Replicas: 2, Ready: 2}),
		kubefake.Secret("demo", "api", "", map[string]string{"token": "geheim"}),
	)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.40.10"}, nil)
	var server node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "leren-server-01", "cluster_id": cl.ID, "primary_ip": "10.0.40.11"}, &server)
	startAgentWith(t, e, c, server.ID, "a1000000000000000000000000000091", &addrs{}, func(ag *agent.Agent, root string) {
		if err := fake.WriteKubeconfig(root); err != nil {
			t.Fatal(err)
		}
		ag.KubeletHealthz = fake.KubeletURL()
	})
	id := uuid.MustParse(cl.ID)
	eventually(t, "eerste sync", func() bool { return e.kube.Sync(ctx, id) == nil })
	base := "/api/v1/kubernetes/clusters/" + cl.ID + "/access"

	// issue vraagt een kubeconfig en geeft hem met een client die het
	// token gebruikt zoals kubectl, op het adres van de nep-API.
	issue := func(c *client, body map[string]any) (kubeAccessIssued, func(method, path, body string) (int, string)) {
		t.Helper()
		// Met de hand, om de header te zien: de kubeconfig mag niet in een
		// cache.
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", c.base+base, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", c.csrf)
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out kubeAccessIssued
		if raw, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &out) != nil ||
			resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("kubeconfig %v: %d %s %q", body, resp.StatusCode, raw, resp.Header.Get("Cache-Control"))
		}
		var kc kubeconfigFile
		if err := yaml.Unmarshal([]byte(out.Kubeconfig), &kc); err != nil || len(kc.Clusters) != 1 || len(kc.Users) != 1 {
			t.Fatalf("kubeconfig: %v\n%s", err, out.Kubeconfig)
		}
		ca, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CA)
		if err != nil || string(ca) != string(fake.CA()) {
			t.Fatalf("CA: %v", err)
		}
		return out, kubefake.TokenClient(fake.URL(), ca, kc.Users[0].User.Token)
	}
	errOf := func(c *client, method, path string, body any) (int, string) {
		t.Helper()
		var er apiErr
		s := c.do(method, path, body, &er)
		return s, er.Code
	}

	// Zonder wachtwoord, met een foute naam of geldigheid: niets.
	for _, tc := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"name": "laptop", "role": "admin", "days": 7}, "invalid_credentials"},
		{map[string]any{"name": "laptop", "role": "admin", "days": 7, "password": "fout"}, "invalid_credentials"},
		{map[string]any{"name": " ", "role": "admin", "days": 7, "password": "een-lang-wachtwoord"}, "validation"},
		{map[string]any{"name": "laptop", "role": "admin", "days": 31, "password": "een-lang-wachtwoord"}, "validation"},
		{map[string]any{"name": "laptop", "role": "edit", "days": 7, "password": "een-lang-wachtwoord"}, "validation"},
	} {
		if s, code := errOf(c, "POST", base, tc.body); s != http.StatusBadRequest || code != tc.code {
			t.Fatalf("%v: %d %s", tc.body, s, code)
		}
	}
	if reqs := strings.Join(fake.Requests(), "\n"); strings.Contains(reqs, "serviceaccounts") {
		t.Fatalf("toch naar de API:\n%s", reqs)
	}

	// Een admin-kubeconfig: alles mag, ook Secrets lezen.
	laptop, asAdmin := issue(c, map[string]any{"name": "laptop", "role": "admin", "days": 7, "password": "een-lang-wachtwoord"})
	if laptop.Filename != "leren.kubeconfig" || laptop.Server != "https://10.0.40.10:6443" || laptop.Access.State != "active" ||
		laptop.Access.Role != "admin" || laptop.Access.User != "admin" || !regexp.MustCompile(`^cf-admin-[0-9a-f]{6}$`).MatchString(laptop.Access.Account) {
		t.Fatalf("uitgegeven: %+v", laptop)
	}
	if d := time.Until(laptop.Access.ExpiresAt); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour+time.Minute {
		t.Fatalf("verloopt: %s", laptop.Access.ExpiresAt)
	}
	for _, want := range []string{"server: \"https://10.0.40.10:6443\"", "current-context: \"leren\"", "user: \"admin@leren\"", "namespace: default"} {
		if !strings.Contains(laptop.Kubeconfig, want) {
			t.Fatalf("geen %s in\n%s", want, laptop.Kubeconfig)
		}
	}
	if s, _ := asAdmin("GET", "/api/v1/namespaces/demo/secrets/api", ""); s != http.StatusOK {
		t.Fatalf("admin leest een Secret: %d", s)
	}
	if s, body := asAdmin("PATCH", "/apis/apps/v1/namespaces/demo/deployments/web", `{"spec":{"replicas":3}}`); s != http.StatusOK {
		t.Fatalf("admin schaalt: %d %s", s, body)
	}
	// Het token staat nergens bij ClusterForge, ook niet in het logboek.
	token := strings.TrimSuffix(strings.SplitN(strings.SplitN(laptop.Kubeconfig, "token: \"", 2)[1], "\"", 2)[0], "\n")
	var dump string
	if err := e.pool.QueryRow(ctx, `SELECT coalesce((SELECT string_agg(k::text, ' ') FROM kube_access k), '') ||
		coalesce((SELECT string_agg(a.payload::text, ' ') FROM events a), '')`).Scan(&dump); err != nil || token == "" || strings.Contains(dump, token) {
		t.Fatalf("token bewaard: %v %q", err, token)
	}
	kubeLog := func(action string) []auditEntry {
		var out []auditEntry
		for _, it := range c.audit("category=kubernetes&cluster=" + cl.ID).Items {
			if it.Action == action {
				out = append(out, it)
			}
		}
		return out
	}
	issued := kubeLog("kube.access_issued")
	if len(issued) != 1 || issued[0].Summary != "Kubeconfig laptop voor admin uitgegeven: beheerder, geldig tot "+laptop.Access.ExpiresAt.Local().Format("2-1-2006") {
		t.Fatalf("logboek: %+v", issued)
	}

	// Een viewer krijgt alleen view, en kan daarmee lezen maar niets
	// wijzigen en geen Secrets zien.
	e.createUser("lisa", "een-lang-wachtwoord", store.UserRoleViewer)
	lisa := e.client()
	if s, _, _ := lisa.login("lisa", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login lisa: %d", s)
	}
	if s, code := errOf(lisa, "POST", base, map[string]any{"name": "pc", "role": "admin", "days": 7, "password": "een-lang-wachtwoord"}); s != http.StatusForbidden ||
		code != "forbidden" {
		t.Fatalf("viewer vraagt admin: %d %s", s, code)
	}
	pc, asViewer := issue(lisa, map[string]any{"name": "pc", "role": "view", "days": 1, "password": "een-lang-wachtwoord"})
	if pc.Access.Role != "view" || pc.Access.User != "lisa" {
		t.Fatalf("viewer: %+v", pc.Access)
	}
	if s, _ := asViewer("GET", "/apis/apps/v1/namespaces/demo/deployments", ""); s != http.StatusOK {
		t.Fatalf("viewer leest: %d", s)
	}
	for _, r := range [][3]string{
		{"PATCH", "/apis/apps/v1/namespaces/demo/deployments/web", `{"spec":{"replicas":1}}`},
		{"DELETE", "/apis/apps/v1/namespaces/demo/deployments/web", ""},
		{"GET", "/api/v1/namespaces/demo/secrets/api", ""},
	} {
		if s, _ := asViewer(r[0], r[1], r[2]); s != http.StatusForbidden {
			t.Fatalf("viewer %s %s: %d", r[0], r[1], s)
		}
	}
	var mine, all list[kubeAccessView]
	lisa.do("GET", base, nil, &mine)
	c.do("GET", base, nil, &all)
	if len(mine.Items) != 1 || mine.Items[0].Name != "pc" || len(all.Items) != 2 || all.Items[0].Name != "pc" {
		t.Fatalf("lijsten: %+v / %+v", mine, all)
	}
	if s, _ := errOf(lisa, "POST", base+"/"+laptop.Access.ID+"/revoke", nil); s != http.StatusForbidden {
		t.Fatalf("viewer trekt andermans in: %d", s)
	}

	// Intrekken: het ServiceAccount is weg, en het token werkt meteen niet
	// meer; dat van de viewer wel.
	var revoked kubeAccessView
	if s := c.do("POST", base+"/"+laptop.Access.ID+"/revoke", nil, &revoked); s != http.StatusOK || revoked.State != "revoked" ||
		revoked.RemovedAt == nil || revoked.RevokedBy != "admin" {
		t.Fatalf("intrekken: %d %+v", s, revoked)
	}
	if s, _ := asAdmin("GET", "/api/v1/namespaces", ""); s != http.StatusUnauthorized {
		t.Fatalf("na intrekken: %d", s)
	}
	if s, _ := asViewer("GET", "/api/v1/namespaces", ""); s != http.StatusOK {
		t.Fatalf("de viewer na intrekken van de andere: %d", s)
	}
	if out := kubeLog("kube.access_revoked"); len(out) != 1 || out[0].Summary != "Kubeconfig laptop van admin ingetrokken (beheerder)" {
		t.Fatalf("logboek intrekken: %+v", out)
	}

	// Een uitgeschakelde gebruiker, een admin die viewer werd, en een
	// verlopen kubeconfig: de sync haalt de ServiceAccounts weg.
	e.createUser("piet", "een-lang-wachtwoord", store.UserRoleAdmin)
	piet := e.client()
	if s, _, _ := piet.login("piet", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login piet: %d", s)
	}
	_, asPiet := issue(piet, map[string]any{"name": "werk", "role": "admin", "days": 30, "password": "een-lang-wachtwoord"})
	kort, asKort := issue(c, map[string]any{"name": "kort", "role": "view", "days": 1, "password": "een-lang-wachtwoord"})
	if _, err := e.pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE username = 'lisa'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET role = 'viewer' WHERE username = 'piet'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE kube_access SET expires_at = now() - interval '1 minute' WHERE id = $1`, kort.Access.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.kube.Sync(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, as := range []func(string, string, string) (int, string){asViewer, asPiet, asKort} {
		if s, _ := as("GET", "/api/v1/namespaces", ""); s != http.StatusUnauthorized {
			t.Fatalf("na de sync: %d", s)
		}
	}
	c.do("GET", base, nil, &all)
	states := map[string]string{}
	for _, a := range all.Items {
		states[a.Name] = a.State + " " + a.RevokeReason
		if a.RemovedAt == nil {
			t.Fatalf("niet opgeruimd: %+v", a)
		}
	}
	if states["pc"] != "revoked de gebruiker is uitgeschakeld" || states["werk"] != "revoked de gebruiker is geen admin meer" ||
		states["kort"] != "expired " || states["laptop"] != "revoked " {
		t.Fatalf("toestanden: %v", states)
	}
	if out := kubeLog("kube.access_revoked"); len(out) != 3 {
		t.Fatalf("logboek na de sync: %+v", out)
	}

	// Op prod vraagt een admin-kubeconfig tweestapsverificatie en de slug;
	// een kubeconfig die alleen leest niet.
	if s := c.do("PATCH", "/api/v1/clusters/"+cl.ID, map[string]any{"environment": "prod"}, nil); s != http.StatusOK {
		t.Fatalf("naar prod: %d", s)
	}
	admin := map[string]any{"name": "prod", "role": "admin", "days": 1, "password": "een-lang-wachtwoord"}
	if s, code := errOf(c, "POST", base, admin); s != http.StatusForbidden || code != "totp_required" {
		t.Fatalf("prod zonder tweestapsverificatie: %d %s", s, code)
	}
	issue(c, map[string]any{"name": "lezen", "role": "view", "days": 1, "password": "een-lang-wachtwoord"})
	var setup struct {
		Secret string `json:"secret"`
	}
	c.do("POST", "/api/v1/auth/totp/setup", nil, &setup)
	now := time.Now()
	code, _ := totp.GenerateCode(setup.Secret, now.Add(-30*time.Second))
	if s := c.do("POST", "/api/v1/auth/totp/enable", map[string]string{"code": code}, nil); s != http.StatusNoContent {
		t.Fatalf("totp aan: %d", s)
	}
	code, _ = totp.GenerateCode(setup.Secret, now)
	if s, code := errOf(c, "POST", base, map[string]any{"name": "prod", "role": "admin", "days": 1, "totp_code": code}); s != http.StatusConflict ||
		code != "needs_confirmation" {
		t.Fatalf("prod zonder slug: %d %s", s, code)
	}
	prod, _ := issue(c, map[string]any{"name": "prod", "role": "admin", "days": 1, "totp_code": code, "confirm": "leren"})
	if prod.Access.State != "active" {
		t.Fatalf("prod: %+v", prod.Access)
	}

	// Antwoordt de API niet, dan is de kubeconfig toch ingetrokken; de
	// sync haalt het ServiceAccount later weg.
	fake.Close()
	var later kubeAccessView
	if s := c.do("POST", base+"/"+prod.Access.ID+"/revoke", nil, &later); s != http.StatusOK || later.State != "revoking" || later.RemoveError == "" {
		t.Fatalf("intrekken zonder API: %d %+v", s, later)
	}
}

// Zonder agent op een server komt er geen kubeconfig, en ook niets in
// kube_access.
func TestKubernetesAccessNoAgent(t *testing.T) {
	e, c := adminClient(t)
	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "leren", "name": "Leren", "type": "kubernetes", "environment": "lab"}, &cl)
	var er apiErr
	if s := c.do("POST", "/api/v1/kubernetes/clusters/"+cl.ID+"/access", map[string]any{"name": "laptop", "role": "admin", "days": 7,
		"password": "een-lang-wachtwoord"}, &er); s != http.StatusServiceUnavailable || !strings.Contains(er.Message, "geen server-node") {
		t.Fatalf("zonder agent: %d %+v", s, er)
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM kube_access`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("kube_access na een mislukte uitgifte: %d %v", n, err)
	}
}
