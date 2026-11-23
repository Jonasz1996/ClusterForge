package opnsense

// Voorstellen naar het VIP (fase 48, B4.12, B5.6). Na een uitrol, overname
// of verhuizing wijzen port forwards (NAT) en regels vaak nog naar het oude
// adres of naar één node. ClusterForge kent de oude adressen (tabel
// replacements) en de node-IP's van elk cluster met een VIP, en stelt voor:
//
//   - een port forward naar zo'n adres: het doel naar het VIP zetten;
//   - een pass-regel naar zo'n adres met een poort: dezelfde regel naar het
//     VIP erbij (de oude blijft tot je hem zelf weghaalt), tenzij er al een
//     regel naar het VIP voor die poort is.
//
// Nooit vanzelf: het zijn voorstellen in Regeladvies, met de wijziging als
// voorbeeld, die je kiest en met de naam van het cluster bevestigt. Ze
// lopen dan via opnsense.change (herstelpunt, timer, proeven).

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// vipMove is een adres dat nu door een VIP bediend wordt.
type vipMove struct {
	Old, VIP, Cluster, Why string
}

// vipMoves leest de oude adressen: eerst de vervangen installaties, dan de
// node-IP's van clusters met een VIP.
func (s *Service) vipMoves(ctx context.Context) map[string]vipMove {
	out := map[string]vipMove{}
	rows, err := s.pool.Query(ctx, `SELECT host(r.old_address), coalesce(host(r.vip), (SELECT host(v.address) FROM vips v WHERE v.cluster_id = r.cluster_id ORDER BY v.created_at LIMIT 1), ''),
		c.name, coalesce(nullif(r.old_hostname, ''), host(r.old_address))
		FROM replacements r JOIN clusters c ON c.id = r.cluster_id WHERE r.state IN ('actief', 'opruimen')`)
	if err == nil {
		for rows.Next() {
			var m vipMove
			var name string
			if rows.Scan(&m.Old, &m.VIP, &m.Cluster, &name) == nil && m.VIP != "" && m.VIP != m.Old {
				m.Why = fmt.Sprintf("%s is vervangen door cluster %s", name, m.Cluster)
				out[m.Old] = m
			}
		}
		rows.Close()
	}
	rows, err = s.pool.Query(ctx, `SELECT host(n.primary_ip), n.hostname, c.name,
		(SELECT host(v.address) FROM vips v WHERE v.cluster_id = c.id ORDER BY v.created_at LIMIT 1)
		FROM nodes n JOIN clusters c ON c.id = n.cluster_id
		WHERE n.primary_ip IS NOT NULL AND EXISTS (SELECT 1 FROM vips v WHERE v.cluster_id = c.id)
		AND (SELECT count(*) FROM nodes m WHERE m.cluster_id = c.id) > 1`)
	if err == nil {
		for rows.Next() {
			var m vipMove
			var host string
			if rows.Scan(&m.Old, &host, &m.Cluster, &m.VIP) == nil && m.VIP != "" && m.VIP != m.Old {
				if _, ok := out[m.Old]; !ok {
					m.Why = fmt.Sprintf("%s is één node van cluster %s; valt hij uit, dan neemt een andere node het VIP over", host, m.Cluster)
					out[m.Old] = m
				}
			}
		}
		rows.Close()
	}
	return out
}

