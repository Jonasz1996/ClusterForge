package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Testlijst 2 (23/11), punt 1.1: een gok raakt nooit meer ongemerkt een
// reeks hosts. Elke wijziging wordt vooraf en achteraf bekeken (HTTP-code en
// <title>); toont de host daarna een andere app, dan gaat hij terug. Wat
// ClusterForge veranderde staat in publish_changes, met per host
// terugdraaien.

// ActRevert draait één eerder door ClusterForge gemaakte wijziging terug.
const ActRevert = "revert"

// Change is één wijziging aan een proxy host door ClusterForge.
type Change struct {
	ID          uuid.UUID      `json:"id"`
	JobID       *uuid.UUID     `json:"job_id,omitempty"`
	HostID      int            `json:"host_id"`
	Domain      string         `json:"domain"`
	Action      string         `json:"action"`
	Created     bool           `json:"created"`
	Deleted     bool           `json:"deleted"`
	Old         map[string]any `json:"old"`
	New         map[string]any `json:"new"`
	TitleBefore string         `json:"title_before,omitempty"`
	TitleAfter  string         `json:"title_after,omitempty"`
	ChangedBy   string         `json:"changed_by,omitempty"`
	ChangedAt   time.Time      `json:"changed_at"`
	RevertedAt  *time.Time     `json:"reverted_at,omitempty"`
	RevertedBy  string         `json:"reverted_by,omitempty"`
	// Summary is de wijziging in gewone taal (doel oud → nieuw, ...).
	Summary []string `json:"summary"`
}

