package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agentupgrade"
	"github.com/Jonasz1996/clusterforge/internal/apps"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/soc"
)

// "Aandacht nodig" (fase 24): één lijst met wat nu een actie vraagt, voor
// het overzicht en de tellers in het menu. Alleen lezen, uit gegevens die
// ClusterForge al heeft; elke regel verwijst naar de pagina waar je het
// oplost.

const attentionNames = 4

// names geeft de eerste paar namen, met "en n andere" erachter.
func names(list []string) string {
	slices.Sort(list)
	if len(list) <= attentionNames {
		return strings.Join(list, ", ")
	}
	return fmt.Sprintf("%s en %d andere", strings.Join(list[:attentionNames], ", "), len(list)-attentionNames)
}

func (s *Server) GetAttention(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	a, err := s.attention(r.Context(), p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) attention(ctx context.Context, p auth.Principal) (gen.Attention, error) {
	var items []gen.AttentionItem
	add := func(id string, tone gen.AttentionItemTone, title, detail, href string, count int) {
		if count > 0 {
			items = append(items, gen.AttentionItem{Id: id, Tone: tone, Title: title, Detail: detail, Href: href, Count: count})
		}
	}
	red, amber := gen.AttentionItemToneRed, gen.AttentionItemToneAmber

	// Clusters in storing of met een probleem.
	clusters, err := s.q.ListClusters(ctx)
	if err != nil {
		return gen.Attention{}, err
	}
	var down, degraded, split []string
	for _, c := range clusters {
		switch c.Cluster.Status {
		case string(gen.StatusDown):
			down = append(down, c.Cluster.Name)
		case string(gen.StatusDegraded):
			degraded = append(degraded, c.Cluster.Name)
		case string(gen.StatusSplitBrain):
			split = append(split, c.Cluster.Name)
		}
	}
	add("clusters-split", red, plural(len(split), "cluster heeft", "clusters hebben")+" split-brain (VIP op meer dan één node)", names(split), "/clusters", len(split))
	add("clusters-down", red, plural(len(down), "cluster ligt", "clusters liggen")+" plat", names(down), "/clusters", len(down))
	add("clusters-degraded", amber, plural(len(degraded), "cluster draait", "clusters draaien")+" verminderd", names(degraded), "/clusters", len(degraded))

	// Nodes: agents die niet antwoorden en drift, alleen voor actieve nodes
	// (onderhoud of afbouwen is bewust).
	nodes, err := s.q.ListNodes(ctx)
	if err != nil {
		return gen.Attention{}, err
	}
	var offline, drift []string
	for _, row := range nodes {
		n := toAPINode(nodeRow(row))
		if n.Lifecycle != gen.NodeLifecycleActive {
			continue
		}
		if a, err := n.Agent.Get(); err == nil && a.Connection == gen.Offline {
			offline = append(offline, n.Hostname)
		}
		if d, err := n.DriftStatus.Get(); err == nil && d == gen.NodeDriftStatusDrift {
			drift = append(drift, n.Hostname)
		}
	}
	add("agents-offline", red, plural(len(offline), "agent antwoordt", "agents antwoorden")+" niet", names(offline), "/nodes", len(offline))
	add("drift", amber, plural(len(drift), "node wijkt", "nodes wijken")+" af van de gewenste staat", names(drift), "/clusters", len(drift))

	incidents, err := s.q.CountOpenIncidents(ctx)
	if err != nil {
		return gen.Attention{}, err
	}
	add("incidents", red, plural(int(incidents), "open incident", "open incidenten"), "", "/incidenten", int(incidents))

	// Agents die bijgewerkt moeten worden en dat niet vanzelf doen.
	if s.upgrades != nil {
		ov, err := s.upgrades.Overview(ctx)
		if err != nil {
			return gen.Attention{}, err
		}
		var old, failed []string
		for _, a := range ov.Agents {
			switch {
			case a.State == agentupgrade.StateFailed:
				failed = append(failed, a.Hostname)
			case a.State == agentupgrade.StateOutdated && !a.Auto:
				old = append(old, a.Hostname)
			}
		}
		add("agents-upgrade-failed", red, "Bijwerken mislukt voor "+plural(len(failed), "agent", "agents"), names(failed), "/instellingen#agents-bijwerken", len(failed))
		add("agents-outdated", amber, plural(len(old), "agent is", "agents zijn")+" verouderd", names(old), "/instellingen#agents-bijwerken", len(old))
	}

	failedJobs, err := s.q.CountFailedJobsSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return gen.Attention{}, err
	}
	add("jobs-failed", amber, plural(int(failedJobs), "taak", "taken")+" mislukt in de laatste 24 uur", "", "/taken", int(failedJobs))

	if pending, err := s.q.CountPendingGitChanges(ctx); err == nil {
		add("git-pending", amber, plural(int(pending), "wijziging", "wijzigingen")+" uit Git wacht op goedkeuring", "", "/gitops", int(pending))
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return gen.Attention{}, err
	}

	// Het beveiligingsoverzicht (fase 25): vanaf verhoogd een regel met de
	// reden. Het overzicht is gecachet, dus dit kost bijna niets.
	if s.soc != nil {
		o, err := s.soc.Overview(ctx, soc.Window24h)
		if err != nil {
			return gen.Attention{}, err
		}
		switch o.Level.Level {
		case soc.LevelCritical, soc.LevelElevated:
			tone := amber
			if o.Level.Level == soc.LevelCritical {
				tone = red
			}
			add("soc", tone, "Dreigingsniveau "+o.Level.Level, strings.Join(o.Level.Reasons, " · "), "/soc", 1)
		}
	}

	// Kwetsbaarheden (fase 27): actief misbruikt en kritieke netwerkbevindingen.
	if s.security != nil {
		sm, err := s.security.Summary(ctx)
		if err != nil {
			return gen.Attention{}, err
		}
		add("security-network", red, plural(len(sm.CriticalNet), "adres heeft", "adressen hebben")+" een kritieke netwerkbevinding",
			names(sm.CriticalNet), "/kwetsbaarheden/scans?tab=bevindingen", len(sm.CriticalNet))
		add("security-kev", red, plural(len(sm.KEVNodes), "node heeft", "nodes hebben")+" een actief misbruikte kwetsbaarheid",
			names(sm.KEVNodes), "/kwetsbaarheden", len(sm.KEVNodes))
		add("security-malware", red, plural(len(sm.Malware), "node heeft", "nodes hebben")+" malware of een rootkit volgens ClamAV of chkrootkit",
			names(sm.Malware), "/kwetsbaarheden/tools", len(sm.Malware))
		add("security-ssh-waiting", amber, plural(len(sm.SSHWaiting), "node wacht", "nodes wachten")+" op bevestiging van een SSH-wijziging",
			names(sm.SSHWaiting)+". Kun je nog inloggen? Bevestig, anders zet de node de oude instellingen terug.", "/kwetsbaarheden/verharding", len(sm.SSHWaiting))
	}

	// Docker (fase 28): containers die niet gezond draaien en containers
	// die root op de host geven.
	if s.docker != nil {
		if hosts, err := s.docker.Hosts(ctx); err == nil {
			var bad, risky []string
			nBad, nRisky := 0, 0
			for _, h := range hosts {
				if h.Summary == nil || !h.Online {
					continue
				}
				for _, pr := range h.Summary.Problems {
					nBad++
					bad = append(bad, pr.Name+" op "+h.Hostname+" ("+pr.Reason+")")
				}
				seen := map[string]bool{}
				for _, f := range h.Summary.Findings {
					if f.Serious() && !seen[f.ID] {
						seen[f.ID] = true
						nRisky++
						risky = append(risky, f.Name+" op "+h.Hostname)
					}
				}
			}
			add("docker-problems", red, plural(nBad, "container draait", "containers draaien")+" niet gezond", names(bad), "/docker/containers?filter=problem", nBad)
			add("docker-root", amber, plural(nRisky, "container kan", "containers kunnen")+" root worden op de host (privileged of de Docker-socket)",
				names(risky), "/docker", nRisky)
		}
	}

	// Apps van templates (fase 29): wat hun ctl-scripts melden en de
	// opslag van SeaweedFS. Eén regel per cluster en kleur.
	if overviews, err := s.appOverviews(ctx, nil); err == nil {
		byID := map[string]string{}
		for _, c := range clusters {
			byID[c.Cluster.ID.String()] = c.Cluster.Name
		}
		ids := make([]string, 0, len(overviews))
		for id := range overviews {
			ids = append(ids, id.String())
		}
		slices.Sort(ids)
		for _, id := range ids {
			cid, _ := uuid.Parse(id)
			o := overviews[cid]
			var issues []apps.Issue
			for _, a := range o.Apps {
				issues = append(issues, a.Issues...)
			}
			if o.Storage != nil {
				issues = append(issues, o.Storage.Issues...)
			}
			for _, tone := range []gen.AttentionItemTone{red, amber} {
				var titles []string
				for _, i := range issues {
					if i.Tone == string(tone) {
						titles = append(titles, i.Title)
					}
				}
				if len(titles) == 0 {
					continue
				}
				title := byID[id] + ": " + titles[0]
				if len(titles) > 1 {
					title = byID[id] + ": " + plural(len(titles), "probleem", "problemen") + " met de applicatie"
				}
				add("apps-"+string(tone)+"-"+id, tone, title, names(titles), "/clusters/detail?id="+id+"#applicatie", len(titles))
			}
		}
	} else {
		return gen.Attention{}, err
	}

	// Proxmox Backup Server (fase 32): de waarschuwingen van elke PBS en de
	// back-ups van de clusternodes, uit de laatste achtergrondronde.
	if s.pbs != nil {
		s.pbsAttention(add)
	}

	// Zabbix-monitoring (fase 33): API, proxy, HA, problemen die niemand
	// bevestigt en nodes die Zabbix niet ziet.
	if s.zabbixMon != nil {
		for _, n := range s.zabbixMon.Notices() {
			tone, detail := amber, n.Detail
			if n.Red {
				tone = red
			}
			if len(n.Names) > 0 {
				detail = names(n.Names)
			}
			add(n.ID, tone, n.Title, detail, n.Href, n.Count)
		}
	}

	// OPNsense (fase 34): problemen per firewallcluster, onderhoudsmodus en
	// oude back-ups.
	if s.opnsense != nil {
		if ns, err := s.opnsense.Notices(ctx); err == nil {
			for _, n := range ns {
				tone, detail := amber, n.Detail
				if n.Red {
					tone = red
				}
				if len(n.Names) > 0 {
					detail = names(n.Names)
				}
				add(n.ID, tone, n.Title, detail, n.Href, n.Count)
			}
		} else {
			s.log.Warn("OPNsense-aandacht", "err", err)
		}
	}

	if p.User.TotpEnabledAt == nil {
		add("totp-off", amber, "Tweestapsverificatie staat uit voor je account", "Nodig voor acties op prod", "/instellingen#tweestap", 1)
	}

	// Rood eerst, dan op aantal.
	slices.SortStableFunc(items, func(a, b gen.AttentionItem) int {
		if a.Tone != b.Tone {
			if a.Tone == red {
				return -1
			}
			return 1
		}
		return b.Count - a.Count
	})
	badges := map[string]gen.AttentionBadge{}
	for _, it := range items {
		href, _, _ := strings.Cut(it.Href, "#")
		href, _, _ = strings.Cut(href, "?")
		// Kwetsbaarheden is één menu-item met tabbladen.
		if strings.HasPrefix(href, "/kwetsbaarheden/") {
			href = "/kwetsbaarheden"
		}
		// Zabbix is één menu-item met tabbladen.
		if href == "/zabbix" || strings.HasPrefix(href, "/zabbix/") {
			href = "/zabbix/overzicht"
		}
		// Een clusterpagina telt bij Clusters.
		if strings.HasPrefix(href, "/clusters/") {
			href = "/clusters"
		}
		b := badges[href]
		b.Count += it.Count
		if b.Tone != gen.AttentionBadgeToneRed {
			b.Tone = gen.AttentionBadgeTone(it.Tone)
		}
		badges[href] = b
	}
	if items == nil {
		items = []gen.AttentionItem{}
	}
	return gen.Attention{Items: items, Badges: badges}, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
