package agent

// Fase 32: cf-agent op een Proxmox Backup Server. Net als bij Proxmox VE
// (fase 26) geeft de agent de API door: lezen met proxmox-backup-debug api
// get, veranderen als run (pbs_api) die wacht op de PBS-taak en zijn log
// toont. De agent controleert de vorm; welke paden de GUI gebruikt, beslist
// de server (internal/pbs).

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
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const errNotPBS = pveError("dit is geen Proxmox Backup Server (proxmox-backup-manager of /etc/proxmox-backup ontbreekt)")

// isPBS zegt of dit een Proxmox Backup Server is.
func (h pveHost) isPBS() bool {
	found := false
	for _, p := range []string{"/usr/sbin/proxmox-backup-manager", "/usr/bin/proxmox-backup-manager"} {
		if _, err := os.Stat(h.path(p)); err == nil {
			found = true
		}
	}
	if !found {
		return false
	}
	fi, err := os.Stat(h.path("/etc/proxmox-backup"))
	return err == nil && fi.IsDir()
}

// pbsFacts beschrijft de PBS; nil op een andere machine.
func (h pveHost) pbsFacts(ctx context.Context) *protocol.PBSFacts {
	if !h.isPBS() {
		return nil
	}
	f := &protocol.PBSFacts{}
	if out, err := h.exec(ctx, "dpkg-query", "-W", "-f=${Version}", "proxmox-backup-server"); err == nil {
		f.Version = strings.TrimSpace(string(out))
	}
	name, _ := os.Hostname()
	f.Node, _, _ = strings.Cut(name, ".")
	f.Datastores = pbsDatastores(h.path("/etc/proxmox-backup/datastore.cfg"))
	return f
}

// pbsDatastores leest de namen uit datastore.cfg ("datastore: naam").
func pbsDatastores(file string) []string {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "datastore:"); ok {
			if n := strings.TrimSpace(rest); n != "" {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return out
}

func (c *Collector) pbs(ctx context.Context) *protocol.PBSFacts {
	return pveHost{root: c.Root, run: c.Run}.pbsFacts(ctx)
}

// pbsArgs bouwt de opties als --sleutel=waarde, zodat een waarde die met
// een streepje begint geen optie wordt.
func pbsArgs(params map[string]string) []string { return pveshArgs(params) }

// pbsGet is proxmox-backup-debug api get met de foutmelding als het misgaat.
func (h pveHost) pbsGet(ctx context.Context, path string, params map[string]string) (json.RawMessage, error) {
	if !h.isPBS() {
		return nil, errNotPBS
	}
	args := append([]string{"api", "get", path, "--output-format", "json"}, pbsArgs(params)...)
	var out []byte
	var err error
	if h.run != nil {
		out, err = h.run(ctx, "proxmox-backup-debug", args...)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "proxmox-backup-debug", args...)
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
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return json.RawMessage("null"), nil
	}
	if len(out) > pveGetMax {
		return nil, fmt.Errorf("antwoord van %s is te groot (%d kB)", path, len(out)>>10)
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("proxmox-backup-debug gaf geen JSON voor %s", path)
	}
	return out, nil
}

// Een UPID van PBS: UPID:node:pid:pstart:taskid:starttime:type:id:user:
var pbsUPIDRe = regexp.MustCompile(`UPID:[A-Za-z0-9-]+:[0-9A-F]{8}:[0-9A-F]{8,16}:[0-9A-F]{8,16}:[0-9A-F]{8}:[a-z0-9_-]+:[^:\s"]*:[^:\s"]+:`)

// pbsAPI voert een aanroep uit en wacht op de taak die PBS startte.
func (r *RunExecutor) pbsAPI(ctx context.Context, dir string, c *protocol.PVEAPICall, out io.Writer, st *protocol.RunStatus) (int, error) {
	defer r.forgetSecrets(dir, c)
	h := r.host()
	if !h.isPBS() {
		return -1, errNotPBS
	}
	var secrets []string
	for _, k := range c.Secret {
		secrets = append(secrets, c.Params[k])
	}
	shown := map[string]string{}
	for k, v := range c.Params {
		if slices.Contains(c.Secret, k) {
			v = "[geheim]"
		}
		shown[k] = v
	}
	argv := append([]string{"proxmox-backup-debug", "api", c.Method, c.Path, "--output-format", "json"}, pbsArgs(c.Params)...)
	_, _ = fmt.Fprintf(out, "\n$ %s\n", strings.Join(append([]string{"proxmox-backup-debug", "api", c.Method, c.Path}, pbsArgs(shown)...), " "))
	var buf bytes.Buffer
	sink := io.MultiWriter(out, &buf)
	if c.Quiet {
		sink = &buf
		_, _ = io.WriteString(out, "(antwoord verborgen: het bevat een geheim)\n")
	}
	sw := secretWriter{w: sink, secrets: secrets}
	code, err := r.cmd(ctx, "", argv, nil, sw)
	if err != nil {
		return code, err
	}
	text := buf.String()
	if code != 0 {
		return code, fmt.Errorf("proxmox-backup-debug api %s %s mislukte: %s", c.Method, c.Path, lastLines(strings.TrimSpace(text), 2))
	}
	res := &protocol.PVERunResult{}
	st.PVE = res
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if last := []byte(strings.TrimSpace(lines[len(lines)-1])); len(last) > 0 && len(last) <= 256<<10 && json.Valid(last) {
		res.Data = last
	}
	upid := pbsUPIDRe.FindString(text)
	if upid == "" {
		return 0, nil
	}
	res.UPID = upid
	return r.pbsWaitTask(ctx, upid, out)
}

// pbsWaitTask wacht tot een taak van PBS klaar is en toont zijn log.
func (r *RunExecutor) pbsWaitTask(ctx context.Context, upid string, out io.Writer) (int, error) {
	base := "/nodes/localhost/tasks/" + upid
	h := r.host()
	start := 0
	for {
		raw, err := h.pbsGet(ctx, base+"/status", nil)
		if err != nil {
			return -1, fmt.Errorf("de toestand van taak %s lezen mislukte: %w", upid, err)
		}
		var s struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		_ = json.Unmarshal(raw, &s)
		start = r.pbsTaskLog(ctx, base, start, out)
		if s.Status == "stopped" {
			switch {
			case s.ExitStatus == "OK":
				return 0, nil
			case strings.HasPrefix(s.ExitStatus, "WARNINGS"):
				_, _ = fmt.Fprintf(out, "PBS: %s\n", s.ExitStatus)
				return 0, nil
			}
			return 1, fmt.Errorf("de PBS-taak mislukte: %s", s.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *RunExecutor) pbsTaskLog(ctx context.Context, base string, start int, out io.Writer) int {
	raw, err := r.host().pbsGet(ctx, base+"/log", map[string]string{"start": fmt.Sprint(start), "limit": "500"})
	if err != nil {
		return start
	}
	var lines []struct {
		N int    `json:"n"`
		T string `json:"t"`
	}
	if json.Unmarshal(raw, &lines) != nil {
		return start
	}
	for _, l := range lines {
		if l.T == "no content" {
			continue
		}
		_, _ = fmt.Fprintln(out, l.T)
		if l.N > start {
			start = l.N
		}
	}
	return start
}
