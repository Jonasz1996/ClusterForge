package docs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Hoeveel er hoogstens in een document komt.
const (
	maxRevisions = 25
	maxFindings  = 40
)

// world is wat één ronde leest, één keer voor alle clusters.
type world struct {
	what     risk.WhatIfWorld
	nodes    []store.ListDocNodesRow
	vips     []store.ListDocVIPsRow
	failover []store.ListDocFailoverTestsRow
	// De DR-repetities per cluster: de laatste, de laatste gemeten en de
	// scenario's.
	drLast, drMeasured map[uuid.UUID]*DRRun
	drScenarios        []store.ListDocRehearsalScenariosRow
	summaries          map[uuid.UUID]*Summary
	backups            map[uuid.UUID]backups.Item
	grades             map[uuid.UUID]risk.ClusterScore
	overview           risk.Overview
	// Wat de sync laatst van de Kubernetes-clusters zag.
	kubeFacts map[uuid.UUID]store.ListKubeFactsRow
	kubeNodes []store.ListKubeNodeFactsRow
	kubeApps  []store.ListKubeAppFactsRow
	// De databases per cluster.
	dbs map[uuid.UUID]*components.ClusterDatabases
	// De beheerde en de gevonden cronjobs.
	cronJobs  []store.ListCronJobsRow
	nodeCrons []store.ListNodeCronsRow
	// Wat Zelfherstel de laatste dertig dagen deed, en Vanzelf herstellen
	// per cluster.
	healEvents []store.ListDocHealEventsRow
	healAuto   map[uuid.UUID]store.HealAuto
}

// Gather leest alles voor de documentatie. Ze leest alleen.
func (s *Service) Gather(ctx context.Context) (Fleet, error) {
	w, rows, err := s.read(ctx)
	if err != nil {
		return Fleet{}, err
	}
	var f Fleet
	for _, r := range rows {
		c, err := s.cluster(ctx, w, r)
		if err != nil {
			return Fleet{}, err
		}
		f.Clusters = append(f.Clusters, c)
	}
	f.Proxmox = proxmoxOf(w.what.Topo)
	f.SPOFs = spofsOf(w.what)
	f.Findings = findingsOf(w.overview)
	f.Links = linksOf(w.what.Graph)
	return f, nil
}

// GatherCluster leest alleen wat de hoofdstukken van één cluster nodig
// hebben.
func (s *Service) GatherCluster(ctx context.Context, id uuid.UUID) (Cluster, error) {
	w, rows, err := s.read(ctx)
	if err != nil {
		return Cluster{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return s.cluster(ctx, w, r)
		}
	}
	return Cluster{}, ErrNotFound
}

