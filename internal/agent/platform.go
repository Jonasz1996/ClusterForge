package agent

// Fase 34: cf-agent draait ook op FreeBSD (OPNsense). Daar is geen systemd:
// de service is een rc.d-script onder daemon(8), en een eenmalige timer is
// een losgekoppeld proces (daemon -f) dat slaapt en daarna het commando
// start. Hier staan de verschillen op één plek.

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// hostOS is het besturingssysteem van deze agent; tests zetten het.
var hostOS = runtime.GOOS

// execFunc voert een systeemcommando uit en geeft de uitvoer.
type execFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// FreeBSD-paden.
const (
	bsdDaemon  = "/usr/sbin/daemon"
	bsdService = "/usr/sbin/service"
	// bsdRCName is de naam van het rc.d-script van de agent.
	bsdRCName = "cf_agent"
)

// oneShotPidfile is het pidbestand van een eenmalige timer op FreeBSD.
func oneShotPidfile(unit string) string { return "/var/run/" + unit + ".pid" }

// armOneShot start argv over after, los van cf-agent: een herstart of een
// nieuwe agent die niet start, raakt hem niet. Een vorige timer met dezelfde
// naam gaat eerst weg.
func armOneShot(ctx context.Context, exec execFunc, unit, desc string, after time.Duration, argv ...string) error {
	disarmOneShot(ctx, exec, unit)
	secs := max(int(after.Seconds()), 1)
	var out []byte
	var err error
	if hostOS == "freebsd" {
		// sh krijgt de seconden als $0 en het commando als "$@".
		args := append([]string{"-f", "-p", oneShotPidfile(unit), "/bin/sh", "-c", `sleep "$0" && exec "$@"`, strconv.Itoa(secs)}, argv...)
		out, err = exec(ctx, bsdDaemon, args...)
	} else {
		args := append([]string{"--unit=" + unit, "--collect",
			fmt.Sprintf("--on-active=%ds", secs), "--timer-property=AccuracySec=1s",
			"--description=" + desc}, argv...)
		out, err = exec(ctx, "systemd-run", args...)
	}
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// disarmOneShot stopt een timer van armOneShot als hij nog wacht.
func disarmOneShot(ctx context.Context, exec execFunc, unit string) {
	if hostOS == "freebsd" {
		_, _ = exec(ctx, "/bin/pkill", "-F", oneShotPidfile(unit))
		return
	}
	_, _ = exec(ctx, "systemctl", "stop", unit+".timer")
	_, _ = exec(ctx, "systemctl", "reset-failed", unit+".timer", unit+".service")
}

// oneShotActive is true als de timer nog wacht.
func oneShotActive(ctx context.Context, exec execFunc, unit string) bool {
	if hostOS == "freebsd" {
		_, err := exec(ctx, "/bin/pgrep", "-F", oneShotPidfile(unit))
		return err == nil
	}
	_, err := exec(ctx, "systemctl", "is-active", "--quiet", unit+".timer")
	return err == nil
}

// restartAgent herstart de service cf-agent zonder erop te wachten. Op
// FreeBSD los van de agent (daemon -f), anders stopt service(8) zijn eigen
// ouder halverwege.
func restartAgent(ctx context.Context, exec execFunc) ([]byte, error) {
	if hostOS == "freebsd" {
		return exec(ctx, bsdDaemon, "-f", bsdService, bsdRCName, "restart")
	}
	return exec(ctx, "systemctl", "--no-block", "restart", "cf-agent")
}

// agentPlatform is de sleutel van deze binary bij de server
// (protocol.AgentPlatform): amd64 op Linux, freebsd-amd64 op OPNsense.
func agentPlatform() string { return protocol.AgentPlatform(hostOS, runtime.GOARCH) }

// bsdStartDetached start argv los van de agent onder daemon(8), met het
// pidbestand van unit; op FreeBSD de tegenhanger van een systemd-run-unit.
func bsdStartDetached(ctx context.Context, exec execFunc, unit string, argv ...string) error {
	out, err := exec(ctx, bsdDaemon, append([]string{"-f", "-p", oneShotPidfile(unit)}, argv...)...)
	if err != nil {
		return fmt.Errorf("daemon: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// bsdStopDetached stuurt SIGTERM naar het proces van unit.
func bsdStopDetached(ctx context.Context, exec execFunc, unit string) error {
	out, err := exec(ctx, "/bin/pkill", "-TERM", "-F", oneShotPidfile(unit))
	if err != nil {
		return fmt.Errorf("pkill: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
