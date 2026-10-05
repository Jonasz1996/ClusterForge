package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// DefaultStatePath bewaart wat de agent over een herstart heen moet
// onthouden: het onderhoud en het laatste reboot- of shutdown-commando.
const DefaultStatePath = "/var/lib/clusterforge/agent-state.json"

// agentState staat in StatePath.
type agentState struct {
	Maintenance bool `json:"maintenance"`
	// Hoe keepalived voor het onderhoud stond; zo komt het na het onderhoud
	// terug zoals het was.
	KeepalivedEnabled bool `json:"keepalived_enabled,omitempty"`
	KeepalivedActive  bool `json:"keepalived_active,omitempty"`
	// PowerCommand is het id van het laatste reboot- of shutdowncommando. Stuurt
	// de server het na de herstart nog eens, dan herstart de agent niet opnieuw.
	PowerCommand string `json:"power_command,omitempty"`
}

const keepalivedUnit = "keepalived"

func (a *Agent) statePath() string {
	if a.StatePath != "" {
		return a.StatePath
	}
	return DefaultStatePath
}

func (a *Agent) loadState() (agentState, error) {
	var st agentState
	b, err := os.ReadFile(a.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s: %w", a.statePath(), err)
	}
	return st, nil
}

func (a *Agent) saveState(st agentState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.statePath(), append(b, '\n'), 0o600)
}

