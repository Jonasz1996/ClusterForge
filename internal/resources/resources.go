// Package resources zoekt problemen met resources en gezondheid en stelt een
// oplossing voor die je met één klik uitvoert (testlijst 3, N27).
//
// Wat het bekijkt: volle schijven (ook de trend van zeven dagen), volle
// swap, aanhoudend weinig vrij geheugen, aanhoudend hoge load en containers
// die blijven herstarten, met de database waarop ze wachten (N26). Een
// voorstel is een taak met een log: rootfs van een CT vergroten, swap of
// geheugen erbij, een kern erbij, of een container stoppen. Nooit
// automatisch, en nooit krimpen of verwijderen: "minder geheugen" is
// hoogstens een tip.
package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/dockerhost"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/pvehost"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Thresholds zijn de drempels van één omgeving.
type Thresholds struct {
	// DiskPct: een schijf vanaf zoveel procent krijgt een voorstel.
	DiskPct int `json:"disk_pct"`
	// DiskDays: of als ze volgens de trend binnen zoveel dagen vol is.
	DiskDays int `json:"disk_days"`
	// SwapPct: gemiddeld swapgebruik over zeven dagen.
	SwapPct int `json:"swap_pct"`
	// MemPct: geheugengebruik (p95 over zeven dagen).
	MemPct int `json:"mem_pct"`
	// LoadRatio: gemiddelde load15 gedeeld door het aantal kernen.
	LoadRatio float64 `json:"load_ratio"`
}

// Settings zijn de drempels per omgeving (lab, test, prod).
type Settings map[string]Thresholds

// Defaults zonder instelling.
var Defaults = Settings{
	"lab":  {DiskPct: 90, DiskDays: 14, SwapPct: 80, MemPct: 90, LoadRatio: 1.5},
	"test": {DiskPct: 90, DiskDays: 14, SwapPct: 80, MemPct: 90, LoadRatio: 1.5},
	"prod": {DiskPct: 85, DiskDays: 21, SwapPct: 70, MemPct: 85, LoadRatio: 1.2},
}

// Soorten voorstel.
const (
	KindDisk       = "disk"
	KindSwap       = "swap"
	KindMemory     = "memory"
	KindCPU        = "cpu"
	KindRestarts   = "restarts"
	KindDependency = "dependency"
	KindTip        = "tip"
)

// Proposal is één probleem met (als het kan) een oplossing.
type Proposal struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Level       string    `json:"level"`
	NodeID      uuid.UUID `json:"node_id"`
	Node        string    `json:"node"`
	Environment string    `json:"environment"`
	Title       string    `json:"title"`
	Detail      string    `json:"detail,omitempty"`
	Href        string    `json:"href,omitempty"`
	Fix         *Fix      `json:"fix,omitempty"`
	// NoFix zegt waarom er geen knop is.
	NoFix string `json:"no_fix,omitempty"`
}

// Fix is de oplossing met één klik: een taak met log.
type Fix struct {
	Label string `json:"label"`
	// Undo zegt hoe je het terugdraait.
	Undo string `json:"undo"`
	// PVE: een actie op een Proxmox-host.
	HostNodeID *uuid.UUID       `json:"host_node_id,omitempty"`
	PVE        *protocol.PVERun `json:"pve,omitempty"`
	// Docker: een container stoppen.
	DockerNode *uuid.UUID `json:"docker_node,omitempty"`
	Container  string     `json:"container,omitempty"`
}

// PVE is wat nodig is van de Proxmox-hosts.
type PVE interface {
	Hosts(ctx context.Context) ([]pvehost.Host, []pvehost.Missing, error)
	Inventory(ctx context.Context, nodeID uuid.UUID) (protocol.PVEInventory, error)
	FindGuest(ctx context.Context, connID uuid.UUID, vmid int) (uuid.UUID, string, protocol.PVEGuest, error)
	Start(ctx context.Context, actor events.Actor, req pvehost.Request) (store.Job, error)
}