func (s *Service) read(ctx context.Context) (*world, []store.ListDocClustersRow, error) {
	rows, err := s.q.ListDocClusters(ctx)
	if err != nil {
		return nil, nil, err
	}
	w := &world{backups: map[uuid.UUID]backups.Item{}, grades: map[uuid.UUID]risk.ClusterScore{}}
	if w.nodes, err = s.q.ListDocNodes(ctx); err != nil {
		return nil, nil, err
	}
	if w.vips, err = s.q.ListDocVIPs(ctx); err != nil {
		return nil, nil, err
	}
	if w.failover, err = s.q.ListDocFailoverTests(ctx); err != nil {
		return nil, nil, err
	}
	if w.drLast, err = rehearsalRuns(s.q.ListDocRehearsals(ctx)); err != nil {
		return nil, nil, err
	}
	measured, err := s.q.ListDocMeasuredRehearsals(ctx)
	if err != nil {
		return nil, nil, err
	}
	same := make([]store.ListDocRehearsalsRow, len(measured))
	for i, r := range measured {
		same[i] = store.ListDocRehearsalsRow(r)
	}
	if w.drMeasured, err = rehearsalRuns(same, nil); err != nil {
		return nil, nil, err
	}
	if w.drScenarios, err = s.q.ListDocRehearsalScenarios(ctx); err != nil {
		return nil, nil, err
	}
	facts, err := s.q.ListKubeFacts(ctx)
	if err != nil {
		return nil, nil, err
	}
	w.kubeFacts = map[uuid.UUID]store.ListKubeFactsRow{}
	for _, f := range facts {
		w.kubeFacts[f.ClusterID] = f
	}
	if w.kubeNodes, err = s.q.ListKubeNodeFacts(ctx); err != nil {
		return nil, nil, err
	}
	if w.kubeApps, err = s.q.ListKubeAppFacts(ctx); err != nil {
		return nil, nil, err
	}
	if s.Components != nil {
		if w.dbs, err = s.Components.DescribeDatabases(ctx); err != nil {
			return nil, nil, err
		}
	}
	if w.cronJobs, err = s.q.ListCronJobs(ctx); err != nil {
		return nil, nil, err
	}
	if w.nodeCrons, err = s.q.ListNodeCrons(ctx); err != nil {
		return nil, nil, err
	}
	if w.healEvents, err = s.q.ListDocHealEvents(ctx, s.Now().Add(-healWindow)); err != nil {
		return nil, nil, err
	}
	autos, err := s.q.ListHealAuto(ctx)
	if err != nil {
		return nil, nil, err
	}
	w.healAuto = map[uuid.UUID]store.HealAuto{}
	for _, a := range autos {
		w.healAuto[a.ClusterID] = a
	}
	sums, err := s.q.ListDocSummaries(ctx)
	if err != nil {
		return nil, nil, err
	}
	w.summaries = map[uuid.UUID]*Summary{}
	for _, r := range sums {
		w.summaries[r.ClusterID] = &Summary{Text: r.Text, Model: r.Model, At: r.CreatedAt, By: r.Username}
	}
	if w.what, err = s.Risk.World(ctx); err != nil {
		return nil, nil, err
	}
	ov, err := s.Backups.Overview(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, it := range ov.Items {
		if it.Node != nil {
			w.backups[it.Node.ID] = it
		}
	}
	if w.overview, err = s.Risk.Overview(ctx); err != nil {
		return nil, nil, err
	}
	for _, c := range w.overview.Clusters {
		w.grades[c.ID] = c
	}
	return w, rows, nil
}

func (s *Service) cluster(ctx context.Context, w *world, r store.ListDocClustersRow) (Cluster, error) {
	c := Cluster{
		ID: r.ID, Name: r.Name, Slug: r.Slug, Env: r.Environment, Type: r.Type, Description: r.Description, Tags: r.Tags,
		Status: r.Status, Revision: int(r.SpecRevision), Applied: int(r.AppliedRevision), GitLinked: r.GitLinked,
	}
	if g, ok := w.grades[r.ID]; ok {
		c.Grade, c.Score = g.Grade, g.Score
	}
	m := w.what.Topo

	var inv []deploy.InventoryNode
	for _, n := range w.nodes {
		if n.ClusterID == r.ID {
			inv = append(inv, deploy.InventoryNode{ID: n.ID, Hostname: n.Hostname, Lifecycle: n.Lifecycle})
		}
	}
	spec, specErr := deploy.ParseSpec(r.Spec, templates.ClusterInfo{Name: r.Name, Slug: r.Slug, Environment: r.Environment}, inv)
	hasSpec := specErr == nil
	var tpl *templates.Template
	if hasSpec {
		t, ok := s.Deploy.Templates.Get(spec.Template.Name, spec.Template.Version)
		c.Template = &Template{Name: spec.Template.Name, Version: spec.Template.Version, Available: ok}
		if ok {
			tpl = t
			c.Template.Title, c.Template.Description, c.Template.Failback = t.Title, t.Description, t.Failback
		}
		if latest, ok := s.Deploy.Templates.Latest(spec.Template.Name); ok {
			c.Template.Latest = latest.Version
		}
		tg := spec.Target
		c.Network = &Network{
			Proxmox: m.ConnName(tg.ProxmoxID), Mode: tg.Network, FirstIP: tg.FirstIP, Gateway: tg.Gateway, DNS: tg.DNS,
			Bridge: or(tg.Bridge, "vmbr0"), VLAN: tg.VLAN, Storage: tg.Storage, ImageVMID: tg.ImageVMID,
		}
	}

	// Nodes.
	guests := map[uuid.UUID]topo.Guest{}
	for _, g := range m.Guests {
		if g.NodeID != nil {
			guests[*g.NodeID] = g
		}
	}
	var keepalived = map[uuid.UUID]*protocol.Keepalived{}
	initHost := ""
	for _, row := range w.nodes {
		if row.ClusterID != r.ID {
			continue
		}
		n := Node{
			ID: row.ID, Hostname: row.Hostname, Role: row.Role, Lifecycle: row.Lifecycle, Description: row.Description,
			Address: row.PrimaryIp, HasAgent: row.HasAgent,
		}
		if hasSpec {
			for _, sn := range spec.Nodes {
				if sn.NodeID == row.ID {
					vm := VMShape{CPU: sn.VM.CPU, MemoryMiB: sn.VM.MemoryMiB, DiskGiB: sn.VM.DiskGiB}
					n.Role, n.Index, n.VM = sn.Role, sn.Index, &vm
				}
			}
		}
		var facts protocol.Facts
		if len(row.Facts) > 0 && json.Unmarshal(row.Facts, &facts) == nil {
			factsOf(&n, facts)
			keepalived[row.ID] = facts.Keepalived
			if facts.Kube != nil && facts.Kube.Init {
				initHost = row.Hostname
			}
		}
		if g, ok := guests[row.ID]; ok {
			n.Guest = guestOf(m, g)
		}
		if it, ok := w.backups[row.ID]; ok {
			b := &NodeBackup{Freshness: string(it.Freshness), Count: it.Count}
			if l := it.Latest; l != nil {
				b.Volid, b.Storage, b.Host, b.Time, b.Size = l.Volid, l.Storage, l.Host, l.Time, l.Size
			}
			n.Backup = b
			if v := it.LastVerification; v != nil {
				n.Verify = &Verification{Result: v.Result, FinishedAt: v.FinishedAt, RecoverySeconds: v.RecoverySeconds}
			}
		}
		c.Nodes = append(c.Nodes, n)
	}
	slices.SortStableFunc(c.Nodes, func(a, b Node) int {
		return cmp.Or(cmp.Compare(a.Role, b.Role), cmp.Compare(a.Index, b.Index), cmp.Compare(a.Hostname, b.Hostname))
	})

	// VIP's, met de nodes op volgorde van prioriteit.
	for _, v := range w.vips {
		if v.ClusterID != r.ID {
			continue
		}
		vip := VIP{Address: v.Address, Interface: v.Interface, Description: v.Description}
		if v.Vrid != nil {
			id := int(*v.Vrid)
			vip.VRID = &id
		}
		vip.Nodes = vipNodes(c.Nodes, keepalived, v.Address)
		c.VIPs = append(c.VIPs, vip)
	}

	c.Services, c.Deps = servicesOf(w.what.Graph, &c)

	// Configuratie.
	if hasSpec {
		h, err := s.Deploy.History(ctx, r.ID)
		if err != nil {
			return c, err
		}
		for _, p := range h.Params {
			c.Params = append(c.Params, Param{Name: p.Name, Label: p.Label, Value: p.Value, Secret: p.Secret})
		}
		c.Notes = h.Notes
		for i, rev := range h.Revisions {
			if i >= maxRevisions {
				c.OlderRevisions = len(h.Revisions) - maxRevisions
				break
			}
			out := Revision{Revision: rev.Revision, Source: rev.Source, At: rev.CreatedAt, Template: rev.Template + " " + rev.TemplateVersion}
			if rev.CommitSha != nil {
				out.Commit = *rev.CommitSha
			}
			if rev.CreatedByName != nil {
				out.By = *rev.CreatedByName
			}
			for _, ch := range rev.Changes {
				out.Changes = append(out.Changes, Change{ch.Label, ch.From, ch.To})
			}
			c.Revisions = append(c.Revisions, out)
		}
		if tpl == nil {
			c.FilesNote = spec.Template.Name + " " + spec.Template.Version + " zit niet in deze server, dus ClusterForge kan de bestanden niet renderen."
		} else {
			c.Files, c.FilesNote = s.files(ctx, r.ID, tpl, spec)
		}
		if name, data, err := s.GitOps.Export(ctx, r.ID); err == nil {
			c.YAML = &File{Name: name, Content: string(data)}
		}
	}

	pol, err := s.Backups.Policy(ctx, r.ID)
	if err != nil {
		return c, err
	}
	c.Backup = BackupPolicy{MaxAgeHours: pol.MaxAgeHours, Default: pol.Default, VerifyEnabled: pol.VerifyEnabled, Window: pol.Window}
	for _, t := range w.failover {
		if t.ClusterID == r.ID {
			c.Failover = append(c.Failover, failoverOf(t))
		}
	}
	c.DR = DR{Default: r.RtoTargetMinutes == nil && r.RpoTargetHours == nil, Last: w.drLast[r.ID], Measured: w.drMeasured[r.ID]}
	c.DR.RTOMinutes, c.DR.RPOHours = backups.Targets(store.Environment(r.Environment), r.RtoTargetMinutes, r.RpoTargetHours)
	for _, sc := range w.drScenarios {
		if sc.ClusterID != r.ID {
			continue
		}
		var p struct {
			Bridge string `json:"bridge"`
		}
		_ = json.Unmarshal(sc.Params, &p)
		c.DR.Scenarios = append(c.DR.Scenarios, DRScenario{Name: sc.Name, Schedule: sc.Schedule, Bridge: or(p.Bridge, backups.DefaultBridge)})
	}
	c.Outages = outagesOf(w.what, &c)
	c.Summary = w.summaries[r.ID]
	if f, ok := w.kubeFacts[r.ID]; ok {
		c.Kube = s.kubeOf(w, f, tpl, initHost)
	}
	if r.HasDatabase {
		c.DB = dbOf(w.dbs[r.ID])
	}
	c.Cron = cronOf(w, r.ID)
	c.Heal = healOf(w, r)
	return c, nil
}

// dbOf zet de databases van een cluster om voor het hoofdstuk Database,
// zonder de database van de failovertest.
func dbOf(cd *components.ClusterDatabases) *ClusterDB {
	out := &ClusterDB{}
	if cd == nil {
		return out
	}
	for _, d := range cd.Components {
		dc := DBComponent{
			Title: d.Title, Engine: d.Engine, Port: d.Port, VIP: d.VIP, OwnVIP: d.OwnVIP, AutoRecover: d.AutoRecover,
			BackupOn: d.Backup.On, BackupAt: d.Backup.At, KeepDays: d.Backup.KeepDays,
		}
		for _, n := range d.Nodes {
			if n.Role == components.RoleArbiter {
				dc.Arbiters = append(dc.Arbiters, n.Hostname)
			} else {
				dc.Data = append(dc.Data, n.Hostname)
			}
		}
		dump := map[string]string{}
		for _, b := range d.Dumps {
			dump[b.Database] = b.Freshness
		}
		if d.Group != nil {
			dc.Version = d.Group.Version
			dc.Databases = dbDatabases(d.Group.Databases, dump)
		}
		out.Components = append(out.Components, dc)
	}
	for _, g := range cd.Unmanaged {
		ds := DBServers{Engine: g.Engine, Kind: g.Kind, Version: g.Version, Databases: dbDatabases(g.Databases, nil)}
		for _, sv := range g.Servers {
			ds.Nodes = append(ds.Nodes, sv.Hostname)
		}
		out.Servers = append(out.Servers, ds)
	}
	return out
}

func dbDatabases(in []databases.Database, dump map[string]string) []DBDatabase {
	var out []DBDatabase
	for _, x := range in {
		if x.Name == protocol.DBProbeDatabase {
			continue
		}
		d := DBDatabase{Name: x.Name, SizeBytes: x.SizeBytes, Tables: x.Tables, Dump: dump[x.Name]}
		for _, u := range x.Users {
			d.Users = append(d.Users, u.Describe())
		}
		slices.Sort(d.Users)
		out = append(out, d)
	}
	return out
}

// factsOf neemt het adres, de netwerkkaarten en het besturingssysteem uit
// de facts.
func factsOf(n *Node, f protocol.Facts) {
	n.HasFacts = true
	if n.Address == "" {
		n.Address = f.PrimaryAddress
	}
	n.OS = f.OS.PrettyName
	n.Gateway, n.DNS = f.DefaultGateway, f.DNS
	for _, i := range f.Interfaces {
		if i.Name == "lo" || i.Kind == "veth" || i.Name == "docker0" || strings.HasPrefix(i.Name, "br-") || strings.HasPrefix(i.Name, "veth") {
			continue
		}
		var addrs []string
		for _, a := range i.Addresses {
			p, err := netip.ParsePrefix(a)
			if err != nil || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			addrs = append(addrs, a)
			if p.Addr().String() == n.Address {
				n.Prefix, n.Interface = p.Bits(), i.Name
			}
		}
		// Zonder adres telt alleen een fysieke kaart of een bond, zoals de
		// poort onder een bridge; ifb en dergelijke van tc vallen weg.
		if len(addrs) == 0 && (i.MAC == "" || strings.HasPrefix(i.Name, "ifb") ||
			(i.Kind != "" && i.Kind != "physical" && i.Kind != "bond")) {
			continue
		}
		n.Interfaces = append(n.Interfaces, Iface{Name: i.Name, MAC: i.MAC, Addresses: addrs})
	}
	slices.SortStableFunc(n.Interfaces, func(a, b Iface) int { return cmp.Compare(a.Name, b.Name) })
}

func guestOf(m topo.Model, g topo.Guest) *Guest {
	out := &Guest{
		Proxmox: m.ConnName(g.Conn), VMID: g.VMID, Type: g.Type, Name: g.Name, Host: g.Host, HA: g.HA(), Pool: g.Pool, MaxMem: g.MaxMem,
	}
	if cfg := g.Config; cfg != nil {
		out.ConfigRead, out.OnBoot = true, cfg.OnBoot
		for _, n := range cfg.NICs {
			out.NICs = append(out.NICs, NIC{Key: n.Key, Bridge: n.Bridge, MAC: n.MAC, Tag: n.Tag})
		}
		for _, d := range cfg.Disks {
			out.Disks = append(out.Disks, Disk{Key: d.Key, Storage: d.Storage, SizeBytes: d.SizeBytes})
		}
	}
	return out
}

// vipNodes zijn de nodes met het VIP in hun keepalived-configuratie, op
// volgorde van prioriteit. Zonder prioriteit (een agent van voor protocol 5)
// telt de index uit de specificatie, dan de naam.
func vipNodes(nodes []Node, ka map[uuid.UUID]*protocol.Keepalived, addr string) []string {
	type cand struct {
		name    string
		prio    int
		index   int
		hasPrio bool
	}
	var cands []cand
	for _, n := range nodes {
		k := ka[n.ID]
		if k == nil || !slices.Contains(k.VIPs, addr) {
			continue
		}
		c := cand{name: n.Hostname, index: n.Index}
		for _, in := range k.Instances {
			if slices.Contains(in.VIPs, addr) && in.Priority > 0 {
				c.prio, c.hasPrio = in.Priority, true
			}
		}
		cands = append(cands, c)
	}
	slices.SortStableFunc(cands, func(a, b cand) int {
		if a.hasPrio && b.hasPrio && a.prio != b.prio {
			return cmp.Compare(b.prio, a.prio)
		}
		return cmp.Or(cmp.Compare(a.index, b.index), cmp.Compare(a.name, b.name))
	})
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.name)
	}
	return out
}

