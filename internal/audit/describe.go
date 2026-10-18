package audit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// describe maakt van een rij een regel met een zin en leesbare namen.
func describe(r store.ListAuditRow, n names) Entry {
	p := map[string]any{}
	_ = json.Unmarshal(r.Payload, &p)
	spec := events.Lookup(r.Action)
	e := Entry{
		ID: r.ID, Ts: r.Ts, Action: r.Action, Category: string(spec.Category),
		Actor:   actor(r),
		Subject: Subject{Type: r.SubjectType, ID: r.SubjectID},
		Changes: []Change{},
		Payload: p,
	}
	if r.ActorType == store.ActorTypeSystem && r.JobRequestedBy != nil && r.JobRequestedByUsername != nil {
		e.OnBehalfOf = &Ref{ID: r.JobRequestedBy.String(), Name: *r.JobRequestedByUsername}
	}
	if r.ClusterRef != "" {
		name := r.ClusterName
		if name == "" {
			name = "verwijderd cluster"
		}
		e.Cluster = &Ref{ID: r.ClusterRef, Name: name, Deleted: !r.ClusterExists}
	}
	if r.NodeRef != nil && *r.NodeRef != "" {
		host := r.NodeHostname
		if host == "" {
			host = str(p, "hostname")
		}
		e.Node = &Ref{ID: *r.NodeRef, Name: host, Deleted: !r.NodeExists}
	}
	if r.JobRef != nil && *r.JobRef != "" {
		title := str(p, "title")
		if r.JobTitle != nil {
			title = *r.JobTitle
		}
		e.Job = &Ref{ID: *r.JobRef, Name: title}
	}
	origin, _ := p["origin"].(map[string]any)
	if ip := or(str(p, "ip"), str(origin, "ip")); ip != "" {
		e.IP = &ip
	}
	if sess := str(origin, "session"); sess != "" {
		e.Session = &sess
	}
	e.Subject.Name, e.Subject.Deleted = subjectName(r, p, n, e)
	if r.Action == "auth.login_failed" && e.Subject.Name == "onbekend" {
		// Oude regels bewaarden hier wat er getypt werd.
		e.Subject.ID = ""
	}
	if strings.HasSuffix(r.Action, ".updated") {
		e.Changes = changes(r.SubjectType, p, n)
	}
	if r.Action == "backup.policy_updated" || r.Action == "backup.watch_updated" || r.Action == "gitops.repo_updated" ||
		r.Action == "assistant.settings_updated" || r.Action == "docs.target_updated" {
		e.Changes = changes(r.Action, p, n)
	}
	e.Summary = summary(r, p, n, e, spec)
	return e
}

func actor(r store.ListAuditRow) Actor {
	a := Actor{Type: string(r.ActorType), ID: r.ActorID}
	switch r.ActorType {
	case store.ActorTypeUser:
		a.Name = "onbekende gebruiker"
		if r.ActorUsername != nil {
			a.Name = *r.ActorUsername
		}
	case store.ActorTypeAgent:
		a.Name = "agent"
		if r.ActorHostname != "" {
			a.Name = "agent op " + r.ActorHostname
		}
	default:
		a.Name = "systeem"
	}
	return a
}

func subjectName(r store.ListAuditRow, p map[string]any, n names, e Entry) (string, bool) {
	switch r.SubjectType {
	case "cluster":
		if e.Cluster != nil {
			return e.Cluster.Name, e.Cluster.Deleted
		}
		return str(p, "name"), false
	case "node":
		if e.Node != nil {
			return e.Node.Name, e.Node.Deleted
		}
		return str(p, "hostname"), false
	case "job":
		if e.Job != nil {
			return e.Job.Name, false
		}
	case "user":
		if name, ok := n.users[r.SubjectID]; ok {
			return name, false
		}
		// Een oude regel van een mislukte login met een bestaande naam.
		if id, ok := n.byUsername[strings.ToLower(r.SubjectID)]; ok {
			return n.users[id.String()], false
		}
		if name := str(p, "username"); name != "" {
			return name, false
		}
		return "onbekend", false
	case "vip":
		return str(p, "address"), false
	case "cron_job":
		return str(p, "name"), false
	case "agent":
		return "agent op " + str(p, "hostname"), false
	case "proxmox":
		if name := n.byID[r.SubjectID]; name != "" {
			return name, false
		}
		if m, ok := p["name"].(map[string]any); ok {
			return text(m["to"]), false
		}
		return str(p, "name"), false
	case "enrollment_token":
		return str(p, "description"), false
	case "failover_test":
		if m, ok := p["name"].(map[string]any); ok {
			return text(m["to"]), false
		}
		return or(str(p, "test"), str(p, "name")), false
	case "test_run":
		return str(p, "name"), false
	case "disaster_scenario":
		if m, ok := p["name"].(map[string]any); ok {
			return text(m["to"]), false
		}
		return or(str(p, "scenario"), str(p, "name")), false
	case "service":
		if m, ok := p["name"].(map[string]any); ok {
			return text(m["to"]), false
		}
		return or(str(p, "service"), str(p, "name")), false
	case "dependency":
		return str(p, "consumer") + " → " + str(p, "provider"), false
	case "git_repo":
		return str(p, "repo"), false
	case "docs_target":
		if m, ok := p["repo"].(map[string]any); ok {
			return text(m["to"]), false
		}
		return or(str(p, "repo"), str(p, "repo_name")), false
	case "git_file":
		return r.SubjectID, false
	case "git_change":
		return or(str(p, "name"), str(p, "slug")), false
	case "audit":
		return events.Lookup(r.SubjectID).Label, false
	case "incident":
		return "#" + num(p["number"]), false
	case "fleet":
		return "ClusterForge", false
	case "risk_acceptance":
		return str(p, "subject_name"), false
	}
	return r.SubjectID, false
}

var severities = map[string]string{"low": "laag", "high": "hoog", "critical": "kritiek"}

var reauthWhat = map[string]string{
	"password_change": "wachtwoord wijzigen",
	"totp_enable":     "tweestapsverificatie aanzetten",
	"totp_disable":    "tweestapsverificatie uitzetten",
	"assistant":       "instellingen van de assistent",
}

var enrollReasons = map[string]string{
	"unknown": "onbekend token",
	"expired": "token verlopen",
	"used_up": "token opgebruikt",
}

var specSources = map[string]string{"ui": "via de webinterface", "git": "uit Git"}

// commandText zegt in gewone woorden wat een commando naar een agent doet.
func commandText(p map[string]any) string {
	switch str(p, "command") {
	case "system.reboot":
		return "herstarten"
	case "system.shutdown":
		return "afsluiten"
	case "node.maintenance.enter":
		return "onderhoud starten, keepalived uit"
	case "node.maintenance.exit":
		return "onderhoud beëindigen, keepalived terug"
	case "apply.steps":
		steps, _ := p["steps"].([]any)
		if len(steps) == 1 {
			if st, ok := steps[0].(map[string]any); ok {
				return stepText(st)
			}
		}
		return fmt.Sprintf("%d stappen toepassen", len(steps))
	case "fault.inject":
		f, _ := p["fault"].(map[string]any)
		return "storing aanzetten: " + faultText(f) + fmt.Sprintf(", hoogstens %s s", num(f["max_seconds"]))
	case "fault.clear":
		ids, _ := p["fault_ids"].([]any)
		if len(ids) == 0 {
			return "alle storingen opheffen"
		}
		return "storingen opheffen"
	}
	return str(p, "command")
}

// faultText beschrijft een storing van fault.inject.
func faultText(f map[string]any) string {
	switch str(f, "kind") {
	case "link_down":
		return "interface " + str(f, "interface") + " down"
	case "vrrp_block":
		return "VRRP blokkeren"
	case "port_block":
		return "TCP-poort " + num(f["port"]) + " blokkeren"
	case "netem":
		var parts []string
		if d := num(f["delay_ms"]); d != "" && d != "0" {
			parts = append(parts, d+" ms vertraging")
		}
		if l := num(f["loss_percent"]); l != "" && l != "0" {
			parts = append(parts, l+" % verlies")
		}
		return str(f, "interface") + " " + strings.Join(parts, " en ")
	case "disk_fill":
		return "schijf vullen tot " + num(f["fill_percent"]) + " %"
	}
	return str(f, "kind")
}

func stepText(st map[string]any) string {
	switch str(st, "kind") {
	case "package":
		verb := "installeren"
		if str(st, "state") == "absent" {
			verb = "verwijderen"
		}
		pk, _ := st["packages"].([]any)
		if len(pk) > 1 {
			return "pakketten " + list(pk) + " " + verb
		}
		return "pakket " + list(pk) + " " + verb
	case "file":
		return "bestand " + str(st, "path") + " schrijven"
	case "service":
		verb := map[string]string{"started": "starten", "stopped": "stoppen", "restarted": "herstarten", "reloaded": "herladen"}[str(st, "state")]
		if verb == "" {
			verb = "instellen"
		}
		return "service " + str(st, "unit") + " " + verb
	case "user":
		return "gebruiker " + str(st, "user") + " aanmaken"
	case "directory":
		return "map " + str(st, "path") + " maken"
	case "command":
		if c := str(st, "creates"); c != "" {
			return "commando uitvoeren (tenzij " + c + " al bestaat)"
		}
		return "commando uitvoeren"
	}
	return "stap " + str(st, "kind")
}

