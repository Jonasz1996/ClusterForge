package components

// Het VIP van de app voor de database: keepalived krijgt op elke node met
// het VIP een track_script cf_db (cf-agent check db), zodat het VIP alleen
// op een node staat waarvan MariaDB mag schrijven. De app verbindt met het
// VIP en schrijft zo altijd naar één node.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// VIP is het VIP van de database in de spec: het adres en de nodes van het
// onderdeel die het in keepalived hebben.
type VIP struct {
	ID      uuid.UUID   `json:"id"`
	Address string      `json:"address"`
	Nodes   []uuid.UUID `json:"nodes"`
}

// trackDir is de map met de vrrp_script van ClusterForge.
const trackDir = "/etc/keepalived/conf.d"

// vipSteps zijn de bestanden die keepalived nodig heeft voor de controle:
// de vrrp_script cf_db in conf.d. keepalived.conf zelf past de agent aan
// met DBTrack, omdat Jonas die met de hand schreef.
func vipSteps(slug, address string) []templates.Step {
	conf := fmt.Sprintf(`# Beheerd door ClusterForge (cluster %s). Het VIP %s staat alleen op een
# node waarvan MariaDB mag schrijven: Synced in een Primary component van
# Galera. keepalived.conf neemt dit bestand op met een include.
vrrp_script %s {
    script "%s"
    interval 2
    timeout 2
    fall 2
    rise 2
    user root
}
`, slug, address, protocol.DBTrackScript, protocol.DBCheckCommand)
	return []templates.Step{
		{Step: protocol.Step{Directory: &protocol.DirectoryStep{Path: trackDir, Mode: "0755"}}, Title: "map " + trackDir},
		{Step: protocol.Step{File: &protocol.FileStep{Path: protocol.DBTrackFile, Content: conf, Mode: "0644"}}, Title: "bestand " + protocol.DBTrackFile},
	}
}

// vipTitles zijn de stappen op een node met het VIP, in woorden.
func vipTitles(slug, address, instance string) []string {
	var out []string
	for _, st := range vipSteps(slug, address) {
		out = append(out, st.Title)
	}
	return append(out, "keepalived.conf: track_script "+protocol.DBTrackScript+" in "+cmp.Or(instance, "de vrrp_instance met "+address), "keepalived herladen")
}

// hasInstance zegt of keepalived op de node een vrrp_instance met het VIP
// heeft, en welke.
func hasInstance(f *protocol.Facts, address string) (string, bool) {
	if f == nil || f.Keepalived == nil {
		return "", false
	}
	for _, in := range f.Keepalived.Instances {
		if slices.Contains(in.VIPs, address) {
			return in.Name, true
		}
	}
	return "", false
}

