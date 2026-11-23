package crowdsec

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// AutoRules: wanneer ClusterForge een IP zelf in de lijst "Automatisch
// geblokkeerd" zet, en daarmee op alle nodes. Een regel met 0 staat uit.
type AutoRules struct {
	Enabled bool `json:"enabled"`
	// MultiNodes: een IP met alerts op zoveel verschillende nodes binnen
	// MultiWindowMinutes.
	MultiNodes         int `json:"multi_nodes"`
	MultiWindowMinutes int `json:"multi_window_minutes"`
	// RepeatAlerts: een IP met zoveel alerts (op welke node ook) binnen
	// RepeatWindowHours.
	RepeatAlerts      int `json:"repeat_alerts"`
	RepeatWindowHours int `json:"repeat_window_hours"`
	// DurationHours is de duur van een automatische block; met Escalate
	// verdubbelt hij bij elke herhaling, tot MaxHours.
	DurationHours int  `json:"duration_hours"`
	Escalate      bool `json:"escalate"`
	MaxHours      int  `json:"max_hours"`
}

// DefaultAutoRules: aan, want Jonas wil over alle nodes heen blokkeren.
var DefaultAutoRules = AutoRules{Enabled: true, MultiNodes: 2, MultiWindowMinutes: 60, RepeatAlerts: 3, RepeatWindowHours: 24,
	DurationHours: 168, Escalate: true, MaxHours: 2160}

// Validate controleert de regels.
func (r AutoRules) Validate() error {
	switch {
	case r.MultiNodes < 0 || r.MultiNodes == 1 || r.MultiNodes > 100:
		return &FieldError{"aantal nodes is 0 (uit) of 2 tot 100"}
	case r.MultiNodes > 0 && (r.MultiWindowMinutes < 5 || r.MultiWindowMinutes > 10080):
		return &FieldError{"venster voor nodes is 5 minuten tot 7 dagen"}
	case r.RepeatAlerts < 0 || r.RepeatAlerts == 1 || r.RepeatAlerts > 1000:
		return &FieldError{"aantal alerts is 0 (uit) of 2 tot 1000"}
	case r.RepeatAlerts > 0 && (r.RepeatWindowHours < 1 || r.RepeatWindowHours > 720):
		return &FieldError{"venster voor alerts is 1 uur tot 30 dagen"}
	case r.DurationHours < 1 || r.DurationHours > 8760:
		return &FieldError{"duur is 1 uur tot 1 jaar"}
	case r.Escalate && (r.MaxHours < r.DurationHours || r.MaxHours > 8760):
		return &FieldError{"de langste duur ligt tussen de duur en 1 jaar"}
	}
	return nil
}

