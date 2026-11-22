// Package reports maakt de rapporten op /rapporten. Het rapport
// Herstelbaarheid zet per cluster en per maand bij elkaar wat er over
// herstel bekend is: de nieuwste back-ups, de controles, failovertests,
// rampoefeningen en DR-repetities van de maand, RTO en RPO tegen de doelen
// en de open risico's. De planner bewaart elke eerste van de maand het
// rapport van de maand ervoor, zodat het later nog zegt hoe het toen stond.
package reports

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// SaveDays is hoe lang na het begin van een maand de planner het rapport
// van de vorige maand nog bewaart, als de server op de eerste uit stond.
const SaveDays = 7

// Cards zijn de risicokaarten die bij herstel horen.
var Cards = []risk.CardID{risk.Backups, risk.Recovery, risk.Redundancy}

var (
	// ErrMonth is een maand die niet als JJJJ-MM te lezen is.
	ErrMonth = errors.New("de maand is niet als JJJJ-MM te lezen")
	// ErrFuture is een maand die nog moet komen.
	ErrFuture = errors.New("die maand moet nog komen")
)

type Service struct {
	q   *store.Queries
	log *slog.Logger

	Backups *backups.Service
	Risk    *risk.Service
	Now     func() time.Time
	// Loc is de tijdzone van de maanden; nil is die van de server.
	Loc *time.Location
}

func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{q: store.New(pool), log: log, Now: time.Now}
}

