package opnsense

// HA en firmware (fase 34 M5): de CARP-onderhoudsmodus, een failovertest,
// firmware-updates in de juiste volgorde (eerst de backup, dan de master
// terwijl de backup het verkeer heeft) en drift tussen master en backup.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	KindCarp     = "opnsense.carp"
	KindFirmware = "opnsense.firmware"

	CarpOn   = "maintenance_on"
	CarpOff  = "maintenance_off"
	CarpTest = "failover_test"

	// TakeoverWithin is hoe lang de backup mag doen over het overnemen.
	TakeoverWithin = 90 * time.Second
	// FirmwareWithin is hoe lang een update mag duren, herstart inbegrepen.
	FirmwareWithin = 60 * time.Minute
)

// CarpInput is een vraag uit de GUI voor de onderhoudsmodus of de
// failovertest.
type CarpInput struct {
	Action string `json:"action"`
	// Confirm is de overgetikte naam van het cluster (aanzetten en test).
	Confirm string `json:"confirm"`
}

type carpParams struct {
	Cluster string      `json:"cluster"`
	Action  string      `json:"action"`
	NodeID  uuid.UUID   `json:"node_id"`
	Host    string      `json:"host"`
	Nodes   []uuid.UUID `json:"nodes"`
	Names   []string    `json:"names"`
}

func (p carpParams) name(id uuid.UUID) string {
	if i := slices.Index(p.Nodes, id); i >= 0 && i < len(p.Names) {
		return p.Names[i]
	}
	return id.String()
}

// StartCarp zet de onderhoudsmodus aan of uit op firewall id, of doet de
// failovertest op de master van zijn cluster.
func (s *Service) StartCarp(ctx context.Context, actor events.Actor, id uuid.UUID, in CarpInput) (store.Job, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if len(c.Nodes) < 2 {
		return store.Job{}, ValidationError{"dit is een losse firewall: zonder backup neemt niemand het verkeer over"}
	}
	for _, n := range c.Nodes {
		if n.Why != "" {
			return store.Job{}, ValidationError{n.Why}
		}
	}
	p := carpParams{Cluster: c.Name, Action: in.Action}
	for _, n := range c.Nodes {
		p.Nodes = append(p.Nodes, n.NodeID)
		p.Names = append(p.Names, n.Hostname)
	}
	var title string
	switch in.Action {
	case CarpOn, CarpOff:
		f, err := s.Firewall(ctx, id)
		if err != nil {
			return store.Job{}, err
		}
		p.NodeID, p.Host = f.NodeID, f.Hostname
		if in.Action == CarpOn {
			title = "Onderhoudsmodus aan op " + f.Hostname
		} else {
			title = "Onderhoudsmodus uit op " + f.Hostname
		}
	case CarpTest:
		ck := preflight(c)
		if !ck.OK {
			return store.Job{}, ValidationError{ck.Why}
		}
		if ck.Master.Facts.CARP.Maintenance {
			return store.Job{}, ValidationError{"de master staat al in onderhoudsmodus"}
		}
		p.NodeID, p.Host = ck.Master.NodeID, ck.Master.Hostname
		title = "Failovertest op " + c.Name
	default:
		return store.Job{}, ValidationError{"actie is maintenance_on, maintenance_off of failover_test"}
	}
	if in.Action != CarpOff && strings.TrimSpace(in.Confirm) != c.Name {
		return store.Job{}, ValidationError{fmt.Sprintf("het verkeer gaat even naar de andere firewall; tik %s over om te bevestigen", c.Name)}
	}
	j, err := s.enqueue(ctx, actor, c, KindCarp, title, p.NodeID, p)
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, p.NodeID, "opnsense.carp_requested", map[string]any{"cluster": c.Name, "firewall": p.Host, "action": in.Action, "job_id": j.ID.String()})
	return j, nil
}

