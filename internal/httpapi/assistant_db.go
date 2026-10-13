package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De tools voor databases lezen via dezelfde GET-handlers als de pagina
// Databases en het onderdeel Database: wat ClusterForge elke minuut van de
// servers leest, de dumps, en wie welke database gebruikt. Wachtwoorden en
// hashes staan in geen van die weergaven, en rijen leest ClusterForge niet:
// alleen de catalogus en de status.

// dbListBudget is wat de databases van één server in een tool mogen innemen.
const dbListBudget = 6 * 1024

func (s *Server) databaseTools() []assistant.Tool {
	return []assistant.Tool{
		{
			Name: "database",
			Description: "De databases van een cluster: per onderdeel Database (MariaDB Galera of PostgreSQL met Patroni) de gezondheid " +
				"met de reden, de nodes met hun rol en toestand (Synced, Donor, primary, replica en de achterstand), het VIP en wie het " +
				"heeft, de laatste herstart van de database, de instellingen, de databases met hun grootte, en per database de nieuwste " +
				"dump. Ook de databaseservers op de nodes die ClusterForge alleen leest.",
			Properties: map[string]any{"cluster": strProp("Naam of id van het cluster met de database, zoals het cluster van de app")},
			Required:   []string{"cluster"},
			Label:      func(in map[string]any) string { return "Database van " + arg(in, "cluster") + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster string `json:"cluster"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findCluster(ctx, c, in.Cluster)
				if err != nil {
					return nil, err
				}
				var comps list[gen.ClusterComponent]
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListClusterComponents(w, r, id) }, &comps); err != nil {
					return nil, err
				}
				var groups list[gen.DatabaseGroup]
				if err := s.view(ctx, c, s.ListDatabases, &groups); err != nil {
					return nil, err
				}
				byKey := map[string]gen.DatabaseGroup{}
				for _, g := range groups.Items {
					byKey[g.Key] = g
				}
				var parts []any
				managed := map[string]bool{}
				for _, cc := range comps.Items {
					if cc.Engine != protocol.DBMariaDB && cc.Engine != protocol.DBPostgreSQL {
						continue
					}
					key := cc.Engine + "/" + id.String()
					managed[key] = true
					var dumps gen.ComponentDumps
					if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListComponentDumps(w, r, id, cc.Id) }, &dumps); err != nil {
						return nil, err
					}
					var g *gen.DatabaseGroup
					if x, ok := byKey[key]; ok {
						g = &x
					}
					parts = append(parts, componentForModel(cc, g, dumps))
				}
				var others []any
				for _, g := range groups.Items {
					if v, err := g.ClusterId.Get(); err != nil || v != id || managed[g.Key] {
						continue
					}
					others = append(others, groupForModel(g))
				}
				if len(parts) == 0 && len(others) == 0 {
					return map[string]any{"opmerking": "dit cluster heeft geen database die ClusterForge kent: geen onderdeel Database en " +
						"geen databaseserver op zijn nodes. Gebruikt het een database elders, zoek dat dan op met database_clients."}, nil
				}
				out := map[string]any{"cluster_id": id, "onderdelen": parts}
				if len(others) > 0 {
					out["alleen_gelezen"] = others
					out["opmerking_alleen_gelezen"] = "deze databaseservers leest ClusterForge elke minuut, maar het beheert ze niet"
				}
				return compact(out), nil
			},
		},
		{
			Name: "database_clients",
			Description: "Wie welke database gebruikt: per database de clusters, nodes en onbekende adressen die er de laatste dertig " +
				"dagen mee verbonden, met de gebruiker in de database, het aantal verbindingen, wanneer laatst, en of de afhankelijkheid " +
				"in de graaf staat. Bij een onderdeel Database ook de gebruikers met hun rechten per database. Met een cluster: de " +
				"databases op dat cluster, en de databases elders die het gebruikt; leeg voor alle databases.",
			Properties: map[string]any{"cluster": strProp(clusterDesc)},
			Required:   []string{"cluster"},
			Label: func(in map[string]any) string {
				if v := arg(in, "cluster"); v != "" {
					return "Gebruikers van de databases van " + v + " bekijken"
				}
				return "Gebruikers van alle databases bekijken"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster string `json:"cluster"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				var id uuid.UUID
				if in.Cluster != "" {
					var err error
					if id, err = s.findCluster(ctx, c, in.Cluster); err != nil {
						return nil, err
					}
				}
				var groups list[gen.DatabaseGroup]
				if err := s.view(ctx, c, s.ListDatabases, &groups); err != nil {
					return nil, err
				}
				var here, elsewhere []any
				for _, g := range groups.Items {
					own := id == uuid.Nil
					if v, err := g.ClusterId.Get(); err == nil && v == id {
						own = true
					}
					for _, d := range g.Databases {
						if !own && !slices.ContainsFunc(d.Users, func(u gen.DatabaseUser) bool {
							v, err := u.ClusterId.Get()
							return err == nil && v == id
						}) {
							continue
						}
						item := map[string]any{"database": d.Name, "server": groupName(g), "engine": g.Engine, "gedeeld": d.Shared, "laatst_gebruikt": d.LastUsedAt}
						clients := make([]any, 0, len(d.Users))
						for _, u := range d.Users {
							clients = append(clients, map[string]any{
								"wie": u.Name, "soort": u.Kind, "nodes": u.Nodes, "gebruikers": u.DbUsers, "verbindingen": u.Connections,
								"laatst": u.LastSeen, "afhankelijkheid": u.Dependency,
							})
						}
						item["clients"] = clients
						if len(clients) == 0 {
							item["opmerking"] = "de laatste dertig dagen verbond er niemand mee"
						}
						if own {
							here = append(here, compact(item))
						} else {
							elsewhere = append(elsewhere, compact(item))
						}
					}
				}
				out := map[string]any{}
				if id == uuid.Nil {
					if len(here) == 0 {
						return map[string]any{"opmerking": "ClusterForge las nog geen enkele databaseserver"}, nil
					}
					return withList(out, "databases", here, false, false), nil
				}
				rights, err := s.componentRights(ctx, c, id)
				if err != nil {
					return nil, err
				}
				if len(rights) > 0 {
					out["rechten"] = rights
				}
				if len(elsewhere) > 0 {
					out["gebruikt_elders"] = elsewhere
				} else {
					out["opmerking_elders"] = "dit cluster verbond de laatste dertig dagen met geen database elders die ClusterForge leest"
				}
				if len(here) == 0 {
					out["opmerking"] = "op dit cluster las ClusterForge geen databaseserver"
					return out, nil
				}
				return withList(out, "op_dit_cluster", here, false, false), nil
			},
		},
	}
}