// Recovery is het rapport Herstelbaarheid van één maand.
type Recovery struct {
	// Month is JJJJ-MM; From en To zijn het begin van die maand en van de
	// volgende.
	Month       string    `json:"month"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	GeneratedAt time.Time `json:"generated_at"`
	// Saved is true voor het rapport dat de planner bewaarde. Anders is
	// het nu berekend: de runs komen uit de geschiedenis, maar de
	// back-ups, de doelen en de risico's zijn die van nu.
	Saved bool `json:"saved"`
	// Running is true als de maand nog niet voorbij was.
	Running  bool      `json:"running"`
	Clusters []Cluster `json:"clusters"`
}

// Cluster is één cluster in het rapport.
type Cluster struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Environment string    `json:"environment"`
	Grade       string    `json:"grade"`
	Score       *int      `json:"score"`
	// De doelen; 0 is geen doel.
	RTOTargetMinutes int      `json:"rto_target_minutes"`
	RPOTargetHours   int      `json:"rpo_target_hours"`
	Backups          []Backup `json:"backups"`
	// Runs zijn de afgeronde tests van de maand, oudste eerst.
	Runs []Run `json:"runs"`
	// DR is de laatste DR-repetitie tot het einde van de maand die de RTO
	// mat; nil zonder.
	DR    *Measured `json:"dr"`
	Risks []Risk    `json:"risks"`
}

// Backup is de nieuwste back-up van een VM van het cluster.
type Backup struct {
	Node       string `json:"node"`
	Connection string `json:"connection"`
	VMID       int    `json:"vmid"`
	// Time is nil als er geen back-up is.
	Time      *time.Time `json:"time"`
	Storage   string     `json:"storage"`
	Size      int64      `json:"size"`
	Count     int        `json:"count"`
	Freshness string     `json:"freshness"`
	// LastVerification is de laatste back-upcontrole, ook van voor de
	// maand.
	LastVerification *Verification `json:"last_verification"`
}

type Verification struct {
	RunID           uuid.UUID  `json:"run_id"`
	Result          string     `json:"result"`
	FinishedAt      *time.Time `json:"finished_at"`
	RecoverySeconds *float64   `json:"recovery_seconds"`
}

// Run is een afgeronde test met de meting die bij haar soort hoort.
type Run struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Trigger    string    `json:"trigger"`
	Result     string    `json:"result"`
	Summary    string    `json:"summary"`
	FinishedAt time.Time `json:"finished_at"`
	// RecoverySeconds: een back-upcontrole, terugzetten plus opstarten.
	RecoverySeconds *float64 `json:"recovery_seconds"`
	// TakeoverMS en DowntimeMS: een failovertest.
	TakeoverMS *int64 `json:"takeover_ms"`
	DowntimeMS *int64 `json:"downtime_ms"`
	// RTOSeconds en RPOSeconds: een DR-repetitie.
	RTOSeconds *float64 `json:"rto_seconds"`
	RPOSeconds *float64 `json:"rpo_seconds"`
}

// Measured is een DR-repetitie die de RTO mat, tegen de doelen van het
// rapport.
type Measured struct {
	RunID      uuid.UUID `json:"run_id"`
	Name       string    `json:"name"`
	Result     string    `json:"result"`
	FinishedAt time.Time `json:"finished_at"`
	RTOSeconds *float64  `json:"rto_seconds"`
	RPOSeconds *float64  `json:"rpo_seconds"`
	// RTOMet en RPOMet zijn nil zonder doel.
	RTOMet *bool `json:"rto_met"`
	RPOMet *bool `json:"rpo_met"`
}

// Risk is een open bevinding op een kaart die bij herstel hoort.
type Risk struct {
	Check    string `json:"check"`
	Card     string `json:"card"`
	Severity string `json:"severity"`
	Text     string `json:"text"`
	Points   int    `json:"points"`
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

// month is het begin van de maand waarin t valt.
func (s *Service) month(t time.Time) time.Time {
	t = t.In(s.loc())
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, s.loc())
}

func dateOf(month time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC), Valid: true}
}

// Recovery geeft het rapport van een maand (JJJJ-MM; leeg is deze maand):
// het bewaarde als dat er is, anders nu berekend.
func (s *Service) Recovery(ctx context.Context, month string) (Recovery, error) {
	now := s.Now()
	from := s.month(now)
	if month != "" {
		t, err := time.ParseInLocation("2006-01", month, s.loc())
		if err != nil {
			return Recovery{}, ErrMonth
		}
		if t.After(from) {
			return Recovery{}, ErrFuture
		}
		from = t
	}
	row, err := s.q.GetRecoveryReport(ctx, dateOf(from))
	switch {
	case err == nil:
		var r Recovery
		if err := json.Unmarshal(row.Report, &r); err != nil {
			return Recovery{}, err
		}
		r.Saved = true
		return r, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Recovery{}, err
	}
	return s.build(ctx, from, now)
}

// Months zijn de maanden met een bewaard rapport, nieuwste eerst.
func (s *Service) Months(ctx context.Context) ([]string, error) {
	rows, err := s.q.ListRecoveryReportMonths(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, d := range rows {
		out = append(out, d.Time.Format("2006-01"))
	}
	return out, nil
}

// Save is de plannertaak: in de eerste dagen van een maand bewaart ze het
// rapport van de maand ervoor, één keer. Een maand waarin nog geen cluster
// bestond, slaat ze over.
func (s *Service) Save(ctx context.Context, now time.Time) error {
	this := s.month(now)
	if now.Sub(this) >= SaveDays*24*time.Hour {
		return nil
	}
	prev := this.AddDate(0, -1, 0)
	if _, err := s.q.GetRecoveryReport(ctx, dateOf(prev)); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	r, err := s.build(ctx, prev, now)
	if err != nil || len(r.Clusters) == 0 {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	n, err := s.q.InsertRecoveryReport(ctx, store.InsertRecoveryReportParams{Month: dateOf(prev), Report: data, GeneratedAt: r.GeneratedAt})
	if err == nil && n > 0 {
		s.log.Info("rapport Herstelbaarheid bewaard", "maand", r.Month, "clusters", len(r.Clusters))
	}
	return err
}

// build rekent het rapport van de maand die op from begint.
func (s *Service) build(ctx context.Context, from, now time.Time) (Recovery, error) {
	to := from.AddDate(0, 1, 0)
	out := Recovery{Month: from.Format("2006-01"), From: from, To: to, GeneratedAt: now, Running: now.Before(to), Clusters: []Cluster{}}
	rows, err := s.q.ListReportClusters(ctx, to)
	if err != nil || len(rows) == 0 {
		return out, err
	}
	runs, err := s.q.ListReportRuns(ctx, store.ListReportRunsParams{Since: from, Before: to})
	if err != nil {
		return out, err
	}
	measured, err := s.q.ListReportMeasuredRehearsals(ctx, to)
	if err != nil {
		return out, err
	}
	bk, err := s.Backups.Overview(ctx)
	if err != nil {
		return out, err
	}
	rk, err := s.Risk.Overview(ctx)
	if err != nil {
		return out, err
	}
	grades := map[uuid.UUID]risk.ClusterScore{}
	for _, c := range rk.Clusters {
		grades[c.ID] = c
	}
	for _, r := range rows {
		c := Cluster{
			ID: r.ID, Name: r.Name, Slug: r.Slug, Environment: string(r.Environment),
			Backups: []Backup{}, Runs: []Run{}, Risks: []Risk{},
		}
		c.RTOTargetMinutes, c.RPOTargetHours = backups.Targets(r.Environment, r.RtoTargetMinutes, r.RpoTargetHours)
		if g, ok := grades[r.ID]; ok {
			c.Grade, c.Score = g.Grade, g.Score
		}
		for _, it := range bk.Items {
			if it.Cluster != nil && it.Cluster.ID == r.ID {
				c.Backups = append(c.Backups, backupOf(it))
			}
		}
		for _, run := range runs {
			if run.ClusterID == r.ID {
				c.Runs = append(c.Runs, runOf(run))
			}
		}
		for _, m := range measured {
			if m.ClusterID == r.ID {
				c.DR = measuredOf(m, c.RTOTargetMinutes, c.RPOTargetHours)
			}
		}
		for _, f := range rk.Findings {
			if f.Cluster == nil || f.Cluster.ID != r.ID || f.Accepted != nil || !slices.Contains(Cards, f.Card) {
				continue
			}
			c.Risks = append(c.Risks, Risk{Check: f.Check, Card: string(f.Card), Severity: string(f.Severity), Text: f.Text, Points: f.Points})
		}
		out.Clusters = append(out.Clusters, c)
	}
	return out, nil
}

func backupOf(it backups.Item) Backup {
	b := Backup{Connection: it.ConnectionName, VMID: it.VMID, Count: it.Count, Freshness: string(it.Freshness), Node: it.Name}
	if it.Node != nil {
		b.Node = it.Node.Name
	}
	if v := it.Latest; v != nil {
		t := v.Time
		b.Time, b.Storage, b.Size = &t, v.Storage, v.Size
	}
	if v := it.LastVerification; v != nil {
		b.LastVerification = &Verification{RunID: v.RunID, Result: v.Result, FinishedAt: v.FinishedAt, RecoverySeconds: v.RecoverySeconds}
	}
	return b
}

// measurements zijn de velden uit test_runs.measurements die het rapport
// gebruikt; elke soort vult er een deel van.
type measurements struct {
	RestoreSeconds *float64 `json:"restore_seconds"`
	BootSeconds    *float64 `json:"boot_seconds"`
	TakeoverMS     *int64   `json:"takeover_ms"`
	DowntimeMS     *int64   `json:"downtime_ms"`
	RTOSeconds     *float64 `json:"rto_seconds"`
	RPOSeconds     *float64 `json:"rpo_seconds"`
}

func runOf(r store.ListReportRunsRow) Run {
	out := Run{ID: r.ID, Kind: r.Kind, Name: r.Name, Trigger: r.Trigger, Result: r.Result, Summary: r.Summary, FinishedAt: r.FinishedAt}
	var m measurements
	if len(r.Measurements) == 0 || json.Unmarshal(r.Measurements, &m) != nil {
		return out
	}
	switch r.Kind {
	case backups.KindVerify:
		if m.RestoreSeconds != nil {
			total := *m.RestoreSeconds
			if m.BootSeconds != nil {
				total += *m.BootSeconds
			}
			out.RecoverySeconds = &total
		}
	case backups.KindRehearsal:
		out.RTOSeconds, out.RPOSeconds = m.RTOSeconds, m.RPOSeconds
	default:
		out.TakeoverMS, out.DowntimeMS = m.TakeoverMS, m.DowntimeMS
	}
	return out
}

func measuredOf(r store.ListReportMeasuredRehearsalsRow, rtoMinutes, rpoHours int) *Measured {
	out := &Measured{RunID: r.ID, Name: r.Name, Result: r.Result, FinishedAt: r.FinishedAt}
	var m measurements
	if json.Unmarshal(r.Measurements, &m) != nil {
		return out
	}
	out.RTOSeconds, out.RPOSeconds = m.RTOSeconds, m.RPOSeconds
	out.RTOMet = met(m.RTOSeconds, float64(rtoMinutes)*60)
	out.RPOMet = met(m.RPOSeconds, float64(rpoHours)*3600)
	return out
}

func met(v *float64, goal float64) *bool {
	if v == nil || goal == 0 {
		return nil
	}
	ok := *v <= goal
	return &ok
}