var loginReasons = map[string]string{
	"unknown_user": "onbekende gebruikersnaam",
	"bad_password": "fout wachtwoord",
	"bad_totp":     "foute code voor tweestapsverificatie",
	"disabled":     "account is uitgeschakeld",
}

var roles = map[string]string{"admin": "beheerder", "viewer": "kijker"}

func summary(r store.ListAuditRow, p map[string]any, n names, e Entry, spec events.Spec) string {
	name := e.Subject.Name
	from, to := str(p, "from"), str(p, "to")
	switch r.Action {
	case "auth.login":
		return "Ingelogd" + vanaf(e)
	case "auth.login_failed":
		reason := loginReasons[str(p, "reason")]
		if reason == "" {
			reason = str(p, "reason")
		}
		who := "voor " + name
		if name == "onbekend" {
			who = "met een onbekende gebruikersnaam"
			if reason == loginReasons["unknown_user"] {
				reason = ""
			}
		}
		return "Mislukte inlogpoging " + who + paren(reason) + vanaf(e)
	case "auth.reauth_failed":
		reason := loginReasons[str(p, "reason")]
		return "Bevestiging mislukt bij " + or(reauthWhat[str(p, "what")], str(p, "what")) + colon(reason) + vanaf(e)
	case "auth.totp_setup_started":
		return "Instellen van tweestapsverificatie gestart" + vanaf(e)
	case "auth.rate_limited":
		return "Loginlimiet bereikt" + vanaf(e)
	case "audit.throttled":
		return fmt.Sprintf("%s keer '%s' niet apart vastgelegd: te veel binnen %s seconden",
			num(p["omitted"]), events.Lookup(str(p, "action")).Label, num(p["seconds"]))
	case "user.created":
		return fmt.Sprintf("Gebruiker %s aangemaakt als %s", name, or(roles[str(p, "role")], str(p, "role")))
	case "audit.exported":
		return fmt.Sprintf("Logboek geëxporteerd (%s regels)", num(p["count"]))
	case "cluster.created":
		return "Cluster " + name + " aangemaakt"
	case "cluster.deleted":
		return "Cluster " + name + " verwijderd" + usedBy(p)
	case "cluster.spec_changed":
		if str(p, "kind") == "baseline" {
			return fmt.Sprintf("Specificatie van cluster %s: revisie %s, een baseline", name, num(p["revision"])) + paren(specSources[str(p, "source")])
		}
		source := specSources[str(p, "source")]
		if c := str(p, "commit"); c != "" {
			source += ", commit " + shortSHA(c)
		}
		return fmt.Sprintf("Specificatie van cluster %s: revisie %s uit %s %s", name, num(p["revision"]),
			str(p, "template"), str(p, "template_version")) + paren(source)
	case "secret.created":
		return "Geheim " + str(p, "secret_name") + " van cluster " + name + " opgeslagen"
	case "cluster.database_added":
		s := fmt.Sprintf("%s toegevoegd aan cluster %s op %s", str(p, "component"), name, list(p["nodes"]))
		if a := list(p["arbiter"]); a != "" {
			s += ", met scheidsrechter " + a
		}
		return s
	case "cluster.database_applied":
		return fmt.Sprintf("%s van cluster %s uitgerold op %s (revisie %s)", str(p, "component"), name, list(p["nodes"]), num(p["revision"]))
	case "cluster.database_removed":
		return fmt.Sprintf("%s van cluster %s weggehaald uit ClusterForge; op %s blijft staan wat al geïnstalleerd was", str(p, "component"), name,
			list(p["nodes"]))
	case "cluster.database_down":
		return fmt.Sprintf("%s van cluster %s is weg", str(p, "component"), name) + colon(str(p, "reason"))
	case "cluster.database_up":
		s := fmt.Sprintf("%s van cluster %s draait weer", str(p, "component"), name)
		if secs, ok := p["seconds"].(float64); ok {
			s += ", na " + duration(time.Duration(secs)*time.Second)
		}
		return s
	case "cluster.database_node_out":
		return fmt.Sprintf("%s doet niet mee in %s van cluster %s", str(p, "hostname"), str(p, "component"), name) + colon(str(p, "reason"))
	case "cluster.database_node_back":
		return fmt.Sprintf("%s doet weer mee in %s van cluster %s", str(p, "hostname"), str(p, "component"), name)
	case "cluster.database_restarted":
		s := fmt.Sprintf("%s op %s herstartte (%s van cluster %s)", str(p, "server"), str(p, "hostname"), str(p, "component"), name)
		if secs, ok := p["uptime"].(float64); ok {
			s += "; draait nu " + duration(time.Duration(secs)*time.Second)
		}
		return s
	case "cluster.database_failover":
		return fmt.Sprintf("Failover in %s van cluster %s: %s is nu de primary in plaats van %s", str(p, "component"), name, str(p, "to"), str(p, "from"))
	case "cluster.database_state_transfer":
		s := fmt.Sprintf("%s sluit opnieuw aan bij %s van cluster %s en haalt de data die hij mist bij een andere node", str(p, "hostname"), str(p, "component"), name)
		if d := list(p["donors"]); d != "" {
			s = fmt.Sprintf("%s sluit opnieuw aan bij %s van cluster %s en haalt de data die hij mist bij %s", str(p, "hostname"), str(p, "component"), name, d)
		}
		return s + " (state transfer)"
	case "cluster.database_recovering":
		s := fmt.Sprintf("%s van cluster %s opnieuw gestart", str(p, "component"), name)
		if p["auto"] == true {
			s = fmt.Sprintf("ClusterForge startte %s van cluster %s vanzelf opnieuw", str(p, "component"), name) + colon(str(p, "reason"))
		}
		if skip := list(p["skip"]); skip != "" {
			s += "; zonder " + skip
		}
		return s
	case "cluster.database_recovered":
		if b := str(p, "begin"); b != "" {
			return fmt.Sprintf("%s van cluster %s draait weer; %s begon het cluster met de nieuwste data", str(p, "component"), name, b)
		}
		return fmt.Sprintf("%s van cluster %s draait weer op %s", str(p, "component"), name, list(p["nodes"]))
	case "cluster.database_resynced":
		return fmt.Sprintf("%s opnieuw gesynchroniseerd in %s van cluster %s", str(p, "hostname"), str(p, "component"), name)
	case "cluster.database_switchover":
		s := fmt.Sprintf("%s is de primary van %s van cluster %s in plaats van %s", str(p, "to"), str(p, "component"), name, str(p, "from"))
		if p["maintenance"] == true {
			s += ", voor het onderhoud van " + str(p, "from")
		}
		if p["update"] == true {
			s += ", voor het bijwerken van " + str(p, "from")
		}
		return s
	case "cluster.database_settings":
		var parts []string
		changes, _ := p["changes"].([]any)
		for _, c := range changes {
			ch, _ := c.(map[string]any)
			parts = append(parts, fmt.Sprintf("%s van %s naar %s", strings.ToLower(str(ch, "label")), str(ch, "from"), str(ch, "to")))
		}
		return fmt.Sprintf("Instellingen van %s van cluster %s gewijzigd (revisie %s)", str(p, "component"), name, num(p["revision"])) + colon(strings.Join(parts, ", "))
	case "cluster.database_node_added":
		return fmt.Sprintf("%s toegevoegd aan %s van cluster %s (revisie %s)", str(p, "node"), str(p, "component"), name, num(p["revision"]))
	case "cluster.database_updated":
		var parts []string
		nodes, _ := p["nodes"].([]any)
		for _, x := range nodes {
			n, _ := x.(map[string]any)
			if from, to := str(n, "from"), str(n, "to"); n["updated"] == true && from != "" && to != "" && from != to {
				parts = append(parts, fmt.Sprintf("%s van %s naar %s", str(n, "hostname"), from, to))
			} else if n["updated"] == true {
				parts = append(parts, str(n, "hostname"))
			}
		}
		if len(parts) == 0 {
			return fmt.Sprintf("%s van cluster %s nagekeken; er was geen nieuwere versie", str(p, "component"), name)
		}
		return fmt.Sprintf("%s van cluster %s bijgewerkt", str(p, "component"), name) + colon(strings.Join(parts, ", "))
	case "cluster.database_reinitialized":
		return fmt.Sprintf("%s opnieuw opgebouwd als replica in %s van cluster %s", str(p, "hostname"), str(p, "component"), name)
	case "cluster.database_auto_recover":
		if p["on"] == true {
			return fmt.Sprintf("Vanzelf opnieuw starten van %s van cluster %s aangezet", str(p, "component"), name)
		}
		return fmt.Sprintf("Vanzelf opnieuw starten van %s van cluster %s uitgezet", str(p, "component"), name)
	case "cluster.database_created":
		s := fmt.Sprintf("Database %s aangemaakt in %s van cluster %s", str(p, "database"), str(p, "component"), name)
		if u := str(p, "user"); u != "" {
			s += fmt.Sprintf(", met gebruiker %s vanaf %s", u, list(p["hosts"]))
		}
		return s
	case "cluster.database_dropped":
		s := fmt.Sprintf("Database %s weggehaald uit %s van cluster %s", str(p, "database"), str(p, "component"), name)
		if t, ok := p["tables"].(float64); ok && t >= 0 {
			s += fmt.Sprintf(" (%d tabellen)", int(t))
		}
		return s
	case "cluster.database_password":
		return fmt.Sprintf("Gebruiker %s van %s van cluster %s kreeg een nieuw wachtwoord", str(p, "user"), str(p, "component"), name)
	case "cluster.database_user_dropped":
		return fmt.Sprintf("Gebruiker %s weggehaald uit %s van cluster %s", str(p, "user"), str(p, "component"), name)
	case "cluster.database_not_added":
		return fmt.Sprintf("Database van cluster %s na het uitrollen niet toegevoegd op %s", name, list(p["nodes"])) + colon(str(p, "reason"))
	case "cluster.database_trial_ok", "cluster.database_trial_failed":
		s := fmt.Sprintf("Proefverhuizing van %s van %s naar cluster %s", str(p, "database"), str(p, "source"), name)
		if r.Action == "cluster.database_trial_failed" {
			return s + " mislukt" + colon(strings.SplitN(str(p, "message"), "\n", 2)[0])
		}
		s += " gelukt"
		if ms, ok := p["import_ms"].(float64); ok {
			s += ", importeren duurde " + duration(time.Duration(ms)*time.Millisecond)
		}
		if d, ok := p["differences"].(float64); ok && d > 0 {
			s += fmt.Sprintf("; %s met andere aantallen rijen, omdat de app doorwerkte", count(d, "tabel", "tabellen"))
		}
		return s
	case "cluster.database_move_copied":
		s := fmt.Sprintf("Verhuizing van %s van %s naar cluster %s gekopieerd: alle tabellen gelijk", str(p, "database"), str(p, "source"), name)
		if ms, ok := p["import_ms"].(float64); ok {
			s += ", importeren duurde " + duration(time.Duration(ms)*time.Millisecond)
		}
		return s + "; wacht op overschakelen"
	case "cluster.database_move_failed":
		return fmt.Sprintf("Verhuizing van %s van %s naar cluster %s mislukt", str(p, "database"), str(p, "source"), name) +
			colon(strings.SplitN(str(p, "message"), "\n", 2)[0])
	case "cluster.database_moved":
		s := fmt.Sprintf("Database %s verhuisd van %s naar cluster %s", str(p, "database"), str(p, "source"), name)
		if a := str(p, "address"); a != "" {
			s += " (" + a + ")"
		}
		if f, _ := p["forced"].(bool); f {
			s += ", toch overgeschakeld"
		}
		if l, _ := p["locked"].(bool); l {
			s += "; de oude database staat op slot"
		}
		return s
	case "cluster.database_move_cancelled":
		return fmt.Sprintf("Verhuizing van %s van %s naar cluster %s geannuleerd; de kopie is weg", str(p, "database"), str(p, "source"), name)
	case "cluster.database_locked":
		return fmt.Sprintf("Oude database %s op %s op slot gezet: %s %s ze alleen nog lezen", str(p, "database"), str(p, "source"), list(p["users"]),
			verbFor(p["users"], "mag", "mogen"))
	case "cluster.database_unlocked":
		return fmt.Sprintf("Slot van de oude database %s op %s gehaald: %s %s er weer in schrijven", str(p, "database"), str(p, "source"),
			list(p["users"]), verbFor(p["users"], "mag", "mogen"))
	case "cluster.database_retired":
		s := fmt.Sprintf("Oude database %s op %s afgebouwd", str(p, "database"), str(p, "source"))
		if n := str(p, "dump_node"); n != "" {
			s += "; laatste dump op " + n + sizeText(p["dump_bytes"])
		}
		if l, ok := p["dropped"].([]any); ok && len(l) > 0 {
			s += "; weggehaald: " + list(p["dropped"])
		}
		if l, ok := p["kept"].([]any); ok && len(l) > 0 {
			s += "; " + count(float64(len(l)), "gebruiker blijft", "gebruikers blijven")
		}
		if e, _ := p["empty"].(bool); e {
			s += "; " + list(p["servers"]) + " " + verbFor(p["servers"], "heeft", "hebben") + " geen database van een app meer"
		}
		return s
	case "cluster.deployed":
		return fmt.Sprintf("Cluster %s uitgerold uit %s %s met %s nodes", name, str(p, "template"), str(p, "version"), num(p["nodes"]))
	case "node.created":
		if str(p, "via") == "enrollment" {
			return "Node " + name + " toegevoegd door aanmelding van de agent"
		}
		return "Node " + name + " toegevoegd"
	case "node.deleted":
		return "Node " + name + " verwijderd" + usedBy(p)
	case "vip.created":
		return "VIP " + name + " toegevoegd"
	case "vip.deleted":
		return "VIP " + name + " verwijderd"
	case "cluster.updated", "node.updated", "vip.updated", "proxmox.updated":
		what := map[string]string{"cluster": "Cluster ", "node": "Node ", "vip": "VIP ", "proxmox": "Proxmox-koppeling "}[r.SubjectType]
		return what + name + " gewijzigd" + changeSummary(e.Changes)
	case "node.lifecycle_changed":
		return fmt.Sprintf("%s van %s naar %s", name, lifecycle(from), lifecycle(to)) + paren(prefixed("reden: ", str(p, "reason")))
	case "node.status_changed":
		return fmt.Sprintf("%s van %s naar %s", name, status(from), status(to)) + colon(str(p, "reason"))
	case "cluster.status_changed":
		return fmt.Sprintf("Cluster %s van %s naar %s", name, status(from), status(to)) + colon(str(p, "reason"))
	case "incident.opened":
		s := fmt.Sprintf("Incident %s geopend: %s (%s)", name, str(p, "title"), or(severities[str(p, "severity")], str(p, "severity")))
		if b, _ := p["caused_by_test"].(bool); b {
			s += ", door een test"
		}
		return s
	case "incident.escalated":
		return fmt.Sprintf("Incident %s is nu %s (was %s): %s", name, or(severities[str(p, "severity")], str(p, "severity")),
			or(severities[from], from), str(p, "title"))
	case "incident.resolved":
		secs, _ := p["duration_seconds"].(float64)
		return fmt.Sprintf("Incident %s opgelost na %s: %s", name, duration(time.Duration(secs)*time.Second), str(p, "title"))
	case "incident.reopened":
		return fmt.Sprintf("Incident %s opnieuw geopend: %s", name, str(p, "title"))
	case "incident.note_updated":
		if p["empty"] == true {
			return "Notitie bij incident " + name + " gewist"
		}
		return "Notitie bij incident " + name + " bijgewerkt"
	case "incident.analyzed":
		who := "Incident " + name + " geanalyseerd door de assistent"
		if p["automatic"] == true {
			who = "Incident " + name + " automatisch geanalyseerd door de assistent"
		}
		return who + ", " + dollars(p["cost_usd"]) + paren(str(p, "model"))
	case "assistant.settings_updated":
		if len(e.Changes) == 1 && e.Changes[0].Field == "key_changed" {
			return "API-sleutel van de assistent vervangen"
		}
		return "Instellingen van de assistent gewijzigd" + changeSummary(e.Changes)
	case "assistant.asked":
		tools, _ := p["tools"].([]any)
		s := or(map[string]string{
			"analysis": "Analyse door de assistent", "explain": "Uitleg door de assistent", "summary": "Samenvatting door de assistent",
		}[str(p, "kind")], "Vraag aan de assistent")
		if len(tools) > 0 {
			s += fmt.Sprintf(" met %s (%s)", count(float64(len(tools)), "tool", "tools"), list(tools))
		}
		return s + ", " + dollars(p["cost_usd"]) + paren(str(p, "model"))
	case "risk.grade_changed":
		var reasons []string
		xs, _ := p["reasons"].([]any)
		for _, x := range xs {
			reasons = append(reasons, text(x))
		}
		s := fmt.Sprintf("Cijfer van %s van %s naar %s (score %s)", name, or(from, "geen"), to, num(p["score"]))
		if len(reasons) > 0 {
			s += ": " + strings.Join(reasons, "; ")
		}
		return s
	case "risk.accepted":
		s := "Bevinding geaccepteerd: " + or(str(p, "text"), str(p, "label"))
		if d := day(str(p, "expires_at")); d != "" {
			s += ", tot " + d
		}
		return s + ". Reden: " + str(p, "reason")
	case "risk.acceptance_removed":
		return "Acceptatie ingetrokken: " + str(p, "label") + " bij " + str(p, "subject_name")
	case "assistant.budget_reached":
		return fmt.Sprintf("Maandbudget van de assistent op: %s van %s", dollars(p["spent_usd"]), dollars(p["budget_usd"]))
	case "vip.owner_changed":
		old, owner := n.byID[from], str(p, "owner_hostname")
		switch {
		case owner == "":
			return "VIP " + name + " heeft geen eigenaar meer"
		case old == "":
			return "VIP " + name + " ligt nu op " + owner
		}
		return fmt.Sprintf("VIP %s verhuisd van %s naar %s", name, old, owner)
	case "node.cron_added":
		return fmt.Sprintf("Cronjob op %s verschenen in %s: %s %s", name, str(p, "file"), str(p, "schedule"), str(p, "command"))
	case "node.cron_removed":
		return fmt.Sprintf("Cronjob op %s verdwenen uit %s: %s %s", name, str(p, "file"), str(p, "schedule"), str(p, "command"))
	case "node.cron_changed":
		if c := num(p["count"]); c != "" && c != "0" {
			return fmt.Sprintf("%s cronjobs op %s gewijzigd%s", c, name, colon(list(p["files"])))
		}
		s := fmt.Sprintf("Cronjob op %s gewijzigd in %s", name, str(p, "file"))
		if str(p, "old_schedule") != str(p, "schedule") {
			s += fmt.Sprintf(": schema %s, was %s", str(p, "schedule"), str(p, "old_schedule"))
		}
		if str(p, "old_command") != str(p, "command") {
			s += fmt.Sprintf("; commando %s", str(p, "command"))
		}
		return s
	case "node.cron_failed":
		s := fmt.Sprintf("Cronjob op %s mislukt", name)
		if e := num(p["exit"]); e != "" {
			s += " met exitcode " + e
		}
		return s + fmt.Sprintf(": %s (%s)", str(p, "command"), str(p, "file"))
	case "node.cron_recovered":
		return fmt.Sprintf("Cronjob op %s lukt weer: %s (%s)", name, str(p, "command"), str(p, "file"))
	case "cron_job.created":
		return fmt.Sprintf("Cronjob %s gemaakt in %s: %s %s", name, str(p, "cluster"), str(p, "schedule"), str(p, "command"))
	case "cron_job.updated":
		return fmt.Sprintf("Cronjob %s gewijzigd%s", name, colon(strings.Join(changedFields(p), ", ")))
	case "cron_job.enabled":
		return "Cronjob " + name + " aangezet"
	case "cron_job.disabled":
		return "Cronjob " + name + " uitgezet"
	case "cron_job.deleted":
		return fmt.Sprintf("Cronjob %s weggehaald uit %s", name, str(p, "cluster"))
	case "cron_job.run_requested":
		return fmt.Sprintf("Cronjob %s met de hand gestart op %s", name, list(p["nodes"]))
	case "cron_job.run_stopped":
		return fmt.Sprintf("Run van cronjob %s op %s met de hand gestopt", name, str(p, "hostname"))
	case "cron_job.run_failed":
		s := fmt.Sprintf("Cronjob %s mislukt op %s", name, str(p, "hostname"))
		if str(p, "result") == "timeout" {
			return s + ": tijdslimiet bereikt"
		}
		if e := num(p["exit"]); e != "" {
			s += " met exitcode " + e
		}
		return s
	case "node.cron_disabled":
		return fmt.Sprintf("Cronjob %s op %s in commentaar gezet (%s)", str(p, "command"), name, str(p, "file"))
	case "node.cron_enabled":
		return fmt.Sprintf("Cronjob %s op %s weer aangezet (%s)", str(p, "command"), name, str(p, "file"))
	case "node.cron_logging":
		if b, _ := p["enabled"].(bool); b {
			return "cron op " + name + " logt nu het einde en de exitcode van elke job"
		}
		return "cron op " + name + " logt weer alleen de start van een job"
	case "node.facts_changed":
		if b, _ := p["first"].(bool); b {
			return "Eerste facts van " + name + " ontvangen"
		}
		return "Facts van " + name + " gewijzigd" + colon(list(p["changed"]))
	case "agent.enrolled":
		s := "Agent op " + str(p, "hostname") + " aangemeld" + paren(prefixed("versie ", str(p, "version")))
		if num(p["replaced"]) != "0" && num(p["replaced"]) != "" {
			s += "; de vorige agent is ingetrokken"
		}
		return s
	case "agent.enroll_failed":
		host := str(p, "hostname")
		who := "Aanmelding van een agent"
		if host != "" {
			who = "Aanmelding van agent op " + host
		}
		return who + " geweigerd" + colon(or(enrollReasons[str(p, "reason")], str(p, "reason"))) + vanaf(e)
	case "agent.command":
		return "Commando aan " + name + ": " + commandText(p) + paren(prefixed("reden: ", str(p, "reason")))
	case "agent.revoked":
		return "Agent op " + str(p, "hostname") + " ingetrokken"
	case "enrollment_token.created":
		return "Aanmeldtoken gemaakt" + colon(str(p, "description"))
	case "proxmox.created":
		return "Proxmox-koppeling " + name + " aangemaakt"
	case "proxmox.deleted":
		return "Proxmox-koppeling " + name + " verwijderd"
	case "proxmox.sync_requested":
		return "Proxmox-koppeling " + name + " nu ververst"
	case "proxmox.self_location_set":
		return fmt.Sprintf("ClusterForge draait op %s (VM %s) van Proxmox %s", or(str(p, "guest"), "?"), num(p["vmid"]), name)
	case "proxmox.self_location_cleared":
		return "Ingevulde VM van ClusterForge weggehaald; ClusterForge zoekt hem weer zelf"
	case "proxmox.sync_failed":
		return "Proxmox " + name + " niet bereikbaar" + colon(str(p, "error"))
	case "proxmox.sync_recovered":
		return "Proxmox " + name + " weer bereikbaar"
	case "proxmox.host_offline":
		return "Host " + str(p, "host") + " van Proxmox " + name + " is offline"
	case "proxmox.host_online":
		return "Host " + str(p, "host") + " van Proxmox " + name + " is weer online"
	case "node.link_proposed":
		return fmt.Sprintf("Voorstel: node %s is VM %s (%s) op %s, zelfde MAC-adres %s", name, num(p["vmid"]),
			or(str(p, "guest"), "?"), or(str(p, "host"), "?"), str(p, "mac"))
	case "node.link_confirmed":
		return fmt.Sprintf("Node %s gekoppeld aan VM %s (%s) op %s, voorstel bevestigd", name, num(p["vmid"]),
			or(str(p, "guest"), "?"), or(str(p, "host"), "?"))
	case "node.link_ignored":
		return fmt.Sprintf("Voorstel om node %s aan VM %s (%s) te koppelen genegeerd", name, num(p["vmid"]), or(str(p, "guest"), "?"))
	case "vm.status_changed":
		return fmt.Sprintf("VM %s van %s gaat van %s naar %s", num(p["vmid"]), str(p, "hostname"), vmStatus(from), vmStatus(to))
	case "vm.moved":
		return fmt.Sprintf("VM %s van %s verhuisd van %s naar %s", num(p["vmid"]), str(p, "hostname"), from, to)
	case "vm.missing":
		return fmt.Sprintf("VM %s van %s is verdwenen uit Proxmox", num(p["vmid"]), str(p, "hostname"))
	case "job.failed":
		return spec.Label + ": " + name + colon(str(p, "error"))
	case "backup.fresh":
		return "Back-up van " + backupOf(r, p, name) + " weer vers"
	case "backup.stale":
		return fmt.Sprintf("Back-up van %s te oud: de nieuwste is ouder dan %s uur", backupOf(r, p, name), num(p["max_age_hours"]))
	case "backup.missing":
		return "Geen back-up van " + backupOf(r, p, name) + " in Proxmox"
	case "backup.inventory_failed":
		return "Back-ups van Proxmox-koppeling " + name + " niet te lezen" + colon(str(p, "error"))
	case "backup.inventory_recovered":
		return "Back-ups van Proxmox-koppeling " + name + " weer te lezen"
	case "backup.policy_updated":
		return "Back-upbeleid van cluster " + name + " gewijzigd" + changeSummary(e.Changes)
	case "backup.watch_updated":
		return "Lijst ook bewaken van Proxmox-koppeling " + name + " gewijzigd" + changeSummary(e.Changes)
	case "backup.sandbox_created":
		if str(p, "kind") == "dr.rehearsal" {
			return fmt.Sprintf("DR-repetitie van %s: %s teruggezet als sandbox-VM %s op %s", str(p, "cluster"), str(p, "hostname"), num(p["vmid"]), str(p, "host"))
		}
		return fmt.Sprintf("Back-upcontrole van %s: back-up teruggezet als sandbox-VM %s op %s", str(p, "hostname"), num(p["vmid"]), str(p, "host"))
	case "backup.sandbox_destroyed":
		if str(p, "kind") == "dr.rehearsal" {
			return fmt.Sprintf("Sandbox-VM %s van %s uit de DR-repetitie van %s verwijderd", num(p["vmid"]), str(p, "hostname"), str(p, "cluster"))
		}
		return fmt.Sprintf("Sandbox-VM %s van de back-upcontrole van %s verwijderd", num(p["vmid"]), str(p, "hostname"))
	case "backup.sandbox_destroy_failed":
		if str(p, "kind") == "dr.rehearsal" {
			return fmt.Sprintf("Sandbox-VM %s van %s uit de DR-repetitie van %s niet te verwijderen", num(p["vmid"]), str(p, "hostname"),
				str(p, "cluster")) + colon(str(p, "error"))
		}
		return fmt.Sprintf("Sandbox-VM %s van de back-upcontrole van %s niet te verwijderen", num(p["vmid"]), str(p, "hostname")) + colon(str(p, "error"))
	case "backup.sandbox_connection_refused":
		return "Tweede agentverbinding van " + or(str(p, "hostname"), name) + " geweigerd tijdens een back-upcontrole" + prefixed(" vanaf ", str(p, "remote"))
	case "backup.db_dumped":
		return fmt.Sprintf("Dump van %s van cluster %s op %s", str(p, "database"), name, str(p, "node")) + dumpSize(p)
	case "backup.db_dump_suspect":
		return fmt.Sprintf("Dump van %s van cluster %s op %s is verdacht", str(p, "database"), name, str(p, "node")) + colon(str(p, "message"))
	case "backup.db_dump_failed":
		return fmt.Sprintf("Dump van %s van cluster %s op %s mislukt", str(p, "database"), name, str(p, "node")) + colon(str(p, "message"))
	case "backup.db_restored":
		return fmt.Sprintf("Dump van %s van cluster %s teruggezet als %s op %s", str(p, "database"), name, str(p, "target"), str(p, "node"))
	case "backup.db_restore_failed":
		return fmt.Sprintf("Dump van %s van cluster %s terugzetten als %s mislukt", str(p, "database"), name, str(p, "target")) + colon(str(p, "message"))
	case "backup.db_settings":
		if on, ok := p["on"].(bool); ok && !on {
			return "Nachtelijke dump van de databases van cluster " + name + " uitgezet"
		}
		return fmt.Sprintf("Nachtelijke dump van de databases van cluster %s om %s, %s bewaard", name, str(p, "at"), count(p["keep_days"], "dag", "dagen"))
	case "backup.verify_finished":
		if str(p, "trigger") == "schedule" {
			return "Geplande back-upcontrole van " + str(p, "hostname") + colon(str(p, "summary"))
		}
		return "Back-upcontrole van " + str(p, "hostname") + colon(str(p, "summary"))
	case "drift.detected":
		return fmt.Sprintf("Drift op %s: %s", name, count(p["count"], "afwijking", "afwijkingen"))
	case "drift.changed":
		return fmt.Sprintf("Drift op %s veranderd: nu %s", name, count(p["count"], "afwijking", "afwijkingen"))
	case "drift.resolved":
		if str(p, "job_id") != "" {
			s := "Drift op " + name + " hersteld met een taak"
			if d, ok := p["duration_seconds"].(float64); ok {
				s += ", na " + duration(time.Duration(d)*time.Second)
			}
			return s
		}
		if d, ok := p["duration_seconds"].(float64); ok {
			return fmt.Sprintf("Drift op %s verdwenen na %s", name, duration(time.Duration(d)*time.Second))
		}
		return "Drift op " + name + " verdwenen"
	case "drift.check_failed":
		return "Driftcontrole van " + name + " mislukt" + colon(str(p, "error"))
	case "drift.ignore_added":
		return "Drift genegeerd in cluster " + name + ": " + str(p, "key") + prefixed(" op ", str(p, "node")) +
			paren(str(p, "reason")) + prefixed(" tot ", day(str(p, "expires_at")))
	case "drift.ignore_removed":
		return "Negeerregel van cluster " + name + " opgeheven: " + str(p, "key") + prefixed(" op ", str(p, "node"))
	case "drift.baseline_set":
		return fmt.Sprintf("Baseline van cluster %s vastgelegd voor %s (revisie %s)", name, list(p["nodes"]), num(p["revision"]))
	case "drift.remediation_requested":
		nodes, _ := p["nodes"].([]any)
		return fmt.Sprintf("Herstel van drift aangevraagd in cluster %s: %s op %s", name, count(p["steps"], "stap", "stappen"), enList(nodes))
	case "failover_test.created":
		return "Failovertest " + name + " aangemaakt" + paren(str(p, "scenario"))
	case "failover_test.updated":
		return "Failovertest " + name + " gewijzigd" + changeSummary(e.Changes)
	case "failover_test.deleted":
		return "Failovertest " + name + " verwijderd"
	case "failover.fault_injected":
		if str(p, "scenario") == "restart_all" {
			nodes, _ := p["nodes"].([]any)
			return fmt.Sprintf("Failovertest %s: %s herstart", str(p, "name"), enList(nodes))
		}
		if str(p, "scenario") == "vm_hard_stop" {
			return fmt.Sprintf("Failovertest %s: VM van %s hard uitgezet", str(p, "name"), str(p, "hostname"))
		}
		return fmt.Sprintf("Failovertest %s: %s gestopt op %s", str(p, "name"), str(p, "unit"), str(p, "hostname"))
	case "failover.fault_cleared":
		s := fmt.Sprintf("Failovertest %s: %s weer gestart op %s", str(p, "name"), str(p, "unit"), str(p, "hostname"))
		switch str(p, "scenario") {
		case "vm_hard_stop":
			s = fmt.Sprintf("Failovertest %s: VM van %s weer gestart", str(p, "name"), str(p, "hostname"))
		case "restart_all":
			nodes, _ := p["nodes"].([]any)
			s = fmt.Sprintf("Failovertest %s: %s terug na de herstart, en de database is gezond", str(p, "name"), enList(nodes))
		}
		if b, _ := p["restore"].(bool); b {
			s += " (opnieuw herstellen)"
		}
		return s
	case "failover.finished":
		if str(p, "trigger") == "schedule" {
			return "Geplande failovertest " + str(p, "name") + colon(str(p, "summary"))
		}
		return "Failovertest " + str(p, "name") + colon(str(p, "summary"))
	case "disaster_scenario.created":
		return "Rampoefening " + name + " aangemaakt in cluster " + str(p, "cluster") + colon(str(p, "description"))
	case "disaster_scenario.updated":
		return "Rampoefening " + name + " gewijzigd" + changeSummary(e.Changes)
	case "disaster_scenario.deleted":
		return "Rampoefening " + name + " verwijderd"
	case "disaster.started":
		clusters, _ := p["clusters"].([]any)
		return "Rampoefening " + str(p, "name") + " gestart" + colon(str(p, "description")) + prefixed("; gemeten: ", enList(clusters))
	case "disaster.fault_injected":
		return "Rampoefening " + str(p, "name") + ": " + str(p, "description")
	case "disaster.fault_cleared":
		targets, _ := p["targets"].([]any)
		s := "Rampoefening " + str(p, "name") + ": alles weer gestart op " + enList(targets)
		if b, _ := p["restore"].(bool); b {
			s += " (opnieuw herstellen)"
		}
		return s
	case "disaster.finished":
		return "Rampoefening " + str(p, "name") + colon(str(p, "summary"))
	case "dr.started":
		what := "DR-repetitie "
		if str(p, "trigger") == "schedule" {
			what = "Geplande DR-repetitie "
		}
		return what + str(p, "name") + " gestart: " + count(p["nodes"], "node", "nodes") + " van " + str(p, "cluster") +
			" terugzetten op " + str(p, "bridge") + prefixed(" op ", str(p, "host"))
	case "dr.aborted":
		return "DR-repetitie " + str(p, "name") + " afgebroken, alle sandboxes hard uitgezet" + colon(str(p, "reason"))
	case "dr.finished":
		if str(p, "trigger") == "schedule" {
			return "Geplande DR-repetitie " + str(p, "name") + colon(str(p, "summary"))
		}
		return "DR-repetitie " + str(p, "name") + colon(str(p, "summary"))
	case "cluster.recovery_targets_updated":
		return "RTO- en RPO-doelen van cluster " + name + " gewijzigd" + changeSummary(e.Changes)
	case "service.created":
		kind := paren(or(kindLabels[str(p, "kind")], str(p, "kind")))
		switch {
		case str(p, "source") == "discovered" && str(p, "address") != "":
			return "Externe dienst " + name + kind + " voorgesteld" + colon(str(p, "evidence"))
		case str(p, "source") == "discovered":
			hosts, _ := p["hosts"].([]any)
			return "Dienst " + name + kind + " voorgesteld in " + str(p, "scope") + prefixed(" op ", enList(hosts))
		case str(p, "address") != "":
			return "Externe dienst " + name + paren(str(p, "address")+prefixed(":", num(p["port"])))
		case str(p, "source") == "template":
			return "Dienst " + name + kind + " uit de template in " + str(p, "scope")
		}
		return "Dienst " + name + kind + " toegevoegd aan " + str(p, "scope")
	case "service.updated":
		where := serviceIn(name, str(p, "scope"))
		if len(e.Changes) == 1 && e.Changes[0].Field == "state" {
			switch m, _ := p["state"].(map[string]any); text(m["to"]) {
			case "confirmed":
				return "Dienst " + where + " bevestigd"
			case "ignored":
				return "Dienst " + where + " genegeerd"
			case "suggested":
				return "Dienst " + where + " teruggezet als voorstel"
			}
		}
		return "Dienst " + where + " gewijzigd" + changeSummary(e.Changes)
	case "service.deleted":
		s := "Dienst " + serviceIn(name, str(p, "scope")) + " verwijderd"
		if arrows, _ := p["dependencies"].([]any); len(arrows) > 0 {
			s += " met " + count(float64(len(arrows)), "afhankelijkheid", "afhankelijkheden")
		}
		if kept, _ := p["kept"].(bool); kept {
			s += "; hij blijft als genegeerd staan, zodat hij niet opnieuw voorgesteld wordt"
		}
		return s
	case "dependency.created":
		how := " hangt nu hard af van "
		if str(p, "strength") == "soft" {
			how = " hangt nu zacht af van "
		}
		if str(p, "source") == "discovered" {
			s := "Voorstel: " + serviceIn(str(p, "consumer"), str(p, "consumer_scope")) + " hangt af van " + serviceIn(str(p, "provider"), str(p, "provider_scope"))
			if n, ok := p["connections"].(float64); ok {
				s += ", uit " + dotted(int64(n)) + " " + map[bool]string{true: "verbinding", false: "verbindingen"}[n == 1] + " in de afgelopen 24 uur"
			}
			return s
		}
		s := serviceIn(str(p, "consumer"), str(p, "consumer_scope")) + how + serviceIn(str(p, "provider"), str(p, "provider_scope"))
		if str(p, "source") == "template" {
			s += " (uit de template)"
		}
		return s
	case "dependency.updated":
		arrow := "Afhankelijkheid van " + serviceIn(str(p, "consumer"), str(p, "consumer_scope")) + " op " + serviceIn(str(p, "provider"), str(p, "provider_scope"))
		if len(e.Changes) == 1 && e.Changes[0].Field == "state" {
			switch m, _ := p["state"].(map[string]any); text(m["to"]) {
			case "confirmed":
				return arrow + " bevestigd"
			case "ignored":
				return arrow + " genegeerd"
			case "suggested":
				return arrow + " teruggezet als voorstel"
			}
		}
		return arrow + " gewijzigd" + changeSummary(e.Changes)
	case "dependency.deleted":
		return "Afhankelijkheid van " + serviceIn(str(p, "consumer"), str(p, "consumer_scope")) + " op " +
			serviceIn(str(p, "provider"), str(p, "provider_scope")) + " verwijderd"
	case "service.status_changed":
		return serviceStatusChanged(serviceIn(name, str(p, "scope")), p)
	case "gitops.repo_connected":
		return "Git-repository " + str(p, "repo") + " gekoppeld" + paren("branch "+str(p, "branch")+", map "+str(p, "path"))
	case "gitops.repo_updated":
		if len(e.Changes) == 1 && e.Changes[0].Field == "token_changed" {
			return "Token van de Git-koppeling vervangen"
		}
		return "Koppeling met Git gewijzigd" + changeSummary(e.Changes)
	case "gitops.repo_disconnected":
		s := "Git-repository " + str(p, "repo") + " ontkoppeld"
		if n, _ := p["clusters"].(float64); n > 0 {
			s += "; " + count(n, "cluster", "clusters") + " niet meer vanuit Git beheerd"
		}
		return s
	case "docs.target_connected":
		return "Documentatie naar GitHub-repository " + str(p, "repo") + paren("branch "+str(p, "branch")+", map "+or(str(p, "path"), "de hoofdmap"))
	case "docs.target_updated":
		if len(e.Changes) == 1 && e.Changes[0].Field == "token_changed" {
			return "Token van de documentatie-export vervangen"
		}
		return "Koppeling van de documentatie met GitHub gewijzigd" + changeSummary(e.Changes)
	case "docs.target_disconnected":
		return "Documentatie niet meer naar " + name + " geschreven; wat er al staat, blijft in de repository"
	case "docs.published":
		docs, _ := p["documents"].([]any)
		var names []string
		for _, d := range docs {
			names = append(names, text(d))
		}
		n, _ := p["files"].(float64)
		return fmt.Sprintf("Documentatie naar %s geschreven: commit %s met %s", name, shortSHA(str(p, "sha")), count(n, "bestand", "bestanden")) +
			paren(strings.Join(names, ", "))
	case "docs.publish_failed":
		return "Documentatie naar " + name + " schrijven mislukt" + colon(str(p, "error"))
	case "docs.publish_recovered":
		return "Documentatie naar " + name + " schrijven lukt weer"
	case "docs.summary_updated":
		return "Samenvatting 'In het kort' van " + name + " geschreven door de assistent, " + dollars(p["cost_usd"]) + paren(str(p, "model"))
	case "docs.summary_removed":
		return "Samenvatting 'In het kort' van " + name + " weggehaald"
	case "kube.scaled":
		return fmt.Sprintf("%s geschaald van %s naar %s", kubeObject(p), num(p["from"]), count(p["to"], "pod", "pods"))
	case "kube.restarted":
		return kubeObject(p) + " herstart"
	case "kube.rolled_back":
		s := kubeObject(p) + " teruggedraaid"
		if from := num(p["from_revision"]); from != "" {
			s += " van versie " + from
		}
		return s + " naar versie " + num(p["to_revision"]) + paren(list(p["images"]))
	case "kube.deleted":
		return kubeObject(p) + " weggehaald"
	case "kube.cordoned":
		return "Node " + str(p, "node") + " op cordon gezet in Kubernetes"
	case "kube.uncordoned":
		return "Node " + str(p, "node") + " krijgt weer pods in Kubernetes"
	case "kube.drained":
		s := fmt.Sprintf("Node %s leeggemaakt in Kubernetes: %s uitgezet", str(p, "node"), count(p["evicted"], "pod", "pods"))
		if p["force"] == true {
			s += ", ook zonder eigenaar"
		}
		return s
	case "kube.node_removed":
		s := fmt.Sprintf("Worker %s weggehaald: leeggemaakt, uit Kubernetes en VM %s verwijderd", str(p, "node"), num(p["vmid"]))
		if r := num(p["revision"]); r != "" && r != "0" {
			s += "; revisie " + r + " van de specificatie"
		}
		return s
	case "kube.rolled_out":
		return "Uitrol van " + str(p, "name") + " in " + str(p, "namespace") + ": image " + or(str(p, "from"), "(geen)") + " → " + str(p, "to")
	case "kube.app_status_changed":
		return fmt.Sprintf("App %s in %s van %s naar %s", str(p, "name"), str(p, "namespace"), status(str(p, "from")), status(str(p, "to"))) +
			colon(str(p, "reason"))
	case "kube.access_issued":
		return fmt.Sprintf("Kubeconfig %s voor %s uitgegeven: %s, geldig tot %s", str(p, "name"), str(p, "user"), accessRole(str(p, "role")),
			day(str(p, "expires_at")))
	case "kube.access_revoked":
		return fmt.Sprintf("Kubeconfig %s van %s ingetrokken (%s)", str(p, "name"), str(p, "user"), accessRole(str(p, "role"))) + colon(str(p, "reason"))
	case "kube.snapshot_saved":
		s := "Snapshot van etcd gemaakt op " + or(str(p, "hostname"), str(p, "node"))
		if f := str(p, "file"); f != "" {
			s += ": " + f
		}
		return s
	case "kube.snapshot_restore_started":
		s := fmt.Sprintf("Terugzetten van snapshot %s op %s gestart", str(p, "file"), or(str(p, "hostname"), str(p, "node")))
		if m := moment(str(p, "taken_at")); m != "" {
			s += ", naar " + m
		}
		return s + "; de API van het cluster antwoordt een paar minuten niet"
	case "kube.snapshot_restored":
		s := fmt.Sprintf("Snapshot %s teruggezet op %s", str(p, "file"), or(str(p, "hostname"), str(p, "node")))
		if m := moment(str(p, "taken_at")); m != "" {
			s += "; het cluster staat weer zoals op " + m
		}
		return s
	case "kube.applied":
		var parts []string
		if c := list(p["created"]); c != "" {
			parts = append(parts, "nieuw "+c)
		}
		if u := list(p["updated"]); u != "" {
			parts = append(parts, "anders "+u)
		}
		s := "Toegepast in Kubernetes: " + strings.Join(parts, "; ")
		if p["force"] == true {
			s += ", velden van een ander programma overgenomen"
		}
		return s
	case "gitops.commit_seen":
		return fmt.Sprintf("Nieuwe commit %s %q van %s", shortSHA(str(p, "sha")), str(p, "message"), str(p, "author")) + verifiedText(p)
	case "gitops.sync_failed":
		return "Lezen uit Git mislukt" + colon(str(p, "error"))
	case "gitops.sync_recovered":
		return "Lezen uit Git lukt weer" + prefixed(" bij commit ", shortSHA(str(p, "sha")))
	case "gitops.file_invalid":
		errs, _ := p["errors"].([]any)
		first := ""
		if len(errs) > 0 {
			m, _ := errs[0].(map[string]any)
			first = prefixed("regel ", num(m["line"])) + prefixed(", ", str(m, "field")) + ": " + str(m, "message")
		}
		return fmt.Sprintf("%s ongeldig in commit %s (%s)", str(p, "path"), shortSHA(str(p, "commit")), count(float64(len(errs)), "fout", "fouten")) + colon(first)
	case "gitops.file_missing":
		return str(p, "path") + " ontbreekt in commit " + shortSHA(str(p, "commit")) + "; cluster " + str(p, "name") + " blijft zoals het is"
	case "gitops.change_planned":
		what := "Wijziging"
		if str(p, "kind") == "create" {
			what = "Nieuw cluster"
		}
		return fmt.Sprintf("%s %s uit commit %s gepland", what, or(str(p, "name"), str(p, "slug")), shortSHA(str(p, "commit"))) + colon(str(p, "summary"))
	case "gitops.change_superseded":
		return fmt.Sprintf("Wijziging voor %s uit commit %s vervalt", str(p, "slug"), shortSHA(str(p, "commit"))) + colon(str(p, "reason"))
	case "gitops.change_approved":
		if str(p, "kind") == "create" {
			return fmt.Sprintf("Nieuw cluster %s uit commit %s goedgekeurd: wordt uitgerold", or(str(p, "name"), str(p, "slug")), shortSHA(str(p, "commit")))
		}
		s := fmt.Sprintf("Wijziging voor %s uit commit %s goedgekeurd: revisie %s", or(str(p, "name"), str(p, "slug")), shortSHA(str(p, "commit")), num(p["revision"]))
		if p["job_id"] != nil {
			s += ", wordt toegepast"
		}
		if n, _ := p["new_nodes"].(float64); n > 0 {
			s += " met " + count(n, "nieuwe node", "nieuwe nodes")
		}
		return s
	case "gitops.change_rejected":
		return fmt.Sprintf("Wijziging voor %s uit commit %s afgewezen", or(str(p, "name"), str(p, "slug")), shortSHA(str(p, "commit"))) + colon(str(p, "reason"))
	case "gitops.change_applied":
		s := fmt.Sprintf("Wijziging voor %s uit commit %s toegepast (revisie %s)", str(p, "slug"), shortSHA(str(p, "commit")), num(p["revision"]))
		if b, _ := p["no_steps"].(bool); b {
			s += "; op de nodes veranderde niets"
		}
		return s
	case "gitops.change_failed":
		return fmt.Sprintf("Wijziging voor %s uit commit %s mislukt", str(p, "slug"), shortSHA(str(p, "commit"))) + colon(str(p, "error"))
	case "gitops.reapply_requested":
		return fmt.Sprintf("Revisie %s van cluster %s opnieuw toepassen (toegepast was revisie %s)", num(p["revision"]), name, num(p["applied_revision"]))
	case "cluster.git_linked":
		return "Cluster " + name + " aan Git gekoppeld" + prefixed(": ", str(p, "path"))
	case "cluster.git_unlinked":
		return "Cluster " + name + " van Git ontkoppeld" + colon(str(p, "reason"))
	}
	if strings.HasPrefix(r.Action, "job.") {
		return spec.Label + ": " + name
	}
	if name != "" && name != r.SubjectID {
		return spec.Label + ": " + name
	}
	return spec.Label
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func verifiedText(p map[string]any) string {
	if b, _ := p["verified"].(bool); b {
		return ", geverifieerd"
	}
	return ""
}

// serviceStatusChanged zegt wat er met een dienst gebeurde: zijn eigen
// status, de doorgegeven uitval of allebei.
func serviceStatusChanged(where string, p map[string]any) string {
	var parts []string
	if from, to := str(p, "from"), str(p, "to"); from != to {
		parts = append(parts, fmt.Sprintf("van %s naar %s", status(from), status(to))+colon(str(p, "reason")))
	}
	if from, to := str(p, "impact_from"), str(p, "impact"); from != to {
		switch to {
		case "down":
			parts = append(parts, "down door een afhankelijkheid"+colon(str(p, "impact_reason")))
		case "degraded":
			parts = append(parts, "verminderd door een afhankelijkheid"+colon(str(p, "impact_reason")))
		default:
			parts = append(parts, "niet meer geraakt door een afhankelijkheid")
		}
	}
	if len(parts) == 0 {
		return "Status van " + where + " gewijzigd"
	}
	return where + " " + strings.Join(parts, ", en ")
}

// usedBy noemt de diensten van buiten die afhingen van een verwijderd
// cluster of een verwijderde node: die afhankelijkheden verdwenen mee.
func usedBy(p map[string]any) string {
	deps, _ := p["used_by"].([]any)
	var who []any
	seen := map[string]bool{}
	for _, d := range deps {
		m, _ := d.(map[string]any)
		c := serviceIn(str(m, "consumer"), str(m, "consumer_scope"))
		if !seen[c] {
			seen[c] = true
			who = append(who, c)
		}
	}
	switch len(deps) {
	case 0:
		return ""
	case 1:
		return "; daarmee verdween de afhankelijkheid van " + enList(who)
	}
	return "; daarmee verdwenen de afhankelijkheden van " + enList(who)
}

// serviceIn noemt een dienst met zijn cluster of node: "nginx in web-prod",
// of "nfs (extern)".
func serviceIn(name, scope string) string {
	switch scope {
	case "":
		return name
	case "extern":
		return name + " (extern)"
	}
	return name + " in " + scope
}

// enList maakt van ["a", "b", "c"] "a, b en c".
func enList(xs []any) string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, text(x))
	}
	if len(out) <= 1 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " en " + out[len(out)-1]
}

