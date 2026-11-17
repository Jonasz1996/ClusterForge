package opnsense

// Een wijziging op een OPNsense-cluster als taak (fase 34): voorcontrole,
// toepassen op de master met een terugdraaitimer op de firewall zelf,
// proeven, bevestigen of terugdraaien, en de configsync naar de backup.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/textdiff"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	KindChange = "opnsense.change"
	KindSync   = "opnsense.sync"
	KindKey    = "opnsense.key"

	// DefaultConfirmWithin is de standaard terugdraaitijd in seconden.
	DefaultConfirmWithin = 120
)

// Change is een wijziging uit de GUI.
type Change struct {
	Calls []protocol.OPNCall `json:"calls"`
	// ConfirmWithin is de terugdraaitijd (60-600 s); 0 is de standaard.
	ConfirmWithin int `json:"confirm_within"`
	// Confirm is de overgetikte naam van het cluster bij een wijziging die
	// de toegang kan raken.
	Confirm string `json:"confirm"`
	// Restore zet een revisie uit /conf/backup terug (bestandsnaam) in
	// plaats van Calls; altijd met overtikken, proeven en terugdraaitimer.
	Restore string `json:"restore,omitempty"`
}

// planFor geeft het plan van een wijziging of een terugzetting.
func planFor(c Change) (Plan, error) {
	if c.Restore == "" {
		return check(c.Calls)
	}
	if len(c.Calls) > 0 {
		return Plan{}, errors.New("terugzetten gaat niet samen met andere aanroepen")
	}
	if !protocol.ValidOPNRevisionFile(c.Restore) {
		return Plan{}, errors.New("ongeldige revisie")
	}
	return Plan{Titles: []string{"revisie van " + revisionLabel(c.Restore) + " terugzetten"}, Level: LevelAccess}, nil
}

// revisionLabel maakt van config-1763340000.5.xml een leesbare tijd.
func revisionLabel(file string) string {
	n := strings.TrimSuffix(strings.TrimPrefix(file, "config-"), ".xml")
	n, _, _ = strings.Cut(n, ".")
	var sec int64
	if _, err := fmt.Sscan(n, &sec); err != nil || sec <= 0 {
		return file
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04 UTC")
}

// Check is de uitslag van de voorcontrole.
type Check struct {
	OK bool `json:"ok"`
	// Why zegt waarom het niet kan.
	Why     string     `json:"why,omitempty"`
	Master  *Firewall  `json:"-"`
	Backup  []Firewall `json:"-"`
	Cluster Cluster    `json:"-"`
}

// Preflight bekijkt of er op het cluster van id gewijzigd mag worden en op
// welke node.
func (s *Service) Preflight(ctx context.Context, id uuid.UUID) (Check, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return Check{}, err
	}
	return preflight(c), nil
}

func preflight(c Cluster) Check {
	ck := Check{Cluster: c}
	for _, p := range c.Problems {
		if strings.HasPrefix(p.Text, "split-brain") {
			ck.Why = "split-brain: twee firewalls zijn MASTER; los dat eerst op"
			return ck
		}
	}
	for _, n := range c.Nodes {
		if n.Why != "" {
			ck.Why = n.Why
			return ck
		}
		if n.Facts == nil || !n.Facts.API.OK {
			ck.Why = fmt.Sprintf("de agent op %s heeft geen werkende API-sleutel van OPNsense", n.Hostname)
			return ck
		}
	}
	switch {
	case len(c.Nodes) == 1:
		n := c.Nodes[0]
		ck.Master = &n
	case c.Master == nil:
		ck.Why = "geen enkele firewall is MASTER voor alle CARP-VIP's; wijzigen kan pas als één firewall dat is"
		return ck
	default:
		for _, n := range c.Nodes {
			if n.NodeID == *c.Master {
				m := n
				ck.Master = &m
				continue
			}
			if n.Role != "backup" {
				ck.Why = fmt.Sprintf("%s is geen BACKUP (rol %s); wijzigen kan pas als de rollen kloppen", n.Hostname, orDash(n.Role))
				return ck
			}
			ck.Backup = append(ck.Backup, n)
		}
	}
	ck.OK = true
	return ck
}

func orDash(s string) string {
	if s == "" {
		return "geen CARP"
	}
	return s
}

