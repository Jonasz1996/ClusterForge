package audit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	if ip := str(p, "ip"); ip != "" {
		e.IP = &ip
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
	}
	return r.SubjectID, false
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
	case "user.created":
		return fmt.Sprintf("Gebruiker %s aangemaakt als %s", name, or(roles[str(p, "role")], str(p, "role")))
	case "audit.exported":
		return fmt.Sprintf("Logboek geëxporteerd (%s regels)", num(p["count"]))
	case "cluster.created":
		return "Cluster " + name + " aangemaakt"
	case "cluster.deleted":
		return "Cluster " + name + " verwijderd"
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
	case "agent.revoked":
		return "Agent op " + str(p, "hostname") + " ingetrokken"
	case "enrollment_token.created":
		return "Aanmeldtoken gemaakt" + colon(str(p, "description"))
	case "proxmox.created":
		return "Proxmox-koppeling " + name + " aangemaakt"
	case "proxmox.deleted":
		return "Proxmox-koppeling " + name + " verwijderd"
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
	"vip":                   {{"address", "Adres"}, {"interface", "Interface"}, {"vrid", "VRRP-id"}, {"description", "Omschrijving"}},
	"proxmox":               {{"name", "Naam"}, {"api_url", "API-adres"}, {"token_id", "Token-id"}, {"token_secret", "Token-secret"}, {"tls_fingerprint", "TLS-vingerafdruk"}},
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
