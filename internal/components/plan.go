package components

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Request is wat de gebruiker kiest bij 'Database toevoegen'.
type Request struct {
	Engine string
	// Nodes zijn de datanodes, op volgorde: de eerste begint het cluster.
	Nodes []uuid.UUID
	// Arbiter is een node die alleen meestemt; nodig bij twee datanodes.
	Arbiter *uuid.UUID
	// Settings zijn de parameters van het onderdeel zonder geheimen, zoals
	// buffer_pool en max_connections; wat ontbreekt, krijgt de standaard.
	Settings map[string]any
	// VIP is het VIP van de app dat de database volgt; nil voor geen.
	VIP *uuid.UUID
	// VIPAddress is een eigen VIP voor de database, met een eigen
	// vrrp_instance die ClusterForge maakt; leeg voor geen. PostgreSQL heeft
	// er een nodig, MariaDB kan ook het VIP van de app volgen.
	VIPAddress string
}

// Niveaus van een bevinding in het plan.
const (
	// Blocked: zo kan het niet; de knop om uit te rollen blijft uit.
	Blocked = "blocked"
	// Warning: het kan, maar Jonas moet het weten.
	Warning = "warning"
	// Info: goed om te weten, zonder dat er iets moet.
	Info = "info"
)

// Finding is een bevinding van het plan, eventueel bij een node, met een
// commando dat het oplost.
type Finding struct {
	Level    string
	Code     string
	NodeID   *uuid.UUID
	Hostname string
	Message  string
	Command  string
}

// PlanNode is wat het plan over een node zegt.
type PlanNode struct {
	NodeID   uuid.UUID
	Hostname string
	Role     string
	Index    int
	Address  string
	// OS is zoals Debian 13 (trixie).
	OS string
	// PveType is lxc of qemu, PveNode de Proxmox-host; leeg als
	// ClusterForge de node niet in Proxmox kent.
	PveType string
	PveNode string
	VMID    int
	// MemoryBytes is het geheugen van de node, MemoryUsedPct wat er zeven
	// dagen lang hoogstens gebruikt werd (p95), NeedBytes wat de database
	// erbij vraagt en FreeAfterPct wat er daarna overblijft. Zonder
	// metingen zijn de percentages nil.
	MemoryBytes   int64
	MemoryUsedPct *float64
	NeedBytes     int64
	FreeAfterPct  *float64
	// DiskFreeBytes is de vrije ruimte op het bestandssysteem van
	// /var/lib/mysql (DiskMount); nil zonder facts.
	DiskFreeBytes *int64
	DiskMount     string
	// Existing zegt wat er al aan MariaDB op de node draait.
	Existing string
	// Steps zijn de stappen op deze node, in woorden.
	Steps []string
	// VIPInstance is de vrrp_instance met het VIP, VIPDiff wat
	// ClusterForge in keepalived.conf verandert (zonder auth_pass); leeg
	// als het VIP niet op deze node staat of er al bij hoort.
	VIPInstance string
	VIPDiff     string
}

// PlanVIP is het VIP dat de database volgt, met de node die het nu heeft.
// Own is een eigen VIP dat ClusterForge maakt, met VRRP-id VRID op de
// netwerkkaart Interface van de eerste datanode; zijn ID is dan nog leeg.
type PlanVIP struct {
	ID        uuid.UUID
	Address   string
	Owner     string
	Own       bool
	VRID      int
	Interface string
}

// Plan is wat 'Database toevoegen' zou doen.
type Plan struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Prod        bool
	Engine      string
	Template    string
	Version     string
	Title       string
	// Params zijn de instellingen met de standaarden ingevuld; een geheim
	// staat er als plaatshouder.
	Params   map[string]any
	Nodes    []PlanNode
	VIP      *PlanVIP
	Findings []Finding
	Blocked  bool
	// Hash vat het plan samen; uitrollen kan alleen met dezelfde hash.
	Hash string
}

// Galera-poorten: MySQL, groepscommunicatie, IST en SST.
var galeraPorts = []int{3306, 4567, 4568, 4444}

// engine is wat het plan per engine anders doet.
type engine struct {
	// cluster is hoe het plan het cluster noemt, en server de database.
	cluster, server string
	// ports zijn de poorten van een datanode en van de scheidsrechter, en
	// units de units die ze mogen hebben (ook als unit@instantie).
	ports, arbiterPorts []int
	units, arbiterUnits []string
	// dataDir is waar de data staat, voor de vrije ruimte.
	dataDir string
	// memParam is de instelling met het meeste geheugen, overhead wat de
	// database er ongeveer bij gebruikt, en smaller wat het plan aanraadt
	// als het niet past.
	memParam string
	overhead int64
	smaller  string
}