// changeParams zijn de parameters van een wijzigingstaak.
type changeParams struct {
	Cluster     string             `json:"cluster"`
	MasterID    uuid.UUID          `json:"master_id"`
	Master      string             `json:"master"`
	Backups     []uuid.UUID        `json:"backups,omitempty"`
	Change      protocol.OPNChange `json:"change"`
	Titles      []string           `json:"titles"`
	BackupNames []string           `json:"backup_names,omitempty"`
	// Each: ook op elke backup apart toepassen in plaats van te
	// synchroniseren (plugins).
	Each bool `json:"each,omitempty"`
	// Sealed zijn de geheime velden, versleuteld; sleutel "i|root.veld".
	Sealed *sealed `json:"sealed,omitempty"`
}

// sealSecrets haalt de geheime velden uit de aanroepen en versleutelt ze.
func (s *Service) sealSecrets(calls []protocol.OPNCall) (*sealed, error) {
	plain := map[string]string{}
	for i, c := range calls {
		for _, sp := range secretPaths(c.Body) {
			root, k, _ := strings.Cut(sp, ".")
			inner := c.Body[root].(map[string]any)
			if v, ok := inner[k].(string); ok {
				plain[fmt.Sprintf("%d|%s", i, sp)] = v
			}
			delete(inner, k)
		}
	}
	if len(plain) == 0 {
		return nil, nil
	}
	if s.Box == nil {
		return nil, ValidationError{"deze server heeft geen masterkey; zonder kan ClusterForge geen wachtwoorden of sleutels doorgeven"}
	}
	data, _ := json.Marshal(plain)
	nonce := uuid.NewString()
	return &sealed{KeyID: s.Box.KeyID, Nonce: nonce, Data: s.Box.Seal(data, changeAAD(nonce))}, nil
}

func changeAAD(nonce string) []byte { return []byte("opnsense-change:" + nonce) }

// openSecrets zet de geheime velden terug in de aanroepen.
func (s *Service) openSecrets(seal *sealed, calls []protocol.OPNCall) error {
	if seal == nil {
		return nil
	}
	if s.Box == nil {
		return errors.New("geen masterkey meer: de geheimen van deze taak zijn niet te lezen")
	}
	data, err := s.Box.Open(seal.Data, changeAAD(seal.Nonce), seal.KeyID)
	if err != nil {
		return fmt.Errorf("de geheimen van deze taak zijn niet te lezen: %w", err)
	}
	var plain map[string]string
	if err := json.Unmarshal(data, &plain); err != nil {
		return err
	}
	for key, v := range plain {
		idx, sp, _ := strings.Cut(key, "|")
		root, k, _ := strings.Cut(sp, ".")
		var i int
		if _, err := fmt.Sscan(idx, &i); err != nil || i < 0 || i >= len(calls) {
			return errors.New("ongeldige geheimen in de taak")
		}
		inner, ok := calls[i].Body[root].(map[string]any)
		if !ok {
			return errors.New("ongeldige geheimen in de taak")
		}
		inner[k] = v
	}
	return nil
}

