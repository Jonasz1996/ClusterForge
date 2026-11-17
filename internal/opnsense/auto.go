package opnsense

// Automatisch beheer (fase 40 M2): ClusterForge kent elke node, elk cluster
// en elk VIP en houdt daarmee aliassen, DNS-overrides (Unbound) en
// DHCP-reservaties (Kea of Dnsmasq) op de firewall bij. Per soort is de stand
// uit, voorbeeld (alleen voorstellen) of beheerd (ClusterForge past de
// voorstellen zelf toe). Toepassen gaat altijd via opnsense.change: herstelpunt,
// terugdraaitimer, proeven en de sync naar de backup.
//
// ClusterForge raakt alleen wat van hem is: aliassen met een naam die met
// cf_ begint en overrides en reservaties met een omschrijving die met cf:
// begint. Staat er al iets anders met dezelfde naam, hetzelfde MAC- of
// IP-adres, dan is dat een conflict: het blijft staan en de GUI toont het.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	ModeOff     = "uit"
	ModePreview = "voorbeeld"
	ModeManaged = "beheerd"

	autoMark        = "cf:"
	autoAliasPrefix = "cf_"

	// AutoEvery is hoe vaak de beheerde soorten worden bijgehouden.
	AutoEvery = 10 * time.Minute
	// autoRetry is hoe lang ClusterForge wacht na een mislukte automatische
	// wijziging voor hij het opnieuw probeert.
	autoRetry = 6 * time.Hour
	// maxAutoCalls past in één wijziging (protocol: 50 aanroepen).
	maxAutoCalls = 50
)

// AutoSettings is de stand per soort.
type AutoSettings struct {
	Aliases string `json:"aliases"`
	DNS     string `json:"dns"`
	DHCP    string `json:"dhcp"`
	// Domain is het domein van de DNS-overrides; leeg is lan.<hoofddomein>.
	Domain  string   `json:"domain"`
	Ignored []string `json:"ignored"`
}

func defaultAuto() AutoSettings {
	return AutoSettings{Aliases: ModePreview, DNS: ModePreview, DHCP: ModePreview, Ignored: []string{}}
}

func (a AutoSettings) mode(kind string) string {
	switch kind {
	case "alias":
		return a.Aliases
	case "dns":
		return a.DNS
	case "dhcp":
		return a.DHCP
	}
	return ModeOff
}

func (a AutoSettings) managed() bool {
	return a.Aliases == ModeManaged || a.DNS == ModeManaged || a.DHCP == ModeManaged
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (a *AutoSettings) validate() error {
	for _, m := range []string{a.Aliases, a.DNS, a.DHCP} {
		if m != ModeOff && m != ModePreview && m != ModeManaged {
			return errors.New("de stand is uit, voorbeeld of beheerd")
		}
	}
	a.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(a.Domain), "."))
	if a.Domain != "" && (len(a.Domain) > 200 || !domainRe.MatchString(a.Domain)) {
		return errors.New("ongeldig domein")
	}
	if a.Ignored == nil {
		a.Ignored = []string{}
	}
	if len(a.Ignored) > 1000 {
		return errors.New("te veel genegeerde voorstellen")
	}
	for _, k := range a.Ignored {
		if len(k) > 300 {
			return errors.New("ongeldige sleutel")
		}
	}
	return nil
}

// AutoItem is één voorstel.
type AutoItem struct {
	// Key is vast per object, zoals dns:node1.lan.x.be of dhcp:aa:bb:...
	Key  string `json:"key"`
	Kind string `json:"kind"`
	// Op is add, set, del of conflict.
	Op     string `json:"op"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Ignored: je negeerde dit voorstel; het wordt niet toegepast.
	Ignored bool `json:"ignored"`
	// Manual: wordt nooit vanzelf toegepast, ook niet in de stand beheerd
	// (een alias weghalen kan regels raken).
	Manual bool `json:"manual,omitempty"`
	call   *protocol.OPNCall
}

// AutoView is wat de tab Automatisch toont.
type AutoView struct {
	Cluster  string       `json:"cluster"`
	Firewall string       `json:"firewall"`
	Settings AutoSettings `json:"settings"`
	// Domain is het domein dat echt gebruikt wordt.
	Domain string     `json:"domain"`
	Items  []AutoItem `json:"items"`
	// InOrder telt per soort wat al klopt.
	InOrder map[string]int `json:"in_order"`
	Notes   []string       `json:"notes"`
	// Pending is het aantal voorstellen dat Toepassen zou doen.
	Pending   int        `json:"pending"`
	LastRun   *time.Time `json:"last_run,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	LastJob   *uuid.UUID `json:"last_job,omitempty"`
}

