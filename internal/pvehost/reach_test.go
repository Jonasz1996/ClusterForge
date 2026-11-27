package pvehost

import (
	"slices"
	"testing"
)

func TestReachList(t *testing.T) {
	got := ReachList("http://192.168.113.153:8080", "tls://192.168.113.153:4222")
	if !slices.Equal(got, []string{"192.168.113.153:8080", "192.168.113.153:4222"}) {
		t.Fatalf("%v", got)
	}
	got = ReachList("https://cf.lan/", "tls://cf.lan")
	if !slices.Equal(got, []string{"cf.lan:443", "cf.lan:4222"}) {
		t.Fatalf("%v", got)
	}
	if got := ReachList("geen url", ""); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