func (s *Service) runCarp(ctx context.Context, j *jobs.Job) error {
	var p carpParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.cache.clear()
	var probes []string
	if err := j.Step(ctx, "Voorcontrole", func(ctx context.Context, st *jobs.Step) error {
		c, err := s.ClusterOf(ctx, p.NodeID)
		if err != nil {
			return err
		}
		for _, n := range c.Nodes {
			if n.Why != "" {
				return errors.New(n.Why)
			}
			st.Logf("%s: %s%s", n.Hostname, orDash(n.Role), maintNote(n.Facts))
		}
		probes = s.vipProbes(ctx, st, c)
		return nil
	}); err != nil {
		return err
	}
	switch p.Action {
	case CarpOn:
		_, err := s.takeover(ctx, j, p, probes)
		return err
	case CarpOff:
		_, err := s.giveBack(ctx, j, p, probes)
		return err
	}
	// Failovertest: overnemen, teruggeven, en zeggen hoe lang het duurde.
	over, err := s.takeover(ctx, j, p, probes)
	if err != nil {
		return err
	}
	back, err := s.giveBack(ctx, j, p, probes)
	if err != nil {
		return err
	}
	return j.Step(ctx, "Uitslag", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("de backup nam het verkeer over in %s en gaf het terug in %s", over.Round(time.Second), back.Round(time.Second))
		return nil
	})
}

func maintNote(f *protocol.OPNsenseFacts) string {
	if f != nil && f.CARP.Maintenance {
		return " (onderhoudsmodus)"
	}
	return ""
}

// vipProbes zijn de CARP-VIP's die nu antwoorden; na een overname moeten
// ze dat weer doen.
func (s *Service) vipProbes(ctx context.Context, st *jobs.Step, c Cluster) []string {
	var out []string
	for _, t := range probeTargets(Cluster{Nodes: vipOnly(c.Nodes)}) {
		if err := s.probe(ctx, t); err != nil {
			st.Logf("%s antwoordt nu al niet (%v); die proef telt niet mee", t, err)
			continue
		}
		out = append(out, t)
	}
	return out
}

// vipOnly geeft de nodes zonder hun eigen adres, zodat probeTargets alleen
// de VIP's geeft.
func vipOnly(nodes []Firewall) []Firewall {
	out := make([]Firewall, len(nodes))
	for i, n := range nodes {
		n.Address = ""
		out[i] = n
	}
	return out
}

// takeover zet de onderhoudsmodus aan op p.NodeID en wacht tot een andere
// firewall MASTER is voor alle VIP's en de VIP's weer antwoorden. Lukt dat
// niet, dan gaat de onderhoudsmodus meteen weer uit.
func (s *Service) takeover(ctx context.Context, j *jobs.Job, p carpParams, probes []string) (time.Duration, error) {
	if err := j.Step(ctx, "Onderhoudsmodus aan op "+p.Host, func(ctx context.Context, st *jobs.Step) error {
		return s.carp(ctx, st, p.NodeID, p.Host, true)
	}); err != nil {
		return 0, err
	}
	var took time.Duration
	err := j.Step(ctx, "Overname controleren", func(ctx context.Context, st *jobs.Step) error {
		d, roles, err := s.waitRoles(ctx, st, p, probes, s.takeoverWithin(), func(r map[uuid.UUID]string) bool {
			return r[p.NodeID] != "master" && countMasters(r, p.NodeID) == 1
		})
		took = d
		if err == nil {
			st.Logf("%s heeft het verkeer na %s", s.masterName(p, roles), d.Round(time.Second))
			return nil
		}
		st.Logf("overname mislukt (%v); onderhoudsmodus weer uit", err)
		if uerr := s.carp(ctx, st, p.NodeID, p.Host, false); uerr != nil {
			return fmt.Errorf("overname mislukt (%v) en de onderhoudsmodus uitzetten mislukte ook (%v): zet hem uit in OPNsense (Interfaces → Virtual IPs → Status)", err, uerr)
		}
		return fmt.Errorf("overname mislukt (%v); de onderhoudsmodus staat weer uit", err)
	})
	return took, err
}