var engines = map[string]engine{
	protocol.DBMariaDB: {
		cluster: "een Galera-cluster", server: "MariaDB", ports: galeraPorts, arbiterPorts: []int{4567}, units: []string{"mariadb"}, arbiterUnits: []string{"garb"},
		dataDir: "/var/lib/mysql", memParam: "buffer_pool", overhead: 300 << 20, smaller: "kies een kleinere buffer pool",
	},
	// Patroni (8008) en etcd (2379 voor Patroni, 2380 tussen de leden)
	// draaien op elke datanode; de scheidsrechter heeft alleen etcd. Een
	// leeg cluster main van Debian op 5432 haalt het onderdeel weg.
	protocol.DBPostgreSQL: {
		cluster: "een PostgreSQL-cluster met Patroni", server: "PostgreSQL", ports: []int{5432, 8008, 2379, 2380}, arbiterPorts: []int{2379, 2380},
		units: []string{"patroni", "etcd", "postgresql"}, arbiterUnits: []string{"etcd"},
		dataDir: "/var/lib/postgresql", memParam: "shared_buffers", overhead: 400 << 20, smaller: "kies kleinere shared buffers",
	},
}

// ours zegt of unit een van units is, of een instantie ervan.
func ours(unit string, units []string) bool {
	return slices.ContainsFunc(units, func(u string) bool { return unit == u || strings.HasPrefix(unit, u+"@") })
}

// maxData is het hoogste aantal datanodes: elke node heeft alle data, en
// elke schrijfactie wacht op allemaal.
const maxData = 9

// Plan bekijkt wat 'Database toevoegen' zou doen. Er verandert niets.
func (s *Service) Plan(ctx context.Context, clusterID uuid.UUID, req Request) (Plan, error) {
	p, _, err := s.plan(ctx, clusterID, req)
	return p, err
}

