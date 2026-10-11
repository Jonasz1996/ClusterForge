package deps

import (
	"context"
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	// unknownAfter is hoe lang een onbekende bestemming moet terugkomen voor
	// ze een voorgestelde externe dienst wordt.
	unknownAfter = time.Hour
	// maxNewExternal begrenst de nieuwe externe diensten per ronde, zodat
	// een dienst die met veel wisselende adressen praat de lijst niet vult.
	maxNewExternal = 20
)

// Nets leest de adressen en luisterende poorten van alle nodes.
func (s *Service) Nets(ctx context.Context) (Nets, error) {
	return netsOf(ctx, s.q)
}

func netsOf(ctx context.Context, q *store.Queries) (Nets, error) {
	rows, err := q.ListNodeNets(ctx)
	if err != nil {
		return Nets{}, err
	}
	ns := make([]Net, 0, len(rows))
	for _, r := range rows {
		ns = append(ns, NetOf(r.ID, r.PrimaryIp, r.Addresses, r.Listeners))
	}
	return NewNets(ns), nil
}

// SkipFor geeft wat nooit voorgesteld wordt: ClusterForge zelf.
func (s *Service) SkipFor(ctx context.Context) Skip {
	if s.Self == nil {
		return nil
	}
	return s.Self(ctx)
}

// ConnOf maakt een Conn van een rij.
func ConnOf(r store.ListObservedConnectionsRow) Conn {
	return Conn{
		NodeID: r.NodeID, Unit: r.Unit, Addr: r.PeerAddr, Port: int(r.PeerPort),
		FirstSeen: r.FirstSeen, LastSeen: r.LastSeen, Recent: r.Recent,
	}
}

// Observe leest de uitgaande verbindingen van de afgelopen Window en zet ze
// om naar paren van diensten in g.
func (s *Service) Observe(ctx context.Context, g *Graph) ([]Seen, error) {
	return s.observe(ctx, s.q, g)
}

func (s *Service) observe(ctx context.Context, q *store.Queries, g *Graph) ([]Seen, error) {
	nets, err := netsOf(ctx, q)
	if err != nil {
		return nil, err
	}
	out := "out"
	rows, err := q.ListObservedConnections(ctx, store.ListObservedConnectionsParams{Since: s.Now().Add(-Window), Direction: &out})
	if err != nil {
		return nil, err
	}
	conns := make([]Conn, 0, len(rows))
	for _, r := range rows {
		conns = append(conns, ConnOf(r))
	}
	return g.Observe(nets, conns, s.SkipFor(ctx)), nil
}

// discover maakt van de verbindingen van de afgelopen Window voorgestelde
// afhankelijkheden met bron discovered, onder dezelfde lock als de
// voorstellen voor diensten. Een pijl die al bestaat, in welke staat ook,
// komt er niet opnieuw bij; een genegeerde dienst aan een van beide kanten
// ook niet. Een onbekende bestemming die minstens unknownAfter terugkomt,
// wordt eerst een voorgestelde externe dienst. Elk voorstel is een eigen
// savepoint.
func (s *Service) discover(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", statusLock); err != nil {
			return err
		}
		q := store.New(tx)
		g, _, err := s.load(ctx, q)
		if err != nil {
			return err
		}
		seen, err := s.observe(ctx, q, g)
		if err != nil {
			return err
		}
		exists := map[[2]uuid.UUID]bool{}
		for _, d := range g.Deps {
			exists[[2]uuid.UUID{d.From, d.To}] = true
		}
		created := map[string]Svc{}
		newExternal := 0
		for _, sn := range seen {
			if sn.From.State == StateIgnored || sn.Recent == 0 {
				continue
			}
			to := sn.To
			if to == nil {
				k := HostPort(sn.Addr, sn.Port)
				if svc, ok := created[k]; ok {
					to = &svc
				} else {
					if sn.LastSeen.Sub(sn.FirstSeen) < unknownAfter || newExternal >= maxNewExternal {
						continue
					}
					svc, err := s.proposeExternal(ctx, tx, g, sn)
					if err != nil {
						s.log.Warn("externe dienst voorstellen mislukt", "address", k, "err", err)
						continue
					}
					newExternal++
					created[k] = svc
					to = &svc
				}
			}
			if to.State == StateIgnored || exists[[2]uuid.UUID{sn.From.ID, to.ID}] {
				continue
			}
			if err := s.proposeDependency(ctx, tx, g, sn, *to); err != nil {
				s.log.Warn("afhankelijkheid voorstellen mislukt", "from", sn.From.Name, "to", to.Name, "err", err)
				continue
			}
			exists[[2]uuid.UUID{sn.From.ID, to.ID}] = true
		}
		return nil
	})
}

