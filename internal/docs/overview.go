package docs

import (
	"strconv"
	"strings"
)

// Overview schrijft het overzicht: de clusters, hoe ze van elkaar afhangen,
// de Proxmox-hosts, de single points of failure en de open bevindingen.
func Overview(f Fleet, o Opts) string {
	d := &doc{}
	d.h1("ClusterForge: overzicht")
	d.p("Deze documentatie schrijft ClusterForge zelf, uit wat er draait. Wijzig de bestanden niet met de hand: de volgende versie overschrijft ze.")

	d.h2("Clusters")
	if len(f.Clusters) == 0 {
		d.p("Er zijn nog geen clusters.")
	} else {
		rows := make([][]string, 0, len(f.Clusters))
		for _, c := range f.Clusters {
			grade := ""
			if c.Grade != "" {
				grade = c.Grade
				if c.Score != nil {
					grade += " (" + strconv.Itoa(*c.Score) + ")"
				}
			}
			tpl := ""
			if t := c.Template; t != nil {
				tpl = code(t.Name) + " " + esc(t.Version)
			}
			var chapters []string
			for _, k := range Kinds(c.Type)[1:] {
				chapters = append(chapters, link(lower(Title(k)), Path(ScopeCluster, k, c.Slug)))
			}
			rows = append(rows, []string{
				"**" + link(c.Name, Path(ScopeCluster, KindArchitecture, c.Slug)) + "**", envOf(c.Env), statusOf(c.Status), grade,
				strconv.Itoa(len(c.Nodes)), tpl, strings.Join(chapters, ", "),
			})
		}
		d.table([]string{"Cluster", "Omgeving", "Status", "Cijfer", "Nodes", "Template", "Meer"}, rows)
	}

	d.h2("Afhankelijkheden tussen clusters")
	if len(f.Links) == 0 {
		d.p("Er zijn geen bevestigde afhankelijkheden tussen clusters.")
	} else {
		d.code("mermaid", linkFlow(f.Links).String())
		var items []string
		for _, l := range f.Links {
			s := linkEnd(l.From) + " hangt af van " + linkEnd(l.To)
			var names []string
			for _, n := range l.Services {
				names = append(names, esc(n))
			}
			s += ": " + list(names)
			if !l.Hard {
				s += " (zacht)"
			}
			items = append(items, s+".")
		}
		d.bullets(items)
	}

	d.h2("Proxmox")
	if len(f.Proxmox) == 0 {
		d.p("Er is geen Proxmox gekoppeld.")
	}
	for _, p := range f.Proxmox {
		d.h3(esc(p.Name))
		if len(p.Hosts) == 0 {
			d.p("ClusterForge heeft nog geen hosts van deze koppeling gelezen.")
			continue
		}
		rows := make([][]string, 0, len(p.Hosts))
		for _, h := range p.Hosts {
			var st []string
			for _, s := range h.Storages {
				t := code(s.Name) + " (" + esc(s.Type)
				if s.Shared {
					t += ", gedeeld"
				}
				if !s.Active {
					t += ", niet actief"
				}
				st = append(st, t+")")
			}
			var br []string
			for _, b := range h.Bridges {
				t := code(b.Name)
				var extra []string
				if len(b.Ports) > 0 {
					extra = append(extra, esc(strings.Join(b.Ports, ", ")))
				} else {
					extra = append(extra, "zonder poorten")
				}
				if b.CIDR != "" {
					extra = append(extra, code(b.CIDR))
				}
				if b.VLANAware {
					extra = append(extra, "VLAN-aware")
				}
				br = append(br, t+" ("+strings.Join(extra, ", ")+")")
			}
			online := "ja"
			if !h.Online {
				online = "nee"
			}
			mem := ""
			if h.MaxMem > 0 {
				mem = size(h.MaxMem)
			}
			rows = append(rows, []string{"**" + esc(h.Name) + "**", online, mem, strings.Join(st, ", "), strings.Join(br, ", "), strconv.Itoa(h.Guests)})
		}
		d.table([]string{"Host", "Online", "Geheugen", "Storage", "Bridges", "VM's"}, rows)
	}

	d.h2("Single points of failure")
	if len(f.SPOFs) == 0 {
		d.p("Geen enkel onderdeel maakt op zich een prod-cluster of een bevestigde prod-dienst down.")
	} else {
		d.p("Onderdelen waarvan de uitval alleen al een prod-cluster of een bevestigde prod-dienst down maakt, ook nadat Proxmox HA herstart wat het kan.")
		rows := make([][]string, 0, len(f.SPOFs))
		for _, s := range f.SPOFs {
			var down []string
			for _, x := range s.Down {
				down = append(down, esc(x))
			}
			if s.Self {
				down = append(down, "ClusterForge zelf")
			}
			what := spofKinds[s.Kind] + " " + esc(s.Name)
			if s.Group != "" {
				what += " (" + esc(s.Group) + ")"
			}
			rows = append(rows, []string{what, strings.Join(down, "; ")})
		}
		d.table([]string{"Onderdeel", "Wat er down gaat"}, rows)
	}

	d.h2("Open bevindingen")
	if len(f.Findings) == 0 {
		d.p("De health score heeft geen open bevindingen.")
	} else {
		rows := make([][]string, 0, len(f.Findings))
		for _, fd := range f.Findings {
			rows = append(rows, []string{esc(or(fd.Where, "ClusterForge")), esc(fd.Text), strconv.Itoa(fd.Points)})
		}
		d.table([]string{"Waar", "Bevinding", "Punten"}, rows)
	}
	return d.String()
}

var spofKinds = map[string]string{"host": "host", "storage": "storage", "bridge": "bridge", "node": "node"}

func linkEnd(e LinkEnd) string {
	if e.External {
		return esc(e.Name) + " (extern)"
	}
	return link(e.Name, Path(ScopeCluster, KindArchitecture, e.Slug))
}

// linkFlow tekent de clusters en externe diensten met een pijl per paar.
func linkFlow(links []Link) *flow {
	f := newFlow("LR")
	end := func(e LinkEnd) string {
		if e.External {
			return f.node("x:"+e.Name, nil, "round", e.Name, "extern")
		}
		return f.node("c:"+e.Slug, nil, "box", e.Name, envOf(e.Env))
	}
	for _, l := range links {
		arrow := "-->"
		if !l.Hard {
			arrow = "-.->"
		}
		text := ""
		if l.Count > 1 {
			text = strconv.Itoa(l.Count) + " pijlen"
		}
		f.edge(end(l.From), end(l.To), arrow, text)
	}
	return f
}
