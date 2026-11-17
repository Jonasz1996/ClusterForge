package agent

// Fase 35: publicatie. De agent brengt de diensten van de node in kaart,
// praat met een lokale webdienst (NPM, Authentik) op 127.0.0.1, test een
// gepubliceerd adres en maakt, controleert en zet kopieën van Nginx Proxy
// Manager terug. Welke aanroepen er gebeuren, beslist de server
// (internal/publish); hier staat alleen hoe.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func (a *Agent) onWeb(m *nats.Msg) {
	go func() {
		res := a.handleWeb(m.Data)
		data, err := envelope(protocol.TypeWebResult, res)
		if err != nil {
			return
		}
		if len(data) > protocol.MaxWebResponse+(64<<10) {
			data, _ = envelope(protocol.TypeWebResult, protocol.WebResponse{Error: "het antwoord is te groot voor ClusterForge"})
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op webverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleWeb(data []byte) protocol.WebResponse {
	var env protocol.Envelope
	var req protocol.WebRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeWebRequest {
		return protocol.WebResponse{Error: "ongeldig webverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.WebResponse{Error: "ongeldig webverzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.WebResponse{Error: "webverzoek verlopen; klopt de klok van deze node?"}
	}
	if err := req.Validate(); err != nil {
		return protocol.WebResponse{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.WebRequestTimeout)
	defer cancel()
	return a.web(ctx, req)
}

func (a *Agent) web(ctx context.Context, req protocol.WebRequest) protocol.WebResponse {
	switch req.Op {
	case protocol.WebOpDiscover:
		return protocol.WebResponse{Services: a.discover(ctx, req.Probe)}
	case protocol.WebOpHTTP:
		status, body, err := localHTTP(ctx, a.webDial, req.HTTP)
		if err != nil {
			return protocol.WebResponse{Error: err.Error()}
		}
		return protocol.WebResponse{Status: status, Body: body}
	case protocol.WebOpCheckURL:
		res := checkURL(ctx, a.webDial, req.Check)
		return protocol.WebResponse{Check: &res}
	case protocol.WebOpAppStats:
		res, err := a.appStats(ctx, req.App)
		if err != nil {
			return protocol.WebResponse{Error: err.Error()}
		}
		return protocol.WebResponse{App: res}
	case protocol.WebOpNPMSnapshot:
		res, err := a.npmSnapshot(ctx)
		if err != nil {
			return protocol.WebResponse{Error: err.Error(), NPM: res}
		}
		return protocol.WebResponse{NPM: res}
	case protocol.WebOpNPMCheck:
		res, err := a.npmCheck(ctx, *req.NPM)
		if err != nil {
			return protocol.WebResponse{Error: err.Error(), NPM: res}
		}
		return protocol.WebResponse{NPM: res}
	case protocol.WebOpNPMRestore:
		res, err := a.npmRestore(ctx, req.NPM.Snapshot)
		if err != nil {
			return protocol.WebResponse{Error: err.Error(), NPM: res}
		}
		return protocol.WebResponse{NPM: res}
	}
	return protocol.WebResponse{Error: "onbekend webverzoek " + req.Op}
}

// webDial verbindt; tests vervangen WebDial om naar een testserver te gaan.
func (a *Agent) webDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if a.WebDial != nil {
		return a.WebDial(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// localHTTP doet één aanroep naar 127.0.0.1:<poort>.
func localHTTP(ctx context.Context, dial dialFunc, h *protocol.WebHTTP) (int, json.RawMessage, error) {
	scheme := "http"
	if h.HTTPS {
		scheme = "https"
	}
	var body io.Reader
	if len(h.Body) > 0 {
		body = bytes.NewReader(h.Body)
	}
	req, err := http.NewRequestWithContext(ctx, h.Method, scheme+"://127.0.0.1:"+strconv.Itoa(h.Port)+h.Path, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}
	if len(h.Body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	c := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext:     dial,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 127.0.0.1, eigen dienst
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("127.0.0.1:%d antwoordt niet: %w", h.Port, unwrapURLError(err))
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, protocol.MaxWebResponse+1))
	if err != nil {
		return res.StatusCode, nil, err
	}
	if len(data) > protocol.MaxWebResponse {
		return res.StatusCode, nil, fmt.Errorf("het antwoord van 127.0.0.1:%d is groter dan %d kB", h.Port, protocol.MaxWebResponse>>10)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return res.StatusCode, nil, nil
	}
	if json.Valid(data) {
		return res.StatusCode, data, nil
	}
	s, _ := json.Marshal(string(data))
	return res.StatusCode, s, nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// checkURL vraagt een adres op zoals een browser, zonder doorverwijzingen
// te volgen. Een ongeldig certificaat komt in CertError; daarna probeert
// hij zonder controle om toch de status te zien.
func checkURL(ctx context.Context, dial dialFunc, c *protocol.WebCheck) protocol.WebCheckResult {
	u, _ := url.Parse(c.URL)
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	connect := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if c.Connect != "" {
			addr = net.JoinHostPort(c.Connect, port)
		}
		return dial(ctx, network, addr)
	}
	get := func(insecure bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "ClusterForge-publicatie/1")
		client := &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: connect,
				TLSClientConfig: &tls.Config{
					ServerName:         u.Hostname(),
					InsecureSkipVerify: insecure, //nolint:gosec // alleen om de status te zien na een certificaatfout
				},
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		return client.Do(req)
	}
	var out protocol.WebCheckResult
	res, err := get(false)
	if err != nil && u.Scheme == "https" {
		var ce *tls.CertificateVerificationError
		var he x509.HostnameError
		var ue x509.UnknownAuthorityError
		var ie x509.CertificateInvalidError
		inner := unwrapURLError(err)
		if errors.As(inner, &ce) || errors.As(inner, &he) || errors.As(inner, &ue) || errors.As(inner, &ie) {
			out.CertError = inner.Error()
			res, err = get(true)
		}
	}
	if err != nil {
		out.Error = unwrapURLError(err).Error()
		return out
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	out.Status = res.StatusCode
	out.Location = res.Header.Get("Location")
	out.Server = res.Header.Get("Server")
	if res.TLS != nil && len(res.TLS.PeerCertificates) > 0 {
		cert := res.TLS.PeerCertificates[0]
		out.CertSubject = cert.Subject.CommonName
		if out.CertSubject == "" && len(cert.DNSNames) > 0 {
			out.CertSubject = cert.DNSNames[0]
		}
		out.CertIssuer = cert.Issuer.CommonName
		na := cert.NotAfter.UTC()
		out.CertNotAfter = &na
		out.CertOK = out.CertError == ""
	}
	return out
}

// --- ontdekken ---

// owner is van wie een socket is.
type webOwner struct {
	unit      string
	container string
}

var containerCgroup = regexp.MustCompile(`(?:docker-|/docker/|libpod-)([0-9a-f]{64})`)

// containerFromCgroup haalt het id van een container uit /proc/<pid>/cgroup.
func containerFromCgroup(s string) string {
	if m := containerCgroup.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// socketOwners koppelt elk socket-inode aan de unit en de container van het
// proces dat hem open heeft.
func (c *Collector) webSocketOwners() map[string]webOwner {
	out := map[string]webOwner{}
	procs, err := os.ReadDir(c.path("/proc"))
	if err != nil {
		return out
	}
	for _, p := range procs {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		cg := c.read("/proc/" + p.Name() + "/cgroup")
		o := webOwner{unit: unitFromCgroup(cg), container: containerFromCgroup(cg)}
		if o.unit == "" && o.container == "" {
			continue
		}
		fds, err := os.ReadDir(c.path("/proc/" + p.Name() + "/fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(c.path("/proc/" + p.Name() + "/fd/" + fd.Name()))
			if err != nil {
				continue
			}
			if inode, ok := strings.CutPrefix(target, "socket:["); ok {
				inode = strings.TrimSuffix(inode, "]")
				if _, taken := out[inode]; !taken {
					out[inode] = o
				}
			}
		}
	}
	return out
}

// dockerContainer is wat de ontdekking van een container gebruikt.
type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

func (d dockerContainer) name() string {
	if len(d.Names) > 0 {
		return strings.TrimPrefix(d.Names[0], "/")
	}
	if len(d.ID) > 12 {
		return d.ID[:12]
	}
	return d.ID
}

// containers geeft de draaiende containers; nil zonder Docker.
func (a *Agent) containers(ctx context.Context) []dockerContainer {
	d := a.docker()
	if !d.present() {
		return nil
	}
	status, body, err := d.do(ctx, http.MethodGet, "/containers/json", nil, nil, protocol.MaxDockerResponse)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var list []dockerContainer
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	return list
}

// discover geeft de diensten van de node: elke luisterende poort (niet op
// loopback, niet tijdelijk) en elke gepubliceerde poort van een container.
func (a *Agent) discover(ctx context.Context, probe bool) []protocol.WebService {
	c := a.Collector
	if c == nil {
		c = &Collector{Root: a.Root}
	}
	owners := c.webSocketOwners()
	containers := a.containers(ctx)
	byID := map[string]dockerContainer{}
	for _, ct := range containers {
		byID[ct.ID] = ct
	}
	lo, hi := c.ephemeral()
	seen := map[string]int{}
	var out []protocol.WebService
	add := func(s protocol.WebService) {
		key := s.Proto + "/" + strconv.Itoa(s.Port)
		if i, ok := seen[key]; ok {
			// Dezelfde poort op IPv4 en IPv6, of een gepubliceerde poort die
			// ook als docker-proxy luistert: vul aan.
			cur := &out[i]
			if cur.Container == "" {
				cur.Container, cur.Image, cur.Project = s.Container, s.Image, s.Project
			}
			cur.Published = cur.Published || s.Published
			if cur.Addr != "0.0.0.0" && (s.Addr == "0.0.0.0" || s.Addr == "::") {
				cur.Addr = s.Addr
			}
			return
		}
		seen[key] = len(out)
		out = append(out, s)
	}
	setContainer := func(s *protocol.WebService, ct dockerContainer) {
		s.Container, s.Image, s.Project = ct.name(), ct.Image, ct.Labels["com.docker.compose.project"]
	}
	for _, proto := range []string{"tcp", "udp"} {
		for _, so := range c.sockets(proto) {
			if proto == "tcp" && so.state != "0A" || proto == "udp" && (so.state != "07" || so.rport != 0) {
				continue
			}
			if so.lport >= lo && so.lport <= hi || so.local.IsLoopback() {
				continue
			}
			o := owners[so.inode]
			s := protocol.WebService{Proto: proto, Addr: so.local.String(), Port: so.lport, Unit: o.unit}
			if ct, ok := byID[o.container]; ok {
				setContainer(&s, ct)
				s.Unit = ""
			}
			add(s)
		}
	}
	for _, ct := range containers {
		for _, p := range ct.Ports {
			if p.PublicPort == 0 || strings.HasPrefix(p.IP, "127.") || p.IP == "::1" {
				continue
			}
			proto := p.Type
			if proto == "" {
				proto = "tcp"
			}
			addr := p.IP
			if addr == "" {
				addr = "0.0.0.0"
			}
			s := protocol.WebService{Proto: proto, Addr: addr, Port: p.PublicPort, Published: true}
			setContainer(&s, ct)
			add(s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	if len(out) > protocol.MaxDiscoverPorts {
		out = out[:protocol.MaxDiscoverPorts]
	}
	for i := range out {
		if out[i].Proto == "udp" {
			out[i].Kind = protocol.WebKindUDP
		}
	}
	if probe {
		a.webProbe(ctx, out)
	}
	return out
}

// noProbe zijn poorten die geen web spreken; daar stuurt de agent niets heen
// (een TLS-groet in MariaDB geeft alleen een waarschuwing in zijn log).
var noProbe = map[int]bool{
	22: true, 25: true, 53: true, 110: true, 111: true, 143: true, 389: true, 445: true, 465: true, 514: true,
	587: true, 636: true, 993: true, 995: true, 2049: true, 2379: true, 2380: true, 3306: true, 3307: true,
	4222: true, 4444: true, 4567: true, 4568: true, 5432: true, 5433: true, 6379: true, 6380: true,
	10050: true, 10051: true, 11211: true, 27017: true,
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// probe spreekt elke TCP-poort aan: eerst TLS, dan gewoon HTTP.
func (a *Agent) webProbe(ctx context.Context, svcs []protocol.WebService) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := range svcs {
		s := &svcs[i]
		if s.Proto != "tcp" {
			continue
		}
		if noProbe[s.Port] {
			s.Kind = protocol.WebKindTCP
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			host := s.Addr
			ip := net.ParseIP(host)
			if ip == nil || ip.IsUnspecified() {
				host = "127.0.0.1"
				if ip != nil && ip.To4() == nil {
					host = "::1"
				}
			}
			probeOne(ctx, a.webDial, host, s)
		}()
	}
	wg.Wait()
}

func probeOne(ctx context.Context, dial dialFunc, host string, s *protocol.WebService) {
	s.Kind = protocol.WebKindTCP
	addr := net.JoinHostPort(host, strconv.Itoa(s.Port))
	for _, scheme := range []string{"https", "http"} {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, scheme+"://"+addr+"/", nil)
		if err != nil {
			cancel()
			return
		}
		req.Header.Set("User-Agent", "ClusterForge-ontdekking/1")
		client := &http.Client{
			Transport: &http.Transport{
				DialContext:       dial,
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // alleen kijken wat er antwoordt
				DisableKeepAlives: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		res, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		_ = res.Body.Close()
		cancel()
		s.Kind = scheme
		s.Status = res.StatusCode
		s.Server = trim(res.Header.Get("Server"), 80)
		s.Location = trim(res.Header.Get("Location"), 200)
		if m := titleRe.FindSubmatch(data); m != nil {
			s.Title = trim(strings.Join(strings.Fields(htmlUnescape(string(m[1]))), " "), 120)
		}
		return
	}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var htmlEntities = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")

func htmlUnescape(s string) string { return htmlEntities.Replace(s) }

// --- Nginx Proxy Manager ---

const (
	// npmSnapshotDir is waar de kopieën van vóór een wijziging staan.
	npmSnapshotDir = "/var/lib/cf-npm/cf-snapshots"
	// npmKeep is hoeveel kopieën de agent houdt.
	npmKeep = 10
)

// npmSnapshots is de map met kopieën.
func (a *Agent) npmSnapshots() string {
	if a.NPMSnapshotDir != "" {
		return a.NPMSnapshotDir
	}
	return a.rootPath(npmSnapshotDir)
}

// npmInstance is de NPM op deze node.
type npmInstance struct {
	container string
	dataDir   string
	// adminPort is de poort van de beheer-API op de host.
	adminPort int
}

// findNPM zoekt de draaiende container van NPM en zijn map /data.
func (a *Agent) findNPM(ctx context.Context) (npmInstance, error) {
	var found []dockerContainer
	for _, ct := range a.containers(ctx) {
		if a.NPMContainer != "" && ct.name() != a.NPMContainer {
			continue
		}
		if strings.Contains(ct.Image, "nginx-proxy-manager") && ct.State == "running" {
			found = append(found, ct)
		}
	}
	if len(found) == 0 {
		return npmInstance{}, errors.New("hier draait geen Nginx Proxy Manager (geen container met image nginx-proxy-manager); staat het VIP op een andere node?")
	}
	// De NPM van het cluster (fase 11) heet cf-npm.
	slices.SortFunc(found, func(x, y dockerContainer) int {
		if x.name() == "cf-npm" {
			return -1
		}
		if y.name() == "cf-npm" {
			return 1
		}
		return strings.Compare(x.name(), y.name())
	})
	ct := found[0]
	_, body, err := a.docker().do(ctx, http.MethodGet, "/containers/"+ct.ID+"/json", nil, nil, protocol.MaxDockerResponse)
	if err != nil {
		return npmInstance{}, err
	}
	var in struct {
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
		HostConfig struct {
			NetworkMode string `json:"NetworkMode"`
		} `json:"HostConfig"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return npmInstance{}, fmt.Errorf("inspect van %s: %w", ct.name(), err)
	}
	n := npmInstance{container: ct.name(), adminPort: 81}
	for _, m := range in.Mounts {
		if m.Destination == "/data" {
			n.dataDir = m.Source
		}
	}
	if n.dataDir == "" {
		return npmInstance{}, fmt.Errorf("de container %s heeft geen map /data van de host; ClusterForge kan zijn database niet veilig kopiëren", ct.name())
	}
	for _, b := range in.NetworkSettings.Ports["81/tcp"] {
		if p, err := strconv.Atoi(b.HostPort); err == nil && p > 0 {
			n.adminPort = p
			break
		}
	}
	return n, nil
}

// sqliteBackup maakt met sqlite3 .backup een consistente kopie, ook terwijl
// NPM schrijft, en controleert die.
func (a *Agent) sqliteBackup(ctx context.Context, db, dst string) error {
	if err := a.ensureSQLite(ctx); err != nil {
		return err
	}
	if out, err := a.exec(ctx, "sqlite3", db, ".backup '"+dst+"'"); err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "executable file not found") {
			return errors.New("sqlite3 ontbreekt op deze node (apt install sqlite3)")
		}
		return fmt.Errorf("sqlite3 .backup: %s", webFirst(out, err))
	}
	return a.sqliteIntegrity(ctx, dst)
}

// ensureSQLite installeert sqlite3 als het ontbreekt, zodat Jonas niets
// met de hand op de NPM-nodes hoeft te zetten.
func (a *Agent) ensureSQLite(ctx context.Context) error {
	if _, err := a.exec(ctx, "sqlite3", "-version"); err == nil {
		return nil
	}
	_, _, err := a.applyPackage(ctx, &protocol.PackageStep{Names: []string{"sqlite3"}, State: "present"})
	if err != nil {
		return fmt.Errorf("sqlite3 ontbreekt en installeren lukte niet: %w", err)
	}
	return nil
}

func (a *Agent) sqliteIntegrity(ctx context.Context, db string) error {
	out, err := a.exec(ctx, "sqlite3", db, "PRAGMA integrity_check;")
	if err != nil {
		return fmt.Errorf("integrity_check: %s", webFirst(out, err))
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("integrity_check: %s", webFirst(out, nil))
	}
	return nil
}

func webFirst(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if s == "" && err != nil {
		s = err.Error()
	}
	s, _, _ = strings.Cut(s, "\n")
	return trim(s, 300)
}

func snapshotName(now time.Time) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return now.UTC().Format("20060102-150405") + "-" + string(b)
}

// npmSnapshot kopieert de database (sqlite3 .backup) en data/nginx (de
// configs die NPM uit de database maakte) naar een nieuwe kopie.
func (a *Agent) npmSnapshot(ctx context.Context) (*protocol.WebNPMResult, error) {
	n, err := a.findNPM(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	res := &protocol.WebNPMResult{Container: n.container, DataDir: n.dataDir, Snapshot: snapshotName(now())}
	base := a.npmSnapshots()
	dir := filepath.Join(base, res.Snapshot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	data := a.rootPath(n.dataDir)
	if err := a.sqliteBackup(ctx, filepath.Join(data, "database.sqlite"), filepath.Join(dir, "database.sqlite")); err != nil {
		_ = os.RemoveAll(dir)
		return res, err
	}
	if err := copyTree(filepath.Join(data, "nginx"), filepath.Join(dir, "nginx")); err != nil {
		_ = os.RemoveAll(dir)
		return res, fmt.Errorf("data/nginx kopiëren: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), fmt.Appendf(nil, "container=%s\ndata=%s\n", n.container, n.dataDir), 0o600); err != nil {
		return res, err
	}
	res.Checks = []protocol.WebCheckItem{{Name: "kopie", OK: true, Detail: "database (integrity_check ok) en data/nginx in " + dir}}
	pruneSnapshots(base, npmKeep)
	return res, nil
}

// pruneSnapshots houdt de nieuwste keep kopieën.
func pruneSnapshots(base string, keep int) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && protocol.ValidWebSnapshot(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.RemoveAll(filepath.Join(base, names[0]))
		names = names[1:]
	}
}

// copyTree kopieert een map met gewone bestanden, mappen en links.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == src {
				return os.MkdirAll(dst, 0o755)
			}
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(target, fi.Mode().Perm())
		case fi.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case fi.Mode().IsRegular():
			return copyFile(p, target, fi.Mode().Perm())
		}
		return nil
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// npmCheck controleert NPM na een wijziging: database en config horen bij
// elkaar. Een mislukte controle is geen fout van het verzoek; de server
// leest Checks.
func (a *Agent) npmCheck(ctx context.Context, in protocol.WebNPM) (*protocol.WebNPMResult, error) {
	n, err := a.findNPM(ctx)
	if err != nil {
		return nil, err
	}
	res := &protocol.WebNPMResult{Container: n.container, DataDir: n.dataDir}
	add := func(name string, ok bool, format string, args ...any) {
		res.Checks = append(res.Checks, protocol.WebCheckItem{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
	}
	data := a.rootPath(n.dataDir)

	// 1. De database: een consistente kopie, integrity_check en de rij.
	tmp, err := os.MkdirTemp("", "cf-npm-check-")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	copyDB := filepath.Join(tmp, "database.sqlite")
	if err := a.sqliteBackup(ctx, filepath.Join(data, "database.sqlite"), copyDB); err != nil {
		add("database", false, "%v", err)
	} else if in.HostID == 0 {
		add("database", true, "integrity_check ok")
	} else {
		out, err := a.exec(ctx, "sqlite3", "-separator", "|", copyDB,
			fmt.Sprintf("SELECT is_deleted, enabled, domain_names FROM proxy_host WHERE id = %d;", in.HostID))
		row := strings.TrimSpace(string(out))
		switch {
		case err != nil:
			add("database", false, "proxy_host %d lezen: %s", in.HostID, webFirst(out, err))
		case in.Absent:
			if row == "" || strings.HasPrefix(row, "1|") {
				add("database", true, "proxy_host %d is weg (integrity_check ok)", in.HostID)
			} else {
				add("database", false, "proxy_host %d staat nog in de database", in.HostID)
			}
		case row == "":
			add("database", false, "proxy_host %d staat niet in de database", in.HostID)
		case strings.HasPrefix(row, "1|"):
			add("database", false, "proxy_host %d is in de database als verwijderd gemarkeerd", in.HostID)
		case in.Domain != "" && !strings.Contains(row, `"`+in.Domain+`"`):
			add("database", false, "proxy_host %d heeft in de database niet de naam %s: %s", in.HostID, in.Domain, trim(row, 200))
		default:
			add("database", true, "proxy_host %d in de database (integrity_check ok)", in.HostID)
		}
	}

	// 2. De config die NPM uit de database maakte, en geen .err.
	if in.HostID > 0 {
		conf := filepath.Join(data, "nginx", "proxy_host", strconv.Itoa(in.HostID)+".conf")
		b, err := os.ReadFile(conf)
		switch {
		case in.Absent && os.IsNotExist(err):
			add("config", true, "%d.conf is weg", in.HostID)
		case in.Absent:
			add("config", false, "%d.conf bestaat nog", in.HostID)
		case err != nil:
			add("config", false, "%d.conf ontbreekt: NPM maakte geen config uit de database", in.HostID)
		case in.Domain != "" && !regexp.MustCompile(`server_name[^;]*\s`+regexp.QuoteMeta(in.Domain)+`[\s;]`).Match(b):
			add("config", false, "%d.conf bevat server_name %s niet", in.HostID, in.Domain)
		default:
			add("config", true, "%d.conf bestaat en hoort bij de database", in.HostID)
		}
		if _, err := os.Stat(conf + ".err"); err == nil {
			add("geen fout", false, "%d.conf.err bestaat: nginx weigerde de config van deze host", in.HostID)
		} else {
			add("geen fout", true, "geen %d.conf.err", in.HostID)
		}
	}

	// 3. nginx -t in de container.
	if out, err := a.exec(ctx, "docker", "exec", n.container, "nginx", "-t"); err != nil {
		add("nginx -t", false, "%s", webLast(out, err))
	} else {
		add("nginx -t", true, "%s", webLast(out, nil))
	}
	return res, nil
}

func webLast(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	s := strings.TrimSpace(lines[len(lines)-1])
	if s == "" && err != nil {
		s = err.Error()
	}
	return trim(s, 300)
}

// npmRestore zet een kopie terug: NPM stoppen (geen schrijver meer), de
// database zonder -wal/-shm/-journal en data/nginx terugzetten, NPM starten
// en wachten tot zijn API antwoordt.
func (a *Agent) npmRestore(ctx context.Context, name string) (*protocol.WebNPMResult, error) {
	n, err := a.findNPM(ctx)
	if err != nil {
		return nil, err
	}
	res := &protocol.WebNPMResult{Container: n.container, DataDir: n.dataDir, Snapshot: name}
	dir := filepath.Join(a.npmSnapshots(), name)
	snapDB := filepath.Join(dir, "database.sqlite")
	if _, err := os.Stat(snapDB); err != nil {
		return res, fmt.Errorf("kopie %s bestaat niet op deze node", name)
	}
	if err := a.sqliteIntegrity(ctx, snapDB); err != nil {
		return res, fmt.Errorf("de kopie %s is niet in orde: %w", name, err)
	}
	data := a.rootPath(n.dataDir)
	if out, err := a.execTimeout(ctx, 90*time.Second, "docker", "stop", "-t", "30", n.container); err != nil {
		return res, fmt.Errorf("NPM stoppen: %s", webFirst(out, err))
	}
	restoreErr := func() error {
		db := filepath.Join(data, "database.sqlite")
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			_ = os.Remove(db + suffix)
		}
		if err := copyFile(snapDB, db+".cf-restore", 0o644); err != nil {
			return err
		}
		if err := os.Rename(db+".cf-restore", db); err != nil {
			return err
		}
		nginx := filepath.Join(data, "nginx")
		old := nginx + ".cf-voor-terugzetten"
		_ = os.RemoveAll(old)
		if err := os.Rename(nginx, old); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := copyTree(filepath.Join(dir, "nginx"), nginx); err != nil {
			_ = os.RemoveAll(nginx)
			_ = os.Rename(old, nginx)
			return err
		}
		_ = os.RemoveAll(old)
		return nil
	}()
	// NPM komt altijd terug, ook als het terugzetten mislukte.
	if out, err := a.exec(ctx, "docker", "start", n.container); err != nil {
		return res, fmt.Errorf("NPM starten: %s", webFirst(out, err))
	}
	if restoreErr != nil {
		return res, fmt.Errorf("terugzetten mislukt, NPM draait met wat er stond: %w", restoreErr)
	}
	res.Checks = append(res.Checks, protocol.WebCheckItem{Name: "terugzetten", OK: true, Detail: "database en data/nginx van " + name})
	deadline := time.Now().Add(90 * time.Second)
	for {
		status, _, err := localHTTP(ctx, a.webDial, &protocol.WebHTTP{Method: http.MethodGet, Port: n.adminPort, Path: "/api/"})
		if err == nil && status == http.StatusOK {
			res.Checks = append(res.Checks, protocol.WebCheckItem{Name: "NPM", OK: true, Detail: "API antwoordt weer"})
			return res, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			res.Checks = append(res.Checks, protocol.WebCheckItem{Name: "NPM", OK: false, Detail: "API antwoordt niet binnen 90 s na het terugzetten"})
			return res, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// WebRequest voert een webverzoek uit zoals het van de server zou komen,
// met de controle van de vorm. Voor tests van de server (internal/publish)
// tegen een echte NPM.
func (a *Agent) WebRequest(ctx context.Context, req protocol.WebRequest) protocol.WebResponse {
	if err := req.Validate(); err != nil {
		return protocol.WebResponse{Error: err.Error()}
	}
	return a.web(ctx, req)
}
