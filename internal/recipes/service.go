package recipes

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// MaxSource is de grootste tekst van een recept.
const MaxSource = 256 << 10

//go:embed examples/*.cfr
var examplesFS embed.FS

// Example is een voorbeeldrecept om mee te beginnen.
type Example struct {
	Name   string
	Title  string
	Source string
}

// Examples geeft de ingebouwde voorbeelden.
func Examples() []Example {
	var out []Example
	entries, _ := fs.ReadDir(examplesFS, "examples")
	for _, e := range entries {
		b, err := examplesFS.ReadFile(path.Join("examples", e.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".cfr")
		out = append(out, Example{Name: name, Title: exampleTitles[name], Source: string(b)})
	}
	return out
}

var exampleTitles = map[string]string{"zabbix": "Zabbix (7.0, 8.0 of nieuwer) met MariaDB Galera"}

// InvalidError zegt dat een recept fouten heeft.
type InvalidError struct{ Diags []Diag }

func (e *InvalidError) Error() string {
	var msgs []string
	for _, d := range e.Diags {
		if !d.Warning {
			msgs = append(msgs, d.Error())
		}
	}
	return strings.Join(msgs, "; ")
}

// ConflictError zegt dat die versie al bestaat met een andere tekst.
type ConflictError struct{ Name, Version string }

func (e *ConflictError) Error() string {
	return "versie " + e.Version + " van " + e.Name + " bestaat al met een andere tekst; verhoog de versie op de RECIPE-regel"
}

// ErrNotFound zegt dat het recept of de versie niet bestaat.
var ErrNotFound = errors.New("recept niet gevonden")

// Service bewaart recepten.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	// Registry krijgt de samengestelde templates; nil is het register van
	// de ingebouwde templates.
	Registry *templates.Registry
}

func NewService(pool *pgxpool.Pool, ev *events.Writer) *Service {
	return &Service{pool: pool, q: store.New(pool), ev: ev}
}

// Summary is een recept met zijn versies, nieuwste eerst.
type Summary struct {
	Name      string
	Versions  []string
	UpdatedAt time.Time
}

// List geeft alle recepten.
func (s *Service) List(ctx context.Context) ([]Summary, error) {
	rows, err := s.q.ListRecipeVersions(ctx)
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].Name != r.Name {
			out = append(out, Summary{Name: r.Name, UpdatedAt: r.CreatedAt})
		}
		out[len(out)-1].Versions = append(out[len(out)-1].Versions, r.Version)
	}
	return out, nil
}

// Versions geeft alle versies van één recept, nieuwste eerst.
func (s *Service) Versions(ctx context.Context, name string) ([]store.RecipeVersion, error) {
	rows, err := s.q.ListVersionsOfRecipe(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows, nil
}

// Get geeft één versie.
func (s *Service) Get(ctx context.Context, name, version string) (store.RecipeVersion, error) {
	r, err := s.q.GetRecipeVersion(ctx, store.GetRecipeVersionParams{Name: name, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Save bewaart een recept als nieuwe versie. Naam en versie komen van de
// RECIPE-regel. Dezelfde tekst nog eens bewaren doet niets; een andere
// tekst met een bestaande versie is een ConflictError.
func (s *Service) Save(ctx context.Context, user uuid.UUID, source string) (store.RecipeVersion, bool, error) {
	if len(source) > MaxSource {
		return store.RecipeVersion{}, false, &InvalidError{[]Diag{{Line: 1, Message: "het recept is groter dan 256 KiB"}}}
	}
	rec, diags := Parse(source)
	if rec == nil {
		return store.RecipeVersion{}, false, &InvalidError{diags}
	}
	var out store.RecipeVersion
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.InsertRecipeVersion(ctx, store.InsertRecipeVersionParams{Name: rec.Name, Version: rec.Version, Source: source, CreatedBy: &user})
		if errors.Is(err, pgx.ErrNoRows) {
			old, err := q.GetRecipeVersion(ctx, store.GetRecipeVersionParams{Name: rec.Name, Version: rec.Version})
			if err != nil {
				return err
			}
			if old.Source != source {
				return &ConflictError{rec.Name, rec.Version}
			}
			out = old
			return nil
		}
		if err != nil {
			return err
		}
		out, created = r, true
		return s.ev.Write(ctx, q, events.Event{Actor: events.User(user), SubjectType: "recipe", SubjectID: rec.Name, Action: "recipe.saved",
			Payload: map[string]any{"name": rec.Name, "version": rec.Version, "steps": countSteps(rec)}})
	})
	return out, created, err
}

// Delete haalt een versie weg.
func (s *Service) Delete(ctx context.Context, actor events.Actor, name, version string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.DeleteRecipeVersion(ctx, store.DeleteRecipeVersionParams{Name: name, Version: version})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return s.ev.Write(ctx, q, events.Event{Actor: actor, SubjectType: "recipe", SubjectID: name, Action: "recipe.deleted",
			Payload: map[string]any{"name": name, "version": version}})
	})
}

func countSteps(r *Recipe) int {
	n := 0
	for _, role := range r.Roles {
		n += len(role.Steps)
	}
	return n
}

// SortDiags zet fouten voor waarschuwingen, per regel.
func SortDiags(d []Diag) {
	slices.SortStableFunc(d, func(a, b Diag) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		if a.Warning == b.Warning {
			return 0
		}
		if a.Warning {
			return 1
		}
		return -1
	})
}