// Docker is wat nodig is van de Docker-hosts.
type Docker interface {
	Hosts(ctx context.Context) ([]dockerhost.Host, error)
	Get(ctx context.Context, nodeID uuid.UUID, path string, query map[string]string, admin bool) (json.RawMessage, error)
	StartCall(ctx context.Context, actor events.Actor, nodeID uuid.UUID, c dockerhost.Call) (store.Job, error)
}

// Metrics geeft de cijfers van zeven dagen.
type Metrics interface {
	Risk(ctx context.Context, now time.Time) (map[uuid.UUID]*metrics.NodeRisk, error)
}

// Service rekent de voorstellen uit.
type Service struct {
	pool    *pgxpool.Pool
	ev      *events.Writer
	PVE     PVE
	Docker  Docker
	Metrics Metrics
	Now     func() time.Time

	mu     sync.Mutex
	cached []Proposal
	at     time.Time
	busy   bool
}

func NewService(pool *pgxpool.Pool, ev *events.Writer) *Service {
	return &Service{pool: pool, ev: ev, Now: time.Now}
}

// ErrNotFound: het voorstel bestaat (niet) meer.
var ErrNotFound = errors.New("dit voorstel bestaat niet (meer); ververs de pagina")

// FieldError is ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

const settingsKey = "resources"

