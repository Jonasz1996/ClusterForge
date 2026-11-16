package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	csAlerts = `[
 {"id": 12, "scenario": "crowdsecurity/ssh-bf", "message": "Ip 203.0.113.7 performed 'crowdsecurity/ssh-bf' (6 events)",
  "events_count": 6, "start_at": "2026-11-15T19:00:01Z", "stop_at": "2026-11-15T19:00:09Z", "simulated": false,
  "source": {"scope": "Ip", "value": "203.0.113.7", "cn": "NL", "as_name": "Example BV", "ip": "203.0.113.7"},
  "decisions": [{"id": 40, "origin": "crowdsec", "type": "ban", "scope": "Ip", "value": "203.0.113.7", "duration": "3h59m", "scenario": "crowdsecurity/ssh-bf"}]},
 {"id": 13, "scenario": "update : +15000/-0 IPs", "events_count": 0, "start_at": "2026-11-15T19:01:00Z", "stop_at": "2026-11-15T19:01:00Z",
  "source": {"scope": "crowdsecurity/community-blocklist", "value": ""},
  "decisions": [{"id": 41, "origin": "CAPI", "type": "ban", "scope": "Ip", "value": "198.51.100.1", "duration": "160h"}]}
]`
	csDecisions = `[
 {"id": 12, "source": {"scope": "Ip", "value": "203.0.113.7", "cn": "NL", "as_name": "Example BV"},
  "decisions": [{"id": 40, "origin": "crowdsec", "type": "ban", "scope": "Ip", "value": "203.0.113.7", "duration": "3h59m", "scenario": "crowdsecurity/ssh-bf"}]},
 {"id": 14, "source": {"scope": "Ip", "value": "198.51.100.9"},
  "decisions": [{"id": 42, "origin": "cscli", "type": "ban", "scope": "Ip", "value": "198.51.100.9", "duration": "-3s", "scenario": "hand"}]}
]`
	csMetrics = `{"acquisition": {"journalctl:journalctl-_SYSTEMD_UNIT=ssh.service": {"reads": 120, "parsed": 118, "unparsed": 2, "pour": 6}},
 "decisions": {"crowdsecurity/ssh-bf": {"crowdsec": {"ban": 1}}, "http:scan": {"CAPI": {"ban": 15000}}, "firehol": {"lists": {"ban": 250}}}}`
	csCollections = `{"collections": [{"name": "crowdsecurity/sshd", "status": "enabled", "local_version": "0.9"}, {"name": "crowdsecurity/linux", "status": "enabled", "local_version": "0.4"}]}`
	csBouncers    = `[{"name": "cs-firewall-bouncer-1", "ip_address": "127.0.0.1", "type": "crowdsec-firewall-bouncer", "version": "v0.0.31", "valid": true, "last_pull": "2026-11-15T19:02:00Z"}]`
	csDocker      = `[{"Name": "/cf-npm", "Config": {"Image": "jc21/nginx-proxy-manager:latest"},
  "Mounts": [{"Source": "/opt/cf-npm/data", "Destination": "/data"}, {"Source": "/opt/cf-npm/letsencrypt", "Destination": "/etc/letsencrypt"}]}]`
)

