package cronjobs

// Bestandssync (fase 6, mijlpaal 6). Een job van de soort sync draait op de
// bron (de node met het VIP of één vaste node) rsync over SSH naar elk doel:
// de andere nodes van het cluster, of de nodes van een ander cluster. De
// server verdeelt de sleutels: de publieke sleutel van elke bron komt in
// authorized_keys op de doelen (Receives bij CronApply), de hostsleutels van
// de doelen in known_hosts op de bron. Zie internal/agent/cronsync.go.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/cronexpr"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Grenzen van een sync.
const (
	maxSyncExclude = 50
	maxSyncBwLimit = 10_000
	// syncKeysMaxAge is hoe lang de sleutels van een node gelden voor de
	// server ze opnieuw vraagt.
	syncKeysMaxAge = time.Hour
)

// SyncInput is de sync van een job zoals een beheerder hem invult.
type SyncInput struct {
	Source string
	// Target leeg is dezelfde map als Source.
	Target string
	// ToCluster nil: de andere nodes van het eigen cluster.
	ToCluster *uuid.UUID
	Delete    bool
	Exclude   []string
	BwLimitMB int
}

// validateSync controleert een sync. Een sync draait als root, op de node met
// het VIP of op één node, nooit op alle nodes tegelijk.
func validateSync(ctx context.Context, q *store.Queries, clusterID uuid.UUID, in *JobInput) error {
	s := in.Sync
	if s == nil {
		return &InvalidError{"sync", "vul de mappen van de sync in"}
	}
	in.Command, in.User = "", "root"
	fix := func(p string) string {
		p = strings.TrimSpace(p)
		if p != "" && !strings.HasSuffix(p, "/") {
			p += "/"
		}
		return p
	}
	s.Source, s.Target = fix(s.Source), fix(s.Target)
	if s.Target == "" {
		s.Target = s.Source
	}
	var ex []string
	for _, e := range s.Exclude {
		if e = strings.TrimSpace(e); e != "" {
			ex = append(ex, e)
		}
	}
	s.Exclude = ex
	switch {
	case !protocol.ValidSyncPath(s.Source):
		return &InvalidError{"sync.source", "een volledige map zoals /var/www/nextcloud/data/, niet / zelf en zonder tekens als * of $"}
	case !protocol.ValidSyncPath(s.Target):
		return &InvalidError{"sync.target", "een volledige map zoals /var/www/nextcloud/data/, niet / zelf en zonder tekens als * of $"}
	case len(s.Exclude) > maxSyncExclude:
		return &InvalidError{"sync.exclude", fmt.Sprintf("hoogstens %d uitzonderingen", maxSyncExclude)}
	case slices.ContainsFunc(s.Exclude, func(e string) bool { return strings.ContainsAny(e, "\n\r") || len(e) > 200 }):
		return &InvalidError{"sync.exclude", "één patroon per regel, zoals *.part"}
	case s.BwLimitMB < 0 || s.BwLimitMB > maxSyncBwLimit:
		return &InvalidError{"sync.bwlimit_mb", "een bandbreedte van 0 (onbeperkt) tot 10000 MB/s"}
	case in.RunOn == protocol.CronOnAll:
		return &InvalidError{"run_on", "een sync draait op de node met het VIP of op één vaste node; op alle nodes tegelijk schrijven ze over elkaar"}
	}
	if s.ToCluster != nil && *s.ToCluster == clusterID {
		s.ToCluster = nil
	}
	nodes, err := q.ListCronCandidateNodes(ctx)
	if err != nil {
		return err
	}
	to := clusterID
	if s.ToCluster != nil {
		to = *s.ToCluster
		if _, err := q.GetCluster(ctx, to); err != nil {
			return &InvalidError{"sync.to_cluster_id", "dat cluster bestaat niet"}
		}
	}
	n := 0
	for _, nd := range nodes {
		if nd.ClusterID != nil && *nd.ClusterID == to {
			n++
		}
	}
	switch {
	case s.ToCluster == nil && n < 2:
		return &InvalidError{"sync.to_cluster_id", "dit cluster heeft maar één node met een agent; kies een ander cluster als doel"}
	case s.ToCluster != nil && n == 0:
		return &InvalidError{"sync.to_cluster_id", "dat cluster heeft geen nodes met een agent"}
	}
	return nil
}

func syncParams(in JobInput) (src, dst string, to *uuid.UUID, del bool, ex []string, bw int32) {
	if in.Sync == nil {
		return "", "", nil, false, []string{}, 0
	}
	ex = in.Sync.Exclude
	if ex == nil {
		ex = []string{}
	}
	return in.Sync.Source, in.Sync.Target, in.Sync.ToCluster, in.Sync.Delete, ex, int32(in.Sync.BwLimitMB)
}

