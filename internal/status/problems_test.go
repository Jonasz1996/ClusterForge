package status

import (
	"slices"
	"testing"
)

func TestParseReasonRoundTrip(t *testing.T) {
	r := Node(NodeInput{
		HasAgent: true, HeartbeatAge: 1, DiskUsedRatio: 0.95, DiskUsedMount: "/",
		Services:        map[string]string{"rsyslog": "inactive", "nginx": "auto-restart", "cf-vaultx": "failed", "cf-zabbix-proxy-check": "failed", "ok": "active"},
		EnabledServices: []string{"rsyslog"},
	})
	svc, other := ParseReason(r.Reason)
	want := []ServiceProblem{{"cf-vaultx", ServiceFailed}, {"nginx", ServiceCrashing}, {"rsyslog", ServiceStopped}}
	if !slices.Equal(svc, want) {
		t.Fatalf("diensten = %v, wil %v (reden %q)", svc, want, r.Reason)
	}
	if len(other) != 2 {
		t.Fatalf("andere = %v", other)
	}
	if got := svc[2].Text(); got != "rsyslog draait niet" {
		t.Fatalf("tekst = %q", got)
	}
	if s, o := ParseReason(""); s != nil || o != nil {
		t.Fatal("lege reden")
	}
}
