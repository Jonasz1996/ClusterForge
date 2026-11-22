package security

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/nmapxml"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Fase 43: een nmap-rapport van buiten ClusterForge inlezen (zoals de scans
// van de Kali-VM). Het rapport gaat door dezelfde analyse als een eigen scan,
// zodat de bevindingen bij de juiste node staan en met dezelfde knoppen
// opgelost worden. Wat nmap alleen vermoedt, telt niet mee.

// ImportScripts zijn NSE-scripts die bij een ingelezen rapport ook gelezen
// worden: ze tonen iets wat de dienst zelf vertelt.
var ImportScripts = []string{"http-git", "smb2-security-mode", "smb-security-mode", "snmp-info", "snmp-sysdescr", "nfs-showmount", "rpcinfo"}

// MaxImportBytes is de grootte van alle bestanden samen.
const MaxImportBytes = 64 << 20

// ImportFile is één XML-bestand van nmap (-oX).
type ImportFile struct {
	Name string `json:"name"`
	XML  string `json:"xml"`
}

// ImportNmap leest een of meer XML-bestanden van één scan in.
func (s *Service) ImportNmap(ctx context.Context, actor events.Actor, name string, files []ImportFile) (ScanRun, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Ingelezen rapport"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	if len(files) == 0 {
		return ScanRun{}, &FieldError{"Kies minstens één XML-bestand van nmap (-oX)."}
	}
	merged := &protocol.ScanResult{Hosts: []protocol.ScanHost{}}
	byAddr := map[string]int{}
	skipped, names := 0, []string{}
	for _, f := range files {
		res, sk, err := nmapxml.Parse([]byte(f.XML), ImportScripts)
		if err != nil {
			return ScanRun{}, &FieldError{f.Name + ": " + err.Error()}
		}
		skipped += sk
		names = append(names, f.Name)
		merged.Truncated = merged.Truncated || res.Truncated
		if merged.Nmap == "" {
			merged.Nmap = res.Nmap
		}
		for _, h := range res.Hosts {
			i, ok := byAddr[h.Addr]
			if !ok {
				byAddr[h.Addr] = len(merged.Hosts)
				merged.Hosts = append(merged.Hosts, h)
				continue
			}
			m := &merged.Hosts[i]
			if m.Hostname == "" {
				m.Hostname = h.Hostname
			}
			if m.MAC == "" {
				m.MAC, m.Vendor = h.MAC, h.Vendor
			}
			for _, p := range h.Ports {
				j := slices.IndexFunc(m.Ports, func(o protocol.ScanPort) bool { return o.Proto == p.Proto && o.Port == p.Port })
				switch {
				case j < 0:
					m.Ports = append(m.Ports, p)
				case len(p.Scripts) > len(m.Ports[j].Scripts) || m.Ports[j].Product == "" && p.Product != "":
					// De volledigste versie van de poort houden.
					for k, v := range m.Ports[j].Scripts {
						if _, ok := p.Scripts[k]; !ok {
							if p.Scripts == nil {
								p.Scripts = map[string]string{}
							}
							p.Scripts[k] = v
						}
					}
					m.Ports[j] = p
				}
			}
		}
	}
	merged.HostsUp = len(merged.Hosts)
	// Geen scope: wat niet in dit rapport staat, blijft zoals het was.
	out, open, found, err := s.analyzeScan(ctx, Scope{}, merged, nil)
	if err != nil {
		return ScanRun{}, err
	}
	out.Notes = append(out.Notes, "Ingelezen uit "+strings.Join(names, ", ")+".")
	if skipped > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d poorten zonder antwoord (open|filtered, meestal UDP) overgeslagen: nmap vermoedt die alleen. Zo vallen de valse meldingen voor SNMP, UPnP en CoAP weg.", skipped))
	}
	out.Notes = append(out.Notes, "Alleen NSE-scripts die tonen wat een dienst zelf vertelt, tellen mee. De vuln-scripts van nmap (zoals http-vuln-cve2011-3192, een Apache-test die op nginx vals aanslaat) worden niet overgenomen; kwetsbare pakketten komen uit het tabblad Pakketten.")
	var runID uuid.UUID
	now := s.Now()
	raw, _ := json.Marshal(out)
	if err := s.pool.QueryRow(ctx, `INSERT INTO security_scan_runs (scan_id, scanner_name, trigger, status, started_at, finished_at, targets, hosts_up, open_ports, findings, result)
VALUES (NULL, $1, 'import', 'klaar', $2, $2, $3, $4, $5, $6, $7) RETURNING id`, name, now, names, merged.HostsUp, open, found, raw).Scan(&runID); err != nil {
		return ScanRun{}, err
	}
	s.event(ctx, actor, "security_scan_run", runID.String(), nil, "security.scan_imported",
		map[string]any{"name": name, "files": names, "hosts": merged.HostsUp, "open_ports": open, "findings": found})
	runs, err := s.runs(ctx, "WHERE r.id = $1", 1, runID)
	if err != nil || len(runs) == 0 {
		return ScanRun{}, err
	}
	return runs[0], nil
}
