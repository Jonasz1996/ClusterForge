// Package nmapxml leest de XML-uitvoer van nmap: op de agent (eigen scans)
// en op de server (een ingelezen rapport van bijvoorbeeld een Kali-VM).
package nmapxml

import (
	"encoding/xml"
	"fmt"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type nmapRun struct {
	Hosts    []nmapHost `xml:"host"`
	RunStats struct {
		Hosts struct {
			Up    int `xml:"up,attr"`
			Total int `xml:"total,attr"`
		} `xml:"hosts"`
	} `xml:"runstats"`
}

type nmapHost struct {
	Status struct {
		State string `xml:"state,attr"`
	} `xml:"status"`
	Addresses []struct {
		Addr   string `xml:"addr,attr"`
		Type   string `xml:"addrtype,attr"`
		Vendor string `xml:"vendor,attr"`
	} `xml:"address"`
	Hostnames []struct {
		Name string `xml:"name,attr"`
	} `xml:"hostnames>hostname"`
	Ports []struct {
		Proto string `xml:"protocol,attr"`
		Port  int    `xml:"portid,attr"`
		State struct {
			State string `xml:"state,attr"`
		} `xml:"state"`
		Service struct {
			Name    string   `xml:"name,attr"`
			Product string   `xml:"product,attr"`
			Version string   `xml:"version,attr"`
			Extra   string   `xml:"extrainfo,attr"`
			Tunnel  string   `xml:"tunnel,attr"`
			CPE     []string `xml:"cpe"`
		} `xml:"service"`
		Scripts []struct {
			ID     string `xml:"id,attr"`
			Output string `xml:"output,attr"`
		} `xml:"script"`
	} `xml:"ports>port"`
}

// parseNmapXML leest de XML-uitvoer van nmap, met de grenzen uit protocol.
// Parse leest de uitvoer met de grenzen uit protocol. Alleen poorten die
// echt open zijn tellen; skipped is het aantal open|filtered. Van de
// NSE-scripts blijven de veilige over, plus extra.
func Parse(data []byte, extra []string) (*protocol.ScanResult, int, error) {
	var run nmapRun
	skipped := 0
	if err := xml.Unmarshal(data, &run); err != nil {
		return nil, 0, fmt.Errorf("uitvoer van nmap onleesbaar: %w", err)
	}
	res := &protocol.ScanResult{HostsUp: run.RunStats.Hosts.Up, HostsSeen: run.RunStats.Hosts.Total, Hosts: []protocol.ScanHost{}}
	for _, h := range run.Hosts {
		if h.Status.State != "" && h.Status.State != "up" {
			continue
		}
		var host protocol.ScanHost
		for _, a := range h.Addresses {
			switch a.Type {
			case "ipv4", "ipv6":
				host.Addr = a.Addr
			case "mac":
				host.MAC, host.Vendor = a.Addr, a.Vendor
			}
		}
		if host.Addr == "" {
			continue
		}
		if len(h.Hostnames) > 0 {
			host.Hostname = h.Hostnames[0].Name
		}
		host.Ports = []protocol.ScanPort{}
		for _, p := range h.Ports {
			if p.State.State != "open" {
				// open|filtered (UDP zonder antwoord) is geen bewijs.
				if p.State.State == "open|filtered" {
					skipped++
				}
				continue
			}
			if len(host.Ports) >= protocol.MaxScanPortsPerHost {
				res.Truncated = true
				break
			}
			sp := protocol.ScanPort{Proto: p.Proto, Port: p.Port, State: p.State.State, Service: p.Service.Name, Product: p.Service.Product,
				Version: p.Service.Version, Extra: p.Service.Extra, Tunnel: p.Service.Tunnel, CPE: p.Service.CPE}
			for _, s := range p.Scripts {
				if !slices.Contains(protocol.SafeScanScripts, s.ID) && !slices.Contains(extra, s.ID) {
					continue
				}
				if sp.Scripts == nil {
					sp.Scripts = map[string]string{}
				}
				out := strings.TrimSpace(s.Output)
				if len(out) > protocol.MaxScanScriptOutput {
					out = out[:protocol.MaxScanScriptOutput] + "\n…"
				}
				sp.Scripts[s.ID] = out
			}
			host.Ports = append(host.Ports, sp)
		}
		if len(res.Hosts) >= protocol.MaxScanHosts {
			res.Truncated = true
			break
		}
		res.Hosts = append(res.Hosts, host)
	}
	return res, skipped, nil
}
