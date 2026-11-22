package disaster

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Target is een node die de storing krijgt: zijn VM gaat hard uit, of een
// unit stopt.
type Target struct {
	NodeID    uuid.UUID       `json:"node_id"`
	Hostname  string          `json:"hostname"`
	ClusterID uuid.UUID       `json:"cluster_id"`
	Cluster   string          `json:"cluster"`
	Unit      string          `json:"unit,omitempty"`
	VM        *failover.VMRef `json:"vm,omitempty"`
}

// Prediction is een status met de reden.
type Prediction struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// VIPPlan is een VIP dat de meting opvraagt.
type VIPPlan struct {
	Address string         `json:"address"`
	Probe   failover.Probe `json:"probe"`
}

// Rollen van een gemeten cluster.
const (
	// RoleTarget: een node van het cluster krijgt de storing.
	RoleTarget = "target"
	// RoleDependent: het cluster hangt volgens de graaf af van een geraakt
	// cluster, of de wat-als voorspelt dat het geraakt wordt.
	RoleDependent = "dependent"
)

// ClusterPlan is een cluster dat de rampoefening meet, met de voorspelling.
type ClusterPlan struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Environment string    `json:"environment"`
	Role        string    `json:"role"`
	// Before is de status bij de start; het herstel wacht tot het cluster
	// minstens zo gezond is.
	Before    string     `json:"before"`
	Predicted Prediction `json:"predicted"`
	VIPs      []VIPPlan  `json:"vips"`
}

