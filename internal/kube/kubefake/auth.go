package kubefake

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// saToken is een token van een ServiceAccount. uid is die van het
// ServiceAccount toen het token gemaakt werd: is het weggehaald en opnieuw
// gemaakt, dan geldt het token niet meer, zoals in Kubernetes.
type saToken struct {
	ns, account, uid string
	expires          time.Time
}

// authorize controleert wie een verzoek doet. Met het clientcertificaat
// van de beheerder mag alles; zonder TLS ook, voor tests die de handler
// rechtstreeks aanroepen. Met een token van een ServiceAccount geldt zijn
// ClusterRoleBinding: cluster-admin mag alles, view alleen lezen en geen
// Secrets, en zonder binding alleen discovery. Een verlopen token, of een
// token van een weggehaald ServiceAccount, weigert hij met 401.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if header == "" {
		if r.TLS == nil || len(r.TLS.PeerCertificates) > 0 {
			return true
		}
		status(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
		return false
	}
	tok, bearer := strings.CutPrefix(header, "Bearer ")
	s.mu.Lock()
	t, found := s.tokens[tok]
	sa := s.objects["v1/serviceaccounts"][t.ns+"/"+t.account]
	var roles []string
	for _, b := range s.objects["rbac.authorization.k8s.io/v1/clusterrolebindings"] {
		subjects, _ := b["subjects"].([]any)
		for _, sub := range subjects {
			m, _ := sub.(map[string]any)
			if m["kind"] == "ServiceAccount" && m["name"] == t.account && m["namespace"] == t.ns {
				ref, _ := b["roleRef"].(map[string]any)
				name, _ := ref["name"].(string)
				roles = append(roles, name)
			}
		}
	}
	s.mu.Unlock()
	if !bearer || !found || time.Now().After(t.expires) || sa == nil || uidOf(sa) != t.uid {
		status(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
		return false
	}
	segs := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	discovery := r.Method == http.MethodGet && (r.URL.Path == "/version" || segs[0] == "api" && len(segs) <= 2 || segs[0] == "apis" && len(segs) <= 3)
	for _, role := range roles {
		switch {
		case role == "cluster-admin":
			return true
		case role == "view" && r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/secrets"):
			return true
		}
	}
	if discovery {
		return true
	}
	user := "system:serviceaccount:" + t.ns + ":" + t.account
	status(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("User %q cannot %s %s", user, strings.ToLower(r.Method), r.URL.Path))
	return false
}

func uidOf(o map[string]any) string {
	md, _ := o["metadata"].(map[string]any)
	uid, _ := md["uid"].(string)
	return uid
}

// tokenRequest maakt een token voor een ServiceAccount, zoals de
// TokenRequest-API: standaard een uur geldig, minstens tien minuten.
func (s *Server) tokenRequest(w http.ResponseWriter, r *http.Request, t target) {
	var tr struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Spec       struct {
			ExpirationSeconds *int64 `json:"expirationSeconds"`
		} `json:"spec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&tr); err != nil || tr.Kind != "TokenRequest" ||
		tr.APIVersion != "authentication.k8s.io/v1" {
		status(w, http.StatusBadRequest, "BadRequest", "geen geldige TokenRequest")
		return
	}
	seconds := int64(3600)
	if tr.Spec.ExpirationSeconds != nil {
		seconds = *tr.Spec.ExpirationSeconds
	}
	if seconds < 600 {
		status(w, http.StatusUnprocessableEntity, "Invalid", "spec.expirationSeconds: Invalid value: may not specify a duration less than 10 minutes")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sa := s.objects["v1/serviceaccounts"][t.ns+"/"+t.name]
	if sa == nil {
		notFound(w, t)
		return
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := "kubefake." + hex.EncodeToString(b)
	exp := time.Now().Add(time.Duration(seconds) * time.Second).UTC().Truncate(time.Second)
	s.tokens[tok] = saToken{ns: t.ns, account: t.name, uid: uidOf(sa), expires: exp}
	writeJSON(w, http.StatusCreated, map[string]any{
		"kind": "TokenRequest", "apiVersion": "authentication.k8s.io/v1",
		"metadata": map[string]any{"name": t.name, "namespace": t.ns, "creationTimestamp": nil},
		"spec":     map[string]any{"audiences": []string{"https://kubernetes.default.svc.cluster.local", "k3s"}, "expirationSeconds": seconds},
		"status":   map[string]any{"token": tok, "expirationTimestamp": exp.Format(time.RFC3339)},
	})
}

// CA is het CA-certificaat van de API, als PEM.
func (s *Server) CA() []byte { return s.ca }

// TokenClient doet verzoeken op server zoals kubectl met een kubeconfig met
// een token: de CA uit de kubeconfig, geen clientcertificaat. Hij geeft de
// HTTP-status en de body.
func TokenClient(server string, ca []byte, token string) func(method, path, body string) (int, string) {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	return func(method, path, body string) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, server+path, rd)
		if err != nil {
			return 0, err.Error()
		}
		req.Header.Set("Authorization", "Bearer "+token)
		switch method {
		case http.MethodPatch:
			req.Header.Set("Content-Type", "application/merge-patch+json")
		case http.MethodPost:
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
}
