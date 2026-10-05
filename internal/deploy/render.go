package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ErrNoSpec betekent dat een cluster geen gewenste staat heeft, omdat het
// niet uit een template is uitgerold.
var ErrNoSpec = errors.New("dit cluster is niet uit een template uitgerold")

// NodeState is wat de server van een node weet om voor hem te renderen:
// het vaste adres uit de inventory en de facts van de agent.
type NodeState struct {
	PrimaryIP string
	Facts     *protocol.Facts
}

// nodeInfo vult adres, prefix en netwerkkaart van een node aan uit de
// inventory en de facts.
func nodeInfo(n SpecNode, st NodeState) (templates.NodeInfo, error) {
	info := templates.NodeInfo{Hostname: n.Hostname, Role: n.Role, Index: n.Index, Address: n.Address, Prefix: n.Prefix}
	var facts protocol.Facts
	if st.Facts != nil {
		facts = *st.Facts
	}
	if info.Address == "" {
		info.Address = st.PrimaryIP
		if info.Address == "" {
			info.Address = facts.PrimaryAddress
		}
	}
	for _, iface := range facts.Interfaces {
		for _, a := range iface.Addresses {
			pf, err := netip.ParsePrefix(a)
			if err == nil && pf.Addr().String() == info.Address {
				info.Interface, info.Prefix = iface.Name, pf.Bits()
			}
		}
	}
	switch {
	case info.Address == "":
		return info, fmt.Errorf("het adres van %s is nog niet bekend; de agent heeft nog geen facts gestuurd", n.Hostname)
	case info.Interface == "":
		return info, fmt.Errorf("geen netwerkkaart met %s gevonden in de facts van %s", info.Address, n.Hostname)
	}
	return info, nil
}

// Context bouwt wat de sjablonen van tpl zien. secrets zijn de opgeslagen
// geheimen; ontbreekt er een, dan is dat een fout.
func (s Spec) Context(tpl *templates.Template, secrets map[string]string, nodes map[uuid.UUID]NodeState) (templates.Context, error) {
	if tpl.Name != s.Template.Name || tpl.Version != s.Template.Version {
		return templates.Context{}, fmt.Errorf("de spec hoort bij %s %s, niet bij %s %s", s.Template.Name, s.Template.Version, tpl.Name, tpl.Version)
	}
	values, err := tpl.Values(s.Params, secrets)
	if err != nil {
		return templates.Context{}, fmt.Errorf("parameters passen niet bij %s %s: %w", tpl.Name, tpl.Version, err)
	}
	c := templates.Context{Params: values, Cluster: s.Cluster}
	for _, n := range s.Nodes {
		info, err := nodeInfo(n, nodes[n.NodeID])
		if err != nil {
			return templates.Context{}, err
		}
		c.Nodes = append(c.Nodes, info)
	}
	return c, nil
}

// RenderNode geeft de stappen van één node, gerenderd met de templateversie
// van de spec. De uitrol gebruikt dezelfde functie, dus wat hier uitkomt is
// wat de uitrol op die node toepaste.
func RenderNode(tpl *templates.Template, s Spec, secrets map[string]string, nodes map[uuid.UUID]NodeState, nodeID uuid.UUID) ([]templates.Step, error) {
	c, err := s.Context(tpl, secrets, nodes)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(s.Nodes, func(n SpecNode) bool { return n.NodeID == nodeID })
	if i < 0 {
		return nil, fmt.Errorf("node %s staat niet in de specificatie", nodeID)
	}
	c.Node = &c.Nodes[i]
	return tpl.Steps(s.Nodes[i].Role, c)
}

// Desired is de gewenste staat van een cluster, klaar om per node te
// renderen: de spec, de template in de versie waarmee het cluster is
// uitgerold, de geheimen en wat de server van de nodes weet.
type Desired struct {
	Spec     Spec
	Revision int
	Template *templates.Template
	// Membership zijn de afwijkingen tussen de spec en de actieve nodes.
	Membership []string

	secrets map[string]string
	nodes   map[uuid.UUID]NodeState
}

