package components

// Instellingen van een database wijzigen, zoals de buffer pool of
// max_connections: een nieuwe revisie van het onderdeel, die ClusterForge
// node voor node toepast met de schrijver als laatste. Bij Galera herstart
// MariaDB op elke node met de nieuwe configuratie, en ClusterForge wacht na
// elke node tot hij weer Synced is. Bij Patroni zet ClusterForge
// max_connections in etcd, waar Patroni het voor het hele cluster bewaart,
// en herstart PostgreSQL daarna node voor node: de replica's eerst, de
// primary als laatste, zonder failover.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// SettingParam is een instelling die de gebruiker kan wijzigen, met haar
// waarde nu.
type SettingParam struct {
	Name, Label, Help, Type string
	Value                   string
	Min, Max                string
}

// SettingChange is een instelling die verandert.
type SettingChange struct {
	Name, Label, From, To string
}

// SettingsNode is een datanode in het plan, op de volgorde van toepassen.
type SettingsNode struct {
	NodeID   uuid.UUID
	Hostname string
	State    string
	Healthy  bool
	Writer   bool
	// MemoryBytes is het geheugen van de node; MemoryUsedPct wat er de
	// laatste week tot gebruikt werd, de database meegerekend, en
	// FreeAfterPct wat er met de nieuwe instellingen vrij blijft.
	MemoryBytes   int64
	MemoryUsedPct *float64
	FreeAfterPct  *float64
}

// SettingsPlan zegt wat nieuwe instellingen doen. Er verandert niets.
type SettingsPlan struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Prod        bool
	Engine      string
	Title       string
	Revision    int
	Params      []SettingParam
	Changes     []SettingChange
	Nodes       []SettingsNode
	Effects     []string
	Findings    []Finding
	Blocked     bool
	Hash        string

	spec  Spec
	order []uuid.UUID
}

// SettingsInput zijn de nieuwe instellingen; wat ontbreekt, blijft.
type SettingsInput struct {
	Settings map[string]any
	Hash     string
}

// editable zijn de parameters die na de uitrol nog kunnen veranderen:
// geen geheimen, geen VIP en niets wat vastligt.
func editable(tpl *templates.Template) []templates.Param {
	var out []templates.Param
	for _, p := range tpl.Params {
		if p.Type != "secret" && !p.Immutable && p.Name != vipParam {
			out = append(out, p)
		}
	}
	return out
}

// paramText is een waarde van een parameter als tekst, zoals 1G of 200.
func paramText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		if x == math.Trunc(x) {
			return fmt.Sprintf("%d", int64(x))
		}
	}
	return fmt.Sprint(v)
}

