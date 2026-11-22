package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeNet speelt ip, nft en tc na.
type fakeNet struct {
	mu    sync.Mutex
	calls []string
	qdisc map[string]string
	fail  map[string]string
}

func (n *fakeNet) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	for prefix, out := range n.fail {
		if strings.HasPrefix(call, prefix) {
			return []byte(out), errors.New("exit status 1")
		}
	}
	if strings.HasPrefix(call, "tc qdisc show dev ") {
		q := n.qdisc[args[3]]
		if q == "" {
			q = "fq_codel"
		}
		return []byte("qdisc " + q + " 0: root refcnt 2 limit 10240p\n"), nil
	}
	n.calls = append(n.calls, call)
	switch {
	case strings.HasPrefix(call, "tc qdisc replace dev "):
		n.qdisc[args[3]] = "netem"
	case strings.HasPrefix(call, "tc qdisc del dev "):
		delete(n.qdisc, args[3])
	}
	return nil, nil
}

func (n *fakeNet) Calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.calls)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestFaults(t *testing.T) (*Faults, *fakeNet, *clock) {
	t.Helper()
	dir := t.TempDir()
	n := &fakeNet{qdisc: map[string]string{}, fail: map[string]string{}}
	c := &clock{t: time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)}
	f := NewFaults(filepath.Join(dir, "agent-state.json"), n.exec, nil)
	f.Now, f.Delay = c.Now, 10*time.Millisecond
	f.Interfaces = func() []protocol.Interface {
		return []protocol.Interface{{Name: "lo"}, {Name: "eth0", Addresses: []string{"10.0.20.11/24"}}, {Name: "eth1"}}
	}
	f.CutOff = func(name string) bool { return name == "eth0" }
	return f, n, c
}

func (f *Faults) script(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, nftScript))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFaultLease(t *testing.T) {
	f, n, c := newTestFaults(t)
	ctx := context.Background()
	res := f.Inject(ctx, protocol.Fault{ID: "run1/0", Kind: protocol.FaultVRRPBlock, LeaseSeconds: 30, MaxSeconds: 120})
	if !res.OK || !slices.Equal(n.Calls(), []string{"nft -f " + filepath.Join(f.Dir, nftScript)}) {
		t.Fatalf("inject: %+v %v", res, n.Calls())
	}
	if s := f.script(t); !strings.Contains(s, "delete table inet cf_fault") ||
		strings.Count(s, `meta l4proto 112 drop comment "run1/0"`) != 2 || !strings.Contains(s, "hook input priority -10") {
		t.Fatalf("script:\n%s", s)
	}
	// Hetzelfde id nog eens: niets nieuws.
	if res := f.Inject(ctx, protocol.Fault{ID: "run1/0", Kind: protocol.FaultVRRPBlock, MaxSeconds: 120}); !res.OK || !res.Repeat || len(n.Calls()) != 1 {
		t.Fatalf("herhaald: %+v", res)
	}
	if got := f.Active(); !slices.Equal(got, []string{"run1/0"}) {
		t.Fatalf("actief: %v", got)
	}

	// Verlengd: na 25 s blijft hij, en met de verlenging ook na 40 s.
	c.Add(25 * time.Second)
	f.Expire(ctx)
	if res := f.Renew([]string{"run1/0"}); !res.OK {
		t.Fatalf("renew: %+v", res)
	}
	c.Add(15 * time.Second)
	f.Expire(ctx)
	if len(f.Active()) != 1 {
		t.Fatal("verlengde storing verdween")
	}

	// Zonder verlenging (de server is weg) verdwijnt hij na de lease.
	c.Add(16 * time.Second)
	f.Expire(ctx)
	if len(f.Active()) != 0 || n.Calls()[len(n.Calls())-1] != "nft delete table inet cf_fault" {
		t.Fatalf("na de lease: %v %v", f.Active(), n.Calls())
	}
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Fatalf("state blijft staan: %v", err)
	}
	if res := f.Renew([]string{"run1/0"}); res.OK || !strings.Contains(res.Error, "niet meer actief: run1/0") {
		t.Fatalf("renew na het verlopen: %+v", res)
	}

	// De bovengrens geldt ook als de server blijft verlengen.
	f.Inject(ctx, protocol.Fault{ID: "run2/0", Kind: protocol.FaultPortBlock, Port: 5432, MaxSeconds: 40})
	for range 5 {
		c.Add(10 * time.Second)
		f.Renew([]string{"run2/0"})
		f.Expire(ctx)
	}
	if len(f.Active()) != 0 {
		t.Fatal("de bovengrens werd niet gehaald")
	}
}

