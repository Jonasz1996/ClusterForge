package components

// Een node toevoegen aan een database die draait: een nieuwe node van het
// cluster, ook een CT die Jonas zelf maakte, krijgt het onderdeel en sluit
// aan. Het is een nieuwe revisie met de node erbij, die de gewone taak
// toepast; de andere nodes herstarten daarbij niet.
//
// Bij Galera krijgt de nieuwe node de configuratie met alle nodes en haalt
// hij alle data bij een node die meedoet (SST). De andere nodes krijgen hem
// in wsrep_cluster_address, maar MariaDB herstart daar niet: dat adres
// gebruikt een node alleen bij het starten, en een node die draait, hoort
// de nieuwe via Galera zelf.
//
// Bij Patroni voegt ClusterForge de nieuwe node eerst als lid toe aan etcd,
// op een node die al meedoet, en start etcd daarna op de nieuwe node, die
// aansluit bij de leden die er zijn. Patroni op de andere nodes krijgt de
// nieuwe node in pg_hba en herlaadt; de nieuwe node wordt een replica en
// kopieert alle data van de primary.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// joinedParam is de parameter van postgresql-patroni met de nodes die later
// bij etcd kwamen.
const joinedParam = "etcd_joined"

// Managed zegt of ClusterForge de parameter zelf bijhoudt; die hoort niet
// in een formulier.
func Managed(param string) bool { return param == joinedParam }

// etcdctl praat met etcd op de node zelf.
const etcdctl = "/usr/local/bin/etcdctl --endpoints=http://127.0.0.1:2379"

// JoinCandidate is een node van het cluster die nog niet bij de database
// hoort.
type JoinCandidate struct {
	NodeID   uuid.UUID
	Hostname string
}

// JoinMember is een node die al bij de database hoort, met wat er bij het
// toevoegen op hem verandert.
type JoinMember struct {
	NodeID   uuid.UUID
	Hostname string
	Role     string
	State    string
	Healthy  bool
	Writer   bool
	// Changes zijn de stappen op deze node, in woorden; geen ervan
	// herstart de database.
	Changes []string
}

// JoinPlan zegt wat het toevoegen van een node doet. Er verandert niets.
type JoinPlan struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Prod        bool
	Engine      string
	Title       string
	Revision    int
	Candidates  []JoinCandidate
	// Node is de nieuwe node; nil zolang er geen gekozen is.
	Node    *PlanNode
	Members []JoinMember
	// VIP is het VIP van de database, en VIPJoins of de nieuwe node het in
	// keepalived krijgt.
	VIP      string
	VIPJoins bool
	// Votes is het aantal stemmen met de nieuwe node erbij.
	Votes    int
	Effects  []string
	Findings []Finding
	Blocked  bool
	Hash     string

	spec Spec
	// etcdVia is de node waarop ClusterForge de nieuwe node lid van etcd
	// maakt, voor het plan.
	etcdVia string
}

