package failover

// De failovertest 'alles herstarten' (fase 5, mijlpaal 13). ClusterForge
// herstart alle nodes van een database tegelijk, zoals bij een herstart van
// de Proxmox-host, en meet hoe lang het duurt tot de database weer gezond
// is: alle datanodes Synced bij Galera, een primary met zijn replica's bij
// Patroni. Tijdens de test neemt de taak het clusterslot, dus de bewaking
// slaat het cluster over; daarom doet de test zelf wat de bewaking zou doen
// (DBComponents.RestartRecover). De node die schrijft, herstart mee: de
// server onthoudt welke rijen de database bevestigde, en na de test kijkt
// diezelfde node na of ze er nog zijn (protocol 15).

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// RestartAll herstart alle nodes van de database achter het VIP tegelijk.
const RestartAll = "restart_all"

// MaxRestartSeconds is de hoogste verwachting van alles herstarten; de
// andere scenario's gaan tot 120 s.
const MaxRestartSeconds = 1800

// stepVerify kijkt na de test na of de bevestigde rijen er nog zijn. Het
// antwoord staat in de state van de stap: een tweede keer nakijken vindt
// niets meer, want de rijen zijn dan weg.
const stepVerify = "Rijen nakijken"

// RestartPlan zijn de nodes die de test herstart: de datanodes en de
// scheidsrechters van de database.
type RestartPlan struct {
	ComponentID uuid.UUID       `json:"component_id"`
	Engine      string          `json:"engine"`
	Title       string          `json:"title"`
	Nodes       []RestartTarget `json:"nodes"`
}

// RestartTarget is een node die herstart.
type RestartTarget struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
	Arbiter  bool      `json:"arbiter,omitempty"`
}

func (p *RestartPlan) hosts() string {
	names := make([]string, len(p.Nodes))
	for i, n := range p.Nodes {
		names[i] = n.Hostname
	}
	return joinNames(names)
}

// restartState staat in de state van de meting.
type restartState struct {
	// Confirmed zijn de volgnummers die de database bevestigde, voor zover
	// de server ze las voor de node die schrijft herstartte; From is de
	// eerste poging die hij nog niet las.
	Confirmed []int64 `json:"confirmed,omitempty"`
	From      int     `json:"from"`
	// Sent zijn de nodes die de herstart aannamen.
	Sent []uuid.UUID `json:"sent,omitempty"`
}

