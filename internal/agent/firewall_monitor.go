package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/florianl/go-nflog/v2"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Monitoring van de firewall (fase 16): de agent telt de nieuwe
// verbindingen die ufw weggooit. De telregel in after.rules stuurt ze naar
// NFLOG-groep 4711; NFLOG werkt per netwerknamespace, dus ook in een LXC
// (de kernel-log van ufw komt daar niet aan). Lukt NFLOG niet, dan leest de
// agent "[UFW BLOCK]" uit de kernel-log.

// fwMonitor houdt de tellers bij tot de server ze ophaalt.
type fwMonitor struct {
	mu      sync.Mutex
	mode    string // nflog, kernel-log of off
	err     string
	since   time.Time
	hits    map[string]*fwHit
	dropped int64
	// journalSince is tot waar de kernel-log gelezen is.
	journalSince time.Time
}

type fwHit struct {
	src, proto  string
	port        int
	count       int64
	first, last time.Time
}

func (m *fwMonitor) add(src, proto string, port int, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hits == nil {
		m.hits = map[string]*fwHit{}
	}
	k := src + "|" + proto + "|" + strconv.Itoa(port)
	h, ok := m.hits[k]
	if !ok {
		if len(m.hits) >= maxFirewallHitsInMemory {
			m.dropped++
			return
		}
		h = &fwHit{src: src, proto: proto, port: port, first: at}
		m.hits[k] = h
	}
	h.count++
	h.last = at
}

const maxFirewallHitsInMemory = 5000

// OpenNflog luistert op een NFLOG-groep en roept fn aan met de payload
// (de IP-header en meer) van elk pakket, tot ctx afloopt. Het geeft een fout
// als luisteren niet kan.
type OpenNflog func(ctx context.Context, group uint16, fn func(payload []byte)) error

func openNflog(ctx context.Context, group uint16, fn func(payload []byte)) error {
	nf, err := nflog.Open(&nflog.Config{Group: group, Copymode: nflog.CopyPacket, Bufsize: 128})
	if err != nil {
		return err
	}
	_ = nf.Con.SetReadBuffer(1 << 20)
	err = nf.RegisterWithErrorFunc(ctx, func(at nflog.Attribute) int {
		if at.Payload != nil {
			fn(*at.Payload)
		}
		return 0
	}, func(error) int { return 0 })
	if err != nil {
		_ = nf.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		_ = nf.Close()
	}()
	return nil
}

// firewallMonitor start het tellen; via NFLOG als dat kan.
func (a *Agent) firewallMonitor(ctx context.Context) {
	open := a.OpenNflog
	if open == nil {
		open = openNflog
	}
	now := a.now()
	err := open(ctx, protocol.FirewallNflogGroup, func(p []byte) {
		if src, proto, port, ok := parsePacket(p); ok {
			a.fw.add(src, proto, port, a.now())
		}
	})
	a.fw.mu.Lock()
	defer a.fw.mu.Unlock()
	a.fw.since, a.fw.journalSince = now, now
	if err == nil {
		a.fw.mode, a.fw.err = "nflog", ""
		return
	}
	a.fw.mode, a.fw.err = "kernel-log", "NFLOG: "+err.Error()
	a.Log.Info("firewallverkeer tellen via NFLOG lukt niet; de kernel-log wordt gebruikt", "err", err)
}

// parsePacket haalt bron, protocol en doelpoort uit een IPv4- of
// IPv6-pakket.
func parsePacket(p []byte) (src, proto string, port int, ok bool) {
	if len(p) < 1 {
		return "", "", 0, false
	}
	var l4 []byte
	var num byte
	switch p[0] >> 4 {
	case 4:
		if len(p) < 20 {
			return "", "", 0, false
		}
		ihl := int(p[0]&0x0f) * 4
		a, _ := netip.AddrFromSlice(p[12:16])
		src, num = a.String(), p[9]
		if ihl >= 20 && len(p) >= ihl {
			l4 = p[ihl:]
		}
	case 6:
		if len(p) < 40 {
			return "", "", 0, false
		}
		a, _ := netip.AddrFromSlice(p[8:24])
		src, num, l4 = a.String(), p[6], p[40:]
	default:
		return "", "", 0, false
	}
	proto = ipProtoName(num)
	if (num == 6 || num == 17) && len(l4) >= 4 {
		port = int(binary.BigEndian.Uint16(l4[2:4]))
	}
	return src, proto, port, true
}