// --- Wat ClusterForge weet ------------------------------------------------

type autoHost struct{ Name, Cluster, IP, MAC string }

type autoVIP struct{ Cluster, IP string }

type autoInput struct {
	Hosts  []autoHost
	VIPs   []autoVIP
	Domain string
	// Nets zijn de netten op de interfaces van de firewall.
	Nets []*net.IPNet
}

// --- Wat er op de firewall staat --------------------------------------------

type haveObj struct {
	UUID string
	F    map[string]string
}

func (h haveObj) owned(descrKey string) bool {
	return strings.HasPrefix(h.F[descrKey], autoMark)
}

type keaSubnet struct {
	UUID string
	Net  *net.IPNet
}

type autoHave struct {
	AliasOK   bool
	Aliases   []haveObj
	DNSOK     bool
	DNS       []haveObj
	KeaOK     bool
	Kea       []haveObj
	Subnets   []keaSubnet
	DnsmasqOK bool
	Dnsmasq   []haveObj
	// Ranges zijn de beginadressen van de DHCP-bereiken van Dnsmasq.
	Ranges []net.IP
}

// --- Plan -------------------------------------------------------------------

var labelBad = regexp.MustCompile(`[^a-z0-9-]+`)

// dnsLabel maakt van een hostnaam of clusternaam één DNS-label.
func dnsLabel(s string) string {
	s, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(s)), ".")
	s = strings.Trim(labelBad.ReplaceAllString(s, "-"), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

var aliasBad = regexp.MustCompile(`[^a-z0-9_]+`)

// aliasName maakt een geldige aliasnaam (hoogstens 32 tekens) met cf_ ervoor.
func aliasName(s string) string {
	s = strings.Trim(aliasBad.ReplaceAllString(strings.ToLower(s), "_"), "_")
	n := autoAliasPrefix + s
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "_")
	}
	return n
}

// eqField vergelijkt een veld uit een zoekresultaat; OPNsense toont soms een
// uitleg achter de waarde ("A (IPv4 address)").
func eqField(have, want string) bool {
	have, want = strings.TrimSpace(have), strings.TrimSpace(want)
	return strings.EqualFold(have, want) || strings.HasPrefix(strings.ToLower(have), strings.ToLower(want)+" ")
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func normMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return ""
	}
	if hw.String() == "00:00:00:00:00:00" {
		return ""
	}
	return hw.String()
}

type autoPlan struct {
	items   []AutoItem
	inOrder map[string]int
	notes   []string
}

func (p *autoPlan) ok(kind string) { p.inOrder[kind]++ }

func (p *autoPlan) add(it AutoItem) { p.items = append(p.items, it) }

