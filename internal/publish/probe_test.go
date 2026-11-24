package publish

import (
	"testing"
	"time"
)

func TestOtherApps(t *testing.T) {
	mk := func(id int, name, svc string) HostView {
		return HostView{ID: id, Enabled: true, Domains: []string{name + ".jbogaert.be"}, ForwardScheme: "http", ForwardHost: "192.168.114.160", ForwardPort: 80, ServiceName: svc}
	}
	hosts := []HostView{mk(1, "cssthema", "cssthema"), mk(2, "wordpress", "cssthema"), mk(3, "media", "cssthema"),
		{ID: 4, Enabled: true, Domains: []string{"home.jbogaert.be"}, ForwardHost: "10.0.0.1", ForwardPort: 80}}
	otherApps(hosts, Probes{"http://192.168.114.160:80/": {Status: 200, Title: "cssthema", At: time.Now()}})
	if hosts[0].OtherApp != "" || hosts[1].OtherApp != "cssthema" || hosts[2].OtherApp != "cssthema" || hosts[3].OtherApp != "" {
		t.Fatalf("andere dienst: %q %q %q %q", hosts[0].OtherApp, hosts[1].OtherApp, hosts[2].OtherApp, hosts[3].OtherApp)
	}
	// Geen enkele naam past (algemene titel): niets markeren.
	hosts = []HostView{mk(1, "a1", ""), mk(2, "b2", "")}
	otherApps(hosts, Probes{"http://192.168.114.160:80/": {Status: 200, Title: "Login", At: time.Now()}})
	if hosts[0].OtherApp != "" || hosts[1].OtherApp != "" {
		t.Fatal("algemene titel gaf toch een melding")
	}
}
