package risk

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// Window is de periode van de beschikbaarheid en de incidenten.
	Window = 30 * 24 * time.Hour
	// VIPWindow is de periode waarin VIP-wissels geteld worden.
	VIPWindow = 7 * 24 * time.Hour
	// minMeasured is hoe lang de status minstens bekend moet zijn voor de
	// beschikbaarheid iets zegt.
	minMeasured = 24 * time.Hour
	day         = 24 * time.Hour
)

// Score rekent de zeven kaarten van een cluster, en bij Kubernetes de
// achtste. acc zijn de acceptaties die nu gelden.
func Score(in ClusterInput, acc map[Key]Acceptance, now time.Time) Result {
	b := builder{acc: acc}
	cards := []Card{
		b.card(Availability, availability(in, now)),
		b.card(Backups, backupsCard(in, now)),
		b.card(Recovery, recovery(in, now)),
		b.card(Redundancy, redundancy(in, now)),
		b.card(Configuration, configuration(in, now)),
		b.card(Maintenance, maintenance(in, now)),
		b.card(Capacity, capacity(in)),
	}
	if in.Kube != nil {
		cards = append(cards, b.card(Kubernetes, kubernetesCard(in, now)))
	}
	return finish(cards, caps(in, cards))
}

// caps zijn de plafonds: een cluster dat nu down is of een split-brain
// heeft, en een prod-cluster zonder enige back-up.
func caps(in ClusterInput, cards []Card) *Cap {
	switch in.Status {
	case "down":
		return &Cap{40, "Het cluster is nu down; dan is de score hoogstens 40."}
	case "split_brain":
		return &Cap{40, "Het cluster heeft nu een split-brain; dan is de score hoogstens 40."}
	}
	if in.Env != "prod" {
		return nil
	}
	known, missing := 0, 0
	for _, bk := range in.Backups {
		if bk.Freshness == "unknown" {
			continue
		}
		known++
		if bk.Freshness == "missing" && !acceptedIn(cards, "backups.missing", bk.NodeID) {
			missing++
		}
	}
	if known > 0 && missing == known {
		return &Cap{60, "Een prod-cluster zonder enige back-up krijgt hoogstens 60."}
	}
	return nil
}

func acceptedIn(cards []Card, check string, subject uuid.UUID) bool {
	for _, c := range cards {
		for _, f := range c.Findings {
			if f.Check == check && f.Subject.ID == subject && f.Accepted != nil {
				return true
			}
		}
	}
	return false
}

// part is de uitkomst van één kaart voor finish: bevindingen, of onbekend
// met een uitleg.
type part struct {
	findings []Finding
	unknown  bool
	note     string
}

type builder struct{ acc map[Key]Acceptance }

func (b builder) card(id CardID, p part) Card {
	c := Card{ID: id, Note: p.note, Findings: []Finding{}}
	for _, w := range Weights {
		if w.Card == id {
			c.Weight = w.Weight
		}
	}
	if id == Platform {
		c.Weight = 1
	}
	if p.unknown {
		return c
	}
	zero := 0
	c.Score = &zero
	for _, f := range p.findings {
		f.Card = id
		if a, ok := b.acc[f.Key()]; ok {
			f.Accepted = &a
		}
		c.Findings = append(c.Findings, f)
	}
	return c
}

func clusterLink(id uuid.UUID) string { return "/clusters/detail?id=" + id.String() }
func nodeLink(id uuid.UUID) string    { return "/nodes/detail?id=" + id.String() }

func clusterSubject(in ClusterInput) Subject { return Subject{"cluster", in.ID, in.Name} }
func nodeSubject(n NodeInput) Subject        { return Subject{"node", n.ID, n.Hostname} }

// counted zijn de nodes die er echt zijn: niet in aanmaak en niet uit
// dienst.
func counted(in ClusterInput) []NodeInput {
	var out []NodeInput
	for _, n := range in.Nodes {
		if n.Lifecycle != "provisioning" && n.Lifecycle != "decommissioned" {
			out = append(out, n)
		}
	}
	return out
}

// --- Beschikbaarheid ---

