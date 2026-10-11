package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De bestanden van k3s. Een server schrijft de kubeconfig van de beheerder
// naar K3sKubeconfig; die verlaat de node nooit.
const (
	K3sKubeconfig  = "/etc/rancher/k3s/k3s.yaml"
	k3sConfig      = "/etc/rancher/k3s/config.yaml"
	k3sServerUnit  = "/etc/systemd/system/k3s.service"
	k3sAgentUnit   = "/etc/systemd/system/k3s-agent.service"
	kubeletHealthz = "http://127.0.0.1:10248/healthz"
)

// kubeSlots is hoeveel kube-verzoeken de agent tegelijk doorstuurt; een
// verzoek wacht hoogstens kubeQueueWait op een plek.
const (
	kubeSlots     = 4
	kubeQueueWait = 15 * time.Second
	kubeTimeout   = 30 * time.Second
)

// kubeBridge stuurt verzoeken van de server door naar de Kubernetes-API op
// deze node, met het beheerderscertificaat uit de kubeconfig van k3s. Hij
// leest de kubeconfig opnieuw als ze verandert, want k3s vernieuwt zijn
// certificaten.
type kubeBridge struct {
	path string
	sem  chan struct{}

	mu     sync.Mutex
	mod    time.Time
	size   int64
	server string
	client *http.Client
}

var errNoKubeconfig = errors.New("deze node heeft geen " + K3sKubeconfig + "; is het een k3s-server en draait k3s?")

func (a *Agent) kubeRoot() string {
	if a.Root != "" {
		return a.Root
	}
	if a.Collector != nil {
		return a.Collector.Root
	}
	return ""
}

func (a *Agent) initKube() {
	if a.kube == nil {
		a.kube = &kubeBridge{path: filepath.Join(rootOr(a.kubeRoot()), K3sKubeconfig), sem: make(chan struct{}, kubeSlots)}
	}
}

func rootOr(root string) string {
	if root == "" {
		return "/"
	}
	return root
}

