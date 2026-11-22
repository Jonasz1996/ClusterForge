package journal

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// FactsChanges vergelijkt de vorige en de nieuwe facts van een node en
// geeft wat de server daaruit zelf waarneemt. Met agentJournal meldt de
// agent herstarts zelf (protocol 5); dan komt een herstart niet uit de
// facts. Regels zonder tijd krijgen van de aanroeper het moment van
// ontvangst.
func FactsChanges(old, cur protocol.Facts, agentJournal bool) []Entry {
	var out []Entry
	if !agentJournal && !old.BootTime.IsZero() && !cur.BootTime.IsZero() && absDur(cur.BootTime.Sub(old.BootTime)) > time.Minute {
		e := Entry{TS: cur.BootTime, Kind: KindBoot, Subject: "boot", Action: "boot", To: cur.Kernel}
		if old.Kernel != cur.Kernel {
			e.From = old.Kernel
		}
		out = append(out, e)
	}
	if old.OS.PrettyName != "" && cur.OS.PrettyName != "" && old.OS.PrettyName != cur.OS.PrettyName {
		out = append(out, Entry{Kind: KindFacts, Subject: "os", Action: "changed", From: old.OS.PrettyName, To: cur.OS.PrettyName})
	}
	if old.Upgrades != nil && cur.Upgrades != nil && old.Upgrades.Total != cur.Upgrades.Total {
		out = append(out, Entry{
			Kind: KindFacts, Subject: "updates", Action: "changed",
			From: strconv.Itoa(old.Upgrades.Total), To: strconv.Itoa(cur.Upgrades.Total),
			Detail: map[string]string{"security": strconv.Itoa(cur.Upgrades.Security)},
		})
	}
	return append(out, networkChanges(old, cur)...)
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// hasNetworkFacts is true voor facts van protocol 5: elke interface heeft
// dan een soort.
func hasNetworkFacts(f protocol.Facts) bool {
	return slices.ContainsFunc(f.Interfaces, func(i protocol.Interface) bool { return i.Kind != "" })
}

// ignoredInterface zijn interfaces die vanzelf komen en gaan, zoals de
// veth's van containers.
func ignoredInterface(i protocol.Interface) bool {
	return i.Kind == "veth" || strings.HasPrefix(i.Name, "veth")
}

func ignoredRouteIface(name string) bool {
	return strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-")
}

// networkChanges vergelijkt interfaces, gateway, DNS, routes, luisterende
// poorten en keepalived. De VIP's van keepalived tellen niet mee als adres:
// die verhuizen bij elke failover, en daar zijn al events voor.
func networkChanges(old, cur protocol.Facts) []Entry {
	var out []Entry
	net := func(subject, action string) Entry { return Entry{Kind: KindNetwork, Subject: subject, Action: action} }
	vips := map[string]bool{}
	for _, f := range []protocol.Facts{old, cur} {
		if f.Keepalived != nil {
			for _, v := range f.Keepalived.VIPs {
				vips[v] = true
			}
		}
	}
	addrs := func(i protocol.Interface) []string {
		var out []string
		for _, a := range i.Addresses {
			ip, _, _ := strings.Cut(a, "/")
			if !vips[ip] && !strings.HasPrefix(ip, "fe80:") {
				out = append(out, a)
			}
		}
		return out
	}
	oldIfs := map[string]protocol.Interface{}
	for _, i := range old.Interfaces {
		if !ignoredInterface(i) {
			oldIfs[i.Name] = i
		}
	}
	full := hasNetworkFacts(old) && hasNetworkFacts(cur)
	seen := map[string]bool{}
	for _, i := range cur.Interfaces {
		if ignoredInterface(i) {
			continue
		}
		seen[i.Name] = true
		o, ok := oldIfs[i.Name]
		if !ok {
			e := net(i.Name, "interface_added")
			e.To = strings.TrimSpace(i.Kind + " " + strings.Join(addrs(i), ", "))
			out = append(out, e)
			continue
		}
		if added, removed := diff(addrs(o), addrs(i)); len(added)+len(removed) > 0 {
			e := net(i.Name, "addresses")
			e.Detail = map[string]string{}
			if len(added) > 0 {
				e.Detail["added"] = joinMax(added)
			}
			if len(removed) > 0 {
				e.Detail["removed"] = joinMax(removed)
			}
			out = append(out, e)
		}
		if full && o.MTU != 0 && i.MTU != 0 && o.MTU != i.MTU {
			e := net(i.Name, "mtu")
			e.From, e.To = strconv.Itoa(o.MTU), strconv.Itoa(i.MTU)
			out = append(out, e)
		}
		if full && o.Master != i.Master {
			e := net(i.Name, "master")
			e.From, e.To = o.Master, i.Master
			out = append(out, e)
		}
	}
	for _, o := range old.Interfaces {
		if !ignoredInterface(o) && !seen[o.Name] {
			out = append(out, net(o.Name, "interface_removed"))
		}
	}
	if !full {
		return append(out, keepalivedChanges(old, cur)...)
	}
	if old.DefaultGateway != cur.DefaultGateway {
		e := net("gateway", "gateway")
		e.From, e.To = old.DefaultGateway, cur.DefaultGateway
		out = append(out, e)
	}
	if !slices.Equal(old.DNS, cur.DNS) {
		e := net("dns", "dns")
		e.From, e.To = strings.Join(old.DNS, ", "), strings.Join(cur.DNS, ", ")
		out = append(out, e)
	}
	routeKeys := func(f protocol.Facts) []string {
		var out []string
		for _, r := range f.Routes {
			if ignoredRouteIface(r.Interface) || r.Dest == "0.0.0.0/0" {
				continue
			}
			k := r.Dest
			if r.Gateway != "" {
				k += " via " + r.Gateway
			}
			out = append(out, k+" ("+r.Interface+")")
		}
		return out
	}
	if added, removed := diff(routeKeys(old), routeKeys(cur)); len(added)+len(removed) > 0 {
		out = append(out, listEntry(net("routes", "routes"), added, removed))
	}
	listenKeys := func(f protocol.Facts) []string {
		var out []string
		for _, l := range f.Listeners {
			k := l.Proto + " " + strconv.Itoa(l.Port)
			if l.Unit != "" {
				k += " (" + l.Unit + ")"
			}
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
		return out
	}
	if added, removed := diff(listenKeys(old), listenKeys(cur)); len(added)+len(removed) > 0 {
		out = append(out, listEntry(net("listeners", "listeners"), added, removed))
	}
	return append(out, keepalivedChanges(old, cur)...)
}

func listEntry(e Entry, added, removed []string) Entry {
	e.Detail = map[string]string{}
	if len(added) > 0 {
		e.Detail["added"] = joinMax(added)
	}
	if len(removed) > 0 {
		e.Detail["removed"] = joinMax(removed)
	}
	return e
}

// keepalivedChanges vergelijkt de vrrp_instance-blokken op naam.
func keepalivedChanges(old, cur protocol.Facts) []Entry {
	inst := func(f protocol.Facts) map[string]string {
		out := map[string]string{}
		if f.Keepalived == nil {
			return out
		}
		for _, i := range f.Keepalived.Instances {
			s := fmt.Sprintf("%s VRID %d", i.Interface, i.VRID)
			if i.State != "" {
				s += " " + strings.ToLower(i.State)
			}
			if i.Priority != 0 {
				s += fmt.Sprintf(" prioriteit %d", i.Priority)
			}
			if len(i.Peers) > 0 {
				s += " peers " + strings.Join(i.Peers, ", ")
			}
			if len(i.VIPs) > 0 {
				s += " VIP " + strings.Join(i.VIPs, ", ")
			}
			out[i.Name] = s
		}
		return out
	}
	o, n := inst(old), inst(cur)
	// Een agent op protocol 4 kent geen instanties; dan is er niets te
	// vergelijken.
	if old.Keepalived == nil || cur.Keepalived == nil || (len(o) == 0 && !hasNetworkFacts(old)) {
		return nil
	}
	var out []Entry
	names := map[string]bool{}
	for k := range o {
		names[k] = true
	}
	for k := range n {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	slices.Sort(sorted)
	for _, name := range sorted {
		if o[name] != n[name] {
			out = append(out, Entry{Kind: KindNetwork, Subject: name, Action: "keepalived", From: o[name], To: n[name]})
		}
	}
	return out
}

// diff geeft wat in b zit en niet in a, en omgekeerd, in de volgorde van
// de lijsten.
func diff(a, b []string) (added, removed []string) {
	for _, x := range b {
		if !slices.Contains(a, x) {
			added = append(added, x)
		}
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			removed = append(removed, x)
		}
	}
	return added, removed
}

func joinMax(s []string) string {
	const n = 10
	if len(s) > n {
		return strings.Join(s[:n], ", ") + fmt.Sprintf(" en %d meer", len(s)-n)
	}
	return strings.Join(s, ", ")
}

// category deelt de toestand van een unit in; een unit die start, telt
// niet als toestand.
func category(state string) string {
	switch state {
	case "active", "reloading":
		return "up"
	case "failed":
		return "failed"
	case "inactive", "deactivating":
		return "down"
	}
	return ""
}

// ServiceChanges vergelijkt de diensten uit twee heartbeats, voor een
// agent die zelf nog geen journaal stuurt.
func ServiceChanges(old, cur map[string]string) []Entry {
	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []Entry
	for _, name := range names {
		o, ok := old[name]
		n := cur[name]
		if !ok || o == n {
			continue
		}
		co, cn := category(o), category(n)
		if cn == "" || co == cn {
			continue
		}
		e := Entry{Kind: KindService, Subject: name}
		switch cn {
		case "up":
			e.Action = "started"
		case "failed":
			e.Action = "failed"
		case "down":
			e.Action = "stopped"
		}
		out = append(out, e)
	}
	return out
}
