package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// KindRestore is de taak die een etcd-snapshot terugzet.
const KindRestore = "kube.restore_snapshot"

// RestorePlan is wat terugzetten zou doen.
type RestorePlan struct {
	Snapshot Snapshot
	// Init is de server die het cluster begon: daar zet k3s de snapshot
	// terug. Others zijn de andere servers; die melden zich daarna opnieuw
	// aan. Workers is het aantal andere nodes in Kubernetes.
	Init    string
	Others  []string
	Workers int
}

// restoreParams gaan mee met de taak.
type restoreParams struct {
	Name    string      `json:"name"`
	File    string      `json:"file"`
	TakenAt *time.Time  `json:"taken_at,omitempty"`
	Init    uuid.UUID   `json:"init"`
	Others  []uuid.UUID `json:"others,omitempty"`
}

// RestoreProdText staat bij een prodcluster: daar zet ClusterForge geen
// snapshot terug, en de documentatie heeft het runbook.
const RestoreProdText = "op prod zet ClusterForge geen snapshot terug: het cluster ligt dan een paar minuten stil, " +
	"en alles wat na de snapshot veranderde is weg. Moet het toch, volg dan het runbook bij Snapshots"

// planRestore controleert dat een snapshot terug kan, en met welke
// servers.
func (s *Service) planRestore(ctx context.Context, clusterID uuid.UUID, name string) (RestorePlan, restoreParams, []bridgeNode, error) {
	var plan RestorePlan
	var p restoreParams
	c, err := s.Cluster(ctx, clusterID)
	if err != nil {
		return plan, p, nil, err
	}
	if c.Environment == store.EnvironmentProd {
		return plan, p, nil, &ConflictError{RestoreProdText}
	}
	if err := s.RefreshSnapshots(ctx, clusterID); err != nil {
		return plan, p, nil, err
	}
	items, err := s.q.ListKubeSnapshots(ctx, &clusterID)
	if err != nil {
		return plan, p, nil, err
	}
	i := slices.IndexFunc(items, func(it Snapshot) bool { return it.Name == name })
	if i < 0 {
		return plan, p, nil, &ConflictError{"deze snapshot bestaat niet meer; k3s houdt de laatste vijf per server"}
	}
	sn := items[i]
	switch {
	case !sn.Ready:
		msg := "k3s meldt deze snapshot niet als klaar"
		if sn.Error != "" {
			msg += ": " + sn.Error
		}
		return plan, p, nil, &ConflictError{msg}
	case sn.S3:
		return plan, p, nil, &ConflictError{"deze snapshot staat in S3; ClusterForge zet alleen een snapshot op een server terug"}
	case !protocol.KubeSnapshotFilePattern.MatchString(sn.File) || sn.Location != "file://"+protocol.KubeSnapshotDir+"/"+sn.File:
		return plan, p, nil, &ConflictError{"deze snapshot staat niet in " + protocol.KubeSnapshotDir + "; die kan ClusterForge niet terugzetten"}
	}
	servers, err := s.servers(ctx, clusterID)
	if err != nil {
		return plan, p, nil, err
	}
	var init []bridgeNode
	for _, n := range servers {
		if n.Kube.Init {
			init = append(init, n)
		}
	}
	switch len(init) {
	case 0:
		return plan, p, nil, &ConflictError{"geen agent meldt de server die het cluster begon (cluster-init in /etc/rancher/k3s/config.yaml); " +
			"zet cf-agent op die server, of werk hem bij met --upgrade"}
	case 1:
	default:
		return plan, p, nil, &ConflictError{"meer dan één server meldt dat hij het cluster begon; dat klopt niet, dus ClusterForge zet niets terug"}
	}
	if sn.Node != init[0].Kube.NodeName {
		return plan, p, nil, &ConflictError{fmt.Sprintf("deze snapshot staat op %s. Terugzetten kan alleen met een snapshot op %s (%s), "+
			"de server die het cluster begon; k3s maakt daar op dezelfde momenten een snapshot", sn.Node, init[0].Kube.NodeName, init[0].Hostname)}
	}
	ordered := []bridgeNode{init[0]}
	known := map[string]bool{}
	for _, n := range servers {
		known[n.Kube.NodeName] = true
		if n.ID != init[0].ID {
			ordered = append(ordered, n)
		}
	}
	for _, n := range ordered {
		switch {
		case n.Protocol < protocol.KubeSince:
			return plan, p, nil, &ConflictError{fmt.Sprintf("de agent op %s is te oud voor Kubernetes; werk hem bij met het installatiescript en --upgrade", n.Hostname)}
		case !n.Fresh:
			return plan, p, nil, &ConflictError{fmt.Sprintf("de agent op %s is niet verbonden; terugzetten vraagt elke server", n.Hostname)}
		}
	}
	// Een server zonder agent zou op de oude database blijven draaien.
	nodes, err := s.q.ListKubeNodes(ctx, clusterID)
	if err != nil {
		return plan, p, nil, err
	}
	for _, n := range nodes {
		if slices.Contains(n.Roles, "control-plane") || slices.Contains(n.Roles, "etcd") {
			if !known[n.Name] {
				return plan, p, nil, &ConflictError{fmt.Sprintf("server %s in Kubernetes heeft geen agent van ClusterForge; terugzetten vraagt elke server", n.Name)}
			}
			continue
		}
		plan.Workers++
	}
	plan.Snapshot, plan.Init = sn, init[0].Hostname
	p = restoreParams{Name: sn.Name, File: sn.File, TakenAt: sn.TakenAt, Init: init[0].ID}
	for _, n := range ordered[1:] {
		plan.Others = append(plan.Others, n.Hostname)
		p.Others = append(p.Others, n.ID)
	}
	return plan, p, ordered, nil
}

