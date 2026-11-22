package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De failover van de master (fase 15): voor de VIP-houder bijgewerkt
// wordt, gaan zijn VIP's naar een node die in deze taak al bijgewerkt is.
// Daarvoor gaat keepalived op de master uit, zoals bij Onderhoud. Na de
// update gaat keepalived weer aan.

// failState is de state van de failoverstap.
type failState struct {
	// Entered: keepalived is uitgezet; de terugstap zet het weer aan.
	Entered  bool      `json:"entered"`
	VIPs     []string  `json:"vips,omitempty"`
	Previous string    `json:"previous,omitempty"`
	At       time.Time `json:"at"`
}

func failoverName(name string) string { return name + ": failover, VIP's naar een bijgewerkte node" }

func (s *Service) actor(j *jobs.Job) events.Actor {
	if j.RequestedBy != nil {
		return events.User(*j.RequestedBy)
	}
	return events.System()
}

// failoverStep verhuist de VIP's van n naar een node uit done.
func (s *Service) failoverStep(ctx context.Context, j *jobs.Job, name string, n jobNode, done []jobNode, out *failState) error {
	j.Done(failoverName(name), out)
	return j.Step(ctx, failoverName(name), func(ctx context.Context, st *jobs.Step) error {
		rt, err := s.q.GetNodeRuntime(ctx, n.ID)
		if err != nil {
			return err
		}
		if rt.ClusterID == nil {
			st.Logf("%s hoort niet bij een cluster; geen failover", n.Hostname)
			return st.SetState(ctx, failState{At: s.Now()})
		}
		vips, err := s.heldVIPs(ctx, *rt.ClusterID, n.ID)
		if err != nil {
			return err
		}
		if len(vips) == 0 {
			st.Logf("%s heeft nu geen VIP; geen failover nodig", n.Hostname)
			return st.SetState(ctx, failState{At: s.Now()})
		}
		updated := make([]uuid.UUID, 0, len(done))
		for _, d := range done {
			updated = append(updated, d.ID)
		}
		if err := s.checkTakeover(ctx, *rt.ClusterID, n.ID, updated); err != nil {
			return err
		}
		// Bij PostgreSQL eerst overschakelen als de master de primary is.
		if s.DB != nil {
			dbs, _, err := s.DB.NodeDB(ctx, n.ID, "maintenance")
			if err != nil {
				return err
			}
			for _, db := range dbs {
				if db.Engine != protocol.DBPostgreSQL {
					continue
				}
				st.Logf("%s: overschakelen als %s de primary is", db.Title, n.Hostname)
				_ = st.Flush(ctx)
				if err := s.DB.LeaveDB(ctx, st, s.actor(j), j, db.ID, n.ID, false); err != nil {
					return fmt.Errorf("%s: overschakelen: %w", db.Title, err)
				}
			}
		}
		prev, err := s.setLifecycle(ctx, j, n.ID, store.NodeLifecycleMaintenance)
		if err != nil {
			return err
		}
		fs := failState{Entered: true, VIPs: vips, Previous: string(prev), At: s.Now()}
		if err := st.SetState(ctx, fs); err != nil {
			return err
		}
		*out = fs
		sent := s.Now()
		st.Logf("keepalived op %s uitzetten; %s gaat naar een andere node", n.Hostname, strings.Join(vips, ", "))
		_ = st.Flush(ctx)
		res, err := s.bus.Command(ctx, n.ID, protocol.Command{
			ID: uuid.NewString(), Action: protocol.CmdMaintenanceEnter, Deadline: sent.Add(time.Minute), Reason: "updates: failover",
		})
		if err == nil && !res.OK {
			err = errors.New(res.Error)
		}
		if err != nil {
			s.undoFailover(ctx, st, j, n, fs)
			return fmt.Errorf("keepalived uitzetten: %w", err)
		}
		if err := s.waitMoved(ctx, st, n, vips, updated, sent); err != nil {
			s.undoFailover(ctx, st, j, n, fs)
			return err
		}
		return nil
	})
}

// undoFailover zet keepalived terug als de failover zelf mislukte; de master
// is dan nog niet aangeraakt.
func (s *Service) undoFailover(ctx context.Context, st *jobs.Step, j *jobs.Job, n jobNode, fs failState) {
	if jobs.Interrupted(ctx) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	st.Logf("terugdraaien: keepalived op %s weer aan", n.Hostname)
	if _, err := s.bus.Command(ctx, n.ID, protocol.Command{ID: uuid.NewString(), Action: protocol.CmdMaintenanceExit, Deadline: s.Now().Add(time.Minute)}); err != nil {
		st.Logf("terugdraaien mislukt: %v", err)
	}
	prev := store.NodeLifecycle(fs.Previous)
	if prev == "" {
		prev = store.NodeLifecycleActive
	}
	if _, err := s.setLifecycle(ctx, j, n.ID, prev); err != nil {
		st.Logf("lifecycle terugzetten mislukt: %v", err)
	}
	_ = st.Flush(ctx)
}

