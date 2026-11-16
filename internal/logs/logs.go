// Package logs laat de beheerder de logs van de nodes bekijken, filteren en
// vergelijken (fase 23). De agents lezen de logs (agentprotocol 34, subject
// logs); de server vraagt meerdere bronnen tegelijk op, legt de regels op één
// tijdlijn en bewaart alleen weergaven (welke bronnen met welk filter), nooit
// de logs zelf.
package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// MaxSources is het meeste aantal bronnen in één vraag of weergave.
const MaxSources = 12

// MaxEntries is het meeste aantal regels in één antwoord over alle bronnen.
const MaxEntries = 20000

// ErrNotFound: de node of weergave bestaat niet.
var ErrNotFound = errors.New("niet gevonden")

// FieldError is een ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// Bus is wat de dienst van agentbus gebruikt.
type Bus interface {
	Logs(ctx context.Context, nodeID uuid.UUID, req protocol.LogsRequest) (protocol.LogsResponse, error)
}

// Service is de logdienst.
type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	bus  Bus
	log  *slog.Logger
	Now  func() time.Time
}

// NewService maakt de dienst.
func NewService(pool *pgxpool.Pool, ev *events.Writer, bus Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, ev: ev, bus: bus, log: log, Now: time.Now}
}

// Node is een node in de kiezer van de logpagina.
type Node struct {
	ID          uuid.UUID  `json:"id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	PrimaryIP   string     `json:"primary_ip"`
	Protocol    int        `json:"protocol"`
	Online      bool       `json:"online"`
	// Supported: de agent kent logverzoeken (protocol 34 of hoger).
	Supported bool `json:"supported"`
	// HoldsVIP: de node heeft nu het VIP van zijn cluster.
	HoldsVIP bool `json:"holds_vip"`
}

// Nodes geeft elke node met een agent, gegroepeerd per cluster.
func (s *Service) Nodes(ctx context.Context) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT n.id, n.hostname, n.cluster_id, coalesce(c.name, ''), coalesce(host(n.primary_ip), ''),
			a.protocol_version, st.heartbeat_at, EXISTS (SELECT 1 FROM vips v WHERE v.owner_node_id = n.id)
		FROM nodes n
		JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
		LEFT JOIN clusters c ON c.id = n.cluster_id
		LEFT JOIN node_status st ON st.node_id = n.id
		ORDER BY lower(coalesce(c.name, '~')), lower(n.hostname)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	out := []Node{}
	for rows.Next() {
		var n Node
		var hb *time.Time
		if err := rows.Scan(&n.ID, &n.Hostname, &n.ClusterID, &n.ClusterName, &n.PrimaryIP, &n.Protocol, &hb, &n.HoldsVIP); err != nil {
			return nil, err
		}
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		n.Supported = n.Protocol >= protocol.LogsSince
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) node(ctx context.Context, id uuid.UUID) (Node, error) {
	var n Node
	var hb *time.Time
	err := s.pool.QueryRow(ctx, `SELECT n.id, n.hostname, n.cluster_id, coalesce(c.name, ''), coalesce(a.protocol_version, 0), st.heartbeat_at
		FROM nodes n
		LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
		LEFT JOIN clusters c ON c.id = n.cluster_id
		LEFT JOIN node_status st ON st.node_id = n.id
		WHERE n.id = $1`, id).Scan(&n.ID, &n.Hostname, &n.ClusterID, &n.ClusterName, &n.Protocol, &hb)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	if err != nil {
		return n, err
	}
	n.Online = hb != nil && s.Now().Sub(*hb) <= status.HeartbeatLate
	n.Supported = n.Protocol >= protocol.LogsSince
	return n, nil
}

func tooOld(n Node) error {
	if n.Protocol == 0 {
		return &FieldError{n.Hostname + ": geen agent op deze node"}
	}
	return &FieldError{fmt.Sprintf("%s: agent te oud (protocol %d, %d nodig); Nodes → Agents bijwerken", n.Hostname, n.Protocol, protocol.LogsSince)}
}

// Sources is de bronnenlijst van een node.
type Sources struct {
	NodeID     uuid.UUID                   `json:"node_id"`
	Hostname   string                      `json:"hostname"`
	Files      []protocol.LogFileInfo      `json:"files"`
	Units      []string                    `json:"units"`
	Containers []protocol.LogContainerInfo `json:"containers"`
	Journal    bool                        `json:"journal"`
	Docker     bool                        `json:"docker"`
}

// Sources vraagt de agent welke logs er op de node zijn.
func (s *Service) Sources(ctx context.Context, nodeID uuid.UUID) (Sources, error) {
	n, err := s.node(ctx, nodeID)
	if err != nil {
		return Sources{}, err
	}
	if !n.Supported {
		return Sources{}, tooOld(n)
	}
	res, err := s.bus.Logs(ctx, nodeID, protocol.LogsRequest{Op: protocol.LogsOpSources})
	if err != nil {
		return Sources{}, err
	}
	if res.Error != "" {
		return Sources{}, errors.New(res.Error)
	}
	out := Sources{NodeID: n.ID, Hostname: n.Hostname, Files: res.Files, Units: res.Units, Containers: res.Containers, Journal: res.Journal, Docker: res.Docker}
	if out.Files == nil {
		out.Files = []protocol.LogFileInfo{}
	}
	if out.Units == nil {
		out.Units = []string{}
	}
	if out.Containers == nil {
		out.Containers = []protocol.LogContainerInfo{}
	}
	return out, nil
}

// Source is een bron op een node.
type Source struct {
	NodeID uuid.UUID `json:"node_id"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
}

