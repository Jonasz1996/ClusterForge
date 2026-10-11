package risk

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// epoch is de waarde die de queries geven voor 'geen tijd'.
var epoch = time.Unix(0, 0)

func notEpoch(t time.Time) *time.Time {
	if !t.After(epoch) {
		return nil
	}
	return &t
}

// world is alle invoer van één berekening.
type world struct {
	clusters []ClusterInput
	platform PlatformInput
	acc      map[Key]Acceptance
}

// gather leest alles wat de score nodig heeft. Een fout in de metrics of de
// back-ups maakt die kaarten onbekend; andere fouten stoppen de berekening.
func (s *Service) gather(ctx context.Context, now time.Time) (world, error) {
	var w world
	q := s.q
	clusters, err := q.ListRiskClusters(ctx)
	if err != nil {
		return w, err
	}
	nodes, err := q.ListRiskNodes(ctx)
	if err != nil {
		return w, err
	}
	updates, err := q.ListUpdateChanges(ctx, now.Add(-180*day))
	if err != nil {
		return w, err
	}
	history, err := q.ListClusterStatusHistory(ctx, now.Add(-Window))
	if err != nil {
		return w, err
	}
	incidents, err := q.CountClusterIncidents(ctx, now.Add(-Window))
	if err != nil {
		return w, err
	}
	moves, err := q.CountVIPMoves(ctx, now.Add(-VIPWindow))
	if err != nil {
		return w, err
	}
	vips, err := q.ListAllVIPs(ctx)
	if err != nil {
		return w, err
	}
	tests, err := q.ListRiskFailoverTests(ctx)
	if err != nil {
		return w, err
	}
	unrestored, err := q.ListRiskUnrestoredRuns(ctx)
	if err != nil {
		return w, err
	}
	rehearsals, err := q.ListRiskRehearsals(ctx)
	if err != nil {
		return w, err
	}
	deps, err := q.ListNonProdDependencies(ctx)
	if err != nil {
		return w, err
	}
	drift, err := q.ListRiskDrift(ctx)
	if err != nil {
		return w, err
	}
	ignores, err := q.ListWildcardIgnores(ctx, &now)
	if err != nil {
		return w, err
	}
	git, err := q.ListRiskGitChanges(ctx)
	if err != nil {
		return w, err
	}
	admins, err := q.ListAdminsWithoutTOTP(ctx)
	if err != nil {
		return w, err
	}
	pve, err := q.ListRiskProxmox(ctx)
	if err != nil {
		return w, err
	}
	if w.acc, err = s.acceptances(ctx, now); err != nil {
		return w, err
	}
	nodeMetrics, metricsRead := s.nodeMetrics(ctx, now)
	backupsOf, verified := s.backupInputs(ctx)

	since := securitySince(updates)
	perCluster := map[uuid.UUID][]NodeInput{}
	for _, n := range nodes {
		in := NodeInput{
			ID: n.ID, Hostname: n.Hostname, Lifecycle: n.Lifecycle, LifecycleSince: notEpoch(n.LifecycleSince), Host: n.Host,
			ProtocolVersion: int(n.ProtocolVersion),
		}
		var f protocol.Facts
		if len(n.Facts) > 0 && json.Unmarshal(n.Facts, &f) == nil {
			factsInput(&in, f)
			if in.SecurityUpdates > 0 {
				in.SecuritySince = since[n.ID]
				if _, seen := since[n.ID]; !seen && n.AgentEnrolledAt != nil {
					// Het journaal zag nooit een verandering: ze staan open
					// sinds de agent er is, of langer.
					in.SecuritySince = n.AgentEnrolledAt
				}
			}
		}
		if m := nodeMetrics[n.ID]; m != nil {
			in.Metrics = &NodeMetrics{CPUP95: m.CPUP95, MemoryP95: m.MemoryP95, SwapAvg: m.SwapAvg, NetErrors: m.NetErrors, DiskDaysLeft: m.DiskDaysLeft}
		}
		perCluster[n.ClusterID] = append(perCluster[n.ClusterID], in)
	}
	hist := map[uuid.UUID][]Transition{}
	for _, h := range history {
		hist[h.ClusterID] = append(hist[h.ClusterID], Transition{At: h.Ts, To: h.Status})
	}
	incidentCount := map[uuid.UUID]int{}
	for _, i := range incidents {
		incidentCount[i.ClusterID] = int(i.Incidents)
	}
	moveCount := map[uuid.UUID]int{}
	for _, m := range moves {
		moveCount[m.VipID] = int(m.Moves)
	}

	for _, c := range clusters {
		in := ClusterInput{
			ID: c.ID, Name: c.Name, Env: c.Environment, Status: c.Status, StatusReason: c.StatusReason,
			Nodes: perCluster[c.ID], History: hist[c.ID], Since: c.CreatedAt, Incidents: incidentCount[c.ID],
			VIPMoves: map[uuid.UUID]int{}, Backups: backupsOf[c.ID], LastVerified: verified[c.ID],
			DesiredState: c.DesiredState, SpecRevision: int(c.SpecRevision), AppliedRevision: int(c.AppliedRevision),
			RevisionSince: notEpoch(c.RevisionSince), RevisionRunning: c.Busy, MetricsRead: metricsRead,
		}
		for _, v := range vips {
			if v.ClusterID != c.ID {
				continue
			}
			addr := v.Address.String()
			vi := VIPInput{ID: v.ID, Address: addr, Candidates: candidates(nodes, c.ID, addr)}
			in.VIPs = append(in.VIPs, vi)
			in.VIPMoves[v.ID] = moveCount[v.ID]
		}
		for _, t := range tests {
			if t.ClusterID != c.ID {
				continue
			}
			ft := FailoverInput{ID: t.ID, Name: t.Name, VIPID: t.VipID}
			if t.RunResult != nil && t.RunFinished != nil {
				ft.LastRun = &RunInput{ID: t.RunID, Name: t.Name, Result: *t.RunResult, Summary: t.RunSummary, Finished: *t.RunFinished}
			}
			in.FailoverTests = append(in.FailoverTests, ft)
		}
		for _, r := range unrestored {
			if r.ClusterID == c.ID {
				in.Unrestored = append(in.Unrestored, RunInput{ID: r.ID, Kind: r.Kind, Name: r.Name, Finished: r.Finished})
			}
		}
		for _, t := range rehearsals {
			if t.ClusterID != c.ID {
				continue
			}
			ri := RehearsalInput{ID: t.ID, Name: t.Name}
			if t.RunResult != nil && t.RunFinished != nil {
				ri.LastRun = &RunInput{ID: t.RunID, Kind: "dr.rehearsal", Name: t.Name, Result: *t.RunResult, Summary: t.RunSummary, Finished: *t.RunFinished}
			}
			in.Rehearsals = append(in.Rehearsals, ri)
		}
		if c.Environment == "prod" {
			for _, d := range deps {
				if d.FromClusterID == c.ID {
					in.NonProdDeps = append(in.NonProdDeps, DepInput{ID: d.ID, From: d.FromName, To: d.ToName, ToCluster: d.ToCluster, ToEnv: d.ToEnv})
				}
			}
		}
		for _, d := range drift {
			if d.ClusterID == c.ID {
				in.Drift = append(in.Drift, DriftInput{NodeID: d.NodeID, Hostname: d.Hostname, Findings: int(d.Findings), Since: d.DriftSince})
			}
		}
		for _, ig := range ignores {
			if ig.ClusterID == c.ID {
				in.WildcardIgnores = append(in.WildcardIgnores, IgnoreInput{ID: ig.ID, Node: ig.Hostname, ExpiresAt: ig.ExpiresAt})
			}
		}
		for _, g := range git {
			if g.ClusterID == c.ID && g.Status == "failed" {
				in.FailedGit = &GitInput{ID: g.ID, Commit: g.CommitSha, Reason: g.Reason, At: g.UpdatedAt}
			}
		}
		w.clusters = append(w.clusters, in)
	}

	if s.Topo != nil && s.Deps != nil {
		if err := s.gatherTopo(ctx, &w, nodes, vips); err != nil {
			if ctx.Err() != nil {
				return w, err
			}
			s.log.Warn("SPOF's rekenen voor de score mislukt", "err", err)
		}
	}
	for _, a := range admins {
		w.platform.AdminsWithoutTOTP = append(w.platform.AdminsWithoutTOTP, UserInput{ID: a.ID, Username: a.Username})
	}
	for _, p := range pve {
		w.platform.Proxmox = append(w.platform.Proxmox, ProxmoxInput{
			ID: p.ID, Name: p.Name, LastError: p.LastError, LastSyncAt: p.LastSyncAt, CreatedAt: p.CreatedAt, CertNotAfter: p.CertNotAfter,
		})
	}
	return w, nil
}

