package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/recipes"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) ListRecipes(w http.ResponseWriter, r *http.Request) {
	list, err := s.recipes.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]gen.RecipeSummary, 0, len(list))
	for _, l := range list {
		out = append(out, gen.RecipeSummary{Name: l.Name, Versions: l.Versions, UpdatedAt: l.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ListRecipeExamples(w http.ResponseWriter, r *http.Request) {
	out := []gen.RecipeExample{}
	for _, e := range recipes.Examples() {
		out = append(out, gen.RecipeExample{Name: e.Name, Title: e.Title, Source: e.Source})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CheckRecipe(w http.ResponseWriter, r *http.Request) {
	var in gen.RecipeSource
	if !decode(w, r, &in) {
		return
	}
	if len(in.Source) > recipes.MaxSource {
		writeError(w, http.StatusBadRequest, "validation", "het recept is groter dan 256 KiB")
		return
	}
	writeJSON(w, http.StatusOK, recipeCheck(in.Source))
}

func (s *Server) SaveRecipe(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.RecipeSource
	if !decode(w, r, &in) {
		return
	}
	v, created, err := s.recipes.Save(r.Context(), p.User.ID, in.Source)
	var ie *recipes.InvalidError
	if errors.As(err, &ie) {
		recipes.SortDiags(ie.Diags)
		writeJSON(w, http.StatusBadRequest, gen.RecipeCheck{Ok: false, Diagnostics: recipeDiags(ie.Diags), Recipe: nullable.NewNullNullable[gen.RecipeParsed]()})
		return
	}
	if s.recipeError(w, r, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.recipeVersion(r, v))
}

func (s *Server) ListRecipeVersions(w http.ResponseWriter, r *http.Request, name string) {
	rows, err := s.recipes.Versions(r.Context(), name)
	if s.recipeError(w, r, err) {
		return
	}
	out := make([]gen.RecipeVersion, 0, len(rows))
	for _, v := range rows {
		out = append(out, s.recipeVersion(r, v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetRecipeVersion(w http.ResponseWriter, r *http.Request, name, version string) {
	v, err := s.recipes.Get(r.Context(), name, version)
	if s.recipeError(w, r, err) {
		return
	}
	rv := s.recipeVersion(r, v)
	writeJSON(w, http.StatusOK, gen.RecipeVersionDetail{
		Name: rv.Name, Version: rv.Version, Source: rv.Source, CreatedAt: rv.CreatedAt, CreatedBy: rv.CreatedBy,
		Check: recipeCheck(v.Source),
	})
}

func (s *Server) DeleteRecipeVersion(w http.ResponseWriter, r *http.Request, name, version string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.recipeError(w, r, s.recipes.Delete(r.Context(), events.User(p.User.ID), name, version)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) recipeError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var ce *recipes.ConflictError
	switch {
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Error())
	case errors.Is(err, recipes.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "recept niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) recipeVersion(r *http.Request, v store.RecipeVersion) gen.RecipeVersion {
	out := gen.RecipeVersion{Name: v.Name, Version: v.Version, Source: v.Source, CreatedAt: v.CreatedAt, CreatedBy: nullable.NewNullNullable[string]()}
	if v.CreatedBy != nil {
		if u, err := s.q.GetUserByID(r.Context(), *v.CreatedBy); err == nil {
			out.CreatedBy = nullable.NewNullableWithValue(u.Username)
		}
	}
	return out
}

func recipeDiags(d []recipes.Diag) []gen.RecipeDiag {
	out := make([]gen.RecipeDiag, 0, len(d))
	for _, x := range d {
		out = append(out, gen.RecipeDiag{Line: x.Line, Message: x.Message, Warning: x.Warning})
	}
	return out
}

// recipeCheck controleert een recept voor de editor.
func recipeCheck(src string) gen.RecipeCheck {
	rec, diags := recipes.Parse(src)
	recipes.SortDiags(diags)
	out := gen.RecipeCheck{Ok: rec != nil, Diagnostics: recipeDiags(diags), Recipe: nullable.NewNullNullable[gen.RecipeParsed]()}
	if rec != nil {
		out.Recipe = recipeParsed(rec)
	}
	return out
}

// recipeParsed zet het gelezen recept om naar het API-type; de vorm is
// dezelfde, de JSON-namen ook.
func recipeParsed(rec *recipes.Recipe) nullable.Nullable[gen.RecipeParsed] {
	b, err := json.Marshal(rec)
	var p gen.RecipeParsed
	if err != nil || json.Unmarshal(b, &p) != nil {
		return nullable.NewNullNullable[gen.RecipeParsed]()
	}
	return nullable.NewNullableWithValue(p)
}
