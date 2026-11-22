package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/nmapxml"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Netwerkscan (fase 27, protocol 42): de agent draait nmap op adressen
// binnen de eigen scopes. Hij controleert zelf dat elk doel in Allowed valt,
// neemt alleen IP-adressen en CIDR's (geen hostnamen) en draait alleen de
// veilige scripts uit protocol.SafeScanScripts.

// checkScanSpec controleert een scan voor hij start.
func checkScanSpec(sp *protocol.ScanSpec) ([]string, []string, error) {
	if sp == nil || len(sp.Targets) == 0 {
		return nil, nil, errors.New("geen doelen voor de scan")
	}
	var allowed []netip.Prefix
	for _, a := range sp.Allowed {
		p, err := parseScanPrefix(a)
		if err != nil {
			return nil, nil, fmt.Errorf("ongeldige scope %q", a)
		}
		allowed = append(allowed, p)
	}
	if len(allowed) == 0 {
		return nil, nil, errors.New("geen scope meegegeven; de agent scant niets buiten een eigen scope")
	}
	var targets, exclude []string
	for _, t := range sp.Targets {
		p, err := parseScanPrefix(t)
		if err != nil {
			return nil, nil, fmt.Errorf("ongeldig doel %q: alleen IP-adressen en CIDR's", t)
		}
		if p.Addr().Is4() && p.Bits() < 16 || p.Addr().Is6() && p.Bits() < 112 {
			return nil, nil, fmt.Errorf("doel %s is te groot", p)
		}
		if !slices.ContainsFunc(allowed, func(a netip.Prefix) bool { return a.Bits() <= p.Bits() && a.Contains(p.Addr()) }) {
			return nil, nil, fmt.Errorf("doel %s valt buiten de scope; niet gescand", p)
		}
		targets = append(targets, p.String())
	}
	for _, e := range sp.Exclude {
		p, err := parseScanPrefix(e)
		if err != nil {
			return nil, nil, fmt.Errorf("ongeldige uitsluiting %q", e)
		}
		exclude = append(exclude, p.String())
	}
	return targets, exclude, nil
}

func parseScanPrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// nmapArgs bouwt de opdracht voor nmap; out is het XML-bestand.
func nmapArgs(sp *protocol.ScanSpec, targets, exclude []string, out string, root bool) []string {
	rate := sp.MaxRate
	if rate <= 0 || rate > 5000 {
		rate = 300
	}
	args := []string{"-oX", out, "-n", "--open", "--max-retries", "2", "--host-timeout", "30m", "--max-rate", strconv.Itoa(rate)}
	if !root {
		args = append(args, "-sT")
	}
	scripts := []string{"-sV", "--version-light", "--script", strings.Join(protocol.SafeScanScripts, ","), "--script-timeout", "60s"}
	switch sp.Profile {
	case protocol.ScanProfileQuick:
		args = append(args, "-T4", "--top-ports", "100")
	case protocol.ScanProfileFull:
		args = append(args, "-T3", "-p-")
		args = append(args, scripts...)
	default:
		args = append(args, "-T3", "--top-ports", "1000")
		args = append(args, scripts...)
	}
	if len(exclude) > 0 {
		args = append(args, "--exclude", strings.Join(exclude, ","))
	}
	return append(args, targets...)
}

func (a *Agent) scan(ctx context.Context, sp *protocol.ScanSpec) (*protocol.ScanResult, error) {
	targets, exclude, err := checkScanSpec(sp)
	if err != nil {
		return nil, err
	}
	ver, err := a.exec(ctx, "nmap", "--version")
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return nil, errors.New("nmap staat niet op deze node; gebruik de scannercontainer of installeer nmap")
		}
		return nil, errors.New("nmap --version mislukt: " + dpkgError(ver, err))
	}
	dir, err := os.MkdirTemp("", "cf-scan-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "scan.xml")
	args := nmapArgs(sp, targets, exclude, out, os.Geteuid() == 0)
	started := time.Now().UTC()
	timeout := protocol.MaxScanDuration
	if d, ok := ctx.Deadline(); ok {
		timeout = time.Until(d)
	}
	msg, err := a.execTimeout(ctx, timeout, "nmap", args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("scan duurde te lang en is gestopt")
		}
		return nil, errors.New("nmap mislukt: " + nmapError(msg, err))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, errors.New("nmap schreef geen resultaat: " + nmapError(msg, err))
	}
	res, err := parseNmapXML(data)
	if err != nil {
		return nil, err
	}
	res.Nmap = nmapVersion(ver)
	res.Args = args
	res.Started = started
	res.Finished = time.Now().UTC()
	return res, nil
}

func nmapVersion(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "Nmap" && f[1] == "version" {
			return f[2]
		}
	}
	return ""
}

func nmapError(out []byte, err error) string {
	if l := lastLine(string(out)); l != "" {
		return l
	}
	return err.Error()
}

// parseNmapXML leest de XML-uitvoer van nmap, met de grenzen uit protocol.
func parseNmapXML(data []byte) (*protocol.ScanResult, error) {
	res, _, err := nmapxml.Parse(data, nil)
	return res, err
}