// planVIP kijkt of het VIP bij de database kan en vult het in de spec en
// het plan in.
func (s *Service) planVIP(ctx context.Context, c store.Cluster, req Request, p *Plan, spec *Spec, byID map[uuid.UUID]store.ListComponentCandidatesRow,
	states map[uuid.UUID]deploy.NodeState, add func(string, string, *Node, string, string)) error {
	vips, err := s.q.ListVIPsByCluster(ctx, c.ID)
	if err != nil {
		return err
	}
	if req.VIP == nil {
		var addrs []string
		for _, v := range vips {
			addrs = append(addrs, v.Vip.Address.String())
		}
		msg := "zonder VIP volgt niets de database: een app die met één node verbindt, valt mee uit met die node"
		if len(addrs) > 0 {
			msg += ". Kies het VIP van de app (" + joinHosts(addrs) + ")"
		}
		add(Warning, "no_vip", nil, msg, "")
		return nil
	}
	i := slices.IndexFunc(vips, func(v store.ListVIPsByClusterRow) bool { return v.Vip.ID == *req.VIP })
	if i < 0 {
		return &InvalidError{"vip_id", "dat VIP hoort niet bij dit cluster"}
	}
	v := vips[i]
	addr := v.Vip.Address.String()
	p.VIP = &PlanVIP{ID: v.Vip.ID, Address: addr, Owner: deref(v.OwnerHostname)}
	if c.TemplateName != nil {
		add(Blocked, "vip_template", nil, fmt.Sprintf("keepalived.conf van %s komt uit de template %s, en de volgende uitrol haalt de controle van de database "+
			"er weer uit. Een VIP voor de database op een cluster uit een template komt later; kies voorlopig geen VIP", c.Name, *c.TemplateName), "")
		return nil
	}
	inSpec := map[uuid.UUID]*Node{}
	for i := range spec.Nodes {
		inSpec[spec.Nodes[i].NodeID] = &spec.Nodes[i]
	}
	// Elke node van het cluster met het VIP moet een database krijgen,
	// anders staat het VIP straks op een node zonder.
	others := slices.SortedFunc(maps.Values(byID), func(a, b store.ListComponentCandidatesRow) int { return strings.Compare(a.Hostname, b.Hostname) })
	for _, r := range others {
		if _, ok := inSpec[r.ID]; ok || r.Lifecycle == store.NodeLifecycleDecommissioned {
			continue
		}
		if inst, ok := hasInstance(states[r.ID].Facts, addr); ok {
			add(Blocked, "vip_outside", nil, fmt.Sprintf("%s heeft het VIP %s ook in keepalived (%s) maar krijgt geen database, dus het VIP kan op een node "+
				"zonder database belanden. Neem %s op in de database of haal het VIP daar uit keepalived", r.Hostname, addr, inst, r.Hostname), "")
		}
	}
	var nodes []Node
	data := 0
	for _, n := range spec.order() {
		f := states[n.NodeID].Facts
		inst, ok := hasInstance(f, addr)
		switch {
		case f == nil:
			continue
		case !ok && n.Role == RoleData:
			add(Warning, "vip_missing", inSpec[n.NodeID], fmt.Sprintf("%s heeft het VIP %s niet in keepalived; het VIP kan niet naar de database op %s", n.Hostname, addr, n.Hostname), "")
			continue
		case !ok:
			continue
		case n.Role == RoleArbiter:
			add(Warning, "vip_arbiter", inSpec[n.NodeID], fmt.Sprintf("scheidsrechter %s heeft het VIP ook in keepalived; daar staat geen data, dus ClusterForge "+
				"zet de controle er ook en komt het VIP nooit meer op %s", n.Hostname, n.Hostname), "")
		default:
			data++
		}
		if f.Keepalived.Active != "active" {
			add(Blocked, "keepalived_down", inSpec[n.NodeID], fmt.Sprintf("keepalived draait niet op %s (%s)", n.Hostname, cmp.Or(f.Keepalived.Active, "onbekend")), "")
		}
		nodes = append(nodes, n)
		if pn := p.node(n.NodeID); pn != nil {
			pn.VIPInstance = inst
		}
	}
	if data < 2 {
		add(Blocked, "vip_too_few", nil, fmt.Sprintf("het VIP %s staat in keepalived op %d van de datanodes; het moet er op minstens twee staan om bij een storing "+
			"te kunnen verhuizen", addr, data), "")
	}
	if v.Vip.OwnerNodeID == nil {
		add(Warning, "vip_no_owner", nil, "het VIP "+addr+" staat nu op geen enkele node", "")
	}
	spec.VIP = &VIP{ID: v.Vip.ID, Address: addr}
	for _, n := range nodes {
		spec.VIP.Nodes = append(spec.VIP.Nodes, n.NodeID)
	}

	// Wat de agent in keepalived.conf zou veranderen, op elke node tegelijk.
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(8)
	for _, n := range nodes {
		r := byID[n.NodeID]
		if !r.HasAgent || r.AgentProtocol < protocol.DBSince || r.HeartbeatAt == nil || s.Now().Sub(*r.HeartbeatAt) > s.Fresh {
			continue
		}
		g.Go(func() error {
			res, err := s.track(ctx, n.NodeID, addr, false)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				add(Blocked, "vip_conf", inSpec[n.NodeID], fmt.Sprintf("%s: keepalived.conf niet te lezen: %v", n.Hostname, err), "")
			default:
				if pn := p.node(n.NodeID); pn != nil {
					pn.VIPInstance, pn.VIPDiff = res.Instance, res.Diff
				}
			}
			return nil
		})
	}
	_ = g.Wait()
	for i := range p.Nodes {
		pn := &p.Nodes[i]
		if slices.Contains(spec.VIP.Nodes, pn.NodeID) {
			pn.Steps = append(pn.Steps, vipTitles(c.Slug, addr, pn.VIPInstance)...)
		}
	}
	return nil
}

// track vraagt de agent om track_script cf_db in keepalived.conf: met
// apply schrijft hij het, anders geeft hij alleen de diff.
func (s *Service) track(ctx context.Context, nodeID uuid.UUID, address string, apply bool) (*protocol.DBTrackResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.DB(ctx, nodeID, protocol.DBRequest{
		ID: uuid.NewString(), Op: protocol.DBTrack, Track: &protocol.DBTrackRequest{VIP: address, Apply: apply}, Deadline: time.Now().Add(s.Timeout),
	})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		if strings.Contains(res.Error, "onbekend databaseverzoek") {
			return nil, errors.New("de agent kent dit nog niet; werk hem bij")
		}
		return nil, errors.New(res.Error)
	}
	if res.Track == nil {
		return nil, errors.New("de agent gaf geen antwoord over keepalived.conf")
	}
	return res.Track, nil
}

func (p *Plan) node(id uuid.UUID) *PlanNode {
	for i := range p.Nodes {
		if p.Nodes[i].NodeID == id {
			return &p.Nodes[i]
		}
	}
	return nil
}

