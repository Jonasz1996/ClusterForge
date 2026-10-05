// Package audit maakt van de eventtabel een leesbaar logboek: filters,
// paginering, een Nederlandse zin per regel en een export.
package audit

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
	// MaxExport is het grootste aantal regels in één export.
	MaxExport   = 100_000
	exportBatch = 1000
)

// Filter beperkt het logboek. Lege velden filteren niet.
type Filter struct {
	// Before geeft regels met een kleiner id: de volgende pagina.
	Before    *int64
	Limit     int
	From, To  *time.Time
	ActorType store.ActorType
	// User vindt wat een gebruiker deed, wat namens hem gebeurde (de afronding
	// van zijn taken) en wat over zijn account ging.
	User     *uuid.UUID
	Cluster  *uuid.UUID
	Node     *uuid.UUID
	Job      *uuid.UUID
	Category events.Category
	Query    string
}

type Actor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Ref struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

type Subject struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// Change is één gewijzigd veld, met waarden zoals een mens ze leest.
type Change struct {
	Field string `json:"field"`
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Entry is één regel van het logboek. De export schrijft dezelfde velden.
type Entry struct {
	ID       int64     `json:"id"`
	Ts       time.Time `json:"ts"`
	Action   string    `json:"action"`
	Category string    `json:"category"`
	Summary  string    `json:"summary"`
	Actor    Actor     `json:"actor"`
	// OnBehalfOf is de gebruiker die de taak aanvroeg waarvan dit event deel
	// is, als het systeem het schreef.
	OnBehalfOf *Ref    `json:"on_behalf_of"`
	Subject    Subject `json:"subject"`
	Cluster    *Ref    `json:"cluster"`
	Node       *Ref    `json:"node"`
	Job        *Ref    `json:"job"`
	IP         *string `json:"ip"`
	// Session is het begin van de sessiehash: regels met dezelfde waarde
	// komen uit dezelfde login.
	Session *string        `json:"session"`
	Changes []Change       `json:"changes"`
	Payload map[string]any `json:"payload"`
}

type Service struct {
	q *store.Queries
}

func NewService(q *store.Queries) *Service {
	return &Service{q: q}
}

// List geeft een pagina van het logboek, nieuwste eerst, en het id om de
// volgende pagina mee op te vragen (nil als dit de laatste is).
func (s *Service) List(ctx context.Context, f Filter) ([]Entry, *int64, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.q.ListAudit(ctx, params(f, limit+1))
	if err != nil {
		return nil, nil, err
	}
	var next *int64
	if len(rows) > limit {
		rows = rows[:limit]
		id := rows[limit-1].ID
		next = &id
	}
	n, err := s.names(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, describe(r, n))
	}
	return out, next, nil
}

// Export schrijft alle regels die aan f voldoen als NDJSON, in porties, tot
// MaxExport. flush wordt na elke portie aangeroepen. Het geeft het aantal
// regels terug.
func (s *Service) Export(ctx context.Context, f Filter, w io.Writer, flush func() error) (int, error) {
	n, err := s.names(ctx)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(w)
	count := 0
	for count < MaxExport {
		rows, err := s.q.ListAudit(ctx, params(f, min(exportBatch, MaxExport-count)))
		if err != nil {
			return count, err
		}
		for _, r := range rows {
			if err := enc.Encode(describe(r, n)); err != nil {
				return count, err
			}
		}
		count += len(rows)
		if err := flush(); err != nil {
			return count, err
		}
		if len(rows) < exportBatch {
			break
		}
		id := rows[len(rows)-1].ID
		f.Before = &id
	}
	return count, nil
}

func params(f Filter, limit int) store.ListAuditParams {
	p := store.ListAuditParams{BeforeID: f.Before, FromTs: f.From, ToTs: f.To, Lim: int32(limit)}
	if f.ActorType != "" {
		p.ActorType = store.NullActorType{ActorType: f.ActorType, Valid: true}
	}
	p.UserID, p.ClusterID, p.NodeID, p.JobID = idText(f.User), idText(f.Cluster), idText(f.Node), idText(f.Job)
	if f.Category != "" {
		p.Actions = events.Actions(f.Category)
		if p.Actions == nil {
			p.Actions = []string{}
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pattern := "%" + likeEscaper.Replace(q) + "%"
		p.Pattern = &pattern
	}
	return p
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func idText(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// User is een gebruiker zoals het logboek hem kent, ook als hij uitgeschakeld is.
type User struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
	Disabled bool      `json:"disabled"`
}

// Category is een soort met zijn Nederlandse naam.
type Category struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Info is wat de filterbalk nodig heeft.
type Info struct {
	Categories      []Category `json:"categories"`
	Users           []User     `json:"users"`
	DeletedClusters []Ref      `json:"deleted_clusters"`
	DeletedNodes    []Ref      `json:"deleted_nodes"`
	Total           int64      `json:"total"`
	Oldest          *time.Time `json:"oldest"`
}

func (s *Service) Info(ctx context.Context) (Info, error) {
	info := Info{Categories: []Category{}, Users: []User{}, DeletedClusters: []Ref{}, DeletedNodes: []Ref{}}
	for _, c := range events.Categories {
		info.Categories = append(info.Categories, Category{Key: string(c.Key), Label: c.Label})
	}
	users, err := s.q.ListAuditUsers(ctx)
	if err != nil {
		return info, err
	}
	for _, u := range users {
		info.Users = append(info.Users, User{ID: u.ID, Username: u.Username, Disabled: u.DisabledAt != nil})
	}
	deleted, err := s.q.ListDeletedSubjects(ctx)
	if err != nil {
		return info, err
	}
	n, err := s.names(ctx)
	if err != nil {
		return info, err
	}
	seen := map[string]bool{}
	for _, d := range deleted {
		// Een id kan terugkomen, bijvoorbeeld een node die opnieuw aangemeld is.
		if seen[d.SubjectID] || n.byID[d.SubjectID] != "" {
			continue
		}
		seen[d.SubjectID] = true
		r := Ref{ID: d.SubjectID, Name: d.Name, Deleted: true}
		if d.SubjectType == "cluster" {
			info.DeletedClusters = append(info.DeletedClusters, r)
		} else {
			info.DeletedNodes = append(info.DeletedNodes, r)
		}
	}
	st, err := s.q.AuditStats(ctx)
	if err != nil {
		return info, err
	}
	info.Total = st.Total
	if st.Total > 0 {
		info.Oldest = &st.Oldest
	}
	return info, nil
}

// names zijn de namen van gebruikers, clusters en nodes op id, voor de
// wijzigingen en de VIP-eigenaar.
type names struct {
	byID       map[string]string
	users      map[string]string
	byUsername map[string]uuid.UUID
}

func (s *Service) names(ctx context.Context) (names, error) {
	n := names{byID: map[string]string{}, users: map[string]string{}, byUsername: map[string]uuid.UUID{}}
	users, err := s.q.ListAuditUsers(ctx)
	if err != nil {
		return n, err
	}
	for _, u := range users {
		n.users[u.ID.String()] = u.Username
		n.byUsername[strings.ToLower(u.Username)] = u.ID
	}
	rows, err := s.q.ListAuditNames(ctx)
	if err != nil {
		return n, err
	}
	for _, r := range rows {
		n.byID[r.ID.String()] = r.Name
	}
	return n, nil
}
