package httpapi

import (
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Aandacht per oorzaak (fase 47). Dezelfde dienst die op 25 nodes faalt, is
// één regel met 25 nodes en één oplossing, niet 25 clusters "verminderd".
// Elke regel krijgt acties bij zich: de logs van de dienst, herstarten, en
// waar het hoort de pagina die het oplost.

// attentionCauses groepeert de diensten die op actieve nodes falen. covered
// zijn de clusters die alleen verminderd zijn door zo'n dienst: hun dienst
// werkt, en ze staan al bij de oorzaak.
func attentionCauses(clusters []store.ListClustersRow, nodes []gen.Node) (items []gen.AttentionItem, covered map[uuid.UUID]bool) {
	clusterStatus := map[uuid.UUID]string{}
	for _, c := range clusters {
		clusterStatus[c.Cluster.ID] = c.Cluster.Status
	}
	type group struct {
		p     status.ServiceProblem
		ids   []uuid.UUID
		hosts []string
		down  bool
	}
	groups := map[status.ServiceProblem]*group{}
	var order []status.ServiceProblem
	// Per cluster: is elke node die niet gezond is dat alleen door diensten?
	onlyServices := map[uuid.UUID]bool{}
	for _, n := range nodes {
		if n.Lifecycle != gen.NodeLifecycleActive || n.Status == gen.StatusHealthy || n.Status == gen.StatusUnknown {
			continue
		}
		cid, cerr := n.ClusterId.Get()
		svc, other := status.ParseReason(n.StatusReason)
		if cerr == nil {
			ok, seen := onlyServices[cid]
			onlyServices[cid] = (ok || !seen) && len(other) == 0 && len(svc) > 0 && n.Status == gen.StatusDegraded
		}
		for _, p := range svc {
			g := groups[p]
			if g == nil {
				g = &group{p: p}
				groups[p] = g
				order = append(order, p)
			}
			g.ids = append(g.ids, n.Id)
			g.hosts = append(g.hosts, n.Hostname)
			if cerr == nil {
				if st := clusterStatus[cid]; st == string(gen.StatusDown) || st == string(gen.StatusSplitBrain) {
					g.down = true
				}
			}
		}
	}
	covered = map[uuid.UUID]bool{}
	for cid, ok := range onlyServices {
		if ok && clusterStatus[cid] == string(gen.StatusDegraded) {
			covered[cid] = true
		}
	}
	slices.SortFunc(order, func(a, b status.ServiceProblem) int {
		if d := len(groups[b].ids) - len(groups[a].ids); d != 0 {
			return d
		}
		return strings.Compare(a.Unit, b.Unit)
	})
	for _, p := range order {
		g := groups[p]
		tone := gen.AttentionItemToneAmber
		if p.State != status.ServiceStopped || g.down {
			tone = gen.AttentionItemToneRed
		}
		where := "op " + g.hosts[0]
		href := "/nodes/detail?id=" + g.ids[0].String()
		if len(g.ids) > 1 {
			where = "op " + plural(len(g.ids), "node", "nodes")
			href = "/nodes?zoek=" + url.QueryEscape(p.Unit)
		}
		impact := gen.AttentionItemImpactServiceOk
		if g.down {
			impact = gen.AttentionItemImpactServiceDown
		}
		help := causeHelp(p, len(g.ids), g.down)
		it := gen.AttentionItem{
			Id: "cause-" + string(p.State) + "-" + p.Unit, Tone: tone, Title: p.Text() + " " + where,
			Detail: names(slices.Clone(g.hosts)), Href: href, Count: len(g.ids), Impact: &impact, Help: &help,
		}
		ids := slices.Clone(g.ids)
		actions := []gen.AttentionAction{
			{Kind: gen.Link, Label: "Logs van " + p.Unit,
				Href: ptr("/logs?node=" + g.ids[0].String() + "&kind=journal&name=" + url.QueryEscape(p.Unit))},
			{Kind: gen.RestartService, Label: restartLabel(len(ids)), Unit: ptr(p.Unit), NodeIds: &ids},
		}
		switch {
		case p.Unit == "ufw":
			actions = append(actions, gen.AttentionAction{Kind: gen.Link, Label: "Firewall", Href: ptr("/firewall?tab=nodes")})
		case p.Unit == "crowdsec" || strings.HasPrefix(p.Unit, "crowdsec-"):
			actions = append(actions, gen.AttentionAction{Kind: gen.Link, Label: "CrowdSec", Href: ptr("/crowdsec")})
		case p.Unit == "docker" || p.Unit == "containerd":
			actions = append(actions, gen.AttentionAction{Kind: gen.Link, Label: "Docker", Href: ptr("/docker")})
		}
		it.Actions = &actions
		items = append(items, it)
	}
	return items, covered
}

func restartLabel(n int) string {
	if n == 1 {
		return "Herstarten"
	}
	return "Herstarten op alle " + plural(n, "node", "nodes")[2:]
}

func causeHelp(p status.ServiceProblem, n int, down bool) string {
	var b strings.Builder
	switch p.State {
	case status.ServiceCrashing:
		b.WriteString("De dienst " + p.Unit + " stopt telkens en systemd start hem opnieuw. ")
	case status.ServiceFailed:
		b.WriteString("De dienst " + p.Unit + " is gestopt met een fout. ")
	default:
		b.WriteString("De dienst " + p.Unit + " staat aan bij het opstarten, maar draait nu niet. ")
	}
	if down {
		b.WriteString("Een cluster ligt daardoor plat. ")
	} else {
		b.WriteString("De clusters werken nog: dit is een waarschuwing, geen storing. ")
	}
	b.WriteString("Kijk eerst in de logs waarom; herstarten lost een eenmalige fout op. ")
	if p.State == status.ServiceStopped {
		b.WriteString("Zette je de dienst bewust uit, schakel hem dan ook uit bij het opstarten (systemctl disable), dan verdwijnt de melding.")
	}
	if n > 1 {
		b.WriteString(" Herstarten gebeurt één node tegelijk.")
	}
	return strings.TrimSpace(b.String())
}

// decorate zet de impact en de vaste acties bij de andere regels.
func decorate(it *gen.AttentionItem) {
	link := func(label, href string) gen.AttentionAction {
		return gen.AttentionAction{Kind: gen.Link, Label: label, Href: ptr(href)}
	}
	set := func(i gen.AttentionItemImpact) {
		if it.Impact == nil {
			it.Impact = &i
		}
	}
	var acts []gen.AttentionAction
	switch it.Id {
	case "clusters-down", "clusters-split":
		set(gen.AttentionItemImpactServiceDown)
		acts = append(acts, link("Wat brak er?", "/incidenten"), link("Logs", "/logs"))
	case "clusters-degraded":
		set(gen.AttentionItemImpactServiceOk)
	case "agents-offline":
		acts = append(acts, link("Nodes", "/nodes"))
	case "jobs-failed":
		acts = append(acts, link("Mislukte taken", "/taken?filter=mislukt"))
	case "agents-outdated", "agents-upgrade-failed":
		acts = append(acts, link("Agents bijwerken", "/instellingen#agents-bijwerken"))
	case "security-kev":
		acts = append(acts, link("Updates", "/updates"))
	case "docker-problems":
		acts = append(acts, link("Containers", "/docker/containers?filter=problem"), link("Logs", "/logs"))
	}
	if len(acts) > 0 && it.Actions == nil {
		it.Actions = &acts
	}
}