// vipHash vat het VIP van het plan samen, met de diffs: verandert
// keepalived.conf op een node, dan is het plan oud.
func vipHash(p Plan, spec Spec) string {
	if spec.VIP == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(spec.VIP.ID.String() + " " + spec.VIP.Address + "\n")
	for _, id := range spec.VIP.Nodes {
		b.WriteString(id.String() + "\n")
		if pn := p.node(id); pn != nil {
			b.WriteString(pn.VIPInstance + "\n" + pn.VIPDiff + "\n")
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// applyVIP koppelt het VIP op één node aan de database: het bestand met
// cf_db, de track_script in keepalived.conf, herladen, en dan wachten tot
// het VIP op een node met de database staat.
func (s *Service) applyVIP(ctx context.Context, j *jobs.Job, st *jobs.Step, r *rendered, n Node) error {
	vip := r.spec.VIP
	changed, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "-vip", s.Retry), vipSteps(r.spec.Cluster.Slug, vip.Address))
	if err != nil {
		return err
	}
	res, err := s.track(ctx, n.NodeID, vip.Address, true)
	if err != nil {
		return fmt.Errorf("keepalived.conf op %s: %w", n.Hostname, err)
	}
	if res.Changed {
		st.Logf("keepalived.conf: track_script %s in %s gezet", protocol.DBTrackScript, res.Instance)
		if res.Diff != "" {
			st.Logf("%s", strings.TrimRight(res.Diff, "\n"))
		}
		changed++
	} else {
		st.Logf("keepalived.conf: %s heeft track_script %s al", res.Instance, protocol.DBTrackScript)
	}
	if changed == 0 {
		return nil
	}
	sent := s.Now()
	reload := []templates.Step{{Step: protocol.Step{Service: &protocol.ServiceStep{Name: "keepalived", State: "reloaded"}}, Title: "keepalived herladen"}}
	if _, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "-vip-herladen", s.Retry), reload); err != nil {
		return err
	}
	return s.waitVIP(ctx, st, r.spec, sent)
}

// vipHolder zoekt de nodes met het VIP volgens hun laatste heartbeat van na
// since.
func (s *Service) vipHolder(ctx context.Context, spec Spec, since time.Time) ([]Node, error) {
	var out []Node
	for _, n := range spec.order() {
		if !slices.Contains(spec.VIP.Nodes, n.NodeID) {
			continue
		}
		rt, err := s.q.GetNodeRuntime(ctx, n.NodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if rt.HeartbeatAt != nil && rt.HeartbeatAt.After(since) && slices.Contains(rt.Addresses, spec.VIP.Address) {
			out = append(out, n)
		}
	}
	return out, nil
}

// waitVIP wacht tot het VIP na het herladen weer op een node staat.
func (s *Service) waitVIP(ctx context.Context, st *jobs.Step, spec Spec, since time.Time) error {
	deadline := s.Now().Add(s.VIPTimeout)
	for {
		holders, err := s.vipHolder(ctx, spec, since)
		if err != nil {
			return err
		}
		if len(holders) > 0 {
			var names []string
			for _, h := range holders {
				names = append(names, h.Hostname)
			}
			st.Logf("het VIP %s staat op %s", spec.VIP.Address, joinHosts(names))
			return nil
		}
		if s.Now().After(deadline) {
			return fmt.Errorf("het VIP %s staat %s na het herladen van keepalived op geen enkele node; kijk op de nodes met journalctl -u keepalived "+
				"en cf-agent check db", spec.VIP.Address, s.VIPTimeout)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.Poll):
		}
	}
}

// verifyVIP controleert dat precies één node het VIP heeft, en dat het een
// datanode is.
func (s *Service) verifyVIP(ctx context.Context, st *jobs.Step, spec Spec) error {
	if spec.VIP == nil {
		return nil
	}
	deadline := s.Now().Add(s.VIPTimeout)
	for {
		holders, err := s.vipHolder(ctx, spec, s.Now().Add(-s.Fresh))
		if err != nil {
			return err
		}
		switch {
		case len(holders) == 1 && holders[0].Role == RoleData:
			st.Logf("het VIP %s staat op %s, een node met de database", spec.VIP.Address, holders[0].Hostname)
			return nil
		case len(holders) > 1:
			var names []string
			for _, h := range holders {
				names = append(names, h.Hostname)
			}
			return fmt.Errorf("het VIP %s staat op %s tegelijk (split-brain); kijk of keepalived op die nodes elkaar hoort", spec.VIP.Address, joinHosts(names))
		case len(holders) == 1:
			return fmt.Errorf("het VIP %s staat op %s, waar geen data is", spec.VIP.Address, holders[0].Hostname)
		}
		if s.Now().After(deadline) {
			return fmt.Errorf("het VIP %s staat op geen enkele node van de database", spec.VIP.Address)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.Poll):
		}
	}
}

// vipOrder is de volgorde van de nodes met het VIP: de huidige eigenaar
// laatst, zodat het VIP hoogstens één keer verhuist.
func (s *Service) vipOrder(ctx context.Context, spec Spec) ([]Node, error) {
	v, err := s.q.GetVIP(ctx, spec.VIP.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("het VIP %s bestaat niet meer in ClusterForge", spec.VIP.Address)
	}
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, n := range spec.order() {
		if slices.Contains(spec.VIP.Nodes, n.NodeID) {
			out = append(out, n)
		}
	}
	if v.OwnerNodeID != nil {
		slices.SortStableFunc(out, func(a, b Node) int {
			switch {
			case a.NodeID == *v.OwnerNodeID:
				return 1
			case b.NodeID == *v.OwnerNodeID:
				return -1
			}
			return 0
		})
	}
	return out, nil
}
