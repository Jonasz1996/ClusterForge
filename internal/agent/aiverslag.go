package agent

// Fase 38: cf-agent op de machine van AI-verslag (Jonas' OPNsense-analyse,
// een FastAPI-dienst op 127.0.0.1:8088). Net als bij PBS en TrueNAS geeft
// de agent de lokale API door; welke paden de GUI gebruikt, beslist de
// server (internal/aiverslag).
//
// AI-verslag heeft geen login: schrijven mag alleen van dezelfde site. Voor
// apps bestaat koppelen met een eenmalige code. De agent doet dat zelf,
// lokaal: POST /api/app/tokens (als dezelfde site) geeft een code,
// POST /api/app/pair geeft een Bearer-sleutel met de naam "ClusterForge".
// Die sleutel staat in /etc/clusterforge/aiverslag.token (0600) en verlaat
// de machine nooit; in AI-verslag is hij zichtbaar en intrekbaar onder
// 📱 App. Is hij ingetrokken of verlopen, dan koppelt de agent opnieuw.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	aivDir       = "/opt/aiverslag"
	aivEnv       = "/etc/aiverslag.env"
	aivTokenFile = "/etc/clusterforge/aiverslag.token"
	aivData      = "/var/lib/aiverslag"
	// aivAppName is de naam van de koppeling in AI-verslag (📱 App).
	aivAppName = "ClusterForge"
)

const errNotAIV = pveError("AI-verslag staat niet op deze machine (/opt/aiverslag/app.py of aiverslag-web.service ontbreekt)")

// aivUnits zijn de units die de facts tonen, in deze volgorde.
var aivUnits = []string{
	"aiverslag-web.service", "aiverslag-run.timer", "aiverslag-run.service",
	"aiverslag-inzicht.timer", "aiverslag-inzicht.service",
	"aiverslag-werker.timer", "aiverslag-werker.service",
	"aiverslag-github.timer", "ollama.service", "nginx.service",
}

// aivBaseOverride vervangt het lokale adres (tests).
var aivBaseOverride string

// aivPairMu zorgt dat er maar één koppeling tegelijk loopt.
var aivPairMu sync.Mutex

func (h pveHost) aivUnitFile() string {
	for _, p := range []string{"/etc/systemd/system/aiverslag-web.service", "/lib/systemd/system/aiverslag-web.service", "/usr/lib/systemd/system/aiverslag-web.service"} {
		if _, err := os.Stat(h.path(p)); err == nil {
			return h.path(p)
		}
	}
	return ""
}

// isAIV zegt of AI-verslag op deze machine staat.
func (h pveHost) isAIV() bool {
	if _, err := os.Stat(h.path(aivDir + "/app.py")); err != nil {
		return false
	}
	return h.aivUnitFile() != ""
}

var (
	aivPortRe = regexp.MustCompile(`--port[ =]([0-9]{2,5})`)
	aivHostRe = regexp.MustCompile(`--host[ =]([0-9.]+|localhost|\[::1\])`)
	aivVerRe  = regexp.MustCompile(`(?m)^APPVERSIE\s*=\s*"([^"]{1,40})"`)
)

// aivBase is het lokale adres van de webdienst, uit ExecStart van de unit.
func (h pveHost) aivBase() string {
	if aivBaseOverride != "" {
		return aivBaseOverride
	}
	host, port := "127.0.0.1", "8088"
	if f := h.aivUnitFile(); f != "" {
		if data, err := os.ReadFile(f); err == nil {
			if m := aivPortRe.FindSubmatch(data); m != nil {
				port = string(m[1])
			}
			if m := aivHostRe.FindSubmatch(data); m != nil && string(m[1]) != "0.0.0.0" {
				host = string(m[1])
			}
		}
	}
	return "http://" + host + ":" + port
}

func aivHTTP(timeout time.Duration) *http.Client {
	// Altijd rechtstreeks: een proxy in de omgeving mag lokaal verkeer niet
	// omleiden.
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
}