func ipProtoName(n byte) string {
	switch n {
	case 1:
		return "icmp"
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 58:
		return "icmpv6"
	case 112:
		return "vrrp"
	case 2:
		return "igmp"
	case 47:
		return "gre"
	case 50:
		return "esp"
	}
	return strconv.Itoa(int(n))
}

var ufwBlockField = regexp.MustCompile(`\b(SRC|PROTO|DPT)=(\S+)`)

// parseUfwBlocks leest "[UFW BLOCK]"-regels van journalctl -o short-unix.
func parseUfwBlocks(out []byte, add func(src, proto string, port int, at time.Time)) time.Time {
	var last time.Time
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "[UFW BLOCK]") {
			continue
		}
		ts, _, _ := strings.Cut(line, " ")
		sec, err := strconv.ParseFloat(ts, 64)
		if err != nil {
			continue
		}
		at := time.Unix(0, int64(sec*float64(time.Second))).UTC()
		var src, proto string
		port := 0
		for _, m := range ufwBlockField.FindAllStringSubmatch(line, -1) {
			switch m[1] {
			case "SRC":
				src = m[2]
			case "PROTO":
				proto = strings.ToLower(m[2])
			case "DPT":
				port, _ = strconv.Atoi(m[2])
			}
		}
		if src == "" {
			continue
		}
		if proto == "112" {
			proto = "vrrp"
		}
		add(src, proto, port, at)
		if at.After(last) {
			last = at
		}
	}
	return last
}

// fwTake geeft de tellers sinds de vorige keer en begint opnieuw.
func (a *Agent) fwTake(ctx context.Context) (b protocol.FirewallBlocked) {
	a.fw.mu.Lock()
	mode, since := a.fw.mode, a.fw.journalSince
	a.fw.mu.Unlock()
	if mode == "kernel-log" {
		out, err := a.stdout(ctx, "journalctl", "-k", "-q", "--no-pager", "-o", "short-unix",
			fmt.Sprintf("--since=@%d", since.Unix()))
		if err == nil {
			last := parseUfwBlocks(out, func(src, proto string, port int, at time.Time) {
				if at.After(since) {
					a.fw.add(src, proto, port, at)
				}
			})
			if !last.IsZero() {
				a.fw.mu.Lock()
				a.fw.journalSince = last
				a.fw.mu.Unlock()
			}
		}
	}
	now := a.now()
	a.fw.mu.Lock()
	defer a.fw.mu.Unlock()
	b = protocol.FirewallBlocked{Source: mode, Since: a.fw.since, Dropped: a.fw.dropped, Hits: []protocol.FirewallHit{}}
	if b.Source == "" {
		b.Source = "off"
	}
	for _, h := range a.fw.hits {
		b.Hits = append(b.Hits, protocol.FirewallHit{Src: h.src, Proto: h.proto, Port: h.port, Count: h.count, First: h.first, Last: h.last})
	}
	sort.Slice(b.Hits, func(i, j int) bool {
		if b.Hits[i].Count != b.Hits[j].Count {
			return b.Hits[i].Count > b.Hits[j].Count
		}
		return b.Hits[i].Src+strconv.Itoa(b.Hits[i].Port) < b.Hits[j].Src+strconv.Itoa(b.Hits[j].Port)
	})
	a.fw.hits, a.fw.dropped, a.fw.since = nil, 0, now
	return b
}
