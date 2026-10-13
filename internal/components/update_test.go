package components

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De volgorde van een rollende wijziging: wat in order staat eerst, de
// rest erachter op de gewone volgorde.
func TestOrdered(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	spec := Spec{Nodes: []Node{
		{NodeID: ids[0], Hostname: "nc1", Role: RoleData, Index: 1},
		{NodeID: ids[1], Hostname: "nc2", Role: RoleData, Index: 2},
		{NodeID: ids[2], Hostname: "nc3", Role: RoleData, Index: 3},
		{NodeID: ids[3], Hostname: "arb", Role: RoleArbiter, Index: 1},
	}}
	names := func(ns []Node) string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Hostname)
		}
		return strings.Join(out, " ")
	}
	if got := names(spec.ordered(nil)); got != "nc1 nc2 nc3 arb" {
		t.Errorf("zonder volgorde: %s", got)
	}
	if got := names(spec.ordered([]uuid.UUID{ids[1], ids[2], ids[0]})); got != "nc2 nc3 nc1 arb" {
		t.Errorf("nc1 als laatste: %s", got)
	}
}

func TestUpdatable(t *testing.T) {
	pkgs := []string{"mariadb-server", "mariadb-client-core", "galera-4", "libmariadb3", "mysql-common", "openssl", "postgresql-17", "libpq5", "patroni", "python3-etcd", "postgresql-common"}
	if got := updatable(protocol.DBMariaDB, RoleData, pkgs); !slices.Equal(got, []string{"mariadb-server", "mariadb-client-core", "galera-4", "libmariadb3", "mysql-common"}) {
		t.Errorf("MariaDB: %v", got)
	}
	if got := updatable(protocol.DBMariaDB, RoleArbiter, pkgs); !slices.Equal(got, []string{"galera-4"}) {
		t.Errorf("garbd: %v", got)
	}
	if got := updatable(protocol.DBPostgreSQL, RoleData, pkgs); !slices.Equal(got, []string{"postgresql-17", "libpq5", "patroni", "python3-etcd", "postgresql-common"}) {
		t.Errorf("PostgreSQL: %v", got)
	}
	if got := updatable(protocol.DBPostgreSQL, RoleArbiter, pkgs); got != nil {
		t.Errorf("scheidsrechter van Patroni: %v", got)
	}
}

// De stappen van bijwerken hebben elk een merkteken van de taak, en de
// patronen staan tussen enkele aanhalingstekens, zodat de shell ze niet
// uitvouwt.
func TestUpdateSteps(t *testing.T) {
	id := uuid.MustParse("6f1c2b8e-0000-4000-8000-000000000001")
	m := "/var/lib/clusterforge/db-update-" + id.String()
	check, found, install, restart := updateSteps(id, updatePackages[protocol.DBMariaDB][RoleData], "mariadb")
	if check.Command.Creates != m+".checked" || install.Command.Creates != m+".done" || found.Command.Unless != "test ! -e "+m+".found" {
		t.Errorf("merktekens: %+v %+v %+v", check.Command, install.Command, found.Command)
	}
	if !strings.Contains(check.Command.Run, `dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' 'mariadb-*' 'libmariadb*' 'galera-*' 'mysql-common'`) ||
		!strings.Contains(check.Command.Run, "! -name 'db-update-"+id.String()+"*' -delete") {
		t.Errorf("check: %s", check.Command.Run)
	}
	if !strings.Contains(install.Command.Run, "date +%s > "+m+".t0") || !strings.Contains(install.Command.Run, "cp "+m+".t0 "+m+".restart") ||
		!strings.Contains(install.Command.Run, "--force-confold $pkgs") || strings.Contains(install.Command.Run, "%!") {
		t.Errorf("install: %s", install.Command.Run)
	}
	if restart.Command.Run != "systemctl restart --no-block mariadb && rm -f "+m+".restart" ||
		!strings.HasPrefix(restart.Command.Unless, "test ! -e "+m+".restart || ") || !strings.Contains(restart.Command.Unless, "+%s") {
		t.Errorf("restart: %+v", restart.Command)
	}
	_, _, _, restart = updateSteps(id, updatePackages[protocol.DBPostgreSQL][RoleData], "patroni")
	if restart.Command.Run != "systemctl restart patroni && rm -f "+m+".restart" {
		t.Errorf("patroni: %s", restart.Command.Run)
	}
}