// SettingsPlan zegt wat de nieuwe instellingen in doen.
func (s *Service) SettingsPlan(ctx context.Context, clusterID, id uuid.UUID, in map[string]any) (SettingsPlan, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return SettingsPlan{}, err
	}
	comp, err := s.dbComponent(ctx, clusterID, id)
	if err != nil {
		return SettingsPlan{}, err
	}
	spec, err := parseSpec(comp.Spec)
	if err != nil {
		return SettingsPlan{}, err
	}
	tpl, ok := registry.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return SettingsPlan{}, fmt.Errorf("%s %s zit niet in deze server", spec.Template.Name, spec.Template.Version)
	}
	p := SettingsPlan{
		ClusterID: clusterID, ClusterName: c.Name, Slug: c.Slug, Prod: c.Environment == store.EnvironmentProd, Engine: comp.Engine, Title: tpl.Title,
		Revision: int(comp.Revision),
	}
	add := func(level, code string, n *Node, msg, command string) {
		f := Finding{Level: level, Code: code, Message: msg, Command: command}
		if n != nil {
			f.NodeID, f.Hostname = &n.NodeID, n.Hostname
		}
		p.Findings = append(p.Findings, f)
	}
	params := maps.Clone(spec.Params)
	for _, prm := range editable(tpl) {
		now := paramText(spec.Params[prm.Name])
		if v, err := tpl.Normalize(prm.Name, spec.Params[prm.Name]); err == nil {
			now = paramText(v)
		}
		p.Params = append(p.Params, SettingParam{Name: prm.Name, Label: prm.Label, Help: prm.Help, Type: prm.Type, Value: now,
			Min: paramText(prm.Min), Max: paramText(prm.Max)})
		v, ok := in[prm.Name]
		if !ok {
			continue
		}
		norm, err := tpl.Normalize(prm.Name, v)
		if err != nil {
			return SettingsPlan{}, &InvalidError{prm.Name, err.Error()}
		}
		if to := paramText(norm); to != now {
			p.Changes = append(p.Changes, SettingChange{Name: prm.Name, Label: prm.Label, From: now, To: to})
			params[prm.Name] = norm
		}
	}
	for k := range in {
		if !slices.ContainsFunc(p.Params, func(x SettingParam) bool { return x.Name == k }) {
			return SettingsPlan{}, &InvalidError{k, "deze instelling kan na de uitrol niet meer veranderen"}
		}
	}
	spec.Params = params
	p.spec = spec

	switch {
	case comp.AppliedRevision == nil:
		add(Blocked, "not_applied", nil, "de database is nog nooit helemaal uitgerold; kies eerst Opnieuw toepassen", "")
	case int(*comp.AppliedRevision) != p.Revision:
		add(Blocked, "pending_revision", nil, fmt.Sprintf("revisie %d is nog niet toegepast; kies eerst Opnieuw toepassen", p.Revision), "")
	}
	if len(p.Changes) == 0 {
		add(Blocked, "no_changes", nil, "er verandert niets", "")
	}
	nodes, err := s.rolling(ctx, comp, spec)
	if err != nil {
		return SettingsPlan{}, err
	}
	rollingFindings(nodes, comp.Engine, "de instellingen wijzigt", add)
	p.order = rollingOrder(nodes)
	s.settingsMemory(ctx, &p, tpl, nodes, add)
	p.Effects = settingsEffects(p, spec, nodes)
	for _, f := range p.Findings {
		if f.Level == Blocked {
			p.Blocked = true
		}
	}
	p.Hash = settingsHash(comp, p)
	return p, nil
}