// backupOf noemt de VM van een back-upevent: de node, of de VM zelf als
// hij via "ook bewaken" zonder node bewaakt wordt.
func backupOf(r store.ListAuditRow, p map[string]any, name string) string {
	if r.SubjectType == "node" {
		return name
	}
	return "VM " + num(p["vmid"]) + paren(str(p, "name"))
}

// changeSummary noemt één wijziging voluit en meerdere bij naam.
func changeSummary(cs []Change) string {
	switch {
	case len(cs) == 0:
		return ""
	case len(cs) == 1 && len(cs[0].From) <= 40 && len(cs[0].To) <= 40:
		c := cs[0]
		if c.From == "" {
			return ": " + strings.ToLower(c.Label) + " " + c.To
		}
		if c.To == "" {
			return ": " + strings.ToLower(c.Label) + " " + c.From + " weggehaald"
		}
		return fmt.Sprintf(": %s van %s naar %s", strings.ToLower(c.Label), c.From, c.To)
	}
	labels := make([]string, len(cs))
	for i, c := range cs {
		labels[i] = strings.ToLower(c.Label)
	}
	return ": " + strings.Join(labels, ", ")
}

// fieldLabels geeft per soort onderwerp de velden in hun vaste volgorde.
var fieldLabels = map[string][][2]string{
	"cluster": {{"name", "Naam"}, {"slug", "Slug"}, {"description", "Omschrijving"}, {"type", "Type"},
		{"environment", "Omgeving"}, {"git_repo_url", "Git-repository"}, {"tags", "Tags"}, {"owner_ids", "Owners"}},
	"node": {{"hostname", "Hostnaam"}, {"cluster_id", "Cluster"}, {"role", "Rol"}, {"description", "Omschrijving"},
		{"lifecycle", "Lifecycle"}, {"primary_ip", "IP-adres"}, {"tags", "Tags"}, {"proxmox", "Proxmox-VM"}},
	"vip":     {{"address", "Adres"}, {"interface", "Interface"}, {"vrid", "VRRP-id"}, {"description", "Omschrijving"}},
	"proxmox": {{"name", "Naam"}, {"api_url", "API-adres"}, {"token_id", "Token-id"}, {"token_secret", "Token-secret"}, {"tls_fingerprint", "TLS-vingerafdruk"}},
	"failover_test": {{"name", "Naam"}, {"vip", "VIP"}, {"scenario", "Scenario"}, {"max_takeover_seconds", "Verwachting in seconden"},
		{"expect_failback", "Terug naar de oorspronkelijke node"}, {"probe", "Probe"}, {"scheduled", "Gepland in het testvenster"}},
	"disaster_scenario": {{"name", "Naam"}, {"description", "Wat er uitvalt"}, {"fault_seconds", "Duur van de storing in seconden"},
		{"recovery_seconds", "Verwachte hersteltijd in seconden"}},
	"service": {{"name", "Naam"}, {"kind", "Soort"}, {"unit", "Unit"}, {"port", "Poort"}, {"address", "Adres"},
		{"description", "Omschrijving"}, {"state", "Staat"}},
	"dependency":            {{"strength", "Sterkte"}, {"note", "Notitie"}, {"state", "Staat"}},
	"backup.policy_updated": {{"max_age_hours", "Maximale leeftijd in uren"}, {"verify_enabled", "Geplande back-upcontrole"}},
	"backup.watch_updated":  {{"watch", "Ook bewaken"}},
	"gitops.repo_updated": {{"repo", "Repository"}, {"branch", "Branch"}, {"path", "Map"}, {"api_url", "API-adres"},
		{"server_url", "Adres van ClusterForge"}, {"token_changed", "Token"}},
	"docs.target_updated": {{"repo", "Repository"}, {"branch", "Branch"}, {"path", "Map"}, {"api_url", "API-adres"},
		{"enabled", "Automatisch publiceren"}, {"token_changed", "Token"}},
	"assistant.settings_updated": {{"enabled", "Aan"}, {"model", "Model"}, {"monthly_budget_usd", "Maandbudget in dollar"},
		{"viewer_access", "Ook voor viewers"}, {"auto_analyze", "Automatisch analyseren"}, {"key_changed", "API-sleutel"}},
}

