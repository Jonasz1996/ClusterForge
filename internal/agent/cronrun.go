package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// CronRunner draait één run van een beheerde job: cf-agent cron-run <id>,
// gestart door de timer of door CronRun. Het legt de run vast in de
// wachtrij die de agent naar de server stuurt.
type CronRunner struct {
	// Root is de bestandssysteemwortel; leeg is "/". Tests gebruiken een map.
	Root string
	// Addresses geeft de adressen van de node; nil leest de interfaces.
	Addresses func() []string
	// Now is de klok; nil is time.Now.
	Now func() time.Time
	// Stderr krijgt wat cron-run zelf meldt; nil is os.Stderr.
	Stderr io.Writer
}

func (r *CronRunner) path(p string) string {
	if r.Root == "" {
		return p
	}
	return filepath.Join(r.Root, p)
}

func (r *CronRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *CronRunner) logf(format string, args ...any) {
	w := r.Stderr
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

// Main draait de job en geeft de exitcode van de run terug, zodat systemd
// een mislukte run ook als mislukt toont.
func (r *CronRunner) Main(ctx context.Context, id string) int {
	if _, err := uuid.Parse(id); err != nil {
		r.logf("ongeldige job-id %q", id)
		return 2
	}
	data, err := os.ReadFile(r.path(path.Join(protocol.CronJobDir, id+".json")))
	if err != nil {
		r.logf("job %s staat niet op deze node: %v", id, err)
		return 2
	}
	var job protocol.ManagedCron
	if err := json.Unmarshal(data, &job); err != nil {
		r.logf("job %s is onleesbaar: %v", id, err)
		return 2
	}
	reason := protocol.CronReasonSchedule
	if err := os.Remove(r.path(path.Join(protocol.CronStateDir, "manual", id))); err == nil {
		reason = protocol.CronReasonManual
	}
	if reason == protocol.CronReasonSchedule && job.RunOn == protocol.CronOnVIP && !r.hasVIP(job.VIPs) {
		// Een andere node heeft het VIP; die draait de job.
		return 0
	}
	run := protocol.CronRunRecord{ID: uuid.NewString(), JobID: id, Revision: job.Revision, Reason: reason, StartedAt: r.now().UTC()}
	unlock, err := cronLock(r.path(path.Join(protocol.CronStateDir, "locks", id+".lock")))
	if err != nil {
		end := run.StartedAt
		run.EndedAt, run.Result = &end, protocol.CronSkipped
		run.Output = "De vorige run liep nog; deze run sloeg over."
		r.spool(run)
		r.logf("vorige run loopt nog; overgeslagen")
		return 0
	}
	defer unlock()

	outDir := r.path(path.Join(protocol.CronStateDir, "output", id))
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		r.logf("map voor de uitvoer: %v", err)
		return 1
	}
	code := 0
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			run = protocol.CronRunRecord{ID: uuid.NewString(), JobID: id, Revision: job.Revision, Reason: protocol.CronReasonRetry, StartedAt: r.now().UTC()}
		}
		code = r.attempt(ctx, job, &run, outDir)
		retry := run.Result == protocol.CronFailed || run.Result == protocol.CronTimedOut
		if !retry || attempt >= min(job.Retries, 3) || ctx.Err() != nil {
			break
		}
		delay := time.Duration(job.RetryDelaySeconds) * time.Second
		if delay <= 0 {
			delay = time.Minute
		}
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		if ctx.Err() != nil {
			break
		}
	}
	pruneOutputs(outDir, protocol.CronKeepOutputs)
	return code
}

// attempt draait het commando één keer en legt de run vast.
func (r *CronRunner) attempt(ctx context.Context, job protocol.ManagedCron, run *protocol.CronRunRecord, outDir string) int {
	outPath := filepath.Join(outDir, fmt.Sprintf("%d-%s.log", run.StartedAt.UnixNano(), run.ID))
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		r.logf("uitvoer: %v", err)
		return 1
	}
	run.Result = protocol.CronRunning
	r.spool(*run)

	timeout := time.Duration(job.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	exit, runErr := runCronCommand(rctx, job.User, job.Command, out)
	timedOut := errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	stopped := ctx.Err() != nil
	cancel()
	if runErr != nil {
		_, _ = fmt.Fprintf(out, "\nClusterForge: %v\n", runErr)
	}
	switch {
	case timedOut:
		_, _ = fmt.Fprintf(out, "\nClusterForge: gestopt na de tijdslimiet van %s\n", timeout)
	case stopped:
		_, _ = fmt.Fprintf(out, "\nClusterForge: met de hand gestopt\n")
	}
	_ = out.Close()

	end := r.now().UTC()
	run.EndedAt, run.Exit = &end, &exit
	switch {
	case timedOut:
		run.Result = protocol.CronTimedOut
	case stopped:
		run.Result = protocol.CronStopped
	case exit == 0 && runErr == nil:
		run.Result = protocol.CronOK
	default:
		run.Result = protocol.CronFailed
	}
	run.Output, run.OutputTruncated = tailFile(outPath, protocol.CronOutputMax)
	run.Output = protocol.MaskSecrets(run.Output)
	r.spool(*run)
	if exit == 0 && run.Result != protocol.CronOK {
		return 1
	}
	return exit
}

func (r *CronRunner) hasVIP(vips []string) bool {
	addrs := hostAddresses()
	if r.Addresses != nil {
		addrs = r.Addresses()
	}
	return slices.ContainsFunc(vips, func(v string) bool { return slices.Contains(addrs, v) })
}

// spool zet de run in de wachtrij; de nieuwste stand vervangt de vorige.
func (r *CronRunner) spool(run protocol.CronRunRecord) {
	dir := r.path(path.Join(protocol.CronStateDir, "spool"))
	data, _ := json.Marshal(run)
	if err := writeFileAtomic(filepath.Join(dir, run.ID+".json"), data, 0o600); err != nil {
		r.logf("run in de wachtrij zetten: %v", err)
	}
	// Een wachtrij die nooit leeg raakt (de server neemt niets aan), houdt
	// de nieuwste duizend runs.
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) > 1000 {
		sort.Slice(files, func(i, j int) bool { return modTime(files[i]).Before(modTime(files[j])) })
		for _, f := range files[:len(files)-1000] {
			_ = os.Remove(f)
			_ = os.Remove(strings.TrimSuffix(f, ".json") + ".sent")
		}
	}
}

func modTime(p string) time.Time {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// tailFile geeft de laatste max bytes van een bestand, vanaf een hele regel.
func tailFile(p string, max int64) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", false
	}
	if fi.Size() <= max {
		data, _ := io.ReadAll(f)
		return string(data), false
	}
	if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
		return "", false
	}
	data, _ := io.ReadAll(f)
	if i := strings.IndexByte(string(data), '\n'); i >= 0 && i < len(data)-1 {
		data = data[i+1:]
	}
	return string(data), true
}

// pruneOutputs houdt de nieuwste keep uitvoeren; de namen beginnen met de
// starttijd.
func pruneOutputs(dir string, keep int) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) <= keep {
		return
	}
	sort.Strings(files)
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(f)
	}
}
