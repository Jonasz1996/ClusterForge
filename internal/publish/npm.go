package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Nginx Proxy Manager via de agent op de VIP-node. NPM kent geen
// API-sleutels: ClusterForge haalt met e-mail en wachtwoord een token
// (POST /api/tokens) en bewaart dat in het geheugen tot kort voor het
// verloopt.

type npmToken struct {
	node    uuid.UUID
	token   string
	expires time.Time
}

// NPMHost is een proxy host zoals NPM hem geeft (de velden die
// ClusterForge gebruikt).
type NPMHost struct {
	ID                    int             `json:"id"`
	DomainNames           []string        `json:"domain_names"`
	ForwardScheme         string          `json:"forward_scheme"`
	ForwardHost           string          `json:"forward_host"`
	ForwardPort           int             `json:"forward_port"`
	CertificateID         json.RawMessage `json:"certificate_id"`
	SSLForced             npmBool         `json:"ssl_forced"`
	HTTP2Support          npmBool         `json:"http2_support"`
	HSTSEnabled           npmBool         `json:"hsts_enabled"`
	HSTSSubdomains        npmBool         `json:"hsts_subdomains"`
	BlockExploits         npmBool         `json:"block_exploits"`
	CachingEnabled        npmBool         `json:"caching_enabled"`
	AllowWebsocketUpgrade npmBool         `json:"allow_websocket_upgrade"`
	AccessListID          int             `json:"access_list_id"`
	AdvancedConfig        string          `json:"advanced_config"`
	Enabled               *npmBool        `json:"enabled,omitempty"`
	Locations             json.RawMessage `json:"locations,omitempty"`
	Meta                  struct {
		NginxOnline *bool   `json:"nginx_online,omitempty"`
		NginxErr    *string `json:"nginx_err,omitempty"`
	} `json:"meta"`
	Certificate *NPMCert `json:"certificate,omitempty"`
	ModifiedOn  string   `json:"modified_on,omitempty"`
}

// npmBool leest true/false en 1/0 (oudere NPM met SQLite geeft getallen).
type npmBool bool

func (b *npmBool) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "true", "1":
		*b = true
	default:
		*b = false
	}
	return nil
}

// CertID geeft het id van het certificaat; 0 zonder.
func (h NPMHost) CertID() int {
	var n int
	if json.Unmarshal(h.CertificateID, &n) == nil {
		return n
	}
	return 0
}

// IsEnabled is true als de host aan staat (oudere NPM kent het veld niet).
func (h NPMHost) IsEnabled() bool { return h.Enabled == nil || bool(*h.Enabled) }

// NPMCert is een certificaat in NPM.
type NPMCert struct {
	ID          int      `json:"id"`
	Provider    string   `json:"provider"`
	NiceName    string   `json:"nice_name"`
	DomainNames []string `json:"domain_names"`
	ExpiresOn   string   `json:"expires_on"`
}

