package opnsense

// Regeladvies (fase 40 M3): voorstellen voor de firewallregels uit wat
// ClusterForge weet. Uit het filterlog: verkeer dat geblokkeerd werd naar een
// poort waarop een node echt luistert (dan stelt ClusterForge een regel voor
// die het toelaat). Uit de regels zelf: dubbele regels, regels die nooit
// raken omdat een eerdere regel alles al afvangt, regels zonder treffer in 30
// dagen en regels die alles van internet doorlaten.
//
// Regeladvies wordt nooit vanzelf toegepast: je kiest, tikt de naam van het
// cluster over en het gaat via opnsense.change met herstelpunt, timer en
// proeven.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// adviceMinHits: zo vaak moet een stroom in 7 dagen geblokkeerd zijn.
	adviceMinHits = 5
	// adviceManySources: vanaf zoveel bronnen op één interface wordt de bron
	// het net van die interface.
	adviceManySources = 3
)

// AdviceView is wat de tab Regeladvies toont.
type AdviceView struct {
	Cluster  string     `json:"cluster"`
	Firewall string     `json:"firewall"`
	Items    []AutoItem `json:"items"`
	Notes    []string   `json:"notes"`
	// Since is het oudste uur in het filterlog van ClusterForge.
	Since *time.Time `json:"since,omitempty"`
	// Confirm is wat je overtikt bij toepassen (regels raken de toegang).
	Confirm string `json:"confirm"`
}

type blockedFlow struct {
	Iface, Src, Dst, Proto string
	Port                   int
	Count                  int64
	SrcName, DstName       string
}

type adviceInput struct {
	Rules   []haveObj
	Blocked []blockedFlow
	// Listen is per adres de poorten waarop iets luistert ("tcp/22").
	Listen map[string][]string
	// Hits telt de treffers van 30 dagen per omschrijving van een regel.
	Hits map[string]int64
	// HitsKnown: het filterlog loopt al minstens 7 dagen.
	HitsKnown bool
	// Owner is per adres de naam van de node.
	Owner map[string]string
}

func ruleSeq(r haveObj) int {
	n, err := strconv.Atoi(r.F["sequence"])
	if err != nil {
		return 1 << 30
	}
	return n
}

func ruleName(r haveObj) string {
	if d := r.F["description"]; d != "" {
		return "“" + d + "”"
	}
	return "regel " + r.F["sequence"]
}

func anyNet(s string) bool { return s == "" || strings.EqualFold(s, "any") }

// covers: regel a vangt alles af wat regel b zou raken.
func covers(a, b haveObj) bool {
	fa, fb := a.F, b.F
	if fa["interface"] != fb["interface"] || orIn(fa["direction"]) != orIn(fb["direction"]) {
		return false
	}
	if fa["quick"] == "0" || fa["source_not"] == "1" || fa["destination_not"] == "1" || fb["source_not"] == "1" || fb["destination_not"] == "1" {
		return false
	}
	ipa, ipb := orDefault(fa["ipprotocol"], "inet"), orDefault(fb["ipprotocol"], "inet")
	if ipa != ipb && ipa != "inet46" {
		return false
	}
	pa, pb := strings.ToLower(orDefault(fa["protocol"], "any")), strings.ToLower(orDefault(fb["protocol"], "any"))
	if pa != "any" && pa != pb {
		return false
	}
	for _, k := range []string{"source_net", "destination_net"} {
		if !anyNet(fa[k]) && !strings.EqualFold(fa[k], fb[k]) {
			return false
		}
	}
	for _, k := range []string{"source_port", "destination_port"} {
		if fa[k] != "" && fa[k] != fb[k] {
			return false
		}
	}
	return true
}

func orIn(s string) string { return orDefault(s, "in") }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func sameMatch(a, b haveObj) bool { return covers(a, b) && covers(b, a) }