// rejoinStep zet keepalived op de bijgewerkte master weer aan.
func (s *Service) rejoinStep(ctx context.Context, j *jobs.Job, name string, n jobNode, fs failState) error {
	if !fs.Entered {
		return nil
	}
	return j.Step(ctx, name+": keepalived weer aan", func(ctx context.Context, st *jobs.Step) error {
		sent := s.Now()
		res, err := s.bus.Command(ctx, n.ID, protocol.Command{ID: uuid.NewString(), Action: protocol.CmdMaintenanceExit, Deadline: sent.Add(time.Minute)})
		if err == nil && !res.OK {
			err = errors.New(res.Error)
		}
		if err != nil {
			return fmt.Errorf("keepalived aanzetten: %w", err)
		}
		prev := store.NodeLifecycle(fs.Previous)
		if prev == "" || prev == store.NodeLifecycleMaintenance || prev == store.NodeLifecycleDraining {
			prev = store.NodeLifecycleActive
		}
		if _, err := s.setLifecycle(ctx, j, n.ID, prev); err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, s.ReadyTimeout)
		defer cancel()
		return health.Poll(wctx, 2*time.Second, func(ctx context.Context) (bool, error) {
			rt, err := s.q.GetNodeRuntime(ctx, n.ID)
			if err != nil || rt.HeartbeatAt == nil || !rt.HeartbeatAt.After(sent) {
				return false, err
			}
			switch health.ServiceStates(rt.Services)["keepalived"] {
			case "failed":
				return false, errors.New("keepalived start niet op " + n.Hostname)
			case "active":
				st.Logf("keepalived draait weer op %s", n.Hostname)
				return true, nil
			}
			return false, nil
		})
	})
}

func (s *Service) heldVIPs(ctx context.Context, clusterID, nodeID uuid.UUID) ([]string, error) {
	rows, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range rows {
		if v.Vip.OwnerNodeID != nil && *v.Vip.OwnerNodeID == nodeID {
			out = append(out, v.Vip.Address.String())
		}
	}
	return out, nil
}

// checkTakeover eist een bijgewerkte node die online is en waarop
// keepalived draait; anders is de failover een sprong in het duister.
func (s *Service) checkTakeover(ctx context.Context, clusterID, nodeID uuid.UUID, updated []uuid.UUID) error {
	peers, err := s.q.ListClusterPeers(ctx, store.ListClusterPeersParams{ClusterID: &clusterID, NodeID: nodeID})
	if err != nil {
		return err
	}
	for _, p := range peers {
		services := map[string]string{}
		_ = json.Unmarshal(p.Services, &services)
		if slices.Contains(updated, p.ID) && p.Lifecycle == store.NodeLifecycleActive && p.HeartbeatAt != nil &&
			s.Now().Sub(*p.HeartbeatAt) <= status.HeartbeatLate && services["keepalived"] == "active" {
			return nil
		}
	}
	return errors.New("geen enkele al bijgewerkte node is online met keepalived actief; de master blijft zoals hij is")
}

// waitMoved wacht tot de VIP's op een andere node staan, en zegt of die
// node in deze taak bijgewerkt is.
func (s *Service) waitMoved(ctx context.Context, st *jobs.Step, n jobNode, vips []string, updated []uuid.UUID, sent time.Time) error {
	wctx, cancel := context.WithTimeout(ctx, s.MoveTimeout)
	defer cancel()
	err := health.Poll(wctx, time.Second, func(ctx context.Context) (bool, error) {
		rt, err := s.q.GetNodeRuntime(ctx, n.ID)
		if err != nil {
			return false, err
		}
		if rt.HeartbeatAt == nil || !rt.HeartbeatAt.After(sent) || rt.ClusterID == nil {
			return false, nil
		}
		for _, a := range vips {
			if slices.Contains(rt.Addresses, a) {
				return false, nil
			}
		}
		rows, err := s.q.ListVIPsByCluster(ctx, *rt.ClusterID)
		if err != nil {
			return false, err
		}
		var moved []string
		for _, v := range rows {
			addr := v.Vip.Address.String()
			if !slices.Contains(vips, addr) {
				continue
			}
			if v.Vip.OwnerNodeID == nil || *v.Vip.OwnerNodeID == n.ID || v.OwnerHostname == nil {
				return false, nil
			}
			note := ""
			if !slices.Contains(updated, *v.Vip.OwnerNodeID) {
				note = " (niet bijgewerkt in deze taak)"
			}
			moved = append(moved, addr+" staat nu op "+*v.OwnerHostname+note)
		}
		for _, m := range moved {
			st.Logf("%s", m)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("de VIP's van %s verhuisden niet binnen %s: %w", n.Hostname, s.MoveTimeout, err)
	}
	return nil
}

// setLifecycle zet de lifecycle van de node en schrijft een event; het geeft
// de vorige.
func (s *Service) setLifecycle(ctx context.Context, j *jobs.Job, nodeID uuid.UUID, to store.NodeLifecycle) (store.NodeLifecycle, error) {
	var prev store.NodeLifecycle
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.SetNodeLifecycle(ctx, store.SetNodeLifecycleParams{NodeID: nodeID, Lifecycle: to})
		if err != nil {
			return err
		}
		prev = row.Previous
		if row.Previous == to || s.ev == nil {
			return nil
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: s.actor(j), SubjectType: "node", SubjectID: nodeID.String(), ClusterID: row.ClusterID,
			Action: "node.lifecycle_changed", Payload: map[string]any{
				"hostname": row.Hostname, "from": row.Previous, "to": to, "job_id": j.ID, "title": j.Title, "reason": "updates: failover",
			},
		})
	})
	return prev, err
}
