package audit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var (
	jonasID = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	web01ID = uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	web02ID = uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	web03ID = uuid.MustParse("00000000-0000-0000-0000-0000000000b3")
	webID   = uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
	pveID   = uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
	testIDs = names{
		byID: map[string]string{web01ID.String(): "web01", web02ID.String(): "web02", webID.String(): "webcluster-prod",
			pveID.String(): "Thuislab"},
		users:      map[string]string{jonasID.String(): "Jonas"},
		byUsername: map[string]uuid.UUID{"jonas": jonasID},
	}
)

// row bootst een rij uit ListAudit na, met de namen die de query opzoekt.
func row(action, subjectType, subjectID string, payload map[string]any) store.ListAuditRow {
	b, _ := json.Marshal(payload)
	r := store.ListAuditRow{
		ID: 1, Action: action, SubjectType: subjectType, SubjectID: subjectID, Payload: b,
		ActorType: store.ActorTypeUser, ActorID: jonasID.String(), ActorUsername: ptr("Jonas"),
	}
	switch subjectType {
	case "cluster":
		r.ClusterRef, r.ClusterName = subjectID, testIDs.byID[subjectID]
		r.ClusterExists = r.ClusterName != ""
	case "node":
		r.NodeRef, r.NodeHostname = &subjectID, testIDs.byID[subjectID]
		r.NodeExists = r.NodeHostname != ""
	case "job":
		r.JobRef = &subjectID
	}
	return r
}

func ptr[T any](v T) *T { return &v }

func TestSummaries(t *testing.T) {
	ip := map[string]any{"ip": "192.0.2.10"}
	for _, tc := range []struct {
		r    store.ListAuditRow
		want string
	}{
		{row("auth.login", "user", jonasID.String(), ip), "Ingelogd vanaf 192.0.2.10"},
		{row("auth.login_failed", "user", jonasID.String(), map[string]any{"ip": "203.0.113.5", "reason": "bad_totp"}),
			"Mislukte inlogpoging voor Jonas (foute code voor tweestapsverificatie) vanaf 203.0.113.5"},
		{row("auth.login_failed", "user", "onbekend", map[string]any{"ip": "203.0.113.5", "reason": "unknown_user"}),
			"Mislukte inlogpoging met een onbekende gebruikersnaam vanaf 203.0.113.5"},
		// Een oude regel met de getypte naam van een bestaande gebruiker.
		{row("auth.login_failed", "user", "JONAS", map[string]any{"reason": "bad_password"}), "Mislukte inlogpoging voor Jonas (fout wachtwoord)"},
		{row("user.created", "user", jonasID.String(), map[string]any{"username": "Jonas", "role": "admin"}), "Gebruiker Jonas aangemaakt als beheerder"},
		{row("node.created", "node", web03ID.String(), map[string]any{"hostname": "web03"}), "Node web03 toegevoegd"},
		{row("node.created", "node", web03ID.String(), map[string]any{"hostname": "web03", "via": "enrollment"}),
			"Node web03 toegevoegd door aanmelding van de agent"},
		{row("cluster.updated", "cluster", webID.String(), map[string]any{"tags": map[string]any{"from": []any{}, "to": []any{"web"}},
			"owner_ids": map[string]any{"from": []any{}, "to": []any{jonasID.String()}}}), "Cluster webcluster-prod gewijzigd: tags, owners"},
		{row("node.updated", "node", web01ID.String(), map[string]any{"cluster_id": map[string]any{"from": nil, "to": webID.String()}}),
			"Node web01 gewijzigd: cluster webcluster-prod"},
		{row("node.updated", "node", web01ID.String(), map[string]any{"cluster_id": map[string]any{"from": webID.String(), "to": nil}}),
			"Node web01 gewijzigd: cluster webcluster-prod weggehaald"},
		{row("proxmox.updated", "proxmox", pveID.String(), map[string]any{"token_secret": "gewijzigd"}),
			"Proxmox-koppeling Thuislab gewijzigd: token-secret gewijzigd"},
		{row("proxmox.updated", "proxmox", uuid.NewString(), map[string]any{"name": map[string]any{"from": "Lab", "to": "Thuislab"}}),
			"Proxmox-koppeling Thuislab gewijzigd: naam van Lab naar Thuislab"},
		{row("node.lifecycle_changed", "node", web01ID.String(), map[string]any{"hostname": "web01", "from": "active", "to": "maintenance", "reason": "kernelupdate"}),
			"web01 van actief naar onderhoud (reden: kernelupdate)"},
		{row("node.status_changed", "node", web01ID.String(), map[string]any{"hostname": "web01", "from": "healthy", "to": "degraded", "reason": "nginx staat stil"}),
			"web01 van gezond naar verminderd: nginx staat stil"},
		{row("vip.owner_changed", "vip", "v1", map[string]any{"address": "10.0.20.100", "from": web01ID.String(), "to": web02ID.String(), "owner_hostname": "web02"}),
			"VIP 10.0.20.100 verhuisd van web01 naar web02"},
		{row("vip.owner_changed", "vip", "v1", map[string]any{"address": "10.0.20.100", "from": web01ID.String(), "to": nil, "owner_hostname": nil}),
			"VIP 10.0.20.100 heeft geen eigenaar meer"},
		{row("cluster.deployed", "cluster", webID.String(), map[string]any{"name": "Web", "template": "keepalived-nginx", "version": "1.0.0", "nodes": 2}),
			"Cluster webcluster-prod uitgerold uit keepalived-nginx 1.0.0 met 2 nodes"},
		{row("vm.status_changed", "node", web01ID.String(), map[string]any{"hostname": "web01", "vmid": 101, "from": "stopped", "to": "running"}),
			"VM 101 van web01 gaat van uit naar draait"},
		{row("job.failed", "job", "j1", map[string]any{"title": "Herstarten: web01", "error": "agent antwoordt niet"}),
			"Taak mislukt: Herstarten: web01: agent antwoordt niet"},
		{row("audit.exported", "audit", "", map[string]any{"count": 6}), "Logboek geëxporteerd (6 regels)"},
		{row("backup.stale", "node", web01ID.String(), map[string]any{"hostname": "web01", "vmid": 101, "max_age_hours": 30}),
			"Back-up van web01 te oud: de nieuwste is ouder dan 30 uur"},
		{row("backup.missing", "proxmox", pveID.String(), map[string]any{"vmid": 100, "name": "clusterforge"}),
			"Geen back-up van VM 100 (clusterforge) in Proxmox"},
		{row("backup.fresh", "node", web01ID.String(), map[string]any{"hostname": "web01", "vmid": 101}), "Back-up van web01 weer vers"},
		{row("backup.inventory_failed", "proxmox", pveID.String(), map[string]any{"name": "Thuislab", "error": "geen rechten"}),
			"Back-ups van Proxmox-koppeling Thuislab niet te lezen: geen rechten"},
		{row("backup.policy_updated", "cluster", webID.String(), map[string]any{"name": "Web",
			"max_age_hours": map[string]any{"from": 30, "to": 48}}),
			"Back-upbeleid van cluster webcluster-prod gewijzigd: maximale leeftijd in uren van 30 naar 48"},
		{row("backup.watch_updated", "proxmox", pveID.String(), map[string]any{"name": "Thuislab",
			"watch": map[string]any{"from": []any{}, "to": []any{"VM 100 (clusterforge)"}}}),
			"Lijst ook bewaken van Proxmox-koppeling Thuislab gewijzigd: ook bewaken VM 100 (clusterforge)"},
		{row("iets.nieuws", "node", web01ID.String(), nil), "iets.nieuws: web01"},
		{row("iets.nieuws", "test", "", nil), "iets.nieuws"},
	} {
		got := describe(tc.r, testIDs)
		if got.Summary != tc.want {
			t.Errorf("%s:\n kreeg  %q\n wilde  %q", tc.r.Action, got.Summary, tc.want)
		}
	}
}