// PlanRestore zegt wat terugzetten van een snapshot zou doen; er
// verandert niets.
func (s *Service) PlanRestore(ctx context.Context, clusterID uuid.UUID, name string) (RestorePlan, error) {
	plan, _, _, err := s.planRestore(ctx, clusterID, name)
	return plan, err
}

// RequestRestore plant het terugzetten als taak. Eerst vraagt het elke
// server of zijn agent snapshots kent, zodat het niet halverwege stopt met
// een cluster dat stilligt.
func (s *Service) RequestRestore(ctx context.Context, actor events.Actor, clusterID uuid.UUID, name string) (store.Job, error) {
	_, p, nodes, err := s.planRestore(ctx, clusterID, name)
	if err != nil {
		return store.Job{}, err
	}
	for i, n := range nodes {
		if err := s.checkServer(ctx, clusterID, n, i == 0); err != nil {
			return store.Job{}, &ConflictError{err.Error()}
		}
	}
	j, err := s.jobs.EnqueueForCluster(ctx, jobs.Spec{
		Kind: KindRestore, Title: "Snapshot terugzetten: " + p.File, Params: p, ClusterID: &clusterID, NodeID: &p.Init, Actor: actor,
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	return j, err
}

// checkServer vraagt de agent hoe k3s erbij staat. De eerste server moet
// cluster-init hebben, de andere server:.
func (s *Service) checkServer(ctx context.Context, clusterID uuid.UUID, n bridgeNode, init bool) error {
	res, err := s.sendTo(ctx, clusterID, n, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotCheck})
	switch {
	case err != nil:
		return err
	case init && !res.Init:
		return fmt.Errorf("op %s staat cluster-init niet meer in de config van k3s", n.Hostname)
	case !init && (res.Init || !res.Join):
		return fmt.Errorf("%s meldt zich niet aan via server: in de config van k3s, dus ClusterForge weet niet hoe hij opnieuw lid wordt", n.Hostname)
	}
	return nil
}

func (s *Service) runRestore(ctx context.Context, j *jobs.Job) error {
	var p restoreParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	if j.ClusterID == nil {
		return errors.New("taak zonder cluster")
	}
	clusterID := *j.ClusterID
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	servers, err := s.servers(ctx, clusterID)
	if err != nil {
		return err
	}
	byID := func(id uuid.UUID) (bridgeNode, error) {
		i := slices.IndexFunc(servers, func(n bridgeNode) bool { return n.ID == id })
		if i < 0 {
			return bridgeNode{}, fmt.Errorf("server %s heeft geen agent meer met k3s", id)
		}
		return servers[i], nil
	}
	init, err := byID(p.Init)
	if err != nil {
		return err
	}
	others := make([]bridgeNode, 0, len(p.Others))
	for _, id := range p.Others {
		n, err := byID(id)
		if err != nil {
			return err
		}
		others = append(others, n)
	}
	logOutput := func(st *jobs.Step, host string, lines []string) {
		for _, l := range lines {
			st.Logf("%s: %s", host, l)
		}
	}

	if err := j.Step(ctx, "Servers controleren", func(ctx context.Context, st *jobs.Step) error {
		for i, n := range append([]bridgeNode{init}, others...) {
			if err := s.checkServer(ctx, clusterID, n, i == 0); err != nil {
				return err
			}
		}
		st.Logf("k3s kan terug op %s; daarna melden %s zich opnieuw aan", init.Hostname, hostList(others))
		return nil
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "Snapshot van nu maken", func(ctx context.Context, st *jobs.Step) error {
		saved, err := s.save(ctx, clusterID, init, SnapshotBeforeRestore)
		switch {
		case err != nil:
			// Juist als het cluster stuk is, lukt dit vaak niet.
			st.Logf("geen snapshot van nu (%v); terugzetten gaat door", err)
		case saved.Snapshot != nil:
			st.Logf("%s op %s: daarmee kun je terug naar hoe het net was", saved.Snapshot.File, init.Hostname)
		default:
			logOutput(st, init.Hostname, saved.Output)
		}
		return nil
	}); err != nil {
		return err
	}

	payload := map[string]any{"file": p.File, "node": init.Kube.NodeName, "hostname": init.Hostname, "job_id": j.ID, "title": j.Title}
	if p.TakenAt != nil {
		payload["taken_at"] = p.TakenAt.UTC().Format(time.RFC3339)
	}
	if err := j.Step(ctx, "k3s stoppen op de servers", func(ctx context.Context, st *jobs.Step) error {
		// Vanaf hier is het cluster down. Staat het begin in het logboek, dan
		// noemt What Broke het terugzetten als oorzaak van dat incident.
		if err := s.ev.Write(ctx, nil, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "kube.snapshot_restore_started", Payload: payload,
		}); err != nil {
			return err
		}
		for _, n := range append(slices.Clone(others), init) {
			res, err := s.sendTo(ctx, clusterID, n, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStop})
			logOutput(st, n.Hostname, res.Output)
			if err != nil {
				return err
			}
			st.Logf("k3s staat stil op %s", n.Hostname)
			_ = st.Flush(ctx)
		}
		st.Logf("de API antwoordt nu niet, tot k3s weer start op %s; de pods op de nodes draaien door", init.Hostname)
		return nil
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "Terugzetten op "+init.Hostname, func(ctx context.Context, st *jobs.Step) error {
		st.Logf("k3s server --cluster-reset --cluster-reset-restore-path=%s/%s", protocol.KubeSnapshotDir, p.File)
		_ = st.Flush(ctx)
		res, err := s.sendTo(ctx, clusterID, init, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotReset, File: p.File})
		logOutput(st, init.Hostname, res.Output)
		if err != nil {
			return err
		}
		st.Logf("etcd heeft nu één lid, met de toestand van de snapshot; de oude database staat in %s/etcd-old-…", protocol.KubeDBDir)
		return nil
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "k3s starten op "+init.Hostname, func(ctx context.Context, st *jobs.Step) error {
		res, err := s.sendTo(ctx, clusterID, init, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStart})
		logOutput(st, init.Hostname, res.Output)
		if err != nil {
			return err
		}
		if err := s.waitAPI(ctx, init); err != nil {
			return err
		}
		st.Logf("de API op %s antwoordt weer", init.Hostname)
		return nil
	}); err != nil {
		return err
	}

	if err := j.Step(ctx, "Andere servers opnieuw aanmelden", func(ctx context.Context, st *jobs.Step) error {
		if len(others) == 0 {
			st.Logf("dit cluster heeft één server")
			return nil
		}
		// Bij opnieuw proberen slaat het de servers over die al lid zijn.
		var done []uuid.UUID
		st.State(&done)
		for _, n := range others {
			if slices.Contains(done, n.ID) {
				continue
			}
			res, err := s.sendTo(ctx, clusterID, n, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotRejoin})
			logOutput(st, n.Hostname, res.Output)
			if err != nil {
				return err
			}
			res, err = s.sendTo(ctx, clusterID, n, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotStart})
			logOutput(st, n.Hostname, res.Output)
			if err != nil {
				return err
			}
			st.Logf("k3s draait weer op %s", n.Hostname)
			done = append(done, n.ID)
			if err := st.SetState(ctx, done); err != nil {
				return err
			}
		}
		names := make([]string, 0, len(others))
		for _, n := range others {
			names = append(names, n.Kube.NodeName)
		}
		if err := s.waitReady(ctx, clusterID, names); err != nil {
			return err
		}
		st.Logf("%s zijn weer Ready", hostList(others))
		return nil
	}); err != nil {
		return err
	}

	return j.Step(ctx, "Nodes controleren", func(ctx context.Context, st *jobs.Step) error {
		if err := s.waitReady(ctx, clusterID, nil); err != nil {
			return err
		}
		st.Logf("elke node is Ready; de workers meldden zich vanzelf weer")
		if err := s.RefreshSnapshots(ctx, clusterID); err != nil {
			st.Logf("de lijst met snapshots lezen mislukte: %v", err)
		}
		if s.Changed != nil {
			s.Changed()
		}
		return s.ev.Write(ctx, nil, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "kube.snapshot_restored",
			Payload: payload,
		})
	})
}

