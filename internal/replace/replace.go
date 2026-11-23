// Package replace is fase 48 (B5.3, B4.8): een losse installatie vervangen
// door een cluster, en na een paar dagen zonder problemen de oude
// installatie opruimen.
//
// Een vervanging is een record "oud adres (en eventueel de oude node) is
// vervangen door cluster X met VIP Y". Daaruit volgen:
//
//   - de taak replace.takeover: op de VIP-node het overnamescript van de
//     template (<template>-ctl.sh overnemen <oud-IP>: oude diensten
//     stilleggen, sleutel en data meenemen), dan de proxy hosts die precies
//     naar het oude adres wijzen naar het VIP (publish.move_address, met
//     controle en terugdraaien), en de voorstellen voor de firewall
//     (regels en port forwards naar het VIP, alleen als voorstel);
//   - na keep_days de melding "oude installatie opruimen?" met één knop:
//     de taak replace.cleanup maakt eerst een kopie (vzdump van de oude
//     gast, of een tar op de oude machine) en zet daarna de oude gast uit
//     en de node uit dienst. Weggooien doe je zelf, wanneer je wil.
package replace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Soorten taken.
const (
	KindTakeover = "replace.takeover"
	KindCleanup  = "replace.cleanup"
)

// Toestanden.
const (
	StateActive  = "actief"
	StateCleanup = "opruimen"
	StateDone    = "opgeruimd"
	StateKept    = "gehouden"
)

// DefaultKeepDays is na hoeveel dagen ClusterForge voorstelt op te ruimen.
const DefaultKeepDays = 14

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound: geen vervanging onder dat id.
var ErrNotFound = errors.New("niet gevonden")

// Publisher zet proxy hosts van het oude adres naar het VIP.
type Publisher interface {
	StartMoveAddress(ctx context.Context, actor events.Actor, oldAddr, newAddr string) (store.Job, error)
	HostsTo(ctx context.Context, addr string) ([]string, error)
}

// PVE doet iets op een Proxmox-host.
type PVE interface {
	FindGuest(ctx context.Context, connID uuid.UUID, vmid int) (hostNode uuid.UUID, pveNode string, g protocol.PVEGuest, err error)
	Do(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, op protocol.PVERun, title string) (protocol.RunStatus, error)
}

// Service doet de vervangingen.
type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	Run  *noderun.Runner
	Pub  Publisher
	PVE  PVE
	Now  func() time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, run *noderun.Runner) *Service {
	s := &Service{pool: pool, ev: ev, log: log, jobs: runner, Run: run, Now: time.Now}
	if runner != nil {
		runner.Register(KindTakeover, s.takeover)
		runner.Register(KindCleanup, s.cleanup)
	}
	return s
}

// Replacement is één vervanging.
type Replacement struct {
	ID          uuid.UUID  `json:"id"`
	ClusterID   uuid.UUID  `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	OldNodeID   *uuid.UUID `json:"old_node_id,omitempty"`
	OldHostname string     `json:"old_hostname,omitempty"`
	OldAddress  string     `json:"old_address"`
	VIP         string     `json:"vip,omitempty"`
	Reason      string     `json:"reason"`
	KeepDays    int        `json:"keep_days"`
	State       string     `json:"state"`
	JobID       *uuid.UUID `json:"job_id,omitempty"`
	CleanupJob  *uuid.UUID `json:"cleanup_job,omitempty"`
	Note        string     `json:"note,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	// DueAt: vanaf dan stelt ClusterForge voor op te ruimen.
	DueAt time.Time `json:"due_at"`
	Due   bool      `json:"due"`
	// Guest: de oude node is een gast in Proxmox (dan vzdump vooraf).
	Guest bool `json:"guest"`
	// OldOnline: de agent van de oude node is verbonden.
	OldOnline bool `json:"old_online"`
}

