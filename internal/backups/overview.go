package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Ref verwijst naar een node of cluster.
type Ref struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Volume is één back-up.
type Volume struct {
	Volid     string    `json:"volid"`
	Storage   string    `json:"storage"`
	Host      string    `json:"host"`
	Time      time.Time `json:"time"`
	Size      int64     `json:"size"`
	Format    string    `json:"format"`
	Notes     string    `json:"notes"`
	Protected bool      `json:"protected"`
	// Verify is de verificatie van Proxmox Backup Server: "", ok of failed.
	Verify string `json:"verify"`
}

// Item is de back-upstand van één bewaakte VM.
type Item struct {
	ConnectionID   uuid.UUID `json:"connection_id"`
	ConnectionName string    `json:"connection_name"`
	VMID           int       `json:"vmid"`
	// Name is de naam van de VM in Proxmox; leeg als hij er niet meer is.
	Name string `json:"name"`
	// Label is wat de gebruiker bij "ook bewaken" opgaf.
	Label     string    `json:"label"`
	GuestType string    `json:"guest_type"`
	Node      *Ref      `json:"node"`
	Cluster   *Ref      `json:"cluster"`
	Watched   bool      `json:"watched"`
	Freshness Freshness `json:"freshness"`
	// Reason legt uit waarom de stand niet ok is.
	Reason string `json:"reason"`
	// Since is sinds wanneer de stand zo is; nil als dat nog niet bekend is.
	Since       *time.Time `json:"since"`
	MaxAgeHours int        `json:"max_age_hours"`
	Latest      *Volume    `json:"latest"`
	Count       int        `json:"count"`
	// LastVerification is de laatste afgeronde back-upcontrole van de node.
	LastVerification *Verification `json:"last_verification"`
}

// Verification is de uitkomst van een back-upcontrole.
type Verification struct {
	RunID      uuid.UUID  `json:"run_id"`
	Result     string     `json:"result"`
	Summary    string     `json:"summary"`
	FinishedAt *time.Time `json:"finished_at"`
	// RecoverySeconds is de hersteltijd: terugzetten plus opstarten.
	RecoverySeconds *float64 `json:"recovery_seconds"`
}

