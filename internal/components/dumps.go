package components

// Back-ups van de databases. Elke nacht maakt ClusterForge van elke
// database van een onderdeel een dump. Bij MariaDB op een datanode zonder
// het VIP, zodat de node die schrijft geen extra last krijgt. Bij
// PostgreSQL op de primary: een lange pg_dump op een replica breekt
// PostgreSQL af zodra de primary iets opruimt dat de dump nog leest (na
// max_standby_streaming_delay, standaard 30 seconden), en langer laten
// wachten houdt een failover op. De dump blijft op die node, in
// protocol.DBDumpDir, en gaat mee in de Proxmox-back-up ervan.
// Na elke ronde haalt ClusterForge op elke datanode de dumps weg die
// ouder zijn dan het aantal dagen dat ze blijven, behalve de nieuwste
// goede per database.
//
// Een dump is goed als hij compleet is en alle tabellen bevat. Is hij meer
// dan de helft kleiner dan de vorige goede, of mist er een tabel, dan is
// hij verdacht: hij blijft staan, maar telt niet als nieuwste goede.
// Terugzetten gaat in een nieuwe database naast de bestaande,
// <naam>_herstel_<datum>, zodat Jonas eerst kan kijken. Bij PostgreSQL
// staat de dump niet altijd op de primary, waar terugzetten moet; dan
// maakt ClusterForge op de primary een tijdelijke beheerder, waarmee de
// agent met de dump over het netwerk terugzet, en haalt hem daarna weg.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// DumpKind maakt dumps, RestoreKind zet er een terug.
	DumpKind    = "component.dump"
	RestoreKind = "component.restore"
	// Wie een dump begon.
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	// DumpStaleAfter: zonder goede dump van de laatste 26 uur staat een
	// database bij Back-ups als te oud; een nacht plus marge.
	DumpStaleAfter = 26 * time.Hour
	// restoreInfix staat in de standaardnaam van een teruggezette kopie.
	// Die krijgt geen nachtelijke dump: het is een kopie om naar te kijken.
	// Een kopie met een eigen naam is een gewone database.
	restoreInfix = "_herstel_"
)

// Hoe vers de dumps van een database zijn.
const (
	DumpFresh   = "ok"
	DumpStale   = "stale"
	DumpMissing = "missing"
	// DumpOff: de nachtelijke dumps staan uit.
	DumpOff = "off"
)

var backupAtRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// tempRoleRe zijn de tijdelijke beheerders in PostgreSQL waarmee een agent
// een dump terugzet op de primary.
var tempRoleRe = regexp.MustCompile(protocol.DBTempRolePattern)

// BackupSettings is wanneer de nachtelijke dump komt en hoe lang hij
// blijft.
type BackupSettings struct {
	On       bool
	At       string
	KeepDays int
	// NextAt is de volgende nachtelijke dump; nil als die uit staat.
	NextAt *time.Time
}

// DatabaseDumps is de nieuwste goede dump van een database, en de laatste
// poging als die daarna mislukte.
type DatabaseDumps struct {
	Database  string
	Latest    *store.DbDump
	Failed    *store.DbDump
	Freshness string
}

// ActiveJob is een dump of terugzetten dat wacht of loopt.
type ActiveJob struct {
	ID    uuid.UUID
	Kind  string
	Title string
}

// DumpOverview zijn de back-ups van een onderdeel.
type DumpOverview struct {
	Settings  BackupSettings
	Databases []DatabaseDumps
	Dumps     []store.DbDump
	Active    *ActiveJob
}

// DatabaseBackup is een database met haar nieuwste dump, voor Back-ups.
type DatabaseBackup struct {
	ClusterID   uuid.UUID
	ClusterName string
	ComponentID uuid.UUID
	Engine      string
	DatabaseDumps
	Settings BackupSettings
}

type dumpParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	// Databases leeg is alle databases op de node.
	Databases []string `json:"databases,omitempty"`
	Trigger   string   `json:"trigger"`
}

type restoreParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	DumpID      uuid.UUID `json:"dump_id"`
	Target      string    `json:"target"`
}

// restoreState is wat de stap van terugzetten onthoudt, zodat een hervatte
// taak verder wacht: wanneer het begon, en bij PostgreSQL de tijdelijke
// beheerder en de primary. Het wachtwoord staat er nooit in.
type restoreState struct {
	At      time.Time `json:"at"`
	Role    string    `json:"role,omitempty"`
	Primary string    `json:"primary,omitempty"`
}

// dumpPick is wat de eerste stap van een dump koos.
type dumpPick struct {
	Node      Node     `json:"node"`
	Databases []string `json:"databases"`
}

func settingsOf(c store.ClusterComponent) BackupSettings {
	b := BackupSettings{On: c.BackupOn, At: c.BackupAt, KeepDays: int(c.BackupKeepDays)}
	if c.BackupOn {
		b.NextAt = c.BackupNextAt
	}
	return b
}

// nextBackup is het eerste moment na after waarop het at is, in de
// tijdzone van de server.
func nextBackup(at string, after time.Time) time.Time {
	m := backupAtRe.FindStringSubmatch(at)
	if m == nil {
		m = []string{"", "02", "00"}
	}
	var h, mm int
	_, _ = fmt.Sscanf(m[1]+" "+m[2], "%d %d", &h, &mm)
	local := after.Local()
	t := time.Date(local.Year(), local.Month(), local.Day(), h, mm, 0, 0, time.Local)
	if !t.After(after) {
		t = time.Date(local.Year(), local.Month(), local.Day()+1, h, mm, 0, 0, time.Local)
	}
	return t
}