// JoinPlan bekijkt wat het toevoegen van nodeID aan de database doet. Zonder
// nodeID geeft het alleen de nodes die erbij kunnen.
func (s *Service) JoinPlan(ctx context.Context, clusterID, id uuid.UUID, nodeID *uuid.UUID) (JoinPlan, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return JoinPlan{}, err
	}
	comp, err := s.dbComponent(ctx, clusterID, id)
	if err != nil {
		return JoinPlan{}, err
	}
	spec, err := parseSpec(comp.Spec)
	if err != nil {
		return JoinPlan{}, err
	}
	tpl, ok := registry.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return JoinPlan{}, fmt.Errorf("%s %s zit niet in deze server", spec.Template.Name, spec.Template.Version)
	}
	e := engines[comp.Engine]
	p := JoinPlan{
		ClusterID: clusterID, ClusterName: c.Name, Slug: c.Slug, Prod: c.Environment == store.EnvironmentProd, Engine: comp.Engine, Title: tpl.Title,
		Revision: int(comp.Revision), Candidates: []JoinCandidate{},
	}
	add := func(level, code string, n *Node, msg, command string) {
		f := Finding{Level: level, Code: code, Message: msg, Command: command}
		if n != nil {
			f.NodeID, f.Hostname = &n.NodeID, n.Hostname
		}
		p.Findings = append(p.Findings, f)
	}
	finish := func() (JoinPlan, error) {
		p.Blocked = slices.ContainsFunc(p.Findings, func(f Finding) bool { return f.Level == Blocked })
		p.Hash = joinHash(comp, p)
		return p, nil
	}

	cands, err := s.q.ListComponentCandidates(ctx, &clusterID)
	if err != nil {
		return JoinPlan{}, err
	}
	byID := map[uuid.UUID]store.ListComponentCandidatesRow{}
	states := map[uuid.UUID]deploy.NodeState{}
	for _, r := range cands {
		byID[r.ID], states[r.ID] = r, nodeState(r)
	}
	inSpec := func(id uuid.UUID) bool {
		return slices.ContainsFunc(spec.Nodes, func(n Node) bool { return n.NodeID == id })
	}
	for _, r := range slices.SortedFunc(maps.Values(byID), func(a, b store.ListComponentCandidatesRow) int { return strings.Compare(a.Hostname, b.Hostname) }) {
		if !inSpec(r.ID) && r.Lifecycle != store.NodeLifecycleDecommissioned {
			p.Candidates = append(p.Candidates, JoinCandidate{NodeID: r.ID, Hostname: r.Hostname})
		}
	}
	if int(*comp.AppliedRevision) != p.Revision {
		add(Blocked, "pending_revision", nil, fmt.Sprintf("revisie %d is nog niet toegepast; kies eerst Opnieuw toepassen", p.Revision), "")
	}
	if nodeID == nil {
		if len(p.Candidates) == 0 {
			add(Blocked, "no_candidates", nil, fmt.Sprintf("%s heeft geen node zonder de database. Maak eerst de node, bijvoorbeeld een CT, en installeer de agent erop; "+
				"daarna staat hij hier", c.Name), "")
		} else {
			add(Blocked, "choose", nil, "kies de node die bij de database komt", "")
		}
		return finish()
	}
	r, ok := byID[*nodeID]
	switch {
	case !ok:
		return JoinPlan{}, &InvalidError{"node_id", "die node hoort niet bij dit cluster"}
	case inSpec(r.ID):
		return JoinPlan{}, &InvalidError{"node_id", r.Hostname + " hoort al bij de database"}
	}

	// De nieuwe spec: de node erbij als laatste datanode.
	ns := spec
	ns.Nodes = slices.Clone(spec.Nodes)
	ns.Params = maps.Clone(spec.Params)
	if spec.VIP != nil {
		v := *spec.VIP
		v.Nodes = slices.Clone(v.Nodes)
		ns.VIP = &v
	}
	index := 0
	for _, n := range spec.Nodes {
		if n.Role == RoleData {
			index = max(index, n.Index)
		}
	}
	n := Node{NodeID: r.ID, Hostname: r.Hostname, Role: RoleData, Index: index + 1}
	ns.Nodes = append(ns.Nodes, n)
	if comp.Engine == protocol.DBPostgreSQL {
		joined, _ := ns.Params[joinedParam].(string)
		ns.Params[joinedParam] = strings.TrimSpace(joined + " " + n.Hostname)
	}
	data := ns.count(RoleData)
	p.Votes = len(ns.Nodes)
	arbiters := spec.hosts(RoleArbiter)
	switch {
	case data > maxData:
		add(Blocked, "too_many", nil, fmt.Sprintf("hoogstens %d nodes: elke schrijfactie wacht op alle nodes", maxData), "")
	case p.Votes%2 == 0 && len(arbiters) > 0:
		add(Warning, "even", nil, fmt.Sprintf("met %s erbij zijn er %d stemmen, de scheidsrechter %s meegerekend: een even aantal. Valt het netwerk in twee gelijke helften, "+
			"dan heeft geen van beide een meerderheid en stopt de database. Met %d datanodes is de scheidsrechter niet meer nodig; ClusterForge kan hem nog niet weghalen",
			n.Hostname, p.Votes, joinHosts(arbiters), data), "")
	case p.Votes%2 == 0:
		add(Warning, "even", nil, fmt.Sprintf("met %s erbij zijn er %d nodes, een even aantal. Eén node mag uitvallen, net als nu, maar valt het netwerk in twee "+
			"gelijke helften, dan heeft geen van beide een meerderheid en stopt de database. Een node meer of een scheidsrechter voorkomt dat", n.Hostname, p.Votes), "")
	}

	// De nodes die er al zijn, moeten allemaal meedoen: de nieuwe node haalt
	// zijn data bij een van hen, en bij PostgreSQL telt etcd een lid meer
	// tot de nieuwe meedoet.
	nodes, err := s.rolling(ctx, comp, spec)
	if err != nil {
		return JoinPlan{}, err
	}
	rollingFindings(nodes, comp.Engine, "een node toevoegt", add)

	// De nieuwe node.
	status := s.statuses(ctx, Spec{Nodes: []Node{n}}, byID, comp.Engine)
	pool, _ := templates.ParseSize(fmt.Sprint(spec.Params[e.memParam]))
	pn, osVersion, ok := s.checkNode(&n, r, states[n.NodeID], status[n.NodeID], comp.Engine, c.Slug, tpl.Title, int64(pool), s.memory(ctx), add)
	if comp.Engine == protocol.DBPostgreSQL && r.HasAgent && r.AgentProtocol >= protocol.DBSince && r.AgentProtocol < protocol.DBPatroniSince {
		add(Blocked, "agent_old", &n, fmt.Sprintf("de agent op %s heeft protocol %d; een node van PostgreSQL vraagt protocol %d. Werk hem bij met het installatiescript en --upgrade",
			n.Hostname, r.AgentProtocol, protocol.DBPatroniSince), "")
	}
	if ok && osVersion != "" {
		for _, m := range nodes {
			if m.node.Role != RoleData || m.facts == nil || m.facts.OS.VersionID == osVersion {
				continue
			}
			add(Blocked, "os_mixed", &n, fmt.Sprintf("%s draait Debian %s en %s Debian %s; alle nodes van een database hebben dezelfde Debian, want elke Debian heeft een andere %s",
				n.Hostname, osVersion, m.node.Hostname, m.facts.OS.VersionID, e.server), "")
			break
		}
	}
	if ok {
		steps, err := deploy.RenderMasked(tpl, ns.deploySpec(), states, n.NodeID)
		if err != nil {
			add(Blocked, "render", &n, n.Hostname+": "+err.Error(), "")
		}
		for _, st := range steps {
			pn.Steps = append(pn.Steps, st.Title)
		}
	}

	// Wat er op de andere nodes verandert. Lid van etcd wordt de nieuwe node
	// via de eerste datanode die meedoet, zoals de taak dat doet.
	if comp.Engine == protocol.DBPostgreSQL {
		for _, m := range spec.order() {
			i := slices.IndexFunc(nodes, func(x rollNode) bool { return x.node.NodeID == m.NodeID })
			if m.Role == RoleData && i >= 0 && nodes[i].healthy {
				p.etcdVia = m.Hostname
				break
			}
		}
	}
	for _, m := range nodes {
		jm := JoinMember{NodeID: m.node.NodeID, Hostname: m.node.Hostname, Role: m.node.Role, State: m.state, Healthy: m.healthy, Writer: m.writer, Changes: []string{}}
		if m.node.Hostname == p.etcdVia {
			jm.Changes = append(jm.Changes, "etcd: "+n.Hostname+" als lid toevoegen")
		}
		if ok {
			changed, err := changedFiles(tpl, spec, ns, states, m.node.NodeID)
			if err != nil {
				add(Blocked, "render", &m.node, m.node.Hostname+": "+err.Error(), "")
			}
			jm.Changes = append(jm.Changes, changed...)
		}
		p.Members = append(p.Members, jm)
	}

	// Het VIP.
	if ns.VIP != nil {
		p.VIP = ns.VIP.Address
		if ok {
			s.planJoinVIP(ctx, &p, &ns, &pn, n, r, states[n.NodeID].Facts, add)
		}
	}
	p.Node, p.spec = &pn, ns
	p.Effects = joinEffects(p, n, nodes)
	return finish()
}

