package homeapps

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/publish"
)

// GoneAfter: zo lang blijft een app die niet meer gevonden wordt als
// "weg?" op de homepage staan; daarna verdwijnt de tegel.
const GoneAfter = 24 * time.Hour

// Statussen van een app.
const (
	StatusOK      = "ok"
	StatusWarn    = "warn"
	StatusDown    = "down"
	StatusUnknown = "unknown"
)

var StatusLabels = map[string]string{StatusOK: "Gezond", StatusWarn: "Let op", StatusDown: "Down", StatusUnknown: "Onbekend"}

// Node is wat de bouwer van een node moet weten.
type Node struct {
	ID          uuid.UUID
	Hostname    string
	IP          string
	ClusterID   *uuid.UUID
	Online      bool
	Maintenance bool
}

// Cluster is wat de bouwer van een cluster moet weten.
type Cluster struct {
	ID       uuid.UUID
	Slug     string
	Name     string
	Template string
	VIP      string
	VIPOwner string
}

// Override is wat Jonas in de GUI bijstuurde; leeg = automatisch.
type Override struct {
	Hidden bool     `json:"hidden"`
	Name   string   `json:"name,omitempty"`
	Group  string   `json:"group,omitempty"`
	Icon   string   `json:"icon,omitempty"`
	URL    string   `json:"url,omitempty"`
	Sort   int      `json:"sort,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

// IsZero: niets bijgestuurd.
func (o Override) IsZero() bool {
	return !o.Hidden && o.Name == "" && o.Group == "" && o.Icon == "" && o.URL == "" && o.Sort == 0 && len(o.Fields) == 0
}

// Known is een app zoals ClusterForge ze bewaart (tabel home_apps).
type Known struct {
	Key       string
	Name      string
	Group     string
	Icon      string
	Cluster   string
	FirstSeen time.Time
	LastSeen  time.Time
	Override  Override
}

// Input is alles wat Build nodig heeft.
type Input struct {
	Now      time.Time
	Services []publish.ServiceRow
	Hosts    []publish.HostView
	Nodes    []Node
	Clusters []Cluster
	Known    []Known
	// Decisions: service-key → "ignore" of "app" (GUI, tabblad Twijfel).
	Decisions map[string]Decision
}

// Decision is wat Jonas bij een twijfelgeval koos.
type Decision struct {
	Choice string
	Name   string
}

// Instance is één plek waar de app draait.
type Instance struct {
	NodeID     uuid.UUID `json:"node_id"`
	Node       string    `json:"node"`
	IP         string    `json:"ip,omitempty"`
	Online     bool      `json:"online"`
	Port       int       `json:"port,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Container  string    `json:"container,omitempty"`
	Image      string    `json:"image,omitempty"`
	Unit       string    `json:"unit,omitempty"`
}

// Ref verwijst naar een cluster.
type Ref struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// App is één tegel.
type App struct {
	// Key is vast: <catalogus-slug of naam>@<cluster-slug of node>.
	Key string `json:"key"`
	// Slug is wat de API gebruikt: de catalogus-slug, of met het cluster of
	// de node erachter als die app meer dan eens voorkomt.
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Icon    string `json:"icon"`
	Catalog string `json:"catalog,omitempty"`
	// Auto: zelf herkend (catalogus) of via een gepubliceerde host.
	Auto        string     `json:"found_by"`
	Status      string     `json:"status"`
	StatusLabel string     `json:"status_label"`
	Reason      string     `json:"reason,omitempty"`
	Gone        bool       `json:"gone"`
	FirstSeen   *time.Time `json:"first_seen,omitempty"`
	LastSeen    *time.Time `json:"last_seen,omitempty"`
	Cluster     *Ref       `json:"cluster,omitempty"`
	VIP         string     `json:"vip,omitempty"`
	ActiveNode  string     `json:"active_node,omitempty"`
	URL         string     `json:"url,omitempty"`
	InternalURL string     `json:"internal_url,omitempty"`
	Version     string     `json:"version,omitempty"`
	Domains     []string   `json:"domains"`
	Authentik   string     `json:"authentik,omitempty"`
	Ports       []int      `json:"ports"`
	Instances   []Instance `json:"instances"`
	Problems    []string   `json:"problems"`
	Stats       string     `json:"stats,omitempty"`
	Override    Override   `json:"override"`
	Hidden      bool       `json:"hidden"`
	Sort        int        `json:"sort"`
}