func availability(in ClusterInput, now time.Time) part {
	var p part
	start := now.Add(-Window)
	if in.Since.After(start) {
		start = in.Since
	}
	down, measured := downtime(in.History, start, now)
	if in.Status == "unknown" && measured == 0 {
		return part{unknown: true, note: "De status van het cluster is nog niet bekend."}
	}
	cs := clusterSubject(in)
	link := clusterLink(in.ID)
	switch in.Status {
	case "down":
		p.add("availability.status", Critical, cs, withReason(in.Name+" is nu down", in.StatusReason), link)
	case "split_brain":
		p.add("availability.status", Critical, cs, withReason(in.Name+" heeft nu een split-brain", in.StatusReason), link)
	case "degraded":
		p.add("availability.status", bump(Medium, envBump(in.Env), High), cs, withReason(in.Name+" is nu verminderd", in.StatusReason), link)
	}
	if measured >= minMeasured {
		ratio := 1 - float64(down)/float64(measured)
		var sev Severity
		switch {
		case ratio >= 0.999:
		case ratio >= 0.995:
			sev = Low
		case ratio >= 0.99:
			sev = Medium
		case ratio >= 0.95:
			sev = High
		default:
			sev = Critical
		}
		if sev != "" {
			period := "30 dagen"
			if now.Sub(start) < Window-time.Hour {
				period = days(now.Sub(start))
			}
			p.add("availability.uptime", bump(sev, envBump(in.Env), Critical), cs,
				fmt.Sprintf("Beschikbaarheid over %s: %s (%s down)", period, percent(ratio*100), dur(down)), link)
		}
	}
	switch n := in.Incidents; {
	case n >= 8:
		p.add("availability.incidents", High, cs, fmt.Sprintf("%d incidenten in de laatste 30 dagen", n), "/incidenten")
	case n >= 4:
		p.add("availability.incidents", Medium, cs, fmt.Sprintf("%d incidenten in de laatste 30 dagen", n), "/incidenten")
	case n >= 2:
		p.add("availability.incidents", Low, cs, fmt.Sprintf("%d incidenten in de laatste 30 dagen", n), "/incidenten")
	}
	for _, v := range in.VIPs {
		n := in.VIPMoves[v.ID]
		var sev Severity
		switch {
		case n >= 16:
			sev = High
		case n >= 8:
			sev = Medium
		case n >= 4:
			sev = Low
		default:
			continue
		}
		p.add("availability.vip_moves", sev, Subject{"vip", v.ID, v.Address},
			fmt.Sprintf("VIP %s wisselde %d keer van eigenaar in 7 dagen", v.Address, n), link)
	}
	return p
}

func (p *part) add(check string, sev Severity, s Subject, text, link string) {
	p.findings = append(p.findings, Finding{Check: check, Severity: sev, Subject: s, Text: text, Link: link})
}

func withReason(s, reason string) string {
	if reason == "" {
		return s
	}
	return s + ": " + reason
}

// downtime telt hoe lang het cluster tussen start en now down was of een
// split-brain had, en hoe lang zijn status bekend was.
func downtime(hist []Transition, start, now time.Time) (down, measured time.Duration) {
	state := "unknown"
	at := start
	for _, t := range hist {
		if !t.At.After(start) {
			state = t.To
			continue
		}
		if t.At.After(now) {
			break
		}
		d, m := span(state, t.At.Sub(at))
		down, measured = down+d, measured+m
		state, at = t.To, t.At
	}
	d, m := span(state, now.Sub(at))
	return down + d, measured + m
}

func span(state string, d time.Duration) (down, measured time.Duration) {
	if d <= 0 || state == "unknown" || state == "" {
		return 0, 0
	}
	if state == "down" || state == "split_brain" {
		return d, d
	}
	return 0, d
}

// --- Back-ups ---

