package deps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Service beheert diensten en afhankelijkheden en draait de resolver.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	// Interval is hoe vaak de resolver voorstellen zoekt.
	Interval time.Duration
	Now      func() time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), ev: ev, log: log, Interval: 5 * time.Minute, Now: time.Now}
}

// ErrNotFound: de dienst of afhankelijkheid bestaat niet.
var ErrNotFound = errors.New("niet gevonden")

// FieldError noemt het veld dat niet klopt.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

// ConflictError: er bestaat al zo'n dienst of pijl.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ServiceInput is een nieuwe dienst. Precies één van ClusterID en NodeID, of
// geen van beide met Address en Port voor een externe dienst.
type ServiceInput struct {
	ClusterID   *uuid.UUID
	NodeID      *uuid.UUID
	Name        string
	Kind        string
	Unit        string
	Port        *int
	Address     string
	Description string
}

// ServicePatch wijzigt een dienst; nil laat een veld staan. ClearPort haalt
// de poort weg.
type ServicePatch struct {
	Name        *string
	Kind        *string
	Unit        *string
	Port        *int
	ClearPort   bool
	Address     *string
	Description *string
	State       *string
}

// fields zijn de velden van een dienst die in een event komen, in de
// volgorde van het logboek.
type fields struct {
	Name        string `event:"name"`
	Kind        string `event:"kind"`
	Unit        string `event:"unit"`
	Port        *int   `event:"port"`
	Address     string `event:"address"`
	Description string `event:"description"`
	State       string `event:"state"`
}

func fieldsOf(s store.Service) fields {
	f := fields{Name: s.Name, Kind: s.Kind, Unit: s.Unit, Address: s.Address, Description: s.Description, State: s.State}
	if s.Port != nil {
		p := int(*s.Port)
		f.Port = &p
	}
	return f
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// check controleert de velden; external zegt of de dienst extern is.
func (f *fields) check(external bool) error {
	f.Name, f.Unit, f.Address = strings.TrimSpace(f.Name), strings.TrimSuffix(strings.TrimSpace(f.Unit), ".service"), strings.TrimSpace(f.Address)
	f.Description = strings.TrimSpace(f.Description)
	switch {
	case !nameRe.MatchString(f.Name):
		return &FieldError{Field: "name", Message: "geef een naam van hoogstens 63 tekens: letters, cijfers en @ . _ : / -"}
	case !KnownKind(f.Kind):
		return &FieldError{Field: "kind", Message: "kies een soort uit de lijst"}
	case f.Unit != "" && !unitRe.MatchString(f.Unit):
		return &FieldError{Field: "unit", Message: "geen geldige systemd-unit"}
	case f.Port != nil && (*f.Port < 1 || *f.Port > 65535):
		return &FieldError{Field: "port", Message: "een poort ligt tussen 1 en 65535"}
	case utf8.RuneCountInString(f.Description) > 500:
		return &FieldError{Field: "description", Message: "hoogstens 500 tekens"}
	}
	if !external {
		if f.Address != "" {
			return &FieldError{Field: "address", Message: "alleen een externe dienst heeft een adres"}
		}
		return nil
	}
	switch {
	case f.Unit != "":
		return &FieldError{Field: "unit", Message: "ClusterForge volgt geen units van een externe dienst"}
	case f.Address == "":
		return &FieldError{Field: "address", Message: "een externe dienst heeft een adres"}
	case f.Port == nil:
		return &FieldError{Field: "port", Message: "een externe dienst heeft een poort"}
	}
	if a, err := netip.ParseAddr(f.Address); err == nil {
		f.Address = a.String()
	} else if !hostRe.MatchString(f.Address) {
		return &FieldError{Field: "address", Message: "geef een IP-adres of hostname"}
	}
	return nil
}

func port32(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

// scopeName noemt waar een dienst hoort: het cluster, de node of extern.
func scopeName(ctx context.Context, q *store.Queries, s store.Service) string {
	switch {
	case s.ClusterID != nil:
		if c, err := q.GetCluster(ctx, *s.ClusterID); err == nil {
			return c.Name
		}
	case s.NodeID != nil:
		if n, err := q.GetNodeRef(ctx, *s.NodeID); err == nil {
			return n.Hostname
		}
	default:
		return "extern"
	}
	return ""
}

func servicePayload(scope string, s store.Service) map[string]any {
	p := map[string]any{
		"name": s.Name, "kind": s.Kind, "unit": s.Unit, "port": s.Port, "address": s.Address,
		"scope": scope, "source": s.Source, "state": s.State,
	}
	if s.NodeID != nil {
		p["node_id"] = s.NodeID.String()
	}
	return p
}

func serviceEvent(actor events.Actor, s store.Service, action string, payload map[string]any) events.Event {
	return events.Event{Actor: actor, SubjectType: "service", SubjectID: s.ID.String(), ClusterID: s.ClusterID, Action: action, Payload: payload}
}

// CreateService maakt een dienst met de hand. Staat er in dezelfde scope een
// genegeerde dienst met die naam, dan komt die terug met de nieuwe velden.
func (s *Service) CreateService(ctx context.Context, actor events.Actor, in ServiceInput) (store.Service, error) {
	if in.ClusterID != nil && in.NodeID != nil {
		return store.Service{}, &FieldError{Field: "node_id", Message: "een dienst hoort bij een cluster of bij een losse node, niet bij beide"}
	}
	external := in.ClusterID == nil && in.NodeID == nil
	f := fields{Name: in.Name, Kind: in.Kind, Unit: in.Unit, Port: in.Port, Address: in.Address, Description: in.Description, State: StateConfirmed}
	if err := f.check(external); err != nil {
		return store.Service{}, err
	}
	if in.ClusterID != nil {
		if _, err := s.q.GetCluster(ctx, *in.ClusterID); errors.Is(err, pgx.ErrNoRows) {
			return store.Service{}, &FieldError{Field: "cluster_id", Message: "dat cluster bestaat niet"}
		} else if err != nil {
			return store.Service{}, err
		}
	}
	if in.NodeID != nil {
		ref, err := s.q.GetNodeRef(ctx, *in.NodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Service{}, &FieldError{Field: "node_id", Message: "die node bestaat niet"}
		}
		if err != nil {
			return store.Service{}, err
		}
		if ref.ClusterID != nil {
			return store.Service{}, &FieldError{Field: "node_id", Message: ref.Hostname + " hoort bij een cluster; zet de dienst bij het cluster"}
		}
	}
	var out store.Service
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.FindServiceByName(ctx, store.FindServiceByNameParams{ClusterID: in.ClusterID, NodeID: in.NodeID, Name: f.Name})
		switch {
		case err == nil && old.State == StateIgnored:
			out, err = q.UpdateService(ctx, store.UpdateServiceParams{
				ID: old.ID, Name: f.Name, Kind: f.Kind, Unit: f.Unit, Port: port32(f.Port), Address: f.Address,
				Description: f.Description, Source: SourceManual, State: StateConfirmed,
			})
		case err == nil:
			return &ConflictError{Message: "er is hier al een dienst " + old.Name}
		case errors.Is(err, pgx.ErrNoRows):
			out, err = q.InsertService(ctx, store.InsertServiceParams{
				ClusterID: in.ClusterID, NodeID: in.NodeID, Name: f.Name, Kind: f.Kind, Unit: f.Unit, Port: port32(f.Port),
				Address: f.Address, Description: f.Description, Source: SourceManual, State: StateConfirmed,
			})
		}
		if err != nil {
			return translate(err)
		}
		return s.ev.Write(ctx, q, serviceEvent(actor, out, "service.created", servicePayload(scopeName(ctx, q, out), out)))
	})
	return out, err
}