// servicesOf geeft de bevestigde diensten van een cluster en de bevestigde
// pijlen waarvan een kant in het cluster ligt.
func servicesOf(g *deps.Graph, c *Cluster) ([]Svc, []Dep) {
	if g == nil {
		return nil, nil
	}
	nodes := map[uuid.UUID]string{}
	for _, n := range c.Nodes {
		nodes[n.ID] = n.Hostname
	}
	var svcs []Svc
	for _, s := range g.Services {
		if s.State != deps.StateConfirmed {
			continue
		}
		switch {
		case s.ClusterID != nil && *s.ClusterID == c.ID:
			svcs = append(svcs, Svc{ID: s.ID, Name: s.Name, Kind: s.Kind, Unit: s.Unit, Port: s.Port, Source: s.Source})
		case s.NodeID != nil && nodes[*s.NodeID] != "":
			svcs = append(svcs, Svc{ID: s.ID, Name: s.Name, Kind: s.Kind, Unit: s.Unit, Port: s.Port, Source: s.Source, On: nodes[*s.NodeID]})
		}
	}
	slices.SortStableFunc(svcs, func(a, b Svc) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.On, b.On), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	var out []Dep
	for _, d := range g.Deps {
		if d.State != deps.StateConfirmed {
			continue
		}
		from, ok1 := endOf(g, d.From)
		to, ok2 := endOf(g, d.To)
		if !ok1 || !ok2 || (!inCluster(c, from) && !inCluster(c, to)) {
			continue
		}
		out = append(out, Dep{From: from, To: to, Strength: d.Strength, Source: d.Source, Note: d.Note})
	}
	slices.SortStableFunc(out, func(a, b Dep) int {
		return cmp.Or(cmp.Compare(a.From.Group, b.From.Group), cmp.Compare(a.From.Name, b.From.Name),
			cmp.Compare(a.To.Group, b.To.Group), cmp.Compare(a.To.Name, b.To.Name))
	})
	return svcs, out
}