// Sandbox is een sandbox die nog kan bestaan.
type Sandbox struct {
	ID             uuid.UUID  `json:"id"`
	ConnectionID   uuid.UUID  `json:"connection_id"`
	ConnectionName string     `json:"connection_name"`
	VMID           int        `json:"vmid"`
	SourceVMID     int        `json:"source_vmid"`
	Source         *Ref       `json:"source"`
	RunID          *uuid.UUID `json:"run_id"`
	// Running is true zolang de controle van de sandbox nog loopt.
	Running     bool       `json:"running"`
	Volid       string     `json:"volid"`
	State       string     `json:"state"`
	Host        string     `json:"host"`
	Storage     string     `json:"storage"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	DestroyedAt *time.Time `json:"destroyed_at"`
}

func sandboxOf(b store.BackupSandbox, connName string, source *string, running bool) Sandbox {
	sb := Sandbox{
		ID: b.ID, ConnectionID: b.ConnectionID, ConnectionName: connName, VMID: int(b.Vmid),
		SourceVMID: int(b.SourceVmid), RunID: b.RunID, Volid: b.Volid, State: b.State, Host: b.Host,
		Storage: b.Storage, Error: b.Error, CreatedAt: b.CreatedAt, DestroyedAt: b.DestroyedAt, Running: running,
	}
	if b.SourceNodeID != nil {
		sb.Source = &Ref{ID: *b.SourceNodeID, Name: deref(source)}
	}
	return sb
}

// Sandbox geeft één sandbox, ook een opgeruimde.
func (s *Service) Sandbox(ctx context.Context, id uuid.UUID) (Sandbox, error) {
	r, err := s.q.GetSandboxView(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sandbox{}, ErrNotFound
	}
	if err != nil {
		return Sandbox{}, err
	}
	running := r.RunResult == nil && r.JobStatus.Valid &&
		(r.JobStatus.JobStatus == store.JobStatusQueued || r.JobStatus.JobStatus == store.JobStatusRunning)
	return sandboxOf(r.BackupSandbox, r.ConnectionName, r.SourceHostname, running && liveState(r.BackupSandbox.State)), nil
}

// RunSandbox geeft de sandbox van een run; nil als er geen was.
func (s *Service) RunSandbox(ctx context.Context, run store.TestRun, running bool) (*Sandbox, error) {
	b, err := s.q.GetRunSandbox(ctx, &run.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var def Definition
	_ = json.Unmarshal(run.Definition, &def)
	sb := sandboxOf(b, def.ConnectionName, &run.Hostname, running && liveState(b.State))
	return &sb, nil
}

// Uncovered is een VM of container die in geen back-upjob zit.
type Uncovered struct {
	VMID int    `json:"vmid"`
	Name string `json:"name"`
	Type string `json:"type"`
	Node *Ref   `json:"node"`
}

// WatchEntry is een VM zonder node waarvan de back-up toch bewaakt wordt.
type WatchEntry struct {
	VMID  int    `json:"vmid"`
	Label string `json:"label"`
}

// Connection is de back-upstand van een Proxmox-koppeling.
type Connection struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	CheckedAt *time.Time `json:"checked_at"`
	Error     string     `json:"error"`
	// Watch is de lijst "ook bewaken".
	Watch []WatchEntry `json:"watch"`
	// NotBackedUp is nil als Proxmox dat niet liet lezen.
	NotBackedUp []Uncovered `json:"not_backed_up"`
	BackupCount int         `json:"backup_count"`
}

// ClusterState is de slechtste stand van de nodes van een cluster.
type ClusterState struct {
	ID        uuid.UUID `json:"id"`
	Freshness Freshness `json:"freshness"`
}

// Overview is alles wat de pagina Back-ups toont.
type Overview struct {
	Items       []Item         `json:"items"`
	Connections []Connection   `json:"connections"`
	Clusters    []ClusterState `json:"clusters"`
	Sandboxes   []Sandbox      `json:"sandboxes"`
}

// Overview geeft de stand van elke bewaakte VM en elke koppeling.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	out := Overview{Items: []Item{}, Connections: []Connection{}, Clusters: []ClusterState{}, Sandboxes: []Sandbox{}}
	targets, err := s.q.ListBackupTargets(ctx)
	if err != nil {
		return out, err
	}
	latest, err := latestByVM(ctx, s.q)
	if err != nil {
		return out, err
	}
	verified, err := s.lastVerifications(ctx)
	if err != nil {
		return out, err
	}
	now := s.Now()
	clusters := map[uuid.UUID]Freshness{}
	var order []uuid.UUID
	linked := map[key]*Ref{}
	for _, t := range targets {
		it := item(t, latest, now)
		if it.Node != nil {
			it.LastVerification = verified[it.Node.ID]
		}
		out.Items = append(out.Items, it)
		if it.Node != nil {
			linked[key{t.ConnectionID, t.Vmid}] = it.Node
		}
		if it.Cluster == nil {
			continue
		}
		f, ok := clusters[it.Cluster.ID]
		if !ok {
			order = append(order, it.Cluster.ID)
			f = OK
		}
		clusters[it.Cluster.ID] = Worse(f, it.Freshness)
	}
	for _, id := range order {
		out.Clusters = append(out.Clusters, ClusterState{ID: id, Freshness: clusters[id]})
	}
	conns, err := s.q.ListBackupConnections(ctx)
	if err != nil {
		return out, err
	}
	watch, err := s.q.ListBackupWatch(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range conns {
		cn := connection(c, linked)
		for _, w := range watch {
			if w.ConnectionID == c.ID {
				cn.Watch = append(cn.Watch, WatchEntry{VMID: int(w.Vmid), Label: w.Label})
			}
		}
		out.Connections = append(out.Connections, cn)
	}
	out.Sandboxes, err = s.sandboxes(ctx)
	return out, err
}

func (s *Service) lastVerifications(ctx context.Context) (map[uuid.UUID]*Verification, error) {
	rows, err := s.q.LatestVerifications(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*Verification, len(rows))
	for _, r := range rows {
		if r.NodeID == nil || r.Result == nil {
			continue
		}
		v := &Verification{RunID: r.ID, Result: *r.Result, Summary: r.Summary, FinishedAt: r.FinishedAt}
		var m Measurements
		if json.Unmarshal(r.Measurements, &m) == nil && m.RestoreSeconds != nil {
			total := *m.RestoreSeconds
			if m.BootSeconds != nil {
				total += *m.BootSeconds
			}
			v.RecoverySeconds = &total
		}
		out[*r.NodeID] = v
	}
	return out, nil
}

func (s *Service) sandboxes(ctx context.Context) ([]Sandbox, error) {
	rows, err := s.q.ListLiveSandboxes(ctx)
	if err != nil {
		return nil, err
	}
	out := []Sandbox{}
	for _, r := range rows {
		running := r.RunResult == nil && r.JobStatus.Valid &&
			(r.JobStatus.JobStatus == store.JobStatusQueued || r.JobStatus.JobStatus == store.JobStatusRunning)
		out = append(out, sandboxOf(r.BackupSandbox, r.ConnectionName, r.SourceHostname, running))
	}
	return out, nil
}

func item(t store.ListBackupTargetsRow, latest map[key]store.ProxmoxBackup, now time.Time) Item {
	it := Item{
		ConnectionID: t.ConnectionID, ConnectionName: t.ConnectionName, VMID: int(t.Vmid), Name: deref(t.GuestName),
		Label: t.Label, GuestType: deref(t.GuestType), Watched: t.Watched, MaxAgeHours: int(t.MaxAgeHours),
		Count: int(t.BackupCount),
	}
	if t.NodeID != nil {
		it.Node = &Ref{ID: *t.NodeID, Name: deref(t.NodeHostname)}
	}
	if t.ClusterID != nil {
		it.Cluster = &Ref{ID: *t.ClusterID, Name: deref(t.ClusterName)}
	}
	var at *time.Time
	if b, ok := latest[key{t.ConnectionID, t.Vmid}]; ok {
		v := volume(b)
		it.Latest, at = &v, &b.Ctime
	}
	switch {
	case t.LastError != "":
		it.Freshness, it.Reason = Unknown, "Proxmox is niet bereikbaar: "+t.LastError
	case t.BackupError != "":
		it.Freshness, it.Reason = Unknown, "De back-ups zijn niet te lezen: "+t.BackupError
	case t.BackupCheckedAt == nil:
		it.Freshness, it.Reason = Unknown, "De back-ups zijn nog niet gelezen."
	default:
		it.Freshness = Judge(at, maxAge(t.MaxAgeHours), now)
		switch it.Freshness {
		case Stale:
			it.Reason = fmt.Sprintf("De nieuwste back-up is ouder dan %d uur.", t.MaxAgeHours)
		case Missing:
			it.Reason = "Proxmox heeft geen back-up van deze VM."
		}
		if t.LastFreshness != nil && *t.LastFreshness == string(it.Freshness) {
			it.Since = t.FreshnessSince
		}
	}
	return it
}

func volume(b store.ProxmoxBackup) Volume {
	return Volume{
		Volid: b.Volid, Storage: b.Storage, Host: b.PveNode, Time: b.Ctime, Size: b.SizeBytes, Format: b.Format,
		Notes: b.Notes, Protected: b.Protected, Verify: b.VerifyState,
	}
}

func connection(c store.ListBackupConnectionsRow, linked map[key]*Ref) Connection {
	out := Connection{
		ID: c.ID, Name: c.Name, CheckedAt: c.BackupCheckedAt, Error: c.BackupError, Watch: []WatchEntry{},
		BackupCount: int(c.BackupCount),
	}
	if c.BackupError != "" {
		out.Error = "De back-ups zijn niet te lezen: " + c.BackupError
	}
	if c.LastError != "" {
		out.Error = "Proxmox is niet bereikbaar: " + c.LastError
	}
	if c.NotBackedUp != nil {
		var guests []Uncovered
		if json.Unmarshal(c.NotBackedUp, &guests) == nil {
			out.NotBackedUp = []Uncovered{}
			for _, g := range guests {
				g.Node = linked[key{c.ID, int32(g.VMID)}]
				out.NotBackedUp = append(out.NotBackedUp, g)
			}
			slices.SortFunc(out.NotBackedUp, func(a, b Uncovered) int { return a.VMID - b.VMID })
		}
	}
	return out
}

// NodeBackups is de back-upstand van één node met al zijn back-ups.
type NodeBackups struct {
	// Item is nil als de node niet aan een VM in Proxmox gekoppeld is.
	Item    *Item    `json:"item"`
	Backups []Volume `json:"backups"`
}

// ForNode geeft de stand en de back-ups van de VM van een node.
func (s *Service) ForNode(ctx context.Context, nodeID uuid.UUID) (NodeBackups, error) {
	out := NodeBackups{Backups: []Volume{}}
	n, err := s.q.GetNode(ctx, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if n.Node.ProxmoxID == nil || n.Node.PveVmid == nil {
		return out, nil
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return out, err
	}
	for i := range ov.Items {
		if it := ov.Items[i]; it.Node != nil && it.Node.ID == nodeID {
			out.Item = &it
		}
	}
	rows, err := s.q.ListGuestBackups(ctx, store.ListGuestBackupsParams{ConnectionID: *n.Node.ProxmoxID, Vmid: *n.Node.PveVmid})
	if err != nil {
		return out, err
	}
	for _, b := range rows {
		out.Backups = append(out.Backups, volume(b))
	}
	return out, nil
}

// Policy is het back-upbeleid van een cluster.
type Policy struct {
	MaxAgeHours int `json:"max_age_hours"`
	// Default is true als het cluster nog geen eigen beleid heeft.
	Default bool `json:"default"`
}

// Policy geeft het beleid van een cluster, of de standaard.
func (s *Service) Policy(ctx context.Context, clusterID uuid.UUID) (Policy, error) {
	if _, err := s.q.GetCluster(ctx, clusterID); errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrNotFound
	} else if err != nil {
		return Policy{}, err
	}
	p, err := s.q.GetBackupPolicy(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{MaxAgeHours: DefaultMaxAgeHours, Default: true}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	return Policy{MaxAgeHours: int(p.MaxAgeHours)}, nil
}

// UpdatePolicy wijzigt de maximale leeftijd van de back-ups van een cluster.
func (s *Service) UpdatePolicy(ctx context.Context, actor events.Actor, by *uuid.UUID, clusterID uuid.UUID, maxAgeHours int) error {
	if maxAgeHours < 1 || maxAgeHours > 720 {
		return ValidationError{"de maximale leeftijd moet tussen 1 en 720 uur liggen"}
	}
	cur, err := s.Policy(ctx, clusterID)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.LockCluster(ctx, clusterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := q.UpsertBackupPolicy(ctx, store.UpsertBackupPolicyParams{
			ClusterID: clusterID, MaxAgeHours: int32(maxAgeHours), UpdatedBy: by,
		}); err != nil {
			return err
		}
		if cur.MaxAgeHours == maxAgeHours {
			return nil
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "backup.policy_updated",
			Payload: map[string]any{
				"name": c.Name, "max_age_hours": map[string]any{"from": cur.MaxAgeHours, "to": maxAgeHours},
			},
		})
	})
	if err != nil {
		return err
	}
	s.Kick()
	return nil
}

// SetWatch vervangt de lijst "ook bewaken" van een koppeling.
func (s *Service) SetWatch(ctx context.Context, actor events.Actor, connID uuid.UUID, entries []WatchEntry) error {
	if len(entries) > 50 {
		return ValidationError{"hoogstens 50 VM's extra bewaken"}
	}
	seen := map[int]bool{}
	for i, e := range entries {
		if e.VMID < 100 || e.VMID > 999999999 {
			return ValidationError{fmt.Sprintf("VMID %d bestaat niet in Proxmox (100 tot 999999999)", e.VMID)}
		}
		if seen[e.VMID] {
			return ValidationError{fmt.Sprintf("VMID %d staat twee keer in de lijst", e.VMID)}
		}
		seen[e.VMID] = true
		entries[i].Label = strings.TrimSpace(e.Label)
		if len(entries[i].Label) > 100 {
			return ValidationError{"een label is hoogstens 100 tekens"}
		}
	}
	slices.SortFunc(entries, func(a, b WatchEntry) int { return a.VMID - b.VMID })
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.LockProxmoxConnection(ctx, connID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := q.ListBackupWatch(ctx)
		if err != nil {
			return err
		}
		var before []string
		for _, r := range rows {
			if r.ConnectionID == connID {
				before = append(before, watchText(int(r.Vmid), r.Label))
			}
		}
		if err := q.DeleteBackupWatch(ctx, connID); err != nil {
			return err
		}
		after := []string{}
		for _, e := range entries {
			if err := q.InsertBackupWatch(ctx, store.InsertBackupWatchParams{ConnectionID: connID, Vmid: int32(e.VMID), Label: e.Label}); err != nil {
				return err
			}
			after = append(after, watchText(e.VMID, e.Label))
		}
		if slices.Equal(before, after) {
			return nil
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "proxmox", SubjectID: connID.String(), Action: "backup.watch_updated",
			Payload: map[string]any{"name": c.Name, "watch": map[string]any{"from": nonNil(before), "to": after}},
		})
	})
	if err != nil {
		return err
	}
	s.Kick()
	return nil
}

func watchText(vmid int, label string) string {
	if label == "" {
		return fmt.Sprintf("VM %d", vmid)
	}
	return fmt.Sprintf("VM %d (%s)", vmid, label)
}
