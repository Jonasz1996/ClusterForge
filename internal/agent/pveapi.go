package agent

// Fase 26: de API van Proxmox via pvesh. Lezen is een verzoek (get), iets
// veranderen een run (api) die wacht op de Proxmox-taak en zijn log
// toont. De agent controleert de vorm; welke paden de GUI gebruikt, beslist
// de server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// pveGetMax is de grootste uitvoer van pvesh get die over NATS gaat.
const pveGetMax = 900 << 10

// pveshArgs bouwt de opties als --sleutel=waarde, zodat een waarde die met
// een streepje begint geen optie wordt.
func pveshArgs(params map[string]string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, "--"+k+"="+params[k])
	}
	return out
}

// get is pvesh get met de foutmelding van pvesh als het misgaat.
func (h pveHost) get(ctx context.Context, path string, params map[string]string) (json.RawMessage, error) {
	if !h.isHost() {
		return nil, errNotPVE
	}
	args := append([]string{"get", path, "--output-format", "json"}, pveshArgs(params)...)
	var out []byte
	var err error
	if h.run != nil {
		out, err = h.run(ctx, "pvesh", args...)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "pvesh", args...)
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
		return nil, fmt.Errorf("pvesh gaf geen JSON voor %s", path)
	}
	return out, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " ")
}

var upidRe = regexp.MustCompile(`UPID:[A-Za-z0-9-]+:[0-9A-F]{8}:[0-9A-F]{8}:[0-9A-F]{8}:[a-z0-9_-]+:[^:\s"]*:[^:\s"]+:`)

// secretWriter vervangt geheime waarden in de uitvoer.
type secretWriter struct {
	w       io.Writer
	secrets []string
}

func (s secretWriter) Write(p []byte) (int, error) {
	text := string(p)
	for _, v := range s.secrets {
		if len(v) >= 3 {
			text = strings.ReplaceAll(text, v, "[geheim]")
		}
	}
	if _, err := io.WriteString(s.w, text); err != nil {
		return 0, err
	}
	return len(p), nil
}

// pveAPI voert een aanroep uit en wacht op de taak die Proxmox startte.
func (r *RunExecutor) pveAPI(ctx context.Context, dir string, c *protocol.PVEAPICall, out io.Writer, st *protocol.RunStatus) (int, error) {
	defer r.forgetSecrets(dir, c)
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
	argv := append([]string{"pvesh", c.Method, c.Path, "--output-format", "json"}, pveshArgs(c.Params)...)
	_, _ = fmt.Fprintf(out, "\n$ %s\n", strings.Join(append([]string{"pvesh", c.Method, c.Path}, pveshArgs(shown)...), " "))
	var buf bytes.Buffer
	sink := io.MultiWriter(out, &buf)
	if c.Quiet {
		// Het antwoord bevat een geheim: alleen in RunStatus, nooit in de uitvoer.
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
		// Mislukt is er geen geheim gemaakt; de foutmelding mag mee.
		return code, fmt.Errorf("pvesh %s %s mislukte: %s", c.Method, c.Path, lastLines(strings.TrimSpace(text), 2))
	}
	res := &protocol.PVERunResult{}
	st.PVE = res
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if last := []byte(strings.TrimSpace(lines[len(lines)-1])); len(last) > 0 && len(last) <= 256<<10 && json.Valid(last) {
		res.Data = last
	}
	upid := upidRe.FindString(text)
	if upid == "" {
		return 0, nil
	}
	res.UPID = upid
	return r.pveWaitTask(ctx, upid, len(lines) <= 1, out)
}

// pveWaitTask wacht tot een taak klaar is. Draaide hij op de achtergrond
// (de uitvoer was alleen de UPID), dan volgt zijn log.
func (r *RunExecutor) pveWaitTask(ctx context.Context, upid string, showLog bool, out io.Writer) (int, error) {
	node := strings.Split(upid, ":")[1]
	base := "/nodes/" + node + "/tasks/" + upid
	h := r.host()
	start := 0
	for {
		raw, err := h.get(ctx, base+"/status", nil)
		if err != nil {
			return -1, fmt.Errorf("de toestand van taak %s lezen mislukte: %w", upid, err)
		}
		var s struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		_ = json.Unmarshal(raw, &s)
		if showLog {
			start = r.pveTaskLog(ctx, base, start, out)
		}
		if s.Status == "stopped" {
			switch {
			case s.ExitStatus == "OK":
				return 0, nil
			case strings.HasPrefix(s.ExitStatus, "WARNINGS"):
				_, _ = fmt.Fprintf(out, "Proxmox: %s\n", s.ExitStatus)
				return 0, nil
			}
			return 1, fmt.Errorf("de Proxmox-taak mislukte: %s", s.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *RunExecutor) pveTaskLog(ctx context.Context, base string, start int, out io.Writer) int {
	raw, err := r.host().get(ctx, base+"/log", map[string]string{"start": fmt.Sprint(start), "limit": "500"})
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

// forgetSecrets haalt de geheime velden uit spec.json van de run.
func (r *RunExecutor) forgetSecrets(dir string, c *protocol.PVEAPICall) {
	if len(c.Secret) == 0 {
		return
	}
	file := filepath.Join(dir, "spec.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var spec protocol.RunSpec
	if json.Unmarshal(data, &spec) != nil || spec.PVE == nil || spec.PVE.API == nil {
		return
	}
	for _, k := range spec.PVE.API.Secret {
		spec.PVE.API.Params[k] = "verwijderd"
	}
	if data, err := json.Marshal(spec); err == nil {
		_ = writeFileAtomic(file, data, 0o600)
	}
}

// pveHostAction voert de acties van fase 26 uit die niet over een gast
// gaan.
func (r *RunExecutor) pveHostAction(ctx context.Context, dir string, p *protocol.PVERun, out io.Writer, st *protocol.RunStatus) (int, error) {
	switch p.Action {
	case protocol.PVEAPI:
		return r.pveAPI(ctx, dir, p.API, out, st)
	case protocol.PVEPassthrough:
		return r.pvePassthrough(ctx, dir, p.Passthrough, out, st)
	case protocol.PVEVolumeFree:
		return r.step(ctx, out, nil, "pvesm", "free", p.Volume)
	case protocol.PVEZpoolScrub:
		if p.Scrub.Stop {
			return r.step(ctx, out, nil, "zpool", "scrub", "-s", p.Scrub.Pool)
		}
		return r.step(ctx, out, nil, "zpool", "scrub", p.Scrub.Pool)
	case protocol.PVEMaintenance:
		node := r.host().members().Nodename
		if node == "" {
			return -1, errors.New("de naam van deze host in Proxmox is onbekend (/etc/pve/.members)")
		}
		mode := "disable"
		if p.Enable {
			mode = "enable"
		}
		return r.step(ctx, out, nil, "ha-manager", "crm-command", "node-maintenance", mode, node)
	}
	return -1, fmt.Errorf("onbekende actie %q", p.Action)
}