// UpdateService wijzigt een dienst, of via State bevestigt, negeert of zet
// een voorstel terug.
func (s *Service) UpdateService(ctx context.Context, actor events.Actor, id uuid.UUID, p ServicePatch) (store.Service, error) {
	var out store.Service
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockService(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		before := fieldsOf(cur)
		after := before
		set(&after.Name, p.Name)
		set(&after.Kind, p.Kind)
		set(&after.Unit, p.Unit)
		set(&after.Address, p.Address)
		set(&after.Description, p.Description)
		set(&after.State, p.State)
		if p.Port != nil {
			after.Port = p.Port
		}
		if p.ClearPort {
			after.Port = nil
		}
		if err := after.check(cur.ClusterID == nil && cur.NodeID == nil); err != nil {
			return err
		}
		switch {
		case after.State != StateConfirmed && after.State != StateSuggested && after.State != StateIgnored:
			return &FieldError{Field: "state", Message: "kies bevestigd, voorstel of genegeerd"}
		case after.State == StateSuggested && cur.Source != SourceDiscovered:
			return &FieldError{Field: "state", Message: "alleen een ontdekte dienst kan weer een voorstel worden"}
		}
		d := diff(before, after)
		out = cur
		if len(d) == 0 {
			return nil
		}
		out, err = q.UpdateService(ctx, store.UpdateServiceParams{
			ID: id, Name: after.Name, Kind: after.Kind, Unit: after.Unit, Port: port32(after.Port), Address: after.Address,
			Description: after.Description, Source: cur.Source, State: after.State,
		})
		if err != nil {
			return translate(err)
		}
		d["service"] = out.Name
		d["scope"] = scopeName(ctx, q, out)
		if out.NodeID != nil {
			d["node_id"] = out.NodeID.String()
		}
		return s.ev.Write(ctx, q, serviceEvent(actor, out, "service.updated", d))
	})
	return out, err
}

