package httpapi

import (
	"net/http"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

type cluster struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Environment string   `json:"environment"`
	Tags        []string `json:"tags"`
	NodeCount   int      `json:"node_count"`
	VipCount    int      `json:"vip_count"`
	Owners      []struct {
		Username string `json:"username"`
	} `json:"owners"`
	Nodes []node `json:"nodes"`
	Vips  []vip  `json:"vips"`
}

type node struct {
	ID          string  `json:"id"`
	Hostname    string  `json:"hostname"`
	ClusterID   *string `json:"cluster_id"`
	ClusterSlug *string `json:"cluster_slug"`
	PrimaryIP   *string `json:"primary_ip"`
	Lifecycle   string  `json:"lifecycle"`
}

type vip struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Vrid    *int   `json:"vrid"`
}

func adminClient(t *testing.T) (*testEnv, *client) {
	t.Helper()
	e := newTestEnv(t)
	e.createUser("admin", "een-lang-wachtwoord", store.UserRoleAdmin)
	c := e.client()
	if status, _, _ := c.login("admin", "een-lang-wachtwoord", ""); status != http.StatusOK {
		t.Fatalf("login: %d", status)
	}
	return e, c
}

func TestClusterCRUD(t *testing.T) {
	e, c := adminClient(t)
	e.createUser("piet", "een-lang-wachtwoord", store.UserRoleViewer)

	var users struct {
		Items []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	if status := c.do("GET", "/api/v1/users", nil, &users); status != http.StatusOK || len(users.Items) != 2 {
		t.Fatalf("users: %d %+v", status, users)
	}
	pietID := users.Items[1].ID

	var created cluster
	status := c.do("POST", "/api/v1/clusters", map[string]any{
		"slug": "web-prod", "name": "Web productie", "type": "keepalived", "environment": "prod",
		"tags": []string{"Web", "web", "dc1"}, "owner_ids": []string{pietID},
	}, &created)
	if status != http.StatusCreated {
		t.Fatalf("create: %d", status)
	}
	if len(created.Tags) != 2 || created.Tags[0] != "web" || len(created.Owners) != 1 || created.Owners[0].Username != "piet" {
		t.Fatalf("aangemaakt cluster: %+v", created)
	}

	var ae apiErr
	if status := c.do("POST", "/api/v1/clusters", map[string]any{
		"slug": "web-prod", "name": "Dubbel", "type": "keepalived", "environment": "prod",
	}, &ae); status != http.StatusConflict {
		t.Fatalf("dubbele slug: %d %+v", status, ae)
	}
	for _, bad := range []map[string]any{
		{"slug": "Web Prod", "name": "x", "type": "keepalived", "environment": "prod"},
		{"slug": "ok", "name": "x", "type": "kubernetes", "environment": "prod"},
		{"slug": "ok", "name": "x", "type": "nginx", "environment": "staging"},
		{"slug": "ok", "name": "x", "type": "nginx", "environment": "lab", "git_repo_url": "file:///etc"},
	} {
		if status := c.do("POST", "/api/v1/clusters", bad, &ae); status != http.StatusBadRequest {
			t.Errorf("ongeldige invoer %v: %d", bad, status)
		}
	}

	var updated cluster
	status = c.do("PATCH", "/api/v1/clusters/"+created.ID, map[string]any{"name": "Web prod", "owner_ids": []string{}}, &updated)
	if status != http.StatusOK || updated.Name != "Web prod" || updated.Slug != "web-prod" || len(updated.Owners) != 0 {
		t.Fatalf("update: %d %+v", status, updated)
	}

	var events struct {
		Items []struct {
			Action  string         `json:"action"`
			Payload map[string]any `json:"payload"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/events", nil, &events)
	if events.Items[0].Action != "cluster.updated" || events.Items[0].Payload["name"] == nil || events.Items[0].Payload["slug"] != nil {
		t.Fatalf("laatste event: %+v", events.Items[0])
	}

	// Een PATCH zonder verschil maakt geen event.
	c.do("PATCH", "/api/v1/clusters/"+created.ID, map[string]any{"name": "Web prod"}, nil)
	var again struct{ Items []struct{ ID int64 } }
	c.do("GET", "/api/v1/events", nil, &again)
	if len(again.Items) != len(events.Items) {
		t.Fatalf("event zonder wijziging: %d -> %d", len(events.Items), len(again.Items))
	}

	if status := c.do("GET", "/api/v1/clusters/00000000-0000-0000-0000-000000000000", nil, nil); status != http.StatusNotFound {
		t.Fatalf("onbekend cluster: %d", status)
	}
	if status := c.do("DELETE", "/api/v1/clusters/"+created.ID, nil, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if status := c.do("DELETE", "/api/v1/clusters/"+created.ID, nil, nil); status != http.StatusNotFound {
		t.Fatalf("tweede delete: %d", status)
	}
}

func TestNodesAndVIPs(t *testing.T) {
	_, c := adminClient(t)

	var cl cluster
	c.do("POST", "/api/v1/clusters", map[string]any{"slug": "lb", "name": "Loadbalancers", "type": "keepalived", "environment": "lab"}, &cl)

	var n1, n2 node
	if status := c.do("POST", "/api/v1/nodes", map[string]any{
		"hostname": "lb1.lab", "cluster_id": cl.ID, "primary_ip": "10.0.0.11",
	}, &n1); status != http.StatusCreated {
		t.Fatalf("node 1: %d", status)
	}
	if n1.ClusterSlug == nil || *n1.ClusterSlug != "lb" || n1.Lifecycle != "active" || *n1.PrimaryIP != "10.0.0.11" {
		t.Fatalf("node 1: %+v", n1)
	}
	if status := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "LB1.lab"}, nil); status != http.StatusConflict {
		t.Fatalf("dubbele hostname: %d", status)
	}
	if status := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb2.lab", "primary_ip": "10.0.0.300"}, nil); status != http.StatusBadRequest {
		t.Fatalf("ongeldig IP: %d", status)
	}
	if status := c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb2.lab", "cluster_id": "00000000-0000-0000-0000-000000000000"}, nil); status != http.StatusBadRequest {
		t.Fatalf("onbekend cluster: %d", status)
	}
	c.do("POST", "/api/v1/nodes", map[string]any{"hostname": "lb2.lab"}, &n2)
	if n2.ClusterID != nil || n2.PrimaryIP != nil {
		t.Fatalf("losse node: %+v", n2)
	}

	// Node aan het cluster hangen, en weer los met null.
	var moved node
	c.do("PATCH", "/api/v1/nodes/"+n2.ID, map[string]any{"cluster_id": cl.ID, "lifecycle": "maintenance"}, &moved)
	if moved.ClusterID == nil || moved.Lifecycle != "maintenance" {
		t.Fatalf("verplaatst: %+v", moved)
	}

	var v vip
	if status := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{
		"address": "10.0.0.10", "interface": "eth0", "vrid": 51,
	}, &v); status != http.StatusCreated || v.Vrid == nil || *v.Vrid != 51 {
		t.Fatalf("vip: %d %+v", status, v)
	}
	if status := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.0.10"}, nil); status != http.StatusConflict {
		t.Fatalf("dubbele vip: %d", status)
	}
	if status := c.do("POST", "/api/v1/clusters/"+cl.ID+"/vips", map[string]any{"address": "10.0.0.12", "vrid": 300}, nil); status != http.StatusBadRequest {
		t.Fatalf("ongeldige vrid: %d", status)
	}
	var cleared vip
	c.do("PATCH", "/api/v1/vips/"+v.ID, map[string]any{"vrid": nil}, &cleared)
	if cleared.Vrid != nil || cleared.Address != "10.0.0.10" {
		t.Fatalf("vrid wissen: %+v", cleared)
	}

	var detail cluster
	c.do("GET", "/api/v1/clusters/"+cl.ID, nil, &detail)
	if len(detail.Nodes) != 2 || len(detail.Vips) != 1 {
		t.Fatalf("detail: %+v", detail)
	}
	var clusters struct{ Items []cluster }
	c.do("GET", "/api/v1/clusters", nil, &clusters)
	if clusters.Items[0].NodeCount != 2 || clusters.Items[0].VipCount != 1 {
		t.Fatalf("lijst: %+v", clusters.Items)
	}

	c.do("PATCH", "/api/v1/nodes/"+n2.ID, map[string]any{"cluster_id": nil}, &moved)
	if moved.ClusterID != nil || moved.ClusterSlug != nil {
		t.Fatalf("losgehaald: %+v", moved)
	}

	// Cluster weg: nodes blijven zonder cluster, VIP's verdwijnen.
	c.do("DELETE", "/api/v1/clusters/"+cl.ID, nil, nil)
	var left node
	c.do("GET", "/api/v1/nodes/"+n1.ID, nil, &left)
	if left.ClusterID != nil {
		t.Fatalf("node na verwijderen cluster: %+v", left)
	}
	if status := c.do("PATCH", "/api/v1/vips/"+v.ID, map[string]any{"description": "x"}, nil); status != http.StatusNotFound {
		t.Fatalf("vip na verwijderen cluster: %d", status)
	}
	if status := c.do("DELETE", "/api/v1/nodes/"+n1.ID, nil, nil); status != http.StatusNoContent {
		t.Fatalf("node delete: %d", status)
	}
}

func TestInventoryWritesAdminOnly(t *testing.T) {
	e, admin := adminClient(t)
	var cl cluster
	admin.do("POST", "/api/v1/clusters", map[string]any{"slug": "db", "name": "Database", "type": "postgresql_ha", "environment": "test"}, &cl)

	e.createUser("viewer", "een-lang-wachtwoord", store.UserRoleViewer)
	v := e.client()
	v.login("viewer", "een-lang-wachtwoord", "")

	if status := v.do("GET", "/api/v1/clusters/"+cl.ID, nil, nil); status != http.StatusOK {
		t.Fatalf("viewer lezen: %d", status)
	}
	writes := []struct{ method, path string }{
		{"POST", "/api/v1/clusters"},
		{"PATCH", "/api/v1/clusters/" + cl.ID},
		{"DELETE", "/api/v1/clusters/" + cl.ID},
		{"POST", "/api/v1/nodes"},
		{"POST", "/api/v1/clusters/" + cl.ID + "/vips"},
	}
	for _, w := range writes {
		if status := v.do(w.method, w.path, map[string]any{}, nil); status != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d", w.method, w.path, status)
		}
	}
}
