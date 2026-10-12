package agent

// De databases op de node: de agent leest MariaDB en PostgreSQL via de
// socket, als root met unix_socket en als postgres met peer-authenticatie,
// zoals Debian ze instelt. Hij heeft dus geen wachtwoord nodig. Hij voert
// alleen de vaste query's uit dbsql uit, op de catalogus en de status, en
// leest nooit een rij uit een tabel van een app.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// dbSlots is hoeveel databaseverzoeken de agent tegelijk doet, en
	// dbQueueWait hoe lang een verzoek op een plek wacht.
	dbSlots     = 2
	dbQueueWait = 5 * time.Second
	// dbTableCounts is voor hoeveel PostgreSQL-databases de agent de
	// tabellen telt; dat kost een verbinding per database.
	dbTableCounts = 100
	// DefaultPatroniURL is de REST-API van Patroni op de node.
	DefaultPatroniURL = "http://127.0.0.1:8008"
)

// dbUnits zijn de units waaraan de agent ziet welke databases er op de node
// staan. mysql.service is op Debian een alias van mariadb.service.
var dbUnits = []string{"mariadb.service", "mysql.service", "postgresql.service", "patroni.service", "postgresql@*.service"}

type dbState struct {
	sem chan struct{}
}

func (a *Agent) initDB() {
	if a.db == nil {
		a.db = &dbState{sem: make(chan struct{}, dbSlots)}
	}
}

