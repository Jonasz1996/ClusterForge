package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ncConf is een keepalived.conf zoals Jonas hem met de hand schreef voor
// Nextcloud, met het VIP .185.
const ncConf = `global_defs {
   router_id nc1
}

vrrp_script chk_apache {
    script "/usr/bin/systemctl is-active --quiet apache2"
    interval 2
}

vrrp_instance VI_1 {
    state MASTER
    interface eth0
    virtual_router_id 85
    priority 150
    advert_int 1
    authentication {
        auth_type PASS
        auth_pass geheim12
    }
    virtual_ipaddress {
        10.0.20.185/24
    }
    track_script {
        chk_apache
    }
}
`

const includeHead = "# ClusterForge: de controle van de database (vrrp_script cf_db) staat in /etc/keepalived/conf.d/clusterforge-db.conf.\n" +
	"include /etc/keepalived/conf.d/*.conf\n\n"

func TestAddDBTrack(t *testing.T) {
	for _, tc := range []struct {
		name, conf, vip, want, inst, err string
	}{
		{
			name: "track_script over meer regels",
			conf: ncConf, vip: "10.0.20.185", inst: "VI_1",
			want: includeHead + strings.Replace(ncConf, "        chk_apache\n", "        chk_apache\n        cf_db\n", 1),
		},
		{
			name: "track_script op één regel",
			conf: strings.Replace(ncConf, "track_script {\n        chk_apache\n    }", "track_script { chk_apache }", 1),
			vip:  "10.0.20.185", inst: "VI_1",
			want: includeHead + strings.Replace(ncConf, "track_script {\n        chk_apache\n    }", "track_script { chk_apache cf_db }", 1),
		},
		{
			name: "zonder track_script",
			conf: strings.Replace(ncConf, "    track_script {\n        chk_apache\n    }\n", "", 1),
			vip:  "10.0.20.185", inst: "VI_1",
			want: includeHead + strings.Replace(ncConf, "    track_script {\n        chk_apache\n    }\n",
				"    # ClusterForge: het VIP alleen op een node met een gezonde database.\n    track_script {\n        cf_db\n    }\n", 1),
		},
		{
			name: "include staat er al",
			conf: "include /etc/keepalived/conf.d/*.conf\n" + ncConf, vip: "10.0.20.185", inst: "VI_1",
			want: "include /etc/keepalived/conf.d/*.conf\n" + strings.Replace(ncConf, "        chk_apache\n", "        chk_apache\n        cf_db\n", 1),
		},
		{
			name: "alles staat er al",
			conf: includeHead + strings.Replace(ncConf, "        chk_apache\n", "        chk_apache\n        cf_db\n", 1),
			vip:  "10.0.20.185", inst: "VI_1",
			want: includeHead + strings.Replace(ncConf, "        chk_apache\n", "        chk_apache\n        cf_db\n", 1),
		},
		{
			name: "accolade op de volgende regel en tabs",
			conf: "vrrp_instance VI_9\n{\n\tvirtual_ipaddress\n\t{\n\t\t10.0.20.185 dev eth0\n\t}\n}\n",
			vip:  "10.0.20.185", inst: "VI_9",
			want: includeHead + "vrrp_instance VI_9\n{\n\tvirtual_ipaddress\n\t{\n\t\t10.0.20.185 dev eth0\n\t}\n" +
				"\t# ClusterForge: het VIP alleen op een node met een gezonde database.\n\ttrack_script {\n\t\tcf_db\n\t}\n}\n",
		},
		{
			name: "het VIP in de tweede instance",
			conf: "vrrp_instance VI_2 {\n    virtual_ipaddress {\n        10.0.20.190\n    }\n}\n" + ncConf,
			vip:  "10.0.20.185", inst: "VI_1",
			want: includeHead + "vrrp_instance VI_2 {\n    virtual_ipaddress {\n        10.0.20.190\n    }\n}\n" +
				strings.Replace(ncConf, "        chk_apache\n", "        chk_apache\n        cf_db\n", 1),
		},
		{name: "VIP onbekend", conf: ncConf, vip: "10.0.20.180", err: "geen vrrp_instance"},
		{name: "VIP in twee instances", conf: ncConf + strings.Replace(ncConf, "VI_1", "VI_2", 1), vip: "10.0.20.185", err: "meer dan één"},
		{name: "instance op één regel", conf: "vrrp_instance VI_1 { virtual_ipaddress { 10.0.20.185 } }\n", vip: "10.0.20.185", err: "op één regel"},
		{name: "accolade te veel", conf: ncConf + "}\n", vip: "10.0.20.185", err: "te veel"},
		{name: "niet gesloten", conf: strings.TrimSuffix(ncConf, "}\n"), vip: "10.0.20.185", err: "niet gesloten"},
		{name: "commentaar telt niet", conf: strings.Replace(ncConf, "        10.0.20.185/24\n", "        # 10.0.20.185/24\n        10.0.20.186/24\n", 1), vip: "10.0.20.185", err: "geen vrrp_instance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, inst, err := addDBTrack(tc.conf, tc.vip)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("fout %v, verwacht %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || inst != tc.inst {
				t.Fatalf("instance %q\n%s\nverwacht %q\n%s", inst, got, tc.inst, tc.want)
			}
			// Een tweede keer verandert niets.
			if again, _, err := addDBTrack(got, tc.vip); err != nil || again != got {
				t.Fatalf("niet idempotent: %v\n%s", err, again)
			}
		})
	}
}