// giveBack zet de onderhoudsmodus uit en wacht tot er precies één MASTER
// is en de VIP's antwoorden.
func (s *Service) giveBack(ctx context.Context, j *jobs.Job, p carpParams, probes []string) (time.Duration, error) {
	if err := j.Step(ctx, "Onderhoudsmodus uit op "+p.Host, func(ctx context.Context, st *jobs.Step) error {
		return s.carp(ctx, st, p.NodeID, p.Host, false)
	}); err != nil {
		return 0, err
	}
	var took time.Duration
	err := j.Step(ctx, "Rollen controleren", func(ctx context.Context, st *jobs.Step) error {
		d, roles, err := s.waitRoles(ctx, st, p, probes, s.takeoverWithin(), func(r map[uuid.UUID]string) bool {
			return countMasters(r, uuid.Nil) == 1
		})
		took = d
		if err != nil {
			return fmt.Errorf("na het uitzetten is er geen duidelijke master (%v); kijk Interfaces → Virtual IPs → Status", err)
		}
		st.Logf("%s is MASTER (na %s)", s.masterName(p, roles), d.Round(time.Second))
		if roles[p.NodeID] != "master" {
			st.Logf("%s nam het verkeer niet terug; dat klopt als preempt uit staat of zijn skew hoger is", p.Host)
		}
		return nil
	})
	return took, err
}

func (s *Service) carp(ctx context.Context, st *jobs.Step, node uuid.UUID, host string, on bool) error {
	res, err := s.do(ctx, st, st, node, protocol.PVERun{Action: protocol.OPNCarp, CARPMaintenance: &on}, "CARP-onderhoudsmodus "+onOff(on)+" op "+host)
	if err != nil {
		return err
	}
	if res.State != protocol.RunOK {
		return fmt.Errorf("onderhoudsmodus %s mislukte: %s", onOff(on), noderun.Describe(res))
	}
	return nil
}

func onOff(on bool) string {
	if on {
		return "aan"
	}
	return "uit"
}

// countMasters telt de nodes (behalve skip) die MASTER zijn voor alle
// VIP's; -1 als er een node gemengd is.
func countMasters(r map[uuid.UUID]string, skip uuid.UUID) int {
	n := 0
	for id, role := range r {
		if role == "mixed" {
			return -1
		}
		if id != skip && role == "master" {
			n++
		}
	}
	return n
}

func (s *Service) masterName(p carpParams, roles map[uuid.UUID]string) string {
	for id, r := range roles {
		if r == "master" {
			return p.name(id)
		}
	}
	return "niemand"
}

func (s *Service) takeoverWithin() time.Duration {
	if s.Takeover > 0 {
		return s.Takeover
	}
	return TakeoverWithin
}

func (s *Service) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 2 * time.Second
}

// waitRoles leest de CARP-rollen tot ok klopt en de VIP's antwoorden, of tot
// within voorbij is.
func (s *Service) waitRoles(ctx context.Context, st *jobs.Step, p carpParams, probes []string, within time.Duration, ok func(map[uuid.UUID]string) bool) (time.Duration, map[uuid.UUID]string, error) {
	start := s.Now()
	last := ""
	for {
		roles := map[uuid.UUID]string{}
		why := ""
		for _, id := range p.Nodes {
			f, err := s.facts(ctx, id)
			if err != nil {
				why = p.name(id) + " antwoordt niet: " + err.Error()
				break
			}
			roles[id] = f.Role()
		}
		if why == "" {
			if !ok(roles) {
				why = rolesText(p, roles)
			} else {
				for _, t := range probes {
					if err := s.probe(ctx, t); err != nil {
						why = fmt.Sprintf("%s antwoordt nog niet (%v)", t, err)
						break
					}
				}
			}
		}
		took := s.Now().Sub(start)
		if why == "" {
			return took, roles, nil
		}
		if why != last {
			st.Logf("nog niet: %s", why)
			last = why
		}
		if took >= within {
			return took, roles, errors.New(why)
		}
		select {
		case <-ctx.Done():
			return took, roles, ctx.Err()
		case <-time.After(s.poll()):
		}
	}
}

func rolesText(p carpParams, r map[uuid.UUID]string) string {
	var parts []string
	for _, id := range p.Nodes {
		parts = append(parts, p.name(id)+" "+orDash(r[id]))
	}
	return strings.Join(parts, ", ")
}

// --- Firmware ----------------------------------------------------------------

