package crowdsec

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Settings zijn de instellingen van de pagina CrowdSec.
type Settings struct {
	AutoApply      bool       `json:"auto_apply"`
	BouncerDefault bool       `json:"bouncer_default"`
	ShareBans      string     `json:"share_bans"`
	Whitelist      []string   `json:"whitelist"`
	UpdatedAt      time.Time  `json:"updated_at"`
	UpdatedBy      *uuid.UUID `json:"-"`
	// ConsoleKeySet: er is een enroll-sleutel voor de CrowdSec Console. De
	// sleutel zelf gaat nooit naar de browser.
	ConsoleKeySet bool `json:"console_key_set"`
	// ConsoleAvailable: de server heeft een masterkey om de sleutel te
	// bewaren.
	ConsoleAvailable bool `json:"console_available"`

	consoleKeyEnc  []byte
	consoleKeyID   string
	consoleKeyHash string
}

// Node is een node met een agent, met zijn CrowdSec-stand.
type Node struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	ClusterSlug string     `json:"cluster_slug"`
	ClusterEnv  string     `json:"cluster_environment"`
	Template    string     `json:"cluster_template"`
	Online      bool       `json:"online"`
	Protocol    int        `json:"protocol"`
	// Supported: de agent kent CrowdSec (protocol 28).
	Supported bool `json:"supported"`
	// Enabled: CrowdSec staat aan in ClusterForge (er is een rij).
	Enabled          bool                     `json:"enabled"`
	Managed          bool                     `json:"managed"`
	Bouncer          bool                     `json:"bouncer"`
	ExtraProfiles    []string                 `json:"extra_profiles"`
	DisabledProfiles []string                 `json:"disabled_profiles"`
	Status           *protocol.CrowdSecStatus `json:"status"`
	StatusAt         *time.Time               `json:"status_at"`
	Plan             *Plan                    `json:"plan"`
	PlanHash         string                   `json:"plan_hash"`
	AppliedHash      string                   `json:"applied_hash"`
	AppliedAt        *time.Time               `json:"applied_at"`
	ApplyJobID       *uuid.UUID               `json:"apply_job_id"`
	ApplyRunning     bool                     `json:"apply_running"`
	ApplyError       string                   `json:"apply_error"`
	ApplyFailedAt    *time.Time               `json:"apply_failed_at"`
	Blocklist        int                      `json:"blocklist"`
	PollAt           *time.Time               `json:"poll_at"`
	PollError        string                   `json:"poll_error"`
	// ConsoleEnrolled: aangemeld bij de CrowdSec Console met de huidige
	// sleutel.
	ConsoleEnrolled   bool       `json:"console_enrolled"`
	ConsoleEnrolledAt *time.Time `json:"console_enrolled_at"`
	ConsoleError      string     `json:"console_error"`
	ConsoleFailedAt   *time.Time `json:"console_failed_at"`

	consoleHash string
	facts       *protocol.Facts
	lastAlertAt *time.Time
}

// Pending: het plan is nog niet toegepast.
func (n Node) Pending() bool { return n.PlanHash != "" && n.PlanHash != n.AppliedHash }

// Signals is wat de node over zijn software zegt.
func (n Node) Signals() Signals {
	s := Signals{Units: map[string]bool{}, Template: n.Template}
	if f := n.facts; f != nil {
		for _, svc := range f.Services {
			if svc.Active == "active" || svc.Enabled == "enabled" {
				s.Units[svc.Name] = true
			}
		}
		s.Docker = f.Docker != nil
	}
	if st := n.Status; st != nil {
		s.Containers, s.Probes = st.Containers, st.Probes
	}
	return s
}

