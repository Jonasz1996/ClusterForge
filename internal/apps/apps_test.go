package apps

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func facts(t *testing.T, f protocol.Facts) []byte {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	app := func(role, version string, ok bool, at time.Time) []protocol.AppStatus {
		return []protocol.AppStatus{{App: "media-platform", Title: "Mediaplatform", Role: role, Version: version, UpdatedAt: at,
			Checks: []protocol.AppCheck{{ID: "mount", Label: "Bestanden gemount", OK: ok, Severity: "red", Detail: "niet gemount"}}}}
	}
	sw := func(leader bool, bytes uint64) *protocol.SeaweedFacts {
		s := &protocol.SeaweedFacts{Volume: &protocol.SeaweedVolume{Bytes: bytes, Volumes: 10},
			Processes: []protocol.SeaweedProcess{{Role: "volume", Disks: []protocol.SeaweedDisk{{Dir: "/data", Total: 1000, Free: 400}}}}}
		if leader {
			s.Master = &protocol.SeaweedMaster{IsLeader: true, VolumeCount: 416, UnderReplicated: 2, LogicalBytes: 216,
				Replications: map[string]int{"001": 400, "002": 16}, NodeBytes: map[string]uint64{"10.0.0.1:8081": 108}}
		}
		return s
	}
	nodes := []Node{
		{ID: uuid.New(), Hostname: "media1", Address: "10.0.0.1", Online: true, Active: true,
			Facts: facts(t, protocol.Facts{Apps: app("actief", "aa6fa8b0000000", true, now), Seaweed: sw(true, 1)})},
		{ID: uuid.New(), Hostname: "media2", Address: "10.0.0.2", Online: true, Active: true,
			Facts: facts(t, protocol.Facts{Apps: app("actief", "84ebbaa0000000", false, now.Add(-time.Minute)), Seaweed: sw(false, 166)})},
		{ID: uuid.New(), Hostname: "media3", Address: "10.0.0.3", Online: true, Active: true,
			Facts: facts(t, protocol.Facts{Apps: app("stand-by", "aa6fa8b0000000", true, now.Add(-time.Hour)), Seaweed: sw(false, 158)})},
	}
	o := Evaluate(nodes, now)
	if len(o.Apps) != 1 || len(o.Apps[0].Nodes) != 3 {
		t.Fatalf("apps: %+v", o.Apps)
	}
	var titles []string
	for _, i := range o.Apps[0].Issues {
		titles = append(titles, i.Tone+": "+i.Title)
	}
	all := strings.Join(titles, "\n")
	for _, want := range []string{"red: Bestanden gemount op media2", "red: Mediaplatform draait op meer dan één node",
		"amber: Mediaplatform: de nodes hebben niet dezelfde versie", "amber: Mediaplatform: geen recente stand van media3"} {
		if !strings.Contains(all, want) {
			t.Errorf("mist %q in\n%s", want, all)
		}
	}
	st := o.Storage
	if st == nil || st.Leader != "media1" || st.UnderReplicated != 2 || len(st.Nodes) != 3 || st.Nodes[0].Bytes != 108 || st.Nodes[1].Bytes != 166 ||
		st.Nodes[0].DiskFree != 400 || len(st.Issues) != 2 {
		t.Errorf("opslag: %+v", st)
	}
}

func TestEvaluateNoneActive(t *testing.T) {
	now := time.Now()
	n := Node{ID: uuid.New(), Hostname: "a", Online: true, Active: true,
		Facts: facts(t, protocol.Facts{Apps: []protocol.AppStatus{{App: "x", Role: "stand-by", UpdatedAt: now}}})}
	o := Evaluate([]Node{n}, now)
	if len(o.Apps[0].Issues) != 1 || o.Apps[0].Issues[0].Tone != "red" || o.Storage != nil {
		t.Errorf("%+v", o)
	}
}
