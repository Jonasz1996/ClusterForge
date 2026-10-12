package components

// Databases en gebruikers in de database van een cluster. ClusterForge
// leest en schrijft op één datanode: die met het VIP als hij Synced is,
// anders de eerste die Synced is. Galera zet een nieuwe database of
// gebruiker zelf op de andere nodes.
//
// Een nieuwe gebruiker krijgt een sterk wachtwoord dat één keer in het
// antwoord staat; ClusterForge bewaart het niet. Hij mag inloggen vanaf
// elke node van het cluster en vanaf elk VIP: een app op de node met het
// VIP die naar het VIP verbindt, komt bij MariaDB binnen met het VIP als
// adres.

import (
	"cmp"
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// MariaDBPort is de poort waarop een app MariaDB bereikt.
const MariaDBPort = 3306

// allPrivileges zijn de rechten op een database die GRANT ALL PRIVILEGES
// op elke versie van MariaDB sinds 10.11 geeft; nieuwere versies geven er
// meer.
var allPrivileges = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "REFERENCES", "INDEX", "ALTER", "CREATE TEMPORARY TABLES",
	"LOCK TABLES", "EXECUTE", "CREATE VIEW", "SHOW VIEW", "CREATE ROUTINE", "ALTER ROUTINE", "EVENT", "TRIGGER",
}

// DatabaseList is wat er in de database van een onderdeel staat.
type DatabaseList struct {
	// Node is de datanode waarvan ClusterForge las.
	Node      string
	Databases []DatabaseInfo
	Users     []UserInfo
	// Address is waar een app verbindt: het VIP van de database, of de
	// eerste datanode.
	Address string
	Port    int
	// Hosts zijn de adressen waarvandaan een nieuwe gebruiker mag inloggen.
	Hosts []string
}

// DatabaseInfo is een database met haar grootte, het aantal verbindingen
// op de node waarvan ClusterForge las, en de gebruikers met rechten erop.
type DatabaseInfo struct {
	Name        string
	SizeBytes   int64
	Tables      int
	Connections int
	Users       []string
}

// UserInfo is een gebruiker met al zijn hosts. System: hij is van MariaDB
// zelf, zoals root.
type UserInfo struct {
	Name      string
	Hosts     []string
	System    bool
	Global    []string
	Databases []UserGrant
}

// UserGrant zijn de rechten van een gebruiker op een database, over al
// zijn hosts samen. All: alle rechten.
type UserGrant struct {
	Database   string
	All        bool
	Privileges []string
}

// CreatedDatabase is een nieuwe database met haar gebruiker. Password staat
// alleen hier; ClusterForge bewaart het niet.
type CreatedDatabase struct {
	Database string
	User     string
	Password string
	Hosts    []string
	Address  string
	Port     int
	Node     string
}

// NewPassword is een nieuw wachtwoord voor een gebruiker.
type NewPassword struct {
	User     string
	Password string
	Hosts    []string
}

// dbTarget is de node waarop ClusterForge leest en schrijft.
type dbTarget struct {
	row     store.ClusterComponent
	spec    Spec
	cluster store.Cluster
	node    Node
}

// target kiest de node om op te lezen en te schrijven.
func (s *Service) target(ctx context.Context, clusterID, id uuid.UUID) (dbTarget, error) {
	r, err := s.get(ctx, s.q, clusterID, id, false)
	if err != nil {
		return dbTarget{}, err
	}
	if r.AppliedRevision == nil {
		return dbTarget{}, &ConflictError{"deze database is nog nooit helemaal uitgerold; pas haar eerst toe"}
	}
	spec, err := parseSpec(r.Spec)
	if err != nil {
		return dbTarget{}, err
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return dbTarget{}, err
	}
	owner := ""
	if spec.VIP != nil {
		vips, err := s.q.ListVIPsByCluster(ctx, clusterID)
		if err != nil {
			return dbTarget{}, err
		}
		for _, v := range vips {
			if v.Vip.ID == spec.VIP.ID {
				owner = deref(v.OwnerHostname)
			}
		}
	}
	all := s.observe(ctx, dataNodes(spec, nil), false)
	slug := spec.Cluster.Slug
	best := -1
	var why []string
	for i, o := range all {
		if !o.synced(slug) {
			why = append(why, o.node.Hostname+": "+o.describe(slug))
			continue
		}
		if best < 0 || o.node.Hostname == owner {
			best = i
		}
	}
	if best < 0 {
		return dbTarget{}, &ConflictError{"geen enkele datanode is Synced, dus de database draait niet; " + strings.Join(why, "; ")}
	}
	return dbTarget{row: r, spec: spec, cluster: c, node: all[best].node}, nil
}