func crowdSecAgent(t *testing.T) (*Agent, *[]string) {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{cscliPath, bouncerPath, "/var/www/nextcloud/data/nextcloud.log"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "/etc/crowdsec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, capiFile), []byte("url: https://api.crowdsec.net/\nlogin: abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	a := &Agent{Root: root, Log: slog.New(slog.DiscardHandler)}
	a.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmd := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, cmd)
		switch {
		case cmd == "cscli version":
			return []byte("2026/11/15 version: v1.6.4-debian-pragmatic-amd64-523164f6\nCodename: alphaga\n"), nil
		case strings.HasPrefix(cmd, "cscli alerts list"):
			return []byte(csAlerts), nil
		case cmd == "cscli decisions list -o json":
			return []byte(csDecisions), nil
		case cmd == "cscli metrics -o json":
			return []byte(csMetrics), nil
		case cmd == "cscli collections list -o json":
			return []byte(csCollections), nil
		case cmd == "cscli bouncers list -o json":
			return []byte(csBouncers), nil
		case cmd == "docker ps -q --no-trunc":
			return []byte("abc\n"), nil
		case cmd == "docker inspect abc":
			return []byte(csDocker), nil
		case strings.HasPrefix(cmd, "systemctl is-active"):
			return []byte("active\n"), nil
		case strings.HasPrefix(cmd, "cscli decisions delete"):
			return []byte("level=info msg=\"2 decision(s) deleted\"\n"), nil
		case strings.HasPrefix(cmd, "cscli decisions add"):
			return []byte("Decision successfully added\n"), nil
		case strings.HasPrefix(cmd, "cscli console enroll"):
			if strings.Contains(cmd, "badkey123") {
				return nil, errors.New("cscli: could not enroll with key badkey123: 403")
			}
			return []byte("Watcher successfully enrolled. Visit https://app.crowdsec.net to accept it.\n"), nil
		case cmd == "systemctl restart crowdsec":
			return nil, nil
		}
		t.Errorf("onverwacht commando %q", cmd)
		return nil, nil
	}
	return a, &calls
}

func csRequest(t *testing.T, req protocol.CrowdSecRequest) []byte {
	t.Helper()
	body, _ := json.Marshal(req)
	data, _ := json.Marshal(protocol.Envelope{V: protocol.Version, Type: protocol.TypeCrowdSecRequest, TS: time.Now(), Body: body})
	return data
}

func TestCrowdSecStatus(t *testing.T) {
	a, _ := crowdSecAgent(t)
	res := a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpStatus,
		Probes: []string{"/var/www/*/data/nextcloud.log", "/var/log/nginx/*.log", "relatief/*"}}))
	if res.Error != "" || res.Status == nil {
		t.Fatalf("antwoord %+v", res)
	}
	st := res.Status
	if !st.Installed || st.Version != "v1.6.4" || st.Active != "active" || st.Bouncer != "active" || !st.CAPI {
		t.Errorf("stand %+v", st)
	}
	if len(st.Collections) != 2 || st.Collections[0].Name != "crowdsecurity/linux" {
		t.Errorf("collections %+v", st.Collections)
	}
	if len(st.Bouncers) != 1 || !st.Bouncers[0].Valid || st.Bouncers[0].LastPull.IsZero() {
		t.Errorf("bouncers %+v", st.Bouncers)
	}
	if len(st.Sources) != 1 || st.Sources[0].Parsed != 118 || st.Sources[0].Poured != 6 {
		t.Errorf("bronnen %+v", st.Sources)
	}
	if len(st.Containers) != 1 || st.Containers[0].Name != "cf-npm" || st.Containers[0].Mounts["/data"] != "/opt/cf-npm/data" {
		t.Errorf("containers %+v", st.Containers)
	}
	if !slices.Equal(st.Probes["/var/www/*/data/nextcloud.log"], []string{"/var/www/nextcloud/data/nextcloud.log"}) ||
		len(st.Probes["/var/log/nginx/*.log"]) != 0 {
		t.Errorf("probes %+v", st.Probes)
	}
	if _, ok := st.Probes["relatief/*"]; ok {
		t.Error("relatieve glob niet negeren")
	}
	if len(st.Errors) != 0 {
		t.Errorf("fouten %v", st.Errors)
	}
}

func TestCrowdSecStatusNotInstalled(t *testing.T) {
	a, _ := crowdSecAgent(t)
	if err := os.Remove(filepath.Join(a.Root, cscliPath)); err != nil {
		t.Fatal(err)
	}
	res := a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpStatus}))
	if res.Status == nil || res.Status.Installed || len(res.Status.Containers) != 1 {
		t.Fatalf("antwoord %+v", res)
	}
	res = a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpPoll}))
	if res.Error == "" {
		t.Error("poll zonder CrowdSec moet falen")
	}
}

