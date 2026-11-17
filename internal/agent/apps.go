package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De stand van apps van templates (fase 29): een ctl-script zet die in
// protocol.AppStatusDir/<app>.json, de agent stuurt ze mee met de facts.
// Hij leest hoogstens een paar kleine bestanden, nooit symlinks, en kort
// teksten in.

var appNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (c *Collector) apps() []protocol.AppStatus {
	dir := c.path(protocol.AppStatusDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []protocol.AppStatus
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		if len(out) >= protocol.MaxAppStatusFiles {
			break
		}
		a, ok := readAppStatus(filepath.Join(dir, name))
		if !ok || a.App != strings.TrimSuffix(name, ".json") {
			continue
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b protocol.AppStatus) int { return strings.Compare(a.App, b.App) })
	return out
}

func readAppStatus(path string) (protocol.AppStatus, bool) {
	var a protocol.AppStatus
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return a, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, protocol.MaxAppStatusBytes+1))
	if err != nil || len(data) > protocol.MaxAppStatusBytes {
		return a, false
	}
	if json.Unmarshal(data, &a) != nil || !appNameRe.MatchString(a.App) {
		return a, false
	}
	a.Title = appClip(a.Title, 80)
	a.Cluster = appClip(a.Cluster, 80)
	a.Version = appClip(a.Version, 64)
	a.Ref = appClip(a.Ref, 200)
	a.Role = appClip(a.Role, 32)
	if len(a.Checks) > protocol.MaxAppChecks {
		a.Checks = a.Checks[:protocol.MaxAppChecks]
	}
	for i := range a.Checks {
		ch := &a.Checks[i]
		ch.ID, ch.Label, ch.Detail = appClip(ch.ID, 64), appClip(ch.Label, 120), appClip(ch.Detail, 300)
		if ch.Severity != "red" {
			ch.Severity = "amber"
		}
	}
	if len(a.Values) > protocol.MaxAppValues {
		a.Values = a.Values[:protocol.MaxAppValues]
	}
	for i := range a.Values {
		v := &a.Values[i]
		v.ID, v.Label, v.Value = appClip(v.ID, 64), appClip(v.Label, 120), appClip(v.Value, 200)
	}
	return a, true
}

// clip kort s in tot n bytes, zonder een teken te breken.
func appClip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