// plan is Plan met de spec erbij, zonder geheimen.
func (s *Service) plan(ctx context.Context, clusterID uuid.UUID, req Request) (Plan, Spec, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return Plan{}, Spec{}, err
	}
	name, ok := Templates[req.Engine]
	if !ok {
		return Plan{}, Spec{}, &InvalidError{"engine", "kies mariadb of postgresql"}
	}
	e := engines[req.Engine]
	tpl, _ := registry.Latest(name)
	settings, err := requestSettings(tpl, req)
	if err != nil {
		return Plan{}, Spec{}, err
	}
	params, err := tpl.MaskedValues(settings)
	if err != nil {
		var fe templates.FieldError
		if errors.As(err, &fe) {
			return Plan{}, Spec{}, &InvalidError{fieldOf(fe.Field), fe.Msg}
		}
		return Plan{}, Spec{}, err
	}
	cands, err := s.q.ListComponentCandidates(ctx, &clusterID)
	if err != nil {
		return Plan{}, Spec{}, err
	}
	byID := map[uuid.UUID]store.ListComponentCandidatesRow{}
	states := map[uuid.UUID]deploy.NodeState{}
	hostnames := map[uuid.UUID]string{}
	for _, r := range cands {
		byID[r.ID], states[r.ID], hostnames[r.ID] = r, nodeState(r), r.Hostname
	}
	spec, err := specOf(c, tpl, req, params, hostnames)
	if err != nil {
		return Plan{}, Spec{}, err
	}
	for _, n := range spec.Nodes {
		if _, ok := byID[n.NodeID]; !ok {
			return Plan{}, Spec{}, &InvalidError{"nodes", "die node hoort niet bij dit cluster"}
		}
	}
	p := Plan{
		ClusterID: c.ID, ClusterName: c.Name, Slug: c.Slug, Prod: c.Environment == store.EnvironmentProd, Engine: req.Engine,
		Template: tpl.Name, Version: tpl.Version, Title: tpl.Title, Params: params,
	}
	add := func(level, code string, n *Node, msg, command string) {
		f := Finding{Level: level, Code: code, Message: msg, Command: command}
		if n != nil {
			f.NodeID, f.Hostname = &n.NodeID, n.Hostname
		}
		p.Findings = append(p.Findings, f)
	}

	// Het cluster.
	existing, err := s.q.ListClusterComponents(ctx, clusterID)
	if err != nil {
		return Plan{}, Spec{}, err
	}
	if slices.ContainsFunc(existing, func(r store.ListClusterComponentsRow) bool { return r.ClusterComponent.Engine == req.Engine }) {
		add(Blocked, "exists", nil, fmt.Sprintf("%s heeft al een %s; een cluster heeft er hoogstens één", c.Name, tpl.Title), "")
	}
	if len(tpl.Secrets()) > 0 && !s.dep.HasMasterKey() {
		add(Blocked, "no_master_key", nil, "de server heeft geen masterkey (CF_MASTER_KEY), dus hij kan de wachtwoorden van de database niet bewaren", "")
	}
	data, votes := spec.count(RoleData), len(spec.Nodes)
	switch {
	case data < 2:
		add(Blocked, "too_few", nil, e.cluster+" heeft minstens twee nodes; met één node is er niets om op terug te vallen", "")
	case data == 2 && req.Arbiter == nil:
		add(Blocked, "two_without_arbiter", nil, "met twee nodes heeft geen van beide een meerderheid als de andere wegvalt, en dan stopt de database. "+
			"Kies een derde node of een scheidsrechter", "")
	case data > maxData:
		add(Blocked, "too_many", nil, fmt.Sprintf("hoogstens %d nodes: elke schrijfactie wacht op alle nodes", maxData), "")
	case votes%2 == 0:
		add(Warning, "even", nil, fmt.Sprintf("%d stemmen is een even aantal: valt het netwerk in twee gelijke helften, dan heeft geen van beide een meerderheid en "+
			"stopt de database. Een oneven aantal of een scheidsrechter voorkomt dat", votes), "")
	}

	// De nodes.
	usage := s.memory(ctx)
	status := s.statuses(ctx, spec, byID, req.Engine)
	pool, _ := templates.ParseSize(fmt.Sprint(params[e.memParam]))
	osVersions := map[string][]string{}
	for i := range spec.Nodes {
		n := &spec.Nodes[i]
		pn, osVersion, ok := s.checkNode(n, byID[n.NodeID], states[n.NodeID], status[n.NodeID], req.Engine, c.Slug, tpl.Title, int64(pool), usage, add)
		if !ok {
			p.Nodes = append(p.Nodes, pn)
			continue
		}
		if osVersion != "" {
			osVersions[osVersion] = append(osVersions[osVersion], n.Hostname)
		}
		steps, err := deploy.RenderMasked(tpl, spec.deploySpec(), states, n.NodeID)
		if err != nil {
			add(Blocked, "render", n, n.Hostname+": "+err.Error(), "")
		}
		if req.Engine == protocol.DBMariaDB && n.Role == RoleData && n.Index == 1 && err == nil && !primaryIn(status, c.Slug) {
			// Zo toont het plan dat de eerste node het cluster begint.
			if b, err := bootstrap(steps, c.Slug, false); err == nil {
				steps = b
			}
		}
		for _, st := range steps {
			pn.Steps = append(pn.Steps, st.Title)
		}
		p.Nodes = append(p.Nodes, pn)
	}
	if len(osVersions) > 1 {
		var parts []string
		for _, v := range slices.Sorted(maps.Keys(osVersions)) {
			parts = append(parts, fmt.Sprintf("Debian %s op %s", v, joinHosts(osVersions[v])))
		}
		add(Blocked, "os_mixed", nil, "alle nodes moeten dezelfde Debian hebben, want elke Debian heeft een andere "+e.server+": "+strings.Join(parts, ", "), "")
	}
	checkHosts(spec, byID, add)
	switch {
	case req.VIPAddress != "":
		err = s.planOwnVIP(ctx, req, &p, &spec, byID, states, add)
	case req.Engine == protocol.DBPostgreSQL && req.VIP != nil:
		err = &InvalidError{"vip_id", "PostgreSQL krijgt een eigen VIP dat de primary volgt; vul vip_address in"}
	case req.Engine == protocol.DBPostgreSQL:
		add(Blocked, "own_vip_required", nil, "PostgreSQL heeft een eigen VIP nodig: alleen de primary schrijft, en het VIP brengt de app daar. "+
			"Kies een vrij adres in het netwerk van de nodes", "")
	default:
		err = s.planVIP(ctx, c, req, &p, &spec, byID, states, add)
	}
	if err != nil {
		return Plan{}, Spec{}, err
	}
	p.Blocked = slices.ContainsFunc(p.Findings, func(f Finding) bool { return f.Level == Blocked })
	p.Hash = planHash(p, spec)
	return p, spec, nil
}

// requestSettings zijn de instellingen van de gebruiker voor tpl, met het eigen
// VIP erbij als het onderdeel dat als parameter heeft. Een geheim of het
// VIP zelf mag de gebruiker niet als instelling geven.
func requestSettings(tpl *templates.Template, req Request) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range req.Settings {
		switch {
		case slices.Contains(tpl.Secrets(), k):
			return nil, &InvalidError{k, "dit wachtwoord maakt ClusterForge zelf"}
		case k == vipParam:
			return nil, &InvalidError{k, "het eigen VIP kies je bij vip_address"}
		case k == joinedParam:
			return nil, &InvalidError{k, "dit houdt ClusterForge zelf bij"}
		}
		out[k] = v
	}
	if req.VIPAddress != "" && slices.ContainsFunc(tpl.Params, func(p templates.Param) bool { return p.Name == vipParam }) {
		out[vipParam] = req.VIPAddress
	}
	return out, nil
}