// endOf beschrijft een dienst als kant van een pijl. Een dienst op een node
// hoort bij het cluster van die node.
func endOf(g *deps.Graph, id uuid.UUID) (End, bool) {
	s, ok := g.Service(id)
	if !ok || s.State != deps.StateConfirmed {
		return End{}, false
	}
	e := End{ID: s.ID, Name: s.Name, Kind: s.Kind, External: s.External(), Address: s.Address, Port: s.Port, Group: g.GroupOf(s).Name}
	cid := s.ClusterID
	if cid == nil && s.NodeID != nil {
		if n, ok := g.Node(*s.NodeID); ok {
			cid = n.ClusterID
			if cid != nil {
				if cl, ok := g.Cluster(*cid); ok {
					e.Name += " op " + n.Hostname
					e.Group = cl.Name
				}
			}
		}
	}
	if cid != nil {
		if cl, ok := g.Cluster(*cid); ok {
			e.ClusterID, e.Slug = cid, cl.Slug
		}
	}
	return e, true
}

// files rendert per node wat de template er zet, met plaatshouders voor de
// geheimen. Er wordt niets ontsleuteld en niets verstuurd.
func (s *Service) files(ctx context.Context, clusterID uuid.UUID, tpl *templates.Template, spec deploy.Spec) ([]NodeFiles, string) {
	_, states, err := s.Deploy.Nodes(ctx, clusterID)
	if err != nil {
		return nil, "De bestanden zijn niet te renderen: " + err.Error()
	}
	if _, err := spec.MaskedContext(tpl, states); err != nil {
		return nil, "De bestanden zijn niet te renderen: " + err.Error()
	}
	var out []NodeFiles
	for _, n := range spec.Nodes {
		nf := NodeFiles{Hostname: n.Hostname}
		if n.NodeID == uuid.Nil {
			nf.Error = "deze node bestaat niet meer"
			out = append(out, nf)
			continue
		}
		steps, err := deploy.RenderMasked(tpl, spec, states, n.NodeID)
		if err != nil {
			nf.Error = err.Error()
			out = append(out, nf)
			continue
		}
		for _, st := range steps {
			switch {
			case st.Package != nil:
				for _, p := range st.Package.Names {
					if st.Package.State == "absent" {
						p = "zonder " + p
					}
					nf.Packages = append(nf.Packages, p)
				}
			case st.Service != nil:
				nf.Services = append(nf.Services, serviceStepText(st.Service))
			case st.File != nil:
				f := st.File
				nf.Files = append(nf.Files, File{Path: f.Path, Mode: f.Mode, Owner: f.Owner, Group: f.Group, Content: f.Content})
			}
		}
		out = append(out, nf)
	}
	return out, ""
}