// planAuto is puur: wat moet er veranderen om de firewall te laten kloppen.
func planAuto(in autoInput, have autoHave, st AutoSettings) autoPlan {
	p := autoPlan{inOrder: map[string]int{}, notes: []string{}}
	hosts := slices.Clone(in.Hosts)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	vips := slices.Clone(in.VIPs)
	sort.Slice(vips, func(i, j int) bool {
		if vips[i].Cluster != vips[j].Cluster {
			return vips[i].Cluster < vips[j].Cluster
		}
		return vips[i].IP < vips[j].IP
	})
	if st.Aliases != ModeOff {
		if have.AliasOK {
			planAliases(&p, hosts, vips, have.Aliases)
		} else {
			p.notes = append(p.notes, "De aliassen van de firewall konden niet gelezen worden; aliassen overgeslagen.")
		}
	}
	if st.DNS != ModeOff {
		if have.DNSOK {
			planDNS(&p, hosts, vips, in.Domain, have.DNS)
		} else {
			p.notes = append(p.notes, "Unbound is niet gevonden; DNS-overrides overgeslagen.")
		}
	}
	if st.DHCP != ModeOff {
		if have.KeaOK || have.DnsmasqOK {
			planDHCP(&p, hosts, in, have)
		} else {
			p.notes = append(p.notes, "Kea en Dnsmasq zijn niet in gebruik; DHCP-reservaties overgeslagen (ISC DHCP volgt later).")
		}
	}
	ign := map[string]bool{}
	for _, k := range st.Ignored {
		ign[k] = true
	}
	for i := range p.items {
		p.items[i].Ignored = ign[p.items[i].Key]
	}
	order := map[string]int{"conflict": 0, "add": 1, "set": 2, "del": 3}
	sort.SliceStable(p.items, func(i, j int) bool {
		a, b := p.items[i], p.items[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if order[a.Op] != order[b.Op] {
			return order[a.Op] < order[b.Op]
		}
		return a.Key < b.Key
	})
	return p
}

type wantAlias struct {
	name, descr string
	content     []string
}

func planAliases(p *autoPlan, hosts []autoHost, vips []autoVIP, have []haveObj) {
	var want []wantAlias
	all := []string{}
	byCluster := map[string][]string{}
	for _, h := range hosts {
		all = append(all, h.IP)
		if h.Cluster != "" {
			byCluster[h.Cluster] = append(byCluster[h.Cluster], h.IP)
		}
	}
	vipIPs := []string{}
	for _, v := range vips {
		vipIPs = append(vipIPs, v.IP)
		byCluster[v.Cluster] = append(byCluster[v.Cluster], v.IP)
	}
	if len(all) > 0 {
		want = append(want, wantAlias{"cf_nodes", "cf: alle nodes met een agent (ClusterForge)", splitList(strings.Join(all, ","))})
	}
	if len(vipIPs) > 0 {
		want = append(want, wantAlias{"cf_vips", "cf: alle VIP's (ClusterForge)", splitList(strings.Join(vipIPs, ","))})
	}
	names := []string{}
	for c := range byCluster {
		names = append(names, c)
	}
	sort.Strings(names)
	taken := map[string]bool{"cf_nodes": true, "cf_vips": true}
	for _, c := range names {
		n := aliasName(c)
		if n == autoAliasPrefix {
			continue
		}
		for i := 2; taken[n]; i++ {
			n = aliasName(fmt.Sprintf("%s_%d", c, i))
		}
		taken[n] = true
		want = append(want, wantAlias{n, "cf: cluster " + c + " (ClusterForge)", splitList(strings.Join(byCluster[c], ","))})
	}
	byName := map[string]haveObj{}
	for _, h := range have {
		byName[strings.ToLower(h.F["name"])] = h
	}
	seen := map[string]bool{}
	for _, w := range want {
		seen[w.name] = true
		body := map[string]any{"alias": map[string]any{
			"enabled": "1", "name": w.name, "type": "host", "content": strings.Join(w.content, "\n"), "description": w.descr,
		}}
		key := "alias:" + w.name
		h, ok := byName[w.name]
		if !ok {
			p.add(AutoItem{Key: key, Kind: "alias", Op: "add", Title: "alias " + w.name + " aanmaken",
				Detail: strings.Join(w.content, ", "), call: &protocol.OPNCall{Path: "firewall/alias/addItem", Body: body}})
			continue
		}
		cur := splitList(h.F["content"])
		if eqField(h.F["type"], "host") && slices.Equal(cur, w.content) && h.F["description"] == w.descr && h.F["enabled"] != "0" {
			p.ok("alias")
			continue
		}
		p.add(AutoItem{Key: key, Kind: "alias", Op: "set", Title: "alias " + w.name + " bijwerken",
			Detail: listDiff(cur, w.content), call: &protocol.OPNCall{Path: "firewall/alias/setItem/" + h.UUID, Body: body}})
	}
	for _, h := range have {
		n := strings.ToLower(h.F["name"])
		if !strings.HasPrefix(n, autoAliasPrefix) || seen[n] || h.UUID == "" {
			continue
		}
		p.add(AutoItem{Key: "alias:" + n, Kind: "alias", Op: "del", Manual: true, Title: "alias " + n + " verwijderen",
			Detail: "Dit cluster bestaat niet meer in ClusterForge. Kijk eerst of geen regel de alias nog gebruikt.",
			call:   &protocol.OPNCall{Path: "firewall/alias/delItem/" + h.UUID}})
	}
}

// listDiff zegt wat er bij komt en wat weg gaat.
func listDiff(cur, want []string) string {
	var plus, minus []string
	for _, w := range want {
		if !slices.Contains(cur, w) {
			plus = append(plus, "+"+w)
		}
	}
	for _, c := range cur {
		if !slices.Contains(want, c) {
			minus = append(minus, "-"+c)
		}
	}
	return strings.Join(append(plus, minus...), " ")
}

type wantDNS struct{ label, ip, descr string }

func planDNS(p *autoPlan, hosts []autoHost, vips []autoVIP, domain string, have []haveObj) {
	var want []wantDNS
	used := map[string]bool{}
	dup := 0
	for _, h := range hosts {
		l := dnsLabel(h.Name)
		if l == "" {
			continue
		}
		if used[l] {
			dup++
			continue
		}
		used[l] = true
		want = append(want, wantDNS{l, h.IP, "cf: node " + h.Name})
	}
	n := map[string]int{}
	for _, v := range vips {
		l := dnsLabel(v.Cluster)
		if l == "" {
			continue
		}
		n[v.Cluster]++
		if n[v.Cluster] > 1 {
			l = fmt.Sprintf("%s-vip%d", l, n[v.Cluster])
		}
		if used[l] {
			l += "-vip"
		}
		if used[l] {
			dup++
			continue
		}
		used[l] = true
		want = append(want, wantDNS{l, v.IP, "cf: VIP van " + v.Cluster})
	}
	if dup > 0 {
		p.notes = append(p.notes, fmt.Sprintf("%d namen kwamen twee keer voor; alleen de eerste kreeg een DNS-override.", dup))
	}
	byName := map[string][]haveObj{}
	for _, h := range have {
		fq := strings.ToLower(h.F["hostname"] + "." + h.F["domain"])
		byName[fq] = append(byName[fq], h)
	}
	seen := map[string]bool{}
	for _, w := range want {
		fq := w.label + "." + domain
		seen[fq] = true
		rr := "A"
		if strings.Contains(w.ip, ":") {
			rr = "AAAA"
		}
		body := map[string]any{"host": map[string]any{
			"enabled": "1", "hostname": w.label, "domain": domain, "rr": rr, "server": w.ip, "description": w.descr,
		}}
		key := "dns:" + fq
		var mine *haveObj
		var other *haveObj
		for _, h := range byName[fq] {
			if h.owned("description") {
				if mine == nil {
					mine = &h
				}
			} else if other == nil {
				other = &h
			}
		}
		switch {
		case mine == nil && other != nil:
			p.add(AutoItem{Key: key, Kind: "dns", Op: "conflict", Title: fq + " bestaat al",
				Detail: fmt.Sprintf("Er staat al een override naar %s die niet van ClusterForge is; ClusterForge wil %s. Pas hem zelf aan of geef hem een omschrijving die met cf: begint.", other.F["server"], w.ip)})
		case mine == nil:
			p.add(AutoItem{Key: key, Kind: "dns", Op: "add", Title: fq + " → " + w.ip,
				call: &protocol.OPNCall{Path: "unbound/settings/addHostOverride", Body: body}})
		case eqField(mine.F["server"], w.ip) && eqField(mine.F["rr"], rr) && mine.F["description"] == w.descr && mine.F["enabled"] != "0":
			p.ok("dns")
		default:
			p.add(AutoItem{Key: key, Kind: "dns", Op: "set", Title: fq + " → " + w.ip,
				Detail: "nu " + mine.F["server"], call: &protocol.OPNCall{Path: "unbound/settings/setHostOverride/" + mine.UUID, Body: body}})
		}
	}
	for _, h := range have {
		fq := strings.ToLower(h.F["hostname"] + "." + h.F["domain"])
		if !h.owned("description") || seen[fq] || h.UUID == "" {
			continue
		}
		seen[fq] = true
		p.add(AutoItem{Key: "dns:" + fq, Kind: "dns", Op: "del", Title: fq + " verwijderen",
			Detail: "Deze node of dit VIP bestaat niet meer in ClusterForge.",
			call:   &protocol.OPNCall{Path: "unbound/settings/delHostOverride/" + h.UUID}})
	}
}

func planDHCP(p *autoPlan, hosts []autoHost, in autoInput, have autoHave) {
	noMAC, noNet := 0, 0
	seenMAC := map[string]bool{}
	keaMine, dmMine := map[string]bool{}, map[string]bool{}
	for _, h := range hosts {
		ip := net.ParseIP(h.IP).To4()
		if ip == nil {
			continue
		}
		mac := normMAC(h.MAC)
		if mac == "" {
			noMAC++
			continue
		}
		if seenMAC[mac] {
			continue
		}
		var fwNet *net.IPNet
		for _, n := range in.Nets {
			if n.Contains(ip) {
				fwNet = n
				break
			}
		}
		if fwNet == nil {
			noNet++
			continue
		}
		label := dnsLabel(h.Name)
		key := "dhcp:" + mac
		descr := "cf: node " + h.Name
		var sub *keaSubnet
		if have.KeaOK {
			for i := range have.Subnets {
				if have.Subnets[i].Net.Contains(ip) {
					sub = &have.Subnets[i]
					break
				}
			}
		}
		if sub != nil {
			seenMAC[mac] = true
			keaMine[mac] = true
			body := map[string]any{"reservation": map[string]any{
				"subnet": sub.UUID, "ip_address": h.IP, "hw_address": mac, "hostname": label, "description": descr,
			}}
			planReservation(p, have.Kea, key, mac, h.IP, "hw_address", "ip_address", "description",
				func(o haveObj) bool {
					return eqField(o.F["ip_address"], h.IP) && eqField(o.F["hostname"], label) && o.F["description"] == descr &&
						(o.F["subnet"] == sub.UUID || eqField(o.F["subnet"], sub.Net.String()))
				},
				h.Name+" → "+h.IP+" (Kea)", "kea/dhcpv4", "Reservation", body)
			continue
		}
		if have.DnsmasqOK && slices.ContainsFunc(have.Ranges, fwNet.Contains) {
			seenMAC[mac] = true
			dmMine[mac] = true
			body := map[string]any{"host": map[string]any{
				"host": label, "domain": in.Domain, "ip": h.IP, "hwaddr": mac, "descr": descr,
			}}
			planReservation(p, have.Dnsmasq, key, mac, h.IP, "hwaddr", "ip", "descr",
				func(o haveObj) bool {
					return eqField(o.F["ip"], h.IP) && eqField(o.F["host"], label) && o.F["descr"] == descr
				},
				h.Name+" → "+h.IP+" (Dnsmasq)", "dnsmasq/settings", "Host", body)
			continue
		}
		noNet++
	}
	for _, o := range have.Kea {
		mac := normMAC(o.F["hw_address"])
		if o.owned("description") && !keaMine[mac] && o.UUID != "" {
			p.add(AutoItem{Key: "dhcp:kea:" + o.UUID, Kind: "dhcp", Op: "del", Title: "reservatie " + o.F["ip_address"] + " verwijderen",
				Detail: "Deze node bestaat niet meer in ClusterForge of kreeg een ander adres.",
				call:   &protocol.OPNCall{Path: "kea/dhcpv4/delReservation/" + o.UUID}})
		}
	}
	for _, o := range have.Dnsmasq {
		mac := normMAC(firstOf(o.F["hwaddr"]))
		if o.owned("descr") && !dmMine[mac] && o.UUID != "" {
			p.add(AutoItem{Key: "dhcp:dnsmasq:" + o.UUID, Kind: "dhcp", Op: "del", Title: "reservatie " + o.F["ip"] + " verwijderen",
				Detail: "Deze node bestaat niet meer in ClusterForge of kreeg een ander adres.",
				call:   &protocol.OPNCall{Path: "dnsmasq/settings/delHost/" + o.UUID}})
		}
	}
	if noMAC > 0 {
		p.notes = append(p.notes, fmt.Sprintf("%d nodes zonder bekend MAC-adres kregen geen reservatie.", noMAC))
	}
	if noNet > 0 {
		p.notes = append(p.notes, fmt.Sprintf("%d nodes zitten niet in een net waar deze firewall DHCP geeft; geen reservatie.", noNet))
	}
}

func firstOf(s string) string {
	l := splitList(s)
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

func planReservation(p *autoPlan, have []haveObj, key, mac, ip, macKey, ipKey, descrKey string, same func(haveObj) bool, title, base, x string, body map[string]any) {
	var mine, other *haveObj
	for i := range have {
		o := have[i]
		hit := normMAC(firstOf(o.F[macKey])) == mac
		if o.owned(descrKey) {
			if hit && mine == nil {
				mine = &have[i]
			}
			continue
		}
		if (hit || eqField(o.F[ipKey], ip)) && other == nil {
			other = &have[i]
		}
	}
	switch {
	case other != nil:
		p.add(AutoItem{Key: key, Kind: "dhcp", Op: "conflict", Title: title,
			Detail: fmt.Sprintf("Er staat al een reservatie voor %s / %s die niet van ClusterForge is. Pas ze zelf aan of geef ze een omschrijving die met cf: begint.", other.F[macKey], other.F[ipKey])})
	case mine == nil:
		p.add(AutoItem{Key: key, Kind: "dhcp", Op: "add", Title: title, call: &protocol.OPNCall{Path: base + "/add" + x, Body: body}})
	case same(*mine):
		p.ok("dhcp")
	default:
		p.add(AutoItem{Key: key, Kind: "dhcp", Op: "set", Title: title, Detail: "nu " + mine.F[ipKey],
			call: &protocol.OPNCall{Path: base + "/set" + x + "/" + mine.UUID, Body: body}})
	}
}

// --- Lezen ------------------------------------------------------------------

func (s *Service) autoInput(ctx context.Context, c Cluster, domain string) (autoInput, error) {
	in := autoInput{Domain: domain}
	rows, err := s.pool.Query(ctx, `SELECT n.hostname, coalesce(c.name, ''), coalesce(host(n.primary_ip), ''),
			coalesce(f.facts ->> 'primary_address', ''), coalesce(f.facts -> 'interfaces', '[]'::jsonb)
		FROM nodes n
		LEFT JOIN clusters c ON c.id = n.cluster_id
		LEFT JOIN node_facts f ON f.node_id = n.id
		WHERE n.lifecycle <> 'decommissioned'
		  AND (f.facts IS NULL OR jsonb_typeof(f.facts -> 'opnsense') IS DISTINCT FROM 'object')`)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var h autoHost
		var primary string
		var ifs []byte
		if err := rows.Scan(&h.Name, &h.Cluster, &h.IP, &primary, &ifs); err != nil {
			rows.Close()
			return in, err
		}
		if h.IP == "" {
			h.IP, _, _ = strings.Cut(primary, "/")
		}
		if net.ParseIP(h.IP) == nil {
			continue
		}
		var list []protocol.Interface
		_ = json.Unmarshal(ifs, &list)
		h.MAC = macFor(list, h.IP)
		in.Hosts = append(in.Hosts, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	rows, err = s.pool.Query(ctx, `SELECT host(v.address), c.name FROM vips v JOIN clusters c ON c.id = v.cluster_id`)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var v autoVIP
		if rows.Scan(&v.IP, &v.Cluster) == nil {
			in.VIPs = append(in.VIPs, v)
		}
	}
	rows.Close()
	for _, n := range c.Nodes {
		if n.Facts == nil {
			continue
		}
		for _, i := range n.Facts.Interfaces {
			for _, a := range i.Addresses {
				if _, ipnet, err := net.ParseCIDR(a); err == nil &&
					!slices.ContainsFunc(in.Nets, func(x *net.IPNet) bool { return x.String() == ipnet.String() }) {
					in.Nets = append(in.Nets, ipnet)
				}
			}
		}
	}
	return in, nil
}

// macFor zoekt het MAC-adres van de interface met dit adres.
func macFor(list []protocol.Interface, ip string) string {
	for _, i := range list {
		for _, a := range i.Addresses {
			if addr, _, _ := strings.Cut(a, "/"); addr == ip {
				return i.MAC
			}
		}
	}
	return ""
}

// searchRows doet een zoekopdracht zonder cache en geeft de rijen als tekst.
func (s *Service) searchRows(ctx context.Context, id uuid.UUID, path string) ([]haveObj, error) {
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpPost, Path: path, Params: map[string]string{"rowCount": "-1"}}, false)
	if err != nil {
		return nil, err
	}
	return parseRows(data)
}