// settingsMemory zet het geheugen per datanode in het plan, en waarschuwt
// als er met een grotere buffer pool of grotere shared buffers minder dan
// 15% vrij blijft.
func (s *Service) settingsMemory(ctx context.Context, p *SettingsPlan, tpl *templates.Template, nodes []rollNode, add func(string, string, *Node, string, string)) {
	e := engines[p.Engine]
	extra := int64(0)
	for _, ch := range p.Changes {
		if ch.Name != e.memParam {
			continue
		}
		from, err1 := templates.ParseSize(ch.From)
		to, err2 := templates.ParseSize(ch.To)
		if err1 == nil && err2 == nil {
			extra = to - from
		}
	}
	usage := s.memory(ctx)
	label := e.memParam
	if prm, ok := tpl.Param(e.memParam); ok {
		label = strings.ToLower(prm.Label)
	}
	for _, r := range nodes {
		if r.node.Role != RoleData {
			continue
		}
		n := SettingsNode{NodeID: r.node.NodeID, Hostname: r.node.Hostname, State: r.state, Healthy: r.healthy, Writer: r.writer}
		if r.facts != nil {
			n.MemoryBytes = int64(min(r.facts.MemoryBytes, math.MaxInt64)) //nolint:gosec // geheugen past altijd
		}
		if pct, ok := usage[r.node.NodeID]; ok && n.MemoryBytes > 0 {
			used := int64(float64(n.MemoryBytes) * pct / 100)
			after := float64(n.MemoryBytes-used-extra) / float64(n.MemoryBytes) * 100
			n.MemoryUsedPct, n.FreeAfterPct = &pct, &after
			if extra > 0 && after < 15 {
				want := int64(math.Ceil(float64(used+extra)/0.85/float64(512<<20))) * 512
				msg := fmt.Sprintf("%s heeft %s geheugen en gebruikte de afgelopen zeven dagen tot %.0f%%, de database meegerekend; met %s meer %s blijft er minder dan 15%% over",
					r.node.Hostname, templates.FormatSize(n.MemoryBytes), pct, templates.FormatSize(extra), label)
				cmdline := ""
				switch {
				case deref(r.cand.PveType) == "lxc" && r.cand.PveVmid != nil:
					cmdline = fmt.Sprintf("pct set %d --memory %d", *r.cand.PveVmid, want)
					msg += fmt.Sprintf(". Geef de CT %d MiB met dit commando op %s", want, firstNonEmpty(deref(r.cand.PveNode), "de Proxmox-host"))
				case deref(r.cand.PveType) == "qemu" && r.cand.PveVmid != nil:
					cmdline = fmt.Sprintf("qm set %d --memory %d", *r.cand.PveVmid, want)
					msg += fmt.Sprintf(". Geef de VM %d MiB met dit commando op %s; zonder hotplug van geheugen telt het pas na een herstart", want,
						firstNonEmpty(deref(r.cand.PveNode), "de Proxmox-host"))
				default:
					msg += fmt.Sprintf(". Geef de node minstens %d MiB", want)
				}
				add(Warning, "memory", &r.node, msg, cmdline)
			}
		}
		p.Nodes = append(p.Nodes, n)
	}
}

// settingsEffects zegt in gewone woorden wat er gebeurt.
func settingsEffects(p SettingsPlan, spec Spec, nodes []rollNode) []string {
	writers, others := dataNames(nodes, true), dataNames(nodes, false)
	order := joinHosts(others)
	if len(writers) > 0 {
		order += " en als laatste " + joinHosts(writers)
	}
	var out []string
	if p.Engine == protocol.DBPostgreSQL {
		changed := map[string]bool{}
		for _, ch := range p.Changes {
			changed[ch.Name] = true
		}
		if changed["shared_buffers"] {
			out = append(out, "ClusterForge schrijft shared_buffers in de configuratie van Patroni op elke node.")
		}
		if changed["max_connections"] {
			out = append(out, "max_connections komt in etcd; Patroni geeft het door aan elke node.")
		}
		out = append(out, fmt.Sprintf("Daarna herstart PostgreSQL node voor node: %s, de primary. ClusterForge wacht na elke node tot hij weer bijhoudt.", order))
		if len(writers) > 0 {
			out = append(out, fmt.Sprintf("Bij de herstart van %s kan de app een paar seconden niet schrijven; Patroni blijft daar de primary, er is geen failover.", joinHosts(writers)))
		}
		return out
	}
	what := "de nieuwe configuratie"
	out = append(out, fmt.Sprintf("ClusterForge schrijft %s op elke node en herstart MariaDB node voor node: %s. Het wacht na elke node tot hij weer Synced is.", what, order))
	if len(writers) > 0 && spec.VIP != nil {
		out = append(out, fmt.Sprintf("Bij de herstart van %s gaat het VIP %s naar een andere node; de app kan een paar seconden niet schrijven.", joinHosts(writers), spec.VIP.Address))
	}
	out = append(out, "De andere nodes werken door, dus de database blijft bereikbaar.")
	return out
}