// onDB beantwoordt een databaseverzoek in een eigen goroutine.
func (a *Agent) onDB(m *nats.Msg) {
	go func() {
		res := a.handleDB(m.Data)
		data, err := envelope(protocol.TypeDBResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op databaseverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleDB(data []byte) protocol.DBResponse {
	var env protocol.Envelope
	var req protocol.DBRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeDBRequest {
		return protocol.DBResponse{Error: "ongeldig databaseverzoek", Servers: []protocol.DBServer{}}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil || req.ID == "" {
		return protocol.DBResponse{Error: "ongeldig databaseverzoek", Servers: []protocol.DBServer{}}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.DBResponse{Error: "databaseverzoek verlopen; klopt de klok van deze node?", Servers: []protocol.DBServer{}}
	}
	a.initDB()
	select {
	case a.db.sem <- struct{}{}:
		defer func() { <-a.db.sem }()
	case <-time.After(dbQueueWait):
		return protocol.DBResponse{Error: "de agent is te druk met andere databaseverzoeken; probeer het zo opnieuw", Servers: []protocol.DBServer{}}
	}
	timeout := protocol.DBTimeout
	if req.Op == protocol.DBPosition && req.Position != nil && req.Position.Recover {
		timeout = protocol.DBRecoverTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if !req.Deadline.IsZero() {
		var c2 context.CancelFunc
		ctx, c2 = context.WithDeadline(ctx, req.Deadline)
		defer c2()
	}
	var res protocol.DBResponse
	switch req.Op {
	case protocol.DBStatus:
		res = a.DBStatus(ctx)
	case protocol.DBTrack:
		res = a.dbTrack(ctx, req.Track)
	case protocol.DBPosition:
		res = a.dbPosition(ctx, req.Position)
	default:
		res = protocol.DBResponse{Error: "onbekend databaseverzoek " + req.Op, Servers: []protocol.DBServer{}}
	}
	if res.Error != "" {
		a.Log.Warn("databaseverzoek mislukt", "op", req.Op, "err", res.Error)
	}
	return res
}

// DBStatus leest elke MariaDB en PostgreSQL op de node.
func (a *Agent) DBStatus(ctx context.Context) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	units, err := a.dbUnits(ctx)
	if err != nil {
		res.Error = "systemctl show: " + err.Error()
		return res
	}
	if u, ok := mariaUnit(units); ok {
		res.Servers = append(res.Servers, a.mariaStatus(ctx, u))
	}
	if u, ok := pgUnit(units); ok {
		res.Servers = append(res.Servers, a.pgStatus(ctx, u))
	}
	return res
}

// dbRun voert een programma uit met stdout als uitvoer; bij een fout staat
// de eerste regel van stderr in de fout. Een vervangen Exec geeft stdout
// en stderr samen, en dan komt de eerste regel daarvan in de fout.
func (a *Agent) dbRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := (&Verifier{Exec: a.Exec}).run(ctx, name, args...)
	if err != nil && a.Exec != nil {
		if line := firstLine(string(out)); line != "" {
			err = fmt.Errorf("%w: %s", err, line)
		}
	}
	return out, err
}

func (a *Agent) dbUnits(ctx context.Context) ([]UnitInfo, error) {
	out, err := a.dbRun(ctx, "systemctl", append([]string{"show", "--property=Id,LoadState,ActiveState,UnitFileState"}, dbUnits...)...)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return parseUnits(out), nil
}

func mariaUnit(units []UnitInfo) (UnitInfo, bool) {
	for _, u := range units {
		if u.Name == "mariadb" || u.Name == "mysql" {
			return u, true
		}
	}
	return UnitInfo{}, false
}

// pgUnit kiest de unit die PostgreSQL start: Patroni als die er is, anders
// de eerste instantie die draait, anders de overkoepelende unit.
func pgUnit(units []UnitInfo) (UnitInfo, bool) {
	var umbrella, instance *UnitInfo
	for i, u := range units {
		switch {
		case u.Name == "patroni":
			return u, true
		case u.Name == "postgresql":
			umbrella = &units[i]
		case strings.HasPrefix(u.Name, "postgresql@") && (instance == nil || u.Active == "active" && instance.Active != "active"):
			instance = &units[i]
		}
	}
	switch {
	case instance != nil:
		return *instance, true
	case umbrella != nil:
		return *umbrella, true
	}
	return UnitInfo{}, false
}

// shortVersion haalt het achtervoegsel van de distributie van een versie:
// 11.8.3-MariaDB-0+deb13u1 wordt 11.8.3, "17.6 (Debian 17.6-1)" wordt 17.6.
func shortVersion(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, " -"); i > 0 {
		v = v[:i]
	}
	return v
}

func dbError(err error) string {
	if notInstalled(err) {
		return "het programma om de database te lezen ontbreekt: " + err.Error()
	}
	return firstLine(err.Error())
}

// --- MariaDB ---

// maria voert een query uit via de socket als root: -N zonder kolomnamen,
// -B met tabs, en een tab of regeleinde in een waarde als \t of \n.
func (a *Agent) maria(ctx context.Context, header bool, query string) ([][]string, error) {
	args := []string{"-B", "-e", query}
	if !header {
		args = append([]string{"-N"}, args...)
	}
	out, err := a.dbRun(ctx, "mariadb", args...)
	if err != nil && notInstalled(err) {
		out, err = a.dbRun(ctx, "mysql", args...)
	}
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		for i, f := range fields {
			fields[i] = unescapeBatch(f)
		}
		rows = append(rows, fields)
	}
	return rows, nil
}

// unescapeBatch keert de escapes van mariadb -B om.
func unescapeBatch(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case '0':
				b.WriteByte(0)
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func keyValues(rows [][]string) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		if len(r) >= 2 {
			m[strings.ToLower(r[0])] = r[1]
		}
	}
	return m
}