// aivFacts beschrijft AI-verslag; nil op een andere machine.
func (h pveHost) aivFacts(ctx context.Context) *protocol.AIVFacts {
	if !h.isAIV() {
		return nil
	}
	f := &protocol.AIVFacts{Dir: aivDir, URL: h.aivBase()}
	if data, err := os.ReadFile(h.path(aivDir + "/v2/kern.py")); err == nil {
		if m := aivVerRe.FindSubmatch(data); m != nil {
			f.Version = string(m[1])
		}
	}
	if data, err := os.ReadFile(h.path(aivEnv)); err == nil {
		f.Model = aivEnvValue(string(data), "MODEL")
	}
	if tok, _ := os.ReadFile(h.path(aivTokenFile)); len(bytes.TrimSpace(tok)) > 0 {
		f.Paired = true
	}
	if out, err := h.exec(ctx, "systemctl", append([]string{"show", "--timestamp=unix", "--property=Id,LoadState,ActiveState,SubState,Result,StateChangeTimestamp"}, aivUnits...)...); err == nil {
		f.Units = parseAIVUnits(string(out))
	}
	dir := aivData
	if _, err := os.Stat(h.path(dir)); err != nil {
		dir = "/"
	}
	if out, err := h.exec(ctx, "df", "-P", h.path(dir)); err == nil {
		f.DataUsedPct = parseDFPct(string(out))
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if req, err := http.NewRequestWithContext(pctx, http.MethodGet, f.URL+"/api/app/ping", nil); err == nil {
		if res, err := aivHTTP(3 * time.Second).Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			_ = res.Body.Close()
			f.Reachable = res.StatusCode == http.StatusOK
		}
	}
	return f
}

func (c *Collector) aiverslag(ctx context.Context) *protocol.AIVFacts {
	return pveHost{root: c.Root, run: c.Run}.aivFacts(ctx)
}

// aivEnvValue geeft de laatste waarde van key in een env-bestand. Alleen
// die ene sleutel wordt gelezen; de rest (geheimen) blijft liggen.
func aivEnvValue(env, key string) string {
	val := ""
	sc := bufio.NewScanner(strings.NewReader(env))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && k == key {
			val = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return val
}

// parseAIVUnits leest systemctl show met meerdere units (blokken met een
// lege regel ertussen). Units die niet bestaan, vallen weg.
func parseAIVUnits(out string) []protocol.AIVUnit {
	var list []protocol.AIVUnit
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n\n") {
		u := protocol.AIVUnit{}
		load := ""
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "Id":
				u.Name = v
			case "ActiveState":
				u.Active = v
			case "SubState":
				u.Sub = v
			case "Result":
				u.Result = v
			case "StateChangeTimestamp":
				if n, err := strconv.ParseInt(strings.TrimPrefix(v, "@"), 10, 64); err == nil {
					u.Since = n
				}
			case "LoadState":
				load = v
			}
		}
		if u.Name == "" || load == "not-found" || (u.Active == "inactive" && u.Sub == "dead" && u.Since == 0 && u.Result == "") {
			continue
		}
		list = append(list, u)
	}
	return list
}