const selectRows = `SELECT r.id, r.cluster_id, c.name, r.old_node_id, coalesce(nullif(r.old_hostname, ''), n.hostname, ''), host(r.old_address),
	coalesce(host(r.vip), ''), r.reason, r.keep_days, r.state, r.job_id, r.cleanup_job, r.note, r.created_at,
	(n.proxmox_id IS NOT NULL AND n.pve_vmid IS NOT NULL),
	coalesce((SELECT st.heartbeat_at > now() - interval '2 minutes' FROM node_status st WHERE st.node_id = r.old_node_id), false)
	FROM replacements r JOIN clusters c ON c.id = r.cluster_id LEFT JOIN nodes n ON n.id = r.old_node_id`

func (s *Service) scan(rows pgx.Rows) ([]Replacement, error) {
	defer rows.Close()
	out := []Replacement{}
	now := s.Now()
	for rows.Next() {
		var r Replacement
		if err := rows.Scan(&r.ID, &r.ClusterID, &r.ClusterName, &r.OldNodeID, &r.OldHostname, &r.OldAddress, &r.VIP, &r.Reason, &r.KeepDays,
			&r.State, &r.JobID, &r.CleanupJob, &r.Note, &r.CreatedAt, &r.Guest, &r.OldOnline); err != nil {
			return nil, err
		}
		r.DueAt = r.CreatedAt.Add(time.Duration(r.KeepDays) * 24 * time.Hour)
		r.Due = r.State == StateActive && !now.Before(r.DueAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// List geeft de vervangingen, van één cluster of alle (nil).
func (s *Service) List(ctx context.Context, cluster *uuid.UUID) ([]Replacement, error) {
	q := selectRows + ` WHERE ($1::uuid IS NULL OR r.cluster_id = $1) ORDER BY r.created_at DESC`
	rows, err := s.pool.Query(ctx, q, cluster)
	if err != nil {
		return nil, err
	}
	return s.scan(rows)
}

// Get geeft één vervanging.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Replacement, error) {
	rows, err := s.pool.Query(ctx, selectRows+` WHERE r.id = $1`, id)
	if err != nil {
		return Replacement{}, err
	}
	list, err := s.scan(rows)
	if err != nil {
		return Replacement{}, err
	}
	if len(list) == 0 {
		return Replacement{}, ErrNotFound
	}
	return list[0], nil
}

// Input is een vervanging vastleggen of de overname starten.
type Input struct {
	ClusterID  uuid.UUID  `json:"cluster_id"`
	OldNodeID  *uuid.UUID `json:"old_node_id,omitempty"`
	OldAddress string     `json:"old_address,omitempty"`
	KeepDays   int        `json:"keep_days,omitempty"`
	// Takeover: het overnamescript van de template draaien (anders alleen
	// vastleggen, bijvoorbeeld als je het al met de hand deed).
	Takeover bool `json:"takeover"`
}

type resolved struct {
	Cluster, ClusterName, Template string
	ClusterID                      uuid.UUID
	OldNode                        *uuid.UUID
	OldHost, Old, VIP              string
	VIPNode                        *uuid.UUID
}

// resolve controleert de vraag tegen de database.
func (s *Service) resolve(ctx context.Context, in Input) (resolved, error) {
	var r resolved
	r.ClusterID = in.ClusterID
	err := s.pool.QueryRow(ctx, `SELECT c.name, coalesce(c.template_name, ''),
		coalesce((SELECT host(v.address) FROM vips v WHERE v.cluster_id = c.id ORDER BY v.created_at LIMIT 1), ''),
		(SELECT v.owner_node_id FROM vips v WHERE v.cluster_id = c.id AND v.owner_node_id IS NOT NULL ORDER BY v.created_at LIMIT 1)
		FROM clusters c WHERE c.id = $1`, in.ClusterID).Scan(&r.ClusterName, &r.Template, &r.VIP, &r.VIPNode)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if r.VIP == "" {
		return r, ValidationError{"dit cluster heeft geen VIP; een vervanging zet het oude adres op het VIP"}
	}
	r.Old = strings.TrimSpace(in.OldAddress)
	if in.OldNodeID != nil {
		var ip string
		var cl *uuid.UUID
		err := s.pool.QueryRow(ctx, `SELECT hostname, coalesce(host(primary_ip), ''), cluster_id FROM nodes WHERE id = $1`, *in.OldNodeID).Scan(&r.OldHost, &ip, &cl)
		if errors.Is(err, pgx.ErrNoRows) {
			return r, ValidationError{"de oude node bestaat niet"}
		}
		if err != nil {
			return r, err
		}
		if cl != nil && *cl == in.ClusterID {
			return r, ValidationError{r.OldHost + " is zelf een node van dit cluster"}
		}
		if r.Old == "" {
			r.Old = ip
		}
		r.OldNode = in.OldNodeID
	}
	if net.ParseIP(r.Old) == nil {
		return r, ValidationError{"geef het IP-adres van de oude installatie, of kies de oude node"}
	}
	if r.Old == r.VIP {
		return r, ValidationError{"het oude adres is al het VIP van het cluster"}
	}
	var member string
	if err := s.pool.QueryRow(ctx, `SELECT hostname FROM nodes WHERE cluster_id = $1 AND primary_ip = $2::inet LIMIT 1`, in.ClusterID, r.Old).Scan(&member); err == nil {
		return r, ValidationError{r.Old + " is " + member + ", een node van dit cluster"}
	}
	return r, nil
}

func keepDays(d int) int {
	if d <= 0 {
		return DefaultKeepDays
	}
	return min(d, 365)
}

// record legt de vervanging vast (of geeft de open vervanging terug).
func (s *Service) record(ctx context.Context, r resolved, reason string, keep int, by *uuid.UUID, job *uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `INSERT INTO replacements (cluster_id, old_node_id, old_hostname, old_address, vip, reason, keep_days, job_id, created_by)
		VALUES ($1, $2, $3, $4::inet, $5::inet, $6, $7, $8, $9)
		ON CONFLICT (cluster_id, old_address) WHERE state IN ('actief', 'opruimen') DO UPDATE SET job_id = coalesce(EXCLUDED.job_id, replacements.job_id), updated_at = now()
		RETURNING id`, r.ClusterID, r.OldNode, r.OldHost, r.Old, r.VIP, reason, keep, job, by).Scan(&id)
	return id, err
}

// Record legt een vervanging vast zonder overname.
func (s *Service) Record(ctx context.Context, actor events.Actor, by *uuid.UUID, in Input) (Replacement, error) {
	r, err := s.resolve(ctx, in)
	if err != nil {
		return Replacement{}, err
	}
	id, err := s.record(ctx, r, "vervangen", keepDays(in.KeepDays), by, nil)
	if err != nil {
		return Replacement{}, err
	}
	s.event(ctx, actor, "replace.recorded", map[string]any{"cluster_id": r.ClusterID.String(), "old_address": r.Old, "vip": r.VIP})
	return s.Get(ctx, id)
}

// takeoverParams zijn de params van replace.takeover.
type takeoverParams struct {
	Input Input      `json:"input"`
	By    *uuid.UUID `json:"by,omitempty"`
}

// StartTakeover start de overname als taak.
func (s *Service) StartTakeover(ctx context.Context, actor events.Actor, by *uuid.UUID, in Input) (store.Job, error) {
	r, err := s.resolve(ctx, in)
	if err != nil {
		return store.Job{}, err
	}
	if r.VIPNode == nil {
		return store.Job{}, ValidationError{"geen node van " + r.ClusterName + " heeft nu het VIP; is het cluster gezond?"}
	}
	in.Takeover = true
	title := fmt.Sprintf("%s vervangen door cluster %s", or(r.OldHost, r.Old), r.ClusterName)
	cid := r.ClusterID
	j, err := s.jobs.EnqueueForCluster(ctx, jobs.Spec{Kind: KindTakeover, Title: title, Actor: actor, ClusterID: &cid, Params: takeoverParams{Input: in, By: by}})
	if err != nil {
		return store.Job{}, err
	}
	s.event(ctx, actor, "replace.takeover_requested", map[string]any{"cluster_id": cid.String(), "old_address": r.Old, "job_id": j.ID.String()})
	return j, nil
}

// TakeoverScript zoekt op de VIP-node het ctl-script van de template met
// een overname, en draait "overnemen <oud-IP>".
func TakeoverScript(template, old string) string {
	return "OLD=" + shq(old) + "\nTPL=" + shq(template) + `
set -e
pick=
for f in /usr/local/lib/clusterforge/*-ctl.sh; do
  [ -x "$f" ] || continue
  case "$f" in */clusterforge-ctl.sh) continue ;; esac
  grep -qE '^[[:space:]]*overnemen\)' "$f" || continue
  case "$f" in */"$TPL"-ctl.sh) pick=$f; break ;; esac
  [ -z "$pick" ] && pick=$f
done
if [ -z "$pick" ]; then
  echo "CFREPLACE geen-overname"
  echo "De template van dit cluster heeft geen overnamescript; ClusterForge legt de vervanging alleen vast."
  exit 0
fi
echo "CFREPLACE script $pick"
exec "$pick" overnemen "$OLD"
`
}

func shq(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Service) takeover(ctx context.Context, j *jobs.Job) error {
	var p takeoverParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	var r resolved
	if err := j.Step(ctx, "Controle: cluster, VIP en oude installatie", func(ctx context.Context, st *jobs.Step) error {
		var err error
		if r, err = s.resolve(ctx, p.Input); err != nil {
			return err
		}
		if r.VIPNode == nil {
			return errors.New("geen node van het cluster heeft nu het VIP")
		}
		st.Logf("Oud: %s (%s); cluster %s met VIP %s", or(r.OldHost, "losse installatie"), r.Old, r.ClusterName, r.VIP)
		return st.SetState(ctx, r)
	}); err != nil {
		return err
	}
	if r.ClusterName == "" && !j.Done("Controle: cluster, VIP en oude installatie", &r) {
		return errors.New("stand van de controle onbekend")
	}
	if err := j.Step(ctx, "Overnemen op de VIP-node (oude diensten stil, sleutel en data mee)", func(ctx context.Context, st *jobs.Step) error {
		res, err := s.Run.Do(ctx, st, *r.VIPNode, protocol.RunSpec{ID: "cf-vervangen-" + j.ID.String(), Kind: protocol.RunScript,
			Title: "overnemen", Script: TakeoverScript(r.Template, r.Old), TimeoutSeconds: int((2 * time.Hour).Seconds())})
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	}); err != nil {
		return err
	}
	var id uuid.UUID
	if err := j.Step(ctx, "Vastleggen", func(ctx context.Context, st *jobs.Step) error {
		var err error
		jid := j.ID
		id, err = s.record(ctx, r, "overname", keepDays(p.Input.KeepDays), p.By, &jid)
		if err != nil {
			return err
		}
		st.Logf("Over %d dagen stelt ClusterForge voor de oude installatie op te ruimen (eerst een kopie).", keepDays(p.Input.KeepDays))
		return nil
	}); err != nil {
		return err
	}
	_ = j.Step(ctx, "Proxy hosts naar het VIP", func(ctx context.Context, st *jobs.Step) error {
		if s.Pub == nil {
			st.Logf("Publicatie is niet ingesteld.")
			return nil
		}
		hosts, err := s.Pub.HostsTo(ctx, r.Old)
		if err != nil || len(hosts) == 0 {
			st.Logf("Geen proxy host in NPM wijst naar %s.", r.Old)
			return nil //nolint:nilerr // niets te doen
		}
		for _, h := range hosts {
			st.Logf("%s", h)
		}
		pj, err := s.Pub.StartMoveAddress(ctx, actorOf(j), r.Old, r.VIP)
		if err != nil {
			st.Logf("Niet gestart: %s. Doe het later bij het cluster (Vervangen → Proxy hosts naar het VIP).", err)
			return nil
		}
		st.Logf("Taak %s zet ze op %s, met een kopie van NPM vooraf en een controle per host.", pj.ID, r.VIP)
		return nil
	})
	_ = j.Step(ctx, "Firewall: voorstellen", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("Port forwards en regels die nog naar %s wijzen, staan als voorstel naar het VIP %s bij OPNsense → Regeladvies en bij het cluster. ClusterForge past ze niet zelf toe.", r.Old, r.VIP)
		return nil
	})
	s.event(ctx, actorOf(j), "replace.taken_over", map[string]any{"cluster_id": r.ClusterID.String(), "old_address": r.Old, "vip": r.VIP, "id": id.String()})
	return nil
}