func serviceStepText(s *protocol.ServiceStep) string {
	var parts []string
	if s.Enabled != nil {
		parts = append(parts, map[bool]string{true: "aan bij opstarten", false: "uit bij opstarten"}[*s.Enabled])
	}
	switch s.State {
	case "started":
		parts = append(parts, "gestart")
	case "stopped":
		parts = append(parts, "gestopt")
	}
	if len(parts) == 0 {
		return s.Name
	}
	return s.Name + " (" + strings.Join(parts, ", ") + ")"
}

func failoverOf(t store.ListDocFailoverTestsRow) FailoverTest {
	out := FailoverTest{
		Name: t.Name, Description: failoverDescribe(t.Scenario, t.Service), VIP: t.Vip, MaxTakeoverSeconds: int(t.MaxTakeoverSeconds),
		ExpectFailback: t.ExpectFailback, Scheduled: t.Scheduled,
	}
	if t.RunFinished != nil && t.RunResult != "" {
		r := &TestRun{Result: t.RunResult, Summary: t.RunSummary, At: *t.RunFinished}
		var m struct {
			DowntimeMS   *int64 `json:"downtime_ms"`
			TakeoverNode string `json:"takeover_node"`
			TakeoverMS   *int64 `json:"takeover_ms"`
			FailbackMS   *int64 `json:"failback_ms"`
			ReturnedTo   string `json:"returned_to"`
		}
		if len(t.RunMeasurements) > 0 && json.Unmarshal(t.RunMeasurements, &m) == nil {
			r.DowntimeMS, r.TakeoverNode, r.TakeoverMS, r.FailbackMS, r.ReturnedTo = m.DowntimeMS, m.TakeoverNode, m.TakeoverMS, m.FailbackMS, m.ReturnedTo
		}
		out.Last = r
	}
	return out
}

