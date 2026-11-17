package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestAppStatsPlexAndArr(t *testing.T) {
	root := t.TempDir()
	plexDir := filepath.Join(root, "var/lib/plexmediaserver/Library/Application Support/Plex Media Server")
	if err := os.MkdirAll(plexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plexDir, "Preferences.xml"), []byte(`<?xml version="1.0"?><Preferences PlexOnlineToken="geheim-plex" FriendlyName="x"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "var/lib/sonarr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "var/lib/sonarr/config.xml"), []byte(`<Config><ApiKey>geheim-arr</ApiKey></Config>`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/identity" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"version":"1.41.2.9200-abc"}}`)
			return
		}
		plex := r.Header.Get("X-Plex-Token") == "geheim-plex"
		arr := r.Header.Get("X-Api-Key") == "geheim-arr"
		switch {
		case plex && r.URL.Path == "/status/sessions":
			_, _ = io.WriteString(w, `{"MediaContainer":{"size":2,"Metadata":[{"TranscodeSession":{}},{}]}}`)
		case plex && r.URL.Path == "/library/sections":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Directory":[{"type":"movie"},{"type":"show"},{"type":"movie"}]}}`)
		case arr && r.URL.Path == "/api/v3/system/status":
			_, _ = io.WriteString(w, `{"version":"4.0.9","appName":"Sonarr"}`)
		case arr && r.URL.Path == "/api/v3/health":
			_, _ = io.WriteString(w, `[{"type":"warning"},{"type":"notice"}]`)
		case arr && r.URL.Path == "/api/v3/queue":
			_, _ = io.WriteString(w, `{"totalRecords":3}`)
		case arr && r.URL.Path == "/api/v3/wanted/missing":
			_, _ = io.WriteString(w, `{"totalRecords":12}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	a := &Agent{Log: slog.New(slog.DiscardHandler), Root: root}

	req := protocol.WebRequest{Op: protocol.WebOpAppStats, App: &protocol.WebAppStats{Kind: protocol.AppStatsPlex, Port: port}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	res := a.web(context.Background(), req)
	if res.Error != "" || res.App == nil {
		t.Fatalf("plex: %+v", res)
	}
	v := res.App.Values
	if v["streams"] != 2 || v["transcodes"] != 1 || v["libraries"] != 3 || v["libraries_movies"] != 2 || v["version"] != "1.41.2.9200" {
		t.Errorf("plex: %v", v)
	}
	res = a.web(context.Background(), protocol.WebRequest{Op: protocol.WebOpAppStats, App: &protocol.WebAppStats{Kind: protocol.AppStatsArr, Port: port}})
	if res.Error != "" || res.App.Values["queue"] != 3 || res.App.Values["missing"] != 12 || res.App.Values["health_issues"] != 1 || res.App.Values["version"] != "4.0.9" {
		t.Errorf("arr: %+v", res)
	}
	// Geen sleutel: nette fout, geen cijfers.
	a.Root = t.TempDir()
	if res = a.web(context.Background(), req); res.Error == "" {
		t.Error("zonder Preferences.xml hoort een fout")
	}
	for _, bad := range []protocol.WebRequest{
		{Op: protocol.WebOpAppStats},
		{Op: protocol.WebOpAppStats, App: &protocol.WebAppStats{Kind: "rm", Port: 1}},
		{Op: protocol.WebOpAppStats, App: &protocol.WebAppStats{Kind: "plex", Port: 1, Container: "../x"}},
	} {
		if bad.Validate() == nil {
			t.Errorf("hoort ongeldig: %+v", bad.App)
		}
	}
}