func backupsCard(in ClusterInput, now time.Time) part {
	var p part
	known := 0
	for _, bk := range in.Backups {
		if bk.Freshness != "unknown" {
			known++
		}
	}
	if known == 0 {
		if len(in.Backups) == 0 {
			return part{unknown: true, note: "Geen node van dit cluster is aan een VM in Proxmox gekoppeld."}
		}
		return part{unknown: true, note: "De back-ups in Proxmox zijn niet te lezen."}
	}
	eb := envBump(in.Env)
	anyBackup := false
	for _, bk := range in.Backups {
		s := Subject{"node", bk.NodeID, bk.Hostname}
		switch bk.Freshness {
		case "missing":
			p.add("backups.missing", bump(High, eb, Critical), s, bk.Hostname+" heeft geen back-up in Proxmox", "/back-ups")
		case "stale":
			anyBackup = true
			age := " is te oud"
			if bk.Latest != nil {
				age = " is " + days(now.Sub(*bk.Latest)) + " oud"
			}
			p.add("backups.stale", bump(Medium, eb, Critical), s,
				fmt.Sprintf("De nieuwste back-up van %s%s; het beleid is %d uur", bk.Hostname, age, bk.MaxAgeHours), "/back-ups")
		case "ok":
			anyBackup = true
		}
		if bk.PBSFailed {
			p.add("backups.pbs_failed", bump(Medium, eb, High), s,
				"Proxmox Backup Server kon de nieuwste back-up van "+bk.Hostname+" niet verifiëren", "/back-ups")
		}
		switch bk.Verification {
		case "fail", "error":
			p.add("backups.verify_failed", bump(Medium, eb, High), s,
				"De laatste back-upcontrole van "+bk.Hostname+" in de sandbox mislukte", "/tests/detail?id="+bk.VerificationID.String())
		case "warning":
			p.add("backups.verify_warning", Low, s,
				"De laatste back-upcontrole van "+bk.Hostname+" in de sandbox gaf een waarschuwing", "/tests/detail?id="+bk.VerificationID.String())
		}
	}
	if anyBackup {
		switch {
		case in.LastVerified == nil:
			p.add("backups.unverified", bump(Low, eb, Medium), clusterSubject(in),
				"Geen back-up van dit cluster is ooit in de sandbox teruggezet en gecontroleerd", "/back-ups")
		case now.Sub(*in.LastVerified) > Window:
			p.add("backups.unverified", bump(Low, eb, Medium), clusterSubject(in),
				"De laatste geslaagde back-upcontrole in de sandbox is "+days(now.Sub(*in.LastVerified))+" geleden", "/back-ups")
		}
	}
	return p
}

// --- Herstelbaarheid ---

func recovery(in ClusterInput, now time.Time) part {
	var p part
	// Een rampoefening die niet hersteld is en een DR-repetitie tellen ook
	// bij een cluster zonder VIP.
	for _, r := range in.Unrestored {
		what := "Failovertest"
		if r.Kind == "disaster.run" {
			what = "Rampoefening"
		}
		p.add("recovery.unrestored", High, Subject{"test_run", r.ID, r.Name},
			what+" '"+r.Name+"' van "+date(r.Finished)+" is niet hersteld", "/tests/detail?id="+r.ID.String())
	}
	eb := envBump(in.Env)
	link := clusterLink(in.ID)
	rehearsals(&p, in, now, eb, link)
	if len(in.VIPs) == 0 {
		if len(p.findings) > 0 || len(in.Rehearsals) > 0 {
			return p
		}
		return part{unknown: true, note: "Zonder VIP is er geen failovertest; deze kaart telt dan niet mee."}
	}
	for _, v := range in.VIPs {
		var tests []FailoverInput
		for _, t := range in.FailoverTests {
			if t.VIPID == v.ID {
				tests = append(tests, t)
			}
		}
		if len(tests) == 0 {
			p.add("recovery.no_test", bump(Low, eb, Medium), Subject{"vip", v.ID, v.Address},
				"VIP "+v.Address+" heeft geen failovertest", link)
		}
		for _, t := range tests {
			s := Subject{"failover_test", t.ID, t.Name}
			r := t.LastRun
			if r == nil {
				p.add("recovery.never_run", bump(Low, eb, Medium), s, "Failovertest '"+t.Name+"' liep nog nooit", link)
				continue
			}
			runLink := "/tests/detail?id=" + r.ID.String()
			switch r.Result {
			case "fail", "error":
				p.add("recovery.failed", bump(Medium, eb, High), s,
					withReason("Failovertest '"+t.Name+"' mislukte op "+date(r.Finished), r.Summary), runLink)
			case "warning":
				p.add("recovery.goal_missed", bump(Low, eb, Medium), s,
					withReason("Failovertest '"+t.Name+"' haalde zijn doel niet", r.Summary), runLink)
			}
			switch age := now.Sub(r.Finished); {
			case age > 90*day:
				p.add("recovery.stale", bump(Medium, eb, High), s,
					"Laatste failovertest '"+t.Name+"' is "+days(age)+" geleden", link)
			case age > Window:
				p.add("recovery.stale", bump(Low, eb, Medium), s,
					"Laatste failovertest '"+t.Name+"' is "+days(age)+" geleden", link)
			}
		}
	}
	return p
}

