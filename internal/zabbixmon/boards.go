package zabbixmon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// --- eigen dashboards (M4) -----------------------------------------------------------

// Paneelsoorten van een eigen dashboard.
const (
	PanelGraph    = "graph"    // lijnen van 1-6 items over een venster
	PanelStat     = "stat"     // de laatste waarde van één item, groot
	PanelGauge    = "gauge"    // de laatste waarde van één item als meter
	PanelProblems = "problems" // open problemen vanaf een ernst, eventueel van één host
	PanelText     = "text"     // een stukje uitleg
)

// Grenzen van een dashboard.
const (
	MaxPanels     = 48
	MaxGraphItems = 6
	maxText       = 2000
	maxName       = 80
	maxDesc       = 300
	maxTitle      = 80
)

// BoardItem is een item in een paneel, met een eigen naam voor de legende.
type BoardItem struct {
	ItemID string `json:"itemid"`
	Label  string `json:"label"`
}

// Panel is één vak op een dashboard. W is de breedte in kolommen (1-4 van 4),
// H de hoogte (1-3: klein, middel, groot).
type Panel struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Title       string      `json:"title"`
	W           int         `json:"w"`
	H           int         `json:"h"`
	Items       []BoardItem `json:"items"`
	Range       string      `json:"range"`
	Warn        *float64    `json:"warn"`
	Crit        *float64    `json:"crit"`
	Max         *float64    `json:"max"`
	MinSeverity int         `json:"min_severity"`
	HostID      string      `json:"hostid"`
	Text        string      `json:"text"`
}

// BoardInput is wat de gebruiker bewaart (en wat een export bevat).
type BoardInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Refresh     int     `json:"refresh"`
	Panels      []Panel `json:"panels"`
}

