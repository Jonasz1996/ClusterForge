package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// enrollPath is waar cf-agent in een nieuwe VM naar een aanmeldbestand kijkt.
const enrollPath = "/etc/clusterforge/enroll.json"

type runCtx struct {
	s     *Service
	j     *jobs.Job
	p     params
	tpl   *templates.Template
	api   proxmox.API
	actor events.Actor
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	r := &runCtx{s: s, j: j, actor: events.System()}
	if err := j.Decode(&r.p); err != nil {
		return err
	}
	if j.RequestedBy != nil {
		r.actor = events.User(*j.RequestedBy)
	}
	tpl, ok := templates.Get(r.p.Template)
	if !ok {
		return fmt.Errorf("template %s zit niet meer in deze server", r.p.Template)
	}
	r.tpl = tpl
	// Na een update van de server kan de template een nieuwere versie zijn;
	// de stappen moeten dan nog steeds met deze parameters werken.
	api, err := s.pve.API(ctx, r.p.Target.ProxmoxID)
	if err != nil {
		return err
	}
	r.api = api
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.pve.Sync(sctx, r.p.Target.ProxmoxID)
	}()

	for i := range r.p.Nodes {
		n := &r.p.Nodes[i]
		if err := j.Step(ctx, "VM "+n.Hostname+" maken", func(ctx context.Context, st *jobs.Step) error {
			return r.createVM(ctx, st, n)
		}); err != nil {
			return err
		}
	}
	if err := j.Step(ctx, "Agents aanmelden", r.enrollAll); err != nil {
		return err
	}
	for i := range r.p.Nodes {
		n := &r.p.Nodes[i]
		if err := j.Step(ctx, "Software op "+n.Hostname, func(ctx context.Context, st *jobs.Step) error {
			return r.apply(ctx, st, n)
		}); err != nil {
			return err
		}
	}
	if err := j.Step(ctx, "Controleren of het cluster werkt", r.check); err != nil {
		return err
	}
	return j.Step(ctx, "Cluster in gebruik nemen", r.activate)
}

func (r *runCtx) guest(st vmState, n *plannedNode) proxmox.Guest {
	return proxmox.Guest{Type: "qemu", Node: n.Host, VMID: st.VMID}
}

// vmState is de bewaarde voortgang van het maken van één VM.
type vmState struct {
	VMID  int    `json:"vmid"`
	Phase string `json:"phase"`
	UPID  string `json:"upid,omitempty"`
}

func (r *runCtx) saveVM(ctx context.Context, st *jobs.Step, vs vmState) error {
	if err := st.SetState(ctx, vs); err != nil {
		return err
	}
	return st.Flush(ctx)
}

// task start een Proxmox-taak en wacht erop. Het id staat in de state, zodat
// een hervatte stap op dezelfde taak verder wacht.
func (r *runCtx) task(ctx context.Context, st *jobs.Step, vs *vmState, start func() (string, error)) error {
	if vs.UPID == "" {
		upid, err := start()
		if err != nil {
			return err
		}
		if upid == "" {
			return nil
		}
		vs.UPID = upid
		if err := r.saveVM(ctx, st, *vs); err != nil {
			return err
		}
	}
	err := r.waitTask(ctx, st, vs.UPID)
	vs.UPID = ""
	return err
}

// waitTask wacht tot een Proxmox-taak klaar is. De voortgangsregels van
// Proxmox blijven weg; bij een fout komen de laatste regels in de stap.
func (r *runCtx) waitTask(ctx context.Context, st *jobs.Step, upid string) error {
	failures := 0
	t := time.NewTicker(r.s.Poll)
	defer t.Stop()
	for {
		status, err := r.api.TaskStatus(ctx, upid)
		switch {
		case err != nil && ctx.Err() == nil:
			failures++
			if failures >= 5 {
				return fmt.Errorf("status van de Proxmox-taak opvragen: %w", err)
			}
		case err == nil && !status.Running():
			if status.OK() {
				return nil
			}
			if lines, err := r.api.TaskLog(ctx, upid); err == nil {
				for _, l := range lines[max(0, len(lines)-10):] {
					st.Logf("  %s", l)
				}
			}
			return fmt.Errorf("taak in Proxmox mislukt: %s", status.ExitStatus)
		case err == nil:
			failures = 0
		}
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if errors.Is(cause, jobs.ErrCanceled) {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer cancel()
				if err := r.api.StopTask(sctx, upid); err == nil {
					st.Logf("Proxmox-taak gestopt")
				}
			}
			return cause
		case <-t.C:
		}
	}
}

