package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Netwerkscans (M3): een scanner (een node met nmap en protocol 42) scant
// de adressen van een eigen scope. De server legt het resultaat naast wat
// hij van de nodes weet: luisterende poorten en het firewallplan.

// KindNetwork is de soort van de bevindingen uit een netwerkscan; subject is
// "ip:<adres>".
const KindNetwork = "netwerk"

// Schema's.
const (
	ScheduleOff    = "uit"
	ScheduleDaily  = "dagelijks"
	ScheduleWeekly = "wekelijks"
)

// Stand van een uitvoering.
const (
	RunRunning = "bezig"
	RunDone    = "klaar"
	RunFailed  = "mislukt"
)

// ScannerHostname is de node van de scannercontainer.
const ScannerHostname = "cf-scanner"

type Scan struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	ScopeID     uuid.UUID  `json:"scope_id"`
	ScopeName   string     `json:"scope_name"`
	ScannerID   *uuid.UUID `json:"scanner_id"`
	ScannerName string     `json:"scanner_name"`
	Profile     string     `json:"profile"`
	MaxRate     int        `json:"max_rate"`
	Schedule    string     `json:"schedule"`
	Weekday     int        `json:"weekday"`
	Hour        int        `json:"hour"`
	NextRun     *time.Time `json:"next_run"`
	Running     bool       `json:"running"`
	LastRun     *ScanRun   `json:"last_run"`
}

type ScanInput struct {
	Name      string
	ScopeID   uuid.UUID
	ScannerID *uuid.UUID
	Profile   string
	MaxRate   int
	Schedule  string
	Weekday   int
	Hour      int
}

type ScanRun struct {
	ID          uuid.UUID  `json:"id"`
	ScanID      uuid.UUID  `json:"scan_id"`
	ScanName    string     `json:"scan_name"`
	ScannerID   *uuid.UUID `json:"scanner_id"`
	ScannerName string     `json:"scanner_name"`
	Trigger     string     `json:"trigger"`
	Status      string     `json:"status"`
	Error       string     `json:"error"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Targets     []string   `json:"targets"`
	HostsUp     int        `json:"hosts_up"`
	OpenPorts   int        `json:"open_ports"`
	Findings    int        `json:"findings"`
}

// RunHost is een gescande host met wat ClusterForge ervan weet.
type RunHost struct {
	protocol.ScanHost
	NodeID   *uuid.UUID `json:"node_id"`
	Node     string     `json:"node"`
	Cluster  string     `json:"cluster"`
	Expected []RunPort  `json:"expected"`
}

// RunPort zegt per open poort wat ClusterForge verwachtte.
type RunPort struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	// Known: luistert (de agent zag een proces), firewall (het plan zet
	// hem open), dicht (het plan zet hem dicht maar hij is bereikbaar),
	// onbekend (niets bij ClusterForge bekend), of leeg (geen node).
	Known   string `json:"known"`
	Process string `json:"process,omitempty"`
}

type RunResult struct {
	Nmap      string    `json:"nmap"`
	Args      []string  `json:"args"`
	Truncated bool      `json:"truncated"`
	Notes     []string  `json:"notes"`
	Hosts     []RunHost `json:"hosts"`
}

type ScanRunDetail struct {
	ScanRun
	Result *RunResult `json:"result"`
}

// Scanner is een node die kan scannen of dat bijna kan.
type Scanner struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Address  string    `json:"address"`
	Online   bool      `json:"online"`
	Protocol int       `json:"protocol"`
	// Ready: online, protocol 42 en nmap geïnstalleerd.
	Ready     bool   `json:"ready"`
	Nmap      bool   `json:"nmap"`
	Container bool   `json:"container"`
	Note      string `json:"note"`
}

type ScansOverview struct {
	Protocol int       `json:"protocol"`
	Profiles []string  `json:"profiles"`
	Scripts  []string  `json:"scripts"`
	Scans    []Scan    `json:"scans"`
	Scanners []Scanner `json:"scanners"`
	Runs     []ScanRun `json:"runs"`
}

// Scanners geeft de nodes die nmap hebben, en de scannercontainer.
func (s *Service) Scanners(ctx context.Context) ([]Scanner, error) {
	nodes, err := s.nodes(ctx, `WHERE coalesce(i.packages @> '[{"name":"nmap"}]', false) OR n.hostname = $1`, ScannerHostname)
	if err != nil {
		return nil, err
	}
	out := []Scanner{}
	for _, n := range nodes {
		sc := Scanner{NodeID: n.NodeID, Hostname: n.Hostname, Address: n.Address, Online: n.Online, Protocol: n.Protocol,
			Container: n.Hostname == ScannerHostname}
		sc.Nmap = n.Packages > 0 // de query vond nmap, of het is de container
		if sc.Container {
			var ok bool
			_ = s.pool.QueryRow(ctx, `SELECT coalesce(packages @> '[{"name":"nmap"}]', false) FROM security_inventory WHERE node_id = $1`, n.NodeID).Scan(&ok)
			sc.Nmap = ok
		}
		switch {
		case !sc.Online:
			sc.Note = "offline"
		case sc.Protocol < protocol.SecurityScanSince:
			sc.Note = fmt.Sprintf("agent te oud (protocol %d nodig): Nodes → Agents bijwerken", protocol.SecurityScanSince)
		case !sc.Nmap:
			sc.Note = "nmap nog niet gezien; de pakketlijst wordt elke 6 uur ververst"
		default:
			sc.Ready = true
		}
		out = append(out, sc)
	}
	return out, nil
}

const scansSQL = `
SELECT s.id, s.name, s.scope_id, sc.name, s.scanner_id, coalesce(n.hostname, ''), s.profile, s.max_rate, s.schedule, s.weekday, s.hour, s.next_run
FROM security_scans s
JOIN security_scopes sc ON sc.id = s.scope_id
LEFT JOIN nodes n ON n.id = s.scanner_id
`

const runsSQL = `
SELECT r.id, r.scan_id, s.name, r.scanner_id, r.scanner_name, r.trigger, r.status, r.error, r.started_at, r.finished_at, r.targets,
       r.hosts_up, r.open_ports, r.findings
FROM security_scan_runs r
JOIN security_scans s ON s.id = r.scan_id
`

func (s *Service) scans(ctx context.Context, where string, args ...any) ([]Scan, error) {
	rows, err := s.pool.Query(ctx, scansSQL+where+` ORDER BY lower(s.name)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scan{}
	for rows.Next() {
		var sc Scan
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.ScopeID, &sc.ScopeName, &sc.ScannerID, &sc.ScannerName, &sc.Profile, &sc.MaxRate,
			&sc.Schedule, &sc.Weekday, &sc.Hour, &sc.NextRun); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	for i := range out {
		out[i].Running = s.running[out[i].ID]
	}
	s.mu.Unlock()
	return out, nil
}

