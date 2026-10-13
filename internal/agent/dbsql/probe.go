package dbsql

// De schrijftest van een failovertest: een kleine tabel met per run de
// volgnummers van de rijen. De agent schrijft er via het VIP in met de
// gebruiker protocol.DBProbeUser; een run is altijd een uuid en een
// volgnummer een getal, dus er komt geen tekst van buiten in de SQL.

import (
	"fmt"
	"regexp"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ProbeRunRe is het id van een run: een uuid in kleine letters.
var ProbeRunRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// probeTable is de tabel met de naam van de database ervoor bij MariaDB;
// bij PostgreSQL verbindt psql met de database zelf.
func probeTable(engine string) string {
	if engine == protocol.DBPostgreSQL {
		return protocol.DBProbeTable
	}
	return "`" + protocol.DBProbeDatabase + "`.`" + protocol.DBProbeTable + "`"
}

// ProbeCreate maakt de tabel als ze er nog niet is. Galera wil een
// primaire sleutel op elke tabel.
func ProbeCreate(engine string) string {
	if engine == protocol.DBPostgreSQL {
		return "CREATE TABLE IF NOT EXISTS " + protocol.DBProbeTable +
			" (run uuid NOT NULL, seq bigint NOT NULL, at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (run, seq))"
	}
	return "CREATE TABLE IF NOT EXISTS " + probeTable(engine) +
		" (run CHAR(36) NOT NULL, seq BIGINT NOT NULL, at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), PRIMARY KEY (run, seq)) ENGINE=InnoDB"
}

func checkRun(run string) error {
	if !ProbeRunRe.MatchString(run) {
		return fmt.Errorf("%q is geen id van een run", run)
	}
	return nil
}

// ProbeInsert schrijft rij seq van run.
func ProbeInsert(engine, run string, seq int64) (string, error) {
	if err := checkRun(run); err != nil {
		return "", err
	}
	return fmt.Sprintf("INSERT INTO %s (run, seq) VALUES ('%s', %d)", probeTable(engine), run, seq), nil
}

// ProbeSelect leest de volgnummers van run.
func ProbeSelect(engine, run string) (string, error) {
	if err := checkRun(run); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT seq FROM %s WHERE run = '%s' ORDER BY seq", probeTable(engine), run), nil
}

// ProbeDelete haalt de rijen van run weg, en die van runs van meer dan een
// dag geleden die niet afgerond werden.
func ProbeDelete(engine, run string) (string, error) {
	if err := checkRun(run); err != nil {
		return "", err
	}
	old := "NOW() - INTERVAL 1 DAY"
	if engine == protocol.DBPostgreSQL {
		old = "now() - interval '1 day'"
	}
	return fmt.Sprintf("DELETE FROM %s WHERE run = '%s' OR at < %s", probeTable(engine), run, old), nil
}
