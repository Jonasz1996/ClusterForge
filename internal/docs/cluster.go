package docs

import (
	"cmp"
	"fmt"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- Architectuur ---

func architecture(c *Cluster, o Opts) string {
	d := &doc{}
	header(d, c, KindArchitecture)
	if c.Description != "" {
		d.p(esc(c.Description))
	}
	tpl, rev := "", ""
	if t := c.Template; t != nil {
		tpl = esc(or(t.Title, t.Name)) + " (" + code(t.Name) + " " + esc(t.Version) + ")"
		rev = revisionText(c)
	}
	git := ""
	if c.GitLinked {
		git = "gekoppeld; wijzigingen komen uit Git"
	}
	d.facts([][2]string{
		{"Omgeving", envOf(c.Env)},
		{"Soort", esc(c.Type)},
		{"Template", tpl},
		{"Revisie", rev},
		{"Git", git},
		{"Tags", esc(strings.Join(c.Tags, ", "))},
	})

	d.h2("Wat het doet")
	if t := c.Template; t != nil && t.Description != "" {
		d.p(esc(t.Description))
	} else if t == nil {
		d.p("Dit cluster is niet uit een template uitgerold. Wat hier staat, komt uit de inventory, de facts van de nodes en de diensten.")
	}
	var hosts []string
	for _, n := range c.Nodes {
		hosts = append(hosts, esc(n.Hostname))
	}
	summary := "Het cluster heeft nog geen nodes."
	if len(c.Nodes) > 0 {
		summary = "Het cluster heeft " + plural(len(c.Nodes), "node", "nodes") + ": " + list(hosts) + "."
	}
	if len(c.VIPs) > 0 {
		var vips []string
		for _, v := range c.VIPs {
			vips = append(vips, code(v.Address))
		}
		summary += " Het is bereikbaar op " + plural(len(c.VIPs), "VIP", "VIP's") + ": " + list(vips) + "."
	}
	d.p(summary)

	d.h2("Nodes")
	if len(c.Nodes) == 0 {
		d.p("Er staan geen nodes in dit cluster.")
	} else {
		rows := make([][]string, 0, len(c.Nodes))
		for _, n := range c.Nodes {
			rows = append(rows, []string{
				"**" + esc(n.Hostname) + "**", esc(n.Role), code(n.Address), vmText(n), guestText(n.Guest), esc(n.OS), lifecycleText[n.Lifecycle],
			})
		}
		d.table([]string{"Node", "Rol", "Adres", "VM", "Proxmox", "Besturingssysteem", "Lifecycle"}, rows)
		var notes []string
		for _, n := range c.Nodes {
			if !n.HasAgent {
				notes = append(notes, esc(n.Hostname)+" heeft geen agent; ClusterForge kent zijn adres en besturingssysteem dan niet uit de facts.")
			}
		}
		d.bullets(notes)
	}

	if len(c.VIPs) > 0 {
		d.h2("VIP")
		var items []string
		for _, v := range c.VIPs {
			items = append(items, vipText(c, v))
		}
		d.bullets(items)
	}

	d.h2("Diensten")
	if len(c.Services) == 0 {
		d.p("ClusterForge kent geen bevestigde diensten in dit cluster.")
		return d.String()
	}
	if f := serviceFlow(c); !f.empty() {
		d.code("mermaid", f.String())
	}
	rows := make([][]string, 0, len(c.Services))
	for _, s := range c.Services {
		port := ""
		if s.Port > 0 {
			port = strconv.Itoa(s.Port)
		}
		rows = append(rows, []string{"**" + esc(s.Name) + "**", esc(s.Kind), code(s.Unit), port, or(esc(s.On), "alle nodes"), sourceText(s.Source)})
	}
	d.table([]string{"Dienst", "Soort", "Unit", "Poort", "Draait op", "Bron"}, rows)

	var uses, usedBy []string
	for _, dp := range c.Deps {
		from, to := inCluster(c, dp.From), inCluster(c, dp.To)
		text := endText(c, dp.From) + " hangt af van " + endText(c, dp.To) + " (" + depDetail(dp) + ")."
		switch {
		case from:
			uses = append(uses, text)
		case to:
			usedBy = append(usedBy, text)
		}
	}
	if len(uses) > 0 {
		d.h3("Afhankelijkheden")
		d.bullets(uses)
	}
	if len(usedBy) > 0 {
		d.h3("Wie hiervan afhangt")
		d.bullets(usedBy)
	}
	return d.String()
}

func revisionText(c *Cluster) string {
	if c.Applied >= c.Revision {
		return strconv.Itoa(c.Revision) + ", toegepast"
	}
	if c.Applied == 0 {
		return strconv.Itoa(c.Revision) + ", nog niet toegepast"
	}
	return fmt.Sprintf("%d, toegepast tot revisie %d", c.Revision, c.Applied)
}

func vmText(n Node) string {
	if v := n.VM; v != nil {
		return fmt.Sprintf("%d vCPU, %s geheugen, %d GB schijf", v.CPU, mib(v.MemoryMiB), v.DiskGiB)
	}
	if g := n.Guest; g != nil && g.MaxMem > 0 {
		return size(g.MaxMem) + " geheugen"
	}
	return ""
}

func guestText(g *Guest) string {
	if g == nil {
		return "niet gekoppeld"
	}
	what := "VM "
	if g.Type == "lxc" {
		what = "container "
	}
	s := what + strconv.Itoa(g.VMID) + " op " + esc(g.Host)
	if g.HA {
		s += ", met HA"
	}
	return s
}

// vipText beschrijft een VIP met de volgorde van de nodes en de failback.
func vipText(c *Cluster, v VIP) string {
	s := code(v.Address)
	if v.Interface != "" {
		s += " op " + code(v.Interface)
	}
	if v.VRID != nil {
		s += ", VRRP-id " + strconv.Itoa(*v.VRID)
	}
	s += "."
	if v.Description != "" {
		s += " " + esc(v.Description) + "."
	}
	if len(v.Nodes) > 0 {
		var names []string
		for _, n := range v.Nodes {
			names = append(names, esc(n))
		}
		if len(names) == 1 {
			s += " Alleen " + names[0] + " heeft het adres in zijn keepalived-configuratie."
		} else {
			s += " Keepalived geeft het aan de node met de hoogste prioriteit: eerst " + names[0] + ", dan " + list(names[1:]) + "."
			if t := c.Template; t != nil && t.Failback {
				s += " Zodra " + names[0] + " weer gezond is, keert het VIP terug (failback)."
			} else if t != nil {
				s += " Het blijft bij de node die het overnam, ook als " + names[0] + " terug is."
			}
		}
	}
	return s
}

var sourceTexts = map[string]string{"template": "template", "manual": "met de hand", "discovered": "ontdekt"}

func sourceText(s string) string { return or(sourceTexts[s], s) }

func depDetail(dp Dep) string {
	s := "hard"
	if dp.Strength == "soft" {
		s = "zacht"
	}
	switch dp.Source {
	case "discovered":
		s += ", ontdekt uit verbindingen"
	case "manual":
		s += ", met de hand"
	case "template":
		s += ", uit de template"
	}
	if dp.Note != "" {
		s += ": " + esc(dp.Note)
	}
	return s
}

func inCluster(c *Cluster, e End) bool { return e.ClusterID != nil && *e.ClusterID == c.ID }

// endText noemt een kant van een pijl zoals dit cluster hem ziet: een dienst
// hier bij naam, een andere met zijn cluster.
func endText(c *Cluster, e End) string {
	switch {
	case inCluster(c, e):
		return "**" + esc(e.Name) + "**"
	case e.External:
		if e.Address != "" {
			return esc(e.Name) + " (extern, " + code(hostPort(e.Address, e.Port)) + ")"
		}
		return esc(e.Name) + " (extern)"
	}
	return esc(e.Name) + " in " + esc(e.Group)
}

func hostPort(addr string, port int) string {
	if port == 0 {
		return addr
	}
	if strings.Contains(addr, ":") {
		return "[" + addr + "]:" + strconv.Itoa(port)
	}
	return addr + ":" + strconv.Itoa(port)
}

// serviceFlow tekent de diensten van het cluster in een vak, met de
// diensten van andere clusters en extern waarmee ze verbonden zijn.
func serviceFlow(c *Cluster) *flow {
	f := newFlow("LR")
	own := f.group(c.Name, envOf(c.Env))
	for _, s := range c.Services {
		lines := []string{s.Name, s.Kind}
		if s.Port > 0 {
			lines[1] += ", poort " + strconv.Itoa(s.Port)
		}
		if s.On != "" {
			lines = append(lines, "op "+s.On)
		}
		shape := "box"
		if s.Kind == "database" {
			shape = "db"
		}
		f.node(s.ID.String(), own, shape, lines...)
	}
	groups := map[string]*flowGroup{}
	end := func(e End) string {
		if f.has(e.ID.String()) {
			return f.ids[e.ID.String()]
		}
		if e.External {
			lines := []string{e.Name, "extern"}
			if e.Address != "" {
				lines[1] = hostPort(e.Address, e.Port)
			}
			return f.node(e.ID.String(), nil, "round", lines...)
		}
		g := groups[e.Group]
		if g == nil {
			g = f.group(e.Group)
			groups[e.Group] = g
		}
		return f.node(e.ID.String(), g, "box", e.Name)
	}
	for _, dp := range c.Deps {
		arrow := "-->"
		if dp.Strength == "soft" {
			arrow = "-.->"
		}
		f.edge(end(dp.From), end(dp.To), arrow, "")
	}
	return f
}

// --- Netwerk ---

func network(c *Cluster, o Opts) string {
	d := &doc{}
	header(d, c, KindNetwork)
	if n := c.Network; n != nil {
		mode := "DHCP"
		if n.Mode == "static" {
			mode = "vaste adressen, vanaf " + code(n.FirstIP)
		}
		vlan := "geen"
		if n.VLAN != nil {
			vlan = strconv.Itoa(*n.VLAN)
		}
		image := ""
		if n.ImageVMID > 0 {
			image = "VM " + strconv.Itoa(n.ImageVMID)
		}
		d.facts([][2]string{
			{"Subnet", code(subnetOf(n.FirstIP))},
			{"Adressen", mode},
			{"Gateway", code(n.Gateway)},
			{"DNS", codes(n.DNS)},
			{"Bridge", code(n.Bridge)},
			{"VLAN", vlan},
			{"Proxmox", esc(n.Proxmox)},
			{"Storage", code(n.Storage)},
			{"Golden image", image},
		})
	} else {
		d.p("Dit cluster is niet uit een template uitgerold. Wat hier staat, komt uit de facts van de nodes en de configuratie van hun VM's in Proxmox.")
		var subnets, gws, dns []string
		for _, n := range c.Nodes {
			if s := subnetOf(n.Address + "/" + strconv.Itoa(n.Prefix)); s != "" && n.Prefix > 0 {
				subnets = appendNew(subnets, s)
			}
			if n.Gateway != "" {
				gws = appendNew(gws, n.Gateway)
			}
			for _, x := range n.DNS {
				dns = appendNew(dns, x)
			}
		}
		d.facts([][2]string{{"Subnet", codes(subnets)}, {"Gateway", codes(gws)}, {"DNS", codes(dns)}})
	}

	if f := networkFlow(c); !f.empty() {
		d.h2("Diagram")
		d.code("mermaid", f.String())
	}

	d.h2("Adressen")
	var rows [][]string
	var notes []string
	for _, n := range c.Nodes {
		rows = append(rows, addressRows(c, n)...)
		if c.Network == nil && (n.Guest == nil || !n.Guest.ConfigRead) {
			why := "de gastconfiguratie is nog niet gelezen"
			if n.Guest == nil {
				why = "de node is niet aan een VM in Proxmox gekoppeld"
			}
			notes = append(notes, "VLAN van "+esc(n.Hostname)+" onbekend: deze node is niet uit een template uitgerold en "+why+".")
		}
	}
	if len(rows) == 0 {
		d.p("Er staan geen nodes in dit cluster.")
	} else {
		d.table([]string{"Node", "Interface", "Adressen", "MAC", "Bridge", "VLAN"}, rows)
	}
	d.bullets(notes)

	if len(c.VIPs) > 0 {
		d.h2("VIP's")
		rows = nil
		for _, v := range c.VIPs {
			vrid := "onbekend"
			if v.VRID != nil {
				vrid = strconv.Itoa(*v.VRID)
			}
			var names []string
			for _, n := range v.Nodes {
				names = append(names, esc(n))
			}
			rows = append(rows, []string{code(v.Address), code(v.Interface), vrid, strings.Join(names, ", "), esc(v.Description)})
		}
		d.table([]string{"VIP", "Interface", "VRRP-id", "Nodes, hoogste prioriteit eerst", "Beschrijving"}, rows)
	}

	rows = nil
	for _, s := range c.Services {
		if s.Port == 0 {
			continue
		}
		where := esc(s.On)
		if s.On == "" {
			where = "alle nodes"
			if len(c.VIPs) > 0 {
				var vips []string
				for _, v := range c.VIPs {
					vips = append(vips, code(v.Address))
				}
				where = list(vips) + " en alle nodes"
			}
		}
		rows = append(rows, []string{"**" + esc(s.Name) + "**", strconv.Itoa(s.Port), where})
	}
	if len(rows) > 0 {
		d.h2("Poorten")
		d.table([]string{"Dienst", "Poort", "Bereikbaar op"}, rows)
	}
	return d.String()
}

// addressRows geeft de netwerkkaarten van een node: uit de facts, met de
// bridge en het VLAN van de kaart in Proxmox met hetzelfde MAC-adres.
func addressRows(c *Cluster, n Node) [][]string {
	var nics []NIC
	if n.Guest != nil {
		nics = n.Guest.NICs
	}
	used := map[int]bool{}
	vlanOf := func(nic *NIC) string {
		switch {
		case nic != nil && nic.Tag > 0:
			return strconv.Itoa(nic.Tag)
		case nic != nil:
			return "geen"
		case c.Network != nil && c.Network.VLAN != nil:
			return strconv.Itoa(*c.Network.VLAN)
		case c.Network != nil:
			return "geen"
		}
		return "onbekend"
	}
	var rows [][]string
	host := "**" + esc(n.Hostname) + "**"
	for _, i := range n.Interfaces {
		var nic *NIC
		for k := range nics {
			if i.MAC != "" && strings.EqualFold(nics[k].MAC, i.MAC) {
				nic, used[k] = &nics[k], true
			}
		}
		bridge := ""
		if nic != nil {
			bridge = code(nic.Bridge)
		}
		rows = append(rows, []string{host, code(i.Name), codes(i.Addresses), code(strings.ToLower(i.MAC)), bridge, vlanOf(nic)})
		host = ""
	}
	for k, nic := range nics {
		if used[k] {
			continue
		}
		rows = append(rows, []string{host, code(nic.Key) + " in Proxmox", "", code(strings.ToLower(nic.MAC)), code(nic.Bridge), vlanOf(&nics[k])})
		host = ""
	}
	if len(rows) == 0 {
		addr := ""
		if n.Address != "" {
			addr = code(n.Address)
		}
		rows = append(rows, []string{host, "", addr, "", "", vlanOf(nil)})
	}
	return rows
}

// networkFlow tekent het subnet, de hosts met hun nodes en de VIP's.
func networkFlow(c *Cluster) *flow {
	f := newFlow("TB")
	type subnet struct{ key, gw string }
	var subnets []subnet
	subnetOfNode := map[uuid.UUID]string{}
	for _, n := range c.Nodes {
		if n.Address == "" || n.Prefix == 0 {
			continue
		}
		s := subnetOf(n.Address + "/" + strconv.Itoa(n.Prefix))
		if s == "" {
			continue
		}
		subnetOfNode[n.ID] = s
		if !slices.ContainsFunc(subnets, func(x subnet) bool { return x.key == s }) {
			gw := n.Gateway
			if c.Network != nil && c.Network.Gateway != "" && subnetOf(c.Network.FirstIP) == s {
				gw = c.Network.Gateway
			}
			subnets = append(subnets, subnet{s, gw})
		}
	}
	for _, s := range subnets {
		lines := []string{s.key}
		if s.gw != "" {
			lines = append(lines, "gateway "+s.gw)
		}
		f.node("subnet:"+s.key, nil, "box", lines...)
	}
	for _, v := range c.VIPs {
		lines := []string{"VIP " + v.Address}
		if v.VRID != nil {
			lines = append(lines, "VRRP-id "+strconv.Itoa(*v.VRID))
		}
		f.node("vip:"+v.Address, nil, "stadium", lines...)
	}
	groups := map[string]*flowGroup{}
	for _, n := range c.Nodes {
		var g *flowGroup
		lines := []string{n.Hostname}
		if n.Address != "" {
			lines = append(lines, n.Address)
		}
		if gu := n.Guest; gu != nil {
			key := gu.Proxmox + "/" + gu.Host
			if g = groups[key]; g == nil {
				g = f.group(gu.Host, "Proxmox "+gu.Proxmox)
				groups[key] = g
			}
			var bridges []string
			for _, nic := range gu.NICs {
				b := nic.Bridge
				if nic.Tag > 0 {
					b += ", VLAN " + strconv.Itoa(nic.Tag)
				}
				bridges = appendNew(bridges, b)
			}
			if len(bridges) > 0 {
				lines = append(lines, strings.Join(bridges, "; "))
			}
		}
		id := f.node("node:"+n.ID.String(), g, "box", lines...)
		if s, ok := subnetOfNode[n.ID]; ok {
			f.edge(f.ids["subnet:"+s], id, "---", "")
		}
	}
	for _, v := range c.VIPs {
		for _, name := range v.Nodes {
			for _, n := range c.Nodes {
				if n.Hostname == name {
					f.edge(f.ids["vip:"+v.Address], f.ids["node:"+n.ID.String()], "-.-", "")
				}
			}
		}
	}
	return f
}

// subnetOf geeft het netwerk van een adres met prefix, zoals 10.0.20.0/24.
func subnetOf(cidr string) string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return ""
	}
	return p.Masked().String()
}

