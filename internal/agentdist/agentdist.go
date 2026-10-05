// Package agentdist serveert het installatiescript en de cf-agent-binaries,
// zodat een node de agent rechtstreeks van zijn eigen ClusterForge haalt.
package agentdist

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed install.sh
var installScript []byte

// downloadable zijn de enige bestanden die uit de agentmap geserveerd worden.
var downloadable = map[string]bool{
	"cf-agent-linux-amd64":        true,
	"cf-agent-linux-arm64":        true,
	"cf-agent-linux-amd64.sha256": true,
	"cf-agent-linux-arm64.sha256": true,
}

// InstallScript serveert /install/agent.sh.
func InstallScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(installScript)
}

// Downloads serveert /downloads/<naam> uit dir.
func Downloads(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/downloads/")
		if !downloadable[name] {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			http.Error(w, "deze agent is niet meegebouwd in deze server", http.StatusNotFound)
			return
		}
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if strings.HasSuffix(name, ".sha256") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, st.ModTime(), f)
	}
}
