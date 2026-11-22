package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Statussen van een bevinding.
const (
	StatusOpen     = "open"
	StatusAccepted = "aanvaard"
	StatusResolved = "opgelost"
)

// Finding is een bevinding zoals de GUI hem toont.
type Finding struct {
	ID            uuid.UUID       `json:"id"`
	Kind          string          `json:"kind"`
	Subject       string          `json:"subject"`
	Key           string          `json:"key"`
	NodeID        *uuid.UUID      `json:"node_id"`
	Hostname      string          `json:"hostname"`
	ClusterName   string          `json:"cluster_name"`
	ClusterSlug   string          `json:"cluster_slug"`
	Severity      string          `json:"severity"`
	Title         string          `json:"title"`
	Summary       string          `json:"summary"`
	Fix           string          `json:"fix"`
	Detail        json.RawMessage `json:"detail"`
	Status        string          `json:"status"`
	FirstSeen     time.Time       `json:"first_seen"`
	LastSeen      time.Time       `json:"last_seen"`
	ResolvedAt    *time.Time      `json:"resolved_at"`
	AcceptedAt    *time.Time      `json:"accepted_at"`
	AcceptedUntil *time.Time      `json:"accepted_until"`
	AcceptedNote  string          `json:"accepted_note"`
	AcceptedBy    string          `json:"accepted_by"`
}

// sync zet de bevindingen van één soort voor één node gelijk aan drafts:
// nieuwe erbij, bestaande bijgewerkt (een opgeloste die terugkomt gaat
// weer open), wat ontbreekt is opgelost.
func (s *Service) sync(ctx context.Context, n Node, kind string, drafts []Draft) error {
	id := n.NodeID
	return s.syncSubject(ctx, kind, n.NodeID.String(), &id, n.ClusterID, n.Hostname, drafts)
}