// DeleteService verwijdert een dienst met zijn pijlen. Een dienst met een
// unit blijft als genegeerde rij staan, zodat de resolver hem niet opnieuw
// voorstelt.
func (s *Service) DeleteService(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockService(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		scope := scopeName(ctx, q, cur)
		removed, err := q.DeleteDependenciesOfService(ctx, id)
		if err != nil {
			return err
		}
		arrows := make([]string, 0, len(removed))
		for _, d := range removed {
			arrows = append(arrows, s.arrowText(ctx, q, d))
		}
		kept := cur.Unit != "" && cur.State != StateIgnored && (cur.ClusterID != nil || cur.NodeID != nil)
		if kept {
			_, err = q.UpdateService(ctx, store.UpdateServiceParams{
				ID: id, Name: cur.Name, Kind: cur.Kind, Unit: cur.Unit, Port: cur.Port, Address: cur.Address,
				Description: cur.Description, Source: cur.Source, State: StateIgnored,
			})
		} else {
			err = q.DeleteService(ctx, id)
		}
		if err != nil {
			return err
		}
		payload := servicePayload(scope, cur)
		payload["kept"] = kept
		payload["dependencies"] = arrows
		return s.ev.Write(ctx, q, serviceEvent(actor, cur, "service.deleted", payload))
	})
}

// arrowText is een pijl in woorden: "nginx in web-prod → mariadb in db-prod".
func (s *Service) arrowText(ctx context.Context, q *store.Queries, d store.ServiceDependency) string {
	name := func(id uuid.UUID) string {
		svc, err := q.GetService(ctx, id)
		if err != nil {
			return "?"
		}
		return svc.Name + " in " + scopeName(ctx, q, svc)
	}
	return name(d.FromServiceID) + " → " + name(d.ToServiceID)
}

// DependencyInput is een nieuwe pijl: From hangt af van To.
type DependencyInput struct {
	From, To uuid.UUID
	Strength string
	Note     string
}

func checkDep(strength, note *string) error {
	*note = strings.TrimSpace(*note)
	if *strength == "" {
		*strength = Hard
	}
	switch {
	case *strength != Hard && *strength != Soft:
		return &FieldError{Field: "strength", Message: "kies hard of zacht"}
	case utf8.RuneCountInString(*note) > 500:
		return &FieldError{Field: "note", Message: "hoogstens 500 tekens"}
	}
	return nil
}

func (s *Service) depPayload(ctx context.Context, q *store.Queries, d store.ServiceDependency) (map[string]any, *uuid.UUID, error) {
	from, err := q.GetService(ctx, d.FromServiceID)
	if err != nil {
		return nil, nil, err
	}
	to, err := q.GetService(ctx, d.ToServiceID)
	if err != nil {
		return nil, nil, err
	}
	cluster := from.ClusterID
	if cluster == nil {
		cluster = to.ClusterID
	}
	return map[string]any{
		"consumer": from.Name, "consumer_scope": scopeName(ctx, q, from),
		"provider": to.Name, "provider_scope": scopeName(ctx, q, to),
		"strength": d.Strength, "note": d.Note, "source": d.Source,
	}, cluster, nil
}

func depEvent(actor events.Actor, d store.ServiceDependency, cluster *uuid.UUID, action string, payload map[string]any) events.Event {
	return events.Event{Actor: actor, SubjectType: "dependency", SubjectID: d.ID.String(), ClusterID: cluster, Action: action, Payload: payload}
}

// CreateDependency legt vast dat From van To afhangt.
func (s *Service) CreateDependency(ctx context.Context, actor events.Actor, in DependencyInput) (store.ServiceDependency, error) {
	if err := checkDep(&in.Strength, &in.Note); err != nil {
		return store.ServiceDependency{}, err
	}
	if in.From == in.To {
		return store.ServiceDependency{}, &FieldError{Field: "to_service_id", Message: "een dienst kan niet van zichzelf afhangen"}
	}
	var out store.ServiceDependency
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		for field, id := range map[string]uuid.UUID{"from_service_id": in.From, "to_service_id": in.To} {
			svc, err := q.GetService(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return &FieldError{Field: field, Message: "die dienst bestaat niet"}
			}
			if err != nil {
				return err
			}
			if svc.State == StateIgnored {
				return &FieldError{Field: field, Message: svc.Name + " is genegeerd; zet hem eerst terug"}
			}
		}
		var err error
		out, err = q.InsertServiceDependency(ctx, store.InsertServiceDependencyParams{
			FromServiceID: in.From, ToServiceID: in.To, Strength: in.Strength, Source: SourceManual, State: StateConfirmed, Note: in.Note,
		})
		if err != nil {
			return translate(err)
		}
		payload, cluster, err := s.depPayload(ctx, q, out)
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, depEvent(actor, out, cluster, "dependency.created", payload))
	})
	return out, err
}