// Doubt is een webdienst die ClusterForge niet zeker herkent: één klik
// maakt er een app van (of negeert ze).
type Doubt struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Node    string `json:"node,omitempty"`
	Cluster string `json:"cluster,omitempty"`
	Port    int    `json:"port"`
	Kind    string `json:"kind"`
	Title   string `json:"title,omitempty"`
	Image   string `json:"image,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

// Result is de uitkomst van Build.
type Result struct {
	Apps   []App
	Doubts []Doubt
	// Seen: de keys die nu gevonden zijn (last_seen bijwerken).
	Seen []App
	// Expired: keys die langer dan GoneAfter weg zijn (opruimen).
	Expired []string
}

type group struct {
	entry   Entry
	known   bool
	auto    string
	scope   string
	cluster *Cluster
	rows    []publish.ServiceRow
}

// Units en containers die nooit een app zijn.
var skipUnits = []string{"sshd", "ssh", "cf-agent", "clusterforge-agent", "zabbix-agent", "zabbix-agent2", "node_exporter", "prometheus-node-exporter",
	"keepalived", "crowdsec", "crowdsec-firewall-bouncer", "postfix", "rpcbind", "systemd-resolved", "chrony", "chronyd", "cups", "exim4", "smbd", "nmbd", "dnsmasq", "containerd", "dockerd", "docker"}

// Build maakt de tegels. Pure functie: geen database, zodat ze te testen is.
func Build(in Input) Result {
	nodes := map[uuid.UUID]Node{}
	for _, n := range in.Nodes {
		nodes[n.ID] = n
	}
	clusters := map[uuid.UUID]*Cluster{}
	for i := range in.Clusters {
		clusters[in.Clusters[i].ID] = &in.Clusters[i]
	}
	hostsBySvc := map[string][]publish.HostView{}
	for _, h := range in.Hosts {
		if h.ServiceKey != "" {
			hostsBySvc[h.ServiceKey] = append(hostsBySvc[h.ServiceKey], h)
		}
	}

	groups := map[string]*group{}
	var order []string
	var res Result
	for _, r := range in.Services {
		scope, cl := scopeOf(r, nodes, clusters)
		e, ok := match(r.Image, r.Unit, r.Container)
		auto := "catalogus"
		if !ok && isWeb(r) {
			if c := cl; c != nil {
				e, ok = templateEntry(c.Template)
				auto = "template"
			}
		}
		if !ok {
			if slices.Contains(skipUnits, strings.TrimSuffix(r.Unit, ".service")) || !isWeb(r) {
				continue
			}
			dec := in.Decisions[r.Key]
			if dec.Choice == "ignore" {
				continue
			}
			if len(hostsBySvc[r.Key]) == 0 && dec.Choice != "app" {
				res.Doubts = append(res.Doubts, doubtOf(r))
				continue
			}
			// Gepubliceerd in NPM maar niet in de catalogus: toch een app,
			// genoemd naar de container, unit of het subdomein.
			name := firstNonEmpty(dec.Name, r.Container, strings.TrimSuffix(r.Unit, ".service"), r.Label, r.Name)
			slug := publish.SuggestLabel(name)
			if slug == "" {
				slug = "app-" + strconv.Itoa(r.Port)
			}
			e = Entry{Slug: slug, Name: prettyName(name), Group: GroupOther, Icon: "mdi-application"}
			auto = "npm"
			if dec.Choice == "app" {
				auto = "gekozen"
			}
		}
		key := e.Slug + "@" + scope
		g := groups[key]
		if g == nil {
			g = &group{entry: e, known: auto == "catalogus" || auto == "template", auto: auto, scope: scope, cluster: cl}
			groups[key] = g
			order = append(order, key)
		}
		g.rows = append(g.rows, r)
	}

	known := map[string]Known{}
	for _, k := range in.Known {
		known[k.Key] = k
	}
	for _, key := range order {
		g := groups[key]
		a := buildApp(key, g, nodes, hostsBySvc)
		if k, ok := known[key]; ok {
			fs, ls := k.FirstSeen, in.Now
			a.FirstSeen, a.LastSeen = &fs, &ls
			a.Override = k.Override
		} else {
			now := in.Now
			a.FirstSeen, a.LastSeen = &now, &now
		}
		res.Seen = append(res.Seen, a)
		res.Apps = append(res.Apps, a)
	}
	// Apps die er niet meer zijn: nog GoneAfter als "weg?" tonen.
	for _, k := range in.Known {
		if _, ok := groups[k.Key]; ok {
			continue
		}
		if in.Now.Sub(k.LastSeen) > GoneAfter {
			res.Expired = append(res.Expired, k.Key)
			continue
		}
		fs, ls := k.FirstSeen, k.LastSeen
		slug, _, _ := strings.Cut(k.Key, "@")
		a := App{
			Key: k.Key, Slug: slug, Name: k.Name, Group: k.Group, Icon: k.Icon, Auto: "weg", Gone: true,
			Status: StatusDown, Reason: "niet meer gevonden sinds " + ls.Format("02/01 15:04"),
			FirstSeen: &fs, LastSeen: &ls, Override: k.Override,
			Domains: []string{}, Ports: []int{}, Instances: []Instance{}, Problems: []string{},
		}
		if e, ok := Lookup(slug); ok {
			a.Catalog = e.Slug
		}
		if k.Cluster != "" {
			a.Cluster = &Ref{Name: k.Cluster}
		}
		res.Apps = append(res.Apps, a)
	}
	assignSlugs(res.Apps)
	for i := range res.Apps {
		applyOverride(&res.Apps[i])
	}
	sort.SliceStable(res.Apps, func(i, j int) bool {
		a, b := res.Apps[i], res.Apps[j]
		if a.Group != b.Group {
			return groupRank(a.Group) < groupRank(b.Group)
		}
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	// Seen moet dezelfde slugs hebben.
	slugByKey := map[string]App{}
	for _, a := range res.Apps {
		slugByKey[a.Key] = a
	}
	for i := range res.Seen {
		res.Seen[i] = slugByKey[res.Seen[i].Key]
	}
	sort.Slice(res.Doubts, func(i, j int) bool { return res.Doubts[i].Key < res.Doubts[j].Key })
	return res
}

func buildApp(key string, g *group, nodes map[uuid.UUID]Node, hostsBySvc map[string][]publish.HostView) App {
	e := g.entry
	a := App{
		Key: key, Slug: e.Slug, Name: e.Name, Group: e.Group, Icon: e.Icon, Auto: g.auto, Stats: e.Stats,
		Domains: []string{}, Ports: []int{}, Instances: []Instance{}, Problems: []string{},
	}
	if g.known {
		a.Catalog = e.Slug
	}
	if c := g.cluster; c != nil {
		a.Cluster = &Ref{ID: c.ID, Slug: c.Slug, Name: c.Name}
		a.VIP = c.VIP
	}
	// De hoofdpoort: die van de catalogus, anders de laagste webpoort.
	main := 0
	for _, r := range g.rows {
		if !slices.Contains(a.Ports, r.Port) {
			a.Ports = append(a.Ports, r.Port)
		}
		if e.Port != 0 && r.Port == e.Port {
			main = r.Port
		}
	}
	slices.Sort(a.Ports)
	if main == 0 {
		for _, r := range g.rows {
			if isWeb(r) && (main == 0 || r.Port < main) {
				main = r.Port
			}
		}
	}
	if main == 0 && len(a.Ports) > 0 {
		main = a.Ports[0]
	}
	seen := map[uuid.UUID]bool{}
	var mainRow *publish.ServiceRow
	for i, r := range g.rows {
		if r.Port == main && mainRow == nil {
			mainRow = &g.rows[i]
		}
		for _, h := range hostsBySvc[r.Key] {
			for _, d := range h.Domains {
				if !slices.Contains(a.Domains, d) {
					a.Domains = append(a.Domains, d)
				}
			}
			if a.Authentik == "" {
				a.Authentik = h.Authentik
			}
			for _, p := range h.Problems {
				if p.Level == "crit" {
					a.Problems = append(a.Problems, "NPM: "+p.Text)
				}
			}
		}
		if a.Version == "" {
			a.Version = ImageTag(r.Image)
		}
		if r.Port != main {
			continue
		}
		for _, ns := range r.Nodes {
			if seen[ns.ID] {
				continue
			}
			seen[ns.ID] = true
			n := nodes[ns.ID]
			inst := Instance{NodeID: ns.ID, Node: ns.Hostname, IP: ns.IP, Online: n.Online && !n.Maintenance, Port: r.Port, Kind: r.Kind,
				HTTPStatus: r.Status, Container: r.Container, Image: r.Image, Unit: strings.TrimSuffix(r.Unit, ".service")}
			a.Instances = append(a.Instances, inst)
		}
	}
	sort.Slice(a.Instances, func(i, j int) bool { return a.Instances[i].Node < a.Instances[j].Node })
	if len(a.Domains) > 0 {
		a.URL = "https://" + a.Domains[0]
	}
	if mainRow != nil {
		scheme := "http"
		if mainRow.Kind == "https" {
			scheme = "https"
		}
		if mainRow.Target != "" {
			a.InternalURL = scheme + "://" + mainRow.Target + ":" + strconv.Itoa(mainRow.Port)
		}
	}
	if a.URL == "" {
		a.URL = a.InternalURL
	}
	if g.cluster != nil && g.cluster.VIPOwner != "" {
		for _, in := range a.Instances {
			if in.Node == g.cluster.VIPOwner {
				a.ActiveNode = in.Node
			}
		}
	}
	if a.ActiveNode == "" {
		for _, in := range a.Instances {
			if in.Online {
				a.ActiveNode = in.Node
				break
			}
		}
	}
	a.Status, a.Reason = appStatus(a.Instances, a.Problems)
	return a
}

func appStatus(ins []Instance, problems []string) (string, string) {
	if len(ins) == 0 {
		return StatusUnknown, "geen node bekend"
	}
	online, bad := 0, 0
	var offline []string
	for _, in := range ins {
		if !in.Online {
			offline = append(offline, in.Node)
			continue
		}
		online++
		if in.HTTPStatus >= 500 {
			bad++
		}
	}
	switch {
	case online == 0:
		return StatusDown, "node offline: " + strings.Join(offline, ", ")
	case bad == online:
		return StatusDown, "antwoordt met een fout (HTTP 5xx)"
	case bad > 0:
		return StatusWarn, fmt.Sprintf("%d van %d nodes antwoorden met een fout", bad, online)
	case len(offline) > 0:
		return StatusWarn, "node offline: " + strings.Join(offline, ", ")
	case len(problems) > 0:
		return StatusWarn, problems[0]
	}
	return StatusOK, ""
}

// assignSlugs: een app die maar één keer voorkomt houdt de korte slug; komt
// ze vaker voor (drie Plex-servers), dan komt er cluster of node achter.
func assignSlugs(apps []App) {
	count := map[string]int{}
	for _, a := range apps {
		count[a.Slug]++
	}
	used := map[string]bool{}
	for i := range apps {
		a := &apps[i]
		if count[a.Slug] > 1 {
			_, scope, _ := strings.Cut(a.Key, "@")
			a.Slug = a.Slug + "-" + publish.SuggestLabel(scope)
			if a.Name != "" && !a.Gone {
				a.Name = a.Name + " (" + scopeLabel(*a) + ")"
			}
		}
		for base, n := a.Slug, 2; used[a.Slug]; n++ {
			a.Slug = base + "-" + strconv.Itoa(n)
		}
		used[a.Slug] = true
	}
}

func scopeLabel(a App) string {
	if a.Cluster != nil && a.Cluster.Name != "" {
		return a.Cluster.Name
	}
	if len(a.Instances) > 0 {
		return a.Instances[0].Node
	}
	_, s, _ := strings.Cut(a.Key, "@")
	return s
}

func applyOverride(a *App) {
	o := a.Override
	a.Hidden = o.Hidden
	if o.Name != "" {
		a.Name = o.Name
	}
	if o.Group != "" {
		a.Group = o.Group
	}
	if o.Icon != "" {
		a.Icon = o.Icon
	}
	if o.URL != "" {
		a.URL = o.URL
	}
	a.Sort = o.Sort
	a.StatusLabel = StatusLabels[a.Status]
}

var groupOrder = []string{GroupMedia, GroupOffice, GroupHome, GroupDownloads, GroupNetwork, GroupSecurity, GroupMonitoring, GroupInfra, GroupOther}

func groupRank(g string) int {
	if i := slices.Index(groupOrder, g); i >= 0 {
		return i
	}
	return len(groupOrder) - 1
}

func scopeOf(r publish.ServiceRow, nodes map[uuid.UUID]Node, clusters map[uuid.UUID]*Cluster) (string, *Cluster) {
	if r.ClusterID != nil {
		if c := clusters[*r.ClusterID]; c != nil {
			return c.Slug, c
		}
	}
	if len(r.Nodes) > 0 {
		n := r.Nodes[0]
		if node, ok := nodes[n.ID]; ok && node.ClusterID != nil {
			if c := clusters[*node.ClusterID]; c != nil {
				return c.Slug, c
			}
		}
		return n.Hostname, nil
	}
	return "onbekend", nil
}

func isWeb(r publish.ServiceRow) bool { return r.Kind == "http" || r.Kind == "https" }

func doubtOf(r publish.ServiceRow) Doubt {
	d := Doubt{Key: r.Key, Name: r.Name, Cluster: r.ClusterName, Port: r.Port, Kind: r.Kind, Title: r.Title, Image: r.Image, Unit: r.Unit}
	if len(r.Nodes) > 0 {
		d.Node = r.Nodes[0].Hostname
	}
	return d
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func prettyName(s string) string {
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
