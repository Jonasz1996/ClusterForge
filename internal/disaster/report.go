package disaster

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Hoe de meting van een cluster zich verhoudt tot de voorspelling.
const (
	MatchAsPredicted   = "as_predicted"
	MatchUnexpected    = "unexpected"
	MatchMilder        = "milder"
	MatchStayedHealthy = "stayed_healthy"
	MatchNotRecovered  = "not_recovered"
	MatchUnknown       = "unknown"
)

// ClusterResult is één regel van de tabel voorspeld tegen gemeten.
type ClusterResult struct {
	ClusterPlan
	Measured Prediction `json:"measured"`
	// LongestMS is de langste onderbreking op een VIP tijdens de storing.
	LongestMS int64 `json:"longest_ms"`
	// RecoveredMS is hoe lang na het terugzetten het cluster weer gezond
	// was; nil als het niet herstelde.
	RecoveredMS *int64 `json:"recovered_ms"`
	Match       string `json:"match"`
	// Slow: het herstel duurde langer dan verwacht.
	Slow bool `json:"slow,omitempty"`
}

// VIPResult is de probe op één VIP over de hele run.
type VIPResult struct {
	ClusterID uuid.UUID          `json:"cluster_id"`
	Address   string             `json:"address"`
	Probe     failover.Probe     `json:"probe"`
	Segments  []failover.Segment `json:"segments"`
}

// Measurements staan in test_runs.measurements.
type Measurements struct {
	FaultMS          int64 `json:"fault_ms"`
	ExpectRecoveryMS int64 `json:"expect_recovery_ms"`
	ShortOutageMS    int64 `json:"short_outage_ms"`
	// ClearMS is wanneer alles weer gestart was, in ms na de storing.
	ClearMS *int64 `json:"clear_ms,omitempty"`
	// RecoveryMS loopt van het terugzetten tot elk cluster weer gezond was,
	// TotalMS van de storing tot dan.
	RecoveryMS *int64         `json:"recovery_ms,omitempty"`
	TotalMS    *int64         `json:"total_ms,omitempty"`
	EndMS      int64          `json:"end_ms"`
	Targets    []TargetResult `json:"targets"`
	// Steps is bij een reeks wat elke stap deed.
	Steps    []StepResult    `json:"steps,omitempty"`
	Clusters []ClusterResult `json:"clusters"`
	VIPs     []VIPResult     `json:"vips"`
	Injected bool            `json:"injected"`
	Canceled bool            `json:"canceled,omitempty"`
	Notes    []string        `json:"notes,omitempty"`
	Skipped  []string        `json:"skipped,omitempty"`
}

// outcome is alles waaruit het rapport volgt.
type outcome struct {
	skipped     string
	precheckErr error
	injected    bool
	injectErr   string
	interrupted bool
	canceled    bool
	m           *Measurement
	restoreErr  error
	rec         *recoveryState
}

// compare zegt hoe gemeten zich verhoudt tot voorspeld. Een probe ziet
// alleen of een VIP antwoordt: bij een cluster dat alleen afhangt, is
// verminderd dus niet van gezond te onderscheiden.
func compare(role, predicted, measured string) string {
	p, m := rank(predicted), rank(measured)
	switch {
	case p < 0 || m < 0:
		return MatchUnknown
	case m > p:
		return MatchUnexpected
	case m == p:
		return MatchAsPredicted
	case role == RoleDependent && p == 1 && m == 0:
		return MatchAsPredicted
	case m == 0:
		return MatchStayedHealthy
	}
	return MatchMilder
}

