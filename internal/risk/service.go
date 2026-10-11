package risk

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
)

const (
	// Interval is hoe vaak de score opnieuw rekent.
	Interval = time.Minute
	// GradeSettle is hoe lang een nieuw cijfer moet blijven voor er een
	// event komt.
	GradeSettle = 10 * time.Minute
	// metricsTTL is hoe lang de risicoqueries geldig blijven.
	metricsTTL = 10 * time.Minute
	// lockID is de advisory lock van de berekening.
	lockID = 4242005
	// minGap is de kortste tijd tussen twee berekeningen na een Kick.
	minGap = 5 * time.Second
)

// FleetID is het onderwerp van de score van het geheel: de nul-uuid.
var FleetID = uuid.Nil

// Service rekent de score elke minuut en na Kick, en bewaart de uitkomst.
// Ze leest alleen en verandert niets aan clusters of nodes.
type Service struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	ev      *events.Writer
	log     *slog.Logger
	metrics *metrics.Client
	backups *backups.Service
	kick    chan struct{}
	started time.Time
	// Warmup is de tijd na het starten waarin niets berekend wordt: na een
	// herstart moeten de agents eerst weer verbinden.
	Warmup time.Duration
	// Now is de klok; tests zetten hem vooruit.
	Now func() time.Time
	// Topo en Deps zijn nodig voor de wat-als, de SPOF's en de back-up
	// van ClusterForge zelf; zonder blijven die weg.
	Topo *topo.Service
	Deps *deps.Service

	mu           sync.Mutex
	metricsAt    time.Time
	metricsCache map[uuid.UUID]*metrics.NodeRisk
	metricsErr   error
	last         time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, mc *metrics.Client, bk *backups.Service) *Service {
	return &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, metrics: mc, backups: bk, kick: make(chan struct{}, 1),
		started: time.Now(), Warmup: 2 * time.Minute, Now: time.Now,
	}
}

// Kick laat de score meteen opnieuw rekenen, bijvoorbeeld na een
// statusovergang.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := s.Compute(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("health score rekenen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
			s.mu.Lock()
			wait := minGap - time.Since(s.last)
			s.mu.Unlock()
			if wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
	}
}

// Compute rekent alles één keer en bewaart wat veranderde. Tijdens de
// opwarmtijd doet ze niets.
func (s *Service) Compute(ctx context.Context) error {
	now := s.Now()
	if now.Sub(s.started) < s.Warmup {
		return nil
	}
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
	w, err := s.gather(ctx, now)
	if err != nil {
		return err
	}
	scored := make([]Scored, len(w.clusters))
	for i, c := range w.clusters {
		scored[i] = Scored{ClusterID: c.ID, Env: c.Env, Result: Score(c, w.acc, now)}
	}
	platform := PlatformCard(w.platform, w.acc, now)
	fs, fg := Fleet(scored, &platform)
	fleet := Result{Score: fs, Grade: fg, Cards: []Card{platform}}
	names := map[uuid.UUID]string{FleetID: "ClusterForge"}
	for _, c := range w.clusters {
		names[c.ID] = c.Name
	}
	return s.save(ctx, now, scored, fleet, names)
}

func (s *Service) save(ctx context.Context, now time.Time, scored []Scored, fleet Result, names map[uuid.UUID]string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lockID).Scan(&locked); err != nil || !locked {
			return err
		}
		q := store.New(tx)
		rows, err := q.ListRiskScores(ctx)
		if err != nil {
			return err
		}
		old := map[uuid.UUID]store.RiskScore{}
		for _, r := range rows {
			old[r.SubjectID] = r
		}
		changed := false
		keep := make([]uuid.UUID, 0, len(scored))
		for _, c := range scored {
			keep = append(keep, c.ClusterID)
			cid := c.ClusterID
			ch, err := s.saveOne(ctx, q, now, "cluster", c.ClusterID, &cid, names[c.ClusterID], c.Result, old)
			if err != nil {
				return err
			}
			changed = changed || ch
		}
		ch, err := s.saveOne(ctx, q, now, "fleet", FleetID, nil, names[FleetID], fleet, old)
		if err != nil {
			return err
		}
		changed = changed || ch
		if err := q.DeleteStaleRiskScores(ctx, keep); err != nil {
			return err
		}
		if changed {
			// De webinterface laadt de score opnieuw.
			if _, err := tx.Exec(ctx, `SELECT pg_notify('cf_events', '{"risk":true}')`); err != nil {
				return err
			}
		}
		return nil
	})
}