func actorOf(j *jobs.Job) events.Actor {
	if j.RequestedBy != nil {
		return events.User(*j.RequestedBy)
	}
	return events.System()
}

// MoveHosts zet de proxy hosts van het oude adres naar het VIP.
func (s *Service) MoveHosts(ctx context.Context, actor events.Actor, id uuid.UUID) (store.Job, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if s.Pub == nil {
		return store.Job{}, ValidationError{"publicatie is niet ingesteld"}
	}
	return s.Pub.StartMoveAddress(ctx, actor, r.OldAddress, r.VIP)
}

// SetKeep verandert het aantal dagen, of houdt de oude installatie
// (StateKept: geen voorstel meer).
func (s *Service) SetKeep(ctx context.Context, actor events.Actor, id uuid.UUID, days int, keep bool) (Replacement, error) {
	state := StateActive
	if keep {
		state = StateKept
	}
	tag, err := s.pool.Exec(ctx, `UPDATE replacements SET keep_days = $2, state = $3, updated_at = now() WHERE id = $1 AND state IN ('actief', 'gehouden')`,
		id, keepDays(days), state)
	if err != nil {
		return Replacement{}, err
	}
	if tag.RowsAffected() == 0 {
		return Replacement{}, ErrNotFound
	}
	s.event(ctx, actor, "replace.keep", map[string]any{"id": id.String(), "days": keepDays(days), "keep": keep})
	return s.Get(ctx, id)
}