// rehearsalRuns leest de runs van de DR-repetitie, één per cluster.
func rehearsalRuns(rows []store.ListDocRehearsalsRow, err error) (map[uuid.UUID]*DRRun, error) {
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]*DRRun{}
	for _, r := range rows {
		var d backups.RehearsalDefinition
		var m backups.RehearsalMeasurements
		if err := json.Unmarshal(r.Definition, &d); err != nil {
			return nil, err
		}
		if len(r.Measurements) > 0 {
			if err := json.Unmarshal(r.Measurements, &m); err != nil {
				return nil, err
			}
		}
		run := &DRRun{
			ID: r.ID, Name: d.Name, Result: r.Result, Summary: r.Summary, At: r.FinishedAt,
			Proxmox: d.ConnectionName, Host: or(m.Host, d.Host), Storage: or(m.Storage, d.Storage), Bridge: or(m.Bridge, d.Bridge),
			RTO: secs(m.RTOSeconds), RPO: secs(m.RPOSeconds),
		}
		for _, n := range m.Nodes {
			run.Nodes = append(run.Nodes, DRNode{
				Hostname: n.Hostname, SourceVMID: n.SourceVMID, SandboxVMID: n.SandboxVMID, BackupTime: n.BackupTime,
				Restore: secs(n.RestoreSeconds), Boot: secs(n.BootSeconds),
			})
		}
		for _, v := range m.VIPs {
			run.VIPs = append(run.VIPs, DRVIP{Address: v.Address, Holders: v.Holders, Checked: v.Checked, Probe: v.Probe, ProbeOK: v.ProbeOK})
		}
		out[r.ClusterID] = run
	}
	return out, nil
}

func secs(f *float64) *time.Duration {
	if f == nil {
		return nil
	}
	d := time.Duration(*f * float64(time.Second))
	return &d
}

// failoverDescribe zegt wat een failovertest doet; dezelfde zinnen als
// failover.Describe, zonder dat pakket te laden.
func failoverDescribe(scenario, service string) string {
	switch scenario {
	case "keepalived_stop":
		return "keepalived stoppen op de eigenaar"
	case "service_stop":
		return or(service, "de dienst") + " stoppen op de eigenaar"
	case "vm_hard_stop":
		return "de VM van de eigenaar hard uitzetten"
	case "database_stop":
		if service == "" {
			return "de database stoppen op de eigenaar"
		}
		return "de database (" + service + ") stoppen op de eigenaar"
	case "restart_all":
		if service == "" {
			return "alle nodes van de database herstarten"
		}
		return "alle nodes van de database (" + service + ") herstarten"
	}
	return scenario
}