// Expires geeft de vervaldatum; nul als ze niet te lezen is.
func (c NPMCert) Expires() time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, c.ExpiresOn); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Covers zegt of het certificaat een naam dekt (exact of met een wildcard
// van één niveau).
func (c NPMCert) Covers(name string) bool {
	for _, d := range c.DomainNames {
		d = strings.ToLower(d)
		if d == name {
			return true
		}
		if rest, ok := strings.CutPrefix(d, "*."); ok {
			if i := strings.IndexByte(name, '.'); i > 0 && name[i+1:] == rest {
				return true
			}
		}
	}
	return false
}

// NPMAccessList is een toegangslijst.
type NPMAccessList struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// NPMRead is wat ClusterForge laatst in NPM las.
type NPMRead struct {
	Node        uuid.UUID       `json:"node_id"`
	Hostname    string          `json:"hostname"`
	Version     string          `json:"version,omitempty"`
	Hosts       []NPMHost       `json:"hosts"`
	Certs       []NPMCert       `json:"certificates"`
	AccessLists []NPMAccessList `json:"access_lists"`
}

// npmCall doet één aanroep van de NPM-API via de agent.
func (s *Service) npmCall(ctx context.Context, st Settings, node nodeRow, method, path string, body any) (int, json.RawMessage, error) {
	token, err := s.npmLogin(ctx, st, node, false)
	if err != nil {
		return 0, nil, err
	}
	status, data, err := s.npmRaw(ctx, st, node, method, path, token, body)
	if status == http.StatusUnauthorized {
		if token, err = s.npmLogin(ctx, st, node, true); err != nil {
			return 0, nil, err
		}
		status, data, err = s.npmRaw(ctx, st, node, method, path, token, body)
	}
	return status, data, err
}

func (s *Service) npmRaw(ctx context.Context, st Settings, node nodeRow, method, path, token string, body any) (int, json.RawMessage, error) {
	h := &protocol.WebHTTP{Method: method, Port: st.NPMPort, Path: "/api" + path}
	if token != "" {
		h.Headers = map[string]string{"Authorization": "Bearer " + token}
	}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		h.Body = b
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := s.bus.Web(cctx, node.ID, protocol.WebRequest{Op: protocol.WebOpHTTP, HTTP: h})
	if err != nil {
		return 0, nil, fmt.Errorf("NPM via %s: %w", node.Hostname, err)
	}
	if res.Error != "" {
		return 0, nil, fmt.Errorf("NPM via %s: %s", node.Hostname, res.Error)
	}
	return res.Status, res.Body, nil
}

// npmLogin haalt een token, of geeft het bewaarde.
func (s *Service) npmLogin(ctx context.Context, st Settings, node nodeRow, fresh bool) (string, error) {
	s.mu.Lock()
	t := s.npmToken
	s.mu.Unlock()
	if !fresh && t.token != "" && t.node == node.ID && s.Now().Before(t.expires) {
		return t.token, nil
	}
	secret, err := s.npmSecret(st)
	if err != nil {
		return "", err
	}
	status, data, err := s.npmRaw(ctx, st, node, http.MethodPost, "/tokens", "", map[string]string{"identity": st.NPMIdentity, "secret": secret})
	if err != nil {
		return "", err
	}
	var res struct {
		Token       string `json:"token"`
		Expires     string `json:"expires"`
		Requires2FA bool   `json:"requires_2fa"`
	}
	_ = json.Unmarshal(data, &res)
	switch {
	case res.Requires2FA:
		return "", invalid("de NPM-gebruiker %s heeft tweestapsverificatie; maak in NPM een aparte beheerder voor ClusterForge zonder 2FA", st.NPMIdentity)
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusBadRequest:
		return "", invalid("NPM weigert de aanmelding van %s: %s", st.NPMIdentity, npmMessage(status, data))
	case status != http.StatusOK || res.Token == "":
		return "", fmt.Errorf("aanmelden bij NPM: %s", npmMessage(status, data))
	}
	exp := s.Now().Add(12 * time.Hour)
	if e, err := time.Parse(time.RFC3339, res.Expires); err == nil {
		exp = e.Add(-time.Hour)
	}
	s.mu.Lock()
	s.npmToken = npmToken{node: node.ID, token: res.Token, expires: exp}
	s.mu.Unlock()
	return res.Token, nil
}

// npmMessage haalt de fout uit een antwoord van NPM
// ({"error":{"code":400,"message":"..."}}).
func npmMessage(status int, data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Error.Message, status)
	}
	var s string
	if json.Unmarshal(data, &s) == nil && s != "" {
		return fmt.Sprintf("HTTP %d: %s", status, trimText(s, 200))
	}
	return fmt.Sprintf("HTTP %d", status)
}

func trimText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// npmGet leest een pad en decodeert het.
func (s *Service) npmGet(ctx context.Context, st Settings, node nodeRow, path string, v any) error {
	status, data, err := s.npmCall(ctx, st, node, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("NPM %s: %s", path, npmMessage(status, data))
	}
	return json.Unmarshal(data, v)
}

// readNPM leest hosts, certificaten en toegangslijsten.
func (s *Service) readNPM(ctx context.Context, st Settings) (*NPMRead, error) {
	node, err := s.npmNode(ctx, st)
	if err != nil {
		return nil, err
	}
	out := &NPMRead{Node: node.ID, Hostname: node.Hostname}
	var ver struct {
		Version struct {
			Major, Minor, Revision int
		} `json:"version"`
	}
	if status, data, err := s.npmRaw(ctx, st, node, http.MethodGet, "/", "", nil); err == nil && status == http.StatusOK && json.Unmarshal(data, &ver) == nil {
		out.Version = fmt.Sprintf("%d.%d.%d", ver.Version.Major, ver.Version.Minor, ver.Version.Revision)
	}
	if err := s.npmGet(ctx, st, node, "/nginx/proxy-hosts?expand=certificate", &out.Hosts); err != nil {
		return nil, err
	}
	if err := s.npmGet(ctx, st, node, "/nginx/certificates", &out.Certs); err != nil {
		return nil, err
	}
	if err := s.npmGet(ctx, st, node, "/nginx/access-lists", &out.AccessLists); err != nil {
		return nil, err
	}
	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].ID < out.Hosts[j].ID })
	if out.Hosts == nil {
		out.Hosts = []NPMHost{}
	}
	if out.Certs == nil {
		out.Certs = []NPMCert{}
	}
	if out.AccessLists == nil {
		out.AccessLists = []NPMAccessList{}
	}
	return out, nil
}

// npmHostFields zijn de velden die ClusterForge bij een host zet of
// vergelijkt. NPM weigert onbekende velden (additionalProperties: false),
// dus een wijziging stuurt alleen wat verandert.
var npmHostFields = []string{
	"domain_names", "forward_scheme", "forward_host", "forward_port", "certificate_id", "ssl_forced", "http2_support",
	"hsts_enabled", "hsts_subdomains", "block_exploits", "caching_enabled", "allow_websocket_upgrade", "access_list_id", "advanced_config",
}

