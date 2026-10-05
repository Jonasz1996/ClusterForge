package events

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// actionRe vindt een action in een event: Action: "x.y" of e.Action = "x.y".
var actionRe = regexp.MustCompile(`\bAction(?::\s*|\s*=\s*)"([a-z_]+\.[a-z_.]+)"`)

// TestCatalogCoversSources zoekt in de Go-bronnen van de server naar elke
// action die als event geschreven wordt, en eist dat de catalogus hem kent.
func TestCatalogCoversSources(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]string{}
	for _, dir := range []string{"internal", "cmd/clusterforge-server"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// De agent schrijft geen events; zijn commando's heten ook Action.
				switch d.Name() {
				case "agent", "gen", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range actionRe.FindAllStringSubmatch(string(b), -1) {
				found[m[1]] = path
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) < 30 {
		t.Fatalf("maar %d actions gevonden; klopt de zoekopdracht nog?", len(found))
	}
	for a, path := range found {
		if _, ok := Known[a]; !ok {
			t.Errorf("%s schrijft %q, maar die staat niet in de catalogus (catalog.go)", path, a)
		}
	}
	// De runner schrijft job.<status> met een berekende naam.
	for _, st := range []string{"succeeded", "failed", "canceled"} {
		if _, ok := Known["job."+st]; !ok {
			t.Errorf("job.%s ontbreekt in de catalogus", st)
		}
	}
}

func TestCatalogIsComplete(t *testing.T) {
	for a, s := range Known {
		if s.Label == "" || !ValidCategory(string(s.Category)) {
			t.Errorf("%s: soort %q, omschrijving %q", a, s.Category, s.Label)
		}
	}
	total := 0
	for _, c := range Categories {
		total += len(Actions(c.Key))
	}
	if total != len(Known) {
		t.Fatalf("%d actions verdeeld over de soorten, %d in de catalogus", total, len(Known))
	}
	if Lookup("iets.nieuws").Label != "iets.nieuws" {
		t.Fatal("een onbekende action moet zichzelf als omschrijving krijgen")
	}
}