func (s *Service) runs(ctx context.Context, where string, limit int, args ...any) ([]ScanRun, error) {
	rows, err := s.pool.Query(ctx, runsSQL+where+` ORDER BY r.started_at DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScanRun{}
	for rows.Next() {
		var r ScanRun
		if err := rows.Scan(&r.ID, &r.ScanID, &r.ScanName, &r.ScannerID, &r.ScannerName, &r.Trigger, &r.Status, &r.Error, &r.StartedAt,
			&r.FinishedAt, &r.Targets, &r.HostsUp, &r.OpenPorts, &r.Findings); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Scans geeft de scans, de scanners en de laatste uitvoeringen.
func (s *Service) Scans(ctx context.Context) (ScansOverview, error) {
	o := ScansOverview{Protocol: protocol.SecurityScanSince, Scripts: protocol.SafeScanScripts,
		Profiles: []string{protocol.ScanProfileQuick, protocol.ScanProfileStandard, protocol.ScanProfileFull}}
	var err error
	if o.Scans, err = s.scans(ctx, ""); err != nil {
		return o, err
	}
	if o.Scanners, err = s.Scanners(ctx); err != nil {
		return o, err
	}
	if o.Runs, err = s.runs(ctx, "", 30); err != nil {
		return o, err
	}
	for i := range o.Scans {
		for j := range o.Runs {
			if o.Runs[j].ScanID == o.Scans[i].ID {
				o.Scans[i].LastRun = &o.Runs[j]
				break
			}
		}
	}
	return o, nil
}

// ScanRunDetail geeft één uitvoering met wat er gevonden is.
func (s *Service) ScanRunDetail(ctx context.Context, id uuid.UUID) (ScanRunDetail, error) {
	runs, err := s.runs(ctx, "WHERE r.id = $1", 1, id)
	if err != nil {
		return ScanRunDetail{}, err
	}
	if len(runs) == 0 {
		return ScanRunDetail{}, ErrNotFound
	}
	d := ScanRunDetail{ScanRun: runs[0]}
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT result FROM security_scan_runs WHERE id = $1`, id).Scan(&raw); err != nil {
		return d, err
	}
	if raw != nil {
		d.Result = &RunResult{}
		if err := json.Unmarshal(raw, d.Result); err != nil {
			return d, err
		}
	}
	return d, nil
}