func trackAgent(t *testing.T, h *agenttest.Host) *Agent {
	t.Helper()
	return &Agent{Root: h.Root, Exec: h.Exec, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func writeMode(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

const trackScriptConf = "vrrp_script cf_db {\n    script \"/usr/local/bin/cf-agent check db\"\n    interval 2\n}\n"

func TestDBTrack(t *testing.T) {
	ctx := context.Background()
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	a := trackAgent(t, h)
	conf := filepath.Join(h.Root, "etc/keepalived/keepalived.conf")
	writeMode(t, conf, ncConf, 0o640)

	// Het plan: de diff zonder wachtwoord, niets geschreven.
	res := a.dbTrack(ctx, &protocol.DBTrackRequest{VIP: "10.0.20.185"})
	if res.Error != "" || res.Track == nil || !res.Track.Changed || res.Track.Instance != "VI_1" {
		t.Fatalf("plan: %+v", res)
	}
	if !strings.Contains(res.Track.Diff, "+        cf_db") || !strings.Contains(res.Track.Diff, "+include /etc/keepalived/conf.d/*.conf") ||
		strings.Contains(res.Track.Diff, "geheim12") {
		t.Errorf("diff:\n%s", res.Track.Diff)
	}
	if b, _ := os.ReadFile(conf); string(b) != ncConf {
		t.Fatal("het plan schreef keepalived.conf")
	}

	// Toepassen zonder het bestand met cf_db weigert.
	res = a.dbTrack(ctx, &protocol.DBTrackRequest{VIP: "10.0.20.185", Apply: true})
	if !strings.Contains(res.Error, "clusterforge-db.conf") || res.Track.Changed {
		t.Fatalf("zonder conf.d: %+v", res)
	}

	// Met het bestand: keepalived -t keurt goed en het bestand verandert,
	// met dezelfde modus.
	writeMode(t, filepath.Join(h.Root, protocol.DBTrackFile), trackScriptConf, 0o644)
	res = a.dbTrack(ctx, &protocol.DBTrackRequest{VIP: "10.0.20.185", Apply: true})
	if res.Error != "" || !res.Track.Changed {
		t.Fatalf("toepassen: %+v", res)
	}
	b, _ := os.ReadFile(conf)
	if !strings.HasPrefix(string(b), includeHead) || !strings.Contains(string(b), "        cf_db\n") {
		t.Fatalf("keepalived.conf:\n%s", b)
	}
	if fi, _ := os.Stat(conf); fi.Mode().Perm() != 0o640 {
		t.Errorf("modus %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(conf + ".clusterforge"); err == nil {
		t.Error("tijdelijk bestand bleef staan")
	}
	calls := strings.Join(h.Calls(), "\n")
	if !strings.Contains(calls, "keepalived -t -f "+conf+".clusterforge") {
		t.Errorf("geen keepalived -t: %s", calls)
	}

	// Nog eens: niets te doen.
	res = a.dbTrack(ctx, &protocol.DBTrackRequest{VIP: "10.0.20.185", Apply: true})
	if res.Error != "" || res.Track.Changed || res.Track.Diff != "" {
		t.Fatalf("tweede keer: %+v", res)
	}

	// Keurt keepalived -t af, dan blijft alles zoals het was.
	writeMode(t, conf, strings.Replace(ncConf, "VI_1", "VI_2", 1), 0o640)
	h.Fail("keepalived -t", "Unknown keyword 'virtual_routr_id'\n")
	res = a.dbTrack(ctx, &protocol.DBTrackRequest{VIP: "10.0.20.185", Apply: true})
	if !strings.Contains(res.Error, "Unknown keyword") || res.Track.Changed {
		t.Fatalf("afgekeurd: %+v", res)
	}
	if b, _ := os.ReadFile(conf); string(b) != strings.Replace(ncConf, "VI_1", "VI_2", 1) {
		t.Fatal("keepalived.conf veranderde")
	}

	if got := maskAuthPass("    auth_pass geheim12\n\tauth_pass\tx # y\n"); got != "    auth_pass [geheim]\n\tauth_pass\t[geheim] # y\n" {
		t.Errorf("gemaskeerd: %q", got)
	}

	for _, bad := range []*protocol.DBTrackRequest{nil, {VIP: "nc1"}} {
		if res := a.dbTrack(ctx, bad); !strings.Contains(res.Error, "geen geldig VIP") {
			t.Errorf("%+v: %+v", bad, res)
		}
	}
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if res := a.dbTrack(ctx, &protocol.DBTrackRequest{VIP: "10.0.20.185"}); !strings.Contains(res.Error, "ontbreekt") {
		t.Errorf("zonder keepalived: %+v", res)
	}
}

func TestCheckDB(t *testing.T) {
	ctx := context.Background()
	h := agenttest.NewHost(t.TempDir(), "10.0.20.186")
	a := trackAgent(t, h)
	if err := a.CheckDB(ctx); err == nil || !strings.Contains(err.Error(), "antwoordt niet") {
		t.Fatalf("zonder MariaDB: %v", err)
	}
	h.MariaDB = &agenttest.MariaDB{Version: "11.8.3-MariaDB", Galera: &agenttest.Galera{
		ClusterName: "nextcloud", UUID: "u", Status: "Primary", Size: 3, State: 4, Comment: "Synced", Ready: true,
	}}
	if err := a.CheckDB(ctx); err != nil {
		t.Fatalf("gezond: %v", err)
	}
	for _, tc := range []struct {
		change func(g *agenttest.Galera)
		want   string
	}{
		{func(g *agenttest.Galera) { g.State, g.Comment = 2, "Donor/Desynced" }, "Donor/Desynced"},
		{func(g *agenttest.Galera) { g.Status = "non-Primary" }, "non-Primary"},
		{func(g *agenttest.Galera) { g.Ready = false }, "wsrep_ready"},
	} {
		g := *h.MariaDB.Galera
		tc.change(h.MariaDB.Galera)
		if err := a.CheckDB(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v, verwacht %q", err, tc.want)
		}
		*h.MariaDB.Galera = g
	}
	h.MariaDB.Galera = nil
	if err := a.CheckDB(ctx); err == nil || !strings.Contains(err.Error(), "niet Synced") {
		t.Errorf("zonder Galera: %v", err)
	}
}