// saveOne bewaart de uitkomst van één onderwerp als ze veranderde, en
// schrijft een event als het cijfer GradeSettle anders bleef.
func (s *Service) saveOne(ctx context.Context, q *store.Queries, now time.Time, typ string, id uuid.UUID, cluster *uuid.UUID,
	name string, res Result, old map[uuid.UUID]store.RiskScore) (bool, error) {
	data, err := json.Marshal(res)
	if err != nil {
		return false, err
	}
	var score *int32
	if res.Score != nil {
		v := int32(*res.Score)
		score = &v
	}
	p := store.UpsertRiskScoreParams{
		SubjectType: typ, SubjectID: id, Score: score, Grade: res.Grade, Result: data, ComputedAt: now,
		AnnouncedGrade: res.Grade, GradeSince: now,
	}
	prev, existed := old[id]
	if existed {
		p.AnnouncedGrade, p.GradeSince = prev.AnnouncedGrade, prev.GradeSince
		if prev.Grade != res.Grade {
			p.GradeSince = now
		}
		if p.AnnouncedGrade == "" {
			p.AnnouncedGrade = res.Grade
		}
	}
	announce := existed && res.Grade != "" && res.Grade != p.AnnouncedGrade && now.Sub(p.GradeSince) >= GradeSettle
	if announce {
		from := p.AnnouncedGrade
		p.AnnouncedGrade = res.Grade
		if err := s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: typ, SubjectID: id.String(), ClusterID: cluster,
			Action: "risk.grade_changed",
			Payload: map[string]any{
				"name": name, "from": from, "to": res.Grade, "score": *res.Score, "reasons": topReasons(res, 3),
			},
		}); err != nil {
			return false, err
		}
	}
	if existed && !announce && prev.AnnouncedGrade == p.AnnouncedGrade && prev.GradeSince.Equal(p.GradeSince) &&
		bytes.Equal(normJSON(prev.Result), normJSON(data)) {
		return false, nil
	}
	return true, q.UpsertRiskScore(ctx, p)
}

// normJSON maakt jsonb uit de database vergelijkbaar met wat Go schrijft.
func normJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, _ := json.Marshal(v)
	return out
}

// topReasons zijn de zinnen van de bevindingen die het meeste opleveren.
func topReasons(res Result, n int) []string {
	var all []Finding
	for _, c := range res.Cards {
		for _, f := range c.Findings {
			if f.Accepted == nil {
				all = append(all, f)
			}
		}
	}
	slices.SortStableFunc(all, func(a, b Finding) int {
		if c := cmp.Compare(b.Gain, a.Gain); c != 0 {
			return c
		}
		return cmp.Compare(b.Points, a.Points)
	})
	out := []string{}
	for i := 0; i < len(all) && i < n; i++ {
		out = append(out, all[i].Text)
	}
	return out
}

// Snapshot legt één keer per dag de scores vast voor de trend. Het is een
// taak van de planner; elke minuut proberen kost niets.
func (s *Service) Snapshot(ctx context.Context, now time.Time) error {
	y, m, d := now.Date()
	_, err := s.q.SnapshotRiskScores(ctx, pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true})
	return err
}

// --- Lezen ---

// Stored is een bewaarde uitkomst.
type Stored struct {
	Result
	ComputedAt time.Time `json:"computed_at"`
}

// ErrNotComputed betekent dat er nog geen score is, bijvoorbeeld vlak na de
// start.
var ErrNotComputed = errors.New("de score is nog niet berekend")

// Get geeft de bewaarde uitkomst van een cluster of van het geheel
// (FleetID).
func (s *Service) Get(ctx context.Context, typ string, id uuid.UUID) (Stored, error) {
	row, err := s.q.GetRiskScore(ctx, store.GetRiskScoreParams{SubjectType: typ, SubjectID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{}, ErrNotComputed
	}
	if err != nil {
		return Stored{}, err
	}
	var st Stored
	if err := json.Unmarshal(row.Result, &st.Result); err != nil {
		return Stored{}, err
	}
	st.ComputedAt = row.ComputedAt
	return st, nil
}

// ClusterScore is de score van een cluster in het overzicht.
type ClusterScore struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Env   string    `json:"environment"`
	Score *int      `json:"score"`
	Grade string    `json:"grade"`
}

// Placed is een bevinding met het cluster waar ze bij hoort; nil voor de
// kaart Platform.
type Placed struct {
	Finding
	Cluster *ClusterRef `json:"cluster"`
}

type ClusterRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Env  string    `json:"environment"`
}

// Overview is de score van het geheel met alle bevindingen, gesorteerd op
// wat het oplossen oplevert.
type Overview struct {
	Score      *int           `json:"score"`
	Grade      string         `json:"grade"`
	ComputedAt *time.Time     `json:"computed_at"`
	Platform   *Card          `json:"platform"`
	Clusters   []ClusterScore `json:"clusters"`
	Findings   []Placed       `json:"findings"`
}

// Overview leest de bewaarde scores.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	out := Overview{Clusters: []ClusterScore{}, Findings: []Placed{}}
	rows, err := s.q.ListRiskScores(ctx)
	if err != nil {
		return out, err
	}
	clusters, err := s.q.ListRiskClusters(ctx)
	if err != nil {
		return out, err
	}
	byID := map[uuid.UUID]store.RiskScore{}
	for _, r := range rows {
		byID[r.SubjectID] = r
	}
	if f, ok := byID[FleetID]; ok && f.SubjectType == "fleet" {
		var res Result
		if err := json.Unmarshal(f.Result, &res); err != nil {
			return out, err
		}
		out.Score, out.Grade, out.ComputedAt = res.Score, res.Grade, &f.ComputedAt
		if len(res.Cards) > 0 {
			pc := res.Cards[0]
			out.Platform = &pc
			for _, fd := range pc.Findings {
				out.Findings = append(out.Findings, Placed{Finding: fd})
			}
		}
	}
	for _, c := range clusters {
		cs := ClusterScore{ID: c.ID, Name: c.Name, Env: c.Environment}
		r, ok := byID[c.ID]
		if ok {
			var res Result
			if err := json.Unmarshal(r.Result, &res); err != nil {
				return out, err
			}
			cs.Score, cs.Grade = res.Score, res.Grade
			ref := &ClusterRef{ID: c.ID, Name: c.Name, Env: c.Environment}
			for _, cd := range res.Cards {
				for _, fd := range cd.Findings {
					out.Findings = append(out.Findings, Placed{Finding: fd, Cluster: ref})
				}
			}
		}
		out.Clusters = append(out.Clusters, cs)
	}
	slices.SortStableFunc(out.Findings, func(a, b Placed) int {
		if a.Accepted == nil != (b.Accepted == nil) {
			if a.Accepted == nil {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.FleetGain, a.FleetGain); c != 0 {
			return c
		}
		return cmp.Compare(b.Points, a.Points)
	})
	return out, nil
}

// Grades geeft per cluster de score en het cijfer, voor de clusterlijst.
func (s *Service) Grades(ctx context.Context) (map[uuid.UUID]ClusterScore, error) {
	rows, err := s.q.ListRiskScores(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]ClusterScore{}
	for _, r := range rows {
		if r.SubjectType != "cluster" {
			continue
		}
		cs := ClusterScore{ID: r.SubjectID, Grade: r.Grade}
		if r.Score != nil {
			v := int(*r.Score)
			cs.Score = &v
		}
		out[r.SubjectID] = cs
	}
	return out, nil
}

// Point is één dag van de trend.
type Point struct {
	Day   string `json:"day"`
	Score *int   `json:"score"`
	Grade string `json:"grade"`
}

