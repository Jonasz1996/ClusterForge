// Package topo is één model van de infrastructuur onder ClusterForge: de
// Proxmox-hosts met hun storage, de VM's en containers met hun schijven en
// netwerkkaarten, de nodes die erop draaien, en waar ClusterForge zelf
// draait. Het model rekent niet; de wat-als en later de kaart lezen het.
package topo

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Model is de infrastructuur zoals de laatste sync en de gastconfiguraties
// haar zien.
type Model struct {
	Connections []Connection
	Hosts       []Host
	Storages    []Storage
	Guests      []Guest
	Self        Self
}

// Connection is een Proxmox-koppeling. Read is false als de laatste sync
// mislukte of nog nooit liep; dan is wat er staat misschien oud.
type Connection struct {
	ID   uuid.UUID
	Name string
	Read bool
}

// Host is een Proxmox-host.
type Host struct {
	Conn   uuid.UUID
	Name   string
	Online bool
	MaxMem int64
	Mem    int64
}

// Storage is een storage zoals één host hem ziet. Gedeelde storage staat er
// per host één keer.
type Storage struct {
	Conn   uuid.UUID
	Name   string
	Host   string
	Shared bool
	Type   string
}

// Guest is een VM (qemu) of container (lxc).
type Guest struct {
	Conn     uuid.UUID
	VMID     int
	Type     string
	Name     string
	Host     string
	Status   string
	Template bool
	// HAState is de toestand bij Proxmox HA, zoals started; leeg zonder HA.
	HAState string
	MaxMem  int64
	Pool    string
	// NodeID en Hostname zijn de node van ClusterForge op deze VM.
	NodeID   *uuid.UUID
	Hostname string
	// Config is nil zolang de configuratie niet gelezen is.
	Config *Config
}

// Config is de gelezen configuratie van een gast.
type Config struct {
	Disks     []proxmox.Disk
	NICs      []proxmox.NIC
	OnBoot    bool
	Addresses []string
	// Error is de fout van de laatste lezing; de gegevens zijn dan van
	// daarvoor.
	Error  string
	ReadAt time.Time
}

// HA is true als Proxmox HA de gast herstart wanneer zijn host uitvalt.
func (g Guest) HA() bool { return g.HAState == "started" || g.HAState == "enabled" }

// Label is de naam van een gast zoals ClusterForge hem toont: de hostnaam
// van de node, anders de naam in Proxmox, met het VMID.
func (g Guest) Label() string {
	name := g.Name
	if g.Hostname != "" {
		name = g.Hostname
	}
	if name == "" {
		return "VM " + itoa(g.VMID)
	}
	return name + " (" + itoa(g.VMID) + ")"
}