func TestChanges(t *testing.T) {
	e := describe(row("cluster.updated", "cluster", webID.String(), map[string]any{
		"environment": map[string]any{"from": "test", "to": "prod"},
		"name":        map[string]any{"from": "Web", "to": "webcluster-prod"},
		"owner_ids":   map[string]any{"from": []any{}, "to": []any{jonasID.String(), uuid.NewString()}},
	}), testIDs)
	got := []string{}
	for _, c := range e.Changes {
		got = append(got, c.Label+": "+c.From+" → "+c.To)
	}
	want := "Naam: Web → webcluster-prod | Omgeving: Test → Productie | Owners:  → Jonas, onbekende gebruiker"
	if strings.Join(got, " | ") != want {
		t.Fatalf("wijzigingen:\n %s\n %s", strings.Join(got, " | "), want)
	}
}

func TestEveryKnownActionHasASentence(t *testing.T) {
	for a := range events.Known {
		subject := strings.SplitN(a, ".", 2)[0]
		e := describe(row(a, subject, web01ID.String(), map[string]any{"hostname": "web01", "name": "web", "title": "t", "address": "10.0.0.1"}), testIDs)
		if e.Summary == "" || e.Summary == a || e.Category == "" {
			t.Errorf("%s: zin %q, soort %q", a, e.Summary, e.Category)
		}
	}
}

func TestWho(t *testing.T) {
	r := row("job.succeeded", "job", "j1", map[string]any{"title": "Herstarten: web01"})
	r.ActorType, r.ActorID, r.ActorUsername = store.ActorTypeSystem, "", nil
	r.JobRequestedBy, r.JobRequestedByUsername = &jonasID, ptr("Jonas")
	e := describe(r, testIDs)
	if e.Actor.Name != "systeem" || e.OnBehalfOf == nil || e.OnBehalfOf.Name != "Jonas" {
		t.Fatalf("namens: %+v", e)
	}
	r = row("node.facts_changed", "node", web01ID.String(), map[string]any{"hostname": "web01", "changed": []any{"kernel", "packages"}})
	r.ActorType, r.ActorHostname = store.ActorTypeAgent, "web01"
	if e := describe(r, testIDs); e.Actor.Name != "agent op web01" || e.Summary != "Facts van web01 gewijzigd: kernel, packages" {
		t.Fatalf("agent: %+v", e)
	}
}

func TestLikeEscaping(t *testing.T) {
	p := params(Filter{Query: ` 100%_\ `}, 10)
	if p.Pattern == nil || *p.Pattern != `%100\%\_\\%` {
		t.Fatalf("patroon: %v", p.Pattern)
	}
	if p := params(Filter{Category: "nergens"}, 10); p.Actions == nil || len(p.Actions) != 0 {
		t.Fatal("een soort zonder actions mag niets vinden, niet alles")
	}
}