// measured leidt per cluster de gemeten status af: down als een VIP langer
// dan short onbereikbaar was, anders de slechtste status van de evaluator.
func measured(def Definition, m *Measurement, short time.Duration) map[uuid.UUID]ClusterResult {
	out := map[uuid.UUID]ClusterResult{}
	worst := map[uuid.UUID]StatusMeasure{}
	for _, sm := range m.Status {
		worst[sm.ClusterID] = sm
	}
	for _, c := range def.Clusters {
		cr := ClusterResult{ClusterPlan: c, Measured: Prediction{Status: string(status.Healthy), Reason: "bleef bereikbaar"}}
		var down, blips []string
		for _, v := range m.VIPs {
			if v.ClusterID != c.ID {
				continue
			}
			cr.LongestMS = max(cr.LongestMS, v.LongestMS)
			longest := time.Duration(v.LongestMS) * time.Millisecond
			switch {
			case v.LongestMS == 0:
			case longest >= short:
				down = append(down, fmt.Sprintf("%s %s onbereikbaar: %s", v.Address, failover.Seconds(longest), v.Error))
			default:
				blips = append(blips, fmt.Sprintf("%s %s onbereikbaar", v.Address, failover.Seconds(longest)))
			}
		}
		sm, seen := worst[c.ID]
		switch {
		case len(down) > 0:
			cr.Measured = Prediction{Status: string(status.Down), Reason: strings.Join(down, "; ")}
		case seen && rank(sm.Worst) > 0:
			cr.Measured = Prediction{Status: sm.Worst, Reason: sm.Reason}
		case len(c.VIPs) == 0:
			cr.Measured.Reason = "geen VIP om op te vragen; de status bleef gezond"
		}
		if len(blips) > 0 && len(down) == 0 {
			note := "korte onderbreking: " + strings.Join(blips, "; ")
			if cr.Measured.Reason == "bleef bereikbaar" {
				cr.Measured.Reason = note
			} else {
				cr.Measured.Reason += "; " + note
			}
		}
		if c.Role == RoleDependent && c.Predicted.Status == string(status.Degraded) && cr.Measured.Status == string(status.Healthy) {
			cr.Measured.Reason += "; verminderd is met een probe niet te zien"
		}
		cr.Match = compare(c.Role, c.Predicted.Status, cr.Measured.Status)
		out[c.ID] = cr
	}
	return out
}

