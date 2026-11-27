package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Automatisch publiceren (fase 48, bevinding B4.2). Een regel per cluster of
// per dienst. Na elke lezing (elke 5 minuten) kijkt ClusterForge welke
// webdiensten van die clusters nog geen proxy host hebben en publiceert er
// één per beurt via de gewone taak publish.apply: kopie van NPM vooraf,
// Authentik zoals de standaard, controles erna en terugdraaien bij een fout.
// Alles staat dus in Taken.
//
// Nooit een gok: alleen een dienst met een exact doel (het VIP van het
// eigen cluster, of de enige node waarop hij draait) en met een vrije naam.
// Een dienst waarbij ClusterForge zou moeten kiezen, slaat hij over met de
// reden erbij. Een mislukte poging wacht een dag; na drie keer stopt het
// tot iemand op "Opnieuw" klikt.
//
// Bij decomm (cluster weg of alle nodes uit dienst) ruimt ClusterForge de
// host, de provider en de applicatie weer op die het zelf maakte; eerst
// bewaart hij de host zoals NPM hem had.

// Toestanden van een automatisch gepubliceerde dienst.
const (
	AutoPublishing = "publishing"
	AutoPublished  = "published"
	AutoFailed     = "failed"
	AutoRemoving   = "removing"
	AutoRemoved    = "removed"
)

const (
	// autoRetry: zo lang na een mislukte poging probeert hij opnieuw.
	autoRetry = 24 * time.Hour
	// autoMaxTries: daarna alleen nog na een klik.
	autoMaxTries = 3
	autoActorID  = "automatisch-publiceren"
)