// World leest wat de wat-als nodig heeft.
func (s *Service) World(ctx context.Context) (WhatIfWorld, error) {
	if s.Topo == nil || s.Deps == nil {
		return WhatIfWorld{}, errors.New("de wat-als is niet ingesteld")
	}
	nodes, err := s.q.ListRiskNodes(ctx)
	if err != nil {
		return WhatIfWorld{}, err
	}
	vips, err := s.q.ListAllVIPs(ctx)
	if err != nil {
		return WhatIfWorld{}, err
	}
	return s.world(ctx, nodes, vips)
}

func (s *Service) world(ctx context.Context, nodes []store.ListRiskNodesRow, vips []store.ListAllVIPsRow) (WhatIfWorld, error) {
	m, err := s.Topo.Load(ctx)
	if err != nil {
		return WhatIfWorld{}, err
	}
	g, err := s.Deps.Load(ctx)
	if err != nil {
		return WhatIfWorld{}, err
	}
	cands := map[uuid.UUID]map[string][]uuid.UUID{}
	for _, v := range vips {
		addr := v.Address.String()
		if c := candidates(nodes, v.ClusterID, addr); len(c) > 0 {
			if cands[v.ClusterID] == nil {
				cands[v.ClusterID] = map[string][]uuid.UUID{}
			}
			cands[v.ClusterID][addr] = c
		}
	}
	return WhatIfWorld{Topo: m, Graph: g, Candidates: cands}, nil
}

