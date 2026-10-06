// Package inventory beheert clusters, nodes en VIP's. Elke wijziging gaat in
// één transactie samen met haar event naar de database.
package inventory

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var ErrNotFound = errors.New("niet gevonden")

// ValidationError is een fout in de invoer.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ConflictError betekent dat de invoer botst met wat er al is, zoals een
// slug die al bestaat.
type ConflictError struct {
	Msg string
	// Code is een stabiele code voor de API; leeg is "conflict".
	Code string
}

func (e ConflictError) Error() string { return e.Msg }

// gitManaged is de fout voor een wijziging aan wat bij een gekoppeld
// cluster uit Git komt.
func gitManaged(what string) error {
	return ConflictError{Code: "git_managed", Msg: what + " komt bij dit cluster uit Git; wijzig het in cluster.yaml, of ontkoppel het cluster eerst"}
}

// gitFields zijn de velden van een cluster die bij een gekoppeld cluster
// uit Git komen. owner_ids hoort er niet bij.
var gitFields = map[string]string{
	"slug": "de slug", "name": "de naam", "description": "de beschrijving", "type": "het type",
	"environment": "de omgeving", "git_repo_url": "de git-repository", "tags": "de tags",
}

// linkedCluster zegt of een cluster aan Git gekoppeld is.
func linkedCluster(ctx context.Context, q *store.Queries, id *uuid.UUID) (bool, error) {
	if id == nil {
		return false, nil
	}
	c, err := q.GetCluster(ctx, *id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && c.GitRepoID != nil, err
}

// ClusterFields zijn de velden van een cluster die een gebruiker beheert. De
// json-namen worden gebruikt in de eventpayload.
type ClusterFields struct {
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        string            `json:"type"`
	Environment store.Environment `json:"environment"`
	GitRepoURL  string            `json:"git_repo_url"`
	Tags        []string          `json:"tags"`
	OwnerIDs    []uuid.UUID       `json:"owner_ids"`
}

type NodeFields struct {
	ClusterID   *uuid.UUID          `json:"cluster_id"`
	Hostname    string              `json:"hostname"`
	Role        string              `json:"role"`
	Description string              `json:"description"`
	Lifecycle   store.NodeLifecycle `json:"lifecycle"`
	// PrimaryIP is leeg als de node geen vast adres heeft.
	PrimaryIP string   `json:"primary_ip"`
	Tags      []string `json:"tags"`
	// Proxmox koppelt de node aan een VM of container; nil als er geen is.
	Proxmox *ProxmoxLink `json:"proxmox"`
}

// ProxmoxLink wijst een VM of container in een Proxmox-omgeving aan.
type ProxmoxLink struct {
	ConnectionID uuid.UUID `json:"connection_id"`
	VMID         int       `json:"vmid"`
}

type VIPFields struct {
	Address     string `json:"address"`
	Interface   string `json:"interface"`
	VRID        *int   `json:"vrid"`
	Description string `json:"description"`
}

type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	// Disconnect verbreekt de NATS-verbinding van een agent; nodig als zijn
	// node verdwijnt. Mag nil zijn.
	Disconnect func(nkeyPublic string)
}

func NewService(pool *pgxpool.Pool, ev *events.Writer) *Service {
	return &Service{pool: pool, ev: ev}
}

func (s *Service) tx(ctx context.Context, fn func(q *store.Queries) error) error {
	return translate(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(store.New(tx))
	}))
}

// Translate zet databasefouten om in fouten die de gebruiker begrijpt, voor
// wie de Tx-functies gebruikt.
func Translate(err error) error { return translate(err) }

// translate zet databasefouten om in fouten die de gebruiker begrijpt.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Code {
	case "23505":
		switch pe.ConstraintName {
		case "clusters_slug_key":
			return ConflictError{Msg: "er bestaat al een cluster met deze slug"}
		case "nodes_hostname_key":
			return ConflictError{Msg: "er bestaat al een node met deze hostname"}
		case "vips_address_key":
			return ConflictError{Msg: "dit VIP-adres is al in gebruik"}
		case "nodes_proxmox_vm_key":
			return ConflictError{Msg: "deze VM is al aan een andere node gekoppeld"}
		}
	case "23503":
		switch pe.ConstraintName {
		case "nodes_cluster_id_fkey":
			return ValidationError{Msg: "cluster bestaat niet"}
		case "cluster_owners_user_id_fkey":
			return ValidationError{Msg: "een van de owners bestaat niet"}
		case "nodes_proxmox_id_fkey":
			return ValidationError{Msg: "Proxmox-koppeling bestaat niet"}
		}
	}
	return err
}