// Een DR-repetitie draait meestal maandelijks; pas daarna is ze oud.
const (
	drStale   = 45 * day
	drVeryOld = 90 * day
)

// rehearsals zijn de bevindingen over de DR-repetities: de laatste uitslag
// en hoe oud die is.
func rehearsals(p *part, in ClusterInput, now time.Time, eb int, link string) {
	for _, t := range in.Rehearsals {
		s := Subject{"disaster_scenario", t.ID, t.Name}
		r := t.LastRun
		if r == nil {
			p.add("recovery.dr_never_run", bump(Low, eb, Medium), s, "DR-repetitie '"+t.Name+"' liep nog nooit", link)
			continue
		}
		runLink := "/tests/detail?id=" + r.ID.String()
		switch r.Result {
		case "fail", "error":
			p.add("recovery.dr_failed", bump(Medium, eb, High), s,
				withReason("DR-repetitie '"+t.Name+"' mislukte op "+date(r.Finished), r.Summary), runLink)
		case "warning":
			p.add("recovery.dr_goal_missed", bump(Low, eb, Medium), s,
				withReason("DR-repetitie '"+t.Name+"' haalde haar doel niet", r.Summary), runLink)
		}
		switch age := now.Sub(r.Finished); {
		case age > drVeryOld:
			p.add("recovery.dr_stale", bump(Medium, eb, High), s,
				"Laatste DR-repetitie '"+t.Name+"' is "+days(age)+" geleden", link)
		case age > drStale:
			p.add("recovery.dr_stale", bump(Low, eb, Medium), s,
				"Laatste DR-repetitie '"+t.Name+"' is "+days(age)+" geleden", link)
		}
	}
}

// --- Redundantie ---

func redundancy(in ClusterInput, now time.Time) part {
	nodes := counted(in)
	if len(nodes) == 0 {
		return part{unknown: true, note: "Het cluster heeft geen nodes."}
	}
	var p part
	eb := envBump(in.Env)
	link := clusterLink(in.ID)
	host := map[uuid.UUID]string{}
	name := map[uuid.UUID]string{}
	var linked []NodeInput
	for _, n := range nodes {
		name[n.ID] = n.Hostname
		if n.Host != "" {
			host[n.ID] = n.Host
			linked = append(linked, n)
		}
	}
	allSame := len(linked) >= 2 && len(linked) == len(nodes)
	for _, n := range linked {
		if n.Host != linked[0].Host {
			allSame = false
		}
	}
	// flagged zijn de hosts waarover al een bevinding staat; een SPOF op
	// dezelfde host zou die herhalen.
	flagged := map[string]bool{}
	if allSame {
		flagged[linked[0].Host] = true
		names := make([]string, len(linked))
		for i, n := range linked {
			names[i] = n.Hostname
		}
		p.add("redundancy.same_host", bump(Medium, eb, High), clusterSubject(in),
			fmt.Sprintf("%s draaien %s op %s: valt %s uit, dan is %s down", list(names), allOf(len(names)), linked[0].Host, linked[0].Host, in.Name), link)
	} else {
		for _, v := range in.VIPs {
			if len(v.Candidates) < 2 {
				continue
			}
			h := host[v.Candidates[0]]
			same := h != ""
			names := []string{}
			for _, c := range v.Candidates {
				same = same && host[c] == h
				names = append(names, name[c])
			}
			if same {
				flagged[h] = true
				p.add("redundancy.vip_same_host", bump(Medium, eb, High), Subject{"vip", v.ID, v.Address},
					fmt.Sprintf("De nodes van VIP %s (%s) draaien %s op %s", v.Address, list(names), allOf(len(names)), h), link)
			}
		}
	}
	if len(nodes) == 1 && in.Env != "lab" {
		p.add("redundancy.single_node", bump(Low, eb, Medium), clusterSubject(in),
			fmt.Sprintf("%s heeft maar één node: valt %s uit, dan is het cluster down", in.Name, nodes[0].Hostname), link)
	}
	for _, n := range nodes {
		if (n.Lifecycle != "maintenance" && n.Lifecycle != "draining") || n.LifecycleSince == nil {
			continue
		}
		age := now.Sub(*n.LifecycleSince)
		what := "in onderhoud"
		if n.Lifecycle == "draining" {
			what = "op leeglopen"
		}
		switch {
		case age > 7*day:
			p.add("redundancy.maintenance", Medium, nodeSubject(n), n.Hostname+" staat al "+days(age)+" "+what, nodeLink(n.ID))
		case age > day:
			p.add("redundancy.maintenance", Low, nodeSubject(n), n.Hostname+" staat al "+days(age)+" "+what, nodeLink(n.ID))
		}
	}
	if in.Env == "prod" {
		// Staat er al een bevinding over een host, dan voegt een SPOF op
		// zijn lokale storage of bridges niets toe: spreiden lost alles op.
		covered := maps.Clone(flagged)
		for _, sp := range in.SPOFs {
			if sp.Kind == KindHost && !flagged[sp.Host] {
				p.add("redundancy.spof", High, Subject{"component", sp.ID, sp.Name}, spofText(in, sp), spofLink(sp.Key))
				covered[sp.Host] = true
			}
		}
		for _, sp := range in.SPOFs {
			if sp.Kind == KindHost || (sp.Kind == KindNode && len(nodes) == 1) || (sp.Host != "" && covered[sp.Host]) {
				continue
			}
			p.add("redundancy.spof", High, Subject{"component", sp.ID, sp.Name}, spofText(in, sp), spofLink(sp.Key))
		}
	}
	for _, d := range in.NonProdDeps {
		p.add("redundancy.nonprod_dependency", High, Subject{"dependency", d.ID, d.From + " → " + d.To},
			fmt.Sprintf("%s hangt af van %s in %s, een %s-cluster", d.From, d.To, d.ToCluster, d.ToEnv), "/afhankelijkheden")
	}
	return p
}