// verdict geeft het resultaat, de samenvatting, of alles hersteld is en de
// tabel voorspeld tegen gemeten.
func verdict(def Definition, o outcome, short time.Duration) (result, summary string, restored *bool, rows []ClusterResult) {
	yes, no := true, false
	switch {
	case o.skipped != "":
		return "skipped", "Overgeslagen: " + o.skipped, nil, nil
	case !o.injected && o.canceled:
		return "canceled", "Afgebroken voor de storing; er is niets veranderd", nil, nil
	case !o.injected:
		msg := "ERROR: de rampoefening kon niet starten"
		if o.precheckErr != nil {
			msg += ": " + o.precheckErr.Error()
		}
		return "error", msg + "; er is niets veranderd", nil, nil
	}
	if o.m != nil && !o.m.Canceled {
		byID := measured(def, o.m, short)
		for _, c := range def.Clusters {
			rows = append(rows, byID[c.ID])
		}
	}
	if o.restoreErr != nil {
		return "error", fmt.Sprintf("ERROR: herstel mislukt: %v. %s; kies Opnieuw herstellen", o.restoreErr, StillDown(def)), &no, rows
	}
	back := "alles hersteld"
	var recovery *int64
	if o.rec != nil {
		for i := range rows {
			for _, cr := range o.rec.Clusters {
				if cr.ClusterID != rows[i].ID {
					continue
				}
				rows[i].RecoveredMS = cr.RecoveredMS
				switch {
				case cr.RecoveredMS == nil:
					rows[i].Match = MatchNotRecovered
				case *cr.RecoveredMS > def.Expect.recovery().Milliseconds():
					rows[i].Slow = true
				}
			}
		}
		recovery = recoveryOf(o.rec)
		if recovery != nil {
			back = "alles hersteld na " + failover.Seconds(time.Duration(*recovery)*time.Millisecond)
		}
	}
	switch {
	case o.interrupted:
		return "error", "ERROR: meting onderbroken door een herstart van ClusterForge; " + back, &yes, rows
	case o.injectErr != "" && o.canceled:
		return "canceled", "Afgebroken tijdens de storing; " + back, &yes, rows
	case o.injectErr != "":
		return "error", "ERROR: storing niet gelukt: " + o.injectErr + "; " + back, &yes, rows
	case o.m == nil:
		return "error", "ERROR: er is geen meting; " + back, &yes, rows
	case o.m.Canceled:
		return "canceled", "Afgebroken tijdens de meting; " + back, &yes, rows
	}

	var fails, warns []string
	expect := def.Expect.recovery()
	for _, r := range rows {
		switch r.Match {
		case MatchNotRecovered:
			fails = append(fails, r.Name+" herstelde niet")
		case MatchUnexpected:
			fails = append(fails, fmt.Sprintf("%s ging onverwacht %s (%s)", r.Name, StatusLabel(r.Measured.Status), r.Measured.Reason))
		case MatchStayedHealthy:
			warns = append(warns, fmt.Sprintf("%s bleef gezond, maar zou %s worden", r.Name, StatusLabel(r.Predicted.Status)))
		case MatchMilder:
			warns = append(warns, fmt.Sprintf("%s werd %s in plaats van %s", r.Name, StatusLabel(r.Measured.Status), StatusLabel(r.Predicted.Status)))
		}
		if r.Slow && r.RecoveredMS != nil {
			warns = append(warns, fmt.Sprintf("%s herstelde pas na %s, verwacht binnen %s", r.Name,
				failover.Seconds(time.Duration(*r.RecoveredMS)*time.Millisecond), failover.Seconds(expect)))
		}
	}
	switch {
	case len(fails) > 0:
		if slices.ContainsFunc(rows, func(r ClusterResult) bool { return r.Match == MatchNotRecovered }) {
			back = "alles weer gestart, maar niet alles herstelde binnen " + failover.Seconds(def.Expect.recovery()+30*time.Second)
		}
		return "fail", "FAIL: " + strings.Join(fails, "; ") + "; " + back, &yes, rows
	case len(warns) > 0:
		return "warning", "WARNING: " + strings.Join(warns, "; ") + "; " + back, &yes, rows
	}
	n := len(rows)
	what := fmt.Sprintf("%d clusters gedroegen zich zoals voorspeld", n)
	if n == 1 {
		what = "het cluster gedroeg zich zoals voorspeld"
	}
	return "pass", "PASS: " + what + ", " + back, &yes, rows
}

// recoveryOf is het herstel van het traagste cluster, of nil als er een niet
// herstelde.
func recoveryOf(rec *recoveryState) *int64 {
	var worst int64
	for _, cr := range rec.Clusters {
		if cr.RecoveredMS == nil {
			return nil
		}
		worst = max(worst, *cr.RecoveredMS)
	}
	return &worst
}