// Query is een vraag naar de regels van een of meer bronnen.
type Query struct {
	Sources []Source
	Filter  protocol.LogFilter
}

// SourceResult is hoe het lezen van één bron ging.
type SourceResult struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id"`
	ClusterName string     `json:"cluster_name"`
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Label       string     `json:"label"`
	Error       string     `json:"error,omitempty"`
	Count       int        `json:"count"`
	Truncated   bool       `json:"truncated"`
	Scanned     int        `json:"scanned"`
	// First en Last zijn de tijden van de oudste en nieuwste regel.
	First *time.Time `json:"first,omitempty"`
	Last  *time.Time `json:"last,omitempty"`
}

// Entry is een regel in het antwoord. Source is de index in Sources.
type Entry struct {
	Source int        `json:"source"`
	TS     *time.Time `json:"ts,omitempty"`
	// Inherited: de regel had geen eigen tijd; TS is die van de regel
	// ervoor in dezelfde bron.
	Inherited bool   `json:"inherited,omitempty"`
	Level     string `json:"level"`
	Proc      string `json:"proc"`
	Msg       string `json:"msg"`
}

// Result is het antwoord op een Query: de regels van alle bronnen samen,
// oud naar nieuw.
type Result struct {
	Sources   []SourceResult `json:"sources"`
	Entries   []Entry        `json:"entries"`
	Truncated bool           `json:"truncated"`
	// Levels telt de regels per niveau ("" heet "none").
	Levels map[string]int `json:"levels"`
}

// CheckQuery controleert een vraag.
func CheckQuery(q Query) error {
	if len(q.Sources) == 0 {
		return &FieldError{"kies minstens één bron"}
	}
	if len(q.Sources) > MaxSources {
		return &FieldError{fmt.Sprintf("hoogstens %d bronnen tegelijk", MaxSources)}
	}
	seen := map[Source]bool{}
	for _, src := range q.Sources {
		if msg := protocol.ValidLogSource(protocol.LogSource{Kind: src.Kind, Name: src.Name}); msg != "" {
			return &FieldError{msg}
		}
		if seen[src] {
			return &FieldError{"dezelfde bron staat er twee keer in"}
		}
		seen[src] = true
	}
	if msg := protocol.CheckLogFilter(q.Filter); msg != "" {
		return &FieldError{msg}
	}
	return nil
}

