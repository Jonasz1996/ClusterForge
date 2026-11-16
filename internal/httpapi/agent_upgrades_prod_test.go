package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/Jonasz1996/clusterforge/internal/agentupgrade"
	"github.com/Jonasz1996/clusterforge/internal/events"
)

type prodView struct {
	upgradeView
	ProdAuto   bool   `json:"prod_auto"`
	ProdHalted bool   `json:"prod_halted"`
	HaltedBy   string `json:"halted_by"`
	ProdQueue  []struct {
		ClusterName string  `json:"cluster_name"`
		JobID       *string `json:"job_id"`
	} `json:"prod_queue"`
}

func (v prodView) queue() string {
	var s []string
	for _, q := range v.ProdQueue {
		mark := ""
		if q.JobID != nil {
			mark = "*"
		}
		s = append(s, q.ClusterName+mark)
	}
	return strings.Join(s, " ")
}

// TestAgentUpgradesProd: één klik "ook productie" met één code werkt de
// labclusters meteen bij en de prodclusters één voor één; een fout stopt
// de rij. Prod vanzelf bijwerken vraagt een code en staat stil na een fout.
func TestAgentUpgradesProd(t *testing.T) {
	e, c := adminClient(t)
	offerAgent(t, e, "nieuwe agent")
	errOf := func(c *client, method, path string, body any) (int, string) {
		t.Helper()
		var er apiErr
		s := c.do(method, path, body, &er)
		return s, er.Code
	}

	var lab, db, mail cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "web", "name": "Web", "type": "keepalived", "environment": "lab"}, &lab)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "DB", "type": "keepalived", "environment": "prod"}, &db)
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "mail", "name": "Mail", "type": "keepalived", "environment": "prod"}, &mail)
	var web01, db01, db02, mail01 node
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "web01", "cluster_id": lab.ID}, &web01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db01", "cluster_id": db.ID}, &db01)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "db02", "cluster_id": db.ID}, &db02)
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "mail01", "cluster_id": mail.ID}, &mail01)
	startUpgradeNode(t, e, c, web01.ID, strings.Repeat("1", 32))
	nDB1 := startUpgradeNode(t, e, c, db01.ID, strings.Repeat("2", 32))
	startUpgradeNode(t, e, c, db02.ID, strings.Repeat("3", 32))
	startUpgradeNode(t, e, c, mail01.ID, strings.Repeat("4", 32))

	var v prodView
	eventually(t, "vier verouderde agents", func() bool {
		c.do("GET", "/api/v1/agent-upgrades", nil, &v)
		return v.states() == "db01=outdated db02=outdated mail01=outdated web01=outdated*"
	})
	// Labcluster uit zodat vanzelf bijwerken niet tussenkomt.
	c.do("PUT", "/api/v1/clusters/"+lab.ID+"/agent-upgrade-auto", map[string]any{"enabled": false}, nil)

	all := map[string]any{"all": true, "include_prod": true}
	if s, code := errOf(c, "POST", "/api/v1/agent-upgrades", all); s != http.StatusForbidden || code != "totp_required" {
		t.Fatalf("ook prod zonder tweestapsverificatie: %d %s", s, code)
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	c.do("POST", "/api/v1/auth/totp/setup", nil, &setup)
	now := time.Now()
	code, _ := totp.GenerateCode(setup.Secret, now.Add(-30*time.Second))
	if s := c.do("POST", "/api/v1/auth/totp/enable", map[string]string{"code": code}, nil); s != http.StatusNoContent {
		t.Fatalf("totp aan: %d", s)
	}
	if s, code := errOf(c, "POST", "/api/v1/agent-upgrades", all); s != http.StatusConflict || code != "needs_confirmation" {
		t.Fatalf("ook prod zonder code: %d %s", s, code)
	}
	if s, code := errOf(c, "POST", "/api/v1/agent-upgrades", map[string]any{"all": true, "include_prod": true, "totp_code": "000000"}); s != http.StatusBadRequest || code != "invalid_totp" {
		t.Fatalf("ook prod met foute code: %d %s", s, code)
	}

	// Eén code voor alles: Web meteen, DB en Mail in de rij.
	code, _ = totp.GenerateCode(setup.Secret, now)
	var started struct {
		Jobs       []job    `json:"jobs"`
		Skipped    []string `json:"skipped"`
		QueuedProd []string `json:"queued_prod"`
	}
	if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"all": true, "include_prod": true, "totp_code": code}, &started); s != http.StatusAccepted ||
		len(started.Jobs) != 1 || strings.Join(started.QueuedProd, ",") != "DB,Mail" {
		t.Fatalf("ook prod: %d %+v", s, started)
	}
	if j := waitJob(t, c, started.Jobs[0].ID); j.Status != "succeeded" {
		t.Fatalf("web: %+v", j)
	}

	// De rij: één prodcluster tegelijk.
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/agent-upgrades", nil, &v)
	if v.queue() != "DB* Mail" {
		t.Fatalf("rij na start: %q", v.queue())
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	var jobs struct {
		Items []job `json:"items"`
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if len(jobs.Items) != 2 || !strings.Contains(jobs.Items[0].Title, "DB") {
		t.Fatalf("één prodcluster tegelijk: %+v", jobs.Items)
	}
	if j := waitJob(t, c, jobs.Items[0].ID); j.Status != "succeeded" || len(j.Steps) != 2 {
		t.Fatalf("db: %+v", j)
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if len(jobs.Items) != 3 || !strings.Contains(jobs.Items[0].Title, "mail01") {
		t.Fatalf("mail na db: %+v", jobs.Items)
	}
	if j := waitJob(t, c, jobs.Items[0].ID); j.Status != "succeeded" {
		t.Fatalf("mail: %+v", j)
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/agent-upgrades", nil, &v)
	if v.queue() != "" || v.states() != "db01=current db02=current mail01=current web01=current" {
		t.Fatalf("na de rij: %q %s", v.queue(), v.states())
	}

	// Een fout in DB stopt de rij: Mail komt niet aan de beurt.
	offerAgent(t, e, "tweede agent")
	nDB1.mu.Lock()
	nDB1.rollback = true
	nDB1.mu.Unlock()
	if err := e.upg.QueueProd(context.Background(), events.System(), nil, []agentupgrade.QueuedCluster{
		{ClusterID: uuid.MustParse(db.ID), Name: "DB"}, {ClusterID: uuid.MustParse(mail.ID), Name: "Mail"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if j := waitJob(t, c, jobs.Items[0].ID); j.Status != "failed" || len(j.Steps) != 1 {
		t.Fatalf("db kapot: %+v", j)
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	eventually(t, "rij gestopt", func() bool {
		c.do("GET", "/api/v1/agent-upgrades", nil, &v)
		return v.queue() == "" && v.ProdHalted && v.HaltedBy == "db01"
	})
	if len(jobs.Items) != 4 {
		t.Fatalf("mail na een fout in db: %+v", jobs.Items[0])
	}

	// Prod vanzelf: aanzetten vraagt een code; na de fout staat het stil.
	if s, code := errOf(c, "PUT", "/api/v1/agent-upgrades/prod-auto", map[string]any{"enabled": true}); s != http.StatusConflict || code != "needs_confirmation" {
		t.Fatalf("prod vanzelf zonder code: %d %s", s, code)
	}
	code, _ = totp.GenerateCode(setup.Secret, now.Add(30*time.Second))
	if s := c.do("PUT", "/api/v1/agent-upgrades/prod-auto", map[string]any{"enabled": true, "totp_code": code}, &v); s != http.StatusOK || !v.ProdAuto {
		t.Fatalf("prod vanzelf aan: %d %+v", s, v)
	}
	if got := v.states(); got != "db01=failed db02=outdated mail01=outdated web01=outdated" {
		t.Fatalf("stil na de fout: %s", got)
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if len(jobs.Items) != 4 {
		t.Fatalf("vanzelf op prod na een fout: %+v", jobs.Items[0])
	}

	// db01 met de knop opnieuw (de rest van DB mee): daarna gaat prod
	// vanzelf verder, één cluster tegelijk.
	nDB1.mu.Lock()
	nDB1.rollback = false
	nDB1.mu.Unlock()
	c.do("PUT", "/api/v1/clusters/"+lab.ID+"/agent-upgrade-auto", map[string]any{"enabled": true}, nil)
	code, _ = totp.GenerateCode(setup.Secret, now.Add(60*time.Second))
	if s, ec := errOf(c, "POST", "/api/v1/agent-upgrades", map[string]any{"cluster_id": db.ID, "totp_code": code}); s != http.StatusAccepted {
		// Te ver in de toekomst voor de klok: dan met de slug.
		if s := c.do("POST", "/api/v1/agent-upgrades", map[string]any{"cluster_id": db.ID, "confirm": "db"}, &started); s != http.StatusAccepted {
			t.Fatalf("db opnieuw: %d %s", s, ec)
		}
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if j := waitJob(t, c, jobs.Items[0].ID); j.Status != "succeeded" {
		t.Fatalf("db opnieuw: %+v", j)
	}
	c.do("GET", "/api/v1/agent-upgrades", nil, &v)
	if v.ProdHalted || v.states() != "db01=current db02=current mail01=outdated* web01=outdated*" {
		t.Fatalf("na db: %v %s", v.ProdHalted, v.states())
	}
	if err := e.upg.AutoRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/api/v1/jobs?limit=20", nil, &jobs)
	if len(jobs.Items) != 7 {
		t.Fatalf("vanzelf mail en web: %d", len(jobs.Items))
	}
	for _, j := range jobs.Items[:2] {
		if j := waitJob(t, c, j.ID); j.Status != "succeeded" || !strings.HasSuffix(j.Title, "(vanzelf)") {
			t.Fatalf("vanzelf: %+v", j)
		}
	}

	var evs struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/events?limit=200", nil, &evs)
	seen := map[string]int{}
	for _, ev := range evs.Items {
		seen[ev.Action]++
	}
	if seen["agents.prod_upgrade_queued"] != 2 || seen["agents.prod_upgrade_stopped"] != 1 || seen["settings.agent_upgrade_prod_auto"] != 1 {
		t.Fatalf("events: %v", seen)
	}
}