// changeSummary zet de velden die veranderden om in korte regels.
func changeSummary(c Change) []string {
	switch {
	case c.Created:
		return []string{fmt.Sprintf("nieuw: → %v://%v:%v", c.New["forward_scheme"], c.New["forward_host"], c.New["forward_port"])}
	case c.Deleted:
		return []string{fmt.Sprintf("verwijderd (was → %v://%v:%v)", c.Old["forward_scheme"], c.Old["forward_host"], c.Old["forward_port"])}
	}
	var out []string
	keys := make([]string, 0, len(c.New))
	for k := range c.New {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "advanced_config" {
			out = append(out, "Advanced aangepast")
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s → %s", k, trimText(jsonText(c.Old[k]), 60), trimText(jsonText(c.New[k]), 60)))
	}
	return out
}

// Changes geeft de wijzigingen van de laatste uren, nieuwste eerst.
func (s *Service) Changes(ctx context.Context, hours int) ([]Change, error) {
	if hours <= 0 {
		hours = 24
	}
	since := s.Now().Add(-time.Duration(hours) * time.Hour)
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.job_id, c.npm_host_id, c.domain, c.action, c.created, c.deleted, c.old, c.new,
		c.title_before, c.title_after, coalesce(u.username, ''), c.changed_at, c.reverted_at, coalesce(r.username, '')
		FROM publish_changes c LEFT JOIN users u ON u.id = c.changed_by LEFT JOIN users r ON r.id = c.reverted_by
		WHERE c.changed_at >= $1 ORDER BY c.changed_at DESC LIMIT 500`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		var oldB, newB []byte
		if err := rows.Scan(&c.ID, &c.JobID, &c.HostID, &c.Domain, &c.Action, &c.Created, &c.Deleted, &oldB, &newB,
			&c.TitleBefore, &c.TitleAfter, &c.ChangedBy, &c.ChangedAt, &c.RevertedAt, &c.RevertedBy); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(oldB, &c.Old)
		_ = json.Unmarshal(newB, &c.New)
		c.Summary = changeSummary(c)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) change(ctx context.Context, id string) (Change, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return Change{}, invalid("ongeldige wijziging %q", id)
	}
	var c Change
	var oldB, newB []byte
	err = s.pool.QueryRow(ctx, `SELECT id, npm_host_id, domain, action, created, deleted, old, new, changed_at, reverted_at
		FROM publish_changes WHERE id = $1`, uid).Scan(&c.ID, &c.HostID, &c.Domain, &c.Action, &c.Created, &c.Deleted, &oldB, &newB, &c.ChangedAt, &c.RevertedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Change{}, ErrNotFound
	}
	if err != nil {
		return Change{}, err
	}
	_ = json.Unmarshal(oldB, &c.Old)
	_ = json.Unmarshal(newB, &c.New)
	return c, nil
}

// recordChange onthoudt een geslaagde wijziging.
func (s *Service) recordChange(ctx context.Context, j *jobs.Job, a Action, hostID int, created bool, ph *PlannedHost) error {
	oldM, newM := ph.Old, ph.Body
	if created || oldM == nil {
		oldM = map[string]any{}
	}
	if ph.Delete || newM == nil {
		newM = map[string]any{}
	}
	oldB, _ := json.Marshal(oldM)
	newB, _ := json.Marshal(newM)
	before := ""
	if ph.Before != nil {
		before = ph.Before.Title
	}
	var jobID *uuid.UUID
	if j != nil && j.ID != uuid.Nil {
		id := j.ID
		jobID = &id
	}
	var by *uuid.UUID
	if j != nil {
		by = j.RequestedBy
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publish_changes (job_id, npm_host_id, domain, action, created, deleted, old, new, title_before, title_after, changed_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		jobID, hostID, ph.Domain, a.Action, created, ph.Delete, oldB, newB, before, ph.TitleAfter, by); err != nil {
		return err
	}
	if a.Action == ActRevert && a.ChangeID != "" {
		if id, err := uuid.Parse(a.ChangeID); err == nil {
			_, err := s.pool.Exec(ctx, `UPDATE publish_changes SET reverted_at = now(), reverted_by = $2 WHERE id = $1`, id, by)
			return err
		}
	}
	return nil
}

// planRevert plant het terugdraaien van één wijziging.
func (s *Service) planRevert(ctx context.Context, npm *NPMRead, byID map[int]NPMHost, taken map[string]int, a Action, p *Plan) error {
	c, err := s.change(ctx, a.ChangeID)
	if err != nil {
		return err
	}
	if c.RevertedAt != nil {
		return invalid("deze wijziging is al teruggedraaid op %s", c.RevertedAt.Format("2006-01-02 15:04"))
	}
	p.Title = "Terugdraaien: " + c.Domain
	ph := PlannedHost{HostID: c.HostID, Domain: c.Domain, AllowOtherApp: true, Scheme: "http"}
	switch {
	case c.Created:
		h, ok := byID[c.HostID]
		if !ok {
			return invalid("%s staat niet meer in NPM; er is niets terug te draaien", c.Domain)
		}
		ph.Delete, ph.Old, ph.Scheme = true, h.fields(), schemeOf(h)
		p.Lines = append(p.Lines, "NPM: de nieuwe host "+c.Domain+" weer verwijderen")
		p.Confirm = c.Domain
	case c.Deleted:
		for _, d := range sortedNames(c.Old["domain_names"]) {
			if id, ok := taken[strings.ToLower(d)]; ok {
				return invalid("%s bestaat intussen weer in NPM (host %d)", d, id)
			}
		}
		body := map[string]any{}
		for _, k := range npmHostFields {
			if v, ok := c.Old[k]; ok {
				body[k] = v
			}
		}
		if id := toInt(body["certificate_id"]); id > 0 {
			ph.Scheme = "https"
			found := false
			for _, cert := range npm.Certs {
				if cert.ID == id {
					found = true
				}
			}
			if !found {
				body["certificate_id"], body["ssl_forced"], body["http2_support"], body["hsts_enabled"] = 0, false, false, false
				ph.Scheme = "http"
				p.Warnings = append(p.Warnings, fmt.Sprintf("certificaat %d bestaat niet meer; de host komt terug zonder SSL", id))
			}
		}
		ph.HostID, ph.Body = 0, body
		p.Lines = append(p.Lines, fmt.Sprintf("NPM: %s opnieuw maken → %v://%v:%v", c.Domain, body["forward_scheme"], body["forward_host"], body["forward_port"]))
	default:
		h, ok := byID[c.HostID]
		if !ok {
			return invalid("%s staat niet meer in NPM; er is niets terug te draaien", c.Domain)
		}
		have := h.fields()
		body := diffFields(have, c.Old)
		if len(body) == 0 {
			return invalid("%s staat al zoals vóór die wijziging", c.Domain)
		}
		var later []string
		for k, v := range c.New {
			if !sameValue(have[k], v) {
				later = append(later, k)
			}
		}
		if len(later) > 0 {
			sort.Strings(later)
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s is sindsdien nog veranderd (%s); terugdraaien zet ook dat terug", c.Domain, strings.Join(later, ", ")))
		}
		old := map[string]any{}
		for k := range body {
			old[k] = have[k]
			p.Lines = append(p.Lines, fmt.Sprintf("%s: %s → %s", k, trimText(jsonText(have[k]), 80), trimText(jsonText(body[k]), 80)))
		}
		ph.Body, ph.Old, ph.Scheme = body, old, schemeOf(h)
		if id, ok := body["certificate_id"]; ok {
			ph.Scheme = "http"
			if toInt(id) > 0 {
				ph.Scheme = "https"
			}
		}
		if names, ok := body["domain_names"]; ok {
			if n := sortedNames(names); len(n) > 0 {
				ph.Domain = n[0]
			}
		}
	}
	if c.Action == ActProtect || c.Action == ActUnprotect || c.Action == ActAKRepair {
		p.Warnings = append(p.Warnings, "alleen NPM gaat terug; de applicatie en provider in Authentik blijven zoals ze zijn")
	}
	p.Hosts = append(p.Hosts, ph)
	return nil
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

// beforeURL is het adres van de host zoals hij vóór de wijziging was.
func beforeURL(ph *PlannedHost) string {
	name := ph.Domain
	if n := sortedNames(ph.Old["domain_names"]); len(n) > 0 {
		name = n[0]
	}
	scheme := ph.Scheme
	if id, ok := ph.Old["certificate_id"]; ok {
		scheme = "http"
		if toInt(id) > 0 {
			scheme = "https"
		}
	}
	return scheme + "://" + name + "/"
}

// lookBefore bekijkt de host vóór de wijziging: HTTP-code en <title>.
func (s *Service) lookBefore(ctx context.Context, step *jobs.Step, node nodeRow, ph *PlannedHost) {
	if ph.HostID == 0 || ph.Delete || ph.Before != nil {
		return
	}
	u := beforeURL(ph)
	res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: u, Connect: "127.0.0.1"}})
	if err != nil || res.Check == nil {
		step.Logf("Vooraf %s niet te bekijken: %v", u, err)
		return
	}
	c := *res.Check
	ph.Before = &c
	switch {
	case c.Error != "":
		step.Logf("Vooraf %s: geen antwoord (%s)", u, trimText(c.Error, 120))
	case c.Title != "":
		step.Logf("Vooraf %s: HTTP %d, titel %q", u, c.Status, c.Title)
	default:
		step.Logf("Vooraf %s: HTTP %d", u, c.Status)
	}
}

func normTitle(t string) string {
	return strings.ToLower(strings.Join(strings.Fields(t), " "))
}

// otherApp zegt waarom de host na de wijziging iets anders toont dan
// vooraf; leeg als er geen reden is om dat te denken. Een host die vooraf
// niet werkte (geen antwoord of 5xx) heeft niets om mee te vergelijken.
func otherApp(before *protocol.WebCheckResult, after protocol.WebCheckResult, protected bool) string {
	if before == nil || before.Error != "" || before.Status == 0 || before.Status >= 500 {
		return ""
	}
	if before.Status < 400 && after.Status >= 400 && !(protected && (after.Status == 401 || after.Status == 403)) {
		return fmt.Sprintf("vooraf HTTP %d, nu HTTP %d", before.Status, after.Status)
	}
	// Een doorverwijzing (naar een aanmelding zoals de Authentik-outpost)
	// of een foutpagina heeft een eigen titel; alleen twee echte pagina's
	// vergelijken.
	if after.Status < 200 || after.Status >= 300 || before.Status < 200 || before.Status >= 300 {
		return ""
	}
	bt, at := normTitle(before.Title), normTitle(after.Title)
	if bt == "" || at == "" || bt == at || strings.Contains(bt, at) || strings.Contains(at, bt) {
		return ""
	}
	return fmt.Sprintf("vooraf titel %q, nu %q: een andere app", before.Title, after.Title)
}

// sameTarget geeft de andere hosts die al naar host:port wijzen.
func sameTarget(hosts []NPMHost, self int, host string, port int) []string {
	var out []string
	for _, h := range hosts {
		if h.ID == self || len(h.DomainNames) == 0 || !h.IsEnabled() {
			continue
		}
		if strings.EqualFold(h.ForwardHost, host) && h.ForwardPort == port {
			out = append(out, strings.ToLower(h.DomainNames[0]))
		}
	}
	sort.Strings(out)
	return out
}

// warnSameTarget zet een waarschuwing in het plan als het nieuwe doel van
// een host al door een andere host gebruikt wordt.
func warnSameTarget(p *Plan, hosts []NPMHost, ph PlannedHost) {
	host, _ := ph.Body["forward_host"].(string)
	port := toInt(ph.Body["forward_port"])
	if host == "" && port == 0 {
		return
	}
	for _, h := range hosts {
		if h.ID == ph.HostID {
			if host == "" {
				host = h.ForwardHost
			}
			if port == 0 {
				port = h.ForwardPort
			}
		}
	}
	if others := sameTarget(hosts, ph.HostID, host, port); len(others) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: %s:%d is al het doel van %s. Twee namen voor dezelfde app kan, maar kijk na of dit de juiste app is",
			ph.Domain, host, port, strings.Join(others, ", ")))
	}
}