func freshness(on bool, latest *store.DbDump, now time.Time) string {
	switch {
	case !on:
		return DumpOff
	case latest == nil:
		return DumpMissing
	case now.Sub(latest.StartedAt) > DumpStaleAfter:
		return DumpStale
	}
	return DumpFresh
}

func restoredCopy(name string) bool { return strings.Contains(name, restoreInfix) }

// appliedSpec is de spec van de revisie die op de nodes staat.
func (s *Service) appliedSpec(ctx context.Context, comp store.ClusterComponent) (Spec, error) {
	if comp.AppliedRevision == nil {
		return Spec{}, &ConflictError{"deze database is nog nooit helemaal uitgerold; pas haar eerst toe"}
	}
	rev, err := s.q.GetClusterComponentRevision(ctx, store.GetClusterComponentRevisionParams{ComponentID: comp.ID, Revision: *comp.AppliedRevision})
	if err != nil {
		return Spec{}, fmt.Errorf("revisie %d van het onderdeel: %w", *comp.AppliedRevision, err)
	}
	return parseSpec(rev.Spec)
}

// Dumps geeft de back-ups van een onderdeel, uit wat ClusterForge weet;
// zonder de agents te vragen.
func (s *Service) Dumps(ctx context.Context, clusterID, id uuid.UUID) (DumpOverview, error) {
	r, err := s.get(ctx, s.q, clusterID, id, false)
	if err != nil {
		return DumpOverview{}, err
	}
	if err := dbEngine(r, "Dumps"); err != nil {
		return DumpOverview{}, err
	}
	all, err := s.DatabaseBackups(ctx)
	if err != nil {
		return DumpOverview{}, err
	}
	out := DumpOverview{Settings: settingsOf(r), Databases: []DatabaseDumps{}}
	for _, b := range all {
		if b.ComponentID == id {
			out.Databases = append(out.Databases, b.DatabaseDumps)
		}
	}
	if out.Dumps, err = s.q.ListDBDumps(ctx, id); err != nil {
		return out, err
	}
	a, err := s.q.ActiveDumpJob(ctx, id)
	switch {
	case err == nil:
		out.Active = &ActiveJob{ID: a.ID, Kind: a.Kind, Title: a.Title}
	case !errors.Is(err, pgx.ErrNoRows):
		return out, err
	}
	return out, nil
}

