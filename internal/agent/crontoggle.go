package agent

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// cronToggle zet een gevonden cronregel in commentaar, of weer aan. De
// agent verandert alleen de regel die e aanwijst, in een crontab die cron
// zelf leest.
func (a *Agent) cronToggle(e *protocol.CronEntry, enable bool) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	if e == nil {
		res.Error = "welke cronregel?"
		return res
	}
	user, ok := toggleFile(e)
	if !ok {
		res.Error = "alleen een regel in /etc/crontab, /etc/cron.d of de crontab van een gebruiker kan aan of uit"
		return res
	}
	c := a.Collector
	if c == nil {
		c = &Collector{}
	}
	p := c.path(e.File)
	fi, err := os.Stat(p)
	if err != nil {
		res.Error = fmt.Sprintf("%s: %v", e.File, err)
		return res
	}
	data, err := os.ReadFile(p)
	if err != nil {
		res.Error = fmt.Sprintf("%s: %v", e.File, err)
		return res
	}
	lines := strings.Split(string(data), "\n")
	// Eerst het regelnummer dat CronList gaf, dan de rest van het bestand.
	order := make([]int, 0, len(lines))
	if e.Line > 0 && e.Line <= len(lines) {
		order = append(order, e.Line-1)
	}
	for i := range lines {
		if i != e.Line-1 {
			order = append(order, i)
		}
	}
	find := func(off bool) int {
		for _, i := range order {
			line := strings.TrimSpace(lines[i])
			rest, isOff := strings.CutPrefix(line, protocol.CronOffPrefix)
			if isOff != off {
				continue
			}
			if isOff {
				line = strings.TrimSpace(rest)
			}
			if sameCron(line, user, e) {
				return i
			}
		}
		return -1
	}
	i := find(enable)
	if i < 0 {
		// Staat hij al zoals gevraagd, dan is er niets te doen.
		if find(!enable) >= 0 {
			res.Output = []string{"de regel in " + e.File + " stond al " + map[bool]string{true: "aan", false: "uit"}[enable]}
			return res
		}
		res.Error = "de regel staat niet meer in " + e.File + "; lees de node opnieuw"
		return res
	}
	if enable {
		lines[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), protocol.CronOffPrefix))
		res.Output = []string{fmt.Sprintf("regel %d in %s staat weer aan", i+1, e.File)}
	} else {
		lines[i] = protocol.CronOffPrefix + strings.TrimSpace(lines[i])
		res.Output = []string{fmt.Sprintf("regel %d in %s staat in commentaar", i+1, e.File)}
	}
	if err := writeKeepOwner(p, []byte(strings.Join(lines, "\n")), fi); err != nil {
		res.Error = fmt.Sprintf("%s schrijven: %v", e.File, err)
		res.Output = nil
	}
	return res
}

// toggleFile zegt of het bestand van e een crontab is die cron leest, met
// de gebruiker van een gebruikerscrontab (leeg: de regel heeft een
// gebruikersveld).
func toggleFile(e *protocol.CronEntry) (string, bool) {
	f := path.Clean(e.File)
	if f != e.File {
		return "", false
	}
	dir, name := path.Split(f)
	dir = strings.TrimSuffix(dir, "/")
	switch {
	case e.Source == protocol.CronSourceCrontab && f == "/etc/crontab":
		return "", true
	case e.Source == protocol.CronSourceCronD && dir == "/etc/cron.d" && runPartsName.MatchString(name):
		return "", true
	case e.Source == protocol.CronSourceUser && slices.Contains(cronSpoolDirs, dir) && name == e.User && !strings.HasPrefix(name, "."):
		return name, true
	}
	return "", false
}

// sameCron zegt of line dezelfde job is als e: dezelfde gebruiker en hetzelfde
// schema, en het commando zoals CronList het meldt.
func sameCron(line, user string, e *protocol.CronEntry) bool {
	schedule, u, cmd, ok := parseCronLine(line, user)
	if !ok || schedule != e.Schedule || u != e.User {
		return false
	}
	cmd = protocol.MaskSecrets(cmd)
	if len(cmd) > protocol.MaxCronCommand {
		cmd = cmd[:protocol.MaxCronCommand] + "…"
	}
	return cmd == e.Command
}

// writeKeepOwner schrijft een bestand opnieuw met dezelfde rechten en
// eigenaar: cron weigert een crontab van een gebruiker die root bezit.
func writeKeepOwner(p string, data []byte, fi os.FileInfo) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && (int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid()) {
		if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
