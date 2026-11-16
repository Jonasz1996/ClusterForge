package security

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

// ErrNotFound: geen bevinding, scope of node.
var ErrNotFound = errors.New("niet gevonden")

// Node is een node met een agent, met de stand van zijn pakketlijst.
type Node struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	Address     string     `json:"address"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	ClusterSlug string     `json:"cluster_slug"`
	Online      bool       `json:"online"`
	Protocol    int        `json:"protocol"`
	// Supported: de agent kent het onderwerp security (protocol 40).
	Supported   bool       `json:"supported"`
	OS          string     `json:"os"`
	Codename    string     `json:"codename"`
	Kernel      string     `json:"kernel"`
	Dpkg        bool       `json:"dpkg"`
	Packages    int        `json:"packages"`
	CollectedAt *time.Time `json:"collected_at"`
	Error       string     `json:"error"`
	ErrorAt     *time.Time `json:"error_at"`
	// Checked: er is een pakketlijst van een Debian-node en een feed voor
	// zijn release.
	Checked bool `json:"checked"`
	// Note zegt waarom een node niet gecontroleerd wordt.
	Note    string         `json:"note"`
	Counts  map[string]int `json:"counts"`
	KEV     int            `json:"kev"`
	Fixable int            `json:"fixable"`

	// Verharding (M2, protocol 41).
	AuditSupported bool       `json:"audit_supported"`
	AuditAt        *time.Time `json:"audit_at"`
	AuditError     string     `json:"audit_error"`
	auditErrorAt   *time.Time
	HardeningScore *int   `json:"hardening_score"`
	Virtualization string `json:"virtualization"`
	docker, router bool
}

const nodesSQL = `
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), ''), n.cluster_id, coalesce(c.name, ''), coalesce(c.slug, ''),
       a.protocol_version, s.heartbeat_at,
       coalesce(i.os_id, ''), coalesce(i.codename, ''), coalesce(i.kernel, ''), coalesce(i.dpkg, false),
       coalesce(jsonb_array_length(i.packages), 0), i.collected_at, coalesce(i.error, ''), i.error_at,
       coalesce(f.facts->'os'->>'id', ''), coalesce(f.facts->'os'->>'codename', ''),
       h.collected_at, coalesce(h.error, ''), h.error_at, h.score, coalesce(f.facts->>'virtualization', ''),
       coalesce(jsonb_typeof(f.facts->'docker') = 'object', false),
       coalesce(jsonb_typeof(f.facts->'keepalived') = 'object', false)
         OR coalesce(f.facts->'services' @> '[{"name":"k3s"}]', false) OR coalesce(f.facts->'services' @> '[{"name":"k3s-agent"}]', false)
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN node_facts f ON f.node_id = n.id
LEFT JOIN security_inventory i ON i.node_id = n.id
LEFT JOIN security_hardening h ON h.node_id = n.id
`

func (s *Service) nodes(ctx context.Context, where string, args ...any) ([]Node, error) {
	rows, err := s.pool.Query(ctx, nodesSQL+where+` ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname)`, args...)
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
		var factsOS, factsCode string
		if err := rows.Scan(&n.NodeID, &n.Hostname, &n.Address, &n.ClusterID, &n.ClusterName, &n.ClusterSlug, &proto, &hb,
			&n.OS, &n.Codename, &n.Kernel, &n.Dpkg, &n.Packages, &n.CollectedAt, &n.Error, &n.ErrorAt, &factsOS, &factsCode,
			&n.AuditAt, &n.AuditError, &n.auditErrorAt, &n.HardeningScore, &n.Virtualization, &n.docker, &n.router); err != nil {
			return nil, err
		}
		n.Protocol = int(proto)
		n.Supported = n.Protocol >= protocol.SecuritySince
		n.AuditSupported = n.Protocol >= protocol.SecurityAuditSince
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		if n.OS == "" {
			n.OS, n.Codename = factsOS, factsCode
		}
		n.Counts = map[string]int{}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) saveInventory(ctx context.Context, id uuid.UUID, inv *protocol.SecurityInventory) error {
	pk, err := json.Marshal(inv.Packages)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO security_inventory (node_id, collected_at, os_id, codename, kernel, dpkg, packages, error, error_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, '', NULL)
ON CONFLICT (node_id) DO UPDATE SET collected_at = $2, os_id = $3, codename = $4, kernel = $5, dpkg = $6, packages = $7,
    error = '', error_at = NULL`, id, s.Now(), inv.OS.ID, inv.OS.Codename, inv.Kernel, inv.Dpkg, pk)
	return err
}