// DatabaseBackups geeft van elke database van elk toegepast onderdeel de
// nieuwste dump. De databases zijn die de agents het laatst meldden, plus
// die met een dump.
func (s *Service) DatabaseBackups(ctx context.Context) ([]DatabaseBackup, error) {
	comps, err := s.q.ListBackupComponents(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.q.ListComponentDatabaseNames(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.q.ListLatestDBDumps(ctx)
	if err != nil {
		return nil, err
	}
	attempts, err := s.q.ListLatestDBDumpAttempts(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		comp uuid.UUID
		db   string
	}
	good, last := map[key]*store.DbDump{}, map[key]*store.DbDump{}
	dbs := map[uuid.UUID][]string{}
	for _, n := range names {
		dbs[n.ComponentID] = append(dbs[n.ComponentID], n.Name)
	}
	// Een database die weg is, telt niet meer; zonder melding van de
	// agents tellen de databases met een dump.
	reported := map[uuid.UUID]bool{}
	for id := range dbs {
		reported[id] = true
	}
	for i, d := range latest {
		good[key{d.ComponentID, d.Database}] = &latest[i]
		if !reported[d.ComponentID] {
			dbs[d.ComponentID] = append(dbs[d.ComponentID], d.Database)
		}
	}
	for i, d := range attempts {
		last[key{d.ComponentID, d.Database}] = &attempts[i]
	}
	now := s.Now()
	var out []DatabaseBackup
	for _, c := range comps {
		list := dbs[c.ID]
		slices.Sort(list)
		for _, db := range slices.Compact(list) {
			if restoredCopy(db) {
				continue
			}
			k := key{c.ID, db}
			b := DatabaseBackup{
				ClusterID: c.ClusterID, ClusterName: c.ClusterName, ComponentID: c.ID, Engine: c.Engine,
				Settings:      settingsOf(store.ClusterComponent{BackupOn: c.BackupOn, BackupAt: c.BackupAt, BackupKeepDays: c.BackupKeepDays, BackupNextAt: c.BackupNextAt}),
				DatabaseDumps: DatabaseDumps{Database: db, Latest: good[k], Freshness: freshness(c.BackupOn, good[k], now)},
			}
			if l := last[k]; l != nil && l.Status != store.DbDumpStatusOk && (b.Latest == nil || l.StartedAt.After(b.Latest.StartedAt)) {
				b.Failed = l
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// SetBackup stelt de nachtelijke dump van een onderdeel in.
func (s *Service) SetBackup(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, on bool, at string, keepDays int) (BackupSettings, error) {
	if !backupAtRe.MatchString(at) {
		return BackupSettings{}, &InvalidError{"at", "een tijd is uu:mm, zoals 02:00"}
	}
	if keepDays < 1 || keepDays > 90 {
		return BackupSettings{}, &InvalidError{"keep_days", "bewaar dumps 1 tot 90 dagen"}
	}
	var out BackupSettings
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := s.get(ctx, q, clusterID, id, true)
		if err != nil {
			return err
		}
		if err := dbEngine(r, "Dumps"); err != nil {
			return err
		}
		c, err := q.GetCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		next := nextBackup(at, s.Now())
		if err := q.SetComponentBackup(ctx, store.SetComponentBackupParams{
			ID: id, BackupOn: on, BackupAt: at, BackupKeepDays: int32(keepDays), NextAt: &next, //nolint:gosec // hoogstens 90
		}); err != nil {
			return err
		}
		r.BackupOn, r.BackupAt, r.BackupKeepDays, r.BackupNextAt = on, at, int32(keepDays), &next //nolint:gosec // hoogstens 90
		out = settingsOf(r)
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "backup.db_settings",
			Payload: map[string]any{"name": c.Name, "on": on, "at": at, "keep_days": keepDays},
		})
	})
	return out, err
}

// RunBackups begint de nachtelijke dumps die aan de beurt zijn. Stond de
// server stil op het moment zelf, dan komt de dump zodra hij weer draait.
func (s *Service) RunBackups(ctx context.Context, now time.Time) error {
	rows, err := s.q.ListBackupComponents(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !r.BackupOn {
			continue
		}
		if r.BackupNextAt == nil || now.Before(*r.BackupNextAt) {
			if r.BackupNextAt == nil {
				next := nextBackup(r.BackupAt, now)
				if err := s.q.SetComponentBackupNext(ctx, store.SetComponentBackupNextParams{ID: r.ID, NextAt: &next}); err != nil {
					return err
				}
			}
			continue
		}
		next := nextBackup(r.BackupAt, now)
		if err := s.q.SetComponentBackupNext(ctx, store.SetComponentBackupNextParams{ID: r.ID, NextAt: &next}); err != nil {
			return err
		}
		_, err := s.enqueueDump(ctx, events.System(), r.ClusterID, r.ID, dumpParams{ComponentID: r.ID, Trigger: TriggerSchedule})
		var ce *ConflictError
		if errors.As(err, &ce) {
			s.log.Warn("nachtelijke dump overgeslagen", "cluster", r.ClusterName, "reason", ce.Msg)
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// DumpNow maakt meteen een dump van één database.
func (s *Service) DumpNow(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, database string) (store.Job, error) {
	r, err := s.get(ctx, s.q, clusterID, id, false)
	if err != nil {
		return store.Job{}, err
	}
	if err := dbsql.CheckDatabaseOf(r.Engine, database); err != nil {
		return store.Job{}, &InvalidError{"database", err.Error()}
	}
	return s.enqueueDump(ctx, actor, clusterID, id, dumpParams{ComponentID: id, Databases: []string{database}, Trigger: TriggerManual})
}

func (s *Service) enqueueDump(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, p dumpParams) (store.Job, error) {
	r, err := s.get(ctx, s.q, clusterID, id, false)
	if err != nil {
		return store.Job{}, err
	}
	if err := dbEngine(r, "Dumps"); err != nil {
		return store.Job{}, err
	}
	if r.AppliedRevision == nil {
		return store.Job{}, &ConflictError{"deze database is nog nooit helemaal uitgerold; pas haar eerst toe"}
	}
	if a, err := s.q.ActiveDumpJob(ctx, id); err == nil {
		return store.Job{}, &ConflictError{"er loopt al een taak met de dumps van deze database (" + a.Title + "); wacht tot die klaar is"}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, err
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return store.Job{}, err
	}
	what := "Nachtelijke dump van de databases"
	if len(p.Databases) == 1 {
		what = "Dump van " + p.Databases[0]
	}
	return s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: DumpKind, Title: what + " op " + c.Name, Params: p, ClusterID: &clusterID, Actor: actor,
	})
}

// RestoreDump zet een dump terug in een nieuwe database. Een lege target is
// <database>_herstel_<datum van de dump>.
func (s *Service) RestoreDump(ctx context.Context, actor events.Actor, clusterID, id, dumpID uuid.UUID, target string) (store.Job, string, error) {
	r, err := s.get(ctx, s.q, clusterID, id, false)
	if err != nil {
		return store.Job{}, "", err
	}
	if err := dbEngine(r, "Dumps"); err != nil {
		return store.Job{}, "", err
	}
	d, err := s.q.GetDBDump(ctx, dumpID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && d.ComponentID != r.ID) {
		return store.Job{}, "", ErrDumpNotFound
	}
	if err != nil {
		return store.Job{}, "", err
	}
	switch {
	case d.Status == store.DbDumpStatusRunning:
		return store.Job{}, "", &ConflictError{"deze dump is nog bezig"}
	case d.Status == store.DbDumpStatusFailed:
		return store.Job{}, "", &ConflictError{"deze dump is mislukt; kies een andere"}
	case d.NodeID == nil:
		return store.Job{}, "", &ConflictError{"de node " + d.Hostname + " met deze dump bestaat niet meer"}
	}
	if target == "" {
		target = RestoreName(d.Database, d.StartedAt)
	}
	if err := dbsql.CheckDatabaseOf(r.Engine, target); err != nil {
		return store.Job{}, "", &InvalidError{"target", err.Error()}
	}
	if target == d.Database {
		return store.Job{}, "", &InvalidError{"target", "terugzetten gaat in een nieuwe database, niet in " + d.Database + " zelf"}
	}
	if a, err := s.q.ActiveDumpJob(ctx, id); err == nil {
		return store.Job{}, "", &ConflictError{"er loopt al een taak met de dumps van deze database (" + a.Title + "); wacht tot die klaar is"}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, "", err
	}
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return store.Job{}, "", err
	}
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: RestoreKind, Title: fmt.Sprintf("Dump van %s terugzetten als %s op %s", d.Database, target, c.Name),
		Params: restoreParams{ComponentID: id, DumpID: dumpID, Target: target}, ClusterID: &clusterID, Actor: actor,
	})
	return j, target, err
}