// parseDFPct leest het gebruik uit df -P.
func parseDFPct(out string) float64 {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	for _, f := range fields {
		if strings.HasSuffix(f, "%") {
			if n, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// aivClient praat met de lokale API.
type aivClient struct {
	h    pveHost
	base string
	http *http.Client
}

func (h pveHost) aivClient(timeout time.Duration) *aivClient {
	return &aivClient{h: h, base: h.aivBase(), http: aivHTTP(timeout)}
}

func (c *aivClient) token() string {
	data, _ := os.ReadFile(c.h.path(aivTokenFile))
	return strings.TrimSpace(string(data))
}

// sameSite zet de koppen waarmee de guard van AI-verslag een verzoek als
// "van dezelfde site" aanvaardt (alleen voor het koppelen zelf).
func (c *aivClient) sameSite(req *http.Request) {
	req.Header.Set("Origin", c.base)
	req.Header.Set("X-Requested-With", "aiverslag")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}

func (c *aivClient) send(ctx context.Context, method, path string, query url.Values, body []byte, bearer string, same bool) (int, []byte, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cf-agent (ClusterForge)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if method != http.MethodGet {
		// Schrijfroutes van Inzicht (/api/inzicht/…) vragen deze kop.
		req.Header.Set("X-Inzicht", "1")
	}
	if same {
		c.sameSite(req)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("AI-verslag niet bereikbaar op %s: %w", c.base, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, pveGetMax+1))
	if err != nil {
		return res.StatusCode, nil, err
	}
	if len(data) > pveGetMax {
		return res.StatusCode, nil, fmt.Errorf("antwoord van %s is te groot", path)
	}
	return res.StatusCode, data, nil
}

// aivDetail haalt de foutmelding uit een antwoord van FastAPI.
func aivDetail(code int, data []byte) error {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		for _, k := range []string{"detail", "fout", "error", "melding"} {
			if s, ok := m[k].(string); ok && s != "" {
				return fmt.Errorf("AI-verslag (%d): %s", code, s)
			}
		}
	}
	txt := strings.TrimSpace(string(data))
	if len(txt) > 200 {
		txt = txt[:200]
	}
	if txt == "" {
		txt = http.StatusText(code)
	}
	return fmt.Errorf("AI-verslag (%d): %s", code, txt)
}

// valid zegt of de sleutel nog werkt (/api/app/start vraagt een app-sleutel).
func (c *aivClient) valid(ctx context.Context, tok string) bool {
	if tok == "" {
		return false
	}
	code, _, err := c.send(ctx, http.MethodGet, "/api/app/start", nil, nil, tok, false)
	return err == nil && code == http.StatusOK
}

// pair koppelt de agent als app en bewaart de sleutel. Oudere koppelingen
// met dezelfde naam worden daarna ingetrokken.
func (c *aivClient) pair(ctx context.Context) (string, error) {
	aivPairMu.Lock()
	defer aivPairMu.Unlock()
	// Een ander verzoek kan intussen gekoppeld hebben.
	if tok := c.token(); c.valid(ctx, tok) {
		return tok, nil
	}
	body, _ := json.Marshal(map[string]string{"naam": aivAppName})
	code, data, err := c.send(ctx, http.MethodPost, "/api/app/tokens", nil, body, "", true)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("koppelcode vragen mislukt: %w", aivDetail(code, data))
	}
	var pc struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(data, &pc) != nil || pc.Code == "" {
		return "", errors.New("AI-verslag gaf geen koppelcode")
	}
	body, _ = json.Marshal(map[string]string{"code": pc.Code})
	code, data, err = c.send(ctx, http.MethodPost, "/api/app/pair", nil, body, "", false)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("koppelen mislukt: %w", aivDetail(code, data))
	}
	var pr struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &pr) != nil || pr.Token == "" {
		return "", errors.New("AI-verslag gaf geen sleutel")
	}
	if err := os.MkdirAll(c.h.path("/etc/clusterforge"), 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(c.h.path(aivTokenFile), []byte(pr.Token+"\n"), 0o600); err != nil {
		return "", err
	}
	c.forgetOld(ctx, pr.Token)
	return pr.Token, nil
}