// nextRun is het volgende moment van een schema, in de tijdzone van de
// server.
func nextRun(schedule string, weekday, hour int, after time.Time) *time.Time {
	if schedule != ScheduleDaily && schedule != ScheduleWeekly {
		return nil
	}
	loc := after.Location()
	t := time.Date(after.Year(), after.Month(), after.Day(), hour, 0, 0, 0, loc)
	for i := 0; i < 9; i++ {
		if t.After(after) && (schedule == ScheduleDaily || int(t.Weekday()) == weekday) {
			return &t
		}
		t = time.Date(t.Year(), t.Month(), t.Day()+1, hour, 0, 0, 0, loc)
	}
	return nil
}

// SaveScan maakt een scan aan (id nil) of werkt hem bij.
func (s *Service) SaveScan(ctx context.Context, actor events.Actor, userID *uuid.UUID, id *uuid.UUID, in ScanInput) (Scan, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 60 {
		return Scan{}, &FieldError{"Geef de scan een naam van hoogstens 60 tekens."}
	}
	if !slices.Contains([]string{protocol.ScanProfileQuick, protocol.ScanProfileStandard, protocol.ScanProfileFull}, in.Profile) {
		return Scan{}, &FieldError{"Kies snel, standaard of volledig."}
	}
	if in.MaxRate == 0 {
		in.MaxRate = 300
	}
	if in.MaxRate < 10 || in.MaxRate > 5000 {
		return Scan{}, &FieldError{"Pakketten per seconde: tussen 10 en 5000."}
	}
	if !slices.Contains([]string{ScheduleOff, ScheduleDaily, ScheduleWeekly}, in.Schedule) {
		return Scan{}, &FieldError{"Kies uit, dagelijks of wekelijks."}
	}
	if in.Weekday < 0 || in.Weekday > 6 || in.Hour < 0 || in.Hour > 23 {
		return Scan{}, &FieldError{"Ongeldige dag of uur."}
	}
	var scopeOK bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM security_scopes WHERE id = $1)`, in.ScopeID).Scan(&scopeOK); err != nil {
		return Scan{}, err
	}
	if !scopeOK {
		return Scan{}, &FieldError{"Kies een eigen scope."}
	}
	next := nextRun(in.Schedule, in.Weekday, in.Hour, s.Now())
	var sid uuid.UUID
	var err error
	if id == nil {
		err = s.pool.QueryRow(ctx, `INSERT INTO security_scans (name, scope_id, scanner_id, profile, max_rate, schedule, weekday, hour, next_run, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`, name, in.ScopeID, in.ScannerID, in.Profile, in.MaxRate, in.Schedule,
			in.Weekday, in.Hour, next, userID).Scan(&sid)
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE security_scans SET name = $2, scope_id = $3, scanner_id = $4, profile = $5, max_rate = $6,
    schedule = $7, weekday = $8, hour = $9, next_run = $10, updated_by = $11, updated_at = now() WHERE id = $1 RETURNING id`,
			*id, name, in.ScopeID, in.ScannerID, in.Profile, in.MaxRate, in.Schedule, in.Weekday, in.Hour, next, userID).Scan(&sid)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Scan{}, ErrNotFound
	}
	if err != nil {
		if strings.Contains(err.Error(), "security_scans_name_key") {
			return Scan{}, &FieldError{"Er is al een scan met die naam."}
		}
		if strings.Contains(err.Error(), "security_scans_scanner_id_fkey") {
			return Scan{}, &FieldError{"Die scanner bestaat niet."}
		}
		return Scan{}, err
	}
	s.event(ctx, actor, "security_scan", sid.String(), nil, "security.scan_saved",
		map[string]any{"name": name, "profile": in.Profile, "schedule": in.Schedule})
	list, err := s.scans(ctx, "WHERE s.id = $1", sid)
	if err != nil {
		return Scan{}, err
	}
	if len(list) == 0 {
		return Scan{}, ErrNotFound
	}
	return list[0], nil
}