// exec voert een systeemcommando uit en geeft stdout en stderr samen terug.
func (a *Agent) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if a.Exec != nil {
		return a.Exec(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.CombinedOutput()
}

func (a *Agent) onCommand(m *nats.Msg) {
	res := a.handleCommand(m.Data)
	data, err := envelope(protocol.TypeResult, res)
	if err != nil {
		return
	}
	if err := m.Respond(data); err != nil {
		a.Log.Warn("antwoord op commando sturen mislukt", "err", err)
	}
}

// handleCommand voert een commando uit. De subscription roept het voor één
// bericht tegelijk aan, dus twee commando's lopen nooit door elkaar.
func (a *Agent) handleCommand(data []byte) protocol.Result {
	var env protocol.Envelope
	var cmd protocol.Command
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeCommand {
		return failed(nil, "ongeldig commando")
	}
	if err := json.Unmarshal(env.Body, &cmd); err != nil || cmd.ID == "" {
		return failed(nil, "ongeldig commando")
	}
	if !cmd.Deadline.IsZero() && time.Now().After(cmd.Deadline) {
		a.Log.Warn("verlopen commando geweigerd", "action", cmd.Action, "id", cmd.ID, "deadline", cmd.Deadline)
		return failed(nil, "commando verlopen; klopt de klok van deze node?")
	}
	a.Log.Info("commando ontvangen", "action", cmd.Action, "id", cmd.ID, "reason", cmd.Reason)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var res protocol.Result
	switch cmd.Action {
	case protocol.CmdFactsCollect:
		if err := a.sendFacts(ctx); err != nil {
			res = failed(nil, "facts sturen mislukt: "+err.Error())
		} else {
			res = done([]string{"facts verzameld en naar de server gestuurd"})
		}
	case protocol.CmdMaintenanceEnter, protocol.CmdMaintenanceExit:
		if cmd.Action == protocol.CmdMaintenanceEnter {
			res = a.maintenanceEnter(ctx)
		} else {
			res = a.maintenanceExit(ctx)
		}
		// Keepalived staat nu anders; de server moet dat ook in de facts zien.
		if a.factsNow != nil {
			a.signal(a.factsNow)
		}
	case protocol.CmdReboot, protocol.CmdShutdown:
		res = a.power(cmd)
	default:
		res = failed(nil, "onbekend commando "+cmd.Action)
	}
	if res.OK {
		a.Log.Info("commando uitgevoerd", "action", cmd.Action, "id", cmd.ID)
	} else {
		a.Log.Warn("commando mislukt", "action", cmd.Action, "id", cmd.ID, "err", res.Error)
	}
	return res
}

func done(out []string) protocol.Result { return protocol.Result{OK: true, Output: out} }

func failed(out []string, msg string) protocol.Result {
	return protocol.Result{Error: msg, Output: out}
}

// unit leest hoe een systemd-unit erbij staat.
func (a *Agent) unit(ctx context.Context, name string) (loaded, enabled, active bool, err error) {
	out, err := a.exec(ctx, "systemctl", "show", name, "--property=LoadState,UnitFileState,ActiveState")
	if err != nil {
		return false, false, false, fmt.Errorf("systemctl show %s: %w", name, err)
	}
	props := map[string]string{}
	for line := range strings.Lines(string(out)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	loaded = props["LoadState"] == "loaded"
	enabled = props["UnitFileState"] == "enabled" || props["UnitFileState"] == "enabled-runtime"
	active = props["ActiveState"] == "active" || props["ActiveState"] == "activating" || props["ActiveState"] == "reloading"
	return loaded, enabled, active, nil
}

// maintenanceEnter zet keepalived uit zodat de VIP's naar de andere nodes
// gaan. Disable zorgt dat het ook na een reboot uit blijft.
func (a *Agent) maintenanceEnter(ctx context.Context) protocol.Result {
	st, err := a.loadState()
	if err != nil {
		return failed(nil, err.Error())
	}
	if st.Maintenance {
		return done([]string{"deze node stond al in onderhoud"})
	}
	loaded, enabled, active, err := a.unit(ctx, keepalivedUnit)
	if err != nil {
		return failed(nil, err.Error())
	}
	// Eerst onthouden hoe het stond, dan pas iets veranderen: zo kan het
	// onderhoud altijd netjes eindigen.
	st.Maintenance, st.KeepalivedEnabled, st.KeepalivedActive = true, loaded && enabled, loaded && active
	if err := a.saveState(st); err != nil {
		return failed(nil, "state bewaren mislukt: "+err.Error())
	}
	switch {
	case !loaded:
		return done([]string{"geen keepalived op deze node"})
	case !enabled && !active:
		return done([]string{"keepalived stond al uit"})
	}
	args := []string{"stop", keepalivedUnit}
	if enabled {
		args = []string{"disable", "--now", keepalivedUnit}
	}
	out, err := a.exec(ctx, "systemctl", args...)
	lines := outputLines(out)
	if err != nil {
		return failed(lines, "keepalived uitzetten mislukt: "+err.Error())
	}
	return done(append(lines, "keepalived gestopt; de VIP's gaan naar de andere nodes"))
}

// maintenanceExit zet keepalived terug zoals het voor het onderhoud stond.
func (a *Agent) maintenanceExit(ctx context.Context) protocol.Result {
	st, err := a.loadState()
	if err != nil {
		return failed(nil, err.Error())
	}
	if !st.Maintenance {
		return done([]string{"deze node stond niet in onderhoud"})
	}
	var lines []string
	for _, step := range []struct {
		do   bool
		verb string
	}{{st.KeepalivedEnabled, "enable"}, {st.KeepalivedActive, "start"}} {
		if !step.do {
			continue
		}
		out, err := a.exec(ctx, "systemctl", step.verb, keepalivedUnit)
		lines = append(lines, outputLines(out)...)
		if err != nil {
			return failed(lines, fmt.Sprintf("systemctl %s %s mislukt: %v", step.verb, keepalivedUnit, err))
		}
	}
	switch {
	case st.KeepalivedActive:
		lines = append(lines, "keepalived draait weer")
	case st.KeepalivedEnabled:
		lines = append(lines, "keepalived staat weer aan voor de volgende start")
	default:
		lines = append(lines, "keepalived blijft uit, zoals voor het onderhoud")
	}
	if err := a.saveState(agentState{PowerCommand: st.PowerCommand}); err != nil {
		return failed(lines, "state bewaren mislukt: "+err.Error())
	}
	return done(lines)
}

// power herstart of stopt de machine na een korte pauze, zodat het antwoord
// nog bij de server komt.
func (a *Agent) power(cmd protocol.Command) protocol.Result {
	st, err := a.loadState()
	if err != nil {
		return failed(nil, err.Error())
	}
	if st.PowerCommand == cmd.ID {
		return protocol.Result{OK: true, Repeat: true, Output: []string{"dit commando is al uitgevoerd"}}
	}
	// Zonder bewaard id zou een herhaald commando na de herstart nog eens
	// herstarten; dan liever niet.
	st.PowerCommand = cmd.ID
	if err := a.saveState(st); err != nil {
		return failed(nil, "state bewaren mislukt: "+err.Error())
	}
	verb, what := "reboot", "herstart"
	if cmd.Action == protocol.CmdShutdown {
		verb, what = "poweroff", "gaat uit"
	}
	delay := time.Duration(max(cmd.DelaySeconds, 1)) * time.Second
	msg := "ClusterForge"
	if cmd.Reason != "" {
		msg += ": " + cmd.Reason
	}
	go func() {
		time.Sleep(delay)
		a.Log.Warn("machine "+what, "reason", cmd.Reason)
		if out, err := a.exec(context.Background(), "systemctl", verb, "--message="+msg); err != nil {
			a.Log.Error("systemctl "+verb+" mislukt", "err", err, "output", strings.TrimSpace(string(out)))
		}
	}()
	return done([]string{fmt.Sprintf("de machine %s over %d s", what, int(delay.Seconds()))})
}

// outputLines splitst de uitvoer van een commando in niet-lege regels.
func outputLines(out []byte) []string {
	var lines []string
	for line := range strings.Lines(string(out)) {
		if l := strings.TrimSpace(line); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
