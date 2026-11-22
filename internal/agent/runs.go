package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// runUnit is de transient unit van een run.
func runUnit(id string) string { return "cf-run-" + id + ".service" }

// runLost is hoe lang een run zonder unit nog als lopend telt: systemd-run
// kan net gestart zijn.
const runLost = 30 * time.Second

func (a *Agent) onRun(m *nats.Msg) {
	go func() {
		res := a.handleRun(m.Data)
		data, err := envelope(protocol.TypeRunResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op runverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleRun(data []byte) protocol.RunResponse {
	var env protocol.Envelope
	var req protocol.RunRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeRunRequest {
		return protocol.RunResponse{Error: "ongeldig runverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil || req.ID == "" {
		return protocol.RunResponse{Error: "ongeldig runverzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.RunResponse{Error: "runverzoek verlopen; klopt de klok van deze node?"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.RunRequestTimeout)
	defer cancel()
	switch req.Op {
	case protocol.RunStart:
		if req.Spec == nil {
			return protocol.RunResponse{Error: "start zonder spec"}
		}
		return a.runStart(ctx, *req.Spec)
	case protocol.RunPoll:
		return a.runPoll(ctx, req.RunID, req.Offset)
	case protocol.RunStop:
		return a.runStop(ctx, req.RunID)
	}
	return protocol.RunResponse{Error: "onbekend runverzoek " + req.Op}
}

func (a *Agent) runDir(id string) string { return runDir(a.Root, id) }

// runStart legt de run vast en start hem met systemd-run. Een run die er
// al is, start niet opnieuw.
func (a *Agent) runStart(ctx context.Context, spec protocol.RunSpec) protocol.RunResponse {
	if err := spec.Validate(); err != nil {
		return protocol.RunResponse{Error: err.Error()}
	}
	dir := a.runDir(spec.ID)
	if _, err := os.Stat(filepath.Join(dir, "spec.json")); err == nil {
		return a.runPoll(ctx, spec.ID, 0)
	}
	a.pruneRuns()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return protocol.RunResponse{Error: err.Error()}
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return protocol.RunResponse{Error: err.Error()}
	}
	st := protocol.RunStatus{State: protocol.RunRunning, StartedAt: time.Now().UTC()}
	if err := writeRunStatus(dir, st); err != nil {
		return protocol.RunResponse{Error: err.Error()}
	}
	if err := writeFileAtomic(filepath.Join(dir, "spec.json"), data, 0o600); err != nil {
		return protocol.RunResponse{Error: err.Error()}
	}
	if err := a.startRun(ctx, spec.ID); err != nil {
		end := time.Now().UTC()
		st.State, st.Error, st.EndedAt = protocol.RunFailed, "starten: "+err.Error(), &end
		_ = writeRunStatus(dir, st)
		return protocol.RunResponse{Error: st.Error, Status: &st}
	}
	return a.runPoll(ctx, spec.ID, 0)
}

// startRun start cf-agent run-exec als eigen unit, zodat een herstart van
// de agent (ook zijn eigen update) de run niet stopt.
func (a *Agent) startRun(ctx context.Context, id string) error {
	if a.StartRun != nil {
		return a.StartRun(ctx, id)
	}
	if hostOS == "freebsd" {
		return bsdStartDetached(ctx, a.exec, runUnit(id), protocol.VerifyPath, "run-exec", id)
	}
	out, err := a.exec(ctx, "systemd-run", "--unit", runUnit(id), "--collect", "--quiet",
		"--description", "ClusterForge-run "+id,
		"--property", "KillMode=mixed", "--property", "TimeoutStopSec=15",
		protocol.VerifyPath, "run-exec", id)
	if err != nil {
		return fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runPoll geeft de stand en de uitvoer vanaf offset.
func (a *Agent) runPoll(ctx context.Context, id string, offset int64) protocol.RunResponse {
	if !protocol.ValidRunID(id) {
		return protocol.RunResponse{Error: "ongeldig run-id"}
	}
	dir := a.runDir(id)
	st, err := readRunStatus(dir)
	if errors.Is(err, os.ErrNotExist) {
		return protocol.RunResponse{Error: "deze run staat niet (meer) op de node"}
	}
	if err != nil {
		return protocol.RunResponse{Error: err.Error()}
	}
	if st.State == protocol.RunRunning && time.Since(st.StartedAt) > runLost && !a.runLives(ctx, dir, id) {
		// De unit is weg zonder eindstand, bijvoorbeeld na een herstart van
		// de node.
		if again, err := readRunStatus(dir); err == nil && again.State == protocol.RunRunning {
			end := time.Now().UTC()
			st.State, st.Error, st.EndedAt = protocol.RunFailed, protocol.RunLostError, &end
			_ = writeRunStatus(dir, st)
			// Holds van een weggevallen apt-run niet laten staan: een
			// hervatte run zou ze anders als "al vastgehouden" laten.
			go a.releaseHolds(context.Background())
		} else if err == nil {
			st = again
		}
	}
	res := protocol.RunResponse{Status: &st, Offset: offset}
	f, err := os.Open(filepath.Join(dir, "output.log"))
	if err != nil {
		return res
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return res
	}
	size := fi.Size()
	if offset < 0 || offset > size {
		offset = size
	}
	if size-offset > protocol.RunChunk {
		// Een poll per seconde houdt dit bij; wie achterloopt, krijgt de
		// rest niet meer in één keer.
		offset, res.Skipped = size-protocol.RunChunk, true
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return res
	}
	buf, _ := io.ReadAll(io.LimitReader(f, size-offset))
	// Alleen hele regels, behalve aan het einde van een afgelopen run.
	if i := strings.LastIndexByte(string(buf), '\n'); i >= 0 && !st.Done() {
		buf = buf[:i+1]
	} else if i < 0 && !st.Done() {
		buf = nil
	}
	res.Output = protocol.MaskSecrets(string(buf))
	res.Offset = offset + int64(len(buf))
	return res
}

// runActive is true als de unit van de run nog bestaat. Alleen een
// duidelijk "inactive" of "failed" telt als weg: tijdens een upgrade van
// systemd (daemon-reexec) antwoordt systemctl even met een fout, en dan is
// de run nog niet dood.
func (a *Agent) runActive(ctx context.Context, id string) bool {
	if hostOS == "freebsd" {
		return oneShotActive(ctx, a.exec, runUnit(id))
	}
	out, _ := a.exec(ctx, "systemctl", "is-active", runUnit(id))
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	switch strings.TrimSpace(first) {
	case "inactive", "failed":
		return false
	}
	return true
}

// runLives zegt of de run nog loopt. Op Linux is het proces van run-exec
// de maat (systemctl kan tijdens een upgrade van systemd even niets
// zeggen); zonder PID-bestand beslist de unit.
func (a *Agent) runLives(ctx context.Context, dir, id string) bool {
	if hostOS != "freebsd" {
		if alive, known := a.runAlive(dir, id); known {
			return alive
		}
	}
	return a.runActive(ctx, id)
}

// runPIDFile is het bestand waarin run-exec zijn PID zet.
const runPIDFile = "pid"

// runAlive zegt of het run-exec-proces van de run nog leeft, los van wat
// systemd zegt; known is false zonder (geldig) PID-bestand.
func (a *Agent) runAlive(dir, id string) (alive, known bool) {
	data, err := os.ReadFile(filepath.Join(dir, runPIDFile))
	if err != nil {
		return false, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false, false
	}
	if _, err := os.Stat(a.procRoot()); err != nil {
		return false, false
	}
	cmdline, err := os.ReadFile(filepath.Join(a.procRoot(), strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false, true
	}
	args := strings.Split(string(cmdline), "\x00")
	return slices.Contains(args, "run-exec") && slices.Contains(args, id), true
}

// procRoot is /proc onder de bestandssysteemwortel van de agent.
func (a *Agent) procRoot() string {
	if a.Root == "" {
		return "/proc"
	}
	return filepath.Join(a.Root, "proc")
}

// runStop stopt de unit; run-exec krijgt SIGTERM en zet de stand op
// stopped.
func (a *Agent) runStop(ctx context.Context, id string) protocol.RunResponse {
	if !protocol.ValidRunID(id) {
		return protocol.RunResponse{Error: "ongeldig run-id"}
	}
	if a.StopRun != nil {
		if err := a.StopRun(ctx, id); err != nil {
			return protocol.RunResponse{Error: err.Error()}
		}
		return a.runPoll(ctx, id, 0)
	}
	if hostOS == "freebsd" {
		if err := bsdStopDetached(ctx, a.exec, runUnit(id)); err != nil && a.runActive(ctx, id) {
			return protocol.RunResponse{Error: err.Error()}
		}
		return a.runPoll(ctx, id, 0)
	}
	if out, err := a.exec(ctx, "systemctl", "stop", runUnit(id)); err != nil && a.runActive(ctx, id) {
		return protocol.RunResponse{Error: fmt.Sprintf("systemctl stop: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return a.runPoll(ctx, id, 0)
}

// pruneRuns houdt de nieuwste RunKeep afgelopen runs.
func (a *Agent) pruneRuns() {
	base := runDir(a.Root, "")
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	type done struct {
		dir string
		at  time.Time
	}
	var ended []done
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		st, err := readRunStatus(dir)
		if err != nil || !st.Done() || st.EndedAt == nil {
			continue
		}
		ended = append(ended, done{dir, *st.EndedAt})
	}
	if len(ended) < protocol.RunKeep {
		return
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].at.After(ended[j].at) })
	for _, d := range ended[protocol.RunKeep-1:] {
		_ = os.RemoveAll(d.dir)
	}
}

// releaseHolds haalt bij de start de holds weg die een apt-run achterliet,
// tenzij die run nog loopt.
func (a *Agent) releaseHolds(ctx context.Context) {
	p, err := a.path(protocol.RunHoldsFile)
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var h runHolds
	if err := json.Unmarshal(data, &h); err != nil || len(h.Packages) == 0 {
		_ = os.Remove(p)
		return
	}
	if protocol.ValidRunID(h.RunID) {
		if st, err := readRunStatus(a.runDir(h.RunID)); err == nil && !st.Done() && a.runActive(ctx, h.RunID) {
			return
		}
	}
	if out, err := a.execTimeout(ctx, 2*time.Minute, "apt-mark", append([]string{"unhold"}, h.Packages...)...); err != nil {
		a.Log.Warn("holds van een afgebroken update weghalen mislukt", "err", err, "output", strings.TrimSpace(string(out)))
		return
	}
	a.Log.Info("holds van een afgebroken update weggehaald", "packages", strings.Join(h.Packages, " "))
	_ = os.Remove(p)
}
