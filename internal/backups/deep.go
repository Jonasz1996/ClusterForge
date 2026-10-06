package backups

// De diepe controle: cf-agent verify in de sandbox, gestart via de guest
// agent. De aanvraag zegt welke services er moeten draaien, op welke poorten
// en adressen op loopback, en of PostgreSQL of MariaDB gecontroleerd wordt.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// CodeAgentTooOld staat bij de controle als de back-up geen cf-agent heeft
// die verify kent; de webinterface toont dan de stappen om dat te regelen.
const CodeAgentTooOld = "agent_too_old"

// CodeExecForbidden staat bij de controle als het API-token in de sandbox
// niets mag starten via de guest agent.
const CodeExecForbidden = "exec_forbidden"

// nameVerify is de controle die zegt dat de diepe controle niet liep.
const nameVerify = "Diepe controle"

// notExpected zijn units uit de facts die op een VM niets zeggen.
var notExpected = []string{"pve-cluster", "pveproxy"}

// verifyRequest bouwt de aanvraag: de services uit de gewenste staat en uit
// de laatste facts van de productienode, hun poorten en de HTTP-controles
// van de template, omgezet naar loopback. notes zijn regels voor het log.
func (s *Service) verifyRequest(ctx context.Context, clusterID *uuid.UUID, nodeID uuid.UUID) (protocol.VerifyRequest, []string, error) {
	req := protocol.VerifyRequest{Services: []string{}}
	var notes []string
	add := func(name string) {
		if !slices.Contains(req.Services, name) && !slices.Contains(notExpected, name) && len(req.Services) < 64 {
			req.Services = append(req.Services, name)
		}
	}
	if clusterID != nil && s.Running != nil {
		r, err := s.Running(ctx, *clusterID, nodeID)
		switch {
		case errors.Is(err, deploy.ErrNoSpec):
		case err != nil:
			notes = append(notes, "de gewenste staat is niet te lezen; alleen de facts tellen: "+err.Error())
		default:
			for _, name := range r.Services {
				add(name)
			}
			for _, p := range r.Ports {
				req.TCP = append(req.TCP, protocol.VerifyTCP{Host: "127.0.0.1", Port: p.Port, Service: p.Unit})
			}
			for _, h := range r.HTTP {
				u, ok := loopbackURL(h.URL)
				if !ok {
					notes = append(notes, "HTTP-controle "+h.URL+" niet in de sandbox: alleen http gaat naar loopback")
					continue
				}
				req.HTTP = append(req.HTTP, protocol.VerifyHTTP{URL: u, Expect: h.Expect})
			}
		}
	}
	f, err := s.q.GetNodeFacts(ctx, nodeID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notes = append(notes, "de productienode heeft nog geen facts gemeld")
	case err != nil:
		return req, notes, err
	default:
		var facts protocol.Facts
		if err := json.Unmarshal(f.Facts, &facts); err != nil {
			return req, notes, fmt.Errorf("facts van de productienode: %w", err)
		}
		for _, svc := range facts.Services {
			if svc.Active == "active" {
				add(svc.Name)
			}
		}
	}
	for _, name := range req.Services {
		switch {
		case strings.HasPrefix(name, "postgresql"):
			req.PostgreSQL = true
		case name == "mariadb" || name == "mysql":
			req.MariaDB = true
		}
	}
	return req, notes, nil
}

// loopbackURL zet een HTTP-controle om naar loopback, met dezelfde poort,
// hetzelfde pad en dezelfde query.
func loopbackURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return "", false
	}
	host := "127.0.0.1"
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	u.Host, u.User = host, nil
	return u.String(), true
}

// describe zegt in één regel wat de aanvraag vraagt.
func describe(req protocol.VerifyRequest) string {
	var parts []string
	if len(req.Services) > 0 {
		parts = append(parts, "services "+strings.Join(req.Services, ", "))
	}
	for _, t := range req.TCP {
		parts = append(parts, fmt.Sprintf("poort %d", t.Port))
	}
	for _, h := range req.HTTP {
		parts = append(parts, h.URL)
	}
	if req.PostgreSQL {
		parts = append(parts, "PostgreSQL")
	}
	if req.MariaDB {
		parts = append(parts, "MariaDB")
	}
	if len(parts) == 0 {
		return "alleen gefaalde units"
	}
	return strings.Join(parts, "; ")
}