// gatherTopo zet de SPOF's bij de clusters en de VM van ClusterForge zelf
// bij de kaart Platform.
func (s *Service) gatherTopo(ctx context.Context, w *world, nodes []store.ListRiskNodesRow, vips []store.ListAllVIPsRow) error {
	ww, err := s.world(ctx, nodes, vips)
	if err != nil {
		return err
	}
	byCluster := SPOFsByCluster(ww.SPOFs())
	for i := range w.clusters {
		w.clusters[i].SPOFs = byCluster[w.clusters[i].ID]
	}
	m := ww.Topo
	if len(m.Guests) == 0 {
		return nil
	}
	self := &SelfInput{Found: m.Self.Found()}
	w.platform.Self = self
	if !self.Found {
		return nil
	}
	self.ID = uuid.NewSHA1(spofNamespace, []byte("self:"+m.Self.Conn.String()+":"+strconv.Itoa(m.Self.VMID)))
	self.Name = "VM " + strconv.Itoa(m.Self.VMID)
	if g, ok := m.Guest(m.Self.Conn, m.Self.VMID); ok {
		self.Name = g.Label()
	}
	st, err := s.q.GetGuestBackupState(ctx, store.GetGuestBackupStateParams{ConnectionID: m.Self.Conn, Vmid: int32(m.Self.VMID)})
	if err != nil {
		return err
	}
	self.BackupsRead = st.BackupCheckedAt != nil && st.BackupError == ""
	self.Latest = notEpoch(st.Latest)
	return nil
}

// factsInput neemt over wat de score uit de facts nodig heeft.
func factsInput(in *NodeInput, f protocol.Facts) {
	in.HasFacts = true
	in.OSID, in.OSVersion, in.OSName = f.OS.ID, f.OS.VersionID, f.OS.PrettyName
	if f.Upgrades != nil {
		in.Updates, in.SecurityUpdates = f.Upgrades.Total, f.Upgrades.Security
	}
	in.AptUpdatedAt = f.AptUpdatedAt
	in.RebootRequired, in.Kernel, in.KernelInstalled = f.RebootRequired, f.Kernel, f.KernelInstalled
	for _, c := range f.Certificates {
		name := c.Subject
		if len(c.DNSNames) > 0 {
			name = c.DNSNames[0]
		}
		in.Certificates = append(in.Certificates, CertInput{Path: c.Path, Name: name, NotAfter: c.NotAfter})
	}
	for _, fs := range f.Filesystems {
		in.Filesystems = append(in.Filesystems, FSInput{Mount: fs.Mount, Size: fs.SizeBytes, Used: fs.UsedBytes})
	}
}

