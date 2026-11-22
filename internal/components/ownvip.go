package components

// Een eigen VIP voor de database: ClusterForge maakt in conf.d een
// vrrp_instance met dat VIP en een vrrp_script die cf-agent check db
// aanroept, op elke datanode. Alleen de node waar de database schrijft,
// slaagt voor de controle, dus daar staat het VIP: bij PostgreSQL de
// primary van Patroni, bij MariaDB een node die Synced is. keepalived.conf
// krijgt alleen een include van conf.d; staat er nog geen, dan maakt de
// agent er een.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// vrrpSecret is het geheim van een onderdeel met het wachtwoord van VRRP
// voor het eigen VIP; keepalived leest er hoogstens 8 tekens van.
const vrrpSecret = "vrrp"

// firstVRID is het laagste VRRP-id dat ClusterForge voor een eigen VIP
// kiest; daaronder laat het ruimte voor wat Jonas zelf instelde.
const firstVRID = 50

// ownVIP zijn de namen van het eigen VIP van een engine: het bestand in
// conf.d, de vrrp_script en zijn commando.
func ownVIP(engine string) (file, script, command string) {
	if engine == protocol.DBPostgreSQL {
		return protocol.DBTrackPGFile, protocol.DBTrackPGScript, protocol.DBCheckPGCommand
	}
	return protocol.DBTrackFile, protocol.DBTrackScript, protocol.DBCheckCommand
}

// ownInstance is de naam van de vrrp_instance van het eigen VIP.
func ownInstance(slug, engine string) string { return "cf-" + slug + "-" + engine }

