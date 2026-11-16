package security

import (
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestToolDrafts(t *testing.T) {
	end := time.Now()
	run := func(items ...string) map[string]protocol.ToolRunState {
		return map[string]protocol.ToolRunState{protocol.ToolRun: {Ended: &end, Items: items}}
	}
	states := []protocol.ToolState{
		{Tool: protocol.ToolClamAV, Installed: true, Last: run()},
		{Tool: protocol.ToolAIDE, Installed: true, Last: run("gewijzigd /etc/passwd")},
		{Tool: protocol.ToolChkrootkit, Installed: true, Last: run("Checking `bindshell'... INFECTED (PORTS:  465)")},
		{Tool: protocol.ToolRkhunter, Installed: false, Last: run("Warning: x")},
		{Tool: protocol.ToolTrivy, Installed: true, Last: map[string]protocol.ToolRunState{protocol.ToolRun: {Ended: &end, Images: []protocol.TrivyImage{
			{Image: "nginx:1.27", Counts: map[string]int{"high": 2}, Vulns: []protocol.TrivyVuln{{ID: "CVE-1", Pkg: "openssl"}}},
			{Image: "redis:8", Counts: map[string]int{}},
		}}}},
	}
	d := ToolDrafts(states)
	got := map[string]string{}
	for _, x := range d {
		got[x.Key] = x.Severity
	}
	want := map[string]string{"aide": SevMedium, "chkrootkit": SevCritical, "image:nginx:1.27": SevHigh}
	if len(got) != len(want) {
		t.Fatalf("drafts = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, wil %q", k, got[k], v)
		}
	}
}
