// Package webui serveert de webinterface. De Next.js-app in web/ wordt als
// static export gebouwd en naar dist/ gekopieerd (make web), zodat hij in de
// server-binary wordt ingebed.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const placeholder = `<!doctype html><html lang="nl"><meta charset="utf-8"><title>ClusterForge</title>
<body style="font-family:sans-serif;max-width:40rem;margin:4rem auto">
<h1>ClusterForge</h1><p>De webinterface is niet meegebouwd in deze binary.
Draai <code>make web</code> en bouw de server opnieuw, of gebruik de Next.js-devserver.</p></body></html>`

// Handler serveert de ingebedde bestanden. Paden zonder bestand krijgen de
// 404-pagina van de export.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholder))
		})
	}
	notFound, _ := fs.ReadFile(sub, "404.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := resolve(sub, strings.TrimPrefix(path.Clean(r.URL.Path), "/"))
		if p == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(notFound)
			return
		}
		if strings.HasPrefix(p, "_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		serveFile(w, r, sub, p)
	})
}

// resolve zoekt het bestand bij een pad: het pad zelf, dan pad.html (zo
// exporteert Next.js /login), dan pad/index.html. Mappen zelf worden nooit
// als lijst getoond. Een lege string betekent: niet gevonden.
func resolve(fsys fs.FS, p string) string {
	if p == "" || p == "." {
		return "index.html"
	}
	if fi, err := fs.Stat(fsys, p); err == nil && !fi.IsDir() {
		return p
	}
	if fi, err := fs.Stat(fsys, p+".html"); err == nil && !fi.IsDir() {
		return p + ".html"
	}
	if fi, err := fs.Stat(fsys, path.Join(p, "index.html")); err == nil && !fi.IsDir() {
		return path.Join(p, "index.html")
	}
	return ""
}

// serveFile serveert één bestand. http.FileServer is hier onhandig: die stuurt
// /index.html door naar / en zou zo een lus maken.
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, fi.ModTime(), rs)
}
