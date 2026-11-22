package status

import (
	"slices"
	"testing"
)

func TestParseReasonRoundTrip(t *testing.T) {
	r := Node(NodeInput{
		HasAgent: true, HeartbeatAge: 1, DiskUsedRatio: 0.95, DiskUsedMount: "/",
		Services:        map[string]string{"ufw": "inactive", "nginx": "auto-restart", "cf-vaultx": "failed", "cf-zabbix-proxy-check": "failed", "ok": "active"},
		EnabledServices: []string{"ufw"},
	})
	svc, other := ParseReason(r.Reason)
	want := []ServiceProblem{{"cf-vaultx", ServiceFailed}, {"nginx", ServiceCrashing}, {"ufw", ServiceStopped}}
	if !slices.Equal(svc, want) {
		t.Fatalf("diensten = %v, wil %v (reden %q)", svc, want, r.Reason)
	}
	if len(other) != 2 {
		t.Fatalf("andere = %v", other)
	}
	if got := svc[2].Text(); got != "ufw draait niet" {
		t.Fatalf("tekst = %q", got)
	}
	if s, o := ParseReason(""); s != nil || o != nil {
		t.Fatal("lege reden")
	}
}
