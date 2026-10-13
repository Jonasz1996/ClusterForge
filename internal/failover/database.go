package failover

// Failovertests op een cluster met een onderdeel Database (fase 5). Raakt de
// test het VIP van de database, dan schrijft de agent op een andere datanode
// tijdens de test elke halve seconde een rij via dat VIP, in de eigen kleine
// database clusterforge_probe. Het verslag zegt hoe lang schrijven niet
// lukte, en of er een bevestigde rij verloren ging. Een database die
// ClusterForge niet beheert, blijft een reden om niet te testen: daar weet
// niemand wat er met de schrijfacties gebeurt.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// DatabaseStop stopt de database op de eigenaar van het VIP van een
// database: MariaDB bij Galera, Patroni (en dus PostgreSQL) bij Patroni.
const DatabaseStop = "database_stop"

// DatabaseUnits zijn de diensten die database_stop mag stoppen.
var DatabaseUnits = []string{"mariadb", "patroni"}

// Database is een onderdeel Database van een cluster, zoals de
// failovertests het zien.
type Database struct {
	ComponentID uuid.UUID
	Engine      string
	// Title is de naam, zoals MariaDB met Galera.
	Title string
	// VIP is het adres van het VIP van de database, leeg zonder; OwnVIP:
	// het is een eigen VIP, niet dat van de app.
	VIP    string
	OwnVIP bool
	Port   int
	// Unit is de dienst die database_stop stopt.
	Unit string
	// Nodes zijn de datanodes.
	Nodes []DatabaseNode
}

// DatabaseNode is een datanode.
type DatabaseNode struct {
	ID       uuid.UUID
	Hostname string
}

// DBComponents geeft de failovertests de databases van een cluster;
// internal/components doet het.
type DBComponents interface {
	// TestDatabases geeft de uitgerolde onderdelen Database van een
	// cluster.
	TestDatabases(ctx context.Context, clusterID uuid.UUID) ([]Database, error)
	// TestHealth leest de database nu, zonder de bewaking (die slaat een
	// cluster met een lopende taak over): gezond, of waarom niet.
	TestHealth(ctx context.Context, clusterID, componentID uuid.UUID) (bool, string, error)
	// PrepareWrites maakt op de node die schrijft de database
	// clusterforge_probe en de gebruiker cf_probe met dit wachtwoord, voor
	// alle nodes en VIP's van het cluster.
	PrepareWrites(ctx context.Context, clusterID, componentID uuid.UUID, password string) error
	// WriteProbe stuurt een DBWriteProbe naar de agent van een node.
	WriteProbe(ctx context.Context, nodeID uuid.UUID, engine string, req protocol.DBProbeRequest) (*protocol.DBProbeResult, error)
}

// WritePlan is de schrijftest van een test op een cluster met een
// database; hij staat in het plan van de voorcontrole.
type WritePlan struct {
	ComponentID uuid.UUID `json:"component_id"`
	Engine      string    `json:"engine"`
	Title       string    `json:"title"`
	VIP         string    `json:"vip"`
	Port        int       `json:"port"`
	// Prober is de datanode die schrijft.
	Prober health.Holder `json:"prober"`
	// StartedAt is het begin van de schrijftest op de klok van de server;
	// de pogingen hebben hun tijd sinds dat begin. Nul: hij liep nog niet.
	StartedAt time.Time `json:"started_at,omitzero"`
}

// WriteMeasurements is wat de schrijftest zag, in test_runs.measurements.
type WriteMeasurements struct {
	Node  string `json:"node"`
	Title string `json:"title"`
	VIP   string `json:"vip"`
	Port  int    `json:"port"`
	// DowntimeMS loopt tijdens de meting van de eerste mislukte poging tot
	// de eerste van drie goede op rij; Recovered: schrijven lukte aan het
	// eind van de meting weer.
	DowntimeMS *int64 `json:"downtime_ms,omitempty"`
	Recovered  bool   `json:"recovered"`
	// FailbackMS is de onderbreking bij de terugkeer.
	FailbackMS *int64 `json:"failback_ms,omitempty"`
	// Attempts is het aantal pogingen sinds de storing, Failed hoeveel
	// daarvan mislukten.
	Attempts int `json:"attempts"`
	Failed   int `json:"failed"`
	// Confirmed is het aantal bevestigde rijen van de hele test, Lost
	// hoeveel daarvan er na de test niet meer waren (nil als tellen niet
	// lukte), met de eerste volgnummers in LostSeqs. Extra zijn rijen
	// waarvan de bevestiging niet aankwam maar die er wel zijn.
	Confirmed  int     `json:"confirmed"`
	Lost       *int    `json:"lost,omitempty"`
	LostSeqs   []int64 `json:"lost_seqs,omitempty"`
	Extra      int     `json:"extra,omitempty"`
	CheckError string  `json:"check_error,omitempty"`
	// Segments zijn de stukken met één uitkomst, in ms na de storing.
	Segments []Segment `json:"segments"`
	// Timeline zijn de gebeurtenissen van de schrijftest.
	Timeline []TimelineEvent `json:"-"`
}

