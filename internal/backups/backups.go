// Package backups leest de back-ups die Proxmox kent (vzdump-bestanden en
// Proxmox Backup Server) en bewaakt per VM hoe oud de nieuwste is.
// ClusterForge maakt zelf geen back-ups: dat blijven de back-upjobs van
// Proxmox.
package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Freshness zegt of een VM een recente back-up heeft.
type Freshness string

const (
	OK      Freshness = "ok"
	Stale   Freshness = "stale"
	Missing Freshness = "missing"
	// Unknown: de back-ups van de koppeling zijn (nog) niet te lezen.
	Unknown Freshness = "unknown"
)

// Judge zegt hoe vers de nieuwste back-up is.
func Judge(latest *time.Time, maxAge time.Duration, now time.Time) Freshness {
	switch {
	case latest == nil:
		return Missing
	case now.Sub(*latest) > maxAge:
		return Stale
	default:
		return OK
	}
}

// severity ordent standen van goed naar slecht, voor de stand van een cluster.
var severity = map[Freshness]int{OK: 0, Unknown: 1, Stale: 2, Missing: 3}

// Worse geeft de slechtste van twee standen.
func Worse(a, b Freshness) Freshness {
	if severity[b] > severity[a] {
		return b
	}
	return a
}

// ErrNotFound betekent dat de koppeling of node niet bestaat.
var ErrNotFound = errors.New("niet gevonden")

// ValidationError is een fout in de invoer.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	pve  *proxmox.Service

	// InventoryEvery is hoe vaak de back-ups van een koppeling opnieuw
	// gelezen worden.
	InventoryEvery time.Duration
	// Tick is hoe vaak de versheid opnieuw berekend wordt.
	Tick time.Duration
	// Now is de klok; tests vervangen hem.
	Now func() time.Time

	// De back-upcontrole; zie EnableVerify.

	// SandboxStorage is de storage voor elke sandbox; leeg kiest de storage
	// met content images en de meeste vrije ruimte.
	SandboxStorage string
	// BootTimeout is hoe lang de guest agent na het starten mag zwijgen.
	BootTimeout time.Duration
	// NoAgentWait is hoe lang een VM zonder guest agent na het starten moet
	// blijven draaien.
	NoAgentWait time.Duration
	// AgentTimeout is de limiet van één vraag aan de guest agent.
	AgentTimeout time.Duration
	// Poll is hoe vaak de controle de guest agent of een lock opvraagt.
	Poll time.Duration
	// ConnPoll is hoe vaak de controle de verbindingen van de bronnode telt.
	ConnPoll time.Duration
	// LockWait is hoe lang Opruimen wacht als Proxmox de VM vergrendeld heeft.
	LockWait time.Duration
	// CleanEvery is het ritme van de opruimer.
	CleanEvery time.Duration
	// MaxSandboxAge: een oudere sandbox ruimt de opruimer altijd op.
	MaxSandboxAge time.Duration
	// JobLimit is de limiet van de hele taak.
	JobLimit time.Duration
	// Window is het testvenster waarin geplande controles draaien.
	Window planner.Window
	// Running geeft wat er volgens de gewenste staat op een node draait;
	// de diepe controle vraagt die services, poorten en adressen aan
	// cf-agent verify. Nil: alleen wat de facts van de node zeggen.
	Running func(ctx context.Context, clusterID, nodeID uuid.UUID) (deploy.Running, error)
	// VerifyWait is hoe lang de controle op cf-agent verify wacht; nul is
	// de limiet van de agent plus 30 s.
	VerifyWait time.Duration

	runner    *jobs.Runner
	conns     Connections
	sbMu      sync.Mutex
	sbLocks   map[uuid.UUID]*sync.Mutex
	cleanKick chan struct{}

	kick  chan struct{}
	invMu sync.Mutex
	evMu  sync.Mutex
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, pve *proxmox.Service) *Service {
	return &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, pve: pve,
		InventoryEvery: 15 * time.Minute, Tick: time.Minute, Now: time.Now,
		BootTimeout: 10 * time.Minute, NoAgentWait: 30 * time.Second, AgentTimeout: time.Minute,
		Poll: 3 * time.Second, ConnPoll: 2 * time.Second, LockWait: 10 * time.Minute,
		CleanEvery: 5 * time.Minute, MaxSandboxAge: 3 * time.Hour, JobLimit: 2 * time.Hour, Window: planner.DefaultWindow,
		sbLocks: map[uuid.UUID]*sync.Mutex{}, cleanKick: make(chan struct{}, 1),
		kick: make(chan struct{}, 1),
	}
}