// isSync zegt of een job een sync is.
func isSync(j store.ListCronJobsRow) bool { return j.Kind == protocol.CronKindSync }

// syncTargetCluster is het cluster waar een sync naartoe gaat.
func syncTargetCluster(j store.ListCronJobsRow) uuid.UUID {
	if j.SyncToCluster != nil {
		return *j.SyncToCluster
	}
	return j.ClusterID
}

// syncTargets zijn de doelen van een sync vanaf bron src.
func (w world) syncTargets(j store.ListCronJobsRow, src uuid.UUID) []store.ListCronCandidateNodesRow {
	to := syncTargetCluster(j)
	var out []store.ListCronCandidateNodesRow
	for _, n := range w.nodes {
		if n.ID != src && n.ClusterID != nil && *n.ClusterID == to {
			out = append(out, n)
		}
	}
	return out
}

// syncSpec is de sync van job j zoals bron n hem draait.
func (w world) syncSpec(j store.ListCronJobsRow, n store.ListCronCandidateNodesRow) *protocol.SyncSpec {
	spec := &protocol.SyncSpec{
		Source: j.SyncSource, Target: j.SyncTarget, Delete: j.SyncDelete, Exclude: j.SyncExclude, BwLimitKB: int(j.SyncBwlimitMb) * 1024,
		Targets: []protocol.SyncTarget{},
	}
	for _, t := range w.syncTargets(j, n.ID) {
		if t.PrimaryIp == nil {
			continue
		}
		keys := w.syncNodes[t.ID].HostKeys
		if keys == nil {
			keys = []string{}
		}
		spec.Targets = append(spec.Targets, protocol.SyncTarget{NodeID: t.ID.String(), Hostname: t.Hostname, Address: t.PrimaryIp.String(), HostKeys: keys})
	}
	return spec
}

// receives zijn de syncs die node n ontvangt: van elke bron van een sync
// waarvan n een doel is.
func (w world) receives(n store.ListCronCandidateNodesRow) []protocol.SyncReceive {
	out := []protocol.SyncReceive{}
	if n.ClusterID == nil {
		return out
	}
	var ownVIPs []string
	for _, v := range w.vips[*n.ClusterID] {
		ownVIPs = append(ownVIPs, v.Address.String())
	}
	for _, j := range w.jobs {
		if !isSync(j) || !j.Enabled || syncTargetCluster(j) != *n.ClusterID {
			continue
		}
		for _, src := range w.nodes {
			key := w.syncNodes[src.ID].PublicKey
			if src.ID == n.ID || !onNode(j, src) || src.PrimaryIp == nil || key == "" {
				continue
			}
			addrs := []string{src.PrimaryIp.String()}
			for _, v := range w.vips[j.ClusterID] {
				addrs = append(addrs, v.Address.String())
			}
			out = append(out, protocol.SyncReceive{
				JobID: j.ID.String(), From: src.ID.String(), FromHost: src.Hostname, Addresses: addrs, PublicKey: key,
				Path: j.SyncTarget, Delete: j.SyncDelete, VIPs: ownVIPs,
			})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].JobID+out[a].From < out[b].JobID+out[b].From })
	return out
}

// syncInvolved zijn de nodes die in een sync zitten, als bron of doel.
func (w world) syncInvolved() map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, j := range w.jobs {
		if !isSync(j) {
			continue
		}
		to := syncTargetCluster(j)
		for _, n := range w.nodes {
			if onNode(j, n) || (n.ClusterID != nil && *n.ClusterID == to) {
				out[n.ID] = true
			}
		}
	}
	return out
}

// ensureKeys vraagt de sleutels van elke node in een sync die ze nog niet
// (of lang niet) gaf, en zet ze in w.
func (s *Service) ensureKeys(ctx context.Context, w *world) {
	now := s.Now()
	for _, n := range w.nodes {
		if !w.syncInvolved()[n.ID] {
			continue
		}
		if k, ok := w.syncNodes[n.ID]; ok && k.PublicKey != "" && now.Sub(k.CheckedAt) < syncKeysMaxAge {
			continue
		}
		if n.AgentProtocol < protocol.CronSyncSince || n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatLate {
			continue
		}
		if k, _, err := s.nodeKeys(ctx, n, nil); err == nil {
			w.syncNodes[n.ID] = k
		}
	}
}

