package agent

// Dumps van PostgreSQL: lezen wat er te dumpen is, en verbinden met de
// primary om een dump terug te zetten. pg_dump zelf staat bij de dumps van
// MariaDB in dbdump.go.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
)

// pgDumpInfo leest of db bestaat, met haar tabellen en haar grootte.
func (a *Agent) pgDumpInfo(ctx context.Context, db string) (exists bool, tables int, size int64, err error) {
	q, err := dbsql.PGDumpInfo(db)
	if err != nil {
		return false, 0, 0, err
	}
	rows, err := a.psql(ctx, "", q)
	if err != nil {
		return false, 0, 0, errors.New("database lezen: " + dbError(err))
	}
	if len(rows) != 1 || len(rows[0]) < 2 {
		return false, 0, 0, errors.New("database lezen: onverwacht antwoord")
	}
	if rows[0][0] == "0" {
		return false, 0, 0, nil
	}
	size, _ = strconv.ParseInt(rows[0][1], 10, 64)
	if rows, err = a.psql(ctx, db, dbsql.PGDumpTables); err != nil {
		return false, 0, 0, errors.New("tabellen lezen: " + dbError(err))
	}
	if len(rows) != 1 || len(rows[0]) < 1 {
		return false, 0, 0, errors.New("tabellen lezen: onverwacht antwoord")
	}
	tables, _ = strconv.Atoi(rows[0][0])
	return true, tables, size, nil
}

// pgLogin is een tijdelijke gebruiker op een andere PostgreSQL: de
// beheerder op de primary bij terugzetten, of de gebruiker die een
// verhuizing leest. Het wachtwoord staat in een passfile in een eigen map
// die alleen root kan lezen, nooit in een argument of de omgeving van psql.
type pgLogin struct {
	host      string
	port      int
	user, dir string
	// timeout is connect_timeout in seconden; 0 is 10.
	timeout int
}

func newPGLogin(host string, port int, user, password string) (*pgLogin, error) {
	return newPGLoginIn("cf-pglogin-", host, port, user, password)
}

// newPGLoginIn maakt de map met het wachtwoord met dit voorvoegsel.
func newPGLoginIn(prefix, host string, port int, user, password string) (*pgLogin, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, fmt.Errorf("map voor het wachtwoord: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pgpass"), []byte("*:*:*:"+user+":"+password+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("wachtwoordbestand: %w", err)
	}
	return &pgLogin{host: host, port: port, user: user, dir: dir}, nil
}

func (l *pgLogin) close() {
	if l != nil {
		_ = os.RemoveAll(l.dir)
	}
}

// conninfo verbindt met db.
func (l *pgLogin) conninfo(db string) string {
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s passfile=%s connect_timeout=%d application_name=clusterforge",
		l.host, l.port, db, l.user, conninfoQuote(filepath.Join(l.dir, "pgpass")), cmp.Or(l.timeout, 10))
}

// args zijn de opties van psql voor opdrachten op stdin, in db, die stoppen
// bij de eerste fout.
func (l *pgLogin) args(db string) []string {
	return []string{"-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", l.conninfo(db)}
}

// conninfoQuote zet een waarde tussen enkele aanhalingstekens, zoals libpq
// dat leest.
func conninfoQuote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// pgRemoteExists leest of db op de primary bestaat.
func (a *Agent) pgRemoteExists(ctx context.Context, l *pgLogin, db string) (bool, error) {
	q, err := dbsql.PGDumpInfo(db)
	if err != nil {
		return false, err
	}
	out, err := a.dbRun(ctx, "psql", "-XAtq", "-F", dbsql.PGFieldSep, "-R", dbsql.PGRecordSep, "-d", l.conninfo("postgres"), "-c", q)
	if err != nil {
		return false, fmt.Errorf("PostgreSQL op %s: %s", l.host, dbError(err))
	}
	rows := psqlRows(out)
	if len(rows) != 1 || len(rows[0]) < 1 {
		return false, fmt.Errorf("PostgreSQL op %s: onverwacht antwoord", l.host)
	}
	return rows[0][0] != "0", nil
}