// Query leest de bronnen tegelijk en legt de regels op één tijdlijn. Een
// bron die faalt (agent offline, bestand weg) staat met zijn fout in
// Sources; de andere komen gewoon mee.
func (s *Service) Query(ctx context.Context, q Query) (Result, error) {
	if err := CheckQuery(q); err != nil {
		return Result{}, err
	}
	res := Result{Sources: make([]SourceResult, len(q.Sources)), Entries: []Entry{}, Levels: map[string]int{}}
	per := make([][]Entry, len(q.Sources))
	nodes := map[uuid.UUID]Node{}
	for i, src := range q.Sources {
		n, ok := nodes[src.NodeID]
		if !ok {
			var err error
			n, err = s.node(ctx, src.NodeID)
			if err != nil {
				return Result{}, err
			}
			nodes[src.NodeID] = n
		}
		res.Sources[i] = SourceResult{NodeID: n.ID, Hostname: n.Hostname, ClusterID: n.ClusterID, ClusterName: n.ClusterName, Kind: src.Kind, Name: src.Name,
			Label: protocol.LogSource{Kind: src.Kind, Name: src.Name}.String()}
	}
	var wg sync.WaitGroup
	for i, src := range q.Sources {
		n := nodes[src.NodeID]
		sr := &res.Sources[i]
		if !n.Supported {
			sr.Error = tooOld(n).Error()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.bus.Logs(ctx, n.ID, protocol.LogsRequest{Op: protocol.LogsOpRead, Source: protocol.LogSource{Kind: src.Kind, Name: src.Name}, Filter: q.Filter})
			if err != nil {
				sr.Error = "de agent antwoordt niet: " + err.Error()
				return
			}
			if r.Error != "" {
				sr.Error = r.Error
				return
			}
			sr.Truncated, sr.Scanned, sr.Count = r.Truncated, r.Scanned, len(r.Entries)
			es := make([]Entry, 0, len(r.Entries))
			var prev *time.Time
			for _, e := range r.Entries {
				out := Entry{Source: i, Level: e.Level, Proc: e.Proc, Msg: e.Msg}
				if !e.TS.IsZero() {
					t := e.TS.UTC()
					out.TS, prev = &t, &t
					if sr.First == nil {
						sr.First = &t
					}
					sr.Last = &t
				} else if prev != nil {
					out.TS, out.Inherited = prev, true
				}
				es = append(es, out)
			}
			per[i] = es
		}()
	}
	wg.Wait()
	res.Entries = merge(per)
	if len(res.Entries) > MaxEntries {
		res.Entries = res.Entries[len(res.Entries)-MaxEntries:]
		res.Truncated = true
	}
	for _, sr := range res.Sources {
		res.Truncated = res.Truncated || sr.Truncated
	}
	for _, e := range res.Entries {
		k := e.Level
		if k == "" {
			k = "none"
		}
		res.Levels[k]++
	}
	return res, nil
}

// merge legt de regels van de bronnen op één tijdlijn. Binnen een bron
// blijft de volgorde van de agent; een regel zonder tijd blijft bij die
// ervoor.
func merge(per [][]Entry) []Entry {
	total := 0
	for _, es := range per {
		total += len(es)
	}
	out := make([]Entry, 0, total)
	idx := make([]int, len(per))
	for len(out) < total {
		best := -1
		for i, es := range per {
			if idx[i] >= len(es) {
				continue
			}
			if best < 0 || before(es[idx[i]], per[best][idx[best]]) {
				best = i
			}
		}
		out = append(out, per[best][idx[best]])
		idx[best]++
	}
	return out
}

func before(a, b Entry) bool {
	switch {
	case a.TS == nil && b.TS == nil:
		return a.Source < b.Source
	case a.TS == nil:
		return true
	case b.TS == nil:
		return false
	}
	if a.TS.Equal(*b.TS) {
		return a.Source < b.Source
	}
	return a.TS.Before(*b.TS)
}