func (s *Service) inventoryFailed(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO security_inventory (node_id, error, error_at) VALUES ($1, $2, $3)
ON CONFLICT (node_id) DO UPDATE SET error = $2, error_at = $3`, id, msg, s.Now())
	return err
}

func (s *Service) loadInventory(ctx context.Context, id uuid.UUID) (osID, codename string, pkgs []protocol.DebPackage, ok bool, err error) {
	var raw []byte
	var at *time.Time
	err = s.pool.QueryRow(ctx, `SELECT os_id, codename, packages, collected_at FROM security_inventory WHERE node_id = $1`, id).
		Scan(&osID, &codename, &raw, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil, false, nil
	}
	if err != nil || at == nil {
		return "", "", nil, false, err
	}
	if err := json.Unmarshal(raw, &pkgs); err != nil {
		return "", "", nil, false, err
	}
	return osID, codename, pkgs, true, nil
}

// Feed is de stand van een feed.
type Feed struct {
	Name      string     `json:"name"`
	Label     string     `json:"label"`
	URL       string     `json:"url"`
	FetchedAt *time.Time `json:"fetched_at"`
	OKAt      *time.Time `json:"ok_at"`
	Error     string     `json:"error"`
	Entries   int        `json:"entries"`
	etag      string
}

func (s *Service) feeds(ctx context.Context) (map[string]*Feed, error) {
	out := map[string]*Feed{
		FeedDebian: {Name: FeedDebian, Label: "Debian Security Tracker", URL: s.DebianURL},
		FeedKEV:    {Name: FeedKEV, Label: "CISA Known Exploited Vulnerabilities", URL: s.KEVURL},
	}
	rows, err := s.pool.Query(ctx, `SELECT name, fetched_at, ok_at, error, entries, etag FROM security_feeds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.Name, &f.FetchedAt, &f.OKAt, &f.Error, &f.Entries, &f.etag); err != nil {
			return nil, err
		}
		if o := out[f.Name]; o != nil {
			o.FetchedAt, o.OKAt, o.Error, o.Entries, o.etag = f.FetchedAt, f.OKAt, f.Error, f.Entries, f.etag
		}
	}
	return out, rows.Err()
}

func (s *Service) feedDone(ctx context.Context, name string, entries int, etag string, ferr error) error {
	now := s.Now()
	if ferr != nil {
		_, err := s.pool.Exec(ctx, `
INSERT INTO security_feeds (name, fetched_at, error) VALUES ($1, $2, $3)
ON CONFLICT (name) DO UPDATE SET fetched_at = $2, error = $3`, name, now, trimText(ferr.Error(), 300))
		return err
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO security_feeds (name, fetched_at, ok_at, error, entries, etag) VALUES ($1, $2, $2, '', $3, $4)
ON CONFLICT (name) DO UPDATE SET fetched_at = $2, ok_at = $2, error = '',
    entries = CASE WHEN $3 < 0 THEN security_feeds.entries ELSE $3 END, etag = $4`, name, now, entries, etag)
	return err
}

func (s *Service) saveAudit(ctx context.Context, id uuid.UUID, au *protocol.SecurityAudit, results []RuleResult, score int) error {
	a, err := json.Marshal(au)
	if err != nil {
		return err
	}
	r, err := json.Marshal(results)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO security_hardening (node_id, collected_at, audit, results, score, error, error_at)
VALUES ($1, $2, $3, $4, $5, '', NULL)
ON CONFLICT (node_id) DO UPDATE SET collected_at = $2, audit = $3, results = $4, score = $5, error = '', error_at = NULL`,
		id, s.Now(), a, r, score)
	return err
}

func (s *Service) auditFailed(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO security_hardening (node_id, error, error_at) VALUES ($1, $2, $3)
ON CONFLICT (node_id) DO UPDATE SET error = $2, error_at = $3`, id, msg, s.Now())
	return err
}
