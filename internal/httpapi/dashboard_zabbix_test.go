package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/zabbixmon"
)

func TestDashboardZabbixFrom(t *testing.T) {
	now := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)
	db, media := uuid.New(), uuid.New()
	dbNode := &zabbixmon.NodeLink{Hostname: "DatabaseClusterNode1", ClusterID: &db}
	mediaNode := &zabbixmon.NodeLink{Hostname: "plex1", ClusterID: &media}
	nvps := 184.6
	o := zabbixmon.Overview{Configured: true, UpdatedAt: &now, State: zabbixmon.State{Level: "probleem", Reasons: []string{"1 probleem met ernst hoog"}}}
	o.Hosts = []zabbixmon.Host{
		{HostID: "1", Enabled: true, Available: zabbixmon.AvailOK, Node: dbNode},
		{HostID: "2", Enabled: true, Available: zabbixmon.AvailDown, Node: mediaNode},
		{HostID: "3", Enabled: true, Available: zabbixmon.AvailOK},
	}
	o.Problems = []zabbixmon.Problem{
		{Name: "Galera", Severity: 3, Since: now.Add(-time.Hour), Hosts: []zabbixmon.HostRef{{HostID: "1", Name: "db1", Node: dbNode}}},
		{Name: "Plex weg", Severity: 4, Since: now.Add(-time.Minute), Hosts: []zabbixmon.HostRef{{HostID: "2", Name: "plex1"}}},
		{Name: "stil", Severity: 5, Suppressed: true, Hosts: []zabbixmon.HostRef{{HostID: "1"}}},
	}
	o.Proxies = []zabbixmon.Proxy{{Name: "p", State: "online"}}
	o.HA = []zabbixmon.HANode{{Name: "zbx1", Status: "active"}, {Name: "zbx2", Status: "standby"}}
	o.Health = []zabbixmon.Health{{NVPS: &nvps}}

	all := dashboardZabbixFrom(o, nil)
	if all.OK || all.Level != "probleem" || all.LevelLabel != "Probleem" || all.Problems != 2 || all.High != 1 || all.Average != 1 ||
		all.Available != "2/3" || all.HostsDown != 1 || all.Proxies != "1/1 online" || all.HAActive != "zbx1" || all.NVPS == nil ||
		len(all.Top) != 2 || all.Top[0].Name != "Plex weg" || all.Top[1].Host != "DatabaseClusterNode1" {
		t.Errorf("alles: %+v", all)
	}
	part := dashboardZabbixFrom(o, []uuid.UUID{db})
	if !part.OK || part.Level != "waarschuwing" || part.Problems != 1 || part.Available != "1/1" || part.Proxies != "" || part.NVPS != nil {
		t.Errorf("databasecluster: %+v", part)
	}
}