func codes(items []string) string {
	var out []string
	for _, s := range items {
		if s != "" {
			out = append(out, code(s))
		}
	}
	return strings.Join(out, ", ")
}

func appendNew(list []string, s string) []string {
	if s == "" || slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// --- Configuratie ---

var revisionSources = map[string]string{"ui": "webinterface", "api": "API", "git": "Git"}

func configuration(c *Cluster, o Opts) string {
	d := &doc{}
	header(d, c, KindConfiguration)
	t := c.Template
	if t == nil {
		d.p("Dit cluster is niet uit een template uitgerold, dus ClusterForge beheert er geen configuratie en kent het geen parameters of revisies.")
		return d.String()
	}
	d.p("Template " + code(t.Name) + " " + esc(t.Version) + ", revisie " + revisionText(c) + ".")
	if len(c.Notes) > 0 {
		var notes []string
		for _, n := range c.Notes {
			notes = append(notes, esc(n))
		}
		d.p("**Let op:**")
		d.bullets(notes)
	}

	d.h2("Parameters")
	if len(c.Params) == 0 {
		d.p("Deze template heeft geen parameters.")
	} else {
		rows := make([][]string, 0, len(c.Params))
		for _, p := range c.Params {
			v := "niet ingevuld"
			switch {
			case p.Secret:
				v = code("[geheim]")
			case p.Value != nil:
				v = code(*p.Value)
			}
			rows = append(rows, []string{esc(p.Label), code(p.Name), v})
		}
		d.table([]string{"Parameter", "Naam", "Waarde"}, rows)
	}

	d.h2("Beheerde bestanden")
	d.p("Wat ClusterForge volgens revisie " + strconv.Itoa(c.Revision) + " op elke node zet. Geheimen staan er als " + code("[geheim]") +
		"; wat er echt op een node staat, vergelijkt de driftcontrole.")
	if c.FilesNote != "" {
		d.p(esc(c.FilesNote))
	}
	for _, nf := range c.Files {
		d.h3(esc(nf.Hostname))
		if nf.Error != "" {
			d.p("Niet te renderen: " + esc(nf.Error))
			continue
		}
		d.facts([][2]string{{"Pakketten", codes(nf.Packages)}, {"Services", esc(strings.Join(nf.Services, ", "))}})
		for _, f := range nf.Files {
			d.h4(code(f.Path))
			own := strings.Trim(or(f.Owner, "root")+":"+or(f.Group, "root"), ":")
			d.p("Modus " + code(or(f.Mode, "0644")) + ", eigenaar " + code(own) + ".")
			d.code(fileLang(f.Path), f.Content)
		}
	}

	if c.YAML != nil {
		d.h2("cluster.yaml")
		if c.GitLinked {
			d.p("Dit cluster komt uit Git. Zo exporteert ClusterForge het bestand " + code(c.YAML.Name) + ", zonder geheimen.")
		} else {
			d.p("Zo zou " + code(c.YAML.Name) + " in Git staan, zonder geheimen.")
		}
		d.code("yaml", c.YAML.Content)
	}

	d.h2("Revisies")
	if len(c.Revisions) == 0 {
		d.p("Er zijn nog geen revisies.")
		return d.String()
	}
	rows := make([][]string, 0, len(c.Revisions))
	for _, r := range c.Revisions {
		src := or(revisionSources[r.Source], r.Source)
		if r.Commit != "" {
			src += ", commit " + code(shortSha(r.Commit))
		}
		var changes []string
		for _, ch := range r.Changes {
			changes = append(changes, esc(ch.Label)+": "+changeText(ch))
		}
		what := strings.Join(changes, "; ")
		if r.Revision == 1 || (what == "" && r.Template != "") {
			what = or(what, "eerste uitrol, "+esc(r.Template))
		}
		row := []string{strconv.Itoa(r.Revision), o.timeText(r.At), src}
		if o.Usernames {
			row = append(row, esc(r.By))
		}
		rows = append(rows, append(row, what))
	}
	head := []string{"Revisie", "Wanneer", "Bron"}
	if o.Usernames {
		head = append(head, "Door")
	}
	d.table(append(head, "Wat veranderde"), rows)
	if c.OlderRevisions > 0 {
		d.p("En " + plural(c.OlderRevisions, "oudere revisie", "oudere revisies") + ".")
	}
	return d.String()
}

func changeText(ch Change) string {
	switch {
	case ch.From == "":
		return code(ch.To) + " ingevuld"
	case ch.To == "":
		return code(ch.From) + " gewist"
	}
	return code(ch.From) + " → " + code(ch.To)
}

func shortSha(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

var langs = map[string]string{
	".yaml": "yaml", ".yml": "yaml", ".json": "json", ".sh": "sh", ".html": "html", ".ini": "ini", ".service": "ini", ".toml": "toml",
	".conf": "conf", ".cfg": "conf", ".xml": "xml", ".sql": "sql", ".py": "python",
}

func fileLang(p string) string { return langs[strings.ToLower(path.Ext(p))] }

// --- Herstel ---

var freshnessText = map[string]string{"ok": "vers", "stale": "te oud", "missing": "ontbreekt", "unknown": "onbekend"}

var resultText = map[string]string{
	"pass": "geslaagd", "warning": "geslaagd met waarschuwing", "fail": "mislukt", "error": "fout", "skipped": "overgeslagen", "canceled": "geannuleerd",
}

func recovery(c *Cluster, o Opts) string {
	d := &doc{}
	header(d, c, KindRecovery)

	d.h2("Back-ups")
	pol := c.Backup
	policy := "Een back-up mag hoogstens " + plural(pol.MaxAgeHours, "uur", "uur") + " oud zijn"
	if pol.Default {
		policy += " (de standaard)"
	}
	policy += "."
	verify := "Geplande back-upcontroles staan uit; een controle start je bij Back-ups."
	if pol.VerifyEnabled {
		verify = "ClusterForge controleert de back-ups gepland in het testvenster (" + esc(pol.Window) +
			"): het zet er een terug in een afgesloten sandbox en kijkt of de VM opstart."
	}
	d.p(policy, verify)
	rows := [][]string{}
	for _, n := range c.Nodes {
		host := "**" + esc(n.Hostname) + "**"
		switch {
		case n.Guest == nil:
			rows = append(rows, []string{host, "niet gekoppeld aan Proxmox", "", "", "", ""})
		case n.Backup == nil || n.Backup.Volid == "":
			rows = append(rows, []string{host, guestText(n.Guest), "geen", "", "", "ontbreekt"})
		default:
			b := n.Backup
			rows = append(rows, []string{host, guestText(n.Guest), o.timeText(b.Time), code(b.Storage), size(b.Size), or(freshnessText[b.Freshness], b.Freshness)})
		}
	}
	if len(rows) > 0 {
		d.table([]string{"Node", "VM", "Nieuwste back-up", "Storage", "Grootte", "Stand"}, rows)
	}

	d.h2("Back-upcontroles")
	rows = nil
	for _, n := range c.Nodes {
		v := n.Verify
		if v == nil || v.FinishedAt == nil {
			continue
		}
		rec := ""
		if v.RecoverySeconds != nil {
			rec = duration(time.Duration(*v.RecoverySeconds * float64(time.Second)))
		}
		rows = append(rows, []string{"**" + esc(n.Hostname) + "**", o.timeText(*v.FinishedAt), or(resultText[v.Result], v.Result), rec})
	}
	if len(rows) == 0 {
		d.p("Er is nog geen back-up van dit cluster gecontroleerd.")
	} else {
		d.p("De hersteltijd is terugzetten plus opstarten, gemeten in de sandbox.")
		d.table([]string{"Node", "Laatste controle", "Uitkomst", "Hersteltijd"}, rows)
	}

	d.h2("Failovertests")
	if len(c.Failover) == 0 {
		d.p("Dit cluster heeft geen failovertests.")
	} else {
		rows = nil
		for _, t := range c.Failover {
			expect := "overname binnen " + plural(t.MaxTakeoverSeconds, "seconde", "seconden")
			if t.ExpectFailback {
				expect += ", daarna failback"
			}
			if t.Scheduled {
				expect += "; gepland"
			}
			last, measured := "nog niet gedraaid", ""
			if r := t.Last; r != nil {
				last = o.dateText(r.At) + ": " + or(resultText[r.Result], r.Result)
				measured = measuredText(r)
			}
			rows = append(rows, []string{"**" + esc(t.Name) + "**", esc(t.Description) + " (" + code(t.VIP) + ")", expect, last, measured})
		}
		d.table([]string{"Test", "Storing", "Verwacht", "Laatste run", "Gemeten"}, rows)
	}

	d.h2("DR-repetitie")
	drSection(d, c, o)

	d.h2("Wat er uitvalt")
	if len(c.Outages) == 0 {
		d.p("ClusterForge kan dit niet voorspellen: er staan geen nodes in dit cluster.")
		return d.String()
	}
	d.p("Wat ClusterForge voorspelt als één onderdeel uitvalt terwijl de rest gezond is, met de statusregels en de afhankelijkheden van nu.")
	rows = nil
	for _, u := range c.Outages {
		rows = append(rows, []string{outageName(u), outageText(u), outageOthers(u)})
	}
	d.table([]string{"Uitval van", esc(c.Name), "Ook geraakt"}, rows)
	return d.String()
}

var scheduleText = map[string]string{"weekly": "elke week", "monthly": "in het eerste testvenster van elke maand"}

// drSection schrijft wat de DR-repetitie van het cluster mat, met datum en
// doel, en wanneer ze draait.
func drSection(d *doc, c *Cluster, o Opts) {
	dr := c.DR
	if m := dr.Measured; m != nil {
		d.p("Gemeten in de sandbox op "+o.dateText(m.At)+", met de DR-repetitie "+quoted(m.Name)+".", drGoals(dr))
		d.table([]string{"", "Gemeten", "Doel", "Gehaald"}, [][]string{
			{"**RTO**, hersteltijd", durationOr(m.RTO), goalText(dr.RTOMinutes, "minuut", "minuten"), metText(m.RTO, time.Duration(dr.RTOMinutes)*time.Minute)},
			{"**RPO**, gegevensverlies", durationOr(m.RPO), goalText(dr.RPOHours, "uur", "uur"), metText(m.RPO, time.Duration(dr.RPOHours)*time.Hour)},
		})
		d.p("De RTO loopt van de start van het terugzetten tot alle controles in de sandbox slaagden. De RPO is hoe oud de oudste back-up toen was.",
			"Alle nodes kwamen tegelijk terug uit hun nieuwste back-up, op "+esc(m.Host)+" ("+esc(m.Proxmox)+") en storage "+code(m.Storage)+
				", met de netwerkkaarten aan de afgesloten bridge "+code(m.Bridge)+".")
		var rows [][]string
		for _, n := range m.Nodes {
			rows = append(rows, []string{"**" + esc(n.Hostname) + "**", "VM " + strconv.Itoa(n.SourceVMID), o.timeText(n.BackupTime), durationOr(n.Restore), durationOr(n.Boot)})
		}
		if len(rows) > 0 {
			d.table([]string{"Node", "Bron", "Back-up van", "Terugzetten", "Opstarten"}, rows)
		}
		var vips []string
		for _, v := range m.VIPs {
			vips = append(vips, drVIPText(v))
		}
		d.p(vips...)
		if m.Result == "warning" {
			d.p("De repetitie gaf een waarschuwing: " + esc(m.Summary))
		}
	} else {
		text := "Er is nog geen DR-repetitie gedaan, dus de hersteltijd (RTO) en het gegevensverlies (RPO) van het hele cluster zijn niet gemeten."
		if dr.Last != nil {
			text = "Er is nog geen DR-repetitie geslaagd, dus de hersteltijd (RTO) van het hele cluster is niet gemeten."
		}
		d.p(text, drGoals(dr))
	}
	if l := dr.Last; l != nil && (dr.Measured == nil || l.ID != dr.Measured.ID) {
		d.p("De laatste repetitie, "+quoted(l.Name)+" op "+o.dateText(l.At)+", is "+or(resultText[l.Result], l.Result)+".", esc(l.Summary))
	}
	if len(dr.Scenarios) == 0 {
		d.p("Er is geen DR-repetitie ingesteld. Die voeg je toe op de pagina van het cluster, bij Rampoefeningen.")
		return
	}
	var lines []string
	for _, sc := range dr.Scenarios {
		when := "alleen met de hand"
		if t, ok := scheduleText[sc.Schedule]; ok {
			when = t + " in het testvenster (" + esc(c.Backup.Window) + ")"
			if sc.Schedule == "monthly" {
				when = t + " (" + esc(c.Backup.Window) + ")"
			}
		}
		lines = append(lines, quoted(sc.Name)+" op de afgesloten bridge "+code(sc.Bridge)+": "+when+".")
	}
	d.p("De DR-repetities van dit cluster:")
	d.bullets(lines)
}

// drGoals zegt welke doelen het cluster heeft.
func drGoals(dr DR) string {
	rto, rpo := dr.RTOMinutes > 0, dr.RPOHours > 0
	var s string
	switch {
	case !rto && !rpo:
		return "Dit cluster heeft geen doel voor de hersteltijd (RTO) en het gegevensverlies (RPO)."
	case !rpo:
		s = "Het doel is een RTO van hoogstens " + plural(dr.RTOMinutes, "minuut", "minuten") + "; voor de RPO is er geen doel"
	case !rto:
		s = "Het doel is een RPO van hoogstens " + plural(dr.RPOHours, "uur", "uur") + "; voor de RTO is er geen doel"
	default:
		s = "Het doel is een RTO van hoogstens " + plural(dr.RTOMinutes, "minuut", "minuten") + " en een RPO van hoogstens " + plural(dr.RPOHours, "uur", "uur")
	}
	if dr.Default {
		s += " (de standaard voor prod)"
	}
	return s + "."
}

func goalText(n int, one, many string) string {
	if n == 0 {
		return "geen doel"
	}
	return "hoogstens " + plural(n, one, many)
}

func metText(v *time.Duration, goal time.Duration) string {
	switch {
	case v == nil || goal == 0:
		return ""
	case *v <= goal:
		return "ja"
	}
	return "**nee**"
}

func durationOr(v *time.Duration) string {
	if v == nil {
		return "niet gemeten"
	}
	return duration(*v)
}

func quoted(s string) string { return "'" + esc(s) + "'" }

// drVIPText zegt wie het VIP in de sandbox had en of het antwoordde.
func drVIPText(v DRVIP) string {
	s := code(v.Address)
	if !v.Checked {
		return s + " is niet gecontroleerd: geen node in de sandbox kon zijn adressen melden."
	}
	var holders []string
	for _, h := range v.Holders {
		holders = append(holders, esc(h))
	}
	switch len(holders) {
	case 0:
		s += " had in de sandbox geen houder"
	case 1:
		s += " had in de sandbox één houder, " + holders[0]
	default:
		s += " had in de sandbox meer dan één houder: " + list(holders)
	}
	probe := ""
	if v.Probe != "" {
		probe = " (" + esc(v.Probe) + ")"
	}
	switch {
	case v.ProbeOK == nil:
	case *v.ProbeOK:
		s += ", en de probe" + probe + " slaagde"
	default:
		s += ", maar de probe" + probe + " faalde"
	}
	return s + "."
}

func measuredText(r *TestRun) string {
	var parts []string
	if r.TakeoverMS != nil {
		s := "overname in " + duration(time.Duration(*r.TakeoverMS)*time.Millisecond)
		if r.TakeoverNode != "" {
			s += " door " + esc(r.TakeoverNode)
		}
		parts = append(parts, s)
	}
	if r.DowntimeMS != nil {
		parts = append(parts, "onbereikbaar "+duration(time.Duration(*r.DowntimeMS)*time.Millisecond))
	}
	if r.FailbackMS != nil {
		s := "failback in " + duration(time.Duration(*r.FailbackMS)*time.Millisecond)
		if r.ReturnedTo != "" {
			s += " naar " + esc(r.ReturnedTo)
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return esc(r.Summary)
	}
	return strings.Join(parts, "; ")
}

func outageName(u Outage) string {
	if u.Kind == "host" {
		return "host " + esc(u.Name)
	}
	return "node " + esc(u.Name)
}

func outageText(u Outage) string {
	s := statusOf(u.Status)
	switch {
	case len(u.Nodes) == 1:
		s += ": " + esc(u.Nodes[0]) + " valt uit"
	case len(u.Nodes) > 1:
		var names []string
		for _, n := range u.Nodes {
			names = append(names, esc(n))
		}
		s += ": " + list(names) + " vallen uit"
	case u.Reason != "":
		s += ": " + esc(u.Reason)
	}
	if len(u.Restarted) > 0 {
		var names []string
		for _, n := range u.Restarted {
			names = append(names, esc(n))
		}
		s += ". Proxmox HA start " + list(names) + " op een andere host"
		if u.During != "" && u.During != u.Status {
			s += "; tot dan " + statusOf(u.During)
		}
	}
	return s
}

func outageOthers(u Outage) string {
	var out []string
	for _, o := range u.Others {
		out = append(out, esc(o))
	}
	if u.Self {
		out = append(out, "ClusterForge zelf")
	}
	return strings.Join(out, "; ")
}

// --- Runbooks ---

func runbooks(c *Cluster, o Opts) string {
	d := &doc{}
	header(d, c, KindRunbooks)
	d.p("Deze stappen volgen wat ClusterForge echt doet, met de namen en adressen van dit cluster. Waar gegevens ontbreken, staat dat erbij.")
	keepalived := len(c.VIPs) > 0

	d.h2("Onderhoud aan een node")
	if keepalived {
		d.steps([]string{
			"Open de node en kies **Onderhoud** bij Beheer, met een reden.",
			"ClusterForge zet keepalived uit en wacht tot " + vipList(c) + " bij een andere node staat, hoogstens twee minuten. Lukt dat niet, dan zet het keepalived weer aan en blijft de node actief.",
			"De node staat in onderhoud en telt niet mee voor de status van het cluster. Keepalived blijft uit, ook na een herstart. Nu mag je patchen of herstarten.",
			"Kies **Onderhoud beëindigen**. Keepalived komt terug zoals het stond en de node is weer actief.",
		})
		var rows [][]string
		for _, n := range c.Nodes {
			rows = append(rows, []string{"**" + esc(n.Hostname) + "**", takeoverText(c, n.Hostname)})
		}
		if len(rows) > 0 {
			d.table([]string{"Node", "Wie neemt het VIP over"}, rows)
		}
		d.p("Kan geen andere node het VIP overnemen (geen actieve, online node waarop keepalived draait), dan vraagt ClusterForge eerst of je toch door wilt.")
	} else {
		d.steps([]string{
			"Open de node en kies **Onderhoud** bij Beheer, met een reden.",
			"De node staat in onderhoud en telt niet mee voor de status van het cluster. Nu mag je patchen of herstarten.",
			"Kies **Onderhoud beëindigen** om hem weer actief te maken.",
		})
	}
	var noAgent []string
	for _, n := range c.Nodes {
		if !n.HasAgent {
			noAgent = append(noAgent, esc(n.Hostname))
		}
	}
	if len(noAgent) > 0 {
		d.p(list(noAgent) + " " + map[bool]string{true: "heeft", false: "hebben"}[len(noAgent) == 1] +
			" geen agent. Die zet je alleen in en uit onderhoud; de VIP's haal je dan zelf weg.")
	}

	d.h2("Een node herstarten")
	if keepalived {
		d.p("Kies **Herstarten** bij Beheer. ClusterForge haalt eerst de VIP's weg en zet de node in onderhoud, herstart hem, wacht tot hij terug is (hoogstens vijftien minuten), zet keepalived weer aan en maakt de node weer actief.")
	} else {
		d.p("Kies **Herstarten** bij Beheer. ClusterForge zet de node in onderhoud, herstart hem, wacht tot hij terug is (hoogstens vijftien minuten) en maakt hem weer actief.")
	}
	d.p("**Afsluiten** werkt zo, maar de node blijft uit en in onderhoud. Start hem in Proxmox of op de machine zelf en beëindig daarna het onderhoud.")

	if keepalived {
		d.h2("Het VIP met de hand verplaatsen")
		d.p("Met ClusterForge zet je de node die het VIP nu heeft in onderhoud, zoals hierboven. Zonder ClusterForge stop je keepalived op die node:")
		d.code("sh", "systemctl stop keepalived")
		for _, v := range c.VIPs {
			iface := or(v.Interface, "eth0")
			d.p("Kijk op de andere node of hij " + code(v.Address) + " heeft:")
			d.code("sh", "ip -brief address show dev "+iface+" | grep -F "+v.Address)
		}
		back := "Daarna start je keepalived weer."
		if c.Template != nil && c.Template.Failback && len(c.VIPs[0].Nodes) > 0 {
			back += " Het VIP keert dan terug naar " + esc(c.VIPs[0].Nodes[0]) + "."
		} else if c.Template != nil {
			back += " Het VIP blijft bij de node die het overnam."
		}
		d.p("Tot je keepalived weer start, telt het cluster een node minder.", back)
		d.code("sh", "systemctl start keepalived")
	}

	d.h2("Terugzetten uit back-up")
	d.p("Twijfel je aan een back-up, controleer hem dan eerst bij Back-ups met **Controleren**: dat zet hem terug in een afgesloten sandbox. " +
		"Na het terugzetten meldt de agent uit de back-up zich weer; kijk daarna bij de node naar Drift voor wat er sinds de back-up veranderde.")
	if len(c.Nodes) > 1 {
		whole := "Moet het hele cluster terug, voer dan de commando's van alle nodes tegelijk uit."
		if keepalived {
			whole += " Controleer daarna of elk VIP precies één houder heeft."
		}
		if m := c.DR.Measured; m != nil && m.RTO != nil {
			whole += " Zo deed de DR-repetitie het op " + o.dateText(m.At) + " in de sandbox: na " + duration(*m.RTO) + " werkte het hele cluster weer."
		} else {
			whole += " Hoe lang dat duurt, meet de DR-repetitie; voor dit cluster heeft die nog geen hersteltijd gemeten."
		}
		d.p(whole)
	}
	for _, n := range c.Nodes {
		d.h3(esc(n.Hostname))
		restoreSteps(d, c, n, o)
	}

	d.h2("Een node vervangen")
	var ways []string
	ways = append(ways, "Is de VM stuk en de back-up goed, zet hem dan terug zoals hierboven. De node houdt zijn naam, adres en aanmelding.")
	switch {
	case c.Template != nil && c.GitLinked && c.Network != nil && c.Network.Mode == "static":
		ways = append(ways, "Is er een nieuwe VM nodig, verhoog dan het aantal nodes in "+code(yamlName(c))+" in Git. "+
			"ClusterForge plant de nieuwe node, past keepalived op de andere aan en rolt hem uit na goedkeuren. Zet daarna de oude node in onderhoud en sluit hem af.")
	case c.Template != nil:
		ways = append(ways, "Een nieuwe node bijmaken kan alleen voor een cluster in Git met vaste adressen; voor dit cluster kan ClusterForge dat nog niet.")
	}
	ways = append(ways, "Een node uit een cluster halen kan ClusterForge nog niet: verwijder je hem in de inventory, dan noemt de specificatie hem nog.")
	if c.Template == nil {
		ways = ways[:1]
	}
	d.bullets(ways)

	hosts := hostOutages(c)
	if len(hosts) > 0 {
		d.h2("Bij uitval van een host")
		for _, u := range hosts {
			d.h3(esc(u.Name))
			var on []string
			for _, n := range c.Nodes {
				if n.Guest != nil && hostLabel(n.Guest) == u.Name {
					on = append(on, esc(n.Hostname)+" ("+strings.TrimSuffix(guestText(n.Guest), " op "+esc(n.Guest.Host))+")")
				}
			}
			d.p("Hier draait "+list(on)+".", "Valt "+esc(u.Name)+" uit, dan is "+esc(c.Name)+" "+outageText(u)+".")
			steps := []string{"Kijk bij Risico, tab Wat als, met deze host: daar staat wat er met de andere clusters gebeurt."}
			if len(u.Restarted) > 0 {
				steps = append(steps, "Proxmox HA start de VM's op een andere host. Controleer daarna of het cluster weer gezond is.")
			} else {
				steps = append(steps, "Zonder HA blijven de VM's uit tot de host terug is. Duurt dat te lang, zet ze dan terug uit de back-up op een andere host, met de storage van die host.")
			}
			steps = append(steps, "Is de host terug, start dan de VM's die uit staan en controleer of elk VIP precies één houder heeft.")
			d.steps(steps)
		}
	}
	return d.String()
}

func vipList(c *Cluster) string {
	var vips []string
	for _, v := range c.VIPs {
		vips = append(vips, code(v.Address))
	}
	return list(vips)
}

// takeoverText zegt welke node het VIP krijgt als hostname het afgeeft.
func takeoverText(c *Cluster, hostname string) string {
	var parts []string
	for _, v := range c.VIPs {
		holds := slices.Contains(v.Nodes, hostname)
		var next string
		for _, n := range v.Nodes {
			if n != hostname {
				next = n
				break
			}
		}
		switch {
		case len(v.Nodes) == 0:
			parts = append(parts, code(v.Address)+": onbekend, geen node meldt het in zijn keepalived-configuratie")
		case !holds:
			parts = append(parts, code(v.Address)+": deze node heeft het niet")
		case next == "":
			parts = append(parts, code(v.Address)+": geen andere node")
		default:
			parts = append(parts, code(v.Address)+" gaat naar "+esc(next))
		}
	}
	return strings.Join(parts, "; ")
}

// restoreSteps geeft de commando's om de VM van een node uit zijn nieuwste
// back-up terug te zetten, of zegt wat er ontbreekt.
func restoreSteps(d *doc, c *Cluster, n Node, o Opts) {
	g := n.Guest
	if g == nil {
		d.p(esc(n.Hostname) + " is niet aan een VM in Proxmox gekoppeld, dus ClusterForge kent geen back-up. Koppel hem bij Proxmox.")
		return
	}
	b := n.Backup
	if b == nil || b.Volid == "" {
		d.p("Van " + guestText(g) + " is geen back-up bekend. Zet de VM in een back-upjob in Proxmox; dan vult dit runbook zich vanzelf.")
		return
	}
	storage, note := restoreStorage(c, g)
	d.p("De nieuwste back-up is van " + o.timeText(b.Time) + ", op storage " + code(b.Storage) + ". Op " + esc(g.Host) +
		" (of een andere host van " + esc(g.Proxmox) + "):")
	id := strconv.Itoa(g.VMID)
	var cmds string
	if g.Type == "lxc" {
		cmds = "pct stop " + id + "\npct restore " + id + " " + b.Volid + " --force 1 --storage " + storage + "\npct start " + id
	} else {
		cmds = "qm stop " + id + "\nqmrestore " + b.Volid + " " + id + " --force 1 --storage " + storage + "\nqm start " + id
	}
	d.code("sh", cmds)
	d.p(code("--force 1")+" overschrijft de VM die er nu staat.", note, drNodeText(c, n, o))
}

// drNodeText zegt hoe lang terugzetten en opstarten van de node in de
// laatste gemeten DR-repetitie duurden.
func drNodeText(c *Cluster, n Node, o Opts) string {
	m := c.DR.Measured
	if m == nil {
		return ""
	}
	for _, dn := range m.Nodes {
		if dn.Hostname != n.Hostname || dn.Restore == nil {
			continue
		}
		s := "In de DR-repetitie van " + o.dateText(m.At) + " duurde terugzetten " + duration(*dn.Restore)
		if dn.Boot != nil {
			s += " en opstarten " + duration(*dn.Boot)
		}
		return s + "."
	}
	return ""
}

// restoreStorage is de storage waarop de schijven terugkomen: die van de
// VM nu, anders die uit de specificatie.
func restoreStorage(c *Cluster, g *Guest) (string, string) {
	for _, dk := range g.Disks {
		if dk.Storage != "" {
			return dk.Storage, ""
		}
	}
	if c.Network != nil && c.Network.Storage != "" {
		return c.Network.Storage, ""
	}
	why := "de configuratie van de VM is nog niet gelezen"
	if g.ConfigRead {
		why = "de VM heeft geen schijf op een storage"
	}
	return "<storage>", "Storage onbekend: " + why + ". Vervang " + code("<storage>") + " door de storage waar de schijven moeten komen."
}

func yamlName(c *Cluster) string {
	if c.YAML != nil {
		return c.YAML.Name
	}
	return c.Slug + "-cluster.yaml"
}

func hostLabel(g *Guest) string { return g.Host + " (" + g.Proxmox + ")" }

func hostOutages(c *Cluster) []Outage {
	var out []Outage
	for _, u := range c.Outages {
		if u.Kind == "host" {
			out = append(out, u)
		}
	}
	slices.SortStableFunc(out, func(a, b Outage) int { return cmp.Compare(a.Name, b.Name) })
	return out
}
