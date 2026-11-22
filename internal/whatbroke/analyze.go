package whatbroke

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
)

// Version is de versie van de regels. Een nieuwe versie rekent elk
// incident dat nog niet vastligt opnieuw.
const Version = 2

// Niveaus van een oorzaak.
const (
	LevelLikely   = "likely"
	LevelPossible = "possible"
	LevelWeak     = "weak"
)

const (
	// Lookback is hoe ver voor het eerste symptoom de analyse naar
	// veranderingen zoekt.
	Lookback = 2 * time.Hour
	// symptomBefore en symptomAfter zijn het venster rond het begin van het
	// incident waarin het eerste symptoom ligt.
	symptomBefore = 15 * time.Minute
	symptomAfter  = time.Minute
	// candidateAfter is hoe lang na het eerste symptoom een verandering nog
	// meetelt, voor een storing die erger werd.
	candidateAfter = time.Minute
	// halfLife is de halveringstijd van de afstand in tijd.
	halfLife = 10 * time.Minute
	// afterFactor is het gewicht van een verandering na het eerste symptoom.
	afterFactor = 0.4
	linkBonus   = 0.3
	maxCauses   = 5
	// groupGap is hoe dicht pakketten of bestanden op dezelfde node bij
	// elkaar liggen om één update te zijn.
	groupGap = 2 * time.Minute
	// vipWindow is hoe kort voor een VIP zonder eigenaar de herstart van de
	// eigenaar moet liggen.
	vipWindow = 10 * time.Minute
)

// Subject is een onderwerp van het incident.
type Subject struct {
	Type      string
	ID        uuid.UUID
	Name      string
	ClusterID *uuid.UUID
	// Status is de ergste status.
	Status string
	Since  time.Time
	Until  *time.Time
}

// Node is een node zoals de analyse hem kent.
type Node struct {
	ID        uuid.UUID
	Hostname  string
	ClusterID *uuid.UUID
	// Host is de Proxmox-host als "<koppeling>/<host>", of leeg.
	Host string
}

// World is de inventory waar de analyse de afstanden uit haalt.
type World struct {
	Nodes    []Node
	Clusters map[uuid.UUID]string
	// Deps zijn paren van een afnemend en een leverend cluster.
	Deps [][2]uuid.UUID
}

// NodeAnomaly is een afwijking in de metrics van een node.
type NodeAnomaly struct {
	NodeID   uuid.UUID
	Hostname string
	metrics.Anomaly
}

// Input is alles wat de analyse nodig heeft. Ze leest zelf niets.
type Input struct {
	IncidentID uuid.UUID
	Started    time.Time
	Subjects   []Subject
	World      World
	// Entries is de tijdlijn over ScopeFor, van Lookback voor het begin tot
	// het einde van het incident.
	Entries   []Entry
	Anomalies []NodeAnomaly
	// MetricsUnknown is true als de metrics niet te lezen waren.
	MetricsUnknown bool
}