func TestFaultNftTogether(t *testing.T) {
	f, n, _ := newTestFaults(t)
	ctx := context.Background()
	f.Inject(ctx, protocol.Fault{ID: "a", Kind: protocol.FaultVRRPBlock, MaxSeconds: 60})
	f.Inject(ctx, protocol.Fault{ID: "b", Kind: protocol.FaultPortBlock, Port: 80, MaxSeconds: 60})
	if s := f.script(t); !strings.Contains(s, `iifname != "lo" tcp dport 80 drop comment "b"`) || !strings.Contains(s, `comment "a"`) {
		t.Fatalf("samen:\n%s", s)
	}
	if res := f.Clear(ctx, []string{"a", "onbekend"}); !res.OK {
		t.Fatalf("clear: %+v", res)
	}
	if s := f.script(t); strings.Contains(s, `comment "a"`) || !strings.Contains(s, `comment "b"`) {
		t.Fatalf("na het opheffen van a:\n%s", s)
	}
	if res := f.Clear(ctx, nil); !res.OK || n.Calls()[len(n.Calls())-1] != "nft delete table inet cf_fault" {
		t.Fatalf("alles: %+v %v", res, n.Calls())
	}

	// nft faalt: dan blijft er niets achter in de state.
	n.fail["nft -f"] = "Error: syntax error"
	if res := f.Inject(ctx, protocol.Fault{ID: "c", Kind: protocol.FaultVRRPBlock, MaxSeconds: 60}); res.OK || !strings.Contains(res.Error, "nft -f") {
		t.Fatalf("nft faalt: %+v", res)
	}
	if len(f.Active()) != 0 {
		t.Fatal("mislukte storing blijft in de state")
	}
}

func TestFaultCutOff(t *testing.T) {
	f, n, c := newTestFaults(t)
	ctx := context.Background()
	// Op de interface naar de server hoogstens 2 minuten.
	if res := f.Inject(ctx, protocol.Fault{ID: "l", Kind: protocol.FaultLinkDown, Interface: "eth0", MaxSeconds: 300}); res.OK ||
		!strings.Contains(res.Error, "eth0 is de interface naar ClusterForge; daar duurt link_down hoogstens 120 s") {
		t.Fatalf("te lang: %+v", res)
	}
	res := f.Inject(ctx, protocol.Fault{ID: "l", Kind: protocol.FaultLinkDown, Interface: "eth0", MaxSeconds: 90})
	if !res.OK || len(n.Calls()) != 0 || !strings.Contains(res.Output[0], "eth0 gaat over 10 ms down") {
		t.Fatalf("link_down: %+v %v", res, n.Calls())
	}
	// Het antwoord gaat eerst; dan pas de storing.
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(n.Calls(), "ip link set dev eth0 down") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Contains(n.Calls(), "ip link set dev eth0 down") {
		t.Fatalf("niet down: %v", n.Calls())
	}
	// Geen lease: de server kan niet verlengen, dus na 30 s blijft hij.
	c.Add(60 * time.Second)
	f.Expire(ctx)
	if len(f.Active()) != 1 {
		t.Fatal("link_down verdween voor de bovengrens")
	}

	// De machine herstart voor de bovengrens: bij de start gaat hij weg.
	g := NewFaults(filepath.Join(f.Dir, "agent-state.json"), n.exec, nil)
	if res := g.ClearAll(ctx); !res.OK || n.Calls()[len(n.Calls())-1] != "ip link set dev eth0 up" ||
		!strings.Contains(strings.Join(res.Output, "; "), "eth0 down achtergebleven, teruggedraaid") {
		t.Fatalf("na een herstart: %+v %v", res, n.Calls())
	}
	if len(g.Active()) != 0 || len(f.Active()) != 0 {
		t.Fatal("state niet leeg")
	}

	// Zonder herstart komt hij na de bovengrens vanzelf terug.
	f.Inject(ctx, protocol.Fault{ID: "l2", Kind: protocol.FaultLinkDown, Interface: "eth0", MaxSeconds: 90})
	c.Add(91 * time.Second)
	f.Expire(ctx)
	if len(f.Active()) != 0 || n.Calls()[len(n.Calls())-1] != "ip link set dev eth0 up" {
		t.Fatalf("na de bovengrens: %v %v", f.Active(), n.Calls())
	}

	// Een andere interface heeft gewoon een lease, en gaat meteen down.
	before := len(n.Calls())
	if res := f.Inject(ctx, protocol.Fault{ID: "l3", Kind: protocol.FaultLinkDown, Interface: "eth1", MaxSeconds: 600}); !res.OK ||
		n.Calls()[before] != "ip link set dev eth1 down" {
		t.Fatalf("eth1: %+v %v", res, n.Calls())
	}
	c.Add(31 * time.Second)
	f.Expire(ctx)
	if len(f.Active()) != 0 {
		t.Fatal("eth1 bleef zonder lease down")
	}
}