// spofText zegt wat er down gaat als het onderdeel uitvalt.
func spofText(in ClusterInput, sp SPOFInput) string {
	text := "Valt " + spofSubject(sp) + " uit, dan "
	switch {
	case sp.ClusterDown:
		return text + "is " + in.Name + " down"
	case len(sp.Services) == 1:
		return text + "is de dienst " + sp.Services[0] + " down"
	}
	return text + "zijn de diensten " + list(sp.Services) + " down"
}

// --- Configuratie ---

func configuration(in ClusterInput, now time.Time) part {
	var p part
	link := clusterLink(in.ID)
	if !in.DesiredState {
		p.add("configuration.no_desired_state", bump(Low, envBump(in.Env), Medium), clusterSubject(in),
			"Geen gewenste staat: ClusterForge ziet geen drift. Leg een baseline vast.", link)
	}
	for _, d := range in.Drift {
		if d.Findings == 0 {
			continue
		}
		sev, since := Low, ""
		if d.Since != nil {
			age := now.Sub(*d.Since)
			since = " sinds " + days(age)
			switch {
			case age >= 7*day:
				sev = High
			case age >= day:
				sev = Medium
			}
		}
		p.add("configuration.drift", sev, Subject{"node", d.NodeID, d.Hostname},
			fmt.Sprintf("Drift op %s%s (%s)", d.Hostname, since, plural(d.Findings, "verschil", "verschillen")), link)
	}
	for _, ig := range in.WildcardIgnores {
		where := "het hele cluster"
		if ig.Node != "" {
			where = ig.Node
		}
		p.add("configuration.ignore_all", Medium, Subject{"drift_ignore", ig.ID, where},
			"Alle drift op "+where+" wordt genegeerd tot "+date(ig.ExpiresAt), link)
	}
	if in.AppliedRevision < in.SpecRevision && !in.RevisionRunning && in.RevisionSince != nil && now.Sub(*in.RevisionSince) > time.Hour {
		p.add("configuration.revision_pending", Medium, clusterSubject(in),
			fmt.Sprintf("Revisie %d is al %s niet toegepast; toegepast is revisie %d", in.SpecRevision, days(now.Sub(*in.RevisionSince)), in.AppliedRevision), link)
	}
	if g := in.FailedGit; g != nil {
		p.add("configuration.git_failed", Medium, Subject{"git_change", g.ID, short(g.Commit)},
			withReason("De Git-wijziging uit commit "+short(g.Commit)+" mislukte op "+date(g.At), g.Reason), "/gitops")
	}
	return p
}