// Config is wat Jonas voor de node koos.
func (n Node) Config() NodeConfig {
	return NodeConfig{Bouncer: n.Bouncer, Extra: n.ExtraProfiles, Disabled: n.DisabledProfiles}
}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.pool.QueryRow(ctx, `SELECT auto_apply, bouncer_default, share_bans, whitelist, updated_at, updated_by,
		console_key_enc, console_key_id, console_key_hash FROM crowdsec_settings`).
		Scan(&st.AutoApply, &st.BouncerDefault, &st.ShareBans, &st.Whitelist, &st.UpdatedAt, &st.UpdatedBy,
			&st.consoleKeyEnc, &st.consoleKeyID, &st.consoleKeyHash)
	if errors.Is(err, pgx.ErrNoRows) {
		st, err = Settings{AutoApply: true, BouncerDefault: true, ShareBans: "all", Whitelist: DefaultWhitelist}, nil
	}
	st.ConsoleKeySet = len(st.consoleKeyEnc) > 0
	st.ConsoleAvailable = s.Box != nil
	return st, err
}

const nodesSQL = `
SELECT n.id, n.hostname, n.cluster_id,
       coalesce(c.name, ''), coalesce(c.slug, ''), coalesce(c.environment::text, ''), coalesce(c.template_name, ''),
       a.protocol_version, s.heartbeat_at, f.facts,
       cn.node_id IS NOT NULL, coalesce(cn.managed, false), coalesce(cn.bouncer, false),
       coalesce(cn.extra_profiles, '{}'), coalesce(cn.disabled_profiles, '{}'),
       cn.status, cn.status_at, cn.plan, coalesce(cn.plan_hash, ''), coalesce(cn.applied_hash, ''), cn.applied_at,
       cn.apply_job_id, coalesce(j.status::text IN ('queued', 'running'), false),
       coalesce(cn.apply_error, ''), cn.apply_failed_at, coalesce(cn.blocklist, -1),
       cn.poll_at, coalesce(cn.poll_error, ''), cn.last_alert_at,
       coalesce(cn.console_hash, ''), cn.console_enrolled_at, coalesce(cn.console_error, ''), cn.console_failed_at,
       coalesce((SELECT console_key_hash FROM crowdsec_settings), '')
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
LEFT JOIN crowdsec_nodes cn ON cn.node_id = n.id
LEFT JOIN jobs j ON j.id = cn.apply_job_id
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
		var curKey string
		if err := rows.Scan(&n.NodeID, &n.Hostname, &n.ClusterID, &n.ClusterName, &n.ClusterSlug, &n.ClusterEnv, &n.Template,
			&proto, &hb, &facts, &n.Enabled, &n.Managed, &n.Bouncer, &n.ExtraProfiles, &n.DisabledProfiles,
			&st, &n.StatusAt, &plan, &n.PlanHash, &n.AppliedHash, &n.AppliedAt, &n.ApplyJobID, &n.ApplyRunning,
			&n.ApplyError, &n.ApplyFailedAt, &n.Blocklist, &n.PollAt, &n.PollError, &n.lastAlertAt,
			&n.consoleHash, &n.ConsoleEnrolledAt, &n.ConsoleError, &n.ConsoleFailedAt, &curKey); err != nil {
			return nil, err
		}
		n.Protocol = int(proto)
		n.ConsoleEnrolled = curKey != "" && n.consoleHash == curKey
		n.Supported = n.Protocol >= protocol.CrowdSecSince
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		if len(facts) > 0 {
			var f protocol.Facts
			if json.Unmarshal(facts, &f) == nil {
				n.facts = &f
			}
		}
		if len(st) > 0 {
			var cs protocol.CrowdSecStatus
			if json.Unmarshal(st, &cs) == nil {
				n.Status = &cs
			}
		}
		if len(plan) > 0 {
			var p Plan
			if json.Unmarshal(plan, &p) == nil {
				n.Plan = &p
			}
		}
		if n.ExtraProfiles == nil {
			n.ExtraProfiles = []string{}
		}
		if n.DisabledProfiles == nil {
			n.DisabledProfiles = []string{}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
