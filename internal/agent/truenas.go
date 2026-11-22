package agent

// Fase 37: cf-agent op TrueNAS SCALE. Net als bij PBS geeft de agent de
// API door, hier de middleware via midclt (lokale socket, als root, zonder
// sleutel). Lezen gaat alleen met leesmethodes; veranderen is een run
// truenas_call die op een taak van de middleware wacht. Welke methodes de
// GUI gebruikt, beslist de server (internal/truenas).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const errNotTrueNAS = pveError("dit is geen TrueNAS SCALE (midclt of /etc/version ontbreekt)")

// trueNASHome is waar install.sh de agent op TrueNAS zet: /data blijft bij
// een update van TrueNAS.
const trueNASHome = "/data/clusterforge"

// isTrueNAS zegt of dit TrueNAS SCALE is.
func (h pveHost) isTrueNAS() bool {
	if _, err := os.Stat(h.path("/usr/bin/midclt")); err != nil {
		return false
	}
	if _, err := os.Stat(h.path("/data/freenas-v1.db")); err != nil {
		return false
	}
	return true
}

// trueNASFacts beschrijft TrueNAS; nil op een andere machine.
func (h pveHost) trueNASFacts(ctx context.Context) *protocol.TrueNASFacts {
	if !h.isTrueNAS() {
		return nil
	}
	f := &protocol.TrueNASFacts{}
	if data, err := os.ReadFile(h.path("/etc/version")); err == nil {
		f.Version = strings.TrimSpace(string(data))
	}
	name, _ := os.Hostname()
	f.Hostname, _, _ = strings.Cut(name, ".")
	if out, err := h.exec(ctx, "zpool", "list", "-H", "-o", "name,health"); err == nil {
		f.Pools = parseZpoolHealth(string(out))
	}
	if _, err := os.Stat(h.path(filepath.Join(trueNASHome, "bin/cf-agent"))); err == nil {
		f.Persistent = true
	}
	return f
}

// parseZpoolHealth leest zpool list -H -o name,health, zonder boot-pool.
func parseZpoolHealth(out string) []protocol.TrueNASPool {
	var pools []protocol.TrueNASPool
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		name, health, ok := strings.Cut(sc.Text(), "\t")
		if !ok || name == "boot-pool" || name == "freenas-boot" {
			continue
		}
		pools = append(pools, protocol.TrueNASPool{Name: name, Health: strings.TrimSpace(health)})
	}
	return pools
}

func (c *Collector) trueNAS(ctx context.Context) *protocol.TrueNASFacts {
	return pveHost{root: c.Root, run: c.Run}.trueNASFacts(ctx)
}

// trueNASArgv bouwt de argumenten van midclt call: elk element van de
// JSON-lijst wordt één argument.
func trueNASArgv(method string, args json.RawMessage, job bool) ([]string, error) {
	argv := []string{"call"}
	if job {
		argv = append(argv, "-job", "-jp", "description")
	}
	argv = append(argv, method)
	if len(args) > 0 {
		var list []json.RawMessage
		if err := json.Unmarshal(args, &list); err != nil {
			return nil, fmt.Errorf("argumenten zijn geen JSON-lijst")
		}
		for _, a := range list {
			argv = append(argv, string(a))
		}
	}
	return argv, nil
}

// trueNASResult maakt van de uitvoer van midclt JSON: een tekst of getal
// drukt midclt af zonder aanhalingstekens.
func trueNASResult(out []byte) json.RawMessage {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(out) {
		return out
	}
	data, _ := json.Marshal(string(out))
	return data
}

// trueNASGet voert een leesmethode uit.
func (h pveHost) trueNASGet(ctx context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
	if !h.isTrueNAS() {
		return nil, errNotTrueNAS
	}
	if !protocol.TrueNASReadMethod(method) {
		return nil, fmt.Errorf("%s is geen leesmethode", method)
	}
	argv, err := trueNASArgv(method, args, false)
	if err != nil {
		return nil, err
	}
	var out []byte
	if h.run != nil {
		out, err = h.run(ctx, "midclt", argv...)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "midclt", argv...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err = cmd.Output()
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = errors.New(lastLines(msg, 3))
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(out) > pveGetMax {
		return nil, fmt.Errorf("antwoord van %s is te groot (%d kB)", method, len(out)>>10)
	}
	return trueNASResult(out), nil
}

// trueNASCall voert een methode uit als run en wacht op de taak.
func (r *RunExecutor) trueNASCall(ctx context.Context, dir string, c *protocol.TrueNASMethodCall, out io.Writer, st *protocol.RunStatus) (int, error) {
	defer r.forgetTrueNASArgs(dir, c)
	if !r.host().isTrueNAS() {
		return -1, errNotTrueNAS
	}
	argv, err := trueNASArgv(c.Method, c.Args, c.Job)
	if err != nil {
		return -1, err
	}
	if c.Secret {
		_, _ = fmt.Fprintf(out, "\n$ midclt call %s [argumenten verborgen]\n", c.Method)
	} else {
		_, _ = fmt.Fprintf(out, "\n$ midclt %s\n", strings.Join(argv, " "))
	}
	var buf bytes.Buffer
	sink := io.MultiWriter(out, &buf)
	if c.Secret {
		sink = &buf
	}
	code, err := r.cmd(ctx, "", append([]string{"midclt"}, argv...), []string{"LC_ALL=C"}, sink)
	if err != nil {
		return code, err
	}
	text := strings.TrimSpace(buf.String())
	if code != 0 {
		return code, fmt.Errorf("midclt call %s mislukte: %s", c.Method, lastLines(text, 3))
	}
	res := &protocol.PVERunResult{}
	st.PVE = res
	// Het resultaat is de laatste regel (de voortgang staat erboven).
	lines := strings.Split(text, "\n")
	if last := []byte(strings.TrimSpace(lines[len(lines)-1])); len(last) > 0 && len(last) <= 256<<10 && !c.Secret {
		res.Data = trueNASResult(last)
	}
	return 0, nil
}

// forgetTrueNASArgs haalt de argumenten met een geheim uit spec.json.
func (r *RunExecutor) forgetTrueNASArgs(dir string, c *protocol.TrueNASMethodCall) {
	if !c.Secret {
		return
	}
	file := filepath.Join(dir, "spec.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var spec protocol.RunSpec
	if json.Unmarshal(data, &spec) != nil || spec.PVE == nil || spec.PVE.TrueNAS == nil {
		return
	}
	spec.PVE.TrueNAS.Args = json.RawMessage(`["verwijderd"]`)
	if data, err := json.Marshal(spec); err == nil {
		_ = writeFileAtomic(file, data, 0o600)
	}
}