func (r *runCtx) createVM(ctx context.Context, st *jobs.Step, n *plannedNode) error {
	var vs vmState
	st.State(&vs)
	img := proxmox.Guest{Type: "qemu", VMID: r.p.Target.ImageVMID}
	if vs.VMID == 0 {
		res, err := r.api.Resources(ctx)
		if err != nil {
			return err
		}
		for _, x := range res {
			if x.Type == "qemu" && x.VMID == img.VMID {
				img.Node = x.Node
			}
		}
		if img.Node == "" {
			return fmt.Errorf("de golden image (VM %d) bestaat niet meer in Proxmox", img.VMID)
		}
		id, err := r.api.NextID(ctx)
		if err != nil {
			return err
		}
		vs = vmState{VMID: id, Phase: "clone"}
		if err := r.saveVM(ctx, st, vs); err != nil {
			return err
		}
	} else if vs.Phase == "clone" && vs.UPID == "" {
		// Onderbroken voor de kloon begon: de golden image opnieuw opzoeken.
		res, err := r.api.Resources(ctx)
		if err != nil {
			return err
		}
		for _, x := range res {
			if x.Type == "qemu" && x.VMID == img.VMID {
				img.Node = x.Node
			}
		}
	}
	g := r.guest(vs, n)

	if vs.Phase == "clone" {
		st.Logf("VM %d klonen uit %s naar %s", vs.VMID, r.p.ImageName, n.Host)
		err := r.task(ctx, st, &vs, func() (string, error) {
			return r.api.Clone(ctx, img, vs.VMID, n.Hostname, n.Host, r.p.Target.Storage)
		})
		if err != nil {
			return fmt.Errorf("klonen: %w", err)
		}
		if err := r.s.q.SetNodeProxmox(ctx, store.SetNodeProxmoxParams{
			ID: n.ID, ProxmoxID: &r.p.Target.ProxmoxID, PveVmid: int32p(vs.VMID),
		}); err != nil {
			return err
		}
		vs.Phase = "config"
		if err := r.saveVM(ctx, st, vs); err != nil {
			return err
		}
	}

	if vs.Phase == "config" {
		form := r.vmConfig(n)
		st.Logf("%d vCPU, %d MB geheugen, netwerk %s", n.VM.CPU, n.VM.MemoryMiB, form.Get("net0"))
		st.Logf("cloud-init: %s", form.Get("ipconfig0"))
		if err := r.task(ctx, st, &vs, func() (string, error) { return r.api.SetConfig(ctx, g, form) }); err != nil {
			return fmt.Errorf("configureren: %w", err)
		}
		vs.Phase = "resize"
		if err := r.saveVM(ctx, st, vs); err != nil {
			return err
		}
	}

	if vs.Phase == "resize" {
		cfg, err := r.api.Config(ctx, g)
		if err != nil {
			return err
		}
		disk, _, size, ok := proxmox.BootDisk(cfg)
		switch {
		case !ok:
			return errors.New("de nieuwe VM heeft geen schijf om van op te starten")
		case size >= n.VM.DiskGiB:
			st.Logf("schijf %s is %d GB; groter dan gevraagd, dus zo gelaten", disk, size)
		default:
			st.Logf("schijf %s van %d naar %d GB", disk, size, n.VM.DiskGiB)
			if err := r.task(ctx, st, &vs, func() (string, error) {
				return r.api.Resize(ctx, g, disk, strconv.Itoa(n.VM.DiskGiB)+"G")
			}); err != nil {
				return fmt.Errorf("schijf vergroten: %w", err)
			}
		}
		vs.Phase = "start"
		if err := r.saveVM(ctx, st, vs); err != nil {
			return err
		}
	}

	if vs.Phase == "start" {
		running := false
		if res, err := r.api.Resources(ctx); err == nil {
			for _, x := range res {
				if x.Type == "qemu" && x.VMID == vs.VMID {
					running = x.Status == "running"
				}
			}
		}
		if !running {
			st.Logf("VM %d starten", vs.VMID)
			if err := r.task(ctx, st, &vs, func() (string, error) { return r.api.Power(ctx, g, "start") }); err != nil {
				return fmt.Errorf("starten: %w", err)
			}
		}
		vs.Phase = "done"
		if err := r.saveVM(ctx, st, vs); err != nil {
			return err
		}
	}
	st.Logf("VM %d draait op %s", vs.VMID, n.Host)
	return nil
}