func (a *Agent) mariaStatus(ctx context.Context, u UnitInfo) protocol.DBServer {
	s := protocol.DBServer{Engine: protocol.DBMariaDB, Unit: u.Name, Active: u.Active, Databases: []protocol.DBDatabase{}}
	rows, err := a.maria(ctx, false, dbsql.MariaVariables)
	if err != nil {
		s.Error = dbError(err)
		return s
	}
	vars := keyValues(rows)
	s.Running = true
	s.Version = shortVersion(vars["version"])
	s.MaxConnections, _ = strconv.Atoi(vars["max_connections"])
	s.ReadOnly = strings.EqualFold(vars["read_only"], "ON")
	var errs []string
	if rows, err = a.maria(ctx, false, dbsql.MariaStatus); err != nil {
		errs = append(errs, "status: "+dbError(err))
	} else {
		st := keyValues(rows)
		s.Connections, _ = strconv.Atoi(st["threads_connected"])
		s.UptimeSeconds, _ = strconv.ParseInt(st["uptime"], 10, 64)
		if strings.EqualFold(vars["wsrep_on"], "ON") {
			s.Galera = galeraOf(vars, st)
		}
	}
	if s.Galera == nil {
		if rows, err := a.maria(ctx, true, dbsql.MariaSource); err != nil {
			errs = append(errs, "replicatie: "+dbError(err))
		} else {
			s.Source = mariaSourceOf(rows)
		}
		if rows, err := a.maria(ctx, false, dbsql.MariaReplicas); err != nil {
			errs = append(errs, "replica's: "+dbError(err))
		} else {
			for _, r := range rows {
				s.Replicas = append(s.Replicas, protocol.DBReplica{Address: hostOnly(r[0]), State: "streaming"})
			}
		}
	}
	switch {
	case s.Galera != nil:
		s.Role = protocol.DBRoleMember
	case s.Source != nil:
		s.Role = protocol.DBRoleReplica
	case len(s.Replicas) > 0:
		s.Role = protocol.DBRolePrimary
	default:
		s.Role = protocol.DBRoleStandalone
	}
	if rows, err := a.maria(ctx, false, dbsql.MariaDatabases); err != nil {
		errs = append(errs, "databases: "+dbError(err))
	} else {
		for _, r := range rows {
			if len(r) < 3 {
				continue
			}
			d := protocol.DBDatabase{Name: r[0]}
			d.SizeBytes, _ = strconv.ParseInt(r[1], 10, 64)
			d.Tables, _ = strconv.Atoi(r[2])
			addDatabase(&s, d)
		}
	}
	if rows, err := a.maria(ctx, false, dbsql.MariaClients); err != nil {
		errs = append(errs, "clients: "+dbError(err))
	} else {
		var cs clients
		for _, r := range rows {
			if len(r) >= 3 {
				cs.add(r[0], mariaClientHost(r[1]), r[2], 1)
			}
		}
		s.Clients = cs.list()
	}
	s.Error = strings.Join(errs, "; ")
	return s
}

// galeraOf maakt de toestand van Galera uit de variabelen en de status.
func galeraOf(vars, st map[string]string) *protocol.DBGalera {
	g := &protocol.DBGalera{
		ClusterName: vars["wsrep_cluster_name"], ClusterUUID: st["wsrep_cluster_state_uuid"],
		ClusterStatus: st["wsrep_cluster_status"], State: st["wsrep_local_state_comment"],
		Ready: strings.EqualFold(st["wsrep_ready"], "ON"),
	}
	g.ClusterSize, _ = strconv.Atoi(st["wsrep_cluster_size"])
	g.StateCode, _ = strconv.Atoi(st["wsrep_local_state"])
	for addr := range strings.SplitSeq(st["wsrep_incoming_addresses"], ",") {
		if host := hostOnly(addr); host != "" && host != "AUTO" {
			g.Members = append(g.Members, host)
		}
	}
	return g
}

// mariaClientHost haalt de poort van HOST in de processlist: 10.0.20.186:51234
// wordt 10.0.20.186. localhost, een verbinding via de socket, wordt leeg.
func mariaClientHost(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.LastIndex(h, ":"); i > 0 && strings.Trim(h[i+1:], "0123456789") == "" {
		h = h[:i]
	}
	return localAddress(strings.Trim(h, "[]"))
}

// localAddress maakt van een adres van de node zelf een leeg adres.
func localAddress(h string) string {
	if h == "localhost" {
		return ""
	}
	if ip, err := netip.ParseAddr(h); err == nil && ip.IsLoopback() {
		return ""
	}
	return h
}

// clients telt verbindingen per gebruiker, adres en database.
type clients struct {
	index map[protocol.DBClient]int
	out   []protocol.DBClient
}