// nodeKeys vraagt een node om zijn sleutels en plan, en bewaart ze.
func (s *Service) nodeKeys(ctx context.Context, n store.ListCronCandidateNodesRow, paths []string) (store.CronSyncNode, map[string]int64, error) {
	now := s.Now()
	rctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.Cron(rctx, n.ID, protocol.CronRequest{Op: protocol.CronKeys, Paths: paths})
	switch {
	case err != nil:
		err = &AgentError{agentMessage(n.Hostname, err)}
	case res.Error != "":
		err = &AgentError{n.Hostname + ": " + res.Error}
	case res.Keys == nil:
		err = &AgentError{n.Hostname + ": geen sleutels"}
	}
	if err != nil {
		_ = s.q.MarkCronSyncNode(ctx, store.MarkCronSyncNodeParams{NodeID: n.ID, Error: err.Error(), CheckedAt: now})
		return store.CronSyncNode{}, nil, err
	}
	k := res.Keys
	row := store.CronSyncNode{
		NodeID: n.ID, PublicKey: k.PublicKey, HostKeys: k.HostKeys, Rsync: k.Rsync, Ssh: k.SSH, Sshd: k.SSHD,
		PermitRootLogin: k.PermitRootLogin, CheckedAt: now,
	}
	if row.HostKeys == nil {
		row.HostKeys = []string{}
	}
	if err := s.q.UpsertCronSyncNode(ctx, store.UpsertCronSyncNodeParams{
		NodeID: n.ID, PublicKey: row.PublicKey, HostKeys: row.HostKeys, Rsync: row.Rsync, Ssh: row.Ssh, Sshd: row.Sshd,
		PermitRootLogin: row.PermitRootLogin, CheckedAt: now,
	}); err != nil {
		return row, nil, err
	}
	return row, k.Free, nil
}

// PlanNode is één node in het plan van een sync.
type PlanNode struct {
	NodeID   uuid.UUID
	Hostname string
	// Role is bron of doel.
	Role    string
	Address string
	OK      bool
	// Checks zijn de controles met hun uitkomst.
	Checks []PlanCheck
}

// PlanCheck is één controle van het plan.
type PlanCheck struct {
	Name string
	OK   bool
	Text string
}

// Plan is wat een sync nodig heeft, per node gecontroleerd.
type Plan struct {
	Nodes []PlanNode
	Notes []string
}

// SyncPlan controleert een sync: rsync en ssh op de bronnen, sshd met
// PermitRootLogin op de doelen, en de vrije ruimte.
func (s *Service) SyncPlan(ctx context.Context, id uuid.UUID) (Plan, error) {
	w, err := s.world(ctx)
	if err != nil {
		return Plan{}, err
	}
	i := slices.IndexFunc(w.jobs, func(j store.ListCronJobsRow) bool { return j.ID == id })
	if i < 0 {
		return Plan{}, ErrJobNotFound
	}
	j := w.jobs[i]
	if !isSync(j) {
		return Plan{}, &InvalidError{"kind", "deze job is geen sync"}
	}
	var plan Plan
	if sc, err := cronexpr.Parse(j.Schedule); err == nil {
		now := s.Now().In(s.loc())
		if a := sc.Next(now); !a.IsZero() {
			if b := sc.Next(a); !b.IsZero() && b.Sub(a) < 5*time.Minute {
				plan.Notes = append(plan.Notes, "Deze sync draait vaker dan om de vijf minuten. Moeten de bestanden op de andere nodes binnen "+
					"seconden kloppen, of schrijft de app op meer nodes tegelijk, dan past gedeelde opslag (NFS of CephFS) beter.")
			}
		}
	}
	if j.SyncDelete {
		plan.Notes = append(plan.Notes, "Verwijderen op het doel staat aan: een bestand dat op de bron weg is, gaat ook op het doel weg. "+
			"Doe eerst een proefrun.")
	}
	now := s.Now()
	to := syncTargetCluster(j)
	for _, n := range w.nodes {
		src := onNode(j, n)
		// Bij een sync binnen het cluster vanaf het VIP is elke node bron en
		// doel; vanaf één vaste node is die node alleen bron.
		dst := n.ClusterID != nil && *n.ClusterID == to && (!src || j.RunOn != protocol.CronOnNode)
		if !src && !dst {
			continue
		}
		role := map[bool]string{true: "bron", false: "doel"}[src]
		if src && dst {
			role = "bron en doel"
		}
		pn := PlanNode{NodeID: n.ID, Hostname: n.Hostname, Role: role}
		if n.PrimaryIp != nil {
			pn.Address = n.PrimaryIp.String()
		}
		add := func(name string, ok bool, text string) { pn.Checks = append(pn.Checks, PlanCheck{name, ok, text}) }
		switch {
		case n.AgentProtocol < protocol.CronSyncSince:
			add("agent", false, fmt.Sprintf("agent te oud: protocol %d, een sync vraagt %d", n.AgentProtocol, protocol.CronSyncSince))
		case n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatLate:
			add("agent", false, "de agent is niet verbonden")
		default:
			var paths []string
			if dst {
				paths = []string{j.SyncTarget}
			}
			k, free, err := s.nodeKeys(ctx, n, paths)
			if err != nil {
				add("agent", false, err.Error())
				break
			}
			add("rsync", k.Rsync, map[bool]string{true: "rsync staat erop", false: "rsync ontbreekt: apt install rsync"}[k.Rsync])
			if src {
				add("ssh", k.Ssh, map[bool]string{true: "ssh staat erop", false: "ssh ontbreekt: apt install openssh-client"}[k.Ssh])
			}
			if dst {
				if pn.Address == "" {
					add("adres", false, "de node heeft geen IP-adres in ClusterForge")
				}
				add("sshd", k.Sshd, map[bool]string{true: "sshd draait", false: "sshd draait niet: apt install openssh-server"}[k.Sshd])
				switch k.PermitRootLogin {
				case "prohibit-password", "without-password", "forced-commands-only", "yes":
					add("root", true, "PermitRootLogin "+k.PermitRootLogin)
				default:
					add("root", false, "PermitRootLogin staat op "+k.PermitRootLogin+"; zet hem op prohibit-password in /etc/ssh/sshd_config")
				}
				if len(k.HostKeys) == 0 {
					add("hostsleutel", false, "geen hostsleutel in /etc/ssh gevonden")
				}
				if f, ok := free[j.SyncTarget]; ok && f >= 0 {
					add("ruimte", f > 1<<30, fmt.Sprintf("%s vrij bij %s", humanBytes(f), j.SyncTarget))
				}
			}
		}
		plan.Nodes = append(plan.Nodes, pn)
	}
	for i := range plan.Nodes {
		plan.Nodes[i].OK = !slices.ContainsFunc(plan.Nodes[i].Checks, func(c PlanCheck) bool { return !c.OK })
	}
	return plan, nil
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TiB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%d MiB", b>>20)
	}
}