// vmConfig is de configuratie van een nieuwe VM: vorm, netwerk en
// cloud-init.
func (r *runCtx) vmConfig(n *plannedNode) url.Values {
	t := r.p.Target
	net0 := "virtio,bridge=" + t.Bridge
	if t.VLAN != nil {
		net0 += ",tag=" + strconv.Itoa(*t.VLAN)
	}
	form := url.Values{
		"cores":  {strconv.Itoa(n.VM.CPU)},
		"memory": {strconv.Itoa(n.VM.MemoryMiB)},
		"net0":   {net0},
		"agent":  {"enabled=1"},
		"onboot": {"1"},
		"tags":   {"clusterforge;" + r.p.Cluster.Slug},
		"description": {fmt.Sprintf("Beheerd door ClusterForge: cluster %s, rol %s, template %s %s.",
			r.p.Cluster.Name, n.Role, r.p.Template, r.p.Version)},
	}
	if t.Network == "static" {
		form.Set("ipconfig0", fmt.Sprintf("ip=%s/%d,gw=%s", n.Address, n.Prefix, t.Gateway))
		if len(t.DNS) > 0 {
			form.Set("nameserver", strings.Join(t.DNS, " "))
		}
	} else {
		form.Set("ipconfig0", "ip=dhcp")
	}
	if t.SSHKeys != "" {
		// Proxmox wil de sleutels nog eens URL-gecodeerd, met %20 voor spaties.
		form.Set("sshkeys", strings.ReplaceAll(url.QueryEscape(t.SSHKeys+"\n"), "+", "%20"))
	}
	return form
}

// enrollAll schrijft in elke nieuwe VM een aanmeldbestand voor cf-agent en
// wacht tot alle agents zich gemeld hebben.
func (r *runCtx) enrollAll(ctx context.Context, st *jobs.Step) error {
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		st.Logf(format, a...)
		_ = st.Flush(ctx)
	}
	g, gctx := errgroup.WithContext(ctx)
	for i := range r.p.Nodes {
		n := &r.p.Nodes[i]
		g.Go(func() error { return r.enroll(gctx, n, logf) })
	}
	return g.Wait()
}

func (r *runCtx) enrolled(ctx context.Context, n *plannedNode) (bool, error) {
	row, err := r.s.q.GetDeployNode(ctx, n.ID)
	if err != nil {
		return false, err
	}
	return row.AgentProtocol != nil && row.HeartbeatAt != nil && time.Since(*row.HeartbeatAt) < status.HeartbeatLate, nil
}

func (r *runCtx) enroll(ctx context.Context, n *plannedNode, logf func(string, ...any)) error {
	if ok, err := r.enrolled(ctx, n); err != nil || ok {
		if ok {
			logf("%s: agent is al aangemeld", n.Hostname)
		}
		return err
	}
	row, err := r.s.q.GetNode(ctx, n.ID)
	if err != nil {
		return err
	}
	if row.Node.PveVmid == nil {
		return fmt.Errorf("%s heeft nog geen VM", n.Hostname)
	}
	g := proxmox.Guest{Type: "qemu", Node: n.Host, VMID: int(*row.Node.PveVmid)}
	// De VM kan intussen verhuisd zijn.
	if res, err := r.api.Resources(ctx); err == nil {
		for _, x := range res {
			if x.Type == "qemu" && x.VMID == g.VMID {
				g.Node = x.Node
			}
		}
	}

	logf("%s: wachten op de QEMU guest agent", n.Hostname)
	start := time.Now()
	if err := r.poll(ctx, r.s.GuestAgentTimeout, func(ctx context.Context) (bool, error) {
		return r.api.AgentPing(ctx, g) == nil, nil
	}); err != nil {
		return fmt.Errorf("%s: de QEMU guest agent antwoordt niet; staat qemu-guest-agent in de golden image? (%w)", n.Hostname, err)
	}
	logf("%s: guest agent antwoordt na %s", n.Hostname, seconds(time.Since(start)))

	by := uuid.Nil
	if r.j.RequestedBy != nil {
		by = *r.j.RequestedBy
	}
	token, _, err := r.s.agents.CreateToken(ctx, by, agents.TokenInput{
		Description: "Uitrol " + r.p.Cluster.Name, NodeID: &n.ID, MaxUses: 1, TTL: time.Hour,
	})
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"server": r.p.ServerURL, "token": token})
	if err := r.api.AgentFileWrite(ctx, g, enrollPath, string(body)+"\n"); err != nil {
		return fmt.Errorf("%s: aanmeldbestand schrijven: %w", n.Hostname, err)
	}
	logf("%s: aanmeldbestand geschreven; wachten op cf-agent", n.Hostname)
	start = time.Now()
	if err := r.poll(ctx, r.s.EnrollTimeout, func(ctx context.Context) (bool, error) { return r.enrolled(ctx, n) }); err != nil {
		return fmt.Errorf("%s: cf-agent meldt zich niet aan; kan de VM %s bereiken, en NATS op poort 4222? (%w)", n.Hostname, r.p.ServerURL, err)
	}
	logf("%s: agent aangemeld na %s", n.Hostname, seconds(time.Since(start)))
	return nil
}

