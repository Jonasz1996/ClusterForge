package agent

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func target(t *testing.T, raw, kind string) protocol.ProbeTarget {
	t.Helper()
	u, _ := url.Parse(raw)
	port, _ := strconv.Atoi(u.Port())
	return protocol.ProbeTarget{Addr: u.Hostname(), Port: port, Kind: kind}
}

func TestProbeServices(t *testing.T) {
	haproxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>Statistics Report for HAProxy</title>Statistics Report for HAProxy <select name="action"></select></html>`))
	}))
	defer haproxy.Close()
	var logins int
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search":
			_, _ = w.Write([]byte(`[]`))
		case "/login":
			logins++
			_, _ = w.Write([]byte(`{"message":"Logged in"}`))
		default:
			_, _ = w.Write([]byte(`<title>Grafana</title>`))
		}
	}))
	defer grafana.Close()
	basic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "admin" && p == "admin" {
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="router"`)
		w.WriteHeader(401)
	}))
	defer basic.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(c).ReadString('\n')
			_, _ = c.Write([]byte("+PONG\r\n"))
			_ = c.Close()
		}
	}()
	redis := protocol.ProbeTarget{Addr: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Kind: protocol.ProbeRedis}

	a := &Agent{}
	sp := &protocol.ProbeSpec{Allowed: []string{"127.0.0.0/8"}, Targets: []protocol.ProbeTarget{
		target(t, haproxy.URL, "http"), target(t, grafana.URL, "http"), target(t, basic.URL, "http"), redis,
		{Addr: "10.9.9.9", Port: 80, Kind: "http"},
	}}
	res, err := a.probeServices(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(r protocol.ProbeResult) []string {
		var out []string
		for _, h := range r.Hits {
			out = append(out, h.ID)
		}
		return out
	}
	if got := ids(res[0]); len(got) != 1 || got[0] != "haproxy-admin" {
		t.Errorf("haproxy %v", got)
	}
	if got := ids(res[1]); len(got) != 1 || got[0] != "grafana-anon" || logins != 0 {
		t.Errorf("grafana zonder standaardwachtwoord: %v, %d pogingen", got, logins)
	}
	if got := ids(res[2]); len(got) != 0 {
		t.Errorf("basic zonder opt-in %v", got)
	}
	if got := ids(res[3]); len(got) != 1 || got[0] != "redis-noauth" {
		t.Errorf("redis %v", got)
	}
	if res[4].Error == "" || len(res[4].Hits) != 0 {
		t.Errorf("buiten scope: %+v", res[4])
	}

	sp.DefaultPasswords = true
	res, _ = a.probeServices(context.Background(), sp)
	if got := ids(res[1]); len(got) != 2 || logins != 1 {
		t.Errorf("grafana met standaardwachtwoord: %v, %d pogingen", got, logins)
	}
	if got := ids(res[2]); len(got) != 1 || got[0] != "basic-default" {
		t.Errorf("basic %v", got)
	}
}
