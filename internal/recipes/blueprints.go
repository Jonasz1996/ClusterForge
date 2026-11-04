package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Blueprint is een zelf samengesteld cluster in zijn nieuwste versie.
type Blueprint struct {
	// Template is de naam van de template: eigen-<naam>.
	Template    string
	Version     string
	Title       string
	Composition Composition
	CreatedAt   time.Time
}

// ComposeResult is wat Compose bewaarde.
type ComposeResult struct {
	Template string
	Version  string
	// Created is false als de samenstelling niet veranderde.
	Created  bool
	Warnings []string
}

// registry is het register waar samengestelde templates in komen: dat van
// de ingebouwde templates, zodat uitrol, plan en drift ze vinden.
func (s *Service) registry() *templates.Registry {
	if s.Registry != nil {
		return s.Registry
	}
	return templates.BuiltinRegistry()
}

// LoadBlueprints zet alle bewaarde samengestelde templates in het register.
// De server doet dat bij het starten, voor hij nakijkt of elk cluster zijn
// templateversie heeft.
func (s *Service) LoadBlueprints(ctx context.Context) error {
	rows, err := s.q.ListBlueprints(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		t, err := ParseComposed(r.TemplateYaml)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", r.Name, r.Version, err))
			continue
		}
		s.registry().Add(t)
	}
	return errors.Join(errs...)
}

// Blueprints geeft de samengestelde clusters, elk in de nieuwste versie.
func (s *Service) Blueprints(ctx context.Context) ([]Blueprint, error) {
	rows, err := s.q.ListBlueprints(ctx)
	if err != nil {
		return nil, err
	}
	var out []Blueprint
	for _, r := range rows {
		var c Composition
		if err := json.Unmarshal(r.Composition, &c); err != nil {
			return nil, err
		}
		b := Blueprint{Template: r.Name, Version: r.Version, Title: r.Title, Composition: c, CreatedAt: r.CreatedAt}
		if len(out) > 0 && out[len(out)-1].Template == r.Name {
			out[len(out)-1] = b
		} else {
			out = append(out, b)
		}
	}
	return out, nil
}

// Compose maakt een template van een samenstelling en bewaart haar. Een
// samenstelling die niets verandert, geeft de versie die er al is; anders
// komt er een nieuwe versie (1.0.1, 1.0.2, …).
func (s *Service) Compose(ctx context.Context, user uuid.UUID, c Composition) (ComposeResult, error) {
	recs := map[string]*Recipe{}
	for i, r := range c.Roles {
		key := RecipeKey(r.Recipe, r.RecipeVersion)
		if recs[key] != nil {
			continue
		}
		rv, err := s.Get(ctx, r.Recipe, r.RecipeVersion)
		if errors.Is(err, ErrNotFound) {
			return ComposeResult{}, composeErr(fmt.Sprintf("roles.%d.recipe", i), "recept %s %s bestaat niet", r.Recipe, r.RecipeVersion)
		}
		if err != nil {
			return ComposeResult{}, err
		}
		rec, diags := Parse(rv.Source)
		if rec == nil {
			return ComposeResult{}, composeErr(fmt.Sprintf("roles.%d.recipe", i), "%s %s heeft fouten: %v", r.Recipe, r.RecipeVersion, (&InvalidError{diags}).Error())
		}
		recs[key] = rec
	}
	name := TemplatePrefix + c.Name
	comp, err := json.Marshal(c)
	if err != nil {
		return ComposeResult{}, err
	}
	var res ComposeResult
	var added *templates.Template
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// Eén samenstelling tegelijk per naam, zodat twee nooit dezelfde
		// versie kiezen.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('blueprint:' || $1))", name); err != nil {
			return err
		}
		version := "1.0.0"
		last, err := q.LatestBlueprint(ctx, name)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			same, err := Compose(c, last.Version, recs)
			if err != nil {
				return err
			}
			if string(same.YAML) == last.TemplateYaml {
				res = ComposeResult{Template: name, Version: last.Version, Warnings: same.Warnings}
				return nil
			}
			version = nextVersion(last.Version)
		}
		out, err := Compose(c, version, recs)
		if err != nil {
			return err
		}
		if _, err := q.InsertBlueprint(ctx, store.InsertBlueprintParams{
			Name: name, Version: version, Title: out.Template.Title, Composition: comp, TemplateYaml: string(out.YAML), CreatedBy: &user,
		}); err != nil {
			return err
		}
		res = ComposeResult{Template: name, Version: version, Created: true, Warnings: out.Warnings}
		if err := s.ev.Write(ctx, q, events.Event{Actor: events.User(user), SubjectType: "template", SubjectID: name, Action: "blueprint.saved",
			Payload: map[string]any{"name": name, "version": version, "title": out.Template.Title}}); err != nil {
			return err
		}
		added = out.Template
		return nil
	})
	if err == nil && added != nil {
		s.registry().Add(added)
	}
	return res, err
}

// nextVersion verhoogt het laatste getal: 1.0.4 wordt 1.0.5.
func nextVersion(v string) string {
	i := strings.LastIndex(v, ".")
	n, _ := strconv.Atoi(v[i+1:])
	return v[:i+1] + strconv.Itoa(n+1)
}
