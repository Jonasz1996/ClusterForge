package httpapi

import (
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/aiverslag"
)

func TestDashboardAIVFrom(t *testing.T) {
	if d := dashboardAIVFrom(nil); d.OK || d.Level != "onbekend" || d.Reason == "" {
		t.Fatalf("leeg: %+v", d)
	}
	sc := 82.0
	sums := []aiverslag.Summary{{
		Instance: aiverslag.Instance{Hostname: "LokaleAI-1"},
		Niveau:   "verhoogd", Redenen: []string{"poortscan op WAN"}, Score: &sc,
		Incidenten: 1, OpenHoog: 2, Unseen: 1, LastRun: 100,
		Warnings: []aiverslag.Warning{{Level: "warning", Text: "x"}},
	}}
	d := dashboardAIVFrom(sums)
	if d.OK || d.Level != "verhoogd" || d.LevelLabel != "Verhoogd" || d.Reason != "poortscan op WAN" {
		t.Fatalf("verhoogd: %+v", d)
	}
	if d.Score == nil || *d.Score != 82 || d.Incidents != 1 || d.OpenHigh != 2 || d.Unseen != 1 || d.Warnings != 1 || d.Machine != "LokaleAI-1" {
		t.Fatalf("cijfers: %+v", d)
	}
	sums[0].Niveau = "waakzaam"
	if d := dashboardAIVFrom(sums); !d.OK {
		t.Fatalf("waakzaam hoort ok: %+v", d)
	}
	sums[0].Niveau, sums[0].Error = "", "agent offline"
	if d := dashboardAIVFrom(sums); d.OK || d.Reason == "" {
		t.Fatalf("fout: %+v", d)
	}
}