// proposeExternal maakt een voorgestelde externe dienst voor een onbekende
// bestemming.
func (s *Service) proposeExternal(ctx context.Context, tx pgx.Tx, g *Graph, sn Seen) (Svc, error) {
	var out Svc
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		q := store.New(sp)
		port := int32(sn.Port)
		now := s.Now()
		row, err := q.InsertService(ctx, store.InsertServiceParams{
			Name: externalName(sn.Addr, sn.Port), Kind: portKind(sn.Port), Port: &port, Address: sn.Addr,
			Source: SourceDiscovered, State: StateSuggested, LastSeenAt: &now,
		})
		if err != nil {
			return err
		}
		out = Svc{
			ID: row.ID, Name: row.Name, Kind: row.Kind, Port: sn.Port, Address: row.Address, Source: row.Source, State: row.State,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		payload := servicePayload("extern", row)
		payload["evidence"] = g.Text(sn)
		return s.ev.Write(ctx, q, serviceEvent(events.System(), row, "service.created", payload))
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Svc{}, errors.New("een externe dienst met dat adres en die poort bestaat al")
	}
	return out, err
}

// proposeDependency schrijft een voorgestelde pijl met het bewijs in het
// event.
func (s *Service) proposeDependency(ctx context.Context, tx pgx.Tx, g *Graph, sn Seen, to Svc) error {
	return pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		q := store.New(sp)
		d, err := q.InsertServiceDependency(ctx, store.InsertServiceDependencyParams{
			FromServiceID: sn.From.ID, ToServiceID: to.ID, Strength: Hard, Source: SourceDiscovered, State: StateSuggested,
		})
		if err != nil {
			return err
		}
		payload, cluster, err := s.depPayload(ctx, q, d)
		if err != nil {
			return err
		}
		payload["evidence"] = g.Text(sn)
		payload["connections"] = sn.Recent
		return s.ev.Write(ctx, q, depEvent(events.System(), d, cluster, "dependency.created", payload))
	})
}

// externalName is de naam van een ontdekte externe dienst: 10.0.40.5:5432,
// of 2001:db8::1.443 bij IPv6, want haken mogen niet in een naam.
func externalName(addr string, port int) string {
	if ip, ok := parseIP(addr); ok && ip.Is6() {
		return ip.String() + "." + strconv.Itoa(port)
	}
	return addr + ":" + strconv.Itoa(port)
}

// portKind raadt de soort van een externe dienst aan zijn poort.
func portKind(port int) string {
	switch port {
	case 80, 443, 8080, 8443:
		return "web"
	case 3306, 5432, 1433, 1521, 27017, 9042, 5984:
		return "database"
	case 6379, 11211:
		return "cache"
	case 5671, 5672, 9092, 1883, 8883:
		return "queue"
	case 53:
		return "dns"
	case 2049, 445, 139, 3260:
		return "storage"
	}
	return "external"
}

// Ports haalt de poorten uit adressen als ":8080", "0.0.0.0:4222",
// "cf.lan:4222" of "https://cf.lan:8443", voor SelfSkip. Een URL zonder
// poort geeft 80 of 443.
func Ports(addrs ...string) []int {
	var out []int
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		port := ""
		if strings.Contains(a, "://") {
			u, err := url.Parse(a)
			if err != nil {
				continue
			}
			port = u.Port()
			if port == "" {
				port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
			}
		} else if _, p, err := net.SplitHostPort(a); err == nil {
			port = p
		}
		if n, err := strconv.Atoi(port); err == nil && n > 0 && n <= 65535 && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
