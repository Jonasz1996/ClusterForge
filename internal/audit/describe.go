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
	if r.Action == "backup.policy_updated" || r.Action == "backup.watch_updated" {
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
	case "audit":
		return events.Lookup(r.SubjectID).Label, false
	}
	return r.SubjectID, false
}

var reauthWhat = map[string]string{
	"password_change": "wachtwoord wijzigen",
	"totp_enable":     "tweestapsverificatie aanzetten",
	"totp_disable":    "tweestapsverificatie uitzetten",
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
	}
	return str(p, "command")
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
		return "Cluster " + name + " verwijderd"
	case "cluster.spec_changed":
		if str(p, "kind") == "baseline" {
			return fmt.Sprintf("Specificatie van cluster %s: revisie %s, een baseline", name, num(p["revision"])) + paren(specSources[str(p, "source")])
		}
		return fmt.Sprintf("Specificatie van cluster %s: revisie %s uit %s %s", name, num(p["revision"]),
			str(p, "template"), str(p, "template_version")) + paren(specSources[str(p, "source")])
	case "secret.created":
		return "Geheim " + str(p, "secret_name") + " van cluster " + name + " opgeslagen"
	case "cluster.deployed":
		return fmt.Sprintf("Cluster %s uitgerold uit %s %s met %s nodes", name, str(p, "template"), str(p, "version"), num(p["nodes"]))
	case "node.created":
		if str(p, "via") == "enrollment" {
			return "Node " + name + " toegevoegd door aanmelding van de agent"
		}
		return "Node " + name + " toegevoegd"
	case "node.deleted":
		return "Node " + name + " verwijderd"
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
	case "vip.owner_changed":
		old, owner := n.byID[from], str(p, "owner_hostname")
		switch {
		case owner == "":
			return "VIP " + name + " heeft geen eigenaar meer"
		case old == "":
			return "VIP " + name + " ligt nu op " + owner
		}
		return fmt.Sprintf("VIP %s verhuisd van %s naar %s", name, old, owner)
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
	case "proxmox.sync_failed":
		return "Proxmox " + name + " niet bereikbaar" + colon(str(p, "error"))
	case "proxmox.sync_recovered":
		return "Proxmox " + name + " weer bereikbaar"
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
		return fmt.Sprintf("Back-upcontrole van %s: back-up teruggezet als sandbox-VM %s op %s", str(p, "hostname"), num(p["vmid"]), str(p, "host"))
	case "backup.sandbox_destroyed":
		return fmt.Sprintf("Sandbox-VM %s van de back-upcontrole van %s verwijderd", num(p["vmid"]), str(p, "hostname"))
	case "backup.sandbox_destroy_failed":
		return fmt.Sprintf("Sandbox-VM %s van de back-upcontrole van %s niet te verwijderen", num(p["vmid"]), str(p, "hostname")) + colon(str(p, "error"))
	case "backup.sandbox_connection_refused":
		return "Tweede agentverbinding van " + or(str(p, "hostname"), name) + " geweigerd tijdens een back-upcontrole" + prefixed(" vanaf ", str(p, "remote"))
	case "backup.verify_finished":
		return "Back-upcontrole van " + str(p, "hostname") + colon(str(p, "summary"))
	case "drift.detected":
		return fmt.Sprintf("Drift op %s: %s", name, count(p["count"], "afwijking", "afwijkingen"))
	case "drift.changed":
		return fmt.Sprintf("Drift op %s veranderd: nu %s", name, count(p["count"], "afwijking", "afwijkingen"))
	case "drift.resolved":
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
	case "failover_test.created":
		return "Failovertest " + name + " aangemaakt" + paren(str(p, "scenario"))
	case "failover_test.updated":
		return "Failovertest " + name + " gewijzigd" + changeSummary(e.Changes)
	case "failover_test.deleted":
		return "Failovertest " + name + " verwijderd"
	case "failover.fault_injected":
		return fmt.Sprintf("Failovertest %s: %s gestopt op %s", str(p, "name"), str(p, "unit"), str(p, "hostname"))
	case "failover.fault_cleared":
		s := fmt.Sprintf("Failovertest %s: %s weer gestart op %s", str(p, "name"), str(p, "unit"), str(p, "hostname"))
		if b, _ := p["restore"].(bool); b {
			s += " (opnieuw herstellen)"
		}
		return s
	case "failover.finished":
		return "Failovertest " + str(p, "name") + colon(str(p, "summary"))
	}
	if strings.HasPrefix(r.Action, "job.") {
		return spec.Label + ": " + name
	}
	if name != "" && name != r.SubjectID {
		return spec.Label + ": " + name
	}
	return spec.Label
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
		{"expect_failback", "Terug naar de oorspronkelijke node"}, {"probe", "Probe"}},
	"backup.policy_updated": {{"max_age_hours", "Maximale leeftijd in uren"}},
	"backup.watch_updated":  {{"watch", "Ook bewaken"}},
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
			if field == "token_secret" {
				// Van een geheim staat alleen dat het veranderde.
				out = append(out, Change{Field: field, Label: label, To: text(v)})
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
		"postgresql_ha": "PostgreSQL HA", "mariadb_ha": "MariaDB HA", "generic": "Algemeen"}
	lifecycleLabels = map[string]string{"provisioning": "wordt opgezet", "active": "actief", "maintenance": "onderhoud",
		"draining": "draining", "decommissioned": "uit dienst"}
	statusLabels = map[string]string{"healthy": "gezond", "degraded": "verminderd", "down": "down",
		"split_brain": "split-brain", "unknown": "onbekend"}
	vmLabels = map[string]string{"running": "draait", "stopped": "uit", "paused": "gepauzeerd", "suspended": "gepauzeerd"}
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