// FirmwareInput start een controle, update of upgrade op het hele cluster.
type FirmwareInput struct {
	// Mode is check, update of upgrade.
	Mode    string `json:"mode"`
	Confirm string `json:"confirm"`
}

type firmwareParams struct {
	Cluster string      `json:"cluster"`
	Mode    string      `json:"mode"`
	Master  uuid.UUID   `json:"master"`
	Nodes   []uuid.UUID `json:"nodes"`
	Names   []string    `json:"names"`
}

// StartFirmware zoekt naar updates of werkt het cluster bij: eerst elke
// backup, dan de master terwijl die in onderhoudsmodus staat.
func (s *Service) StartFirmware(ctx context.Context, actor events.Actor, id uuid.UUID, in FirmwareInput) (store.Job, error) {
	switch in.Mode {
	case "check", "update", "upgrade":
	default:
		return store.Job{}, ValidationError{"mode is check, update of upgrade"}
	}
	ck, err := s.Preflight(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if !ck.OK {
		return store.Job{}, ValidationError{ck.Why}
	}
	if in.Mode != "check" && strings.TrimSpace(in.Confirm) != ck.Cluster.Name {
		return store.Job{}, ValidationError{fmt.Sprintf("de firewalls herstarten mogelijk; tik %s over om te bevestigen", ck.Cluster.Name)}
	}
	p := firmwareParams{Cluster: ck.Cluster.Name, Mode: in.Mode, Master: ck.Master.NodeID}
	// Eerst de backups, de master als laatste.
	for _, b := range ck.Backup {
		p.Nodes = append(p.Nodes, b.NodeID)
		p.Names = append(p.Names, b.Hostname)
	}
	p.Nodes = append(p.Nodes, ck.Master.NodeID)
	p.Names = append(p.Names, ck.Master.Hostname)
	title := map[string]string{"check": "Zoeken naar updates op ", "update": "Firmware bijwerken op ", "upgrade": "Firmware-upgrade op "}[in.Mode] + ck.Cluster.Name
	j, err := s.enqueue(ctx, actor, ck.Cluster, KindFirmware, title, ck.Master.NodeID, p)
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, ck.Master.NodeID, "opnsense.firmware_requested", map[string]any{"cluster": ck.Cluster.Name, "mode": in.Mode, "job_id": j.ID.String()})
	return j, nil
}

// fwStatus is het deel van core/firmware/status dat ClusterForge leest.
type fwStatus struct {
	Status    string          `json:"status"`
	StatusMsg string          `json:"status_msg"`
	Updates   json.RawMessage `json:"updates"`
	Upgrade   string          `json:"upgrade_major_version"`
}

// fwRunning leest core/firmware/upgradestatus: running, done, reboot of
// error.
func (s *Service) fwRunning(ctx context.Context, id uuid.UUID) (string, error) {
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpGet, Path: "core/firmware/upgradestatus"}, false)
	if err != nil {
		return "", err
	}
	var st struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(data, &st)
	return st.Status, nil
}