// changedFiles zijn de bestanden van node die met de nieuwe spec anders
// worden, als stappen in woorden.
func changedFiles(tpl *templates.Template, old, upd Spec, states map[uuid.UUID]deploy.NodeState, node uuid.UUID) ([]string, error) {
	before, err := deploy.RenderMasked(tpl, old.deploySpec(), states, node)
	if err != nil {
		return nil, err
	}
	after, err := deploy.RenderMasked(tpl, upd.deploySpec(), states, node)
	if err != nil {
		return nil, err
	}
	was := map[string]string{}
	for _, st := range before {
		if st.File != nil {
			was[st.File.Path] = st.File.Content
		}
	}
	var out []string
	for _, st := range after {
		if st.File != nil && was[st.File.Path] != st.File.Content {
			out = append(out, st.Title)
		}
	}
	return out, nil
}

// planJoinVIP zet de nieuwe node bij het VIP als dat kan. Een eigen VIP
// komt op elke datanode; het VIP van de app alleen als keepalived op de
// nieuwe node het al heeft.
func (s *Service) planJoinVIP(ctx context.Context, p *JoinPlan, ns *Spec, pn *PlanNode, n Node, r store.ListComponentCandidatesRow, f *protocol.Facts,
	add func(string, string, *Node, string, string)) {
	addr := ns.VIP.Address
	fresh := r.HasAgent && r.AgentProtocol >= protocol.DBSince && r.HeartbeatAt != nil && s.Now().Sub(*r.HeartbeatAt) <= s.Fresh
	if ns.VIP.Own {
		ns.VIP.Nodes = append(ns.VIP.Nodes, n.NodeID)
		p.VIPJoins = true
		if _, pf, ok := nodePrefix(f, cmp.Or(pn.Address, f.PrimaryAddress)); ok {
			if a, err := netip.ParseAddr(addr); err == nil && !pf.Contains(a) {
				add(Blocked, "vip_subnet", &n, fmt.Sprintf("%s ligt niet in het netwerk van %s (%s); het VIP moet in hetzelfde netwerk als de nodes", addr, n.Hostname, pf.Masked()), "")
			}
		}
		if f.Keepalived != nil && f.Keepalived.Active != "active" {
			add(Warning, "keepalived_down", &n, fmt.Sprintf("keepalived draait niet op %s (%s); ClusterForge start het, en dan komen ook de andere VIP's "+
				"in keepalived.conf daar weer op", n.Hostname, cmp.Or(f.Keepalived.Active, "onbekend")), "")
		}
		include := false
		if fresh {
			res, err := s.include(ctx, n.NodeID, addr, false)
			if err != nil {
				add(Blocked, "vip_conf", &n, fmt.Sprintf("%s: keepalived.conf: %v", n.Hostname, err), "")
			} else {
				include, pn.VIPInstance, pn.VIPDiff = res.Changed, ownInstance(p.Slug, p.Engine), res.Diff
			}
		}
		pn.Steps = append(pn.Steps, ownVIPTitles(p.Engine, include)...)
		file, _, _ := ownVIP(p.Engine)
		for i := range p.Members {
			m := &p.Members[i]
			if slices.Contains(ns.VIP.Nodes, m.NodeID) {
				m.Changes = append(m.Changes, fmt.Sprintf("bestand %s: %s als peer", file, n.Hostname), "keepalived herladen")
			}
		}
		return
	}
	inst, has := hasInstance(f, addr)
	if !has {
		add(Warning, "vip_missing", &n, fmt.Sprintf("%s heeft het VIP %s niet in keepalived, dus het VIP komt niet op %s. De database werkt er wel en telt mee "+
			"voor de meerderheid", n.Hostname, addr, n.Hostname), "")
		return
	}
	ns.VIP.Nodes = append(ns.VIP.Nodes, n.NodeID)
	p.VIPJoins = true
	pn.VIPInstance = inst
	if f.Keepalived.Active != "active" {
		add(Blocked, "keepalived_down", &n, fmt.Sprintf("keepalived draait niet op %s (%s)", n.Hostname, cmp.Or(f.Keepalived.Active, "onbekend")), "")
	}
	if fresh {
		res, err := s.track(ctx, n.NodeID, addr, false)
		if err != nil {
			add(Blocked, "vip_conf", &n, fmt.Sprintf("%s: keepalived.conf niet te lezen: %v", n.Hostname, err), "")
		} else {
			pn.VIPInstance, pn.VIPDiff = res.Instance, res.Diff
		}
	}
	pn.Steps = append(pn.Steps, vipTitles(p.Slug, addr, pn.VIPInstance)...)
	// Zolang de controle er niet staat, kan het VIP op de nieuwe node
	// belanden terwijl daar nog geen database is.
	add(Info, "vip_join", &n, fmt.Sprintf("%s heeft het VIP %s al in keepalived (%s); ClusterForge zet de controle cf_db erbij zodra MariaDB daar Synced is",
		n.Hostname, addr, inst), "")
}