// Kick laat de versheid meteen opnieuw berekenen.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run leest de back-ups op tijd opnieuw en berekent de versheid tot ctx
// stopt. Staat de back-upcontrole aan, dan loopt ook de opruimer.
func (s *Service) Run(ctx context.Context) {
	if s.runner != nil {
		go s.cleaner(ctx)
	}
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		s.inventoryDue(ctx)
		if err := s.Evaluate(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("versheid van back-ups berekenen", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

func (s *Service) inventoryDue(ctx context.Context) {
	conns, err := s.q.ListProxmoxConnections(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("Proxmox-verbindingen ophalen", "err", err)
		}
		return
	}
	now := s.Now()
	for _, c := range conns {
		if c.BackupCheckedAt != nil && now.Sub(*c.BackupCheckedAt) < s.InventoryEvery {
			continue
		}
		if err := s.Inventory(ctx, c.ID); err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			s.log.Warn("back-ups lezen mislukt", "proxmox", c.Name, "err", err)
		}
	}
}

// read is één storage die we lezen via één host.
type read struct {
	node, storage string
}

// Inventory leest de back-ups van één koppeling en bewaart ze. Een fout komt
// in backup_error van de koppeling; de vorige lijst blijft dan staan.
func (s *Service) Inventory(ctx context.Context, id uuid.UUID) error {
	s.invMu.Lock()
	defer s.invMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	api, conn, err := s.pve.APIFor(ctx, id)
	if errors.Is(err, proxmox.ErrNotFound) {
		return ErrNotFound
	}
	if err == nil && conn.LastError != "" {
		// Proxmox is niet bereikbaar; dat meldt de gewone sync al.
		return nil
	}
	var (
		vols []store.UpsertProxmoxBackupParams
		keep []string
		nbu  []byte
	)
	if err == nil {
		vols, keep, err = s.readBackups(ctx, api, id)
	}
	if err == nil {
		nbu, err = s.readCoverage(ctx, api, conn)
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return err
		}
		s.saveError(ctx, conn, err)
		s.Kick()
		return err
	}
	now := s.Now()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		for i := range vols {
			vols[i].SyncedAt = now
		}
		var batchErr error
		q.UpsertProxmoxBackup(ctx, vols).Exec(func(_ int, err error) {
			if err != nil && batchErr == nil {
				batchErr = err
			}
		})
		if batchErr != nil {
			return batchErr
		}
		if err := q.DeleteStaleProxmoxBackups(ctx, store.DeleteStaleProxmoxBackupsParams{
			ConnectionID: id, SyncedAt: now, Keep: nonNil(keep),
		}); err != nil {
			return err
		}
		if err := q.SetBackupInventoryResult(ctx, store.SetBackupInventoryResultParams{ID: id, NotBackedUp: nbu}); err != nil {
			return err
		}
		if conn.BackupError == "" {
			return nil
		}
		s.log.Info("back-ups weer te lezen", "proxmox", conn.Name)
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "proxmox", SubjectID: id.String(), Action: "backup.inventory_recovered",
			Payload: map[string]any{"name": conn.Name, "count": len(vols)},
		})
	})
	if err != nil {
		return err
	}
	s.Kick()
	return nil
}