// syncSubject is sync voor een willekeurig subject; nodeID mag nil zijn
// (een adres uit een netwerkscan dat geen node is).
func (s *Service) syncSubject(ctx context.Context, kind, subject string, nodeID, clusterID *uuid.UUID, hostname string, drafts []Draft) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	prev := map[string]bool{} // key → was al open met KEV
	rows, err := tx.Query(ctx, `SELECT key, status <> 'opgelost' AND coalesce((detail->>'kev')::int, 0) > 0
FROM security_findings WHERE kind = $1 AND subject = $2`, kind, subject)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k string
		var kev bool
		if err := rows.Scan(&k, &kev); err != nil {
			rows.Close()
			return err
		}
		prev[k] = kev
	}
	rows.Close()
	now := s.Now()
	keys := make([]string, 0, len(drafts))
	var newKEV []string
	for _, d := range drafts {
		detail, err := json.Marshal(d.Detail)
		if err != nil {
			return err
		}
		keys = append(keys, d.Key)
		if _, err := tx.Exec(ctx, `
INSERT INTO security_findings (kind, subject, key, node_id, severity, title, summary, fix, detail, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
ON CONFLICT (kind, subject, key) DO UPDATE SET node_id = $4, severity = $5, title = $6, summary = $7, fix = $8, detail = $9,
    last_seen = $10,
    first_seen = CASE WHEN security_findings.status = 'opgelost' THEN $10 ELSE security_findings.first_seen END,
    status = CASE WHEN security_findings.status = 'opgelost' THEN 'open' ELSE security_findings.status END,
    resolved_at = NULL`,
			kind, subject, d.Key, nodeID, d.Severity, d.Title, d.Summary, d.Fix, detail, now); err != nil {
			return err
		}
		if d.KEV && !prev[d.Key] {
			newKEV = append(newKEV, d.Key)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE security_findings SET status = 'opgelost', resolved_at = $4
WHERE kind = $1 AND subject = $2 AND status <> 'opgelost' AND NOT (key = ANY($3))`, kind, subject, keys, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, k := range newKEV {
		s.event(ctx, events.System(), "node", subject, clusterID, "security.kev_found",
			map[string]any{"hostname": hostname, "package": k})
	}
	return nil
}

// FindingFilter beperkt de lijst.
type FindingFilter struct {
	// Status: open (standaard: open + aanvaard), aanvaard, opgelost, alle.
	Status string
	NodeID *uuid.UUID
	Kind   string
	// Subjects: alleen deze onderwerpen (bv. "ip:192.168.10.5").
	Subjects []string
}

const findingsSQL = `
SELECT f.id, f.kind, f.subject, f.key, f.node_id, coalesce(n.hostname, ''), coalesce(c.name, ''), coalesce(c.slug, ''),
       f.severity, f.title, f.summary, f.fix, f.detail, f.status, f.first_seen, f.last_seen, f.resolved_at,
       f.accepted_at, f.accepted_until, f.accepted_note, coalesce(u.username, '')
FROM security_findings f
LEFT JOIN nodes n ON n.id = f.node_id
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN users u ON u.id = f.accepted_by
`

const severityOrder = `CASE f.severity WHEN 'kritiek' THEN 0 WHEN 'hoog' THEN 1 WHEN 'middel' THEN 2 WHEN 'laag' THEN 3 ELSE 4 END`

// MaxFindings begrenst de lijst.
const MaxFindings = 2000

// Findings geeft de bevindingen, ernstigste eerst.
func (s *Service) Findings(ctx context.Context, f FindingFilter) ([]Finding, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}
	switch f.Status {
	case StatusAccepted, StatusResolved:
		add("f.status = ?", f.Status)
	case "alle":
	case StatusOpen:
		add("f.status = ?", StatusOpen)
	default:
		where = append(where, "f.status <> 'opgelost'")
	}
	if f.NodeID != nil {
		add("f.node_id = ?", *f.NodeID)
	}
	if f.Kind != "" {
		add("f.kind = ?", f.Kind)
	}
	if f.Subjects != nil {
		add("f.subject = ANY(?)", f.Subjects)
	}
	q := findingsSQL
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY " + severityOrder + ", coalesce((f.detail->>'kev')::int, 0) DESC, lower(coalesce(n.hostname, f.subject)), f.key LIMIT " + itoa(MaxFindings)
	return s.scanFindings(ctx, q, args...)
}

func itoa(n int) string { return strconv.Itoa(n) }

func (s *Service) scanFindings(ctx context.Context, q string, args ...any) ([]Finding, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Finding{}
	for rows.Next() {
		var f Finding
		var detail []byte
		if err := rows.Scan(&f.ID, &f.Kind, &f.Subject, &f.Key, &f.NodeID, &f.Hostname, &f.ClusterName, &f.ClusterSlug,
			&f.Severity, &f.Title, &f.Summary, &f.Fix, &detail, &f.Status, &f.FirstSeen, &f.LastSeen, &f.ResolvedAt,
			&f.AcceptedAt, &f.AcceptedUntil, &f.AcceptedNote, &f.AcceptedBy); err != nil {
			return nil, err
		}
		f.Detail = detail
		out = append(out, f)
	}
	return out, rows.Err()
}

// Finding geeft één bevinding.
func (s *Service) Finding(ctx context.Context, id uuid.UUID) (Finding, error) {
	fs, err := s.scanFindings(ctx, findingsSQL+" WHERE f.id = $1", id)
	if err != nil {
		return Finding{}, err
	}
	if len(fs) == 0 {
		return Finding{}, ErrNotFound
	}
	return fs[0], nil
}

// Accept aanvaardt een bevinding: hij blijft zichtbaar maar telt niet meer
// mee. Met until gaat hij op die dag vanzelf weer open.
func (s *Service) Accept(ctx context.Context, actor events.Actor, userID *uuid.UUID, id uuid.UUID, note string, until *time.Time) (Finding, error) {
	note = strings.TrimSpace(note)
	if len(note) < 3 {
		return Finding{}, &FieldError{"Schrijf kort waarom je dit aanvaardt."}
	}
	if len(note) > 500 {
		return Finding{}, &FieldError{"De reden mag hoogstens 500 tekens zijn."}
	}
	if until != nil && !until.After(s.Now()) {
		return Finding{}, &FieldError{"De einddatum moet in de toekomst liggen."}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE security_findings SET status = 'aanvaard', accepted_by = $2, accepted_at = $3,
    accepted_until = $4, accepted_note = $5 WHERE id = $1 AND status <> 'opgelost'`, id, userID, s.Now(), until, note)
	if err != nil {
		return Finding{}, err
	}
	if tag.RowsAffected() == 0 {
		return Finding{}, ErrNotFound
	}
	f, err := s.Finding(ctx, id)
	if err != nil {
		return Finding{}, err
	}
	s.findingEvent(ctx, actor, f, "security.finding_accepted", map[string]any{"note": note})
	return f, nil
}