// databases geeft de databases van een cluster; nil zonder DB.
func (s *Service) databases(ctx context.Context, clusterID uuid.UUID) ([]Database, error) {
	if s.DB == nil {
		return nil, nil
	}
	return s.DB.TestDatabases(ctx, clusterID)
}

// engineOf is de engine van een databasedienst.
func engineOf(service string) string {
	if service == "postgresql" || service == "patroni" {
		return protocol.DBPostgreSQL
	}
	return protocol.DBMariaDB
}

// databasesOn noemt de nodes waarop een database actief is die niet bij een
// onderdeel Database van het cluster hoort.
func databasesOn(nodes []store.ListFailoverNodesRow, dbs []Database) []string {
	var out []string
	for _, n := range nodes {
		st := health.ServiceStates(n.Services)
		for _, svc := range Databases {
			if st[svc] != "active" {
				continue
			}
			ours := slices.ContainsFunc(dbs, func(d Database) bool {
				return d.Engine == engineOf(svc) && slices.ContainsFunc(d.Nodes, func(dn DatabaseNode) bool { return dn.ID == n.ID })
			})
			if !ours {
				out = append(out, svc+" op "+n.Hostname)
			}
		}
	}
	return out
}

// dbOfVIP is de database met dit VIP.
func dbOfVIP(dbs []Database, vip string) (Database, bool) {
	i := slices.IndexFunc(dbs, func(d Database) bool { return d.VIP != "" && d.VIP == vip })
	if i < 0 {
		return Database{}, false
	}
	return dbs[i], true
}

// writeTarget is de database waarvan de test het VIP raakt: het VIP van de
// test, of een VIP dat ook op het doel staat (keepalived stoppen neemt alle
// VIP's van de node mee).
func writeTarget(dbs []Database, def Definition, targetVIPs []string) (Database, bool) {
	if d, ok := dbOfVIP(dbs, def.VIP); ok {
		return d, true
	}
	if def.Scenario == ServiceStop {
		// nginx of haproxy stoppen verplaatst alleen het VIP van de test.
		return Database{}, false
	}
	for _, v := range targetVIPs {
		if d, ok := dbOfVIP(dbs, v); ok {
			return d, true
		}
	}
	return Database{}, false
}

// DatabaseRef is een database die de storing raakt; de terugkeer wacht tot
// ze weer gezond is.
type DatabaseRef struct {
	ComponentID uuid.UUID `json:"component_id"`
	Title       string    `json:"title"`
}

