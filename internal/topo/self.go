package topo

import (
	"context"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Self is de VM of container waarop ClusterForge draait.
type Self struct {
	// Source is auto (gevonden), manual (ingevuld) of leeg (niet gevonden).
	Source string
	Conn   uuid.UUID
	VMID   int
	// MatchedOn zegt waaraan hij gevonden is, zoals "MAC-adres
	// bc:24:11:00:00:09".
	MatchedOn string
	// Note zegt waarom hij niet gevonden is.
	Note string
}

// Found is true als bekend is waar ClusterForge draait.
func (s Self) Found() bool { return s.Source != "" }

// Manual is een plaats die een beheerder invulde.
type Manual struct {
	Conn uuid.UUID
	VMID int
}

// Own zijn de MAC- en IP-adressen van deze server, en de IP-adressen
// waarmee de nodes hem bereiken.
type Own struct {
	MACs []string
	IPs  []string
}

// OwnAddresses leest de interfaces van deze server en zoekt de hosts uit de
// hints op. Een hint is een host, host:poort of een URL.
func OwnAddresses(ctx context.Context, hints []string) Own {
	var o Own
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(i.HardwareAddr) == 6 {
			o.MACs = append(o.MACs, i.HardwareAddr.String())
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && usable(n.IP) {
				o.IPs = append(o.IPs, n.IP.String())
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, h := range hints {
		host := hintHost(h)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			o.IPs = append(o.IPs, ip.String())
			continue
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if usable(ip) {
				o.IPs = append(o.IPs, ip.String())
			}
		}
	}
	return Own{MACs: uniq(o.MACs), IPs: uniq(o.IPs)}
}

func usable(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// hintHost haalt de host uit "https://cf.lan:8443", "cf.lan:4222" of
// "cf.lan".
func hintHost(h string) string {
	h = strings.TrimSpace(h)
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil {
			return u.Hostname()
		}
		return ""
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}

func uniq(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}

func itoa(n int) string { return strconv.Itoa(n) }

// Locate zoekt waar ClusterForge draait. Een ingevulde plaats wint. Anders
// eerst op MAC-adres, dan op IP-adres; alleen een enkele treffer telt.
// Templates tellen niet mee.
func Locate(m Model, own Own, manual *Manual) Self {
	if manual != nil {
		return Self{Source: "manual", Conn: manual.Conn, VMID: manual.VMID, MatchedOn: "ingevuld door een beheerder"}
	}
	if len(m.Guests) == 0 {
		return Self{Note: "Er is geen Proxmox-koppeling met VM's of containers."}
	}
	var byMAC, byIP []Guest
	var mac, ip string
	read := 0
	for _, g := range m.Guests {
		if g.Template || g.Config == nil {
			continue
		}
		read++
		for _, n := range g.Config.NICs {
			if slices.Contains(own.MACs, n.MAC) {
				byMAC, mac = append(byMAC, g), n.MAC
				break
			}
		}
		for _, a := range g.Config.Addresses {
			if slices.Contains(own.IPs, a) {
				byIP, ip = append(byIP, g), a
				break
			}
		}
	}
	switch {
	case len(byMAC) == 1:
		return Self{Source: "auto", Conn: byMAC[0].Conn, VMID: byMAC[0].VMID, MatchedOn: "MAC-adres " + mac}
	case len(byIP) == 1:
		return Self{Source: "auto", Conn: byIP[0].Conn, VMID: byIP[0].VMID, MatchedOn: "IP-adres " + ip}
	case len(byMAC) > 1 || len(byIP) > 1:
		return Self{Note: "Meer dan één VM heeft een adres van ClusterForge. Kies de juiste bij Instellingen."}
	case read == 0:
		return Self{Note: "De configuratie van de VM's is nog niet gelezen; dat gebeurt binnen tien minuten na de koppeling."}
	}
	return Self{Note: "Geen VM of container heeft een MAC- of IP-adres van ClusterForge. In Docker zie je de adressen van de host niet; " +
		"zet dan CF_NATS_ADVERTISE op het IP-adres van de VM, of kies de VM bij Instellingen."}
}