// --- Onderhoud en beveiliging ---

func maintenance(in ClusterInput, now time.Time) part {
	nodes := counted(in)
	if !slices.ContainsFunc(nodes, func(n NodeInput) bool { return n.HasFacts }) {
		return part{unknown: true, note: "Geen node heeft al facts gemeld."}
	}
	var p part
	eb := envBump(in.Env)
	for _, n := range nodes {
		if !n.HasFacts {
			continue
		}
		s, link := nodeSubject(n), nodeLink(n.ID)
		if n.SecurityUpdates > 0 {
			what := plural(n.SecurityUpdates, "beveiligingsupdate", "beveiligingsupdates") + " open op " + n.Hostname
			sev := Low
			if n.SecuritySince != nil {
				age := now.Sub(*n.SecuritySince)
				what += ", al " + days(age)
				switch {
				case age >= 14*day:
					sev = High
				case age >= 3*day:
					sev = Medium
				}
			}
			p.add("maintenance.security_updates", bump(sev, eb, High), s, what, link)
		}
		if !n.AptUpdatedAt.IsZero() && now.Sub(n.AptUpdatedAt) > 14*day {
			p.add("maintenance.apt_lists", Low, s,
				"De pakketlijsten op "+n.Hostname+" zijn al "+days(now.Sub(n.AptUpdatedAt))+" niet ververst; open updates zijn dan niet te zien", link)
		}
		if n.RebootRequired {
			text := n.Hostname + " moet herstarten na updates"
			if n.KernelInstalled != "" && n.Kernel != "" && n.KernelInstalled != n.Kernel {
				text = n.Hostname + " moet herstarten: kernel " + n.KernelInstalled + " is geïnstalleerd, " + n.Kernel + " draait"
			}
			p.add("maintenance.reboot", bump(Low, eb, Medium), s, text, link)
		}
		if end, ok := EOL(n.OSID, n.OSVersion); ok {
			osName := n.OSName
			if osName == "" {
				osName = n.OSID + " " + n.OSVersion
			}
			switch {
			case now.After(end):
				p.add("maintenance.os_eol", High, s, n.Hostname+" draait "+osName+", sinds "+month(end)+" end-of-life", link)
			case end.Sub(now) < 90*day:
				p.add("maintenance.os_eol", bump(Low, eb, Medium), s, n.Hostname+" draait "+osName+", end-of-life in "+month(end), link)
			}
		}
		if f, ok := certFinding(n, now); ok {
			p.findings = append(p.findings, f)
		}
		if n.ProtocolVersion > 0 && n.ProtocolVersion < protocol.Version {
			p.add("maintenance.agent_old", Low, s,
				"De agent op "+n.Hostname+" is verouderd; installeer hem opnieuw voor alle controles", link)
		}
	}
	return p
}

// certFinding geeft één bevinding per node: het certificaat dat het eerst
// verloopt, met hoeveel andere ook binnen 30 dagen verlopen.
func certFinding(n NodeInput, now time.Time) (Finding, bool) {
	var soon []CertInput
	for _, c := range n.Certificates {
		if c.NotAfter.Sub(now) < Window {
			soon = append(soon, c)
		}
	}
	if len(soon) == 0 {
		return Finding{}, false
	}
	slices.SortFunc(soon, func(a, b CertInput) int { return a.NotAfter.Compare(b.NotAfter) })
	c := soon[0]
	name := c.Name
	if name == "" {
		name = c.Path
	}
	left := c.NotAfter.Sub(now)
	var sev Severity
	var text string
	switch {
	case left <= 0:
		sev, text = Critical, "Certificaat "+name+" op "+n.Hostname+" is verlopen op "+date(c.NotAfter)
	case left < 7*day:
		sev, text = High, "Certificaat "+name+" op "+n.Hostname+" verloopt over "+days(left)
	default:
		sev, text = Medium, "Certificaat "+name+" op "+n.Hostname+" verloopt over "+days(left)
	}
	if len(soon) > 1 {
		text += " (en nog " + plural(len(soon)-1, "certificaat", "certificaten") + ")"
	}
	return Finding{Check: "maintenance.certificate", Severity: sev, Subject: nodeSubject(n), Text: text, Link: nodeLink(n.ID)}, true
}