func (s *Service) runFirmware(ctx context.Context, j *jobs.Job) error {
	var p firmwareParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.cache.clear()
	cp := carpParams{Cluster: p.Cluster, Nodes: p.Nodes, Names: p.Names, NodeID: p.Master}
	cp.Host = cp.name(p.Master)
	if p.Mode == "check" {
		for _, id := range p.Nodes {
			if err := j.Step(ctx, "Zoeken naar updates op "+cp.name(id), func(ctx context.Context, st *jobs.Step) error {
				return s.fwCheck(ctx, st, id, cp.name(id))
			}); err != nil {
				return err
			}
		}
		return nil
	}
	var probes []string
	if err := j.Step(ctx, "Voorcontrole", func(ctx context.Context, st *jobs.Step) error {
		c, err := s.ClusterOf(ctx, p.Master)
		if err != nil {
			return err
		}
		ck := preflight(c)
		if !ck.OK {
			return errors.New(ck.Why)
		}
		if ck.Master.NodeID != p.Master {
			return fmt.Errorf("de master is intussen %s; start opnieuw", ck.Master.Hostname)
		}
		for _, n := range c.Nodes {
			st.Logf("%s: OPNsense %s, %s", n.Hostname, n.Facts.Version, orDash(n.Role))
		}
		if len(c.Nodes) == 1 {
			st.Logf("losse firewall: tijdens de herstart is er geen verkeer")
		}
		probes = s.vipProbes(ctx, st, c)
		return nil
	}); err != nil {
		return err
	}
	for _, id := range p.Nodes {
		if id == p.Master {
			continue
		}
		if err := s.fwUpdate(ctx, j, id, cp.name(id), p.Mode); err != nil {
			return fmt.Errorf("%v; de master is niet aangeraakt", err)
		}
	}
	if len(p.Nodes) == 1 {
		return s.fwUpdate(ctx, j, p.Master, cp.Host, p.Mode)
	}
	if _, err := s.takeover(ctx, j, cp, probes); err != nil {
		return err
	}
	if err := s.fwUpdate(ctx, j, p.Master, cp.Host, p.Mode); err != nil {
		return fmt.Errorf("%v; %s staat nog in onderhoudsmodus, de backup heeft het verkeer", err, cp.Host)
	}
	_, err := s.giveBack(ctx, j, cp, probes)
	return err
}

// fwCheck zoekt naar updates en zegt wat er is.
func (s *Service) fwCheck(ctx context.Context, st *jobs.Step, id uuid.UUID, host string) error {
	res, err := s.do(ctx, st, st, id, protocol.PVERun{Action: protocol.OPNFirmware, Firmware: "check"}, "Zoeken naar updates op "+host)
	if err != nil {
		return err
	}
	if res.State != protocol.RunOK {
		return fmt.Errorf("zoeken mislukte: %s", noderun.Describe(res))
	}
	deadline := s.Now().Add(5 * time.Minute)
	for {
		status, err := s.fwRunning(ctx, id)
		if err == nil && status != "running" {
			break
		}
		if s.Now().After(deadline) {
			return errors.New("de controle duurt langer dan 5 minuten")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.poll()):
		}
	}
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpGet, Path: "core/firmware/status"}, false)
	if err != nil {
		return err
	}
	var fs fwStatus
	_ = json.Unmarshal(data, &fs)
	st.Logf("%s", orDash(fs.StatusMsg))
	if fs.Upgrade != "" {
		st.Logf("upgrade naar %s mogelijk", fs.Upgrade)
	}
	return nil
}

// fwUpdate werkt één firewall bij en wacht tot hij terug is.
func (s *Service) fwUpdate(ctx context.Context, j *jobs.Job, id uuid.UUID, host, mode string) error {
	return j.Step(ctx, "Bijwerken: "+host, func(ctx context.Context, st *jobs.Step) error {
		before, err := s.facts(ctx, id)
		if err != nil {
			return fmt.Errorf("de agent op %s antwoordt niet: %w", host, err)
		}
		res, err := s.do(ctx, st, st, id, protocol.PVERun{Action: protocol.OPNFirmware, Firmware: mode}, "Firmware "+mode+" op "+host)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return fmt.Errorf("de update starten mislukte op %s: %s", host, noderun.Describe(res))
		}
		st.Logf("update loopt; %s herstart mogelijk", host)
		_ = st.Flush(ctx)
		start := s.Now()
		sawDown, sawReboot := false, false
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.poll() * 5):
			}
			status, err := s.fwRunning(ctx, id)
			switch {
			case err != nil:
				if !sawDown {
					st.Logf("%s is weg (herstart)", host)
				}
				sawDown = true
			case status == "error":
				return fmt.Errorf("de update meldt een fout op %s: kijk System → Firmware → Status", host)
			case status == "reboot":
				if !sawReboot {
					st.Logf("%s herstart", host)
				}
				sawReboot = true
			}
			done := err == nil && (status == "done" || (sawDown && status != "running"))
			if !done && sawReboot && !sawDown && s.Now().Sub(start) > 10*time.Minute {
				done = true
			}
			if done {
				break
			}
			if s.Now().Sub(start) > FirmwareWithin {
				return fmt.Errorf("%s is na %s nog niet klaar; kijk de console van de firewall", host, FirmwareWithin)
			}
		}
		after, err := s.facts(ctx, id)
		if err != nil {
			return fmt.Errorf("%s antwoordt niet na de update: %w", host, err)
		}
		if after.Version == before.Version {
			st.Logf("%s blijft op %s (niets bij te werken)", host, after.Version)
		} else {
			st.Logf("%s: %s → %s", host, before.Version, after.Version)
		}
		return nil
	})
}