// History geeft de trend van days dagen, met vandaag de huidige score.
func (s *Service) History(ctx context.Context, typ string, id uuid.UUID, days int) ([]Point, error) {
	now := s.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	rows, err := s.q.ListRiskSnapshots(ctx, store.ListRiskSnapshotsParams{
		SubjectType: typ, SubjectID: id, Since: pgtype.Date{Time: today.AddDate(0, 0, -days+1), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := []Point{}
	for _, r := range rows {
		if !r.Day.Valid || !r.Day.Time.Before(today) {
			continue
		}
		p := Point{Day: r.Day.Time.Format(time.DateOnly), Grade: r.Grade}
		if r.Score != nil {
			v := int(*r.Score)
			p.Score = &v
		}
		out = append(out, p)
	}
	cur, err := s.Get(ctx, typ, id)
	switch {
	case errors.Is(err, ErrNotComputed):
	case err != nil:
		return nil, err
	default:
		out = append(out, Point{Day: today.Format(time.DateOnly), Score: cur.Score, Grade: cur.Grade})
	}
	return out, nil
}

// --- Acceptaties ---

// ValidationError is foute invoer.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound betekent dat de bevinding of acceptatie niet bestaat.
var ErrNotFound = errors.New("niet gevonden")

// AcceptInput is een bevinding die een beheerder accepteert.
type AcceptInput struct {
	Check       string
	SubjectType string
	SubjectID   uuid.UUID
	Reason      string
	ExpiresAt   *time.Time
}

// MaxAcceptance is de langste einddatum van een acceptatie.
const MaxAcceptance = 2 * 365 * 24 * time.Hour

func (s *Service) acceptances(ctx context.Context, now time.Time) (map[Key]Acceptance, error) {
	rows, err := s.q.ListRiskAcceptances(ctx)
	if err != nil {
		return nil, err
	}
	out := map[Key]Acceptance{}
	for _, r := range rows {
		if r.ExpiresAt != nil && !r.ExpiresAt.After(now) {
			continue
		}
		out[Key{r.Check, r.SubjectType, r.SubjectID}] = Acceptance{
			ID: r.ID, Reason: r.Reason, ExpiresAt: r.ExpiresAt, By: r.CreatedByName, At: r.CreatedAt,
		}
	}
	return out, nil
}

// Find zoekt een bevinding in de bewaarde uitkomsten.
func (s *Service) Find(ctx context.Context, k Key) (Placed, bool, error) {
	ov, err := s.Overview(ctx)
	if err != nil {
		return Placed{}, false, err
	}
	for _, f := range ov.Findings {
		if f.Key() == k {
			return f, true, nil
		}
	}
	return Placed{}, false, nil
}

// Accept legt vast dat een bevinding bewust blijft staan en rekent de score
// meteen opnieuw.
func (s *Service) Accept(ctx context.Context, actor events.Actor, by *uuid.UUID, in AcceptInput) error {
	in.Reason = strings.TrimSpace(in.Reason)
	now := s.Now()
	switch n := utf8.RuneCountInString(in.Reason); {
	case n < 3:
		return ValidationError{"geef een reden van minstens 3 tekens"}
	case n > 500:
		return ValidationError{"een reden is hoogstens 500 tekens"}
	case in.ExpiresAt != nil && !in.ExpiresAt.After(now):
		return ValidationError{"de einddatum ligt in het verleden"}
	case in.ExpiresAt != nil && in.ExpiresAt.Sub(now) > MaxAcceptance:
		return ValidationError{"een acceptatie loopt hoogstens twee jaar"}
	case Checks[in.Check] == "":
		return ValidationError{"onbekende controle " + in.Check}
	}
	f, ok, err := s.Find(ctx, Key{in.Check, in.SubjectType, in.SubjectID})
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		a, err := q.InsertRiskAcceptance(ctx, store.InsertRiskAcceptanceParams{
			CheckID: in.Check, SubjectType: in.SubjectType, SubjectID: in.SubjectID, SubjectName: f.Subject.Name,
			Reason: in.Reason, ExpiresAt: in.ExpiresAt, CreatedBy: by,
		})
		if err != nil {
			return err
		}
		payload := map[string]any{
			"check": in.Check, "label": Checks[in.Check], "subject_type": in.SubjectType, "subject_name": f.Subject.Name,
			"text": f.Text, "reason": in.Reason,
		}
		if in.ExpiresAt != nil {
			payload["expires_at"] = in.ExpiresAt.UTC().Format(time.RFC3339)
		}
		var cluster *uuid.UUID
		if f.Cluster != nil {
			cluster = &f.Cluster.ID
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "risk_acceptance", SubjectID: a.ID.String(), ClusterID: cluster,
			Action: "risk.accepted", Payload: payload,
		})
	})
	if err != nil {
		return err
	}
	return s.recompute(ctx)
}

// Unaccept trekt een acceptatie in.
func (s *Service) Unaccept(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		a, err := q.DeleteRiskAcceptance(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "risk_acceptance", SubjectID: a.ID.String(),
			Action: "risk.acceptance_removed",
			Payload: map[string]any{
				"check": a.Check, "label": Checks[a.Check], "subject_type": a.SubjectType, "subject_name": a.SubjectName, "reason": a.Reason,
			},
		})
	})
	if err != nil {
		return err
	}
	return s.recompute(ctx)
}

// recompute rekent meteen opnieuw, zodat de webinterface de acceptatie
// meteen ziet. Lukt dat niet, dan doet de volgende ronde het.
func (s *Service) recompute(ctx context.Context) error {
	if err := s.Compute(ctx); err != nil {
		s.log.Warn("health score na acceptatie rekenen mislukt", "err", err)
	}
	return nil
}