// Render geeft de stappen van één node.
func (d *Desired) Render(nodeID uuid.UUID) ([]templates.Step, error) {
	return RenderNode(d.Template, d.Spec, d.secrets, d.nodes, nodeID)
}

// Desired leest de gewenste staat van een cluster. Zonder spec is het
// ErrNoSpec; ontbreekt de templateversie in deze server, een geheim of de
// masterkey, dan is het een fout en wordt er niets verzonnen.
func (s *Service) Desired(ctx context.Context, clusterID uuid.UUID) (*Desired, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	inv, nodes, err := s.loadNodes(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	spec, err := ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if err != nil {
		return nil, err
	}
	tpl, ok := s.Templates.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return nil, fmt.Errorf("%s %s zit niet in deze server; rol de server terug naar een versie die haar kent", spec.Template.Name, spec.Template.Version)
	}
	secrets, err := s.openSecrets(ctx, clusterID, spec.Secrets)
	if err != nil {
		return nil, err
	}
	return &Desired{
		Spec: spec, Revision: int(c.SpecRevision), Template: tpl, Membership: spec.Membership(inv),
		secrets: secrets, nodes: nodes,
	}, nil
}

// loadNodes leest de nodes van een cluster: voor de lidmaatschapscontrole
// en met wat renderen van ze nodig heeft.
func (s *Service) loadNodes(ctx context.Context, clusterID uuid.UUID) ([]InventoryNode, map[uuid.UUID]NodeState, error) {
	rows, err := s.q.ListDesiredNodes(ctx, &clusterID)
	if err != nil {
		return nil, nil, err
	}
	inv := make([]InventoryNode, len(rows))
	nodes := map[uuid.UUID]NodeState{}
	for i, r := range rows {
		inv[i] = InventoryNode{ID: r.ID, Hostname: r.Hostname, Lifecycle: string(r.Lifecycle)}
		nodes[r.ID] = nodeState(r.PrimaryIp, r.Facts)
	}
	return inv, nodes, nil
}

func nodeState(primaryIP string, facts []byte) NodeState {
	st := NodeState{PrimaryIP: primaryIP}
	if len(facts) > 0 {
		var f protocol.Facts
		if json.Unmarshal(facts, &f) == nil {
			st.Facts = &f
		}
	}
	return st
}

// openSecrets ontsleutelt de geheimen van een cluster.
func (s *Service) openSecrets(ctx context.Context, clusterID uuid.UUID, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) > 0 && s.box == nil {
		return nil, errors.New("de server heeft geen masterkey (CF_MASTER_KEY); zonder kan hij de geheimen van het cluster niet lezen")
	}
	for _, name := range names {
		sec, err := s.q.GetSecret(ctx, store.GetSecretParams{ClusterID: clusterID, Name: name})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("geheim %s van het cluster ontbreekt", name)
		}
		if err != nil {
			return nil, err
		}
		v, err := s.box.Open(sec.ValueEnc, secretAAD(clusterID, name), sec.KeyID)
		if err != nil {
			return nil, fmt.Errorf("geheim %s is niet te ontsleutelen (%w); is CF_MASTER_KEY veranderd?", name, err)
		}
		out[name] = string(v)
	}
	return out, nil
}

// ApplyOptions zegt waar en hoe ApplySteps de stappen toepast.
type ApplyOptions struct {
	NodeID   uuid.UUID
	Hostname string
	// CommandID is het begin van het id van elk commando. Met hetzelfde
	// begin herkent de agent een herhaling na een onderbreking.
	CommandID string
	// Retry is de wachttijd tussen pogingen als de agent even weg is.
	Retry time.Duration
	// Logf schrijft een regel in het logboek van de stap; Flush bewaart wat
	// er staat. Flush mag nil zijn.
	Logf  func(format string, a ...any)
	Flush func()
}

