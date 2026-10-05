package proxmox

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/proxmox/pvefake"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"pve1.lan":                            "https://pve1.lan:8006",
		"pve1.lan:8443":                       "https://pve1.lan:8443",
		"10.0.0.5":                            "https://10.0.0.5:8006",
		"https://pve1.lan:8006/":              "https://pve1.lan:8006",
		"https://pve1.lan:8006/api2/json/":    "https://pve1.lan:8006",
		"https://pve.example.com":             "https://pve.example.com",
		" https://[fd00::5]:8006 ":            "https://[fd00::5]:8006",
		"http://pve1.lan:8006":                "",
		"https://pve1.lan:8006/iets":          "",
		"https://user:pw@pve1.lan:8006":       "",
		"":                                    "",
		"https://pve1.lan:8006/api2/json?x=1": "",
	} {
		got, err := NormalizeURL(in)
		if want == "" {
			if err == nil {
				t.Errorf("NormalizeURL(%q) = %q, verwacht een fout", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	if got := NormalizeFingerprint(FormatFingerprint(hex)); got != hex {
		t.Errorf("met dubbele punten: %q", got)
	}
	if got := NormalizeFingerprint(strings.ToUpper(hex)); got != hex {
		t.Errorf("hoofdletters: %q", got)
	}
	for _, bad := range []string{"", "ab:cd", strings.Repeat("zz", 32)} {
		if NormalizeFingerprint(bad) != "" {
			t.Errorf("%q aanvaard", bad)
		}
	}
}

func newFake(t *testing.T) (*pvefake.Server, *httptest.Server, string) {
	t.Helper()
	pve := pvefake.New("clusterforge@pve!cf=geheim")
	pve.AddHost(pvefake.Host{Name: "pve1", Online: true, MaxCPU: 8, MaxMem: 32 << 30})
	pve.AddHost(pvefake.Host{Name: "pve2", Online: true, MaxCPU: 8, MaxMem: 32 << 30})
	pve.AddGuest(pvefake.Guest{Type: "qemu", VMID: 101, Name: "web01", Node: "pve1", Status: "stopped", MaxMem: 2 << 30})
	pve.AddGuest(pvefake.Guest{Type: "lxc", VMID: 200, Name: "dns01", Node: "pve2", Status: "running"})
	srv := httptest.NewTLSServer(pve.Handler())
	t.Cleanup(srv.Close)
	return pve, srv, Fingerprint(srv.Certificate())
}

func TestClient(t *testing.T) {
	ctx := context.Background()
	pve, srv, fp := newFake(t)

	c, err := NewClient(Config{URL: srv.URL, TokenID: "clusterforge@pve!cf", TokenSecret: "geheim", Fingerprint: FormatFingerprint(fp)})
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.Version(ctx)
	if err != nil || v.Version != "8.4.1" {
		t.Fatalf("Version = %+v, %v", v, err)
	}
	res, err := c.Resources(ctx)
	if err != nil || len(res) != 4 {
		t.Fatalf("Resources = %d, %v", len(res), err)
	}
	if res[2].Type != "qemu" || res[2].VMID != 101 || res[2].Name != "web01" || res[2].Node != "pve1" {
		t.Errorf("resource = %+v", res[2])
	}

	web := Guest{Type: "qemu", Node: "pve1", VMID: 101}
	upid, err := c.Power(ctx, web, "start")
	if err != nil || !strings.HasPrefix(upid, "UPID:pve1:") {
		t.Fatalf("Power = %q, %v", upid, err)
	}
	st, err := c.TaskStatus(ctx, upid)
	if err != nil || st.Running() || !st.OK() {
		t.Fatalf("TaskStatus = %+v, %v", st, err)
	}
	if g, _ := pve.Guest(101); g.Status != "running" {
		t.Errorf("status na start = %s", g.Status)
	}
	if log, err := c.TaskLog(ctx, upid); err != nil || len(log) == 0 || log[len(log)-1] != "TASK OK" {
		t.Errorf("TaskLog = %q, %v", log, err)
	}

	// Een tweede start mislukt in de taak, zoals bij Proxmox.
	upid, _ = c.Power(ctx, web, "start")
	if st, _ := c.TaskStatus(ctx, upid); st.OK() || !strings.Contains(st.ExitStatus, "already running") {
		t.Errorf("tweede start: %+v", st)
	}

	if _, err = c.Snapshot(ctx, web, "voor-update", "test", true); err != nil {
		t.Fatal(err)
	}
	if snaps, err := c.Snapshots(ctx, web); err != nil || len(snaps) != 1 || snaps[0].Name != "voor-update" {
		t.Errorf("Snapshots = %+v, %v", snaps, err)
	}

	if _, err := c.Migrate(ctx, web, "pve2", true); err != nil {
		t.Fatal(err)
	}
	if g, _ := pve.Guest(101); g.Node != "pve2" {
		t.Errorf("node na migratie = %s", g.Node)
	}
	// Op de oude host bestaat de VM niet meer.
	var pe *Error
	if _, err := c.Power(ctx, web, "stop"); !errors.As(err, &pe) || pe.Code != 500 || !strings.Contains(err.Error(), "not on this node") {
		t.Errorf("stop op oude host: %v", err)
	}
	if !strings.Contains(strings.Join(pve.Calls(), "\n"), "POST /nodes/pve1/qemu/101/migrate") {
		t.Errorf("calls = %q", pve.Calls())
	}
}

func TestStopTask(t *testing.T) {
	ctx := context.Background()
	pve, srv, fp := newFake(t)
	pve.SetTaskDuration(time.Hour)
	c, _ := NewClient(Config{URL: srv.URL, TokenID: "clusterforge@pve!cf", TokenSecret: "geheim", Fingerprint: fp})
	upid, err := c.Power(ctx, Guest{Type: "lxc", Node: "pve2", VMID: 200}, "shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := c.TaskStatus(ctx, upid); !st.Running() {
		t.Fatalf("taak loopt niet: %+v", st)
	}
	if err := c.StopTask(ctx, upid); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.TaskStatus(ctx, upid); st.Running() || st.OK() {
		t.Errorf("na stoppen: %+v", st)
	}
}

func TestClientTLSAndAuth(t *testing.T) {
	ctx := context.Background()
	_, srv, fp := newFake(t)

	// Zonder vingerafdruk geldt de gewone CA-controle, en die faalt.
	c, _ := NewClient(Config{URL: srv.URL, TokenID: "clusterforge@pve!cf", TokenSecret: "geheim"})
	if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("zonder vingerafdruk: %v", err)
	}
	// Een andere vingerafdruk wordt geweigerd en de echte staat in de fout.
	c, _ = NewClient(Config{URL: srv.URL, TokenID: "clusterforge@pve!cf", TokenSecret: "geheim", Fingerprint: strings.Repeat("00", 32)})
	var fe *FingerprintError
	if _, err := c.Version(ctx); !errors.As(err, &fe) || fe.Got != fp {
		t.Errorf("andere vingerafdruk: %v", err)
	}
	// Een fout secret geeft een 401.
	c, _ = NewClient(Config{URL: srv.URL, TokenID: "clusterforge@pve!cf", TokenSecret: "fout", Fingerprint: fp})
	var pe *Error
	if _, err := c.Version(ctx); !errors.As(err, &pe) || pe.Code != 401 {
		t.Errorf("fout secret: %v", err)
	}

	p, err := Probe(ctx, srv.URL)
	if err != nil || p.Fingerprint != fp || p.Trusted {
		t.Errorf("Probe = %+v, %v", p, err)
	}
}
