package security

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/soc"
)

// Rapport per scope (M5): wat een pentester zou opschrijven over een eigen
// netwerk, uit wat ClusterForge al weet: de laatste scan, de nodes in de
// scope met hun kwetsbaarheden en verharding, en de open bevindingen.

type ReportNode struct {
	NodeID         uuid.UUID      `json:"node_id"`
	Hostname       string         `json:"hostname"`
	Address        string         `json:"address"`
	ClusterName    string         `json:"cluster_name"`
	OS             string         `json:"os"`
	Counts         map[string]int `json:"counts"`
	KEV            int            `json:"kev"`
	HardeningScore *int           `json:"hardening_score"`
}

type ReportSummary struct {
	Counts   map[string]int `json:"counts"`
	Open     int            `json:"open"`
	Accepted int            `json:"accepted"`
	KEV      int            `json:"kev"`
	// HardeningAvg is het gemiddelde van de nodes met een score.
	HardeningAvg *int `json:"hardening_avg"`
	HostsUp      int  `json:"hosts_up"`
	OpenPorts    int  `json:"open_ports"`
	Unknown      int  `json:"unknown_hosts"`
}

type Report struct {
	Scope       Scope          `json:"scope"`
	GeneratedAt time.Time      `json:"generated_at"`
	Summary     ReportSummary  `json:"summary"`
	LastRun     *ScanRunDetail `json:"last_run"`
	Nodes       []ReportNode   `json:"nodes"`
	Findings    []Finding      `json:"findings"`
}

func inScope(sc Scope, addr string) bool {
	a, err := netip.ParseAddr(strings.TrimPrefix(addr, "ip:"))
	if err != nil {
		return false
	}
	a = a.Unmap()
	return addrIn(sc.CIDRs, a) && !addrIn(sc.Exclude, a)
}

// Report maakt het rapport van één scope.
func (s *Service) Report(ctx context.Context, scopeID uuid.UUID) (Report, error) {
	scopes, err := s.Scopes(ctx)
	if err != nil {
		return Report{}, err
	}
	r := Report{GeneratedAt: s.Now(), Nodes: []ReportNode{}, Findings: []Finding{},
		Summary: ReportSummary{Counts: map[string]int{}}}
	found := false
	for _, sc := range scopes {
		if sc.ID == scopeID {
			r.Scope, found = sc, true
		}
	}
	if !found {
		return Report{}, ErrNotFound
	}
	// Nodes in de scope.
	nodes, err := s.nodes(ctx, "")
	if err != nil {
		return r, err
	}
	idx := map[uuid.UUID]int{}
	sum, cnt := 0, 0
	for _, n := range nodes {
		if !inScope(r.Scope, n.Address) {
			continue
		}
		idx[n.NodeID] = len(r.Nodes)
		r.Nodes = append(r.Nodes, ReportNode{NodeID: n.NodeID, Hostname: n.Hostname, Address: n.Address, ClusterName: n.ClusterName,
			OS: strings.TrimSpace(n.OS + " " + n.Codename), Counts: map[string]int{}, HardeningScore: n.HardeningScore})
		if n.HardeningScore != nil {
			sum += *n.HardeningScore
			cnt++
		}
	}
	if cnt > 0 {
		avg := (sum + cnt/2) / cnt
		r.Summary.HardeningAvg = &avg
	}
	// Bevindingen: van nodes in de scope en van adressen in de scope.
	all, err := s.Findings(ctx, FindingFilter{})
	if err != nil {
		return r, err
	}
	for _, f := range all {
		in := false
		if f.NodeID != nil {
			_, in = idx[*f.NodeID]
		}
		if !in && strings.HasPrefix(f.Subject, "ip:") {
			in = inScope(r.Scope, f.Subject)
		}
		if !in {
			continue
		}
		r.Findings = append(r.Findings, f)
		if f.Status == StatusAccepted {
			r.Summary.Accepted++
			continue
		}
		r.Summary.Open++
		r.Summary.Counts[f.Severity]++
		var d struct {
			KEV int `json:"kev"`
		}
		_ = json.Unmarshal(f.Detail, &d)
		kev := f.Kind == KindCVE && d.KEV > 0
		if kev {
			r.Summary.KEV++
		}
		if f.NodeID != nil {
			if i, ok := idx[*f.NodeID]; ok {
				r.Nodes[i].Counts[f.Severity]++
				if kev {
					r.Nodes[i].KEV++
				}
			}
		}
	}
	// De laatste geslaagde scan van deze scope.
	var runID uuid.UUID
	err = s.pool.QueryRow(ctx, `SELECT r.id FROM security_scan_runs r JOIN security_scans s ON s.id = r.scan_id
WHERE s.scope_id = $1 AND r.status = 'klaar' ORDER BY r.started_at DESC LIMIT 1`, scopeID).Scan(&runID)
	if err == nil {
		d, err := s.ScanRunDetail(ctx, runID)
		if err != nil {
			return r, err
		}
		r.LastRun = &d
		r.Summary.HostsUp, r.Summary.OpenPorts = d.HostsUp, d.OpenPorts
		if d.Result != nil {
			for _, h := range d.Result.Hosts {
				if h.NodeID == nil {
					r.Summary.Unknown++
				}
			}
		}
	}
	return r, nil
}

