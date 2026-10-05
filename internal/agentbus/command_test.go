package agentbus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestSummarizeStepsHidesContent(t *testing.T) {
	on := true
	steps := []protocol.Step{
		{Package: &protocol.PackageStep{Names: []string{"keepalived"}}},
		{File: &protocol.FileStep{Path: "/etc/keepalived/keepalived.conf", Content: "auth_pass geheim12\n", Mode: "0640"}},
		{Service: &protocol.ServiceStep{Name: "keepalived", Enabled: &on, State: "restarted"}},
		{User: &protocol.UserStep{Name: "app", System: true}},
		{Directory: &protocol.DirectoryStep{Path: "/srv/app", Owner: "app"}},
		{Command: &protocol.CommandStep{Run: "echo geheim12 | chpasswd", Unless: "grep -q geheim12 /etc/x", Creates: "/srv/app/.done"}},
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	got := SummarizeSteps(steps, key)
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "geheim12") || strings.Contains(string(b), "chpasswd") {
		t.Fatalf("inhoud of commando in de samenvatting: %s", b)
	}
	kinds := []string{}
	for _, s := range got {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "package,file,service,user,directory,command" {
		t.Fatalf("soorten: %v", kinds)
	}
	if got[1].Path != "/etc/keepalived/keepalived.conf" || len(got[1].Fingerprint) != 64 || got[2].Unit != "keepalived" ||
		got[0].Packages[0] != "keepalived" || got[5].Creates != "/srv/app/.done" || got[3].User != "app" {
		t.Fatalf("samenvatting mist velden: %s", b)
	}
	other := SummarizeSteps(steps[1:2], []byte("anders6789abcdef0123456789abcdef"))
	if other[0].Fingerprint == got[1].Fingerprint {
		t.Fatal("een andere sleutel geeft dezelfde vingerafdruk")
	}
	if SummarizeSteps(steps[1:2], nil)[0].Fingerprint != "" {
		t.Fatal("zonder sleutel hoort er geen vingerafdruk te zijn")
	}
}

func TestChanges(t *testing.T) {
	for _, a := range []string{protocol.CmdReboot, protocol.CmdShutdown, protocol.CmdMaintenanceEnter, protocol.CmdMaintenanceExit, protocol.CmdApply, "iets.nieuws"} {
		if !changes(a) {
			t.Errorf("%s hoort in het logboek", a)
		}
	}
	if changes(protocol.CmdFactsCollect) {
		t.Error("facts.collect leest alleen")
	}
}