// --- clusters ---

func (s *Service) CreateCluster(ctx context.Context, actor events.Actor, f ClusterFields) (store.Cluster, error) {
	var c store.Cluster
	err := s.tx(ctx, func(q *store.Queries) error {
		var err error
		c, err = s.CreateClusterTx(ctx, q, actor, f)
		return err
	})
	return c, err
}

// CreateClusterTx maakt een cluster binnen de transactie van de aanroeper,
// zoals een uitrol die er meteen nodes in zet. Fouten gaan door Translate.
func (s *Service) CreateClusterTx(ctx context.Context, q *store.Queries, actor events.Actor, f ClusterFields) (store.Cluster, error) {
	f.OwnerIDs = dedupe(f.OwnerIDs)
	if err := f.normalize(); err != nil {
		return store.Cluster{}, err
	}
	c, err := q.CreateCluster(ctx, store.CreateClusterParams{
		Slug: f.Slug, Name: f.Name, Description: f.Description, Type: f.Type,
		Environment: f.Environment, GitRepoUrl: f.GitRepoURL, Tags: f.Tags,
	})
	if err != nil {
		return store.Cluster{}, err
	}
	for _, uid := range f.OwnerIDs {
		if err := q.AddClusterOwner(ctx, store.AddClusterOwnerParams{ClusterID: c.ID, UserID: uid}); err != nil {
			return store.Cluster{}, err
		}
	}
	return c, s.ev.Write(ctx, q, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID,
		Action: "cluster.created", Payload: snapshot(f),
	})
}

// UpdateCluster past change toe op de huidige velden. Zonder verschil
// verandert er niets en komt er geen event.
func (s *Service) UpdateCluster(ctx context.Context, actor events.Actor, id uuid.UUID, change func(*ClusterFields)) (store.Cluster, error) {
	var c store.Cluster
	err := s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockCluster(ctx, id)
		if err != nil {
			return err
		}
		owners, err := q.ListClusterOwners(ctx, id)
		if err != nil {
			return err
		}
		before := clusterFields(cur, owners)
		after := before
		after.Tags = slices.Clone(before.Tags)
		after.OwnerIDs = slices.Clone(before.OwnerIDs)
		change(&after)
		after.OwnerIDs = dedupe(after.OwnerIDs)
		// Een ongewijzigd Git-adres wordt niet opnieuw gecontroleerd: GitOps
		// zet er de link naar het bestand in, in tests en make dev over http.
		keepURL := after.GitRepoURL == before.GitRepoURL
		if keepURL {
			after.GitRepoURL = ""
		}
		if err := after.normalize(); err != nil {
			return err
		}
		if keepURL {
			after.GitRepoURL = before.GitRepoURL
		}
		d := diff(before, after)
		c = cur
		if len(d) == 0 {
			return nil
		}
		if cur.GitRepoID != nil {
			for _, k := range slices.Sorted(maps.Keys(d)) {
				if what, ok := gitFields[k]; ok {
					return gitManaged(strings.ToUpper(what[:1]) + what[1:])
				}
			}
		}
		c, err = q.UpdateCluster(ctx, store.UpdateClusterParams{
			ID: id, Slug: after.Slug, Name: after.Name, Description: after.Description, Type: after.Type,
			Environment: after.Environment, GitRepoUrl: after.GitRepoURL, Tags: after.Tags,
		})
		if err != nil {
			return err
		}
		if _, changed := d["owner_ids"]; changed {
			if err := q.ClearClusterOwners(ctx, id); err != nil {
				return err
			}
			for _, uid := range after.OwnerIDs {
				if err := q.AddClusterOwner(ctx, store.AddClusterOwnerParams{ClusterID: id, UserID: uid}); err != nil {
					return err
				}
			}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: id.String(), ClusterID: &id,
			Action: "cluster.updated", Payload: d,
		})
	})
	return c, err
}