// databaseChecks zijn de voorwaarden van een test op een cluster met een
// onderdeel Database. Een test raakt een database als hij het VIP ervan
// verplaatst of als hij een datanode stopt (de database of de hele VM).
// Dan moet ze gezond zijn, want anders kan een node minder de meerderheid
// kosten, en schrijft een andere datanode tijdens de test via haar VIP.
func (s *Service) databaseChecks(ctx context.Context, plan *Plan, c store.Cluster, def Definition, dbs []Database,
	nodes []store.ListFailoverNodesRow, now time.Time, add func(string, bool, string)) {
	w, ok := writeTarget(dbs, def, plan.TargetVIPs)
	switch {
	case def.Scenario == DatabaseStop && (!ok || w.VIP != def.VIP):
		add("Database", false, "de database stoppen kan alleen op het VIP van een database")
		return
	case def.Scenario == DatabaseStop && w.Unit != def.Unit:
		add("Database", false, fmt.Sprintf("%s draait met %s, maar de test stopt %s; sla de test opnieuw op", w.Title, w.Unit, def.Unit))
		return
	case ok && w.Engine == protocol.DBPostgreSQL && w.OwnVIP && def.Scenario == KeepalivedStop:
		add("Database", false, fmt.Sprintf("het VIP %s van %s volgt de primary: zonder failover van Patroni neemt geen andere node het over. "+
			"Test het met De database stoppen", w.VIP, w.Title))
		return
	}
	var hit []Database
	if ok {
		hit = append(hit, w)
	}
	if def.Scenario == VMHardStop {
		for _, d := range dbs {
			if d.ComponentID != w.ComponentID && slices.ContainsFunc(d.Nodes, func(n DatabaseNode) bool { return n.ID == plan.Target.ID }) {
				hit = append(hit, d)
			}
		}
	}
	if len(hit) == 0 {
		add("Database", true, "de test raakt geen database; er loopt geen schrijftest mee")
		return
	}
	for _, d := range hit {
		healthy, reason, err := s.DB.TestHealth(ctx, c.ID, d.ComponentID)
		switch {
		case err != nil:
			add("Database gezond", false, fmt.Sprintf("%s is niet te lezen: %v", d.Title, err))
		case !healthy:
			add("Database gezond", false, reason)
		default:
			add("Database gezond", true, d.Title+": alle datanodes doen mee")
		}
		plan.Databases = append(plan.Databases, DatabaseRef{ComponentID: d.ComponentID, Title: d.Title})
	}
	if !ok {
		// Een VM die uitgaat, raakt het VIP van de database niet: schrijven
		// via dat VIP hoort gewoon te blijven lukken.
		i := slices.IndexFunc(hit, func(d Database) bool { return d.VIP != "" })
		if i < 0 {
			add("Schrijftest", true, "de database heeft geen VIP; er loopt geen schrijftest mee")
			return
		}
		w = hit[i]
	}
	// De node die schrijft: een andere datanode met een verse heartbeat en
	// een agent die de schrijftest kent.
	var why []string
	for _, dn := range w.Nodes {
		if dn.ID == plan.Target.ID {
			continue
		}
		i := slices.IndexFunc(nodes, func(n store.ListFailoverNodesRow) bool { return n.ID == dn.ID })
		switch {
		case i < 0:
		case !nodes[i].HasAgent || nodes[i].HeartbeatAt == nil || now.Sub(*nodes[i].HeartbeatAt) > s.Fresh:
			why = append(why, dn.Hostname+" heeft geen verse heartbeat")
		case nodes[i].AgentProtocol < protocol.DBProbeSince:
			why = append(why, fmt.Sprintf("de agent op %s heeft protocol %d", dn.Hostname, nodes[i].AgentProtocol))
		default:
			plan.Writes = &WritePlan{
				ComponentID: w.ComponentID, Engine: w.Engine, Title: w.Title, VIP: w.VIP, Port: w.Port,
				Prober: health.Holder{ID: dn.ID, Hostname: dn.Hostname},
			}
			add("Schrijftest", true, fmt.Sprintf("%s schrijft tijdens de test elke halve seconde een rij via %s:%d", dn.Hostname, w.VIP, w.Port))
			return
		}
	}
	detail := "geen andere datanode van " + w.Title + " kan schrijven via " + w.VIP
	if len(why) > 0 {
		detail += ": " + strings.Join(why, "; ")
	}
	add("Schrijftest", false, fmt.Sprintf("%s; de schrijftest vraagt agentprotocol %d, werk de agents bij met het installatiescript en --upgrade",
		detail, protocol.DBProbeSince))
}

// forDatabase past een nieuwe of gewijzigde test aan de databases van het
// cluster aan: de database stoppen kan alleen op het VIP van een database
// en stopt dan haar dienst. Een eigen VIP van een database komt niet terug
// naar de eerste node; het blijft waar de database schrijft.
func (s *Service) forDatabase(ctx context.Context, clusterID uuid.UUID, vip string, in *Input) error {
	dbs, err := s.databases(ctx, clusterID)
	if err != nil {
		return err
	}
	d, ok := dbOfVIP(dbs, vip)
	if in.Scenario == DatabaseStop {
		if !ok {
			return &FieldError{Field: "vip_id", Message: "de database stoppen kan alleen op het VIP van een database"}
		}
		in.Service = d.Unit
	}
	if ok && d.OwnVIP && in.ExpectFailback {
		return &FieldError{Field: "expect_failback", Message: fmt.Sprintf("het VIP %s van %s komt niet terug naar de eerste node: "+
			"het blijft waar de database schrijft", vip, d.Title)}
	}
	return nil
}

