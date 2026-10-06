package rollout

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/health"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// nodeState is de state van een nodestap. AppliedAt en Steps staan vast
// voor het eerste commando de deur uitgaat. Een hervatte stap vergelijkt dan
// niet opnieuw, want het herstel was al begonnen en de vingerafdrukken
// kloppen daarna vanzelf niet meer, en past dezelfde stappen toe, zodat de
// agent elk commando aan zijn id herkent.
type nodeState struct {
	AppliedAt *time.Time `json:"applied_at,omitempty"`
	Steps     []string   `json:"steps,omitempty"`
}

// run voert cluster.apply uit. Stappen worden aan hun volgorde herkend en
// het plan ligt vast in de parameters, dus elke poging doorloopt dezelfde
// nodes in dezelfde volgorde.
func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p Plan
	if err := j.Decode(&p); err != nil {
		return err
	}
	if p.Mode != ModeRemediate && p.Mode != ModeChange {
		return fmt.Errorf("onbekende modus %q", p.Mode)
	}
	// Bovenaan elke poging, ook na een herstart van de server.
	if _, err := s.current(ctx, p); err != nil {
		return err
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		name, step := "Herstel op "+n.Hostname, s.remediate
		if p.Mode == ModeChange {
			name, step = "Toepassen op "+n.Hostname, s.change
		}
		if len(n.VIPs) > 0 {
			name += " (VIP-eigenaar)"
		}
		if err := j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
			return step(ctx, j, st, p, n)
		}); err != nil {
			return err
		}
	}
	return nil
}

// current leest de gewenste staat en controleert dat die nog is zoals bij
// de aanvraag.
func (s *Service) current(ctx context.Context, p Plan) (*deploy.Desired, error) {
	d, err := s.dep.Desired(ctx, p.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("de gewenste staat is niet te lezen: %w", err)
	}
	again := "vraag het herstel opnieuw aan vanuit de drift van nu"
	if p.Mode == ModeChange {
		again = "de taak past alleen de revisie toe die goedgekeurd is"
	}
	switch {
	case d.Revision != p.Revision:
		return nil, fmt.Errorf("de specificatie veranderde sinds de aanvraag (revisie %d, nu %d); %s", p.Revision, d.Revision, again)
	case d.Spec.Template.Version != p.Version:
		return nil, fmt.Errorf("de templateversie veranderde sinds de aanvraag (%s, nu %s); %s", p.Version, d.Spec.Template.Version, again)
	case len(d.Membership) > 0:
		return nil, errors.New("het lidmaatschap wijkt nu af van de specificatie: " + strings.Join(d.Membership, "; "))
	}
	return d, nil
}

// remediate herstelt één node: opnieuw kijken, toepassen en wachten tot de
// node en het cluster gezond zijn.
func (s *Service) remediate(ctx context.Context, j *jobs.Job, st *jobs.Step, p Plan, n *PlanNode) error {
	flush := func() { _ = st.Flush(ctx) }
	d, err := s.current(ctx, p)
	if err != nil {
		return err
	}
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{NodeID: &n.NodeID})
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[0].ClusterID == nil || *rows[0].ClusterID != p.ClusterID || !d.Has(n.NodeID) {
		return fmt.Errorf("%s hoort niet meer bij dit cluster", n.Hostname)
	}
	if err := nodeUsable(rows[0], p.Mode); err != nil {
		return err
	}
	var ns nodeState
	st.State(&ns)

	ids := ns.Steps
	if ns.AppliedAt == nil {
		ids = make([]string, 0, len(n.Steps))
		for _, ps := range n.Steps {
			ids = append(ids, ps.Step)
		}
		ignored, err := s.drift.IgnoredSteps(ctx, p.ClusterID, n.NodeID, ids)
		if err != nil {
			return err
		}
		for _, id := range ignored {
			st.Logf("%s wordt nu genegeerd en blijft zoals het is", id)
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return slices.Contains(ignored, id) })
		if len(ids) == 0 {
			st.Logf("niets meer te herstellen op %s", n.Hostname)
			return nil
		}
	}
	rendered, err := d.Render(n.NodeID)
	if err != nil {
		return fmt.Errorf("de stappen van %s zijn niet te renderen: %w", n.Hostname, err)
	}
	steps := Select(rendered, ids)

	if ns.AppliedAt == nil {
		st.Logf("%s opnieuw bekijken", n.Hostname)
		flush()
		findings, err := s.drift.InspectSteps(ctx, n.NodeID, steps)
		if err != nil {
			return fmt.Errorf("%s bekijken: %w; er is niets toegepast", n.Hostname, err)
		}
		if changed := Changed(n.Steps, ids, findings); len(changed) > 0 {
			titles := make([]string, 0, len(changed))
			for _, ps := range n.Steps {
				if slices.Contains(changed, ps.Step) {
					titles = append(titles, lowerFirst(ps.Title))
				}
			}
			return fmt.Errorf("op %s veranderde %s sinds je het herstel aanvroeg; er is niets toegepast. Controleer opnieuw en kies wat je wilt herstellen",
				n.Hostname, strings.Join(titles, " en "))
		}
		st.Logf("de afwijkingen zijn nog zoals bij de aanvraag")
	} else {
		st.Logf("hervat na een onderbreking; het herstel was al begonnen, dus %s opnieuw toepassen", n.Hostname)
	}

	if err := s.apply(ctx, j, st, p, n, &ns, ids, steps); err != nil {
		return err
	}
	return s.settle(ctx, st, p, n, d, rendered, "het herstel van "+n.Hostname, n.Hostname+" is hersteld")
}