// ownVIPConf is het bestand in conf.d op één node. self is de node, peers
// de adressen van de andere nodes met het VIP.
func ownVIPConf(spec Spec, engine string, self templates.NodeInfo, peers []string, pass string) string {
	file, script, command := ownVIP(engine)
	who := "MariaDB mag schrijven (Synced in een Primary component van Galera)"
	if engine == protocol.DBPostgreSQL {
		who = "PostgreSQL de primary is (volgens Patroni)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# Beheerd door ClusterForge (cluster %s). Het VIP %s staat alleen op
# de node waar %s.
# keepalived.conf neemt %s op met een include; een
# wijziging hier ziet de driftcontrole.
vrrp_script %s {
    script "%s"
    interval 2
    timeout 2
    fall 2
    rise 2
    user root
}

# Alle nodes hebben dezelfde prioriteit: alleen de node die voor de
# controle slaagt, kan het VIP krijgen, en een node die terugkomt, neemt
# het niet over.
vrrp_instance %s {
    state BACKUP
    interface %s
    virtual_router_id %d
    priority 100
    advert_int 1
    unicast_src_ip %s
    unicast_peer {
`, spec.Cluster.Slug, spec.VIP.Address, who, file, script, command, ownInstance(spec.Cluster.Slug, engine), self.Interface, spec.VIP.VRID, self.Address)
	for _, p := range peers {
		fmt.Fprintf(&b, "        %s\n", p)
	}
	fmt.Fprintf(&b, `    }
    authentication {
        auth_type PASS
        auth_pass %s
    }
    virtual_ipaddress {
        %s/%d dev %s
    }
    track_script {
        %s
    }
}
`, pass, spec.VIP.Address, self.Prefix, self.Interface, script)
	return b.String()
}

// ownVIPSteps zijn de stappen voor het eigen VIP op node n: keepalived
// installeren en het bestand in conf.d. Zonder geheimen (in het plan) staat
// er een plaatshouder voor het wachtwoord.
func ownVIPSteps(r *rendered, engine string, n Node) ([]templates.Step, error) {
	c, err := r.spec.deploySpec().Context(r.tpl, r.secrets, r.states)
	if err != nil {
		return nil, err
	}
	var self *templates.NodeInfo
	var peers []string
	for i, info := range c.Nodes {
		id := r.spec.Nodes[i].NodeID
		switch {
		case id == n.NodeID:
			self = &c.Nodes[i]
		case slices.Contains(r.spec.VIP.Nodes, id):
			peers = append(peers, info.Address)
		}
	}
	if self == nil {
		return nil, fmt.Errorf("%s hoort niet bij de database", n.Hostname)
	}
	file, _, _ := ownVIP(engine)
	return []templates.Step{
		{Step: protocol.Step{Package: &protocol.PackageStep{Names: []string{"keepalived"}}}, Title: "pakket keepalived"},
		{Step: protocol.Step{Directory: &protocol.DirectoryStep{Path: trackDir, Mode: "0755"}}, Title: "map " + trackDir},
		{Step: protocol.Step{File: &protocol.FileStep{Path: file, Content: ownVIPConf(r.spec, engine, *self, peers, r.vrrp), Mode: "0600"}}, Title: "bestand " + file},
	}, nil
}

// ownVIPTitles zijn de stappen voor het eigen VIP op een node, in woorden.
// include is true als keepalived.conf de include van conf.d nog krijgt.
func ownVIPTitles(engine string, include bool) []string {
	file, _, _ := ownVIP(engine)
	out := []string{"pakket keepalived", "map " + trackDir, "bestand " + file}
	if include {
		out = append(out, "keepalived.conf: include van "+trackDir)
	}
	return append(out, "keepalived starten of herladen")
}

// planOwnVIP kijkt of het eigen VIP kan en vult het in de spec en het plan
// in: een vrij adres in het netwerk van de nodes, met een vrij VRRP-id.
func (s *Service) planOwnVIP(ctx context.Context, req Request, p *Plan, spec *Spec, byID map[uuid.UUID]store.ListComponentCandidatesRow,
	states map[uuid.UUID]deploy.NodeState, add func(string, string, *Node, string, string)) error {
	if req.VIP != nil {
		return &InvalidError{"vip_id", "kies het VIP van de app of een eigen VIP, niet allebei"}
	}
	a, err := netip.ParseAddr(strings.TrimSpace(req.VIPAddress))
	if err != nil || !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return &InvalidError{"vip_address", "moet een IPv4-adres zijn, zoals 10.0.20.190"}
	}
	addr := a.String()

	// Is het adres vrij?
	used, err := s.q.UsedAddresses(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(used, addr) {
		add(Blocked, "vip_in_use", nil, addr+" hoort al bij een node of VIP in ClusterForge; kies een vrij adres", "")
	}
	vrids, err := s.q.UsedVRIDs(ctx)
	if err != nil {
		return err
	}
	taken := map[int]bool{}
	for _, v := range vrids {
		taken[int(v)] = true
	}
	for _, r := range byID {
		f := states[r.ID].Facts
		if f == nil {
			continue
		}
		for _, iface := range f.Interfaces {
			for _, x := range iface.Addresses {
				if pf, err := netip.ParsePrefix(x); err == nil && pf.Addr() == a {
					add(Blocked, "vip_in_use", nil, fmt.Sprintf("%s staat al op %s (%s); kies een vrij adres", addr, r.Hostname, iface.Name), "")
				}
			}
		}
		if f.Keepalived == nil {
			continue
		}
		for _, in := range f.Keepalived.Instances {
			taken[in.VRID] = true
			if slices.Contains(in.VIPs, addr) {
				add(Blocked, "vip_in_use", nil, fmt.Sprintf("%s staat al in keepalived op %s (%s); kies een ander adres of haal het daar weg", addr, r.Hostname, in.Name), "")
			}
		}
	}
	vrid := firstVRID
	for taken[vrid] && vrid <= 255 {
		vrid++
	}
	if vrid > 255 {
		add(Blocked, "vip_no_vrid", nil, "er is geen VRRP-id meer vrij", "")
	}

	// Het VIP komt op de datanodes, in hun netwerk.
	spec.VIP = &VIP{Address: addr, Own: true, VRID: vrid}
	p.VIP = &PlanVIP{Address: addr, Own: true, VRID: vrid}
	var fresh []Node
	for _, n := range spec.order() {
		if n.Role != RoleData {
			continue
		}
		spec.VIP.Nodes = append(spec.VIP.Nodes, n.NodeID)
		f, r := states[n.NodeID].Facts, byID[n.NodeID]
		if f == nil {
			continue
		}
		iface, pf, ok := nodePrefix(f, cmp.Or(states[n.NodeID].PrimaryIP, f.PrimaryAddress))
		if ok && !pf.Contains(a) {
			add(Blocked, "vip_subnet", &n, fmt.Sprintf("%s ligt niet in het netwerk van %s (%s); het VIP moet in hetzelfde netwerk als de nodes", addr, n.Hostname, pf.Masked()), "")
		}
		if p.VIP.Interface == "" {
			p.VIP.Interface = iface
		}
		if f.Keepalived != nil && f.Keepalived.Active != "active" {
			add(Warning, "keepalived_down", &n, fmt.Sprintf("keepalived draait niet op %s (%s); ClusterForge start het, en dan komen ook de andere VIP's "+
				"in keepalived.conf daar weer op", n.Hostname, cmp.Or(f.Keepalived.Active, "onbekend")), "")
		}
		if r.HasAgent && r.AgentProtocol >= protocol.DBSince && r.HeartbeatAt != nil && s.Now().Sub(*r.HeartbeatAt) <= s.Fresh {
			fresh = append(fresh, n)
		}
	}

	// Wat de agent in keepalived.conf zou veranderen: alleen de include.
	include := map[uuid.UUID]bool{}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(8)
	for _, n := range fresh {
		g.Go(func() error {
			res, err := s.include(ctx, n.NodeID, addr, false)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				add(Blocked, "vip_conf", &n, fmt.Sprintf("%s: keepalived.conf: %v", n.Hostname, err), "")
			default:
				include[n.NodeID] = res.Changed
				if pn := p.node(n.NodeID); pn != nil {
					pn.VIPInstance, pn.VIPDiff = ownInstance(spec.Cluster.Slug, p.Engine), res.Diff
				}
			}
			return nil
		})
	}
	_ = g.Wait()
	for i := range p.Nodes {
		pn := &p.Nodes[i]
		if slices.Contains(spec.VIP.Nodes, pn.NodeID) {
			pn.Steps = append(pn.Steps, ownVIPTitles(p.Engine, include[pn.NodeID])...)
		}
	}
	add(Info, "own_vip", nil, fmt.Sprintf("ClusterForge maakt het VIP %s met VRRP-id %d; het staat altijd op de node waar %s schrijft. "+
		"ClusterForge ziet niet of een ander apparaat het adres al gebruikt: kijk dat eerst na met ping %s", addr, vrid, engines[p.Engine].server, addr), "")
	return nil
}

// nodePrefix is de netwerkkaart met address en haar netwerk.
func nodePrefix(f *protocol.Facts, address string) (string, netip.Prefix, bool) {
	for _, iface := range f.Interfaces {
		for _, x := range iface.Addresses {
			if pf, err := netip.ParsePrefix(x); err == nil && pf.Addr().String() == address {
				return iface.Name, pf, true
			}
		}
	}
	return "", netip.Prefix{}, false
}

// include vraagt de agent om de include van conf.d in keepalived.conf: met
// apply schrijft hij haar, anders geeft hij alleen de diff.
func (s *Service) include(ctx context.Context, nodeID uuid.UUID, address string, apply bool) (*protocol.DBTrackResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.DB(ctx, nodeID, protocol.DBRequest{
		ID: uuid.NewString(), Op: protocol.DBTrack, Track: &protocol.DBTrackRequest{VIP: address, Apply: apply, Include: true}, Deadline: time.Now().Add(s.Timeout),
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

// applyOwnVIP zet het eigen VIP op één node: keepalived, het bestand in
// conf.d en de include, en start of herlaadt keepalived als er iets
// veranderde. Waar het VIP daarna staat, kijkt Controleren.
func (s *Service) applyOwnVIP(ctx context.Context, j *jobs.Job, st *jobs.Step, r *rendered, n Node) error {
	engine := r.comp.Engine
	steps, err := ownVIPSteps(r, engine, n)
	if err != nil {
		return err
	}
	changed, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "-vip", s.Retry), steps)
	if err != nil {
		return err
	}
	res, err := s.include(ctx, n.NodeID, r.spec.VIP.Address, true)
	if err != nil {
		return fmt.Errorf("keepalived.conf op %s: %w", n.Hostname, err)
	}
	if res.Changed {
		st.Logf("keepalived.conf: include van %s gezet", trackDir)
		if res.Diff != "" {
			st.Logf("%s", strings.TrimRight(res.Diff, "\n"))
		}
		changed++
	}
	svc := protocol.ServiceStep{Name: "keepalived", Enabled: ptr(true), State: "started"}
	if changed > 0 {
		// reload-or-restart: start keepalived ook als het nog niet draaide.
		svc.State = "reloaded"
	}
	_, err = deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, n, "-vip-herladen", s.Retry),
		[]templates.Step{{Step: protocol.Step{Service: &svc}, Title: "service keepalived"}})
	return err
}

// waitOwnVIP wacht tot het eigen VIP op node want staat, en alleen daar.
func (s *Service) waitOwnVIP(ctx context.Context, st *jobs.Step, spec Spec, want string) error {
	deadline := s.Now().Add(s.VIPTimeout)
	since := s.Now()
	last := ""
	for {
		holders, err := s.vipHolder(ctx, spec, since)
		if err != nil {
			return err
		}
		var names []string
		for _, h := range holders {
			names = append(names, h.Hostname)
		}
		state := "het VIP " + spec.VIP.Address + " staat op geen enkele node"
		if len(names) > 0 {
			state = fmt.Sprintf("het VIP %s staat op %s", spec.VIP.Address, joinHosts(names))
		}
		if state != last {
			st.Logf("%s", state)
			last = state
		}
		switch {
		case len(names) == 1 && names[0] == want:
			return nil
		case len(names) > 1:
			return fmt.Errorf("het VIP %s staat op %s tegelijk (split-brain); kijk of keepalived op die nodes elkaar hoort", spec.VIP.Address, joinHosts(names))
		}
		if s.Now().After(deadline) {
			return fmt.Errorf("het VIP %s staat na %s niet op %s, de primary; kijk op de nodes met journalctl -u keepalived en cf-agent check db postgresql",
				spec.VIP.Address, s.VIPTimeout, want)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.Poll):
		}
	}
}