// dbRequest stuurt een databaseverzoek naar de node van t. Een fout van de
// agent wordt een ConflictError met de naam van de node.
func (s *Service) dbRequest(ctx context.Context, t dbTarget, req protocol.DBRequest) (protocol.DBResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	req.ID, req.Deadline = uuid.NewString(), time.Now().Add(s.Timeout)
	res, err := s.bus.DB(ctx, t.node.NodeID, req)
	switch {
	case err != nil:
		return res, &ConflictError{t.node.Hostname + ": " + err.Error()}
	case strings.HasPrefix(res.Error, "onbekend databaseverzoek"):
		return res, &ConflictError{t.node.Hostname + ": de agent kent dit verzoek nog niet; werk hem bij met het installatiescript en --upgrade"}
	case res.Error != "":
		return res, &ConflictError{t.node.Hostname + ": " + res.Error}
	}
	return res, nil
}

// Databases geeft de databases en gebruikers van een onderdeel.
func (s *Service) Databases(ctx context.Context, clusterID, id uuid.UUID) (DatabaseList, error) {
	t, err := s.target(ctx, clusterID, id)
	if err != nil {
		return DatabaseList{}, err
	}
	return s.databases(ctx, t)
}

func (s *Service) databases(ctx context.Context, t dbTarget) (DatabaseList, error) {
	out := DatabaseList{Node: t.node.Hostname, Databases: []DatabaseInfo{}, Users: []UserInfo{}, Port: MariaDBPort}
	hosts, address, err := s.userHosts(ctx, t)
	if err != nil {
		return out, err
	}
	out.Hosts, out.Address = hosts, address
	st, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBStatus})
	if err != nil {
		return out, err
	}
	us, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBUsers})
	if err != nil {
		return out, err
	}
	i := slices.IndexFunc(st.Servers, func(sv protocol.DBServer) bool { return sv.Engine == protocol.DBMariaDB })
	if i < 0 {
		return out, &ConflictError{t.node.Hostname + ": op deze node staat geen MariaDB"}
	}
	server := st.Servers[i]
	conns := map[string]int{}
	for _, c := range server.Clients {
		conns[c.Database] += c.Count
	}
	for _, d := range server.Databases {
		out.Databases = append(out.Databases, DatabaseInfo{Name: d.Name, SizeBytes: d.SizeBytes, Tables: d.Tables, Connections: conns[d.Name], Users: []string{}})
	}
	out.Users = groupUsers(us.Users)
	for i := range out.Databases {
		d := &out.Databases[i]
		for _, u := range out.Users {
			if slices.ContainsFunc(u.Databases, func(g UserGrant) bool { return g.Database == d.Name }) {
				d.Users = append(d.Users, u.Name)
			}
		}
	}
	return out, nil
}