// forgetOld trekt andere koppelingen met de naam ClusterForge in; de
// nieuwste (de onze) blijft.
func (c *aivClient) forgetOld(ctx context.Context, tok string) {
	code, data, err := c.send(ctx, http.MethodGet, "/api/app/tokens", nil, nil, tok, false)
	if err != nil || code != http.StatusOK {
		return
	}
	var list struct {
		Tokens []struct {
			ID   int    `json:"id"`
			Naam string `json:"naam"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &list) != nil {
		return
	}
	var ids []int
	for _, t := range list.Tokens {
		if t.Naam == aivAppName {
			ids = append(ids, t.ID)
		}
	}
	sort.Ints(ids)
	for i := 0; i+1 < len(ids); i++ {
		_, _, _ = c.send(ctx, http.MethodDelete, "/api/app/tokens/"+strconv.Itoa(ids[i]), nil, nil, tok, false)
	}
}

// do voert een verzoek uit met de app-sleutel; zonder (werkende) sleutel
// koppelt de agent eerst.
func (c *aivClient) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	tok := c.token()
	var err error
	if tok == "" {
		if tok, err = c.pair(ctx); err != nil {
			return nil, err
		}
	}
	code, data, err := c.send(ctx, method, path, query, body, tok, false)
	if err != nil {
		return nil, err
	}
	if (code == http.StatusUnauthorized || code == http.StatusForbidden) && !c.valid(ctx, tok) {
		if tok, err = c.pair(ctx); err != nil {
			return nil, err
		}
		if code, data, err = c.send(ctx, method, path, query, body, tok, false); err != nil {
			return nil, err
		}
	}
	if code < 200 || code > 299 {
		return nil, aivDetail(code, data)
	}
	return aivJSON(data), nil
}

// aivJSON maakt van een antwoord altijd JSON.
func aivJSON(data []byte) json.RawMessage {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(data) {
		return data
	}
	out, _ := json.Marshal(string(data))
	return out
}

// aivGet is een leesverzoek van de server.
func (h pveHost) aivGet(ctx context.Context, path string, params map[string]string) (json.RawMessage, error) {
	if !h.isAIV() {
		return nil, errNotAIV
	}
	if !protocol.ValidAIVPath(path) {
		return nil, fmt.Errorf("ongeldig pad %q", path)
	}
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return h.aivClient(60*time.Second).do(ctx, http.MethodGet, path, q, nil)
}

// aivCall voert een schrijvende aanroep uit als run.
func (r *RunExecutor) aivCall(ctx context.Context, c *protocol.AIVCallSpec, out io.Writer, st *protocol.RunStatus) (int, error) {
	h := r.host()
	if !h.isAIV() {
		return -1, errNotAIV
	}
	if err := c.Validate(); err != nil {
		return -1, err
	}
	_, _ = fmt.Fprintf(out, "\n$ %s %s (AI-verslag, app-sleutel %s)\n", c.Method, c.Path, aivAppName)
	var body []byte
	if c.Method == http.MethodPost {
		body = c.Body
		if len(body) == 0 {
			body = []byte("{}")
		}
	}
	data, err := h.aivClient(2*time.Minute).do(ctx, c.Method, c.Path, nil, body)
	if err != nil {
		_, _ = fmt.Fprintln(out, err.Error())
		return 1, err
	}
	txt := string(data)
	if len(txt) > 2000 {
		txt = txt[:2000] + " …"
	}
	_, _ = fmt.Fprintln(out, txt)
	if len(data) <= 256<<10 {
		st.PVE = &protocol.PVERunResult{Data: data}
	}
	return 0, nil
}

// aivPost is een korte POST van de server (alleen AIVPostPaths).
func (h pveHost) aivPost(ctx context.Context, path string, body json.RawMessage) (json.RawMessage, error) {
	if !h.isAIV() {
		return nil, errNotAIV
	}
	if !protocol.AIVPostPaths[path] {
		return nil, fmt.Errorf("%q is geen pad voor aiv_post", path)
	}
	return h.aivClient(60*time.Second).do(ctx, http.MethodPost, path, nil, body)
}

// aivPython is de Python van AI-verslag (venv), anders python3.
func (h pveHost) aivPython() string {
	if _, err := os.Stat(h.path(aivDir + "/venv/bin/python")); err == nil {
		return aivDir + "/venv/bin/python"
	}
	return "python3"
}

// aivInstall zet een extensie in AI-verslag: het bestand naast app.py, de
// regel onderaan app.py, de webdienst herstarten en nakijken dat ze
// antwoordt. Lukt dat niet, dan gaan app.py en het oude bestand terug.
func (r *RunExecutor) aivInstall(ctx context.Context, e *protocol.AIVExtension, out io.Writer) (int, error) {
	h := r.host()
	if !h.isAIV() {
		return -1, errNotAIV
	}
	if err := e.Validate(); err != nil {
		return -1, err
	}
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
	app := h.path(aivDir + "/app.py")
	file := h.path(aivDir + "/" + e.Name + ".py")
	hook := fmt.Sprintf("import %s; %s.routes(app, guard)", e.Name, e.Name)
	appData, err := os.ReadFile(app)
	if err != nil {
		return 1, err
	}
	fi, err := os.Stat(app)
	if err != nil {
		return 1, err
	}
	oldExt, oldErr := os.ReadFile(file)

	// Eerst nakijken dat Python de code aanvaardt, voor er iets verandert.
	tmp := file + ".cf-nieuw.py"
	if err := os.WriteFile(tmp, []byte(e.Code), 0o644); err != nil {
		return 1, err
	}
	defer func() { _ = os.Remove(tmp) }()
	logf("$ %s -m py_compile %s", h.aivPython(), e.Name+".py")
	if out, err := h.exec(ctx, h.aivPython(), "-m", "py_compile", tmp); err != nil {
		return 1, fmt.Errorf("python aanvaardt %s niet: %v %s", e.Name, err, lastLines(string(out), 3))
	}
	if pyc, _ := filepath.Glob(filepath.Join(filepath.Dir(tmp), "__pycache__", strings.TrimSuffix(filepath.Base(tmp), ".py")+".*.pyc")); len(pyc) > 0 {
		for _, f := range pyc {
			_ = os.Remove(f)
		}
	}

	restore := func(reason error) (int, error) {
		logf("Terugzetten: %v", reason)
		_ = writeFileAtomic(app, appData, fi.Mode().Perm())
		if oldErr == nil {
			_ = writeFileAtomic(file, oldExt, 0o644)
		} else {
			_ = os.Remove(file)
		}
		_, _ = h.exec(ctx, "systemctl", "restart", "aiverslag-web.service")
		return 1, fmt.Errorf("%s installeren mislukt, alles teruggezet: %w", e.Name, reason)
	}
	if err := writeFileAtomic(file, []byte(e.Code), 0o644); err != nil {
		return 1, err
	}
	aivChown(file, fi)
	logf("%s.py geschreven (%d bytes, sha256 %s)", e.Name, len(e.Code), e.SHA256[:12])
	if !strings.Contains(string(appData), hook) {
		_ = writeFileAtomic(app+".cf-kopie", appData, fi.Mode().Perm())
		nieuw := strings.TrimRight(string(appData), "\n") + "\n" + hook + "\n"
		if err := writeFileAtomic(app, []byte(nieuw), fi.Mode().Perm()); err != nil {
			return restore(err)
		}
		aivChown(app, fi)
		logf("app.py: regel %q toegevoegd (kopie in app.py.cf-kopie)", hook)
	} else {
		logf("app.py laadt %s al", e.Name)
	}
	logf("$ systemctl restart aiverslag-web.service")
	if _, err := h.exec(ctx, "systemctl", "restart", "aiverslag-web.service"); err != nil {
		return restore(fmt.Errorf("herstarten: %w", err))
	}
	c := h.aivClient(10 * time.Second)
	deadline := time.Now().Add(aivStartWait)
	for {
		if _, err := c.do(ctx, http.MethodGet, "/api/inzicht/clusterforge", nil, nil); err == nil {
			break
		} else if time.Now().After(deadline) {
			return restore(fmt.Errorf("de webdienst antwoordt na de herstart niet met %s: %w", e.Name, err))
		}
		select {
		case <-ctx.Done():
			return restore(ctx.Err())
		case <-time.After(aivStartPoll):
		}
	}
	logf("AI-verslag draait met %s.", e.Name)
	return 0, nil
}

var (
	aivStartWait = 90 * time.Second
	aivStartPoll = 2 * time.Second
)