// ErrDumpNotFound: de dump bestaat niet, of hoort bij een ander onderdeel.
var ErrDumpNotFound = errors.New("die dump bestaat niet")

// RestoreName is de standaardnaam van een teruggezette dump: de database,
// _herstel_ en de datum van de dump, ingekort tot 63 tekens.
func RestoreName(database string, at time.Time) string {
	suffix := restoreInfix + at.Local().Format("20060102")
	return database[:min(len(database), 63-len(suffix))] + suffix
}

// dumpReq stuurt een DBDump voor engine naar een node.
func (s *Service) dumpReq(ctx context.Context, n Node, engine string, req protocol.DBDumpRequest) (*protocol.DBDumpResult, error) {
	res, err := s.dbRequest(ctx, dbTarget{node: n, engine: engine}, protocol.DBRequest{Op: protocol.DBDump, Dump: &req})
	if err != nil {
		return nil, err
	}
	if res.Dump == nil {
		return nil, &ConflictError{n.Hostname + ": de agent gaf geen antwoord over de dumps"}
	}
	return res.Dump, nil
}

// vipOwner is de hostname van de node met het VIP van de database; leeg
// zonder VIP of als het nergens staat.
func (s *Service) vipOwner(ctx context.Context, clusterID uuid.UUID, spec Spec) (string, error) {
	if spec.VIP == nil {
		return "", nil
	}
	vips, err := s.q.ListVIPsByCluster(ctx, clusterID)
	if err != nil {
		return "", err
	}
	for _, v := range vips {
		if v.Vip.ID == spec.VIP.ID {
			return deref(v.OwnerHostname), nil
		}
	}
	return "", nil
}

// dumpNode kiest de node voor de dumps: bij PostgreSQL de primary, bij
// MariaDB een Synced datanode zonder het VIP, liefst die van de vorige
// dump. Is alleen de node met het VIP Synced, dan die, met de reden.
func (s *Service) dumpNode(ctx context.Context, comp store.ClusterComponent, spec Spec) (Node, string, error) {
	if comp.Engine == protocol.DBPostgreSQL {
		p, err := s.pgPrimary(ctx, comp.ClusterID, spec)
		if err != nil {
			return Node{}, "", err
		}
		return p.node, p.node.Hostname + " is de primary; daar leest pg_dump zonder dat een replica hem afbreekt", nil
	}
	owner, err := s.vipOwner(ctx, comp.ClusterID, spec)
	if err != nil {
		return Node{}, "", err
	}
	prev, err := s.q.LatestDumpHost(ctx, comp.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Node{}, "", err
	}
	slug := spec.Cluster.Slug
	var synced []Node
	var why []string
	for _, o := range s.observe(ctx, dataNodes(spec, nil), false) {
		if o.synced(slug) {
			synced = append(synced, o.node)
		} else {
			why = append(why, o.node.Hostname+": "+o.describe(slug))
		}
	}
	if len(synced) == 0 {
		return Node{}, "", &ConflictError{"geen enkele datanode is Synced, dus de database draait niet; " + strings.Join(why, "; ")}
	}
	// Eerst zonder VIP, dan die van de vorige dump, dan op volgnummer.
	slices.SortStableFunc(synced, func(a, b Node) int {
		return cmp.Or(
			cmpBool(a.Hostname == owner, b.Hostname == owner),
			cmpBool(a.Hostname != prev, b.Hostname != prev),
		)
	})
	n := synced[0]
	if n.Hostname == owner && owner != "" {
		return n, "alleen " + owner + " is Synced, dus de dump komt op de node met het VIP", nil
	}
	return n, "", nil
}

// cmpBool zet false voor true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

const stepPick = "Node kiezen"

func actorOf(j *jobs.Job) events.Actor {
	if j.RequestedBy != nil {
		return events.User(*j.RequestedBy)
	}
	return events.System()
}

