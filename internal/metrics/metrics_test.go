package metrics

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestWriteLines(t *testing.T) {
	prefix := labelPrefix(`web"01`, "id-1", "lb", "", "prod")
	samples := []protocol.Sample{
		{Name: "node_load1", Value: 0.5},
		{Name: "node_filesystem_size_bytes", Value: 1e10, Labels: map[string]string{
			"mountpoint": `/data "x"`, "device": "/dev/sda1",
			// Labels van de server en ongeldige namen vallen weg.
			"node": "vals", "cluster_id": "vals", "__name__": "x", "1bad": "x",
		}},
		{Name: "process_cpu_seconds_total", Value: 1}, // geen node_-metric
		{Name: "node_load5", Value: math.NaN()},
		{Name: "node_load15", Value: math.Inf(1)},
	}
	var buf bytes.Buffer
	writeLines(&buf, prefix, samples, time.UnixMilli(1700000000123))
	want := `node_load1{job="clusterforge",instance="web\"01",node="web\"01",node_id="id-1",cluster="lb",cluster_id="",env="prod"} 0.5 1700000000123
node_filesystem_size_bytes{job="clusterforge",instance="web\"01",node="web\"01",node_id="id-1",cluster="lb",cluster_id="",env="prod",device="/dev/sda1",mountpoint="/data \"x\""} 1e+10 1700000000123
`
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestDiskUsage(t *testing.T) {
	fs := func(mount string, size, free, avail float64) []protocol.Sample {
		l := map[string]string{"mountpoint": mount}
		return []protocol.Sample{
			{Name: "node_filesystem_size_bytes", Labels: l, Value: size},
			{Name: "node_filesystem_free_bytes", Labels: l, Value: free},
			{Name: "node_filesystem_avail_bytes", Labels: l, Value: avail},
		}
	}
	var samples []protocol.Sample
	samples = append(samples, fs("/", 100, 50, 45)...)     // 50 / 95
	samples = append(samples, fs("/var", 100, 8, 3)...)    // 92 / 95
	samples = append(samples, fs("/empty", 0, 0, 0)...)    // telt niet
	samples = append(samples, fs("/boot", 100, 90, 90)...) // 10 / 100
	ratio, mount := diskUsage(samples)
	if mount != "/var" || math.Abs(ratio-92.0/95.0) > 1e-9 {
		t.Errorf("diskUsage = %v %q", ratio, mount)
	}
	if r, m := diskUsage(nil); r != 0 || m != "" {
		t.Errorf("leeg: %v %q", r, m)
	}
}

func TestStepFor(t *testing.T) {
	for rng, want := range map[time.Duration]time.Duration{
		time.Hour: 15 * time.Second, 6 * time.Hour: 90 * time.Second,
		24 * time.Hour: 6 * time.Minute, 7 * 24 * time.Hour: 42 * time.Minute,
	} {
		if got := stepFor(rng); got != want {
			t.Errorf("stepFor(%v) = %v, want %v", rng, got, want)
		}
	}
}

func TestQuery(t *testing.T) {
	var queries []string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, q.Get("query"))
		if q.Get("step") != "15s" {
			t.Errorf("step = %s", q.Get("step"))
		}
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		// Twee lijnen, door elkaar; één met een gat en een ongeldige waarde.
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"device":"eth1"},"values":[[%d,"1"],[%d,"NaN"],[%d,"3"]]},
			{"metric":{"device":"eth0"},"values":[[%d,"2"]]}]}}`, start, start+15, start+30, start+15)
	}))
	defer vm.Close()

	c := NewClient(vm.URL)
	now := time.Unix(1700000000, 0)
	panels := []Panel{{ID: "net", Title: "Netwerk", Unit: UnitBytesPerSecond, Queries: []PanelQuery{
		{`rate(x{$sel}[$w])`, "{device} in"},
	}}}
	res, err := c.Query(context.Background(), panels, `node_id="n1"`, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || queries[0] != `rate(x{node_id="n1"}[60s])` {
		t.Errorf("queries = %q", queries)
	}
	if len(res.Timestamps) != 241 || res.Timestamps[0] != res.Start.Unix() {
		t.Fatalf("timestamps: %d", len(res.Timestamps))
	}
	s := res.Panels[0].Series
	if len(s) != 2 || s[0].Label != "eth0 in" || s[1].Label != "eth1 in" {
		t.Fatalf("series: %+v", s)
	}
	val := func(p *float64) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprint(*p)
	}
	got := []string{val(s[1].Values[0]), val(s[1].Values[1]), val(s[1].Values[2]), val(s[0].Values[1])}
	if strings.Join(got, ",") != "1,nil,3,2" {
		t.Errorf("waarden = %v", got)
	}

	if _, err := NewClient("").Query(context.Background(), panels, "", time.Hour, now); err != ErrDisabled {
		t.Errorf("zonder URL: %v", err)
	}
}