// StartChange zet een wijziging als taak in de wachtrij. id is een node van
// het cluster; de wijziging gaat altijd naar de master.
func (s *Service) StartChange(ctx context.Context, actor events.Actor, id uuid.UUID, c Change) (store.Job, error) {
	plan, err := planFor(c)
	if err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	within := c.ConfirmWithin
	if within == 0 {
		within = DefaultConfirmWithin
	}
	if within < 60 || within > 600 {
		return store.Job{}, ValidationError{"de terugdraaitijd moet tussen 60 en 600 seconden liggen"}
	}
	ck, err := s.Preflight(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if !ck.OK {
		return store.Job{}, ValidationError{ck.Why}
	}
	if plan.Level == LevelAccess && strings.TrimSpace(c.Confirm) != ck.Cluster.Name {
		return store.Job{}, ValidationError{fmt.Sprintf("deze wijziging kan de toegang tot de firewall raken; tik %s over om te bevestigen", ck.Cluster.Name)}
	}
	title := plan.Title()
	ch := protocol.OPNChange{
		ID: uuid.NewString(), Calls: c.Calls, Apply: plan.Apply, ConfirmWithin: within, Restore: c.Restore,
		Description: "ClusterForge: " + truncate(title, 180), RequireMaster: ck.Cluster.HA || ck.Master.Role != "",
	}
	if err := ch.Validate(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	ch.Secret = plan.Secret
	seal, err := s.sealSecrets(ch.Calls)
	if err != nil {
		return store.Job{}, err
	}
	p := changeParams{Cluster: ck.Cluster.Name, MasterID: ck.Master.NodeID, Master: ck.Master.Hostname, Change: ch, Titles: plan.Titles, Sealed: seal}
	for _, b := range ck.Backup {
		p.Backups = append(p.Backups, b.NodeID)
		p.BackupNames = append(p.BackupNames, b.Hostname)
	}
	p.Each = plan.Each
	j, err := s.enqueue(ctx, actor, ck.Cluster, KindChange, title+" op "+ck.Cluster.Name, ck.Master.NodeID, p)
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, ck.Master.NodeID, "opnsense.change_requested", map[string]any{
		"cluster": ck.Cluster.Name, "firewall": ck.Master.Hostname, "titles": plan.Titles, "calls": c.Calls,
		"job_id": j.ID.String(), "change": ch.ID, "confirm_within": within, "restore": c.Restore,
	})
	return j, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (s *Service) enqueue(ctx context.Context, actor events.Actor, c Cluster, kind, title string, node uuid.UUID, p any) (store.Job, error) {
	spec := jobs.Spec{Kind: kind, Title: title, Actor: actor, NodeID: &node, ClusterID: c.ClusterID, Params: p}
	var (
		j   store.Job
		err error
	)
	if c.ClusterID != nil {
		j, err = s.jobs.EnqueueForCluster(ctx, spec)
	} else {
		j, err = s.jobs.Enqueue(ctx, spec)
	}
	if err != nil {
		return store.Job{}, err
	}
	s.cache.clear()
	return j, nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, node uuid.UUID, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "node", SubjectID: node.String(), Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
}

// baseline is de toestand voor de wijziging.
type baseline struct {
	// Probes zijn de adressen (host:poort) die voor de wijziging antwoordden.
	Probes []string `json:"probes"`
	// Roles is de CARP-rol per node.
	Roles map[string]string `json:"roles"`
	// Down zijn de gateways die al down waren.
	Down []string `json:"down"`
	// Revision is de tijd van de laatste revisie op elke backup.
	Revision map[string]int64 `json:"revision"`
}

// probeTargets zijn de webinterface van elke node en de CARP-VIP's.
func probeTargets(c Cluster) []string {
	var out []string
	add := func(host, port string) {
		host, _, _ = strings.Cut(host, "/")
		if host == "" {
			return
		}
		t := net.JoinHostPort(host, port)
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	for _, n := range c.Nodes {
		port := "443"
		if n.Facts != nil {
			if _, p, ok := strings.Cut(n.Facts.GUI, ":"); ok && p != "" {
				port = p
			}
		}
		add(n.Address, port)
		if n.Facts != nil {
			for _, v := range n.Facts.VIPs {
				if v.Mode == "carp" {
					add(v.Address, port)
				}
			}
		}
	}
	return out
}

func (s *Service) probe(ctx context.Context, addr string) error {
	if s.Probe != nil {
		return s.Probe(ctx, addr)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// facts haalt de facts van nu bij de agent.
func (s *Service) facts(ctx context.Context, id uuid.UUID) (*protocol.OPNsenseFacts, error) {
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpFacts}, false)
	if err != nil {
		return nil, err
	}
	var f protocol.OPNsenseFacts
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// gatewaysDown leest welke gateways down zijn.
func (s *Service) gatewaysDown(ctx context.Context, id uuid.UUID) ([]string, error) {
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpConfigctl, Path: "interface gateways status"}, false)
	if err != nil {
		return nil, err
	}
	return parseGatewaysDown(data), nil
}

// parseGatewaysDown leest de uitvoer van "interface gateways status": een
// lijst of een object van gateways met een status (none is online).
func parseGatewaysDown(data json.RawMessage) []string {
	type gw struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var list []gw
	if json.Unmarshal(data, &list) != nil {
		var m map[string]gw
		if json.Unmarshal(data, &m) != nil {
			return nil
		}
		for k, g := range m {
			if g.Name == "" {
				g.Name = k
			}
			list = append(list, g)
		}
	}
	var down []string
	for _, g := range list {
		switch strings.ToLower(g.Status) {
		case "down", "force_down":
			down = append(down, g.Name)
		}
	}
	slices.Sort(down)
	return down
}

func (s *Service) runChange(ctx context.Context, j *jobs.Job) error {
	var p changeParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	if err := s.runChangeSteps(ctx, j, p); err != nil {
		return err
	}
	s.backupStep(ctx, j, append([]uuid.UUID{p.MasterID}, p.Backups...), "na wijziging")
	return nil
}

func (s *Service) runChangeSteps(ctx context.Context, j *jobs.Job, p changeParams) error {
	defer s.cache.clear()
	ch := p.Change
	if err := s.openSecrets(p.Sealed, ch.Calls); err != nil {
		return err
	}
	nodes := append([]uuid.UUID{p.MasterID}, p.Backups...)

	var base baseline
	if err := j.Step(ctx, "Voorcontrole", func(ctx context.Context, st *jobs.Step) error {
		c, err := s.ClusterOf(ctx, p.MasterID)
		if err != nil {
			return err
		}
		ck := preflight(c)
		if !ck.OK {
			return errors.New(ck.Why)
		}
		if ck.Master.NodeID != p.MasterID {
			return fmt.Errorf("de master is intussen %s in plaats van %s; start de wijziging opnieuw", ck.Master.Hostname, p.Master)
		}
		base = baseline{Roles: map[string]string{}, Revision: map[string]int64{}}
		for _, id := range nodes {
			f, err := s.facts(ctx, id)
			if err != nil {
				return fmt.Errorf("de agent op een firewall antwoordt niet: %w", err)
			}
			base.Roles[id.String()] = f.Role()
			if id != p.MasterID {
				base.Revision[id.String()] = f.Config.Time
			}
		}
		for _, t := range probeTargets(c) {
			if err := s.probe(ctx, t); err != nil {
				st.Logf("%s antwoordt nu al niet (%v); die proef telt niet mee", t, err)
				continue
			}
			base.Probes = append(base.Probes, t)
			st.Logf("%s antwoordt", t)
		}
		if down, err := s.gatewaysDown(ctx, p.MasterID); err == nil {
			base.Down = down
			if len(down) > 0 {
				st.Logf("gateways die nu al down zijn: %s", strings.Join(down, ", "))
			}
		} else {
			st.Logf("gatewaystatus lezen mislukte (%v); die proef telt niet mee", err)
			base.Down = nil
		}
		st.Logf("master %s; %d proeven klaar; terugdraaien na %d s zonder bevestiging", p.Master, len(base.Probes), ch.ConfirmWithin)
		return st.SetState(ctx, base)
	}); err != nil {
		return err
	}
	j.Done("Voorcontrole", &base)

	if err := s.applyOn(ctx, j, p.MasterID, p.Master, ch, p, base, ""); err != nil {
		return err
	}
	if p.Each {
		// Niet gesynchroniseerd (plugins): op elke backup apart, met een
		// eigen herstelpunt en timer.
		for i, b := range p.Backups {
			bch := ch
			bch.ID = fmt.Sprintf("%s-b%d", ch.ID[:min(len(ch.ID), 56)], i+1)
			bch.RequireMaster = false
			name := b.String()
			if i < len(p.BackupNames) {
				name = p.BackupNames[i]
			}
			if err := s.applyOn(ctx, j, b, name, bch, p, base, " op "+name); err != nil {
				return err
			}
		}
		return nil
	}
	if len(p.Backups) == 0 {
		return nil
	}
	return s.syncStep(ctx, j, p.MasterID, p.Master, p.Backups, base.Revision)
}

// verify doet de proeven na het toepassen en geeft leeg als alles klopt,
// anders waarom niet. Hij probeert opnieuw tot ongeveer de helft van de
// terugdraaitijd, zodat een herlaadbeurt van een paar seconden geen
// terugdraaien geeft.
// applyOn past ch toe op één node, doet de proeven en bevestigt of draait
// terug.
func (s *Service) applyOn(ctx context.Context, j *jobs.Job, node uuid.UUID, host string, ch protocol.OPNChange, p changeParams, base baseline, suffix string) error {
	if err := j.Step(ctx, "Toepassen op "+host, func(ctx context.Context, st *jobs.Step) error {
		for _, t := range p.Titles {
			st.Logf("• %s", t)
		}
		res, err := s.do(ctx, st, st, node, protocol.PVERun{Action: protocol.OPNAPI, OPN: &ch}, "OPNsense-wijziging op "+host)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return fmt.Errorf("%s; de oude configuratie staat terug", noderun.Describe(res))
		}
		return nil
	}); err != nil {
		return err
	}
	return j.Step(ctx, "Proeven en bevestigen"+suffix, func(ctx context.Context, st *jobs.Step) error {
		why := s.verify(ctx, st, p, base)
		if why == "" {
			res, err := s.do(ctx, st, st, node, protocol.PVERun{Action: protocol.OPNConfirm, Change: ch.ID}, "OPNsense-wijziging bevestigen")
			if err != nil {
				return err
			}
			if res.State != protocol.RunOK {
				return fmt.Errorf("bevestigen mislukte: %s", noderun.Describe(res))
			}
			st.Logf("bevestigd")
			return nil
		}
		st.Logf("proef mislukt: %s; terugdraaien", why)
		res, err := s.do(ctx, st, st, node, protocol.PVERun{Action: protocol.OPNRollback, Change: ch.ID}, "OPNsense-wijziging terugdraaien")
		if err != nil || res.State != protocol.RunOK {
			return fmt.Errorf("proef mislukt (%s); de firewall draait zelf terug zodra de timer afloopt", why)
		}
		return fmt.Errorf("proef mislukt (%s); de oude configuratie staat terug", why)
	})
}

func (s *Service) verify(ctx context.Context, st *jobs.Step, p changeParams, base baseline) string {
	if s.Wait > 0 {
		select {
		case <-ctx.Done():
			return "onderbroken"
		case <-time.After(s.Wait):
		}
	}
	settle := s.Settle
	if settle <= 0 {
		settle = time.Duration(p.Change.ConfirmWithin) * time.Second / 2
	}
	deadline := time.Now().Add(settle)
	for attempt := 1; ; attempt++ {
		why := s.verifyOnce(ctx, p, base)
		if why == "" {
			st.Logf("alle proeven geslaagd (poging %d)", attempt)
			return ""
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return why
		}
		st.Logf("nog niet: %s", why)
		select {
		case <-ctx.Done():
			return why
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *Service) verifyOnce(ctx context.Context, p changeParams, base baseline) string {
	nodes := append([]uuid.UUID{p.MasterID}, p.Backups...)
	for _, id := range nodes {
		f, err := s.facts(ctx, id)
		if err != nil {
			return "een agent antwoordt niet meer: " + err.Error()
		}
		if was, now := base.Roles[id.String()], f.Role(); was != now {
			return fmt.Sprintf("CARP veranderde op een firewall: %s → %s", orDash(was), orDash(now))
		}
	}
	for _, t := range base.Probes {
		if err := s.probe(ctx, t); err != nil {
			return fmt.Sprintf("%s antwoordt niet meer (%v)", t, err)
		}
	}
	if base.Down != nil {
		down, err := s.gatewaysDown(ctx, p.MasterID)
		if err != nil {
			return "gatewaystatus lezen mislukte: " + err.Error()
		}
		for _, d := range down {
			if !slices.Contains(base.Down, d) {
				return "gateway " + d + " is down gegaan"
			}
		}
	}
	return ""
}

// syncStep synchroniseert de master naar de backups en kijkt of die een
// nieuwe revisie kregen.
func (s *Service) syncStep(ctx context.Context, j *jobs.Job, master uuid.UUID, name string, backups []uuid.UUID, before map[string]int64) error {
	return j.Step(ctx, "Synchroniseren naar backup", func(ctx context.Context, st *jobs.Step) error {
		if before == nil {
			before = map[string]int64{}
			for _, b := range backups {
				if f, err := s.facts(ctx, b); err == nil {
					before[b.String()] = f.Config.Time
				}
			}
		}
		res, err := s.do(ctx, st, st, master, protocol.PVERun{Action: protocol.OPNHASync}, "Configuratie synchroniseren vanaf "+name)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return fmt.Errorf("synchroniseren mislukte: %s; de master is wel gewijzigd, start Sync nu opnieuw", noderun.Describe(res))
		}
		for _, b := range backups {
			f, err := s.facts(ctx, b)
			if err != nil {
				return fmt.Errorf("de backup antwoordt niet na de sync: %w", err)
			}
			if f.Config.Time <= before[b.String()] {
				return fmt.Errorf("%s kreeg geen nieuwe configuratie: kijk System → High Availability → Status op de master", f.Hostname)
			}
			st.Logf("%s heeft de nieuwe configuratie (%s)", f.Hostname, f.Config.Description)
		}
		return nil
	})
}

func (s *Service) do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, op protocol.PVERun, title string) (protocol.RunStatus, error) {
	spec := protocol.RunSpec{ID: uuid.NewString(), Kind: protocol.RunPVE, Title: title, PVE: &op}
	if err := spec.Validate(); err != nil {
		return protocol.RunStatus{}, err
	}
	return s.Run.DoWith(ctx, log, ss, nodeID, spec)
}

// --- Sync nu ---------------------------------------------------------------

type syncParams struct {
	MasterID uuid.UUID   `json:"master_id"`
	Master   string      `json:"master"`
	Backups  []uuid.UUID `json:"backups"`
}

// StartSync synchroniseert de configuratie van de master naar de backup.
func (s *Service) StartSync(ctx context.Context, actor events.Actor, id uuid.UUID) (store.Job, error) {
	ck, err := s.Preflight(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if !ck.OK {
		return store.Job{}, ValidationError{ck.Why}
	}
	if len(ck.Backup) == 0 {
		return store.Job{}, ValidationError{"dit is een losse firewall zonder backup"}
	}
	if ck.Master.Facts.HA.SyncTo == "" {
		return store.Job{}, ValidationError{"op de master staat geen configsync naar de backup (System → High Availability → Synchronize Config to IP)"}
	}
	p := syncParams{MasterID: ck.Master.NodeID, Master: ck.Master.Hostname}
	for _, b := range ck.Backup {
		p.Backups = append(p.Backups, b.NodeID)
	}
	j, err := s.enqueue(ctx, actor, ck.Cluster, KindSync, "Configuratie synchroniseren op "+ck.Cluster.Name, ck.Master.NodeID, p)
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, ck.Master.NodeID, "opnsense.sync_requested", map[string]any{"cluster": ck.Cluster.Name, "job_id": j.ID.String()})
	return j, nil
}

func (s *Service) runSync(ctx context.Context, j *jobs.Job) error {
	var p syncParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.cache.clear()
	return s.syncStep(ctx, j, p.MasterID, p.Master, p.Backups, nil)
}

// --- Sleutel plakken -------------------------------------------------------

type keyParams struct {
	NodeID uuid.UUID `json:"node_id"`
	Host   string    `json:"host"`
	Sealed *sealed   `json:"sealed"`
}

type sealed struct {
	KeyID string `json:"key_id"`
	Nonce string `json:"nonce"`
	Data  []byte `json:"data"`
}

func sealAAD(nonce string) []byte { return []byte("opnsense-key:" + nonce) }

// StartKey zet een met de hand gemaakte API-sleutel op een firewall, als de
// agent er zelf geen kan maken. Sleutel en geheim gaan versleuteld in de
// taak.
func (s *Service) StartKey(ctx context.Context, actor events.Actor, id uuid.UUID, in protocol.OPNKeyInput) (store.Job, error) {
	in.Key, in.Secret = strings.TrimSpace(in.Key), strings.TrimSpace(in.Secret)
	op := protocol.PVERun{Action: protocol.OPNKey, OPNKey: &in}
	if err := (&protocol.RunSpec{ID: "check", Kind: protocol.RunPVE, Title: "check", PVE: &op}).Validate(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	f, err := s.Firewall(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if f.Why != "" {
		return store.Job{}, ValidationError{f.Why}
	}
	if s.Box == nil {
		return store.Job{}, ValidationError{"deze server heeft geen masterkey; zonder kan ClusterForge de sleutel niet veilig doorgeven"}
	}
	data, _ := json.Marshal(in)
	nonce := uuid.NewString()
	p := keyParams{NodeID: id, Host: f.Hostname, Sealed: &sealed{KeyID: s.Box.KeyID, Nonce: nonce, Data: s.Box.Seal(data, sealAAD(nonce))}}
	node := id
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{Kind: KindKey, Title: "API-sleutel van OPNsense zetten op " + f.Hostname, Actor: actor, NodeID: &node, Params: p})
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, id, "opnsense.key_pasted", map[string]any{"firewall": f.Hostname, "job_id": j.ID.String()})
	return j, nil
}

func (s *Service) runKey(ctx context.Context, j *jobs.Job) error {
	var p keyParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	if s.Box == nil || p.Sealed == nil {
		return errors.New("geen masterkey: de sleutel in deze taak is niet te lezen")
	}
	data, err := s.Box.Open(p.Sealed.Data, sealAAD(p.Sealed.Nonce), p.Sealed.KeyID)
	if err != nil {
		return fmt.Errorf("de sleutel in deze taak is niet te lezen: %w", err)
	}
	var in protocol.OPNKeyInput
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	return j.Step(ctx, j.Title, func(ctx context.Context, st *jobs.Step) error {
		res, err := s.do(ctx, st, st, p.NodeID, protocol.PVERun{Action: protocol.OPNKey, OPNKey: &in}, "API-sleutel zetten op "+p.Host)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	})
}

// --- Diff ------------------------------------------------------------------

// stripVolatile haalt het revisieblok en de eigen hostname weg.
func stripVolatile(text string) string {
	var out []string
	inRevision := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "<revision>"):
			inRevision = !strings.Contains(t, "</revision>")
			continue
		case inRevision:
			if strings.HasPrefix(t, "</revision>") {
				inRevision = false
			}
			continue
		case strings.HasPrefix(t, "<hostname>") || strings.HasPrefix(t, "<lastchange>"):
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func textDiff(a, b, from, to string) (string, int) {
	return textdiff.Diff(a, b, from, to)
}

// PlanView is wat de GUI toont voor hij een wijziging start.
type PlanView struct {
	// Each: op elke firewall apart (plugins), geen sync.
	Each   bool     `json:"each,omitempty"`
	Titles []string `json:"titles"`
	Apply  []string `json:"apply"`
	Level  string   `json:"level"`
	// OK en Why komen uit de voorcontrole.
	OK      bool     `json:"ok"`
	Why     string   `json:"why,omitempty"`
	Cluster string   `json:"cluster"`
	Master  string   `json:"master,omitempty"`
	Backups []string `json:"backups"`
	// Confirm is wat je overtikt; leeg als het niet hoeft.
	Confirm string `json:"confirm,omitempty"`
	// Prod: er is een code of wachtwoord nodig.
	Prod          bool `json:"prod"`
	ConfirmWithin int  `json:"confirm_within"`
}

// PlanChange geeft plan en voorcontrole zonder iets te veranderen.
func (s *Service) PlanChange(ctx context.Context, id uuid.UUID, c Change) (PlanView, error) {
	plan, err := planFor(c)
	if err != nil {
		return PlanView{}, ValidationError{err.Error()}
	}
	ck, err := s.Preflight(ctx, id)
	if err != nil {
		return PlanView{}, err
	}
	v := PlanView{
		Titles: plan.Titles, Level: plan.Level, Each: plan.Each, OK: ck.OK, Why: ck.Why, Cluster: ck.Cluster.Name, Backups: []string{},
		Prod: ck.Cluster.Environment == "prod", ConfirmWithin: DefaultConfirmWithin,
	}
	for _, a := range plan.Apply {
		v.Apply = append(v.Apply, a.Path)
	}
	if ck.Master != nil {
		v.Master = ck.Master.Hostname
	}
	for _, b := range ck.Backup {
		v.Backups = append(v.Backups, b.Hostname)
	}
	if plan.Level == LevelAccess {
		v.Confirm = ck.Cluster.Name
	}
	return v, nil
}

// IsProd zegt of de firewall in een prodcluster zit.
func (s *Service) IsProd(ctx context.Context, id uuid.UUID) (bool, error) {
	f, err := s.Firewall(ctx, id)
	if err != nil {
		return false, err
	}
	return f.Environment == "prod", nil
}