// --- weergaven ---------------------------------------------------------------

// View is een bewaarde weergave.
type View struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	ClusterID *uuid.UUID      `json:"cluster_id"`
	Sources   []Source        `json:"sources"`
	Filter    json.RawMessage `json:"filter"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// ViewInput is wat de GUI bewaart.
type ViewInput struct {
	Name      string
	ClusterID *uuid.UUID
	Sources   []Source
	// Filter is het filter zoals de GUI het bewaart (relatieve tijd); de
	// server controleert alleen dat het een klein JSON-object is.
	Filter json.RawMessage
}

func checkView(in *ViewInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return &FieldError{"geef een naam van 1 tot 100 tekens"}
	}
	if len(in.Sources) == 0 || len(in.Sources) > MaxSources {
		return &FieldError{fmt.Sprintf("een weergave heeft 1 tot %d bronnen", MaxSources)}
	}
	for _, src := range in.Sources {
		if msg := protocol.ValidLogSource(protocol.LogSource{Kind: src.Kind, Name: src.Name}); msg != "" {
			return &FieldError{msg}
		}
	}
	if len(in.Filter) == 0 {
		in.Filter = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if len(in.Filter) > 16<<10 || json.Unmarshal(in.Filter, &obj) != nil || obj == nil {
		return &FieldError{"het filter moet een klein JSON-object zijn"}
	}
	return nil
}

const viewCols = `id, name, cluster_id, sources, filter, created_at, updated_at`

func scanView(row pgx.Row) (View, error) {
	var v View
	var src []byte
	var f []byte
	if err := row.Scan(&v.ID, &v.Name, &v.ClusterID, &src, &f, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return v, err
	}
	if err := json.Unmarshal(src, &v.Sources); err != nil {
		return v, err
	}
	if v.Sources == nil {
		v.Sources = []Source{}
	}
	v.Filter = json.RawMessage(f)
	return v, nil
}

// Views geeft de bewaarde weergaven op naam.
func (s *Service) Views(ctx context.Context) ([]View, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+viewCols+` FROM log_views ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []View{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func uniqueErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return &FieldError{"er is al een weergave met die naam"}
	}
	if errors.As(err, &pe) && pe.Code == "23503" {
		return &FieldError{"het cluster bestaat niet"}
	}
	return err
}

// SaveView maakt (id nil) of wijzigt een weergave.
func (s *Service) SaveView(ctx context.Context, actor events.Actor, userID *uuid.UUID, id *uuid.UUID, in ViewInput) (View, error) {
	if err := checkView(&in); err != nil {
		return View{}, err
	}
	src, _ := json.Marshal(in.Sources)
	var v View
	var err error
	if id == nil {
		v, err = scanView(s.pool.QueryRow(ctx, `INSERT INTO log_views (name, cluster_id, sources, filter, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+viewCols, in.Name, in.ClusterID, src, []byte(in.Filter), userID))
	} else {
		v, err = scanView(s.pool.QueryRow(ctx, `UPDATE log_views SET name = $2, cluster_id = $3, sources = $4, filter = $5, updated_at = now()
			WHERE id = $1 RETURNING `+viewCols, *id, in.Name, in.ClusterID, src, []byte(in.Filter)))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, uniqueErr(err)
	}
	s.event(ctx, actor, v, "logs.view_saved")
	return v, nil
}

// DeleteView verwijdert een weergave.
func (s *Service) DeleteView(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	v, err := scanView(s.pool.QueryRow(ctx, `DELETE FROM log_views WHERE id = $1 RETURNING `+viewCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	s.event(ctx, actor, v, "logs.view_deleted")
	return nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, v View, action string) {
	if s.ev == nil {
		return
	}
	payload := map[string]any{"name": v.Name, "sources": len(v.Sources)}
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "log_view", SubjectID: v.ID.String(), ClusterID: v.ClusterID, Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}