// groupUsers zet de hosts van een gebruiker bij elkaar, met de rechten van
// alle hosts samen.
func groupUsers(in []protocol.DBUser) []UserInfo {
	var out []UserInfo
	for _, u := range in {
		i := slices.IndexFunc(out, func(x UserInfo) bool { return x.Name == u.Name })
		if i < 0 {
			i = len(out)
			out = append(out, UserInfo{Name: u.Name, System: slices.Contains(dbsql.SystemUsers, u.Name), Global: []string{}, Databases: []UserGrant{}})
		}
		x := &out[i]
		x.Hosts = append(x.Hosts, u.Host)
		for _, p := range u.Global {
			if !slices.Contains(x.Global, p) {
				x.Global = append(x.Global, p)
			}
		}
		for _, g := range u.Databases {
			j := slices.IndexFunc(x.Databases, func(y UserGrant) bool { return y.Database == g.Database })
			if j < 0 {
				j = len(x.Databases)
				x.Databases = append(x.Databases, UserGrant{Database: g.Database, Privileges: []string{}})
			}
			for _, p := range g.Privileges {
				if !slices.Contains(x.Databases[j].Privileges, p) {
					x.Databases[j].Privileges = append(x.Databases[j].Privileges, p)
				}
			}
		}
	}
	for i := range out {
		for j := range out[i].Databases {
			g := &out[i].Databases[j]
			g.All = !slices.ContainsFunc(allPrivileges, func(p string) bool { return !slices.Contains(g.Privileges, p) })
		}
		slices.SortFunc(out[i].Databases, func(a, b UserGrant) int { return cmp.Compare(a.Database, b.Database) })
	}
	slices.SortFunc(out, func(a, b UserInfo) int {
		if a.System != b.System {
			if a.System {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

// userHosts zijn de adressen van alle nodes van het cluster en zijn VIP's,
// en het adres waarop een app de database bereikt.
func (s *Service) userHosts(ctx context.Context, t dbTarget) ([]string, string, error) {
	nodes, err := s.q.ListNodesByCluster(ctx, &t.cluster.ID)
	if err != nil {
		return nil, "", err
	}
	var hosts []string
	add := func(h string) {
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	first := ""
	data := dataNodes(t.spec, nil)
	for _, n := range nodes {
		if n.Node.PrimaryIp == nil {
			continue
		}
		add(n.Node.PrimaryIp.String())
		if len(data) > 0 && data[0].NodeID == n.Node.ID {
			first = n.Node.PrimaryIp.String()
		}
	}
	vips, err := s.q.ListVIPsByCluster(ctx, t.cluster.ID)
	if err != nil {
		return nil, "", err
	}
	for _, v := range vips {
		add(v.Vip.Address.String())
	}
	address := first
	if a := t.spec.vipAddress(); a != "" {
		address = a
	}
	return hosts, address, nil
}

// CreateDatabaseInput is een nieuwe database; User is haar gebruiker, of
// de naam van de database als hij leeg is.
type CreateDatabaseInput struct {
	Name string
	User string
}

// CreateDatabase maakt een database met een eigen gebruiker met alle
// rechten erop, en geeft het wachtwoord één keer terug.
func (s *Service) CreateDatabase(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, in CreateDatabaseInput) (CreatedDatabase, error) {
	if in.User == "" {
		in.User = in.Name
	}
	if err := dbsql.CheckDatabase(in.Name); err != nil {
		return CreatedDatabase{}, &InvalidError{"name", err.Error()}
	}
	if err := dbsql.CheckUser(in.User); err != nil {
		return CreatedDatabase{}, &InvalidError{"user", err.Error()}
	}
	t, err := s.target(ctx, clusterID, id)
	if err != nil {
		return CreatedDatabase{}, err
	}
	list, err := s.databases(ctx, t)
	if err != nil {
		return CreatedDatabase{}, err
	}
	if slices.ContainsFunc(list.Databases, func(d DatabaseInfo) bool { return d.Name == in.Name }) {
		return CreatedDatabase{}, &ConflictError{"de database " + in.Name + " bestaat al"}
	}
	if slices.ContainsFunc(list.Users, func(u UserInfo) bool { return u.Name == in.User }) {
		return CreatedDatabase{}, &ConflictError{"de gebruiker " + in.User + " bestaat al; kies een andere naam, of Nieuw wachtwoord bij die gebruiker"}
	}
	if len(list.Hosts) == 0 {
		return CreatedDatabase{}, &ConflictError{"geen enkele node van het cluster heeft een adres"}
	}
	pw := rand.Text()
	if _, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		Database: in.Name, User: in.User, Hosts: list.Hosts, Password: pw, Grant: true,
	}}); err != nil {
		return CreatedDatabase{}, err
	}
	s.dbEvent(ctx, actor, t, "cluster.database_created", map[string]any{"database": in.Name, "user": in.User, "hosts": list.Hosts, "node": t.node.Hostname})
	return CreatedDatabase{Database: in.Name, User: in.User, Password: pw, Hosts: list.Hosts, Address: list.Address, Port: MariaDBPort, Node: t.node.Hostname}, nil
}

// SetPassword geeft een gebruiker op al zijn hosts een nieuw wachtwoord.
func (s *Service) SetPassword(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, user string) (NewPassword, error) {
	t, u, err := s.user(ctx, clusterID, id, user)
	if err != nil {
		return NewPassword{}, err
	}
	pw := rand.Text()
	if _, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
		User: u.Name, Hosts: u.Hosts, Password: pw,
	}}); err != nil {
		return NewPassword{}, err
	}
	s.dbEvent(ctx, actor, t, "cluster.database_password", map[string]any{"user": u.Name, "hosts": u.Hosts, "node": t.node.Hostname})
	return NewPassword{User: u.Name, Password: pw, Hosts: u.Hosts}, nil
}

