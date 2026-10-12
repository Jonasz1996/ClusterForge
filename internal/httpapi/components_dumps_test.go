package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

type dumpView struct {
	ID        string    `json:"id"`
	Database  string    `json:"database"`
	Node      string    `json:"node"`
	File      string    `json:"file"`
	Trigger   string    `json:"trigger"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	SizeBytes int64     `json:"size_bytes"`
	RawBytes  int64     `json:"raw_bytes"`
	Tables    int       `json:"tables"`
	Message   string    `json:"message"`
}

type backupSettingsView struct {
	On       bool       `json:"on"`
	At       string     `json:"at"`
	KeepDays int        `json:"keep_days"`
	NextAt   *time.Time `json:"next_at"`
}

type componentDumpsView struct {
	Settings  backupSettingsView `json:"settings"`
	Databases []struct {
		Database  string    `json:"database"`
		Latest    *dumpView `json:"latest"`
		Failed    *dumpView `json:"failed"`
		Freshness string    `json:"freshness"`
	} `json:"databases"`
	Dumps  []dumpView `json:"dumps"`
	Active *struct {
		JobID string `json:"job_id"`
		Title string `json:"title"`
	} `json:"active"`
}

// summary is per dump database@node:status, de nieuwste eerst.
func (v componentDumpsView) summary() string {
	var out []string
	for _, d := range v.Dumps {
		out = append(out, d.Database+"@"+d.Node+":"+d.Status)
	}
	return strings.Join(out, " ")
}

// freshness is per database de stand, zoals nextcloud=ok.
func (v componentDumpsView) freshness() string {
	var out []string
	for _, d := range v.Databases {
		out = append(out, d.Database+"="+d.Freshness)
	}
	return strings.Join(out, " ")
}

// TestComponentDumps is de acceptatie van mijlpaal 7 van fase 5: elke nacht
// een dump van elke database op een node zonder het VIP, met de leeftijd bij
// Back-ups; Dump nu; een dump die mislukt of verdacht is; oude dumps
// opruimen; en een dump terugzetten naast de bestaande database.
func TestComponentDumps(t *testing.T) {
	lab := newGaleraLab(t)
	e, c, g := lab.e, lab.c, lab.g
	ctx := context.Background()

	// Het VIP .185 staat op nc1 en hoort bij de database.
	var vip struct {
		ID string `json:"id"`
	}
	if s := c.do("POST", "/api/v1/clusters/"+lab.cluster.ID+"/vips", map[string]any{"address": "10.0.20.185"}, &vip); s != http.StatusCreated {
		t.Fatalf("VIP: %d", s)
	}
	lab.addrs["nc1"].set("10.0.20.186", "10.0.20.185")
	eventually(t, "het VIP op nc1", func() bool {
		var d statusView
		c.do("GET", "/api/v1/clusters/"+lab.cluster.ID, nil, &d)
		return len(d.Vips) == 1 && d.Vips[0].OwnerHostname != nil && *d.Vips[0].OwnerHostname == "nc1"
	})
	for _, table := range []string{"cluster_components", "cluster_component_revisions"} {
		col := "id"
		if table == "cluster_component_revisions" {
			col = "component_id"
		}
		if _, err := e.pool.Exec(ctx, "UPDATE "+table+" SET spec = jsonb_set(spec, '{vip}', jsonb_build_object('id', $1::text, 'address', '10.0.20.185')) WHERE "+col+" = $2",
			vip.ID, lab.cid); err != nil {
			t.Fatal(err)
		}
	}
	// Nextcloud en een wiki staan in Galera, dus op alle drie de nodes.
	setDBs := func(size int64) {
		for _, h := range g.hosts {
			h.Locked(func() {
				h.MariaDB.Databases = []agenttest.Database{{Name: "nextcloud", Size: size, Tables: 158}, {Name: "wiki", Size: 40 << 20, Tables: 12}}
			})
		}
	}
	setDBs(2 << 30)
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	dumps := lab.base + "/" + lab.cid + "/dumps"
	read := func() componentDumpsView {
		t.Helper()
		var v componentDumpsView
		var ae apiErr
		if s := c.do("GET", dumps, nil, &v); s != http.StatusOK {
			c.do("GET", dumps, nil, &ae)
			t.Fatalf("dumps: %d %+v", s, ae)
		}
		return v
	}
	type backupItem struct {
		ClusterName string             `json:"cluster_name"`
		Database    string             `json:"database"`
		Freshness   string             `json:"freshness"`
		Latest      *dumpView          `json:"latest"`
		Settings    backupSettingsView `json:"settings"`
	}
	backups := func() string {
		t.Helper()
		var l struct{ Items []backupItem }
		if s := c.do("GET", "/api/v1/backups/databases", nil, &l); s != http.StatusOK {
			t.Fatalf("back-ups: %d", s)
		}
		var out []string
		for _, b := range l.Items {
			out = append(out, b.ClusterName+"/"+b.Database+"="+b.Freshness)
		}
		return strings.Join(out, " ")
	}
	runJob := func(id string) job {
		t.Helper()
		return waitJobLong(t, c, id)
	}

	// Nog geen dump. Standaard elke nacht om 02:00, zeven dagen bewaard.
	if got := backups(); got != "Nextcloud/nextcloud=missing Nextcloud/wiki=missing" {
		t.Fatalf("zonder dumps: %s", got)
	}
	v := read()
	if v.Settings.At != "02:00" || v.Settings.KeepDays != 7 || !v.Settings.On || v.Settings.NextAt != nil || len(v.Dumps) != 0 {
		t.Fatalf("instellingen: %+v", v)
	}

	// De planner zet de volgende nachtelijke dump, en begint hem als het
	// zover is.
	now := time.Now()
	if err := e.cmps.RunBackups(ctx, now); err != nil {
		t.Fatal(err)
	}
	v = read()
	next := v.Settings.NextAt
	if next == nil || !next.After(now) || next.Sub(now) > 24*time.Hour || next.Local().Format("15:04") != "02:00" || v.Active != nil {
		t.Fatalf("volgende: %v", next)
	}
	if err := e.cmps.RunBackups(ctx, next.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if read().Active != nil {
		t.Fatal("te vroeg begonnen")
	}
	if err := e.cmps.RunBackups(ctx, next.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	v = read()
	if v.Active == nil || !strings.HasPrefix(v.Active.Title, "Nachtelijke dump") {
		t.Fatalf("niet begonnen: %+v", v)
	}
	if j := runJob(v.Active.JobID); j.Status != "succeeded" {
		var msg []string
		rows, _ := e.pool.Query(ctx, "SELECT error FROM job_steps WHERE job_id = $1 ORDER BY seq", j.ID)
		for rows.Next() {
			var m string
			_ = rows.Scan(&m)
			msg = append(msg, m)
		}
		rows.Close()
		t.Fatalf("nachtelijke dump: %s %s %q", j.Status, j.Error, msg)
	}
	v = read()
	// Op nc2: niet op nc1 met het VIP. De nieuwste eerst.
	if v.summary() != "wiki@nc2:ok nextcloud@nc2:ok" || v.Dumps[1].Trigger != "schedule" || v.Dumps[1].Tables != 158 || v.Dumps[1].SizeBytes == 0 {
		t.Fatalf("na de nacht: %s %+v", v.summary(), v.Dumps)
	}
	if v.Settings.NextAt == nil || !v.Settings.NextAt.After(*next) {
		t.Fatalf("volgende nacht: %v", v.Settings.NextAt)
	}
	for _, d := range v.Dumps {
		if _, err := os.Stat(filepath.Join(lab.dumpDirs["nc2"], d.Database, d.File)); err != nil {
			t.Errorf("dump op nc2: %v", err)
		}
	}
	if got := backups(); got != "Nextcloud/nextcloud=ok Nextcloud/wiki=ok" || v.freshness() != "nextcloud=ok wiki=ok" {
		t.Fatalf("na de nacht: %s, %s", got, v.freshness())
	}

	// Een kijker leest, maar maakt geen dump en verandert niets.
	e.createUser("kijker", "een-lang-wachtwoord", store.UserRoleViewer)
	viewer := e.client()
	if s, _, _ := viewer.login("kijker", "een-lang-wachtwoord", ""); s != http.StatusOK {
		t.Fatalf("login kijker: %d", s)
	}
	if s := viewer.do("GET", dumps, nil, nil); s != http.StatusOK {
		t.Fatalf("kijker lezen: %d", s)
	}
	if s := viewer.do("POST", dumps, map[string]any{"database": "wiki"}, nil); s != http.StatusForbidden {
		t.Fatalf("kijker dump: %d", s)
	}
	if s := viewer.do("PUT", lab.base+"/"+lab.cid+"/backup", map[string]any{"on": false, "at": "02:00", "keep_days": 7}, nil); s != http.StatusForbidden {
		t.Fatalf("kijker instellen: %d", s)
	}

	// Dump nu, terwijl nc2 Donor is: dan op nc3.
	nc2 := g.hosts["nc2"]
	nc2.Locked(func() { nc2.MariaDB.Galera.State, nc2.MariaDB.Galera.Comment = 2, "Donor/Desynced" })
	var j job
	if s := c.do("POST", dumps, map[string]any{"database": "nextcloud"}, &j); s != http.StatusAccepted {
		t.Fatalf("dump nu: %d", s)
	}
	if j = runJob(j.ID); j.Status != "succeeded" || j.Title != "Dump van nextcloud op Nextcloud" {
		t.Fatalf("dump nu: %+v", j)
	}
	nc2.Locked(func() { nc2.MariaDB.Galera.State, nc2.MariaDB.Galera.Comment = 4, "Synced" })
	v = read()
	if v.summary() != "nextcloud@nc3:ok wiki@nc2:ok nextcloud@nc2:ok" || v.Dumps[0].Trigger != "manual" {
		t.Fatalf("dump nu: %s", v.summary())
	}
	var ae apiErr
	if s := c.do("POST", dumps, map[string]any{"database": "mysql"}, &ae); s != http.StatusBadRequest {
		t.Errorf("mysql: %d %+v", s, ae)
	}

	// Kromp de database tot minder dan de helft, dan is de dump verdacht;
	// de vorige goede blijft de nieuwste goede. De dump daarna is weer goed.
	setDBs(50 << 20)
	if s := c.do("POST", dumps, map[string]any{"database": "nextcloud"}, &j); s != http.StatusAccepted {
		t.Fatalf("dump nu: %d", s)
	}
	if j = runJob(j.ID); j.Status != "succeeded" {
		t.Fatalf("verdacht: %+v", j)
	}
	v = read()
	if d := v.Dumps[0]; d.Status != "suspect" || !strings.Contains(d.Message, "helft kleiner") || d.Node != "nc3" {
		t.Fatalf("verdacht: %+v", d)
	}
	if d := v.Databases[0]; d.Database != "nextcloud" || d.Latest == nil || d.Latest.Status != "ok" || d.Failed == nil || d.Failed.Status != "suspect" {
		t.Fatalf("nextcloud: %+v", d)
	}

	// Een mislukte dump: de taak mislukt, de fout staat erbij.
	g.hosts["nc3"].Fail("mariadb-dump", "mariadb-dump: Got error: 2013: \"Lost connection to server during query\" when dumping table `page` at row: 0")
	if s := c.do("POST", dumps, map[string]any{"database": "wiki"}, &j); s != http.StatusAccepted {
		t.Fatalf("dump nu: %d", s)
	}
	if j = runJob(j.ID); j.Status != "failed" || !strings.Contains(j.Error, "Lost connection") {
		t.Fatalf("mislukt: %+v", j)
	}
	v = read()
	if d := v.Dumps[0]; d.Status != "failed" || d.Database != "wiki" || !strings.Contains(d.Message, "Lost connection") {
		t.Fatalf("mislukt: %+v", d)
	}

	// Een dag bewaren, en drie dagen later de volgende nacht: oude dumps gaan
	// weg, op elke node, behalve de nieuwste goede per database.
	var set backupSettingsView
	if s := c.do("PUT", lab.base+"/"+lab.cid+"/backup", map[string]any{"on": true, "at": "03:30", "keep_days": 1}, &set); s != http.StatusOK ||
		set.NextAt == nil || set.NextAt.Local().Format("15:04") != "03:30" {
		t.Fatalf("instellen: %d %+v", s, set)
	}
	if s := c.do("PUT", lab.base+"/"+lab.cid+"/backup", map[string]any{"on": true, "at": "25:00", "keep_days": 1}, &ae); s != http.StatusBadRequest {
		t.Errorf("25:00: %d", s)
	}
	lab.days.Store(3)
	if err := e.cmps.RunBackups(ctx, set.NextAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v = read(); v.Active == nil {
		t.Fatal("tweede nacht niet begonnen")
	}
	if j = runJob(v.Active.JobID); j.Status != "succeeded" {
		t.Fatalf("tweede nacht: %+v", j)
	}
	// De nieuwe dump van nextcloud is goed: hij lijkt op de vorige.
	if v = read(); v.summary() != "wiki@nc3:ok nextcloud@nc3:ok" {
		t.Fatalf("na opruimen: %s", v.summary())
	}
	for _, name := range []string{"nc2", "nc3"} {
		var files []string
		_ = filepath.WalkDir(lab.dumpDirs[name], func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, filepath.Base(p))
			}
			return nil
		})
		if want := map[string]int{"nc2": 0, "nc3": 4}[name]; len(files) != want {
			t.Errorf("%s: %v", name, files)
		}
	}

	// Terugzetten naast de bestaande database.
	latest := v.Dumps[1]
	var started struct {
		Job    job    `json:"job"`
		Target string `json:"target"`
	}
	restore := dumps + "/" + latest.ID + "/restore"
	if s := c.do("POST", restore, nil, &started); s != http.StatusAccepted {
		t.Fatalf("terugzetten: %d", s)
	}
	want := "nextcloud_herstel_" + latest.StartedAt.Local().Format("20060102")
	if started.Target != want {
		t.Fatalf("naam: %q, wil %q", started.Target, want)
	}
	if j = runJob(started.Job.ID); j.Status != "succeeded" {
		t.Fatalf("terugzetten: %+v", j)
	}
	nc3 := g.hosts["nc3"]
	var restored agenttest.Database
	nc3.Locked(func() {
		for _, d := range nc3.MariaDB.Databases {
			if d.Name == want {
				restored = d
			}
		}
	})
	if restored.Tables != 158 {
		t.Fatalf("teruggezet: %+v", restored)
	}
	// Nog eens met dezelfde naam kan niet; met een eigen naam wel.
	if s := c.do("POST", restore, nil, &started); s != http.StatusAccepted {
		t.Fatalf("nog eens: %d", s)
	}
	if j = runJob(started.Job.ID); j.Status != "failed" || !strings.Contains(j.Error, "bestaat al") {
		t.Fatalf("nog eens: %+v", j)
	}
	if s := c.do("POST", restore, map[string]any{"target": "nextcloud_controle"}, &started); s != http.StatusAccepted || started.Target != "nextcloud_controle" {
		t.Fatalf("eigen naam: %d %+v", s, started)
	}
	if j = runJob(started.Job.ID); j.Status != "succeeded" {
		t.Fatalf("eigen naam: %+v", j)
	}
	for body, code := range map[string]int{"Next": http.StatusBadRequest, "nextcloud": http.StatusBadRequest} {
		if s := c.do("POST", restore, map[string]any{"target": body}, &ae); s != code {
			t.Errorf("%s: %d %+v", body, s, ae)
		}
	}
	if s := c.do("POST", dumps+"/"+lab.cid+"/restore", nil, &ae); s != http.StatusNotFound {
		t.Errorf("onbekende dump: %d", s)
	}
	// Een teruggezette kopie met de standaardnaam krijgt geen nachtelijke
	// dump en staat niet bij Back-ups; met een eigen naam is het een gewone
	// database.
	var all []agenttest.Database
	nc3.Locked(func() { all = slices.Clone(nc3.MariaDB.Databases) })
	for _, h := range g.hosts {
		h.Locked(func() { h.MariaDB.Databases = slices.Clone(all) })
	}
	if err := e.dbs.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := backups(); got != "Nextcloud/nextcloud=ok Nextcloud/nextcloud_controle=missing Nextcloud/wiki=ok" {
		t.Fatalf("met kopie: %s", got)
	}

	// Een dump van twee dagen oud is te oud; uitgezet staat er uit.
	if _, err := e.pool.Exec(ctx, "UPDATE db_dumps SET started_at = started_at - interval '2 days' WHERE database = 'wiki'"); err != nil {
		t.Fatal(err)
	}
	if got := backups(); got != "Nextcloud/nextcloud=ok Nextcloud/nextcloud_controle=missing Nextcloud/wiki=stale" {
		t.Fatalf("te oud: %s", got)
	}
	if s := c.do("PUT", lab.base+"/"+lab.cid+"/backup", map[string]any{"on": false, "at": "03:30", "keep_days": 1}, &set); s != http.StatusOK || set.NextAt != nil {
		t.Fatalf("uit: %d %+v", s, set)
	}
	if got := backups(); got != "Nextcloud/nextcloud=off Nextcloud/nextcloud_controle=off Nextcloud/wiki=off" {
		t.Fatalf("uit: %s", got)
	}
	if err := e.cmps.RunBackups(ctx, time.Now().Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if read().Active != nil {
		t.Fatal("uitgezet maar toch begonnen")
	}

	var actions []string
	rows, err := e.pool.Query(ctx, "SELECT action FROM events WHERE action LIKE 'backup.db_%' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, strings.TrimPrefix(a, "backup.db_"))
	}
	rows.Close()
	if got := strings.Join(actions, " "); got != "dumped dumped dumped dump_suspect dump_failed settings dumped dumped restored restore_failed restored settings" {
		t.Errorf("events: %s", got)
	}
}