// ApplySteps voert gerenderde stappen uit op één node, één commando per
// stap, zodat de voortgang zichtbaar is. Daarna herlaadt of herstart ze de
// services waarvan de configuratie veranderde. Het geeft het aantal stappen
// dat iets aanpaste.
func ApplySteps(ctx context.Context, bus Commander, o ApplyOptions, steps []templates.Step) (int, error) {
	flush := func() {
		if o.Flush != nil {
			o.Flush()
		}
	}
	var notify []protocol.ServiceStep
	changed := 0
	for i, s := range steps {
		res, err := applyOne(ctx, bus, o, fmt.Sprintf("%d", i+1), s.Step)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", s.Title, err)
		}
		mark := "ongewijzigd"
		if res.Changed {
			mark = "aangepast"
			changed++
			for _, h := range s.Notify {
				if !containsService(notify, h) {
					notify = append(notify, h)
				}
			}
			// Een service die net gestart of herstart is, leest zijn
			// configuratie al opnieuw.
			if svc := s.Service; svc != nil && (svc.State == "started" || svc.State == "restarted") {
				notify = slices.DeleteFunc(notify, func(h protocol.ServiceStep) bool { return h.Name == svc.Name })
			}
		}
		o.Logf("%s: %s", s.Title, mark)
		flush()
	}
	for i, h := range notify {
		if _, err := applyOne(ctx, bus, o, fmt.Sprintf("notify-%d", i+1), protocol.Step{Service: &h}); err != nil {
			return changed, fmt.Errorf("service %s: %w", h.Name, err)
		}
		o.Logf("service %s: %s na gewijzigde configuratie", h.Name, map[string]string{"reloaded": "herladen", "restarted": "herstart"}[h.State])
	}
	o.Logf("%d van %d stappen pasten iets aan", changed, len(steps))
	return changed, nil
}

func containsService(list []protocol.ServiceStep, s protocol.ServiceStep) bool {
	for _, x := range list {
		if x.Name == s.Name && x.State == s.State {
			return true
		}
	}
	return false
}

// applyOne stuurt één stap naar de agent, met een paar pogingen als de
// agent even niet bereikbaar is.
func applyOne(ctx context.Context, bus Commander, o ApplyOptions, suffix string, step protocol.Step) (protocol.StepResult, error) {
	cmd := protocol.Command{ID: o.CommandID + "-" + suffix, Action: protocol.CmdApply, Steps: []protocol.Step{step}}
	var res protocol.Result
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return protocol.StepResult{}, context.Cause(ctx)
			case <-time.After(o.Retry):
			}
		}
		cmd.Deadline = time.Now().Add(2 * time.Minute)
		cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		res, err = bus.Command(cctx, o.NodeID, cmd)
		cancel()
		unreachable := errors.Is(err, agentbus.ErrAgentOffline) || errors.Is(err, agentbus.ErrNoAnswer)
		if err == nil || ctx.Err() != nil || !unreachable {
			break
		}
		o.Logf("agent op %s niet bereikt: %v", o.Hostname, err)
		if o.Flush != nil {
			o.Flush()
		}
	}
	if ctx.Err() != nil {
		return protocol.StepResult{}, context.Cause(ctx)
	}
	if err != nil {
		return protocol.StepResult{}, err
	}
	var sr protocol.StepResult
	if len(res.Steps) > 0 {
		sr = res.Steps[0]
	}
	for _, line := range sr.Output {
		o.Logf("  %s", line)
	}
	if !res.OK {
		msg := res.Error
		if sr.Error != "" {
			msg = sr.Error
		}
		return sr, errors.New(msg)
	}
	return sr, nil
}

// MissingTemplates geeft de clusters waarvan de templateversie niet in deze
// server zit, bijvoorbeeld na een update die een versie liet vallen. Voor
// die clusters is er geen gewenste staat.
func (s *Service) MissingTemplates(ctx context.Context) ([]string, error) {
	rows, err := s.q.ListTemplateClusters(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if _, ok := s.Templates.Get(r.TemplateName, r.TemplateVersion); !ok {
			out = append(out, fmt.Sprintf("%s gebruikt %s %s", r.Name, r.TemplateName, r.TemplateVersion))
		}
	}
	return out, nil
}