// readBackups leest elke storage met back-ups één keer: gedeelde storage via
// een host die online is, lokale storage via haar eigen host. keep zijn de
// storages die niet te lezen waren omdat hun host offline is; hun vorige
// back-ups blijven staan.
func (s *Service) readBackups(ctx context.Context, api proxmox.API, id uuid.UUID) ([]store.UpsertProxmoxBackupParams, []string, error) {
	storages, err := s.q.ListBackupStorages(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(storages) == 0 {
		return nil, nil, errors.New("geen enkele storage in Proxmox bevat back-ups (content backup)")
	}
	var (
		reads []read
		keep  []string
		done  = map[string]bool{}
	)
	for _, st := range storages {
		key := st.Storage
		if !st.Shared {
			key += "@" + st.PveNode
		}
		if done[key] {
			continue
		}
		if !st.HostOnline {
			continue
		}
		done[key] = true
		reads = append(reads, read{st.PveNode, st.Storage})
	}
	for _, st := range storages {
		key := st.Storage
		if !st.Shared {
			key += "@" + st.PveNode
		}
		if !done[key] && !slices.Contains(keep, key) {
			keep = append(keep, key)
		}
	}
	seen := map[string]bool{}
	var out []store.UpsertProxmoxBackupParams
	for _, r := range reads {
		vols, err := api.BackupContent(ctx, r.node, r.storage)
		if err != nil {
			return nil, nil, fmt.Errorf("back-ups op %s (%s): %w", r.storage, r.node, err)
		}
		for _, v := range vols {
			if seen[v.Volid] {
				continue
			}
			seen[v.Volid] = true
			verify := ""
			if v.Verification != nil {
				verify = v.Verification.State
			}
			out = append(out, store.UpsertProxmoxBackupParams{
				ConnectionID: id, Volid: v.Volid, Storage: r.storage, PveNode: r.node, Vmid: int32(v.VMID),
				GuestType: v.Subtype, Ctime: time.Unix(v.Ctime, 0), SizeBytes: v.Size, Format: v.Format,
				Notes: v.Notes, Protected: bool(v.Protected), VerifyState: verify,
			})
		}
	}
	if len(out) == 0 && len(keep) == 0 {
		// Proxmox laat back-ups weg waarvoor het token geen rechten heeft.
		// Geen enkele back-up terwijl we VM's bewaken, is bijna altijd dat.
		n, err := s.q.CountBackupTargets(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if n > 0 {
			return nil, nil, errors.New("Proxmox toont geen enkele back-up; heeft het API-token de rechten VM.Backup en " + //nolint:staticcheck // zin voor de gebruiker
				"Datastore.AllocateSpace (zonder die rechten blijft de lijst leeg), of draait er nog geen back-upjob?")
		}
	}
	return out, keep, nil
}

// readCoverage leest welke VM's in geen back-upjob zitten. Lukt dat niet
// (het token mist Sys.Audit), dan is de dekking onbekend maar gaat de rest
// gewoon door.
func (s *Service) readCoverage(ctx context.Context, api proxmox.API, conn store.ProxmoxConnection) ([]byte, error) {
	guests, err := api.NotBackedUp(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.log.Warn("VM's zonder back-upjob lezen mislukt", "proxmox", conn.Name, "err", err)
		return nil, nil
	}
	return json.Marshal(nonNil(guests))
}

func (s *Service) saveError(ctx context.Context, conn store.ProxmoxConnection, invErr error) {
	msg := invErr.Error()
	ctx = context.WithoutCancel(ctx)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetBackupInventoryResult(ctx, store.SetBackupInventoryResultParams{ID: conn.ID, Error: msg}); err != nil {
			return err
		}
		if conn.BackupError != "" {
			return nil
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.System(), SubjectType: "proxmox", SubjectID: conn.ID.String(), Action: "backup.inventory_failed",
			Payload: map[string]any{"name": conn.Name, "error": msg},
		})
	})
	if err != nil {
		s.log.Error("fout bij back-ups lezen bewaren", "proxmox", conn.Name, "err", err)
	}
}

