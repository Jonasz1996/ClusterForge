package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// snapshot legt inhoud, rechten, eigenaar en mtime van alles onder root
// vast, om te zien dat inspecteren niets verandert.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		uid, gid, _ := fileOwner(fi)
		v := fi.Mode().String() + " " + fi.ModTime().String() + " " + strings.Repeat("x", uid) + "/" + strings.Repeat("y", gid)
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(b)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// recordAll legt elk commando vast dat de agent uitvoert, ook de vragen.
func recordAll(a *Agent, h *agenttest.Host) func() []string {
	var mu sync.Mutex
	var calls []string
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		mu.Unlock()
		return h.Exec(ctx, name, args...)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func inspectCmd(t *testing.T, a *Agent, steps []protocol.InspectStep) protocol.Result {
	t.Helper()
	body, _ := json.Marshal(protocol.Command{ID: "i1", Action: protocol.CmdInspect, Deadline: time.Now().Add(time.Minute), Inspect: steps})
	env, _ := json.Marshal(protocol.Envelope{V: protocol.Version, ID: "i1", Type: protocol.TypeCommand, Body: body})
	return a.handleCommand(env)
}

func TestInspectOnlyReads(t *testing.T) {
	a, h := applyAgent(t)
	res := a.apply(context.Background(), []protocol.Step{
		{Package: &protocol.PackageStep{Names: []string{"nginx"}}},
		{File: &protocol.FileStep{Path: "/etc/nginx/site.conf", Content: "server {}\n", Mode: "0640"}},
		{Directory: &protocol.DirectoryStep{Path: "/srv/data", Mode: "0750"}},
		{User: &protocol.UserStep{Name: "deploy", System: true}},
	})
	if !res.OK {
		t.Fatalf("voorbereiden: %+v", res)
	}
	h.SetVersion("nginx", "1.26.3-3")
	if err := os.Symlink("site.conf", filepath.Join(a.Root, "etc/nginx/link.conf")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, a.Root)
	changes := len(h.Calls())
	all := recordAll(a, h)

	res = inspectCmd(t, a, []protocol.InspectStep{
		{Packages: []string{"nginx", "keepalived"}},
		{File: "/etc/nginx/site.conf"},
		{File: "/etc/nginx/link.conf"},
		{File: "/etc/nginx/ontbreekt.conf"},
		{Directory: "/srv/data"},
		{Service: "nginx"},
		{Service: "bestaat-niet"},
		{User: "deploy"},
		{User: "niemand"},
		{Creates: "/srv/data"},
		{File: "../etc/passwd"},
		{},
	})
	if !res.OK || len(res.Observations) != 12 {
		t.Fatalf("inspect: %+v", res)
	}
	o := res.Observations
	if want := []protocol.PackageState{{Name: "nginx", Installed: true, Version: "1.26.3-3"}, {Name: "keepalived"}}; !reflect.DeepEqual(o[0].Packages, want) {
		t.Errorf("pakketten: %+v", o[0].Packages)
	}
	sum := sha256.Sum256([]byte("server {}\n"))
	if p := o[1].Path; p == nil || !p.Exists || p.Type != "file" || p.Size != 10 || p.Mode != "0640" || p.SHA256 != hex.EncodeToString(sum[:]) || p.ModTime.IsZero() || p.Owner == "" {
		t.Errorf("bestand: %+v", p)
	}
	if p := o[2].Path; p == nil || p.Symlink != "site.conf" || p.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("symlink: %+v", p)
	}
	if p := o[3].Path; p == nil || p.Exists {
		t.Errorf("ontbrekend bestand: %+v", p)
	}
	if p := o[4].Path; p == nil || p.Type != "directory" || p.Mode != "0750" || p.SHA256 != "" {
		t.Errorf("map: %+v", p)
	}
	if s := o[5].Service; s == nil || !s.Loaded || !s.Enabled || !s.Active {
		t.Errorf("nginx: %+v", s)
	}
	if s := o[6].Service; s == nil || s.Loaded {
		t.Errorf("onbekende service: %+v", s)
	}
	if u := o[7].UserExists; u == nil || !*u {
		t.Errorf("deploy: %v", u)
	}
	if u := o[8].UserExists; u == nil || *u {
		t.Errorf("niemand: %v", u)
	}
	if p := o[9].Path; p == nil || !p.Exists {
		t.Errorf("creates: %+v", p)
	}
	if !strings.Contains(o[10].Error, "absoluut") || o[11].Error == "" {
		t.Errorf("ongeldige stappen: %+v %+v", o[10], o[11])
	}

	// Niets veranderd: geen commando dat iets wijzigt, en alleen de vragen
	// die apply ook stelt.
	if got := h.Calls(); len(got) != changes {
		t.Fatalf("inspect veranderde iets: %v", got[changes:])
	}
	for _, c := range all() {
		if !strings.HasPrefix(c, "dpkg-query -W ") && !strings.HasPrefix(c, "systemctl show ") && !strings.HasPrefix(c, "id -u ") {
			t.Errorf("inspect voerde %q uit", c)
		}
	}
	if after := snapshot(t, a.Root); !reflect.DeepEqual(before, after) {
		t.Fatal("inspect veranderde het bestandssysteem")
	}
}

func TestInspectWithoutDpkg(t *testing.T) {
	a, h := applyAgent(t)
	// os/exec meldt een ontbrekend programma als exec.ErrNotFound.
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "dpkg-query" {
			return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return h.Exec(ctx, name, args...)
	}
	res := a.inspect(context.Background(), []protocol.InspectStep{{Packages: []string{"nginx"}}, {Service: "nginx"}})
	if !res.OK || res.Observations[0].Skipped == "" || res.Observations[1].Service == nil {
		t.Fatalf("zonder dpkg: %+v", res)
	}
}