// apply past de stappen toe op een node die een eventueel VIP kan
// afstaan. De stappen liggen vast voor het eerste commando de deur uitgaat.
func (s *Service) apply(ctx context.Context, j *jobs.Job, st *jobs.Step, p Plan, n *PlanNode, ns *nodeState, ids []string, steps []templates.Step) error {
	if err := s.takeover(ctx, p.ClusterID, n, st); err != nil {
		return err
	}
	if ns.AppliedAt == nil {
		now := time.Now()
		ns.AppliedAt, ns.Steps = &now, ids
		if err := st.SetState(ctx, *ns); err != nil {
			return err
		}
	}
	_, err := deploy.ApplySteps(ctx, s.bus, deploy.ApplyOptions{
		NodeID: n.NodeID, Hostname: n.Hostname, CommandID: j.ID.String() + "-" + n.NodeID.String(), Retry: s.Retry,
		Logf: st.Logf, Flush: func() { _ = st.Flush(ctx) },
	}, steps)
	return err
}

// settle wacht na het toepassen tot de node gezond is, elk VIP één houder
// heeft en de controles van de template slagen. after zegt waarna, zoals
// "het herstel van web-02".
func (s *Service) settle(ctx context.Context, st *jobs.Step, p Plan, n *PlanNode, d *deploy.Desired, rendered []templates.Step, after, done string) error {
	flush := func() { _ = st.Flush(ctx) }
	since := time.Now()
	progress := func(msg string) {
		st.Logf("%s", msg)
		flush()
	}
	services := activeServices(rendered)
	st.Logf("wachten tot %s gezond is (%s active)", n.Hostname, strings.Join(services, ", "))
	flush()
	rctx, cancel := context.WithTimeoutCause(ctx, s.ReadyTimeout, fmt.Errorf("%s gaf in %s geen twee gezonde heartbeats", n.Hostname, s.ReadyTimeout))
	err := s.Gate.NodeReady(rctx, n.NodeID, since, services, progress)
	cancel()
	if err != nil {
		return fmt.Errorf("%s komt niet gezond terug: %w; de taak stopt hier", n.Hostname, err)
	}
	vips, err := s.vips(ctx, p.ClusterID)
	if err != nil {
		return err
	}
	if len(vips) > 0 {
		sctx, cancel := context.WithTimeoutCause(ctx, s.SettleTimeout, fmt.Errorf("de VIP's hadden na %s nog geen vaste houder", s.SettleTimeout))
		_, err := s.Gate.Settled(sctx, p.ClusterID, vips, since, nil, progress)
		cancel()
		if err != nil {
			return fmt.Errorf("na %s: %w; de taak stopt hier", after, err)
		}
	}
	c, err := d.Context()
	if err != nil {
		return err
	}
	if err := s.dep.Check(ctx, p.ClusterID, d.Template, c, s.CheckTimeout, st.Logf, flush); err != nil {
		return fmt.Errorf("controle na %s: %w; de taak stopt hier", after, err)
	}
	st.Logf("%s", done)
	return nil
}

