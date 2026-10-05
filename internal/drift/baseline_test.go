package drift

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestBaselineItemsValidate(t *testing.T) {
	got, err := BaselineItems{
		Packages: []string{" nginx ", "nginx", "", "libssl3t64"},
		Services: []string{"nginx.service", "nginx", "getty@tty1"},
		Files:    []string{"/etc/nginx/nginx.conf", "/etc/nginx/nginx.conf "},
	}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	want := BaselineItems{Packages: []string{"nginx", "libssl3t64"}, Services: []string{"nginx", "getty@tty1"}, Files: []string{"/etc/nginx/nginx.conf"}}
	if !slices.Equal(got.Packages, want.Packages) || !slices.Equal(got.Services, want.Services) || !slices.Equal(got.Files, want.Files) {
		t.Fatalf("schoongemaakt: %+v", got)
	}

	many := make([]string, maxItems+1)
	for i := range many {
		many[i] = "p" + strconv.Itoa(i)
	}
	for _, tc := range []struct {
		in    BaselineItems
		field string
	}{
		{BaselineItems{}, "packages"},
		{BaselineItems{Packages: []string{" "}}, "packages"},
		{BaselineItems{Packages: []string{"Nginx"}}, "packages"},
		{BaselineItems{Packages: []string{"nginx; rm -rf /"}}, "packages"},
		{BaselineItems{Packages: many}, "packages"},
		{BaselineItems{Services: []string{"-nginx"}}, "services"},
		{BaselineItems{Services: []string{"a b"}}, "services"},
		{BaselineItems{Files: []string{"etc/x"}}, "files"},
		{BaselineItems{Files: []string{"/etc/../x"}}, "files"},
		{BaselineItems{Files: []string{"/etc/x/"}}, "files"},
		{BaselineItems{Files: []string{"/"}}, "files"},
		{BaselineItems{Files: []string{"/etc/a\nb"}}, "files"},
		{BaselineItems{Files: []string{"/" + strings.Repeat("a", maxPathLen)}}, "files"},
	} {
		_, err := tc.in.Validate()
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field || fe.Message == "" {
			t.Errorf("%+v: %v", tc.in, err)
		}
	}
}

func TestBaselineCapture(t *testing.T) {
	key := []byte("sleutel")
	items := BaselineItems{
		Packages: []string{"nginx", "keepalived"},
		Services: []string{"nginx", "weg"},
		Files:    []string{"/etc/nginx/nginx.conf", "/etc/nginx", "/etc/ontbreekt", "/var/log/groot.log", "/dev/null"},
	}
	req := items.CaptureRequest()
	if len(req) != 8 || !slices.Equal(req[0].Packages, items.Packages) || req[1].Service != "nginx" || req[3].File != "/etc/nginx/nginx.conf" {
		t.Fatalf("request: %+v", req)
	}
	sum := sha("worker_processes auto;\n")
	obs := []protocol.Observation{
		{Packages: []protocol.PackageState{{Name: "nginx", Installed: true, Version: "1.26.3-3"}, {Name: "keepalived"}}},
		{Service: &protocol.UnitState{Loaded: true, Enabled: true, Active: true}},
		{Service: &protocol.UnitState{}},
		{Path: &protocol.PathState{Exists: true, Type: "file", Size: 23, Mode: "0644", Owner: "root", Group: "root", SHA256: sum}},
		{Path: &protocol.PathState{Exists: true, Type: "directory", Mode: "0755", Owner: "root", Group: "root"}},
		{Path: &protocol.PathState{}},
		{Path: &protocol.PathState{Exists: true, Type: "file", Size: 1 << 30, Mode: "0640", Owner: "root", Group: "adm"}},
		{Path: &protocol.PathState{Exists: true, Type: "other"}},
	}
	expect, notes, err := items.Capture(key, obs)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range expect {
		kinds = append(kinds, e.Kind+":"+e.Name)
	}
	if !slices.Equal(kinds, []string{"package:nginx", "service:nginx", "file:/etc/nginx/nginx.conf", "directory:/etc/nginx", "file:/var/log/groot.log"}) {
		t.Fatalf("vastgelegd: %q", kinds)
	}
	conf := expect[2]
	if conf.ContentHMAC == "" || conf.ContentHMAC == sum || conf.Size != 23 || conf.Mode != "0644" || expect[4].ContentHMAC != "" {
		t.Fatalf("bestanden: %+v", expect)
	}
	if expect[0].Version != "1.26.3-3" || !*expect[1].Enabled || !*expect[1].Active {
		t.Fatalf("pakket en service: %+v", expect[:2])
	}
	wantNotes := []string{
		"pakket keepalived is niet geïnstalleerd", "service weg bestaat niet", "/etc/ontbreekt bestaat niet",
		"/var/log/groot.log is te groot om de inhoud te vergelijken; alleen rechten en eigenaar tellen", "/dev/null is geen bestand of map",
	}
	if !slices.Equal(notes, wantNotes) {
		t.Fatalf("opmerkingen: %q", notes)
	}
	raw, _ := json.Marshal(expect)
	if strings.Contains(string(raw), sum) {
		t.Fatalf("de sha256 staat in de baseline: %s", raw)
	}

	// Zonder sleutel geen bestanden: hun inhoud is dan niet veilig te
	// vergelijken.
	expect, notes, err = items.Capture(nil, obs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range expect {
		if e.Kind == "file" || e.Kind == "directory" {
			t.Fatalf("zonder sleutel vastgelegd: %+v", e)
		}
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "zonder CF_MASTER_KEY") }) {
		t.Fatalf("opmerkingen zonder sleutel: %q", notes)
	}

	// Een agent die iets niet kan bekijken.
	_, notes, _ = BaselineItems{Packages: []string{"nginx"}, Services: []string{"nginx"}}.Capture(key, []protocol.Observation{
		{Skipped: "deze node heeft geen dpkg"}, {Error: "systemctl ontbreekt"},
	})
	if !slices.Equal(notes, []string{"pakketten niet vastgelegd: deze node heeft geen dpkg", "service nginx niet te bekijken: systemctl ontbreekt"}) {
		t.Fatalf("niet te bekijken: %q", notes)
	}
	if _, _, err := items.Capture(key, obs[:3]); err == nil {
		t.Fatal("te weinig observaties geaccepteerd")
	}
}

