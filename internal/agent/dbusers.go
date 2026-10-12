package agent

// Databases en gebruikers in MariaDB: lezen wie er is en met welke rechten,
// en een database of gebruiker aanmaken of weghalen. De opdrachten komen
// uit dbsql; ze gaan via stdin naar mariadb, zodat een wachtwoord nooit als
// argument in ps staat. mariadb herhaalt een mislukte opdracht standaard op
// stderr, met het wachtwoord erin; daarom --skip-print-query-on-error, en
// komt alleen de regel met ERROR in de fout, zonder het wachtwoord.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// granteeRe leest een GRANTEE uit information_schema: 'naam'@'host'.
var granteeRe = regexp.MustCompile(`^'(.*)'@'(.*)'$`)

// needMaria zegt waarom er op deze node niets met MariaDB kan.
func (a *Agent) needMaria(ctx context.Context) error {
	units, err := a.dbUnits(ctx)
	if err != nil {
		return fmt.Errorf("systemctl show: %w", err)
	}
	if _, ok := mariaUnit(units); !ok {
		return errors.New("op deze node staat geen MariaDB")
	}
	return nil
}

func (a *Agent) dbUsers(ctx context.Context) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}, Users: []protocol.DBUser{}}
	if err := a.needMaria(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	rows, err := a.maria(ctx, false, dbsql.MariaUsers)
	if err != nil {
		res.Error = "gebruikers: " + dbError(err)
		return res
	}
	index := map[[2]string]int{}
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		index[[2]string{r[0], r[1]}] = len(res.Users)
		res.Users = append(res.Users, protocol.DBUser{Name: r[0], Host: r[1]})
	}
	user := func(grantee string) *protocol.DBUser {
		m := granteeRe.FindStringSubmatch(grantee)
		if m == nil {
			return nil
		}
		if i, ok := index[[2]string{m[1], m[2]}]; ok {
			return &res.Users[i]
		}
		return nil
	}
	if rows, err = a.maria(ctx, false, dbsql.MariaUserGrants); err != nil {
		res.Error = "rechten: " + dbError(err)
		return res
	}
	for _, r := range rows {
		if len(r) < 2 || r[1] == "USAGE" {
			continue
		}
		if u := user(r[0]); u != nil {
			u.Global = append(u.Global, r[1])
		}
	}
	if rows, err = a.maria(ctx, false, dbsql.MariaSchemaGrants); err != nil {
		res.Error = "rechten per database: " + dbError(err)
		return res
	}
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		u := user(r[0])
		if u == nil {
			continue
		}
		// In een GRANT is _ een jokerteken; ClusterForge schrijft het als
		// \_, en zo staat het in de rechten.
		db := strings.NewReplacer(`\_`, "_", `\%`, "%").Replace(r[1])
		i := slices.IndexFunc(u.Databases, func(g protocol.DBGrant) bool { return g.Database == db })
		if i < 0 {
			i = len(u.Databases)
			u.Databases = append(u.Databases, protocol.DBGrant{Database: db})
		}
		u.Databases[i].Privileges = append(u.Databases[i].Privileges, r[2])
	}
	return res
}

func (a *Agent) dbEnsure(ctx context.Context, req *protocol.DBEnsureRequest) protocol.DBResponse {
	if req == nil {
		return protocol.DBResponse{Error: "ensure zonder inhoud", Servers: []protocol.DBServer{}}
	}
	sql, err := dbsql.Ensure(*req)
	if err != nil {
		return protocol.DBResponse{Error: err.Error(), Servers: []protocol.DBServer{}}
	}
	return a.dbWrite(ctx, sql, req.Password)
}

func (a *Agent) dbDrop(ctx context.Context, req *protocol.DBDropRequest) protocol.DBResponse {
	if req == nil {
		return protocol.DBResponse{Error: "drop zonder inhoud", Servers: []protocol.DBServer{}}
	}
	sql, err := dbsql.Drop(*req)
	if err != nil {
		return protocol.DBResponse{Error: err.Error(), Servers: []protocol.DBServer{}}
	}
	return a.dbWrite(ctx, sql, "")
}

// dbWrite voert opdrachten uit via stdin. Een fout is alleen de regel met
// ERROR, zonder secret.
func (a *Agent) dbWrite(ctx context.Context, sql, secret string) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if err := a.needMaria(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	args := []string{"--batch", "--skip-print-query-on-error"}
	out, err := a.runStdin(ctx, []byte(sql), "mariadb", args...)
	if err != nil && notInstalled(err) {
		out, err = a.runStdin(ctx, []byte(sql), "mysql", args...)
	}
	if err == nil {
		return res
	}
	msg := err.Error()
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "ERROR") {
			msg = strings.TrimSpace(line)
			break
		}
	}
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "********")
	}
	res.Error = msg
	return res
}

// runStdin voert een programma uit met stdin als invoer, en geeft stdout
// en stderr samen terug.
func (a *Agent) runStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if a.ExecStdin != nil {
		return a.ExecStdin(ctx, stdin, name, args...)
	}
	if a.Exec != nil {
		return nil, errors.New("agent: Exec zonder ExecStdin")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	return out, err
}