// ErrNotSync: de job is geen sync.
var ErrNotSync = errors.New("deze job is geen sync")

// DryRun draait een sync als proef naar één doel, vanaf de bron die hem nu
// zou draaien. Het geeft de bron en de regels van rsync.
func (s *Service) DryRun(ctx context.Context, actor events.Actor, id, target uuid.UUID) (string, []string, error) {
	w, err := s.world(ctx)
	if err != nil {
		return "", nil, err
	}
	i := slices.IndexFunc(w.jobs, func(j store.ListCronJobsRow) bool { return j.ID == id })
	if i < 0 {
		return "", nil, ErrJobNotFound
	}
	j := w.jobs[i]
	if !isSync(j) {
		return "", nil, ErrNotSync
	}
	var src *store.ListCronCandidateNodesRow
	for _, n := range w.nodes {
		if !onNode(j, n) {
			continue
		}
		if j.RunOn == protocol.CronOnVIP && !slices.ContainsFunc(w.vips[j.ClusterID], func(v store.ListCronVIPsRow) bool {
			return v.OwnerNodeID != nil && *v.OwnerNodeID == n.ID
		}) {
			continue
		}
		src = &n
		break
	}
	if src == nil {
		return "", nil, &AgentError{"geen node heeft nu het VIP van " + j.ClusterName}
	}
	if src.AgentProtocol < protocol.CronSyncSince {
		return src.Hostname, nil, fmt.Errorf("%w: de agent op %s heeft protocol %d; een sync vraagt protocol %d", ErrAgentTooOld, src.Hostname, src.AgentProtocol, protocol.CronSyncSince)
	}
	if !slices.ContainsFunc(w.syncTargets(j, src.ID), func(n store.ListCronCandidateNodesRow) bool { return n.ID == target }) {
		return src.Hostname, nil, &InvalidError{"target", "die node is geen doel van deze sync"}
	}
	// De bron moet de laatste versie hebben, met de hostsleutel van het doel.
	s.applyClusters(ctx, j.ClusterID, syncTargetCluster(j))
	rctx, cancel := context.WithTimeout(ctx, max(s.Timeout, 2*time.Minute))
	defer cancel()
	res, err := s.bus.Cron(rctx, src.ID, protocol.CronRequest{Op: protocol.CronDryRun, JobID: id.String(), Target: target.String()})
	switch {
	case err != nil:
		return src.Hostname, nil, &AgentError{agentMessage(src.Hostname, err)}
	case res.Error != "":
		return src.Hostname, res.Output, &AgentError{src.Hostname + ": " + res.Error}
	}
	_ = s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "cron_job", SubjectID: id.String(), ClusterID: &j.ClusterID, Action: "cron_job.dry_run",
		Payload: map[string]any{"name": j.Name, "hostname": src.Hostname, "target": target.String(), "lines": len(res.Output)},
	})
	return src.Hostname, res.Output, nil
}