type key struct {
	conn uuid.UUID
	vmid int32
}

// Evaluate berekent de versheid van elke bewaakte VM en schrijft een event
// bij elke overgang. De eerste berekening voor een VM schrijft niets, en een
// koppeling waarvan de back-ups niet te lezen zijn, blijft staan zoals ze
// was.
func (s *Service) Evaluate(ctx context.Context) error {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		targets, err := q.ListBackupTargets(ctx)
		if err != nil {
			return err
		}
		latest, err := latestByVM(ctx, q)
		if err != nil {
			return err
		}
		rows, err := q.ListBackupStatus(ctx)
		if err != nil {
			return err
		}
		prev := map[key]store.BackupStatus{}
		for _, r := range rows {
			prev[key{r.ConnectionID, r.Vmid}] = r
		}
		now := s.Now()
		seen := map[key]bool{}
		for _, t := range targets {
			k := key{t.ConnectionID, t.Vmid}
			seen[k] = true
			if t.LastError != "" || t.BackupCheckedAt == nil || t.BackupError != "" {
				continue
			}
			var at *time.Time
			b, has := latest[k]
			if has {
				at = &b.Ctime
			}
			f := Judge(at, maxAge(t.MaxAgeHours), now)
			p, known := prev[k]
			switch {
			case !known:
				err = q.InsertBackupStatus(ctx, store.InsertBackupStatusParams{
					ConnectionID: k.conn, Vmid: k.vmid, Freshness: string(f), LatestBackupAt: at,
				})
			case p.Freshness != string(f) || !sameTime(p.LatestBackupAt, at):
				err = q.UpdateBackupStatus(ctx, store.UpdateBackupStatusParams{
					ConnectionID: k.conn, Vmid: k.vmid, Freshness: string(f), LatestBackupAt: at,
				})
			}
			if err != nil {
				return err
			}
			if !known || p.Freshness == string(f) {
				continue
			}
			if err := s.ev.Write(ctx, q, transition(t, Freshness(p.Freshness), f, b, has)); err != nil {
				return err
			}
		}
		for k := range prev {
			if !seen[k] {
				if err := q.DeleteBackupStatus(ctx, store.DeleteBackupStatusParams{ConnectionID: k.conn, Vmid: k.vmid}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func transition(t store.ListBackupTargetsRow, from, to Freshness, b store.ProxmoxBackup, has bool) events.Event {
	action := map[Freshness]string{OK: "backup.fresh", Stale: "backup.stale", Missing: "backup.missing"}[to]
	p := map[string]any{
		"vmid": t.Vmid, "from": string(from), "to": string(to), "max_age_hours": t.MaxAgeHours,
		"connection": t.ConnectionName,
	}
	if has {
		p["latest_backup_at"] = b.Ctime.UTC().Format(time.RFC3339)
		p["volid"] = b.Volid
	}
	e := events.Event{Actor: events.System(), Action: action, Payload: p}
	if t.NodeID != nil {
		e.SubjectType, e.SubjectID, e.ClusterID = "node", t.NodeID.String(), t.ClusterID
		p["hostname"] = deref(t.NodeHostname)
	} else {
		e.SubjectType, e.SubjectID = "proxmox", t.ConnectionID.String()
		p["name"] = or(t.Label, deref(t.GuestName))
	}
	return e
}

func latestByVM(ctx context.Context, q *store.Queries) (map[key]store.ProxmoxBackup, error) {
	rows, err := q.ListLatestBackups(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[key]store.ProxmoxBackup, len(rows))
	for _, r := range rows {
		out[key{r.ConnectionID, r.Vmid}] = r
	}
	return out, nil
}

// DefaultMaxAgeHours is de maximale leeftijd zonder eigen beleid: past bij
// een dagelijkse back-upjob.
const DefaultMaxAgeHours = 30

func maxAge(hours int32) time.Duration { return time.Duration(hours) * time.Hour }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
