package httpapi

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/opnsense"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestDashboardOpnsenseFrom(t *testing.T) {
	cl, other := uuid.New(), uuid.New()
	m, b := uuid.New(), uuid.New()
	f := &protocol.OPNsenseFacts{Version: "26.1.2"}
	cs := []opnsense.Cluster{
		{Name: "fw", ClusterID: &cl, Level: "warn", HA: true, Master: &m,
			Nodes:    []opnsense.Firewall{{NodeID: m, Hostname: "fw1", Online: true, Facts: f}, {NodeID: b, Hostname: "fw2", Online: false, Facts: f}},
			Problems: []opnsense.Problem{{Level: "warn", Text: "pfsync meldt geen synchronisatie"}},
			VIPs:     []opnsense.VIPRow{{Address: "192.168.10.1"}}},
		{Name: "los", ClusterID: &other, Level: "ok", Nodes: []opnsense.Firewall{{NodeID: uuid.New(), Hostname: "los", Online: true}}},
	}
	d := dashboardOpnsenseFrom(cs, nil)
	if d.OK || d.Level != "waarschuwing" || d.Clusters != 2 || d.Online != "2/3" || d.Master != "fw1" || d.VIPs != 1 || d.Version != "26.1.2" || len(d.Rows) != 2 || d.Rows[1].Master != "los" {
		t.Fatalf("%+v", d)
	}
	if d := dashboardOpnsenseFrom(cs, []uuid.UUID{other}); !d.OK || d.Clusters != 1 || d.Firewalls != 1 {
		t.Fatalf("token voor één cluster: %+v", d)
	}
	if d := dashboardOpnsenseFrom(nil, nil); d.OK || d.Level != "onbekend" {
		t.Fatalf("leeg: %+v", d)
	}
}
