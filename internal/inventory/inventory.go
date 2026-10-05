// Package inventory beheert clusters, nodes en VIP's. Elke wijziging gaat in
// één transactie samen met haar event naar de database.
package inventory

import (
	"context"
	"errors"
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
type ConflictError struct{ Msg string }

func (e ConflictError) Error() string { return e.Msg }

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
		}
	case "23503":
		switch pe.ConstraintName {
		case "nodes_cluster_id_fkey":
			return ValidationError{Msg: "cluster bestaat niet"}
		case "cluster_owners_user_id_fkey":
			return ValidationError{Msg: "een van de owners bestaat niet"}
		}
	}
	return err
}

// --- clusters ---

func (s *Service) CreateCluster(ctx context.Context, actor events.Actor, f ClusterFields) (store.Cluster, error) {
	f.OwnerIDs = dedupe(f.OwnerIDs)
	if err := f.normalize(); err != nil {
		return store.Cluster{}, err
	}
	var c store.Cluster
	err := s.tx(ctx, func(q *store.Queries) error {
		var err error
		c, err = q.CreateCluster(ctx, store.CreateClusterParams{
			Slug: f.Slug, Name: f.Name, Description: f.Description, Type: f.Type,
			Environment: f.Environment, GitRepoUrl: f.GitRepoURL, Tags: f.Tags,
		})
		if err != nil {
			return err
		}
		for _, uid := range f.OwnerIDs {
			if err := q.AddClusterOwner(ctx, store.AddClusterOwnerParams{ClusterID: c.ID, UserID: uid}); err != nil {
				return err
			}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID,
			Action: "cluster.created", Payload: snapshot(f),
		})
	})
	return c, err
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
		if err := after.normalize(); err != nil {
			return err
		}
		d := diff(before, after)
		c = cur
		if len(d) == 0 {
			return nil
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
		if _, err := q.DeleteCluster(ctx, id); err != nil {
			return err
		}
		// Geen cluster_id op het event: dat zou naar een verwijderd cluster wijzen.
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: id.String(),
			Action: "cluster.deleted", Payload: map[string]any{"slug": cur.Slug, "name": cur.Name},
		})
	})
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
	if f.Lifecycle == "" {
		f.Lifecycle = store.NodeLifecycleActive
	}
	if err := f.normalize(); err != nil {
		return store.Node{}, err
	}
	var n store.Node
	err := s.tx(ctx, func(q *store.Queries) error {
		var err error
		n, err = q.CreateNode(ctx, store.CreateNodeParams{
			ClusterID: f.ClusterID, Hostname: f.Hostname, Role: f.Role, Description: f.Description,
			Lifecycle: f.Lifecycle, PrimaryIp: parseIP(f.PrimaryIP), Tags: f.Tags,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: n.ID.String(), ClusterID: n.ClusterID,
			Action: "node.created", Payload: snapshot(f),
		})
	})
	return n, err
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
		n, err = q.UpdateNode(ctx, store.UpdateNodeParams{
			ID: id, ClusterID: after.ClusterID, Hostname: after.Hostname, Role: after.Role,
			Description: after.Description, Lifecycle: after.Lifecycle, PrimaryIp: parseIP(after.PrimaryIP),
			Tags: after.Tags,
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
		if a, err := q.GetActiveAgentByNode(ctx, id); err == nil {
			agentKey = a.NkeyPublic
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := q.DeleteNode(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: id.String(), ClusterID: cur.ClusterID,
			Action: "node.deleted", Payload: map[string]any{"hostname": cur.Hostname},
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
	return NodeFields{
		ClusterID: n.ClusterID, Hostname: n.Hostname, Role: n.Role, Description: n.Description,
		Lifecycle: n.Lifecycle, PrimaryIP: ip, Tags: n.Tags,
	}
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
	if err := f.normalize(); err != nil {
		return store.Vip{}, err
	}
	var v store.Vip
	err := s.tx(ctx, func(q *store.Queries) error {
		if _, err := q.LockCluster(ctx, clusterID); err != nil {
			return err
		}
		var err error
		v, err = q.CreateVIP(ctx, store.CreateVIPParams{
			ClusterID: clusterID, Address: netip.MustParseAddr(f.Address), Interface: f.Interface,
			Vrid: toInt32(f.VRID), Description: f.Description,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "vip", SubjectID: v.ID.String(), ClusterID: &clusterID,
			Action: "vip.created", Payload: snapshot(f),
		})
	})
	return v, err
}

func (s *Service) UpdateVIP(ctx context.Context, actor events.Actor, id uuid.UUID, change func(*VIPFields)) (store.Vip, error) {
	var v store.Vip
	err := s.tx(ctx, func(q *store.Queries) error {
		cur, err := q.LockVIP(ctx, id)
		if err != nil {
			return err
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
	}
	return reflect.DeepEqual(a, b)
}