var errTimeout = errors.New("wachten duurde te lang")

func (r *runCtx) poll(ctx context.Context, limit time.Duration, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeoutCause(ctx, limit, errTimeout)
	defer cancel()
	t := time.NewTicker(r.s.Poll)
	defer t.Stop()
	for {
		ok, err := check(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-t.C:
		}
	}
}

func seconds(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
}

// renderContext bouwt wat de sjablonen zien, met de adressen en
// netwerkkaarten uit de facts van de nodes.
func (r *runCtx) renderContext(ctx context.Context) (templates.Context, error) {
	values, err := r.tpl.Validate(r.p.Params)
	if err != nil {
		return templates.Context{}, fmt.Errorf("parameters passen niet meer bij de template: %w", err)
	}
	for _, name := range r.p.Secrets {
		sec, err := r.s.q.GetSecret(ctx, store.GetSecretParams{ClusterID: r.p.ClusterID, Name: name})
		if err != nil {
			return templates.Context{}, fmt.Errorf("geheim %s: %w", name, err)
		}
		v, err := r.s.box.Open(sec.ValueEnc, secretAAD(r.p.ClusterID, name), sec.KeyID)
		if err != nil {
			return templates.Context{}, fmt.Errorf("geheim %s is niet te ontsleutelen (%w); is CF_MASTER_KEY veranderd?", name, err)
		}
		values[name] = string(v)
	}
	c := templates.Context{Params: values, Cluster: r.p.Cluster}
	for _, n := range r.p.Nodes {
		info, err := r.nodeInfo(ctx, n)
		if err != nil {
			return templates.Context{}, err
		}
		c.Nodes = append(c.Nodes, info)
	}
	return c, nil
}

func (r *runCtx) nodeInfo(ctx context.Context, n plannedNode) (templates.NodeInfo, error) {
	info := templates.NodeInfo{Hostname: n.Hostname, Role: n.Role, Index: n.Index, Address: n.Address, Prefix: n.Prefix}
	row, err := r.s.q.GetDeployNode(ctx, n.ID)
	if err != nil {
		return info, err
	}
	var facts protocol.Facts
	if len(row.Facts) > 0 {
		_ = json.Unmarshal(row.Facts, &facts)
	}
	if info.Address == "" {
		info.Address = row.PrimaryIp
		if info.Address == "" {
			info.Address = facts.PrimaryAddress
		}
	}
	for _, iface := range facts.Interfaces {
		for _, a := range iface.Addresses {
			pf, err := netip.ParsePrefix(a)
			if err == nil && pf.Addr().String() == info.Address {
				info.Interface, info.Prefix = iface.Name, pf.Bits()
			}
		}
	}
	switch {
	case info.Address == "":
		return info, fmt.Errorf("het adres van %s is nog niet bekend; de agent heeft nog geen facts gestuurd", n.Hostname)
	case info.Interface == "":
		return info, fmt.Errorf("geen netwerkkaart met %s gevonden in de facts van %s", info.Address, n.Hostname)
	}
	return info, nil
}