// user zoekt een gebruiker die ClusterForge mag veranderen.
func (s *Service) user(ctx context.Context, clusterID, id uuid.UUID, name string) (dbTarget, UserInfo, error) {
	if err := dbsql.CheckUser(name); err != nil {
		return dbTarget{}, UserInfo{}, &InvalidError{"user", err.Error()}
	}
	t, err := s.target(ctx, clusterID, id)
	if err != nil {
		return t, UserInfo{}, err
	}
	us, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBUsers})
	if err != nil {
		return t, UserInfo{}, err
	}
	users := groupUsers(us.Users)
	i := slices.IndexFunc(users, func(u UserInfo) bool { return u.Name == name })
	if i < 0 {
		return t, UserInfo{}, &ConflictError{"de gebruiker " + name + " bestaat niet"}
	}
	return t, users[i], nil
}

// DropDatabase haalt een database weg; confirm is de naam die de
// gebruiker intikte.
func (s *Service) DropDatabase(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, name, confirm string) error {
	if err := dbsql.CheckDatabase(name); err != nil {
		return &InvalidError{"name", err.Error()}
	}
	if confirm != name {
		return &InvalidError{"confirm", "tik ter bevestiging de naam van de database in: " + name}
	}
	t, err := s.target(ctx, clusterID, id)
	if err != nil {
		return err
	}
	list, err := s.databases(ctx, t)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list.Databases, func(d DatabaseInfo) bool { return d.Name == name })
	if i < 0 {
		return &ConflictError{"de database " + name + " bestaat niet"}
	}
	d := list.Databases[i]
	if _, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{Database: name, Confirm: confirm}}); err != nil {
		return err
	}
	s.dbEvent(ctx, actor, t, "cluster.database_dropped", map[string]any{
		"database": name, "size_bytes": d.SizeBytes, "tables": d.Tables, "connections": d.Connections, "node": t.node.Hostname,
	})
	return nil
}

// DropUser haalt een gebruiker op al zijn hosts weg; confirm is de naam die
// de gebruiker intikte.
func (s *Service) DropUser(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, name, confirm string) error {
	if confirm != name {
		return &InvalidError{"confirm", "tik ter bevestiging de naam van de gebruiker in: " + name}
	}
	t, u, err := s.user(ctx, clusterID, id, name)
	if err != nil {
		return err
	}
	if _, err := s.dbRequest(ctx, t, protocol.DBRequest{Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: u.Name, Hosts: u.Hosts, Confirm: confirm}}); err != nil {
		return err
	}
	s.dbEvent(ctx, actor, t, "cluster.database_user_dropped", map[string]any{"user": u.Name, "hosts": u.Hosts, "node": t.node.Hostname})
	return nil
}

// dbEvent schrijft wat er veranderde in het logboek, nooit met een
// wachtwoord, en laat het menu Databases opnieuw lezen. Het verzoek is al
// gelukt, dus een fout in het logboek wordt alleen gelogd.
func (s *Service) dbEvent(ctx context.Context, actor events.Actor, t dbTarget, action string, payload map[string]any) {
	payload["name"], payload["component"] = t.cluster.Name, title(t.spec)
	err := s.ev.Write(ctx, s.q, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: t.cluster.ID.String(), ClusterID: &t.cluster.ID, Action: action, Payload: payload,
	})
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", action, "err", err)
	}
	if s.DBChanged != nil {
		s.DBChanged()
	}
}
