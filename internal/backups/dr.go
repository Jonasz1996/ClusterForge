package backups

// De DR-repetitie: van elke node van een cluster de nieuwste back-up terug
// als sandbox, op één host, aan een afgesloten bridge. De nodes zien elkaar
// daar, maar niets anders. ClusterForge controleert of het cluster werkend
// terugkomt en meet hoe lang dat duurde (RTO) en hoe oud de gegevens zijn
// (RPO). Productie raakt de repetitie nooit; daarom mag ze ook voor prod.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// KindRehearsal is de taak van de DR-repetitie. Niet Retryable: na een
// herstart van de server ruimt hij alleen op.
const KindRehearsal = "dr.rehearsal"

// DefaultBridge is de afgesloten bridge als het scenario er geen noemt.
const DefaultBridge = "vmbr99"

// De standaarddoelen van prod; lab en test hebben er geen.
const (
	ProdRTOMinutes = 60
	ProdRPOHours   = 26
)

// spreadWarning: liggen de back-ups van de nodes verder uit elkaar, dan
// passen hun gegevens misschien niet bij elkaar.
const spreadWarning = time.Hour

// Targets zijn de doelen van een cluster: zijn eigen, of anders de standaard
// van de omgeving. Nul is geen doel.
func Targets(env store.Environment, rto, rpo *int32) (rtoMinutes, rpoHours int) {
	if env == store.EnvironmentProd {
		rtoMinutes, rpoHours = ProdRTOMinutes, ProdRPOHours
	}
	if rto != nil {
		rtoMinutes = int(*rto)
	}
	if rpo != nil {
		rpoHours = int(*rpo)
	}
	return rtoMinutes, rpoHours
}

// RehearsalVIP is een VIP van het cluster, met de probe van de failovertest
// en de node die het echte VIP had toen de repetitie begon.
type RehearsalVIP struct {
	Address string         `json:"address"`
	Probe   failover.Probe `json:"probe"`
	OwnerID *uuid.UUID     `json:"owner_id,omitempty"`
}

// RehearsalInput is wat het scenario meegeeft.
type RehearsalInput struct {
	ScenarioID uuid.UUID
	Name       string
	ClusterID  uuid.UUID
	Bridge     string
	// VIPs zijn de VIP's van het cluster met hun probe; het eigenaarschap
	// leest de repetitie zelf.
	VIPs []RehearsalVIP

	// ownJob is de taak zelf, bij de voorcontrole in de taak: die telt niet
	// als andere test.
	ownJob uuid.UUID
}

// RehearsalNode is een node met de back-up die teruggaat.
type RehearsalNode struct {
	NodeID        uuid.UUID `json:"node_id"`
	Hostname      string    `json:"hostname"`
	SourceVMID    int       `json:"source_vmid"`
	Volid         string    `json:"volid"`
	BackupTime    time.Time `json:"backup_time"`
	BackupStorage string    `json:"backup_storage"`
	Size          int64     `json:"size"`
	// Address is het adres van de node, waarop de andere nodes hem in de
	// sandbox bereiken.
	Address string `json:"address,omitempty"`
	// Memory en Disk zijn wat de VM volgens zijn config uit de back-up
	// vraagt; Nets zijn zijn netwerkkaarten, met het MAC-adres van de bron.
	Memory int64             `json:"memory"`
	Disk   int64             `json:"disk"`
	Nets   map[string]string `json:"nets"`
	Agent  bool              `json:"agent"`
}

// RehearsalDefinition staat in test_runs.definition: wat er teruggaat en
// waar.
type RehearsalDefinition struct {
	ScenarioID     uuid.UUID       `json:"scenario_id"`
	Name           string          `json:"name"`
	ClusterID      uuid.UUID       `json:"cluster_id"`
	ClusterName    string          `json:"cluster_name"`
	Environment    string          `json:"environment"`
	Bridge         string          `json:"bridge"`
	ConnectionID   uuid.UUID       `json:"connection_id"`
	ConnectionName string          `json:"connection_name"`
	Host           string          `json:"host"`
	Storage        string          `json:"storage"`
	Nodes          []RehearsalNode `json:"nodes"`
	VIPs           []RehearsalVIP  `json:"vips"`
	// De doelen; nul is geen doel.
	RTOTargetMinutes int `json:"rto_target_minutes"`
	RPOTargetHours   int `json:"rpo_target_hours"`
	// Fix zijn de commando's die de bridge afsluiten, als die niet klopt.
	Fix []string `json:"fix,omitempty"`
}

