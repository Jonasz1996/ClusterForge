package agent

// Beheerde cronjobs (fase 6, agentprotocol 18). De server stuurt met
// CronApply de hele set jobs van de node; de agent schrijft per job
// /etc/clusterforge/cron/<id>.json en een timer cf-cron-<id>.timer die de
// gedeelde service cf-cron@<id>.service start. Die draait cf-agent cron-run
// <id> (zie CronRunner), dat de run in een wachtrij zet; cronRunsLoop stuurt
// die wachtrij naar de server, met een Ack.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// cronUser is een gebruikersnaam zoals useradd die toelaat.
var cronUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// cronServiceUnit is de gedeelde service van de beheerde jobs.
const cronServiceUnit = "cf-cron@.service"

func cronTimerUnit(id string) string { return "cf-cron-" + id + ".timer" }

func cronServiceContent() string {
	return "# Beheerd door ClusterForge; wijzigingen gaan verloren.\n" +
		"[Unit]\nDescription=ClusterForge-cronjob %i\nAfter=network-online.target\n\n" +
		"[Service]\nType=oneshot\nExecStart=" + protocol.VerifyPath + " cron-run %i\nKillMode=mixed\nTimeoutStopSec=30\n"
}

func cronTimerContent(j protocol.ManagedCron) string {
	return "# Beheerd door ClusterForge; wijzigingen gaan verloren.\n" +
		"[Unit]\nDescription=ClusterForge-cronjob " + j.Name + "\n\n" +
		"[Timer]\nOnCalendar=" + j.OnCalendar + "\nPersistent=true\nUnit=cf-cron@" + j.ID + ".service\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

func validManagedCron(j protocol.ManagedCron) error {
	if _, err := uuid.Parse(j.ID); err != nil || strings.ToLower(j.ID) != j.ID {
		return fmt.Errorf("ongeldige job-id %q", j.ID)
	}
	switch {
	case strings.ContainsAny(j.Name, "\n\r") || strings.ContainsAny(j.OnCalendar, "\n\r") || j.OnCalendar == "":
		return fmt.Errorf("job %s: ongeldige naam of schema", j.ID)
	case !cronUser.MatchString(j.User):
		return fmt.Errorf("job %s: ongeldige gebruiker %q", j.ID, j.User)
	case j.Kind == protocol.CronKindSync:
		if err := validSync(j); err != nil {
			return err
		}
	case j.Kind != "" && j.Kind != protocol.CronKindCommand:
		return fmt.Errorf("job %s: onbekende soort %q", j.ID, j.Kind)
	case strings.TrimSpace(j.Command) == "":
		return fmt.Errorf("job %s: geen commando", j.ID)
	}
	switch {
	case j.RunOn != protocol.CronOnVIP && j.RunOn != protocol.CronOnNode && j.RunOn != protocol.CronOnAll:
		return fmt.Errorf("job %s: onbekend run_on %q", j.ID, j.RunOn)
	}
	return nil
}

// cronApply zet de set beheerde jobs van de node, en wat hij voor syncs
// nodig heeft.
func (a *Agent) cronApply(ctx context.Context, jobs []protocol.ManagedCron, receives []protocol.SyncReceive) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	fail := func(err error) protocol.CronResponse {
		res.Error = err.Error()
		return res
	}
	want := map[string]protocol.ManagedCron{}
	for _, j := range jobs {
		if err := validManagedCron(j); err != nil {
			return fail(err)
		}
		want[j.ID] = j
	}
	jobDir, err := a.path(protocol.CronJobDir)
	if err != nil {
		return fail(err)
	}
	unitDir, err := a.path(protocol.CronUnitDir)
	if err != nil {
		return fail(err)
	}
	systemctl := func(args ...string) error {
		if out, err := a.exec(ctx, "systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// Jobs die er niet meer bij horen: timer stoppen en weghalen.
	var stale []string
	timers, _ := filepath.Glob(filepath.Join(unitDir, "cf-cron-*.timer"))
	for _, t := range timers {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(t), "cf-cron-"), ".timer")
		if _, ok := want[id]; !ok {
			stale = append(stale, id)
		}
	}
	files, _ := filepath.Glob(filepath.Join(jobDir, "*.json"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, ok := want[id]; !ok && !slices.Contains(stale, id) {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	reload := false
	for _, id := range stale {
		t := filepath.Join(unitDir, cronTimerUnit(id))
		if _, err := os.Stat(t); err == nil {
			if err := systemctl("disable", "--now", cronTimerUnit(id)); err != nil {
				return fail(err)
			}
			if err := os.Remove(t); err != nil {
				return fail(err)
			}
			reload = true
		}
		if err := os.Remove(filepath.Join(jobDir, id+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		res.Output = append(res.Output, "job "+id+" weggehaald")
	}

	// De service en de jobs zelf; alleen wat verandert, wordt geschreven.
	write := func(p string, data []byte, perm os.FileMode) (bool, error) {
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
			return false, nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return false, err
		}
		return true, writeFileAtomic(p, data, perm)
	}
	svc := filepath.Join(unitDir, cronServiceUnit)
	if len(want) > 0 {
		changed, err := write(svc, []byte(cronServiceContent()), 0o644)
		if err != nil {
			return fail(err)
		}
		reload = reload || changed
	} else if _, err := os.Stat(svc); err == nil {
		if err := os.Remove(svc); err != nil {
			return fail(err)
		}
		reload = true
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var changedJobs []string
	for _, id := range ids {
		j := want[id]
		data, _ := json.MarshalIndent(j, "", "  ")
		c1, err := write(filepath.Join(jobDir, id+".json"), append(data, '\n'), 0o600)
		if err != nil {
			return fail(err)
		}
		c2, err := write(filepath.Join(unitDir, cronTimerUnit(id)), []byte(cronTimerContent(j)), 0o644)
		if err != nil {
			return fail(err)
		}
		if c1 || c2 {
			changedJobs = append(changedJobs, id)
		}
		reload = reload || c2
	}
	if reload {
		if err := systemctl("daemon-reload"); err != nil {
			return fail(err)
		}
	}
	for _, id := range changedJobs {
		j := want[id]
		if j.Enabled {
			if err := systemctl("enable", cronTimerUnit(id)); err != nil {
				return fail(err)
			}
			// restart rekent de volgende run met het nieuwe schema.
			if err := systemctl("restart", cronTimerUnit(id)); err != nil {
				return fail(err)
			}
			res.Output = append(res.Output, fmt.Sprintf("job %s (%s) staat aan: %s", j.Name, id, j.OnCalendar))
		} else {
			if err := systemctl("disable", "--now", cronTimerUnit(id)); err != nil {
				return fail(err)
			}
			res.Output = append(res.Output, fmt.Sprintf("job %s (%s) staat uit", j.Name, id))
		}
	}
	syncOut, err := a.syncState(jobs, receives)
	if err != nil {
		return fail(err)
	}
	res.Output = append(res.Output, syncOut...)
	res.Hash = protocol.CronApplyHash(jobs, receives)
	return res
}

// cronRunNow start een beheerde job meteen. Het merkteken zegt cron-run dat
// de run met de hand gestart is, zodat hij ook zonder het VIP draait.
func (a *Agent) cronRunNow(ctx context.Context, id string) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	if _, err := uuid.Parse(id); err != nil {
		res.Error = "ongeldige job-id"
		return res
	}
	jobFile, err := a.path(path.Join(protocol.CronJobDir, id+".json"))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if _, err := os.Stat(jobFile); err != nil {
		res.Error = "deze job staat nog niet op de node"
		return res
	}
	marker, err := a.path(path.Join(protocol.CronStateDir, "manual", id))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if err := writeFileAtomic(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		res.Error = err.Error()
		return res
	}
	if out, err := a.exec(ctx, "systemctl", "start", "--no-block", "cf-cron@"+id+".service"); err != nil {
		res.Error = fmt.Sprintf("systemctl start: %v: %s", err, strings.TrimSpace(string(out)))
		return res
	}
	res.Output = []string{"gestart"}
	return res
}

// cronSpool is de wachtrij met runs op de node.
type cronSpool struct{ dir string }

func (a *Agent) cronSpool() (cronSpool, error) {
	dir, err := a.path(path.Join(protocol.CronStateDir, "spool"))
	return cronSpool{dir}, err
}

// next geeft de runs die nog weg moeten, oudste eerst: elke run die klaar
// is, en een lopende run die de server nog niet kent.
func (s cronSpool) next() []protocol.CronRunRecord {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	var out []protocol.CronRunRecord
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r protocol.CronRunRecord
		if err := json.Unmarshal(data, &r); err != nil || r.ID == "" {
			_ = os.Remove(f)
			continue
		}
		if r.EndedAt == nil {
			if _, err := os.Stat(strings.TrimSuffix(f, ".json") + ".sent"); err == nil {
				continue
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	if len(out) > protocol.MaxCronRuns {
		out = out[:protocol.MaxCronRuns]
	}
	return out
}

// sent vergeet wat de server bevestigde: een run die klaar is, gaat weg;
// van een lopende run onthoudt de agent dat de start bekend is.
func (s cronSpool) sent(runs []protocol.CronRunRecord) {
	for _, r := range runs {
		base := filepath.Join(s.dir, r.ID)
		if r.EndedAt != nil {
			_ = os.Remove(base + ".json")
			_ = os.Remove(base + ".sent")
			continue
		}
		_ = os.WriteFile(base+".sent", nil, 0o600)
	}
}

// cronRunsLoop stuurt de runs van beheerde jobs naar de server.
func (a *Agent) cronRunsLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := a.sendCronRuns(ctx); err != nil && !errors.Is(err, nats.ErrNoResponders) && ctx.Err() == nil {
			a.Log.Warn("runs van cronjobs sturen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) sendCronRuns(ctx context.Context) error {
	sp, err := a.cronSpool()
	if err != nil {
		return err
	}
	for ctx.Err() == nil && a.nc.IsConnected() {
		runs := sp.next()
		if len(runs) == 0 {
			return nil
		}
		data, err := envelope(protocol.TypeCronRuns, protocol.CronRuns{Runs: runs})
		if err != nil {
			return err
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		msg, err := a.nc.RequestWithContext(rctx, protocol.Subject(a.Config.NodeID, protocol.SubjectEvents), data)
		cancel()
		if err != nil {
			return err
		}
		var ack protocol.Ack
		if err := json.Unmarshal(msg.Data, &ack); err != nil {
			return err
		}
		if !ack.OK {
			return errors.New("server: " + ack.Error)
		}
		sp.sent(runs)
	}
	return nil
}

// cronOutput geeft de uitvoer tot nu toe van een run.
func (a *Agent) cronOutput(jobID, runID string) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	if _, err := uuid.Parse(jobID); err != nil {
		res.Error = "ongeldige job-id"
		return res
	}
	if _, err := uuid.Parse(runID); err != nil {
		res.Error = "ongeldige run-id"
		return res
	}
	dir, err := a.path(path.Join(protocol.CronStateDir, "output", jobID))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*-"+runID+".log"))
	if len(files) == 0 {
		res.Error = "de uitvoer van deze run staat niet meer op de node"
		return res
	}
	out, truncated := tailFile(files[0], protocol.CronOutputMax)
	res.RunOutput, res.RunOutputTruncated = protocol.MaskSecrets(out), truncated
	return res
}

// cronStop stopt de lopende run van een job; cf-agent cron-run krijgt
// SIGTERM en stopt het commando met zijn hele procesgroep.
func (a *Agent) cronStop(ctx context.Context, jobID string) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	if _, err := uuid.Parse(jobID); err != nil {
		res.Error = "ongeldige job-id"
		return res
	}
	if out, err := a.exec(ctx, "systemctl", "stop", "cf-cron@"+jobID+".service"); err != nil {
		res.Error = fmt.Sprintf("systemctl stop: %v: %s", err, strings.TrimSpace(string(out)))
		return res
	}
	res.Output = []string{"gestopt"}
	return res
}