// DeleteCluster verwijdert een cluster en zijn VIP's; de nodes blijven
// bestaan zonder cluster.
func (s *Service) DeleteCluster(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockCluster(ctx, id)
		if err != nil {
			return err
		}
		if cur.GitRepoID != nil {
			return ConflictError{Code: "git_managed", Msg: "dit cluster wordt vanuit Git beheerd; ontkoppel het eerst, dan kun je het verwijderen"}
		}
		payload := map[string]any{"slug": cur.Slug, "name": cur.Name}
		if err := usedBy(ctx, q, payload, &id, nil); err != nil {
			return err
		}
		if _, err := q.DeleteCluster(ctx, id); err != nil {
			return err
		}
		// Geen cluster_id op het event: dat zou naar een verwijderd cluster wijzen.
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: id.String(),
			Action: "cluster.deleted", Payload: payload,
		})
	})
}

// usedBy zet in de payload de bevestigde afhankelijkheden van buiten op de
// diensten van het cluster of de losse node. Die verdwijnen met de
// cascade; zo blijft er een spoor in het logboek.
func usedBy(ctx context.Context, q *store.Queries, payload map[string]any, clusterID, nodeID *uuid.UUID) error {
	rows, err := q.ListIncomingDependencies(ctx, store.ListIncomingDependenciesParams{ClusterID: clusterID, NodeID: nodeID})
	if err != nil || len(rows) == 0 {
		return err
	}
	deps := make([]map[string]any, 0, len(rows))
	for _, d := range rows {
		deps = append(deps, map[string]any{
			"id": d.ID.String(), "consumer": d.Consumer, "consumer_scope": d.ConsumerScope, "provider": d.Provider,
			"strength": d.Strength, "source": d.Source,
		})
	}
	payload["used_by"] = deps
	return nil
}

func clusterFields(c store.Cluster, owners []store.ListClusterOwnersRow) ClusterFields {
	ids := make([]uuid.UUID, 0, len(owners))
	for _, o := range owners {
		ids = append(ids, o.ID)
	}
	return ClusterFields{
		Slug: c.Slug, Name: c.Name, Description: c.Description, Type: c.Type,
		Environment: c.Environment, GitRepoURL: c.GitRepoUrl, Tags: c.Tags, OwnerIDs: ids,
	}
}

// --- nodes ---

func (s *Service) CreateNode(ctx context.Context, actor events.Actor, f NodeFields) (store.Node, error) {
	var n store.Node
	err := s.tx(ctx, func(q *store.Queries) error {
		if linked, err := linkedCluster(ctx, q, f.ClusterID); err != nil || linked {
			return cmp.Or(err, gitManaged("Welke nodes erin zitten"))
		}
		var err error
		n, err = s.CreateNodeTx(ctx, q, actor, f)
		return err
	})
	return n, err
}

// CreateNodeTx maakt een node binnen de transactie van de aanroeper.
func (s *Service) CreateNodeTx(ctx context.Context, q *store.Queries, actor events.Actor, f NodeFields) (store.Node, error) {
	if f.Lifecycle == "" {
		f.Lifecycle = store.NodeLifecycleActive
	}
	if err := f.normalize(); err != nil {
		return store.Node{}, err
	}
	if err := notSandbox(ctx, q, f.Proxmox); err != nil {
		return store.Node{}, err
	}
	proxmoxID, vmid := f.Proxmox.columns()
	n, err := q.CreateNode(ctx, store.CreateNodeParams{
		ClusterID: f.ClusterID, Hostname: f.Hostname, Role: f.Role, Description: f.Description,
		Lifecycle: f.Lifecycle, PrimaryIp: parseIP(f.PrimaryIP), Tags: f.Tags, ProxmoxID: proxmoxID, PveVmid: vmid,
	})
	if err != nil {
		return store.Node{}, err
	}
	return n, s.ev.Write(ctx, q, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID,
		Action: "node.created", Payload: snapshot(f),
	})
}