func (s *Service) runDump(ctx context.Context, j *jobs.Job) error {
	var p dumpParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	comp, err := s.q.GetClusterComponent(ctx, p.ComponentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("het onderdeel bestaat niet meer")
	}
	if err != nil {
		return err
	}
	spec, err := s.appliedSpec(ctx, comp)
	if err != nil {
		return err
	}
	var pick dumpPick
	if err := j.Step(ctx, stepPick, func(ctx context.Context, st *jobs.Step) error {
		if st.State(&pick) {
			return nil
		}
		n, why, err := s.dumpNode(ctx, comp, spec)
		if err != nil {
			return err
		}
		pick.Node = n
		if why != "" {
			st.Logf("%s", why)
		}
		st.Logf("De dumps komen op %s, in %s.", n.Hostname, protocol.DBDumpDir)
		pick.Databases = p.Databases
		if len(pick.Databases) == 0 {
			res, err := s.dbRequest(ctx, dbTarget{node: n}, protocol.DBRequest{Op: protocol.DBStatus})
			if err != nil {
				return err
			}
			for _, sv := range res.Servers {
				if sv.Engine != comp.Engine {
					continue
				}
				for _, d := range sv.Databases {
					// De schrijftest van de failovertests hoeft geen dump.
					if !restoredCopy(d.Name) && d.Name != protocol.DBProbeDatabase && dbsql.CheckDatabaseOf(comp.Engine, d.Name) == nil {
						pick.Databases = append(pick.Databases, d.Name)
					}
				}
			}
			if len(pick.Databases) == 0 {
				st.Logf("Er staat nog geen database in.")
			}
		}
		return st.SetState(ctx, pick)
	}); err != nil {
		return err
	}
	j.Done(stepPick, &pick)
	var failed []error
	for _, db := range pick.Databases {
		err := j.Step(ctx, db+": dump op "+pick.Node.Hostname, func(ctx context.Context, st *jobs.Step) error {
			return s.dumpOne(ctx, j, st, comp, pick.Node, db, p.Trigger)
		})
		if err != nil {
			if jobs.Interrupted(ctx) || jobs.Canceled(ctx) {
				return err
			}
			failed = append(failed, fmt.Errorf("%s: %w", db, err))
		}
	}
	if err := j.Step(ctx, "Oude dumps opruimen", func(ctx context.Context, st *jobs.Step) error {
		return s.pruneDumps(ctx, st, comp, spec)
	}); err != nil {
		return err
	}
	switch {
	case len(failed) == 1 && len(pick.Databases) == 1:
		return errors.Unwrap(failed[0])
	case len(failed) > 0:
		msgs := make([]string, len(failed))
		for i, err := range failed {
			msgs[i] = err.Error()
		}
		return fmt.Errorf("%d van de %d dumps mislukt: %s", len(failed), len(pick.Databases), strings.Join(msgs, "; "))
	}
	return nil
}

// dumpState is wat een stap van een dump onthoudt, zodat een hervatte taak
// verder wacht op dezelfde dump.
type dumpStepState struct {
	ID   uuid.UUID `json:"id"`
	File string    `json:"file"`
}

