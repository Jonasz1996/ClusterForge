package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func testJob(id, command string) protocol.ManagedCron {
	u, _ := user.Current()
	return protocol.ManagedCron{
		ID: id, Name: "opruimen", Revision: 1, Schedule: "0 */6 * * *", OnCalendar: "*-*-* 00,06,12,18:00:00",
		RunOn: protocol.CronOnVIP, VIPs: []string{"10.0.0.185"}, User: u.Username, Command: command, TimeoutSeconds: 60, Enabled: true,
	}
}

const (
	jobA = "0b9e8d4c-1111-4c3b-9a7e-000000000001"
	jobB = "0b9e8d4c-1111-4c3b-9a7e-000000000002"
)

func TestCronApply(t *testing.T) {
	root := t.TempDir()
	var calls []string
	a := &Agent{Root: root, Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}}
	ctx := context.Background()
	jobs := []protocol.ManagedCron{testJob(jobA, "echo a"), testJob(jobB, "echo b")}
	jobs[1].Enabled = false
	res := a.cronApply(ctx, jobs, nil)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.Hash != protocol.CronSetHash(jobs) {
		t.Errorf("hash %q", res.Hash)
	}
	timer, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/cf-cron-"+jobA+".timer"))
	if err != nil || !strings.Contains(string(timer), "OnCalendar=*-*-* 00,06,12,18:00:00\nPersistent=true\nUnit=cf-cron@"+jobA+".service") {
		t.Errorf("timer: %s %v", timer, err)
	}
	svc, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/cf-cron@.service"))
	if err != nil || !strings.Contains(string(svc), "ExecStart=/usr/local/bin/cf-agent cron-run %i") {
		t.Errorf("service: %s %v", svc, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "etc/clusterforge/cron", jobA+".json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("jobbestand: %v %v", fi, err)
	}
	want := "systemctl daemon-reload; systemctl enable cf-cron-" + jobA + ".timer; systemctl restart cf-cron-" + jobA + ".timer; systemctl disable --now cf-cron-" + jobB + ".timer"
	if got := strings.Join(calls, "; "); got != want {
		t.Errorf("commando's:\n%s\nwil\n%s", got, want)
	}

	// Dezelfde set nog eens: niets te doen.
	calls = nil
	if res := a.cronApply(ctx, jobs, nil); res.Error != "" || len(calls) != 0 {
		t.Errorf("tweede keer: %v %v", res.Error, calls)
	}

	// Job B weg, job A met een nieuw schema.
	calls = nil
	a1 := testJob(jobA, "echo a")
	a1.OnCalendar, a1.Revision = "*-*-* 03:00:00", 2
	if res := a.cronApply(ctx, []protocol.ManagedCron{a1}, nil); res.Error != "" {
		t.Fatal(res.Error)
	}
	want = "systemctl disable --now cf-cron-" + jobB + ".timer; systemctl daemon-reload; systemctl enable cf-cron-" + jobA + ".timer; systemctl restart cf-cron-" + jobA + ".timer"
	if got := strings.Join(calls, "; "); got != want {
		t.Errorf("commando's:\n%s\nwil\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/clusterforge/cron", jobB+".json")); !os.IsNotExist(err) {
		t.Errorf("jobbestand van B: %v", err)
	}

	// Geen jobs meer: ook de service gaat weg.
	if res := a.cronApply(ctx, nil, nil); res.Error != "" {
		t.Fatal(res.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/cf-cron@.service")); !os.IsNotExist(err) {
		t.Errorf("service staat er nog: %v", err)
	}

	// Een ongeldige gebruiker of id komt er niet door.
	bad := testJob(jobA, "echo")
	bad.User = "root; rm -rf /"
	if res := a.cronApply(ctx, []protocol.ManagedCron{bad}, nil); res.Error == "" {
		t.Error("ongeldige gebruiker aanvaard")
	}
	bad = testJob("../../etc/passwd", "echo")
	if res := a.cronApply(ctx, []protocol.ManagedCron{bad}, nil); res.Error == "" {
		t.Error("ongeldige id aanvaard")
	}
}

func writeJob(t *testing.T, root string, j protocol.ManagedCron) {
	t.Helper()
	data, _ := json.Marshal(j)
	writeTree(t, root, map[string]string{"/etc/clusterforge/cron/" + j.ID + ".json": string(data)}, 0o600)
}

func spooled(t *testing.T, root string) []protocol.CronRunRecord {
	t.Helper()
	return cronSpool{filepath.Join(root, protocol.CronStateDir, "spool")}.next()
}

func TestCronRunner(t *testing.T) {
	root := t.TempDir()
	addrs := []string{"192.0.2.11"}
	var stderr bytes.Buffer
	r := &CronRunner{Root: root, Addresses: func() []string { return addrs }, Stderr: &stderr}
	ctx := context.Background()

	// Zonder het VIP draait de job niet, en staat er niets in de wachtrij.
	writeJob(t, root, testJob(jobA, "echo hallo; echo kapot >&2; exit 3"))
	if code := r.Main(ctx, jobA); code != 0 || len(spooled(t, root)) != 0 {
		t.Fatalf("zonder VIP: %d %v", code, spooled(t, root))
	}

	// Met het VIP wel: exitcode 3, mislukt, met de uitvoer.
	addrs = append(addrs, "10.0.0.185")
	if code := r.Main(ctx, jobA); code != 3 {
		t.Fatalf("exitcode %d: %s", code, stderr.String())
	}
	runs := spooled(t, root)
	if len(runs) != 1 || runs[0].Result != protocol.CronFailed || runs[0].Exit == nil || *runs[0].Exit != 3 ||
		runs[0].Output != "hallo\nkapot\n" || runs[0].Reason != protocol.CronReasonSchedule || runs[0].EndedAt == nil {
		t.Fatalf("run: %+v", runs)
	}
	sp := cronSpool{filepath.Join(root, protocol.CronStateDir, "spool")}
	sp.sent(runs)
	if len(spooled(t, root)) != 0 {
		t.Error("run na de Ack nog in de wachtrij")
	}

	// Met de hand gestart draait hij ook zonder VIP; wachtwoorden in de
	// uitvoer zijn gemaskeerd.
	addrs = nil
	writeJob(t, root, testJob(jobA, "echo mysql -uroot -pGeheim db"))
	writeTree(t, root, map[string]string{"/var/lib/clusterforge/cron/manual/" + jobA: "nu"}, 0o600)
	if code := r.Main(ctx, jobA); code != 0 {
		t.Fatalf("met de hand: %d", code)
	}
	runs = spooled(t, root)
	if len(runs) != 1 || runs[0].Result != protocol.CronOK || runs[0].Reason != protocol.CronReasonManual || strings.Contains(runs[0].Output, "Geheim") {
		t.Fatalf("met de hand: %+v", runs)
	}
	sp.sent(runs)

	// Na de tijdslimiet stopt de job.
	j := testJob(jobB, "sleep 30")
	j.RunOn, j.TimeoutSeconds = protocol.CronOnAll, 1
	writeJob(t, root, j)
	start := time.Now()
	r.Main(ctx, jobB)
	if time.Since(start) > 15*time.Second {
		t.Errorf("tijdslimiet duurde %s", time.Since(start))
	}
	runs = spooled(t, root)
	if len(runs) != 1 || runs[0].Result != protocol.CronTimedOut || !strings.Contains(runs[0].Output, "tijdslimiet") {
		t.Fatalf("tijdslimiet: %+v", runs)
	}
	sp.sent(runs)

	// Loopt de vorige run nog, dan slaat deze over.
	unlock, err := cronLock(filepath.Join(root, protocol.CronStateDir, "locks", jobB+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	r.Main(ctx, jobB)
	unlock()
	runs = spooled(t, root)
	if len(runs) != 1 || runs[0].Result != protocol.CronSkipped {
		t.Fatalf("overgeslagen: %+v", runs)
	}
}

func TestCronSpoolRunning(t *testing.T) {
	root := t.TempDir()
	sp := cronSpool{filepath.Join(root, "spool")}
	run := protocol.CronRunRecord{ID: "r1", JobID: jobA, Result: protocol.CronRunning, StartedAt: time.Now()}
	data, _ := json.Marshal(run)
	writeTree(t, root, map[string]string{"/spool/r1.json": string(data)}, 0o600)
	if got := sp.next(); len(got) != 1 {
		t.Fatalf("lopende run: %v", got)
	}
	sp.sent(sp.next())
	if got := sp.next(); len(got) != 0 {
		t.Fatalf("lopende run twee keer: %v", got)
	}
	end := time.Now()
	run.EndedAt, run.Result = &end, protocol.CronOK
	data, _ = json.Marshal(run)
	writeTree(t, root, map[string]string{"/spool/r1.json": string(data)}, 0o600)
	got := sp.next()
	if len(got) != 1 || got[0].Result != protocol.CronOK {
		t.Fatalf("einde: %v", got)
	}
	sp.sent(got)
	if files, _ := filepath.Glob(filepath.Join(root, "spool", "*")); len(files) != 0 {
		t.Errorf("over: %v", files)
	}
}

func TestCronRunnerRetryStopOutput(t *testing.T) {
	root := t.TempDir()
	r := &CronRunner{Root: root, Addresses: func() []string { return nil }, Stderr: &bytes.Buffer{}}
	sp := cronSpool{filepath.Join(root, protocol.CronStateDir, "spool")}

	// Twee keer herhalen na een fout: drie runs, de laatste twee als
	// herhaling.
	j := testJob(jobA, "echo poging; exit 1")
	j.RunOn, j.Retries, j.RetryDelaySeconds = protocol.CronOnAll, 2, 1
	writeJob(t, root, j)
	if code := r.Main(context.Background(), jobA); code != 1 {
		t.Fatalf("exitcode %d", code)
	}
	runs := sp.next()
	if len(runs) != 3 || runs[0].Reason != protocol.CronReasonSchedule || runs[1].Reason != protocol.CronReasonRetry || runs[2].Reason != protocol.CronReasonRetry {
		t.Fatalf("runs: %+v", runs)
	}
	for _, run := range runs {
		if run.Result != protocol.CronFailed {
			t.Errorf("run %+v", run)
		}
	}
	sp.sent(runs)

	// Stoppen: de context van cron-run loopt af, zoals bij SIGTERM.
	j = testJob(jobB, "echo begonnen; sleep 30")
	j.RunOn = protocol.CronOnAll
	writeJob(t, root, j)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	go func() { done <- r.Main(ctx, jobB) }()
	a := &Agent{Root: root}
	var running protocol.CronRunRecord
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rs := sp.next(); len(rs) == 1 && rs[0].Result == protocol.CronRunning {
			running = rs[0]
			if out := a.cronOutput(jobB, running.ID); out.RunOutput == "begonnen\n" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out := a.cronOutput(jobB, running.ID); out.Error != "" || out.RunOutput != "begonnen\n" {
		t.Fatalf("uitvoer tijdens de run: %+v", out)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("stoppen duurde te lang")
	}
	runs = sp.next()
	if len(runs) != 1 || runs[0].Result != protocol.CronStopped || !strings.Contains(runs[0].Output, "met de hand gestopt") {
		t.Fatalf("gestopt: %+v", runs)
	}
	if out := a.cronOutput(jobB, "0b9e8d4c-1111-4c3b-9a7e-00000000ffff"); out.Error == "" {
		t.Error("uitvoer van een onbekende run")
	}
}