// Definition is de invoer van een run bij de start, met de voorspelling die
// de admin zag. Een latere wijziging van het scenario verandert hem niet.
type Definition struct {
	ScenarioID  uuid.UUID `json:"scenario_id"`
	ClusterID   uuid.UUID `json:"cluster_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	Params      Params    `json:"params"`
	Expect      Expect    `json:"expect"`
	// Unit is wat stopt en weer start; leeg bij host_down en sequence.
	Unit string `json:"unit,omitempty"`
	// Steps zijn bij sequence de stappen met hun nodes. Targets zijn dan
	// alle nodes die een stap raakt.
	Steps    []PlannedStep `json:"steps,omitempty"`
	Targets  []Target      `json:"targets"`
	Clusters []ClusterPlan `json:"clusters"`
	// Skipped zijn draaiende VM's op de host die niet in ClusterForge staan;
	// ze blijven aan.
	Skipped []string `json:"skipped"`
	// Notes is wat de voorspelling onzeker maakt.
	Notes []string `json:"notes"`
}

// targetClusters zijn de clusters met een node die de storing krijgt,
// gesorteerd op id.
func (d Definition) targetClusters() []uuid.UUID {
	var out []uuid.UUID
	for _, c := range d.Clusters {
		if c.Role == RoleTarget {
			out = append(out, c.ID)
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out
}

// clusterIDs zijn alle gemeten clusters.
func (d Definition) clusterIDs() []uuid.UUID {
	out := make([]uuid.UUID, 0, len(d.Clusters))
	for _, c := range d.Clusters {
		out = append(out, c.ID)
	}
	return out
}

// Preview is de voorcontrole met de voorspelling, zonder iets te doen.
type Preview struct {
	Definition
	Checks []failover.Check
	OK     bool
	// Confirm zijn de slugs die een admin intikt: van elk cluster met een
	// node die de storing krijgt.
	Confirm []string
	// Rehearsal is bij een DR-repetitie het plan van de back-upcontrole.
	Rehearsal *backups.RehearsalPlan
}

// Preview doet de voorcontrole en voorspelt wat er gebeurt.
func (s *Service) Preview(ctx context.Context, id uuid.UUID) (Preview, error) {
	row, err := s.q.GetDisasterScenario(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preview{}, ErrNotFound
	}
	if err != nil {
		return Preview{}, err
	}
	sc := Decode(row)
	if sc.Kind == DRRehearsal {
		return s.previewRehearsal(ctx, sc)
	}
	return s.plan(ctx, sc, uuid.Nil)
}

// rank ordent statussen van gezond naar slecht; onbekend telt niet.
func rank(st string) int {
	switch st {
	case string(status.Healthy):
		return 0
	case string(status.Degraded):
		return 1
	case string(status.Down), string(status.SplitBrain):
		return 2
	}
	return -1
}

// effective is de status van een cluster met de ergste dienst erin: de
// evaluator kijkt naar de nodes, deps naar de diensten en hun afhankelijkheden.
func effective(row store.ListDisasterClustersRow) (string, string) {
	if rank(row.ServiceStatus) > rank(row.Status) {
		return row.ServiceStatus, row.ServiceReason
	}
	return row.Status, row.StatusReason
}

var statusLabels = map[string]string{
	string(status.Healthy): "gezond", string(status.Degraded): "verminderd", string(status.Down): "down",
	string(status.SplitBrain): "split-brain", string(status.Unknown): "onbekend",
}

// StatusLabel is een status in gewone taal.
func StatusLabel(st string) string {
	if l, ok := statusLabels[st]; ok {
		return l
	}
	return st
}

// plan rekent de doelen, de voorspelling en de voorcontrole uit. self is de
// taak die de rampoefening uitvoert; zijn eigen sloten tellen niet als
// bezet.
func (s *Service) plan(ctx context.Context, sc Scenario, self uuid.UUID) (Preview, error) {
	p := Preview{
		Definition: Definition{
			ScenarioID: sc.ID, ClusterID: sc.ClusterID, Name: sc.Name, Kind: sc.Kind, Description: Describe(sc.Kind, sc.P),
			Params: sc.P, Expect: sc.E, Unit: unitOf(sc.Kind, sc.P), Targets: []Target{}, Clusters: []ClusterPlan{},
			Skipped: []string{}, Notes: []string{},
		},
		Checks: []failover.Check{}, Confirm: []string{},
	}
	add := func(name string, ok bool, detail string) {
		p.Checks = append(p.Checks, failover.Check{Name: name, OK: ok, Detail: detail})
	}
	c, err := s.q.GetCluster(ctx, sc.ClusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if s.World == nil {
		return p, errors.New("de wat-als is niet ingesteld")
	}
	w, err := s.World(ctx)
	if err != nil {
		return p, err
	}
	if w.Graph == nil {
		w.Graph = deps.Build(deps.World{})
	}

	// 1. De doelen.
	nodes := map[uuid.UUID]store.ListFailoverNodesRow{}
	var loose []string
	var seq *seqPlan
	switch sc.Kind {
	case HostDown:
		loose, err = s.hostTargets(ctx, &p, sc, w, add)
	case ServiceDown, VIPLoss:
		err = s.nodeTargets(ctx, &p, c, w.Topo, nodes, add)
	case Sequence:
		seq, err = s.seqTargets(ctx, &p, sc, c, w.Topo, add)
	default:
		return p, fmt.Errorf("onbekende soort %q", sc.Kind)
	}
	if err != nil {
		return p, err
	}
	if len(p.Targets) == 0 {
		add("Doel", false, "er is niets om te laten uitvallen: "+noTargets(sc))
	}

	// 2. De geraakte clusters en de voorspelling.
	targets := map[uuid.UUID]bool{}
	for _, t := range p.Targets {
		targets[t.ClusterID] = true
	}
	var pred map[uuid.UUID]Prediction
	var notes []string
	if seq != nil {
		pred, notes = predictSequence(w, c, p.Definition, seq)
	} else {
		pred, notes = predict(w, sc, c, targets)
	}
	p.Notes = append(p.Notes, notes...)
	measured := map[uuid.UUID]string{}
	for id := range targets {
		measured[id] = RoleTarget
	}
	for id, pr := range pred {
		if measured[id] == "" && pr.Status != string(status.Healthy) {
			measured[id] = RoleDependent
		}
	}
	for id := range dependents(w.Graph, targets) {
		if measured[id] == "" {
			measured[id] = RoleDependent
		}
	}
	ids := make([]uuid.UUID, 0, len(measured))
	for id := range measured {
		ids = append(ids, id)
	}
	rows, err := s.q.ListDisasterClusters(ctx, ids)
	if err != nil {
		return p, err
	}
	before := map[uuid.UUID]string{}
	for _, row := range rows {
		before[row.ID], _ = effective(row)
	}
	for id, role := range measured {
		cl, err := s.q.GetCluster(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return p, err
		}
		vips, err := s.q.ListVIPsByCluster(ctx, id)
		if err != nil {
			return p, err
		}
		cp := ClusterPlan{
			ID: id, Name: cl.Name, Slug: cl.Slug, Environment: string(cl.Environment), Role: role, Before: cmp.Or(before[id], cl.Status),
			Predicted: Prediction{Status: string(status.Healthy), Reason: "geen uitval verwacht"}, VIPs: []VIPPlan{},
		}
		if pr, ok := pred[id]; ok {
			cp.Predicted = pr
		}
		for _, v := range vips {
			addr := v.Vip.Address.String()
			cp.VIPs = append(cp.VIPs, VIPPlan{Address: addr, Probe: s.Probes(cl, addr)})
		}
		p.Clusters = append(p.Clusters, cp)
		if role == RoleTarget {
			p.Confirm = append(p.Confirm, cl.Slug)
		}
	}
	slices.SortFunc(p.Clusters, func(a, b ClusterPlan) int {
		return cmp.Or(cmp.Compare(roleOrder(a.Role), roleOrder(b.Role)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	slices.Sort(p.Confirm)

	// 3. De voorcontrole.
	if err := s.checkClusters(ctx, &p, loose, nodes, self, add); err != nil {
		return p, err
	}
	p.OK = !slices.ContainsFunc(p.Checks, func(ch failover.Check) bool { return !ch.OK })
	return p, nil
}

func roleOrder(r string) int {
	if r == RoleTarget {
		return 0
	}
	return 1
}

func noTargets(sc Scenario) string {
	switch sc.Kind {
	case HostDown:
		return "op " + sc.P.Host + " draait geen gekoppelde VM"
	case ServiceDown:
		return "geen actieve node van dit cluster heeft " + sc.P.Unit
	case Sequence:
		return "geen stap raakt een actieve node met een agent"
	}
	return "geen actieve node van dit cluster heeft keepalived"
}

// hostTargets zoekt de gekoppelde VM's op de host. Gekoppelde VM's die niet
// bij een cluster horen, geeft het terug: daarvoor start de oefening niet.
func (s *Service) hostTargets(ctx context.Context, p *Preview, sc Scenario, w risk.WhatIfWorld, add func(string, bool, string)) ([]string, error) {
	m, g := w.Topo, w.Graph
	if sc.P.ConnectionID == nil {
		add("Host", false, "het scenario heeft geen Proxmox-host")
		return nil, nil
	}
	conn, host := *sc.P.ConnectionID, sc.P.Host
	hi := slices.IndexFunc(m.Hosts, func(h topo.Host) bool { return h.Conn == conn && h.Name == host })
	switch {
	case hi < 0:
		add("Host", false, host+" staat niet (meer) in Proxmox")
		return nil, nil
	case !m.Hosts[hi].Online:
		add("Host", false, host+" is niet online in Proxmox")
		return nil, nil
	}
	var ha, loose []string
	selfHere := false
	for _, gu := range m.Guests {
		if gu.Conn != conn || gu.Host != host || gu.Template || gu.Pool == proxmox.SandboxPool || gu.Status != "running" {
			continue
		}
		if m.Self.Found() && m.Self.Conn == conn && m.Self.VMID == gu.VMID {
			selfHere = true
		}
		if gu.HA() {
			ha = append(ha, gu.Label())
		}
		if gu.NodeID == nil {
			p.Skipped = append(p.Skipped, gu.Label())
			continue
		}
		n, ok := g.Node(*gu.NodeID)
		if !ok || n.ClusterID == nil {
			loose = append(loose, gu.Label())
			continue
		}
		cl, _ := g.Cluster(*n.ClusterID)
		p.Targets = append(p.Targets, Target{
			NodeID: n.ID, Hostname: n.Hostname, ClusterID: cl.ID, Cluster: cl.Name, VM: &failover.VMRef{ConnectionID: conn, VMID: gu.VMID},
		})
	}
	slices.SortFunc(p.Targets, func(a, b Target) int { return cmp.Compare(a.Hostname, b.Hostname) })
	slices.Sort(p.Skipped)
	add("Host", true, fmt.Sprintf("%s is online in Proxmox (%s); de host zelf blijft aan", host, m.ConnName(conn)))
	switch {
	case selfHere:
		add("ClusterForge zelf", false, "ClusterForge draait zelf op "+host+" en zou dus mee uitvallen")
	case !m.Self.Found() && len(p.Skipped) > 0:
		add("ClusterForge zelf", false, "ClusterForge weet niet op welke VM hij draait, en op "+host+" draaien VM's buiten ClusterForge ("+
			list(p.Skipped)+"); kies bij Instellingen, Waar ClusterForge draait, de VM van ClusterForge")
	default:
		add("ClusterForge zelf", true, "ClusterForge draait niet op "+host)
	}
	if len(ha) > 0 {
		add("Geen HA", false, "onder Proxmox HA: "+list(ha)+"; de HA-manager zou ze zelf starten of verplaatsen, en dan toont de oefening niet wat een echte uitval doet")
	} else {
		add("Geen HA", true, "geen VM op "+host+" staat onder Proxmox HA")
	}
	return loose, nil
}

// nodeTargets zijn bij service_down en vip_loss de actieve nodes van het
// cluster waarop de unit staat.
func (s *Service) nodeTargets(ctx context.Context, p *Preview, c store.Cluster, m topo.Model, nodes map[uuid.UUID]store.ListFailoverNodesRow,
	add func(string, bool, string)) error {
	rows, err := s.q.ListFailoverNodes(ctx, &c.ID)
	if err != nil {
		return err
	}
	unit := p.Unit
	active, total := 0, 0
	var old, self []string
	for _, n := range rows {
		if n.Lifecycle != store.NodeLifecycleActive {
			continue
		}
		state, ok := health.ServiceStates(n.Services)[unit]
		if !ok {
			continue
		}
		total++
		if state == "active" {
			active++
		}
		nodes[n.ID] = n
		p.Targets = append(p.Targets, Target{NodeID: n.ID, Hostname: n.Hostname, ClusterID: c.ID, Cluster: c.Name, Unit: unit})
		if !n.HasAgent || n.AgentProtocol < protocol.ApplySince {
			old = append(old, n.Hostname)
		}
		if m.Self.Found() && n.ProxmoxID != nil && n.PveVmid != nil && *n.ProxmoxID == m.Self.Conn && int(*n.PveVmid) == m.Self.VMID {
			self = append(self, n.Hostname)
		}
	}
	if total == 0 {
		return nil
	}
	if len(old) > 0 {
		add("Agent", false, "de agent op "+list(old)+" is te oud voor deploystappen of ontbreekt; werk hem bij met het installatiescript")
	} else {
		add("Agent", true, "de agents kunnen "+unit+" stoppen en weer starten")
	}
	if active > 0 {
		add("Dienst draait", true, fmt.Sprintf("%s draait op %d van de %d nodes", unit, active, total))
	} else {
		add("Dienst draait", false, unit+" draait op geen enkele node")
	}
	if p.Kind == VIPLoss {
		vips, err := s.q.ListVIPsByCluster(ctx, c.ID)
		if err != nil {
			return err
		}
		if len(vips) == 0 {
			add("VIP", false, "dit cluster heeft geen VIP")
		} else {
			add("VIP", true, fmt.Sprintf("%d VIP's, die zonder keepalived geen houder hebben", len(vips)))
		}
	}
	if len(self) > 0 {
		add("ClusterForge zelf", false, "ClusterForge draait zelf op "+list(self))
	}
	return nil
}

// predict geeft per cluster de voorspelde status uit de wat-als.
func predict(w risk.WhatIfWorld, sc Scenario, c store.Cluster, targets map[uuid.UUID]bool) (map[uuid.UUID]Prediction, []string) {
	g := w.Graph
	var keys, notes []string
	switch sc.Kind {
	case HostDown:
		if sc.P.ConnectionID != nil {
			keys = append(keys, topo.HostKey(*sc.P.ConnectionID, sc.P.Host))
		}
	case ServiceDown:
		for _, sv := range g.Services {
			if sv.State == deps.StateConfirmed && !sv.External() && sv.Unit == sc.P.Unit && inCluster(g, sv, c.ID) {
				keys = append(keys, topo.ServiceKey(sv.ID))
			}
		}
		if len(keys) == 0 {
			notes = append(notes, "Bij Afhankelijkheden staat geen bevestigde dienst "+sc.P.Unit+" in "+c.Name+
				"; welke clusters ervan afhangen, kan de voorspelling niet weten.")
		}
	case VIPLoss:
		keys = append(keys, topo.ClusterKey(c.ID))
	}
	out := map[uuid.UUID]Prediction{}
	worse := func(id uuid.UUID, st, reason string) {
		if cur, ok := out[id]; !ok || rank(st) > rank(cur.Status) {
			out[id] = Prediction{Status: st, Reason: reason}
		}
	}
	comps, err := w.Resolve(keys)
	if err != nil {
		notes = append(notes, "De wat-als kent dit onderdeel niet: "+err.Error()+".")
		comps = nil
	}
	if len(comps) > 0 {
		res := w.Run(comps, false)
		for _, co := range res.Clusters {
			f := co.Final()
			worse(co.ID, f.Status, f.Reason)
		}
		for _, so := range res.Services {
			sv, ok := g.Service(so.ID)
			if !ok {
				continue
			}
			id := clusterOf(g, sv)
			if id == nil {
				continue
			}
			st := string(status.Degraded)
			if so.Impact == string(deps.ImpactDown) {
				st = string(status.Down)
			}
			worse(*id, st, so.Name+": "+so.Reason)
		}
		notes = append(notes, res.Notes...)
	}
	switch sc.Kind {
	case ServiceDown:
		out[c.ID] = Prediction{Status: string(status.Down), Reason: sc.P.Unit + " staat op alle nodes stil"}
	case VIPLoss:
		out[c.ID] = Prediction{Status: string(status.Down), Reason: "keepalived staat op alle nodes stil, dus geen node heeft het VIP"}
	}
	// Een doelcluster dat de wat-als niet noemt, blijft volgens haar gezond.
	for id := range targets {
		if _, ok := out[id]; !ok {
			out[id] = Prediction{Status: string(status.Healthy), Reason: "de wat-als verwacht geen uitval"}
		}
	}
	return out, notes
}

// inCluster is true voor een dienst van het cluster of van een node ervan.
func inCluster(g *deps.Graph, sv deps.Svc, id uuid.UUID) bool {
	c := clusterOf(g, sv)
	return c != nil && *c == id
}

// clusterOf is het cluster van een dienst, of van zijn node.
func clusterOf(g *deps.Graph, sv deps.Svc) *uuid.UUID {
	if sv.ClusterID != nil {
		return sv.ClusterID
	}
	if sv.NodeID != nil {
		if n, ok := g.Node(*sv.NodeID); ok {
			return n.ClusterID
		}
	}
	return nil
}

// dependents zijn de clusters met een dienst die, rechtstreeks of via
// andere, afhangt van een dienst van een geraakt cluster.
func dependents(g *deps.Graph, targets map[uuid.UUID]bool) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	seen := map[uuid.UUID]bool{}
	var queue []uuid.UUID
	for _, sv := range g.Services {
		if c := clusterOf(g, sv); c != nil && targets[*c] {
			seen[sv.ID] = true
			queue = append(queue, sv.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, d := range g.Consumers(id) {
			if d.State != deps.StateConfirmed || seen[d.From] {
				continue
			}
			seen[d.From] = true
			queue = append(queue, d.From)
			if sv, ok := g.Service(d.From); ok {
				if c := clusterOf(g, sv); c != nil && !targets[*c] {
					out[*c] = true
				}
			}
		}
	}
	return out
}

// checkClusters doet de controles die voor elke soort gelden.
func (s *Service) checkClusters(ctx context.Context, p *Preview, loose []string, nodes map[uuid.UUID]store.ListFailoverNodesRow, self uuid.UUID,
	add func(string, bool, string)) error {
	var envs, prodTargets, prodOthers, unhealthy []string
	for _, cp := range p.Clusters {
		switch {
		case cp.Role == RoleTarget && cp.Environment == string(store.EnvironmentProd):
			prodTargets = append(prodTargets, cp.Name)
		case cp.Role == RoleTarget:
			envs = append(envs, cp.Name+" ("+cp.Environment+")")
		case cp.Environment == string(store.EnvironmentProd):
			prodOthers = append(prodOthers, cp.Name)
		}
		if cp.Role == RoleTarget && cp.Before != string(status.Healthy) {
			unhealthy = append(unhealthy, cp.Name+" is "+StatusLabel(cp.Before))
		}
	}
	switch {
	case len(prodTargets) > 0:
		add("Omgeving", false, "de storing raakt een prodnode in "+list(prodTargets)+"; live rampoefeningen kunnen alleen in lab en test")
	case len(loose) > 0:
		add("Omgeving", false, "de storing raakt "+list(loose)+", die bij geen cluster horen; alleen nodes in lab- en testclusters mogen uitvallen")
	case len(envs) > 0:
		add("Omgeving", true, "de storing raakt alleen "+list(envs))
	}
	if len(prodOthers) > 0 {
		add("Prod niet geraakt", false, "prodcluster "+list(prodOthers)+" hangt af van wat uitvalt, of wordt volgens de wat-als geraakt")
	} else {
		add("Prod niet geraakt", true, "geen prodcluster hangt af van wat uitvalt")
	}
	if len(unhealthy) > 0 {
		add("Clusters gezond", false, list(unhealthy))
	} else if len(p.Targets) > 0 {
		add("Clusters gezond", true, "elk geraakt cluster is gezond")
	}

	// Verse heartbeats van elk geraakt cluster, en wat op de nodes draait.
	now := time.Now()
	var waiting, docker []string
	for _, cp := range p.Clusters {
		if cp.Role != RoleTarget {
			continue
		}
		vips := make([]string, 0, len(cp.VIPs))
		for _, v := range cp.VIPs {
			vips = append(vips, v.Address)
		}
		snap, err := s.Gate.Look(ctx, cp.ID, vips, now.Add(-s.Fresh))
		if err != nil {
			return err
		}
		waiting = append(waiting, snap.Waiting...)
		if p.Kind == HostDown {
			rows, err := s.q.ListFailoverNodes(ctx, &cp.ID)
			if err != nil {
				return err
			}
			for _, n := range rows {
				nodes[n.ID] = n
			}
		}
	}
	if len(waiting) > 0 {
		add("Verse heartbeats", false, fmt.Sprintf("geen heartbeat van de laatste %s van %s", failover.Seconds(s.Fresh), list(waiting)))
	} else if len(p.Targets) > 0 {
		add("Verse heartbeats", true, "alle actieve nodes van de geraakte clusters stuurden een verse heartbeat")
	}

	if p.Kind == HostDown && len(p.Targets) > 0 {
		var bad []string
		for _, t := range p.Targets {
			if n, ok := nodes[t.NodeID]; ok && health.ServiceStates(n.Services)["docker"] == "active" {
				docker = append(docker, t.Hostname)
			}
			if s.Proxmox == nil {
				continue
			}
			gs, err := s.Proxmox.GuestStatus(ctx, t.VM.ConnectionID, t.VM.VMID)
			switch {
			case err != nil:
				bad = append(bad, fmt.Sprintf("VM %d van %s is niet te lezen: %v", t.VM.VMID, t.Hostname, err))
			case gs.Status != "running":
				bad = append(bad, fmt.Sprintf("VM %d van %s draait niet (%s)", t.VM.VMID, t.Hostname, or(gs.Status, "onbekend")))
			case gs.HAState != "":
				bad = append(bad, fmt.Sprintf("VM %d van %s staat onder Proxmox HA (%s)", t.VM.VMID, t.Hostname, gs.HAState))
			}
		}
		switch {
		case s.Proxmox == nil:
			add("VM's", false, "ClusterForge heeft geen Proxmox-koppeling; zet CF_MASTER_KEY en koppel Proxmox")
		case len(bad) > 0:
			add("VM's", false, strings.Join(bad, "; "))
		default:
			names := make([]string, len(p.Targets))
			for i, t := range p.Targets {
				names[i] = fmt.Sprintf("%s (%d)", t.Hostname, t.VM.VMID)
			}
			add("VM's", true, list(names)+" draaien en staan niet onder Proxmox HA")
		}
		if len(docker) > 0 {
			add("Geen docker", false, "op "+list(docker)+" draait docker; containers met volumes kunnen data bevatten")
		} else {
			add("Geen docker", true, "op geen van de VM's draait docker")
		}
	}

	// Sloten: elk geraakt cluster, en over heel ClusterForge één test.
	var busy []string
	for _, cp := range p.Clusters {
		if cp.Role != RoleTarget {
			continue
		}
		j, err := s.q.GetClusterSlotJob(ctx, cp.ID)
		switch {
		case errors.Is(err, pgx.ErrNoRows) || err == nil && j.ID == self:
		case err != nil:
			return err
		default:
			busy = append(busy, cp.Name+" ("+j.Title+")")
		}
	}
	if len(busy) > 0 {
		add("Clusterslot", false, "er loopt al een taak in "+list(busy))
	} else if len(p.Targets) > 0 {
		add("Clusterslot", true, "in geen enkel geraakt cluster loopt een andere taak")
	}
	slot, err := s.q.GetTestSlotJob(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || err == nil && slot.ID == self:
		add("Testslot", true, "er loopt geen andere test")
	case err != nil:
		return err
	default:
		add("Testslot", false, "er loopt al een test ("+slot.Title+"); er loopt er hoogstens één tegelijk")
	}

	// Elk VIP dat de meting opvraagt, moet nu antwoorden.
	var probes []string
	var failed []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, cp := range p.Clusters {
		for _, v := range cp.VIPs {
			probes = append(probes, v.Address)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.probeTimes(ctx, v, 3); err != nil {
					mu.Lock()
					failed = append(failed, fmt.Sprintf("%s (%s) antwoordt niet op %s: %v", v.Address, cp.Name, v.Probe, err))
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	slices.Sort(failed)
	switch {
	case len(failed) > 0:
		add("Probe", false, strings.Join(failed, "; ")+"; de server moet elk VIP rechtstreeks kunnen bereiken")
	case len(probes) > 0:
		add("Probe", true, list(probes)+" antwoorden, drie keer na elkaar")
	}
	return nil
}

// probeTimes vraagt een VIP n keer na elkaar op; de eerste fout stopt.
func (s *Service) probeTimes(ctx context.Context, v VIPPlan, n int) error {
	for i := range n {
		if i > 0 {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(s.ProbeInterval):
			}
		}
		if err := s.Prober.Probe(ctx, v.Address, v.Probe); err != nil {
			return err
		}
	}
	return nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// list noemt dingen als "a, b en c".
func list(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " en " + items[len(items)-1]
}
