package incidents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// statusLock is de advisory lock van de evaluator: de detector rekent na
// diens commit, en nooit tegelijk met een volgende ronde.
const statusLock = 4242001

// testTail is hoe lang na het einde van een failovertest een overgang nog
// door de test komt: de nodes hebben even nodig om te herstellen.
const testTail = 2 * time.Minute

// Detector opent, groepeert en sluit incidenten.
type Detector struct {
	pool  *pgxpool.Pool
	ev    *events.Writer
	log   *slog.Logger
	Rules Rules
	// Now is de klok; tests zetten hem vooruit.
	Now func() time.Time
	// Changed wordt aangeroepen na een ronde die een incident opende,
	// bijwerkte of sloot, na de commit.
	Changed func()
}

func NewDetector(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger) *Detector {
	return &Detector{pool: pool, ev: ev, log: log, Rules: DefaultRules, Now: time.Now}
}

// Detect kijkt één keer wat er misgaat en werkt de incidenten bij. Hij
// draait na elke ronde van de evaluator.
func (d *Detector) Detect(ctx context.Context) error {
	changed := false
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", statusLock); err != nil {
			return err
		}
		q := store.New(tx)
		now := d.Now()
		w, err := d.world(ctx, q, now)
		if err != nil {
			return err
		}
		incs, err := load(ctx, q, now.Add(-d.Rules.Reopen))
		if err != nil {
			return err
		}
		incs, evs := Step(d.Rules, w, incs, now)
		for _, inc := range incs {
			c, err := save(ctx, q, inc)
			if err != nil {
				return err
			}
			changed = changed || c
		}
		for _, e := range evs {
			if err := d.write(ctx, q, e, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && changed && d.Changed != nil {
		d.Changed()
	}
	return err
}

func (d *Detector) world(ctx context.Context, q *store.Queries, now time.Time) (World, error) {
	w := World{Hosts: map[Key][]string{}, Related: map[[2]uuid.UUID]bool{}, Status: map[Key]string{}}
	clusters, err := q.ListIncidentClusters(ctx)
	if err != nil {
		return w, err
	}
	nodes, err := q.ListIncidentNodes(ctx)
	if err != nil {
		return w, err
	}
	services, err := q.ListIncidentServices(ctx)
	if err != nil {
		return w, err
	}
	apps, err := q.ListIncidentApps(ctx)
	if err != nil {
		return w, err
	}
	runs, err := q.ListIncidentTestRuns(ctx, store.ListIncidentTestRunsParams{
		FailedSince: now.Add(-d.Rules.Reopen), RunningSince: now.Add(-testTail),
	})
	if err != nil {
		return w, err
	}
	deps, err := q.ListClusterDependencies(ctx)
	if err != nil {
		return w, err
	}

	env := map[uuid.UUID]string{}
	running := map[uuid.UUID]*uuid.UUID{}
	// Een failovertest of rampoefening die loopt of net klaar was, verklaart
	// de uitval; een rampoefening in elk cluster dat ze mat.
	for _, r := range runs {
		if (r.Kind != "failover.test" && r.Kind != "disaster.run") || (r.FinishedAt != nil && now.Sub(*r.FinishedAt) > testTail) {
			continue
		}
		id := r.ID
		if r.ClusterID != nil {
			running[*r.ClusterID] = &id
		}
		for _, c := range r.ClusterIds {
			running[c] = &id
		}
	}
	nodeHost := map[uuid.UUID]string{}
	for _, n := range nodes {
		k := Key{SubjectNode, n.ID}
		w.Status[k] = n.Status
		if n.Host != "" {
			nodeHost[n.ID] = n.Host
			w.Hosts[k] = []string{n.Host}
			if n.ClusterID != nil {
				ck := Key{SubjectCluster, *n.ClusterID}
				w.Hosts[ck] = appendNew(w.Hosts[ck], n.Host)
			}
		}
	}
	for _, c := range clusters {
		k := Key{SubjectCluster, c.ID}
		env[c.ID] = c.Environment
		w.Status[k] = c.Status
		if !bad(c.Status) {
			continue
		}
		id := c.ID
		w.Problems = append(w.Problems, Problem{
			Key: k, Name: c.Name, ClusterID: &id, Env: c.Environment, Status: c.Status, Reason: c.StatusReason,
			Since: or(c.StatusSince, now), Primary: true, TestRun: running[c.ID],
		})
	}
	for _, n := range nodes {
		if n.Lifecycle != store.NodeLifecycleActive {
			continue
		}
		p := Problem{
			Key: Key{SubjectNode, n.ID}, Name: n.Hostname, ClusterID: n.ClusterID, Status: n.Status, Reason: n.StatusReason,
			Since: or(n.StatusSince, now),
		}
		switch {
		case n.ClusterID == nil && n.Status == "down":
			p.Primary = true
		case n.ClusterID != nil && (n.Status == "down" || n.Status == "degraded"):
			p.Env, p.TestRun = env[*n.ClusterID], running[*n.ClusterID]
		default:
			continue
		}
		w.Problems = append(w.Problems, p)
	}
	for _, s := range services {
		k := Key{SubjectService, s.ID}
		w.Status[k] = s.Status
		switch {
		case s.NodeID != nil && nodeHost[*s.NodeID] != "":
			w.Hosts[k] = []string{nodeHost[*s.NodeID]}
		case s.ClusterID != nil:
			w.Hosts[k] = w.Hosts[Key{SubjectCluster, *s.ClusterID}]
		}
		if s.Status != "down" {
			continue
		}
		p := Problem{Key: k, Name: s.Name, ClusterID: s.ClusterID, Status: s.Status, Reason: s.StatusReason, Since: now, Primary: true}
		if s.ClusterID != nil {
			p.Env, p.TestRun = env[*s.ClusterID], running[*s.ClusterID]
		}
		w.Problems = append(w.Problems, p)
	}
	// Een app die verminderd of down is, kan zelf een incident openen, net
	// als een dienst; het cluster komt erbij.
	for _, a := range apps {
		k := Key{SubjectApp, a.ID}
		w.Status[k] = a.Status
		w.Hosts[k] = w.Hosts[Key{SubjectCluster, a.ClusterID}]
		if !bad(a.Status) {
			continue
		}
		cid := a.ClusterID
		w.Problems = append(w.Problems, Problem{
			Key: k, Name: a.Name + " in " + a.Namespace, ClusterID: &cid, Env: env[a.ClusterID], Status: a.Status,
			Reason: a.StatusReason, Since: or(a.StatusSince, now), Primary: true, TestRun: running[a.ClusterID],
		})
	}
	for _, r := range runs {
		if !r.Failed || r.FinishedAt == nil {
			continue
		}
		k := Key{SubjectTestRun, r.ID}
		name := "Geplande back-upcontrole van " + r.Hostname
		if r.Kind == "failover.test" {
			name = "Geplande failovertest op " + r.ClusterName
		}
		p := Problem{Key: k, Name: name, ClusterID: r.ClusterID, Status: r.Result, Reason: clip(r.Summary, 300),
			Since: *r.FinishedAt, Primary: true, Point: true}
		if r.ClusterID != nil {
			p.Env = env[*r.ClusterID]
			w.Hosts[k] = w.Hosts[Key{SubjectCluster, *r.ClusterID}]
		}
		w.Problems = append(w.Problems, p)
	}

	// Een cronjob waarvan de laatste run mislukte, tot hij weer lukt.
	crons, err := q.ListIncidentCrons(ctx)
	if err != nil {
		return w, err
	}
	for _, c := range crons {
		k := Key{SubjectCron, *c.SubjectID}
		if !c.Failing {
			w.Status[k] = "healthy"
			continue
		}
		reason := "de laatste run mislukte"
		if c.LastExit != nil {
			reason = fmt.Sprintf("de laatste run mislukte met exitcode %d", *c.LastExit)
		}
		p := Problem{Key: k, Name: clip(c.Command, 60) + " op " + c.Hostname, ClusterID: c.ClusterID, Status: "fail",
			Reason: reason + " (" + c.File + ")", Since: or(c.LastRunAt, now), Primary: true}
		w.Status[k] = "fail"
		if h := nodeHost[c.NodeID]; h != "" {
			w.Hosts[k] = []string{h}
		}
		if c.ClusterID != nil {
			p.Env = env[*c.ClusterID]
		}
		w.Problems = append(w.Problems, p)
	}

	// Een beheerde cronjob waarvan de laatste run op een node mislukte, tot
	// die weer lukt; per job uit te zetten.
	managed, err := q.ListIncidentCronJobs(ctx)
	if err != nil {
		return w, err
	}
	failing := map[uuid.UUID]*Problem{}
	var order []uuid.UUID
	for _, r := range managed {
		k := Key{SubjectCronJob, r.JobID}
		if _, ok := w.Status[k]; !ok {
			w.Status[k] = "healthy"
		}
		if !r.Alert || !r.Enabled || (r.Result != "failed" && r.Result != "timeout") {
			continue
		}
		reason := r.Hostname + ": de laatste run mislukte"
		switch {
		case r.Result == "timeout":
			reason = r.Hostname + ": de laatste run haalde de tijdslimiet niet"
		case r.ExitCode != nil:
			reason = fmt.Sprintf("%s: de laatste run mislukte met exitcode %d", r.Hostname, *r.ExitCode)
		}
		if p := failing[r.JobID]; p != nil {
			p.Reason += "; " + reason
			w.Hosts[k] = append(w.Hosts[k], r.Hostname)
			continue
		}
		cid := r.ClusterID
		p := &Problem{Key: k, Name: r.Name, ClusterID: &cid, Status: "fail", Reason: reason, Since: r.StartedAt, Primary: true, Env: env[cid]}
		failing[r.JobID] = p
		order = append(order, r.JobID)
		w.Status[k] = "fail"
		w.Hosts[k] = []string{r.Hostname}
	}
	for _, id := range order {
		w.Problems = append(w.Problems, *failing[id])
	}

	// Clusters die via de graaf van elkaar afhangen, ook over een tussenstap.
	next := map[uuid.UUID][]uuid.UUID{}
	for _, dep := range deps {
		if dep.FromCluster != nil && dep.ToCluster != nil {
			next[*dep.FromCluster] = append(next[*dep.FromCluster], *dep.ToCluster)
		}
	}
	for from := range next {
		todo, seen := []uuid.UUID{from}, map[uuid.UUID]bool{from: true}
		for len(todo) > 0 {
			c := todo[0]
			todo = todo[1:]
			for _, to := range next[c] {
				if !seen[to] {
					seen[to] = true
					todo = append(todo, to)
					w.Related[[2]uuid.UUID{from, to}], w.Related[[2]uuid.UUID{to, from}] = true, true
				}
			}
		}
	}
	return w, nil
}

func bad(status string) bool {
	return status == "degraded" || status == "down" || status == "split_brain"
}

func or(t *time.Time, def time.Time) time.Time {
	if t == nil {
		return def
	}
	return *t
}

func appendNew(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func load(ctx context.Context, q *store.Queries, since time.Time) ([]*Incident, error) {
	rows, err := q.ListDetectorIncidents(ctx, since)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	byID := map[uuid.UUID]*Incident{}
	out := make([]*Incident, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		out[i] = &Incident{
			ID: r.ID, Number: r.Number, Title: r.Title, Severity: r.Severity, Open: r.Status == "open",
			StartedAt: r.StartedAt, DetectedAt: r.DetectedAt, ResolvedAt: r.ResolvedAt, CausedByTest: r.CausedByTest,
			TestRunID: r.TestRunID,
		}
		byID[r.ID] = out[i]
	}
	subjects, err := q.ListIncidentSubjectsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, s := range subjects {
		inc := byID[s.IncidentID]
		inc.Subjects = append(inc.Subjects, &Subject{
			Key: Key{s.SubjectType, s.SubjectID}, Name: s.Name, ClusterID: s.ClusterID, First: s.FirstStatus,
			Worst: s.WorstStatus, Last: s.LastStatus, Reason: s.Reason, Since: s.Since, Until: s.Until,
		})
	}
	return out, nil
}

// save schrijft wat er aan een incident veranderde, en zegt of dat iets was.
func save(ctx context.Context, q *store.Queries, inc *Incident) (bool, error) {
	status := "resolved"
	if inc.Open {
		status = "open"
	}
	changed := inc.ID == uuid.Nil || inc.dirty
	switch {
	case inc.ID == uuid.Nil:
		row, err := q.CreateIncident(ctx, store.CreateIncidentParams{
			Title: inc.Title, Severity: inc.Severity, StartedAt: inc.StartedAt, DetectedAt: inc.DetectedAt,
			CausedByTest: inc.CausedByTest, TestRunID: inc.TestRunID,
		})
		if err != nil {
			return false, err
		}
		inc.ID, inc.Number = row.ID, row.Number
		if inc.Open {
			break
		}
		fallthrough
	case inc.dirty:
		if err := q.UpdateIncidentState(ctx, store.UpdateIncidentStateParams{
			ID: inc.ID, Severity: inc.Severity, Status: status, ResolvedAt: inc.ResolvedAt, StartedAt: inc.StartedAt,
		}); err != nil {
			return false, err
		}
	}
	inc.dirty = false
	for _, s := range inc.Subjects {
		if !s.dirty {
			continue
		}
		changed = true
		if err := q.UpsertIncidentSubject(ctx, store.UpsertIncidentSubjectParams{
			IncidentID: inc.ID, SubjectType: s.Type, SubjectID: s.ID, Name: s.Name, ClusterID: s.ClusterID,
			FirstStatus: s.First, WorstStatus: s.Worst, LastStatus: s.Last, Reason: s.Reason, Since: s.Since, Until: s.Until,
		}); err != nil {
			return false, err
		}
		s.dirty = false
	}
	return changed, nil
}

func (d *Detector) write(ctx context.Context, q *store.Queries, e Event, now time.Time) error {
	inc := e.Incident
	p := map[string]any{"number": inc.Number, "title": inc.Title, "severity": inc.Severity}
	switch e.Action {
	case "incident.opened":
		names := make([]string, 0, len(inc.Subjects))
		for _, s := range inc.Subjects {
			names = append(names, s.Name)
		}
		p["subjects"], p["caused_by_test"] = names, inc.CausedByTest
	case "incident.escalated":
		p["from"] = e.From
	case "incident.resolved":
		p["duration_seconds"] = int(now.Sub(inc.StartedAt).Seconds())
	}
	var cluster *uuid.UUID
	for _, s := range inc.Subjects {
		if s.ClusterID != nil {
			cluster = s.ClusterID
			break
		}
	}
	return d.ev.Write(ctx, q, events.Event{
		Actor: events.System(), SubjectType: "incident", SubjectID: inc.ID.String(), ClusterID: cluster,
		Action: e.Action, Payload: p,
	})
}