func (s *Service) dumpOne(ctx context.Context, j *jobs.Job, st *jobs.Step, comp store.ClusterComponent, n Node, db, trigger string) error {
	var state dumpStepState
	if !st.State(&state) {
		res, err := s.dumpReq(ctx, n, comp.Engine, protocol.DBDumpRequest{Action: protocol.DBDumpStart, Database: db})
		if err != nil {
			return err
		}
		row, err := s.q.InsertDBDump(ctx, store.InsertDBDumpParams{
			ComponentID: comp.ID, ClusterID: comp.ClusterID, NodeID: &n.NodeID, Hostname: n.Hostname, Database: db,
			File: res.Started, Trigger: trigger, StartedAt: s.Now(), JobID: &j.ID, RequestedBy: j.RequestedBy,
		})
		if err != nil {
			return err
		}
		state = dumpStepState{ID: row.ID, File: res.Started}
		st.Logf("%s begonnen.", res.Started)
		if err := st.SetState(ctx, state); err != nil {
			return err
		}
	}
	deadline := s.Now().Add(protocol.DBDumpTimeout + time.Hour)
	reported := int64(0)
	for {
		res, err := s.dumpReq(ctx, n, comp.Engine, protocol.DBDumpRequest{Action: protocol.DBDumpList})
		switch {
		case err != nil && ctx.Err() != nil:
			return err
		case err != nil:
			if s.Now().After(deadline) {
				return s.finishDump(ctx, comp, state.ID, err.Error())
			}
		case res.Running != nil && res.Running.File == state.File:
			if b := res.Running.Bytes; b-reported >= 64<<20 {
				reported = b
				st.Logf("%s geschreven.", humanBytes(b))
				_ = st.Flush(ctx)
			}
		default:
			i := slices.IndexFunc(res.Dumps, func(f protocol.DBDumpFile) bool { return f.File == state.File })
			if i < 0 {
				return s.finishDump(ctx, comp, state.ID, "de dump staat niet op "+n.Hostname+"; stopte de agent tijdens de dump?")
			}
			f := res.Dumps[i]
			d, err := s.finishDumpFile(ctx, comp, state.ID, f)
			if err != nil {
				return err
			}
			switch d.Status {
			case store.DbDumpStatusOk:
				st.Logf("Klaar: %s, %d tabellen, %s gecomprimeerd.", humanBytes(d.RawBytes), d.Tables, humanBytes(d.SizeBytes))
			case store.DbDumpStatusSuspect:
				st.Logf("Klaar, maar verdacht: %s", d.Message)
			default:
				return errors.New(d.Message)
			}
			if comp.Engine == protocol.DBMariaDB {
				s.dumpTables(ctx, st, n, d)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(s.DumpPoll):
		}
	}
}

// dumpTables onthoudt bij een dump van MariaDB welke tabellen geen
// primaire sleutel hebben of niet in InnoDB staan, voor de scorekaart
// Database: Galera repliceert die traag of niet. Lukt dat niet, dan blijft
// het onbekend; de dump is er wel.
func (s *Service) dumpTables(ctx context.Context, st *jobs.Step, n Node, d store.DbDump) {
	res, err := s.migrateReq(ctx, n, protocol.DBMariaDB, protocol.DBMigrateRequest{Action: protocol.DBMigrateInspect, Database: d.Database})
	if err == nil && (res == nil || res.Info == nil || !res.Info.Exists) {
		err = errors.New("de agent zei niets over de tabellen")
	}
	if err != nil {
		st.Logf("Welke tabellen een primaire sleutel hebben, lukte niet te lezen: %v.", err)
		return
	}
	noPK, other := []string{}, []string{}
	for _, t := range res.Info.Tables {
		if !t.PrimaryKey {
			noPK = append(noPK, t.Name)
		}
		if t.Engine != "" && !strings.EqualFold(t.Engine, "InnoDB") {
			other = append(other, t.Name)
		}
	}
	if err := s.q.SetDBDumpTables(ctx, store.SetDBDumpTablesParams{ID: d.ID, NoPrimaryKey: noPK, NotInnodb: other}); err != nil {
		st.Logf("De tabellen vastleggen lukte niet: %v.", err)
		return
	}
	if len(noPK) > 0 {
		st.Logf("%s zonder primaire sleutel: %s.", plural(len(noPK), "Eén tabel", "tabellen"), listSome(noPK, 8))
	}
	if len(other) > 0 {
		st.Logf("%s niet in InnoDB: %s.", plural(len(other), "Eén tabel staat", "tabellen staan"), listSome(other, 8))
	}
}

// finishDumpFile beoordeelt een klaar bestand en legt het vast.
func (s *Service) finishDumpFile(ctx context.Context, comp store.ClusterComponent, id uuid.UUID, f protocol.DBDumpFile) (store.DbDump, error) {
	status, msg := store.DbDumpStatusOk, ""
	switch {
	case f.Error != "":
		status, msg = store.DbDumpStatusFailed, f.Error
	case !f.Complete:
		tool := "mariadb-dump"
		if comp.Engine == protocol.DBPostgreSQL {
			tool = "pg_dump"
		}
		status, msg = store.DbDumpStatusFailed, "de dump is niet compleet: de laatste regel van "+tool+" ontbreekt"
	case f.Tables < f.TablesExpected:
		status, msg = store.DbDumpStatusSuspect, fmt.Sprintf("er staan %d van de %d tabellen in", f.Tables, f.TablesExpected)
	default:
		prev, err := s.q.PreviousDBDump(ctx, store.PreviousDBDumpParams{ComponentID: comp.ID, Database: f.Database, Exclude: id})
		switch {
		case err == nil && f.RawBytes*2 < prev.RawBytes:
			status = store.DbDumpStatusSuspect
			msg = fmt.Sprintf("meer dan de helft kleiner dan de vorige (%s, nu %s); kromp de database echt, dan is de volgende dump weer goed",
				humanBytes(prev.RawBytes), humanBytes(f.RawBytes))
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return store.DbDump{}, err
		}
	}
	finished := f.Finished
	if finished.IsZero() {
		finished = s.Now()
	}
	d, err := s.q.FinishDBDump(ctx, store.FinishDBDumpParams{
		ID: id, Status: status, FinishedAt: &finished, SizeBytes: f.SizeBytes, RawBytes: f.RawBytes,
		Tables: int32(f.Tables), TablesExpected: int32(f.TablesExpected), Message: msg, //nolint:gosec // aantallen tabellen
	})
	if err != nil {
		return d, err
	}
	s.dumpEvent(ctx, comp, d)
	return d, nil
}

// finishDump legt een dump vast die mislukte voor er een bestand was.
func (s *Service) finishDump(ctx context.Context, comp store.ClusterComponent, id uuid.UUID, msg string) error {
	now := s.Now()
	d, err := s.q.FinishDBDump(ctx, store.FinishDBDumpParams{ID: id, Status: store.DbDumpStatusFailed, FinishedAt: &now, Message: msg})
	if err != nil {
		return err
	}
	s.dumpEvent(ctx, comp, d)
	return errors.New(msg)
}

func (s *Service) dumpEvent(ctx context.Context, comp store.ClusterComponent, d store.DbDump) {
	action := map[store.DbDumpStatus]string{
		store.DbDumpStatusOk: "backup.db_dumped", store.DbDumpStatusSuspect: "backup.db_dump_suspect", store.DbDumpStatusFailed: "backup.db_dump_failed",
	}[d.Status]
	c, err := s.q.GetCluster(ctx, comp.ClusterID)
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", action, "err", err)
		return
	}
	actor := events.System()
	if d.RequestedBy != nil {
		actor = events.User(*d.RequestedBy)
	}
	err = s.ev.Write(ctx, s.q, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: action,
		Payload: map[string]any{
			"name": c.Name, "database": d.Database, "node": d.Hostname, "file": d.File, "trigger": d.Trigger,
			"size_bytes": d.SizeBytes, "raw_bytes": d.RawBytes, "tables": d.Tables, "message": d.Message, "dump_id": d.ID,
		},
	})
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", action, "err", err)
	}
}