// onKube beantwoordt een kube-verzoek in een eigen goroutine, zodat een
// trage lijst de andere verzoeken niet ophoudt.
func (a *Agent) onKube(m *nats.Msg) {
	go func() {
		res := a.handleKube(m.Data)
		data, err := envelope(protocol.TypeKubeResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op kube-verzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleKube(data []byte) protocol.KubeResponse {
	var env protocol.Envelope
	var req protocol.KubeRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeKubeRequest {
		return protocol.KubeResponse{Error: "ongeldig kube-verzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil || req.ID == "" {
		return protocol.KubeResponse{Error: "ongeldig kube-verzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.KubeResponse{Error: "kube-verzoek verlopen; klopt de klok van deze node?"}
	}
	a.initKube()
	b := a.kube
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
	case <-time.After(kubeQueueWait):
		return protocol.KubeResponse{Error: "de agent is te druk met andere kube-verzoeken; probeer het zo opnieuw"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeTimeout)
	defer cancel()
	if !req.Deadline.IsZero() {
		var c2 context.CancelFunc
		ctx, c2 = context.WithDeadline(ctx, req.Deadline)
		defer c2()
	}
	var res protocol.KubeResponse
	switch req.Op {
	case protocol.KubeRead:
		res = b.read(ctx, req)
	default:
		res = protocol.KubeResponse{Error: "onbekend kube-verzoek " + req.Op}
	}
	if res.Error != "" {
		a.Log.Warn("kube-verzoek mislukt", "op", req.Op, "path", req.Path, "err", res.Error)
	} else {
		a.Log.Debug("kube-verzoek", "op", req.Op, "path", req.Path, "status", res.Status)
	}
	return res
}

// kubeTarget is wat een pad op de API aanwijst.
type kubeTarget struct {
	// Info is /version, /readyz of /livez.
	Info string
	// Discovery is /api, /apis, /api/v1, /apis/<groep> of
	// /apis/<groep>/<versie>.
	Discovery bool
	Group     string
	Version   string
	Namespace string
	Resource  string
	Name      string
	// Sub is de subresource, zoals status; leeg is het object zelf.
	Sub string
}

// List is true voor een lijst van objecten.
func (t kubeTarget) List() bool { return t.Resource != "" && t.Name == "" }

var kubeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// parseKubePath ontleedt een pad op de API zoals de API-server dat doet. Een
// pad dat niet netjes is, met %, .., een lege of vreemde stap, of te veel
// stappen, wordt geweigerd.
func parseKubePath(p string) (kubeTarget, error) {
	var t kubeTarget
	bad := fmt.Errorf("pad %q is niet toegestaan", p)
	if len(p) > 512 || !strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.ContainsAny(p, "%?#\\ ") {
		return t, bad
	}
	switch p {
	case "/version", "/readyz", "/livez":
		t.Info = p
		return t, nil
	case "/api", "/apis":
		t.Discovery = true
		return t, nil
	}
	segs := strings.Split(p[1:], "/")
	for _, s := range segs {
		if !kubeSegment.MatchString(s) {
			return t, bad
		}
	}
	var rest []string
	switch segs[0] {
	case "api":
		t.Version = segs[1]
		if len(segs) == 2 {
			t.Discovery = true
			return t, nil
		}
		rest = segs[2:]
	case "apis":
		t.Group = segs[1]
		if len(segs) <= 3 {
			if len(segs) == 3 {
				t.Version = segs[2]
			}
			t.Discovery = true
			return t, nil
		}
		t.Version = segs[2]
		rest = segs[3:]
	default:
		return t, bad
	}
	// Zoals de API-server: namespaces/<ns>/<rest> is een object in een
	// namespace, behalve status en finalize van de namespace zelf.
	if rest[0] == "namespaces" && len(rest) > 2 && rest[2] != "status" && rest[2] != "finalize" {
		t.Namespace = rest[1]
		rest = rest[2:]
	}
	if len(rest) > 3 {
		return t, bad
	}
	t.Resource = rest[0]
	if len(rest) > 1 {
		t.Name = rest[1]
	}
	if len(rest) > 2 {
		t.Sub = rest[2]
	}
	return t, nil
}

// readQuery controleert de queryparameters van een leesverzoek. Een lijst
// komt altijd in pagina's.
func readQuery(t kubeTarget, in map[string]string) (url.Values, error) {
	q := url.Values{}
	for k, v := range in {
		if len(v) > 1024 {
			return nil, fmt.Errorf("queryparameter %s is te lang", k)
		}
		switch k {
		case "labelSelector", "fieldSelector", "continue":
			if !t.List() {
				return nil, fmt.Errorf("queryparameter %s kan alleen bij een lijst", k)
			}
			q.Set(k, v)
		case "limit":
			n, err := strconv.Atoi(v)
			if !t.List() || err != nil || n < 1 || n > protocol.KubeMaxPage {
				return nil, fmt.Errorf("limit moet tussen 1 en %d liggen, bij een lijst", protocol.KubeMaxPage)
			}
			q.Set(k, v)
		default:
			return nil, fmt.Errorf("queryparameter %s is niet toegestaan", k)
		}
	}
	if t.List() && q.Get("limit") == "" {
		q.Set("limit", strconv.Itoa(protocol.KubePageSize))
	}
	return q, nil
}

func (b *kubeBridge) read(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t, err := parseKubePath(req.Path)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	// Een subresource zoals exec, attach, portforward, proxy of log is
	// geen object; lezen mag alleen het object en zijn status.
	if t.Sub != "" && t.Sub != "status" {
		return protocol.KubeResponse{Error: fmt.Sprintf("pad %q is niet toegestaan", req.Path)}
	}
	q, err := readQuery(t, req.Query)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	return b.do(ctx, http.MethodGet, req.Path, q, t)
}

// do stuurt het verzoek naar de API en filtert het antwoord.
func (b *kubeBridge) do(ctx context.Context, method, p string, q url.Values, t kubeTarget) protocol.KubeResponse {
	client, server, err := b.load()
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	u := server + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	hreq, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("User-Agent", "cf-agent")
	resp, err := client.Do(hreq)
	if err != nil {
		return protocol.KubeResponse{Error: "de Kubernetes-API op deze node antwoordt niet: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, protocol.KubeMaxRead+1))
	if err != nil {
		return protocol.KubeResponse{Error: "antwoord van de API lezen mislukt: " + err.Error()}
	}
	if len(body) > protocol.KubeMaxRead {
		return protocol.KubeResponse{Error: "het antwoord van de API is groter dan 32 MB; filter op namespace of label"}
	}
	return kubeAnswer(t, resp.StatusCode, body)
}

// kubeAnswer maakt van het antwoord van de API een KubeResponse. Alleen
// JSON gaat terug, gefilterd: wat de agent niet kan lezen, kan hij ook niet
// nakijken op geheimen.
func kubeAnswer(t kubeTarget, status int, body []byte) protocol.KubeResponse {
	if t.Info == "/readyz" || t.Info == "/livez" {
		return protocol.KubeResponse{Status: status}
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return protocol.KubeResponse{Status: status, Error: fmt.Sprintf("de API gaf geen JSON terug (HTTP %d)", status)}
	}
	scrub(v, t.Resource == "secrets")
	out, err := json.Marshal(v)
	if err != nil {
		return protocol.KubeResponse{Status: status, Error: err.Error()}
	}
	if len(out) <= protocol.KubeGzipAbove {
		return protocol.KubeResponse{Status: status, Body: out}
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(out)
	_ = zw.Close()
	if buf.Len() > protocol.KubeMaxBody {
		return protocol.KubeResponse{Status: status, Error: fmt.Sprintf(
			"het antwoord is te groot (%d kB gecomprimeerd); filter op namespace of label", buf.Len()>>10)}
	}
	return protocol.KubeResponse{Status: status, Gzip: buf.Bytes()}
}

const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// scrub haalt weg wat de node niet mag verlaten: de waarden van elk Secret,
// ook in een lijst, en de managedFields van elk object. secret is true als
// het pad naar secrets wijst; dan telt het object, of elk item van de
// lijst, als Secret, wat de kind ook zegt.
func scrub(v any, secret bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	kind, _ := m["kind"].(string)
	if items, ok := m["items"].([]any); ok {
		for _, it := range items {
			scrub(it, secret || kind == "SecretList")
		}
	}
	if md, ok := m["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
	}
	if secret || kind == "Secret" {
		hideSecret(m)
	}
}

// hideSecret vervangt elke waarde van een Secret door zijn lengte. Een
// veld dat geen gewone lijst van waarden is, gaat er helemaal af.
func hideSecret(m map[string]any) {
	for _, field := range []string{"data", "stringData"} {
		raw, present := m[field]
		if !present || raw == nil {
			continue
		}
		d, ok := raw.(map[string]any)
		if !ok {
			delete(m, field)
			continue
		}
		for k, val := range d {
			s, _ := val.(string)
			n := len(s)
			if field == "data" {
				if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
					n = len(dec)
				}
			}
			d[k] = protocol.KubeHidden(n)
		}
	}
	if md, ok := m["metadata"].(map[string]any); ok {
		if ann, ok := md["annotations"].(map[string]any); ok {
			if _, ok := ann[lastApplied]; ok {
				ann[lastApplied] = "(verborgen)"
			}
		}
	}
}

// load geeft de HTTP-client voor de API, en leest de kubeconfig opnieuw als
// ze veranderde.
func (b *kubeBridge) load() (*http.Client, string, error) {
	st, err := os.Stat(b.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", errNoKubeconfig
	}
	if err != nil {
		return nil, "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client != nil && st.ModTime().Equal(b.mod) && st.Size() == b.size {
		return b.client, b.server, nil
	}
	data, err := os.ReadFile(b.path)
	if err != nil {
		return nil, "", err
	}
	kc, err := parseKubeconfig(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", K3sKubeconfig, err)
	}
	cert, err := tls.X509KeyPair(kc.cert, kc.key)
	if err != nil {
		return nil, "", fmt.Errorf("%s: certificaat: %w", K3sKubeconfig, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(kc.ca) {
		return nil, "", fmt.Errorf("%s: geen geldige CA", K3sKubeconfig)
	}
	if b.client != nil {
		b.client.CloseIdleConnections()
	}
	b.client = &http.Client{
		Transport: &http.Transport{
			// De API staat op deze node; nooit via een proxy.
			Proxy:               nil,
			TLSClientConfig:     &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			MaxIdleConnsPerHost: kubeSlots,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	b.server, b.mod, b.size = strings.TrimRight(kc.server, "/"), st.ModTime(), st.Size()
	return b.client, b.server, nil
}

type kubeconfig struct {
	server        string
	ca, cert, key []byte
}

// parseKubeconfig leest de kubeconfig die k3s schrijft: één cluster en één
// gebruiker, met de certificaten erin. Een volledige YAML-parser is daar
// niet voor nodig.
func parseKubeconfig(data []byte) (kubeconfig, error) {
	var kc kubeconfig
	for line := range strings.Lines(string(data)) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		var dst *[]byte
		switch strings.TrimPrefix(k, "- ") {
		case "server":
			if kc.server == "" {
				kc.server = v
			}
			continue
		case "certificate-authority-data":
			dst = &kc.ca
		case "client-certificate-data":
			dst = &kc.cert
		case "client-key-data":
			dst = &kc.key
		default:
			continue
		}
		if *dst != nil {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return kc, fmt.Errorf("%s: %w", k, err)
		}
		*dst = b
	}
	u, err := url.Parse(kc.server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return kc, errors.New("geen https-server")
	}
	if kc.ca == nil || kc.cert == nil || kc.key == nil {
		return kc, errors.New("de certificaten ontbreken")
	}
	return kc, nil
}

// kubeFacts beschrijft k3s op deze node, of nil zonder k3s.
func (a *Agent) kubeFacts(ctx context.Context, hostname string) *protocol.KubeFacts {
	root := rootOr(a.kubeRoot())
	exists := func(p string) bool {
		_, err := os.Stat(filepath.Join(root, p))
		return err == nil
	}
	f := &protocol.KubeFacts{NodeName: hostname}
	switch {
	case exists(K3sKubeconfig) || exists(k3sServerUnit):
		f.Role = "server"
	case exists(k3sAgentUnit):
		f.Role = "agent"
	default:
		return nil
	}
	if b, err := os.ReadFile(filepath.Join(root, k3sConfig)); err == nil {
		for line := range strings.Lines(string(b)) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "node-name:"); ok {
				if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
					f.NodeName = v
				}
			}
		}
	}
	if out, err := a.execTimeout(ctx, 5*time.Second, "k3s", "--version"); err == nil {
		// k3s version v1.35.9+k3s1 (0a1b2c3d)
		if fields := strings.Fields(string(out)); len(fields) >= 3 && fields[0] == "k3s" && fields[1] == "version" {
			f.Version = fields[2]
		}
	}
	if f.Role == "server" {
		a.initKube()
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		res := a.kube.do(rctx, http.MethodGet, "/readyz", nil, kubeTarget{Info: "/readyz"})
		cancel()
		f.APIReady = res.Status == http.StatusOK
	}
	f.KubeletReady = a.kubeletReady(ctx)
	return f
}

func (a *Agent) kubeletReady(ctx context.Context) bool {
	u := a.KubeletHealthz
	if u == "" {
		u = kubeletHealthz
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode == http.StatusOK
}
