package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agentupgrade"
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