// pruneDumps haalt op elke datanode de dumps weg die te oud zijn, behalve
// de nieuwste goede per database, waar die ook staat, en vergeet de dumps
// die er niet meer staan. Een node die niet antwoordt, komt de volgende
// nacht aan de beurt.
func (s *Service) pruneDumps(ctx context.Context, st *jobs.Step, comp store.ClusterComponent, spec Spec) error {
	rows, err := s.q.ListDBDumps(ctx, comp.ID)
	if err != nil {
		return err
	}
	// De rijen staan op volgorde, de nieuwste eerst.
	newest := map[string]store.DbDump{}
	for _, d := range rows {
		if _, ok := newest[d.Database]; !ok && d.Status == store.DbDumpStatusOk {
			newest[d.Database] = d
		}
	}
	// Een agent die Engine niet kent, zou bij PostgreSQL de dumps van
	// MariaDB opruimen; die komt pas na een update aan de beurt.
	agents, err := s.agentProtocols(ctx, comp.ClusterID)
	if err != nil {
		return err
	}
	for _, n := range dataNodes(spec, nil) {
		if comp.Engine == protocol.DBPostgreSQL && agents[n.NodeID] < protocol.DBPGSince {
			st.Logf("%s: niet opgeruimd: de agent heeft protocol %d; werk hem bij met het installatiescript en --upgrade", n.Hostname, agents[n.NodeID])
			continue
		}
		keep := []string{}
		for _, d := range newest {
			if d.Hostname == n.Hostname {
				keep = append(keep, d.File)
			}
		}
		slices.Sort(keep)
		res, err := s.dumpReq(ctx, n, comp.Engine, protocol.DBDumpRequest{Action: protocol.DBDumpPrune, KeepDays: int(comp.BackupKeepDays), Keep: keep})
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			st.Logf("%s: niet opgeruimd: %v", n.Hostname, err)
			continue
		}
		present := map[string]bool{}
		for _, f := range res.Dumps {
			present[f.File] = true
		}
		if res.Running != nil {
			present[res.Running.File] = true
		}
		gone := 0
		for _, d := range rows {
			if d.Hostname == n.Hostname && d.Status != store.DbDumpStatusRunning && !present[d.File] {
				if err := s.q.DeleteDBDump(ctx, d.ID); err != nil {
					return err
				}
				gone++
			}
		}
		switch {
		case len(res.Removed) > 0:
			st.Logf("%s: %d dumps ouder dan %d dagen weggehaald.", n.Hostname, len(res.Removed), comp.BackupKeepDays)
		default:
			st.Logf("%s: niets om op te ruimen.", n.Hostname)
		}
		if extra := gone - len(res.Removed); extra > 0 {
			st.Logf("%s: %d dumps stonden er niet meer.", n.Hostname, extra)
		}
	}
	return nil
}

// agentProtocols is per node van een cluster het protocol van zijn agent;
// 0 zonder agent.
func (s *Service) agentProtocols(ctx context.Context, clusterID uuid.UUID) (map[uuid.UUID]int, error) {
	cands, err := s.q.ListComponentCandidates(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]int{}
	for _, c := range cands {
		if c.HasAgent {
			out[c.ID] = int(c.AgentProtocol)
		}
	}
	return out, nil
}

// failDumpsOf zet de dumps van een taak die stopte op mislukt. De agent
// kan de dump nog afmaken; de volgende keer opruimen ziet hem dan.
func (s *Service) failDumpsOf(ctx context.Context, j store.Job) {
	if err := s.q.FailRunningDBDumps(ctx, store.FailRunningDBDumpsParams{JobID: &j.ID, Message: "de taak stopte: " + j.Error}); err != nil {
		s.log.Error("dumps van een gestopte taak afronden mislukt", "job", j.ID, "err", err)
	}
}

func (s *Service) runRestore(ctx context.Context, j *jobs.Job) error {
	var p restoreParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	comp, err := s.q.GetClusterComponent(ctx, p.ComponentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("het onderdeel bestaat niet meer")
	}
	if err != nil {
		return err
	}
	d, err := s.q.GetDBDump(ctx, p.DumpID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("de dump bestaat niet meer")
	}
	if err != nil {
		return err
	}
	if d.NodeID == nil {
		return errors.New("de node " + d.Hostname + " met deze dump bestaat niet meer")
	}
	n := Node{NodeID: *d.NodeID, Hostname: d.Hostname}
	pg := comp.Engine == protocol.DBPostgreSQL
	return j.Step(ctx, fmt.Sprintf("%s terugzetten als %s op %s", d.File, p.Target, d.Hostname), func(ctx context.Context, st *jobs.Step) error {
		var state restoreState
		if !st.State(&state) {
			req := protocol.DBDumpRequest{Action: protocol.DBDumpRestore, Database: d.Database, File: d.File, Target: p.Target}
			if pg {
				role, primary, err := s.restoreLogin(ctx, st, comp, n, &req)
				if err != nil {
					if ctx.Err() != nil {
						return err
					}
					return s.restoreEvent(ctx, j, comp, d, p.Target, err.Error())
				}
				state.Role, state.Primary = role, primary
			}
			if _, err := s.dumpReq(ctx, n, comp.Engine, req); err != nil {
				if ctx.Err() != nil {
					return err
				}
				s.dropRestoreLogin(ctx, st, comp, state.Role)
				return s.restoreEvent(ctx, j, comp, d, p.Target, err.Error())
			}
			state.At = s.Now()
			if pg {
				st.Logf("Database %s gemaakt op %s; de dump loopt erin vanaf %s.", p.Target, state.Primary, d.Hostname)
			} else {
				st.Logf("Database %s gemaakt; de dump loopt erin.", p.Target)
			}
			if err := st.SetState(ctx, state); err != nil {
				return err
			}
		}
		done := func(msg string) error {
			s.dropRestoreLogin(ctx, st, comp, state.Role)
			return s.restoreEvent(ctx, j, comp, d, p.Target, msg)
		}
		reported := int64(0)
		for {
			res, err := s.dumpReq(ctx, n, comp.Engine, protocol.DBDumpRequest{Action: protocol.DBDumpList})
			switch {
			case err != nil && ctx.Err() != nil:
				return err
			case err != nil:
				st.Logf("%v", err)
			case res.Running != nil && res.Running.Target == p.Target:
				if b := res.Running.Bytes; b-reported >= 16<<20 && res.Running.Total > 0 {
					reported = b
					st.Logf("%d%% gelezen.", b*100/res.Running.Total)
					_ = st.Flush(ctx)
				}
			default:
				i := -1
				for k := len(res.Restores) - 1; k >= 0 && i < 0; k-- {
					if res.Restores[k].Target == p.Target {
						i = k
					}
				}
				if i < 0 {
					return done("de agent op " + d.Hostname + " weet niets van dit terugzetten; startte hij opnieuw? " +
						"Kijk bij Databases en gebruikers of " + p.Target + " er staat, haal de database weg en probeer het opnieuw")
				}
				if r := res.Restores[i]; r.Error != "" {
					return done(r.Error)
				}
				if pg {
					st.Logf("Klaar. %s staat naast %s; kijk erin als root op %s met: runuser -u postgres -- psql %s", p.Target, d.Database, state.Primary, p.Target)
				} else {
					st.Logf("Klaar. %s staat naast %s; kijk erin als root op %s met: mariadb %s", p.Target, d.Database, d.Hostname, p.Target)
				}
				return done("")
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(s.DumpPoll):
			}
		}
	})
}