// UpdateAutoRules bewaart de regels.
func (s *Service) UpdateAutoRules(ctx context.Context, actor events.Actor, userID *uuid.UUID, r AutoRules) (Settings, error) {
	if !r.Escalate {
		r.MaxHours = r.DurationHours
	}
	if err := r.Validate(); err != nil {
		return Settings{}, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Settings{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET auto_rules = $1, updated_at = now(), updated_by = $2`, b, userID); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "crowdsec.auto_rules_changed", map[string]any{"rules": r})
	s.kickLists()
	return s.Settings(ctx)
}

// offender is een IP dat aan een regel voldoet.
type offender struct {
	value     string
	nodes     int
	alerts    int
	scenarios []string
	hosts     []string
	why       string
}

// AutoBlocked is een IP dat de regels net blokkeerden.
type AutoBlocked struct {
	Value     string    `json:"value"`
	Why       string    `json:"why"`
	Until     time.Time `json:"until"`
	Scenarios []string  `json:"scenarios"`
}

// candidates zoekt de IP's die aan een regel voldoen. Alerts van voor de
// laatste (handmatige of automatische) beslissing over een IP tellen niet,
// zodat Deblokkeren niet meteen weer blokkeert.
func (s *Service) candidates(ctx context.Context, r AutoRules) ([]offender, error) {
	found := map[string]*offender{}
	run := func(window time.Duration, having string, n int, why func(o *offender) string) error {
		rows, err := s.pool.Query(ctx, `SELECT a.value, count(DISTINCT a.node_id), count(*), array_agg(DISTINCT a.scenario),
			array_agg(DISTINCT n.hostname)
			FROM crowdsec_alerts a JOIN nodes n ON n.id = a.node_id
			LEFT JOIN crowdsec_offenders o ON o.value = a.value
			WHERE a.started_at > $1 AND NOT a.simulated AND a.scope IN ('ip', 'range')
			  AND a.started_at > coalesce(o.last_at, '-infinity'::timestamptz)
			GROUP BY a.value HAVING `+having+` >= $2`, s.Now().Add(-window), n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o offender
			if err := rows.Scan(&o.value, &o.nodes, &o.alerts, &o.scenarios, &o.hosts); err != nil {
				return err
			}
			if found[o.value] == nil {
				o.why = why(&o)
				found[o.value] = &o
			}
		}
		return rows.Err()
	}
	if r.MultiNodes > 0 {
		if err := run(time.Duration(r.MultiWindowMinutes)*time.Minute, "count(DISTINCT a.node_id)", r.MultiNodes, func(o *offender) string {
			return fmt.Sprintf("alerts op %d nodes (%s)", o.nodes, strings.Join(o.hosts, ", "))
		}); err != nil {
			return nil, err
		}
	}
	if r.RepeatAlerts > 0 {
		if err := run(time.Duration(r.RepeatWindowHours)*time.Hour, "count(*)", r.RepeatAlerts, func(o *offender) string {
			return fmt.Sprintf("%d alerts in %d uur", o.alerts, r.RepeatWindowHours)
		}); err != nil {
			return nil, err
		}
	}
	out := make([]offender, 0, len(found))
	for _, o := range found {
		sort.Strings(o.scenarios)
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].value < out[j].value })
	return out, nil
}

// autoBlock past de regels toe: nieuwe IP's gaan in de automatische lijst.
func (s *Service) autoBlock(ctx context.Context) error {
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	r := set.AutoRules
	if !r.Enabled {
		return nil
	}
	cands, err := s.candidates(ctx, r)
	if err != nil || len(cands) == 0 {
		return err
	}
	auto, err := s.listBySlug(ctx, "auto")
	if err != nil {
		return err
	}
	g, err := s.guard(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	var blocked []AutoBlocked
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := s.list(ctx, tx, auto.ID)
		if err != nil {
			return err
		}
		cur, err := s.entriesTx(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		var added []string
		for _, o := range cands {
			if e, ok := cur[o.value]; ok && (e.ExpiresAt == nil || e.ExpiresAt.After(now)) {
				continue
			}
			norm, why := g.Check(o.value)
			if norm == "" || why != "" {
				continue
			}
			// Hoe vaak al? Na 90 dagen rust begint het opnieuw.
			var offenses int
			if err := tx.QueryRow(ctx, `INSERT INTO crowdsec_offenders (value, offenses, last_at) VALUES ($1, 1, $2)
				ON CONFLICT (value) DO UPDATE SET offenses = CASE WHEN crowdsec_offenders.last_at < $2 - interval '90 days' THEN 1
				ELSE crowdsec_offenders.offenses + 1 END, last_at = $2 RETURNING offenses`, norm, now).Scan(&offenses); err != nil {
				return err
			}
			hours := r.DurationHours
			if r.Escalate {
				for i := 1; i < offenses && hours < r.MaxHours; i++ {
					hours *= 2
				}
				hours = min(hours, r.MaxHours)
			}
			until := now.Add(time.Duration(hours) * time.Hour).Truncate(time.Second)
			text := o.why + ": " + strings.Join(o.scenarios, ", ")
			if offenses > 1 {
				text = fmt.Sprintf("%de keer, %s", offenses, text)
			}
			cur[norm] = storedEntry{Value: norm, Reason: reason(text), ExpiresAt: &until}
			added = append(added, norm)
			blocked = append(blocked, AutoBlocked{Value: norm, Why: text, Until: until, Scenarios: o.scenarios})
		}
		if len(added) == 0 {
			return nil
		}
		_, err = s.replaceEntries(ctx, tx, l, cur, len(added), 0, fmt.Sprintf("automatisch: %d erbij", len(added)), nil, g)
		return err
	})
	if err != nil {
		return err
	}
	for _, b := range blocked {
		s.listEvent(ctx, events.System(), auto, "crowdsec.auto_blocked", map[string]any{"value": b.Value, "why": b.Why, "until": b.Until})
	}
	if len(blocked) > 0 && set.Notify.AutoBlock {
		var lines []string
		for i, b := range blocked {
			if i == 10 {
				lines = append(lines, fmt.Sprintf("… en nog %d", len(blocked)-10))
				break
			}
			lines = append(lines, fmt.Sprintf("%s tot %s: %s", b.Value, b.Until.Local().Format("02/01 15:04"), b.Why))
		}
		s.notify(ctx, fmt.Sprintf("%d IP automatisch geblokkeerd op alle nodes", len(blocked)), strings.Join(lines, "\n"), "warning")
	}
	return nil
}