// report bouwt de tijdlijn en de metingen.
func (r *disasterRun) report(o outcome, rows []ClusterResult) ([]failover.TimelineEvent, Measurements) {
	def := r.def
	meas := Measurements{
		FaultMS: def.Expect.fault().Milliseconds(), ExpectRecoveryMS: def.Expect.recovery().Milliseconds(),
		ShortOutageMS: r.s.ShortOutage.Milliseconds(), Targets: []TargetResult{}, Clusters: rows, VIPs: []VIPResult{},
		Injected: o.injected, Notes: def.Notes, Skipped: def.Skipped,
	}
	if meas.Clusters == nil {
		meas.Clusters = []ClusterResult{}
	}
	timeline := []failover.TimelineEvent{}
	// Bij een reeks staat elke stap al in de tijdlijn van de meting.
	if o.injected && o.injectErr == "" && def.Kind != Sequence {
		timeline = append(timeline, failover.TimelineEvent{TMS: 0, Kind: "fault", Text: faultText(def)})
	}
	vips := map[string]*VIPResult{}
	var order []string
	addSegs := func(vs []VIPMeasure) {
		for _, v := range vs {
			k := v.ClusterID.String() + "/" + v.Address
			if vips[k] == nil {
				vips[k] = &VIPResult{ClusterID: v.ClusterID, Address: v.Address, Probe: v.Probe, Segments: []failover.Segment{}}
				order = append(order, k)
			}
			vips[k].Segments = append(vips[k].Segments, v.Segments...)
		}
	}
	if m := o.m; m != nil {
		timeline = append(timeline, m.Timeline...)
		meas.EndMS = m.EndMS
		meas.Canceled = m.Canceled
		addSegs(m.VIPs)
	}
	if !r.clearedAt.IsZero() {
		at := r.clearedAt.Sub(r.injectedAt).Milliseconds()
		meas.ClearMS = &at
		timeline = append(timeline, failover.TimelineEvent{TMS: at, Kind: "clear", Text: clearText(def)})
		meas.EndMS = max(meas.EndMS, at)
	}
	if rec := o.rec; rec != nil {
		timeline = append(timeline, rec.Timeline...)
		addSegs(rec.VIPs)
		meas.EndMS = max(meas.EndMS, rec.EndMS)
		if d := recoveryOf(rec); d != nil && meas.ClearMS != nil {
			total := *meas.ClearMS + *d
			meas.RecoveryMS, meas.TotalMS = d, &total
		}
	}
	for _, k := range order {
		meas.VIPs = append(meas.VIPs, *vips[k])
	}
	return sortTimeline(timeline), meas
}

func sortTimeline(t []failover.TimelineEvent) []failover.TimelineEvent {
	if t == nil {
		return []failover.TimelineEvent{}
	}
	slices.SortStableFunc(t, func(a, b failover.TimelineEvent) int { return cmp.Compare(a.TMS, b.TMS) })
	return t
}

// finish schrijft het rapport en geeft de fout voor de taak: geannuleerd
// blijft geannuleerd, een error maakt de taak mislukt, PASS, WARNING en FAIL
// zijn een geslaagde taak.
func (r *disasterRun) finish(ctx context.Context, o outcome) error {
	dctx, cancel := jobs.Detach(ctx)
	defer cancel()
	result, summary, restored, rows := verdict(r.def, o, r.s.ShortOutage)
	timeline, meas := r.report(o, rows)
	err := r.j.Step(dctx, stepReport, func(ctx context.Context, st *jobs.Step) error {
		st.Logf("%s", summary)
		for _, row := range rows {
			st.Logf("%s: voorspeld %s, gemeten %s", row.Name, StatusLabel(row.Predicted.Status), StatusLabel(row.Measured.Status))
		}
		// Wat de storing per doel deed, staat in de state van de meting.
		if steps, err := r.s.q.ListJobSteps(ctx, r.j.ID); err == nil {
			for _, js := range steps {
				var ms measureState
				if js.Name == stepMeasure && json.Unmarshal(js.State, &ms) == nil && len(ms.Injected) > 0 {
					meas.Targets = ms.Injected
					meas.Steps = ms.Steps
				}
			}
		}
		tl, err := json.Marshal(timeline)
		if err != nil {
			return err
		}
		mb, err := json.Marshal(meas)
		if err != nil {
			return err
		}
		run, err := r.s.q.GetTestRun(ctx, r.run.ID)
		if err != nil {
			return err
		}
		_, err = r.s.q.FinishTestRun(ctx, store.FinishTestRunParams{
			ID: r.run.ID, Result: &result, Restored: restored, Summary: summary, Checks: run.TestRun.Checks, Timeline: tl, Measurements: mb,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		extra := map[string]any{"result": result, "summary": summary}
		if restored != nil {
			extra["restored"] = *restored
		}
		if meas.RecoveryMS != nil {
			extra["recovery_ms"] = *meas.RecoveryMS
		}
		r.event(ctx, "disaster.finished", extra)
		return nil
	})
	switch {
	case jobs.Interrupted(dctx) || err != nil:
		return err
	case result == "canceled":
		return jobs.ErrCanceled
	case result == "error":
		return errors.New(strings.TrimPrefix(summary, "ERROR: "))
	}
	return nil
}
