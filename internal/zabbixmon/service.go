package zabbixmon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/zabbixagent"
)

// Service verzamelt elke minuut wat er in Zabbix gebeurt en houdt de laatste
// stand in het geheugen; elk kwartier gaat een momentopname naar de database.
type Service struct {
	pool   *pgxpool.Pool
	ev     *events.Writer
	agents *zabbixagent.Service
	log    *slog.Logger

	// Client geeft de API en het adres van api_jsonrpc.php; tests zetten een eigen.
	Client func(ctx context.Context) (API, string, error)
	Now    func() time.Time
	Every  time.Duration
	// Changed wordt na elke ronde aangeroepen (bv. om caches te legen).
	Changed func()

	run        sync.Mutex // één ronde tegelijk
	mu         sync.RWMutex
	snap       *Snapshot
	configured bool
	apiURL     string
	lastErr    string
	lastOK     time.Time
	lastRun    time.Time
	lastSaved  time.Time
	cache      itemCache
	dash       dashCache
	// logged: de problemen die al als event in het logboek staan (alleen
	// onder run).
	logged map[string]loggedProblem
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, agents *zabbixagent.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, ev: ev, agents: agents, log: log, Now: time.Now, Every: time.Minute}
	s.Client = func(ctx context.Context) (API, string, error) {
		c, u, err := agents.APIClient(ctx)
		if c == nil {
			return nil, u, err
		}
		return c, u, err
	}
	return s
}

// ErrNotFound: geen host of node met dat id.
var ErrNotFound = errors.New("niet gevonden")

// FieldError: ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// Loop draait elke minuut een ronde; na een fout wacht hij langer (tot 5 min).
func (s *Service) Loop(ctx context.Context) {
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := s.Tick(ctx)
		switch {
		case err == nil:
			wait = s.Every
		case errors.Is(err, zabbixagent.ErrNotConfigured):
			wait = s.Every
		default:
			s.log.Warn("Zabbix-monitoring: ronde mislukt", "err", err)
			wait = min(max(2*wait, s.Every), 5*time.Minute)
		}
	}
}

// Refresh draait meteen een ronde, tenzij er net een was.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.RLock()
	recent := s.Now().Sub(s.lastRun) < 5*time.Second
	s.mu.RUnlock()
	if recent {
		return nil
	}
	return s.Tick(ctx)
}

// Tick doet één ronde.
func (s *Service) Tick(ctx context.Context) error {
	s.run.Lock()
	defer s.run.Unlock()
	now := s.Now()
	api, url, err := s.Client(ctx)
	if errors.Is(err, zabbixagent.ErrNotConfigured) {
		s.mu.Lock()
		s.configured, s.apiURL, s.snap, s.lastErr, s.lastRun = false, url, nil, "", now
		s.mu.Unlock()
		return err
	}
	if err == nil {
		var nodes []CFNode
		var links map[string]*uuid.UUID
		if nodes, err = s.cfNodes(ctx); err == nil {
			if links, err = s.links(ctx); err == nil {
				cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
				var snap Snapshot
				snap, err = Collect(cctx, api, nodes, links, now)
				cancel()
				if err == nil {
					s.mu.Lock()
					s.configured, s.apiURL, s.snap, s.lastErr, s.lastOK, s.lastRun = true, url, &snap, "", now, now
					s.mu.Unlock()
					s.save(ctx, &snap, now)
					s.logProblems(ctx, &snap, now)
					if s.Changed != nil {
						s.Changed()
					}
					return nil
				}
			}
		}
	}
	s.mu.Lock()
	s.configured, s.apiURL, s.lastErr, s.lastRun = true, url, err.Error(), now
	s.mu.Unlock()
	return err
}

// Current geeft de laatste stand (nil zonder geslaagde ronde).
func (s *Service) Current() (*Snapshot, State, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.configured {
		return nil, State{Level: LevelUnknown, Reasons: []string{"de Zabbix-API is nog niet ingesteld"}}, false
	}
	return s.snap, Evaluate(s.snap, s.lastErr, s.lastOK, s.Now()), true
}

const cfNodesSQL = `
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), ''), n.cluster_id, coalesce(c.name, ''),
       n.lifecycle::text, coalesce(zn.zabbix_host_id, '')
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN zabbix_agent_nodes zn ON zn.node_id = n.id
ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname)`

