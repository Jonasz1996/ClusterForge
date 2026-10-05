// Package deploy rolt een nieuw cluster uit een template uit: VM's klonen
// uit een golden image in Proxmox, de agent erin aanmelden, de stappen van
// de template op elke node uitvoeren en controleren of het cluster werkt.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is het soort taak van een uitrol.
const Kind = "cluster.deploy"

// ValidationError is een fout in de aanvraag. Field wijst het veld aan,
// zoals "params.vip" of "target.first_ip".
type ValidationError struct {
	Field string
	Msg   string
}

func (e ValidationError) Error() string { return e.Msg }

func invalid(field, format string, a ...any) error {
	return ValidationError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

// Commander stuurt commando's naar agents.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
	Disconnect(nkeyPublic string)
}

type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	log    *slog.Logger
	jobs   *jobs.Runner
	box    *secrets.Box
	pve    *proxmox.Service
	bus    Commander
	inv    *inventory.Service
	agents *agents.Service

	// Poll is hoe vaak een uitrol naar de toestand van VM's en agents kijkt.
	Poll time.Duration
	// GuestAgentTimeout is hoe lang een nieuwe VM mag doen over het starten
	// van de QEMU guest agent; EnrollTimeout hoe lang de agent daarna over
	// het aanmelden.
	GuestAgentTimeout time.Duration
	EnrollTimeout     time.Duration
	// HTTPGet doet de HTTP-controle; tests vervangen hem.
	HTTPGet func(ctx context.Context, url string) (int, error)
	// Changed wordt aangeroepen als nodes van lifecycle veranderen. Mag nil zijn.
	Changed func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, box *secrets.Box,
	pve *proxmox.Service, bus Commander) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, box: box, pve: pve, bus: bus,
		inv: inventory.NewService(pool, ev), agents: agents.NewService(pool, ev, bus),
		Poll: 3 * time.Second, GuestAgentTimeout: 10 * time.Minute, EnrollTimeout: 5 * time.Minute,
		HTTPGet: httpGet,
	}
	runner.Register(Kind, s.run)
	runner.Retryable(Kind)
	return s
}

func httpGet(ctx context.Context, u string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	// Rechtstreeks naar het VIP, niet via een proxy uit de omgeving.
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// Request is een aanvraag om een cluster uit te rollen.
type Request struct {
	Template string
	Cluster  ClusterInput
	Params   map[string]any
	Target   Target
	// ServerURL is het adres waarmee de nieuwe nodes ClusterForge bereiken.
	ServerURL string
}

type ClusterInput struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Environment string `json:"environment"`
	Description string `json:"description"`
}