func TestCompareBaseline(t *testing.T) {
	key := []byte("sleutel")
	sum := sha("worker_processes auto;\n")
	n := BaseNode{NodeID: uuid.New(), Hostname: "web01", CapturedAt: time.Now(), Expect: []Expect{
		{Kind: "package", Name: "nginx", Version: "1.26.3-3"},
		{Kind: "service", Name: "nginx", Enabled: yes(), Active: yes()},
		{Kind: "file", Name: "/etc/nginx/nginx.conf", Mode: "0644", Owner: "root", Group: "root", Size: 23, ContentHMAC: hmacOf(key, sum)},
		{Kind: "directory", Name: "/etc/nginx", Mode: "0755", Owner: "root", Group: "root"},
		{Kind: "onbekend", Name: "x"},
	}}
	steps, _ := n.Steps()
	if req := Request(steps); len(req) != 4 || req[0].Packages[0] != "nginx" || req[2].File != "/etc/nginx/nginx.conf" {
		t.Fatalf("request: %+v", req)
	}
	same := func() []protocol.Observation {
		return []protocol.Observation{
			// Een nieuwere versie is geen drift: updates horen bij beheer.
			{Packages: []protocol.PackageState{{Name: "nginx", Installed: true, Version: "1.26.3-4"}}},
			{Service: &protocol.UnitState{Loaded: true, Enabled: true, Active: true}},
			{Path: &protocol.PathState{Exists: true, Type: "file", Size: 23, Mode: "0644", Owner: "root", Group: "root", SHA256: sum}},
			{Path: &protocol.PathState{Exists: true, Type: "directory", Mode: "0755", Owner: "root", Group: "root"}},
		}
	}
	if res := CompareBaseline(key, n, same()); len(res.Findings) != 0 || len(res.Unchecked) != 0 {
		t.Fatalf("ongewijzigd: %+v", res)
	}

	obs := same()
	obs[1].Service.Active = false
	obs[2].Path.SHA256, obs[2].Path.Size, obs[2].Path.Mode = sha("anders\n"), 7, "0600"
	obs[3].Path.Owner = "www-data"
	res := CompareBaseline(key, n, obs)
	var keys []string
	for _, f := range res.Findings {
		keys = append(keys, f.Key)
	}
	slices.Sort(keys)
	want := []string{"directory:/etc/nginx:owner", "file:/etc/nginx/nginx.conf:content", "file:/etc/nginx/nginx.conf:mode", "service:nginx:active"}
	if !slices.Equal(keys, want) {
		t.Fatalf("afwijkingen: %q", keys)
	}
	for _, f := range res.Findings {
		if f.Aspect == "content" && (f.Expected != "inhoud zoals vastgelegd" || f.Detail != "7 bytes in plaats van 23") {
			t.Fatalf("inhoud: %+v", f)
		}
	}

	// Met een andere sleutel klopt de HMAC niet meer.
	if res := CompareBaseline([]byte("andere"), n, same()); len(res.Findings) != 1 || res.Findings[0].Aspect != "content" {
		t.Fatalf("andere sleutel: %+v", res)
	}

	// Zonder sleutel wordt de inhoud niet vergeleken in plaats van overal
	// drift te melden.
	if res := CompareBaseline(nil, n, same()); len(res.Findings) != 0 || len(res.Unchecked) != 1 ||
		res.Unchecked[0].Reason != "inhoud niet vergeleken: CF_MASTER_KEY ontbreekt" {
		t.Fatalf("zonder sleutel: %+v", res)
	}

	// Een pakket dat weg is.
	obs = same()
	obs[0].Packages[0] = protocol.PackageState{Name: "nginx"}
	if res := CompareBaseline(key, n, obs); len(res.Findings) != 1 || res.Findings[0].Key != "package:nginx:installed" {
		t.Fatalf("pakket weg: %+v", res)
	}
}