func changes(subjectType string, p map[string]any, n names) []Change {
	out := []Change{}
	known := map[string]bool{}
	add := func(field, label string) {
		v, ok := p[field]
		if !ok {
			return
		}
		known[field] = true
		m, isDiff := v.(map[string]any)
		if !isDiff {
			switch field {
			case "token_secret":
				// Van een geheim staat alleen dat het veranderde.
				out = append(out, Change{Field: field, Label: label, To: text(v)})
			case "token_changed", "key_changed":
				out = append(out, Change{Field: field, Label: label, To: "vervangen"})
			}
			return
		}
		out = append(out, Change{Field: field, Label: label, From: value(field, m["from"], n), To: value(field, m["to"], n)})
	}
	for _, f := range fieldLabels[subjectType] {
		add(f[0], f[1])
	}
	var rest []string
	for k := range p {
		if !known[k] && k != "origin" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if _, ok := p[k].(map[string]any); ok {
			add(k, k)
		}
	}
	return out
}

var (
	envLabels  = map[string]string{"lab": "Lab", "test": "Test", "prod": "Productie"}
	typeLabels = map[string]string{"keepalived": "Keepalived", "nginx": "Nginx", "docker": "Docker", "cron": "Cron",
		"postgresql_ha": "PostgreSQL HA", "mariadb_ha": "MariaDB HA", "generic": "Algemeen", "kubernetes": "Kubernetes"}
	lifecycleLabels = map[string]string{"provisioning": "wordt opgezet", "active": "actief", "maintenance": "onderhoud",
		"draining": "draining", "decommissioned": "uit dienst"}
	statusLabels = map[string]string{"healthy": "gezond", "degraded": "verminderd", "down": "down",
		"split_brain": "split-brain", "unknown": "onbekend"}
	vmLabels = map[string]string{"running": "draait", "stopped": "uit", "paused": "gepauzeerd", "suspended": "gepauzeerd"}
	// kindLabels zijn de soorten diensten uit internal/deps.
	kindLabels = map[string]string{"web": "web", "lb": "loadbalancer", "vip": "VIP", "database": "database", "cache": "cache",
		"queue": "wachtrij", "storage": "opslag", "dns": "DNS", "cron": "cron", "container": "containers", "app": "applicatie",
		"external": "extern", "other": "overig"}
	stateLabels    = map[string]string{"confirmed": "bevestigd", "suggested": "voorstel", "ignored": "genegeerd"}
	strengthLabels = map[string]string{"hard": "hard", "soft": "zacht"}
)

