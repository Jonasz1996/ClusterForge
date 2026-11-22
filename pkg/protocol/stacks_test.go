package protocol

import (
	"slices"
	"strings"
	"testing"
)

func TestStackPaths(t *testing.T) {
	for d, ok := range map[string]bool{
		"/docker/wordpress": true, "/docker": true, "/opt/stacks/x": true, "/": false, "/etc": false, "/var/lib/docker": false,
		"/docker/../etc": false, "docker/x": false, "/proc/1": false, "/docker/x/": false, "/etc/clusterforge/x": false,
	} {
		if ValidStackDir(d) != ok {
			t.Errorf("ValidStackDir(%q) = %v", d, !ok)
		}
	}
	for p, ok := range map[string]bool{
		"docker-compose.yml": true, ".env": true, "config/prod.env": true, "../x": false, "/abs": false, "a/../b": false, "a//b": false, "": false,
	} {
		if ValidStackFile(p) != ok {
			t.Errorf("ValidStackFile(%q) = %v", p, !ok)
		}
	}
}

func TestStackRunValidate(t *testing.T) {
	good := StackRun{Stacks: []StackApply{{Name: "plant-it", Dir: "/docker/plantit", ComposeFiles: []string{"docker-compose.yml"}, Want: StackUp,
		Files: []StackFile{{Path: "server.env", Content: "A=1"}}}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Stacks = []StackApply{{Name: "x", Dir: "/docker/x", ComposeFiles: []string{"c.yml"}, Want: "kapot"}}
	if bad.Validate() == nil {
		t.Fatal("onbekende actie aanvaard")
	}
	bad.Stacks = []StackApply{{Name: "x", Dir: "/docker/x", ComposeFiles: []string{"c.yml"}, Want: StackUp, Files: []StackFile{{Path: "../etc/passwd"}}}}
	if bad.Validate() == nil {
		t.Fatal("pad buiten de map aanvaard")
	}
	spec := RunSpec{ID: "stack-1", Kind: RunStack, Stack: &good}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStackListRoundTrip(t *testing.T) {
	in := []StackListLine{
		{Name: "wordpress", Dir: "/docker/wordpress", ComposeFiles: []string{"docker-compose.yaml"}, Placement: PlaceVIP, Critical: true},
		{Name: "node1", Dir: "/docker/tautulli/node1", ComposeFiles: []string{"docker-compose.yml", "extra.yml"}, Placement: PlaceNode},
	}
	got := ParseStackList(FormatStackList(in) + "kapot\tregel\n")
	if len(got) != 2 || !got[0].Critical || got[1].Critical || !slices.Equal(got[1].ComposeFiles, in[1].ComposeFiles) {
		t.Fatalf("%+v", got)
	}
}

func TestPauseProjects(t *testing.T) {
	ps, ok := PauseProjects("cf-agent stack-pause wordpress plant-it wordpress")
	if !ok || !slices.Equal(ps, []string{"wordpress", "plant-it"}) {
		t.Fatalf("%v %v", ps, ok)
	}
	if _, ok := PauseProjects("sqlite3 /x .backup /y"); ok {
		t.Fatal("gewone voorbereiding als pauze gelezen")
	}
}

func TestMaskFileRoundTrip(t *testing.T) {
	in := "environment:\n  MYSQL_ROOT_PASSWORD: s3cret\n  MYSQL_DATABASE: wp\n  - JWT_SECRET=abc\n  hmac_key: zzz\n  INVIDIOUS_CONFIG: |\n"
	masked, secrets := MaskFile(in)
	if strings.Contains(masked, "s3cret") || strings.Contains(masked, "abc") || strings.Contains(masked, "zzz") || len(secrets) != 3 {
		t.Fatalf("%q %v", masked, secrets)
	}
	if !strings.Contains(masked, "MYSQL_DATABASE: wp") || !strings.Contains(masked, "INVIDIOUS_CONFIG: |") {
		t.Fatalf("te veel gemaskeerd: %q", masked)
	}
	back, err := UnmaskFile(masked, secrets)
	if err != nil || back != in {
		t.Fatalf("%q %v", back, err)
	}
	if _, err := UnmaskFile("x: «geheim-9»", secrets); err == nil {
		t.Fatal("onbekende plaatshouder aanvaard")
	}
}
