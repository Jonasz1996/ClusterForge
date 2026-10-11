package whatbroke

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	// Interval is hoe vaak de analyse de open incidenten opnieuw rekent.
	Interval = 30 * time.Second
	// Settle is hoe lang na het einde van een incident de analyse nog
	// bijwerkt, bijvoorbeeld als een journaal na een herverbinding binnenkomt.
	// Daarna ligt ze vast.
	Settle = time.Hour
	// tail is hoe ver na het begin van een incident de tijdlijn van de
	// analyse loopt; de symptomen daarna verklaren niets meer.
	tail = 30 * time.Minute
	// maxMetricNodes is het grootste aantal nodes waarvan de metrics
	// gelezen worden.
	maxMetricNodes = 5
	// anomalyFresh is hoe lang een opgevraagde afwijking geldig blijft zolang
	// er nog punten kunnen bijkomen.
	anomalyFresh = time.Minute
)

// Analyzer rekent de analyse van incidenten en bewaart ze. Hij leest
// alleen, neemt nooit het clusterslot en stuurt geen commando's.
type Analyzer struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	au      *audit.Service
	metrics *metrics.Client
	log     *slog.Logger
	kick    chan struct{}
	// Now is de klok; tests zetten hem vooruit.
	Now func() time.Time

	mu    sync.Mutex
	cache map[anomalyKey]anomalyResult
}

type anomalyKey struct {
	node uuid.UUID
	at   time.Time
}

type anomalyResult struct {
	found   []metrics.Anomaly
	err     error
	fetched time.Time
}

func NewAnalyzer(pool *pgxpool.Pool, mc *metrics.Client, log *slog.Logger) *Analyzer {
	q := store.New(pool)
	return &Analyzer{
		pool: pool, q: q, au: audit.NewService(q), metrics: mc, log: log, kick: make(chan struct{}, 1), Now: time.Now,
		cache: map[anomalyKey]anomalyResult{},
	}
}

// Kick laat de analyse meteen rekenen, bijvoorbeeld als de detector een
// incident opende of bijwerkte.
func (a *Analyzer) Kick() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *Analyzer) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := a.RunOnce(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("incidenten analyseren mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.kick:
		}
	}
}

// RunOnce analyseert de open incidenten, die korter dan Settle geleden
// sloten en die nog nooit geanalyseerd werden.
func (a *Analyzer) RunOnce(ctx context.Context) error {
	now := a.Now()
	incs, err := a.q.ListIncidentsToAnalyze(ctx, now.Add(-Settle))
	if err != nil || len(incs) == 0 {
		return err
	}
	w, err := LoadWorld(ctx, a.q)
	if err != nil {
		return err
	}
	var errs []error
	for _, inc := range incs {
		if err := a.analyze(ctx, inc, w, now); err != nil {
			errs = append(errs, err)
		}
	}
	a.prune(now)
	return errors.Join(errs...)
}

func (a *Analyzer) analyze(ctx context.Context, inc store.Incident, w World, now time.Time) error {
	rows, err := a.q.ListIncidentSubjectsOf(ctx, []uuid.UUID{inc.ID})
	if err != nil {
		return err
	}
	subjects := SubjectsOf(rows)
	in, err := a.input(ctx, inc, subjects, w, now)
	if err != nil {
		return err
	}
	// Eerst zonder metrics voor het eerste symptoom, dan met de afwijkingen
	// van het kwartier ervoor.
	first := Analyze(in).FirstSymptom
	in.Anomalies, in.MetricsUnknown = a.anomalies(ctx, metricNodes(in, first), first.To, now)
	res := Analyze(in)
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if inc.AnalysisVersion != nil && int(*inc.AnalysisVersion) == Version && sameJSON(inc.Analysis, raw) {
		return nil
	}
	v := int32(Version)
	return a.q.SetIncidentAnalysis(ctx, store.SetIncidentAnalysisParams{Analysis: raw, AnalysisVersion: &v, AnalyzedAt: &now, ID: inc.ID})
}

// input leest de tijdlijn van een incident.
func (a *Analyzer) input(ctx context.Context, inc store.Incident, subjects []Subject, w World, now time.Time) (Input, error) {
	from, to := AnalysisWindow(inc, now)
	entries, _, err := Timeline(ctx, a.q, a.au, ScopeFor(inc.ID, subjects, w), from, to, MaxEntries)
	if err != nil {
		return Input{}, err
	}
	return Input{IncidentID: inc.ID, Started: inc.StartedAt, Subjects: subjects, World: w, Entries: entries}, nil
}

// AnalysisWindow is het venster van de tijdlijn die de analyse leest: ruim
// twee uur voor het eerste symptoom, tot een half uur na het begin of het
// einde als dat eerder is.
func AnalysisWindow(inc store.Incident, now time.Time) (time.Time, time.Time) {
	from := inc.StartedAt.Add(-symptomBefore - Lookback - heartbeatDelay)
	to := inc.StartedAt.Add(tail)
	end := now
	if inc.ResolvedAt != nil {
		end = *inc.ResolvedAt
	}
	if end.Add(time.Minute).Before(to) {
		to = end.Add(time.Minute)
	}
	return from, to
}