// outagesOf rekent de wat-als voor elke node van het cluster en elke host
// waarop er een draait, alsof de rest gezond is.
func outagesOf(w risk.WhatIfWorld, c *Cluster) []Outage {
	if w.Graph == nil {
		return nil
	}
	mine := map[uuid.UUID]bool{}
	keys := map[string]bool{}
	own := map[string]bool{c.Name: true}
	for _, n := range c.Nodes {
		mine[n.ID] = true
		keys[topo.NodeKey(n.ID)] = true
		own[n.Hostname] = true
	}
	for _, g := range w.Topo.Guests {
		if g.NodeID != nil && mine[*g.NodeID] {
			keys[topo.HostKey(g.Conn, g.Host)] = true
		}
	}
	var out []Outage
	for _, comp := range w.Components() {
		if !keys[comp.Key] {
			continue
		}
		r := w.Run([]risk.Component{comp}, true)
		u := Outage{Kind: comp.Kind, Name: comp.Name, Status: "healthy", Self: r.Self != nil && r.Self.Outcome == "down"}
		if comp.Kind == risk.KindHost {
			u.Name = comp.Name + " (" + comp.Group + ")"
		}
		for _, n := range r.Nodes {
			if !mine[n.ID] {
				continue
			}
			if n.Outcome == "restarted" {
				u.Restarted = append(u.Restarted, n.Hostname)
			} else {
				u.Nodes = append(u.Nodes, n.Hostname)
			}
		}
		for _, co := range r.Clusters {
			final := co.Final()
			switch {
			case co.ID == c.ID:
				u.Status, u.Reason = final.Status, final.Reason
				if co.AfterHA != nil {
					u.During = co.During.Status
				}
			case final.Status != co.Before.Status && (final.Status == "down" || final.Status == "degraded"):
				u.Others = append(u.Others, co.Name+": "+statusOf(final.Status))
			}
		}
		for _, sv := range r.Services {
			if sv.Impact == "none" || sv.Recovers || own[sv.Group] {
				continue
			}
			u.Others = append(u.Others, sv.Name+" in "+sv.Group+": "+statusOf(sv.Impact))
		}
		out = append(out, u)
	}
	return out
}