func parseRows(data json.RawMessage) ([]haveObj, error) {
	var res struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	out := make([]haveObj, 0, len(res.Rows))
	for _, r := range res.Rows {
		o := haveObj{F: map[string]string{}}
		for k, v := range r {
			switch x := v.(type) {
			case nil:
			case string:
				o.F[k] = x
			case float64, bool:
				o.F[k] = fmt.Sprint(x)
			case []any:
				parts := make([]string, 0, len(x))
				for _, e := range x {
					parts = append(parts, fmt.Sprint(e))
				}
				o.F[k] = strings.Join(parts, ",")
			}
		}
		o.UUID = o.F["uuid"]
		out = append(out, o)
	}
	return out, nil
}

func (s *Service) autoHave(ctx context.Context, id uuid.UUID, st AutoSettings) autoHave {
	var h autoHave
	var err error
	if st.Aliases != ModeOff {
		h.Aliases, err = s.searchRows(ctx, id, "firewall/alias/searchItem")
		h.AliasOK = err == nil
	}
	if st.DNS != ModeOff {
		h.DNS, err = s.searchRows(ctx, id, "unbound/settings/searchHostOverride")
		h.DNSOK = err == nil
	}
	if st.DHCP != ModeOff {
		if subs, err := s.searchRows(ctx, id, "kea/dhcpv4/searchSubnet"); err == nil {
			for _, o := range subs {
				if _, n, err := net.ParseCIDR(o.F["subnet"]); err == nil && o.UUID != "" {
					h.Subnets = append(h.Subnets, keaSubnet{UUID: o.UUID, Net: n})
				}
			}
			if len(h.Subnets) > 0 {
				h.Kea, err = s.searchRows(ctx, id, "kea/dhcpv4/searchReservation")
				h.KeaOK = err == nil
			}
		}
		if ranges, err := s.searchRows(ctx, id, "dnsmasq/settings/searchRange"); err == nil {
			for _, o := range ranges {
				if ip := net.ParseIP(o.F["start_addr"]); ip != nil {
					h.Ranges = append(h.Ranges, ip)
				}
			}
			if len(h.Ranges) > 0 {
				h.Dnsmasq, err = s.searchRows(ctx, id, "dnsmasq/settings/searchHost")
				h.DnsmasqOK = err == nil
			}
		}
	}
	return h
}