// DeleteScan verwijdert een scan met zijn uitvoeringen. De bevindingen
// blijven tot een volgende scan ze oplost.
func (s *Service) DeleteScan(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	var name string
	err := s.pool.QueryRow(ctx, `DELETE FROM security_scans WHERE id = $1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	s.event(ctx, actor, "security_scan", id.String(), nil, "security.scan_deleted", map[string]any{"name": name})
	return nil
}

// ErrScanRunning: de scan loopt al.
var ErrScanRunning = errors.New("deze scan loopt al")

// ErrNoScanner: er is geen scanner die nu kan scannen.
var ErrNoScanner = errors.New("geen scanner beschikbaar")

// StartScan start een scan op de achtergrond en geeft de uitvoering terug.
func (s *Service) StartScan(ctx context.Context, actor events.Actor, id uuid.UUID, trigger string) (uuid.UUID, error) {
	list, err := s.scans(ctx, "WHERE s.id = $1", id)
	if err != nil {
		return uuid.Nil, err
	}
	if len(list) == 0 {
		return uuid.Nil, ErrNotFound
	}
	sc := list[0]
	s.mu.Lock()
	if s.running[id] {
		s.mu.Unlock()
		return uuid.Nil, ErrScanRunning
	}
	s.running[id] = true
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
	}
	scanner, err := s.pickScanner(ctx, sc.ScannerID)
	if err != nil {
		release()
		return uuid.Nil, err
	}
	scopes, err := s.Scopes(ctx)
	if err != nil {
		release()
		return uuid.Nil, err
	}
	var scope *Scope
	for i := range scopes {
		if scopes[i].ID == sc.ScopeID {
			scope = &scopes[i]
		}
	}
	if scope == nil {
		release()
		return uuid.Nil, ErrNotFound
	}
	targets, notes := scanTargets(scope)
	if len(targets) == 0 {
		release()
		return uuid.Nil, &FieldError{"Deze scope heeft geen netwerk dat te scannen is."}
	}
	var runID uuid.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO security_scan_runs (scan_id, scanner_id, scanner_name, trigger, targets, started_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, sc.ID, scanner.NodeID, scanner.Hostname, trigger, targets, s.Now()).Scan(&runID); err != nil {
		release()
		return uuid.Nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE security_scans SET next_run = $2 WHERE id = $1`, sc.ID,
		nextRun(sc.Schedule, sc.Weekday, sc.Hour, s.Now())); err != nil {
		release()
		return uuid.Nil, err
	}
	s.event(ctx, actor, "security_scan", sc.ID.String(), nil, "security.scan_started",
		map[string]any{"name": sc.Name, "scanner": scanner.Hostname, "targets": targets, "trigger": trigger})
	spec := protocol.ScanSpec{Targets: targets, Exclude: scope.Exclude, Allowed: scope.CIDRs, Profile: sc.Profile, MaxRate: sc.MaxRate}
	s.mu.Lock()
	base := s.base
	s.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		s.runScan(base, sc, *scope, scanner, runID, spec, notes)
	}()
	return runID, nil
}

// Wait wacht tot de lopende scans klaar zijn (voor tests en afsluiten).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) pickScanner(ctx context.Context, want *uuid.UUID) (Scanner, error) {
	list, err := s.Scanners(ctx)
	if err != nil {
		return Scanner{}, err
	}
	for _, sc := range list {
		if want != nil && sc.NodeID == *want {
			if !sc.Ready {
				return Scanner{}, fmt.Errorf("%w: %s (%s)", ErrNoScanner, sc.Hostname, sc.Note)
			}
			return sc, nil
		}
	}
	if want != nil {
		return Scanner{}, fmt.Errorf("%w: de gekozen scanner heeft geen nmap meer", ErrNoScanner)
	}
	// Automatisch: de container eerst, dan de rest.
	slices.SortStableFunc(list, func(a, b Scanner) int {
		if a.Container != b.Container {
			if a.Container {
				return -1
			}
			return 1
		}
		return 0
	})
	for _, sc := range list {
		if sc.Ready {
			return sc, nil
		}
	}
	return Scanner{}, fmt.Errorf("%w: zet de scannercontainer aan of installeer nmap op een node", ErrNoScanner)
}

// scanTargets zijn de netwerken van een scope die een scan aankan: IPv4
// tot /16, IPv6 alleen kleine stukken (een /64 is niet te doorlopen).
func scanTargets(sc *Scope) ([]string, []string) {
	var out, notes []string
	for _, c := range sc.CIDRs {
		p, err := ParsePrefix(c)
		if err != nil {
			continue
		}
		if p.Addr().Is6() && p.Bits() < 112 {
			notes = append(notes, fmt.Sprintf("%s overgeslagen: een IPv6-netwerk zo groot is niet te doorlopen.", p))
			continue
		}
		out = append(out, p.String())
	}
	return out, notes
}