// RestartMeasurement is wat de meting van alles herstarten zag, in ms na
// het versturen van de herstart.
type RestartMeasurement struct {
	Title  string        `json:"title"`
	Engine string        `json:"engine"`
	Nodes  []RestartNode `json:"nodes"`
	// BackMS: alle nodes waren terug; HealthyMS: de database was weer
	// gezond. Nil als dat niet binnen de meting gebeurde.
	BackMS    *int64 `json:"back_ms,omitempty"`
	HealthyMS *int64 `json:"healthy_ms,omitempty"`
	// Began is de node waarop ClusterForge Galera opnieuw begon, Started
	// de nodes waarop het MariaDB opnieuw startte. Leeg als de database
	// vanzelf terugkwam.
	Began   string   `json:"began,omitempty"`
	Started []string `json:"started,omitempty"`
	// RecoverError zegt waarom opnieuw beginnen niet lukte, Reason waarom
	// de database aan het eind van de meting nog niet gezond was.
	RecoverError string `json:"recover_error,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// RestartNode is een node die herstartte. Error: de herstart lukte niet.
type RestartNode struct {
	Hostname string `json:"hostname"`
	Arbiter  bool   `json:"arbiter,omitempty"`
	BackMS   *int64 `json:"back_ms,omitempty"`
	Error    string `json:"error,omitempty"`
}

// restartChecks zijn de voorwaarden om alle nodes van database d te
// herstarten: elke node heeft een verse heartbeat en een agent, en bij
// Galera staat vanzelf opnieuw starten aan.
func (s *Service) restartChecks(plan *Plan, d Database, nodes []store.ListFailoverNodesRow, now time.Time, add func(string, bool, string)) {
	rp := &RestartPlan{ComponentID: d.ComponentID, Engine: d.Engine, Title: d.Title}
	var why []string
	check := func(dn DatabaseNode, arbiter bool) {
		rp.Nodes = append(rp.Nodes, RestartTarget{ID: dn.ID, Hostname: dn.Hostname, Arbiter: arbiter})
		i := slices.IndexFunc(nodes, func(n store.ListFailoverNodesRow) bool { return n.ID == dn.ID })
		switch {
		case i < 0:
			why = append(why, dn.Hostname+" hoort niet bij dit cluster")
		case !nodes[i].HasAgent || nodes[i].HeartbeatAt == nil || now.Sub(*nodes[i].HeartbeatAt) > s.Fresh:
			why = append(why, dn.Hostname+" heeft geen verse heartbeat")
		case nodes[i].AgentProtocol < protocol.CommandsSince:
			why = append(why, "de agent op "+dn.Hostname+" kent geen commando's")
		}
	}
	for _, n := range d.Nodes {
		check(n, false)
	}
	for _, n := range d.Arbiters {
		check(n, true)
	}
	if len(why) > 0 {
		add("Herstarten", false, strings.Join(why, "; "))
	} else {
		add("Herstarten", true, fmt.Sprintf("%s herstarten tegelijk; wat erop draait, is even weg", rp.hosts()))
		plan.Restart = rp
	}
	if d.Engine == protocol.DBMariaDB {
		if d.AutoRecover {
			add("Vanzelf opnieuw starten", true, "na de herstart begint ClusterForge "+d.Title+" opnieuw als Galera dat niet zelf doet, zoals de bewaking")
		} else {
			add("Vanzelf opnieuw starten", false, "vanzelf opnieuw starten staat uit bij "+d.Title+"; zonder komt Galera na een herstart van alle nodes "+
				"niet terug. Zet het aan bij de database")
		}
	}
}

// restartWindow is hoe lang de meting hoogstens duurt.
func (s *Service) restartWindow(def Definition) time.Duration { return def.expect() + s.RestartMargin }

// booted: de node stuurde na since een heartbeat van na een herstart die
// na since begon.
func (s *Service) booted(ctx context.Context, nodeID uuid.UUID, since time.Time) bool {
	rt, err := s.q.GetNodeRuntime(ctx, nodeID)
	if err != nil || rt.HeartbeatAt == nil || !rt.HeartbeatAt.After(since) {
		return false
	}
	boot := rt.HeartbeatAt.Add(-time.Duration(rt.UptimeSeconds) * time.Second)
	// Een seconde speling: de uptime is afgerond.
	return boot.After(since.Add(-time.Second))
}

// confirmAttempts onthoudt de bevestigde volgnummers en geeft de volgende
// From.
func confirmAttempts(rs *restartState, res *protocol.DBProbeResult, from int) int {
	for _, a := range res.Attempts {
		if a.OK {
			rs.Confirmed = append(rs.Confirmed, a.Seq)
		}
	}
	return from + len(res.Attempts)
}

// measureRestart herstart alle nodes en meet tot de database weer gezond
// is en het VIP weer antwoordt.
func (r *testRun) measureRestart(ctx context.Context, st *jobs.Step, ms *measureState) error {
	s, rp, w := r.s, r.plan.Restart, r.plan.Writes
	if rp == nil {
		return errors.New("het plan zegt niet welke nodes herstarten; start de test opnieuw")
	}
	rs := &restartState{}
	if w != nil && !w.StartedAt.IsZero() {
		res, err := s.probeRequest(ctx, w, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: r.run.ID.String()})
		if err != nil {
			return fmt.Errorf("de schrijftest op %s lezen lukte niet: %w", w.Prober.Hostname, err)
		}
		rs.From = confirmAttempts(rs, res, 0)
	}
	ms.InjectedAt, ms.Restart = time.Now(), rs
	if err := st.SetState(ctx, ms); err != nil {
		ms.InjectedAt = time.Time{}
		return err
	}
	st.Logf("%s tegelijk herstarten", rp.hosts())
	_ = st.Flush(ctx)
	failed := r.sendReboots(ctx, rs)
	switch {
	case jobs.Interrupted(ctx):
		return context.Cause(ctx)
	case len(rs.Sent) == 0 && !jobs.Canceled(ctx):
		ms.InjectError = "de herstart lukte nergens: " + strings.Join(failed, "; ")
		_ = st.SetState(ctx, ms)
		return errors.New(ms.InjectError)
	case len(rs.Sent) > 0:
		r.event(ctx, "failover.fault_injected", map[string]any{"nodes": r.restartHosts()})
	}
	if len(failed) > 0 && len(rs.Sent) > 0 {
		ms.InjectError = "de herstart lukte niet op " + strings.Join(failed, "; ")
		st.Logf("%s", ms.InjectError)
	}
	_ = st.SetState(ctx, ms)
	m := r.restartMeasure(ctx, st, ms)
	if jobs.Interrupted(ctx) {
		return context.Cause(ctx)
	}
	ms.Measurement = &m
	return st.SetState(ctx, ms)
}

func (r *testRun) restartHosts() []string {
	out := []string{}
	if r.plan.Restart != nil {
		for _, n := range r.plan.Restart.Nodes {
			out = append(out, n.Hostname)
		}
	}
	return out
}

// sendReboots stuurt alle nodes tegelijk de herstart, en geeft de nodes
// waar dat niet lukte, met de reden.
func (r *testRun) sendReboots(ctx context.Context, rs *restartState) []string {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var failed []string
	for _, n := range r.plan.Restart.Nodes {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			res, err := r.s.bus.Command(cctx, n.ID, protocol.Command{
				ID: r.run.ID.String() + "-reboot-" + n.ID.String(), Action: protocol.CmdReboot, Reason: r.reason(),
				DelaySeconds: r.s.RebootDelay, Deadline: time.Now().Add(time.Minute),
			})
			if err == nil && !res.OK {
				err = errors.New(res.Error)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, n.Hostname+": "+err.Error())
				return
			}
			rs.Sent = append(rs.Sent, n.ID)
		})
	}
	wg.Wait()
	slices.Sort(failed)
	return failed
}

// restartMeasure kijkt tot alle nodes terug zijn, de database gezond is en
// het VIP weer antwoordt, of tot het meetvenster om is. Ondertussen leest
// het de schrijftest zolang de node die schrijft dat kan.
func (r *testRun) restartMeasure(ctx context.Context, st *jobs.Step, ms *measureState) Measurement {
	s, def, rp, rs, w := r.s, r.def, r.plan.Restart, ms.Restart, r.plan.Writes
	injectedAt := ms.InjectedAt
	window := s.restartWindow(def)
	deadline := injectedAt.Add(window)
	rm := &RestartMeasurement{Title: rp.Title, Engine: rp.Engine, Nodes: []RestartNode{}}
	for _, n := range rp.Nodes {
		rn := RestartNode{Hostname: n.Hostname, Arbiter: n.Arbiter}
		if !slices.Contains(rs.Sent, n.ID) {
			rn.Error = "herstartte niet"
		}
		rm.Nodes = append(rm.Nodes, rn)
	}
	m := Measurement{WindowMS: window.Milliseconds(), Restart: rm}
	at := func(t time.Time) int64 { return t.Sub(injectedAt).Milliseconds() }
	pl := NewProbeLog(injectedAt, def.VIP)
	pctx, pstop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { s.probeLoop(pctx, def, pl) })
	st.Logf("meten: %s elke %d ms opvragen (%s) en wachten tot %s weer gezond is, hoogstens %s",
		def.VIP, s.ProbeInterval.Milliseconds(), def.Probe, rp.Title, seconds(window))
	_ = st.Flush(ctx)

	var timeline []TimelineEvent
	reading := w != nil && !w.StartedAt.IsZero()
	var healthyAt, lastRecover time.Time
	tick := time.NewTicker(s.WritePoll)
	defer tick.Stop()
	for time.Now().Before(deadline) && ctx.Err() == nil {
		// De node die schrijft: lezen tot hij herstart.
		if reading {
			res, err := s.probeRequest(ctx, w, protocol.DBProbeRequest{Action: protocol.DBProbeRead, Run: r.run.ID.String(), From: rs.From})
			switch {
			case ctx.Err() != nil:
			case err != nil:
				reading = false
				st.Logf("%s herstart; de database bevestigde daarvoor %d rijen", w.Prober.Hostname, len(rs.Confirmed))
			case len(res.Attempts) > 0:
				rs.From = confirmAttempts(rs, res, rs.From)
				_ = st.SetState(ctx, ms)
			}
		}
		all := true
		for i, n := range rp.Nodes {
			rn := &rm.Nodes[i]
			if rn.BackMS != nil || rn.Error != "" {
				continue
			}
			if !s.booted(ctx, n.ID, injectedAt) {
				all = false
				continue
			}
			back := at(time.Now())
			rn.BackMS = &back
			timeline = append(timeline, TimelineEvent{TMS: back, Kind: "node_back", Text: n.Hostname + " is terug na de herstart"})
			st.Logf("%s is terug na %s", n.Hostname, seconds(time.Duration(back)*time.Millisecond))
			_ = st.Flush(ctx)
			if w != nil && n.ID == w.Prober.ID && reading {
				// Een nieuwe agent kent de schrijftest niet meer.
				reading = false
				st.Logf("de database bevestigde %d rijen voor %s herstartte", len(rs.Confirmed), n.Hostname)
			}
		}
		if all && rm.BackMS == nil {
			back := at(time.Now())
			rm.BackMS = &back
		}
		if all && healthyAt.IsZero() {
			// Wat de bewaking zou doen, hoogstens eens per RestartPoll.
			if rm.RecoverError == "" && time.Since(lastRecover) >= s.RestartPoll {
				lastRecover = time.Now()
				rec, err := s.DB.RestartRecover(ctx, st, r.run.ID.String()+"-herstel", r.cluster, rp.ComponentID, injectedAt, false)
				switch {
				case ctx.Err() != nil:
				case err != nil:
					rm.RecoverError = short(err.Error())
					st.Logf("opnieuw beginnen lukte niet: %v", err)
				case rec.Did():
					rm.Started = append(rm.Started, rec.Started...)
					text := "ClusterForge startte MariaDB opnieuw op " + joinNames(rec.Started)
					if rec.Began != "" {
						rm.Began = rec.Began
						text = "ClusterForge begon " + rp.Title + " opnieuw op " + rec.Began
					}
					timeline = append(timeline, TimelineEvent{TMS: at(lastRecover), Kind: "db_recover", Text: text})
				}
			}
			ok, reason, err := s.DB.TestHealth(ctx, r.cluster, rp.ComponentID)
			switch {
			case ctx.Err() != nil:
			case err == nil && ok:
				healthyAt = time.Now()
				h := at(healthyAt)
				rm.HealthyMS, rm.Reason = &h, ""
				timeline = append(timeline, TimelineEvent{TMS: h, Kind: "db_ready", Text: rp.Title + " is weer gezond"})
				st.Logf("%s is weer gezond na %s", rp.Title, seconds(healthyAt.Sub(injectedAt)))
				_ = st.Flush(ctx)
			default:
				if err != nil {
					reason = rp.Title + " is niet te lezen: " + err.Error()
				}
				if reason != rm.Reason {
					rm.Reason = reason
					st.Logf("%s", reason)
					_ = st.Flush(ctx)
				}
			}
		}
		if !healthyAt.IsZero() && pl.Recovered() {
			break
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	pstop()
	wg.Wait()
	end := time.Now()
	if ctx.Err() != nil {
		m.Canceled = jobs.Canceled(ctx)
	}
	m.EndMS = at(end)
	m.Recovered = pl.Recovered()
	m.DowntimeMS = pl.Downtime(end).Milliseconds()
	var probeEvents []TimelineEvent
	m.Probe, probeEvents = pl.Finish(end)
	m.Timeline = sortTimeline(append(probeEvents, timeline...))
	switch {
	case m.Canceled:
		st.Logf("afgebroken; nu volgt het herstel")
	case healthyAt.IsZero():
		st.Logf("%s was na %s nog niet gezond", rp.Title, seconds(window))
	case !m.Recovered:
		st.Logf("%s antwoordde na %s nog niet", def.VIP, seconds(window))
	default:
		st.Logf("%s antwoordde weer na %s", def.VIP, seconds(time.Duration(m.DowntimeMS)*time.Millisecond))
	}
	return m
}

var errRestartTimeout = errors.New("de nodes en de database kwamen niet op tijd terug")

// restoreRestart wacht tot alle nodes terug zijn en de database gezond is,
// en doet daarvoor wat de bewaking zou doen, zonder te wachten.
func (r *testRun) restoreRestart(ctx context.Context, st *jobs.Step) error {
	s, rp := r.s, r.plan.Restart
	if rp == nil {
		return errors.New("het plan zegt niet welke nodes herstarten")
	}
	var sent []uuid.UUID
	if r.restart != nil {
		sent = r.restart.Sent
	}
	ctx, cancel := context.WithTimeoutCause(ctx, s.BootTimeout, errRestartTimeout)
	defer cancel()
	st.Logf("wachten tot %s terug zijn", rp.hosts())
	_ = st.Flush(ctx)
	// Een node die de herstart niet aannam, is terug zodra hij na de
	// vertraging van de herstart nog een heartbeat stuurt.
	after := r.injectedAt.Add(time.Duration(s.RebootDelay)*time.Second + 5*time.Second)
	for _, n := range rp.Nodes {
		err := health.Poll(ctx, s.Gate.Poll, func(ctx context.Context) (bool, error) {
			if slices.Contains(sent, n.ID) {
				return s.booted(ctx, n.ID, r.injectedAt), nil
			}
			rt, err := s.q.GetNodeRuntime(ctx, n.ID)
			return err == nil && rt.HeartbeatAt != nil && rt.HeartbeatAt.After(after), nil
		})
		if errors.Is(context.Cause(ctx), errRestartTimeout) {
			return fmt.Errorf("%s is niet terug binnen %s na de herstart", n.Hostname, seconds(s.BootTimeout))
		}
		if err != nil {
			return err
		}
	}
	if err := s.recoverDatabase(ctx, st, r.cluster, rp.ComponentID, rp.Title, r.injectedAt, r.run.ID.String()); err != nil {
		return err
	}
	r.clearedAt = time.Now()
	r.event(ctx, "failover.fault_cleared", map[string]any{"nodes": r.restartHosts()})
	return st.SetState(ctx, restoreState{ClearedAt: r.clearedAt})
}

// recoverDatabase doet wat de bewaking zou doen, zonder te wachten, tot de
// database gezond is. Na drie mislukte pogingen stopt het.
func (s *Service) recoverDatabase(ctx context.Context, st *jobs.Step, clusterID, componentID uuid.UUID, title string, since time.Time, id string) error {
	ctx, cancel := context.WithTimeoutCause(ctx, s.DBReturnTimeout, errRestartTimeout)
	defer cancel()
	last := ""
	for attempt := 1; ; {
		ok, reason, err := s.DB.TestHealth(ctx, clusterID, componentID)
		if err == nil && ok {
			st.Logf("%s is gezond", title)
			return nil
		}
		if err != nil {
			reason = title + " is niet te lezen: " + err.Error()
		}
		if reason != last {
			last = reason
			st.Logf("%s", reason)
			_ = st.Flush(ctx)
		}
		rec, err := s.DB.RestartRecover(ctx, st, fmt.Sprintf("%s-herstel%d", id, attempt), clusterID, componentID, since, true)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			st.Logf("opnieuw beginnen lukte niet: %v", err)
			if attempt++; attempt > 3 {
				return fmt.Errorf("%s opnieuw beginnen lukte niet: %w", title, err)
			}
		case rec.Began != "":
			st.Logf("ClusterForge begon %s opnieuw op %s", title, rec.Began)
		}
		select {
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), errRestartTimeout) {
				return fmt.Errorf("%s is niet gezond binnen %s: %s", title, seconds(s.DBReturnTimeout), last)
			}
			return context.Cause(ctx)
		case <-time.After(2 * s.WritePoll):
		}
	}
}

// restoreRestartRun is Opnieuw herstellen van alles herstarten: wachten tot
// de nodes van de database er zijn, en de database gezond maken.
func (s *Service) restoreRestartRun(ctx context.Context, st *jobs.Step, clusterID uuid.UUID, def Definition, id string) error {
	dbs, err := s.databases(ctx, clusterID)
	if err != nil {
		return err
	}
	d, ok := dbOfVIP(dbs, def.VIP)
	if !ok {
		return fmt.Errorf("%s is niet meer het VIP van een database", def.VIP)
	}
	since := time.Now()
	ctx, cancel := context.WithTimeoutCause(ctx, s.BootTimeout, errRestartTimeout)
	defer cancel()
	for _, n := range append(slices.Clone(d.Nodes), d.Arbiters...) {
		st.Logf("wachten op een verse heartbeat van %s", n.Hostname)
		_ = st.Flush(ctx)
		err := health.Poll(ctx, s.Gate.Poll, func(ctx context.Context) (bool, error) {
			rt, err := s.q.GetNodeRuntime(ctx, n.ID)
			return err == nil && rt.HeartbeatAt != nil && time.Since(*rt.HeartbeatAt) <= s.Fresh, nil
		})
		if errors.Is(context.Cause(ctx), errRestartTimeout) {
			return fmt.Errorf("%s stuurt geen heartbeat; start de node", n.Hostname)
		}
		if err != nil {
			return err
		}
	}
	return s.recoverDatabase(ctx, st, clusterID, d.ComponentID, d.Title, since, id)
}

// verifyWrites kijkt na de test na of de rijen die de database bevestigde
// voor de node die schreef herstartte, er nog zijn. De agent kent de
// schrijftest niet meer; hij logt in met een nieuw wachtwoord.
func (r *testRun) verifyWrites(ctx context.Context, st *jobs.Step, o outcome) *WriteMeasurements {
	w := r.plan.Writes
	out := &WriteMeasurements{Node: w.Prober.Hostname, Title: w.Title, VIP: w.VIP, Port: w.Port, Segments: []Segment{}, Confirmed: len(o.confirmed)}
	if o.restoreErr != nil {
		out.CheckError = "de database is niet hersteld, dus de rijen zijn niet nagekeken"
		r.stopWrites(w)
		return out
	}
	cctx, cancel := context.WithTimeout(ctx, r.s.WriteFinishTimeout)
	defer cancel()
	password := rand.Text()
	st.Logf("%s kijkt na of de %d bevestigde rijen er nog zijn", w.Prober.Hostname, len(o.confirmed))
	_ = st.Flush(ctx)
	if err := r.s.DB.PrepareWrites(cctx, r.cluster, w.ComponentID, password); err != nil {
		out.CheckError = "de schrijftest klaarzetten om na te kijken lukte niet: " + err.Error()
		return out
	}
	res, err := r.s.probeRequest(cctx, w, protocol.DBProbeRequest{
		Action: protocol.DBProbeVerify, Run: r.run.ID.String(), Host: w.VIP, Port: w.Port, Password: password, Seqs: o.confirmed,
	})
	if err != nil {
		out.CheckError = "de rijen nakijken op " + w.Prober.Hostname + " lukte niet: " + err.Error()
		return out
	}
	out.Confirmed, out.Extra = res.Confirmed, res.Extra
	if res.Checked {
		lost := res.Missing
		out.Lost, out.LostSeqs = &lost, res.MissingSeqs
		st.Logf("%d van de %d bevestigde rijen ontbreken", lost, res.Confirmed)
	} else {
		out.CheckError = or(res.CheckError, "de agent telde de rijen niet")
		st.Logf("nakijken lukte niet: %s", out.CheckError)
	}
	return out
}

// restartVerdict is het resultaat van alles herstarten, na de gevallen die
// voor elke test gelden (verdict).
func restartVerdict(def Definition, o outcome, back string) (string, string, *bool) {
	yes := true
	m, w := o.m, o.writes
	rm := m.Restart
	if rm == nil {
		return "error", "ERROR: er is geen meting van de herstart; " + back, &yes
	}
	window := time.Duration(m.WindowMS) * time.Millisecond
	var missing, hosts []string
	for _, n := range rm.Nodes {
		hosts = append(hosts, n.Hostname)
		if n.BackMS == nil && n.Error == "" {
			missing = append(missing, n.Hostname)
		}
	}
	how := ""
	switch {
	case rm.Began != "":
		how = " (ClusterForge begon het cluster opnieuw op " + rm.Began + ")"
	case len(rm.Started) > 0:
		how = " (ClusterForge startte MariaDB opnieuw op " + joinNames(rm.Started) + ")"
	}
	var healthy time.Duration
	if rm.HealthyMS != nil {
		healthy = time.Duration(*rm.HealthyMS) * time.Millisecond
	}
	switch {
	case len(missing) > 0:
		was := "was"
		if len(missing) > 1 {
			was = "waren"
		}
		return "fail", fmt.Sprintf("FAIL: %s %s %s na het herstarten nog niet terug; %s", joinNames(missing), was, seconds(window), back), &yes
	case rm.HealthyMS == nil:
		why := rm.Reason
		if rm.RecoverError != "" {
			why = "opnieuw beginnen lukte niet: " + rm.RecoverError
		}
		return "fail", fmt.Sprintf("FAIL: %s was %s na het herstarten nog niet gezond (%s); %s", rm.Title, seconds(window), or(why, "onbekend waarom"), back), &yes
	case !m.Recovered:
		return "fail", fmt.Sprintf("FAIL: %s antwoordde %s na het herstarten nog niet; %s", def.VIP, seconds(window), back), &yes
	case healthy > def.expect():
		return "fail", fmt.Sprintf("FAIL: %s was pas na %s weer gezond%s, meer dan de verwachte %s; %s",
			rm.Title, seconds(healthy), how, seconds(def.expect()), back), &yes
	case w != nil && w.CheckError != "":
		return "error", fmt.Sprintf("ERROR: de schrijftest kon niet nagaan of er rijen verloren gingen: %s; %s", w.CheckError, back), &yes
	}
	vip := def.VIP + " bleef antwoorden"
	if m.DowntimeMS > 0 {
		vip = def.VIP + " antwoordde weer na " + seconds(time.Duration(m.DowntimeMS)*time.Millisecond)
	}
	rows := ""
	switch {
	case w == nil:
	case w.Confirmed == 1:
		rows = "; de bevestigde rij was er nog"
	case w.Confirmed > 1:
		rows = fmt.Sprintf("; alle %d bevestigde rijen waren er nog", w.Confirmed)
	}
	return "pass", fmt.Sprintf("PASS: na het herstarten van %s was %s na %s weer gezond%s, %s%s; %s",
		joinNames(hosts), rm.Title, seconds(healthy), how, vip, rows, back), &yes
}

// joinNames schrijft "nc1", "nc1 en nc2" of "nc1, nc2 en nc3".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
}
