package agent

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Verifier voert cf-agent verify uit: de diepe back-upcontrole in een
// teruggezette sandbox. Alles is alleen lezen: systemctl show, systemctl
// --failed, journalctl, verbindingen naar loopback, en psql of mariadb met
// vaste vragen. Uit de aanvraag komen alleen namen, poorten en adressen op
// loopback, nooit SQL of shell.
type Verifier struct {
	Version string
	// Exec voert een programma uit; nil gebruikt os/exec. Bij een fout
	// staat stderr in de fout.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Root is de bestandssysteemwortel voor /proc, /etc en /sys; leeg is
	// "/". Tests gebruiken een map.
	Root string
	// Dial maakt een TCP-verbinding; nil gebruikt net.Dialer. Alleen
	// loopback komt erdoor.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// CheckLimit en Limit zijn de limieten per controle en samen; nul
	// gebruikt protocol.VerifyCheckLimit en protocol.VerifyLimit.
	CheckLimit, Limit time.Duration
	// BootWait is hoe lang verify wacht tot systemd klaar is met
	// opstarten; Settle is hoe lang een poort of adres nog niet hoeft te
	// antwoorden; Retry is de pauze tussen twee pogingen.
	BootWait, Settle, Retry time.Duration
}

const (
	maxVerifyRequest  = 1 << 20
	maxVerifyServices = 64
	maxVerifyTCP      = 32
	maxVerifyHTTP     = 16
)