func (s *Service) runScan(ctx context.Context, sc Scan, scope Scope, scanner Scanner, runID uuid.UUID, spec protocol.ScanSpec, notes []string) {
	rctx, cancel := context.WithTimeout(ctx, protocol.MaxScanDuration)
	defer cancel()
	res, err := s.bus.Security(rctx, scanner.NodeID, protocol.SecurityRequest{Op: protocol.SecurityOpScan, Scan: &spec})
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	if err == nil && res.Scan == nil {
		err = errors.New("de scanner stuurde geen resultaat")
	}
	// Ook na het afsluiten van de server de uitvoering nog afronden.
	dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer dcancel()
	if err != nil {
		if _, e := s.pool.Exec(dctx, `UPDATE security_scan_runs SET status = 'mislukt', error = $2, finished_at = $3 WHERE id = $1`,
			runID, err.Error(), s.Now()); e != nil {
			s.log.Warn("scan bewaren mislukt", "err", e)
		}
		s.event(dctx, events.System(), "security_scan", sc.ID.String(), nil, "security.scan_failed",
			map[string]any{"name": sc.Name, "scanner": scanner.Hostname, "error": err.Error()})
		return
	}
	result, open, found, err := s.analyzeScan(dctx, scope, res.Scan)
	if err != nil {
		s.log.Warn("scan verwerken mislukt", "err", err)
		_, _ = s.pool.Exec(dctx, `UPDATE security_scan_runs SET status = 'mislukt', error = $2, finished_at = $3 WHERE id = $1`,
			runID, "verwerken mislukt: "+err.Error(), s.Now())
		return
	}
	result.Notes = append(notes, result.Notes...)
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	if _, err := s.pool.Exec(dctx, `UPDATE security_scan_runs SET status = 'klaar', finished_at = $2, hosts_up = $3, open_ports = $4,
    findings = $5, result = $6 WHERE id = $1`, runID, s.Now(), res.Scan.HostsUp, open, found, raw); err != nil {
		s.log.Warn("scan bewaren mislukt", "err", err)
	}
	s.event(dctx, events.System(), "security_scan", sc.ID.String(), nil, "security.scan_done",
		map[string]any{"name": sc.Name, "scanner": scanner.Hostname, "hosts_up": res.Scan.HostsUp, "open_ports": open, "findings": found})
}

// scheduleScans start de scans waarvan het tijd is.
func (s *Service) scheduleScans(ctx context.Context) error {
	now := s.Now()
	list, err := s.scans(ctx, "WHERE s.schedule <> 'uit' AND (s.next_run IS NULL OR s.next_run <= $1)", now)
	if err != nil {
		return err
	}
	for _, sc := range list {
		if sc.Running {
			continue
		}
		if sc.NextRun == nil {
			// Na een wijziging zonder volgende run: alleen plannen.
			_, err := s.pool.Exec(ctx, `UPDATE security_scans SET next_run = $2 WHERE id = $1`, sc.ID, nextRun(sc.Schedule, sc.Weekday, sc.Hour, now))
			if err != nil {
				return err
			}
			continue
		}
		if _, err := s.StartScan(ctx, events.System(), sc.ID, "schema"); err != nil {
			s.log.Info("geplande scan niet gestart", "scan", sc.Name, "err", err)
			// Volgende keer opnieuw; een uur later, zodat een offline
			// scanner geen ronde per minuut kost.
			if _, e := s.pool.Exec(ctx, `UPDATE security_scans SET next_run = $2 WHERE id = $1`, sc.ID, now.Add(time.Hour)); e != nil {
				return e
			}
		}
	}
	return nil
}

// failStaleRuns: uitvoeringen die nog "bezig" zijn bij het starten van de
// server, zijn met de vorige server verloren gegaan.
func (s *Service) failStaleRuns(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE security_scan_runs SET status = 'mislukt', error = 'de server herstartte tijdens de scan',
    finished_at = $1 WHERE status = 'bezig'`, s.Now())
	return err
}

func addrIn(prefixes []string, a netip.Addr) bool {
	for _, c := range prefixes {
		if p, err := ParsePrefix(c); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

// LinkedScanner schrijft in het logboek dat de scannercontainer gekoppeld
// wordt.
func (s *Service) LinkedScanner(ctx context.Context, actor events.Actor, nodeID uuid.UUID) {
	s.event(ctx, actor, "node", nodeID.String(), nil, "security.scanner_linked", map[string]any{"hostname": ScannerHostname})
}
