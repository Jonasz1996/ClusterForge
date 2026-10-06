package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// NewNode is een node die bij omhoog schalen bij een bestaand cluster
// komt: zijn VM moet nog gemaakt en zijn agent aangemeld worden.
type NewNode struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Role     string    `json:"role"`
	Index    int       `json:"index"`
	// Address is leeg bij DHCP.
	Address string            `json:"address,omitempty"`
	Prefix  int               `json:"prefix,omitempty"`
	VM      templates.VMShape `json:"vm"`
}

// NewNodeOf maakt van een node uit de spec een nieuwe node.
func NewNodeOf(n SpecNode) NewNode {
	return NewNode(n)
}

// AddNodesTx zet nieuwe nodes van een cluster in de inventory, in
// lifecycle provisioning, en geeft elke node zijn id.
func (s *Service) AddNodesTx(ctx context.Context, q *store.Queries, actor events.Actor, clusterID uuid.UUID, tpl *templates.Template, nodes []NewNode) error {
	for i := range nodes {
		n := &nodes[i]
		created, err := s.inv.CreateNodeTx(ctx, q, actor, inventory.NodeFields{
			ClusterID: &clusterID, Hostname: n.Hostname, Role: n.Role, Lifecycle: store.NodeLifecycleProvisioning,
			PrimaryIP: n.Address, Tags: []string{},
			Description: fmt.Sprintf("Erbij gekomen met %s %s", tpl.Name, tpl.Version),
		})
		if err != nil {
			var ve inventory.ValidationError
			if errors.As(err, &ve) {
				return invalid("nodes", "%s: %s", n.Hostname, ve.Msg)
			}
			return inventory.Translate(err)
		}
		n.NodeID = created.ID
	}
	return nil
}

// Grow maakt de VM's van nieuwe nodes en meldt hun agents aan, met de
// stappen van een uitrol. De taak die het gebruikt, past daarna de
// template toe.
type Grow struct {
	r *runCtx

	once  sync.Once
	place error
}

// Grow bereidt het maken van de nieuwe nodes voor. spec is de gewenste
// staat met de nieuwe nodes erin; serverURL is het adres waarmee hun agents
// zich aanmelden.
func (s *Service) Grow(ctx context.Context, j *jobs.Job, clusterID uuid.UUID, spec Spec, serverURL string, nodes []NewNode) (*Grow, error) {
	if spec.Target.ProxmoxID == uuid.Nil {
		return nil, errors.New("de gewenste staat noemt geen Proxmox-koppeling, dus ClusterForge weet niet waar de nieuwe VM's moeten komen")
	}
	api, err := s.pve.API(ctx, spec.Target.ProxmoxID)
	if err != nil {
		return nil, err
	}
	r := &runCtx{s: s, j: j, api: api, actor: events.System(), p: params{
		Template: spec.Template.Name, Version: spec.Template.Version, ClusterID: clusterID, Cluster: spec.Cluster,
		Params: spec.Params, Secrets: spec.Secrets, Target: spec.Target, ServerURL: serverURL,
		ImageName: fmt.Sprintf("VM %d", spec.Target.ImageVMID),
	}}
	if j.RequestedBy != nil {
		r.actor = events.User(*j.RequestedBy)
	}
	for _, n := range nodes {
		r.p.Nodes = append(r.p.Nodes, plannedNode{
			ID: n.NodeID, Hostname: n.Hostname, Role: n.Role, Index: n.Index, Address: n.Address, Prefix: n.Prefix, VM: n.VM,
		})
	}
	return &Grow{r: r}, nil
}

// placeAll kiest één keer een host voor de VM's die nog niet bestaan, met
// het vrije geheugen van nu.
func (g *Grow) placeAll(ctx context.Context) error {
	g.once.Do(func() {
		r := g.r
		img, err := r.s.pve.Image(ctx, r.p.Target.ProxmoxID, r.p.Target.ImageVMID)
		if err != nil {
			g.place = fmt.Errorf("de golden image (VM %d): %w", r.p.Target.ImageVMID, err)
			return
		}
		r.p.ImageName, r.p.ImageShared = img.Name, img.Shared
		p := &params{Target: r.p.Target, ImageShared: img.Shared}
		var idx []int
		for i, n := range r.p.Nodes {
			row, err := r.s.q.GetNode(ctx, n.ID)
			if err != nil {
				g.place = err
				return
			}
			if row.Node.PveVmid == nil {
				p.Nodes = append(p.Nodes, n)
				idx = append(idx, i)
			}
		}
		if len(p.Nodes) == 0 {
			return
		}
		if err := r.s.placeNodes(ctx, p); err != nil {
			g.place = err
			return
		}
		for k, i := range idx {
			r.p.Nodes[i].Host = p.Nodes[k].Host
		}
	})
	return g.place
}

// CreateVM maakt de VM van de i-de nieuwe node. Heeft de node al een VM,
// zoals bij opnieuw toepassen na een mislukte taak, dan blijft die.
func (g *Grow) CreateVM(ctx context.Context, st *jobs.Step, i int) error {
	n := &g.r.p.Nodes[i]
	row, err := g.r.s.q.GetNode(ctx, n.ID)
	if err != nil {
		return err
	}
	if row.Node.PveVmid != nil {
		st.Logf("%s heeft al VM %d", n.Hostname, *row.Node.PveVmid)
		return nil
	}
	if err := g.placeAll(ctx); err != nil {
		return err
	}
	return g.r.createVM(ctx, st, n)
}

// Enroll schrijft in elke nieuwe VM een aanmeldbestand en wacht tot alle
// agents zich gemeld hebben, met facts.
func (g *Grow) Enroll(ctx context.Context, st *jobs.Step) error {
	if g.r.p.ServerURL == "" {
		return errors.New("het adres van ClusterForge ontbreekt, dus de nieuwe nodes kunnen zich niet aanmelden")
	}
	for i := range g.r.p.Nodes {
		n := &g.r.p.Nodes[i]
		if n.Host != "" {
			continue
		}
		// De host van een bestaande VM; enroll zoekt hem ook zelf op.
		if row, err := g.r.s.q.GetNode(ctx, n.ID); err == nil && row.PveNode != nil {
			n.Host = *row.PveNode
		}
	}
	return g.r.enrollAll(ctx, st)
}

// Close laat de server de VM's in Proxmox opnieuw lezen, zodat de nieuwe
// meteen bij hun node staan.
func (g *Grow) Close(ctx context.Context) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = g.r.s.pve.Sync(sctx, g.r.p.Target.ProxmoxID)
}
