package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func yes() *bool { b := true; return &b }

func TestCompare(t *testing.T) {
	key := []byte("sleutel")
	conf := "server {}\n"
	confSum := "a1d0c6e83f027327d8461063f4ac58a6" // geen echte hash: anders dan de inhoud
	steps := []templates.Step{
		{Step: protocol.Step{Package: &protocol.PackageStep{Names: []string{"nginx", "curl"}}}},
		{Step: protocol.Step{Package: &protocol.PackageStep{Names: []string{"telnet"}, State: "absent"}}},
		{Step: protocol.Step{File: &protocol.FileStep{Path: "/etc/nginx/site.conf", Content: conf, Mode: "0640", Owner: "root", Group: "www-data"}}},
		{Step: protocol.Step{Directory: &protocol.DirectoryStep{Path: "/srv/www"}}},
		{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "nginx", Enabled: yes(), State: "started"}}},
		{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "cron"}}},
		{Step: protocol.Step{User: &protocol.UserStep{Name: "deploy"}}},
		{Step: protocol.Step{Command: &protocol.CommandStep{Run: "make", Creates: "/srv/www/out"}}},
		{Step: protocol.Step{Command: &protocol.CommandStep{Run: "a2enmod", Unless: "test -e /x"}}},
	}
	if req := Request(steps); len(req) != 8 || req[7].Creates != "/srv/www/out" {
		t.Fatalf("request: %+v", req)
	}
	mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	obs := []protocol.Observation{
		{Packages: []protocol.PackageState{{Name: "nginx", Installed: true, Version: "1.26"}, {Name: "curl"}}},
		{Packages: []protocol.PackageState{{Name: "telnet", Installed: true, Version: "0.17"}}},
		{Path: &protocol.PathState{Exists: true, Type: "file", Size: 1234, Mode: "0644", Owner: "root", Group: "root", SHA256: confSum, ModTime: mtime}},
		{Path: &protocol.PathState{Exists: true, Type: "file", Mode: "0755"}},
		{Service: &protocol.UnitState{Loaded: true, Enabled: false, Active: true}},
		{Service: &protocol.UnitState{Loaded: false}},
		{UserExists: new(bool)},
		{Path: &protocol.PathState{Exists: false}},
	}
	aligned, err := Align(steps, obs)
	if err != nil {
		t.Fatal(err)
	}
	res := Compare(key, steps, aligned)
	want := []string{
		"package:curl:installed",
		"package:telnet:installed",
		"file:/etc/nginx/site.conf:content",
		"file:/etc/nginx/site.conf:mode",
		"file:/etc/nginx/site.conf:group",
		"directory:/srv/www:type",
		"service:nginx:enabled",
		"service:cron:loaded",
		"user:deploy:exists",
		"command:/srv/www/out:creates",
	}
	if got := Keys(res.Findings); !slices.Equal(got, want) {
		t.Fatalf("sleutels:\n%q\nwil\n%q", got, want)
	}
	byKey := map[string]Finding{}
	for _, f := range res.Findings {
		byKey[f.Key] = f
		if len(f.Fingerprint) != 64 || strings.Contains(f.Fingerprint, confSum) {
			t.Errorf("vingerafdruk van %s: %q", f.Key, f.Fingerprint)
		}
	}
	if f := byKey["file:/etc/nginx/site.conf:content"]; f.Detail != "1.234 bytes in plaats van 10" || f.ModTime == nil || !f.ModTime.Equal(mtime) ||
		strings.Contains(f.Expected+f.Actual+f.Detail, confSum) {
		t.Errorf("inhoud: %+v", f)
	}
	if f := byKey["file:/etc/nginx/site.conf:mode"]; f.Expected != "0640" || f.Actual != "0644" {
		t.Errorf("rechten: %+v", f)
	}
	if f := byKey["package:telnet:installed"]; f.Actual != "geïnstalleerd (0.17)" {
		t.Errorf("telnet: %+v", f)
	}
	if len(res.Unchecked) != 1 || res.Unchecked[0].Step != "command:a2enmod" || !strings.Contains(res.Unchecked[0].Reason, "unless") {
		t.Fatalf("niet gecontroleerd: %+v", res.Unchecked)
	}

	// Een andere waarneming met dezelfde sleutel geeft een andere
	// vingerafdruk; dezelfde waarneming dezelfde.
	obs[2].Path.SHA256 = "ff" + confSum[2:]
	again := Compare(key, steps, mustAlign(t, steps, obs))
	if again.Findings[2].Fingerprint == res.Findings[2].Fingerprint || again.Findings[3].Fingerprint != res.Findings[3].Fingerprint {
		t.Fatal("vingerafdrukken")
	}
}

