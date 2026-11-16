package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// knownNode is wat ClusterForge van een adres weet.
type knownNode struct {
	ID        uuid.UUID
	Hostname  string
	Cluster   string
	ClusterID *uuid.UUID
	// listen: "tcp/22" → proces (de agent zag iets luisteren).
	listen map[string]string
	// plan: de poorten uit het firewallplan, met hun stand (open, closed,
	// docker, ignored); managed: het plan wordt ook toegepast.
	plan    []planEntry
	managed bool
}

type planEntry struct {
	Proto string `json:"proto"`
	Port  string `json:"port"`
	State string `json:"state"`
}

func (e planEntry) covers(proto string, port int) bool {
	if e.Proto != proto && e.Proto != "any" {
		return false
	}
	lo, hi, ok := strings.Cut(e.Port, ":")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return false
	}
	b := a
	if ok {
		if b, err = strconv.Atoi(hi); err != nil {
			return false
		}
	}
	return port >= a && port <= b
}

// Riskante diensten: wat op een eigen netwerk zelden open hoort te staan.
type riskyPort struct {
	Severity, Title, Fix string
}

var riskyPorts = map[int]riskyPort{
	2375:  {SevCritical, "Docker-API zonder TLS", "Wie deze poort bereikt, is root op de host. Zet de tcp-socket van dockerd uit of gebruik TLS op 2376."},
	23:    {SevHigh, "Telnet staat open", "Telnet stuurt wachtwoorden onversleuteld. Zet het uit en gebruik SSH."},
	5900:  {SevHigh, "VNC staat open", "VNC heeft vaak een zwak of geen wachtwoord. Alleen via een tunnel of VPN openzetten."},
	5901:  {SevHigh, "VNC staat open", "VNC heeft vaak een zwak of geen wachtwoord. Alleen via een tunnel of VPN openzetten."},
	21:    {SevMedium, "FTP staat open", "FTP stuurt wachtwoorden onversleuteld. Gebruik SFTP."},
	3389:  {SevMedium, "RDP staat open", "Beperk RDP tot het beheernetwerk of een VPN."},
	445:   {SevMedium, "SMB staat open", "Beperk SMB tot de toestellen die de share nodig hebben."},
	6379:  {SevMedium, "Redis bereikbaar over het netwerk", "Redis draait vaak zonder wachtwoord. Bind aan 127.0.0.1 of zet requirepass en een firewallregel."},
	27017: {SevMedium, "MongoDB bereikbaar over het netwerk", "Zet authenticatie aan en beperk de poort tot de apps die hem gebruiken."},
	9200:  {SevMedium, "Elasticsearch bereikbaar over het netwerk", "Zet de beveiliging van Elasticsearch aan en beperk de poort."},
	11211: {SevMedium, "Memcached bereikbaar over het netwerk", "Memcached heeft geen login. Bind aan 127.0.0.1."},
	111:   {SevLow, "rpcbind staat open", "Zonder NFS is rpcbind overbodig: systemctl disable --now rpcbind.socket rpcbind."},
}

var (
	reOldTLS   = regexp.MustCompile(`(?m)^\s*(SSLv2|SSLv3|TLSv1\.0|TLSv1\.1):`)
	reWeakTLS  = regexp.MustCompile(`least strength: ([D-F])`)
	reNotAfter = regexp.MustCompile(`Not valid after:\s*(\S+)`)
	weakSSH    = []string{"diffie-hellman-group1-sha1", "diffie-hellman-group-exchange-sha1", "ssh-dss", "arcfour", "-cbc", "hmac-md5", "hmac-sha1-96"}
)