// Settings geeft de drempels, aangevuld met de standaard.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	out := Settings{}
	for k, v := range Defaults {
		out[k] = v
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT thresholds FROM resources_settings WHERE id`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var saved Settings
	if json.Unmarshal(raw, &saved) == nil {
		for k, v := range saved {
			if _, ok := out[k]; ok {
				out[k] = v
			}
		}
	}
	return out, nil
}

// SetSettings bewaart de drempels.
func (s *Service) SetSettings(ctx context.Context, actor events.Actor, in Settings) (Settings, error) {
	out := Settings{}
	for env := range Defaults {
		t, ok := in[env]
		if !ok {
			t = Defaults[env]
		}
		switch {
		case t.DiskPct < 50 || t.DiskPct > 99:
			return nil, &FieldError{"schijf: kies tussen 50 en 99 %"}
		case t.DiskDays < 0 || t.DiskDays > 365:
			return nil, &FieldError{"dagen tot vol: kies tussen 0 en 365"}
		case t.SwapPct < 10 || t.SwapPct > 100:
			return nil, &FieldError{"swap: kies tussen 10 en 100 %"}
		case t.MemPct < 50 || t.MemPct > 100:
			return nil, &FieldError{"geheugen: kies tussen 50 en 100 %"}
		case t.LoadRatio < 0.5 || t.LoadRatio > 10:
			return nil, &FieldError{"load: kies tussen 0,5 en 10 keer het aantal kernen"}
		}
		out[env] = t
	}
	raw, _ := json.Marshal(out)
	if _, err := s.pool.Exec(ctx, `INSERT INTO resources_settings (id, thresholds, updated_at) VALUES (true, $1, now())
		ON CONFLICT (id) DO UPDATE SET thresholds = EXCLUDED.thresholds, updated_at = now()`, raw); err != nil {
		return nil, err
	}
	if s.ev != nil {
		_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "settings", SubjectID: settingsKey, Action: "resources.settings_updated"})
	}
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
	return out, nil
}

// node is een node met wat de voorstellen nodig hebben.
type node struct {
	ID          uuid.UUID
	Hostname    string
	Environment string
	ProxmoxID   *uuid.UUID
	VMID        *int32
	Facts       protocol.Facts
}

func (s *Service) nodes(ctx context.Context) ([]node, error) {
	rows, err := s.pool.Query(ctx, `SELECT n.id, n.hostname, coalesce(c.environment::text, 'lab'), n.proxmox_id, n.pve_vmid, f.facts
		FROM nodes n
		LEFT JOIN clusters c ON c.id = n.cluster_id
		LEFT JOIN node_facts f ON f.node_id = n.id
		WHERE n.lifecycle = 'active'
		ORDER BY lower(n.hostname)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []node
	for rows.Next() {
		var n node
		var facts []byte
		if err := rows.Scan(&n.ID, &n.Hostname, &n.Environment, &n.ProxmoxID, &n.VMID, &facts); err != nil {
			return nil, err
		}
		if len(facts) > 0 {
			_ = json.Unmarshal(facts, &n.Facts)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// cacheFor is hoe lang een berekening geldt; de inventaris van Proxmox en
// Docker vragen gaat via de agents.
const cacheFor = time.Minute

// Proposals geeft de voorstellen, gesorteerd op ernst en node.
func (s *Service) Proposals(ctx context.Context, fresh bool) ([]Proposal, error) {
	s.mu.Lock()
	if !fresh && s.cached != nil && s.Now().Sub(s.at) < cacheFor {
		out := s.cached
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	out, err := s.compute(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cached, s.at = out, s.Now()
	s.mu.Unlock()
	return out, nil
}

// guest is een CT of VM met zijn Proxmox-host.
type guest struct {
	host    uuid.UUID
	pveNode string
	g       protocol.PVEGuest
	storage map[string]protocol.PVEStorage
}

var subvolRe = regexp.MustCompile(`subvol-(\d+)-disk-(\d+)`)

func (s *Service) compute(ctx context.Context) ([]Proposal, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodes(ctx)
	if err != nil {
		return nil, err
	}
	var risk map[uuid.UUID]*metrics.NodeRisk
	if s.Metrics != nil {
		risk, _ = s.Metrics.Risk(ctx, s.Now())
	}
	// De Proxmox-hosts en hun inventaris (één keer per host).
	pveHosts := map[uuid.UUID]pvehost.Host{}
	inv := map[uuid.UUID]*protocol.PVEInventory{}
	if s.PVE != nil {
		if hs, _, err := s.PVE.Hosts(ctx); err == nil {
			for _, h := range hs {
				pveHosts[h.NodeID] = h
			}
		}
	}
	inventory := func(id uuid.UUID) *protocol.PVEInventory {
		if v, ok := inv[id]; ok {
			return v
		}
		inv[id] = nil
		h, ok := pveHosts[id]
		if !ok || h.Ready() != "" {
			return nil
		}
		i, err := s.PVE.Inventory(ctx, id)
		if err != nil {
			return nil
		}
		inv[id] = &i
		return &i
	}
	guestOf := func(n node) (*guest, string) {
		if s.PVE == nil || n.ProxmoxID == nil || n.VMID == nil {
			return nil, "ClusterForge weet niet op welke Proxmox-host deze node draait"
		}
		hostID, pveNode, g, err := s.PVE.FindGuest(ctx, *n.ProxmoxID, int(*n.VMID))
		if err != nil {
			return nil, err.Error()
		}
		out := &guest{host: hostID, pveNode: pveNode, g: g, storage: map[string]protocol.PVEStorage{}}
		if i := inventory(hostID); i != nil {
			for _, sto := range i.Storages {
				out.storage[sto.Name] = sto
			}
		}
		return out, ""
	}

	var out []Proposal
	for _, n := range nodes {
		th, ok := st[n.Environment]
		if !ok {
			th = st["lab"]
		}
		r := risk[n.ID]
		var gst *guest
		gstWhy := ""
		gstDone := false
		myGuest := func() (*guest, string) {
			if !gstDone {
				gst, gstWhy = guestOf(n)
				gstDone = true
			}
			return gst, gstWhy
		}
		base := Proposal{NodeID: n.ID, Node: n.Hostname, Environment: n.Environment, Href: "/nodes/detail?id=" + n.ID.String()}

		// 1. Schijven.
		for _, fs := range n.Facts.Filesystems {
			if fs.SizeBytes < 512<<20 {
				continue
			}
			pct := float64(fs.UsedBytes) / float64(fs.SizeBytes) * 100
			days := math.Inf(1)
			if r != nil {
				if d, ok := r.DiskDaysLeft[fs.Mount]; ok {
					days = d
				}
			}
			if pct < float64(th.DiskPct) && (th.DiskDays == 0 || days > float64(th.DiskDays)) {
				continue
			}
			p := base
			p.Kind, p.ID = KindDisk, "disk:"+n.ID.String()+":"+fs.Mount
			p.Level = "warn"
			if pct >= 95 || days <= 3 {
				p.Level = "crit"
			}
			p.Title = fmt.Sprintf("Schijf %s op %s is %.0f %% vol", fs.Mount, n.Hostname, math.Floor(pct))
			if !math.IsInf(days, 1) {
				p.Detail = fmt.Sprintf("Volgens de trend van zeven dagen vol over ongeveer %.0f dagen. ", math.Max(1, days))
			}
			// Een subvolume van een CT op een Proxmox-host, of de rootfs van
			// een CT zelf.
			if m := subvolRe.FindStringSubmatch(fs.Mount); m != nil {
				if _, isHost := pveHosts[n.ID]; isHost {
					vmid, _ := strconv.Atoi(m[1])
					if i := inventory(n.ID); i != nil {
						for _, g := range i.Guests {
							if g.VMID == vmid {
								sto := map[string]protocol.PVEStorage{}
								for _, x := range i.Storages {
									sto[x.Name] = x
								}
								p.Title = fmt.Sprintf("Schijf van CT %d (%s) is %.0f %% vol", vmid, g.Name, math.Floor(pct))
								s.diskFix(&p, &guest{host: n.ID, pveNode: pveHosts[n.ID].PVENode, g: g, storage: sto}, fs)
							}
						}
					}
				}
			} else if fs.Mount == "/" {
				if g, why := myGuest(); g != nil {
					s.diskFix(&p, g, fs)
				} else {
					p.NoFix = why
				}
			}
			if p.Fix == nil && p.NoFix == "" {
				p.NoFix = "geen schijf van een CT; kijk bij Schijfruimte wat ruimte inneemt"
			}
			out = append(out, p)
		}

		if r == nil {
			continue
		}
		cores := n.Facts.CPUs
		// 2. Swap.
		if r.SwapAvg != nil && *r.SwapAvg >= float64(th.SwapPct) && n.Facts.SwapBytes > 0 {
			p := base
			p.Kind, p.ID, p.Level = KindSwap, "swap:"+n.ID.String(), "warn"
			p.Title = fmt.Sprintf("Swap van %s zit gemiddeld %.0f %% vol", n.Hostname, *r.SwapAvg)
			if r.MemoryP95 != nil {
				p.Detail = fmt.Sprintf("Geheugen p95 %.0f %%. ", *r.MemoryP95)
				if *r.MemoryP95 < 50 {
					p.Detail += "Er is geheugen genoeg: de kernel swapt oude pagina's weg (swappiness). Meer swap geeft rust; vm.swappiness lager zetten kan ook. "
				}
			}
			if g, why := myGuest(); g != nil && g.g.Type == "lxc" {
				cur := g.g.SwapMiB
				want := max(cur*2, 512)
				if want > 8192 {
					want = max(cur+2048, 8192)
				}
				p.Fix = &Fix{Label: fmt.Sprintf("Swap van CT %d van %d naar %d MB", g.g.VMID, cur, want),
					Undo:       fmt.Sprintf("terug naar %d MB via Proxmox → Gasten → instellingen", cur),
					HostNodeID: &g.host, PVE: &protocol.PVERun{Action: protocol.PVESet, Type: "lxc", VMID: g.g.VMID, Set: &protocol.PVESetting{SwapMiB: &want}}}
			} else if g != nil {
				p.NoFix = "een VM: swap zit in het gastsysteem zelf"
			} else {
				p.NoFix = why
			}
			out = append(out, p)
		}
		// 3. Geheugen.
		if r.MemoryP95 != nil && *r.MemoryP95 >= float64(th.MemPct) {
			p := base
			p.Kind, p.ID, p.Level = KindMemory, "memory:"+n.ID.String(), "warn"
			p.Title = fmt.Sprintf("Geheugen van %s zat zeven dagen lang vaak boven %d %% (p95 %.0f %%)", n.Hostname, th.MemPct, *r.MemoryP95)
			if g, why := myGuest(); g != nil && g.g.MemoryMiB > 0 {
				cur := g.g.MemoryMiB
				want := cur + 1024
				p.Fix = &Fix{Label: fmt.Sprintf("Geheugen van %s %d van %d naar %d MB", guestWord(g.g), g.g.VMID, cur, want),
					Undo:       fmt.Sprintf("terug naar %d MB via Proxmox → Gasten → instellingen", cur),
					HostNodeID: &g.host, PVE: &protocol.PVERun{Action: protocol.PVESet, Type: g.g.Type, VMID: g.g.VMID, Set: &protocol.PVESetting{MemoryMiB: &want}}}
				if g.g.Type == "qemu" {
					p.Detail += "Bij een VM werkt meer geheugen pas na een herstart (tenzij hotplug aan staat). "
				}
			} else if why != "" {
				p.NoFix = why
			}
			out = append(out, p)
		} else if r.MemoryP95 != nil && *r.MemoryP95 < 25 && (r.SwapAvg == nil || *r.SwapAvg < 10) && n.Facts.MemoryBytes >= 4<<30 {
			p := base
			p.Kind, p.ID, p.Level = KindTip, "tip-memory:"+n.ID.String(), "info"
			p.Title = fmt.Sprintf("%s gebruikt zeven dagen lang hoogstens %.0f %% van zijn geheugen", n.Hostname, *r.MemoryP95)
			p.Detail = "Tip: minder geheugen geven kan. ClusterForge doet dat nooit zelf."
			out = append(out, p)
		}
		// 4. Load.
		if r.Load15Avg != nil && cores > 0 && *r.Load15Avg >= float64(cores)*th.LoadRatio {
			p := base
			p.Kind, p.ID, p.Level = KindCPU, "cpu:"+n.ID.String(), "warn"
			p.Title = fmt.Sprintf("Load van %s is zeven dagen gemiddeld %.1f bij %d kernen", n.Hostname, *r.Load15Avg, cores)
			if r.CPUP95 != nil && *r.CPUP95 < 50 {
				p.Detail = fmt.Sprintf("De CPU zelf is niet druk (p95 %.0f %%): de load komt dan van wachten op schijf of netwerk (I/O); een kern erbij helpt weinig. ", *r.CPUP95)
			}
			if g, why := myGuest(); g != nil && g.g.Cores > 0 {
				cur := g.g.Cores
				want := cur + 1
				p.Fix = &Fix{Label: fmt.Sprintf("%s %d van %d naar %d kernen", guestWord(g.g), g.g.VMID, cur, want),
					Undo:       fmt.Sprintf("terug naar %d kernen via Proxmox → Gasten → instellingen", cur),
					HostNodeID: &g.host, PVE: &protocol.PVERun{Action: protocol.PVESet, Type: g.g.Type, VMID: g.g.VMID, Set: &protocol.PVESetting{Cores: &want}}}
			} else if why != "" {
				p.NoFix = why
			}
			out = append(out, p)
		}
	}

	// 5. Containers die blijven herstarten, en waar ze op wachten.
	if s.Docker != nil {
		known := map[string]bool{}
		for _, n := range nodes {
			for _, ip := range nodeIPs(n.Facts) {
				known[ip] = true
			}
		}
		if hs, err := s.Docker.Hosts(ctx); err == nil {
			for _, h := range hs {
				if h.Summary == nil {
					continue
				}
				for _, c := range h.Summary.Problems {
					if c.Reason != "herstartlus" {
						continue
					}
					nid := h.NodeID
					p := Proposal{NodeID: h.NodeID, Node: h.Hostname, Environment: h.Environment,
						Kind: KindRestarts, ID: "restarts:" + h.NodeID.String() + ":" + c.Name, Level: "warn",
						Title: fmt.Sprintf("Container %s op %s blijft herstarten", c.Name, h.Hostname),
						Href:  "/logs?node=" + h.NodeID.String() + "&kind=docker&unit=" + url.QueryEscape(c.Name)}
					p.Detail = c.Status + ". "
					deps := s.dependencies(ctx, h.NodeID, c.ID, known)
					for _, d := range deps {
						p.Detail += d + ". "
					}
					p.Fix = &Fix{Label: "Container " + c.Name + " stoppen (tot de oorzaak hersteld is)", Undo: "start hem weer bij Containers",
						DockerNode: &nid, Container: c.Name}
					out = append(out, p)
				}
			}
		}
	}

	rank := map[string]int{"crit": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Level] != rank[out[j].Level] {
			return rank[out[i].Level] < rank[out[j].Level]
		}
		if out[i].Node != out[j].Node {
			return strings.ToLower(out[i].Node) < strings.ToLower(out[j].Node)
		}
		return out[i].ID < out[j].ID
	})
	if out == nil {
		out = []Proposal{}
	}
	return out, nil
}

func guestWord(g protocol.PVEGuest) string {
	if g.Type == "qemu" {
		return "VM"
	}
	return "CT"
}

// diskFix vult het voorstel om een schijf van een CT te vergroten: tot ze
// ongeveer 70 % vol is, minstens 2 GB erbij, als de storage dat heeft.
func (s *Service) diskFix(p *Proposal, g *guest, fs protocol.Filesystem) {
	if g.g.Type != "lxc" {
		p.NoFix = "een VM: de schijf vergroten vraagt ook het bestandssysteem in de VM te vergroten; doe dat in Proxmox"
		return
	}
	// De schijf van de CT die bij dit bestandssysteem past: rootfs voor /,
	// anders die met dezelfde grootte.
	var disk *protocol.PVEDisk
	sizeGiB := int(math.Round(float64(fs.SizeBytes) / (1 << 30)))
	for i, d := range g.g.Disks {
		if d.Name == "rootfs" && (fs.Mount == "/" || abs(d.SizeGiB-sizeGiB) <= 1) {
			disk = &g.g.Disks[i]
			break
		}
	}
	if disk == nil {
		for i, d := range g.g.Disks {
			if abs(d.SizeGiB-sizeGiB) <= 1 {
				disk = &g.g.Disks[i]
				break
			}
		}
	}
	if disk == nil {
		p.NoFix = "ClusterForge vond de schijf van de CT niet die bij dit bestandssysteem hoort"
		return
	}
	cur := disk.SizeGiB
	if cur == 0 {
		cur = sizeGiB
	}
	want := int(math.Ceil(float64(fs.UsedBytes) / 0.7 / (1 << 30)))
	add := max(want-cur, 2)
	if sto, ok := g.storage[disk.Storage]; ok && sto.Avail > 0 {
		if sto.Avail < int64(add)<<30+int64(add)<<27 {
			p.NoFix = fmt.Sprintf("storage %s heeft maar %.1f GB vrij; %d GB erbij past niet", disk.Storage, float64(sto.Avail)/(1<<30), add)
			return
		}
	}
	p.Detail += fmt.Sprintf("Voorstel: %s van CT %d van %d naar %d GB (op %s).", disk.Name, g.g.VMID, cur, cur+add, disk.Storage)
	h := g.host
	p.Fix = &Fix{Label: fmt.Sprintf("%s van CT %d vergroten met %d GB", disk.Name, g.g.VMID, add),
		Undo:       "een schijf kleiner maken kan niet; ClusterForge vergroot alleen",
		HostNodeID: &h, PVE: &protocol.PVERun{Action: protocol.PVEResize, Type: "lxc", VMID: g.g.VMID, Disk: disk.Name, Size: fmt.Sprintf("+%dG", add)}}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func nodeIPs(f protocol.Facts) []string {
	var out []string
	for _, i := range f.Interfaces {
		for _, a := range i.Addresses {
			ip, _, err := net.ParseCIDR(a)
			if err != nil {
				ip = net.ParseIP(a)
			}
			if ip != nil {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

// dbEnv zijn de omgevingsvariabelen die naar een database wijzen.
var dbEnv = regexp.MustCompile(`(?i)^([A-Z0-9]+__)?(DB|DATABASE|POSTGRES|POSTGRESQL|PG|MYSQL|MARIADB|REDIS|MONGO)[A-Z_]*(HOST|ADDR|URL|URI|SERVER)$`)

// dependencies zoekt in de omgeving van een container naar databases die
// hij nodig heeft, en zegt of ClusterForge dat adres kent (N26).
func (s *Service) dependencies(ctx context.Context, nodeID uuid.UUID, id string, known map[string]bool) []string {
	raw, err := s.Docker.Get(ctx, nodeID, "/containers/"+id+"/json", nil, true)
	if err != nil {
		return nil
	}
	var c struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return Dependencies(c.Config.Env, known)
}

// Dependencies haalt de database-adressen uit de omgeving van een container.
func Dependencies(env []string, known map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	port := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if strings.HasSuffix(strings.ToUpper(k), "PORT") {
			port[strings.TrimSuffix(strings.ToUpper(k), "PORT")] = v
		}
	}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if !dbEnv.MatchString(k) || v == "" {
			continue
		}
		host := v
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			host = u.Host
		}
		h, p, err := net.SplitHostPort(host)
		if err != nil {
			h = host
			pre := strings.ToUpper(k)
			for _, suf := range []string{"HOST", "ADDR", "SERVER"} {
				if strings.HasSuffix(pre, suf) {
					p = port[strings.TrimSuffix(pre, suf)]
				}
			}
		}
		addr := h
		if p != "" {
			addr = net.JoinHostPort(h, p)
		}
		if seen[addr] {
			continue
		}
		seen[addr] = true
		txt := fmt.Sprintf("wacht op database %s (%s)", addr, k)
		if ip := net.ParseIP(h); ip != nil && !known[ip.String()] {
			txt += ": geen enkele node van ClusterForge heeft dat adres; bestaat die server nog?"
		}
		out = append(out, txt)
	}
	sort.Strings(out)
	return out
}

// Apply voert de oplossing van een voorstel uit als taak.
func (s *Service) Apply(ctx context.Context, actor events.Actor, id string) (store.Job, error) {
	ps, err := s.Proposals(ctx, true)
	if err != nil {
		return store.Job{}, err
	}
	for _, p := range ps {
		if p.ID != id {
			continue
		}
		f := p.Fix
		switch {
		case f == nil:
			return store.Job{}, &FieldError{"voor dit voorstel is er geen knop: " + p.NoFix}
		case f.PVE != nil && f.HostNodeID != nil:
			j, err := s.PVE.Start(ctx, actor, pvehost.Request{NodeID: *f.HostNodeID, Op: *f.PVE})
			if err == nil {
				s.event(ctx, actor, p)
			}
			return j, err
		case f.DockerNode != nil && f.Container != "":
			j, err := s.Docker.StartCall(ctx, actor, *f.DockerNode, dockerhost.Call{Title: f.Label,
				Calls: []protocol.DockerCall{{Method: "POST", Path: "/containers/" + f.Container + "/stop", Title: "stoppen"}}})
			if err == nil {
				s.event(ctx, actor, p)
			}
			return j, err
		}
		return store.Job{}, &FieldError{"onbekende oplossing"}
	}
	return store.Job{}, ErrNotFound
}

func (s *Service) event(ctx context.Context, actor events.Actor, p Proposal) {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
	if s.ev == nil {
		return
	}
	_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "node", SubjectID: p.NodeID.String(), Action: "resources.fix_started",
		Payload: map[string]any{"proposal": p.ID, "fix": p.Fix.Label}})
}

// Peek geeft de laatste berekening zonder te wachten, en rekent op de
// achtergrond opnieuw als ze ouder is dan een minuut (voor Aandacht nodig).
func (s *Service) Peek() []Proposal {
	s.mu.Lock()
	out, stale := s.cached, s.cached == nil || s.Now().Sub(s.at) >= cacheFor
	start := stale && !s.busy
	if start {
		s.busy = true
	}
	s.mu.Unlock()
	if start {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = s.Proposals(ctx, true)
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()
	}
	return out
}
