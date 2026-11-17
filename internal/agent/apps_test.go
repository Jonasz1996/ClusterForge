package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApps(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "/var/lib/clusterforge/app-status/media-platform.json",
		`{"app":"media-platform","title":"Mediaplatform","version":"aa6fa8b","role":"actief","updated_at":"2026-11-17T01:00:00Z",
		"checks":[{"id":"mount","label":"Bestanden","ok":false,"severity":"rood","detail":"`+strings.Repeat("x", 500)+`"}],
		"values":[{"id":"volume_bytes","label":"Media","value":"170G","bytes":182536110080}]}`)
	// Andere naam dan het bestand, een verborgen bestand en een symlink: weg.
	writeFile(t, root, "/var/lib/clusterforge/app-status/x.json", `{"app":"y"}`)
	writeFile(t, root, "/var/lib/clusterforge/app-status/.tmp.json", `{"app":".tmp"}`)
	if err := os.Symlink(filepath.Join(root, "/var/lib/clusterforge/app-status/media-platform.json"),
		filepath.Join(root, "/var/lib/clusterforge/app-status/link.json")); err != nil {
		t.Fatal(err)
	}
	c := &Collector{Root: root}
	apps := c.apps()
	if len(apps) != 1 {
		t.Fatalf("apps: %+v", apps)
	}
	a := apps[0]
	if a.App != "media-platform" || a.Role != "actief" || a.Version != "aa6fa8b" || a.UpdatedAt.IsZero() {
		t.Errorf("app: %+v", a)
	}
	if len(a.Checks) != 1 || a.Checks[0].OK || a.Checks[0].Severity != "amber" || len(a.Checks[0].Detail) != 300 {
		t.Errorf("checks: %+v", a.Checks)
	}
	if len(a.Values) != 1 || a.Values[0].Bytes == nil || *a.Values[0].Bytes != 182536110080 {
		t.Errorf("values: %+v", a.Values)
	}
}
