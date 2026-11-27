package mailrelay

import (
	"testing"

	"github.com/google/uuid"
)

func TestParse(t *testing.T) {
	st, ok := Parse([]string{"== iets", `CFPF {"queue":7,"deferred":3,"active":true,"vip":true,"relay":"closed","queue_warn":5}`})
	if !ok || st.Queue != 7 || st.Deferred != 3 || !st.Active || !st.VIP || st.Relay != "closed" || st.QueueWarn != 5 {
		t.Fatalf("%+v %v", st, ok)
	}
	if _, ok := Parse([]string{"niets"}); ok {
		t.Fatal("zonder CFPF")
	}
}

func TestGrowing(t *testing.T) {
	for _, c := range []struct {
		h    []int
		want bool
	}{{[]int{1, 2, 3}, false}, {[]int{1, 2, 3, 4}, true}, {[]int{1, 2, 2, 4}, false}, {[]int{5, 1, 2, 3}, false}} {
		if got := Growing(c.h); got != c.want {
			t.Errorf("%v: %v", c.h, got)
		}
	}
}

func TestNotices(t *testing.T) {
	s := &Service{status: map[uuid.UUID]NodeStatus{
		uuid.New(): {Hostname: "relay-01", Cluster: "Mail", Active: true, Relay: "open", QueueWarn: 20},
		uuid.New(): {Hostname: "relay-02", Cluster: "Mail", Active: true, Relay: "closed", Queue: 25, QueueWarn: 20},
		uuid.New(): {Hostname: "relay-03", Cluster: "Mail", Active: true, Relay: "closed", Queue: 4, Growing: true},
		uuid.New(): {Hostname: "relay-04", Cluster: "Mail", Error: "agent niet verbonden"},
	}}
	n := s.Notices()
	if len(n) != 3 {
		t.Fatalf("%+v", n)
	}
	levels := map[string]int{}
	for _, x := range n {
		levels[x.Level]++
	}
	if levels["crit"] != 1 || levels["warn"] != 2 {
		t.Fatalf("%+v", n)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatal(got)
	}
}
