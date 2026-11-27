package deploy

import (
	"context"
	"encoding/json"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// NetHint is wat de wizard invult bij een eerste adres (fase 48, 4.3):
// VLAN, gateway en DNS van het subnet, de vrije adressen, en het publieke
// adres van de firewall. Het is een voorstel: de wizard toont waar elke
// waarde vandaan komt en je kunt alles aanpassen.
type NetHint struct {
	// Prefix is het subnet, zoals 192.168.105.0/24.
	Prefix string `json:"prefix,omitempty"`
	// FirstIP is het adres met prefix, zoals 192.168.105.20/24.
	FirstIP string   `json:"first_ip,omitempty"`
	VLAN    *int     `json:"vlan,omitempty"`
	Bridge  string   `json:"bridge,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	// Source zegt per veld waar het vandaan komt: "firewall" (de interface
	// op OPNsense), "uitrol" (een eerdere uitrol in dit subnet) of "regel"
	// (eerste adres van het subnet).
	Source map[string]string `json:"source"`
	// Interface is de interface op de firewall met dit subnet.
	Interface string `json:"interface,omitempty"`
	// Free zijn de eerste vrije adressen vanaf het gevraagde; FreeCount
	// hoeveel er in het subnet vrij zijn volgens ClusterForge.
	Free      []string `json:"free"`
	FreeCount int      `json:"free_count"`
	// WAN is het publieke adres van de firewall: de CARP-VIP op wan, of
	// het adres van de wan-interface.
	WAN string `json:"wan,omitempty"`
}

// vlanRe haalt de tag uit een VLAN-apparaat: vlan0.40, vtnet1_vlan40,
// vlan40, igb0.40.
var vlanRe = regexp.MustCompile(`(?:^vlan\d*\.|_vlan|^vlan|^[a-z]+\d+\.)(\d{1,4})$`)

func vlanOf(device string) (int, bool) {
	m := vlanRe.FindStringSubmatch(device)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 || n > 4094 {
		return 0, false
	}
	return n, true
}

// pastTarget is wat een eerdere uitrol over het netwerk koos.
type pastTarget struct {
	FirstIP string   `json:"first_ip"`
	Bridge  string   `json:"bridge"`
	VLAN    *int     `json:"vlan"`
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`
}

// NetworkHint zoekt de netwerkinstellingen bij een adres. ip mag met of
// zonder prefix; zonder prefix komt die van de firewall, anders /24.
func (s *Service) NetworkHint(ctx context.Context, ip string, firewalls []protocol.OPNsenseFacts, want int) (NetHint, error) {
	used, err := s.usedAddresses(ctx)
	if err != nil {
		return NetHint{}, err
	}
	var past []pastTarget
	rows, err := s.pool.Query(ctx, `SELECT params->'target' FROM jobs WHERE kind = $1
		AND coalesce(params->'target'->>'first_ip', '') <> '' AND status = 'succeeded' ORDER BY created_at DESC LIMIT 200`, Kind)
	if err != nil {
		return NetHint{}, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return NetHint{}, err
		}
		var t pastTarget
		if json.Unmarshal(raw, &t) == nil {
			past = append(past, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return NetHint{}, err
	}
	return networkHint(ip, firewalls, past, used, want), nil
}

func networkHint(ip string, firewalls []protocol.OPNsenseFacts, past []pastTarget, used map[string]bool, want int) NetHint {
	h := NetHint{Source: map[string]string{}, Free: []string{}}
	h.WAN = wanOf(firewalls)
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return h
	}
	var addr netip.Addr
	var pf netip.Prefix
	if p, err := netip.ParsePrefix(ip); err == nil {
		addr, pf = p.Addr(), p.Masked()
	} else if a, err := netip.ParseAddr(ip); err == nil {
		addr = a
	} else {
		return h
	}
	if !addr.Is4() {
		return h
	}

	// De firewall: welke interface heeft dit subnet?
	for _, f := range firewalls {
		for _, i := range f.Interfaces {
			for _, a := range i.Addresses {
				ip, err := netip.ParsePrefix(a)
				if err != nil || !ip.Addr().Is4() || !ip.Masked().Contains(addr) {
					continue
				}
				if pf.IsValid() && pf.Bits() != ip.Bits() {
					continue
				}
				if !pf.IsValid() {
					pf = ip.Masked()
				}
				if h.Interface == "" {
					h.Interface = cmpOr(i.Descr, i.Name)
					if v, ok := vlanOf(i.Device); ok {
						h.VLAN, h.Source["vlan"] = &v, "firewall"
					}
				}
				// Het adres van de firewall zelf is bezet.
				used[ip.Addr().String()] = true
			}
		}
		for _, v := range f.VIPs {
			vp, err := netip.ParsePrefix(v.Address)
			if err != nil || !vp.Masked().Contains(addr) {
				continue
			}
			used[vp.Addr().String()] = true
			if h.Gateway == "" && (v.Mode == "" || v.Mode == "carp") {
				// Bij een CARP-cluster is het VIP de gateway (.1 bij Jonas).
				h.Gateway, h.Source["gateway"] = vp.Addr().String(), "firewall"
			}
		}
	}
	if !pf.IsValid() {
		pf = netip.PrefixFrom(addr, 24).Masked()
	}
	h.Prefix = pf.String()
	h.FirstIP = netip.PrefixFrom(addr, pf.Bits()).String()

	// Een eerdere geslaagde uitrol in hetzelfde subnet.
	for _, t := range past {
		tp, err := netip.ParsePrefix(t.FirstIP)
		if err != nil || tp.Masked() != pf {
			continue
		}
		if h.VLAN == nil && t.VLAN != nil {
			v := *t.VLAN
			h.VLAN, h.Source["vlan"] = &v, "uitrol"
		}
		if h.Gateway == "" && t.Gateway != "" {
			h.Gateway, h.Source["gateway"] = t.Gateway, "uitrol"
		}
		if len(h.DNS) == 0 && len(t.DNS) > 0 {
			h.DNS, h.Source["dns"] = slices.Clone(t.DNS), "uitrol"
		}
		if h.Bridge == "" && t.Bridge != "" {
			h.Bridge, h.Source["bridge"] = t.Bridge, "uitrol"
		}
		break
	}
	if h.Gateway == "" {
		h.Gateway, h.Source["gateway"] = pf.Addr().Next().String(), "regel"
	}
	if len(h.DNS) == 0 && h.Source["gateway"] == "firewall" {
		// Unbound op de firewall luistert op het VIP.
		h.DNS, h.Source["dns"] = []string{h.Gateway}, "firewall"
	}
	used[h.Gateway] = true

	// Vrije adressen vanaf het gevraagde; nooit de eerste drie, het
	// netwerk- of het broadcastadres.
	last := lastAddr(pf)
	start := addr
	if reserved(pf, start) || start == pf.Addr() {
		start = pf.Addr().Next().Next().Next().Next()
	}
	want = max(want, 1)
	n := 0
	for a := pf.Addr().Next(); pf.Contains(a) && a != last && n < 65536; a, n = a.Next(), n+1 {
		if reserved(pf, a) || used[a.String()] {
			continue
		}
		h.FreeCount++
		if a.Compare(start) >= 0 && len(h.Free) < max(want, 12) {
			h.Free = append(h.Free, a.String())
		}
	}
	return h
}

// wanOf is het publieke adres: een CARP-VIP op wan, anders het adres van
// de wan-interface.
func wanOf(firewalls []protocol.OPNsenseFacts) string {
	for _, f := range firewalls {
		for _, v := range f.VIPs {
			if v.Interface == "wan" {
				if p, err := netip.ParsePrefix(v.Address); err == nil {
					return p.Addr().String()
				}
			}
		}
	}
	for _, f := range firewalls {
		for _, i := range f.Interfaces {
			if i.Name != "wan" {
				continue
			}
			for _, a := range i.Addresses {
				if p, err := netip.ParsePrefix(a); err == nil && p.Addr().Is4() {
					return p.Addr().String()
				}
			}
		}
	}
	return ""
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