// AutoRule zet automatisch publiceren aan voor een cluster of een dienst.
type AutoRule struct {
	ID          uuid.UUID  `json:"id"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName string     `json:"cluster_name,omitempty"`
	ServiceKey  string     `json:"service_key,omitempty"`
	Enabled     bool       `json:"enabled"`
	Cleanup     bool       `json:"cleanup"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// AutoItem is wat ClusterForge voor één dienst vanzelf deed.
type AutoItem struct {
	ServiceKey  string     `json:"service_key"`
	Domain      string     `json:"domain"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName string     `json:"cluster_name,omitempty"`
	State       string     `json:"state"`
	JobID       *uuid.UUID `json:"job_id,omitempty"`
	NPMHostID   *int       `json:"npm_host_id,omitempty"`
	Cleanup     bool       `json:"cleanup"`
	Tries       int        `json:"tries"`
	Error       string     `json:"error,omitempty"`
	HasBackup   bool       `json:"has_backup"`
	TriedAt     *time.Time `json:"tried_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// AutoCandidate is een dienst onder een regel: wat de volgende beurt doet,
// of waarom hij hem overslaat.
type AutoCandidate struct {
	ServiceKey  string `json:"service_key"`
	ClusterName string `json:"cluster_name,omitempty"`
	Name        string `json:"name"`
	Domain      string `json:"domain,omitempty"`
	Target      string `json:"target"`
	Authentik   bool   `json:"authentik"`
	// Skip is de reden om niet te publiceren; leeg = wordt gepubliceerd.
	Skip string `json:"skip,omitempty"`
}

// AutoView is alles voor het blok "Automatisch publiceren".
type AutoView struct {
	Rules      []AutoRule      `json:"rules"`
	Items      []AutoItem      `json:"items"`
	Candidates []AutoCandidate `json:"candidates"`
	// Ready is leeg als het kan; anders waarom het niet loopt.
	Ready string `json:"ready,omitempty"`
}

// AutoRuleInput is een regel bewaren.
type AutoRuleInput struct {
	ClusterID  *uuid.UUID `json:"cluster_id,omitempty"`
	ServiceKey string     `json:"service_key,omitempty"`
	Enabled    bool       `json:"enabled"`
	Cleanup    *bool      `json:"cleanup,omitempty"`
}

func autoActor() events.Actor {
	return events.Actor{Type: events.System().Type, ID: autoActorID}
}

// AutoRules geeft de regels.
func (s *Service) AutoRules(ctx context.Context) ([]AutoRule, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.id, r.cluster_id, coalesce(c.name, ''), coalesce(r.service_key, ''), r.enabled, r.cleanup, r.updated_at
		FROM publish_auto_rules r LEFT JOIN clusters c ON c.id = r.cluster_id ORDER BY coalesce(c.name, r.service_key)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutoRule{}
	for rows.Next() {
		var r AutoRule
		if err := rows.Scan(&r.ID, &r.ClusterID, &r.ClusterName, &r.ServiceKey, &r.Enabled, &r.Cleanup, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveAutoRule zet automatisch publiceren aan of uit voor een cluster of
// een dienst.
func (s *Service) SaveAutoRule(ctx context.Context, actor events.Actor, userID *uuid.UUID, in AutoRuleInput) (AutoRule, error) {
	in.ServiceKey = strings.TrimSpace(in.ServiceKey)
	if (in.ClusterID == nil) == (in.ServiceKey == "") {
		return AutoRule{}, invalid("kies een cluster of een dienst")
	}
	cleanup := true
	if in.Cleanup != nil {
		cleanup = *in.Cleanup
	}
	var id uuid.UUID
	var err error
	if in.ClusterID != nil {
		var name string
		if err := s.pool.QueryRow(ctx, `SELECT name FROM clusters WHERE id = $1`, *in.ClusterID).Scan(&name); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return AutoRule{}, ErrNotFound
			}
			return AutoRule{}, err
		}
		err = s.pool.QueryRow(ctx, `INSERT INTO publish_auto_rules (cluster_id, enabled, cleanup, updated_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT (cluster_id) WHERE cluster_id IS NOT NULL DO UPDATE SET enabled = EXCLUDED.enabled, cleanup = EXCLUDED.cleanup,
			updated_by = EXCLUDED.updated_by, updated_at = now() RETURNING id`, *in.ClusterID, in.Enabled, cleanup, userID).Scan(&id)
	} else {
		err = s.pool.QueryRow(ctx, `INSERT INTO publish_auto_rules (service_key, enabled, cleanup, updated_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT (service_key) WHERE service_key IS NOT NULL DO UPDATE SET enabled = EXCLUDED.enabled, cleanup = EXCLUDED.cleanup,
			updated_by = EXCLUDED.updated_by, updated_at = now() RETURNING id`, in.ServiceKey, in.Enabled, cleanup, userID).Scan(&id)
	}
	if err != nil {
		return AutoRule{}, err
	}
	payload := map[string]any{"enabled": in.Enabled, "cleanup": cleanup}
	if in.ClusterID != nil {
		payload["cluster_id"] = in.ClusterID.String()
	} else {
		payload["service_key"] = in.ServiceKey
	}
	s.event(ctx, actor, "publish.auto_rule", payload)
	rules, err := s.AutoRules(ctx)
	if err != nil {
		return AutoRule{}, err
	}
	s.Kick()
	for _, r := range rules {
		if r.ID == id {
			return r, nil
		}
	}
	return AutoRule{}, ErrNotFound
}

// RetryAuto zet de teller van een mislukte dienst op nul, zodat de volgende
// beurt hem opnieuw probeert.
func (s *Service) RetryAuto(ctx context.Context, actor events.Actor, key string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE publish_auto_items SET tries = 0, tried_at = NULL, error = '', updated_at = now()
		WHERE service_key = $1 AND state = 'failed'`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.event(ctx, actor, "publish.auto_retry", map[string]any{"service_key": key})
	s.Kick()
	return nil
}

func (s *Service) autoItems(ctx context.Context) ([]AutoItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT service_key, domain, cluster_id, cluster_name, state, job_id, npm_host_id, cleanup, tries, error,
		backup IS NOT NULL, tried_at, updated_at FROM publish_auto_items ORDER BY cluster_name, domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutoItem{}
	for rows.Next() {
		var it AutoItem
		if err := rows.Scan(&it.ServiceKey, &it.Domain, &it.ClusterID, &it.ClusterName, &it.State, &it.JobID, &it.NPMHostID, &it.Cleanup,
			&it.Tries, &it.Error, &it.HasBackup, &it.TriedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AutoOverview geeft de regels, wat ClusterForge vanzelf deed en wat de
// volgende beurt zou doen.
func (s *Service) AutoOverview(ctx context.Context) (*AutoView, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.AutoRules(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.autoItems(ctx)
	if err != nil {
		return nil, err
	}
	v := &AutoView{Rules: rules, Items: items, Candidates: []AutoCandidate{}}
	if !st.NPMLinked() {
		v.Ready = "koppel eerst NPM in Instellingen → Domeinen en publicatie"
		return v, nil
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	v.Candidates = autoCandidates(st, ov, rules, items, s.Now())
	return v, nil
}

// ruleFor zegt of een dienst onder een aanstaande regel valt; een regel per
// dienst gaat voor die van het cluster.
func ruleFor(rules []AutoRule, r ServiceRow) (AutoRule, bool) {
	for _, rule := range rules {
		if rule.ServiceKey != "" && rule.ServiceKey == r.Key {
			return rule, rule.Enabled
		}
	}
	for _, rule := range rules {
		if rule.ClusterID != nil && r.ClusterID != nil && *rule.ClusterID == *r.ClusterID {
			return rule, rule.Enabled
		}
	}
	return AutoRule{}, false
}

// autoCandidates bepaalt per dienst onder een regel of hij gepubliceerd
// wordt, en anders waarom niet. Puur, zodat het te testen is.
func autoCandidates(st Settings, ov *Overview, rules []AutoRule, items []AutoItem, now time.Time) []AutoCandidate {
	byKey := map[string]AutoItem{}
	for _, it := range items {
		byKey[it.ServiceKey] = it
	}
	taken := map[string]bool{}
	for _, h := range ov.rawHosts {
		for _, d := range h.DomainNames {
			taken[strings.ToLower(d)] = true
		}
	}
	// Twee diensten met hetzelfde voorstel: dan is de naam een gok.
	labels := map[string]int{}
	for _, r := range ov.Services {
		if _, on := ruleFor(rules, r); on {
			labels[r.Label]++
		}
	}
	out := []AutoCandidate{}
	for _, r := range ov.Services {
		if _, on := ruleFor(rules, r); !on {
			continue
		}
		c := AutoCandidate{ServiceKey: r.Key, ClusterName: r.ClusterName, Name: r.Name, Target: fmt.Sprintf("%s:%d", r.Target, r.Port), Authentik: r.AuthentikDefault && st.AuthentikLinked()}
		name, nerr := FullName(r.Label, st.Domain)
		if nerr == nil {
			c.Domain = name
		}
		it, seen := byKey[r.Key]
		switch {
		case !r.Web:
			continue // geen webdienst: niets te publiceren, niet tonen
		case len(r.Published) > 0:
			c.Skip = "al gepubliceerd als " + r.Published[0].Domain
		case seen && (it.State == AutoPublishing || it.State == AutoRemoving):
			c.Skip = "taak loopt"
		case seen && it.State == AutoPublished:
			c.Skip = "ClusterForge publiceerde hem al als " + it.Domain + "; de host is er niet meer (bewust weggehaald?)"
		case seen && it.State == AutoRemoved:
			c.Skip = "opgeruimd bij decomm"
		case seen && it.State == AutoFailed && it.Tries >= autoMaxTries:
			c.Skip = fmt.Sprintf("%d keer mislukt: %s (klik Opnieuw)", it.Tries, it.Error)
		case seen && it.State == AutoFailed && it.TriedAt != nil && now.Sub(*it.TriedAt) < autoRetry:
			c.Skip = "mislukt, nieuwe poging na " + it.TriedAt.Add(autoRetry).Format("02/01 15:04") + ": " + it.Error
		case r.Internal:
			c.Skip = "intern onderdeel (" + r.InternalWhy + ")"
		case r.ClusterID == nil:
			c.Skip = "draait niet in een cluster"
		case !r.TargetVIP && len(r.Nodes) != 1:
			c.Skip = "draait op meerdere nodes zonder VIP: het doel zou een gok zijn"
		case r.Target == "":
			c.Skip = "geen adres"
		case nerr != nil:
			c.Skip = "geen geldige naam: " + nerr.Error()
		case taken[name]:
			c.Skip = name + " bestaat al in NPM voor iets anders"
		case labels[r.Label] > 1:
			c.Skip = "meerdere diensten zouden " + name + " krijgen; publiceer deze met de hand"
		}
		out = append(out, c)
	}
	return out
}

// autoPublish doet één beurt: eerst afronden wat liep, dan opruimen bij
// decomm, dan hoogstens één nieuwe publicatie (er loopt toch maar één taak
// op NPM tegelijk).
func (s *Service) autoPublish(ctx context.Context, st Settings) {
	if !st.NPMLinked() {
		return
	}
	if err := s.autoSettle(ctx); err != nil {
		s.log.Warn("automatisch publiceren: taken afronden", "err", err)
		return
	}
	if busy, _ := s.applyBusy(ctx); busy != nil {
		return
	}
	if started, err := s.autoCleanup(ctx); err != nil {
		s.log.Warn("automatisch publiceren: opruimen", "err", err)
	} else if started {
		return
	}
	rules, err := s.AutoRules(ctx)
	if err != nil || len(rules) == 0 {
		return
	}
	items, err := s.autoItems(ctx)
	if err != nil {
		return
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return
	}
	for _, c := range autoCandidates(st, ov, rules, items, s.Now()) {
		if c.Skip != "" {
			continue
		}
		var svc ServiceRow
		for _, r := range ov.Services {
			if r.Key == c.ServiceKey {
				svc = r
			}
		}
		a := Action{Action: ActPublish, ServiceKey: c.ServiceKey}
		j, _, err := s.Start(ctx, autoActor(), a)
		tries := 1
		for _, it := range items {
			if it.ServiceKey == c.ServiceKey {
				tries = it.Tries + 1
			}
		}
		state, msg := AutoPublishing, ""
		var jobID *uuid.UUID
		if err != nil {
			state, msg = AutoFailed, err.Error()
		} else {
			jobID = &j.ID
		}
		rule, _ := ruleFor(rules, svc)
		if _, err := s.pool.Exec(ctx, `INSERT INTO publish_auto_items (service_key, domain, cluster_id, cluster_name, state, job_id, cleanup, tries, error, tried_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
			ON CONFLICT (service_key) DO UPDATE SET domain = EXCLUDED.domain, cluster_id = EXCLUDED.cluster_id, cluster_name = EXCLUDED.cluster_name,
			state = EXCLUDED.state, job_id = EXCLUDED.job_id, cleanup = EXCLUDED.cleanup, tries = EXCLUDED.tries, error = EXCLUDED.error,
			tried_at = now(), updated_at = now()`,
			c.ServiceKey, c.Domain, svc.ClusterID, svc.ClusterName, state, jobID, rule.Cleanup, tries, msg); err != nil {
			s.log.Warn("automatisch publiceren bewaren", "err", err)
		}
		s.event(ctx, autoActor(), "publish.auto_started", map[string]any{"service_key": c.ServiceKey, "domain": c.Domain, "error": msg})
		return
	}
}

// autoSettle kijkt hoe de gestarte taken afliepen.
func (s *Service) autoSettle(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT i.service_key, i.state, i.domain, j.status::text, coalesce(j.error, '')
		FROM publish_auto_items i JOIN jobs j ON j.id = i.job_id
		WHERE i.state IN ('publishing', 'removing') AND j.status IN ('succeeded', 'failed', 'canceled')`)
	if err != nil {
		return err
	}
	type done struct{ key, state, domain, status, msg string }
	var list []done
	for rows.Next() {
		var d done
		if err := rows.Scan(&d.key, &d.state, &d.domain, &d.status, &d.msg); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		var next, msg string
		switch {
		case d.state == AutoPublishing && d.status == "succeeded":
			next = AutoPublished
		case d.state == AutoPublishing:
			next, msg = AutoFailed, firstLine(d.msg, "taak "+d.status)
		case d.status == "succeeded":
			next = AutoRemoved
		default:
			// Opruimen mislukt: blijft gepubliceerd, de volgende beurt
			// probeert opnieuw na autoRetry.
			next, msg = AutoPublished, "opruimen mislukt: "+firstLine(d.msg, d.status)
		}
		var hostID *int
		if next == AutoPublished {
			var id int
			if err := s.pool.QueryRow(ctx, `SELECT npm_host_id FROM publish_hosts WHERE lower(domain) = lower($1)`, d.domain).Scan(&id); err == nil {
				hostID = &id
			}
		}
		if _, err := s.pool.Exec(ctx, `UPDATE publish_auto_items SET state = $2, error = $3, npm_host_id = coalesce($4, npm_host_id),
			tries = CASE WHEN $2 = 'published' AND $3 = '' THEN 0 ELSE tries END, updated_at = now() WHERE service_key = $1`,
			d.key, next, msg, hostID); err != nil {
			return err
		}
		s.event(ctx, autoActor(), "publish.auto_"+next, map[string]any{"service_key": d.key, "domain": d.domain, "error": msg})
	}
	return nil
}

func firstLine(s, or string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return or
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return trimText(s, 300)
}

// autoCleanup ruimt één host op van een cluster dat uit dienst is: het
// cluster bestaat niet meer, of al zijn nodes zijn uit dienst.
func (s *Service) autoCleanup(ctx context.Context) (bool, error) {
	var key, domain string
	var hostID int
	var triedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT i.service_key, i.domain, i.npm_host_id, i.tried_at FROM publish_auto_items i
		WHERE i.state = 'published' AND i.cleanup AND i.npm_host_id IS NOT NULL AND i.cluster_id IS NOT NULL
		AND (NOT EXISTS (SELECT 1 FROM clusters c WHERE c.id = i.cluster_id)
		     OR NOT EXISTS (SELECT 1 FROM nodes n WHERE n.cluster_id = i.cluster_id AND n.lifecycle <> 'decommissioned'))
		AND (i.error NOT LIKE 'opruimen mislukt%' OR i.tried_at < now() - $1::interval)
		ORDER BY i.updated_at LIMIT 1`, fmt.Sprintf("%d seconds", int(autoRetry.Seconds()))).Scan(&key, &domain, &hostID, &triedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	// Kopie vooraf: de host zoals NPM hem nu heeft.
	var backup []byte
	if node, nerr := s.npmNode(ctx, st); nerr == nil {
		if h, herr := s.npmHost(ctx, st, node, hostID); herr == nil && h != nil {
			backup, _ = json.Marshal(h)
		} else if herr == nil {
			// De host is al weg: niets meer te doen.
			_, err := s.pool.Exec(ctx, `UPDATE publish_auto_items SET state = 'removed', error = '', updated_at = now() WHERE service_key = $1`, key)
			return false, err
		}
	}
	a := Action{Action: ActDelete, HostID: hostID, Confirm: domain}
	j, _, serr := s.Start(ctx, autoActor(), a)
	if serr != nil {
		_, err := s.pool.Exec(ctx, `UPDATE publish_auto_items SET error = $2, tried_at = now(), updated_at = now(), backup = coalesce($3, backup)
			WHERE service_key = $1`, key, "opruimen mislukt: "+serr.Error(), nullJSON(backup))
		return false, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE publish_auto_items SET state = 'removing', job_id = $2, error = '', tried_at = now(), updated_at = now(),
		backup = coalesce($3, backup) WHERE service_key = $1`, key, j.ID, nullJSON(backup)); err != nil {
		return true, err
	}
	s.event(ctx, autoActor(), "publish.auto_cleanup", map[string]any{"service_key": key, "domain": domain, "job_id": j.ID.String()})
	return true, nil
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// AutoBackup geeft de host zoals NPM hem had vlak voor het opruimen.
func (s *Service) AutoBackup(ctx context.Context, key string) (json.RawMessage, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT backup FROM publish_auto_items WHERE service_key = $1`, key).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(b) == 0) {
		return nil, ErrNotFound
	}
	return b, err
}