// vipParam is de parameter met het eigen VIP, voor een onderdeel dat het
// in zijn configuratie nodig heeft, zoals pg_hba.conf van PostgreSQL.
const vipParam = "vip"

// fieldOf is het veld van de invoer bij een parameter.
func fieldOf(param string) string {
	if param == vipParam {
		return "vip_address"
	}
	return param
}

// specOf maakt de spec uit de keuze van de gebruiker. params zijn de
// instellingen zonder geheimen.
func specOf(c store.Cluster, tpl *templates.Template, req Request, params map[string]any, hostnames map[uuid.UUID]string) (Spec, error) {
	spec := Spec{
		Template: deploy.SpecTemplate{Name: tpl.Name, Version: tpl.Version},
		Cluster:  templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)},
		Params:   map[string]any{}, Secrets: tpl.Secrets(),
	}
	for k, v := range params {
		if !slices.Contains(spec.Secrets, k) {
			spec.Params[k] = v
		}
	}
	if len(req.Nodes) == 0 {
		return spec, &InvalidError{"nodes", "kies de nodes voor de database"}
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range req.Nodes {
		if seen[id] {
			return spec, &InvalidError{"nodes", "een node staat er twee keer in"}
		}
		seen[id] = true
	}
	if req.Arbiter != nil && seen[*req.Arbiter] {
		return spec, &InvalidError{"arbiter", "de scheidsrechter is een andere node dan de datanodes"}
	}
	for i, id := range req.Nodes {
		spec.Nodes = append(spec.Nodes, Node{NodeID: id, Hostname: hostnames[id], Role: RoleData, Index: i + 1})
	}
	if req.Arbiter != nil {
		spec.Nodes = append(spec.Nodes, Node{NodeID: *req.Arbiter, Hostname: hostnames[*req.Arbiter], Role: RoleArbiter, Index: 1})
	}
	return spec, nil
}

