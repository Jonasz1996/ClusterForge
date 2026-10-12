package httpapi

import (
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

func (s *Server) ListDatabases(w http.ResponseWriter, r *http.Request) {
	groups, err := s.databases.Overview(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.DatabaseGroup, 0, len(groups))
	for _, g := range groups {
		out := gen.DatabaseGroup{
			ClusterId: nullableOf(g.ClusterID), ClusterName: g.ClusterName, ClusterSlug: g.ClusterSlug,
			ClusterEnvironment: nullable.NewNullNullable[gen.Environment](),
			Engine:             gen.DatabaseGroupEngine(g.Engine), Kind: gen.DatabaseGroupKind(g.Kind), Health: gen.DatabaseGroupHealth(g.Health),
			Summary: g.Summary, Version: g.Version, DatabasesFrom: g.DatabasesFrom, DatabasesTotal: g.DatabasesTotal,
			Servers: make([]gen.DatabaseServer, 0, len(g.Servers)), Databases: make([]gen.DatabaseInfo, 0, len(g.Databases)),
		}
		if g.ClusterEnv != "" {
			out.ClusterEnvironment = nullable.NewNullableWithValue(gen.Environment(g.ClusterEnv))
		}
		if g.ClusterID != nil {
			out.Key = g.Engine + "/" + g.ClusterID.String()
		} else if len(g.Servers) > 0 {
			out.Key = g.Engine + "/node/" + g.Servers[0].NodeID.String()
		}
		for _, srv := range g.Servers {
			out.Servers = append(out.Servers, toAPIDatabaseServer(srv))
		}
		for _, d := range g.Databases {
			out.Databases = append(out.Databases, toAPIDatabaseInfo(d))
		}
		items = append(items, out)
	}
	writeJSON(w, http.StatusOK, list[gen.DatabaseGroup]{items})
}

func toAPIDatabaseServer(s databases.Server) gen.DatabaseServer {
	out := gen.DatabaseServer{
		NodeId: s.NodeID, Hostname: s.Hostname, Unit: s.Unit, Active: s.Active, Running: s.Running, Version: s.Version, Role: s.Role,
		ReadOnly: s.ReadOnly, Connections: s.Connections, MaxConnections: s.MaxConnections, State: s.State, Ok: s.OK, Error: s.Error,
		ErrorKind: s.ErrorKind, SyncedAt: s.SyncedAt, OkAt: nullableOf(s.OkAt),
		Galera: nullable.NewNullNullable[gen.DatabaseGalera](), Patroni: nullable.NewNullNullable[gen.DatabasePatroni](),
		Source: nullable.NewNullNullable[gen.DatabaseSource](), Replicas: make([]gen.DatabaseReplica, 0, len(s.Replicas)),
	}
	if g := s.Galera; g != nil {
		members := g.Members
		if members == nil {
			members = []string{}
		}
		out.Galera = nullable.NewNullableWithValue(gen.DatabaseGalera{
			ClusterName: g.ClusterName, ClusterStatus: g.ClusterStatus, ClusterSize: g.ClusterSize, State: g.State, Ready: g.Ready, Members: members,
		})
	}
	if p := s.Patroni; p != nil {
		out.Patroni = nullable.NewNullableWithValue(gen.DatabasePatroni{Scope: p.Scope, Role: p.Role, State: p.State, Timeline: p.Timeline, Error: p.Error})
	}
	if src := s.Source; src != nil {
		out.Source = nullable.NewNullableWithValue(gen.DatabaseSource{Host: src.Host, Running: src.Running, LagSeconds: nullableOf(src.LagSeconds), Error: src.Error})
	}
	for _, r := range s.Replicas {
		out.Replicas = append(out.Replicas, gen.DatabaseReplica{Address: r.Address, State: r.State, Sync: r.Sync, LagBytes: r.LagBytes})
	}
	return out
}

func toAPIDatabaseInfo(d databases.Database) gen.DatabaseInfo {
	out := gen.DatabaseInfo{
		Name: d.Name, SizeBytes: d.SizeBytes, Tables: d.Tables, Shared: d.Shared, LastUsedAt: nullableOf(d.LastUsed),
		Users: make([]gen.DatabaseUser, 0, len(d.Users)),
	}
	for _, u := range d.Users {
		out.Users = append(out.Users, gen.DatabaseUser{
			Key: u.Key(), Kind: u.Kind, ClusterId: nullableOf(u.ClusterID), NodeId: nullableOf(u.NodeID), Name: u.Name,
			Nodes: u.Nodes, DbUsers: u.DBUsers, Connections: u.Connections, LastSeen: u.LastSeen, Dependency: u.Dependency,
		})
	}
	return out
}