// joinEffects zegt in gewone woorden wat er gebeurt.
func joinEffects(p JoinPlan, n Node, nodes []rollNode) []string {
	var others []string
	for _, m := range nodes {
		if m.node.Role == RoleData {
			others = append(others, m.node.Hostname)
		}
	}
	var out []string
	if p.Engine == protocol.DBPostgreSQL {
		via := cmp.Or(p.etcdVia, "een node die meedoet")
		out = append(out,
			fmt.Sprintf("ClusterForge maakt %s lid van etcd via %s en start etcd op %s. Tot etcd daar draait, moeten de andere leden van etcd er allemaal zijn.", n.Hostname, via, n.Hostname),
			fmt.Sprintf("Patroni op %s krijgt %s in pg_hba en herlaadt; PostgreSQL herstart daar niet.", joinHosts(others), n.Hostname),
			fmt.Sprintf("%s wordt een replica en kopieert alle data van de primary. De primary blijft schrijven; het kopiëren kost hem wat schijf en netwerk.", n.Hostname))
	} else {
		out = append(out,
			fmt.Sprintf("%s krijgt MariaDB en haalt alle data bij een node die meedoet (SST met mariabackup). Die node blijft werken, maar is tijdens het kopiëren trager.", n.Hostname),
			fmt.Sprintf("%s krijgen %s in hun configuratie voor de volgende start; MariaDB herstart daar niet.", joinHosts(others), n.Hostname))
	}
	if p.VIP != "" {
		switch {
		case !p.VIPJoins:
			out = append(out, fmt.Sprintf("Het VIP %s blijft op de nodes waar het nu kan staan.", p.VIP))
		case p.spec.VIP != nil && p.spec.VIP.Own:
			out = append(out, fmt.Sprintf("keepalived op %s krijgt het VIP %s; op de andere nodes komt %s erbij als peer en herlaadt keepalived, de node met het VIP als laatste.",
				n.Hostname, p.VIP, n.Hostname))
		default:
			out = append(out, fmt.Sprintf("keepalived op %s krijgt de controle cf_db bij het VIP %s, zodat het VIP daar alleen staat als MariaDB er mag schrijven.", n.Hostname, p.VIP))
		}
	}
	return append(out, fmt.Sprintf("Daarna heeft de database %d stemmen; ClusterForge controleert dat elke node meedoet.", p.Votes))
}

