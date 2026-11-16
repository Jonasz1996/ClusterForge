package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Probe (fase 27 M4, protocol 43): na een scan spreekt de scanner de
// gevonden diensten aan met één anonieme aanvraag per controle. Hij kijkt
// of een beheerpaneel of API zonder login open staat, en alleen als de
// beheerder het aanzet, één keer of het bekende standaardwachtwoord werkt.
// Geen brute force, geen exploits, niets dat iets verandert.

const (
	svcProbeTimeout = 6 * time.Second
	probeBodyMax    = 256 << 10
	probeParallel   = 8
)

var reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func (a *Agent) probeServices(ctx context.Context, sp *protocol.ProbeSpec) ([]protocol.ProbeResult, error) {
	if sp == nil || len(sp.Targets) == 0 {
		return []protocol.ProbeResult{}, nil
	}
	var allowed []netip.Prefix
	for _, c := range sp.Allowed {
		p, err := parseScanPrefix(c)
		if err != nil {
			return nil, fmt.Errorf("ongeldige scope %q", c)
		}
		allowed = append(allowed, p)
	}
	if len(allowed) == 0 {
		return nil, errors.New("geen scope meegegeven")
	}
	targets := sp.Targets
	if len(targets) > protocol.MaxProbeTargets {
		targets = targets[:protocol.MaxProbeTargets]
	}
	out := make([]protocol.ProbeResult, len(targets))
	sem := make(chan struct{}, probeParallel)
	var wg sync.WaitGroup
	for i, t := range targets {
		out[i] = protocol.ProbeResult{Addr: t.Addr, Port: t.Port, Kind: t.Kind}
		addr, err := netip.ParseAddr(t.Addr)
		if err != nil || t.Port <= 0 || t.Port > 65535 || !slices.ContainsFunc(allowed, func(p netip.Prefix) bool { return p.Contains(addr.Unmap()) }) {
			out[i].Error = "buiten de scope; niet aangesproken"
			continue
		}
		wg.Add(1)
		go func(r *protocol.ProbeResult) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				r.Error = "geen tijd meer"
				return
			}
			defer func() { <-sem }()
			hostport := net.JoinHostPort(addr.Unmap().String(), strconv.Itoa(r.Port))
			switch r.Kind {
			case protocol.ProbeHTTP, protocol.ProbeHTTPS:
				probeHTTP(ctx, r, hostport, sp.DefaultPasswords)
			case protocol.ProbeRedis:
				probeLine(ctx, r, hostport, "PING\r\n", "+PONG", "redis-noauth")
			case protocol.ProbeMemcached:
				probeLine(ctx, r, hostport, "version\r\n", "VERSION ", "memcached-open")
			default:
				r.Error = "onbekende soort"
			}
		}(&out[i])
	}
	wg.Wait()
	return out, nil
}

func probeLine(ctx context.Context, r *protocol.ProbeResult, hostport, send, want, hit string) {
	d := net.Dialer{Timeout: svcProbeTimeout}
	c, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(svcProbeTimeout))
	if _, err := io.WriteString(c, send); err != nil {
		r.Error = err.Error()
		return
	}
	line, err := bufio.NewReader(io.LimitReader(c, 4096)).ReadString('\n')
	if err != nil && line == "" {
		r.Error = err.Error()
		return
	}
	if strings.HasPrefix(line, want) {
		r.Hits = append(r.Hits, protocol.ProbeHit{ID: hit, Detail: strings.TrimSpace(line)})
	}
}

type httpProbe struct {
	c    *http.Client
	base string
}

func (h *httpProbe) get(ctx context.Context, path string, user, pass string) (int, http.Header, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+path, nil)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", "ClusterForge-scanner")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	return h.do(req)
}

func (h *httpProbe) do(req *http.Request) (int, http.Header, string, error) {
	res, err := h.c.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(res.Body, probeBodyMax))
	return res.StatusCode, res.Header, string(b), nil
}