// TestApplyInspectConsistent past de stappen van elke versie van de
// ingebouwde templates een voor een toe, en eist na elke stap dat inspect en
// Compare over de stappen tot dan niets vinden. Een tweede keer apply
// verandert niets meer. Zo lopen apply en de driftcontrole nooit uit
// elkaar.
func TestApplyInspectConsistent(t *testing.T) {
	key := []byte("sleutel")
	for _, latest := range templates.BuiltinRegistry().All() {
		for _, v := range templates.BuiltinRegistry().Versions(latest.Name) {
			tpl, _ := templates.Get(latest.Name, v)
			for _, role := range tpl.Roles {
				t.Run(tpl.Name+"-"+tpl.Version+"-"+role.Name, func(t *testing.T) {
					consistent(t, key, tpl, role.Name, tpl == latest)
				})
			}
		}
	}
}

func consistent(t *testing.T, key []byte, tpl *templates.Template, role string, latest bool) {
	a, h := applyAgent(t)
	steps := renderBuiltin(t, tpl, role)
	for i, s := range steps {
		if res := a.apply(context.Background(), []protocol.Step{s.Step}); !res.OK {
			t.Fatalf("%s: %+v", s.Title, res)
		}
		if cmp := inspectAndCompare(t, a, key, steps[:i+1]); len(cmp.Findings) != 0 {
			t.Fatalf("afwijkingen direct na stap %d (%s): %+v", i+1, s.Title, cmp.Findings)
		}
	}
	for _, s := range steps {
		if res := a.apply(context.Background(), []protocol.Step{s.Step}); !res.OK || res.Steps[0].Changed {
			t.Fatalf("tweede keer %s: %+v", s.Title, res)
		}
	}
	cmp := inspectAndCompare(t, a, key, steps)
	for _, u := range cmp.Unchecked {
		if !strings.HasPrefix(u.Step, "command:") {
			t.Errorf("niet gecontroleerd: %+v", u)
		}
	}
	if tpl.Name != "keepalived-nginx" || !latest {
		return
	}

	// Drie wijzigingen met de hand geven vier afwijkingen.
	conf := filepath.Join(a.Root, "etc/keepalived/keepalived.conf")
	if err := os.WriteFile(conf, []byte("vrrp_instance VI_1 {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(a.Root, "var/www/html/index.html"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.SetUnit("nginx", agenttest.Unit{})
	cmp = inspectAndCompare(t, a, key, steps)
	want := []string{
		"file:/var/www/html/index.html:mode",
		"service:nginx:enabled",
		"service:nginx:active",
		"file:/etc/keepalived/keepalived.conf:content",
	}
	if got := drift.Keys(cmp.Findings); !reflect.DeepEqual(got, want) {
		t.Fatalf("afwijkingen:\n%q\nwil\n%q", got, want)
	}
	c := cmp.Findings[3]
	if c.ModTime == nil || !strings.HasSuffix(c.Detail, "bytes in plaats van "+thousandsOf(len(steps[3].File.Content))) {
		t.Fatalf("inhoud: %+v", c)
	}
	// Een tweede wijziging van hetzelfde bestand geeft een andere
	// vingerafdruk, met dezelfde sleutel.
	if err := os.WriteFile(conf, []byte("vrrp_instance VI_2 {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	again := inspectAndCompare(t, a, key, steps)
	if again.Findings[3].Key != c.Key || again.Findings[3].Fingerprint == c.Fingerprint {
		t.Fatalf("vingerafdruk: %+v %+v", c, again.Findings[3])
	}
}

func thousandsOf(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

// sampleParams zijn ingevulde parameters per ingebouwde template; de rest
// komt uit de standaardwaarden.
var sampleParams = map[string]map[string]any{
	"keepalived-nginx": {"vip": "10.0.20.100", "vrid": 51},
	"docker":           {},
	"cron":             {"vip": "10.0.20.100", "vrid": 52},
	"generic":          {"vip": "10.0.20.100", "vrid": 53, "image": "ghcr.io/jonas/app:1.4.2", "port": 8080, "container_port": 3000},
}

func renderBuiltin(t *testing.T, tpl *templates.Template, role string) []templates.Step {
	t.Helper()
	sample, ok := sampleParams[tpl.Name]
	if !ok {
		t.Fatalf("zet voorbeeldparameters voor %s in sampleParams", tpl.Name)
	}
	params, err := tpl.Validate(sample)
	if err != nil {
		t.Fatal(err)
	}
	c := templates.Context{
		Params:  params,
		Cluster: templates.ClusterInfo{Name: "Web", Slug: "web", Environment: "prod"},
		Nodes: []templates.NodeInfo{
			{Hostname: "web-01", Role: role, Index: 1, Address: "10.0.20.11", Prefix: 24, Interface: "eth0"},
			{Hostname: "web-02", Role: role, Index: 2, Address: "10.0.20.12", Prefix: 24, Interface: "eth0"},
		},
	}
	c.Node = &c.Nodes[0]
	steps, err := tpl.Steps(c.Node.Role, c)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func inspectAndCompare(t *testing.T, a *Agent, key []byte, steps []templates.Step) drift.Result {
	t.Helper()
	res := a.inspect(context.Background(), drift.Request(steps))
	if !res.OK {
		t.Fatalf("inspect: %+v", res)
	}
	obs, err := drift.Align(steps, res.Observations)
	if err != nil {
		t.Fatal(err)
	}
	return drift.Compare(key, steps, obs)
}