// --- Stand bewaren ----------------------------------------------------------

type autoRow struct {
	AutoSettings
	LastRun   *time.Time
	LastError string
	LastJob   *uuid.UUID
}

func (s *Service) autoRow(ctx context.Context, key string) (autoRow, error) {
	r := autoRow{AutoSettings: defaultAuto()}
	err := s.pool.QueryRow(ctx, `SELECT aliases, dns, dhcp, domain, ignored, last_run, last_error, last_job
		FROM opnsense_auto WHERE cluster_key = $1`, key).
		Scan(&r.Aliases, &r.DNS, &r.DHCP, &r.Domain, &r.Ignored, &r.LastRun, &r.LastError, &r.LastJob)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	return r, err
}

func (s *Service) autoDomain(ctx context.Context, st AutoSettings) string {
	if st.Domain != "" {
		return st.Domain
	}
	var d string
	if err := s.pool.QueryRow(ctx, `SELECT domain FROM publish_settings LIMIT 1`).Scan(&d); err != nil || d == "" {
		return "lan"
	}
	return "lan." + strings.ToLower(d)
}

// AutoChangesToManaged zegt of de nieuwe stand een soort op beheerd zet die
// dat nog niet was (op prod vraagt dat een code).
func (s *Service) AutoChangesToManaged(ctx context.Context, id uuid.UUID, in AutoSettings) (bool, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return false, err
	}
	cur, err := s.autoRow(ctx, c.Key)
	if err != nil {
		return false, err
	}
	for _, k := range []string{"alias", "dns", "dhcp"} {
		if in.mode(k) == ModeManaged && cur.mode(k) != ModeManaged {
			return true, nil
		}
	}
	return false, nil
}