// portDrafts zijn de bevindingen voor één open poort.
func portDrafts(p protocol.ScanPort, kn *knownNode, known string, now time.Time) []Draft {
	var out []Draft
	pp := fmt.Sprintf("%d/%s", p.Port, p.Proto)
	svc := strings.TrimSpace(p.Product + " " + p.Version)
	if svc == "" {
		svc = p.Service
	}
	detail := map[string]any{"port": p.Port, "proto": p.Proto, "service": svc}
	if r, ok := riskyPorts[p.Port]; ok && p.Proto == "tcp" {
		out = append(out, Draft{Kind: KindNetwork, Key: "poort:" + pp, Severity: r.Severity, Title: r.Title + " (" + pp + ")",
			Summary: "De scanner bereikte " + pp + " (" + svc + ").", Fix: r.Fix, Detail: detail})
	}
	if kn != nil {
		switch known {
		case "dicht":
			out = append(out, Draft{Kind: KindNetwork, Key: "firewall:" + pp, Severity: SevHigh,
				Title:   "Bereikbaar terwijl de firewall hem dicht zet (" + pp + ")",
				Summary: "Het firewallplan van " + kn.Hostname + " zet " + pp + " dicht, maar de scanner bereikte de poort.",
				Fix:     "Kijk op Firewall of het plan toegepast is en ufw aanstaat; Docker zet poorten soms langs ufw open.", Detail: detail})
		case "onbekend":
			out = append(out, Draft{Kind: KindNetwork, Key: "onbekend:" + pp, Severity: SevMedium,
				Title:   "Open poort die ClusterForge niet kent (" + pp + ")",
				Summary: "Op " + kn.Hostname + " zag de agent niets luisteren op " + pp + ", en het firewallplan kent de poort niet.",
				Fix:     "Zoek met ss -tlnp welk proces dit is; is het in orde, zet het dan op Firewall als eigen regel.", Detail: detail})
		}
	}
	if out2 := p.Scripts["ssl-enum-ciphers"]; out2 != "" {
		if m := reOldTLS.FindAllStringSubmatch(out2, -1); len(m) > 0 {
			var vs []string
			for _, x := range m {
				vs = append(vs, x[1])
			}
			out = append(out, Draft{Kind: KindNetwork, Key: "tls-versie:" + pp, Severity: SevMedium,
				Title: "Verouderde TLS-versie (" + pp + ")", Summary: "De dienst aanvaardt nog " + strings.Join(vs, ", ") + ".",
				Fix: "Zet minimaal TLS 1.2 in de configuratie van de dienst (bijvoorbeeld ssl_protocols TLSv1.2 TLSv1.3).", Detail: detail})
		} else if m := reWeakTLS.FindStringSubmatch(out2); m != nil {
			out = append(out, Draft{Kind: KindNetwork, Key: "tls-cipher:" + pp, Severity: SevMedium,
				Title: "Zwakke TLS-ciphers (" + pp + ")", Summary: "nmap geeft de zwakste cipher klasse " + m[1] + ".",
				Fix: "Laat alleen moderne ciphers toe (Mozilla-profiel intermediate).", Detail: detail})
		}
	}
	if cert := p.Scripts["ssl-cert"]; cert != "" {
		if m := reNotAfter.FindStringSubmatch(cert); m != nil {
			if t, err := parseCertTime(m[1]); err == nil {
				switch {
				case t.Before(now):
					out = append(out, Draft{Kind: KindNetwork, Key: "cert:" + pp, Severity: SevMedium, Title: "Certificaat verlopen (" + pp + ")",
						Summary: "Het certificaat is verlopen op " + t.Format("2006-01-02") + ".", Fix: "Vernieuw het certificaat.", Detail: detail})
				case t.Before(now.Add(14 * 24 * time.Hour)):
					out = append(out, Draft{Kind: KindNetwork, Key: "cert:" + pp, Severity: SevLow, Title: "Certificaat verloopt binnenkort (" + pp + ")",
						Summary: "Het certificaat verloopt op " + t.Format("2006-01-02") + ".", Fix: "Vernieuw het certificaat op tijd.", Detail: detail})
				}
			}
		}
	}
	if algos := p.Scripts["ssh2-enum-algos"]; algos != "" {
		var weak []string
		for _, l := range strings.Split(algos, "\n") {
			l = strings.TrimSpace(l)
			if strings.Contains(l, ":") {
				continue
			}
			for _, w := range weakSSH {
				if strings.Contains(l, w) && !slices.Contains(weak, l) {
					weak = append(weak, l)
				}
			}
		}
		if len(weak) > 0 {
			out = append(out, Draft{Kind: KindNetwork, Key: "ssh-algos:" + pp, Severity: SevLow, Title: "Zwakke SSH-algoritmen (" + pp + ")",
				Summary: "De SSH-server biedt nog: " + strings.Join(weak, ", ") + ".",
				Fix:     "Beperk Ciphers, MACs en KexAlgorithms in sshd_config tot moderne algoritmen.", Detail: detail})
		}
	}
	return out
}

