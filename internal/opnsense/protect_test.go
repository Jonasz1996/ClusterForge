package opnsense

import (
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestSecRules(t *testing.T) {
	p, err := check([]protocol.OPNCall{
		{Path: "system/user/add", Body: map[string]any{"user": map[string]any{"name": "jonas", "password": "x"}}},
		{Path: "ipsec/pre_shared_keys/addItem", Body: map[string]any{"preSharedKey": map[string]any{"ident": "a", "Key": "geheim"}}},
		{Path: "trust/cert/add", Body: map[string]any{"cert": map[string]any{"descr": "web", "prv_payload": "-----BEGIN"}}},
	})
	if err != nil || p.Level != LevelAccess {
		t.Fatal(err, p.Level)
	}
	want := map[string]bool{"user.password": true, "preSharedKey.Key": true, "cert.prv_payload": true}
	for _, sp := range p.Secret {
		delete(want, sp)
	}
	if len(want) > 0 {
		t.Fatalf("niet geheim: %v (wel: %v)", want, p.Secret)
	}
	if !isProtected("user", " ClusterForge") || !isProtected("group", "admins") || isProtected("user", "jonas") {
		t.Fatal("beschermd")
	}
}