// change past op één node de stappen van een nieuwe revisie toe en wacht
// tot de node en het cluster gezond zijn. Met All zijn dat alle stappen,
// behalve wat nu genegeerd wordt.
func (s *Service) change(ctx context.Context, j *jobs.Job, st *jobs.Step, p Plan, n *PlanNode) error {
	d, err := s.current(ctx, p)
	if err != nil {
		return err
	}
	rows, err := s.q.ListDriftNodes(ctx, store.ListDriftNodesParams{NodeID: &n.NodeID})
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[0].ClusterID == nil || *rows[0].ClusterID != p.ClusterID || !d.Has(n.NodeID) {
		return fmt.Errorf("%s hoort niet meer bij dit cluster", n.Hostname)
	}
	if err := nodeUsable(rows[0], p.Mode); err != nil {
		return err
	}
	rendered, err := d.Render(n.NodeID)
	if err != nil {
		return fmt.Errorf("de stappen van %s zijn niet te renderen: %w", n.Hostname, err)
	}
	var ns nodeState
	st.State(&ns)
	ids := ns.Steps
	switch {
	case ns.AppliedAt != nil:
		st.Logf("hervat na een onderbreking; het toepassen was al begonnen, dus %s opnieuw toepassen", n.Hostname)
	case p.All:
		ids = nil
		for _, ts := range rendered {
			ids = append(ids, drift.StepIDs(ts)...)
		}
		ignored, err := s.drift.IgnoredSteps(ctx, p.ClusterID, n.NodeID, ids)
		if err != nil {
			return err
		}
		for _, id := range ignored {
			st.Logf("%s wordt genegeerd en blijft zoals het is", id)
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return slices.Contains(ignored, id) })
	default:
		ids = make([]string, 0, len(n.Steps))
		for _, ps := range n.Steps {
			ids = append(ids, ps.Step)
		}
	}
	steps := Select(rendered, ids)
	if len(steps) == 0 {
		st.Logf("niets toe te passen op %s", n.Hostname)
		return nil
	}
	titles := make([]string, 0, len(steps))
	for _, ts := range steps {
		titles = append(titles, lowerFirst(title(ts, drift.StepIDs(ts)[0])))
	}
	if !p.All {
		st.Logf("revisie %d toepassen: %s", p.Revision, strings.Join(titles, ", "))
	} else {
		st.Logf("revisie %d toepassen: alle %d stappen", p.Revision, len(steps))
	}
	if err := s.apply(ctx, j, st, p, n, &ns, ids, steps); err != nil {
		return err
	}
	return s.settle(ctx, st, p, n, d, rendered, "het toepassen op "+n.Hostname, n.Hostname+" is bijgewerkt")
}

// takeover controleert dat een andere node de VIP's van deze node kan
// overnemen, als hij er nu een heeft.
func (s *Service) takeover(ctx context.Context, clusterID uuid.UUID, n *PlanNode, st *jobs.Step) error {
	rt, err := s.q.GetNodeRuntime(ctx, n.NodeID)
	if err != nil {
		return err
	}
	vips, err := s.vips(ctx, clusterID)
	if err != nil {
		return err
	}
	var held []string
	for _, v := range vips {
		if slices.Contains(rt.Addresses, v) {
			held = append(held, v)
		}
	}
	if len(held) == 0 {
		return nil
	}
	peers, err := s.q.ListClusterPeers(ctx, store.ListClusterPeersParams{ClusterID: &clusterID, NodeID: n.NodeID})
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if peer.Lifecycle == store.NodeLifecycleActive && peer.HeartbeatAt != nil && time.Since(*peer.HeartbeatAt) <= s.Fresh &&
			health.ServiceStates(peer.Services)["keepalived"] == "active" {
			st.Logf("%s heeft %s; %s kan het overnemen", n.Hostname, strings.Join(held, ", "), peer.Hostname)
			return nil
		}
	}
	return fmt.Errorf("%s heeft %s en geen andere node kan het nu overnemen: er is geen actieve, online node waarop keepalived draait. Er is niets toegepast",
		n.Hostname, strings.Join(held, ", "))
}

func (s *Service) vips(ctx context.Context, clusterID uuid.UUID) ([]string, error) {
	rows, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Vip.Address.String())
	}
	return out, nil
}

// Select kiest uit de stappen van een node de stappen met deze ids
// (Finding.Step), in hun volgorde. Een pakketstap houdt alleen de gekozen
// pakketten.
func Select(steps []templates.Step, ids []string) []templates.Step {
	var out []templates.Step
	for _, ts := range steps {
		if ts.Package != nil {
			var names []string
			for _, name := range ts.Package.Names {
				if slices.Contains(ids, "package:"+name) {
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				pkg := *ts.Package
				pkg.Names = names
				ts.Step = protocol.Step{Package: &pkg}
				ts.Title = "Pakket " + strings.Join(names, ", ")
				out = append(out, ts)
			}
			continue
		}
		if slices.ContainsFunc(drift.StepIDs(ts), func(id string) bool { return slices.Contains(ids, id) }) {
			out = append(out, ts)
		}
	}
	return out
}

// Changed geeft de stappen waarvan de afwijkingen nu andere
// vingerafdrukken hebben dan in het plan, ook als een afwijking verdween.
func Changed(planned []PlanStep, ids []string, findings []drift.Finding) []string {
	now := map[string][]string{}
	for _, f := range findings {
		now[f.Step] = append(now[f.Step], f.Fingerprint)
	}
	var out []string
	for _, ps := range planned {
		if slices.Contains(ids, ps.Step) && !sameSet(ps.Fingerprints, now[ps.Step]) {
			out = append(out, ps.Step)
		}
	}
	return out
}

// activeServices zijn de services die na het toepassen active moeten zijn.
func activeServices(steps []templates.Step) []string {
	var out []string
	for _, ts := range steps {
		if svc := ts.Service; svc != nil && (svc.State == "started" || svc.State == "restarted" || svc.State == "reloaded") &&
			!slices.Contains(out, svc.Name) {
			out = append(out, svc.Name)
		}
	}
	return out
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}