func parseCertTime(s string) (time.Time, error) {
	for _, f := range []string{"2006-01-02T15:04:05", time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("onbekende datum %q", s)
}

// expectedPort zegt wat ClusterForge van een open poort wist.
func expectedPort(kn *knownNode, proto string, port int) RunPort {
	rp := RunPort{Proto: proto, Port: port}
	if kn == nil {
		return rp
	}
	key := proto + "/" + strconv.Itoa(port)
	if pr, ok := kn.listen[key]; ok {
		rp.Known, rp.Process = "luistert", pr
		return rp
	}
	for _, e := range kn.plan {
		if !e.covers(proto, port) {
			continue
		}
		if e.State == "closed" && kn.managed {
			rp.Known = "dicht"
			return rp
		}
		if e.State != "closed" {
			rp.Known = "firewall"
			return rp
		}
	}
	rp.Known = "onbekend"
	return rp
}

// analyzeHosts legt het resultaat naast wat ClusterForge weet.
func analyzeHosts(res *protocol.ScanResult, known map[string]*knownNode, probes []protocol.ProbeResult, now time.Time) ([]RunHost, map[string][]Draft, int) {
	byPort := map[string]protocol.ProbeResult{}
	for _, p := range probes {
		byPort[p.Addr+"|"+strconv.Itoa(p.Port)] = p
	}
	hosts := []RunHost{}
	drafts := map[string][]Draft{}
	open := 0
	for _, h := range res.Hosts {
		rh := RunHost{ScanHost: h, Expected: []RunPort{}}
		kn := known[h.Addr]
		if kn != nil {
			id := kn.ID
			rh.NodeID, rh.Node, rh.Cluster = &id, kn.Hostname, kn.Cluster
		}
		var ds []Draft
		for _, p := range h.Ports {
			open++
			ep := expectedPort(kn, p.Proto, p.Port)
			if pr, ok := byPort[h.Addr+"|"+strconv.Itoa(p.Port)]; ok && p.Proto == "tcp" {
				ep.Title, ep.Hits = pr.Title, pr.Hits
				ds = append(ds, probeDrafts(p, pr)...)
			}
			rh.Expected = append(rh.Expected, ep)
			ds = append(ds, portDrafts(p, kn, ep.Known, now)...)
		}
		drafts[h.Addr] = ds
		hosts = append(hosts, rh)
	}
	return hosts, drafts, open
}

// knownNodes leest wat ClusterForge per adres weet.
func (s *Service) knownNodes(ctx context.Context) (map[string]*knownNode, error) {
	out := map[string]*knownNode{}
	byID := map[uuid.UUID]*knownNode{}
	rows, err := s.pool.Query(ctx, `SELECT n.id, n.hostname, host(n.primary_ip), coalesce(c.name, ''), n.cluster_id
FROM nodes n LEFT JOIN clusters c ON c.id = n.cluster_id WHERE n.primary_ip IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		kn := &knownNode{listen: map[string]string{}}
		var addr string
		if err := rows.Scan(&kn.ID, &kn.Hostname, &addr, &kn.Cluster, &kn.ClusterID); err != nil {
			rows.Close()
			return nil, err
		}
		if a, err := netip.ParseAddr(addr); err == nil {
			out[a.Unmap().String()] = kn
			byID[kn.ID] = kn
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT node_id, proto, port, coalesce(nullif(container, ''), nullif(unit, ''), process) FROM firewall_ports`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var proto, proc string
		var port int
		if err := rows.Scan(&id, &proto, &port, &proc); err != nil {
			rows.Close()
			return nil, err
		}
		if kn := byID[id]; kn != nil {
			kn.listen[proto+"/"+strconv.Itoa(port)] = proc
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT node_id, mode = 'managed' AND applied_hash <> '', coalesce(plan->'entries', '[]')
FROM firewall_nodes WHERE mode <> 'off' AND plan IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var managed bool
		var raw []byte
		if err := rows.Scan(&id, &managed, &raw); err != nil {
			return nil, err
		}
		if kn := byID[id]; kn != nil {
			kn.managed = managed
			_ = json.Unmarshal(raw, &kn.plan)
		}
	}
	return out, rows.Err()
}

// analyzeScan verwerkt een scan: de hosts met wat er verwacht werd, en de
// bevindingen per adres. Adressen in de scope die niet meer opduiken,
// krijgen hun netwerkbevindingen opgelost.
func (s *Service) analyzeScan(ctx context.Context, scope Scope, res *protocol.ScanResult, probes []protocol.ProbeResult) (*RunResult, int, int, error) {
	known, err := s.knownNodes(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	hosts, drafts, open := analyzeHosts(res, known, probes, s.Now())
	out := &RunResult{Nmap: res.Nmap, Args: res.Args, Truncated: res.Truncated, Hosts: hosts, Notes: []string{}}
	if res.Truncated {
		out.Notes = append(out.Notes, fmt.Sprintf("Het resultaat is ingekort tot %d hosts en %d poorten per host.", protocol.MaxScanHosts, protocol.MaxScanPortsPerHost))
	}
	found := 0
	for addr, ds := range drafts {
		var nodeID, clusterID *uuid.UUID
		hostname := addr
		if kn := known[addr]; kn != nil {
			id := kn.ID
			nodeID, clusterID, hostname = &id, kn.ClusterID, kn.Hostname
		}
		if err := s.syncSubject(ctx, KindNetwork, "ip:"+addr, nodeID, clusterID, hostname, ds); err != nil {
			return nil, 0, 0, err
		}
		found += len(ds)
	}
	// Adressen die niet meer opduiken: hun bevindingen zijn opgelost.
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT subject FROM security_findings WHERE kind = $1 AND status <> 'opgelost'`, KindNetwork)
	if err != nil {
		return nil, 0, 0, err
	}
	var gone []string
	for rows.Next() {
		var subj string
		if err := rows.Scan(&subj); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		a, err := netip.ParseAddr(strings.TrimPrefix(subj, "ip:"))
		if err != nil || !addrIn(scope.CIDRs, a) || addrIn(scope.Exclude, a) {
			continue
		}
		if _, seen := drafts[a.String()]; !seen {
			gone = append(gone, subj)
		}
	}
	rows.Close()
	if !res.Truncated && len(gone) > 0 {
		if _, err := s.pool.Exec(ctx, `UPDATE security_findings SET status = 'opgelost', resolved_at = $3
WHERE kind = $1 AND subject = ANY($2) AND status <> 'opgelost'`, KindNetwork, gone, s.Now()); err != nil {
			return nil, 0, 0, err
		}
	}
	return out, open, found, nil
}

// probeHits zijn de vaste uitkomsten van het aanspreken van een dienst.
var probeHits = map[string]riskyPort{
	"haproxy-admin":      {SevHigh, "HAProxy-statistiek met beheeracties zonder login", "Zet stats auth <gebruiker>:<wachtwoord> en bind de statistiek aan 127.0.0.1 of het beheernetwerk: zonder login kan iedereen servers uit de pool halen."},
	"haproxy-stats":      {SevMedium, "HAProxy-statistiek zonder login", "Zet stats auth <gebruiker>:<wachtwoord> of beperk de poort tot het beheernetwerk."},
	"dirlist":            {SevLow, "Mappenlijst van een webserver", "Zet autoindex of Indexes uit voor deze map."},
	"phpmyadmin-open":    {SevHigh, "phpMyAdmin zonder aanmeldscherm", "Zet auth_type op cookie en beperk phpMyAdmin tot het beheernetwerk."},
	"docker-api":         {SevCritical, "Docker-API zonder TLS-clientcertificaat", "Wie deze API bereikt, is root op de host. Zet de tcp-socket uit of gebruik --tlsverify."},
	"elasticsearch-open": {SevHigh, "Elasticsearch zonder login", "Zet xpack.security.enabled: true en beperk poort 9200."},
	"etcd-open":          {SevMedium, "etcd zonder clientcertificaat", "Zet client-cert-auth aan of beperk 2379 tot de clusterleden."},
	"prometheus-open":    {SevLow, "Prometheus-API zonder login", "Beperk Prometheus tot het beheernetwerk of zet er een proxy met login voor."},
	"grafana-anon":       {SevMedium, "Grafana laat anoniem dashboards zien", "Zet [auth.anonymous] enabled = false in grafana.ini."},
	"grafana-default":    {SevCritical, "Grafana met het standaardwachtwoord admin/admin", "Wijzig meteen het wachtwoord van admin in Grafana."},
	"traefik-api":        {SevMedium, "Traefik-API zonder login", "Zet api.insecure op false en bescherm het dashboard met een middleware met login."},
	"basic-default":      {SevCritical, "Login met het standaardwachtwoord admin/admin", "Wijzig meteen het wachtwoord van dit toestel of deze dienst."},
	"redis-noauth":       {SevHigh, "Redis zonder wachtwoord", "Zet requirepass (of ACL's) en bind Redis aan 127.0.0.1 of het clusternetwerk."},
	"memcached-open":     {SevMedium, "Memcached open over het netwerk", "Bind memcached aan 127.0.0.1 (-l 127.0.0.1)."},
}

func probeDrafts(p protocol.ScanPort, pr protocol.ProbeResult) []Draft {
	var out []Draft
	pp := fmt.Sprintf("%d/%s", p.Port, p.Proto)
	for _, h := range pr.Hits {
		r, ok := probeHits[h.ID]
		if !ok {
			continue
		}
		out = append(out, Draft{Kind: KindNetwork, Key: "dienst:" + h.ID + ":" + pp, Severity: r.Severity, Title: r.Title + " (" + pp + ")",
			Summary: h.Detail + ".", Fix: r.Fix, Detail: map[string]any{"port": p.Port, "proto": p.Proto, "check": h.ID, "title": pr.Title}})
	}
	return out
}

// probeTargets kiest uit een scan de diensten die de scanner aanspreekt.
func probeTargets(res *protocol.ScanResult) []protocol.ProbeTarget {
	var out []protocol.ProbeTarget
	for _, h := range res.Hosts {
		for _, p := range h.Ports {
			if p.Proto != "tcp" {
				continue
			}
			kind := ""
			svc := strings.ToLower(p.Service)
			switch {
			case p.Port == 6379 || svc == "redis":
				kind = protocol.ProbeRedis
			case p.Port == 11211 || strings.HasPrefix(svc, "memcache"):
				kind = protocol.ProbeMemcached
			case p.Tunnel == "ssl" && (strings.HasPrefix(svc, "http") || svc == "" || svc == "ssl"), svc == "https", svc == "https-alt":
				kind = protocol.ProbeHTTPS
			case strings.HasPrefix(svc, "http"), slices.Contains([]int{2375, 2379, 9090, 9200}, p.Port):
				kind = protocol.ProbeHTTP
			}
			if kind != "" && len(out) < protocol.MaxProbeTargets {
				out = append(out, protocol.ProbeTarget{Addr: h.Addr, Port: p.Port, Kind: kind})
			}
		}
	}
	return out
}