// apply voert de stappen van de template op één node uit, één commando per
// stap, zodat de voortgang zichtbaar is.
func (r *runCtx) apply(ctx context.Context, st *jobs.Step, n *plannedNode) error {
	row, err := r.s.q.GetDeployNode(ctx, n.ID)
	if err != nil {
		return err
	}
	if row.AgentProtocol == nil || *row.AgentProtocol < protocol.ApplySince {
		return fmt.Errorf("de agent op %s is te oud voor deploystappen; bouw de golden image opnieuw", n.Hostname)
	}
	c, err := r.renderContext(ctx)
	if err != nil {
		return err
	}
	for i := range c.Nodes {
		if c.Nodes[i].Hostname == n.Hostname {
			c.Node = &c.Nodes[i]
		}
	}
	steps, err := r.tpl.Steps(n.Role, c)
	if err != nil {
		return err
	}
	var notify []protocol.ServiceStep
	changed := 0
	for i, s := range steps {
		res, err := r.applyOne(ctx, st, n, fmt.Sprintf("%d", i+1), s.Step)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Title, err)
		}
		mark := "ongewijzigd"
		if res.Changed {
			mark = "aangepast"
			changed++
			for _, h := range s.Notify {
				if !containsService(notify, h) {
					notify = append(notify, h)
				}
			}
			// Een service die net gestart of herstart is, leest zijn
			// configuratie al opnieuw.
			if svc := s.Service; svc != nil && (svc.State == "started" || svc.State == "restarted") {
				notify = slices.DeleteFunc(notify, func(h protocol.ServiceStep) bool { return h.Name == svc.Name })
			}
		}
		st.Logf("%s: %s", s.Title, mark)
		_ = st.Flush(ctx)
	}
	for i, h := range notify {
		h := h
		if _, err := r.applyOne(ctx, st, n, fmt.Sprintf("notify-%d", i+1), protocol.Step{Service: &h}); err != nil {
			return fmt.Errorf("service %s: %w", h.Name, err)
		}
		st.Logf("service %s: %s na gewijzigde configuratie", h.Name, map[string]string{"reloaded": "herladen", "restarted": "herstart"}[h.State])
	}
	st.Logf("%d van %d stappen pasten iets aan", changed, len(steps))
	return nil
}

func containsService(list []protocol.ServiceStep, s protocol.ServiceStep) bool {
	for _, x := range list {
		if x.Name == s.Name && x.State == s.State {
			return true
		}
	}
	return false
}

// applyOne stuurt één stap naar de agent, met een paar pogingen als de
// agent even niet bereikbaar is.
func (r *runCtx) applyOne(ctx context.Context, st *jobs.Step, n *plannedNode, suffix string, step protocol.Step) (protocol.StepResult, error) {
	cmd := protocol.Command{
		ID: r.j.ID.String() + "-" + n.ID.String() + "-" + suffix, Action: protocol.CmdApply, Steps: []protocol.Step{step},
	}
	var res protocol.Result
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return protocol.StepResult{}, context.Cause(ctx)
			case <-time.After(5 * r.s.Poll):
			}
		}
		cmd.Deadline = time.Now().Add(2 * time.Minute)
		cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		res, err = r.s.bus.Command(cctx, n.ID, cmd)
		cancel()
		unreachable := errors.Is(err, agentbus.ErrAgentOffline) || errors.Is(err, agentbus.ErrNoAnswer)
		if err == nil || ctx.Err() != nil || !unreachable {
			break
		}
		st.Logf("agent op %s niet bereikt: %v", n.Hostname, err)
		_ = st.Flush(ctx)
	}
	if ctx.Err() != nil {
		return protocol.StepResult{}, context.Cause(ctx)
	}
	if err != nil {
		return protocol.StepResult{}, err
	}
	var sr protocol.StepResult
	if len(res.Steps) > 0 {
		sr = res.Steps[0]
	}
	for _, line := range sr.Output {
		st.Logf("  %s", line)
	}
	if !res.OK {
		msg := res.Error
		if sr.Error != "" {
			msg = sr.Error
		}
		return sr, errors.New(msg)
	}
	return sr, nil
}