func mustAlign(t *testing.T, steps []templates.Step, obs []protocol.Observation) []protocol.Observation {
	t.Helper()
	out, err := Align(steps, obs)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompareEdges(t *testing.T) {
	key := []byte("k")
	file := templates.Step{Step: protocol.Step{File: &protocol.FileStep{Path: "/etc/a", Content: "abc"}}}
	service := templates.Step{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "x", State: "stopped"}}}
	for name, tc := range map[string]struct {
		step templates.Step
		obs  protocol.Observation
		keys []string
		// unchecked is de reden van een stap die niet te bekijken was.
		unchecked string
	}{
		"ontbreekt":          {file, protocol.Observation{Path: &protocol.PathState{}}, []string{"file:/etc/a:exists"}, ""},
		"map i.p.v. bestand": {file, protocol.Observation{Path: &protocol.PathState{Exists: true, Type: "directory", Mode: "0755"}}, []string{"file:/etc/a:type"}, ""},
		"standaardrechten":   {file, protocol.Observation{Path: &protocol.PathState{Exists: true, Type: "file", Mode: "0644", Size: 3, SHA256: sha("abc")}}, []string{}, ""},
		"te groot, even groot": {file, protocol.Observation{Path: &protocol.PathState{Exists: true, Type: "file", Mode: "0644", Size: 3}}, []string{},
			"inhoud niet vergeleken: te groot om te hashen"},
		"fout bij de agent": {file, protocol.Observation{Error: "permission denied"}, []string{}, "niet te bekijken: permission denied"},
		"geen dpkg":         {file, protocol.Observation{Skipped: "dpkg-query ontbreekt"}, []string{}, "dpkg-query ontbreekt"},
		"gestopt en uit":    {service, protocol.Observation{Service: &protocol.UnitState{Loaded: true}}, []string{}, ""},
		"gestopt maar draait": {service, protocol.Observation{Service: &protocol.UnitState{Loaded: true, Active: true}},
			[]string{"service:x:active"}, ""},
	} {
		res := Compare(key, []templates.Step{tc.step}, []protocol.Observation{tc.obs})
		if got := Keys(res.Findings); !slices.Equal(got, tc.keys) {
			t.Errorf("%s: %q", name, got)
		}
		reason := ""
		if len(res.Unchecked) > 0 {
			reason = res.Unchecked[0].Reason
		}
		if reason != tc.unchecked {
			t.Errorf("%s: niet gecontroleerd %q", name, reason)
		}
	}
	if _, err := Align([]templates.Step{file}, nil); err == nil {
		t.Fatal("te weinig observaties")
	}
	if _, err := Align([]templates.Step{file}, make([]protocol.Observation, 2)); err == nil {
		t.Fatal("te veel observaties")
	}
}

func fk(key string) Finding { return Finding{Key: key} }

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestConfirmed(t *testing.T) {
	prev := func(keys ...string) store.DriftCheck {
		fs := []Finding{}
		for _, k := range keys {
			fs = append(fs, fk(k))
		}
		b, _ := json.Marshal(fs)
		return store.DriftCheck{Findings: b}
	}
	for name, tc := range map[string]struct {
		prev          store.DriftCheck
		first, second []Finding
		want          []string
	}{
		// Een afwijking telt pas als ze beide keren gezien is.
		"nieuw, twee keer":       {prev(), []Finding{fk("a")}, []Finding{fk("a")}, []string{"a"}},
		"nieuw, één keer":        {prev(), []Finding{fk("a")}, []Finding{}, []string{}},
		"nieuw, alleen tweede":   {prev(), []Finding{}, []Finding{fk("a")}, []string{}},
		"bekend blijft":          {prev("a"), []Finding{fk("a"), fk("b")}, []Finding{fk("a")}, []string{"a"}},
		"bekend, één keer weg":   {prev("a"), []Finding{}, []Finding{fk("a")}, []string{"a"}},
		"bekend, tweede weg":     {prev("a"), []Finding{fk("a")}, []Finding{}, []string{"a"}},
		"bekend, beide keer weg": {prev("a"), []Finding{}, []Finding{}, []string{}},
	} {
		if got := Keys(confirmed(tc.prev, tc.first, tc.second)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestKeysFingerprint(t *testing.T) {
	if keysFingerprint(nil) != "" {
		t.Fatal("zonder afwijkingen hoort de vingerafdruk leeg te zijn")
	}
	a, b := keysFingerprint([]string{"x", "y"}), keysFingerprint([]string{"y", "x"})
	if a != b || len(a) != 64 || a == keysFingerprint([]string{"x"}) {
		t.Fatal("vingerafdruk van sleutels")
	}
}

func TestSkip(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-5 * time.Second)
	old := now.Add(-10 * time.Minute)
	ok := store.ListDriftNodesRow{Lifecycle: store.NodeLifecycleActive, HasAgent: true, AgentProtocol: protocol.Version, HeartbeatAt: &fresh}
	with := func(change func(*store.ListDriftNodesRow)) store.ListDriftNodesRow {
		n := ok
		change(&n)
		return n
	}
	for name, tc := range map[string]struct {
		n    store.ListDriftNodesRow
		busy bool
		want string
	}{
		"klaar":        {ok, false, ""},
		"onderhoud":    {with(func(n *store.ListDriftNodesRow) { n.Lifecycle = store.NodeLifecycleMaintenance }), false, "node is niet actief"},
		"geen agent":   {with(func(n *store.ListDriftNodesRow) { n.HasAgent = false }), false, "geen agent"},
		"protocol 3":   {with(func(n *store.ListDriftNodesRow) { n.AgentProtocol = 3 }), false, AgentTooOld},
		"stil":         {with(func(n *store.ListDriftNodesRow) { n.HeartbeatAt = &old }), false, "agent niet verbonden"},
		"nooit gezien": {with(func(n *store.ListDriftNodesRow) { n.HeartbeatAt = nil }), false, "agent niet verbonden"},
		"taak bezig":   {ok, true, "taak bezig"},
	} {
		if got := Skip(tc.n, tc.busy, now); got != tc.want {
			t.Errorf("%s: %q", name, got)
		}
	}
}