func (c *clients) add(user, addr, db string, n int) {
	k := protocol.DBClient{User: user, Address: addr, Database: db}
	if i, ok := c.index[k]; ok {
		c.out[i].Count += n
		return
	}
	if len(c.out) >= protocol.DBMaxClients {
		return
	}
	if c.index == nil {
		c.index = map[protocol.DBClient]int{}
	}
	c.index[k] = len(c.out)
	k.Count = n
	c.out = append(c.out, k)
}

func (c *clients) list() []protocol.DBClient {
	slices.SortFunc(c.out, func(a, b protocol.DBClient) int {
		return cmp.Or(cmp.Compare(a.Database, b.Database), cmp.Compare(a.User, b.User), cmp.Compare(a.Address, b.Address))
	})
	return c.out
}

// mariaSourceOf leest SHOW ALL SLAVES STATUS met kolomnamen. Bij meer
// bronnen telt de eerste.
func mariaSourceOf(rows [][]string) *protocol.DBSource {
	if len(rows) < 2 {
		return nil
	}
	col := map[string]string{}
	for i, name := range rows[0] {
		if i < len(rows[1]) {
			col[name] = rows[1][i]
		}
	}
	src := &protocol.DBSource{
		Host:    col["Master_Host"],
		Running: col["Slave_IO_Running"] == "Yes" && col["Slave_SQL_Running"] == "Yes",
	}
	if lag, err := strconv.ParseInt(col["Seconds_Behind_Master"], 10, 64); err == nil {
		src.LagSeconds = &lag
	}
	for _, k := range []string{"Last_IO_Error", "Last_SQL_Error", "Last_Error"} {
		if col[k] != "" {
			src.Error = col[k]
			break
		}
	}
	return src
}

// hostOnly haalt de poort van een adres: 10.0.0.1:3306 wordt 10.0.0.1, en
// [fd00::1]:3306 wordt fd00::1.
func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "[") {
		if i := strings.Index(addr, "]"); i > 0 {
			return addr[1:i]
		}
	}
	if strings.Count(addr, ":") == 1 {
		addr, _, _ = strings.Cut(addr, ":")
	}
	return addr
}

// addDatabase zet een database in de lijst, tot DBMaxDatabases; de rest telt
// alleen mee in DatabasesTotal.
func addDatabase(s *protocol.DBServer, d protocol.DBDatabase) {
	s.DatabasesTotal++
	if len(s.Databases) < protocol.DBMaxDatabases {
		s.Databases = append(s.Databases, d)
	}
}

// --- PostgreSQL ---

// psql voert een query uit als postgres via de socket, in database db of
// in de standaarddatabase als db leeg is.
func (a *Agent) psql(ctx context.Context, db, query string) ([][]string, error) {
	args := []string{"-u", "postgres", "--", "psql", "-XAtq", "-F", dbsql.PGFieldSep, "-R", dbsql.PGRecordSep}
	if db != "" {
		args = append(args, "-d", db)
	}
	out, err := a.dbRun(ctx, "runuser", append(args, "-c", query)...)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for rec := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), dbsql.PGRecordSep) {
		if rec == "" {
			continue
		}
		rows = append(rows, strings.Split(rec, dbsql.PGFieldSep))
	}
	return rows, nil
}

