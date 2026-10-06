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
		// Het IP uit de herkomst telt ook.
		{row("auth.totp_setup_started", "user", jonasID.String(), map[string]any{"origin": map[string]any{"ip": "192.0.2.11", "session": "deadbeef"}}),
			"Instellen van tweestapsverificatie gestart vanaf 192.0.2.11"},
		{row("auth.reauth_failed", "user", jonasID.String(), map[string]any{"what": "totp_disable", "reason": "bad_password"}),
			"Bevestiging mislukt bij tweestapsverificatie uitzetten: fout wachtwoord"},
		{row("auth.rate_limited", "auth", "login", ip), "Loginlimiet bereikt vanaf 192.0.2.10"},
		{row("audit.throttled", "audit", "agent.enroll_failed", map[string]any{"action": "agent.enroll_failed", "omitted": 12, "seconds": 60}),
			"12 keer 'Aanmelding van agent geweigerd' niet apart vastgelegd: te veel binnen 60 seconden"},
		{row("agent.enroll_failed", "agent", "onbekend", map[string]any{"ip": "203.0.113.9", "hostname": "web09", "reason": "expired"}),
			"Aanmelding van agent op web09 geweigerd: token verlopen vanaf 203.0.113.9"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"hostname": "web01", "command": "system.reboot", "reason": "kernelupdate"}),
			"Commando aan web01: herstarten (reden: kernelupdate)"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "node.maintenance.enter"}),
			"Commando aan web01: onderhoud starten, keepalived uit"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "node.maintenance.exit", "reason": "kernelupdate"}),
			"Commando aan web01: onderhoud beëindigen, keepalived terug (reden: kernelupdate)"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "apply.steps",
			"steps": []any{map[string]any{"kind": "file", "path": "/etc/keepalived/keepalived.conf", "fingerprint": "ab"}}}),
			"Commando aan web01: bestand /etc/keepalived/keepalived.conf schrijven"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "apply.steps",
			"steps": []any{map[string]any{"kind": "package", "packages": []any{"nginx", "keepalived"}}}}),
			"Commando aan web01: pakketten nginx, keepalived installeren"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "apply.steps",
			"steps": []any{map[string]any{"kind": "service", "unit": "keepalived", "state": "restarted"}}}),
			"Commando aan web01: service keepalived herstarten"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "apply.steps",
			"steps": []any{map[string]any{"kind": "command", "creates": "/srv/.klaar"}}}),
			"Commando aan web01: commando uitvoeren (tenzij /srv/.klaar al bestaat)"},
		{row("agent.command", "node", web01ID.String(), map[string]any{"command": "apply.steps", "steps": []any{map[string]any{}, map[string]any{}}}),
			"Commando aan web01: 2 stappen toepassen"},
		{row("cluster.spec_changed", "cluster", webID.String(), map[string]any{"revision": 1, "source": "ui", "template": "keepalived-nginx", "template_version": "1.0.0"}),
			"Specificatie van cluster webcluster-prod: revisie 1 uit keepalived-nginx 1.0.0 (via de webinterface)"},
		{row("secret.created", "cluster", webID.String(), map[string]any{"secret_name": "auth_pass"}),
			"Geheim auth_pass van cluster webcluster-prod opgeslagen"},
		{row("proxmox.sync_requested", "proxmox", pveID.String(), map[string]any{"name": "Thuislab"}),
			"Proxmox-koppeling Thuislab nu ververst"},
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
		{row("drift.detected", "node", web01ID.String(), map[string]any{"hostname": "web01", "count": 4,
			"keys": []any{"file:/etc/keepalived/keepalived.conf:content"}}), "Drift op web01: 4 afwijkingen"},
		{row("drift.changed", "node", web01ID.String(), map[string]any{"hostname": "web01", "count": 1}), "Drift op web01 veranderd: nu 1 afwijking"},
		{row("drift.resolved", "node", web01ID.String(), map[string]any{"hostname": "web01", "duration_seconds": 5400}),
			"Drift op web01 verdwenen na 1 uur"},
		{row("drift.check_failed", "node", web01ID.String(), map[string]any{"hostname": "web01", "error": "geheim auth_pass ontbreekt"}),
			"Driftcontrole van web01 mislukt: geheim auth_pass ontbreekt"},
		{row("drift.ignore_added", "cluster", webID.String(), map[string]any{"name": "Web", "key": "file:/var/www/html/index.html",
			"reason": "tijdelijke onderhoudspagina", "node": "web01", "expires_at": "2026-10-12T10:00:00Z"}),
			"Drift genegeerd in cluster webcluster-prod: file:/var/www/html/index.html op web01 (tijdelijke onderhoudspagina) tot 12-10-2026"},
		{row("drift.ignore_removed", "cluster", webID.String(), map[string]any{"name": "Web", "key": "service:*", "node": nil}),
			"Negeerregel van cluster webcluster-prod opgeheven: service:*"},
		{row("drift.baseline_set", "cluster", webID.String(), map[string]any{"name": "Web", "revision": 2, "nodes": []any{"web01", "web02"}}),
			"Baseline van cluster webcluster-prod vastgelegd voor web01, web02 (revisie 2)"},
		{row("cluster.spec_changed", "cluster", webID.String(), map[string]any{"name": "Web", "revision": 2, "kind": "baseline", "source": "ui"}),
			"Specificatie van cluster webcluster-prod: revisie 2, een baseline (via de webinterface)"},
		{row("failover_test.created", "failover_test", "t1", map[string]any{"name": "keepalived op de eigenaar",
			"scenario": "keepalived stoppen op de eigenaar", "vip": "10.0.20.10"}),
			"Failovertest keepalived op de eigenaar aangemaakt (keepalived stoppen op de eigenaar)"},
		{row("failover_test.updated", "failover_test", "t1", map[string]any{"test": "nginx",
			"max_takeover_seconds": map[string]any{"from": 10, "to": 8}}),
			"Failovertest nginx gewijzigd: verwachting in seconden van 10 naar 8"},
		{row("failover_test.deleted", "failover_test", "t1", map[string]any{"name": "nginx"}), "Failovertest nginx verwijderd"},
		{row("failover_test.updated", "failover_test", "t1", map[string]any{"test": "nginx",
			"scheduled": map[string]any{"from": true, "to": false}, "reason": "het cluster staat in prod"}),
			"Failovertest nginx gewijzigd: gepland in het testvenster van ja naar nee"},
		{row("backup.policy_updated", "cluster", webID.String(), map[string]any{"name": "Web",
			"verify_enabled": map[string]any{"from": false, "to": true}}),
			"Back-upbeleid van cluster webcluster-prod gewijzigd: geplande back-upcontrole van nee naar ja"},
		{row("failover.fault_injected", "test_run", "r1", map[string]any{"name": "VM hard uit", "unit": "keepalived", "hostname": "web01",
			"scenario": "vm_hard_stop"}),
			"Failovertest VM hard uit: VM van web01 hard uitgezet"},
		{row("failover.fault_cleared", "test_run", "r1", map[string]any{"name": "VM hard uit", "unit": "keepalived", "hostname": "web01",
			"scenario": "vm_hard_stop"}),
			"Failovertest VM hard uit: VM van web01 weer gestart"},
		{row("failover.finished", "test_run", "r1", map[string]any{"name": "keepalived", "result": "skipped", "trigger": "schedule",
			"summary": "Overgeslagen: de run stond om 03:00 gepland en is meer dan 30 minuten te laat"}),
			"Geplande failovertest keepalived: Overgeslagen: de run stond om 03:00 gepland en is meer dan 30 minuten te laat"},
		{row("backup.verify_finished", "test_run", "r1", map[string]any{"hostname": "cluster Web", "result": "skipped", "trigger": "schedule",
			"summary": "Overgeslagen: geen node van dit cluster is aan een VM in Proxmox gekoppeld"}),
			"Geplande back-upcontrole van cluster Web: Overgeslagen: geen node van dit cluster is aan een VM in Proxmox gekoppeld"},
		{row("service.created", "service", "s1", map[string]any{"name": "redis-server", "kind": "cache", "scope": "web-prod",
			"source": "discovered", "hosts": []any{"web01", "web02"}}),
			"Dienst redis-server (cache) voorgesteld in web-prod op web01 en web02"},
		{row("service.created", "service", "s1", map[string]any{"name": "nfs", "kind": "storage", "scope": "extern",
			"source": "manual", "address": "10.0.0.60", "port": 2049}),
			"Externe dienst nfs (10.0.0.60:2049)"},
		{row("service.created", "service", "s1", map[string]any{"name": "php8.2-fpm", "kind": "app", "scope": "web-prod", "source": "manual"}),
			"Dienst php8.2-fpm (applicatie) toegevoegd aan web-prod"},
		{row("service.updated", "service", "s1", map[string]any{"service": "redis-server", "scope": "web-prod",
			"state": map[string]any{"from": "suggested", "to": "confirmed"}}),
			"Dienst redis-server in web-prod bevestigd"},
		{row("service.updated", "service", "s1", map[string]any{"service": "db", "scope": "db-prod",
			"name": map[string]any{"from": "mariadb", "to": "db"}, "port": map[string]any{"from": nil, "to": 3306}}),
			"Dienst db in db-prod gewijzigd: naam, poort"},
		{row("service.deleted", "service", "s1", map[string]any{"name": "nginx", "scope": "web-prod", "kept": true,
			"dependencies": []any{"nginx in web-prod → keepalived in web-prod"}}),
			"Dienst nginx in web-prod verwijderd met 1 afhankelijkheid; hij blijft als genegeerd staan, zodat hij niet opnieuw voorgesteld wordt"},
		{row("dependency.created", "dependency", "d1", map[string]any{"consumer": "php8.2-fpm", "consumer_scope": "web-prod",
			"provider": "mariadb", "provider_scope": "db-prod", "strength": "hard", "source": "manual"}),
			"php8.2-fpm in web-prod hangt nu hard af van mariadb in db-prod"},
		{row("dependency.updated", "dependency", "d1", map[string]any{"consumer": "app", "consumer_scope": "app-test",
			"provider": "nfs", "provider_scope": "extern", "strength": map[string]any{"from": "hard", "to": "soft"}}),
			"Afhankelijkheid van app in app-test op nfs (extern) gewijzigd: sterkte van hard naar zacht"},
		{row("service.status_changed", "service", "s1", map[string]any{"name": "php8.2-fpm", "scope": "web-prod", "from": "healthy", "to": "healthy",
			"impact_from": "none", "impact": "down", "impact_reason": "mariadb in db-prod is down",
			"cause": "mariadb in db-prod", "path": []any{"mariadb in db-prod", "php8.2-fpm"}}),
			"php8.2-fpm in web-prod down door een afhankelijkheid: mariadb in db-prod is down"},
		{row("service.status_changed", "service", "s1", map[string]any{"name": "app", "scope": "app-test", "from": "unknown", "to": "unknown",
			"impact_from": "none", "impact": "degraded", "impact_reason": "mariadb in db-prod is down"}),
			"app in app-test verminderd door een afhankelijkheid: mariadb in db-prod is down"},
		{row("service.status_changed", "service", "s1", map[string]any{"name": "nginx", "scope": "web-prod", "from": "healthy", "to": "healthy",
			"impact_from": "down", "impact": "none", "impact_reason": ""}),
			"nginx in web-prod niet meer geraakt door een afhankelijkheid"},
		{row("service.status_changed", "service", "s1", map[string]any{"name": "mariadb", "scope": "db-prod", "from": "healthy", "to": "down",
			"reason": "mariadb draait op geen enkele node (db01: failed)", "impact_from": "none", "impact": "none"}),
			"mariadb in db-prod van gezond naar down: mariadb draait op geen enkele node (db01: failed)"},
		{row("cluster.deleted", "cluster", webID.String(), map[string]any{"name": "webcluster-prod", "slug": "webcluster-prod", "used_by": []any{
			map[string]any{"consumer": "php8.2-fpm", "consumer_scope": "web-prod", "provider": "mariadb", "strength": "hard"},
			map[string]any{"consumer": "app", "consumer_scope": "app-test", "provider": "mariadb", "strength": "soft"},
		}}),
			"Cluster webcluster-prod verwijderd; daarmee verdwenen de afhankelijkheden van php8.2-fpm in web-prod en app in app-test"},
		{row("node.deleted", "node", web01ID.String(), map[string]any{"hostname": "web01", "used_by": []any{
			map[string]any{"consumer": "app", "consumer_scope": "app-test", "provider": "nfs", "strength": "hard"},
		}}),
			"Node web01 verwijderd; daarmee verdween de afhankelijkheid van app in app-test"},
		{row("failover.fault_injected", "test_run", "r1", map[string]any{"name": "nginx", "unit": "nginx", "hostname": "web01"}),
			"Failovertest nginx: nginx gestopt op web01"},
		{row("failover.fault_cleared", "test_run", "r1", map[string]any{"name": "nginx", "unit": "nginx", "hostname": "web01", "restore": true}),
			"Failovertest nginx: nginx weer gestart op web01 (opnieuw herstellen)"},
		{row("failover.finished", "test_run", "r1", map[string]any{"name": "keepalived", "result": "pass",
			"summary": "PASS: 3,4 s onbereikbaar, overgenomen door web02, daarna terug op web01, alles hersteld"}),
			"Failovertest keepalived: PASS: 3,4 s onbereikbaar, overgenomen door web02, daarna terug op web01, alles hersteld"},
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
