package agent

// Waar een Galera-node staat, voor het opnieuw beginnen van het cluster na
// een volledige uitval. Draait MariaDB, dan zegt Galera het zelf. Staat hij
// stil, dan staat de positie na een gewone stop in grastate.dat; na een
// crash niet, en dan haalt recoverPosition haar uit InnoDB. Dat start
// mariadbd even zonder netwerk en mag dus alleen als MariaDB echt stilstaat.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func (a *Agent) dbPosition(ctx context.Context, req *protocol.DBPositionRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil {
		req = &protocol.DBPositionRequest{}
	}
	units, err := a.dbUnits(ctx)
	if err != nil {
		res.Error = "systemctl show: " + err.Error()
		return res
	}
	u, ok := mariaUnit(units)
	if !ok {
		res.Error = "op deze node staat geen MariaDB"
		return res
	}
	p := &protocol.DBPositionResult{Unit: u.Name, Active: u.Active, Seqno: -1}
	res.Position = p
	if rows, err := a.maria(ctx, false, dbsql.MariaVariables); err == nil {
		vars := keyValues(rows)
		p.Running = true
		if !strings.EqualFold(vars["wsrep_on"], "ON") {
			p.Error = "MariaDB draait zonder Galera"
			return res
		}
		rows, err := a.maria(ctx, false, dbsql.MariaPosition)
		if err != nil {
			p.Error = "status: " + dbError(err)
			return res
		}
		st := keyValues(rows)
		p.Galera = galeraOf(vars, st)
		p.UUID = st["wsrep_cluster_state_uuid"]
		p.UptimeSeconds, _ = strconv.ParseInt(st["uptime"], 10, 64)
		if n, err := strconv.ParseInt(st["wsrep_last_committed"], 10, 64); err == nil {
			p.Seqno = n
		}
		p.From = protocol.DBFromRunning
		return res
	}
	path, err := a.path(galera.GrastateFile)
	if err != nil {
		p.Error = err.Error()
		return res
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Nog nooit met Galera gestart, of een teruggezette back-up: dat
		// zegt alleen InnoDB.
		if !req.Recover || u.Active != "inactive" && u.Active != "failed" {
			p.Error = galera.NoGrastate
			return res
		}
	case err != nil:
		p.Error = err.Error()
		return res
	default:
		pos, err := galera.ParseGrastate(string(b))
		if err != nil {
			p.Error = err.Error()
			return res
		}
		p.UUID, p.Seqno, p.SafeToBootstrap, p.From = pos.UUID, pos.Seqno, pos.SafeToBootstrap, protocol.DBFromGrastate
		if pos.Seqno >= 0 {
			return res
		}
		// Na een crash.
		switch {
		case !req.Recover:
			p.Error = "MariaDB stopte niet gewoon; de positie staat alleen nog in InnoDB"
			return res
		case u.Active != "inactive" && u.Active != "failed":
			p.Error = "MariaDB is " + u.Active + "; de positie lezen kan pas als hij stilstaat"
			return res
		}
	}
	r, via, err := a.recoverPosition(ctx)
	p.Via = via
	if err != nil {
		p.Error = err.Error()
		return res
	}
	p.UUID, p.Seqno, p.From = r.UUID, r.Seqno, protocol.DBFromRecovery
	return res
}