// joinHash vat het plan samen: de revisie, de nieuwe node met zijn adres,
// wat er op elke node verandert, het VIP en de bevindingen, zonder
// metingen.
func joinHash(comp store.ClusterComponent, p JoinPlan) string {
	type finding struct {
		Level, Code string
		Node        *uuid.UUID
	}
	v := struct {
		Component uuid.UUID    `json:"component"`
		Revision  int          `json:"revision"`
		Node      *uuid.UUID   `json:"node"`
		Address   string       `json:"address"`
		Index     int          `json:"index"`
		Members   []JoinMember `json:"members"`
		VIPNodes  []uuid.UUID  `json:"vip_nodes"`
		VIPDiff   string       `json:"vip_diff"`
		Findings  []finding    `json:"findings"`
	}{Component: comp.ID, Revision: p.Revision}
	if p.Node != nil {
		v.Node, v.Address, v.Index, v.VIPDiff = &p.Node.NodeID, p.Node.Address, p.Node.Index, p.Node.VIPDiff
	}
	for _, m := range p.Members {
		m.State, m.Healthy, m.Writer = "", false, false
		v.Members = append(v.Members, m)
	}
	if p.spec.VIP != nil {
		v.VIPNodes = p.spec.VIP.Nodes
	}
	for _, f := range p.Findings {
		v.Findings = append(v.Findings, finding{f.Level, f.Code, f.NodeID})
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Join voegt de node toe aan de database: een nieuwe revisie met de node
// erbij, en de taak die haar toepast, als het plan nog is wat de gebruiker
// zag en niets het tegenhoudt.
func (s *Service) Join(ctx context.Context, actor events.Actor, clusterID, id, nodeID uuid.UUID, hash string) (store.Job, error) {
	p, err := s.JoinPlan(ctx, clusterID, id, &nodeID)
	if err != nil {
		return store.Job{}, err
	}
	if p.Hash != hash {
		return store.Job{}, ErrStale
	}
	for _, f := range p.Findings {
		if f.Level == Blocked {
			return store.Job{}, &ConflictError{f.Message}
		}
	}
	b, err := json.Marshal(p.spec)
	if err != nil {
		return store.Job{}, err
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		comp, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		if int(comp.Revision) != p.Revision {
			return ErrStale
		}
		rev := comp.Revision + 1
		var by *uuid.UUID
		if uid, err := uuid.Parse(actor.ID); err == nil {
			by = &uid
		}
		if err := q.InsertClusterComponentRevision(ctx, store.InsertClusterComponentRevisionParams{ComponentID: comp.ID, Revision: rev, Spec: b, CreatedBy: by}); err != nil {
			return err
		}
		if err := q.SetClusterComponentSpec(ctx, store.SetClusterComponentSpecParams{ID: comp.ID, Spec: b, Revision: rev}); err != nil {
			return err
		}
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: Kind, Title: fmt.Sprintf("Node toevoegen: %s aan %s op %s", p.Node.Hostname, p.Title, p.ClusterName),
			Params: jobParams{ComponentID: comp.ID, Revision: int(rev), Join: []uuid.UUID{nodeID}}, ClusterID: &clusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		if err := q.SetClusterComponentJob(ctx, store.SetClusterComponentJobParams{ID: comp.ID, JobID: &j.ID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_node_added",
			Payload: map[string]any{
				"name": p.ClusterName, "component": p.Title, "node": p.Node.Hostname, "revision": rev, "nodes": p.spec.hosts(RoleData),
				"vip": p.VIPJoins, "job_id": j.ID, "title": j.Title,
			},
		})
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.jobs.Kick()
	}
	return j, err
}

// joining zegt of de taak een node toevoegt, en n een node is die er al
// was: die herstart dan niet.
func (r *rendered) joining(n Node) bool {
	return len(r.join) > 0 && !slices.Contains(r.join, n.NodeID)
}

// quiet haalt het herladen en herstarten uit de stappen van een node die
// er al was: een nieuwe node in de configuratie geldt bij de volgende
// start.
func quiet(steps []templates.Step) []templates.Step {
	out := slices.Clone(steps)
	for i := range out {
		out[i].Notify = nil
	}
	return out
}

// etcdJoin maakt n lid van etcd, via de eerste node die er al was en het
// kan. Is hij al lid (een hervatte taak), dan verandert er niets.
func (s *Service) etcdJoin(ctx context.Context, j *jobs.Job, st *jobs.Step, r *rendered, n Node) error {
	c, err := r.spec.deploySpec().Context(r.tpl, r.secrets, r.states)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(r.spec.Nodes, func(x Node) bool { return x.NodeID == n.NodeID })
	if i < 0 {
		return fmt.Errorf("%s hoort niet bij de database", n.Hostname)
	}
	step := etcdJoinStep(n.Hostname, "http://"+c.Nodes[i].Address+":2380")
	var errs []string
	for _, m := range r.spec.order() {
		if m.Role != RoleData || slices.Contains(r.join, m.NodeID) {
			continue
		}
		changed, err := deploy.ApplySteps(ctx, s.bus, options(ctx, j, st, m, "-etcd-lid", s.Retry), []templates.Step{step})
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			errs = append(errs, m.Hostname+": "+err.Error())
			st.Logf("%s kon %s niet als lid van etcd toevoegen: %v", m.Hostname, n.Hostname, err)
			continue
		}
		if changed > 0 {
			st.Logf("%s is lid van etcd, toegevoegd via %s; etcd telt nu een lid meer tot het op %s draait", n.Hostname, m.Hostname, n.Hostname)
		} else {
			st.Logf("%s was al lid van etcd", n.Hostname)
		}
		return nil
	}
	return fmt.Errorf("geen node kon %s als lid van etcd toevoegen: %s", n.Hostname, strings.Join(errs, "; "))
}

// etcdJoinStep maakt de node hostname met peer-adres peer lid van etcd, op
// een node die al lid is. Is hij het al, ook als lid dat nog niet gestart
// is (dan heeft het nog geen naam), dan verandert er niets.
func etcdJoinStep(hostname, peer string) templates.Step {
	return templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run:    fmt.Sprintf("%s member add %s --peer-urls=%s", etcdctl, hostname, peer),
		Unless: fmt.Sprintf("%s member list | grep -qF '%s'", etcdctl, peer),
	}}, Title: "etcd: " + hostname + " als lid toevoegen"}
}

// etcdWait wacht tot etcd op de nieuwe node meedoet.
func etcdWait() templates.Step {
	health := etcdctl + " endpoint health >/dev/null 2>&1"
	return templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: fmt.Sprintf("for i in $(seq 1 90); do %s && exit 0; sleep 2; done; "+
			"echo 'etcd doet hier na drie minuten nog niet mee; kijk met journalctl -u etcd' >&2; exit 1", health),
		Unless: health,
	}}, Title: "wachten tot etcd hier meedoet"}
}
