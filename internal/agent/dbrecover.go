package agent

// De positie van een Galera-node na een crash staat alleen in InnoDB.
// galera_recovery haalt haar eruit door mariadbd even te starten met
// --wsrep-recover, zonder netwerk. Sinds MariaDB 12.3.2 zit dat script in
// het pakket mariadb-server-galera; op een node zonder dat pakket doet de
// agent zelf wat het script doet.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// noRecoveryScript zegt waarom de agent het zonder galera_recovery doet.
const noRecoveryScript = "galera_recovery ontbreekt op deze node (sinds MariaDB 12.3.2 zit het in het pakket " + galera.Package + ")"

// recoverPosition leest de positie uit InnoDB; MariaDB moet stilstaan. via
// is het programma dat haar las.
func (a *Agent) recoverPosition(ctx context.Context) (pos galera.Position, via string, err error) {
	out, err := a.dbRun(ctx, "galera_recovery")
	switch {
	case err == nil:
		pos, err := galera.ParseRecovered(string(out))
		if err != nil {
			return pos, "galera_recovery", fmt.Errorf("galera_recovery: %w", err)
		}
		return pos, "galera_recovery", nil
	case !notInstalled(err):
		return galera.Position{Seqno: -1}, "galera_recovery", errors.New("galera_recovery: " + dbError(err))
	}
	// Zoals galera_recovery: als de gebruiker uit de config, met de log
	// op stderr in plaats van in het logbestand.
	defaults, _ := a.printDefaults(ctx)
	args := []string{"--user=" + cmp.Or(defaults["user"], "mysql"), "--wsrep-recover", "--disable-log-error"}
	for _, bin := range []string{"mariadbd", "mysqld"} {
		// rctx loopt niet later af dan de tijdslimiet van execTimeout; zo
		// is te zien dat de agent mariadbd afbrak.
		rctx, cancel := context.WithTimeout(ctx, protocol.DBRecoverTimeout)
		out, err = a.execTimeout(rctx, protocol.DBRecoverTimeout, bin, args...)
		stopped := rctx.Err() != nil
		cancel()
		if err != nil && notInstalled(err) {
			continue
		}
		via = bin + " --wsrep-recover"
		switch {
		case err != nil && stopped:
			return galera.Position{Seqno: -1}, via, fmt.Errorf("%s, en de agent brak %s af na de tijdslimiet (%s); InnoDB was nog bezig met herstellen. "+
				"Laat het met de hand uitlopen: mariadbd --user=mysql --wsrep-recover --disable-log-error. Laatste regel: %s",
				noRecoveryScript, via, protocol.DBRecoverTimeout, recoverFailure(out, nil))
		case err != nil:
			return galera.Position{Seqno: -1}, via, fmt.Errorf("%s, en %s lukte niet: %s", noRecoveryScript, via, recoverFailure(out, err))
		}
		pos, err := galera.ParseRecovered(string(out))
		switch {
		case err == nil:
			return pos, via, nil
		case strings.Contains(string(out), "skipping position recovery"):
			return pos, via, fmt.Errorf("%s, en volgens %s staat Galera uit (wsrep_on)", noRecoveryScript, bin)
		}
		return pos, via, fmt.Errorf("%s, en %s gaf geen positie: %s", noRecoveryScript, via, recoverFailure(out, nil))
	}
	return galera.Position{Seqno: -1}, "", errors.New(noRecoveryScript + ", en mariadbd ook")
}

// recoverFailure is de regel uit de log van mariadbd die zegt wat er mis
// ging: de eerste met [ERROR], anders de laatste.
func recoverFailure(out []byte, err error) string {
	var last string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.Contains(line, "[ERROR]"):
			return clip(line, 300)
		}
		last = line
	}
	switch {
	case last != "":
		return clip(last, 300)
	case err != nil:
		return err.Error()
	}
	return "lege uitvoer"
}