// waitAPI wacht tot de API op node n antwoordt op /readyz.
func (s *Service) waitAPI(ctx context.Context, n bridgeNode) error {
	deadline := s.Now().Add(s.RestoreWait)
	var last string
	for {
		rctx, cancel := context.WithTimeout(ctx, s.Timeout)
		res, err := s.bus.Kube(rctx, n.ID, protocol.KubeRequest{ID: uuid.NewString(), Op: protocol.KubeRead, Path: "/readyz"})
		cancel()
		switch {
		case err != nil:
			last = err.Error()
		case res.Status == http.StatusOK:
			return nil
		case res.Error != "":
			last = res.Error
		default:
			last = fmt.Sprintf("HTTP %d", res.Status)
		}
		if s.Now().After(deadline) {
			return fmt.Errorf("de API op %s antwoordt na %s nog niet (%s); kijk met journalctl -u k3s op die node", n.Hostname, s.RestoreWait, last)
		}
		if err := sleepCtx(ctx, s.RestorePoll); err != nil {
			return err
		}
	}
}

// waitReady wacht tot de nodes in names Ready zijn, of elke node als names
// leeg is.
func (s *Service) waitReady(ctx context.Context, clusterID uuid.UUID, names []string) error {
	deadline := s.Now().Add(s.RestoreWait)
	for {
		s.forget(clusterID)
		want := names
		var notReady []string
		var err error
		if want == nil {
			var nodes []Node
			if nodes, err = s.Nodes(ctx, clusterID); err == nil {
				for _, n := range nodes {
					if !n.Ready() {
						notReady = append(notReady, n.Metadata.Name)
					}
				}
			}
		} else {
			notReady, err = s.NotReady(ctx, clusterID, want)
		}
		if err == nil && len(notReady) == 0 {
			return nil
		}
		if s.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("de nodes zijn niet te lezen: %w", err)
			}
			return fmt.Errorf("na %s nog niet Ready: %s. Kijk met journalctl -u k3s of journalctl -u k3s-agent op die node, "+
				"en probeer het daarna opnieuw", s.RestoreWait, strings.Join(notReady, ", "))
		}
		if err := sleepCtx(ctx, s.RestorePoll); err != nil {
			return err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func hostList(nodes []bridgeNode) string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Hostname)
	}
	switch len(names) {
	case 0:
		return "geen andere servers"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
}
