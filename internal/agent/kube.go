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
	"reflect"
	"regexp"
	"slices"
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
	ca     []byte
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
	timeout := kubeTimeout
	if req.Op == protocol.KubeSnapshot {
		timeout = protocol.KubeSnapshotTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	case protocol.KubeLogs:
		res = b.logs(ctx, req)
	case protocol.KubePatch:
		res = b.patch(ctx, req)
	case protocol.KubeDelete:
		res = b.remove(ctx, req)
	case protocol.KubeEvict:
		res = b.evict(ctx, req)
	case protocol.KubeApply:
		res = b.apply(ctx, req)
	case protocol.KubeToken:
		res = b.token(ctx, req)
	case protocol.KubeSnapshot:
		res = a.snapshot(ctx, req)
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
	return b.do(ctx, req.Path, q, t)
}

var kubeContainer = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// logs leest de log van één container. Alleen
// /api/v1/namespaces/<ns>/pods/<naam>/log mag, met hoogstens
// KubeMaxLogLines regels. Van een grotere log dan KubeMaxLogBytes blijven
// de nieuwste regels over.
func (b *kubeBridge) logs(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t, err := parseKubePath(req.Path)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	if t.Group != "" || t.Version != "v1" || t.Namespace == "" || t.Resource != "pods" || t.Name == "" || t.Sub != "log" {
		return protocol.KubeResponse{Error: fmt.Sprintf("pad %q is geen log van een pod", req.Path)}
	}
	q := url.Values{"tailLines": {"500"}}
	for k, v := range req.Query {
		switch k {
		case "container":
			if !kubeContainer.MatchString(v) {
				return protocol.KubeResponse{Error: "ongeldige naam van een container"}
			}
		case "previous", "timestamps":
			if v != "true" && v != "false" {
				return protocol.KubeResponse{Error: k + " moet true of false zijn"}
			}
		case "tailLines":
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > protocol.KubeMaxLogLines {
				return protocol.KubeResponse{Error: fmt.Sprintf("tailLines moet tussen 1 en %d liggen", protocol.KubeMaxLogLines)}
			}
		default:
			return protocol.KubeResponse{Error: fmt.Sprintf("queryparameter %s is niet toegestaan", k)}
		}
		q.Set(k, v)
	}
	status, body, res := b.send(ctx, http.MethodGet, req.Path, q, nil, "")
	if res != nil {
		return *res
	}
	if status != http.StatusOK {
		// Een fout van de API, zoals een vorige container die er niet is,
		// komt als JSON.
		return kubeAnswer(t, status, body)
	}
	// limitBytes van de API zou de nieuwste regels afknippen; daarom
	// knipt de agent zelf, aan het begin.
	text, cut := body, false
	if len(text) > protocol.KubeMaxLogBytes {
		text, cut = text[len(text)-protocol.KubeMaxLogBytes:], true
	}
	for {
		if cut {
			if i := bytes.IndexByte(text, '\n'); i >= 0 {
				text = text[i+1:]
			}
		}
		s := string(text)
		if cut {
			s = protocol.KubeLogTrimmed + s
		}
		out, err := json.Marshal(s)
		if err != nil {
			return protocol.KubeResponse{Status: status, Error: err.Error()}
		}
		res := pack(status, out)
		if res.Error == "" || len(text) < 4096 {
			return res
		}
		// Ook gecomprimeerd te groot voor een bericht: de oudste helft
		// valt weg.
		text, cut = text[len(text)/2:], true
	}
}

// patchable zijn de soorten die patch mag wijzigen, met de subresources
// die erbij mogen. Al de rest gaat later via server-side apply.
var patchable = map[string][]string{
	"apps/deployments":  {"", "scale"},
	"apps/statefulsets": {"", "scale"},
	"apps/replicasets":  {"", "scale"},
	"apps/daemonsets":   {""},
	"/nodes":            {""},
}

var patchTypes = map[string]string{
	protocol.KubeMergePatch: "application/merge-patch+json",
	protocol.KubeJSONPatch:  "application/json-patch+json",
}