func TestFaultNetemAndDisk(t *testing.T) {
	f, n, _ := newTestFaults(t)
	ctx := context.Background()
	n.qdisc["eth1"] = "htb"
	if res := f.Inject(ctx, protocol.Fault{ID: "n", Kind: protocol.FaultNetem, Interface: "eth1", DelayMS: 200, MaxSeconds: 60}); res.OK ||
		!strings.Contains(res.Error, "op eth1 staat al een eigen qdisc (htb)") {
		t.Fatalf("eigen qdisc: %+v", res)
	}
	res := f.Inject(ctx, protocol.Fault{ID: "n", Kind: protocol.FaultNetem, Interface: "eth0", DelayMS: 200, LossPercent: 5, MaxSeconds: 60})
	if !res.OK || n.Calls()[len(n.Calls())-1] != "tc qdisc replace dev eth0 root netem delay 200ms loss 5%" {
		t.Fatalf("netem: %+v %v", res, n.Calls())
	}
	if res := f.Clear(ctx, []string{"n"}); !res.OK || n.Calls()[len(n.Calls())-1] != "tc qdisc del dev eth0 root" {
		t.Fatalf("netem weg: %+v %v", res, n.Calls())
	}

	// De schijf: 10 MiB groot, 2 MiB gebruikt; tot 50 % is 3 MiB erbij.
	const mib = 1 << 20
	f.Statfs = func(string) (fsStat, bool) { return fsStat{size: 10 * mib, free: 8 * mib, avail: 8 * mib}, true }
	res = f.Inject(ctx, protocol.Fault{ID: "d", Kind: protocol.FaultDiskFill, FillPercent: 50, MaxSeconds: 60})
	fi, err := os.Stat(filepath.Join(f.Dir, fillFile))
	if !res.OK || err != nil || fi.Size() != 3*mib {
		t.Fatalf("disk_fill: %+v %v %v", res, fi, err)
	}
	if res := f.Clear(ctx, nil); !res.OK {
		t.Fatalf("disk weg: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, fillFile)); !os.IsNotExist(err) {
		t.Fatalf("vulbestand blijft: %v", err)
	}
	// Al voller dan gevraagd: niets schrijven.
	f.Statfs = func(string) (fsStat, bool) { return fsStat{size: 10 * mib, free: 1 * mib, avail: 1 * mib}, true }
	if res := f.Inject(ctx, protocol.Fault{ID: "d2", Kind: protocol.FaultDiskFill, FillPercent: 50, MaxSeconds: 60}); !res.OK ||
		!strings.Contains(res.Output[0], "de schijf stond al op 90 %") {
		t.Fatalf("al vol: %+v", res)
	}
}

func TestFaultValidate(t *testing.T) {
	f, _, _ := newTestFaults(t)
	ctx := context.Background()
	f.Inject(ctx, protocol.Fault{ID: "v", Kind: protocol.FaultVRRPBlock, MaxSeconds: 60})
	for want, fault := range map[string]protocol.Fault{
		"ongeldig id":                       {ID: "a b", Kind: protocol.FaultVRRPBlock, MaxSeconds: 60},
		"hoogstens 600 s":                   {ID: "x", Kind: protocol.FaultVRRPBlock, MaxSeconds: 601},
		"de loopback-interface mag niet":    {ID: "x", Kind: protocol.FaultLinkDown, Interface: "lo", MaxSeconds: 60},
		"interface eth9 bestaat niet":       {ID: "x", Kind: protocol.FaultLinkDown, Interface: "eth9", MaxSeconds: 60},
		"ongeldige interface":               {ID: "x", Kind: protocol.FaultNetem, Interface: "eth0; reboot", DelayMS: 1, MaxSeconds: 60},
		"vertraging tot 2000 ms":            {ID: "x", Kind: protocol.FaultNetem, Interface: "eth1", DelayMS: 2001, MaxSeconds: 60},
		"minstens een van beide":            {ID: "x", Kind: protocol.FaultNetem, Interface: "eth1", MaxSeconds: 60},
		"disk_fill gaat hoogstens tot 95 %": {ID: "x", Kind: protocol.FaultDiskFill, FillPercent: 96, MaxSeconds: 60},
		"ongeldige poort":                   {ID: "x", Kind: protocol.FaultPortBlock, Port: 70000, MaxSeconds: 60},
		"VRRP is al geblokkeerd":            {ID: "x", Kind: protocol.FaultVRRPBlock, MaxSeconds: 60},
		`onbekende storing "reboot"`:        {ID: "x", Kind: "reboot", MaxSeconds: 60},
		"de lease ligt tussen 1 en 120 s":   {ID: "x", Kind: protocol.FaultPortBlock, Port: 22, LeaseSeconds: 500, MaxSeconds: 60},
	} {
		if res := f.Inject(ctx, fault); res.OK || !strings.Contains(res.Error, want) {
			t.Errorf("%s: %+v", want, res)
		}
	}
}

// De agent neemt de commando's aan en ruimt bij de start op.
func TestAgentFaultCommands(t *testing.T) {
	dir := t.TempDir()
	n := &fakeNet{qdisc: map[string]string{}, fail: map[string]string{}}
	a := &Agent{StatePath: filepath.Join(dir, "agent-state.json"), Exec: n.exec, Log: slog.New(slog.DiscardHandler)}
	if res := a.handleCommand(command(t, protocol.Command{ID: "c1", Action: protocol.CmdFaultInject})); res.OK {
		t.Fatalf("zonder storing: %+v", res)
	}
	res := a.handleCommand(command(t, protocol.Command{ID: "c2", Action: protocol.CmdFaultInject,
		Fault: &protocol.Fault{ID: "r/0", Kind: protocol.FaultVRRPBlock, MaxSeconds: 60}}))
	if !res.OK || !slices.Equal(a.faults.Active(), []string{"r/0"}) {
		t.Fatalf("inject: %+v", res)
	}
	if res := a.handleCommand(command(t, protocol.Command{ID: "c3", Action: protocol.CmdFaultRenew, FaultIDs: []string{"r/0"}})); !res.OK {
		t.Fatalf("renew: %+v", res)
	}
	// Een nieuwe agent (na een crash) ruimt bij de start op.
	b := &Agent{StatePath: a.StatePath, Exec: n.exec, Log: slog.New(slog.DiscardHandler)}
	b.initFaults()
	if res := b.faults.ClearAll(context.Background()); !res.OK || len(b.faults.Active()) != 0 ||
		n.Calls()[len(n.Calls())-1] != "nft delete table inet cf_fault" {
		t.Fatalf("bij de start: %+v %v", res, n.Calls())
	}
	if res := a.handleCommand(command(t, protocol.Command{ID: "c4", Action: protocol.CmdFaultClear})); !res.OK {
		t.Fatalf("clear zonder storingen: %+v", res)
	}
}
