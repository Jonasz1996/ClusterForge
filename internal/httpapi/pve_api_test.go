package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPVEAPI: fase 26. Het datacenter van de host met een agent, het
// overzicht, lezen van de API, en veranderen als taak met bevestiging en
// versleutelde geheimen.
func TestPVEAPI(t *testing.T) {
	e, c := adminClient(t)
	pve, _, _ := newPVE(t)
	sim := &pveSim{t: t, pve: pve}
	sim.extra = func(cmd string, out io.Writer) (int, bool) {
		switch {
		case strings.HasPrefix(cmd, "pvesh get /cluster/resources "):
			_, _ = io.WriteString(out, `[{"type":"node","node":"pve1","status":"online","maxcpu":8,"maxmem":34359738368,"mem":1073741824},
				{"type":"lxc","node":"pve1","vmid":101,"status":"running","maxcpu":2,"maxmem":2147483648},
				{"type":"storage","node":"pve1","storage":"local","plugintype":"dir","disk":97,"maxdisk":100,"status":"available"}]`)
		case strings.HasPrefix(cmd, "pvesh get /cluster/status "):
			_, _ = io.WriteString(out, `[{"type":"cluster","name":"thuislab","quorate":1,"version":3},{"type":"node","name":"pve1","online":1,"ip":"10.0.0.11"}]`)
		case strings.HasPrefix(cmd, "pvesh get /cluster/ha/resources "):
			_, _ = io.WriteString(out, `[{"sid":"ct:101","state":"started"}]`)
		case strings.HasPrefix(cmd, "pvesh get /cluster/"), strings.HasPrefix(cmd, "pvesh get /nodes/pve1/status "):
			_, _ = io.WriteString(out, `[]`)
		case strings.HasPrefix(cmd, "pvesh create /nodes/pve1/status "), strings.HasPrefix(cmd, "pvesh create /storage "):
			_, _ = io.WriteString(out, "null\n")
		default:
			return 0, false
		}
		return 0, true
	}
	startPVEHost(t, e, c, sim, []byte("x"))

	var dcs struct {
		Items []struct {
			ID  string  `json:"id"`
			Via *string `json:"via"`
		} `json:"items"`
	}
	eventually(t, "datacenter", func() bool {
		c.do("GET", "/api/v1/pve/datacenters", nil, &dcs)
		return len(dcs.Items) == 1 && dcs.Items[0].Via != nil
	})
	if dcs.Items[0].ID != "thuislab" {
		t.Fatalf("datacenters: %+v", dcs)
	}

	var o struct {
		Name     string `json:"name"`
		Quorate  bool   `json:"quorate"`
		CTs      int    `json:"cts"`
		Warnings []struct {
			Text string `json:"text"`
		} `json:"warnings"`
	}
	if s := c.do("GET", "/api/v1/pve/datacenters/thuislab/overview", nil, &o); s != 200 || o.Name != "thuislab" || !o.Quorate || o.CTs != 1 {
		t.Fatalf("overzicht: %d %+v", s, o)
	}
	if len(o.Warnings) == 0 || !strings.Contains(o.Warnings[0].Text, "local op pve1 is 97% vol") {
		t.Errorf("waarschuwingen: %+v", o.Warnings)
	}

	var got struct {
		Data []map[string]any `json:"data"`
	}
	if s := c.do("GET", "/api/v1/pve/datacenters/thuislab/api?path=/cluster/ha/resources", nil, &got); s != 200 || len(got.Data) != 1 || got.Data[0]["sid"] != "ct:101" {
		t.Fatalf("lezen: %d %+v", s, got)
	}
	var de deployErr
	if s := c.do("GET", "/api/v1/pve/datacenters/elders/api?path=/cluster/status", nil, &de); s != http.StatusNotFound {
		t.Errorf("onbekend datacenter: %d", s)
	}

	// Herstarten vraagt de naam van de host.
	call := map[string]any{"method": "create", "path": "/nodes/pve1/status", "params": map[string]string{"command": "reboot"}}
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/api", call, &de); s != http.StatusBadRequest || !strings.Contains(de.Message, "pve1") {
		t.Fatalf("zonder bevestiging: %d %+v", s, de)
	}
	call["confirm"] = "pve1"
	var j job
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/api", call, &j); s != http.StatusAccepted || j.Title != "Pve1 herstarten" {
		t.Fatalf("herstarten: %d %+v", s, j)
	}
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" || len(sim.called("pvesh create /nodes/pve1/status --output-format json --command=reboot")) != 1 {
		t.Fatalf("herstarten: %s %s %v", j.Status, j.Error, sim.called("pvesh create"))
	}

	// Een wachtwoord komt bij pvesh, maar niet leesbaar in de database.
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/api", map[string]any{
		"method": "create", "path": "/storage",
		"params": map[string]string{"storage": "pbs", "type": "pbs", "server": "10.0.0.5", "datastore": "main", "username": "cf@pbs", "password": "PBS-geheim-42"},
	}, &j); s != http.StatusAccepted {
		t.Fatalf("storage: %d", s)
	}
	if j = waitJob(t, c, j.ID); j.Status != "succeeded" || len(sim.called("pvesh create /storage")) != 1 || !strings.Contains(sim.called("pvesh create /storage")[0], "--password=PBS-geheim-42") {
		t.Fatalf("storage: %s %s %v", j.Status, j.Error, sim.called("pvesh create /storage"))
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE params::text LIKE '%PBS-geheim-42%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("wachtwoord leesbaar in de taak: %d %v", n, err)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE payload::text LIKE '%PBS-geheim-42%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("wachtwoord in het logboek: %d %v", n, err)
	}

	// Wat ClusterForge niet kent, gaat niet.
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/api", map[string]any{"method": "create", "path": "/nodes/pve1/execute"}, &de); s != http.StatusBadRequest {
		t.Errorf("onbekend pad: %d", s)
	}
	if s := c.do("POST", "/api/v1/pve/datacenters/thuislab/api", map[string]any{
		"method": "set", "path": "/nodes/pve1/qemu/101/config", "params": map[string]string{"args": "-device x"},
	}, &de); s != http.StatusBadRequest || !strings.Contains(de.Message, "args") {
		t.Errorf("args: %d %+v", s, de)
	}
}