func (a *Agent) pgStatus(ctx context.Context, u UnitInfo) protocol.DBServer {
	s := protocol.DBServer{Engine: protocol.DBPostgreSQL, Unit: u.Name, Active: u.Active, Databases: []protocol.DBDatabase{}}
	if u.Name == "patroni" {
		s.Patroni = a.patroni(ctx)
	}
	rows, err := a.psql(ctx, "", dbsql.PGInfo)
	if err != nil || len(rows) != 1 || len(rows[0]) < 6 {
		if err == nil {
			err = errors.New("onverwacht antwoord")
		}
		s.Error = dbError(err)
		return s
	}
	r := rows[0]
	s.Running = true
	s.Version = shortVersion(r[0])
	recovery := r[1] == "t"
	s.MaxConnections, _ = strconv.Atoi(r[2])
	s.Connections, _ = strconv.Atoi(r[3])
	s.UptimeSeconds, _ = strconv.ParseInt(r[4], 10, 64)
	s.ReadOnly = recovery || r[5] == "on"
	var errs []string
	if recovery {
		if rows, err := a.psql(ctx, "", dbsql.PGSource); err != nil {
			errs = append(errs, "replicatie: "+dbError(err))
		} else {
			src := &protocol.DBSource{}
			if len(rows) > 0 && len(rows[0]) >= 3 {
				src.Host, src.Running = rows[0][0], rows[0][1] == "streaming"
				if lag, err := strconv.ParseInt(rows[0][2], 10, 64); err == nil && lag >= 0 {
					src.LagSeconds = &lag
				}
				if !src.Running {
					src.Error = "de WAL-ontvanger staat op " + rows[0][1]
				}
			} else {
				src.Error = "geen WAL-ontvanger: de replica haalt niets van een primary"
			}
			s.Source = src
		}
	} else if rows, err := a.psql(ctx, "", dbsql.PGReplicas); err != nil {
		errs = append(errs, "replica's: "+dbError(err))
	} else {
		for _, r := range rows {
			if len(r) < 4 {
				continue
			}
			rep := protocol.DBReplica{Address: r[0], State: r[1], Sync: r[2]}
			rep.LagBytes, _ = strconv.ParseInt(r[3], 10, 64)
			s.Replicas = append(s.Replicas, rep)
		}
	}
	switch {
	case recovery:
		s.Role = protocol.DBRoleReplica
	case len(s.Replicas) > 0 || s.Patroni != nil:
		s.Role = protocol.DBRolePrimary
	default:
		s.Role = protocol.DBRoleStandalone
	}
	if rows, err := a.psql(ctx, "", dbsql.PGDatabases); err != nil {
		errs = append(errs, "databases: "+dbError(err))
	} else {
		counted := 0
		for _, r := range rows {
			if len(r) < 2 {
				continue
			}
			d := protocol.DBDatabase{Name: r[0], Tables: -1}
			d.SizeBytes, _ = strconv.ParseInt(r[1], 10, 64)
			// psql leest een naam met = als verbindingsgegevens; zulke
			// namen telt de agent niet.
			if counted < dbTableCounts && dbNameRe.MatchString(d.Name) {
				counted++
				if t, err := a.psql(ctx, d.Name, dbsql.PGTables); err == nil && len(t) == 1 {
					d.Tables, _ = strconv.Atoi(t[0][0])
				}
			}
			// De standaarddatabase postgres telt alleen als er tabellen in
			// staan.
			if d.Name == "postgres" && d.Tables <= 0 {
				continue
			}
			addDatabase(&s, d)
		}
	}
	if rows, err := a.psql(ctx, "", dbsql.PGClients); err != nil {
		errs = append(errs, "clients: "+dbError(err))
	} else {
		var cs clients
		for _, r := range rows {
			if len(r) < 4 {
				continue
			}
			n, _ := strconv.Atoi(r[3])
			cs.add(r[0], localAddress(r[1]), r[2], n)
		}
		s.Clients = cs.list()
	}
	s.Error = strings.Join(errs, "; ")
	return s
}

// patroni leest wat Patroni over deze node zegt.
func (a *Agent) patroni(ctx context.Context) *protocol.DBPatroni {
	base := a.PatroniURL
	if base == "" {
		base = DefaultPatroniURL
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/patroni", nil)
	if err != nil {
		return &protocol.DBPatroni{Error: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &protocol.DBPatroni{Error: "de REST-API van Patroni op " + base + " antwoordt niet"}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var v struct {
		State    string `json:"state"`
		Role     string `json:"role"`
		Timeline int    `json:"timeline"`
		Patroni  struct {
			Scope string `json:"scope"`
		} `json:"patroni"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return &protocol.DBPatroni{Error: fmt.Sprintf("onverwacht antwoord van Patroni (HTTP %d)", resp.StatusCode)}
	}
	role := v.Role
	if role == "master" {
		role = "primary"
	}
	return &protocol.DBPatroni{Scope: v.Patroni.Scope, Role: role, State: v.State, Timeline: v.Timeline}
}