var (
	unitRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9@._:-]{0,127}$`)
	// dbNameRe zijn de databasenamen die verify als argument gebruikt. Een
	// naam als "host=..." of een URL zou psql als verbindingsgegevens lezen.
	dbNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	// ignoredUnits falen in een sandbox zonder netwerk vrijwel altijd.
	ignoredUnits = []string{"systemd-networkd-wait-online.service", "NetworkManager-wait-online.service", "networking.service"}
)

// ReadVerifyRequest leest en controleert een aanvraag.
func ReadVerifyRequest(in io.Reader) (protocol.VerifyRequest, error) {
	var req protocol.VerifyRequest
	data, err := io.ReadAll(io.LimitReader(in, maxVerifyRequest+1))
	if err != nil {
		return req, fmt.Errorf("aanvraag lezen: %w", err)
	}
	if len(data) > maxVerifyRequest {
		return req, errors.New("de aanvraag is groter dan 1 MB")
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, fmt.Errorf("de aanvraag is geen geldige JSON: %w", err)
	}
	return req, normalize(&req)
}

func normalize(req *protocol.VerifyRequest) error {
	switch {
	case len(req.Services) > maxVerifyServices:
		return fmt.Errorf("hoogstens %d services", maxVerifyServices)
	case len(req.TCP) > maxVerifyTCP:
		return fmt.Errorf("hoogstens %d poorten", maxVerifyTCP)
	case len(req.HTTP) > maxVerifyHTTP:
		return fmt.Errorf("hoogstens %d adressen", maxVerifyHTTP)
	}
	var services []string
	for _, s := range req.Services {
		if !unitRe.MatchString(s) {
			return fmt.Errorf("ongeldige unit %q", s)
		}
		if !slices.Contains(services, s) {
			services = append(services, s)
		}
	}
	req.Services = services
	for i := range req.TCP {
		t := &req.TCP[i]
		if t.Host == "" {
			t.Host = "127.0.0.1"
		}
		if !loopbackHost(t.Host) {
			return fmt.Errorf("poort %d: alleen 127.0.0.1 of ::1, niet %q", t.Port, t.Host)
		}
		if t.Port < 1 || t.Port > 65535 {
			return fmt.Errorf("ongeldige poort %d", t.Port)
		}
		if t.Service != "" && !unitRe.MatchString(t.Service) {
			return fmt.Errorf("ongeldige unit %q", t.Service)
		}
	}
	for i := range req.HTTP {
		h := &req.HTTP[i]
		u, err := url.Parse(h.URL)
		if err != nil || u.Scheme != "http" || u.User != nil || !loopbackHost(u.Hostname()) {
			return fmt.Errorf("adres %q: alleen http://127.0.0.1 of http://[::1]", h.URL)
		}
		if h.Expect == 0 {
			h.Expect = http.StatusOK
		}
		if h.Expect < 100 || h.Expect > 599 {
			return fmt.Errorf("adres %s: ongeldige statuscode %d", h.URL, h.Expect)
		}
	}
	return nil
}

func loopbackHost(h string) bool { return h == "127.0.0.1" || h == "::1" }

// Main leest de aanvraag van in, schrijft het resultaat als JSON naar out en
// geeft de exitcode: 0, ook als controles falen, of 3 bij een ongeldige
// aanvraag.
func (v *Verifier) Main(ctx context.Context, in io.Reader, out io.Writer) int {
	code := 0
	req, err := ReadVerifyRequest(in)
	var res protocol.VerifyResult
	if err != nil {
		res = protocol.VerifyResult{ProtocolVersion: protocol.Version, AgentVersion: v.Version, Error: err.Error(), Checks: []protocol.VerifyCheck{}}
		code = protocol.VerifyInvalid
	} else {
		res = v.Verify(ctx, req)
	}
	_ = json.NewEncoder(out).Encode(res)
	return code
}

// Verify voert de controles uit, binnen de limiet voor alles samen.
func (v *Verifier) Verify(ctx context.Context, req protocol.VerifyRequest) protocol.VerifyResult {
	start := time.Now()
	ctx, cancel := context.WithTimeoutCause(ctx, or(v.Limit, protocol.VerifyLimit),
		fmt.Errorf("de limiet van %s voor alle controles is bereikt", or(v.Limit, protocol.VerifyLimit)))
	defer cancel()
	res := protocol.VerifyResult{ProtocolVersion: protocol.Version, AgentVersion: v.Version, Checks: []protocol.VerifyCheck{}}
	add := func(c protocol.VerifyCheck) { res.Checks = append(res.Checks, c) }

	v.waitBoot(ctx)
	states := v.unitStates(ctx, req.Services)
	byService := map[string]protocol.VerifyCheck{}
	for _, name := range req.Services {
		c := v.service(ctx, name, states[name])
		byService[name] = c
		add(c)
	}
	add(v.failedUnits(ctx, req.Services))
	for _, t := range req.TCP {
		add(v.tcp(ctx, t, byService[t.Service]))
	}
	for _, h := range req.HTTP {
		add(v.http(ctx, h))
	}
	if req.PostgreSQL {
		add(v.postgres(ctx))
	}
	if req.MariaDB {
		add(v.mariadb(ctx))
	}
	res.Seconds = math.Round(time.Since(start).Seconds()*10) / 10
	return res
}

// check geeft de context van één controle.
func (v *Verifier) check(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, or(v.CheckLimit, protocol.VerifyCheckLimit))
}

// waitBoot wacht tot systemd klaar is met opstarten, zodat een service die
// nog start niet als gestopt telt. Lukt dat niet, dan gaan de controles
// gewoon door.
func (v *Verifier) waitBoot(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, or(v.BootWait, 150*time.Second))
	defer cancel()
	_, _ = v.run(ctx, "systemctl", "is-system-running", "--wait")
}

// unitState is wat systemctl show over een unit zegt.
type unitState struct {
	Load, Active, Sub, Result string
	err                       error
}

func unitFile(s string) string {
	for _, suffix := range []string{".service", ".socket", ".timer", ".mount", ".target", ".path"} {
		if strings.HasSuffix(s, suffix) {
			return s
		}
	}
	return s + ".service"
}

// unitStates leest alle services in één systemctl show. De blokken komen
// in de volgorde van de argumenten.
func (v *Verifier) unitStates(ctx context.Context, names []string) map[string]unitState {
	out := map[string]unitState{}
	if len(names) == 0 {
		return out
	}
	ctx, cancel := v.check(ctx)
	defer cancel()
	args := []string{"show", "--property=Id,LoadState,ActiveState,SubState,Result"}
	for _, n := range names {
		args = append(args, unitFile(n))
	}
	raw, err := v.run(ctx, "systemctl", args...)
	if err != nil && len(raw) == 0 {
		for _, n := range names {
			out[n] = unitState{err: err}
		}
		return out
	}
	blocks := strings.Split(strings.TrimSpace(string(raw)), "\n\n")
	for i, n := range names {
		if i >= len(blocks) {
			out[n] = unitState{err: errors.New("systemctl show gaf geen antwoord")}
			continue
		}
		kv := map[string]string{}
		for line := range strings.SplitSeq(blocks[i], "\n") {
			if k, val, ok := strings.Cut(line, "="); ok {
				kv[k] = val
			}
		}
		out[n] = unitState{Load: kv["LoadState"], Active: kv["ActiveState"], Sub: kv["SubState"], Result: kv["Result"]}
	}
	return out
}

func (v *Verifier) service(ctx context.Context, name string, st unitState) protocol.VerifyCheck {
	c := protocol.VerifyCheck{Kind: "service", Name: name}
	ctx, cancel := v.check(ctx)
	defer cancel()
	// Een service die nog start of herlaadt, krijgt tot de limiet de tijd.
	for st.err == nil && (st.Active == "activating" || st.Active == "reloading") {
		if err := sleepCtx(ctx, or(v.Retry, time.Second)); err != nil {
			break
		}
		st = v.unitStates(ctx, []string{name})[name]
	}
	switch {
	case st.err != nil:
		c.Status, c.Detail = protocol.VerifyFail, "systemctl show mislukte: "+st.err.Error()
		return c
	case st.Load == "not-found":
		c.Status, c.Detail = protocol.VerifyWarning, "niet geïnstalleerd in deze back-up"
		return c
	case st.Active == "active":
		c.Status, c.Detail = protocol.VerifyOK, "active"
		if st.Sub != "" {
			c.Detail += " (" + st.Sub + ")"
		}
		return c
	}
	state := or(st.Active, "onbekend")
	if st.Result != "" && st.Result != "success" {
		state += ", " + st.Result
	}
	if why := v.clustered(name); why != "" {
		c.Status, c.Detail = protocol.VerifyWarning, state+": "+why
		return c
	}
	journal := v.journal(ctx, unitFile(name))
	if strings.Contains(strings.ToLower(journal), "cannot assign requested address") {
		c.Status = protocol.VerifyWarning
		c.Detail = state + ": hij bindt aan een adres dat in de afgesloten sandbox ontbreekt (Cannot assign requested address)"
		return c
	}
	c.Status, c.Detail = protocol.VerifyFail, state
	if last := lastLine(journal); last != "" {
		c.Detail += "; laatste regel in de journal: " + last
	}
	return c
}

// clustered zegt waarom een clusterdienst zonder de andere nodes niet
// start, of "" als het geen bekende clusterdienst is.
func (v *Verifier) clustered(name string) string {
	switch {
	case name == "patroni" || strings.HasPrefix(name, "patroni@"):
		return "Patroni start zonder de andere nodes en etcd niet; in de afgesloten sandbox is dat te verwachten"
	case (name == "mariadb" || name == "mysql") && v.galera():
		return "MariaDB Galera start zonder de andere nodes niet (wsrep_on); in de afgesloten sandbox is dat te verwachten"
	}
	return ""
}

var wsrepOn = regexp.MustCompile(`(?im)^\s*wsrep[_-]on\s*=\s*(on|1|true)\s*$`)

// galera zegt of MariaDB hier als Galera-node ingesteld is.
func (v *Verifier) galera() bool {
	for _, pattern := range []string{"etc/mysql/*.cnf", "etc/mysql/conf.d/*.cnf", "etc/mysql/mariadb.conf.d/*.cnf"} {
		files, _ := filepath.Glob(filepath.Join(v.root(), pattern))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil && wsrepOn.Match(b) {
				return true
			}
		}
	}
	return false
}

func (v *Verifier) journal(ctx context.Context, unit string) string {
	out, err := v.run(ctx, "journalctl", "-u", unit, "-b", "-n", "20", "--no-pager", "-o", "cat")
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if s == "-- No entries --" {
		return ""
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 200 {
		last = last[:200] + "…"
	}
	return last
}

// failedUnits zoekt units die gefaald zijn, behalve de services die al
// apart gecontroleerd zijn en de units die zonder netwerk altijd falen.
func (v *Verifier) failedUnits(ctx context.Context, services []string) protocol.VerifyCheck {
	c := protocol.VerifyCheck{Kind: "units", Name: "gefaalde units"}
	ctx, cancel := v.check(ctx)
	defer cancel()
	out, err := v.run(ctx, "systemctl", "--failed", "--plain", "--no-legend")
	if err != nil {
		c.Status, c.Detail = protocol.VerifyWarning, "systemctl --failed mislukte: "+err.Error()
		return c
	}
	var failed, ignored []string
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		u := strings.TrimPrefix(f[0], "●")
		if u == "" && len(f) > 1 {
			u = f[1]
		}
		switch {
		case slices.Contains(ignoredUnits, u):
			ignored = append(ignored, u)
		case slices.ContainsFunc(services, func(s string) bool { return unitFile(s) == u }):
		default:
			failed = append(failed, u)
		}
	}
	switch {
	case len(failed) > 0:
		c.Status = protocol.VerifyWarning
		c.Detail = fmt.Sprintf("%d gefaalde %s: %s", len(failed), plural(len(failed), "unit", "units"), strings.Join(failed, ", "))
	case len(ignored) > 0:
		c.Status, c.Detail = protocol.VerifyOK, "geen, behalve wat zonder netwerk te verwachten is: "+strings.Join(ignored, ", ")
	default:
		c.Status, c.Detail = protocol.VerifyOK, "geen"
	}
	return c
}

func (v *Verifier) tcp(ctx context.Context, t protocol.VerifyTCP, svc protocol.VerifyCheck) protocol.VerifyCheck {
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	c := protocol.VerifyCheck{Kind: "tcp", Name: addr}
	ctx, cancel := v.check(ctx)
	defer cancel()
	err := v.retry(ctx, func(ctx context.Context) error {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := v.dial(dctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
		}
		return err
	})
	if err == nil {
		c.Status, c.Detail = protocol.VerifyOK, "neemt verbindingen aan"
		return c
	}
	c.Status, c.Detail = v.unreachable(t.Port, svc, err)
	return c
}

func (v *Verifier) http(ctx context.Context, h protocol.VerifyHTTP) protocol.VerifyCheck {
	c := protocol.VerifyCheck{Kind: "http", Name: h.URL}
	ctx, cancel := v.check(ctx)
	defer cancel()
	client := &http.Client{
		Timeout: 10 * time.Second,
		// Nooit via een proxy en nooit een omleiding volgen: alleen loopback.
		Transport:     &http.Transport{Proxy: nil, DialContext: v.dial, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var status int
	err := v.retry(ctx, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		status = resp.StatusCode
		return nil
	})
	switch {
	case err != nil:
		port := 80
		if u, perr := url.Parse(h.URL); perr == nil && u.Port() != "" {
			port, _ = strconv.Atoi(u.Port())
		}
		c.Status, c.Detail = v.unreachable(port, protocol.VerifyCheck{}, err)
	case status == h.Expect:
		c.Status, c.Detail = protocol.VerifyOK, fmt.Sprintf("gaf %d", status)
	default:
		c.Status, c.Detail = protocol.VerifyFail, fmt.Sprintf("gaf %d in plaats van %d", status, h.Expect)
	}
	return c
}

// unreachable zegt waarom loopback niet antwoordt. Luistert de dienst wel
// op een ander adres, of draait de bijbehorende service om een reden die in
// de sandbox te verwachten is, dan is het een waarschuwing.
func (v *Verifier) unreachable(port int, svc protocol.VerifyCheck, err error) (string, string) {
	if svc.Status == protocol.VerifyWarning {
		return protocol.VerifyWarning, fmt.Sprintf("niet te controleren: %s draait niet (%s)", svc.Name, svc.Detail)
	}
	if other := v.listeners(port); len(other) > 0 {
		return protocol.VerifyWarning, fmt.Sprintf("luistert alleen op %s, niet op loopback; vanuit de sandbox niet te controleren", strings.Join(other, ", "))
	}
	return protocol.VerifyFail, "geen verbinding: " + err.Error()
}

// listeners geeft de adressen buiten loopback waarop iets op port luistert,
// uit /proc/net/tcp en tcp6.
func (v *Verifier) listeners(port int) []string {
	var out []string
	for _, f := range []string{"proc/net/tcp", "proc/net/tcp6"} {
		file, err := os.Open(filepath.Join(v.root(), f))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// sl local_address rem_address st ...; st 0A is LISTEN.
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			ip, p, ok := parseProcAddr(fields[1])
			if !ok || p != port || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			out = append(out, net.JoinHostPort(ip.String(), strconv.Itoa(p)))
		}
		_ = file.Close()
	}
	return out
}

// parseProcAddr leest "0100007F:0050": het adres in de bytevolgorde van de
// kernel, per 32 bits omgedraaid, en de poort in hex.
func parseProcAddr(s string) (net.IP, int, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, 0, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, false
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw), int(port), true
}

// dial verbindt alleen met loopback.
func (v *Verifier) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("verify verbindt alleen met loopback, niet met %s", host)
	}
	if v.Dial != nil {
		return v.Dial(ctx, network, address)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

// retry probeert f tot hij lukt of Settle voorbij is.
func (v *Verifier) retry(ctx context.Context, f func(context.Context) error) error {
	deadline := time.Now().Add(or(v.Settle, 15*time.Second))
	for {
		err := f(ctx)
		if err == nil || !time.Now().Before(deadline) || ctx.Err() != nil {
			if err != nil && ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return err
		}
		if err := sleepCtx(ctx, or(v.Retry, time.Second)); err != nil {
			return err
		}
	}
}

// postgres controleert PostgreSQL via de socket met peer-authenticatie:
// bereikbaar, herstelmodus, de databases en SELECT 1 in elke database.
func (v *Verifier) postgres(ctx context.Context) protocol.VerifyCheck {
	c := protocol.VerifyCheck{Kind: "postgresql", Name: "PostgreSQL"}
	ctx, cancel := v.check(ctx)
	defer cancel()
	psql := func(args ...string) (string, error) {
		out, err := v.run(ctx, "runuser", append([]string{"-u", "postgres", "--", "psql", "-XAtq"}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	if _, err := psql("-c", "SELECT 1"); err != nil {
		if notInstalled(err) {
			c.Status, c.Detail = protocol.VerifyWarning, "psql ontbreekt; PostgreSQL niet gecontroleerd"
			return c
		}
		c.Status, c.Detail = protocol.VerifyFail, "antwoordt niet: "+err.Error()
		return c
	}
	recovery, _ := psql("-c", "SELECT pg_is_in_recovery()")
	list, err := psql("-c", "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1")
	if err != nil {
		c.Status, c.Detail = protocol.VerifyFail, "de lijst met databases is niet te lezen: "+err.Error()
		return c
	}
	var names, skipped, failed []string
	for name := range strings.SplitSeq(list, "\n") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		names = append(names, name)
		if !dbNameRe.MatchString(name) {
			skipped = append(skipped, name)
			continue
		}
		if _, err := psql("-d", name, "-c", "SELECT 1"); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", name, firstLine(err.Error())))
		}
	}
	c.Count = len(names)
	if len(failed) > 0 {
		c.Status = protocol.VerifyFail
		c.Detail = fmt.Sprintf("%d van %d databases antwoorden niet op SELECT 1: %s", len(failed), len(names), strings.Join(failed, "; "))
		return c
	}
	c.Status = protocol.VerifyOK
	c.Detail = fmt.Sprintf("bereikbaar, %d %s", len(names), plural(len(names), "database", "databases"))
	if len(names) > 0 {
		c.Detail += " (" + strings.Join(names, ", ") + ") en elk antwoordt op SELECT 1"
	}
	if len(skipped) > 0 {
		c.Detail += "; niet gecontroleerd wegens een ongewone naam: " + strings.Join(skipped, ", ")
	}
	if recovery == "t" {
		c.Detail += "; in herstelmodus, zoals een standby"
	}
	return c
}

var systemDatabases = []string{"information_schema", "mysql", "performance_schema", "sys"}

// mariadb controleert MariaDB via de socket als root: ping en de databases.
func (v *Verifier) mariadb(ctx context.Context) protocol.VerifyCheck {
	c := protocol.VerifyCheck{Kind: "mariadb", Name: "MariaDB"}
	ctx, cancel := v.check(ctx)
	defer cancel()
	ping, err := v.runFirst(ctx, [][]string{{"mariadb-admin", "ping"}, {"mysqladmin", "ping"}})
	if err != nil {
		switch {
		case notInstalled(err):
			c.Status, c.Detail = protocol.VerifyWarning, "mariadb-admin ontbreekt; MariaDB niet gecontroleerd"
		case v.galera():
			c.Status, c.Detail = protocol.VerifyWarning, "niet gecontroleerd: MariaDB Galera start zonder de andere nodes niet"
		default:
			c.Status, c.Detail = protocol.VerifyFail, "antwoordt niet: "+err.Error()
		}
		return c
	}
	if !strings.Contains(string(ping), "alive") {
		c.Status, c.Detail = protocol.VerifyFail, "antwoordt niet: "+firstLine(string(ping))
		return c
	}
	out, err := v.runFirst(ctx, [][]string{{"mariadb", "-N", "-B", "-e", "SHOW DATABASES"}, {"mysql", "-N", "-B", "-e", "SHOW DATABASES"}})
	if err != nil {
		c.Status, c.Detail = protocol.VerifyFail, "de lijst met databases is niet te lezen: "+err.Error()
		return c
	}
	var user []string
	for name := range strings.SplitSeq(string(out), "\n") {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(systemDatabases, name) {
			user = append(user, name)
		}
	}
	c.Count = len(user)
	c.Status = protocol.VerifyOK
	c.Detail = fmt.Sprintf("bereikbaar, %d %s naast de systeemdatabases", len(user), plural(len(user), "database", "databases"))
	if len(user) > 0 {
		c.Detail += " (" + strings.Join(user, ", ") + ")"
	}
	return c
}

// runFirst voert het eerste programma uit dat er is.
func (v *Verifier) runFirst(ctx context.Context, cmds [][]string) ([]byte, error) {
	var err error
	for _, c := range cmds {
		var out []byte
		out, err = v.run(ctx, c[0], c[1:]...)
		if err == nil || !notInstalled(err) {
			return out, err
		}
	}
	return nil, err
}

func (v *Verifier) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if v.Exec != nil {
		return v.Exec(ctx, name, args...)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Dir = "/"
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			err = fmt.Errorf("%w: %s", err, firstLine(msg))
		}
	}
	if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	return out, err
}

func notInstalled(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found")
}

func (v *Verifier) root() string {
	if v.Root == "" {
		return "/"
	}
	return v.Root
}

// InSandbox zegt of deze machine een sandbox van de back-upcontrole is: het
// SMBIOS-serienummer is cf-sandbox. root is de bestandssysteemwortel; leeg
// is "/".
func InSandbox(root string) bool {
	if root == "" {
		root = "/"
	}
	b, err := os.ReadFile(filepath.Join(root, "sys/class/dmi/id/product_serial"))
	return err == nil && strings.TrimSpace(string(b)) == protocol.SandboxSerial
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func or[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