func TestCrowdSecPoll(t *testing.T) {
	a, calls := crowdSecAgent(t)
	res := a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpPoll, Since: time.Now().Add(-10 * time.Minute)}))
	if res.Error != "" || res.Poll == nil {
		t.Fatalf("antwoord %+v", res)
	}
	p := res.Poll
	if len(p.Alerts) != 1 || p.Alerts[0].ID != 12 || p.Alerts[0].Scope != "ip" || p.Alerts[0].Country != "NL" || p.Alerts[0].Decisions != 1 {
		t.Errorf("alerts %+v", p.Alerts)
	}
	if len(p.Decisions) != 1 || p.Decisions[0].Value != "203.0.113.7" || time.Until(p.Decisions[0].Until) < 3*time.Hour {
		t.Errorf("bans %+v", p.Decisions)
	}
	if p.Blocklist != 15250 {
		t.Errorf("blocklist %d", p.Blocklist)
	}
	if !slices.Contains(*calls, "cscli alerts list -o json --since 11m --limit 1000") {
		t.Errorf("aanroepen %v", *calls)
	}
}

func TestCrowdSecDecide(t *testing.T) {
	a, calls := crowdSecAgent(t)
	res := a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpDecide,
		Decide: &protocol.CrowdSecDecision{Scope: "ip", Value: "203.0.113.7", Duration: "4h", Reason: "cf:crowdsecurity/ssh-bf@nc1"}}))
	if res.Error != "" || res.Count != 1 {
		t.Fatalf("antwoord %+v", res)
	}
	if want := "cscli decisions add --ip 203.0.113.7 --duration 4h --type ban --reason cf:crowdsecurity/ssh-bf@nc1"; !slices.Contains(*calls, want) {
		t.Errorf("aanroepen %v", *calls)
	}
	res = a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpDecide,
		Decide: &protocol.CrowdSecDecision{Delete: true, Scope: "range", Value: "203.0.113.0/24"}}))
	if res.Error != "" || res.Count != 2 {
		t.Fatalf("antwoord %+v", res)
	}
	for _, bad := range []protocol.CrowdSecDecision{
		{Scope: "ip", Value: "1.2.3.4; rm -rf /", Duration: "4h"},
		{Scope: "ip", Value: "1.2.3.4", Duration: "4 hours"},
		{Scope: "ip", Value: "1.2.3.4", Duration: "4h", Reason: "--type captcha;x"},
		{Scope: "country", Value: "NL", Duration: "4h"},
	} {
		res = a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpDecide, Decide: &bad}))
		if res.Error == "" {
			t.Errorf("%+v moet geweigerd worden", bad)
		}
	}
}

func TestCrowdSecEnroll(t *testing.T) {
	a, calls := crowdSecAgent(t)
	res := a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpEnroll,
		Enroll: &protocol.CrowdSecEnroll{Key: "testkey0123456789", Name: "nc1", Overwrite: true}}))
	if res.Error != "" || res.Count != 1 {
		t.Fatalf("antwoord %+v", res)
	}
	want := []string{"cscli console enroll --name nc1 --overwrite testkey0123456789", "systemctl restart crowdsec"}
	if !slices.Equal(*calls, want) {
		t.Errorf("commando's %q", *calls)
	}
	res = a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpEnroll,
		Enroll: &protocol.CrowdSecEnroll{Key: "badkey123", Name: "nc1"}}))
	if res.Error == "" || strings.Contains(res.Error, "badkey123") {
		t.Errorf("fout %q: moet er zijn, zonder sleutel", res.Error)
	}
	for _, e := range []protocol.CrowdSecEnroll{{Key: "kort", Name: "nc1"}, {Key: "testkey0123456789", Name: "-x"}, {Key: "a;rm -rf /x", Name: "nc1"}} {
		if res := a.handleCrowdSec(csRequest(t, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpEnroll, Enroll: &e})); res.Error == "" {
			t.Errorf("%+v werd aanvaard", e)
		}
	}
}