// settingsHash vat het plan samen: de revisie, wat verandert, de volgorde
// en de bevindingen, zonder metingen.
func settingsHash(comp store.ClusterComponent, p SettingsPlan) string {
	type finding struct {
		Level, Code string
		Node        *uuid.UUID
	}
	v := struct {
		Component uuid.UUID       `json:"component"`
		Revision  int             `json:"revision"`
		Changes   []SettingChange `json:"changes"`
		Order     []uuid.UUID     `json:"order"`
		Findings  []finding       `json:"findings"`
	}{Component: comp.ID, Revision: p.Revision, Changes: p.Changes, Order: p.order}
	for _, f := range p.Findings {
		v.Findings = append(v.Findings, finding{f.Level, f.Code, f.NodeID})
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ChangeSettings maakt een nieuwe revisie met de nieuwe instellingen en zet
// de taak die ze toepast in de wachtrij, als het plan nog is wat de
// gebruiker zag en niets het tegenhoudt.
func (s *Service) ChangeSettings(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, in SettingsInput) (store.Job, error) {
	p, err := s.SettingsPlan(ctx, clusterID, id, in.Settings)
	if err != nil {
		return store.Job{}, err
	}
	if p.Hash != in.Hash {
		return store.Job{}, ErrStale
	}
	for _, f := range p.Findings {
		if f.Level == Blocked {
			return store.Job{}, &ConflictError{f.Message}
		}
	}
	b, err := json.Marshal(p.spec)
	if err != nil {
		return store.Job{}, err
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		comp, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		if int(comp.Revision) != p.Revision {
			return ErrStale
		}
		rev := comp.Revision + 1
		var by *uuid.UUID
		if uid, err := uuid.Parse(actor.ID); err == nil {
			by = &uid
		}
		if err := q.InsertClusterComponentRevision(ctx, store.InsertClusterComponentRevisionParams{ComponentID: comp.ID, Revision: rev, Spec: b, CreatedBy: by}); err != nil {
			return err
		}
		if err := q.SetClusterComponentSpec(ctx, store.SetClusterComponentSpecParams{ID: comp.ID, Spec: b, Revision: rev}); err != nil {
			return err
		}
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: Kind, Title: fmt.Sprintf("Instellingen wijzigen: %s op %s", p.Title, p.ClusterName),
			Params: jobParams{ComponentID: comp.ID, Revision: int(rev), Order: p.order, Restart: true}, ClusterID: &clusterID, Actor: actor,
		})
		if err != nil {
			return err
		}
		if err := q.SetClusterComponentJob(ctx, store.SetClusterComponentJobParams{ID: comp.ID, JobID: &j.ID}); err != nil {
			return err
		}
		changes := make([]map[string]string, len(p.Changes))
		for i, ch := range p.Changes {
			changes[i] = map[string]string{"name": ch.Name, "label": ch.Label, "from": ch.From, "to": ch.To}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "cluster.database_settings",
			Payload: map[string]any{"name": p.ClusterName, "component": p.Title, "revision": rev, "changes": changes, "job_id": j.ID, "title": j.Title},
		})
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.jobs.Kick()
	}
	return j, err
}

