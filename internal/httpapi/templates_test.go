package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// TestTemplatesFleet rolt docker, cron en generic uit in de fleet-test. Na
// elke uitrol is het cluster gezond, staan de diensten met unit en poort in
// de graaf, en vindt een driftcontrole op geen enkele node een afwijking of
// een stap die ze niet kan controleren.
func TestTemplatesFleet(t *testing.T) {
	e, c := adminClient(t)

	var tpls struct {
		Items []struct {
			template
			Title    string `json:"title"`
			Services []struct {
				Name      string   `json:"name"`
				Kind      string   `json:"kind"`
				Unit      *string  `json:"unit"`
				Port      *int     `json:"port"`
				PortParam *string  `json:"port_param"`
				DependsOn []string `json:"depends_on"`
			} `json:"services"`
		}
	}
	if s := c.do("GET", "/api/v1/templates", nil, &tpls); s != 200 || len(tpls.Items) != 4 {
		t.Fatalf("templates: %d %+v", s, tpls)
	}
	var list []string
	for _, tp := range tpls.Items {
		list = append(list, tp.Name+" "+tp.Version+" "+tp.ClusterType+" "+tp.Title)
	}
	if got := strings.Join(list, ", "); got != "cron 1.0.0 cron Cron-cluster, docker 1.0.0 docker Docker-hosts, "+
		"generic 1.0.0 generic Eigen applicatie, keepalived-nginx 1.1.0 keepalived Nginx met keepalived" {
		t.Fatalf("templatelijst: %s", got)
	}
	app := tpls.Items[2].Services[0]
	if app.Name != "app" || app.Unit == nil || *app.Unit != "cf-app" || app.Port != nil || app.PortParam == nil || *app.PortParam != "port" ||
		!slices.Equal(app.DependsOn, []string{"docker", "keepalived"}) {
		t.Fatalf("dienst app in de templatelijst: %+v", app)
	}
	if nginx := tpls.Items[3].Services[0]; nginx.Port == nil || *nginx.Port != 80 || nginx.PortParam != nil {
		t.Fatalf("dienst nginx in de templatelijst: %+v", nginx)
	}

	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "qemu", VMID: 9001, Name: "debian-13-cf", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"scsi0": "ceph:base-9001-disk-0,size=3G", "ide2": "ceph:vm-9001-cloudinit,media=cdrom",
			"boot": "order=scsi0", "agent": "enabled=1", "name": "debian-13-cf",
		},
	})
	f := newFleet(t, map[string]string{
		"docker-01": "10.0.40.11", "docker-02": "10.0.40.12",
		"taken-01": "10.0.40.21", "taken-02": "10.0.40.22",
		"app-01": "10.0.40.31", "app-02": "10.0.40.32",
	})
	pve.OnFileWrite(f.boot)
	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	// De controle via HTTP lukt pas als de eigenaar van het VIP de
	// applicatie draait.
	e.deploy.HTTPGet = func(_ context.Context, raw string) (int, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return 0, err
		}
		owner := f.group.Owner(u.Hostname())
		if owner == nil {
			return 0, errors.New("no route to host")
		}
		if st, _ := owner.Unit("cf-app"); !st.Active || u.Port() != "8080" || u.Path != "/health" {
			return 0, errors.New("connection refused")
		}
		return http.StatusOK, nil
	}

	for _, tc := range []struct {
		template, name, slug, first string
		params                      map[string]any
		vip                         string
		services                    []string
		logs                        []string
	}{
		{
			template: "docker", name: "Docker", slug: "docker", first: "10.0.40.11/24",
			params:   map[string]any{"memory": "2G"},
			services: []string{"docker container docker -"},
			logs:     []string{"pakketten: docker.io, docker-compose: aangepast", "map /opt/stacks: aangepast"},
		},
		{
			template: "cron", name: "Taken", slug: "taken", first: "10.0.40.21/24",
			params:   map[string]any{"vip": "10.0.40.100"},
			vip:      "10.0.40.100",
			services: []string{"cron cron cron - keepalived", "keepalived vip keepalived -"},
			logs:     []string{"bestand /usr/local/bin/cf-leader: aangepast", "10.0.40.100 staat op taken-01"},
		},
		{
			template: "generic", name: "App", slug: "app", first: "10.0.40.31/24",
			params: map[string]any{
				"vip": "10.0.40.101", "image": "ghcr.io/jonas/app:1.4.2", "port": 8080, "container_port": 3000, "health_path": "/health",
			},
			vip: "10.0.40.101",
			services: []string{
				"app app cf-app 8080 docker,keepalived", "docker container docker -", "keepalived vip keepalived -",
			},
			logs: []string{"systemctl daemon-reload", "10.0.40.101 staat op app-01", "http://10.0.40.101:8080/health geeft 200"},
		},
	} {
		t.Run(tc.template, func(t *testing.T) {
			var res struct {
				Job       job    `json:"job"`
				ClusterID string `json:"cluster_id"`
			}
			if s := c.do("POST", "/api/v1/deployments", map[string]any{
				"template": tc.template,
				"cluster":  map[string]any{"name": tc.name, "slug": tc.slug, "environment": "lab"},
				"params":   tc.params,
				"target": map[string]any{
					"proxmox_id": conn.ID, "image_vmid": 9001, "network": "static",
					"first_ip": tc.first, "gateway": "10.0.40.1", "dns": []string{"10.0.40.1"},
					"ssh_keys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJvbmFzIGtleQ jonas@laptop",
				},
				"server_url": e.srv.URL,
			}, &res); s != http.StatusAccepted {
				t.Fatalf("uitrollen: %d", s)
			}
			j := waitJob(t, c, res.Job.ID)
			logs := ""
			for _, s := range j.Steps {
				logs += strings.Join(s.Log, "\n") + "\n"
			}
			if j.Status != "succeeded" {
				t.Fatalf("uitrol: %+v\n%s", j, logs)
			}
			for _, w := range tc.logs {
				if !strings.Contains(logs, w) {
					t.Errorf("log mist %q:\n%s", w, logs)
				}
			}

			var cl struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Nodes  []node `json:"nodes"`
				Vips   []struct {
					Address       string  `json:"address"`
					OwnerHostname *string `json:"owner_hostname"`
				} `json:"vips"`
			}
			eventually(t, "cluster gezond", func() bool {
				c.do("GET", "/api/v1/clusters/"+res.ClusterID, nil, &cl)
				return cl.Status == "healthy"
			})
			if cl.Type != tc.template || len(cl.Nodes) != 2 {
				t.Fatalf("cluster: %+v", cl)
			}
			if tc.vip != "" && (len(cl.Vips) != 1 || cl.Vips[0].Address != tc.vip || cl.Vips[0].OwnerHostname == nil ||
				*cl.Vips[0].OwnerHostname != tc.slug+"-01") {
				t.Fatalf("VIP: %+v", cl.Vips)
			}
			if tc.vip == "" && len(cl.Vips) != 0 {
				t.Fatalf("VIP's zonder vip-parameter: %+v", cl.Vips)
			}

			// De diensten staan met unit en poort in de graaf, en draaien.
			var dg depGraph
			eventually(t, "diensten gezond", func() bool {
				c.do("GET", "/api/v1/dependency-graph?cluster_id="+res.ClusterID, nil, &dg)
				for _, s := range dg.Services {
					if s.Status != "healthy" {
						return false
					}
				}
				return len(dg.Services) == len(tc.services)
			})
			var got []string
			for _, s := range dg.Services {
				port := "-"
				if s.Port != nil {
					port = strconv.Itoa(*s.Port)
				}
				deps := []string{}
				for _, ed := range dg.Edges {
					if ed.From == s.ID {
						deps = append(deps, nameOf(dg, ed.To))
					}
				}
				slices.Sort(deps)
				line := strings.TrimSpace(strings.Join([]string{s.Name, s.Kind, s.Unit, port, strings.Join(deps, ",")}, " "))
				got = append(got, line)
				if s.Source != "template" || s.State != "confirmed" || len(s.Instances) != 2 {
					t.Errorf("dienst %s: %+v", s.Name, s)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.services) {
				t.Fatalf("diensten:\n%q\nwil\n%q", got, tc.services)
			}

			// Direct na de uitrol vindt de driftcontrole niets, en elke stap
			// is te controleren.
			var rep driftReport
			if s := c.do("POST", "/api/v1/clusters/"+res.ClusterID+"/drift/check", nil, &rep); s != 200 || len(rep.Nodes) != 2 {
				t.Fatalf("driftcontrole: %d %+v", s, rep)
			}
			for _, n := range rep.Nodes {
				if n.Status != "in_sync" || len(n.Findings) != 0 || len(n.Unchecked) != 0 || n.Error != "" {
					t.Fatalf("%s na de uitrol: %+v", n.Hostname, n)
				}
			}

			switch tc.template {
			case "cron":
				fi, err := os.Stat(filepath.Join(f.host("taken-02").Root, "usr/local/bin/cf-leader"))
				if err != nil || fi.Mode().Perm() != 0o755 {
					t.Fatalf("cf-leader op taken-02: %v %v", fi, err)
				}
			case "generic":
				if u, _ := f.host("app-02").Unit("cf-app"); !u.Enabled || !u.Active {
					t.Fatalf("cf-app op app-02: %+v", u)
				}
				var ft failoverList
				c.do("GET", "/api/v1/clusters/"+res.ClusterID+"/failover-tests", nil, &ft)
				if !ft.Options.DefaultFailback {
					t.Fatalf("generic gebruikt preempt: %+v", ft.Options)
				}
			}
		})
	}
}

func nameOf(g depGraph, id string) string {
	for _, s := range g.Services {
		if s.ID == id {
			return s.Name
		}
	}
	return id
}