// check voert de controles van de template uit.
func (r *runCtx) check(ctx context.Context, st *jobs.Step) error {
	c, err := r.renderContext(ctx)
	if err != nil {
		return err
	}
	checks, err := r.tpl.RenderChecks(c)
	if err != nil {
		return err
	}
	for _, ch := range checks {
		start := time.Now()
		switch {
		case ch.VIPOwned != nil:
			vip := ch.VIPOwned.VIP
			st.Logf("wachten tot een node %s heeft", vip)
			_ = st.Flush(ctx)
			var owner string
			err := r.poll(ctx, ch.Within(), func(ctx context.Context) (bool, error) {
				vips, err := r.s.q.ListVIPsByCluster(ctx, r.p.ClusterID)
				if err != nil {
					return false, err
				}
				for _, v := range vips {
					if v.Vip.Address.String() == vip && v.OwnerHostname != nil {
						owner = *v.OwnerHostname
						return true, nil
					}
				}
				return false, nil
			})
			if err != nil {
				return fmt.Errorf("geen enkele node heeft %s na %s; kijk naar keepalived met journalctl -u keepalived", vip, seconds(ch.Within()))
			}
			st.Logf("%s staat op %s (na %s)", vip, owner, seconds(time.Since(start)))
		case ch.HTTP != nil:
			st.Logf("GET %s", ch.HTTP.URL)
			_ = st.Flush(ctx)
			var last string
			err := r.poll(ctx, ch.Within(), func(ctx context.Context) (bool, error) {
				code, err := r.s.HTTPGet(ctx, ch.HTTP.URL)
				if err != nil {
					var ue *url.Error
					if errors.As(err, &ue) {
						err = ue.Err
					}
					last = err.Error()
					return false, nil
				}
				last = fmt.Sprintf("status %d", code)
				return code == ch.HTTP.Expect, nil
			})
			if err != nil {
				return fmt.Errorf("%s geeft na %s nog geen %d; laatste antwoord: %s", ch.HTTP.URL, seconds(ch.Within()), ch.HTTP.Expect, last)
			}
			st.Logf("%s geeft %d", ch.HTTP.URL, ch.HTTP.Expect)
		}
		_ = st.Flush(ctx)
	}
	return nil
}

// activate maakt de nodes actief, zodat ze meetellen voor het cluster.
func (r *runCtx) activate(ctx context.Context, st *jobs.Step) error {
	// De netwerkkaart van het VIP is die van de nodes, als ze overal gelijk is.
	iface := ""
	if r.p.VIP != "" {
		c, err := r.renderContext(ctx)
		if err != nil {
			return err
		}
		for i, n := range c.Nodes {
			if i == 0 {
				iface = n.Interface
			} else if n.Interface != iface {
				iface = ""
				break
			}
		}
	}
	err := pgx.BeginFunc(ctx, r.s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if iface != "" {
			if err := q.SetVIPInterface(ctx, store.SetVIPInterfaceParams{ClusterID: r.p.ClusterID, Address: r.p.VIP, Interface: iface}); err != nil {
				return err
			}
		}
		for _, n := range r.p.Nodes {
			row, err := q.SetNodeLifecycle(ctx, store.SetNodeLifecycleParams{NodeID: n.ID, Lifecycle: store.NodeLifecycleActive})
			if err != nil {
				return err
			}
			if row.Previous == store.NodeLifecycleActive {
				continue
			}
			if err := r.s.ev.Write(ctx, q, events.Event{
				Actor: r.actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: &r.p.ClusterID,
				Action: "node.lifecycle_changed",
				Payload: map[string]any{
					"hostname": n.Hostname, "from": row.Previous, "to": store.NodeLifecycleActive,
					"job_id": r.j.ID, "title": r.j.Title,
				},
			}); err != nil {
				return err
			}
		}
		return r.s.ev.Write(ctx, q, events.Event{
			Actor: r.actor, SubjectType: "cluster", SubjectID: r.p.ClusterID.String(), ClusterID: &r.p.ClusterID,
			Action: "cluster.deployed",
			Payload: map[string]any{
				"name": r.p.Cluster.Name, "template": r.p.Template, "version": r.p.Version, "nodes": len(r.p.Nodes),
				"job_id": r.j.ID,
			},
		})
	})
	if err != nil {
		return err
	}
	if r.s.Changed != nil {
		r.s.Changed()
	}
	st.Logf("%d nodes zijn actief en tellen mee voor de status van %s", len(r.p.Nodes), r.p.Cluster.Name)
	return nil
}
