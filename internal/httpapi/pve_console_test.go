package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestPVEConsole: een console gaat van de websocket via de server en NATS
// naar de agent en terug. Het programma is hier een shell die een
// RFB-groet stuurt en daarna alles terugkaatst.
func TestPVEConsole(t *testing.T) {
	e, c := adminClient(t)
	pve, _, _ := newPVE(t)
	sim := &pveSim{t: t, pve: pve}
	sim.extra = func(cmd string, out io.Writer) (int, bool) {
		switch {
		case strings.HasPrefix(cmd, "pvesh get /cluster/status "):
			_, _ = io.WriteString(out, `[{"type":"cluster","name":"thuislab","quorate":1,"version":3},{"type":"node","name":"pve1","online":1,"ip":"10.0.0.11"}]`)
		case strings.HasPrefix(cmd, "pvesh get /cluster/"):
			_, _ = io.WriteString(out, `[]`)
		default:
			return 0, false
		}
		return 0, true
	}
	var gotPW string
	sim.console = func(req protocol.ConsoleRequest, pw string) *exec.Cmd {
		if req.Kind == protocol.ConsoleHost {
			return exec.Command("sh")
		}
		gotPW = pw
		return exec.Command("sh", "-c", "printf 'RFB 003.008\\n'; exec cat")
	}
	startPVEHost(t, e, c, sim, []byte("x"))
	eventually(t, "datacenter", func() bool {
		var dcs struct {
			Items []struct {
				Via *string `json:"via"`
			} `json:"items"`
		}
		c.do("GET", "/api/v1/pve/datacenters", nil, &dcs)
		return len(dcs.Items) == 1 && dcs.Items[0].Via != nil
	})

	var bad apiErr
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/console", map[string]any{"node": "pve1", "kind": "vnc"}, &bad); s != http.StatusBadRequest {
		t.Fatalf("vnc zonder vmid: %d %+v", s, bad)
	}
	var opened struct {
		Session  string `json:"session"`
		Password string `json:"password"`
		Title    string `json:"title"`
	}
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/console", map[string]any{"node": "pve1", "kind": "vnc", "vmid": 101}, &opened); s != http.StatusCreated {
		t.Fatalf("console openen: %d", s)
	}
	if len(opened.Password) != 8 || opened.Password != gotPW || opened.Title != "VM 101 op pve1" {
		t.Fatalf("geopend: %+v (agent kreeg %q)", opened, gotPW)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/pve/console/" + opened.Session
	// Een andere Origin (een vreemde site met de cookie) wordt geweigerd.
	if _, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": {"http://evil.example"}}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("vreemde origin: %v %v", err, resp)
	}
	// Eerst koppelen, dan Accept: de geweigerde poging sloot de sessie.
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/console", map[string]any{"node": "pve1", "kind": "vnc", "vmid": 101}, &opened); s != http.StatusCreated {
		t.Fatalf("console opnieuw openen: %d", s)
	}
	url = "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/pve/console/" + opened.Session
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}, Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	read := func(want string) {
		t.Helper()
		var got string
		for !strings.Contains(got, want) {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatalf("lezen (tot nu %q): %v", got, err)
			}
			got += string(b)
		}
	}
	read("RFB 003.008\n")
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("hallo console")); err != nil {
		t.Fatal(err)
	}
	read("hallo console")

	// Een tweede venster op dezelfde sessie mag niet.
	if _, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}}); err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("tweede venster: %v %v", err, resp)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")

	// Een terminal op de host: een pseudo-terminal met de grootte van de
	// browser, en een andere grootte na een resize.
	var term struct {
		Session string `json:"session"`
	}
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/console", map[string]any{"node": "pve1", "kind": "host", "cols": 100, "rows": 30}, &term); s != http.StatusCreated {
		t.Fatalf("terminal openen: %d", s)
	}
	ws, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/v1/pve/console/"+term.Session, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("stty size\n"))
	read("30 100")
	_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("stty size\n"))
	read("40 120")
	// exit sluit de shell; de server sluit de websocket dan netjes.
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("exit\n"))
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("na exit: %v", err)
			}
			break
		}
	}

	// Het auditlog kent het openen; het wachtwoord staat er niet in.
	var evs struct {
		Items []map[string]any `json:"items"`
	}
	c.do("GET", "/api/v1/events?limit=200", nil, &evs)
	found := false
	for _, ev := range evs.Items {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), opened.Password) {
			t.Fatal("het VNC-wachtwoord staat in het auditlog")
		}
		found = found || ev["action"] == "pvehost.console_opened"
	}
	if !found {
		t.Fatal("geen auditevent voor de console")
	}
}
