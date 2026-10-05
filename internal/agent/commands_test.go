package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeSystemd speelt systemctl na voor één unit.
type fakeSystemd struct {
	mu              sync.Mutex
	loaded, enabled bool
	active          bool
	calls           []string
	fail            string
}

func (f *fakeSystemd) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.fail != "" && strings.HasPrefix(call, f.fail) {
		return []byte("Job failed\n"), errors.New("exit status 1")
	}
	if name != "systemctl" {
		return nil, errors.New("onverwacht commando " + name)
	}
	switch args[0] {
	case "show":
		if !f.loaded {
			return []byte("LoadState=not-found\nUnitFileState=\nActiveState=inactive\n"), nil
		}
		state := map[bool]string{true: "enabled", false: "disabled"}[f.enabled]
		act := map[bool]string{true: "active", false: "inactive"}[f.active]
		return []byte("LoadState=loaded\nUnitFileState=" + state + "\nActiveState=" + act + "\n"), nil
	case "disable":
		f.enabled = false
		if slices.Contains(args, "--now") {
			f.active = false
			return []byte("Removed \"/etc/systemd/system/multi-user.target.wants/keepalived.service\".\n"), nil
		}
	case "enable":
		f.enabled = true
	case "start":
		f.active = true
	case "stop":
		f.active = false
	}
	return nil, nil
}

func (f *fakeSystemd) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "systemctl show") {
			out = append(out, c)
		}
	}
	return out
}

func testAgent(t *testing.T, sys *fakeSystemd) *Agent {
	t.Helper()
	return &Agent{
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		StatePath: filepath.Join(t.TempDir(), "state", "agent-state.json"),
		Exec:      sys.exec,
	}
}

func command(t *testing.T, cmd protocol.Command) []byte {
	t.Helper()
	if cmd.Deadline.IsZero() {
		cmd.Deadline = time.Now().Add(time.Minute)
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(protocol.Envelope{V: protocol.Version, ID: cmd.ID, Type: protocol.TypeCommand, Body: b})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMaintenance(t *testing.T) {
	sys := &fakeSystemd{loaded: true, enabled: true, active: true}
	a := testAgent(t, sys)
	res := a.handleCommand(command(t, protocol.Command{ID: "1", Action: protocol.CmdMaintenanceEnter}))
	if !res.OK || sys.enabled || sys.active {
		t.Fatalf("enter: %+v, systemd %+v", res, sys)
	}
	// Een tweede keer verandert niets aan wat de agent onthield.
	res = a.handleCommand(command(t, protocol.Command{ID: "2", Action: protocol.CmdMaintenanceEnter}))
	if !res.OK || res.Output[0] != "deze node stond al in onderhoud" {
		t.Fatalf("enter opnieuw: %+v", res)
	}
	// Ook een nieuwe agent (na een reboot) weet nog hoe het stond.
	b := testAgent(t, sys)
	b.StatePath = a.StatePath
	res = b.handleCommand(command(t, protocol.Command{ID: "3", Action: protocol.CmdMaintenanceExit}))
	if !res.OK || !sys.enabled || !sys.active {
		t.Fatalf("exit: %+v, systemd %+v", res, sys)
	}
	want := []string{"systemctl disable --now keepalived", "systemctl enable keepalived", "systemctl start keepalived"}
	if got := sys.called(); !slices.Equal(got, want) {
		t.Fatalf("aanroepen %q, verwacht %q", got, want)
	}
	res = b.handleCommand(command(t, protocol.Command{ID: "4", Action: protocol.CmdMaintenanceExit}))
	if !res.OK || res.Output[0] != "deze node stond niet in onderhoud" {
		t.Fatalf("exit opnieuw: %+v", res)
	}
}

func TestMaintenanceKeepalivedVariants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sys   *fakeSystemd
		enter []string
		exit  []string
	}{
		{"geen keepalived", &fakeSystemd{}, nil, nil},
		{"uit", &fakeSystemd{loaded: true}, nil, nil},
		{"draait maar niet enabled", &fakeSystemd{loaded: true, active: true}, []string{"systemctl stop keepalived"}, []string{"systemctl start keepalived"}},
		{"enabled maar gestopt", &fakeSystemd{loaded: true, enabled: true}, []string{"systemctl disable --now keepalived"}, []string{"systemctl enable keepalived"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys := tc.sys
			a := testAgent(t, sys)
			if res := a.handleCommand(command(t, protocol.Command{ID: "1", Action: protocol.CmdMaintenanceEnter})); !res.OK {
				t.Fatalf("enter: %+v", res)
			}
			if got := sys.called(); !slices.Equal(got, tc.enter) {
				t.Fatalf("enter: %q, verwacht %q", got, tc.enter)
			}
			sys.calls = nil
			if res := a.handleCommand(command(t, protocol.Command{ID: "2", Action: protocol.CmdMaintenanceExit})); !res.OK {
				t.Fatalf("exit: %+v", res)
			}
			if got := sys.called(); !slices.Equal(got, tc.exit) {
				t.Fatalf("exit: %q, verwacht %q", got, tc.exit)
			}
		})
	}
}