// --- Capaciteit ---

func capacity(in ClusterInput) part {
	nodes := counted(in)
	known := slices.ContainsFunc(nodes, func(n NodeInput) bool { return len(n.Filesystems) > 0 || n.Metrics != nil })
	if !known {
		return part{unknown: true, note: "Geen node meldde al schijven of metrics."}
	}
	var p part
	if !in.MetricsRead {
		p.note = "Zonder metrics telt alleen de schijfbezetting; de trend, CPU en geheugen zijn onbekend."
	}
	for _, n := range nodes {
		s, link := nodeSubject(n), nodeLink(n.ID)
		var worst FSInput
		ratio := 0.0
		for _, fs := range n.Filesystems {
			if fs.Size < 100<<20 {
				continue
			}
			if r := float64(fs.Used) / float64(fs.Size); r > ratio {
				worst, ratio = fs, r
			}
		}
		switch {
		case ratio >= 0.95:
			p.add("capacity.disk", High, s, fmt.Sprintf("Schijf %s op %s is %.0f %% vol", worst.Mount, n.Hostname, math.Floor(ratio*100)), link)
		case ratio >= 0.85:
			p.add("capacity.disk", Medium, s, fmt.Sprintf("Schijf %s op %s is %.0f %% vol", worst.Mount, n.Hostname, math.Floor(ratio*100)), link)
		}
		m := n.Metrics
		if m == nil {
			continue
		}
		mount, left := "", math.Inf(1)
		for mt, d := range m.DiskDaysLeft {
			if d < left || (d == left && mt < mount) {
				mount, left = mt, d
			}
		}
		if left <= 14 {
			p.add("capacity.disk_trend", High, s,
				fmt.Sprintf("Schijf %s op %s is vol over ongeveer %s, volgens de trend van 7 dagen", mount, n.Hostname, plural(max(int(left), 1), "dag", "dagen")), link)
		}
		if v := m.MemoryP95; v != nil && *v > 90 {
			p.add("capacity.memory", Medium, s, fmt.Sprintf("Het geheugen van %s zat de afgelopen 7 dagen vaak boven 90 %% (p95 %.0f %%)", n.Hostname, *v), link)
		}
		if v := m.CPUP95; v != nil && *v > 90 {
			p.add("capacity.cpu", Medium, s, fmt.Sprintf("De CPU van %s zat de afgelopen 7 dagen vaak boven 90 %% (p95 %.0f %%)", n.Hostname, *v), link)
		}
		if v := m.SwapAvg; v != nil && *v > 50 {
			p.add("capacity.swap", Low, s, fmt.Sprintf("%s gebruikt gemiddeld %.0f %% van zijn swap", n.Hostname, *v), link)
		}
		if v := m.NetErrors; v != nil && *v > 1000 {
			p.add("capacity.network_errors", Low, s, fmt.Sprintf("%s had %s netwerkfouten in 7 dagen", n.Hostname, thousands(int(*v))), link)
		}
	}
	return p
}

// --- Platform ---