// deep start cf-agent verify in de sandbox en zet de uitkomst in de
// controles. Lukt dat niet, dan is dat een waarschuwing: de back-up zelf is
// dan nog steeds teruggezet en gestart.
func (v *verify) deep(ctx context.Context, st *jobs.Step) {
	s := v.s
	if v.run.NodeID == nil {
		return
	}
	req, notes, err := s.verifyRequest(ctx, v.run.ClusterID, *v.run.NodeID)
	for _, n := range notes {
		st.Logf("%s", n)
	}
	if err != nil {
		v.addCheck(Check{Name: nameVerify, Warning: true, Detail: "de aanvraag voor cf-agent verify is niet te maken: " + err.Error()})
		return
	}
	body, err := json.Marshal(req)
	if err != nil {
		v.addCheck(Check{Name: nameVerify, Warning: true, Detail: err.Error()})
		return
	}
	st.Logf("cf-agent verify: %s", describe(req))
	_ = st.Flush(ctx)
	vm, err := s.guard(ctx, v.sb.ID)
	if err != nil {
		v.addCheck(Check{Name: nameVerify, Warning: true, Detail: err.Error()})
		return
	}
	t0 := s.Now()
	pid, err := vm.runVerify(ctx, body)
	if err != nil {
		if ctx.Err() == nil {
			v.addCheck(startProblem(err))
		}
		return
	}
	st.Logf("cf-agent verify loopt in de sandbox (pid %d)", pid)
	_ = st.Flush(ctx)
	wait := s.VerifyWait
	if wait == 0 {
		wait = protocol.VerifyLimit + 30*time.Second
	}
	var status proxmox.ExecStatus
	for {
		sctx, cancel := context.WithTimeout(ctx, s.AgentTimeout)
		status, err = vm.execStatus(sctx, pid)
		cancel()
		if err == nil && bool(status.Exited) {
			break
		}
		if ctx.Err() != nil {
			return
		}
		if s.Now().Sub(t0) > wait {
			v.addCheck(Check{Name: nameVerify, Warning: true, Detail: "cf-agent verify gaf na " + duration(wait) + " geen antwoord"})
			return
		}
		if err := sleep(ctx, s.Poll); err != nil {
			return
		}
	}
	d := s.Now().Sub(t0)
	var res protocol.VerifyResult
	if jerr := json.Unmarshal([]byte(status.OutData), &res); jerr != nil || res.ProtocolVersion == 0 {
		v.addCheck(tooOld(status))
		return
	}
	v.m.AgentVersion = res.AgentVersion
	if res.Error != "" {
		v.addCheck(Check{Name: nameVerify, Warning: true, Detail: "cf-agent " + res.AgentVersion + " weigerde de aanvraag: " + res.Error})
		return
	}
	failed := 0
	for _, c := range res.Checks {
		ch := deepCheck(c)
		if !ch.OK && !ch.Warning {
			failed++
		}
		switch c.Kind {
		case "service":
			v.m.ServicesExpected++
			if c.Status == protocol.VerifyOK {
				v.m.ServicesActive++
			}
		case "postgresql", "mariadb":
			if c.Status == protocol.VerifyOK {
				v.m.Databases = append(v.m.Databases, fmt.Sprintf("%s met %s", ch.Name, count(c.Count, "database", "databases")))
			}
		}
		v.addCheck(ch)
	}
	st.Logf("cf-agent %s antwoordde na %s: %s, %d mislukt", res.AgentVersion, duration(d),
		count(len(res.Checks), "controle", "controles"), failed)
	v.event(fmt.Sprintf("cf-agent verify klaar: %s", count(len(res.Checks), "controle", "controles")))
}

// deepCheck zet een controle van de agent om naar het rapport.
func deepCheck(c protocol.VerifyCheck) Check {
	name := c.Name
	switch c.Kind {
	case "service":
		name = "Service " + c.Name
	case "units":
		name = "Gefaalde units"
	case "tcp":
		name = "Poort " + c.Name
	case "http":
		name = "HTTP " + c.Name
	}
	out := Check{Name: name, Detail: c.Detail}
	switch c.Status {
	case protocol.VerifyOK:
		out.OK = true
	case protocol.VerifyFail:
	default:
		out.Warning = true
	}
	return out
}

// startProblem zegt waarom cf-agent verify niet startte.
func startProblem(err error) Check {
	var pe *proxmox.Error
	if errors.As(err, &pe) && pe.Code == 403 {
		return Check{Name: nameVerify, Warning: true, Code: CodeExecForbidden, Detail: "het API-token mag in de sandbox niets starten via de guest agent; " +
			"geef het VM.GuestAgent.Unrestricted op /pool/" + proxmox.SandboxPool + " (Proxmox 9) of VM.Monitor (Proxmox 8), zoals in de README"}
	}
	if strings.Contains(err.Error(), "No such file or directory") || strings.Contains(err.Error(), "Failed to execute") {
		return Check{Name: nameVerify, Warning: true, Code: CodeAgentTooOld,
			Detail: "er staat geen cf-agent in deze back-up (" + protocol.VerifyPath + " ontbreekt); alleen terugzetten, starten en de guest agent zijn gecontroleerd"}
	}
	return Check{Name: nameVerify, Warning: true, Detail: "cf-agent verify starten mislukte: " + err.Error()}
}

// tooOld is het antwoord van een agent zonder verify: exitcode 2 en geen
// JSON.
func tooOld(st proxmox.ExecStatus) Check {
	if st.Signal != 0 {
		return Check{Name: nameVerify, Warning: true, Detail: fmt.Sprintf("cf-agent verify stopte door signaal %d", st.Signal)}
	}
	return Check{Name: nameVerify, Warning: true, Code: CodeAgentTooOld, Detail: fmt.Sprintf(
		"cf-agent in deze back-up kent verify nog niet (exitcode %d); alleen terugzetten, starten en de guest agent zijn gecontroleerd", st.ExitCode)}
}