// writeLimit is hoe lang de agent hoogstens schrijft: de meting, de
// terugkeer en wat marge. Daarna stopt hij vanzelf.
func (s *Service) writeLimit(def Definition) int {
	d := s.MaxWindow + s.returnTimeout(def) + s.DBReturnTimeout + 2*time.Minute
	return min(int(d.Seconds()), protocol.DBProbeMaxSeconds)
}

// probeRequest stuurt een DBWriteProbe naar de node die schrijft.
func (s *Service) probeRequest(ctx context.Context, w *WritePlan, req protocol.DBProbeRequest) (*protocol.DBProbeResult, error) {
	return s.DB.WriteProbe(ctx, w.Prober.ID, w.Engine, req)
}

// startWrites begint de schrijftest in de voorcontrole van de taak, en
// wacht tot de agent drie keer na elkaar kon schrijven. Liep hij al (een
// hervatte taak), dan blijft het wachtwoord hetzelfde.
func (r *testRun) startWrites(ctx context.Context, st *jobs.Step, w *WritePlan) error {
	s, run := r.s, r.run.ID.String()
	where := fmt.Sprintf("%s:%d", w.VIP, w.Port)
	res, err := s.probeRequest(ctx, w, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: run})
	if err != nil || !res.Running {
		password := rand.Text()
		st.Logf("de database %s en de gebruiker %s klaarzetten", protocol.DBProbeDatabase, protocol.DBProbeUser)
		_ = st.Flush(ctx)
		if err := s.DB.PrepareWrites(ctx, r.cluster, w.ComponentID, password); err != nil {
			return fmt.Errorf("de schrijftest klaarzetten lukte niet: %w", err)
		}
		res, err = s.probeRequest(ctx, w, protocol.DBProbeRequest{
			Action: protocol.DBProbeStart, Run: run, Host: w.VIP, Port: w.Port, Password: password, MaxSeconds: s.writeLimit(r.def),
		})
		if err != nil {
			return fmt.Errorf("%s kan niet schrijven via %s: %w", w.Prober.Hostname, where, err)
		}
	}
	w.StartedAt = time.Now().Add(-time.Duration(res.ElapsedMS) * time.Millisecond)
	st.Logf("%s schrijft elke %d ms een rij via %s in %s", w.Prober.Hostname, protocol.DBProbeInterval.Milliseconds(), where, protocol.DBProbeDatabase)
	_ = st.Flush(ctx)
	wl := NewWriteLog(w.StartedAt, w.VIP)
	from := 0
	deadline := time.Now().Add(s.WriteStart)
	last := ""
	for {
		res, err = s.probeRequest(ctx, w, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: run, From: from})
		if err == nil {
			from = addAttempts(wl, w.StartedAt, res, from)
			if len(res.Attempts) > 0 {
				if a := res.Attempts[len(res.Attempts)-1]; !a.OK {
					last = a.Error
				}
			}
			if wl.Recovered() {
				return nil
			}
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			r.stopWrites(w)
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%s kon niet drie keer na elkaar schrijven via %s: %s", w.Prober.Hostname, where, or(last, "geen pogingen"))
		}
		select {
		case <-ctx.Done():
		case <-time.After(s.WritePoll):
		}
	}
}

// addAttempts zet de nieuwe pogingen in wl en geeft de volgende From.
func addAttempts(wl *ProbeLog, startedAt time.Time, res *protocol.DBProbeResult, from int) int {
	for _, a := range res.Attempts {
		var err error
		if !a.OK {
			err = errors.New(a.Error)
		}
		wl.Add(startedAt.Add(time.Duration(a.AtMS)*time.Millisecond), err)
	}
	return from + len(res.Attempts)
}

// stopWrites stopt de schrijftest zonder te tellen, los van de taak.
func (r *testRun) stopWrites(w *WritePlan) {
	ctx, cancel := context.WithTimeout(context.Background(), r.s.EmergencyTimeout)
	defer cancel()
	if _, err := r.s.probeRequest(ctx, w, protocol.DBProbeRequest{Action: protocol.DBProbeStop, Run: r.run.ID.String()}); err != nil {
		r.s.log.Warn("schrijftest stoppen mislukt", "run", r.run.ID, "node", w.Prober.Hostname, "err", err)
	}
}

// writeWatch leest tijdens de meting de pogingen van de agent.
type writeWatch struct {
	wl   *ProbeLog
	from int
	at   time.Time
}

func (r *testRun) newWriteWatch(injectedAt time.Time) *writeWatch {
	if r.plan.Writes == nil || r.plan.Writes.StartedAt.IsZero() {
		return nil
	}
	return &writeWatch{wl: NewWriteLog(injectedAt, r.plan.Writes.VIP)}
}