// oldest is de tijd van de oudste back-up; spread het verschil met de
// nieuwste.
func (d RehearsalDefinition) oldest() (time.Time, time.Duration) {
	if len(d.Nodes) == 0 {
		return time.Time{}, 0
	}
	lo, hi := d.Nodes[0].BackupTime, d.Nodes[0].BackupTime
	for _, n := range d.Nodes[1:] {
		if n.BackupTime.Before(lo) {
			lo = n.BackupTime
		}
		if n.BackupTime.After(hi) {
			hi = n.BackupTime
		}
	}
	return lo, hi.Sub(lo)
}

// RehearsalPlan is de voorcontrole: wat er teruggaat, waar, en of het kan.
type RehearsalPlan struct {
	RehearsalDefinition
	Checks []Check
	OK     bool
	// RPO is de leeftijd van de oudste back-up nu.
	RPO time.Duration
	// Spread is hoe ver de back-ups uit elkaar liggen.
	Spread time.Duration
}

// bridgeRe is een gewone Linux-bridgenaam: hoogstens 15 tekens.
var bridgeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// ValidBridge controleert de naam van de afgesloten bridge.
func ValidBridge(b string) error {
	if !bridgeRe.MatchString(b) {
		return ValidationError{"geef de naam van een bridge zoals " + DefaultBridge + ": letters, cijfers, punt, - of _, hoogstens 15 tekens"}
	}
	return nil
}