// Reopen zet een aanvaarde bevinding weer open.
func (s *Service) Reopen(ctx context.Context, actor events.Actor, id uuid.UUID) (Finding, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE security_findings SET status = 'open', accepted_until = NULL WHERE id = $1 AND status = 'aanvaard'`, id)
	if err != nil {
		return Finding{}, err
	}
	if tag.RowsAffected() == 0 {
		return Finding{}, ErrNotFound
	}
	f, err := s.Finding(ctx, id)
	if err != nil {
		return Finding{}, err
	}
	s.findingEvent(ctx, actor, f, "security.finding_reopened", nil)
	return f, nil
}

func (s *Service) findingEvent(ctx context.Context, actor events.Actor, f Finding, action string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["title"], payload["kind"], payload["severity"] = f.Title, f.Kind, f.Severity
	var cluster *uuid.UUID
	if f.NodeID != nil {
		payload["hostname"] = f.Hostname
		_ = s.pool.QueryRow(ctx, `SELECT cluster_id FROM nodes WHERE id = $1`, *f.NodeID).Scan(&cluster)
	}
	s.event(ctx, actor, "security_finding", f.ID.String(), cluster, action, payload)
}

// Stats zijn de cijfers bovenaan.
type Stats struct {
	Open     int            `json:"open"`
	Accepted int            `json:"accepted"`
	Counts   map[string]int `json:"counts"`
	KEV      int            `json:"kev"`
	// Fixable is het aantal open pakketbevindingen met een fix.
	Fixable       int `json:"fixable"`
	ResolvedWeek  int `json:"resolved_week"`
	NodesChecked  int `json:"nodes_checked"`
	NodesTotal    int `json:"nodes_total"`
	NodesOldAgent int `json:"nodes_old_agent"`
}

// Overview is de pagina Kwetsbaarheden.
type Overview struct {
	Protocol int     `json:"protocol"`
	Feeds    []*Feed `json:"feeds"`
	Stats    Stats   `json:"stats"`
	Nodes    []Node  `json:"nodes"`
	Scopes   int     `json:"scopes"`
}

// Overview geeft de stand.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	o := Overview{Protocol: protocol.SecuritySince, Stats: Stats{Counts: map[string]int{}}}
	feeds, err := s.feeds(ctx)
	if err != nil {
		return o, err
	}
	o.Feeds = []*Feed{feeds[FeedDebian], feeds[FeedKEV]}
	nodes, err := s.nodes(ctx, "")
	if err != nil {
		return o, err
	}
	loaded, err := s.loadedReleases(ctx)
	if err != nil {
		return o, err
	}
	byNode := map[uuid.UUID]int{}
	for i := range nodes {
		byNode[nodes[i].NodeID] = i
	}
	rows, err := s.pool.Query(ctx, `SELECT node_id, status, severity, coalesce((detail->>'kev')::int, 0), coalesce((detail->>'fixable')::int, 0)
FROM security_findings WHERE status <> 'opgelost' AND kind = 'cve'`)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var nid *uuid.UUID
		var st, sev string
		var kev, fix int
		if err := rows.Scan(&nid, &st, &sev, &kev, &fix); err != nil {
			rows.Close()
			return o, err
		}
		if st == StatusAccepted {
			o.Stats.Accepted++
			continue
		}
		o.Stats.Open++
		o.Stats.Counts[sev]++
		if kev > 0 {
			o.Stats.KEV++
		}
		if fix > 0 {
			o.Stats.Fixable++
		}
		if nid != nil {
			if i, ok := byNode[*nid]; ok {
				nodes[i].Counts[sev]++
				if kev > 0 {
					nodes[i].KEV++
				}
				if fix > 0 {
					nodes[i].Fixable++
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM security_findings WHERE status = 'opgelost' AND kind = 'cve' AND resolved_at > $1`,
		s.Now().Add(-7*24*time.Hour)).Scan(&o.Stats.ResolvedWeek); err != nil {
		return o, err
	}
	for i := range nodes {
		n := &nodes[i]
		o.Stats.NodesTotal++
		switch {
		case !n.Supported:
			o.Stats.NodesOldAgent++
			n.Note = "Agent te oud (protocol 40 nodig): Nodes → Agents bijwerken."
		case n.CollectedAt == nil && n.Error != "":
			n.Note = "Pakketlijst ophalen mislukt: " + n.Error
		case n.CollectedAt == nil:
			n.Note = "Wacht op de eerste pakketlijst."
		case !n.Dpkg:
			n.Note = "Geen dpkg: geen Debian-node."
		case n.OS != "debian":
			n.Note = "Alleen Debian wordt gecontroleerd (dit is " + n.OS + ")."
		case !contains(loaded, n.Codename):
			n.Note = "Nog geen gegevens van de Security Tracker voor " + n.Codename + "."
		default:
			n.Checked = true
			o.Stats.NodesChecked++
		}
	}
	o.Nodes = nodes
	if o.Nodes == nil {
		o.Nodes = []Node{}
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM security_scopes`).Scan(&o.Scopes); err != nil {
		return o, err
	}
	return o, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Scopes geeft alle scopes.
func (s *Service) Scopes(ctx context.Context) ([]Scope, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, cidrs, exclude, public, public_note, created_at, updated_at
FROM security_scopes ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scope{}
	for rows.Next() {
		var sc Scope
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Description, &sc.CIDRs, &sc.Exclude, &sc.Public, &sc.PublicNote,
			&sc.CreatedAt, &sc.UpdatedAt); err != nil {
			return nil, err
		}
		sc.Addresses = countAddresses(sc.CIDRs)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// SaveScope maakt een scope aan (id nil) of werkt hem bij.
func (s *Service) SaveScope(ctx context.Context, actor events.Actor, userID *uuid.UUID, id *uuid.UUID, in ScopeInput) (Scope, error) {
	cidrs, exclude, public, err := NormalizeScope(in)
	if err != nil {
		return Scope{}, err
	}
	name, desc := strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if len(desc) > 300 {
		return Scope{}, &FieldError{"De omschrijving mag hoogstens 300 tekens zijn."}
	}
	note := ""
	if public {
		note = strings.TrimSpace(in.PublicNote)
	}
	var sid uuid.UUID
	if id == nil {
		err = s.pool.QueryRow(ctx, `INSERT INTO security_scopes (name, description, cidrs, exclude, public, public_note, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, name, desc, cidrs, exclude, public, note, userID).Scan(&sid)
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE security_scopes SET name = $2, description = $3, cidrs = $4, exclude = $5, public = $6,
    public_note = $7, updated_by = $8, updated_at = now() WHERE id = $1 RETURNING id`, *id, name, desc, cidrs, exclude, public, note, userID).Scan(&sid)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, ErrNotFound
	}
	if err != nil {
		if strings.Contains(err.Error(), "security_scopes_name_key") {
			return Scope{}, &FieldError{"Er is al een scope met die naam."}
		}
		return Scope{}, err
	}
	s.event(ctx, actor, "security_scope", sid.String(), nil, "security.scope_saved",
		map[string]any{"name": name, "cidrs": cidrs, "exclude": exclude, "public": public})
	scopes, err := s.Scopes(ctx)
	if err != nil {
		return Scope{}, err
	}
	for _, sc := range scopes {
		if sc.ID == sid {
			return sc, nil
		}
	}
	return Scope{}, ErrNotFound
}

// DeleteScope verwijdert een scope.
func (s *Service) DeleteScope(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	var name string
	err := s.pool.QueryRow(ctx, `DELETE FROM security_scopes WHERE id = $1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	s.event(ctx, actor, "security_scope", id.String(), nil, "security.scope_deleted", map[string]any{"name": name})
	return nil
}