// Moment is het eerste symptoom.
type Moment struct {
	EntryID string    `json:"entry_id"`
	Ts      time.Time `json:"ts"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Summary string    `json:"summary"`
	// Why legt het venster uit; leeg als het moment exact is.
	Why string `json:"why"`
}

// Cause is een mogelijke oorzaak.
type Cause struct {
	EntryID  string    `json:"entry_id"`
	Ts       time.Time `json:"ts"`
	Level    string    `json:"level"`
	Score    float64   `json:"score"`
	Kind     string    `json:"kind"`
	Summary  string    `json:"summary"`
	Hostname string    `json:"hostname"`
	Cluster  string    `json:"cluster"`
	Link     string    `json:"link"`
	Reasons  []string  `json:"reasons"`
	// Also is het aantal regels die erbij horen: dezelfde taak, of de
	// andere pakketten van dezelfde update.
	Also   int    `json:"also"`
	Commit string `json:"commit"`
	// links is het aantal vaste verbanden; bij een gelijke score gaat de
	// oorzaak met meer verbanden voor.
	links int
}

// Finding is een afwijking in de metrics, zoals het rapport hem toont.
type Finding struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Metric   string    `json:"metric"`
	Label    string    `json:"label"`
	Before   float64   `json:"before"`
	After    float64   `json:"after"`
	At       time.Time `json:"at"`
	Summary  string    `json:"summary"`
}

// Ref is een cluster met zijn naam.
type Ref struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Analysis is de uitkomst. Ze is deterministisch: dezelfde invoer geeft
// dezelfde JSON.
type Analysis struct {
	Version      int       `json:"version"`
	FirstSymptom Moment    `json:"first_symptom"`
	WindowFrom   time.Time `json:"window_from"`
	WindowTo     time.Time `json:"window_to"`
	Conclusion   string    `json:"conclusion"`
	Causes       []Cause   `json:"causes"`
	// Changes is het aantal veranderingen in het venster, ook die te ver
	// weg lagen om oorzaak te zijn.
	Changes        int       `json:"changes"`
	Anomalies      []Finding `json:"anomalies"`
	MetricsUnknown bool      `json:"metrics_unknown"`
	// Consumers zijn clusters die van een geraakt cluster afhangen en zelf
	// niet in het incident zitten.
	Consumers []Ref `json:"consumers"`
}

// reach is wat het incident raakte en wat eromheen ligt.
type reach struct {
	w        World
	nodes    map[uuid.UUID]Node
	clusters map[uuid.UUID]bool
	// symptomNodes zijn de nodes die zelf misgingen.
	symptomNodes map[uuid.UUID]bool
	// affected zijn de symptomNodes en de nodes van de geraakte clusters.
	affected  map[uuid.UUID]bool
	suppliers map[uuid.UUID]bool
	consumers map[uuid.UUID]bool
	// hosts zijn de Proxmox-hosts van de geraakte nodes, met die nodes.
	hosts       map[string][]uuid.UUID
	sameHost    map[uuid.UUID]bool
	connections map[uuid.UUID]bool
}

func newReach(subjects []Subject, w World) *reach {
	r := &reach{
		w: w, nodes: map[uuid.UUID]Node{}, clusters: map[uuid.UUID]bool{}, symptomNodes: map[uuid.UUID]bool{},
		affected: map[uuid.UUID]bool{}, suppliers: map[uuid.UUID]bool{}, consumers: map[uuid.UUID]bool{},
		hosts: map[string][]uuid.UUID{}, sameHost: map[uuid.UUID]bool{}, connections: map[uuid.UUID]bool{},
	}
	for _, n := range w.Nodes {
		r.nodes[n.ID] = n
	}
	for _, s := range subjects {
		switch s.Type {
		case "cluster":
			r.clusters[s.ID] = true
		case "node":
			r.symptomNodes[s.ID], r.affected[s.ID] = true, true
		}
		if s.ClusterID != nil {
			r.clusters[*s.ClusterID] = true
		}
	}
	for _, n := range w.Nodes {
		if n.ClusterID != nil && r.clusters[*n.ClusterID] {
			r.affected[n.ID] = true
		}
	}
	// Leveranciers en afnemers, ook via een tussenstap.
	walk := func(next map[uuid.UUID][]uuid.UUID, into map[uuid.UUID]bool) {
		var todo []uuid.UUID
		for c := range r.clusters {
			todo = append(todo, c)
		}
		for len(todo) > 0 {
			c := todo[0]
			todo = todo[1:]
			for _, to := range next[c] {
				if !r.clusters[to] && !into[to] {
					into[to] = true
					todo = append(todo, to)
				}
			}
		}
	}
	up, down := map[uuid.UUID][]uuid.UUID{}, map[uuid.UUID][]uuid.UUID{}
	for _, d := range w.Deps {
		up[d[0]] = append(up[d[0]], d[1])
		down[d[1]] = append(down[d[1]], d[0])
	}
	walk(up, r.suppliers)
	walk(down, r.consumers)
	for _, n := range w.Nodes {
		if n.Host != "" && r.affected[n.ID] {
			r.hosts[n.Host] = append(r.hosts[n.Host], n.ID)
		}
	}
	for _, n := range w.Nodes {
		if n.Host == "" {
			continue
		}
		if _, ok := r.hosts[n.Host]; ok && !r.affected[n.ID] {
			r.sameHost[n.ID] = true
		}
		if conn, _, ok := strings.Cut(n.Host, "/"); ok && r.hosts[n.Host] != nil {
			if id, err := uuid.Parse(conn); err == nil {
				r.connections[id] = true
			}
		}
	}
	return r
}

// ScopeFor geeft waarover de tijdlijn van een incident moet gaan: de
// geraakte clusters en nodes, hun leveranciers, de nodes op dezelfde
// Proxmox-hosts, die Proxmox-koppelingen en het incident zelf.
func ScopeFor(incidentID uuid.UUID, subjects []Subject, w World) Scope {
	r := newReach(subjects, w)
	var sc Scope
	clusters := map[uuid.UUID]bool{}
	for c := range r.clusters {
		clusters[c] = true
	}
	for c := range r.suppliers {
		clusters[c] = true
	}
	nodes := map[uuid.UUID]bool{}
	for id := range r.affected {
		nodes[id] = true
	}
	for id := range r.sameHost {
		nodes[id] = true
	}
	for _, n := range w.Nodes {
		if n.ClusterID != nil && r.suppliers[*n.ClusterID] {
			nodes[n.ID] = true
		}
	}
	sc.Clusters, sc.Nodes = sortedIDs(clusters), sortedIDs(nodes)
	sc.Others = sortedIDs(r.connections)
	if incidentID != uuid.Nil {
		sc.Others = append(sc.Others, incidentID)
	}
	return sc
}

func sortedIDs(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out
}

func (r *reach) inScope(e Entry) bool {
	return (e.NodeID != nil && r.affected[*e.NodeID]) || (e.ClusterID != nil && r.clusters[*e.ClusterID])
}

func (r *reach) hostname(id uuid.UUID) string {
	if n, ok := r.nodes[id]; ok {
		return n.Hostname
	}
	return id.String()[:8]
}

func (r *reach) hostnames(ids []uuid.UUID) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = r.hostname(id)
	}
	slices.Sort(names)
	return list(names)
}

// list zet namen achter elkaar: "a", "a en b", "a, b en c".
func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
}

// facts zijn de symptomen waar de vaste verbanden naar kijken.
type facts struct {
	// failing zijn per node de units die faalden, met wanneer.
	failing map[uuid.UUID]map[string][]time.Time
	// down is per node wanneer hij down ging.
	down map[uuid.UUID]time.Time
	// diskFull is per node wanneer hij verminderd werd door een volle schijf.
	diskFull map[uuid.UUID]time.Time
	vipLost  []vipLoss
	// apps zijn per app in een Kubernetes-cluster de symptomen.
	apps map[string][]appTrouble
}

type vipLoss struct {
	address string
	owner   uuid.UUID
	ts      time.Time
}

func (r *reach) facts(entries []Entry, from time.Time) facts {
	f := facts{
		failing: map[uuid.UUID]map[string][]time.Time{}, down: map[uuid.UUID]time.Time{}, diskFull: map[uuid.UUID]time.Time{},
		apps: kubeFacts(entries, from),
	}
	fail := func(node uuid.UUID, unit string, ts time.Time) {
		if f.failing[node] == nil {
			f.failing[node] = map[string][]time.Time{}
		}
		f.failing[node][unit] = append(f.failing[node][unit], ts)
	}
	for _, e := range entries {
		if e.Class != events.ClassSymptom || e.Ts.Before(from) {
			continue
		}
		switch {
		case e.Source != SourceLog && e.Kind == journal.KindService && e.NodeID != nil:
			fail(*e.NodeID, unitBase(e.Subject), e.Ts)
		case e.Action == "node.status_changed" && e.NodeID != nil:
			for _, u := range unitsFromReason(e.Detail["reason"]) {
				fail(*e.NodeID, u, e.Ts)
			}
			if e.Detail["to"] == "down" {
				if _, ok := f.down[*e.NodeID]; !ok {
					f.down[*e.NodeID] = e.Ts
				}
			}
			if diskFromReason(e.Detail["reason"]) {
				if _, ok := f.diskFull[*e.NodeID]; !ok {
					f.diskFull[*e.NodeID] = e.Ts
				}
			}
		case e.Action == "vip.owner_changed":
			if id, err := uuid.Parse(e.Detail["from"]); err == nil {
				f.vipLost = append(f.vipLost, vipLoss{address: e.Detail["address"], owner: id, ts: e.Ts})
			}
		}
	}
	return f
}

// firstFailure is de eerste keer dat unit op node faalde vanaf t.
func (f facts) firstFailure(node uuid.UUID, unit string, t time.Time) (time.Time, bool) {
	for _, ts := range f.failing[node][unit] {
		if !ts.Before(t) {
			return ts, true
		}
	}
	return time.Time{}, false
}

func (f facts) units(node uuid.UUID) []string {
	out := make([]string, 0, len(f.failing[node]))
	for u := range f.failing[node] {
		out = append(out, u)
	}
	slices.Sort(out)
	return out
}

// Analyze zet het eerste symptoom en de waarschijnlijke oorzaken op een rij.
func Analyze(in Input) Analysis {
	r := newReach(in.Subjects, in.World)
	entries := make([]Entry, 0, len(in.Entries)+len(in.Anomalies))
	entries = append(entries, in.Entries...)
	a := Analysis{Version: Version, Causes: []Cause{}, Anomalies: []Finding{}, Consumers: []Ref{}, MetricsUnknown: in.MetricsUnknown}
	for _, an := range in.Anomalies {
		f := finding(an)
		a.Anomalies = append(a.Anomalies, f)
		entries = append(entries, f.Entry())
	}
	slices.SortFunc(a.Anomalies, func(x, y Finding) int {
		return cmp.Or(x.At.Compare(y.At), strings.Compare(x.Hostname, y.Hostname), strings.Compare(x.Metric, y.Metric),
			strings.Compare(x.Label, y.Label))
	})
	Sort(entries)
	for _, c := range sortedIDs(r.consumers) {
		a.Consumers = append(a.Consumers, Ref{ID: c, Name: in.World.Clusters[c]})
	}

	first := r.firstSymptom(entries, in.Started, in.Subjects)
	a.FirstSymptom = first
	a.WindowFrom, a.WindowTo = first.From.Add(-Lookback), first.To.Add(candidateAfter)
	f := r.facts(entries, in.Started.Add(-symptomBefore))
	repairs := responses(entries, first)

	var cands []candidate
	for _, e := range entries {
		isChange := e.Class == events.ClassChange
		early := e.Source == SourceMetrics || (e.Source != SourceLog && e.Kind == journal.KindOOM)
		if (!isChange && !early) || e.Ts.Before(a.WindowFrom) || e.Ts.After(a.WindowTo) {
			continue
		}
		if e.JobID != nil && repairs[*e.JobID] {
			continue
		}
		if isChange {
			a.Changes++
		}
		cands = append(cands, r.score(Redact(e), first, f))
	}
	a.Causes = group(cands, r, first)
	a.Conclusion = conclusion(a)
	return a
}

// firstSymptom is het vroegste symptoom in wat geraakt was, rond het begin
// van het incident. Een OOM-kill en een afwijking in de metrics zijn een
// vroeg teken en kandidaat-oorzaak, geen eerste symptoom. Zonder symptoom in
// de tijdlijn is het het begin van het eerste onderwerp.
func (r *reach) firstSymptom(entries []Entry, started time.Time, subjects []Subject) Moment {
	lo, hi := started.Add(-symptomBefore), started.Add(symptomAfter)
	var best *Entry
	for i := range entries {
		e := &entries[i]
		if e.Class != events.ClassSymptom || e.Source == SourceMetrics || (e.Source != SourceLog && e.Kind == journal.KindOOM) {
			continue
		}
		if e.Ts.Before(lo) || e.Ts.After(hi) || !r.inScope(*e) {
			continue
		}
		if best == nil || e.From.Before(best.From) {
			best = e
		}
	}
	if best != nil {
		if len(r.symptomNodes) == 0 && best.NodeID != nil {
			r.symptomNodes[*best.NodeID] = true
		}
		summary := best.Summary
		if best.Source != SourceLog && best.Hostname != "" {
			summary += " op " + best.Hostname
		}
		return Moment{EntryID: best.ID, Ts: best.Ts, From: best.From, To: best.To, Summary: summary, Why: best.Delay}
	}
	m := Moment{Ts: started, From: started.Add(-statusDelay), To: started, Why: "de status komt uit de heartbeat van elke 10 s"}
	for _, s := range subjects {
		if m.Summary == "" || s.Since.Before(m.Ts) {
			m.Ts, m.From, m.To = s.Since, s.Since.Add(-statusDelay), s.Since
			m.Summary = subjectLabel(s.Type) + " " + s.Name + ": " + statusText(s.Status)
		}
	}
	if m.Summary == "" {
		m.Summary = "Begin van het incident"
	}
	return m
}

func subjectLabel(t string) string {
	switch t {
	case "cluster":
		return "Cluster"
	case "node":
		return "Node"
	case "service":
		return "Dienst"
	case "app":
		return "App"
	case "test_run":
		return "Test"
	}
	return t
}

func statusText(s string) string {
	switch s {
	case "degraded":
		return "verminderd"
	case "down":
		return "down"
	case "split_brain":
		return "split-brain"
	case "fail":
		return "mislukt"
	case "error":
		return "fout"
	}
	return s
}

// candidate is een verandering met haar score.
type candidate struct {
	e     Entry
	dist  float64
	where string
	score float64
	links []string
}

func (r *reach) score(e Entry, first Moment, f facts) candidate {
	c := candidate{e: e}
	c.dist, c.where = r.distance(e)
	c.links = r.links(e, f, c.dist)
	c.score = min(1, c.dist*timeFactor(e, first)*kindWeight(e)+linkBonus*float64(len(c.links)))
	return c
}

// distance is de afstand in de infrastructuur, met een zin.
func (r *reach) distance(e Entry) (float64, string) {
	if e.Action == "proxmox.host_offline" {
		host := e.Detail["connection"] + "/" + e.Detail["host"]
		if ids := r.hosts[host]; len(ids) > 0 {
			return 1, fmt.Sprintf("Op Proxmox-host %s, waar %s op draaien.", e.Detail["host"], r.hostnames(ids))
		}
		return 0.15, "Een Proxmox-host zonder geraakte nodes."
	}
	if e.NodeID != nil {
		id := *e.NodeID
		host := e.Hostname
		if host == "" {
			host = r.hostname(id)
		}
		cluster := e.ClusterID
		if cluster == nil {
			cluster = r.nodes[id].ClusterID
		}
		switch {
		case r.symptomNodes[id]:
			return 1, "Op " + host + ", de node die misging."
		case r.affected[id]:
			return 0.8, "Op " + host + ", in een cluster dat geraakt was."
		case cluster != nil && r.suppliers[*cluster]:
			return 0.6, fmt.Sprintf("Op %s, in %s, waar het geraakte cluster van afhangt.", host, r.w.Clusters[*cluster])
		case r.sameHost[id]:
			return 0.5, "Op " + host + ", op dezelfde Proxmox-host."
		}
	}
	if e.ClusterID != nil {
		switch c := *e.ClusterID; {
		case r.clusters[c]:
			return 0.8, "In cluster " + r.w.Clusters[c] + ", dat geraakt was."
		case r.suppliers[c]:
			return 0.6, "In cluster " + r.w.Clusters[c] + ", waar het geraakte cluster van afhangt."
		}
	}
	return 0.15, "Buiten wat geraakt was."
}

// timeFactor halveert elke tien minuten voor het eerste symptoom. Een
// verandering na het eerste symptoom kan alleen een verergering verklaren.
func timeFactor(e Entry, first Moment) float64 {
	switch {
	case e.To.Before(first.From):
		return math.Pow(0.5, first.From.Sub(e.To).Minutes()/halfLife.Minutes())
	case e.From.After(first.To):
		return afterFactor
	}
	return 1
}

// vmStops zijn de Proxmox-taken die een VM stilleggen.
var vmStops = map[string]bool{
	"qmstop": true, "vzstop": true, "qmshutdown": true, "vzshutdown": true, "qmreset": true, "qmsuspend": true,
	"vzsuspend": true, "hastop": true, "qmreboot": true, "vzreboot": true,
}

// vmChanges zijn de Proxmox-taken die een VM wezenlijk veranderen.
var vmChanges = map[string]bool{
	"qmigrate": true, "vzmigrate": true, "qmrollback": true, "vzrollback": true, "qmrestore": true, "vzrestore": true,
	"qmconfig": true, "qmresize": true, "vzresize": true, "qmmove": true, "qmdiskmove": true,
}

// kindWeight is het gewicht van de soort: een taak, commit, pakketupdate of
// VM-actie het zwaarst, dan een configuratiebestand, een herstart en een
// SSH-login.
func kindWeight(e Entry) float64 {
	if e.Source == SourceMetrics {
		switch e.Action {
		case "disk":
			return 0.9
		case "memory":
			return 0.6
		}
		return 0.5
	}
	if e.Source == SourceLog {
		switch e.Action {
		case "gitops.change_applied", "gitops.change_failed", "gitops.reapply_requested", "cluster.deployed",
			"proxmox.host_offline", "failover.fault_injected", "vm.missing", "kube.rolled_out", "kube.applied", "kube.rolled_back",
			"kube.snapshot_restored", "kube.snapshot_restore_started":
			return 1
		case "vm.status_changed":
			if to := e.Detail["to"]; to == "stopped" || to == "paused" {
				return 1
			}
			return 0.3
		case "job.queued", "job.succeeded", "job.failed", "job.retried", "agent.command", "drift.remediation_requested",
			"kube.scaled", "kube.restarted", "kube.deleted", "kube.drained", "kube.node_removed", "cluster.heal_started",
			"cluster.heal_done", "node.heal_started", "node.heal_done":
			return 0.9
		case "drift.detected", "drift.changed":
			return 0.8
		case "vm.moved", "node.lifecycle_changed", "agent.enrolled", "agent.revoked", "failover.fault_cleared", "kube.cordoned",
			"kube.uncordoned":
			return 0.6
		case "cluster.spec_changed", "vip.created", "vip.updated", "vip.deleted":
			return 0.5
		}
		return 0.3
	}
	w := 0.3
	switch e.Kind {
	case journal.KindPackage:
		w = 1
	case journal.KindVM:
		switch {
		case vmStops[e.Action]:
			w = 1
		case vmChanges[e.Action]:
			w = 0.8
		}
	case journal.KindService:
		switch e.Action {
		case "stopped", "disabled":
			w = 0.9
		case "restarted", "reloaded", "changed":
			w = 0.8
		}
	case journal.KindOOM, journal.KindCommand:
		w = 0.9
	case journal.KindFile:
		w = 0.8
	case journal.KindBoot, journal.KindNetwork, journal.KindUser, journal.KindFacts:
		w = 0.6
	case journal.KindLogin:
		w = 0.4
	}
	if e.Source == journal.SourceClusterForge {
		w = max(w, 0.9)
	}
	return w
}

// links zijn de vaste verbanden tussen een verandering en de symptomen, elk
// met een zin. dist is de afstand van de verandering.
func (r *reach) links(e Entry, f facts, dist float64) []string {
	var out []string
	node := uuid.Nil
	if e.NodeID != nil {
		node = *e.NodeID
	}
	host := e.Hostname
	if host == "" && node != uuid.Nil {
		host = r.hostname(node)
	}
	journaled := e.Source != SourceLog && e.Source != SourceMetrics
	switch {
	case journaled && e.Kind == journal.KindPackage && node != uuid.Nil:
		for _, u := range f.units(node) {
			for _, p := range packageNames(e.Subject) {
				if !packageMatches(p, u) {
					continue
				}
				if at, ok := f.firstFailure(node, u, e.Ts); ok {
					out = append(out, fmt.Sprintf("%s op %s faalde %s na deze update van %s.", u, host, span(at.Sub(e.Ts)), p))
					break
				}
			}
		}
	case journaled && e.Kind == journal.KindFile && node != uuid.Nil:
		for _, u := range f.units(node) {
			if !fileMatches(e.Subject, u) {
				continue
			}
			if at, ok := f.firstFailure(node, u, e.Ts); ok {
				out = append(out, fmt.Sprintf("Het bestand hoort bij %s, dat %s later faalde op %s.", u, span(at.Sub(e.Ts)), host))
			}
		}
	case journaled && e.Kind == journal.KindOOM && node != uuid.Nil:
		for _, u := range f.units(node) {
			if !processMatches(e.Subject, u) {
				continue
			}
			if _, ok := f.firstFailure(node, u, e.Ts.Add(-time.Minute)); ok {
				out = append(out, fmt.Sprintf("De OOM-killer stopte het proces van %s.", u))
			}
		}
	case e.Source == SourceMetrics && e.Action == "disk" && node != uuid.Nil:
		var after []string
		for _, u := range f.units(node) {
			if _, ok := f.firstFailure(node, u, e.Ts); ok {
				after = append(after, u)
			}
		}
		switch at, ok := f.diskFull[node]; {
		case len(after) > 0:
			out = append(out, fmt.Sprintf("De schijf liep vol voordat %s faalde.", list(after)))
		case ok && !at.Before(e.Ts):
			out = append(out, "De schijf liep vol voordat "+host+" verminderd raakte.")
		}
	case e.Action == "proxmox.host_offline":
		var hit []uuid.UUID
		for _, id := range r.hosts[e.Detail["connection"]+"/"+e.Detail["host"]] {
			if at, ok := f.down[id]; ok && !at.Before(e.From) {
				hit = append(hit, id)
			}
		}
		switch len(hit) {
		case 0:
		case 1:
			out = append(out, fmt.Sprintf("De Proxmox-host ging offline voordat %s erop down ging.", r.hostnames(hit)))
		default:
			out = append(out, fmt.Sprintf("De Proxmox-host ging offline voordat %d VM's erop down gingen: %s.", len(hit), r.hostnames(hit)))
		}
	}
	if e.Source == SourceLog && e.Action == "kube.rolled_out" {
		if l, ok := rolloutLink(e, f); ok {
			out = append(out, l)
		}
	}
	if stopsVM(e) && node != uuid.Nil {
		if at, ok := f.down[node]; ok && !at.Before(e.Ts) {
			what := "stopte in"
			if e.Action == "vm.missing" {
				what = "verdween uit"
			}
			out = append(out, "De VM "+what+" Proxmox voordat "+host+" down ging.")
		}
	}
	if (stopsVM(e) || (journaled && e.Kind == journal.KindBoot)) && node != uuid.Nil {
		for _, v := range f.vipLost {
			if v.owner == node && !v.ts.Before(e.Ts) && v.ts.Sub(e.Ts) <= vipWindow {
				out = append(out, fmt.Sprintf("%s had VIP %s en herstartte of stopte vlak voordat het VIP geen eigenaar meer had.", host, v.address))
				break
			}
		}
	}
	if (e.Action == "gitops.change_failed" || e.Action == "job.failed") && dist >= 0.5 {
		out = append(out, "De wijziging mislukte.")
	}
	return out
}

// stopsVM is true voor een VM die in Proxmox stopte of verdween.
func stopsVM(e Entry) bool {
	if e.Source == SourceLog {
		to := e.Detail["to"]
		return e.Action == "vm.missing" || (e.Action == "vm.status_changed" && (to == "stopped" || to == "paused"))
	}
	return e.Kind == journal.KindVM && vmStops[e.Action]
}

// group voegt regels samen die één verandering zijn: dezelfde taak, of
// pakketten of bestanden op dezelfde node vlak na elkaar. Een groep krijgt
// de hoogste score van haar regels.
func group(cands []candidate, r *reach, first Moment) []Cause {
	keys := make([]string, len(cands))
	type run struct {
		key  string
		last time.Time
	}
	runs := map[string]*run{}
	for i, c := range cands {
		e := c.e
		switch {
		case e.JobID != nil:
			keys[i] = "job:" + e.JobID.String()
		case e.Source != SourceLog && e.Source != SourceMetrics && e.NodeID != nil &&
			(e.Kind == journal.KindPackage || e.Kind == journal.KindFile):
			k := e.Kind + ":" + e.NodeID.String()
			if ru := runs[k]; ru != nil && e.Ts.Sub(ru.last) <= groupGap {
				ru.last = e.Ts
				keys[i] = ru.key
			} else {
				runs[k] = &run{key: k + ":" + e.ID, last: e.Ts}
				keys[i] = runs[k].key
			}
		default:
			keys[i] = e.ID
		}
	}
	var order []string
	members := map[string][]candidate{}
	for i, k := range keys {
		if members[k] == nil {
			order = append(order, k)
		}
		members[k] = append(members[k], cands[i])
	}
	var out []Cause
	for _, k := range order {
		ms := members[k]
		best := ms[0]
		for _, m := range ms[1:] {
			if m.score > best.score || (m.score == best.score && m.e.Ts.After(best.e.Ts)) {
				best = m
			}
		}
		lvl := level(best.score)
		if lvl == "" {
			continue
		}
		rep := representative(ms, best)
		c := Cause{
			EntryID: rep.e.ID, Ts: best.e.Ts, Level: lvl, Score: math.Round(best.score*100) / 100, Kind: rep.e.Kind,
			Summary: rep.e.Summary, Hostname: rep.e.Hostname, Link: rep.e.Link, Also: len(ms) - 1,
		}
		if c.Hostname == "" {
			c.Hostname = best.e.Hostname
		}
		if rep.e.ClusterID != nil {
			c.Cluster = r.w.Clusters[*rep.e.ClusterID]
		}
		c.Reasons = append(c.Reasons, best.where, capitalize(when(best.e.Ts, first.From, first.To))+".")
		seen := map[string]bool{}
		for _, m := range ms {
			if c.Commit == "" {
				c.Commit = m.e.Detail["commit"]
			}
			for _, l := range m.links {
				if !seen[l] {
					seen[l] = true
					c.Reasons = append(c.Reasons, l)
					c.links++
				}
			}
		}
		if n := len(ms) - 1; n > 0 {
			switch {
			case strings.HasPrefix(k, "job:"):
				what := "dezelfde taak"
				if j := jobTitle(ms); j != "" {
					what = "de taak '" + j + "'"
				}
				c.Reasons = append(c.Reasons, fmt.Sprintf("Samen met %s van %s.", count(n, "andere regel", "andere regels"), what))
			case strings.HasPrefix(k, journal.KindPackage+":"):
				c.Reasons = append(c.Reasons, fmt.Sprintf("Samen met %s in dezelfde update.", count(n, "ander pakket", "andere pakketten")))
			default:
				c.Reasons = append(c.Reasons, fmt.Sprintf("Samen met %s binnen twee minuten.", count(n, "ander bestand", "andere bestanden")))
			}
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b Cause) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.links, a.links), b.Ts.Compare(a.Ts), strings.Compare(a.EntryID, b.EntryID))
	})
	if len(out) > maxCauses {
		out = out[:maxCauses]
	}
	if out == nil {
		out = []Cause{}
	}
	return out
}

func level(score float64) string {
	switch {
	case score >= 0.5:
		return LevelLikely
	case score >= 0.2:
		return LevelPossible
	case score >= 0.05:
		return LevelWeak
	}
	return ""
}

// representative is de regel die een groep het best beschrijft: bij een
// taak de Git-wijziging, de mislukte taak of het herstel van Zelfherstel,
// dan de afronding van de taak, anders de regel met de hoogste score.
func representative(ms []candidate, best candidate) candidate {
	for _, want := range []string{"gitops.change_failed", "gitops.change_applied", "job.failed", "cluster.heal_done", "node.heal_done",
		"cluster.heal_started", "node.heal_started", "job.succeeded", "job.queued"} {
		for _, m := range ms {
			if m.e.Source == SourceLog && m.e.Action == want {
				return m
			}
		}
	}
	return best
}

func jobTitle(ms []candidate) string {
	for _, m := range ms {
		if m.e.Job != "" {
			return m.e.Job
		}
	}
	return ""
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// phrase is een oorzaak in één zin: wat, waar en wanneer.
func phrase(c Cause) string {
	s := c.Summary
	if c.Hostname != "" && !strings.Contains(s, c.Hostname) {
		s += " op " + c.Hostname
	}
	return s
}

func conclusion(a Analysis) string {
	var top *Cause
	if len(a.Causes) > 0 {
		top = &a.Causes[0]
	}
	whenTop := func() string {
		return when(top.Ts, a.FirstSymptom.From, a.FirstSymptom.To)
	}
	if a.Changes == 0 {
		s := "ClusterForge zag in de twee uur ervoor geen verandering."
		switch {
		case top != nil && top.Level == LevelLikely:
			return s + " Waarschijnlijke oorzaak: " + phrase(*top) + ", " + whenTop() + "."
		case len(a.Anomalies) > 0:
			return s + " De afwijkingen in de metrics staan hieronder; de oorzaak ligt waarschijnlijk buiten de nodes."
		case a.MetricsUnknown:
			return s + " De metrics waren niet te lezen; de oorzaak ligt waarschijnlijk buiten de nodes."
		}
		return s + " Ook de metrics toonden geen afwijking; de oorzaak ligt waarschijnlijk buiten de nodes."
	}
	switch {
	case top == nil:
		return "Wat er in de twee uur ervoor veranderde, ligt te ver van de storing om oorzaak te zijn."
	case top.Level == LevelLikely:
		return "Waarschijnlijke oorzaak: " + phrase(*top) + ", " + whenTop() + "."
	case top.Level == LevelPossible:
		return "Geen duidelijke oorzaak. Het meest verdacht: " + phrase(*top) + ", " + whenTop() + "."
	}
	return "Geen duidelijke oorzaak: wat er in de twee uur ervoor veranderde, ligt ver van de storing in tijd of plaats."
}

// finding maakt van een afwijking een regel voor het rapport.
func finding(a NodeAnomaly) Finding {
	f := Finding{NodeID: a.NodeID, Hostname: a.Hostname, Metric: a.Metric, Label: a.Label, Before: round1(a.Before),
		After: round1(a.After), At: a.At}
	switch a.Metric {
	case "cpu":
		f.Summary = fmt.Sprintf("CPU van %s piekte op %.0f %% (gemiddeld %.0f %% in de twee uur ervoor)", a.Hostname, a.After, a.Before)
	case "memory":
		f.Summary = fmt.Sprintf("Geheugen van %s liep op tot %.0f %% (gemiddeld %.0f %% in de twee uur ervoor)", a.Hostname, a.After, a.Before)
	case "disk":
		f.Summary = fmt.Sprintf("Schijf %s van %s liep vol tot %.0f %% (gemiddeld %.0f %% in de twee uur ervoor)", a.Label, a.Hostname, a.After, a.Before)
	case "network_errors":
		f.Summary = fmt.Sprintf("Netwerkfouten op %s: %s per seconde (daarvoor bijna geen)", a.Hostname,
			strings.Replace(strconv.FormatFloat(a.After, 'f', 1, 64), ".", ",", 1))
	default:
		f.Summary = a.Metric + " van " + a.Hostname + " week af"
	}
	return f
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Entry maakt van een afwijking een regel in de tijdlijn.
func (f Finding) Entry() Entry {
	id := f.NodeID
	return Entry{
		ID: "m" + f.NodeID.String() + ":" + f.Metric + ":" + f.Label, Ts: f.At, From: f.At.Add(-time.Minute), To: f.At,
		Delay: "de metrics hebben een punt per minuut", Class: events.ClassSymptom, Source: SourceMetrics, Kind: "metrics",
		Action: f.Metric, Summary: f.Summary, NodeID: &id, Hostname: f.Hostname, Subject: f.Label,
		Link: "/nodes/detail?id=" + f.NodeID.String(), Detail: map[string]string{},
	}
}
