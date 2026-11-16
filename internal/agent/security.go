package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Beveiliging (fase 27): de agent geeft zijn pakketlijst; de server stelt
// de kwetsbaarheden vast. Alleen lezen.

func (a *Agent) onSecurity(m *nats.Msg) {
	go func() {
		res := a.handleSecurity(m.Data)
		data, err := envelope(protocol.TypeSecurityResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op beveiligingsverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleSecurity(data []byte) protocol.SecurityResponse {
	var env protocol.Envelope
	var req protocol.SecurityRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeSecurityRequest {
		return protocol.SecurityResponse{Error: "ongeldig beveiligingsverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.SecurityResponse{Error: "ongeldig beveiligingsverzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.SecurityResponse{Error: "beveiligingsverzoek verlopen; klopt de klok van deze node?"}
	}
	timeout := protocol.SecurityRequestTimeout - 5*time.Second
	if req.Op == protocol.SecurityOpScan || req.Op == protocol.SecurityOpProbe {
		timeout = protocol.MaxScanDuration
	}
	if !req.Deadline.IsZero() {
		if d := time.Until(req.Deadline) - time.Second; d < timeout {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	switch req.Op {
	case protocol.SecurityOpInventory:
		inv, err := a.inventory(ctx)
		if err != nil {
			return protocol.SecurityResponse{Error: err.Error()}
		}
		return protocol.SecurityResponse{Inventory: inv}
	case protocol.SecurityOpAudit:
		return protocol.SecurityResponse{Audit: a.audit(ctx)}
	case protocol.SecurityOpScan:
		res, err := a.scan(ctx, req.Scan)
		if err != nil {
			return protocol.SecurityResponse{Error: err.Error()}
		}
		return protocol.SecurityResponse{Scan: res}
	case protocol.SecurityOpProbe:
		res, err := a.probeServices(ctx, req.Probe)
		if err != nil {
			return protocol.SecurityResponse{Error: err.Error()}
		}
		return protocol.SecurityResponse{Probe: res}
	default:
		return protocol.SecurityResponse{Error: "onbekend beveiligingsverzoek: " + req.Op}
	}
}

// dpkgFormat geeft per pakket: status, naam, versie, architectuur, bron en
// bronversie. ${source:Package} en ${source:Version} vallen terug op het
// pakket zelf als er geen apart bronpakket is.
const dpkgFormat = "${db:Status-Abbrev}\t${Package}\t${Version}\t${Architecture}\t${source:Package}\t${source:Version}\n"

func (a *Agent) inventory(ctx context.Context) (*protocol.SecurityInventory, error) {
	c := a.Collector
	if c == nil {
		c = &Collector{Root: a.Root}
	}
	inv := &protocol.SecurityInventory{
		OS:       c.osRelease(),
		Kernel:   c.read("/proc/sys/kernel/osrelease"),
		Packages: []protocol.DebPackage{},
	}
	out, err := a.exec(ctx, "dpkg-query", "-W", "-f="+dpkgFormat)
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return inv, nil // geen dpkg: geen Debian
		}
		return nil, errors.New("dpkg-query mislukt: " + dpkgError(out, err))
	}
	inv.Dpkg = true
	inv.Packages, inv.Truncated = parseDpkg(out)
	return inv, nil
}

// parseDpkg leest de uitvoer van dpkg-query met dpkgFormat. Alleen
// geïnstalleerde pakketten (status ii, hi) tellen.
func parseDpkg(out []byte) ([]protocol.DebPackage, bool) {
	pkgs := []protocol.DebPackage{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 6 {
			continue
		}
		st := strings.TrimSpace(f[0])
		if len(st) < 2 || st[1] != 'i' {
			continue
		}
		p := protocol.DebPackage{Name: f[1], Version: f[2], Arch: f[3], Source: f[4], SourceVersion: f[5]}
		if p.Name == "" || p.Version == "" {
			continue
		}
		if p.Source == "" {
			p.Source = p.Name
		}
		if p.SourceVersion == "" {
			p.SourceVersion = p.Version
		}
		if len(pkgs) >= protocol.MaxSecurityPackages {
			return pkgs, true
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, false
}

func dpkgError(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return err.Error()
	}
	return s
}
