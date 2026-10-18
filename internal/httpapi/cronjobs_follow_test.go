package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
)

// followNode speelt systemd na zoals managedNode, en stopt een lopende
// service bij systemctl stop.
type followNode struct {
	managedNode
	stops sync.Map
}

func (h *followNode) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) == 2 && args[0] == "stop" && strings.HasPrefix(args[1], "cf-cron@") {
		id := strings.TrimSuffix(strings.TrimPrefix(args[1], "cf-cron@"), ".service")
		cancel, ok := h.stops.Load(id)
		if ok {
			cancel.(context.CancelFunc)()
		}
		return nil, nil
	}
	if name == "systemctl" && len(args) == 3 && args[0] == "start" && strings.HasPrefix(args[2], "cf-cron@") {
		id := strings.TrimSuffix(strings.TrimPrefix(args[2], "cf-cron@"), ".service")
		rctx, cancel := context.WithCancel(context.Background())
		h.stops.Store(id, cancel)
		h.wg.Go(func() {
			defer cancel()
			h.runner().Main(rctx, id)
		})
		return nil, nil
	}
	return h.managedNode.exec(ctx, name, args...)
}

type followJobView struct {
	managedJobView
	Retries  int     `json:"retries"`
	Alert    bool    `json:"alert"`
	MissedAt *string `json:"missed_at"`
	Running  int     `json:"running"`
}