// metricNodes zijn de nodes waarvan de metrics gelezen worden: die van het
// eerste symptoom en de nodes in het incident, anders de nodes van de
// geraakte clusters, hoogstens maxMetricNodes.
func metricNodes(in Input, first Moment) []Node {
	r := newReach(in.Subjects, in.World)
	var ids []uuid.UUID
	for _, e := range in.Entries {
		if e.ID == first.EntryID && e.NodeID != nil {
			ids = append(ids, *e.NodeID)
		}
	}
	more := sortedIDs(r.symptomNodes)
	if len(more) == 0 {
		more = sortedIDs(r.affected)
	}
	for _, id := range more {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	var out []Node
	for _, id := range ids {
		if n, ok := r.nodes[id]; ok && len(out) < maxMetricNodes {
			out = append(out, n)
		}
	}
	return out
}

// anomalies leest de afwijkingen van de nodes in het kwartier voor at. Het
// tweede antwoord is true als de metrics niet te lezen waren.
func (a *Analyzer) anomalies(ctx context.Context, nodes []Node, at, now time.Time) ([]NodeAnomaly, bool) {
	if a.metrics == nil || !a.metrics.Enabled() {
		return nil, true
	}
	var out []NodeAnomaly
	unknown := false
	for _, n := range nodes {
		res := a.anomaliesOf(ctx, n.ID, at, now)
		if res.err != nil {
			unknown = true
			continue
		}
		for _, an := range res.found {
			out = append(out, NodeAnomaly{NodeID: n.ID, Hostname: n.Hostname, Anomaly: an})
		}
	}
	return out, unknown && len(out) == 0
}

func (a *Analyzer) anomaliesOf(ctx context.Context, node uuid.UUID, at, now time.Time) anomalyResult {
	key := anomalyKey{node, at.Truncate(time.Minute)}
	a.mu.Lock()
	res, ok := a.cache[key]
	a.mu.Unlock()
	// Ruim na at komen er geen punten meer bij.
	if ok && (res.fetched.Sub(at) > 2*time.Minute || now.Sub(res.fetched) < anomalyFresh) {
		return res
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	found, err := a.metrics.Anomalies(qctx, metrics.NodeSelector(node), key.at)
	if err != nil {
		a.log.Debug("afwijkingen in de metrics lezen mislukt", "node", node, "err", err)
	}
	res = anomalyResult{found: found, err: err, fetched: now}
	a.mu.Lock()
	a.cache[key] = res
	a.mu.Unlock()
	return res
}

func (a *Analyzer) prune(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.cache {
		if now.Sub(v.fetched) > 2*Settle {
			delete(a.cache, k)
		}
	}
}

// sameJSON vergelijkt de opgeslagen analyse met de nieuwe. Postgres zet de
// sleutels van jsonb in een eigen volgorde, dus dat gaat via de struct.
func sameJSON(stored, fresh []byte) bool {
	if len(stored) == 0 {
		return false
	}
	var old Analysis
	if json.Unmarshal(stored, &old) != nil {
		return false
	}
	b, err := json.Marshal(old)
	return err == nil && string(b) == string(fresh)
}

// LoadWorld leest de nodes, clusters en afhankelijkheden.
func LoadWorld(ctx context.Context, q *store.Queries) (World, error) {
	w := World{Clusters: map[uuid.UUID]string{}}
	clusters, err := q.ListIncidentClusters(ctx)
	if err != nil {
		return w, err
	}
	for _, c := range clusters {
		w.Clusters[c.ID] = c.Name
	}
	nodes, err := q.ListIncidentNodes(ctx)
	if err != nil {
		return w, err
	}
	for _, n := range nodes {
		w.Nodes = append(w.Nodes, Node{ID: n.ID, Hostname: n.Hostname, ClusterID: n.ClusterID, Host: n.Host})
	}
	deps, err := q.ListClusterDependencies(ctx)
	if err != nil {
		return w, err
	}
	for _, d := range deps {
		if d.FromCluster != nil && d.ToCluster != nil {
			w.Deps = append(w.Deps, [2]uuid.UUID{*d.FromCluster, *d.ToCluster})
		}
	}
	return w, nil
}

// SubjectsOf zet de onderwerpen van een incident om.
func SubjectsOf(rows []store.IncidentSubject) []Subject {
	out := make([]Subject, len(rows))
	for i, s := range rows {
		out[i] = Subject{Type: s.SubjectType, ID: s.SubjectID, Name: s.Name, ClusterID: s.ClusterID, Status: s.WorstStatus,
			Since: s.Since, Until: s.Until}
	}
	return out
}

// Decode leest een opgeslagen analyse; nil als er nog geen is of als ze van
// een andere versie is.
func Decode(inc store.Incident) *Analysis {
	if len(inc.Analysis) == 0 || inc.AnalysisVersion == nil || int(*inc.AnalysisVersion) != Version {
		return nil
	}
	var a Analysis
	if json.Unmarshal(inc.Analysis, &a) != nil {
		return nil
	}
	return &a
}

// TopCause is de oorzaak voor de lijst van incidenten: de eerste als ze
// waarschijnlijk of mogelijk is.
func (a *Analysis) TopCause() *Cause {
	if a == nil || len(a.Causes) == 0 || a.Causes[0].Level == LevelWeak {
		return nil
	}
	c := a.Causes[0]
	c.Summary = phrase(c)
	return &c
}