// proxmoxOf geeft per koppeling de hosts met hun storage en bridges.
func proxmoxOf(m topo.Model) []Proxmox {
	var out []Proxmox
	for _, conn := range m.Connections {
		p := Proxmox{Name: conn.Name}
		for _, h := range m.Hosts {
			if h.Conn != conn.ID {
				continue
			}
			host := Host{Name: h.Name, Online: h.Online, MaxMem: h.MaxMem}
			for _, st := range m.Storages {
				if st.Conn == conn.ID && st.Host == h.Name {
					host.Storages = append(host.Storages, Storage{Name: st.Name, Type: st.Type, Shared: st.Shared, Active: st.Active})
				}
			}
			slices.SortStableFunc(host.Storages, func(a, b Storage) int { return cmp.Compare(a.Name, b.Name) })
			for _, b := range m.Bridges() {
				if b.Conn != conn.ID || b.Host != h.Name {
					continue
				}
				br := Bridge{Name: b.Name}
				for _, i := range m.HostIfaces {
					if i.Conn == conn.ID && i.Host == h.Name && i.Iface == b.Name {
						br.Ports, br.CIDR, br.VLANAware = i.Ports, i.CIDR, i.VLANAware
					}
				}
				host.Bridges = append(host.Bridges, br)
			}
			for _, g := range m.Guests {
				if g.Conn == conn.ID && g.Host == h.Name && !g.Template {
					host.Guests++
				}
			}
			p.Hosts = append(p.Hosts, host)
		}
		slices.SortStableFunc(p.Hosts, func(a, b Host) int { return cmp.Compare(a.Name, b.Name) })
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b Proxmox) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func spofsOf(w risk.WhatIfWorld) []SPOF {
	var out []SPOF
	for _, s := range w.SPOFs() {
		sp := SPOF{Kind: s.Component.Kind, Name: s.Component.Name, Group: s.Component.Group, Self: s.Self}
		for _, c := range s.Clusters {
			sp.Down = append(sp.Down, c.Name+": "+c.Reason)
		}
		for _, sv := range s.Services {
			sp.Down = append(sp.Down, sv.Name+" in "+sv.Group+": "+sv.Reason)
		}
		out = append(out, sp)
	}
	return out
}

func findingsOf(ov risk.Overview) []Finding {
	var out []Finding
	for _, f := range ov.Findings {
		if f.Accepted != nil || len(out) >= maxFindings {
			continue
		}
		where := ""
		if f.Cluster != nil {
			where = f.Cluster.Name
		}
		text := f.Text
		if f.Check == "platform.admin_totp" {
			// Viewers lezen de documentatie; welke beheerder geen
			// tweestapsverificatie heeft, is een doelwit.
			text = "Een beheerder heeft geen tweestapsverificatie"
		}
		out = append(out, Finding{Where: where, Text: text, Points: f.Points})
	}
	return out
}

// linksOf telt de bevestigde pijlen tussen clusters en naar externe
// diensten samen per paar.
func linksOf(g *deps.Graph) []Link {
	if g == nil {
		return nil
	}
	byKey := map[string]*Link{}
	var keys []string
	for _, d := range g.Deps {
		if d.State != deps.StateConfirmed {
			continue
		}
		from, ok1 := endOf(g, d.From)
		to, ok2 := endOf(g, d.To)
		if !ok1 || !ok2 || from.ClusterID == nil || (to.ClusterID == nil && !to.External) {
			continue
		}
		if to.ClusterID != nil && *to.ClusterID == *from.ClusterID {
			continue
		}
		fe := linkEndOf(g, from)
		te := linkEndOf(g, to)
		key := fe.Slug + "\x00" + te.Slug + "\x00" + te.Name
		l := byKey[key]
		if l == nil {
			l = &Link{From: fe, To: te}
			byKey[key] = l
			keys = append(keys, key)
		}
		l.Count++
		l.Hard = l.Hard || d.Strength != deps.Soft
		l.Services = append(l.Services, from.Name+" → "+to.Name)
	}
	out := make([]Link, 0, len(keys))
	for _, k := range keys {
		l := byKey[k]
		slices.Sort(l.Services)
		out = append(out, *l)
	}
	slices.SortStableFunc(out, func(a, b Link) int {
		return cmp.Or(cmp.Compare(a.From.Name, b.From.Name), cmp.Compare(a.To.Name, b.To.Name))
	})
	return out
}

func linkEndOf(g *deps.Graph, e End) LinkEnd {
	if e.External {
		name := e.Name
		if e.Address != "" {
			name = hostPort(e.Address, e.Port)
			if e.Name != name {
				name = e.Name + " (" + name + ")"
			}
		}
		return LinkEnd{Name: name, External: true}
	}
	cl, _ := g.Cluster(*e.ClusterID)
	return LinkEnd{Name: cl.Name, Slug: cl.Slug, Env: cl.Environment}
}

// ErrNotFound betekent dat het cluster of document niet bestaat.
var ErrNotFound = errors.New("niet gevonden")

// kubeOf zet wat de sync van een Kubernetes-cluster zag om voor de
// documentatie. Wat elke minuut verandert, zoals Ready of het aantal
// herstarts, staat er niet in: de documentatie beschrijft het cluster, niet
// zijn toestand van nu.
func (s *Service) kubeOf(w *world, f store.ListKubeFactsRow, tpl *templates.Template, initHost string) *Kube {
	k := &Kube{Synced: f.Synced, Version: f.Version, Init: initHost, Snapshots: int(f.Snapshots)}
	if tpl != nil {
		k.TemplateK3s = tpl.KubeVersion()
		if next := s.Deploy.Templates.KubeNext(tpl.Name, tpl.Version); next != nil {
			k.NextVersion, k.NextK3s = next.Version, next.KubeVersion()
		}
	}
	var newest []kube.Snapshot
	if f.NewestSnapshot.After(time.Unix(0, 0)) {
		t := f.NewestSnapshot
		newest = []kube.Snapshot{{Ready: true, TakenAt: &t}}
	}
	readErr := f.SnapshotsError
	if f.SnapshotsAt == nil && readErr == "" {
		readErr = "nog niet gelezen"
	}
	k.Fresh = kube.Fresh(newest, readErr, s.Now())
	if len(f.Inventory) > 0 {
		var inv kube.Inventory
		if json.Unmarshal(f.Inventory, &inv) == nil {
			k.Inventory = &inv
		}
	}
	for _, n := range w.kubeNodes {
		if n.ClusterID == f.ClusterID {
			k.Nodes = append(k.Nodes, KubeNode{
				Name: n.Name, Hostname: n.Hostname, Server: slices.Contains(n.Roles, "control-plane"), Kubelet: n.Kubelet, IP: n.InternalIp,
				CPUMillis: n.CpuMillis, MemoryBytes: n.MemoryBytes,
			})
		}
	}
	slices.SortStableFunc(k.Nodes, func(a, b KubeNode) int {
		if a.Server != b.Server {
			if a.Server {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	for _, a := range w.kubeApps {
		if a.ClusterID == f.ClusterID {
			k.Apps = append(k.Apps, KubeApp{
				Namespace: a.Namespace, Kind: a.Kind, Name: a.Name, System: a.System, Desired: int(a.Desired), Images: a.Images,
			})
		}
	}
	return k
}