// fields maakt van een host de velden die ClusterForge beheert.
func (h NPMHost) fields() map[string]any {
	names := slices.Clone(h.DomainNames)
	for i := range names {
		names[i] = strings.ToLower(names[i])
	}
	return map[string]any{
		"domain_names": names, "forward_scheme": h.ForwardScheme, "forward_host": h.ForwardHost, "forward_port": h.ForwardPort,
		"certificate_id": h.CertID(), "ssl_forced": bool(h.SSLForced), "http2_support": bool(h.HTTP2Support),
		"hsts_enabled": bool(h.HSTSEnabled), "hsts_subdomains": bool(h.HSTSSubdomains), "block_exploits": bool(h.BlockExploits),
		"caching_enabled": bool(h.CachingEnabled), "allow_websocket_upgrade": bool(h.AllowWebsocketUpgrade),
		"access_list_id": h.AccessListID, "advanced_config": h.AdvancedConfig,
	}
}

// diffFields geeft de velden van want die verschillen van have.
func diffFields(have, want map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range npmHostFields {
		w, ok := want[k]
		if !ok {
			continue
		}
		if !sameValue(have[k], w) {
			out[k] = w
		}
	}
	return out
}

// sameValue vergelijkt twee JSON-waarden (getallen als float64).
func sameValue(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	var na, nb any
	_ = json.Unmarshal(ja, &na)
	_ = json.Unmarshal(jb, &nb)
	ra, _ := json.Marshal(na)
	rb, _ := json.Marshal(nb)
	return string(ra) == string(rb)
}

// npmHost leest één host.
func (s *Service) npmHost(ctx context.Context, st Settings, node nodeRow, id int) (*NPMHost, error) {
	status, data, err := s.npmCall(ctx, st, node, http.MethodGet, fmt.Sprintf("/nginx/proxy-hosts/%d?expand=certificate", id), nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("host %d lezen: %s", id, npmMessage(status, data))
	}
	var h NPMHost
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// errNPM is een weigering van NPM (validatie), met zijn melding.
type errNPM struct{ msg string }

func (e errNPM) Error() string { return e.msg }

// npmWrite maakt (id 0), wijzigt of verwijdert (body nil, del) een host.
func (s *Service) npmWrite(ctx context.Context, st Settings, node nodeRow, id int, body map[string]any, del bool) (*NPMHost, error) {
	method, path := http.MethodPost, "/nginx/proxy-hosts"
	switch {
	case del:
		method, path = http.MethodDelete, fmt.Sprintf("/nginx/proxy-hosts/%d", id)
	case id > 0:
		method, path = http.MethodPut, fmt.Sprintf("/nginx/proxy-hosts/%d", id)
	}
	var payload any
	if !del {
		payload = body
	}
	status, data, err := s.npmCall(ctx, st, node, method, path, payload)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, errNPM{fmt.Sprintf("NPM weigert %s %s: %s", method, path, npmMessage(status, data))}
	}
	if del {
		return nil, nil
	}
	var h NPMHost
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("antwoord van NPM: %w", err)
	}
	return &h, nil
}

// npmRequestCert vraagt een certificaat aan bij Let's Encrypt (HTTP-challenge
// via NPM). Oudere NPM wil e-mail en akkoord in meta, nieuwere weigert die
// velden: eerst met, bij een weigering daarop zonder.
func (s *Service) npmRequestCert(ctx context.Context, st Settings, node nodeRow, name string) (*NPMCert, error) {
	body := map[string]any{"provider": "letsencrypt", "domain_names": []string{name},
		"meta": map[string]any{"dns_challenge": false, "letsencrypt_agree": true, "letsencrypt_email": st.Defaults.LetsEncryptEmail}}
	status, data, err := s.npmCall(ctx, st, node, http.MethodPost, "/nginx/certificates", body)
	if err == nil && status == http.StatusBadRequest && strings.Contains(string(data), "additional") {
		body["meta"] = map[string]any{"dns_challenge": false}
		status, data, err = s.npmCall(ctx, st, node, http.MethodPost, "/nginx/certificates", body)
	}
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, errNPM{"Let's Encrypt voor " + name + ": " + npmMessage(status, data)}
	}
	var c NPMCert
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// pickCert zoekt een geldig certificaat voor een naam: exact eerst, dan een
// wildcard; het langst geldige.
func pickCert(certs []NPMCert, name string, now time.Time) *NPMCert {
	var best *NPMCert
	for i := range certs {
		c := certs[i]
		if !c.Covers(name) {
			continue
		}
		exp := c.Expires()
		if !exp.IsZero() && exp.Before(now.Add(24*time.Hour)) {
			continue
		}
		if best == nil || exp.After(best.Expires()) {
			best = &certs[i]
		}
	}
	return best
}
