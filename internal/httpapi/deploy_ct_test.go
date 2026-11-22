package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

// claimFrom meldt een agent aan zonder token, zoals cf-agent in een CT uit
// een CT-template: vanaf addr (via X-Forwarded-For van een vertrouwde
// proxy), en opnieuw zolang er nog geen uitrol op hem wacht.
func claimFrom(server, hostname, addr string, wait time.Duration) func(root, _ string) (*agent.Config, error) {
	return func(root, _ string) (*agent.Config, error) {
		client := &http.Client{Timeout: 10 * time.Second, Transport: forwardedFor{addr}}
		deadline := time.Now().Add(wait)
		for {
			c, err := agent.Enroll(context.Background(), agent.EnrollOptions{
				ServerURL: server, Hostname: hostname, Claim: true, AllowHTTP: true, Version: "test", Root: root, HTTPClient: client,
			})
			if !errors.Is(err, agent.ErrNotClaimed) || time.Now().After(deadline) {
				return c, err
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
}

type forwardedFor struct{ addr string }

func (f forwardedFor) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forwarded-For", f.addr)
	return http.DefaultTransport.RoundTrip(r)
}

// TestDeployCT: een cluster op nieuwe CT's uit een CT-template. ClusterForge
// kloont de CT's, zet hostname en adres in net0, en de agent in elke CT
// meldt zich zelf aan, alleen vanaf het adres dat de uitrol hem gaf.
func TestDeployCT(t *testing.T) {
	e, c := adminClient(t)
	pve, srv, fp := newPVE(t)
	pve.AddGuest(pvefake.Guest{
		Type: "lxc", VMID: 9100, Name: "debian-13-clusterforge-ct", Node: "pve1", Status: "stopped", Template: true, MaxCPU: 1, MaxMem: 1 << 30,
		Config: map[string]string{
			"rootfs": "ceph:base-9100-disk-0,size=4G", "hostname": "debian-13-clusterforge-ct",
			"net0": "name=eth0,bridge=vmbr0,ip=dhcp", "ostype": "debian", "unprivileged": "1",
		},
	})
	// De agents komen binnen via een vertrouwde proxy, die hun adres in
	// X-Forwarded-For zet.
	e.api.cfg.TrustProxyHeaders = true
	proxied := httptest.NewServer(e.api.Handler())
	t.Cleanup(proxied.Close)
	addrs := map[string]string{"web-01": "10.0.20.11", "web-02": "10.0.20.12"}
	f := newFleet(t, addrs)
	// Een indringer met de juiste hostname maar een ander adres komt er
	// niet in, ook niet terwijl de uitrol op web-01 wacht.
	intruder := make(chan error, 1)
	pve.OnPower(func(vmid int, status string) {
		g, ok := pve.Guest(vmid)
		if !ok || g.Type != "lxc" || status != "running" {
			return
		}
		if g.Name == "web-01" {
			// Wachten tot de uitrol het token klaarzet, en dan eerst de
			// indringer.
			for range 200 {
				var n int
				_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM enrollment_tokens WHERE claim_ip = '10.0.20.11'").Scan(&n)
				if n > 0 {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			_, err := claimFrom(proxied.URL, "web-01", "10.0.20.99", 0)(t.TempDir(), "")
			intruder <- err
		}
		f.start(g.Name, claimFrom(proxied.URL, g.Name, addrs[g.Name], 30*time.Second))
	})
	pve.OnFileWrite(func(vmid int, name, file, _ string) {
		t.Errorf("bestand %s in %s (%d): een CT heeft geen guest agent", file, name, vmid)
	})

	var conn pveConn
	if s := c.do("POST", "/api/v1/proxmox", map[string]any{
		"name": "Thuislab", "api_url": srv.URL, "token_id": "clusterforge@pve!cf", "token_secret": "geheim-secret", "tls_fingerprint": fp,
	}, &conn); s != 201 {
		t.Fatalf("koppelen: %d", s)
	}
	request := func(change func(target map[string]any)) map[string]any {
		target := map[string]any{
			"proxmox_id": conn.ID, "image_vmid": 9100, "network": "static", "vlan": 199,
			"first_ip": "10.0.20.11/24", "gateway": "10.0.20.1", "dns": []string{"10.0.20.1"},
		}
		if change != nil {
			change(target)
		}
		return map[string]any{
			"template": "keepalived-nginx",
			"cluster":  map[string]any{"name": "Web", "slug": "web", "environment": "lab"},
			"params":   map[string]any{"vip": "10.0.20.100", "auth_pass": "geheim12"},
			"target":   target, "server_url": e.srv.URL,
		}
	}

	// Een CT kan niet met DHCP, en SSH-sleutels zet Proxmox niet bij een kloon.
	for name, tc := range map[string]struct {
		change func(map[string]any)
		field  string
	}{
		"dhcp": {func(tg map[string]any) { tg["network"] = "dhcp" }, "target.network"},
		"ssh": {func(tg map[string]any) {
			tg["ssh_keys"] = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJvbmFzIGtleQ jonas@laptop"
		}, "target.ssh_keys"},
	} {
		var er deployErr
		if s := c.do("POST", "/api/v1/deployments/plan", request(tc.change), &er); s != http.StatusBadRequest || er.Field == nil || *er.Field != tc.field {
			t.Errorf("%s: %d %+v", name, s, er)
		}
	}

	var res struct {
		Job       job    `json:"job"`
		ClusterID string `json:"cluster_id"`
	}
	if s := c.do("POST", "/api/v1/deployments", request(nil), &res); s != http.StatusAccepted {
		t.Fatalf("uitrollen: %d", s)
	}
	if j := waitJobLong(t, c, res.Job.ID); j.Status != "succeeded" {
		t.Fatalf("uitrol: %s %s", j.Status, j.Error)
	}
	if err := <-intruder; !errors.Is(err, agent.ErrNotClaimed) {
		t.Fatalf("indringer: %v", err)
	}
	var wrongIP int
	_ = e.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM events WHERE action = 'agent.enroll_failed' AND payload->>'reason' = 'claim_wrong_ip' AND payload->>'ip' = '10.0.20.99'").Scan(&wrongIP)
	if wrongIP == 0 {
		t.Fatal("geen event voor de aanmelding vanaf het verkeerde adres")
	}

	// Proxmox kreeg CT's: hostname bij het klonen, adres en VLAN in net0,
	// geen cloud-init, en de schijf rootfs vergroot.
	var cts []pvefake.Guest
	for _, id := range pve.Guests() {
		if g, _ := pve.Guest(id); g.Type == "lxc" && !g.Template && strings.HasPrefix(g.Name, "web-") {
			cts = append(cts, g)
		}
	}
	slices.SortFunc(cts, func(a, b pvefake.Guest) int { return strings.Compare(a.Name, b.Name) })
	if len(cts) != 2 {
		t.Fatalf("CT's: %+v", cts)
	}
	for i, g := range cts {
		ip := addrs[g.Name]
		if g.Config["hostname"] != g.Name || g.Config["net0"] != "name=eth0,bridge=vmbr0,tag=199,ip="+ip+"/24,gw=10.0.20.1" ||
			g.Config["ipconfig0"] != "" || g.Config["nameserver"] != "10.0.20.1" || !strings.Contains(g.Config["rootfs"], "size=20G") ||
			g.Status != "running" || g.Config["onboot"] != "1" {
			t.Fatalf("CT %d: %+v", i, g)
		}
	}
	calls := strings.Join(pve.Calls(), "\n")
	if strings.Contains(calls, "/agent/") || !strings.Contains(calls, "/lxc/9100/clone") ||
		!slices.ContainsFunc(pve.Calls(), func(c string) bool {
			return strings.HasPrefix(c, "PUT /nodes/") && strings.Contains(c, "/lxc/") && strings.HasSuffix(c, "/config")
		}) {
		t.Fatalf("Proxmox-aanroepen:\n%s", calls)
	}

	// Het claimtoken is gebruikt; zonder uitrol komt er niemand meer binnen.
	if _, err := claimFrom(proxied.URL, "web-01", "10.0.20.11", 0)(t.TempDir(), ""); !errors.Is(err, agent.ErrNotClaimed) {
		t.Fatalf("tweede aanmelding: %v", err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM agents WHERE revoked_at IS NULL").Scan(&n)
	if n != 2 {
		t.Fatalf("%d agents", n)
	}
}
