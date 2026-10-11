package docs

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/gitops/ghfake"
)

func TestPush(t *testing.T) {
	gh := ghfake.New("jonas", "docs", "tok")
	srv := httptest.NewServer(gh)
	defer srv.Close()
	ctx := context.Background()
	at := time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	g, err := newGitHub(srv.URL, "jonas", "docs", "tok")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"docs/README.md": "# Overzicht\n", "docs/clusters/web/netwerk.md": "# Netwerk\n"}

	if _, err := push(ctx, g, "main", "docs", want, at); err == nil || !strings.Contains(err.Error(), "de repository is leeg") {
		t.Fatalf("lege repository: %v", err)
	}

	begin := gh.Commit("Begin", "Jonas", map[string]string{"README.md": "# Mijn documentatie\n", "notities.txt": "x\n"})
	r, err := push(ctx, g, "main", "docs", want, at)
	if err != nil || r.SHA == "" || len(r.Changes) != 2 || r.Changes[0].Kind != Added {
		t.Fatalf("eerste publicatie: %+v %v", r, err)
	}
	c, _ := gh.CommitOf(r.SHA)
	if !strings.HasPrefix(c.Message, "Documentatie bijgewerkt: overzicht, web\n\n- README.md: nieuw\n- clusters/web/netwerk.md: nieuw\n") ||
		c.Author != "ClusterForge" || !slices.Equal(c.Parents, []string{begin}) {
		t.Fatalf("commit: %+v", c)
	}
	files := gh.Files()
	if files["README.md"] != "# Mijn documentatie\n" || files["notities.txt"] != "x\n" || files["docs/clusters/web/netwerk.md"] != "# Netwerk\n" {
		t.Fatalf("bestanden: %v", files)
	}

	// Niets veranderd: geen commit.
	head := gh.Head("main")
	if r, err := push(ctx, g, "main", "docs", want, at); err != nil || r.SHA != "" || gh.Head("main") != head {
		t.Fatalf("tweede ronde: %+v %v", r, err)
	}

	// Een eigen bestand in de map blijft, een document van een verdwenen
	// cluster gaat weg, en alleen wat veranderde komt in de commit.
	gh.Commit("Eigen notitie", "Jonas", map[string]string{"docs/eigen.md": "mijn tekst\n", "docs/clusters/oud/herstel.md": "# Oud\n"})
	want["docs/clusters/web/netwerk.md"] = "# Netwerk\n\nVLAN 20\n"
	r, err = push(ctx, g, "main", "docs", want, at)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, ch := range r.Changes {
		got = append(got, ch.Path+" "+ch.Kind)
	}
	if strings.Join(got, ", ") != "docs/clusters/oud/herstel.md deleted, docs/clusters/web/netwerk.md modified" {
		t.Fatalf("wijzigingen: %v", got)
	}
	files = gh.Files()
	if files["docs/eigen.md"] != "mijn tekst\n" || files["docs/clusters/oud/herstel.md"] != "" || files["docs/README.md"] != "# Overzicht\n" {
		t.Fatalf("na opruimen: %v", files)
	}

	// Iemand pusht tegelijk: de commit wordt opnieuw gebouwd op de nieuwe
	// kop, zonder force, en zijn bestand blijft.
	gh.RaceOnce("main", "Tegelijk", map[string]string{"ander.txt": "y\n"})
	want["docs/README.md"] = "# Overzicht\n\nTwee clusters\n"
	r, err = push(ctx, g, "main", "docs", want, at)
	if err != nil {
		t.Fatal(err)
	}
	c, _ = gh.CommitOf(r.SHA)
	parent, _ := gh.CommitOf(c.Parents[0])
	if parent.Message != "Tegelijk" || gh.Files()["ander.txt"] != "y\n" || gh.Files()["docs/README.md"] != want["docs/README.md"] {
		t.Fatalf("na de race: parent %q, bestanden %v", parent.Message, gh.Files())
	}

	// Een branch die nog niet bestaat, krijgt alleen de documentatie.
	mainHead := gh.Head("main")
	only := map[string]string{"README.md": "# Overzicht\n"}
	r, err = push(ctx, g, "documentatie", "", only, at)
	if err != nil || r.SHA == "" {
		t.Fatalf("nieuwe branch: %+v %v", r, err)
	}
	c, _ = gh.CommitOf(r.SHA)
	if len(c.Parents) != 0 || !maps.Equal(gh.FilesOn("documentatie"), only) || gh.Head("main") != mainHead {
		t.Fatalf("nieuwe branch: %+v %v", c, gh.FilesOn("documentatie"))
	}

	// Een token zonder schrijfrecht, en een fout token.
	gh.AddToken("alleen-lezen", false)
	ro, _ := newGitHub(srv.URL, "jonas", "docs", "alleen-lezen")
	want["docs/README.md"] = "# Anders\n"
	if _, err := push(ctx, ro, "main", "docs", want, at); err == nil || !strings.Contains(err.Error(), "mag niet schrijven") {
		t.Fatalf("alleen lezen: %v", err)
	}
	bad, _ := newGitHub(srv.URL, "jonas", "docs", "fout")
	if _, err := push(ctx, bad, "main", "docs", want, at); err == nil || !strings.Contains(err.Error(), "ongeldig of verlopen") {
		t.Fatalf("fout token: %v", err)
	}
	if _, err := probe(ctx, ro, "main", "docs"); err == nil || !strings.Contains(err.Error(), "mag niet schrijven") {
		t.Fatalf("test met alleen lezen: %v", err)
	}
	p, err := probe(ctx, g, "main", "docs")
	if err != nil || !p.BranchExists || p.Head != gh.Head("main") || p.Files != 2 {
		t.Fatalf("test: %+v %v", p, err)
	}
	if p, err := probe(ctx, g, "nieuw", "docs"); err != nil || p.BranchExists {
		t.Fatalf("test van een nieuwe branch: %+v %v", p, err)
	}

	for _, call := range gh.Calls() {
		if call.Method != http.MethodGet && call.Write && call.Status < 300 &&
			!slices.Contains([]string{"/repos/jonas/docs/git/blobs", "/repos/jonas/docs/git/trees", "/repos/jonas/docs/git/commits",
				"/repos/jonas/docs/git/refs/heads/main", "/repos/jonas/docs/git/refs"}, call.Path) {
			t.Errorf("onverwachte schrijfactie: %+v", call)
		}
	}
	for _, req := range gh.Requests() {
		if strings.Contains(req, "tok") {
			t.Fatal("token in een URL")
		}
	}
}