func probeHTTP(ctx context.Context, r *protocol.ProbeResult, hostport string, defaults bool) {
	scheme := "http"
	if r.Kind == protocol.ProbeHTTPS {
		scheme = "https"
	}
	h := &httpProbe{base: scheme + "://" + hostport, c: &http.Client{
		Timeout: svcProbeTimeout,
		Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true,
			// Alleen lezen wat de dienst toont; een eigen certificaat mag.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // scanner op het eigen netwerk
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Host != hostport {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}}
	status, hdr, body, err := h.get(ctx, "/", "", "")
	if err != nil {
		r.Error = err.Error()
		return
	}
	r.Status, r.Server = status, hdr.Get("Server")
	if m := reTitle.FindStringSubmatch(body); m != nil {
		r.Title = strings.Join(strings.Fields(m[1]), " ")
		if len(r.Title) > 120 {
			r.Title = r.Title[:120]
		}
	}
	hit := func(id, detail string) { r.Hits = append(r.Hits, protocol.ProbeHit{ID: id, Detail: detail}) }
	lower := strings.ToLower(body)

	if strings.Contains(body, "Statistics Report for HAProxy") {
		if strings.Contains(lower, `name="action"`) || strings.Contains(lower, "name=action") {
			hit("haproxy-admin", "statistiekpagina met beheeracties, zonder login")
		} else {
			hit("haproxy-stats", "statistiekpagina zonder login")
		}
	}
	if strings.HasPrefix(r.Title, "Index of /") {
		hit("dirlist", r.Title)
	}
	if strings.Contains(body, "phpMyAdmin") && !strings.Contains(body, "pma_password") && strings.Contains(lower, "navigation") {
		hit("phpmyadmin-open", "phpMyAdmin zonder aanmeldscherm")
	}
	switch r.Port {
	case 2375, 2376:
		if st, _, b, err := h.get(ctx, "/version", "", ""); err == nil && st == 200 && strings.Contains(b, "ApiVersion") {
			hit("docker-api", "Docker-API antwoordt zonder TLS-clientcertificaat")
		}
	case 9200:
		if status == 200 && strings.Contains(body, "cluster_name") {
			hit("elasticsearch-open", "Elasticsearch antwoordt zonder login")
		}
	case 2379:
		if st, _, b, err := h.get(ctx, "/version", "", ""); err == nil && st == 200 && strings.Contains(b, "etcdserver") {
			hit("etcd-open", "etcd antwoordt zonder clientcertificaat")
		}
	case 9090:
		if st, _, b, err := h.get(ctx, "/api/v1/status/buildinfo", "", ""); err == nil && st == 200 && strings.Contains(b, `"version"`) {
			hit("prometheus-open", "Prometheus-API zonder login")
		}
	}
	if strings.Contains(lower, "grafana") {
		if st, _, b, err := h.get(ctx, "/api/search?limit=1", "", ""); err == nil && st == 200 && strings.HasPrefix(strings.TrimSpace(b), "[") {
			hit("grafana-anon", "Grafana laat anoniem dashboards zien")
		}
		if defaults {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/login", strings.NewReader(`{"user":"admin","password":"admin"}`))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "ClusterForge-scanner")
				if st, _, b, err := h.do(req); err == nil && st == 200 && strings.Contains(b, "Logged in") {
					hit("grafana-default", "admin/admin werkt")
				}
			}
		}
	}
	if st, _, b, err := h.get(ctx, "/api/rawdata", "", ""); err == nil && st == 200 && strings.Contains(b, `"routers"`) {
		hit("traefik-api", "Traefik-API zonder login")
	}
	if defaults && status == http.StatusUnauthorized && strings.HasPrefix(strings.ToLower(hdr.Get("WWW-Authenticate")), "basic") {
		if st, _, _, err := h.get(ctx, "/", "admin", "admin"); err == nil && st >= 200 && st < 400 {
			hit("basic-default", "admin/admin werkt")
		}
	}
}
