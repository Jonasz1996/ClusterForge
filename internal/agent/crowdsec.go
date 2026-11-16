package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// CrowdSec (fase 14). De agent leest CrowdSec met cscli en zet bans; hij
// installeert zelf niets: dat doet een run met het script van de server.

const (
	cscliPath   = "/usr/bin/cscli"
	bouncerPath = "/usr/bin/crowdsec-firewall-bouncer"
	capiFile    = "/etc/crowdsec/online_api_credentials.yaml"
	bouncerUnit = "crowdsec-firewall-bouncer"
	maxProbeHit = 20
)

func (a *Agent) onCrowdSec(m *nats.Msg) {
	go func() {
		res := a.handleCrowdSec(m.Data)
		data, err := envelope(protocol.TypeCrowdSecResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op CrowdSec-verzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleCrowdSec(data []byte) protocol.CrowdSecResponse {
	var env protocol.Envelope
	var req protocol.CrowdSecRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeCrowdSecRequest {
		return protocol.CrowdSecResponse{Error: "ongeldig CrowdSec-verzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.CrowdSecResponse{Error: "ongeldig CrowdSec-verzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.CrowdSecResponse{Error: "CrowdSec-verzoek verlopen; klopt de klok van deze node?"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.CrowdSecRequestTimeout)
	defer cancel()
	switch req.Op {
	case protocol.CrowdSecOpStatus:
		if len(req.Probes) > protocol.MaxCrowdSecProbes {
			return protocol.CrowdSecResponse{Error: "te veel globs"}
		}
		st := a.crowdSecStatus(ctx, req.Probes)
		return protocol.CrowdSecResponse{Status: &st}
	case protocol.CrowdSecOpPoll:
		if !a.crowdSecInstalled() {
			return protocol.CrowdSecResponse{Error: "CrowdSec staat niet op deze node"}
		}
		p, err := a.crowdSecPoll(ctx, req.Since)
		if err != nil {
			return protocol.CrowdSecResponse{Error: err.Error()}
		}
		return protocol.CrowdSecResponse{Poll: &p}
	case protocol.CrowdSecOpDecide:
		if req.Decide == nil {
			return protocol.CrowdSecResponse{Error: "decide zonder ban"}
		}
		if !a.crowdSecInstalled() {
			return protocol.CrowdSecResponse{Error: "CrowdSec staat niet op deze node"}
		}
		n, err := a.crowdSecDecide(ctx, *req.Decide)
		if err != nil {
			return protocol.CrowdSecResponse{Error: err.Error()}
		}
		return protocol.CrowdSecResponse{Count: n}
	case protocol.CrowdSecOpEnroll:
		if req.Enroll == nil {
			return protocol.CrowdSecResponse{Error: "enroll zonder sleutel"}
		}
		if !a.crowdSecInstalled() {
			return protocol.CrowdSecResponse{Error: "CrowdSec staat niet op deze node"}
		}
		if err := a.crowdSecEnroll(ctx, *req.Enroll); err != nil {
			return protocol.CrowdSecResponse{Error: err.Error()}
		}
		return protocol.CrowdSecResponse{Count: 1}
	}
	return protocol.CrowdSecResponse{Error: "onbekend CrowdSec-verzoek " + req.Op}
}

func (a *Agent) rootPath(p string) string {
	if a.Root == "" {
		return p
	}
	return filepath.Join(a.Root, p)
}

func (a *Agent) crowdSecInstalled() bool {
	_, err := os.Stat(a.rootPath(cscliPath))
	return err == nil
}

// stdout voert een programma uit en geeft alleen stdout terug (JSON); de
// fout heeft de laatste regel van stderr.
func (a *Agent) stdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if a.Exec != nil {
		return a.Exec(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		msg := strings.TrimSpace(string(ee.Stderr))
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if msg != "" {
			err = fmt.Errorf("%s: %s", name, msg)
		}
	}
	return out, err
}

func (a *Agent) crowdSecStatus(ctx context.Context, probes []string) protocol.CrowdSecStatus {
	st := protocol.CrowdSecStatus{Installed: a.crowdSecInstalled()}
	st.Probes = a.probeGlobs(probes)
	st.Containers = a.dockerContainers(ctx)
	if !st.Installed {
		return st
	}
	fail := func(what string, err error) { st.Errors = append(st.Errors, what+": "+err.Error()) }
	if out, err := a.stdout(ctx, "cscli", "version"); err == nil {
		st.Version = parseCscliVersion(string(out))
	} else {
		fail("cscli version", err)
	}
	st.Active = a.unitActive(ctx, "crowdsec")
	if _, err := os.Stat(a.rootPath(bouncerPath)); err == nil {
		st.Bouncer = a.unitActive(ctx, bouncerUnit)
	}
	if b, err := os.ReadFile(a.rootPath(capiFile)); err == nil && strings.Contains(string(b), "login:") {
		st.CAPI = true
	}
	if out, err := a.stdout(ctx, "cscli", "collections", "list", "-o", "json"); err == nil {
		if st.Collections, err = parseCscliItems(out, "collections"); err != nil {
			fail("cscli collections list", err)
		}
	} else {
		fail("cscli collections list", err)
	}
	if out, err := a.stdout(ctx, "cscli", "bouncers", "list", "-o", "json"); err == nil {
		if st.Bouncers, err = parseCscliBouncers(out); err != nil {
			fail("cscli bouncers list", err)
		}
	} else {
		fail("cscli bouncers list", err)
	}
	if out, err := a.stdout(ctx, "cscli", "metrics", "-o", "json"); err == nil {
		st.Sources = parseCscliSources(out)
	} else {
		fail("cscli metrics", err)
	}
	if b, err := os.ReadFile(a.rootPath(protocol.CrowdSecStateFile)); err == nil {
		var ap protocol.CrowdSecAppliedState
		if json.Unmarshal(b, &ap) == nil {
			st.Applied = &ap
		}
	}
	return st
}

func (a *Agent) unitActive(ctx context.Context, unit string) string {
	out, _ := a.stdout(ctx, "systemctl", "is-active", unit)
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	return s
}

// probeGlobs zegt per glob welke bestanden bestaan (hoogstens maxProbeHit).
func (a *Agent) probeGlobs(globs []string) map[string][]string {
	if len(globs) == 0 {
		return nil
	}
	res := map[string][]string{}
	for _, g := range globs {
		if !filepath.IsAbs(g) || strings.Contains(g, "..") {
			continue
		}
		m, _ := filepath.Glob(a.rootPath(g))
		hits := []string{}
		for _, f := range m {
			if len(hits) == maxProbeHit {
				break
			}
			if a.Root != "" {
				f = "/" + strings.TrimPrefix(strings.TrimPrefix(f, a.Root), "/")
			}
			hits = append(hits, f)
		}
		res[g] = hits
	}
	return res
}

// dockerContainers geeft de draaiende containers met image en mounts; leeg
// zonder Docker.
func (a *Agent) dockerContainers(ctx context.Context) []protocol.CrowdSecContainer {
	ids, err := a.stdout(ctx, "docker", "ps", "-q", "--no-trunc")
	if err != nil {
		return nil
	}
	args := append([]string{"inspect"}, strings.Fields(string(ids))...)
	if len(args) == 1 {
		return nil
	}
	out, err := a.stdout(ctx, "docker", args...)
	if err != nil {
		return nil
	}
	return parseDockerInspect(out)
}

func parseDockerInspect(out []byte) []protocol.CrowdSecContainer {
	var raw []struct {
		Name   string
		Config struct{ Image string }
		Mounts []struct{ Source, Destination string }
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	res := make([]protocol.CrowdSecContainer, 0, len(raw))
	for _, r := range raw {
		c := protocol.CrowdSecContainer{Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image}
		for _, m := range r.Mounts {
			if m.Source == "" || m.Destination == "" {
				continue
			}
			if c.Mounts == nil {
				c.Mounts = map[string]string{}
			}
			c.Mounts[m.Destination] = m.Source
		}
		res = append(res, c)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res
}

var cscliVersion = regexp.MustCompile(`(?m)\bversion:\s*(v?[0-9]+\.[0-9]+\.[0-9]+)`)

// parseCscliVersion haalt v1.6.4 uit "version: v1.6.4-debian-pragmatic-…".
func parseCscliVersion(out string) string {
	if m := cscliVersion.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

func parseCscliItems(out []byte, kind string) ([]protocol.CrowdSecItem, error) {
	var raw map[string][]struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		LocalVersion string `json:"local_version"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	res := []protocol.CrowdSecItem{}
	for _, r := range raw[kind] {
		res = append(res, protocol.CrowdSecItem{Name: r.Name, Status: r.Status, Version: r.LocalVersion})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

func parseCscliBouncers(out []byte) ([]protocol.CrowdSecBouncer, error) {
	if strings.TrimSpace(string(out)) == "null" {
		return nil, nil
	}
	var raw []struct {
		Name     string `json:"name"`
		IP       string `json:"ip_address"`
		Type     string `json:"type"`
		Version  string `json:"version"`
		Valid    *bool  `json:"valid"`
		Revoked  bool   `json:"revoked"`
		LastPull any    `json:"last_pull"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	res := make([]protocol.CrowdSecBouncer, 0, len(raw))
	for _, r := range raw {
		b := protocol.CrowdSecBouncer{Name: r.Name, IP: r.IP, Type: r.Type, Version: r.Version, Valid: !r.Revoked}
		if r.Valid != nil {
			b.Valid = *r.Valid
		}
		if s, ok := r.LastPull.(string); ok {
			b.LastPull, _ = time.Parse(time.RFC3339Nano, s)
		}
		res = append(res, b)
	}
	return res, nil
}

// parseCscliSources haalt de acquisitiecijfers uit cscli metrics -o json.
// De vorm wisselt per versie; alleen het blok acquisition telt.
func parseCscliSources(out []byte) []protocol.CrowdSecSource {
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	var acq map[string]map[string]any
	if json.Unmarshal(raw["acquisition"], &acq) != nil {
		return nil
	}
	res := []protocol.CrowdSecSource{}
	for src, m := range acq {
		res = append(res, protocol.CrowdSecSource{
			Source: src, Read: num(m["reads"]), Parsed: num(m["parsed"]),
			Unparsed: num(m["unparsed"]), Poured: num(m["pour"]),
		})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Source < res[j].Source })
	return res
}

// blocklistCount telt de bans van de Central API en lijsten in het blok
// decisions van cscli metrics: reden → origin → actie → aantal.
func blocklistCount(out []byte) int {
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil {
		return -1
	}
	var dec map[string]map[string]map[string]any
	if json.Unmarshal(raw["decisions"], &dec) != nil {
		return -1
	}
	n := int64(0)
	for _, origins := range dec {
		for origin, actions := range origins {
			if !isListOrigin(origin) {
				continue
			}
			for _, v := range actions {
				n += num(v)
			}
		}
	}
	return int(n)
}

func isListOrigin(o string) bool {
	switch strings.ToLower(o) {
	case "capi", "lists":
		return true
	}
	return false
}

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

type cscliAlert struct {
	ID          int64  `json:"id"`
	Scenario    string `json:"scenario"`
	Message     string `json:"message"`
	EventsCount int    `json:"events_count"`
	StartAt     string `json:"start_at"`
	StopAt      string `json:"stop_at"`
	Simulated   bool   `json:"simulated"`
	Source      struct {
		Scope  string `json:"scope"`
		Value  string `json:"value"`
		CN     string `json:"cn"`
		ASName string `json:"as_name"`
	} `json:"source"`
	Decisions []struct {
		ID       int64  `json:"id"`
		Origin   string `json:"origin"`
		Type     string `json:"type"`
		Scope    string `json:"scope"`
		Value    string `json:"value"`
		Scenario string `json:"scenario"`
		Duration string `json:"duration"`
	} `json:"decisions"`
}

func localScope(s string) bool {
	switch strings.ToLower(s) {
	case "ip", "range":
		return true
	}
	return false
}

func (a *Agent) crowdSecPoll(ctx context.Context, since time.Time) (protocol.CrowdSecPoll, error) {
	now := time.Now()
	if since.IsZero() || since.After(now) {
		since = now.Add(-time.Hour)
	}
	if now.Sub(since) > 30*24*time.Hour {
		since = now.Add(-30 * 24 * time.Hour)
	}
	mins := int(now.Sub(since)/time.Minute) + 1
	out, err := a.stdout(ctx, "cscli", "alerts", "list", "-o", "json",
		"--since", strconv.Itoa(mins)+"m", "--limit", strconv.Itoa(protocol.MaxCrowdSecAlerts))
	if err != nil {
		return protocol.CrowdSecPoll{}, fmt.Errorf("alerts lezen: %w", err)
	}
	p := protocol.CrowdSecPoll{Alerts: parseCscliAlerts(out), Blocklist: -1}
	out, err = a.stdout(ctx, "cscli", "decisions", "list", "-o", "json")
	if err != nil {
		return protocol.CrowdSecPoll{}, fmt.Errorf("bans lezen: %w", err)
	}
	p.Decisions = parseCscliDecisions(out, now)
	if out, err := a.stdout(ctx, "cscli", "metrics", "-o", "json"); err == nil {
		p.Blocklist = blocklistCount(out)
	}
	return p, nil
}

func parseCscliAlerts(out []byte) []protocol.CrowdSecAlert {
	var raw []cscliAlert
	if json.Unmarshal(out, &raw) != nil {
		return []protocol.CrowdSecAlert{}
	}
	res := []protocol.CrowdSecAlert{}
	for _, r := range raw {
		if !localScope(r.Source.Scope) {
			continue // community-blocklist en lijsten
		}
		list := false
		for _, d := range r.Decisions {
			list = list || isListOrigin(d.Origin)
		}
		if list {
			continue
		}
		al := protocol.CrowdSecAlert{
			ID: r.ID, Scenario: r.Scenario, Message: r.Message, Scope: strings.ToLower(r.Source.Scope),
			Value: r.Source.Value, Country: r.Source.CN, AS: r.Source.ASName, Events: r.EventsCount,
			Decisions: len(r.Decisions), Simulated: r.Simulated,
		}
		al.StartedAt, _ = time.Parse(time.RFC3339Nano, r.StartAt)
		al.StoppedAt, _ = time.Parse(time.RFC3339Nano, r.StopAt)
		res = append(res, al)
	}
	return res
}

func parseCscliDecisions(out []byte, now time.Time) []protocol.CrowdSecActive {
	var raw []cscliAlert
	if json.Unmarshal(out, &raw) != nil {
		return []protocol.CrowdSecActive{}
	}
	res := []protocol.CrowdSecActive{}
	for _, r := range raw {
		for _, d := range r.Decisions {
			if isListOrigin(d.Origin) {
				continue
			}
			dur, err := time.ParseDuration(d.Duration)
			if err != nil || dur <= 0 {
				continue
			}
			res = append(res, protocol.CrowdSecActive{
				ID: d.ID, Origin: d.Origin, Type: d.Type, Scope: strings.ToLower(d.Scope), Value: d.Value,
				Scenario: d.Scenario, Until: now.Add(dur).Truncate(time.Second),
				Country: r.Source.CN, AS: r.Source.ASName,
			})
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res
}

var deletedCount = regexp.MustCompile(`(\d+) decision`)

func (a *Agent) crowdSecDecide(ctx context.Context, d protocol.CrowdSecDecision) (int, error) {
	if err := d.Validate(); err != nil {
		return 0, err
	}
	flag := "--ip"
	if d.Scope == "range" {
		flag = "--range"
	}
	if d.Delete {
		out, err := a.stdout(ctx, "cscli", "decisions", "delete", flag, d.Value)
		if err != nil {
			return 0, fmt.Errorf("ban weghalen: %w", err)
		}
		if m := deletedCount.FindStringSubmatch(string(out)); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, nil
		}
		return 0, nil
	}
	typ := d.Type
	if typ == "" {
		typ = "ban"
	}
	args := []string{"decisions", "add", flag, d.Value, "--duration", d.Duration, "--type", typ}
	if d.Reason != "" {
		args = append(args, "--reason", d.Reason)
	}
	if _, err := a.stdout(ctx, "cscli", args...); err != nil {
		return 0, fmt.Errorf("ban zetten: %w", err)
	}
	return 1, nil
}

// crowdSecEnroll meldt de node aan bij de CrowdSec Console en herstart
// CrowdSec, zodat de aanmelding meteen doorgaat. De sleutel komt nooit in
// een foutmelding terug.
func (a *Agent) crowdSecEnroll(ctx context.Context, e protocol.CrowdSecEnroll) error {
	if err := e.Validate(); err != nil {
		return err
	}
	hide := func(err error) error {
		return errors.New(strings.ReplaceAll(err.Error(), e.Key, "…"))
	}
	args := []string{"console", "enroll", "--name", e.Name}
	if e.Overwrite {
		args = append(args, "--overwrite")
	}
	args = append(args, e.Key)
	if _, err := a.stdout(ctx, "cscli", args...); err != nil {
		return fmt.Errorf("aanmelden bij de Console: %w", hide(err))
	}
	if _, err := a.stdout(ctx, "systemctl", "restart", "crowdsec"); err != nil {
		return fmt.Errorf("aangemeld, maar CrowdSec herstarten mislukte: %w", err)
	}
	return nil
}
