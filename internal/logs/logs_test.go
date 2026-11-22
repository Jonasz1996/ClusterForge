package logs

import (
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestMerge(t *testing.T) {
	at := func(s int) *time.Time {
		v := time.Date(2026, 11, 16, 12, 0, s, 0, time.UTC)
		return &v
	}
	per := [][]Entry{
		{{Source: 0, TS: at(1), Msg: "a1"}, {Source: 0, TS: at(1), Inherited: true, Msg: "a1-vervolg"}, {Source: 0, TS: at(4), Msg: "a4"}},
		{{Source: 1, TS: at(1), Msg: "b1"}, {Source: 1, TS: at(3), Msg: "b3"}},
		{{Source: 2, Msg: "zonder tijd"}},
	}
	got := merge(per)
	var msgs []string
	for _, e := range got {
		msgs = append(msgs, e.Msg)
	}
	want := []string{"zonder tijd", "a1", "a1-vervolg", "b1", "b3", "a4"}
	if len(msgs) != len(want) {
		t.Fatalf("%v", msgs)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Fatalf("%v, wilde %v", msgs, want)
		}
	}
}

func TestCheckQuery(t *testing.T) {
	src := Source{Kind: protocol.LogKindJournal, Name: "nginx.service"}
	if err := CheckQuery(Query{Sources: []Source{src, src}}); err == nil {
		t.Fatal("dubbele bron aanvaard")
	}
	many := make([]Source, MaxSources+1)
	for i := range many {
		many[i] = Source{Kind: protocol.LogKindJournal, Name: "u" + string(rune('a'+i)) + ".service"}
	}
	if err := CheckQuery(Query{Sources: many}); err == nil {
		t.Fatal("te veel bronnen aanvaard")
	}
	if err := CheckQuery(Query{Sources: []Source{src}, Filter: protocol.LogFilter{Limit: 9999}}); err == nil {
		t.Fatal("te hoge limiet aanvaard")
	}
	if err := CheckQuery(Query{Sources: []Source{src}, Filter: protocol.LogFilter{Levels: []string{"error", ""}}}); err != nil {
		t.Fatal(err)
	}
}
