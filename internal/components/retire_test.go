package components

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestRetireSources: Jonas' oude cluster db1 tot db3 met MariaDB en
// PostgreSQL is één oude server. Nextcloud verhuisde en is afgebouwd,
// gitea verhuisde en wacht, zabbix staat er nog. De regel van Nextcloud kan
// weg, die van Gitea nog niet.
func TestRetireSources(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	dbCluster, ncCluster, giteaCluster := uuid.New(), uuid.New(), uuid.New()
	nodes := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	servers := func() []databases.Server {
		var out []databases.Server
		for i, id := range nodes {
			out = append(out, databases.Server{NodeID: id, Hostname: []string{"db1", "db2", "db3"}[i], Running: true})
		}
		return out
	}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour
	user := func(kind string, cluster *uuid.UUID, name string, seen time.Time, nodes ...string) databases.User {
		return databases.User{Kind: kind, ClusterID: cluster, Name: name, Nodes: nodes, DBUsers: []string{"app"}, LastSeen: seen}
	}
	maria := databases.Group{
		ClusterID: &dbCluster, ClusterName: "db", Engine: protocol.DBMariaDB, Kind: databases.KindGalera, Servers: servers(), DatabasesFrom: "db1",
		DatabasesTotal: 1,
		Databases:      []databases.Database{{Name: "zabbix", Users: []databases.User{user(databases.UserNode, nil, "zbx", ago(time.Minute), "zbx")}}},
		Clients: []databases.User{
			user(databases.UserCluster, &ncCluster, "Nextcloud", ago(20*day), "nc1"),
			user(databases.UserNode, nil, "zbx", ago(time.Minute), "zbx"),
		},
	}
	pg := databases.Group{
		ClusterID: &dbCluster, ClusterName: "db", Engine: protocol.DBPostgreSQL, Kind: databases.KindReplication, Servers: servers(),
		DatabasesFrom: "db1", DatabasesTotal: 2,
		Databases: []databases.Database{
			{Name: "gitea", Users: []databases.User{
				user(databases.UserCluster, &giteaCluster, "Gitea", ago(5*day), "git1"),
				user(databases.UserLocal, nil, "", ago(time.Hour), "db1"),
			}},
			{Name: "postgres"},
		},
		Clients: []databases.User{user(databases.UserCluster, &giteaCluster, "Gitea", ago(5*day), "git1")},
	}
	other := databases.Group{Engine: protocol.DBMariaDB, Servers: []databases.Server{{NodeID: uuid.New(), Hostname: "web1"}}, DatabasesFrom: "web1"}
	retired := ago(2 * day)
	rows := []store.ListDoneDBMigrationsRow{
		{DbMigration: store.DbMigration{ID: uuid.New(), ClusterID: giteaCluster, SourceKey: pg.Key(), Database: "gitea", SourceAddress: "10.0.20.180",
			SwitchedAt: ptr(ago(10 * day)), StartedAt: ago(10 * day)}, ClusterName: "Gitea"},
		{DbMigration: store.DbMigration{ID: uuid.New(), ClusterID: ncCluster, SourceKey: maria.Key(), Database: "nextcloud", SourceAddress: "10.0.20.180",
			SwitchedAt: ptr(ago(19 * day)), StartedAt: ago(19 * day), RetiredAt: &retired, RetireNode: "db2"}, ClusterName: "Nextcloud"},
		// Een oudere verhuizing van dezelfde database telt niet.
		{DbMigration: store.DbMigration{ID: uuid.New(), ClusterID: ncCluster, SourceKey: maria.Key(), Database: "nextcloud",
			SwitchedAt: ptr(ago(40 * day)), StartedAt: ago(40 * day)}, ClusterName: "Nextcloud"},
	}
	out := retireSources([]databases.Group{other, maria, pg}, rows, now)
	if len(out) != 1 {
		t.Fatalf("oude servers: %+v", out)
	}
	src := out[0]
	if src.Name != "db" || src.ClusterID == nil || *src.ClusterID != dbCluster || strings.Join(src.Servers, " ") != "db1 db2 db3" ||
		strings.Join(src.Keys, " ") != maria.Key()+" "+pg.Key() || !src.Readable || src.Empty || len(src.Moves) != 2 {
		t.Fatalf("oude server: %+v", src)
	}
	var left []string
	for _, r := range src.Remaining {
		left = append(left, r.Database+map[bool]string{true: "*", false: ""}[r.Moved])
	}
	if strings.Join(left, " ") != "zabbix gitea*" {
		t.Fatalf("nog op db1 tot db3: %v", left)
	}

	// gitea: Gitea verbond vijf dagen geleden nog, dus nog twee dagen
	// wachten; de back-up op db1 zelf telt niet.
	gitea := src.Moves[0]
	if gitea.Migration.Database != "gitea" || gitea.Ready || !gitea.Exists || gitea.LastBy != "Gitea (git1) als app" ||
		!gitea.ReadyAt.Equal(now.Add(2*day)) || gitea.LocalBy != "de databasenode zelf (db1) als app" || !gitea.LastLocal.Equal(ago(time.Hour)) {
		t.Fatalf("gitea: %+v", gitea)
	}
	nc := src.Moves[1]
	if nc.Migration.Database != "nextcloud" || nc.Ready || nc.ReadyAt != nil || nc.Exists {
		t.Fatalf("nextcloud: %+v", nc)
	}
	var rules []string
	for _, r := range src.Rules {
		rules = append(rules, r.ClusterName+" "+r.Address+":"+map[int]string{3306: "3306", 5432: "5432"}[r.Port]+" "+r.State)
	}
	// Gitea verbond na het overschakelen nog: de regel is in gebruik.
	if strings.Join(rules, ", ") != "Gitea 10.0.20.180:5432 in_use, Nextcloud 10.0.20.180:3306 unused" {
		t.Fatalf("regels: %v", rules)
	}

	// Tien dagen later is gitea klaar om af te bouwen.
	if mv := retireSources([]databases.Group{maria, pg}, rows, now.Add(10*day))[0].Moves[0]; !mv.Ready {
		t.Fatalf("gitea na tien dagen: %+v", mv)
	}

	// Alles weg: leeg.
	maria.Databases, maria.DatabasesTotal = nil, 0
	pg.Databases, pg.DatabasesTotal = []databases.Database{{Name: "postgres"}}, 1
	rows[0].DbMigration.RetiredAt = &retired
	src = retireSources([]databases.Group{maria, pg}, rows, now)[0]
	if !src.Empty || len(src.Remaining) != 0 {
		t.Fatalf("leeg: %+v", src)
	}
	// Een server die geen lijst gaf: niet leeg, want onbekend.
	pg.DatabasesFrom = ""
	if src = retireSources([]databases.Group{maria, pg}, rows, now)[0]; src.Empty || src.Readable {
		t.Fatalf("onleesbaar: %+v", src)
	}
}

