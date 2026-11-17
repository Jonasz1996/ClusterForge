package publish

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// fakeAuthentik is een Authentik met de API v3 die de publicatie gebruikt,
// plus de outpost: /outpost.goauthentik.io/auth/nginx geeft 401 (niet
// aangemeld), zodat NPM doorstuurt naar /outpost.goauthentik.io/start.
type fakeAuthentik struct {
	mu        sync.Mutex
	nextPK    int
	apps      []map[string]any
	providers []map[string]any
	outpost   map[string]any
	calls     []string
	authHits  int
}

func newFakeAuthentik() *fakeAuthentik {
	return &fakeAuthentik{nextPK: 10, outpost: map[string]any{
		"pk": "11111111-2222-3333-4444-555555555555", "name": "authentik Embedded Outpost", "type": "proxy",
		"managed": "goauthentik.io/outposts/embedded", "providers": []any{},
	}}
}

func page(results any) map[string]any {
	return map[string]any{"pagination": map[string]any{"next": 0}, "results": results}
}

func (f *fakeAuthentik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if strings.HasPrefix(p, "/outpost.goauthentik.io") {
		if strings.HasPrefix(p, "/outpost.goauthentik.io/auth/nginx") {
			f.authHits++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "https://auth.example.test/application/o/authorize/", http.StatusFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer ak-token" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"detail":"Token invalid/expired"}`)
		return
	}
	f.calls = append(f.calls, r.Method+" "+p)
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
	}
	write := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case p == "/api/v3/admin/version/":
		write(200, map[string]any{"version_current": "2025.6.2"})
	case p == "/api/v3/flows/instances/":
		if r.URL.Query().Get("designation") == "authorization" {
			write(200, page([]map[string]any{{"pk": "flow-auth", "slug": "default-provider-authorization-implicit-consent", "designation": "authorization"}}))
		} else {
			write(200, page([]map[string]any{{"pk": "flow-inval", "slug": "default-provider-invalidation-flow", "designation": "invalidation"}}))
		}
	case p == "/api/v3/core/applications/" && r.Method == http.MethodGet:
		write(200, page(f.apps))
	case p == "/api/v3/core/applications/" && r.Method == http.MethodPost:
		body["pk"] = "app-" + body["slug"].(string)
		f.apps = append(f.apps, body)
		for _, pr := range f.providers {
			if toInt(pr["pk"]) == toInt(body["provider"]) {
				pr["assigned_application_slug"] = body["slug"]
			}
		}
		write(201, body)
	case strings.HasPrefix(p, "/api/v3/core/applications/") && r.Method == http.MethodDelete:
		slug := strings.Trim(strings.TrimPrefix(p, "/api/v3/core/applications/"), "/")
		for i, a := range f.apps {
			if a["slug"] == slug {
				f.apps = append(f.apps[:i], f.apps[i+1:]...)
				for _, pr := range f.providers {
					if pr["assigned_application_slug"] == slug {
						pr["assigned_application_slug"] = ""
					}
				}
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(404)
	case p == "/api/v3/providers/proxy/" && r.Method == http.MethodGet:
		write(200, page(f.providers))
	case p == "/api/v3/providers/proxy/" && r.Method == http.MethodPost:
		if body["authorization_flow"] != "flow-auth" || body["invalidation_flow"] != "flow-inval" || body["mode"] != "forward_single" {
			write(400, map[string]any{"authorization_flow": []string{"ongeldig"}})
			return
		}
		f.nextPK++
		body["pk"] = f.nextPK
		body["assigned_application_slug"] = ""
		f.providers = append(f.providers, body)
		write(201, body)
	case strings.HasPrefix(p, "/api/v3/providers/proxy/"):
		pk, _ := strconv.Atoi(strings.Trim(strings.TrimPrefix(p, "/api/v3/providers/proxy/"), "/"))
		for i, pr := range f.providers {
			if toInt(pr["pk"]) != pk {
				continue
			}
			if r.Method == http.MethodDelete {
				f.providers = append(f.providers[:i], f.providers[i+1:]...)
				w.WriteHeader(204)
				return
			}
			for k, v := range body {
				pr[k] = v
			}
			write(200, pr)
			return
		}
		w.WriteHeader(404)
	case p == "/api/v3/outposts/instances/" && r.Method == http.MethodGet:
		write(200, page([]map[string]any{f.outpost}))
	case strings.HasPrefix(p, "/api/v3/outposts/instances/") && r.Method == http.MethodPatch:
		f.outpost["providers"] = body["providers"]
		write(200, f.outpost)
	default:
		w.WriteHeader(404)
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func (f *fakeAuthentik) counts() (apps, providers, inOutpost int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ps, _ := f.outpost["providers"].([]any)
	return len(f.apps), len(f.providers), len(ps)
}