// Target zegt waar en hoe de VM's komen.
type Target struct {
	ProxmoxID uuid.UUID `json:"proxmox_id"`
	// ImageVMID is de VM-template (golden image) om uit te klonen.
	ImageVMID int    `json:"image_vmid"`
	Storage   string `json:"storage,omitempty"`
	Bridge    string `json:"bridge"`
	VLAN      *int   `json:"vlan,omitempty"`
	// Network is dhcp of static.
	Network string `json:"network"`
	// FirstIP is het adres van de eerste node met prefix, zoals
	// 10.0.20.11/24; de volgende nodes krijgen de adressen erna.
	FirstIP string   `json:"first_ip,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	SSHKeys string   `json:"ssh_keys,omitempty"`
}

// plannedNode is een node zoals de aanvraag hem plant.
type plannedNode struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
	Role     string    `json:"role"`
	Index    int       `json:"index"`
	// Address is leeg bij DHCP.
	Address string            `json:"address,omitempty"`
	Prefix  int               `json:"prefix,omitempty"`
	Host    string            `json:"host"`
	VM      templates.VMShape `json:"vm"`
}

// params gaan met de taak mee. Geheimen staan er niet in; die staan
// versleuteld in de tabel secrets.
type params struct {
	Template    string                `json:"template"`
	Version     string                `json:"version"`
	ClusterID   uuid.UUID             `json:"cluster_id"`
	Cluster     templates.ClusterInfo `json:"cluster"`
	Params      map[string]any        `json:"params"`
	Secrets     []string              `json:"secrets"`
	Target      Target                `json:"target"`
	ServerURL   string                `json:"server_url"`
	Nodes       []plannedNode         `json:"nodes"`
	VIP         string                `json:"vip,omitempty"`
	ImageName   string                `json:"image_name"`
	ImageShared bool                  `json:"image_shared"`
}

var (
	bridgeRe  = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)
	storageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,99}$`)
	sshKeyRe  = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/=]+( [^\r\n]*)?$`)
)

// Plan is wat een uitrol zou maken.
type Plan struct {
	Nodes []PlannedNode
	VIP   string
	VRID  int
}

type PlannedNode struct {
	Hostname string
	Role     string
	// Address is leeg bij DHCP.
	Address string
	Prefix  int
	Host    string
	VM      templates.VMShape
}

// Plan controleert een aanvraag en zegt wat een uitrol zou maken, zonder
// iets te maken.
func (s *Service) Plan(ctx context.Context, req Request) (Plan, error) {
	_, p, err := s.plan(ctx, req)
	if err != nil {
		return Plan{}, err
	}
	out := Plan{VIP: p.VIP}
	out.VRID, _ = p.Params["vrid"].(int)
	for _, n := range p.Nodes {
		out.Nodes = append(out.Nodes, PlannedNode{
			Hostname: n.Hostname, Role: n.Role, Address: n.Address, Prefix: n.Prefix, Host: n.Host, VM: n.VM,
		})
	}
	return out, nil
}

func (s *Service) plan(ctx context.Context, req Request) (*templates.Template, *params, error) {
	if s.box == nil {
		return nil, nil, invalid("", "de server heeft geen masterkey (CF_MASTER_KEY); die is nodig voor Proxmox en voor de geheimen van een cluster")
	}
	tpl, ok := templates.Get(req.Template)
	if !ok {
		return nil, nil, invalid("template", "onbekende template %q", req.Template)
	}
	values, err := tpl.Validate(req.Params)
	if err != nil {
		var fe templates.FieldError
		if errors.As(err, &fe) {
			return nil, nil, invalid("params."+fe.Field, "%s", fe.Msg)
		}
		return nil, nil, err
	}
	p := &params{Template: tpl.Name, Version: tpl.Version, Params: values, Secrets: tpl.Secrets()}
	p.Cluster = templates.ClusterInfo{
		Name: strings.TrimSpace(req.Cluster.Name), Slug: strings.TrimSpace(req.Cluster.Slug), Environment: req.Cluster.Environment,
	}
	if err := s.checkCluster(ctx, p.Cluster); err != nil {
		return nil, nil, err
	}
	if p.ServerURL, err = serverURL(req.ServerURL); err != nil {
		return nil, nil, err
	}
	if err := s.checkTarget(ctx, &req.Target, p); err != nil {
		return nil, nil, err
	}
	p.Target = req.Target
	used, err := s.usedAddresses(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := s.planNodes(ctx, tpl, p, used); err != nil {
		return nil, nil, err
	}
	if err := s.checkVIP(ctx, tpl, p, used); err != nil {
		return nil, nil, err
	}
	return tpl, p, nil
}

// Request controleert de aanvraag, maakt het cluster met zijn nodes (in
// lifecycle provisioning) en zet de uitrol als taak in de wachtrij.
func (s *Service) Request(ctx context.Context, actor events.Actor, req Request) (store.Job, uuid.UUID, error) {
	tpl, p, err := s.plan(ctx, req)
	if err != nil {
		return store.Job{}, uuid.Nil, err
	}

	// Geheimen apart, de rest van de parameters gaat in de spec en de taak.
	secretValues := map[string]string{}
	for _, name := range p.Secrets {
		secretValues[name], _ = p.Params[name].(string)
		delete(p.Params, name)
	}
	spec, err := json.Marshal(map[string]any{
		"template": map[string]string{"name": tpl.Name, "version": tpl.Version},
		"params":   p.Params, "secrets": p.Secrets, "target": p.Target,
		"nodes": func() []map[string]any {
			var out []map[string]any
			for _, n := range p.Nodes {
				out = append(out, map[string]any{"hostname": n.Hostname, "role": n.Role, "address": n.Address, "vm": n.VM})
			}
			return out
		}(),
	})
	if err != nil {
		return store.Job{}, uuid.Nil, err
	}

	var job store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := s.inv.CreateClusterTx(ctx, q, actor, inventory.ClusterFields{
			Slug: p.Cluster.Slug, Name: p.Cluster.Name, Description: strings.TrimSpace(req.Cluster.Description),
			Type: tpl.ClusterType, Environment: store.Environment(p.Cluster.Environment), Tags: []string{},
		})
		if err != nil {
			return err
		}
		p.ClusterID = c.ID
		rev, err := q.SetClusterSpec(ctx, store.SetClusterSpecParams{
			ID: c.ID, Spec: spec, TemplateName: &tpl.Name, TemplateVersion: &tpl.Version,
		})
		if err != nil {
			return err
		}
		var by *uuid.UUID
		if id, err := uuid.Parse(actor.ID); err == nil && actor.Type == store.ActorTypeUser {
			by = &id
		}
		if err := q.InsertSpecRevision(ctx, store.InsertSpecRevisionParams{
			ClusterID: c.ID, Revision: rev, Spec: spec, Source: "ui", CreatedBy: by,
		}); err != nil {
			return err
		}
		err = s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "cluster.spec_changed",
			Payload: map[string]any{
				"name": c.Name, "revision": rev, "previous_revision": nil, "source": "ui",
				"template": tpl.Name, "template_version": tpl.Version,
			},
		})
		if err != nil {
			return err
		}
		names := make([]string, 0, len(secretValues))
		for name := range secretValues {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if err := q.InsertSecret(ctx, store.InsertSecretParams{
				ClusterID: c.ID, Name: name, ValueEnc: s.box.Seal([]byte(secretValues[name]), secretAAD(c.ID, name)), KeyID: s.box.KeyID,
			}); err != nil {
				return err
			}
			// Van een geheim alleen de naam, nooit de waarde.
			err := s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: "secret.created",
				Payload: map[string]any{"name": c.Name, "secret_name": name},
			})
			if err != nil {
				return err
			}
		}
		if p.VIP != "" {
			vrid, _ := p.Params["vrid"].(int)
			f := inventory.VIPFields{Address: p.VIP, Description: "Uitgerold met " + tpl.Name}
			if vrid > 0 {
				f.VRID = &vrid
			}
			if _, err := s.inv.CreateVIPTx(ctx, q, actor, c.ID, f); err != nil {
				return err
			}
		}
		for i := range p.Nodes {
			n := &p.Nodes[i]
			created, err := s.inv.CreateNodeTx(ctx, q, actor, inventory.NodeFields{
				ClusterID: &c.ID, Hostname: n.Hostname, Role: n.Role, Lifecycle: store.NodeLifecycleProvisioning,
				PrimaryIP: n.Address, Tags: []string{},
				Description: fmt.Sprintf("Uitgerold met %s %s", tpl.Name, tpl.Version),
			})
			if err != nil {
				return err
			}
			n.ID = created.ID
		}
		job, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: Kind, Title: "Uitrollen: " + p.Cluster.Name, Params: p, ClusterID: &c.ID,
			ProxmoxID: &p.Target.ProxmoxID, Actor: actor,
		})
		return err
	})
	if err != nil {
		var ve inventory.ValidationError
		if errors.As(err, &ve) {
			return store.Job{}, uuid.Nil, invalid("cluster", "%s", ve.Msg)
		}
		return store.Job{}, uuid.Nil, inventory.Translate(err)
	}
	s.jobs.Kick()
	return job, p.ClusterID, nil
}

// slugRe laat ruimte voor -NN of -<rol>-NN in een hostname van hoogstens
// 63 tekens.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (s *Service) checkCluster(ctx context.Context, c templates.ClusterInfo) error {
	switch {
	case c.Name == "" || len(c.Name) > 128:
		return invalid("cluster.name", "geef het cluster een naam van hoogstens 128 tekens")
	case !slugRe.MatchString(c.Slug) || strings.HasSuffix(c.Slug, "-"):
		return invalid("cluster.slug", "de slug mag alleen kleine letters, cijfers en streepjes bevatten (hoogstens 40 tekens); hij is ook het begin van de hostnames")
	}
	switch store.Environment(c.Environment) {
	case store.EnvironmentLab, store.EnvironmentTest, store.EnvironmentProd:
	default:
		return invalid("cluster.environment", "kies een omgeving")
	}
	taken, err := s.q.ClusterSlugTaken(ctx, c.Slug)
	if err != nil {
		return err
	}
	if taken {
		return invalid("cluster.slug", "er bestaat al een cluster met slug %s", c.Slug)
	}
	return nil
}

func secretAAD(clusterID uuid.UUID, name string) []byte {
	return []byte("secret:" + clusterID.String() + ":" + name)
}

func serverURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", invalid("server_url", "het adres van ClusterForge ontbreekt of is ongeldig")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

func (s *Service) checkTarget(ctx context.Context, t *Target, p *params) error {
	if t.ProxmoxID == uuid.Nil {
		return invalid("target.proxmox_id", "kies een Proxmox-koppeling")
	}
	t.Bridge = strings.TrimSpace(t.Bridge)
	if t.Bridge == "" {
		t.Bridge = "vmbr0"
	}
	if !bridgeRe.MatchString(t.Bridge) {
		return invalid("target.bridge", "ongeldige bridge %q", t.Bridge)
	}
	if t.VLAN != nil && (*t.VLAN < 1 || *t.VLAN > 4094) {
		return invalid("target.vlan", "VLAN moet tussen 1 en 4094 liggen")
	}
	t.Storage = strings.TrimSpace(t.Storage)
	if t.Storage != "" && !storageRe.MatchString(t.Storage) {
		return invalid("target.storage", "ongeldige storage %q", t.Storage)
	}
	var keys []string
	for line := range strings.Lines(t.SSHKeys) {
		if l := strings.TrimSpace(line); l != "" {
			if !sshKeyRe.MatchString(l) {
				return invalid("target.ssh_keys", "%q is geen publieke SSH-sleutel", short(l))
			}
			keys = append(keys, l)
		}
	}
	if len(keys) > 10 {
		return invalid("target.ssh_keys", "hoogstens 10 SSH-sleutels")
	}
	t.SSHKeys = strings.Join(keys, "\n")
	if err := checkNetwork(t); err != nil {
		return err
	}

	img, err := s.pve.Image(ctx, t.ProxmoxID, t.ImageVMID)
	switch {
	case errors.Is(err, proxmox.ErrNotFound):
		return invalid("target.image_vmid", "kies een golden image (een VM-template in Proxmox)")
	case err != nil:
		var ve proxmox.ValidationError
		if errors.As(err, &ve) {
			return invalid("target.image_vmid", "%s", ve.Msg)
		}
		return err
	case !img.CloudInit:
		return invalid("target.image_vmid", "%s heeft geen cloud-init-schijf; maak de template met het golden-image-script", img.Name)
	}
	p.ImageName, p.ImageShared = img.Name, img.Shared
	if t.Storage != "" {
		storages, err := s.pve.Storages(ctx, t.ProxmoxID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(storages, func(st proxmox.StorageInfo) bool { return st.Name == t.Storage }) {
			return invalid("target.storage", "storage %s bestaat niet in Proxmox", t.Storage)
		}
	}
	return nil
}

func short(s string) string {
	if len(s) > 30 {
		return s[:30] + "…"
	}
	return s
}

func checkNetwork(t *Target) error {
	switch t.Network {
	case "dhcp":
		t.FirstIP, t.Gateway, t.DNS = "", "", nil
		return nil
	case "static":
	default:
		return invalid("target.network", "kies dhcp of vaste adressen")
	}
	pf, err := netip.ParsePrefix(strings.TrimSpace(t.FirstIP))
	if err != nil || !pf.Addr().Is4() || pf.Bits() < 8 || pf.Bits() > 30 {
		return invalid("target.first_ip", "geef het eerste adres met prefix, zoals 10.0.20.11/24")
	}
	t.FirstIP = pf.String()
	gw, err := netip.ParseAddr(strings.TrimSpace(t.Gateway))
	if err != nil || !gw.Is4() || !pf.Masked().Contains(gw) {
		return invalid("target.gateway", "de gateway moet een adres in %s zijn", pf.Masked())
	}
	t.Gateway = gw.String()
	var dns []string
	for _, d := range t.DNS {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		a, err := netip.ParseAddr(d)
		if err != nil || !a.Is4() {
			return invalid("target.dns", "%q is geen IPv4-adres", d)
		}
		dns = append(dns, a.String())
	}
	if len(dns) > 3 {
		return invalid("target.dns", "hoogstens 3 DNS-servers")
	}
	t.DNS = dns
	return nil
}

func (s *Service) usedAddresses(ctx context.Context) (map[string]bool, error) {
	list, err := s.q.UsedAddresses(ctx)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, a := range list {
		used[a] = true
	}
	return used, nil
}

// planNodes bepaalt per node de hostname, het adres, de host en de vorm van
// de VM.
func (s *Service) planNodes(ctx context.Context, tpl *templates.Template, p *params, used map[string]bool) error {
	c := templates.Context{Params: p.Params, Cluster: p.Cluster}
	counts, err := tpl.Counts(c)
	if err != nil {
		return invalid("params", "%s", err.Error())
	}
	var next netip.Addr
	var subnet netip.Prefix
	if p.Target.Network == "static" {
		pf := netip.MustParsePrefix(p.Target.FirstIP)
		next, subnet = pf.Addr(), pf.Masked()
	}
	for i, r := range tpl.Roles {
		vm, err := tpl.VM(r.Name, c)
		if err != nil {
			return invalid("params", "%s", err.Error())
		}
		for n := 1; n <= counts[i]; n++ {
			host := fmt.Sprintf("%s-%02d", p.Cluster.Slug, n)
			if len(tpl.Roles) > 1 {
				host = fmt.Sprintf("%s-%s-%02d", p.Cluster.Slug, r.Name, n)
			}
			node := plannedNode{Hostname: host, Role: r.Name, Index: n, VM: vm}
			if next.IsValid() {
				broadcast := lastAddr(subnet)
				if !subnet.Contains(next) || next == broadcast || next == subnet.Addr() {
					return invalid("target.first_ip", "er passen niet genoeg adressen in %s na %s", subnet, p.Target.FirstIP)
				}
				if used[next.String()] || next.String() == p.Target.Gateway {
					return invalid("target.first_ip", "%s is al in gebruik (bij een node, VIP of als gateway); kies een ander eerste adres", next)
				}
				node.Address, node.Prefix = next.String(), subnet.Bits()
				next = next.Next()
			}
			if _, err := s.q.FindNodeByHostname(ctx, host); err == nil {
				return invalid("cluster.slug", "er bestaat al een node %s; kies een andere slug", host)
			}
			p.Nodes = append(p.Nodes, node)
		}
	}
	return s.placeNodes(ctx, p)
}

func lastAddr(pf netip.Prefix) netip.Addr {
	a := pf.Masked().Addr().As4()
	for i := pf.Bits(); i < 32; i++ {
		a[i/8] |= 1 << (7 - i%8)
	}
	return netip.AddrFrom4(a)
}

// placeNodes verdeelt de VM's over de online hosts, zodat twee nodes van
// een cluster niet op dezelfde host staan als het kan. Zonder gedeelde
// storage kan een kloon alleen op de host van de template.
func (s *Service) placeNodes(ctx context.Context, p *params) error {
	hosts, err := s.pve.Hosts(ctx, p.Target.ProxmoxID)
	if err != nil {
		return err
	}
	img, err := s.q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: p.Target.ProxmoxID, Vmid: int32p(p.Target.ImageVMID)})
	if err != nil {
		return err
	}
	var online []proxmox.HostInfo
	for _, h := range hosts {
		if h.Online && (p.ImageShared || h.Name == img.PveNode) {
			online = append(online, h)
		}
	}
	if len(online) == 0 {
		return invalid("target.image_vmid", "host %s van de template is niet online", img.PveNode)
	}
	sort.SliceStable(online, func(i, j int) bool { return online[i].FreeMem > online[j].FreeMem })
	need := map[string]int64{}
	for i := range p.Nodes {
		h := online[i%len(online)]
		p.Nodes[i].Host = h.Name
		need[h.Name] += int64(p.Nodes[i].VM.MemoryMiB) << 20
	}
	for _, h := range online {
		if need[h.Name] > h.FreeMem {
			return invalid("params.memory", "op %s is maar %d MB geheugen vrij; de nieuwe VM's vragen %d MB",
				h.Name, h.FreeMem>>20, need[h.Name]>>20)
		}
	}
	return nil
}

func int32p(n int) *int32 {
	v := int32(n)
	return &v
}

// checkVIP controleert de parameter vip: een template met een vip krijgt een
// VIP in de inventory. Zonder vrid kiest ClusterForge een vrij nummer.
func (s *Service) checkVIP(ctx context.Context, tpl *templates.Template, p *params, used map[string]bool) error {
	vip, _ := p.Params["vip"].(string)
	if vip == "" {
		return nil
	}
	a := netip.MustParseAddr(vip)
	if used[vip] {
		return invalid("params.vip", "%s is al in gebruik bij een node of VIP", vip)
	}
	for _, n := range p.Nodes {
		if n.Address == vip {
			return invalid("params.vip", "het VIP valt samen met het adres van %s", n.Hostname)
		}
	}
	if p.Target.Network == "static" {
		subnet := netip.MustParsePrefix(p.Target.FirstIP).Masked()
		if !subnet.Contains(a) || a.String() == p.Target.Gateway {
			return invalid("params.vip", "het VIP moet een vrij adres in %s zijn", subnet)
		}
	}
	p.VIP = vip
	if !slices.ContainsFunc(tpl.Params, func(pr templates.Param) bool { return pr.Name == "vrid" }) {
		return nil
	}
	vrids, err := s.q.UsedVRIDs(ctx)
	if err != nil {
		return err
	}
	if v, ok := p.Params["vrid"].(int); ok {
		if slices.Contains(vrids, int32(v)) {
			return invalid("params.vrid", "VRRP-id %d is al in gebruik bij een ander cluster", v)
		}
		return nil
	}
	for v := 51; v <= 255; v++ {
		if !slices.Contains(vrids, int32(v)) {
			p.Params["vrid"] = v
			return nil
		}
	}
	return invalid("params.vrid", "er is geen VRRP-id meer vrij")
}