// restoreLogin maakt op de primary van PostgreSQL een tijdelijke beheerder
// en zet hem in req, zodat de agent op n de dump daar terugzet. Het
// wachtwoord gaat alleen naar de twee agents; ClusterForge bewaart het
// niet.
func (s *Service) restoreLogin(ctx context.Context, st *jobs.Step, comp store.ClusterComponent, n Node, req *protocol.DBDumpRequest) (string, string, error) {
	spec, err := s.appliedSpec(ctx, comp)
	if err != nil {
		return "", "", err
	}
	agents, err := s.agentProtocols(ctx, comp.ClusterID)
	if err != nil {
		return "", "", err
	}
	if err := pgAgent(pgNode{node: n, protocol: agents[n.NodeID]}); err != nil {
		return "", "", err
	}
	p, err := s.pgPrimary(ctx, comp.ClusterID, spec)
	if err != nil {
		return "", "", err
	}
	if p.address == "" {
		return "", "", &ConflictError{"de primary " + p.node.Hostname + " heeft geen adres"}
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	role, pw := "cf_herstel_"+hex.EncodeToString(b), rand.Text()
	if _, err := s.dbRequest(ctx, dbTarget{node: p.node, engine: protocol.DBPostgreSQL}, protocol.DBRequest{
		Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{User: role, Password: pw, Restore: true},
	}); err != nil {
		return "", "", err
	}
	st.Logf("Tijdelijke beheerder %s gemaakt op de primary %s; hij vervalt na %d uur.", role, p.node.Hostname, int(protocol.DBTempRoleTTL.Hours()))
	req.Primary, req.User, req.Password = p.address, role, pw
	return role, p.node.Hostname, nil
}

// dropRestoreLogin haalt de tijdelijke beheerder weg, op de primary van nu.
// Lukt dat niet, dan vervalt hij vanzelf en ruimt het volgende terugzetten
// hem op.
func (s *Service) dropRestoreLogin(ctx context.Context, st *jobs.Step, comp store.ClusterComponent, role string) {
	if role == "" {
		return
	}
	err := func() error {
		spec, err := s.appliedSpec(ctx, comp)
		if err != nil {
			return err
		}
		p, err := s.pgPrimary(ctx, comp.ClusterID, spec)
		if err != nil {
			return err
		}
		_, err = s.dbRequest(ctx, dbTarget{node: p.node, engine: protocol.DBPostgreSQL}, protocol.DBRequest{
			Op: protocol.DBDrop, Drop: &protocol.DBDropRequest{User: role},
		})
		return err
	}()
	if err != nil {
		st.Logf("De tijdelijke beheerder %s weghalen mislukte: %v. Hij vervalt na %d uur; het volgende terugzetten ruimt hem op.",
			role, err, int(protocol.DBTempRoleTTL.Hours()))
		return
	}
	st.Logf("Tijdelijke beheerder %s weggehaald.", role)
}

// restoreEvent schrijft hoe het terugzetten afliep; met msg mislukte het.
func (s *Service) restoreEvent(ctx context.Context, j *jobs.Job, comp store.ClusterComponent, d store.DbDump, target, msg string) error {
	action := "backup.db_restored"
	if msg != "" {
		action = "backup.db_restore_failed"
	}
	c, err := s.q.GetCluster(ctx, comp.ClusterID)
	if err == nil {
		err = s.ev.Write(ctx, s.q, events.Event{
			Actor: actorOf(j), SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID, Action: action,
			Payload: map[string]any{
				"name": c.Name, "database": d.Database, "target": target, "node": d.Hostname, "file": d.File, "dump_id": d.ID, "message": msg,
			},
		})
	}
	if err != nil {
		s.log.Error("event schrijven mislukt", "action", action, "err", err)
	}
	if s.DBChanged != nil {
		s.DBChanged()
	}
	if msg != "" {
		return errors.New(msg)
	}
	return nil
}

// humanBytes schrijft een grootte zoals 2,3 GB.
func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, u := float64(n), 0
	for f >= 1024 && u < len(units)-1 {
		f /= 1024
		u++
	}
	if u == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", f, units[u]), ".", ",", 1)
}