// PlanRehearsal doet de voorcontrole van een DR-repetitie zonder iets te
// veranderen: kiest per node de nieuwste back-up, één host met genoeg
// geheugen en storage, en controleert de afgesloten bridge daarop.
func (s *Service) PlanRehearsal(ctx context.Context, in RehearsalInput) (p RehearsalPlan, err error) {
	p = RehearsalPlan{RehearsalDefinition: RehearsalDefinition{
		ScenarioID: in.ScenarioID, Name: in.Name, ClusterID: in.ClusterID, Bridge: or(in.Bridge, DefaultBridge),
		Nodes: []RehearsalNode{}, VIPs: slices.Clone(in.VIPs),
	}}
	add := func(name string, ok bool, detail string) {
		p.Checks = append(p.Checks, Check{Name: name, OK: ok, Detail: detail})
	}
	c, err := s.q.GetCluster(ctx, in.ClusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.ClusterName, p.Environment = c.Name, string(c.Environment)
	p.RTOTargetMinutes, p.RPOTargetHours = Targets(c.Environment, c.RtoTargetMinutes, c.RpoTargetHours)
	if p.VIPs == nil {
		p.VIPs = []RehearsalVIP{}
	}
	if err := s.vipOwners(ctx, &p.RehearsalDefinition); err != nil {
		return p, err
	}
	defer func() { p.OK = !slices.ContainsFunc(p.Checks, func(c Check) bool { return !c.OK && !c.Warning }) }()

	if s.runner == nil {
		add("Repetitie", false, "de back-upcontrole staat uit op deze server, en daarmee ook de DR-repetitie")
		return p, nil
	}
	if job, err := s.q.GetTestSlotJob(ctx); err == nil && job.ID != in.ownJob {
		add("Testslot", false, "er loopt al een test: "+job.Title+"; er loopt er hoogstens één tegelijk")
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	} else {
		add("Testslot", true, "er loopt geen andere test")
	}
	switch c.Status {
	case "split_brain":
		add("Echt cluster", false, c.Name+" heeft nu split-brain; de bewaking tijdens de repetitie heeft dan geen vast beginpunt")
	default:
		add("Echt cluster", true, c.Name+" blijft draaien; verhuist een VIP of komt er split-brain, dan zet ClusterForge alle sandboxes meteen hard uit")
	}

	// De nodes en hun nieuwste back-up.
	rows, err := s.q.ListRehearsalNodes(ctx, &in.ClusterID)
	if err != nil {
		return p, err
	}
	var problems []string
	var conn *uuid.UUID
	backups := map[uuid.UUID]store.ProxmoxBackup{}
	for _, n := range rows {
		switch {
		case n.ProxmoxID == nil || n.PveVmid == nil:
			problems = append(problems, n.Hostname+" is niet aan een VM in Proxmox gekoppeld")
			continue
		case deref(n.GuestType) == "lxc":
			problems = append(problems, n.Hostname+" is een LXC-container; die gaat niet in een sandbox")
			continue
		case n.LastError != nil && *n.LastError != "":
			problems = append(problems, "Proxmox "+deref(n.ConnectionName)+" is niet bereikbaar: "+*n.LastError)
			continue
		case conn != nil && *conn != *n.ProxmoxID:
			problems = append(problems, n.Hostname+" staat in een andere Proxmox-koppeling; alle nodes moeten in dezelfde staan")
			continue
		}
		conn = n.ProxmoxID
		p.ConnectionName = deref(n.ConnectionName)
		b, err := s.q.GetLatestGuestBackup(ctx, store.GetLatestGuestBackupParams{ConnectionID: *n.ProxmoxID, Vmid: *n.PveVmid})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			problems = append(problems, fmt.Sprintf("Proxmox heeft geen back-up van %s (VM %d)", n.Hostname, *n.PveVmid))
			continue
		case err != nil:
			return p, err
		case b.GuestType == "lxc":
			problems = append(problems, n.Hostname+" heeft alleen een back-up van een container")
			continue
		}
		backups[n.ID] = b
		node := RehearsalNode{
			NodeID: n.ID, Hostname: n.Hostname, SourceVMID: int(*n.PveVmid), Volid: b.Volid, BackupTime: b.Ctime,
			BackupStorage: b.Storage, Size: b.SizeBytes,
		}
		if n.PrimaryIp != nil {
			node.Address = n.PrimaryIp.String()
		}
		p.Nodes = append(p.Nodes, node)
	}
	switch {
	case len(rows) == 0:
		add("Nodes", false, c.Name+" heeft geen actieve nodes")
		return p, nil
	case len(problems) > 0:
		add("Nodes", false, strings.Join(problems, "; "))
		return p, nil
	}
	p.ConnectionID = *conn
	oldest, spread := p.oldest()
	p.RPO, p.Spread = s.Now().Sub(oldest), spread
	add("Nodes", true, fmt.Sprintf("%s, elk met zijn nieuwste back-up; de oudste is van %s",
		count(len(p.Nodes), "node", "nodes"), oldest.Local().Format("02-01-2006 15:04")))
	if spread > spreadWarning {
		p.Checks = append(p.Checks, Check{Name: "Back-ups bij elkaar", Warning: true, Detail: fmt.Sprintf(
			"de back-ups liggen %s uit elkaar; de gegevens van de nodes passen dan misschien niet bij elkaar, bijvoorbeeld bij een database met replicatie",
			duration(spread))})
	}

	api, _, err := s.pve.APIFor(ctx, p.ConnectionID)
	if err != nil {
		add("Proxmox", false, "Proxmox is niet bereikbaar: "+err.Error())
		return p, nil
	}
	res, err := api.Resources(ctx)
	if err != nil {
		add("Proxmox", false, "Proxmox is niet bereikbaar: "+err.Error())
		return p, nil
	}
	if !s.chooseHost(ctx, api, res, &p, backups) {
		return p, nil
	}
	s.checkBridge(ctx, api, res, &p)
	return p, nil
}

