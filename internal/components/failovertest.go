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

	"github.com/Jonasz1996/clusterforge/internal/failover"
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
		for _, n := range dataNodes(spec, nil) {
			d.Nodes = append(d.Nodes, failover.DatabaseNode{ID: n.NodeID, Hostname: n.Hostname})
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