// Summary is wat het SOC en "Aandacht nodig" van de beveiligingsmodule
// tonen: open bevindingen per soort en ernst, de nodes met een actief
// misbruikte kwetsbaarheid en de gemiddelde verharding.
type Summary struct {
	CVE          map[string]int `json:"cve"`
	Hardening    map[string]int `json:"hardening"`
	Network      map[string]int `json:"network"`
	KEVNodes     []string       `json:"kev_nodes"`
	CriticalNet  []string       `json:"critical_network"`
	HardeningAvg *int           `json:"hardening_avg"`
	LowHardening []string       `json:"low_hardening"`
	// SSHWaiting: nodes met een SSH-wijziging die op bevestiging wacht.
	SSHWaiting []string `json:"ssh_waiting"`
	// Tools: open bevindingen van de tools; Malware: nodes met een
	// kritieke (ClamAV, chkrootkit, Trivy-kritiek telt niet mee).
	Tools   map[string]int `json:"tools"`
	Malware []string       `json:"malware"`
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	out := Summary{CVE: map[string]int{}, Hardening: map[string]int{}, Network: map[string]int{}, KEVNodes: []string{}, CriticalNet: []string{}, LowHardening: []string{},
		SSHWaiting: []string{}, Tools: map[string]int{}, Malware: []string{}}
	rows, err := s.pool.Query(ctx, `
SELECT f.kind, f.severity, coalesce((f.detail->>'kev')::int, 0) > 0, coalesce(n.hostname, f.subject), f.key
FROM security_findings f LEFT JOIN nodes n ON n.id = f.node_id WHERE f.status = 'open'`)
	if err != nil {
		return out, err
	}
	kev, crit, mal := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var kind, sev, host, key string
		var isKEV bool
		if err := rows.Scan(&kind, &sev, &isKEV, &host, &key); err != nil {
			rows.Close()
			return out, err
		}
		host = strings.TrimPrefix(host, "ip:")
		switch kind {
		case KindCVE:
			out.CVE[sev]++
			if isKEV && !kev[host] {
				kev[host] = true
				out.KEVNodes = append(out.KEVNodes, host)
			}
		case KindHardening:
			out.Hardening[sev]++
		case KindTools:
			out.Tools[sev]++
			if sev == SevCritical && !strings.HasPrefix(key, "image:") && !mal[host] {
				mal[host] = true
				out.Malware = append(out.Malware, host)
			}
		case KindNetwork:
			out.Network[sev]++
			if sev == SevCritical && !crit[host] {
				crit[host] = true
				out.CriticalNet = append(out.CriticalNet, host)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if err := func() error {
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT n.hostname FROM security_fix_runs r JOIN nodes n ON n.id = r.node_id
			WHERE r.ssh_pending AND r.confirmed_at IS NULL AND r.rolled_back_at IS NULL AND r.rollback_at > now()`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return err
			}
			out.SSHWaiting = append(out.SSHWaiting, h)
		}
		return rows.Err()
	}(); err != nil {
		return out, err
	}
	rows, err = s.pool.Query(ctx, `SELECT n.hostname, h.score FROM security_hardening h JOIN nodes n ON n.id = h.node_id WHERE h.score IS NOT NULL`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	sum, cnt := 0, 0
	for rows.Next() {
		var host string
		var score int
		if err := rows.Scan(&host, &score); err != nil {
			return out, err
		}
		sum += score
		cnt++
		if score < 70 {
			out.LowHardening = append(out.LowHardening, host)
		}
	}
	if cnt > 0 {
		avg := (sum + cnt/2) / cnt
		out.HardeningAvg = &avg
	}
	return out, rows.Err()
}

// SOCVulns geeft de samenvatting in de vorm van het beveiligingsoverzicht.
func (s *Service) SOCVulns(ctx context.Context) (soc.Vulns, error) {
	sm, err := s.Summary(ctx)
	if err != nil {
		return soc.Vulns{}, err
	}
	return soc.Vulns{KEVNodes: sm.KEVNodes, CriticalNet: sm.CriticalNet, HighCVE: sm.CVE[SevHigh], HighNet: sm.Network[SevHigh],
		HardeningAvg: sm.HardeningAvg, LowHardening: sm.LowHardening}, nil
}