// Board is een bewaard dashboard.
type Board struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Refresh     int       `json:"refresh"`
	Panels      []Panel   `json:"panels"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   *string   `json:"updated_by"`
}

// refreshes: de keuzes voor verversen in seconden (0 = niet).
var refreshes = map[int]bool{0: true, 30: true, 60: true, 300: true, 900: true}

// ErrBoardExists: er is al een dashboard met die naam.
var ErrBoardExists = errors.New("er is al een dashboard met die naam")

// Normalize controleert een dashboard en zet ontbrekende waarden goed. Zo
// komt er alleen een geldig dashboard in de database, ook bij een import.
func (in *BoardInput) Normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	switch {
	case in.Name == "":
		return &FieldError{"geef het dashboard een naam"}
	case utf8.RuneCountInString(in.Name) > maxName:
		return &FieldError{fmt.Sprintf("de naam is te lang (hoogstens %d tekens)", maxName)}
	case utf8.RuneCountInString(in.Description) > maxDesc:
		return &FieldError{fmt.Sprintf("de beschrijving is te lang (hoogstens %d tekens)", maxDesc)}
	case !refreshes[in.Refresh]:
		return &FieldError{"verversen kan elke 30 s, 1 min, 5 min, 15 min of niet"}
	case len(in.Panels) > MaxPanels:
		return &FieldError{fmt.Sprintf("hoogstens %d panelen per dashboard", MaxPanels)}
	}
	if in.Panels == nil {
		in.Panels = []Panel{}
	}
	seen := map[string]bool{}
	for i := range in.Panels {
		p := &in.Panels[i]
		if err := p.normalize(); err != nil {
			return &FieldError{fmt.Sprintf("paneel %d: %s", i+1, err.(*FieldError).Msg)}
		}
		if p.ID == "" || seen[p.ID] {
			p.ID = newPanelID()
		}
		seen[p.ID] = true
	}
	return nil
}

func (p *Panel) normalize() error {
	p.Title = strings.TrimSpace(p.Title)
	p.ID = strings.TrimSpace(p.ID)
	if len(p.ID) > 32 {
		p.ID = ""
	}
	if utf8.RuneCountInString(p.Title) > maxTitle {
		return &FieldError{fmt.Sprintf("de titel is te lang (hoogstens %d tekens)", maxTitle)}
	}
	p.W = min(max(p.W, 1), 4)
	p.H = min(max(p.H, 1), 3)
	if p.Items == nil {
		p.Items = []BoardItem{}
	}
	for i := range p.Items {
		it := &p.Items[i]
		it.ItemID = strings.TrimSpace(it.ItemID)
		it.Label = strings.TrimSpace(it.Label)
		if _, err := strconv.ParseUint(it.ItemID, 10, 64); err != nil {
			return &FieldError{"ongeldig item"}
		}
		if utf8.RuneCountInString(it.Label) > maxTitle {
			return &FieldError{"de naam van een item is te lang"}
		}
	}
	for _, v := range []*float64{p.Warn, p.Crit, p.Max} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return &FieldError{"ongeldige grens"}
		}
	}
	// Wat niet bij de soort hoort, valt weg; zo blijft een export proper.
	keep := func(items, rng, limits, problems, text bool) {
		if !items {
			p.Items = []BoardItem{}
		}
		if !rng {
			p.Range = ""
		}
		if !limits {
			p.Warn, p.Crit, p.Max = nil, nil, nil
		}
		if !problems {
			p.MinSeverity, p.HostID = 0, ""
		}
		if !text {
			p.Text = ""
		}
	}
	switch p.Type {
	case PanelGraph:
		if len(p.Items) == 0 || len(p.Items) > MaxGraphItems {
			return &FieldError{fmt.Sprintf("een grafiek heeft 1 tot %d items", MaxGraphItems)}
		}
		if _, ok := Ranges[p.Range]; !ok {
			p.Range = "24h"
		}
		keep(true, true, false, false, false)
	case PanelStat, PanelGauge:
		if len(p.Items) != 1 {
			return &FieldError{"kies één item"}
		}
		if p.Type == PanelGauge && p.Max != nil && *p.Max <= 0 {
			return &FieldError{"het maximum van een meter moet boven 0 liggen"}
		}
		keep(true, false, true, false, false)
	case PanelProblems:
		p.MinSeverity = min(max(p.MinSeverity, 0), 5)
		p.HostID = strings.TrimSpace(p.HostID)
		if p.HostID != "" {
			if _, err := strconv.ParseUint(p.HostID, 10, 64); err != nil {
				return &FieldError{"ongeldige host"}
			}
		}
		keep(false, false, false, true, false)
	case PanelText:
		p.Text = strings.TrimSpace(p.Text)
		if utf8.RuneCountInString(p.Text) > maxText {
			return &FieldError{fmt.Sprintf("de tekst is te lang (hoogstens %d tekens)", maxText)}
		}
		keep(false, false, false, false, true)
	default:
		return &FieldError{"onbekende soort paneel"}
	}
	return nil
}

func newPanelID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const boardCols = `d.id, d.name, d.description, d.refresh, d.panels, d.created_at, d.updated_at, u.username`

func scanBoard(row pgx.Row) (Board, error) {
	var b Board
	var raw []byte
	if err := row.Scan(&b.ID, &b.Name, &b.Description, &b.Refresh, &raw, &b.CreatedAt, &b.UpdatedAt, &b.UpdatedBy); err != nil {
		return Board{}, err
	}
	if err := json.Unmarshal(raw, &b.Panels); err != nil {
		return Board{}, err
	}
	if b.Panels == nil {
		b.Panels = []Panel{}
	}
	return b, nil
}

// Boards geeft alle eigen dashboards, op naam.
func (s *Service) Boards(ctx context.Context) ([]Board, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+boardCols+` FROM zabbix_dashboards d LEFT JOIN users u ON u.id = d.updated_by ORDER BY lower(d.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Board{}
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Board geeft één eigen dashboard.
func (s *Service) Board(ctx context.Context, id uuid.UUID) (Board, error) {
	b, err := scanBoard(s.pool.QueryRow(ctx, `SELECT `+boardCols+` FROM zabbix_dashboards d LEFT JOIN users u ON u.id = d.updated_by WHERE d.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Board{}, ErrNotFound
	}
	return b, err
}

func uniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// CreateBoard bewaart een nieuw dashboard.
func (s *Service) CreateBoard(ctx context.Context, actor events.Actor, userID *uuid.UUID, in BoardInput) (Board, error) {
	if err := in.Normalize(); err != nil {
		return Board{}, err
	}
	panels, err := json.Marshal(in.Panels)
	if err != nil {
		return Board{}, err
	}
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO zabbix_dashboards (name, description, refresh, panels, updated_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, in.Name, in.Description, in.Refresh, panels, userID).Scan(&id); err != nil {
		if uniqueViolation(err) {
			return Board{}, ErrBoardExists
		}
		return Board{}, err
	}
	s.event(ctx, actor, "zabbix.dashboard_created", "zabbix_dashboard", id.String(), map[string]any{"name": in.Name, "panels": len(in.Panels)})
	return s.Board(ctx, id)
}

// UpdateBoard vervangt een dashboard.
func (s *Service) UpdateBoard(ctx context.Context, actor events.Actor, userID *uuid.UUID, id uuid.UUID, in BoardInput) (Board, error) {
	if err := in.Normalize(); err != nil {
		return Board{}, err
	}
	panels, err := json.Marshal(in.Panels)
	if err != nil {
		return Board{}, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE zabbix_dashboards SET name = $2, description = $3, refresh = $4, panels = $5,
		updated_at = now(), updated_by = $6 WHERE id = $1`, id, in.Name, in.Description, in.Refresh, panels, userID)
	if err != nil {
		if uniqueViolation(err) {
			return Board{}, ErrBoardExists
		}
		return Board{}, err
	}
	if tag.RowsAffected() == 0 {
		return Board{}, ErrNotFound
	}
	s.event(ctx, actor, "zabbix.dashboard_updated", "zabbix_dashboard", id.String(), map[string]any{"name": in.Name, "panels": len(in.Panels)})
	return s.Board(ctx, id)
}

// DeleteBoard gooit een dashboard weg.
func (s *Service) DeleteBoard(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	var name string
	if err := s.pool.QueryRow(ctx, `DELETE FROM zabbix_dashboards WHERE id = $1 RETURNING name`, id).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	s.event(ctx, actor, "zabbix.dashboard_deleted", "zabbix_dashboard", id.String(), map[string]any{"name": name})
	return nil
}