// planVIPAdvice maakt de voorstellen uit de port forwards en de regels.
func planVIPAdvice(nat, filter []haveObj, moves map[string]vipMove) []AutoItem {
	var out []AutoItem
	for _, r := range nat {
		if r.UUID == "" || r.F["disabled"] == "1" || r.F["enabled"] == "0" {
			continue
		}
		target := strings.TrimSpace(r.F["target"])
		m, ok := moves[target]
		if !ok {
			continue
		}
		name := orDefault(r.F["descr"], orDefault(r.F["description"], r.UUID[:8]))
		port := orDefault(r.F["local-port"], r.F["destination_port"])
		out = append(out, AutoItem{Key: "vipnat:" + r.UUID, Kind: "vip", Op: "set", Manual: true,
			Title: fmt.Sprintf("port forward %s naar het VIP %s", name, m.VIP),
			Detail: fmt.Sprintf("Wijst nu naar %s%s. %s. Voorbeeld: doel %s → %s.", target, portText(port), m.Why, target, m.VIP),
			call:  &protocol.OPNCall{Path: "firewall/d_nat/setRule/" + r.UUID, Body: map[string]any{"rule": map[string]any{"target": m.VIP}}}})
	}
	// Welke poorten hebben al een pass-regel naar het VIP.
	have := map[string]bool{}
	for _, r := range filter {
		if r.F["action"] == "pass" && r.F["enabled"] != "0" {
			have[r.F["destination_net"]+"|"+r.F["destination_port"]] = true
		}
	}
	for _, r := range filter {
		if r.UUID == "" || r.F["enabled"] == "0" || r.F["action"] != "pass" || r.F["destination_port"] == "" {
			continue
		}
		dst := strings.TrimSpace(r.F["destination_net"])
		m, ok := moves[strings.TrimSuffix(dst, "/32")]
		if !ok || have[m.VIP+"|"+r.F["destination_port"]] {
			continue
		}
		have[m.VIP+"|"+r.F["destination_port"]] = true
		rule := map[string]any{}
		for _, k := range []string{"action", "quick", "interface", "direction", "ipprotocol", "protocol", "source_net", "source_port", "source_not", "destination_port", "log"} {
			if v, ok := r.F[k]; ok && v != "" {
				rule[k] = v
			}
		}
		rule["enabled"], rule["destination_net"] = "1", m.VIP
		rule["description"] = truncate("cf: "+orDefault(r.F["description"], "regel")+" (naar het VIP)", 200)
		if seq := ruleSeq(r); seq < 1<<30 {
			rule["sequence"] = fmt.Sprint(max(seq-1, 1))
		}
		out = append(out, AutoItem{Key: "viprule:" + r.UUID, Kind: "vip", Op: "add", Manual: true,
			Title: fmt.Sprintf("regel %s ook naar het VIP %s", ruleName(r), m.VIP),
			Detail: fmt.Sprintf("Laat %s → %s poort %s toe, maar niet het VIP. %s. De oude regel blijft staan; haal hem weg als de oude installatie weg is.",
				orDefault(r.F["source_net"], "any"), dst, r.F["destination_port"], m.Why),
			call: &protocol.OPNCall{Path: "firewall/filter/addRule", Body: map[string]any{"rule": rule}}})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func portText(p string) string {
	if p == "" {
		return ""
	}
	return " poort " + p
}

// vipAdvice leest de port forwards en geeft de voorstellen naar het VIP.
func (s *Service) vipAdvice(ctx context.Context, fw Firewall, filter []haveObj) ([]AutoItem, string) {
	moves := s.vipMoves(ctx)
	if len(moves) == 0 {
		return nil, ""
	}
	nat, err := s.searchRows(ctx, fw.NodeID, "firewall/d_nat/searchRule")
	note := ""
	if err != nil {
		note = "De port forwards konden niet gelezen worden (" + err.Error() + "); voorstellen naar het VIP alleen uit de regels."
	}
	return planVIPAdvice(nat, filter, moves), note
}

// VIPAdviceFor geeft de voorstellen naar het VIP van één cluster van
// ClusterForge, over alle firewalls heen (voor de uitrol- en
// verhuiswizard).
type VIPAdvice struct {
	FirewallID string     `json:"firewall_id"`
	Cluster    string     `json:"cluster"`
	Confirm    string     `json:"confirm"`
	Prod       bool       `json:"prod"`
	Items      []AutoItem `json:"items"`
}

// VIPAdviceFor zoekt in het regeladvies van elke firewall de voorstellen
// naar de VIP's van dit cluster.
func (s *Service) VIPAdviceFor(ctx context.Context, vips []string) ([]VIPAdvice, error) {
	clusters, err := s.Clusters(ctx)
	if err != nil {
		return nil, err
	}
	out := []VIPAdvice{}
	for _, c := range clusters {
		fw := readNodeOf(c)
		if fw == nil {
			continue
		}
		v, err := s.advice(ctx, c)
		if err != nil {
			continue
		}
		a := VIPAdvice{FirewallID: fw.NodeID.String(), Cluster: c.Name, Confirm: v.Confirm, Prod: c.Environment == "prod", Items: []AutoItem{}}
		for _, it := range v.Items {
			if it.Kind == "vip" && !it.Ignored && slices.ContainsFunc(vips, func(vip string) bool { return strings.Contains(it.Title, " "+vip) }) {
				a.Items = append(a.Items, it)
			}
		}
		if len(a.Items) > 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

