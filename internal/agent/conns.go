package agent

import (
	"net"
	"sort"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ConnTracker telt de gevestigde TCP-verbindingen tussen twee berichten
// conns. Er gaan geen PID's, commandoregels of inhoud mee.
type ConnTracker struct {
	mu    sync.Mutex
	since time.Time
	out   map[protocol.ConnOut]int
	in    map[protocol.ConnIn]int
}

// Sample bekijkt één keer de gevestigde verbindingen in /proc/net/tcp en
// tcp6. Een verbinding naar een eigen luisterende poort is inkomend; de
// afzender telt alleen mee als die een privé-adres heeft. Een uitgaande
// verbinding naar een tijdelijke poort telt niet.
func (c *Collector) SampleConns(t *ConnTracker, now time.Time) {
	socks := c.sockets("tcp")
	listening := map[int]bool{}
	for _, s := range socks {
		if s.state == "0A" {
			listening[s.lport] = true
		}
	}
	lo, hi := c.ephemeral()
	units := c.socketUnits()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.out == nil {
		t.out, t.in, t.since = map[protocol.ConnOut]int{}, map[protocol.ConnIn]int{}, now
	}
	for _, s := range socks {
		// 01 is ESTABLISHED.
		if s.state != "01" {
			continue
		}
		unit := units[s.inode]
		if listening[s.lport] {
			k := protocol.ConnIn{Unit: unit, Port: s.lport}
			if private(s.rem) {
				k.Peer = s.rem.String()
			}
			t.in[k]++
			continue
		}
		if s.rport >= lo && s.rport <= hi {
			continue
		}
		t.out[protocol.ConnOut{Unit: unit, Addr: s.rem.String(), Port: s.rport}]++
	}
}

func private(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// Take geeft de samenvatting sinds de vorige keer en begint opnieuw. Leeg
// als er niets bekeken is.
func (t *ConnTracker) Take(now time.Time) (protocol.Conns, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.out == nil {
		return protocol.Conns{}, false
	}
	res := protocol.Conns{Since: t.since.UTC(), Until: now.UTC(), Out: []protocol.ConnOut{}, In: []protocol.ConnIn{}}
	for k, n := range t.out {
		k.Count = n
		res.Out = append(res.Out, k)
	}
	for k, n := range t.in {
		k.Count = n
		res.In = append(res.In, k)
	}
	sort.Slice(res.Out, func(i, j int) bool {
		a, b := res.Out[i], res.Out[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Addr != b.Addr {
			return a.Addr < b.Addr
		}
		return a.Port < b.Port
	})
	sort.Slice(res.In, func(i, j int) bool {
		a, b := res.In[i], res.In[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Peer < b.Peer
	})
	if len(res.Out) > protocol.MaxConns {
		res.Out = res.Out[:protocol.MaxConns]
	}
	if len(res.In) > protocol.MaxConns {
		res.In = res.In[:protocol.MaxConns]
	}
	t.out, t.in = nil, nil
	return res, true
}