func (s *Service) hostnames(ctx context.Context, clusterID uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := s.q.ListComponentCandidates(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, r := range rows {
		out[r.ID] = r.Hostname
	}
	return out, nil
}

func nodeState(r store.ListComponentCandidatesRow) deploy.NodeState {
	st := deploy.NodeState{PrimaryIP: r.PrimaryIp}
	if len(r.Facts) > 0 {
		var f protocol.Facts
		if json.Unmarshal(r.Facts, &f) == nil {
			st.Facts = &f
		}
	}
	return st
}

// memory leest het geheugengebruik uit VictoriaMetrics; zonder metingen
// nil.
func (s *Service) memory(ctx context.Context) map[uuid.UUID]float64 {
	if s.Memory == nil {
		return nil
	}
	m, err := s.Memory(ctx)
	if err != nil {
		s.log.Debug("geheugengebruik voor het plan niet te lezen", "err", err)
		return nil
	}
	return m
}

// dbStatus is het antwoord van één node op de vraag naar zijn databases.
type dbStatus struct {
	server *protocol.DBServer
	err    error
}

// statuses vraagt de datanodes met een verse agent op protocol 9 wat er
// van de engine draait, tegelijk.
func (s *Service) statuses(ctx context.Context, spec Spec, byID map[uuid.UUID]store.ListComponentCandidatesRow, engine string) map[uuid.UUID]dbStatus {
	out := map[uuid.UUID]dbStatus{}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(8)
	for _, n := range spec.Nodes {
		r := byID[n.NodeID]
		if n.Role != RoleData || !r.HasAgent || r.AgentProtocol < protocol.DBSince || r.HeartbeatAt == nil || s.Now().Sub(*r.HeartbeatAt) > s.Fresh {
			continue
		}
		g.Go(func() error {
			srv, err := s.server(ctx, n.NodeID, engine)
			mu.Lock()
			out[n.NodeID] = dbStatus{srv, err}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return out
}

// primaryIn is true als een van de nodes al meedoet in een Primary
// component van het cluster slug.
func primaryIn(status map[uuid.UUID]dbStatus, slug string) bool {
	for _, st := range status {
		if g := st.server; g != nil && g.Running && g.Galera != nil && g.Galera.ClusterName == slug && g.Galera.ClusterStatus == "Primary" {
			return true
		}
	}
	return false
}

// mariadb vraagt de agent naar de MariaDB op zijn node; nil als er geen
// is.
func (s *Service) mariadb(ctx context.Context, nodeID uuid.UUID) (*protocol.DBServer, error) {
	return s.server(ctx, nodeID, protocol.DBMariaDB)
}

// server vraagt de agent naar de databaseserver van engine op zijn node;
// nil als er geen is.
func (s *Service) server(ctx context.Context, nodeID uuid.UUID, engine string) (*protocol.DBServer, error) {
	return s.status(ctx, nodeID, engine, false)
}

// quickServer is server zonder de databases en de clients, voor de
// bewaking. Een agent ouder dan protocol 13 geeft ze toch.
func (s *Service) quickServer(ctx context.Context, nodeID uuid.UUID, engine string) (*protocol.DBServer, error) {
	return s.status(ctx, nodeID, engine, true)
}

func (s *Service) status(ctx context.Context, nodeID uuid.UUID, engine string, quick bool) (*protocol.DBServer, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.DB(ctx, nodeID, protocol.DBRequest{ID: uuid.NewString(), Op: protocol.DBStatus, Deadline: time.Now().Add(s.Timeout), Quick: quick})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	for i := range res.Servers {
		if res.Servers[i].Engine == engine {
			return &res.Servers[i], nil
		}
	}
	return nil, nil
}

// checkNode controleert een node voor de database: de agent, de toestand
// in ClusterForge, het systeem, de poorten, wat er al draait, het geheugen
// en de schijf. Het geeft wat het plan over de node zegt en de versie van
// Debian, leeg als het geen Debian 12 of 13 is; ok is false zolang de agent
// geen facts stuurde.
func (s *Service) checkNode(n *Node, r store.ListComponentCandidatesRow, st deploy.NodeState, status dbStatus, engineName, slug, title string,
	pool int64, usage map[uuid.UUID]float64, add func(string, string, *Node, string, string)) (pn PlanNode, osVersion string, ok bool) {
	e := engines[engineName]
	pn = PlanNode{NodeID: n.NodeID, Hostname: n.Hostname, Role: n.Role, Index: n.Index, Address: st.PrimaryIP,
		PveType: deref(r.PveType), PveNode: deref(r.PveNode)}
	if r.PveVmid != nil {
		pn.VMID = int(*r.PveVmid)
	}
	f := st.Facts
	switch {
	case !r.HasAgent:
		add(Blocked, "no_agent", n, "op "+n.Hostname+" draait geen agent", "")
	case r.HeartbeatAt == nil || s.Now().Sub(*r.HeartbeatAt) > s.Fresh:
		add(Blocked, "offline", n, "de agent op "+n.Hostname+" is niet verbonden", "")
	case r.AgentProtocol < protocol.DBSince:
		add(Blocked, "old_agent", n, fmt.Sprintf("de agent op %s is te oud (protocol %d); werk hem bij naar protocol %d", n.Hostname, r.AgentProtocol, protocol.DBSince), "")
	}
	switch r.Lifecycle {
	case store.NodeLifecycleActive:
	case store.NodeLifecycleDraining, store.NodeLifecycleDecommissioned:
		add(Blocked, "lifecycle", n, fmt.Sprintf("%s staat op %s", n.Hostname, r.Lifecycle), "")
	default:
		add(Warning, "lifecycle", n, fmt.Sprintf("%s staat op %s, niet op actief", n.Hostname, r.Lifecycle), "")
	}
	if f == nil {
		add(Blocked, "no_facts", n, "ClusterForge kent "+n.Hostname+" nog niet: de agent heeft nog geen facts gestuurd", "")
		return pn, "", false
	}
	if pn.Address == "" {
		pn.Address = f.PrimaryAddress
	}
	pn.OS = cmp.Or(f.OS.PrettyName, strings.TrimSpace(f.OS.ID+" "+f.OS.VersionID))
	if f.OS.ID != "debian" || (f.OS.VersionID != "12" && f.OS.VersionID != "13") {
		add(Blocked, "os", n, fmt.Sprintf("ClusterForge zet %s alleen op Debian 12 of 13; %s draait %s", title, n.Hostname, cmp.Or(pn.OS, "een onbekend systeem")), "")
	} else {
		osVersion = f.OS.VersionID
	}
	ports, units := e.ports, e.units
	if n.Role == RoleArbiter {
		ports, units = e.arbiterPorts, e.arbiterUnits
	}
	for _, l := range f.Listeners {
		if l.Proto == "tcp" && slices.Contains(ports, l.Port) && !ours(l.Unit, units) {
			add(Blocked, "port", n, fmt.Sprintf("op %s is poort %d al bezet door %s", n.Hostname, l.Port, cmp.Or(l.Unit, "een proces dat ClusterForge niet kent")), "")
		}
	}
	if n.Role == RoleData {
		if engineName == protocol.DBPostgreSQL {
			checkExistingPG(&pn, n, status, slug, add)
		} else {
			s.checkExisting(&pn, n, f, status, slug, add)
		}
		s.checkMemory(&pn, n, r, f, pool, usage, e, add)
		checkDisk(&pn, n, f, e.dataDir, add)
	}
	return pn, osVersion, true
}

// checkExisting kijkt wat er al aan MariaDB of MySQL op een datanode staat.
func (s *Service) checkExisting(pn *PlanNode, n *Node, f *protocol.Facts, st dbStatus, slug string, add func(string, string, *Node, string, string)) {
	for _, svc := range f.Services {
		if svc.Name == "mysql" {
			add(Blocked, "mysql", n, "op "+n.Hostname+" staat MySQL; ClusterForge zet MariaDB er niet naast", "")
			return
		}
	}
	srv := st.server
	switch {
	case st.err != nil:
		add(Blocked, "no_answer", n, fmt.Sprintf("de agent op %s zei niet welke MariaDB er draait: %v", n.Hostname, st.err), "")
	case srv == nil:
	case srv.Galera != nil && srv.Galera.ClusterName == slug:
		pn.Existing = fmt.Sprintf("Galera-cluster %s, %s", srv.Galera.ClusterName, srv.Galera.State)
		add(Blocked, "galera_exists", n, fmt.Sprintf("op %s draait al Galera-cluster %s; ClusterForge kent het niet als onderdeel. "+
			"Haal het eerst weg of neem andere nodes", n.Hostname, slug), "")
	case srv.Galera != nil:
		pn.Existing = "Galera-cluster " + srv.Galera.ClusterName
		add(Blocked, "galera_exists", n, fmt.Sprintf("op %s draait al een ander Galera-cluster (%s)", n.Hostname, srv.Galera.ClusterName), "")
	case len(srv.Databases) > 0:
		var names []string
		for _, d := range srv.Databases {
			names = append(names, d.Name)
		}
		pn.Existing = "MariaDB " + srv.Version + " met " + strings.Join(names, ", ")
		add(Blocked, "mariadb_in_use", n, fmt.Sprintf("op %s staat al een MariaDB met databases (%s). ClusterForge zet er geen cluster overheen; "+
			"verhuis ze eerst of neem andere nodes", n.Hostname, strings.Join(names, ", ")), "")
	case srv.Running:
		pn.Existing = "MariaDB " + srv.Version + " zonder eigen databases"
		add(Warning, "mariadb_takeover", n, fmt.Sprintf("op %s draait al een MariaDB zonder eigen databases; ClusterForge neemt hem over en "+
			"herstart hem als node van het cluster", n.Hostname), "")
	default:
		// Een stilstaande MariaDB toont geen databases; misschien staan ze
		// er wel. Een nieuwe Galera-node krijgt bij het aansluiten een
		// volledige kopie en wist wat er stond.
		pn.Existing = "MariaDB staat erop maar draait niet"
		add(Blocked, "mariadb_stopped", n, fmt.Sprintf("op %s staat MariaDB maar die draait niet, dus ClusterForge ziet niet welke databases erin "+
			"staan en zet er geen cluster overheen. Start hem eerst; kijk op de pagina Databases bij Zelfherstel als hij niet start", n.Hostname),
			"systemctl start mariadb")
	}
}

// checkMemory rekent uit wat er na de database aan geheugen overblijft. Is
// dat minder dan 15%, dan zegt het plan hoeveel de CT of VM erbij moet.
func (s *Service) checkMemory(pn *PlanNode, n *Node, r store.ListComponentCandidatesRow, f *protocol.Facts, pool int64,
	usage map[uuid.UUID]float64, e engine, add func(string, string, *Node, string, string)) {
	total := int64(min(f.MemoryBytes, math.MaxInt64)) //nolint:gosec // geheugen past altijd
	need := pool + e.overhead
	pn.MemoryBytes, pn.NeedBytes = total, need
	if total <= 0 {
		return
	}
	used := int64(0)
	pct, measured := usage[n.NodeID]
	if measured {
		used = int64(float64(total) * pct / 100)
		after := float64(total-used-need) / float64(total) * 100
		pn.MemoryUsedPct, pn.FreeAfterPct = &pct, &after
		if after >= 15 {
			return
		}
	} else if need <= total/2 {
		return
	}
	// Wat de node nodig heeft om 15% over te houden, afgerond op 512 MiB.
	want := int64(math.Ceil(float64(used+need)/0.85/float64(512<<20))) * 512
	msg := fmt.Sprintf("%s heeft %s geheugen; de database vraagt er %s bij", n.Hostname, templates.FormatSize(total), templates.FormatSize(need))
	if measured {
		msg += fmt.Sprintf(" en de rest gebruikte de afgelopen zeven dagen tot %.0f%%, dus er blijft minder dan 15%% over", pct)
	} else {
		msg += ", meer dan de helft; ClusterForge heeft geen metingen van wat de rest gebruikt"
	}
	cmdline := ""
	switch {
	case want*(1<<20) <= total:
	case deref(r.PveType) == "lxc" && r.PveVmid != nil:
		cmdline = fmt.Sprintf("pct set %d --memory %d", *r.PveVmid, want)
		msg += fmt.Sprintf(". Geef de CT %d MiB met dit commando op %s", want, cmp.Or(deref(r.PveNode), "de Proxmox-host"))
	case deref(r.PveType) == "qemu" && r.PveVmid != nil:
		cmdline = fmt.Sprintf("qm set %d --memory %d", *r.PveVmid, want)
		msg += fmt.Sprintf(". Geef de VM %d MiB met dit commando op %s; zonder hotplug van geheugen telt het pas na een herstart", want,
			cmp.Or(deref(r.PveNode), "de Proxmox-host"))
	default:
		msg += fmt.Sprintf(". Geef de node minstens %d MiB, of %s", want, e.smaller)
	}
	add(Warning, "memory", n, msg, cmdline)
}

// checkDisk kijkt hoeveel ruimte er is op path, waar de database haar data
// schrijft.
func checkDisk(pn *PlanNode, n *Node, f *protocol.Facts, path string, add func(string, string, *Node, string, string)) {
	var best *protocol.Filesystem
	for i := range f.Filesystems {
		fs := &f.Filesystems[i]
		if under(path, fs.Mount) && (best == nil || len(fs.Mount) > len(best.Mount)) {
			best = fs
		}
	}
	if best == nil {
		return
	}
	free := int64(0)
	if best.SizeBytes > best.UsedBytes {
		free = int64(min(best.SizeBytes-best.UsedBytes, math.MaxInt64)) //nolint:gosec // past altijd
	}
	pn.DiskFreeBytes, pn.DiskMount = &free, best.Mount
	switch {
	case free < 2<<30:
		add(Blocked, "disk", n, fmt.Sprintf("op %s is er nog %s vrij op %s; de database heeft minstens 2G nodig", n.Hostname, templates.FormatSize(free), best.Mount), "")
	case free < 10<<30:
		add(Warning, "disk_low", n, fmt.Sprintf("op %s is er nog %s vrij op %s; reken de data twee keer (de database en een dump) plus de logs", n.Hostname,
			templates.FormatSize(free), best.Mount), "")
	}
}

// under zegt of path op het bestandssysteem van mount ligt.
func under(path, mount string) bool {
	return mount == "/" || path == mount || strings.HasPrefix(path, strings.TrimRight(mount, "/")+"/")
}

// checkHosts waarschuwt als het uitvallen van één Proxmox-host de
// meerderheid meeneemt.
func checkHosts(spec Spec, byID map[uuid.UUID]store.ListComponentCandidatesRow, add func(string, string, *Node, string, string)) {
	votes := len(spec.Nodes)
	byHost := map[string][]string{}
	for _, n := range spec.order() {
		r := byID[n.NodeID]
		if r.PveNode == nil {
			continue
		}
		key := deref(r.ProxmoxName) + "/" + *r.PveNode
		byHost[key] = append(byHost[key], n.Hostname)
	}
	for _, key := range slices.Sorted(maps.Keys(byHost)) {
		hosts := byHost[key]
		if len(hosts) < 2 || votes-len(hosts) > votes/2 {
			continue
		}
		_, node, _ := strings.Cut(key, "/")
		add(Warning, "same_host", nil, fmt.Sprintf("%s staan op dezelfde Proxmox-host %s: valt die uit, dan is de meerderheid weg en stopt de database. "+
			"Zet ze op verschillende hosts", joinHosts(hosts), node), "")
	}
}

// planHash vat samen wat er zou gebeuren: de nodes met adres en rol, de
// instellingen en de bevindingen, zonder metingen die elke minuut anders
// kunnen zijn.
func planHash(p Plan, spec Spec) string {
	type node struct {
		ID      uuid.UUID `json:"id"`
		Role    string    `json:"role"`
		Index   int       `json:"index"`
		Address string    `json:"address"`
	}
	type finding struct {
		Level, Code string
		Node        *uuid.UUID
	}
	v := struct {
		Cluster  uuid.UUID      `json:"cluster"`
		Slug     string         `json:"slug"`
		Template string         `json:"template"`
		Version  string         `json:"version"`
		Params   map[string]any `json:"params"`
		Nodes    []node         `json:"nodes"`
		VIP      string         `json:"vip"`
		Findings []finding      `json:"findings"`
	}{Cluster: p.ClusterID, Slug: p.Slug, Template: p.Template, Version: p.Version, Params: p.Params, VIP: vipHash(p, spec)}
	for i, n := range spec.Nodes {
		v.Nodes = append(v.Nodes, node{n.NodeID, n.Role, n.Index, p.Nodes[i].Address})
	}
	for _, f := range p.Findings {
		v.Findings = append(v.Findings, finding{f.Level, f.Code, f.NodeID})
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Add voegt de database toe en zet de uitrol in de wachtrij, als het plan
// nog hetzelfde is als wat de gebruiker zag en niets het tegenhoudt.
func (s *Service) Add(ctx context.Context, actor events.Actor, clusterID uuid.UUID, req Request, hash string) (store.Job, error) {
	p, planned, err := s.plan(ctx, clusterID, req)
	if err != nil {
		return store.Job{}, err
	}
	if p.Hash != hash {
		return store.Job{}, ErrStale
	}
	if p.Blocked {
		for _, f := range p.Findings {
			if f.Level == Blocked {
				return store.Job{}, &ConflictError{f.Message}
			}
		}
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return store.Job{}, err
	}
	tpl, _ := registry.Get(p.Template, p.Version)
	settings, err := requestSettings(tpl, req)
	if err != nil {
		return store.Job{}, err
	}
	values, err := tpl.Validate(settings)
	if err != nil {
		return store.Job{}, err
	}
	hostnames, err := s.hostnames(ctx, clusterID)
	if err != nil {
		return store.Job{}, err
	}
	spec, err := specOf(c, tpl, req, values, hostnames)
	if err != nil {
		return store.Job{}, err
	}
	spec.VIP = planned.VIP
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if spec.VIP != nil && spec.VIP.Own {
			// Het eigen VIP komt in de inventory, zodat ClusterForge ziet
			// wie het heeft en niemand het adres nog eens kiest.
			v, err := q.CreateVIP(ctx, store.CreateVIPParams{
				ClusterID: clusterID, Address: netip.MustParseAddr(spec.VIP.Address), Interface: p.VIP.Interface, Vrid: ptr(int32(spec.VIP.VRID)), //nolint:gosec // 50 tot 255
				Description: fmt.Sprintf("%s van %s; staat op de node waar de database schrijft (ClusterForge)", tpl.Title, c.Name),
			})
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" {
				return &ConflictError{spec.VIP.Address + " is intussen al in gebruik bij een ander VIP"}
			}
			if err != nil {
				return err
			}
			spec.VIP.ID = v.ID
			if err := s.dep.AddSecretTx(ctx, q, actor, clusterID, c.Name, secretName(req.Engine, vrrpSecret), templates.RandomSecret(8)); err != nil {
				return err
			}
		}
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		comp, err := q.InsertClusterComponent(ctx, store.InsertClusterComponentParams{ClusterID: clusterID, Engine: req.Engine, Spec: b})
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return &ConflictError{fmt.Sprintf("%s heeft al een %s", c.Name, tpl.Title)}
		}
		if err != nil {
			return err
		}
		var by *uuid.UUID
		if id, err := uuid.Parse(actor.ID); err == nil {
			by = &id
		}
		if err := q.InsertClusterComponentRevision(ctx, store.InsertClusterComponentRevisionParams{
			ComponentID: comp.ID, Revision: 1, Spec: b, CreatedBy: by,
		}); err != nil {
			return err
		}
		for _, name := range spec.Secrets {
			if err := s.dep.AddSecretTx(ctx, q, actor, clusterID, c.Name, secretName(req.Engine, name), fmt.Sprint(values[name])); err != nil {
				return err
			}
		}
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: Kind, Title: fmt.Sprintf("Database toevoegen: %s op %s", tpl.Title, c.Name),
			Params: jobParams{ComponentID: comp.ID, Revision: 1}, ClusterID: &clusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		if err := q.SetClusterComponentJob(ctx, store.SetClusterComponentJobParams{ID: comp.ID, JobID: &j.ID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_added",
			Payload: map[string]any{
				"name": c.Name, "engine": req.Engine, "component": tpl.Title, "version": tpl.Version, "nodes": spec.hosts(RoleData),
				"arbiter": spec.hosts(RoleArbiter), "params": spec.Params, "vip": spec.vipAddress(), "job_id": j.ID, "title": j.Title,
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
