package disaster

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// seqNodes zijn web01 (VIP-houder) en web02 met keepalived en nginx, één
// interface eth0 met het adres naar ClusterForge en het VIP-netwerk.
func seqNodes() []faultNode {
	mk := func(name string, owner bool) faultNode {
		return faultNode{
			NodeOption: NodeOption{
				ID: uuid.New(), Hostname: name, Owner: owner, Protocol: protocol.FaultsSince, Nft: true, Tc: true,
				Interfaces: []string{"eth0", "eth1"}, Primary: "eth0", Units: []string{"keepalived", "nginx"},
			},
			services:  map[string]string{"keepalived": "active", "nginx": "active"},
			addressed: map[string]bool{"eth0": true}, vipIface: "eth0",
		}
	}
	return []faultNode{mk("web01", true), mk("web02", false)}
}

func seqOptions(nodes []faultNode) Options {
	return Options{Allowed: true, VIPs: []string{"10.0.30.100"}, Nodes: options(nodes), Actions: actionOptions(nodes)}
}

func TestValidateSteps(t *testing.T) {
	nodes := seqNodes()
	o := seqOptions(nodes)
	web02 := nodes[1].ID
	in := Input{Name: "reeks", Kind: Sequence, Unit: "nginx", FaultSeconds: 90, RecoverySeconds: 120, Steps: []Step{
		{AtSeconds: 30, Action: ActServiceStop, On: OnNode, NodeID: &web02, Unit: "keepalived", Port: 80},
		{AtSeconds: 0, Action: protocol.FaultVRRPBlock, On: OnOwner, Interface: "eth0"},
	}}
	if err := in.validate(o); err != nil {
		t.Fatal(err)
	}
	// Op volgorde van tijdstip, met alleen de velden van de actie.
	want := []Step{
		{AtSeconds: 0, Action: protocol.FaultVRRPBlock, On: OnOwner},
		{AtSeconds: 30, Action: ActServiceStop, On: OnNode, NodeID: &web02, Hostname: "web02", Unit: "keepalived"},
	}
	if in.Unit != "" || len(in.Steps) != 2 || in.Steps[0] != want[0] || in.Steps[1].Hostname != "web02" || in.Steps[1].Port != 0 {
		t.Fatalf("stappen: %+v", in.Steps)
	}
	if d := Describe(Sequence, in.params()); d != "0 s: VRRP blokkeren op de VIP-houder; 30 s: keepalived stoppen op web02" {
		t.Fatalf("beschrijving: %q", d)
	}

	for msg, steps := range map[string][]Step{
		"minstens één stap":                       nil,
		"hoogstens 5 stappen":                     slices.Repeat([]Step{{Action: protocol.FaultVRRPBlock, On: OnAll}}, 6),
		"stap 1: het tijdstip":                    {{AtSeconds: 90, Action: protocol.FaultVRRPBlock, On: OnAll}},
		"stap 1: kies wat":                        {{Action: "reboot", On: OnAll}},
		"stap 1: kies op welke":                   {{Action: protocol.FaultVRRPBlock, On: "half"}},
		"stap 1: kies een node":                   {{Action: protocol.FaultVRRPBlock, On: OnNode}},
		"stap 1: kies een interface":              {{Action: protocol.FaultLinkDown, On: OnAll, Interface: "eth0; reboot"}},
		"stap 1: geef een vertraging":             {{Action: protocol.FaultNetem, On: OnAll, Interface: "eth0", DelayMS: 5000}},
		"stap 1: de poort":                        {{Action: protocol.FaultPortBlock, On: OnAll, Port: 70000}},
		"stap 1: vul de schijf":                   {{Action: protocol.FaultDiskFill, On: OnAll, FillPercent: 99}},
		"stap 1: kies welke dienst":               {{Action: ActServiceStop, On: OnAll, Unit: "sshd"}},
		"stap 1: web01 heeft geen interface eth9": {{Action: protocol.FaultLinkDown, On: OnOwner, Interface: "eth9"}},
		// link_down op de interface naar ClusterForge duurt hoogstens 120 s.
		"ten vroegste op": {{AtSeconds: 0, Action: protocol.FaultLinkDown, On: OnNode, NodeID: &web02, Interface: "eth0"}},
		"stap 1 en 2 doen allebei iets met vrrp_block op web01": {
			{Action: protocol.FaultVRRPBlock, On: OnOwner}, {AtSeconds: 5, Action: protocol.FaultVRRPBlock, On: OnAll},
		},
		"na stap 1 is web02 afgesneden": {
			{AtSeconds: 0, Action: protocol.FaultLinkDown, On: OnOthers, Interface: "eth0"}, {AtSeconds: 1, Action: ActServiceStop, On: OnAll, Unit: "nginx"},
		},
	} {
		in := Input{Name: "reeks", Kind: Sequence, FaultSeconds: 90, RecoverySeconds: 120, Steps: steps}
		if strings.Contains(msg, "ten vroegste") {
			in.FaultSeconds = 200
		}
		var fe *failover.FieldError
		if err := in.validate(o); !errors.As(err, &fe) || fe.Field != "steps" || !strings.Contains(fe.Message, msg) {
			t.Errorf("%s: %v", msg, err)
		}
	}
	// Een agent zonder protocol 6 en zonder tc: de acties zijn grijs.
	old := seqNodes()
	old[0].Protocol, old[1].Protocol = 5, 5
	oo := seqOptions(old)
	in = Input{Name: "reeks", Kind: Sequence, FaultSeconds: 90, RecoverySeconds: 120, Steps: []Step{{Action: protocol.FaultVRRPBlock, On: OnAll}}}
	if err := in.validate(oo); err == nil || !strings.Contains(err.Error(), "protocol 6") {
		t.Fatalf("oude agent: %v", err)
	}
	if i := slices.IndexFunc(oo.Actions, func(a ActionOption) bool { return a.Key == ActServiceStop }); !oo.Actions[i].Available {
		t.Fatalf("dienst stoppen kan met elke agent: %+v", oo.Actions[i])
	}
	notc := seqNodes()
	notc[0].Tc, notc[1].Tc = false, false
	for _, a := range actionOptions(notc) {
		if a.Available == (a.Key == protocol.FaultNetem) {
			t.Errorf("zonder tc: %+v", a)
		}
	}
}

