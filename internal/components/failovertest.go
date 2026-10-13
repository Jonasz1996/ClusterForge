package components

// Wat de failovertests (internal/failover) van een onderdeel Database
// nodig hebben: welke databases een cluster heeft, of ze nu gezond zijn, en
// de schrijftest. Een failovertest neemt het clusterslot, dus de bewaking
// slaat het cluster dan over; daarom leest TestHealth de nodes zelf.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var _ failover.DBComponents = (*Service)(nil)

// dbUnit is de dienst van een database: wat een failovertest stopt.
func dbUnit(engine string) string {
	if engine == protocol.DBPostgreSQL {
		return "patroni"
	}
	return "mariadb"
}

// TestDatabases geeft de toegepaste onderdelen Database van een cluster.
func (s *Service) TestDatabases(ctx context.Context, clusterID uuid.UUID) ([]failover.Database, error) {
	rows, err := s.q.ListClusterComponents(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	var out []failover.Database
	for _, r := range rows {
		c := r.ClusterComponent
		if c.AppliedRevision == nil || (c.Engine != protocol.DBMariaDB && c.Engine != protocol.DBPostgreSQL) {
			continue
		}
		spec, err := parseSpec(c.Spec)
		if err != nil {
			return nil, err
		}
		d := failover.Database{ComponentID: c.ID, Engine: c.Engine, Title: title(spec), Port: enginePort(c.Engine), Unit: dbUnit(c.Engine)}
		if spec.VIP != nil {
			d.VIP, d.OwnVIP = spec.VIP.Address, spec.VIP.Own
		}
		d.AutoRecover = c.AutoRecover
		for _, n := range spec.order() {
			dn := failover.DatabaseNode{ID: n.NodeID, Hostname: n.Hostname}
			switch n.Role {
			case RoleData:
				d.Nodes = append(d.Nodes, dn)
			case RoleArbiter:
				d.Arbiters = append(d.Arbiters, dn)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// TestHealth leest de database nu: gezond, of waarom niet.
func (s *Service) TestHealth(ctx context.Context, clusterID, componentID uuid.UUID) (bool, string, error) {
	r, err := s.get(ctx, s.q, clusterID, componentID, false)
	if err != nil {
		return false, "", err
	}
	spec, err := parseSpec(r.Spec)
	if err != nil {
		return false, "", err
	}
	var nodes []dbNode
	if r.Engine == protocol.DBPostgreSQL {
		nodes = s.observePatroni(ctx, spec)
	} else {
		nodes = galeraNodes(s.observe(ctx, dataNodes(spec, nil), false), spec.Cluster.Slug)
	}
	now := s.Now()
	in, err := s.healthInput(ctx, r.Engine, spec, nodes, now)
	if err != nil {
		return false, "", err
	}
	// Eén waarneming: net na een wissel kan het VIP nog onderweg zijn, en
	// wie wacht, vraagt het straks opnieuw.
	in.stablePrimary = observeNodes(r.Engine, nodes, now).Primary
	h, reason := dbHealth(in)
	return h == status.Healthy, reason, nil
}

// PrepareWrites maakt de database van de schrijftest en haar gebruiker,
// met dit wachtwoord, op alle nodes en VIP's van het cluster.
func (s *Service) PrepareWrites(ctx context.Context, clusterID, componentID uuid.UUID, password string) error {
	r, err := s.get(ctx, s.q, clusterID, componentID, false)
	if err != nil {
		return err
	}
	t, err := s.targetOf(ctx, r)
	if err != nil {
		return err
	}
	hosts, _, err := s.userHosts(ctx, t)
	if err != nil {
		return err
	}
	_, err = s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: protocol.DBProbeDatabase, User: protocol.DBProbeUser, Hosts: hosts, Password: password, Grant: true,
	}})
	return err
}

// WriteProbe stuurt een schrijftest naar de agent van een node.
func (s *Service) WriteProbe(ctx context.Context, nodeID uuid.UUID, engine string, req protocol.DBProbeRequest) (*protocol.DBProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	dr := protocol.DBRequest{ID: uuid.NewString(), Op: protocol.DBWriteProbe, Deadline: s.Now().Add(s.Timeout), Probe: &req}
	if engine == protocol.DBPostgreSQL {
		dr.Engine = engine
	}
	res, err := s.bus.DB(ctx, nodeID, dr)
	switch {
	case err != nil:
		return nil, err
	case strings.HasPrefix(res.Error, "onbekend databaseverzoek"):
		return nil, errors.New("de agent kent de schrijftest nog niet; werk hem bij met het installatiescript en --upgrade")
	case res.Error != "":
		return nil, errors.New(res.Error)
	case res.Probe == nil:
		return nil, fmt.Errorf("de agent gaf geen antwoord op %s", req.Action)
	}
	return res.Probe, nil
}

// RestartRecover is de bewaking binnen de failovertest die alle nodes
// herstart. Ze beslist zoals de bewaking (recoverDue), zonder de backoff,
// en doet het werk van component.recover in de stap van de test.
func (s *Service) RestartRecover(ctx context.Context, st *jobs.Step, id string, clusterID, componentID uuid.UUID, since time.Time,
	force bool) (failover.Recovery, error) {
	var out failover.Recovery
	c, err := s.get(ctx, s.q, clusterID, componentID, false)
	if err != nil || c.Engine != protocol.DBMariaDB || c.AppliedRevision == nil {
		return out, err
	}
	r, err := s.load(ctx, jobParams{ComponentID: c.ID, Revision: int(*c.AppliedRevision)})
	if err != nil {
		return out, err
	}
	slug := r.spec.Cluster.Slug
	nodes := dataNodes(r.spec, nil)
	all := s.observe(ctx, nodes, false)
	gone := s.Now().Sub(since)
	if force {
		gone = max(gone, s.RecoverGrace, s.HungAfter)
	}
	if up(c.Engine, galeraNodes(all, slug)) {
		// Het cluster draait: een node die niet startte, start opnieuw.
		for i, o := range all {
			if !o.answered() || o.pos.Active != "failed" || gone < s.RecoverGrace {
				continue
			}
			st.Logf("MariaDB startte niet op %s terwijl het cluster draait; ClusterForge start hem daar opnieuw, zoals de bewaking", o.node.Hostname)
			_ = st.Flush(ctx)
			if err := s.recoverJoin(ctx, id, st, nodes[i], slug, 1); err != nil {
				return out, err
			}
			out.Started = append(out.Started, o.node.Hostname)
		}
		return out, nil
	}
	if !s.recoverDue(all, gone) {
		return out, nil
	}
	ev := func(action string, payload map[string]any) {
		payload["name"], payload["component"], payload["auto"] = r.spec.Cluster.Name, r.tpl.Title, true
		if err := s.ev.Write(ctx, nil, events.Event{
			Actor: events.System(), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: action, Payload: payload,
		}); err != nil {
			s.log.Warn("event van de failovertest", "action", action, "err", err)
		}
	}
	ev("cluster.database_recovering", map[string]any{"reason": "na het herstarten van alle nodes in een failovertest"})
	st.Logf("geen enkele node zit in een Primary component; ClusterForge begint %s opnieuw, zoals de bewaking na een volledige uitval", r.tpl.Title)
	_ = st.Flush(ctx)
	dec, err := s.decide(ctx, id, st, r, nodes)
	if err != nil {
		return out, err
	}
	joined := 0
	for _, n := range nodes {
		if dec.Begin != nil && n.NodeID == *dec.Begin {
			if err := s.recoverBegin(ctx, id, st, r, n); err != nil {
				return out, err
			}
			out.Began, joined = n.Hostname, 1
		}
	}
	for _, n := range nodes {
		if n.Hostname == out.Began {
			continue
		}
		joined++
		if err := s.recoverJoin(ctx, id, st, n, slug, joined); err != nil {
			return out, err
		}
		out.Started = append(out.Started, n.Hostname)
	}
	for _, n := range r.spec.order() {
		if n.Role != RoleArbiter {
			continue
		}
		joined++
		if err := s.recoverArbiter(ctx, id, st, nodes[0], n, slug, joined); err != nil {
			return out, err
		}
	}
	if err := s.verifyNodes(ctx, st, slug, nodes, joined); err != nil {
		return out, err
	}
	if err := s.verifyVIP(ctx, st, r.spec); err != nil {
		return out, err
	}
	ev("cluster.database_recovered", map[string]any{"begin": out.Began, "nodes": hostsOf(nodes)})
	if s.Changed != nil {
		s.Changed()
	}
	return out, nil
}

// healthInput is wat dbHealth naast de nodes nodig heeft: wie het VIP
// heeft, en welke scheidsrechters offline zijn.
func (s *Service) healthInput(ctx context.Context, engine string, spec Spec, nodes []dbNode, now time.Time) (healthInput, error) {
	in := healthInput{engine: engine, spec: spec, nodes: nodes}
	if spec.VIP != nil {
		holders, err := s.vipHolder(ctx, spec, now.Add(-s.Fresh))
		if err != nil {
			return in, err
		}
		for _, h := range holders {
			in.holders = append(in.holders, h.Hostname)
		}
	}
	for _, n := range spec.order() {
		if n.Role != RoleArbiter {
			continue
		}
		rt, err := s.q.GetNodeRuntime(ctx, n.NodeID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return in, err
		}
		if err != nil || rt.HeartbeatAt == nil || now.Sub(*rt.HeartbeatAt) > s.Fresh {
			in.arbitersDown = append(in.arbitersDown, n.Hostname)
		}
	}
	return in, nil
}