// poll haalt de nieuwe pogingen op, hoogstens eens per WritePoll. Pogingen
// van voor de storing tellen niet.
func (r *testRun) poll(ctx context.Context, w *writeWatch, injectedAt time.Time) {
	if w == nil || time.Since(w.at) < r.s.WritePoll {
		return
	}
	w.at = time.Now()
	wp := r.plan.Writes
	res, err := r.s.probeRequest(ctx, wp, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: r.run.ID.String(), From: w.from})
	if err != nil {
		return
	}
	for _, a := range res.Attempts {
		at := wp.StartedAt.Add(time.Duration(a.AtMS) * time.Millisecond)
		if at.Before(injectedAt) {
			continue
		}
		var aerr error
		if !a.OK {
			aerr = errors.New(a.Error)
		}
		w.wl.Add(at, aerr)
	}
	w.from += len(res.Attempts)
}

// recovered: de schrijftest raakte de storing niet, of schrijven lukte
// weer drie keer na elkaar.
func (w *writeWatch) recovered() bool { return w == nil || w.wl.Recovered() }

// finishWrites rondt de schrijftest af in het rapport: na een storing telt
// de agent de rijen, anders stopt hij alleen. Een hervatte taak vraagt het
// opnieuw en krijgt hetzelfde antwoord.
func (r *testRun) finishWrites(ctx context.Context, o outcome) *WriteMeasurements {
	w := r.plan.Writes
	if w == nil || w.StartedAt.IsZero() {
		return nil
	}
	if !o.injected {
		r.stopWrites(w)
		return nil
	}
	out := &WriteMeasurements{Node: w.Prober.Hostname, Title: w.Title, VIP: w.VIP, Port: w.Port, Segments: []Segment{}}
	cctx, cancel := context.WithTimeout(ctx, r.s.WriteFinishTimeout)
	defer cancel()
	res, err := r.s.probeRequest(cctx, w, protocol.DBProbeRequest{Action: protocol.DBProbeFinish, Run: r.run.ID.String()})
	if err != nil {
		out.CheckError = "de schrijftest afronden op " + w.Prober.Hostname + " lukte niet: " + err.Error()
		return out
	}
	out.Confirmed, out.Extra = res.Confirmed, res.Extra
	if res.Checked {
		lost := res.Missing
		out.Lost, out.LostSeqs = &lost, res.MissingSeqs
	} else {
		out.CheckError = or(res.CheckError, "de agent telde de rijen niet")
	}
	// De meting loopt tot EndMS; wat daarna komt, is de terugkeer.
	measureEnd := time.Time{}
	if o.m != nil {
		measureEnd = r.injectedAt.Add(time.Duration(o.m.EndMS) * time.Millisecond)
	}
	during, after := NewWriteLog(r.injectedAt, w.VIP), NewWriteLog(r.injectedAt, w.VIP)
	var lastDuring, lastAfter time.Time
	for _, a := range res.Attempts {
		at := w.StartedAt.Add(time.Duration(a.AtMS) * time.Millisecond)
		if at.Before(r.injectedAt) {
			continue
		}
		var aerr error
		if !a.OK {
			aerr = errors.New(a.Error)
			out.Failed++
		}
		out.Attempts++
		if measureEnd.IsZero() || !at.After(measureEnd) {
			during.Add(at, aerr)
			lastDuring = at
		} else {
			after.Add(at, aerr)
			lastAfter = at
		}
	}
	if lastDuring.IsZero() && out.CheckError == "" {
		out.CheckError = "de agent op " + w.Prober.Hostname + " deed geen pogingen tijdens de meting"
	}
	if !lastDuring.IsZero() {
		end := later(lastDuring, measureEnd)
		down := during.Downtime(end).Milliseconds()
		out.DowntimeMS, out.Recovered = &down, during.Recovered()
		segs, events := during.Finish(end)
		out.Segments = append(out.Segments, segs...)
		out.Timeline = append(out.Timeline, events...)
	}
	if !lastAfter.IsZero() {
		back := after.Downtime(lastAfter).Milliseconds()
		out.FailbackMS = &back
		segs, events := after.Finish(lastAfter)
		out.Segments = append(out.Segments, segs...)
		for i := range events {
			if events[i].Kind == "write_down" {
				events[i].Text += " bij de terugkeer"
			}
		}
		out.Timeline = append(out.Timeline, events...)
	}
	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