// ConnName geeft de naam van een koppeling.
func (m Model) ConnName(id uuid.UUID) string {
	for _, c := range m.Connections {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// Shared is true als een storage van de koppeling gedeeld is.
func (m Model) Shared(conn uuid.UUID, name string) bool {
	for _, s := range m.Storages {
		if s.Conn == conn && s.Name == name && s.Shared {
			return true
		}
	}
	return false
}

// Guest zoekt een gast op VMID.
func (m Model) Guest(conn uuid.UUID, vmid int) (Guest, bool) {
	for _, g := range m.Guests {
		if g.Conn == conn && g.VMID == vmid {
			return g, true
		}
	}
	return Guest{}, false
}

// Bridge is een bridge op een host, met de gasten die eraan hangen. Tot het
// netwerk van de hosts gelezen wordt (mijlpaal 7), komen bridges alleen uit
// de netwerkkaarten van de gasten.
type Bridge struct {
	Conn uuid.UUID
	Host string
	Name string
}

// Bridges geeft de bridges per host die een gast gebruikt.
func (m Model) Bridges() []Bridge {
	seen := map[Bridge]bool{}
	var out []Bridge
	for _, g := range m.Guests {
		if g.Config == nil || g.Template {
			continue
		}
		for _, n := range g.Config.NICs {
			b := Bridge{g.Conn, g.Host, n.Bridge}
			if n.Bridge == "" || seen[b] {
				continue
			}
			seen[b] = true
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Bridge) int {
		return cmp.Or(cmp.Compare(a.Conn.String(), b.Conn.String()), cmp.Compare(a.Host, b.Host), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// Service laadt het model uit de database. Waar ClusterForge zelf draait,
// zoekt ze aan de adressen van deze server en de adressen waarmee de nodes
// hem bereiken (Advertise en het adres bij de Git-koppeling).
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	// Advertise is CF_NATS_ADVERTISE.
	Advertise string
	// Own geeft de adressen van deze server; tests zetten hem vast.
	Own func(ctx context.Context, hints []string) Own

	mu    sync.Mutex
	own   Own
	ownAt time.Time
	key   string
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, advertise string) *Service {
	return &Service{pool: pool, q: store.New(pool), ev: ev, log: log, Advertise: advertise, Own: OwnAddresses}
}

// ownTTL is hoe lang de eigen adressen geldig blijven.
const ownTTL = 10 * time.Minute

// Load leest het model.
func (s *Service) Load(ctx context.Context) (Model, error) {
	var m Model
	conns, err := s.q.ListTopoConnections(ctx)
	if err != nil {
		return m, err
	}
	for _, c := range conns {
		m.Connections = append(m.Connections, Connection{ID: c.ID, Name: c.Name, Read: c.LastSyncAt != nil && c.LastError == ""})
	}
	rows, err := s.q.ListTopoResources(ctx)
	if err != nil {
		return m, err
	}
	cfgs, err := s.q.ListGuestConfigs(ctx)
	if err != nil {
		return m, err
	}
	type key struct {
		conn uuid.UUID
		vmid int32
	}
	byVM := map[key]*Config{}
	for _, c := range cfgs {
		cfg := &Config{OnBoot: c.Onboot, Addresses: c.Addresses, Error: c.Error, ReadAt: c.ReadAt}
		_ = json.Unmarshal(c.Disks, &cfg.Disks)
		_ = json.Unmarshal(c.Nets, &cfg.NICs)
		byVM[key{c.ConnectionID, c.Vmid}] = cfg
	}
	for _, r := range rows {
		var d proxmox.Resource
		_ = json.Unmarshal(r.Data, &d)
		switch r.Type {
		case "node":
			m.Hosts = append(m.Hosts, Host{Conn: r.ConnectionID, Name: r.PveNode, Online: r.Status == "online", MaxMem: d.MaxMem, Mem: d.Mem})
		case "storage":
			m.Storages = append(m.Storages, Storage{Conn: r.ConnectionID, Name: r.Name, Host: r.PveNode, Shared: d.Shared == 1, Type: d.Plugin})
		case "qemu", "lxc":
			if r.Vmid == nil {
				continue
			}
			g := Guest{
				Conn: r.ConnectionID, VMID: int(*r.Vmid), Type: r.Type, Name: r.Name, Host: r.PveNode, Status: r.Status,
				Template: r.Template, HAState: d.HAState, MaxMem: d.MaxMem, Pool: d.Pool, NodeID: r.NodeID,
			}
			if r.NodeHostname != nil {
				g.Hostname = *r.NodeHostname
			}
			if c := byVM[key{r.ConnectionID, *r.Vmid}]; c != nil && !c.ReadAt.IsZero() && (c.Error == "" || c.Disks != nil || c.NICs != nil) {
				g.Config = c
			}
			m.Guests = append(m.Guests, g)
		}
	}
	manual, err := s.q.GetSelfLocation(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		m.Self = Locate(m, s.ownAddresses(ctx), nil)
	case err != nil:
		return m, err
	default:
		m.Self = Locate(m, Own{}, &Manual{Conn: manual.ConnectionID, VMID: int(manual.Vmid)})
	}
	return m, nil
}

// ownAddresses geeft de eigen adressen, hoogstens elke ownTTL opnieuw
// opgezocht, of meteen als de hints veranderden.
func (s *Service) ownAddresses(ctx context.Context) Own {
	hints := []string{}
	if s.Advertise != "" {
		hints = append(hints, s.Advertise)
	}
	if urls, err := s.q.ListGitServerURLs(ctx); err == nil {
		hints = append(hints, urls...)
	}
	key, _ := json.Marshal(hints)
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(key) != s.key || time.Since(s.ownAt) >= ownTTL {
		s.own, s.ownAt, s.key = s.Own(ctx, hints), time.Now(), string(key)
	}
	return s.own
}

// ErrNotFound: de VM staat niet in Proxmox.
var ErrNotFound = errors.New("VM niet gevonden in Proxmox")

// SetSelf vult in op welke VM of container ClusterForge draait. Die moet in
// de laatste sync staan en geen template zijn.
func (s *Service) SetSelf(ctx context.Context, actor events.Actor, by *uuid.UUID, conn uuid.UUID, vmid int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		id := int32(vmid)
		g, err := q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: conn, Vmid: &id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if g.Template {
			return ErrNotFound
		}
		c, err := q.GetProxmoxConnection(ctx, conn)
		if err != nil {
			return err
		}
		if err := q.SetSelfLocation(ctx, store.SetSelfLocationParams{ConnectionID: conn, Vmid: int32(vmid), UpdatedBy: by}); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "proxmox", SubjectID: conn.String(), Action: "proxmox.self_location_set",
			Payload: map[string]any{"name": c.Name, "vmid": vmid, "guest": g.Name},
		})
	})
}

// ClearSelf haalt de ingevulde VM weg; ClusterForge zoekt dan zelf.
func (s *Service) ClearSelf(ctx context.Context, actor events.Actor) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetSelfLocation(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := q.DeleteSelfLocation(ctx); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "proxmox", SubjectID: cur.ConnectionID.String(), Action: "proxmox.self_location_cleared",
			Payload: map[string]any{"vmid": cur.Vmid},
		})
	})
}