// Notice is een melding voor Aandacht.
type Notice struct {
	ID     string
	Title  string
	Detail string
	Href   string
	Count  int
}

// Notices geeft de vervangingen die klaar zijn om op te ruimen: de termijn
// is voorbij en het cluster heeft geen open incident.
func (s *Service) Notices(ctx context.Context) ([]Notice, error) {
	list, err := s.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []Notice
	for _, r := range list {
		if !r.Due {
			continue
		}
		var open int
		// Zonder problemen: geen open incident en geen nieuw in het laatste etmaal.
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM incidents i JOIN incident_subjects x ON x.incident_id = i.id
			WHERE x.cluster_id = $1 AND (i.status = 'open' OR i.started_at > now() - interval '24 hours')`, r.ClusterID).Scan(&open)
		if open > 0 {
			continue
		}
		out = append(out, Notice{ID: "replace-cleanup-" + r.ID.String(), Title: "Oude installatie opruimen?",
			Detail: fmt.Sprintf("%s (%s) is %d dagen vervangen door %s zonder problemen. Eén klik: eerst een kopie, dan uitzetten.",
				or(r.OldHostname, r.OldAddress), r.OldAddress, r.KeepDays, r.ClusterName),
			Href: "/clusters/detail?id=" + r.ClusterID.String() + "#vervangen", Count: 1})
	}
	return out, nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "system", Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

