package dockerhost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestDanger(t *testing.T) {
	cases := []struct {
		calls []protocol.DockerCall
		want  string
	}{
		{[]protocol.DockerCall{{Method: "POST", Path: "/containers/web/restart"}}, ""},
		{[]protocol.DockerCall{{Method: "DELETE", Path: "/containers/web"}}, "web"},
		{[]protocol.DockerCall{{Method: "POST", Path: "/containers/web/kill"}}, "web"},
		{[]protocol.DockerCall{{Method: "DELETE", Path: "/images/ghcr.io/x/y:1"}}, "ghcr.io/x/y:1"},
		{[]protocol.DockerCall{{Method: "DELETE", Path: "/volumes/a"}, {Method: "DELETE", Path: "/volumes/b"}}, "verwijderen"},
		{[]protocol.DockerCall{{Method: "DELETE", Path: "/volumes/a"}, {Method: "POST", Path: "/images/prune"}}, "opruimen"},
	}
	for _, c := range cases {
		if got := Danger(c.calls); got != c.want {
			t.Errorf("%v: %q, wil %q", c.calls, got, c.want)
		}
	}
}

func TestTitle(t *testing.T) {
	cases := map[string]protocol.DockerCall{
		"Web herstarten op d1":               {Method: "POST", Path: "/containers/web/restart"},
		"Nginx:1.29 halen op d1":             {Method: "POST", Path: "/images/create", Query: map[string]string{"fromImage": "nginx", "tag": "1.29"}},
		"Ongebruikte volumes opruimen op d1": {Method: "POST", Path: "/volumes/prune"},
		"Netwerk maken op d1":                {Method: "POST", Path: "/networks/create"},
		"Ghcr.io/x/y:1 verwijderen op d1":    {Method: "DELETE", Path: "/images/ghcr.io/x/y:1"},
		"Front verbinden op d1":              {Method: "POST", Path: "/networks/front/connect"},
	}
	for want, c := range cases {
		if got := Title([]protocol.DockerCall{c}, "d1"); got != want {
			t.Errorf("%s: %q, wil %q", c.Path, got, want)
		}
	}
	if got := Title(make([]protocol.DockerCall, 3), "d1"); got != "3 Docker-acties op d1" {
		t.Errorf("%q", got)
	}
}

func TestMaskEnv(t *testing.T) {
	in := json.RawMessage(`{"Id":"x","Config":{"Env":["PASSWORD=geheim","TZ=Europe/Brussels","LEEG"]}}`)
	out := string(maskEnv("/containers/web/json", in))
	if strings.Contains(out, "geheim") || strings.Contains(out, "Europe") || !strings.Contains(out, `"PASSWORD=•••"`) || !strings.Contains(out, `"LEEG=•••"`) {
		t.Fatalf("%s", out)
	}
	if got := string(maskEnv("/containers/json", in)); got != string(in) {
		t.Fatalf("lijst veranderd: %s", got)
	}
	if got := string(maskEnv("/images/nginx:1/json", in)); strings.Contains(got, "geheim") {
		t.Fatalf("image: %s", got)
	}
}

func TestCount(t *testing.T) {
	var s Summary
	count(&s, []ContainerSummary{
		{ID: "1", Names: []string{"/a"}, State: "running", Status: "Up 1 hour (healthy)"},
		{ID: "2", Names: []string{"/b"}, State: "running", Status: "Up 1 hour (unhealthy)"},
		{ID: "3", Names: []string{"/c"}, State: "restarting", Status: "Restarting (1) 3 seconds ago"},
		{ID: "4", Names: []string{"/d"}, State: "exited", Status: "Exited (0) 2 days ago"},
		{ID: "5", Names: []string{"/e"}, State: "exited", Status: "Exited (137) 2 days ago"},
		{ID: "6", Names: []string{"/f"}, State: "exited", Status: "Exited (1) 1 day ago", Labels: map[string]string{"com.docker.compose.oneoff": "True"}},
		{ID: "7", Names: []string{"/g"}, State: "paused", Status: "Up (Paused)"},
	})
	if s.Containers != 7 || s.Running != 2 || s.Restarting != 1 || s.Stopped != 3 || s.Paused != 1 || s.Unhealthy != 1 {
		t.Fatalf("%+v", s)
	}
	var names []string
	for _, p := range s.Problems {
		names = append(names, p.Name+":"+p.Reason)
	}
	if strings.Join(names, ",") != "b:unhealthy,c:herstartlus,e:gestopt met een fout" {
		t.Fatalf("%v", names)
	}
}

func TestListFindings(t *testing.T) {
	var c ContainerSummary
	if err := json.Unmarshal([]byte(`{"Id":"abc","Names":["/portainer"],"Image":"portainer/portainer-ce","State":"running",
		"Ports":[{"IP":"0.0.0.0","PrivatePort":9000,"PublicPort":9000,"Type":"tcp"},{"IP":"::","PrivatePort":9000,"PublicPort":9000,"Type":"tcp"},{"IP":"127.0.0.1","PrivatePort":8000,"PublicPort":8000,"Type":"tcp"}],
		"Mounts":[{"Type":"bind","Source":"/var/run/docker.sock"}],"HostConfig":{"NetworkMode":"bridge"}}`), &c); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range listFindings(c) {
		got[f.Kind] = f.Detail
	}
	want := map[string]string{FindSocket: "/var/run/docker.sock", FindPublic: "9000/tcp", FindLatest: "portainer/portainer-ce"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	for _, img := range []string{"registry.lan:5000/app:1.2", "nginx@sha256:abcd", "mariadb:11.4"} {
		c := ContainerSummary{ID: "x", Image: img, State: "running"}
		if f := listFindings(c); len(f) != 0 {
			t.Errorf("%s: geen bevinding verwacht, kreeg %v", img, f)
		}
	}
	if f := listFindings(ContainerSummary{ID: "x", Image: "registry.lan:5000/app", State: "running"}); len(f) != 1 || f[0].Kind != FindLatest {
		t.Errorf("registry met poort zonder tag: %v", f)
	}
}
