package protocol

import (
	"slices"
	"strings"
	"testing"
)

const userRules = `*filter
:ufw-user-input - [0:0]
### RULES ###

### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in comment=63663a737368
-A ufw-user-input -p tcp --dport 22 -j ACCEPT

### tuple ### allow tcp 2379:2380 0.0.0.0/0 any 192.168.103.243 in
-A ufw-user-input -p tcp --dport 2379:2380 -s 192.168.103.243 -j ACCEPT

### tuple ### allow vrrp any 0.0.0.0/0 any 192.168.0.0/24 in
### tuple ### allow any 22 0.0.0.0/0 any 0.0.0.0/0 OpenSSH - in
### tuple ### deny_log tcp 25 0.0.0.0/0 any 0.0.0.0/0 in_eth0
### END RULES ###
COMMIT`

const user6Rules = `### tuple ### allow tcp 22 ::/0 any ::/0 in comment=63663a737368`

func TestParseUfwTuples(t *testing.T) {
	rs := ParseUfwTuples(userRules, user6Rules)
	if len(rs) != 5 {
		t.Fatalf("regels: %+v", rs)
	}
	if rs[0].Comment != "cf:ssh" || !rs[0].Owned() || rs[0].From != "any" || rs[0].Port != "22" {
		t.Errorf("ssh: %+v", rs[0])
	}
	if rs[1].From != "192.168.103.243" || rs[1].Port != "2379:2380" || rs[1].Owned() {
		t.Errorf("etcd: %+v", rs[1])
	}
	if rs[2].Proto != "vrrp" || rs[2].Port != "" || rs[2].From != "192.168.0.0/24" {
		t.Errorf("vrrp: %+v", rs[2])
	}
	if rs[3].App != "OpenSSH" {
		t.Errorf("app: %+v", rs[3])
	}
	if rs[4].Action != "deny" || rs[4].Iface != "eth0" || rs[4].Direction != "in" {
		t.Errorf("deny: %+v", rs[4])
	}
	if got := strings.Join(rs[4].Args(), " "); got != "deny in on eth0 proto tcp from any to any port 25" {
		t.Errorf("args: %s", got)
	}
}

func TestDiffFirewall(t *testing.T) {
	cur := ParseUfwTuples(userRules, user6Rules)
	want := []FirewallRule{
		{Action: "allow", Proto: "tcp", Port: "22", From: "any", Comment: "cf:ssh"},
		{Action: "allow", Proto: "tcp", Port: "2379:2380", From: "192.168.103.243/32", Comment: "cf:etcd (cluster)"},
		{Action: "allow", Proto: "vrrp", From: "192.168.103.243", Comment: "cf:vrrp"},
	}
	d := DiffFirewall(cur, want, false)
	if len(d.Keep) != 1 || len(d.Adopt) != 1 || len(d.Add) != 1 {
		t.Fatalf("diff: %+v", d)
	}
	if len(d.Remove) != 0 || len(d.Foreign) != 3 {
		t.Fatalf("niet strikt: %+v", d)
	}
	d = DiffFirewall(cur, want, true)
	// De app-regel blijft altijd.
	if len(d.Remove) != 2 || len(d.Foreign) != 1 || d.Foreign[0].App != "OpenSSH" {
		t.Fatalf("strikt: %+v", d)
	}
	if DiffFirewall(cur, want, false).Empty() {
		t.Error("leeg")
	}
}

func TestFirewallRuleValidate(t *testing.T) {
	ok := []FirewallRule{
		{Action: "allow", Proto: "tcp", Port: "22", From: "any", Comment: "cf:ssh"},
		{Action: "allow", Proto: "udp", Port: "4567", From: "10.0.0.0/8", Comment: "cf:galera (cluster)"},
		{Action: "allow", Proto: "vrrp", From: "192.168.103.243", Comment: "cf:vrrp"},
	}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := []FirewallRule{
		{Action: "deny", Proto: "tcp", Port: "22", From: "any", Comment: "cf:x"},
		{Action: "allow", Proto: "tcp", Port: "0", From: "any", Comment: "cf:x"},
		{Action: "allow", Proto: "tcp", Port: "80;rm", From: "any", Comment: "cf:x"},
		{Action: "allow", Proto: "tcp", Port: "80", From: "any", Comment: "geen prefix"},
		{Action: "allow", Proto: "tcp", Port: "80", From: "any", Comment: "cf:'quote"},
		{Action: "allow", Proto: "vrrp", Port: "1", From: "any", Comment: "cf:x"},
		{Action: "allow", Proto: "tcp", Port: "80", From: "host", Comment: "cf:x"},
		{Action: "allow", Proto: "tcp", Port: "90:80", From: "any", Comment: "cf:x"},
	}
	for _, r := range bad {
		if r.Validate() == nil {
			t.Errorf("%+v: geen fout", r)
		}
	}
	args := ok[2].Args()
	if !slices.Equal(args, []string{"allow", "proto", "vrrp", "from", "192.168.103.243", "to", "any"}) {
		t.Errorf("vrrp: %v", args)
	}
}