// planAdvice is puur: de voorstellen uit regels, filterlog en luisterende
// poorten.
func planAdvice(in adviceInput) []AutoItem {
	var out []AutoItem
	rules := slices.Clone(in.Rules)
	sort.SliceStable(rules, func(i, j int) bool { return ruleSeq(rules[i]) < ruleSeq(rules[j]) })
	var on []haveObj
	for _, r := range rules {
		if r.F["enabled"] != "0" && r.UUID != "" {
			on = append(on, r)
		}
	}

	// Dubbel of overschaduwd: een eerdere regel vangt alles al af.
	hidden := map[string]bool{}
	for j, b := range on {
		for _, a := range on[:j] {
			if hidden[a.UUID] || !covers(a, b) {
				continue
			}
			hidden[b.UUID] = true
			if sameMatch(a, b) && a.F["action"] == b.F["action"] {
				out = append(out, AutoItem{Key: "dup:" + b.UUID, Kind: "dup", Op: "del", Manual: true,
					Title:  "dubbele regel " + ruleName(b) + " verwijderen",
					Detail: fmt.Sprintf("Op %s doet %s al precies hetzelfde.", b.F["interface"], ruleName(a)),
					call:   &protocol.OPNCall{Path: "firewall/filter/delRule/" + b.UUID}})
			} else {
				out = append(out, AutoItem{Key: "shadow:" + b.UUID, Kind: "shadow", Op: "set", Manual: true,
					Title: "regel " + ruleName(b) + " uitzetten",
					Detail: fmt.Sprintf("Deze regel raakt nooit: %s (%s) staat ervoor en vangt al dit verkeer af. Klopt de volgorde niet, verplaats hem dan in Regels en aliassen.",
						ruleName(a), a.F["action"]),
					call: &protocol.OPNCall{Path: "firewall/filter/toggleRule/" + b.UUID + "/0"}})
			}
			break
		}
	}

	// Alles van internet door.
	for _, r := range on {
		if r.F["action"] == "pass" && strings.Contains(strings.ToLower(r.F["interface"]), "wan") && anyNet(r.F["source_net"]) &&
			(anyNet(r.F["destination_net"]) || r.F["destination_port"] == "") && r.F["source_not"] != "1" {
			out = append(out, AutoItem{Key: "wan:" + r.UUID, Kind: "wan", Op: "conflict",
				Title:  "regel " + ruleName(r) + " laat veel van internet door",
				Detail: "Bron is any op de WAN, zonder vast doel of zonder poort. Beperk de bron of het doel, of zet er een poort op."})
		}
	}

	// Geen treffer in 30 dagen (alleen regels die loggen).
	if in.HitsKnown {
		for _, r := range on {
			d := r.F["description"]
			if d == "" || r.F["log"] != "1" || hidden[r.UUID] || in.Hits[d] > 0 {
				continue
			}
			out = append(out, AutoItem{Key: "unused:" + r.UUID, Kind: "unused", Op: "set", Manual: true,
				Title:  "regel " + ruleName(r) + " uitzetten",
				Detail: "Deze regel logt, maar raakte in 30 dagen niets. Uitzetten kan altijd terug.",
				call:   &protocol.OPNCall{Path: "firewall/filter/toggleRule/" + r.UUID + "/0"}})
		}
	}

	// Geblokkeerd naar een poort waarop een node luistert.
	type group struct {
		iface, dst, proto string
		port              int
		srcs              []blockedFlow
		count             int64
	}
	groups := map[string]*group{}
	var keys []string
	for _, f := range in.Blocked {
		if f.Port == 0 || f.Count < adviceMinHits || strings.Contains(strings.ToLower(f.Iface), "wan") || f.Iface == "" {
			continue
		}
		if !slices.Contains(in.Listen[f.Dst], f.Proto+"/"+strconv.Itoa(f.Port)) {
			continue
		}
		k := f.Iface + "|" + f.Dst + "|" + f.Proto + "|" + strconv.Itoa(f.Port)
		g := groups[k]
		if g == nil {
			g = &group{iface: f.Iface, dst: f.Dst, proto: f.Proto, port: f.Port}
			groups[k] = g
			keys = append(keys, k)
		}
		g.srcs = append(g.srcs, f)
		g.count += f.Count
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		dstName := orDefault(in.Owner[g.dst], g.dst)
		src, srcName := g.srcs[0].Src, orDefault(g.srcs[0].SrcName, g.srcs[0].Src)
		if len(g.srcs) >= adviceManySources {
			src, srcName = g.iface, "het net van "+g.iface
		}
		ipv := "inet"
		if strings.Contains(g.dst, ":") {
			ipv = "inet6"
		}
		descr := truncate(fmt.Sprintf("cf: %s naar %s %d/%s", srcName, dstName, g.port, g.proto), 200)
		body := map[string]any{"rule": map[string]any{
			"enabled": "1", "action": "pass", "quick": "1", "interface": g.iface, "direction": "in", "ipprotocol": ipv,
			"protocol": strings.ToUpper(g.proto), "source_net": src, "destination_net": g.dst,
			"destination_port": strconv.Itoa(g.port), "log": "1", "description": descr,
		}}
		// Een blokkeerregel die dit verkeer al afvangt moet na de nieuwe regel
		// komen: de nieuwe regel krijgt een lager volgnummer.
		cand := haveObj{F: map[string]string{}}
		for k, v := range body["rule"].(map[string]any) {
			cand.F[k] = v.(string)
		}
		for _, r := range on {
			if r.F["action"] != "pass" && covers(r, cand) {
				seq := max(ruleSeq(r)-1, 1)
				body["rule"].(map[string]any)["sequence"] = strconv.Itoa(seq)
				break
			}
		}
		out = append(out, AutoItem{Key: "open:" + k, Kind: "open", Op: "add",
			Title: fmt.Sprintf("%s → %s %d/%s toelaten", srcName, dstName, g.port, g.proto),
			Detail: fmt.Sprintf("%d keer geblokkeerd in 7 dagen op %s, en %s luistert op die poort. Alleen toepassen als dit verkeer mag.",
				g.count, g.iface, dstName),
			call: &protocol.OPNCall{Path: "firewall/filter/addRule", Body: body}})
	}

	order := map[string]int{"wan": 0, "open": 1, "shadow": 2, "dup": 3, "unused": 4, "vip": 5}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Kind] < order[out[j].Kind] })
	return out
}