// writeQuery controleert de queryparameters van een schrijvend verzoek:
// alleen dryRun, bij delete ook hoe en hoe snel, en bij apply of het velden
// van een ander programma mag overnemen.
func writeQuery(op string, in map[string]string) (url.Values, error) {
	q := url.Values{}
	for k, v := range in {
		switch {
		case k == "dryRun" && v == "All":
		case op == protocol.KubeApply && k == "force" && v == "true":
		case op == protocol.KubeDelete && k == "propagationPolicy" && (v == "Background" || v == "Foreground" || v == "Orphan"):
		case op == protocol.KubeDelete && k == "gracePeriodSeconds":
			if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 3600 {
				return nil, errors.New("gracePeriodSeconds moet tussen 0 en 3600 liggen")
			}
		default:
			return nil, fmt.Errorf("queryparameter %s=%s is niet toegestaan", k, v)
		}
		q.Set(k, v)
	}
	return q, nil
}

// patch wijzigt een app of node. De body moet een JSON-object zijn bij een
// merge patch en een lijst bij een JSON patch.
func (b *kubeBridge) patch(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t, err := parseKubePath(req.Path)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	subs, ok := patchable[t.Group+"/"+t.Resource]
	if !ok || t.Name == "" || t.Version != "v1" || !slices.Contains(subs, t.Sub) || (t.Resource == "nodes") != (t.Namespace == "") {
		return protocol.KubeResponse{Error: fmt.Sprintf("pad %q mag niet gewijzigd worden", req.Path)}
	}
	ctype, ok := patchTypes[req.PatchType]
	if !ok {
		return protocol.KubeResponse{Error: "onbekende soort patch " + req.PatchType}
	}
	if len(req.Body) > protocol.KubeMaxPatch {
		return protocol.KubeResponse{Error: "de patch is te groot"}
	}
	var v any
	if err := json.Unmarshal(req.Body, &v); err != nil {
		return protocol.KubeResponse{Error: "de patch is geen JSON"}
	}
	if _, obj := v.(map[string]any); req.PatchType == protocol.KubeMergePatch && !obj {
		return protocol.KubeResponse{Error: "een merge patch is een JSON-object"}
	}
	if _, list := v.([]any); req.PatchType == protocol.KubeJSONPatch && !list {
		return protocol.KubeResponse{Error: "een JSON patch is een lijst"}
	}
	q, err := writeQuery(req.Op, req.Query)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	q.Set("fieldManager", protocol.KubeFieldManager)
	return b.write(ctx, http.MethodPatch, req.Path, q, req.Body, ctype, t)
}

// protectedNamespaces haalt de agent nooit weg: zonder die werkt het
// cluster niet meer.
var protectedNamespaces = []string{"default", "kube-system", "kube-public", "kube-node-lease"}

// remove haalt één object weg; nooit een hele lijst of een namespace van
// Kubernetes zelf.
func (b *kubeBridge) remove(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t, err := parseKubePath(req.Path)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	if t.Resource == "" || t.Name == "" || t.Sub != "" ||
		(t.Group == "" && t.Resource == "namespaces" && slices.Contains(protectedNamespaces, t.Name)) {
		return protocol.KubeResponse{Error: fmt.Sprintf("pad %q mag niet weggehaald worden", req.Path)}
	}
	q, err := writeQuery(req.Op, req.Query)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	return b.write(ctx, http.MethodDelete, req.Path, q, nil, "", t)
}

// evict zet één pod uit. De agent maakt de Eviction zelf, zodat er niets
// anders in kan staan dan de pod.
func (b *kubeBridge) evict(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t, err := parseKubePath(req.Path)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	if t.Group != "" || t.Version != "v1" || t.Namespace == "" || t.Resource != "pods" || t.Name == "" || t.Sub != "eviction" {
		return protocol.KubeResponse{Error: fmt.Sprintf("pad %q is geen eviction van een pod", req.Path)}
	}
	q, err := writeQuery(req.Op, req.Query)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "policy/v1", "kind": "Eviction",
		"metadata": map[string]any{"name": t.Name, "namespace": t.Namespace},
	})
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	return b.write(ctx, http.MethodPost, req.Path, q, body, "application/json", t)
}