// TestManagedCronFollow is de acceptatie van mijlpaal 4 van fase 6: een
// lopende run stoppen en zijn uitvoer live zien, herhalen na een mislukte
// run, een incident tot de job weer lukt, en een gemiste run.
func TestManagedCronFollow(t *testing.T) {
	e, c := adminClient(t)
	ctx := context.Background()
	me, _ := user.Current()

	var web cluster
	if s := c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "test"}, &web); s != http.StatusCreated {
		t.Fatalf("cluster: %d", s)
	}
	var n node
	if s := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "w1", "cluster_id": web.ID}, &n); s != http.StatusCreated {
		t.Fatalf("node: %d", s)
	}
	a := &addrs{}
	a.set("192.0.2.1")
	h := &followNode{managedNode: managedNode{addrs: a}}
	startAgentWith(t, e, c, n.ID, strings.Repeat("6", 32), a, func(ag *agent.Agent, root string) {
		h.root = root
		ag.Root, ag.Exec = root, h.exec
		ag.CronRunsInterval = 100 * time.Millisecond
	})
	t.Cleanup(h.wg.Wait)
	eventually(t, "agent op w1", func() bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+web.ID, nil, &d)
		return len(d.Nodes) == 1 && d.Nodes[0].Status == "healthy"
	})

	// Een lange run: live uitvoer en stoppen.
	in := map[string]any{"cluster_id": web.ID, "name": "lang", "schedule": "0 3 * * *", "run_on": "all", "user": me.Username,
		"command": "echo begonnen; sleep 30; echo klaar", "timeout_seconds": 600, "enabled": true}
	var job followJobView
	if s := c.do("POST", "/api/v1/cron-jobs", in, &job); s != http.StatusCreated {
		t.Fatalf("maken: %d", s)
	}
	if !job.Alert || job.Retries != 0 {
		t.Errorf("standaard: %+v", job)
	}
	var runErr map[string]any
	if s := c.do("POST", "/api/v1/cron-jobs/"+job.ID+"/run", nil, &runErr); s != http.StatusAccepted {
		t.Fatalf("nu uitvoeren: %d %v", s, runErr)
	}
	var detail struct {
		Job  followJobView `json:"job"`
		Runs []cronRunView `json:"runs"`
	}
	eventually(t, "de lopende run", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+job.ID, nil, &detail)
		return len(detail.Runs) == 1 && detail.Runs[0].Result == "running" && detail.Job.Running == 1
	})
	runID := detail.Runs[0].ID
	var live struct {
		cronRunView
		Live bool `json:"live"`
	}
	eventually(t, "live uitvoer", func() bool {
		return c.do("GET", "/api/v1/cron-runs/"+runID, nil, &live) == http.StatusOK && live.Live && strings.Contains(live.Output, "begonnen")
	})
	if s := c.do("POST", "/api/v1/cron-runs/"+runID+"/stop", nil, nil); s != http.StatusAccepted {
		t.Fatalf("stoppen: %d", s)
	}
	eventually(t, "gestopt", func() bool {
		c.do("GET", "/api/v1/cron-runs/"+runID, nil, &live)
		return live.Result == "stopped"
	})
	if live.Live || !strings.Contains(live.Output, "met de hand gestopt") || strings.Contains(live.Output, "klaar") {
		t.Errorf("gestopte run: %+v", live)
	}
	if s := c.do("POST", "/api/v1/cron-runs/"+runID+"/stop", nil, nil); s != http.StatusConflict {
		t.Errorf("tweede keer stoppen: %d", s)
	}
	var stopped int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = 'cron_job.run_stopped' AND subject_id = $1", job.ID).Scan(&stopped); err != nil || stopped != 1 {
		t.Errorf("%d stop-events: %v", stopped, err)
	}

	// Herhalen: een mislukte run probeert het nog een keer, en die lukt.
	delete(in, "cluster_id")
	marker := filepath.Join(t.TempDir(), "tweede")
	in["command"], in["retries"] = "if [ -e "+marker+" ]; then echo gelukt; else touch "+marker+"; exit 3; fi", 1
	if s := c.do("PUT", "/api/v1/cron-jobs/"+job.ID, in, &job.managedJobView); s != http.StatusOK {
		t.Fatalf("wijzigen: %d", s)
	}
	jobFile := filepath.Join(h.root, "etc/clusterforge/cron", job.ID+".json")
	var onNode map[string]any
	data, err := os.ReadFile(jobFile)
	if err == nil {
		err = json.Unmarshal(data, &onNode)
	}
	if err != nil || onNode["retries"] != float64(1) || onNode["retry_delay_seconds"] != float64(60) {
		t.Fatalf("job op de node: %s %v", data, err)
	}
	// Een minuut wachten is te lang voor een test.
	onNode["retry_delay_seconds"] = 1
	data, _ = json.Marshal(onNode)
	if err := os.WriteFile(jobFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.runner().Main(ctx, job.ID)
	eventually(t, "de herhaling", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+job.ID, nil, &detail)
		return len(detail.Runs) == 3 && detail.Runs[0].Result == "ok"
	})
	if r := detail.Runs; r[0].Reason != "retry" || r[1].Reason != "schedule" || r[1].Result != "failed" {
		t.Errorf("runs: %+v", r)
	}

	// Mislukt hij helemaal, dan een incident tot de job weer lukt.
	in["command"], in["retries"] = "echo stuk; exit 5", 0
	if s := c.do("PUT", "/api/v1/cron-jobs/"+job.ID, in, &job.managedJobView); s != http.StatusOK {
		t.Fatalf("wijzigen: %d", s)
	}
	h.runner().Main(ctx, job.ID)
	var inc incidentView
	eventually(t, "incident van de job", func() bool {
		for _, i := range c.incidents("") {
			if slices.ContainsFunc(i.Subjects, func(s incidentSubjectView) bool { return s.Type == "cron_job" && s.Name == "lang" }) {
				inc = i
				return true
			}
		}
		return false
	})
	if inc.Title != "Cronjob lang mislukt" || inc.Status != "open" {
		t.Errorf("incident: %+v", inc)
	}
	in["command"] = "echo weer goed"
	if s := c.do("PUT", "/api/v1/cron-jobs/"+job.ID, in, &job.managedJobView); s != http.StatusOK {
		t.Fatalf("wijzigen: %d", s)
	}
	h.runner().Main(ctx, job.ID)
	eventually(t, "incident gesloten", func() bool {
		return slices.ContainsFunc(c.incidents("status=resolved"), func(i incidentView) bool { return i.ID == inc.ID })
	})

	// Zonder melden: geen incident.
	in["command"], in["alert"], in["name"] = "exit 1", false, "stil"
	in["cluster_id"] = web.ID
	var quiet followJobView
	if s := c.do("POST", "/api/v1/cron-jobs", in, &quiet); s != http.StatusCreated || quiet.Alert {
		t.Fatalf("stille job: %d %+v", s, quiet)
	}
	h.runner().Main(ctx, quiet.ID)
	eventually(t, "de stille run", func() bool {
		c.do("GET", "/api/v1/cron-jobs/"+quiet.ID, nil, &detail)
		return len(detail.Runs) == 1 && detail.Runs[0].Result == "failed"
	})
	time.Sleep(2 * time.Second)
	for _, i := range c.incidents("") {
		if slices.ContainsFunc(i.Subjects, func(s incidentSubjectView) bool { return s.Name == "stil" }) {
			t.Errorf("incident voor een stille job: %+v", i)
		}
	}

	// Een gemiste run: het laatste geplande moment ging voorbij zonder run.
	in["name"], in["schedule"], in["alert"] = "gemist", "0 */6 * * *", true
	var missed followJobView
	if s := c.do("POST", "/api/v1/cron-jobs", in, &missed); s != http.StatusCreated || missed.MissedAt != nil {
		t.Fatalf("job gemist: %d %+v", s, missed)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE cron_jobs SET updated_at = now() - interval '7 hours' WHERE id = $1", missed.ID); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []followJobView `json:"items"`
	}
	c.do("GET", "/api/v1/cron-jobs", nil, &list)
	i := slices.IndexFunc(list.Items, func(j followJobView) bool { return j.ID == missed.ID })
	if i < 0 || list.Items[i].MissedAt == nil {
		t.Fatalf("gemiste run: %+v", list.Items)
	}
	// Een run na het gemiste moment haalt de melding weg.
	h.runner().Main(ctx, missed.ID)
	eventually(t, "niet meer gemist", func() bool {
		c.do("GET", "/api/v1/cron-jobs", nil, &list)
		i := slices.IndexFunc(list.Items, func(j followJobView) bool { return j.ID == missed.ID })
		return i >= 0 && list.Items[i].MissedAt == nil
	})
}