func (s *Service) cfNodes(ctx context.Context) ([]CFNode, error) {
	rows, err := s.pool.Query(ctx, cfNodesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CFNode
	for rows.Next() {
		var n CFNode
		if err := rows.Scan(&n.ID, &n.Hostname, &n.IP, &n.ClusterID, &n.ClusterName, &n.Lifecycle, &n.ZabbixHostID); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) links(ctx context.Context) (map[string]*uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT hostid, node_id FROM zabbix_host_links`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*uuid.UUID{}
	for rows.Next() {
		var h string
		var n *uuid.UUID
		if err := rows.Scan(&h, &n); err != nil {
			return nil, err
		}
		out[h] = n
	}
	return out, rows.Err()
}

// SetLink koppelt een Zabbix-host met de hand aan een node; nodeID nil
// betekent "bewust geen node".
func (s *Service) SetLink(ctx context.Context, actor events.Actor, userID *uuid.UUID, hostID string, nodeID *uuid.UUID) error {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" || len(hostID) > 32 {
		return &FieldError{"ongeldig hostid"}
	}
	var hostname string
	if nodeID != nil {
		if err := s.pool.QueryRow(ctx, `SELECT hostname FROM nodes WHERE id = $1`, *nodeID).Scan(&hostname); err != nil {
			return ErrNotFound
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Een node hoort bij hoogstens één host: een oude handmatige koppeling
	// van deze node gaat weg.
	if nodeID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM zabbix_host_links WHERE node_id = $1 AND hostid <> $2`, *nodeID, hostID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO zabbix_host_links (hostid, node_id, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (hostid) DO UPDATE SET node_id = excluded.node_id, updated_at = now(), updated_by = excluded.updated_by`,
		hostID, nodeID, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	payload := map[string]any{"hostid": hostID}
	if nodeID != nil {
		payload["node_id"], payload["hostname"] = nodeID.String(), hostname
	}
	s.event(ctx, actor, "zabbix.host_linked", "zabbix_host", hostID, payload)
	return nil
}

// DeleteLink haalt een handmatige koppeling weg; daarna geldt weer de
// automatische.
func (s *Service) DeleteLink(ctx context.Context, actor events.Actor, hostID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM zabbix_host_links WHERE hostid = $1`, hostID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.event(ctx, actor, "zabbix.host_unlinked", "zabbix_host", hostID, map[string]any{"hostid": hostID})
	return nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, action, subjectType, subjectID string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: subjectType, SubjectID: subjectID, Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// FrontendURL is het adres van de Zabbix-webinterface bij het API-adres
// (…/api_jsonrpc.php weg, met een / op het eind).
func FrontendURL(apiURL string) string {
	u := strings.TrimSpace(apiURL)
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, "api_jsonrpc.php")
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

// Overview is wat de pagina Monitoring → Zabbix toont.
type Overview struct {
	Configured  bool         `json:"configured"`
	GeneratedAt time.Time    `json:"generated_at"`
	UpdatedAt   *time.Time   `json:"updated_at"`
	Error       string       `json:"error"`
	State       State        `json:"state"`
	FrontendURL string       `json:"frontend_url"`
	Window      string       `json:"window"`
	Trend       []TrendPoint `json:"trend"`
	Snapshot
}

// Overview geeft de laatste stand met de trend van het venster.
func (s *Service) Overview(ctx context.Context, window string) (Overview, error) {
	d, ok := Windows[window]
	if !ok {
		window, d = "24h", Windows["24h"]
	}
	snap, st, configured := s.Current()
	s.mu.RLock()
	o := Overview{
		Configured: configured, GeneratedAt: s.Now().UTC(), Error: s.lastErr, State: st,
		FrontendURL: FrontendURL(s.apiURL), Window: window,
	}
	if !s.lastOK.IsZero() {
		t := s.lastOK.UTC()
		o.UpdatedAt = &t
	}
	s.mu.RUnlock()
	if snap != nil {
		o.Snapshot = *snap
	} else {
		o.Snapshot = Snapshot{Problems: []Problem{}, Hosts: []Host{}, Proxies: []Proxy{}, HA: []HANode{}, Health: []Health{}, Unwatched: []NodeRef{}}
	}
	tr, err := s.Trend(ctx, d)
	if err != nil {
		return o, err
	}
	o.Trend = tr
	return o, nil
}