// --- Drift -------------------------------------------------------------------

// DriftRow zegt welke onderdelen van de configuratie op een backup anders
// zijn dan op de master.
type DriftRow struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	// Sections zijn de onderdelen die verschillen.
	Sections []string `json:"sections"`
	Error    string   `json:"error,omitempty"`
}

// Drift vergelijkt de master met elke backup.
type Drift struct {
	Master string     `json:"master"`
	Rows   []DriftRow `json:"rows"`
	// SyncItems zijn de onderdelen die de master synchroniseert.
	SyncItems []string `json:"sync_items"`
	// Own zijn de onderdelen die op elke firewall anders horen te zijn en
	// niet meetellen.
	Own []string `json:"own"`
}

// ownSections verschillen altijd tussen master en backup.
var ownSections = []string{"interfaces", "system", "hasync", "opnsense/hasync", "revision", "lastchange", "version", "ppps", "wireless", "bridges", "gifs", "gres", "laggs", "opnsense/dashboard", "widgets"}

// Drift vergelijkt de configuratie van de master met die van elke backup.
func (s *Service) Drift(ctx context.Context, id uuid.UUID) (Drift, error) {
	ck, err := s.Preflight(ctx, id)
	if err != nil {
		return Drift{}, err
	}
	if !ck.OK {
		return Drift{}, ValidationError{ck.Why}
	}
	d := Drift{Master: ck.Master.Hostname, Rows: []DriftRow{}, SyncItems: ck.Master.Facts.HA.SyncItems, Own: ownSections}
	if d.SyncItems == nil {
		d.SyncItems = []string{}
	}
	if len(ck.Backup) == 0 {
		return d, nil
	}
	mtext, err := s.revision(ctx, ck.Master.NodeID, "current")
	if err != nil {
		return Drift{}, err
	}
	master := configSections(mtext)
	for _, b := range ck.Backup {
		row := DriftRow{NodeID: b.NodeID, Hostname: b.Hostname, Sections: []string{}}
		btext, err := s.revision(ctx, b.NodeID, "current")
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Sections = driftSections(master, configSections(btext))
		}
		d.Rows = append(d.Rows, row)
	}
	return d, nil
}

func driftSections(a, b map[string]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range []map[string]string{a, b} {
		for k := range m {
			if seen[k] || slices.Contains(ownSections, strings.ToLower(k)) {
				continue
			}
			seen[k] = true
			if a[k] != b[k] {
				out = append(out, k)
			}
		}
	}
	slices.Sort(out)
	return out
}

// configSections splitst config.xml in zijn onderdelen: de kinderen van de
// wortel, en onder <OPNsense> de kinderen daarvan (OPNsense/Firewall ...).
// Elk onderdeel is genormaliseerd: zonder revisie, hostnaam en CARP-skew,
// die per firewall verschillen.
func configSections(text string) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = false
	depth := 0
	var top string
	var start int64
	for {
		off := dec.InputOffset()
		tok, err := dec.RawToken()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 2:
				top, start = t.Name.Local, off
			case depth == 3 && top == "OPNsense":
				start = off
				top = "OPNsense/" + t.Name.Local
			}
		case xml.EndElement:
			end := dec.InputOffset()
			switch {
			case depth == 3 && strings.HasPrefix(top, "OPNsense/"):
				out[top] = normSection(text[start:end])
				top = "OPNsense"
			case depth == 2 && top != "OPNsense":
				out[top] = normSection(text[start:end])
			}
			depth--
		}
	}
	return out
}

var advskew = regexp.MustCompile(`<advskew>[^<]*</advskew>`)

func normSection(s string) string {
	s = stripVolatile(advskew.ReplaceAllString(s, ""))
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}
