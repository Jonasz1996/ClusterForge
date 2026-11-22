package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRisk(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	seen := map[string]bool{}
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("time") != fmt.Sprint(now.Unix()) {
			t.Errorf("onverwachte request %s %s", r.URL.Path, r.URL.RawQuery)
		}
		mu.Lock()
		seen[q] = true
		mu.Unlock()
		var result string
		switch {
		case strings.Contains(q, "node_cpu_seconds_total"):
			result = fmt.Sprintf(`{"metric":{"node_id":%q},"value":[1,"91.5"]},{"metric":{"node_id":"geen-uuid"},"value":[1,"50"]}`, a)
		case strings.Contains(q, "MemAvailable"):
			result = fmt.Sprintf(`{"metric":{"node_id":%q},"value":[1,"NaN"]},{"metric":{"node_id":%q},"value":[1,"42"]}`, a, b)
		case strings.Contains(q, "Swap"):
			result = ""
		case strings.Contains(q, "errs_total"):
			result = fmt.Sprintf(`{"metric":{"node_id":%q},"value":[1,"12"]}`, b)
		case strings.Contains(q, "deriv"):
			result = fmt.Sprintf(`{"metric":{"node_id":%q,"mountpoint":"/"},"value":[1,"9.5"]},`+
				`{"metric":{"node_id":%q,"mountpoint":"/var"},"value":[1,"+Inf"]},`+
				`{"metric":{"node_id":%q,"mountpoint":"/data"},"value":[1,"120"]}`, a, a, a)
		default:
			t.Errorf("onverwachte query %s", q)
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, result)
	}))
	defer vm.Close()

	out, err := NewClient(vm.URL).Risk(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(riskQueries) {
		t.Fatalf("%d queries, verwacht %d", len(seen), len(riskQueries))
	}
	if len(out) != 2 {
		t.Fatalf("nodes: %+v", out)
	}
	na, nb := out[a], out[b]
	if na.CPUP95 == nil || *na.CPUP95 != 91.5 || na.MemoryP95 != nil || na.SwapAvg != nil || na.NetErrors != nil {
		t.Fatalf("node a: %+v", na)
	}
	if len(na.DiskDaysLeft) != 2 || na.DiskDaysLeft["/"] != 9.5 || na.DiskDaysLeft["/data"] != 120 {
		t.Fatalf("schijf van a: %+v", na.DiskDaysLeft)
	}
	if nb.MemoryP95 == nil || *nb.MemoryP95 != 42 || nb.NetErrors == nil || *nb.NetErrors != 12 || nb.CPUP95 != nil || nb.DiskDaysLeft != nil {
		t.Fatalf("node b: %+v", nb)
	}

	// Een fout van VictoriaMetrics geeft een fout met de naam van de query.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "deriv") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":"error","error":"te veel series"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer bad.Close()
	if _, err := NewClient(bad.URL).Risk(context.Background(), now); err == nil || !strings.Contains(err.Error(), "disk_days: VictoriaMetrics: te veel series") {
		t.Fatalf("fout: %v", err)
	}
	if _, err := NewClient("").Risk(context.Background(), now); !errors.Is(err, ErrDisabled) {
		t.Fatalf("zonder VictoriaMetrics: %v", err)
	}
}