// UpdateDependency wijzigt de sterkte of de notitie.
func (s *Service) UpdateDependency(ctx context.Context, actor events.Actor, id uuid.UUID, strength, note *string) (store.ServiceDependency, error) {
	var out store.ServiceDependency
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockServiceDependency(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		st, nt := cur.Strength, cur.Note
		set(&st, strength)
		set(&nt, note)
		if err := checkDep(&st, &nt); err != nil {
			return err
		}
		d := diff(struct {
			Strength string `event:"strength"`
			Note     string `event:"note"`
		}{cur.Strength, cur.Note}, struct {
			Strength string `event:"strength"`
			Note     string `event:"note"`
		}{st, nt})
		out = cur
		if len(d) == 0 {
			return nil
		}
		if out, err = q.UpdateServiceDependency(ctx, store.UpdateServiceDependencyParams{ID: id, Strength: st, Note: nt}); err != nil {
			return err
		}
		payload, cluster, err := s.depPayload(ctx, q, out)
		if err != nil {
			return err
		}
		for k, v := range d {
			payload[k] = v
		}
		return s.ev.Write(ctx, q, depEvent(actor, out, cluster, "dependency.updated", payload))
	})
	return out, err
}

// DeleteDependency verwijdert een pijl.
func (s *Service) DeleteDependency(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockServiceDependency(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		payload, cluster, err := s.depPayload(ctx, q, cur)
		if err != nil {
			return err
		}
		if err := q.DeleteServiceDependency(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, depEvent(actor, cur, cluster, "dependency.deleted", payload))
	})
}

// FromTemplateTx zet de diensten en afhankelijkheden van een template in
// een nieuw cluster, in de transactie van de uitrol.
func FromTemplateTx(ctx context.Context, q *store.Queries, ev *events.Writer, actor events.Actor, clusterID uuid.UUID, clusterName string, services []templates.Service) error {
	ids := map[string]store.Service{}
	for _, ts := range services {
		f := fields{Name: ts.Name, Kind: ts.Kind, Unit: ts.Unit, State: StateConfirmed}
		if ts.Port > 0 {
			f.Port = &ts.Port
		}
		if err := f.check(false); err != nil {
			return fmt.Errorf("dienst %s uit de template: %w", ts.Name, err)
		}
		svc, err := q.InsertService(ctx, store.InsertServiceParams{
			ClusterID: &clusterID, Name: f.Name, Kind: f.Kind, Unit: f.Unit, Port: port32(f.Port),
			Source: SourceTemplate, State: StateConfirmed,
		})
		if err != nil {
			return err
		}
		ids[ts.Name] = svc
		if err := ev.Write(ctx, q, serviceEvent(actor, svc, "service.created", servicePayload(clusterName, svc))); err != nil {
			return err
		}
	}
	for _, ts := range services {
		strength := ts.Strength
		if strength == "" {
			strength = Hard
		}
		for _, to := range ts.DependsOn {
			from, provider := ids[ts.Name], ids[to]
			d, err := q.InsertServiceDependency(ctx, store.InsertServiceDependencyParams{
				FromServiceID: from.ID, ToServiceID: provider.ID, Strength: strength, Source: SourceTemplate, State: StateConfirmed,
			})
			if err != nil {
				return err
			}
			payload := map[string]any{
				"consumer": from.Name, "consumer_scope": clusterName, "provider": provider.Name, "provider_scope": clusterName,
				"strength": strength, "note": "", "source": SourceTemplate,
			}
			if err := ev.Write(ctx, q, depEvent(actor, d, &clusterID, "dependency.created", payload)); err != nil {
				return err
			}
		}
	}
	return nil
}

// translate zet databasefouten om in fouten die de gebruiker begrijpt.
func translate(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "23505" {
		return err
	}
	switch pe.ConstraintName {
	case "services_name_key":
		return &ConflictError{Message: "er is hier al een dienst met die naam"}
	case "services_external_key":
		return &ConflictError{Message: "er is al een externe dienst op dat adres en die poort"}
	case "service_dependencies_from_service_id_to_service_id_key":
		return &ConflictError{Message: "die afhankelijkheid bestaat al"}
	}
	return err
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}