// SetAuto bewaart de stand van een firewallcluster.
func (s *Service) SetAuto(ctx context.Context, actor events.Actor, user *uuid.UUID, id uuid.UUID, in AutoSettings) (AutoSettings, error) {
	if err := in.validate(); err != nil {
		return in, ValidationError{err.Error()}
	}
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return in, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO opnsense_auto (cluster_key, aliases, dns, dhcp, domain, ignored, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (cluster_key) DO UPDATE SET aliases = $2, dns = $3, dhcp = $4, domain = $5, ignored = $6,
			updated_by = $7, updated_at = now(), last_error = ''`,
		c.Key, in.Aliases, in.DNS, in.DHCP, in.Domain, in.Ignored, user)
	if err != nil {
		return in, err
	}
	s.event(ctx, actor, id, "opnsense.auto_updated", map[string]any{
		"cluster": c.Name, "aliases": in.Aliases, "dns": in.DNS, "dhcp": in.DHCP, "domain": in.Domain, "ignored": len(in.Ignored),
	})
	return in, nil
}

// readNode is de firewall waar ClusterForge leest en schrijft: de master,
// of de eerste die klaar is.
func readNodeOf(c Cluster) *Firewall {
	for i := range c.Nodes {
		if c.Master != nil && c.Nodes[i].NodeID == *c.Master {
			return &c.Nodes[i]
		}
	}
	for i := range c.Nodes {
		if c.Nodes[i].Why == "" {
			return &c.Nodes[i]
		}
	}
	return nil
}

// Auto geeft de voorstellen voor het cluster van deze firewall.
func (s *Service) Auto(ctx context.Context, id uuid.UUID) (AutoView, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return AutoView{}, err
	}
	v, _, err := s.autoView(ctx, c)
	return v, err
}

func (s *Service) autoView(ctx context.Context, c Cluster) (AutoView, autoRow, error) {
	row, err := s.autoRow(ctx, c.Key)
	if err != nil {
		return AutoView{}, row, err
	}
	v := AutoView{Cluster: c.Name, Settings: row.AutoSettings, Items: []AutoItem{}, InOrder: map[string]int{}, Notes: []string{},
		LastRun: row.LastRun, LastError: row.LastError, LastJob: row.LastJob}
	v.Domain = s.autoDomain(ctx, row.AutoSettings)
	fw := readNodeOf(c)
	if fw == nil {
		v.Notes = append(v.Notes, "Geen firewall van dit cluster is klaar om gelezen te worden.")
		return v, row, nil
	}
	v.Firewall = fw.Hostname
	in, err := s.autoInput(ctx, c, v.Domain)
	if err != nil {
		return v, row, err
	}
	p := planAuto(in, s.autoHave(ctx, fw.NodeID, row.AutoSettings), row.AutoSettings)
	v.Items, v.InOrder, v.Notes = p.items, p.inOrder, append(v.Notes, p.notes...)
	for _, it := range v.Items {
		if it.call != nil && !it.Ignored && !it.Manual {
			v.Pending++
		}
	}
	return v, row, nil
}

// pick kiest de aanroepen: de gevraagde sleutels, of anders alles wat niet
// genegeerd of handmatig is (en bij managedOnly alleen de beheerde soorten).
func pick(items []AutoItem, keys []string, st AutoSettings, managedOnly bool) ([]protocol.OPNCall, []string) {
	var calls []protocol.OPNCall
	var picked []string
	for _, it := range items {
		if it.call == nil {
			continue
		}
		if len(keys) > 0 {
			if !slices.Contains(keys, it.Key) {
				continue
			}
		} else if it.Ignored || it.Manual || (managedOnly && st.mode(it.Kind) != ModeManaged) {
			continue
		}
		if len(calls) == maxAutoCalls {
			break
		}
		calls = append(calls, *it.call)
		picked = append(picked, it.Key)
	}
	return calls, picked
}

// ApplyAuto past voorstellen toe als één wijziging (opnsense.change). Zonder
// sleutels: alles wat niet genegeerd of handmatig is, hoogstens 50.
func (s *Service) ApplyAuto(ctx context.Context, actor events.Actor, id uuid.UUID, keys []string) (store.Job, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	v, row, err := s.autoView(ctx, c)
	if err != nil {
		return store.Job{}, err
	}
	calls, picked := pick(v.Items, keys, row.AutoSettings, false)
	if len(calls) == 0 {
		return store.Job{}, ValidationError{"er is niets om toe te passen"}
	}
	return s.startAuto(ctx, actor, c, calls, picked)
}

func (s *Service) startAuto(ctx context.Context, actor events.Actor, c Cluster, calls []protocol.OPNCall, keys []string) (store.Job, error) {
	fw := readNodeOf(c)
	if fw == nil {
		return store.Job{}, ValidationError{"geen firewall van dit cluster is klaar"}
	}
	j, err := s.StartChange(ctx, actor, fw.NodeID, Change{Calls: calls})
	if err != nil {
		return j, err
	}
	_, _ = s.pool.Exec(ctx, `INSERT INTO opnsense_auto (cluster_key, last_job, last_run) VALUES ($1, $2, now())
		ON CONFLICT (cluster_key) DO UPDATE SET last_job = $2, last_run = now(), last_error = ''`, c.Key, j.ID)
	s.event(ctx, actor, fw.NodeID, "opnsense.auto_applied", map[string]any{"cluster": c.Name, "keys": keys, "job_id": j.ID.String()})
	return j, nil
}

// AutoLoop houdt de beheerde soorten bij.
func (s *Service) AutoLoop(ctx context.Context) {
	t := time.NewTicker(AutoEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.RunAuto(ctx)
	}
}

// RunAuto doet één ronde voor elk cluster met een beheerde soort.
func (s *Service) RunAuto(ctx context.Context) {
	cs, err := s.Clusters(ctx)
	if err != nil {
		s.log.Warn("automatisch OPNsense-beheer: clusters lezen mislukt", "err", err)
		return
	}
	for _, c := range cs {
		if err := s.runAutoOne(ctx, c); err != nil {
			s.log.Info("automatisch OPNsense-beheer", "cluster", c.Name, "err", err)
			_, _ = s.pool.Exec(ctx, `UPDATE opnsense_auto SET last_error = $2, last_run = now() WHERE cluster_key = $1`, c.Key, truncate(err.Error(), 500))
		}
	}
}

func (s *Service) runAutoOne(ctx context.Context, c Cluster) error {
	row, err := s.autoRow(ctx, c.Key)
	if err != nil || !row.managed() {
		return err
	}
	if row.LastJob != nil {
		var status string
		var finished *time.Time
		if err := s.pool.QueryRow(ctx, `SELECT status::text, finished_at FROM jobs WHERE id = $1`, *row.LastJob).Scan(&status, &finished); err == nil {
			switch {
			case status == "queued" || status == "running":
				return nil
			case status == "failed" && finished != nil && s.Now().Sub(*finished) < autoRetry:
				return nil
			}
		}
	}
	v, row, err := s.autoView(ctx, c)
	if err != nil {
		return err
	}
	calls, picked := pick(v.Items, nil, row.AutoSettings, true)
	if len(calls) == 0 {
		_, err := s.pool.Exec(ctx, `UPDATE opnsense_auto SET last_run = now(), last_error = '' WHERE cluster_key = $1`, c.Key)
		return err
	}
	_, err = s.startAuto(ctx, events.System(), c, calls, picked)
	return err
}