func (s *Service) UpdateNode(ctx context.Context, actor events.Actor, id uuid.UUID, change func(*NodeFields)) (store.Node, error) {
	var n store.Node
	err := s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockNode(ctx, id)
		if err != nil {
			return err
		}
		before := nodeFields(cur)
		after := before
		after.Tags = slices.Clone(before.Tags)
		change(&after)
		if err := after.normalize(); err != nil {
			return err
		}
		d := diff(before, after)
		n = cur
		if len(d) == 0 {
			return nil
		}
		if _, moved := d["cluster_id"]; moved {
			for _, id := range []*uuid.UUID{before.ClusterID, after.ClusterID} {
				if linked, err := linkedCluster(ctx, q, id); err != nil || linked {
					return cmp.Or(err, gitManaged("Welke nodes erin zitten"))
				}
			}
		}
		if _, changed := d["primary_ip"]; changed {
			if linked, err := linkedCluster(ctx, q, before.ClusterID); err != nil || linked {
				return cmp.Or(err, gitManaged("Het adres van een node"))
			}
		}
		if _, changed := d["proxmox"]; changed {
			if err := notSandbox(ctx, q, after.Proxmox); err != nil {
				return err
			}
		}
		proxmoxID, vmid := after.Proxmox.columns()
		n, err = q.UpdateNode(ctx, store.UpdateNodeParams{
			ID: id, ClusterID: after.ClusterID, Hostname: after.Hostname, Role: after.Role,
			Description: after.Description, Lifecycle: after.Lifecycle, PrimaryIp: parseIP(after.PrimaryIP),
			Tags: after.Tags, ProxmoxID: proxmoxID, PveVmid: vmid,
		})
		if err != nil {
			return err
		}
		// Bij een verhuizing hoort het event bij het oude cluster als de node
		// eruit gehaald wordt, en anders bij het nieuwe.
		clusterID := after.ClusterID
		if clusterID == nil {
			clusterID = before.ClusterID
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: id.String(), ClusterID: clusterID,
			Action: "node.updated", Payload: d,
		})
	})
	return n, err
}

// DeleteNode verwijdert een node; zijn agent verdwijnt mee en wordt
// losgekoppeld.
func (s *Service) DeleteNode(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	var agentKey string
	err := s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockNode(ctx, id)
		if err != nil {
			return err
		}
		if linked, err := linkedCluster(ctx, q, cur.ClusterID); err != nil || linked {
			return cmp.Or(err, gitManaged("Welke nodes erin zitten"))
		}
		if a, err := q.GetActiveAgentByNode(ctx, id); err == nil {
			agentKey = a.NkeyPublic
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		payload := map[string]any{"hostname": cur.Hostname}
		if err := usedBy(ctx, q, payload, nil, &id); err != nil {
			return err
		}
		if _, err := q.DeleteNode(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: id.String(), ClusterID: cur.ClusterID,
			Action: "node.deleted", Payload: payload,
		})
	})
	if err == nil && agentKey != "" && s.Disconnect != nil {
		s.Disconnect(agentKey)
	}
	return err
}

func nodeFields(n store.Node) NodeFields {
	ip := ""
	if n.PrimaryIp != nil {
		ip = n.PrimaryIp.String()
	}
	var link *ProxmoxLink
	if n.ProxmoxID != nil && n.PveVmid != nil {
		link = &ProxmoxLink{ConnectionID: *n.ProxmoxID, VMID: int(*n.PveVmid)}
	}
	return NodeFields{
		ClusterID: n.ClusterID, Hostname: n.Hostname, Role: n.Role, Description: n.Description,
		Lifecycle: n.Lifecycle, PrimaryIP: ip, Tags: n.Tags, Proxmox: link,
	}
}

// notSandbox weigert een koppeling met een sandbox-VM van een
// back-upcontrole: die VM is tijdelijk en ClusterForge ruimt hem zelf op.
func notSandbox(ctx context.Context, q *store.Queries, l *ProxmoxLink) error {
	if l == nil {
		return nil
	}
	sandbox, err := q.IsSandboxGuest(ctx, store.IsSandboxGuestParams{ConnectionID: l.ConnectionID, Vmid: int32(l.VMID)})
	if err != nil {
		return err
	}
	if sandbox {
		return ValidationError{Msg: "deze VM is een tijdelijke sandbox van een back-upcontrole en kan niet aan een node gekoppeld worden"}
	}
	return nil
}

func (l *ProxmoxLink) columns() (*uuid.UUID, *int32) {
	if l == nil {
		return nil, nil
	}
	id, vmid := l.ConnectionID, int32(l.VMID)
	return &id, &vmid
}

func parseIP(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &ip
}

// --- VIP's ---

func (s *Service) CreateVIP(ctx context.Context, actor events.Actor, clusterID uuid.UUID, f VIPFields) (store.Vip, error) {
	var v store.Vip
	err := s.tx(ctx, func(q *store.Queries) error {
		c, err := q.LockCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if c.GitRepoID != nil {
			return gitManaged("Het VIP")
		}
		v, err = s.CreateVIPTx(ctx, q, actor, clusterID, f)
		return err
	})
	return v, err
}