func lifecycle(s string) string { return or(lifecycleLabels[s], s) }
func status(s string) string    { return or(statusLabels[s], or(s, "onbekend")) }
func vmStatus(s string) string  { return or(vmLabels[s], or(s, "onbekend")) }

// value zet een veldwaarde om naar tekst; leeg betekent geen waarde.
func value(field string, v any, n names) string {
	switch field {
	case "environment":
		return or(envLabels[fmt.Sprint(v)], text(v))
	case "type":
		return or(typeLabels[fmt.Sprint(v)], text(v))
	case "lifecycle":
		return lifecycle(text(v))
	case "kind":
		return or(kindLabels[text(v)], text(v))
	case "state":
		return or(stateLabels[text(v)], text(v))
	case "strength":
		return or(strengthLabels[text(v)], text(v))
	case "cluster_id":
		if id := text(v); id != "" {
			return or(n.byID[id], "verwijderd cluster")
		}
		return ""
	case "owner_ids":
		ids, _ := v.([]any)
		var out []string
		for _, id := range ids {
			out = append(out, or(n.users[fmt.Sprint(id)], "onbekende gebruiker"))
		}
		return strings.Join(out, ", ")
	case "proxmox":
		if m, ok := v.(map[string]any); ok {
			return "VM " + num(m["vmid"])
		}
		return ""
	}
	return text(v)
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		return list(x)
	case bool:
		if x {
			return "ja"
		}
		return "nee"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func list(v any) string {
	xs, _ := v.([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, text(x))
	}
	return strings.Join(out, ", ")
}

func num(v any) string { return text(v) }

// verbFor is one bij één naam in v, anders many.
func verbFor(v any, one, many string) string {
	if xs, _ := v.([]any); len(xs) == 1 {
		return one
	}
	return many
}

// kubeObject schrijft een object in Kubernetes als "Deployment demo/web".
func kubeObject(p map[string]any) string {
	name := str(p, "name")
	if ns := str(p, "namespace"); ns != "" {
		name = ns + "/" + name
	}
	return or(str(p, "kind"), "Object") + " " + name
}

// dollars schrijft een bedrag zoals $0,12.
func dollars(v any) string {
	f, _ := v.(float64)
	return "$" + strings.Replace(strconv.FormatFloat(f, 'f', 2, 64), ".", ",", 1)
}

// dotted schrijft 1240 als 1.240.
func dotted(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// count geeft "1 afwijking" of "4 afwijkingen".
func count(v any, one, many string) string {
	n, _ := v.(float64)
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", int(n), many)
}

// duration zegt hoe lang iets duurde, afgerond op wat leest.
func duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconden", int(d.Seconds()))
	case d < 2*time.Minute:
		return "1 minuut"
	case d < time.Hour:
		return fmt.Sprintf("%d minuten", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d uur", int(d.Hours()))
	}
	return fmt.Sprintf("%d dagen", int(d.Hours()/24))
}

func str(p map[string]any, k string) string {
	s, _ := p[k].(string)
	return s
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// day geeft de datum van een tijdstip uit een payload, zoals 12-10-2026.
func day(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return t.Local().Format("2-1-2006")
}

// accessRole is de rol van een kubeconfig in woorden.
// moment is een tijdstip als 12-10-2026 14:05, op de klok van de server.
func moment(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return t.Local().Format("2-1-2006 15:04")
}

func accessRole(role string) string {
	if role == "view" {
		return "alleen lezen"
	}
	return "beheerder"
}

func colon(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

func vanaf(e Entry) string {
	if e.IP == nil {
		return ""
	}
	return " vanaf " + *e.IP
}

// dumpSize is de grootte van een dump in een beschrijving.
func dumpSize(p map[string]any) string { return sizeText(p["size_bytes"]) }

// sizeText is een grootte tussen haakjes, zoals " (1,2 GB)"; leeg zonder.
func sizeText(v any) string {
	n, ok := v.(float64)
	if !ok || n <= 0 {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	u := 0
	for n >= 1024 && u < len(units)-1 {
		n /= 1024
		u++
	}
	return " (" + strings.Replace(fmt.Sprintf("%.1f %s", n, units[u]), ".", ",", 1) + ")"
}

// changedFields geeft de namen van de gewijzigde velden van een cronjob.
func changedFields(p map[string]any) []string {
	labels := map[string]string{
		"name": "naam", "schedule": "schema", "run_on": "waar", "node_id": "node", "user": "gebruiker", "command": "commando",
		"timeout_seconds": "tijdslimiet", "enabled": "aan of uit", "retries": "herhalen", "alert": "incident",
	}
	c, _ := p["changes"].(map[string]any)
	var out []string
	for k := range c {
		if l := labels[k]; l != "" {
			k = l
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
