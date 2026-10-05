package webui

import (
	"testing"
	"testing/fstest"
)

func TestResolve(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":             {Data: []byte("home")},
		"login.html":             {Data: []byte("login")},
		"instellingen.html":      {Data: []byte("settings")},
		"instellingen/x.txt":     {Data: []byte("rsc")},
		"docs/index.html":        {Data: []byte("docs")},
		"_next/static/app.js":    {Data: []byte("js")},
		"leeg/alleen-een-map.md": {Data: []byte("")},
	}
	cases := map[string]string{
		"":                    "index.html",
		".":                   "index.html",
		"login":               "login.html",
		"instellingen":        "instellingen.html",
		"instellingen/x.txt":  "instellingen/x.txt",
		"docs":                "docs/index.html",
		"_next/static/app.js": "_next/static/app.js",
		"leeg":                "",
		"bestaat-niet":        "",
	}
	for in, want := range cases {
		if got := resolve(fsys, in); got != want {
			t.Errorf("resolve(%q) = %q, want %q", in, got, want)
		}
	}
}