// componentForModel is een onderdeel Database met wat ClusterForge laatst
// van zijn servers las en zijn dumps.
func componentForModel(cc gen.ClusterComponent, g *gen.DatabaseGroup, dumps gen.ComponentDumps) any {
	nodes := make([]any, 0, len(cc.Nodes))
	for _, n := range cc.Nodes {
		nodes = append(nodes, map[string]any{"hostname": n.Hostname, "rol": n.Role})
	}
	out, _ := compact(map[string]any{
		"titel": cc.Title, "onderdeel": cc.Template, "versie_onderdeel": cc.Version, "toestand": cc.State, "fout_van_de_laatste_taak": cc.JobError,
		"taak": cc.JobId, "gezondheid": cc.Health, "reden": cc.HealthReason, "bekeken_om": cc.HealthAt, "down_sinds": cc.DownSince,
		"waarom_down": cc.DownReason, "nodes_die_niet_meedoen": cc.NodesOut, "laatste_herstart": cc.Recovery,
		"vanzelf_herstarten": cc.AutoRecover, "nodes": nodes, "vip": cc.Vip, "instellingen": cc.Params,
	}, "id", "vrid").(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if g == nil {
		out["opmerking_servers"] = "ClusterForge las de servers van dit onderdeel nog niet"
	} else {
		m, _ := groupForModel(*g).(map[string]any)
		for k, v := range m {
			out[k] = v
		}
	}
	perDB := make([]any, 0, len(dumps.Databases))
	for _, d := range dumps.Databases {
		perDB = append(perDB, compact(d, "id", "file", "restore_name", "raw_bytes"))
	}
	kept, dropped := fit(perDB, dbListBudget/2, false)
	dm := map[string]any{"nachtelijk": compact(dumps.Settings), "per_database": kept, "bezig": compact(dumps.Active)}
	if dropped > 0 {
		dm["let_op"] = "niet alle databases staan erin"
	}
	out["dumps"] = compact(dm)
	return out
}

// groupForModel is één databasecluster of losse server zoals de pagina
// Databases hem toont; wie welke database gebruikt, staat bij
// database_clients.
func groupForModel(g gen.DatabaseGroup) any {
	servers := make([]any, 0, len(g.Servers))
	for _, sv := range g.Servers {
		servers = append(servers, compact(sv, "node_id", "synced_at"))
	}
	dbs := make([]any, 0, len(g.Databases))
	for _, d := range g.Databases {
		users := make([]string, 0, len(d.Users))
		for _, u := range d.Users {
			users = append(users, u.Name)
		}
		dbs = append(dbs, compact(map[string]any{"naam": d.Name, "bytes": d.SizeBytes, "tabellen": d.Tables, "gebruikt_door": users, "gedeeld": d.Shared}))
	}
	kept, dropped := fit(dbs, dbListBudget, false)
	out := map[string]any{
		"soort": g.Kind, "engine": g.Engine, "versie": g.Version, "samenvatting": g.Summary, "servers": servers, "databases": kept,
		"databases_gelezen_op": g.DatabasesFrom,
	}
	if dropped > 0 || g.DatabasesTotal > len(g.Databases) {
		out["let_op_databases"] = fmt.Sprintf("niet alle databases staan erin; er zijn er %d", g.DatabasesTotal)
	}
	return compact(out)
}

// groupName is het cluster van een databaseserver, of de node als die in
// geen cluster zit.
func groupName(g gen.DatabaseGroup) string {
	if g.ClusterName != "" || len(g.Servers) == 0 {
		return g.ClusterName
	}
	return g.Servers[0].Hostname
}

// componentRights zijn de gebruikers met hun rechten in de onderdelen
// Database van een cluster. ClusterForge leest ze op de database zelf; lukt
// dat niet, zoals als ze down is, dan staat er waarom.
func (s *Server) componentRights(ctx context.Context, c assistant.Caller, clusterID uuid.UUID) ([]any, error) {
	var comps list[gen.ClusterComponent]
	if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListClusterComponents(w, r, clusterID) }, &comps); err != nil {
		return nil, err
	}
	var out []any
	for _, cc := range comps.Items {
		if cc.Engine != protocol.DBMariaDB && cc.Engine != protocol.DBPostgreSQL {
			continue
		}
		var l gen.ComponentDatabases
		err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListComponentDatabases(w, r, clusterID, cc.Id) }, &l)
		var te assistant.ToolError
		switch {
		case errors.As(err, &te):
			out = append(out, map[string]any{"onderdeel": cc.Title, "opmerking": "de rechten konden niet gelezen worden: " + te.Msg})
			continue
		case err != nil:
			return nil, err
		}
		var users, system []any
		for _, u := range l.Users {
			if u.System {
				system = append(system, u.Name)
				continue
			}
			// Alle rechten op een database is één woord; de lijst ervan
			// zegt het model niets meer.
			grants := make([]any, 0, len(u.Databases))
			for _, g := range u.Databases {
				if g.All {
					grants = append(grants, map[string]any{"database": g.Database, "alles": true})
				} else {
					grants = append(grants, map[string]any{"database": g.Database, "rechten": g.Privileges})
				}
			}
			users = append(users, compact(map[string]any{"gebruiker": u.Name, "hosts": u.Hosts, "overal": u.Global, "databases": grants}))
		}
		out = append(out, compact(map[string]any{
			"onderdeel": cc.Title, "gelezen_op": l.Node, "adres": l.Address, "poort": l.Port, "gebruikers": users, "systeemgebruikers": system,
		}))
	}
	return out, nil
}