// CreateVIPTx maakt een VIP binnen de transactie van de aanroeper.
func (s *Service) CreateVIPTx(ctx context.Context, q *store.Queries, actor events.Actor, clusterID uuid.UUID, f VIPFields) (store.Vip, error) {
	if err := f.normalize(); err != nil {
		return store.Vip{}, err
	}
	v, err := q.CreateVIP(ctx, store.CreateVIPParams{
		ClusterID: clusterID, Address: netip.MustParseAddr(f.Address), Interface: f.Interface,
		Vrid: toInt32(f.VRID), Description: f.Description,
	})
	if err != nil {
		return store.Vip{}, err
	}
	return v, s.ev.Write(ctx, q, events.Event{
		Actor: actor, SubjectType: "vip", SubjectID: v.ID.String(), ClusterID: &clusterID,
		Action: "vip.created", Payload: snapshot(f),
	})
}

func (s *Service) UpdateVIP(ctx context.Context, actor events.Actor, id uuid.UUID, change func(*VIPFields)) (store.Vip, error) {
	var v store.Vip
	err := s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockVIP(ctx, id)
		if err != nil {
			return err
		}
		if linked, err := linkedCluster(ctx, q, &cur.ClusterID); err != nil || linked {
			return cmp.Or(err, gitManaged("Het VIP"))
		}
		before := vipFields(cur)
		after := before
		change(&after)
		if err := after.normalize(); err != nil {
			return err
		}
		d := diff(before, after)
		v = cur
		if len(d) == 0 {
			return nil
		}
		v, err = q.UpdateVIP(ctx, store.UpdateVIPParams{
			ID: id, Address: netip.MustParseAddr(after.Address), Interface: after.Interface,
			Vrid: toInt32(after.VRID), Description: after.Description,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "vip", SubjectID: id.String(), ClusterID: &cur.ClusterID,
			Action: "vip.updated", Payload: d,
		})
	})
	return v, err
}

func (s *Service) DeleteVIP(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockVIP(ctx, id)
		if err != nil {
			return err
		}
		if linked, err := linkedCluster(ctx, q, &cur.ClusterID); err != nil || linked {
			return cmp.Or(err, gitManaged("Het VIP"))
		}
		if _, err := q.DeleteVIP(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "vip", SubjectID: id.String(), ClusterID: &cur.ClusterID,
			Action: "vip.deleted", Payload: map[string]any{"address": cur.Address.String()},
		})
	})
}

func vipFields(v store.Vip) VIPFields {
	var vrid *int
	if v.Vrid != nil {
		n := int(*v.Vrid)
		vrid = &n
	}
	return VIPFields{Address: v.Address.String(), Interface: v.Interface, VRID: vrid, Description: v.Description}
}

func toInt32(p *int) *int32 {
	if p == nil {
		return nil
	}
	n := int32(*p)
	return &n
}

// --- hulpfuncties ---

func dedupe(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// fieldName geeft de json-naam van een structveld.
func fieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}

// snapshot zet de velden om naar een eventpayload.
func snapshot(v any) map[string]any {
	rv := reflect.ValueOf(v)
	out := map[string]any{}
	for i := range rv.NumField() {
		out[fieldName(rv.Type().Field(i))] = rv.Field(i).Interface()
	}
	return out
}

// diff geeft per gewijzigd veld {"from": oud, "to": nieuw}. Volgorde telt niet
// mee voor owners, wel voor tags.
func diff[T any](before, after T) map[string]any {
	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	out := map[string]any{}
	for i := range bv.NumField() {
		b, a := bv.Field(i).Interface(), av.Field(i).Interface()
		if equal(b, a) {
			continue
		}
		out[fieldName(bv.Type().Field(i))] = map[string]any{"from": b, "to": a}
	}
	return out
}

func equal(a, b any) bool {
	switch x := a.(type) {
	case []uuid.UUID:
		y := b.([]uuid.UUID)
		return len(x) == len(y) && !slices.ContainsFunc(x, func(id uuid.UUID) bool { return !slices.Contains(y, id) })
	case []string:
		y := b.([]string)
		return slices.Equal(x, y)
	case *uuid.UUID:
		y := b.(*uuid.UUID)
		return (x == nil) == (y == nil) && (x == nil || *x == *y)
	case *int:
		y := b.(*int)
		return (x == nil) == (y == nil) && (x == nil || *x == *y)
	case *ProxmoxLink:
		y := b.(*ProxmoxLink)
		return (x == nil) == (y == nil) && (x == nil || *x == *y)
	}
	return reflect.DeepEqual(a, b)
}