// Hardening geeft de verharding van alle nodes.
func (s *Service) Hardening(ctx context.Context) (HardeningOverview, error) {
	o := HardeningOverview{Protocol: protocol.SecurityAuditSince, FixProtocol: protocol.SecurityFixSince, Rules: Rules, Nodes: []HardeningNode{}}
	nodes, err := s.nodes(ctx, "")
	if err != nil {
		return o, err
	}
	type row struct {
		results []byte
		lynis   []byte
	}
	rows, err := s.pool.Query(ctx, `SELECT node_id, results, audit->'lynis' FROM security_hardening WHERE collected_at IS NOT NULL`)
	if err != nil {
		return o, err
	}
	data := map[uuid.UUID]row{}
	for rows.Next() {
		var id uuid.UUID
		var r row
		if err := rows.Scan(&id, &r.results, &r.lynis); err != nil {
			rows.Close()
			return o, err
		}
		data[id] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}
	for _, n := range nodes {
		h := HardeningNode{Node: n, Results: []RuleResult{}}
		if r, ok := data[n.NodeID]; ok {
			_ = json.Unmarshal(r.results, &h.Results)
			if len(r.lynis) > 0 && string(r.lynis) != "null" {
				h.Lynis = &protocol.LynisReport{}
				_ = json.Unmarshal(r.lynis, h.Lynis)
			}
		}
		switch {
		case !n.AuditSupported:
			h.Note = fmt.Sprintf("Agent te oud (protocol %d nodig): Nodes → Agents bijwerken.", protocol.SecurityAuditSince)
		case n.AuditAt == nil && n.AuditError != "":
			h.Note = "Uitlezen mislukt: " + n.AuditError
		case n.AuditAt == nil:
			h.Note = "Wacht op de eerste controle."
		}
		o.Nodes = append(o.Nodes, h)
	}
	return o, nil
}