// PlatformCard rekent de kaart Platform: wat ClusterForge zelf kwetsbaar
// maakt.
func PlatformCard(in PlatformInput, acc map[Key]Acceptance, now time.Time) Card {
	var p part
	for _, u := range in.AdminsWithoutTOTP {
		p.add("platform.admin_totp", High, Subject{"user", u.ID, u.Username},
			"Beheerder "+u.Username+" heeft geen tweestapsverificatie", "/instellingen")
	}
	for _, c := range in.Proxmox {
		s := Subject{"proxmox", c.ID, c.Name}
		switch {
		case c.LastError != "":
			p.add("platform.proxmox_unreachable", High, s, withReason("Proxmox-koppeling "+c.Name+" kan niet gelezen worden", c.LastError), "/proxmox")
		case c.LastSyncAt != nil && now.Sub(*c.LastSyncAt) > 10*time.Minute:
			p.add("platform.proxmox_unreachable", High, s, "Proxmox-koppeling "+c.Name+" is al "+days(now.Sub(*c.LastSyncAt))+" niet gelezen", "/proxmox")
		case c.LastSyncAt == nil && now.Sub(c.CreatedAt) > 10*time.Minute:
			p.add("platform.proxmox_unreachable", High, s, "Proxmox-koppeling "+c.Name+" is nog nooit gelezen", "/proxmox")
		}
		if c.CertNotAfter != nil {
			switch left := c.CertNotAfter.Sub(now); {
			case left <= 0:
				p.add("platform.proxmox_certificate", Critical, s, "Het certificaat van Proxmox-koppeling "+c.Name+" is verlopen op "+date(*c.CertNotAfter), "/proxmox")
			case left < 7*day:
				p.add("platform.proxmox_certificate", High, s, "Het certificaat van Proxmox-koppeling "+c.Name+" verloopt over "+days(left), "/proxmox")
			case left < Window:
				p.add("platform.proxmox_certificate", Medium, s, "Het certificaat van Proxmox-koppeling "+c.Name+" verloopt over "+days(left), "/proxmox")
			}
		}
	}
	if s := in.Self; s != nil {
		subj := Subject{"self", s.ID, s.Name}
		switch {
		case !s.Found:
			p.add("platform.self_unknown", Low, Subject{"self", uuid.Nil, "ClusterForge"},
				"Niet bekend op welke VM ClusterForge draait, dus de wat-als ziet niet wanneer hij zelf uitvalt", "/instellingen#waar-clusterforge-draait")
		case !s.BackupsRead:
		case s.Latest == nil:
			p.add("platform.self_backup", High, subj, "De VM van ClusterForge, "+s.Name+", heeft geen back-up", "/backups")
		case now.Sub(*s.Latest) > selfBackupMaxAge:
			p.add("platform.self_backup", Medium, subj, "De nieuwste back-up van de VM van ClusterForge, "+s.Name+", is "+
				days(now.Sub(*s.Latest))+" oud; het beleid is "+strconv.Itoa(backups.DefaultMaxAgeHours)+" uur", "/backups")
		}
	}
	card := builder{acc: acc}.card(Platform, p)
	res := finish([]Card{card}, nil)
	return res.Cards[0]
}

// selfBackupMaxAge is hoe oud de back-up van ClusterForge zelf mag zijn:
// het standaardbeleid.
const selfBackupMaxAge = time.Duration(backups.DefaultMaxAgeHours) * time.Hour

// --- Tekst ---

var months = []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func month(t time.Time) string { return months[t.Month()-1] + " " + strconv.Itoa(t.Year()) }

func date(t time.Time) string {
	t = t.Local()
	return strconv.Itoa(t.Day()) + " " + months[t.Month()-1][:3] + " " + strconv.Itoa(t.Year())
}

// days schrijft een duur in hele dagen, of in hele uren onder twee dagen. Zo
// verandert de zin hoogstens elk uur en hoeft de uitkomst niet elke minuut
// opnieuw bewaard te worden.
func days(d time.Duration) string {
	switch {
	case d >= 2*day:
		return plural(int(d/day), "dag", "dagen")
	case d >= time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " u"
	}
	return "minder dan een uur"
}

// dur schrijft een duur zoals mensen hem zeggen: 45 s, 5 min, 2 u 10 min,
// 3 dagen.
func dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return strconv.Itoa(h) + " u"
		}
		return fmt.Sprintf("%d u %d min", h, m)
	}
	return plural(int(d/day), "dag", "dagen")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// percent schrijft een percentage met een komma: 99,95 % of 97,2 %.
func percent(v float64) string {
	prec := 1
	if v >= 99 {
		prec = 2
	}
	s := strconv.FormatFloat(math.Floor(v*math.Pow10(prec))/math.Pow10(prec), 'f', prec, 64)
	return strings.Replace(s, ".", ",", 1) + " %"
}

// thousands schrijft 4312 als 4.312.
func thousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// list zet namen achter elkaar: "a", "a en b", "a, b en c".
func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
}

// allOf is "allebei" bij twee en "alle drie" of "alle 4" daarboven.
func allOf(n int) string {
	switch n {
	case 2:
		return "allebei"
	case 3:
		return "alle drie"
	case 4:
		return "alle vier"
	}
	return "alle " + strconv.Itoa(n)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