// TestRetireSameNodes: losse servers op dezelfde node horen bij elkaar, ook
// via een derde.
func TestRetireSameNodes(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	srv := func(ids ...uuid.UUID) []databases.Server {
		var out []databases.Server
		for _, id := range ids {
			out = append(out, databases.Server{NodeID: id})
		}
		return out
	}
	groups := []databases.Group{{Servers: srv(a)}, {Servers: srv(c)}, {Servers: srv(a, b)}, {Servers: srv(b, c)}, {Servers: srv(uuid.New())}}
	got := sameNodes(groups)
	if got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 0 || got[4] != 4 {
		t.Fatalf("sets: %v", got)
	}
}

func TestRetireKeepReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		u      *protocol.DBUser
		others []string
		pg     bool
		want   string
	}{
		{"alleen deze database", &protocol.DBUser{Databases: []protocol.DBGrant{{Database: "nextcloud"}}}, nil, false, ""},
		{"met \\_", &protocol.DBUser{Databases: []protocol.DBGrant{{Database: `next\_cloud`}}}, nil, false, ""},
		{"andere database", &protocol.DBUser{Databases: []protocol.DBGrant{{Database: "nextcloud"}, {Database: "zabbix"}, {Database: "z%"}}}, nil, false,
			"hij heeft ook rechten op zabbix en z%"},
		{"alle databases", &protocol.DBUser{Global: []string{"SELECT", "RELOAD"}}, nil, false, "hij heeft ook rechten op alle databases (SELECT, RELOAD)"},
		{"gebruikt een andere", &protocol.DBUser{}, []string{"zabbix"}, false, "hij gebruikte de laatste 30 dagen ook zabbix"},
		{"rol zonder inloggen", nil, nil, true, ""},
		{"CREATEDB", &protocol.DBUser{Global: []string{"CREATEDB"}}, nil, true, ""},
		{"superuser", &protocol.DBUser{Global: []string{"SUPERUSER", "CREATEDB"}}, nil, true, "hij is SUPERUSER"},
	} {
		db := "nextcloud"
		if tc.name == "met \\_" {
			db = "next_cloud"
		}
		if got := keepReason(tc.u, db, tc.others, tc.pg); got != tc.want {
			t.Errorf("%s: %q, wil %q", tc.name, got, tc.want)
		}
	}
}