// vipOwners legt vast wie elk echt VIP nu heeft.
func (s *Service) vipOwners(ctx context.Context, d *RehearsalDefinition) error {
	owners, err := s.q.ListClusterVIPOwners(ctx, d.ClusterID)
	if err != nil {
		return err
	}
	for i := range d.VIPs {
		for _, o := range owners {
			if o.Address.String() == d.VIPs[i].Address {
				d.VIPs[i].OwnerID = o.OwnerNodeID
			}
		}
	}
	return nil
}

// hostFit is een host met wat hij voor de repetitie heeft.
type hostFit struct {
	name    string
	free    int64
	storage proxmox.Resource
	problem string
}

// chooseHost leest de config van elke back-up en kiest de host waarop alle
// back-ups te lezen zijn, met genoeg geheugen en storage voor alle VM's
// samen. Alle sandboxes komen op die ene host, want alleen daar zien ze
// elkaar op de afgesloten bridge.
func (s *Service) chooseHost(ctx context.Context, api proxmox.API, res []proxmox.Resource, p *RehearsalPlan, backups map[uuid.UUID]store.ProxmoxBackup) bool {
	add := func(name string, ok bool, detail string) {
		p.Checks = append(p.Checks, Check{Name: name, OK: ok, Detail: detail})
	}
	hosts := map[string]proxmox.Resource{}
	for _, r := range res {
		if r.Type == "node" && r.Status == "online" {
			hosts[r.Node] = r
		}
	}
	// readable[host] = alle back-ups zijn er te lezen.
	readable := map[string]bool{}
	for h := range hosts {
		readable[h] = true
	}
	for _, n := range p.Nodes {
		b := backups[n.NodeID]
		on := map[string]bool{}
		for _, r := range res {
			if r.Type != "storage" || r.Storage != b.Storage || r.Status == "unknown" {
				continue
			}
			if r.Shared == 1 || r.Plugin == "pbs" || r.Node == b.PveNode {
				on[r.Node] = true
			}
		}
		for h := range readable {
			if !on[h] {
				delete(readable, h)
			}
		}
	}
	if len(readable) == 0 {
		add("Host", false, "geen host die online is, kan alle back-ups lezen; zet ze op gedeelde storage of Proxmox Backup Server")
		return false
	}
	// De config uit elke back-up: geheugen, schijven en netwerkkaarten.
	reader := slices.Sorted(maps.Keys(readable))[0]
	var need, mem int64
	var bad []string
	for i := range p.Nodes {
		n := &p.Nodes[i]
		cfg, err := api.ExtractConfig(ctx, reader, n.Volid)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: config uit de back-up lezen: %v", n.Hostname, err))
			continue
		}
		if cfg["template"] == "1" {
			bad = append(bad, n.Hostname+": de back-up is van een template")
			continue
		}
		if x := notIsolatable(cfg); len(x) > 0 {
			bad = append(bad, n.Hostname+": niet af te sluiten: "+strings.Join(x, ", "))
			continue
		}
		n.Memory, n.Disk, n.Agent, n.Nets = memoryBytes(cfg), diskBytes(cfg), hasAgent(cfg), map[string]string{}
		for k, v := range cfg {
			if netKey(k) {
				n.Nets[k] = v
			}
		}
		if len(n.Nets) == 0 {
			bad = append(bad, n.Hostname+": de VM heeft geen netwerkkaart")
		}
		need += n.Disk
		mem += n.Memory
	}
	if len(bad) > 0 {
		add("Back-ups", false, strings.Join(bad, "; "))
		return false
	}
	add("Back-ups", true, fmt.Sprintf("%s samen, %s geheugen", gb(need), gb(mem)))

	// Per host: geheugen plus 1 GB, en een storage met 10 % marge onder 85 %.
	want := need + need/10
	var fits []hostFit
	for h := range readable {
		f := hostFit{name: h, free: hosts[h].MaxMem - hosts[h].Mem}
		var best *proxmox.Resource
		for i, r := range res {
			if r.Type != "storage" || r.Node != h || r.Status != "available" {
				continue
			}
			if s.SandboxStorage != "" {
				if r.Storage == s.SandboxStorage {
					best = &res[i]
				}
				continue
			}
			if !slices.Contains(strings.Split(r.Content, ","), "images") {
				continue
			}
			if best == nil || r.MaxDisk-r.Disk > best.MaxDisk-best.Disk {
				best = &res[i]
			}
		}
		switch {
		case best == nil && s.SandboxStorage != "":
			f.problem = fmt.Sprintf("sandbox-storage %s is niet beschikbaar op %s", s.SandboxStorage, h)
		case best == nil:
			f.problem = h + " heeft geen storage voor VM-schijven"
		case best.MaxDisk <= 0:
			f.problem = "de vrije ruimte op " + best.Storage + " op " + h + " is onbekend"
		case float64(best.Disk+want) > 0.85*float64(best.MaxDisk):
			f.problem = fmt.Sprintf("storage %s op %s zou boven 85 %% komen: nu %d %% gebruikt, de VM's vragen %s met 10 %% marge",
				best.Storage, h, best.Disk*100/best.MaxDisk, gb(want))
		case f.free < mem+1<<30:
			f.problem = fmt.Sprintf("%s heeft te weinig vrij geheugen: nodig %s (alle VM's plus 1 GB), vrij %s", h, gb(mem+1<<30), gb(f.free))
		default:
			f.storage = *best
		}
		fits = append(fits, f)
	}
	slices.SortFunc(fits, func(a, b hostFit) int {
		if (a.problem == "") != (b.problem == "") {
			if a.problem == "" {
				return -1
			}
			return 1
		}
		if a.free != b.free {
			if a.free > b.free {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	if fits[0].problem != "" {
		var why []string
		for _, f := range fits {
			why = append(why, f.problem)
		}
		add("Capaciteit", false, strings.Join(why, "; "))
		return false
	}
	// Liefst een host waarop de bridge al afgesloten is.
	for _, f := range fits {
		if f.problem != "" {
			break
		}
		if p.Host == "" {
			p.Host, p.Storage = f.name, f.storage.Storage
		}
		if ifaces, err := api.HostNetwork(ctx, f.name); err == nil {
			if prob, _ := bridgeProblem(proxmox.ParseHostNetwork(ifaces), f.name, p.Bridge); prob == "" {
				p.Host, p.Storage = f.name, f.storage.Storage
				break
			}
		}
	}
	add("Capaciteit", true, fmt.Sprintf("alles op %s: storage %s en %s vrij geheugen", p.Host, p.Storage, gb(hostFree(fits, p.Host))))
	return true
}

func hostFree(fits []hostFit, name string) int64 {
	for _, f := range fits {
		if f.name == name {
			return f.free
		}
	}
	return 0
}

// checkBridge controleert de afgesloten bridge op de gekozen host: ze
// bestaat, is een Linux-bridge zonder poorten en zonder adres, en geen VM
// buiten het sandbox-register hangt eraan. Anders komen de commando's om
// het te herstellen in het plan.
func (s *Service) checkBridge(ctx context.Context, api proxmox.API, res []proxmox.Resource, p *RehearsalPlan) {
	ifaces, err := api.HostNetwork(ctx, p.Host)
	if err != nil {
		p.Checks = append(p.Checks, Check{Name: "Bridge", Detail: "het netwerk van " + p.Host + " is niet te lezen: " + err.Error() +
			"; het API-token heeft Sys.Audit op /nodes nodig"})
		return
	}
	if prob, fix := bridgeProblem(proxmox.ParseHostNetwork(ifaces), p.Host, p.Bridge); prob != "" {
		p.Fix = fix
		p.Checks = append(p.Checks, Check{Name: "Bridge", Code: "bridge_not_isolated", Detail: prob})
		return
	}
	others, err := s.onBridge(ctx, api, res, p.Host, p.Bridge, nil)
	if err != nil {
		p.Checks = append(p.Checks, Check{Name: "Bridge", Detail: "de VM's op " + p.Host + " zijn niet te lezen: " + err.Error()})
		return
	}
	if len(others) > 0 {
		p.Checks = append(p.Checks, Check{Name: "Bridge", Code: "bridge_in_use", Detail: p.Bridge + " op " + p.Host + " is niet afgesloten: " +
			strings.Join(others, ", ") + "; haal die netwerkkaarten eraf of kies een andere bridge bij het scenario"})
		return
	}
	p.Checks = append(p.Checks, Check{Name: "Bridge", OK: true, Detail: p.Bridge + " op " + p.Host +
		" is afgesloten: een bridge zonder poorten en zonder adres, en geen andere VM hangt eraan"})
}

// bridgeProblem zegt wat er aan de bridge op host mankeert, met de
// commando's om het op de host te herstellen. Leeg als ze klopt.
func bridgeProblem(ifaces []proxmox.HostIface, host, bridge string) (string, []string) {
	apply := "pvesh set /nodes/" + host + "/network"
	i := slices.IndexFunc(ifaces, func(x proxmox.HostIface) bool { return x.Iface == bridge })
	if i < 0 {
		return fmt.Sprintf("op %s bestaat geen bridge %s; maak een afgesloten bridge zonder poorten en zonder adres", host, bridge), []string{
			fmt.Sprintf("pvesh create /nodes/%s/network --iface %s --type bridge --autostart 1 --comments 'ClusterForge DR-repetitie: afgesloten'", host, bridge),
			apply,
		}
	}
	b := ifaces[i]
	if b.Type != "bridge" {
		return fmt.Sprintf("%s op %s is geen Linux-bridge maar %s; kies een andere naam bij het scenario", bridge, host, b.Type), nil
	}
	var del, why []string
	if len(b.Ports) > 0 {
		del = append(del, "bridge_ports")
		why = append(why, "hangt aan "+strings.Join(b.Ports, ", "))
	}
	if b.CIDR != "" || b.Gateway != "" {
		del = append(del, "cidr", "gateway")
		why = append(why, "heeft adres "+or(b.CIDR, b.Gateway))
	}
	if b.CIDR6 != "" || b.Gateway6 != "" {
		del = append(del, "cidr6", "gateway6")
		why = append(why, "heeft adres "+or(b.CIDR6, b.Gateway6))
	}
	if len(why) == 0 {
		return "", nil
	}
	return fmt.Sprintf("%s op %s is niet afgesloten: ze %s", bridge, host, strings.Join(why, " en ")), []string{
		fmt.Sprintf("pvesh set /nodes/%s/network/%s --type bridge --delete %s", host, bridge, strings.Join(del, ",")),
		apply,
	}
}

// onBridge zijn de VM's en containers op host die aan de bridge hangen,
// gelezen uit hun config van nu. Alleen de sandboxes in mine tellen niet:
// die van de repetitie zelf. Ook een achtergebleven sandbox van een eerdere
// repetitie telt, want die kan dezelfde adressen hebben.
func (s *Service) onBridge(ctx context.Context, api proxmox.API, res []proxmox.Resource, host, bridge string, mine map[int]bool) ([]string, error) {
	var out []string
	for _, r := range res {
		if (r.Type != "qemu" && r.Type != "lxc") || r.Node != host {
			continue
		}
		if inSandboxPool(r) && mine[r.VMID] {
			continue
		}
		cfg, err := api.Config(ctx, proxmox.Guest{Type: r.Type, Node: r.Node, VMID: r.VMID})
		if err != nil {
			return nil, fmt.Errorf("config van %d: %w", r.VMID, err)
		}
		for _, nic := range proxmox.ParseGuestConfig(cfg).NICs {
			if nic.Bridge == bridge {
				out = append(out, fmt.Sprintf("VM %d (%s) hangt er met %s aan", r.VMID, r.Name, nic.Key))
			}
		}
	}
	return out, nil
}

type rehearsalParams struct {
	RunID uuid.UUID `json:"run_id"`
}

// PrecheckError is een DR-repetitie die niet start omdat de voorcontrole
// faalt.
type PrecheckError struct {
	Checks []Check
	Fix    []string
}

func (e *PrecheckError) Error() string {
	var failed []string
	for _, c := range e.Checks {
		if !c.OK && !c.Warning {
			failed = append(failed, c.Name+": "+c.Detail)
		}
	}
	return "de voorcontrole faalt: " + strings.Join(failed, "; ")
}

// StartRehearsal zet een DR-repetitie in de wachtrij na de voorcontrole.
// Een geplande repetitie waarvan de voorcontrole faalt, wordt een run met
// uitslag error en de commando's; een handmatige geeft de PrecheckError.
func (s *Service) StartRehearsal(ctx context.Context, actor events.Actor, in RehearsalInput, trigger string) (store.TestRun, error) {
	p, err := s.PlanRehearsal(ctx, in)
	if err != nil {
		return store.TestRun{}, err
	}
	if !p.OK && trigger != TriggerSchedule {
		return store.TestRun{}, &PrecheckError{Checks: p.Checks, Fix: p.Fix}
	}
	defJSON, err := json.Marshal(p.RehearsalDefinition)
	if err != nil {
		return store.TestRun{}, err
	}
	checks, err := json.Marshal(nonNil(p.Checks))
	if err != nil {
		return store.TestRun{}, err
	}
	title := "DR-repetitie: " + p.ClusterName
	if trigger == TriggerSchedule {
		title += " (gepland)"
	}
	var run store.TestRun
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		run, err = q.InsertRehearsalRun(ctx, store.InsertRehearsalRunParams{
			Trigger: trigger, ClusterID: &in.ClusterID, ScenarioID: &in.ScenarioID, Definition: defJSON, Checks: checks,
			RequestedBy: userOf(actor),
		})
		if err != nil {
			return err
		}
		if !p.OK {
			// Gepland en de voorcontrole faalt: meteen een rapport.
			result, summary := "error", precheckSummary(p.ClusterName+" op "+p.Bridge, failedChecks(p.Checks), p.Fix)
			if run, err = q.FinishTestRun(ctx, store.FinishTestRunParams{
				ID: run.ID, Result: &result, Summary: summary, Checks: checks, Timeline: []byte("[]"), Measurements: []byte("{}"),
			}); err != nil {
				return err
			}
			return s.ev.Write(ctx, q, rehearsalEvent(run, p.RehearsalDefinition, "dr.finished", actor, nil))
		}
		// Geen clusterslot: de repetitie raakt het echte cluster niet aan.
		// Het testslot houdt het bij één invasieve test tegelijk.
		j, err := s.runner.EnqueueTx(ctx, q, jobs.Spec{
			Kind: KindRehearsal, Title: title, Params: rehearsalParams{RunID: run.ID},
			ClusterID: &in.ClusterID, ProxmoxID: &p.ConnectionID, Actor: actor,
		})
		if err != nil {
			return err
		}
		run.JobID = &j.ID
		if err := q.SetTestRunJob(ctx, store.SetTestRunJobParams{ID: run.ID, JobID: &j.ID}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, rehearsalEvent(run, p.RehearsalDefinition, "dr.started", actor, nil))
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "jobs_test_slot" {
		return store.TestRun{}, ConflictError{"busy", "er loopt al een test; er loopt er hoogstens één tegelijk"}
	}
	if err != nil {
		return store.TestRun{}, err
	}
	if p.OK {
		s.runner.Kick()
	}
	return run, nil
}

// precheckSummary is de samenvatting van een repetitie die niet startte.
func precheckSummary(head, failed string, fix []string) string {
	msg := "ERROR: " + head + ": de DR-repetitie startte niet, want de voorcontrole faalt: " + failed + "."
	if len(fix) > 0 {
		msg += " De commando's om de bridge af te sluiten staan in het rapport."
	}
	return msg
}

func failedChecks(checks []Check) string {
	var out []string
	for _, c := range checks {
		if !c.OK && !c.Warning {
			out = append(out, c.Name+": "+c.Detail)
		}
	}
	return strings.Join(out, "; ")
}

// rehearsalEvent is een event over een DR-repetitie.
func rehearsalEvent(run store.TestRun, d RehearsalDefinition, action string, actor events.Actor, extra map[string]any) events.Event {
	if actor.Type == "" {
		actor = events.System()
	}
	p := map[string]any{
		"run_id": run.ID, "name": d.Name, "cluster": d.ClusterName, "bridge": d.Bridge, "host": d.Host,
		"nodes": len(d.Nodes), "trigger": run.Trigger,
	}
	if run.Result != nil {
		p["result"], p["summary"] = *run.Result, run.Summary
	}
	maps.Copy(p, extra)
	return events.Event{Actor: actor, SubjectType: "test_run", SubjectID: run.ID.String(), ClusterID: run.ClusterID, Action: action, Payload: p}
}

// SkipRehearsal schrijft een overgeslagen geplande DR-repetitie, zonder
// taak.
func (s *Service) SkipRehearsal(ctx context.Context, in RehearsalInput, clusterName, reason string) error {
	d := RehearsalDefinition{
		ScenarioID: in.ScenarioID, Name: in.Name, ClusterID: in.ClusterID, ClusterName: clusterName, Bridge: or(in.Bridge, DefaultBridge),
		Nodes: []RehearsalNode{}, VIPs: nonNil(in.VIPs),
	}
	defJSON, err := json.Marshal(d)
	if err != nil {
		return err
	}
	result, summary := "skipped", "Overgeslagen: "+reason
	var run store.TestRun
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.InsertRehearsalRun(ctx, store.InsertRehearsalRunParams{
			Trigger: TriggerSchedule, ClusterID: &in.ClusterID, ScenarioID: &in.ScenarioID, Definition: defJSON, Checks: []byte("[]"),
		})
		if err != nil {
			return err
		}
		run, err = q.FinishTestRun(ctx, store.FinishTestRunParams{
			ID: r.ID, Result: &result, Summary: summary, Checks: []byte("[]"), Timeline: []byte("[]"), Measurements: []byte("{}"),
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, rehearsalEvent(run, d, "dr.finished", events.System(), nil))
	})
	return err
}

// SetRecoveryTargets zet de doelen van een cluster. Nil is de standaard van
// de omgeving, nul is geen doel.
func (s *Service) SetRecoveryTargets(ctx context.Context, actor events.Actor, clusterID uuid.UUID, rto, rpo *int32) (store.Cluster, error) {
	if rto != nil && (*rto < 0 || *rto > 10080) {
		return store.Cluster{}, ValidationError{"het RTO-doel ligt tussen 0 en 10080 minuten (een week); 0 is geen doel"}
	}
	if rpo != nil && (*rpo < 0 || *rpo > 8760) {
		return store.Cluster{}, ValidationError{"het RPO-doel ligt tussen 0 en 8760 uur (een jaar); 0 is geen doel"}
	}
	var out store.Cluster
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockCluster(ctx, clusterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out, err = q.SetClusterRecoveryTargets(ctx, store.SetClusterRecoveryTargetsParams{ID: clusterID, RtoTargetMinutes: rto, RpoTargetHours: rpo})
		if err != nil {
			return err
		}
		fromRTO, fromRPO := Targets(cur.Environment, cur.RtoTargetMinutes, cur.RpoTargetHours)
		toRTO, toRPO := Targets(out.Environment, rto, rpo)
		p := map[string]any{"name": cur.Name}
		if fromRTO != toRTO {
			p["rto_target_minutes"] = map[string]any{"from": fromRTO, "to": toRTO}
		}
		if fromRPO != toRPO {
			p["rpo_target_hours"] = map[string]any{"from": fromRPO, "to": toRPO}
		}
		if len(p) == 1 {
			return nil
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.recovery_targets_updated", Payload: p,
		})
	})
	return out, err
}