// plan zet stappen om in een definitie zoals seqTargets dat doet.
func planSteps(nodes []faultNode, fault int, steps ...Step) Definition {
	def := Definition{Kind: Sequence, Expect: Expect{FaultSeconds: fault, RecoverySeconds: 120}}
	for _, st := range steps {
		ps := PlannedStep{Step: st, Text: st.Text()}
		for _, n := range resolve(st, options(nodes)) {
			ps.Nodes = append(ps.Nodes, StepNode{NodeID: n.ID, Hostname: n.Hostname, CutOff: st.Action == protocol.FaultLinkDown && n.cutOff(st.Interface)})
		}
		def.Steps = append(def.Steps, ps)
	}
	return def
}

func TestSimulate(t *testing.T) {
	probes := []failover.Probe{failover.DefaultProbe}
	vips := []string{"10.0.30.100"}
	for _, tc := range []struct {
		name   string
		steps  []Step
		status string
		reason string
		keys   []string
	}{
		{"vrrp op alle nodes", []Step{{Action: protocol.FaultVRRPBlock, On: OnAll}}, "split_brain", "na stap 1: VRRP geblokkeerd op web01 en web02", nil},
		{"vrrp op de houder", []Step{{Action: protocol.FaultVRRPBlock, On: OnOwner}}, "split_brain", "VRRP geblokkeerd op web01:", nil},
		// Daarna keepalived weg op web02: het split-brain was er al.
		{"vrrp en keepalived", []Step{{Action: protocol.FaultVRRPBlock, On: OnOwner}, {AtSeconds: 30, Action: ActServiceStop, On: OnOthers, Unit: "keepalived"}},
			"split_brain", "na stap 1:", nil},
		{"keepalived op de houder", []Step{{Action: ActServiceStop, On: OnOwner, Unit: "keepalived"}}, "degraded", "keepalived draait niet op web01", nil},
		{"keepalived overal", []Step{{Action: ActServiceStop, On: OnAll, Unit: "keepalived"}}, "down", "10.0.30.100 heeft geen houder", nil},
		{"nginx overal", []Step{{Action: ActServiceStop, On: OnAll, Unit: "nginx"}}, "down", "nginx staat op alle nodes stil", []string{"unit:nginx"}},
		{"poort 80 dicht op de houder", []Step{{Action: protocol.FaultPortBlock, On: OnOwner, Port: 80}}, "down", "poort 80 is dicht op web01", nil},
		{"poort 80 dicht op de andere", []Step{{Action: protocol.FaultPortBlock, On: OnOthers, Port: 80}}, "healthy", "", nil},
		{"poort 8080 op de houder", []Step{{Action: protocol.FaultPortBlock, On: OnOwner, Port: 8080}}, "healthy", "", nil},
		{"web02 afgesneden", []Step{{AtSeconds: 0, Action: protocol.FaultLinkDown, On: OnOthers, Interface: "eth0"}}, "degraded",
			"web02 is afgesneden van ClusterForge", []string{"node:"}},
		{"eth1 zonder adres", []Step{{Action: protocol.FaultLinkDown, On: OnAll, Interface: "eth1"}}, "healthy", "", nil},
		{"netem", []Step{{Action: protocol.FaultNetem, On: OnOwner, Interface: "eth0", DelayMS: 200}}, "healthy", "", nil},
		{"schijf 92", []Step{{Action: protocol.FaultDiskFill, On: OnOthers, FillPercent: 92}}, "degraded", "de schijf van web02 staat op 92 %", nil},
		{"schijf 80", []Step{{Action: protocol.FaultDiskFill, On: OnOthers, FillPercent: 80}}, "healthy", "", nil},
	} {
		nodes := seqNodes()
		def := planSteps(nodes, 60, tc.steps...)
		pr, notes, keys := simulate(def, &seqPlan{nodes: nodes, probes: probes, vips: vips})
		if pr.Status != tc.status || !strings.Contains(pr.Reason, tc.reason) {
			t.Errorf("%s: %+v %v", tc.name, pr, notes)
		}
		if len(keys) != len(tc.keys) || len(keys) > 0 && !strings.HasPrefix(keys[0], tc.keys[0]) {
			t.Errorf("%s: sleutels %v", tc.name, keys)
		}
	}
}