// --- Lezen ------------------------------------------------------------------

func listenKey(proto string, port int) string {
	return strings.ToLower(proto) + "/" + strconv.Itoa(port)
}

func wildcard(addr string) bool {
	return addr == "" || addr == "*" || addr == "0.0.0.0" || addr == "::" || addr == "[::]"
}

func (s *Service) adviceInput(ctx context.Context, c Cluster, fw Firewall) (adviceInput, []string, *time.Time, error) {
	in := adviceInput{Listen: map[string][]string{}, Hits: map[string]int64{}, Owner: map[string]string{}}
	var notes []string
	rules, err := s.searchRows(ctx, fw.NodeID, "firewall/filter/searchRule")
	if err != nil {
		notes = append(notes, "De regels konden niet gelezen worden ("+err.Error()+"); alleen voorstellen uit het filterlog.")
	}
	in.Rules = rules

	// Luisterende poorten per adres.
	rows, err := s.pool.Query(ctx, `SELECT n.hostname, coalesce(f.facts -> 'listeners', '[]'::jsonb), coalesce(f.facts -> 'interfaces', '[]'::jsonb)
		FROM nodes n JOIN node_facts f ON f.node_id = n.id
		WHERE n.lifecycle <> 'decommissioned' AND jsonb_typeof(f.facts -> 'opnsense') IS DISTINCT FROM 'object'`)
	if err != nil {
		return in, notes, nil, err
	}
	for rows.Next() {
		var host string
		var lb, ib []byte
		if rows.Scan(&host, &lb, &ib) != nil {
			continue
		}
		var ls []protocol.Listener
		var ifs []protocol.Interface
		_ = json.Unmarshal(lb, &ls)
		_ = json.Unmarshal(ib, &ifs)
		for _, i := range ifs {
			for _, a := range i.Addresses {
				ip, _, _ := strings.Cut(a, "/")
				if net.ParseIP(ip) == nil || net.ParseIP(ip).IsLoopback() {
					continue
				}
				in.Owner[ip] = host
				for _, l := range ls {
					if wildcard(l.Addr) || l.Addr == ip {
						in.Listen[ip] = append(in.Listen[ip], listenKey(l.Proto, l.Port))
					}
				}
			}
		}
	}
	rows.Close()

	ids := make([]uuid.UUID, 0, len(c.Nodes))
	devs := map[string]string{}
	for _, n := range c.Nodes {
		ids = append(ids, n.NodeID)
		if n.Facts != nil {
			for _, i := range n.Facts.Interfaces {
				if i.Device != "" {
					devs[i.Device] = i.Name
				}
			}
		}
	}
	var since *time.Time
	_ = s.pool.QueryRow(ctx, `SELECT min(hour) FROM opnsense_flows WHERE node_id = ANY($1)`, ids).Scan(&since)
	in.HitsKnown = since != nil && s.Now().Sub(*since) >= 7*24*time.Hour
	if since == nil {
		notes = append(notes, "Er is nog geen filterlog gelezen; voorstellen uit het verkeer volgen zodra het binnenkomt (agent met protocol 56 nodig).")
	} else if !in.HitsKnown {
		notes = append(notes, "Het filterlog loopt nog geen 7 dagen; regels zonder treffers worden pas daarna gemeld.")
	}

	book := s.names(ctx, c)
	rows, err = s.pool.Query(ctx, `SELECT iface, src, dst, proto, port, sum(count)::bigint
		FROM opnsense_flows WHERE node_id = ANY($1) AND hour >= $2 AND action IN ('block', 'reject') AND port > 0
		GROUP BY iface, src, dst, proto, port HAVING sum(count) >= $3 ORDER BY 6 DESC LIMIT 2000`,
		ids, s.Now().Add(-7*24*time.Hour), adviceMinHits)
	if err != nil {
		return in, notes, since, err
	}
	for rows.Next() {
		var f blockedFlow
		if rows.Scan(&f.Iface, &f.Src, &f.Dst, &f.Proto, &f.Port, &f.Count) != nil {
			continue
		}
		if name, ok := devs[f.Iface]; ok {
			f.Iface = name
		}
		f.SrcName, f.DstName = book.name(f.Src), book.name(f.Dst)
		in.Blocked = append(in.Blocked, f)
	}
	rows.Close()

	if in.HitsKnown {
		names := s.ruleNames(ctx, c)
		rows, err = s.pool.Query(ctx, `SELECT rule, sum(count)::bigint FROM opnsense_flows WHERE node_id = ANY($1) AND hour >= $2 GROUP BY rule`,
			ids, s.Now().Add(-FlowsKeep))
		if err != nil {
			return in, notes, since, err
		}
		for rows.Next() {
			var label string
			var n int64
			if rows.Scan(&label, &n) == nil {
				if d := names[label]; d != "" {
					in.Hits[d] += n
				}
			}
		}
		rows.Close()
		if len(names) == 0 {
			in.HitsKnown = false
		}
	}
	return in, notes, since, nil
}

