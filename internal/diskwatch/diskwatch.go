// Package diskwatch bewaakt de schijfruimte van de nodes (fase 41).
//
// De agents melden al de bezetting van hun bestandssystemen; de server
// bewaart per node de volste (node_status.disk_used_ratio). Hier komen de
// drempels bij (standaard 80 en 90 %), de meldingen voor Aandacht nodig en
// twee taken: meten wat er ruimte inneemt, en veilig opruimen. Beide zijn
// scripts die de agent al kan draaien; er is geen nieuw protocol nodig.
package diskwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/status"
)

// De taken.
const (
	KindScan    = "disk.scan"
	KindCleanup = "disk.cleanup"
)

// Settings zijn de drempels.
type Settings struct {
	WarnPct      int      `json:"warn_pct"`
	CritPct      int      `json:"crit_pct"`
	IgnoreMounts []string `json:"ignore_mounts"`
}

// Defaults zijn de drempels zonder instelling.
var Defaults = Settings{WarnPct: 80, CritPct: 90, IgnoreMounts: []string{}}

// FieldError: ongeldige invoer, met een uitleg voor de gebruiker.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// ErrNotFound: geen node met een agent.
var ErrNotFound = errors.New("node niet gevonden of zonder agent")

// Service is de schijfbewaking.
type Service struct {
	pool   *pgxpool.Pool
	ev     *events.Writer
	runner *jobs.Runner
	log    *slog.Logger
	Run    *noderun.Runner
	Now    func() time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus noderun.Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, ev: ev, runner: runner, log: log, Run: &noderun.Runner{Bus: bus}, Now: time.Now}
	if runner != nil {
		runner.Register(KindScan, s.runScan)
		runner.Register(KindCleanup, s.runCleanup)
	}
	return s
}

// Settings leest de drempels.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.pool.QueryRow(ctx, `SELECT warn_pct, crit_pct, ignore_mounts FROM disk_watch_settings`).Scan(&st.WarnPct, &st.CritPct, &st.IgnoreMounts)
	if errors.Is(err, pgx.ErrNoRows) {
		return Defaults, nil
	}
	if st.IgnoreMounts == nil {
		st.IgnoreMounts = []string{}
	}
	return st, err
}

// SetSettings bewaart de drempels.
func (s *Service) SetSettings(ctx context.Context, actor events.Actor, in Settings) (Settings, error) {
	if in.WarnPct < 50 || in.WarnPct > 99 {
		return Settings{}, &FieldError{"de waarschuwing moet tussen 50 en 99 % liggen"}
	}
	if in.CritPct <= in.WarnPct || in.CritPct > 100 {
		return Settings{}, &FieldError{"ernstig moet hoger liggen dan de waarschuwing, en hoogstens 100 %"}
	}
	var mounts []string
	for _, m := range in.IgnoreMounts {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !strings.HasPrefix(m, "/") || len(m) > 256 {
			return Settings{}, &FieldError{fmt.Sprintf("%q is geen map; een mount begint met /, zoals /mnt/backup", m)}
		}
		if !slices.Contains(mounts, m) {
			mounts = append(mounts, m)
		}
	}
	if mounts == nil {
		mounts = []string{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO disk_watch_settings (id, warn_pct, crit_pct, ignore_mounts, updated_at) VALUES (true, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET warn_pct = $1, crit_pct = $2, ignore_mounts = $3, updated_at = now()`, in.WarnPct, in.CritPct, mounts)
	if err != nil {
		return Settings{}, err
	}
	out := Settings{WarnPct: in.WarnPct, CritPct: in.CritPct, IgnoreMounts: mounts}
	if s.ev != nil {
		_ = s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "settings", SubjectID: "disk", Action: "disk.settings_updated",
			Payload: map[string]any{"warn_pct": out.WarnPct, "crit_pct": out.CritPct, "ignore_mounts": out.IgnoreMounts}})
	}
	return out, nil
}

// Levels.
const (
	LevelOK      = "ok"
	LevelWarn    = "warn"
	LevelCrit    = "crit"
	LevelUnknown = "unknown"
)

// Level zegt hoe vol een schijf is volgens de drempels.
func (st Settings) Level(ratio float64, mount string) string {
	if mount == "" || ratio <= 0 {
		return LevelUnknown
	}
	if slices.Contains(st.IgnoreMounts, mount) {
		return LevelOK
	}
	pct := math.Floor(ratio * 100)
	switch {
	case pct >= float64(st.CritPct):
		return LevelCrit
	case pct >= float64(st.WarnPct):
		return LevelWarn
	}
	return LevelOK
}

// Node is de schijfstand van één node.
type Node struct {
	NodeID     uuid.UUID
	Hostname   string
	ClusterID  *uuid.UUID
	Lifecycle  string
	Online     bool
	Ratio      float64
	Mount      string
	Level      string
	RunningJob *uuid.UUID
	Scan       *Scan
}

const nodesSQL = `
SELECT n.id, n.hostname, n.cluster_id, n.lifecycle::text, s.heartbeat_at,
       coalesce(s.disk_used_ratio, 0), coalesce(s.disk_used_mount, ''),
       CASE WHEN j.status::text IN ('queued', 'running') THEN ds.running_job END,
       ds.result
FROM nodes n
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
LEFT JOIN disk_scans ds ON ds.node_id = n.id
LEFT JOIN jobs j ON j.id = ds.running_job
`

func (s *Service) nodes(ctx context.Context, where string, args ...any) ([]Node, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, nodesSQL+where+` ORDER BY lower(n.hostname)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var out []Node
	for rows.Next() {
		var n Node
		var hb *time.Time
		var result []byte
		if err := rows.Scan(&n.NodeID, &n.Hostname, &n.ClusterID, &n.Lifecycle, &hb, &n.Ratio, &n.Mount, &n.RunningJob, &result); err != nil {
			return nil, err
		}
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		n.Level = st.Level(n.Ratio, n.Mount)
		if len(result) > 0 {
			var sc Scan
			if json.Unmarshal(result, &sc) == nil {
				n.Scan = &sc
			}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Node geeft de schijfstand van één node.
func (s *Service) Node(ctx context.Context, id uuid.UUID) (Node, error) {
	ns, err := s.nodes(ctx, ` WHERE n.id = $1`, id)
	if err != nil {
		return Node{}, err
	}
	if len(ns) == 0 {
		return Node{}, ErrNotFound
	}
	return ns[0], nil
}

// Full geeft de actieve nodes boven de waarschuwingsdrempel; onderhoud en
// afbouwen tellen niet mee.
func (s *Service) Full(ctx context.Context) ([]Node, error) {
	ns, err := s.nodes(ctx, ` WHERE n.lifecycle = 'active'`)
	if err != nil {
		return nil, err
	}
	var out []Node
	for _, n := range ns {
		if n.Level == LevelWarn || n.Level == LevelCrit {
			out = append(out, n)
		}
	}
	return out, nil
}
