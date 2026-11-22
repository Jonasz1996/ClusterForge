package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Settings zijn de instellingen van de pagina Firewall.
type Settings struct {
	AutoApply      bool       `json:"auto_apply"`
	LAN            []string   `json:"lan"`
	Admin          []string   `json:"admin"`
	SSHFrom        string     `json:"ssh_from"`
	UnknownPorts   string     `json:"unknown_ports"`
	Strict         bool       `json:"strict"`
	ConfirmSeconds int        `json:"confirm_seconds"`
	GraceHours     int        `json:"grace_hours"`
	Monitor        bool       `json:"monitor"`
	UpdatedAt      time.Time  `json:"updated_at"`
	UpdatedBy      *uuid.UUID `json:"-"`
}

// DefaultLAN zijn de eigen netwerken als er niets ingesteld is.
var DefaultLAN = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}

func (s Settings) plan() PlanSettings {
	return PlanSettings{LAN: s.LAN, Admin: s.Admin, SSHFrom: s.SSHFrom, Unknown: s.UnknownPorts, Strict: s.Strict}
}

// Node is een node met een agent, met zijn firewallstand.
type Node struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	Address     string     `json:"address"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	ClusterSlug string     `json:"cluster_slug"`
	ClusterEnv  string     `json:"cluster_environment"`
	Template    string     `json:"cluster_template"`
	Online      bool       `json:"online"`
	Protocol    int        `json:"protocol"`
	// Supported: de agent kent de firewall (protocol 30) en de node is
	// een gewone Linux-node (geen OPNsense of TrueNAS).
	Supported bool `json:"supported"`
	// Unsupported zegt waarom niet.
	Unsupported string `json:"unsupported,omitempty"`
	// Mode is off, preview of managed.
	Mode          string                   `json:"mode"`
	Status        *protocol.FirewallStatus `json:"status"`
	StatusAt      *time.Time               `json:"status_at"`
	StatusError   string                   `json:"status_error"`
	Plan          *Plan                    `json:"plan"`
	PlanHash      string                   `json:"plan_hash"`
	AppliedHash   string                   `json:"applied_hash"`
	AppliedAt     *time.Time               `json:"applied_at"`
	ApplyJobID    *uuid.UUID               `json:"apply_job_id"`
	ApplyRunning  bool                     `json:"apply_running"`
	ApplyError    string                   `json:"apply_error"`
	ApplyFailedAt *time.Time               `json:"apply_failed_at"`
	BlockedAt     *time.Time               `json:"blocked_at"`
	BlockedSource string                   `json:"blocked_source"`
	// Diff is het verschil tussen de regels op de node en het plan.
	Diff *protocol.FirewallDiff `json:"diff"`
	// Blocked24h is het aantal geblokkeerde pakketten in 24 uur.
	Blocked24h int64 `json:"blocked_24h"`

	facts *protocol.Facts
}

// Pending: het plan is nog niet toegepast.
func (n Node) Pending() bool { return n.PlanHash != "" && n.PlanHash != n.AppliedHash }

// Enabled: ClusterForge kijkt naar de firewall van deze node.
func (n Node) Enabled() bool { return n.Mode == "preview" || n.Mode == "managed" }

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.pool.QueryRow(ctx, `SELECT auto_apply, lan, admin, ssh_from, unknown_ports, strict, confirm_seconds, grace_hours, monitor,
		updated_at, updated_by FROM firewall_settings`).
		Scan(&st.AutoApply, &st.LAN, &st.Admin, &st.SSHFrom, &st.UnknownPorts, &st.Strict, &st.ConfirmSeconds, &st.GraceHours, &st.Monitor,
			&st.UpdatedAt, &st.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{AutoApply: true, LAN: DefaultLAN, Admin: []string{}, SSHFrom: ClassLAN, UnknownPorts: ClassLAN, Strict: true,
			ConfirmSeconds: 120, GraceHours: 24, Monitor: true}, nil
	}
	if st.Admin == nil {
		st.Admin = []string{}
	}
	return st, err
}

const nodesSQL = `
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), ''), n.cluster_id,
       coalesce(c.name, ''), coalesce(c.slug, ''), coalesce(c.environment::text, ''), coalesce(c.template_name, ''),
       a.protocol_version, s.heartbeat_at, f.facts,
       coalesce(fn.mode, 'off'), fn.status, fn.status_at, coalesce(fn.status_error, ''), fn.plan, coalesce(fn.plan_hash, ''),
       coalesce(fn.applied_hash, ''), fn.applied_at, fn.apply_job_id, coalesce(j.status::text IN ('queued', 'running'), false),
       coalesce(fn.apply_error, ''), fn.apply_failed_at, fn.blocked_at, coalesce(fn.blocked_source, ''),
       coalesce((SELECT sum(b.count) FROM firewall_blocked b WHERE b.node_id = n.id AND b.hour > now() - interval '24 hours'), 0)::bigint
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
LEFT JOIN firewall_nodes fn ON fn.node_id = n.id
LEFT JOIN jobs j ON j.id = fn.apply_job_id
`

// Nodes geeft alle nodes met een agent.
func (s *Service) Nodes(ctx context.Context) ([]Node, error) {
	return s.nodes(ctx, nodesSQL+` ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname)`)
}

// Node geeft één node.
func (s *Service) Node(ctx context.Context, id uuid.UUID) (Node, error) {
	ns, err := s.nodes(ctx, nodesSQL+` WHERE n.id = $1`, id)
	if err != nil {
		return Node{}, err
	}
	if len(ns) == 0 {
		return Node{}, ErrNotFound
	}
	return ns[0], nil
}

// ErrNotFound: geen node met een agent.
var ErrNotFound = errors.New("node niet gevonden of zonder agent")

func (s *Service) nodes(ctx context.Context, sql string, args ...any) ([]Node, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var out []Node
	for rows.Next() {
		var n Node
		var proto int32
		var hb *time.Time
		var facts, st, plan []byte
		if err := rows.Scan(&n.NodeID, &n.Hostname, &n.Address, &n.ClusterID, &n.ClusterName, &n.ClusterSlug, &n.ClusterEnv, &n.Template,
			&proto, &hb, &facts, &n.Mode, &st, &n.StatusAt, &n.StatusError, &plan, &n.PlanHash,
			&n.AppliedHash, &n.AppliedAt, &n.ApplyJobID, &n.ApplyRunning,
			&n.ApplyError, &n.ApplyFailedAt, &n.BlockedAt, &n.BlockedSource, &n.Blocked24h); err != nil {
			return nil, err
		}
		n.Protocol = int(proto)
		n.Supported = n.Protocol >= protocol.FirewallSince
		if !n.Supported {
			n.Unsupported = "agent te oud (protocol 30 nodig; Nodes → Agents bijwerken)"
		}
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		if len(facts) > 0 {
			var f protocol.Facts
			if json.Unmarshal(facts, &f) == nil {
				n.facts = &f
				if app := f.Appliance(); app != "" {
					// Eigen firewall: een automatische toepassing zou
					// elk uur opnieuw mislukken.
					n.Supported = false
					n.Unsupported = app + " heeft een eigen firewall; ClusterForge beheert daar geen ufw"
				}
				if n.Address == "" {
					n.Address = f.PrimaryAddress
				}
			}
		}
		if len(st) > 0 {
			var fs protocol.FirewallStatus
			if json.Unmarshal(st, &fs) == nil {
				n.Status = &fs
			}
		}
		if len(plan) > 0 {
			var p Plan
			if json.Unmarshal(plan, &p) == nil {
				n.Plan = &p
			}
		}
		if n.Status != nil && n.Plan != nil {
			d := protocol.DiffFirewall(n.Status.Rules, n.Plan.Rules, n.Plan.Strict)
			n.Diff = &d
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// addresses zijn de IP-adressen van een node: het primaire adres en de
// adressen op zijn echte interfaces (niet Docker, niet loopback).
func (n Node) addresses() []string {
	var out []string
	add := func(s string) {
		if a, err := netip.ParseAddr(s); err == nil && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !slices.Contains(out, a.String()) {
			out = append(out, a.String())
		}
	}
	add(n.Address)
	if f := n.facts; f != nil {
		add(f.PrimaryAddress)
		for _, i := range f.Interfaces {
			if i.Name == "lo" || i.Name == "docker0" || strings.HasPrefix(i.Name, "br-") || strings.HasPrefix(i.Name, "veth") ||
				strings.HasPrefix(i.Name, "cni") || strings.HasPrefix(i.Name, "flannel") || strings.HasPrefix(i.Name, "cali") {
				continue
			}
			for _, a := range i.Addresses {
				if p, err := netip.ParsePrefix(a); err == nil {
					add(p.Addr().String())
				}
			}
		}
	}
	return out
}

// units zijn de systemd-units die actief of enabled zijn.
func (n Node) units() []string {
	var out []string
	if f := n.facts; f != nil {
		for _, svc := range f.Services {
			if svc.Active == "active" || svc.Enabled == "enabled" {
				out = append(out, svc.Name)
			}
		}
	}
	return out
}