// Advice geeft het regeladvies voor het cluster van deze firewall.
func (s *Service) Advice(ctx context.Context, id uuid.UUID) (AdviceView, error) {
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return AdviceView{}, err
	}
	return s.advice(ctx, c)
}

func (s *Service) advice(ctx context.Context, c Cluster) (AdviceView, error) {
	v := AdviceView{Cluster: c.Name, Items: []AutoItem{}, Notes: []string{}, Confirm: c.Name}
	fw := readNodeOf(c)
	if fw == nil {
		v.Notes = append(v.Notes, "Geen firewall van dit cluster is klaar om gelezen te worden.")
		return v, nil
	}
	v.Firewall = fw.Hostname
	in, notes, since, err := s.adviceInput(ctx, c, *fw)
	if err != nil {
		return v, err
	}
	v.Notes, v.Since = append(v.Notes, notes...), since
	var ignored []string
	_ = s.pool.QueryRow(ctx, `SELECT advice_ignored FROM opnsense_auto WHERE cluster_key = $1`, c.Key).Scan(&ignored)
	v.Items = planAdvice(in)
	vip, note := s.vipAdvice(ctx, *fw, in.Rules)
	v.Items = append(v.Items, vip...)
	if note != "" {
		v.Notes = append(v.Notes, note)
	}
	for i := range v.Items {
		v.Items[i].Ignored = slices.Contains(ignored, v.Items[i].Key)
	}
	return v, nil
}

// IgnoreAdvice zet een voorstel op negeren of haalt dat weg.
func (s *Service) IgnoreAdvice(ctx context.Context, actor events.Actor, id uuid.UUID, key string, ignore bool) error {
	if key == "" || len(key) > 300 {
		return ValidationError{"ongeldige sleutel"}
	}
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return err
	}
	if ignore {
		_, err = s.pool.Exec(ctx, `INSERT INTO opnsense_auto (cluster_key, advice_ignored) VALUES ($1, ARRAY[$2])
			ON CONFLICT (cluster_key) DO UPDATE SET advice_ignored = array_append(array_remove(opnsense_auto.advice_ignored, $2), $2)`, c.Key, key)
	} else {
		_, err = s.pool.Exec(ctx, `UPDATE opnsense_auto SET advice_ignored = array_remove(advice_ignored, $2) WHERE cluster_key = $1`, c.Key, key)
	}
	if err == nil {
		s.event(ctx, actor, id, "opnsense.advice_ignored", map[string]any{"cluster": c.Name, "key": key, "ignore": ignore})
	}
	return err
}

// ApplyAdvice past de gekozen voorstellen toe; regels raken de toegang, dus
// je tikt de naam van het cluster over.
func (s *Service) ApplyAdvice(ctx context.Context, actor events.Actor, id uuid.UUID, keys []string, confirm string) (store.Job, error) {
	if len(keys) == 0 {
		return store.Job{}, ValidationError{"kies minstens één voorstel"}
	}
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	v, err := s.advice(ctx, c)
	if err != nil {
		return store.Job{}, err
	}
	calls, picked := pick(v.Items, keys, AutoSettings{}, false)
	if len(calls) == 0 {
		return store.Job{}, ValidationError{"deze voorstellen gelden niet meer; laad de pagina opnieuw"}
	}
	fw := readNodeOf(c)
	if fw == nil {
		return store.Job{}, ValidationError{"geen firewall van dit cluster is klaar"}
	}
	j, err := s.StartChange(ctx, actor, fw.NodeID, Change{Calls: calls, Confirm: confirm})
	if err != nil {
		return j, err
	}
	s.event(ctx, actor, fw.NodeID, "opnsense.advice_applied", map[string]any{"cluster": c.Name, "keys": picked, "job_id": j.ID.String()})
	return j, nil
}