func TestCommitMessage(t *testing.T) {
	var changes []FileChange
	for _, s := range []string{"f", "e", "d", "c", "b", "a"} {
		changes = append(changes, FileChange{"docs/clusters/" + s + "/netwerk.md", Modified})
	}
	changes = append(changes, FileChange{"docs/README.md", Modified})
	title, _, _ := strings.Cut(commitMessage(changes, "docs"), "\n")
	if title != "Documentatie bijgewerkt: overzicht, a, b, c, en 3 andere" {
		t.Fatal(title)
	}
	if !ours("README.md") || !ours("clusters/web/runbooks.md") || ours("clusters/web/eigen.md") || ours("clusters/web/x/netwerk.md") || ours("docs.md") {
		t.Fatal("ours")
	}
}

func TestTargetInput(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		path *string
		want string
	}{{nil, "docs"}, {str(""), ""}, {str("/"), ""}, {str(" wiki/clusterforge/ "), "wiki/clusterforge"}} {
		got, err := TargetInput{Owner: "jonas", Name: "docs", Path: tc.path}.normalize()
		if err != nil || got.path != tc.want || got.branch != "main" || got.api != "https://api.github.com" {
			t.Errorf("%v: %+v %v", tc.path, got, err)
		}
	}
	for _, p := range []string{"../x", "a/./b", "a b", "a//b"} {
		if _, err := (TargetInput{Owner: "jonas", Name: "docs", Path: str(p)}).normalize(); err == nil {
			t.Errorf("%q toegestaan", p)
		}
	}
	if _, err := (TargetInput{Owner: "jonas", Name: "docs", APIURL: "http://192.0.2.1"}).normalize(); err == nil {
		t.Error("http naar een ander toestel toegestaan")
	}
}