// securitySince geeft per node sinds wanneer er volgens het journaal
// onafgebroken beveiligingsupdates open staan. Een node zonder regels in het
// journaal ontbreekt; een node waar het nu nul is, heeft nil.
func securitySince(rows []store.ListUpdateChangesRow) map[uuid.UUID]*time.Time {
	out := map[uuid.UUID]*time.Time{}
	done := map[uuid.UUID]bool{}
	// Nieuwste eerst per node: loop terug zolang er beveiligingsupdates
	// open stonden.
	for _, r := range rows {
		if done[r.NodeID] {
			continue
		}
		n, err := strconv.Atoi(r.Security)
		if err != nil || n == 0 {
			done[r.NodeID] = true
			if _, ok := out[r.NodeID]; !ok {
				out[r.NodeID] = nil
			}
			continue
		}
		ts := r.Ts
		out[r.NodeID] = &ts
	}
	return out
}

// candidates zijn de nodes van het cluster die het VIP in hun
// keepalived-configuratie hebben.
func candidates(nodes []store.ListRiskNodesRow, cluster uuid.UUID, addr string) []uuid.UUID {
	var out []uuid.UUID
	for _, n := range nodes {
		if n.ClusterID != cluster || len(n.Facts) == 0 {
			continue
		}
		var f struct {
			Keepalived *protocol.Keepalived `json:"keepalived"`
		}
		if json.Unmarshal(n.Facts, &f) != nil || f.Keepalived == nil {
			continue
		}
		for _, v := range f.Keepalived.VIPs {
			if v == addr {
				out = append(out, n.ID)
				break
			}
		}
	}
	return out
}

// nodeMetrics leest de risicoqueries, hoogstens elke metricsTTL. Zonder
// VictoriaMetrics of bij een fout is read false.
func (s *Service) nodeMetrics(ctx context.Context, now time.Time) (map[uuid.UUID]*metrics.NodeRisk, bool) {
	if s.metrics == nil || !s.metrics.Enabled() {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metricsAt.IsZero() || now.Sub(s.metricsAt) >= metricsTTL {
		m, err := s.metrics.Risk(ctx, now)
		if err != nil {
			if s.metricsErr == nil {
				s.log.Warn("risicoqueries in VictoriaMetrics mislukt", "err", err)
			}
			s.metricsCache, s.metricsErr = nil, err
		} else {
			s.metricsCache, s.metricsErr = m, nil
		}
		s.metricsAt = now
	}
	return s.metricsCache, s.metricsErr == nil
}

// backupInputs geeft per cluster de back-upstand van zijn nodes, en de
// laatste geslaagde back-upcontrole.
func (s *Service) backupInputs(ctx context.Context) (map[uuid.UUID][]BackupInput, map[uuid.UUID]*time.Time) {
	out, verified := map[uuid.UUID][]BackupInput{}, map[uuid.UUID]*time.Time{}
	if s.backups == nil {
		return out, verified
	}
	ov, err := s.backups.Overview(ctx)
	if err != nil {
		s.log.Warn("back-ups lezen voor de score mislukt", "err", err)
		return out, verified
	}
	for _, it := range ov.Items {
		if it.Node == nil || it.Cluster == nil {
			continue
		}
		b := BackupInput{NodeID: it.Node.ID, Hostname: it.Node.Name, Freshness: string(it.Freshness), MaxAgeHours: it.MaxAgeHours}
		if it.Latest != nil {
			t := it.Latest.Time
			b.Latest = &t
			b.PBSFailed = it.Latest.Verify == "failed"
		}
		if v := it.LastVerification; v != nil {
			b.Verification, b.VerificationAt, b.VerificationID = v.Result, v.FinishedAt, v.RunID
			if (v.Result == "pass" || v.Result == "warning") && v.FinishedAt != nil {
				if cur := verified[it.Cluster.ID]; cur == nil || v.FinishedAt.After(*cur) {
					verified[it.Cluster.ID] = v.FinishedAt
				}
			}
		}
		out[it.Cluster.ID] = append(out[it.Cluster.ID], b)
	}
	return out, verified
}