// apply doet server-side apply van één object, van elke soort die de API
// kent: YAML toepassen is beheerdersmacht, en de server laat het alleen een
// admin doen. Nooit een lijst of een subresource. Eerst leest de agent het
// object zoals het nu is, zodat hij kan zeggen of er iets verandert, ook
// bij een Secret, waarvan de server de waarden niet ziet.
func (b *kubeBridge) apply(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	t, err := parseKubePath(req.Path)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	if t.Resource == "" || t.Name == "" || t.Sub != "" {
		return protocol.KubeResponse{Error: fmt.Sprintf("pad %q is geen object", req.Path)}
	}
	if len(req.Body) > protocol.KubeMaxPatch {
		return protocol.KubeResponse{Error: "het object is te groot"}
	}
	var obj map[string]any
	if err := json.Unmarshal(req.Body, &obj); err != nil || obj == nil {
		return protocol.KubeResponse{Error: "het object is geen JSON-object"}
	}
	q, err := writeQuery(req.Op, req.Query)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	q.Set("fieldManager", protocol.KubeFieldManager)
	was, before, res := b.send(ctx, http.MethodGet, req.Path, nil, nil, "")
	if res != nil {
		return *res
	}
	status, after, res := b.send(ctx, http.MethodPatch, req.Path, q, req.Body, "application/apply-patch+yaml")
	if res != nil {
		return *res
	}
	out := kubeAnswer(t, status, after)
	if was == http.StatusOK && status == http.StatusOK {
		out.Same = sameObject(before, after)
	}
	return out
}

// sameObject vergelijkt twee versies van een object, zonder de
// boekhouding die bij elke apply verandert.
func sameObject(a, b []byte) bool {
	norm := func(body []byte) any {
		var v any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			return nil
		}
		if m, ok := v.(map[string]any); ok {
			if md, ok := m["metadata"].(map[string]any); ok {
				delete(md, "managedFields")
				delete(md, "resourceVersion")
				delete(md, "generation")
			}
		}
		return v
	}
	x, y := norm(a), norm(b)
	return x != nil && reflect.DeepEqual(x, y)
}

// write stuurt een schrijvend verzoek en filtert het antwoord, net als bij
// lezen: ook een weggehaald Secret komt zonder waarden terug.
func (b *kubeBridge) write(ctx context.Context, method, p string, q url.Values, body []byte, ctype string, t kubeTarget) protocol.KubeResponse {
	status, out, res := b.send(ctx, method, p, q, body, ctype)
	if res != nil {
		return *res
	}
	return kubeAnswer(t, status, out)
}

// do stuurt het verzoek naar de API en filtert het antwoord.
func (b *kubeBridge) do(ctx context.Context, p string, q url.Values, t kubeTarget) protocol.KubeResponse {
	status, body, res := b.send(ctx, http.MethodGet, p, q, nil, "")
	if res != nil {
		return *res
	}
	return kubeAnswer(t, status, body)
}

// send doet een verzoek op de API. Gaat het mis voordat er een antwoord
// is, dan is res de fout voor de server.
func (b *kubeBridge) send(ctx context.Context, method, p string, q url.Values, reqBody []byte, ctype string) (status int, body []byte, res *protocol.KubeResponse) {
	fail := func(msg string) (int, []byte, *protocol.KubeResponse) {
		return 0, nil, &protocol.KubeResponse{Error: msg}
	}
	client, server, err := b.load()
	if err != nil {
		return fail(err.Error())
	}
	u := server + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if reqBody != nil {
		rd = bytes.NewReader(reqBody)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fail(err.Error())
	}
	if ctype != "" {
		hreq.Header.Set("Content-Type", ctype)
	}
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("User-Agent", "cf-agent")
	resp, err := client.Do(hreq)
	if err != nil {
		return fail("de Kubernetes-API op deze node antwoordt niet: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, protocol.KubeMaxRead+1))
	if err != nil {
		return fail("antwoord van de API lezen mislukt: " + err.Error())
	}
	if len(body) > protocol.KubeMaxRead {
		return fail("het antwoord van de API is groter dan 32 MB; filter op namespace of label")
	}
	return resp.StatusCode, body, nil
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
	return pack(status, out)
}

// pack zet een antwoord in een KubeResponse, gecomprimeerd als het groot
// is.
func pack(status int, out []byte) protocol.KubeResponse {
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
	b.server, b.ca, b.mod, b.size = strings.TrimRight(kc.server, "/"), kc.ca, st.ModTime(), st.Size()
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
	if cfg, err := readK3sConfig(root); err == nil {
		if cfg.NodeName != "" {
			f.NodeName = cfg.NodeName
		}
		f.Init = f.Role == "server" && cfg.Init
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
		res := a.kube.do(rctx, "/readyz", nil, kubeTarget{Info: "/readyz"})
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
