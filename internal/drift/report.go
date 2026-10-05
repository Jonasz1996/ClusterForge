package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Report is de drift van een cluster, of van één node daarin.
type Report struct {
	// Source is nil als het cluster geen gewenste staat heeft.
	Source *Source
	Notes  []string
	Nodes  []NodeReport
}

// Source zegt waarmee vergeleken wordt.
type Source struct {
	Kind     string
	Template string
	Version  string
	Revision int
}

// NodeReport is de laatste controle van één node, met waarom hij nu niet
// gecontroleerd wordt.
type NodeReport struct {
	NodeID   uuid.UUID
	Hostname string
	// Status is in_sync, drift, error, none, of unknown als hij nog nooit
	// gecontroleerd is.
	Status       string
	CheckedAt    *time.Time
	DriftSince   *time.Time
	SpecRevision int
	Findings     []Finding
	Unchecked    []Unchecked
	Error        string
	Skipped      string
	AgentTooOld  bool
}

// Report leest de drift van een cluster. Met nodeID alleen die node.
func (s *Service) Report(ctx context.Context, clusterID uuid.UUID, nodeID *uuid.UUID) (Report, error) {
	rep := Report{Notes: []string{}, Nodes: []NodeReport{}}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return rep, err
	}
	if c.TemplateName == nil {
		return rep, nil
	}
	rep.Source = &Source{Kind: "template", Template: *c.TemplateName, Version: deref(c.TemplateVersion), Revision: int(c.SpecRevision)}
	d, err := s.dep.Desired(ctx, clusterID)
	switch {
	case errors.Is(err, deploy.ErrNoSpec):
	case err != nil:
		rep.Notes = append(rep.Notes, "De gewenste staat is niet te lezen, dus elke controle mislukt: "+err.Error())
	default:
		rep.Source.Version = d.Spec.Template.Version
		if latest, ok := s.dep.Templates.Latest(d.Spec.Template.Name); ok && latest.Version != d.Spec.Template.Version {
			rep.Notes = append(rep.Notes, fmt.Sprintf("Deze server kent ook %s %s; dit cluster wordt vergeleken met %s, de versie waarmee het is uitgerold.",
				d.Spec.Template.Name, latest.Version, d.Spec.Template.Version))
		}
		if len(d.Membership) > 0 {
			rep.Notes = append(rep.Notes, "Het lidmaatschap wijkt af van de specificatie:")
			rep.Notes = append(rep.Notes, d.Membership...)
		}
	}
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{ClusterID: &clusterID, NodeID: nodeID})
	if err != nil {
		return rep, err
	}
	now := s.Now()
	for _, n := range rows {
		busy, err := s.q.NodeJobBusy(ctx, store.NodeJobBusyParams{NodeID: &n.ID, ClusterID: n.ClusterID})
		if err != nil {
			return rep, err
		}
		nr := NodeReport{
			NodeID: n.ID, Hostname: n.Hostname, Status: "unknown", Findings: []Finding{}, Unchecked: []Unchecked{},
			Skipped: Skip(n, busy, now), AgentTooOld: n.HasAgent && n.AgentProtocol < protocol.InspectSince,
		}
		row, err := s.q.GetDriftCheck(ctx, n.ID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return rep, err
		default:
			nr.Status, nr.CheckedAt, nr.DriftSince, nr.SpecRevision, nr.Error = row.Status, &row.CheckedAt, row.DriftSince, int(row.SpecRevision), row.Error
			_ = json.Unmarshal(row.Findings, &nr.Findings)
			_ = json.Unmarshal(row.Unchecked, &nr.Unchecked)
		}
		rep.Nodes = append(rep.Nodes, nr)
	}
	return rep, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