func TestFaultOfStep(t *testing.T) {
	nodes := seqNodes()
	def := planSteps(nodes, 60,
		Step{Action: protocol.FaultVRRPBlock, On: OnOthers},
		Step{AtSeconds: 10, Action: ActServiceStop, On: OnAll, Unit: "nginx"},
		Step{AtSeconds: 20, Action: protocol.FaultLinkDown, On: OnOthers, Interface: "eth0"},
	)
	s := &Service{Lease: protocol.FaultLease}
	run := uuid.New()
	r := &disasterRun{s: s, def: def}
	r.run.ID = run
	web01, web02 := nodes[0].ID, nodes[1].ID
	// web02 wordt later afgesneden: zijn VRRP-blokkade krijgt de langste
	// lease, zodat ze blijft terwijl ClusterForge hem niet bereikt.
	if f := r.fault(0, def.Steps[0], web02); f.ID != run.String()+"/1" || f.Kind != protocol.FaultVRRPBlock || f.LeaseSeconds != maxLease || f.MaxSeconds != 120 {
		t.Fatalf("vrrp op web02: %+v", f)
	}
	if f := r.fault(2, def.Steps[2], web02); f.LeaseSeconds != 0 || f.MaxSeconds != 40 || f.Interface != "eth0" {
		t.Fatalf("link_down op web02: %+v", f)
	}
	if ids := def.faultIDs(run, web02); !slices.Equal(ids, []string{run.String() + "/1", run.String() + "/3"}) {
		t.Fatalf("storingen op web02: %v", ids)
	}
	if ids := def.faultIDs(run, web01); len(ids) != 0 || !slices.Equal(def.stoppedUnits(web01), []string{"nginx"}) {
		t.Fatalf("web01: %v %v", ids, def.stoppedUnits(web01))
	}
	if !def.cutOffNode(web02) || def.cutOffNode(web01) || !slices.Equal(def.readyUnits(Target{NodeID: web02}), []string{"nginx"}) {
		t.Fatal("afgesneden of klaar")
	}
	if err := def.check(); err != nil {
		t.Fatal(err)
	}
	def.Steps[1].Unit = "sshd"
	if err := def.check(); err == nil {
		t.Fatal("sshd in de definitie")
	}
	if !strings.Contains(StillDown(def), "storingen van de reeks staan mogelijk nog aan op") {
		t.Fatal(StillDown(def))
	}
}