func TestMaintenanceFailureKeepsState(t *testing.T) {
	sys := &fakeSystemd{loaded: true, enabled: true, active: true, fail: "systemctl disable"}
	a := testAgent(t, sys)
	res := a.handleCommand(command(t, protocol.Command{ID: "1", Action: protocol.CmdMaintenanceEnter}))
	if res.OK || !strings.Contains(res.Error, "keepalived uitzetten mislukt") || res.Output[0] != "Job failed" {
		t.Fatalf("enter: %+v", res)
	}
	// De server maakt het onderhoud dan ongedaan; keepalived komt terug.
	sys.fail = ""
	if res := a.handleCommand(command(t, protocol.Command{ID: "2", Action: protocol.CmdMaintenanceExit})); !res.OK {
		t.Fatalf("exit: %+v", res)
	}
	if got := sys.called(); !slices.Contains(got, "systemctl start keepalived") {
		t.Fatalf("keepalived niet teruggezet: %q", got)
	}
}

func TestPowerRunsOnce(t *testing.T) {
	sys := &fakeSystemd{}
	a := testAgent(t, sys)
	res := a.handleCommand(command(t, protocol.Command{ID: "r1", Action: protocol.CmdReboot, Reason: "kernelupdate", DelaySeconds: 1}))
	if !res.OK || res.Repeat {
		t.Fatalf("reboot: %+v", res)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(sys.called()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := sys.called(); !slices.Equal(got, []string{"systemctl reboot --message=ClusterForge: kernelupdate"}) {
		t.Fatalf("aanroepen %q", got)
	}
	// Na de herstart stuurt de server hetzelfde commando misschien nog eens.
	b := testAgent(t, sys)
	b.StatePath = a.StatePath
	res = b.handleCommand(command(t, protocol.Command{ID: "r1", Action: protocol.CmdReboot}))
	if !res.OK || !res.Repeat {
		t.Fatalf("herhaald: %+v", res)
	}
	time.Sleep(1200 * time.Millisecond)
	if n := len(sys.called()); n != 1 {
		t.Fatalf("%d keer herstart", n)
	}
	res = b.handleCommand(command(t, protocol.Command{ID: "s1", Action: protocol.CmdShutdown}))
	if !res.OK || res.Repeat || !strings.Contains(res.Output[0], "gaat uit") {
		t.Fatalf("shutdown: %+v", res)
	}
}

func TestCommandRejected(t *testing.T) {
	a := testAgent(t, &fakeSystemd{})
	for _, tc := range []struct {
		data []byte
		want string
	}{
		{[]byte("{"), "ongeldig commando"},
		{command(t, protocol.Command{Action: protocol.CmdReboot}), "ongeldig commando"},
		{command(t, protocol.Command{ID: "x", Action: protocol.CmdReboot, Deadline: time.Now().Add(-time.Second)}), "commando verlopen; klopt de klok van deze node?"},
		{command(t, protocol.Command{ID: "y", Action: "shell.run"}), "onbekend commando shell.run"},
	} {
		if res := a.handleCommand(tc.data); res.OK || res.Error != tc.want {
			t.Errorf("%s: %+v", tc.data, res)
		}
	}
}