func TestParseBaseline(t *testing.T) {
	if b, err := ParseBaseline([]byte(`{"template":{"name":"x","version":"1"}}`)); b != nil || err != nil {
		t.Fatalf("templatespec: %v %v", b, err)
	}
	if b, err := ParseBaseline([]byte(`{}`)); b != nil || err != nil {
		t.Fatalf("lege spec: %v %v", b, err)
	}
	if _, err := ParseBaseline([]byte(`{`)); err == nil {
		t.Fatal("ongeldige spec geaccepteerd")
	}
	id := uuid.New()
	raw, _ := json.Marshal(Baseline{Kind: "baseline", Nodes: []BaseNode{{NodeID: id, Hostname: "web01"}}})
	b, err := ParseBaseline(raw)
	if err != nil || b == nil {
		t.Fatalf("baseline: %v %v", b, err)
	}
	if n, ok := b.Node(id); !ok || n.Hostname != "web01" {
		t.Fatalf("node: %+v", n)
	}
	if _, ok := b.Node(uuid.New()); ok {
		t.Fatal("onbekende node gevonden")
	}
}

func TestIgnore(t *testing.T) {
	f := Finding{Key: "file:/etc/nginx/nginx.conf:content", Step: "file:/etc/nginx/nginx.conf"}
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"file:/etc/nginx/nginx.conf", true},
		{"file:/etc/nginx/*", true},
		{"file:*", true},
		{"*", true},
		{"file:/etc/nginx/nginx.conf:content", false},
		{"file:/etc/nginx", false},
		{"service:*", false},
	} {
		if got := (Ignore{Key: tc.key}).Matches(f); got != tc.want {
			t.Errorf("%s: %v", tc.key, got)
		}
	}

	now := time.Now()
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	if err := ValidateIgnore("file:/etc/nginx/*", "nginx wordt omgebouwd", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIgnore("*", "onderhoud", &later, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key, reason string
		expires     *time.Time
		field       string
	}{
		{"", "reden", nil, "key"},
		{"nginx.conf", "reden", nil, "key"},
		{"file:/etc/*/x", "reden", nil, "key"},
		{"file:**", "reden", nil, "key"},
		{"file:" + strings.Repeat("a", 500), "reden", nil, "key"},
		{"*", "onderhoud", nil, "expires_at"},
		{"service:nginx", "ab", nil, "reason"},
		{"service:nginx", "   ab   ", nil, "reason"},
		{"service:nginx", strings.Repeat("é", 501), nil, "reason"},
		{"service:nginx", "reden", &earlier, "expires_at"},
	} {
		err := ValidateIgnore(tc.key, tc.reason, tc.expires, now)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%q %q: %v", tc.key, tc.reason, err)
		}
	}

	id := uuid.New()
	findings := []Finding{
		{Key: "file:/etc/nginx/nginx.conf:content", Step: "file:/etc/nginx/nginx.conf"},
		{Key: "service:nginx:active", Step: "service:nginx", Ignored: true},
	}
	keys := markIgnored(findings, []Ignore{{ID: id, Key: "file:/etc/nginx/*"}})
	if !slices.Equal(keys, []string{"service:nginx:active"}) || !findings[0].Ignored || *findings[0].IgnoreID != id ||
		findings[1].Ignored || findings[1].IgnoreID != nil {
		t.Fatalf("markIgnored: %q %+v", keys, findings)
	}
	if got := activeKeys(findings); !slices.Equal(got, keys) {
		t.Fatalf("activeKeys: %q", got)
	}
}

func hmacOf(key []byte, sum string) string { return secrets.Fingerprint(key, []byte(sum)) }