// patroniSettings zet de instellingen van Patroni die voor het hele cluster
// gelden in etcd, en herstart PostgreSQL daarna node voor node waar dat
// nodig is: de replica's eerst, de primary als laatste. Met restart herstart
// elke node, zoals na nieuwe instellingen; anders alleen een node waarvan
// Patroni zegt dat een instelling een herstart nodig heeft.
func (s *Service) patroniSettings(ctx context.Context, j *jobs.Job, r *rendered, restart bool) error {
	slug := r.spec.Cluster.Slug
	if err := j.Step(ctx, "Instellingen in etcd", func(ctx context.Context, st *jobs.Step) error {
		nodes, err := s.pgNodes(ctx, r.comp.ClusterID, r.spec)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(nodes, func(p pgNode) bool { return p.err == nil && isPrimary(p.server, slug) })
		if i < 0 {
			return errors.New("er is geen primary om de instellingen aan te geven")
		}
		if err := patroniProtocol(nodes[i]); err != nil {
			if restart {
				return err
			}
			st.Logf("%v; de instellingen in etcd blijven zoals ze zijn", err)
			return nil
		}
		want := map[string]string{}
		for _, k := range protocol.DBPatroniParameters {
			if v, ok := r.spec.Params[k]; ok {
				want[k] = paramText(v)
			}
		}
		res, err := s.patroniOp(ctx, nodes[i].node, protocol.DBPatroniRequest{Action: protocol.DBPatroniConfig, Parameters: want})
		if err != nil {
			return err
		}
		st.Logf("Patroni op %s: %s", nodes[i].node.Hostname, res.Message)
		return nil
	}); err != nil {
		return err
	}
	return j.Step(ctx, "PostgreSQL herstarten waar nodig", func(ctx context.Context, st *jobs.Step) error {
		var done []uuid.UUID
		st.State(&done)
		nodes, err := s.pgNodes(ctx, r.comp.ClusterID, r.spec)
		if err != nil {
			return err
		}
		// De replica's eerst, de primary als laatste.
		slices.SortStableFunc(nodes, func(a, b pgNode) int {
			pa, pb := a.err == nil && isPrimary(a.server, slug), b.err == nil && isPrimary(b.server, slug)
			switch {
			case pa == pb:
				return a.node.Index - b.node.Index
			case pa:
				return 1
			}
			return -1
		})
		for _, p := range nodes {
			if slices.Contains(done, p.node.NodeID) {
				continue
			}
			if p.err != nil {
				return fmt.Errorf("%s: %w", p.node.Hostname, p.err)
			}
			if err := patroniProtocol(p); err != nil {
				if restart {
					return err
				}
				st.Logf("%v; ClusterForge herstart PostgreSQL daar niet", err)
				continue
			}
			if err := s.restartPG(ctx, st, p.node, slug, restart); err != nil {
				return err
			}
			done = append(done, p.node.NodeID)
			if err := st.SetState(ctx, done); err != nil {
				return err
			}
		}
		return nil
	})
}

// restartPG herstart PostgreSQL op n via Patroni en wacht tot hij weer
// meedoet zonder dat een instelling nog op een herstart wacht. Met always
// herstart hij ook als Patroni het niet nodig vindt; eerst wacht ClusterForge
// even tot Patroni de instellingen uit etcd gelezen heeft.
func (s *Service) restartPG(ctx context.Context, st *jobs.Step, n Node, slug string, always bool) error {
	if always {
		deadline := s.Now().Add(s.PendingWait)
		for {
			srv, err := s.server(ctx, n.NodeID, protocol.DBPostgreSQL)
			if err == nil && srv.Patroni != nil && srv.Patroni.PendingRestart {
				break
			}
			if s.Now().After(deadline) {
				st.Logf("Patroni op %s zegt niet dat een instelling op een herstart wacht; ClusterForge herstart toch", n.Hostname)
				break
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(s.Poll):
			}
		}
	}
	st.Logf("PostgreSQL op %s herstart", n.Hostname)
	_ = st.Flush(ctx)
	since := s.Now()
	res, err := s.patroniOp(ctx, n, protocol.DBPatroniRequest{Action: protocol.DBPatroniRestart, Pending: !always})
	if err != nil {
		return err
	}
	if res.Message != "" {
		st.Logf("Patroni: %s", res.Message)
	}
	if !res.Changed {
		return nil
	}
	restarted := false
	return s.waitServer(ctx, st, n.Hostname+" draait weer met de nieuwe instellingen", n.NodeID, protocol.DBPostgreSQL, func(srv *protocol.DBServer, err error) (string, bool) {
		if err != nil {
			restarted = true
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		}
		state, ok := patroniHealthy(srv, slug)
		if !ok || time.Duration(srv.UptimeSeconds)*time.Second < s.Now().Sub(since) {
			restarted = true
		}
		if ok && srv.Patroni.PendingRestart {
			state += ", wacht nog op een herstart"
			ok = false
		}
		return n.Hostname + ": " + state, ok && restarted
	})
}
