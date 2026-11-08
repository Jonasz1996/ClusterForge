package components

// De database-instructies van een recept (fase 9): DATABASE, DBUSER en
// IMPORT. Ze lopen op één datanode, die waar ClusterForge ook databases en
// gebruikers beheert (de VIP-houder als hij Synced is); Galera zet het
// resultaat zelf op de andere nodes. Elke stap is idempotent, dus een
// tweede uitrol verandert niets.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// RecipeDBStep is één database-instructie met de geheimen ingevuld.
// Precies één van Database, User of Import hoort erbij, volgens Kind.
type RecipeDBStep struct {
	// Kind is db_database, db_user of db_import.
	Kind      string
	Database  string
	Charset   string
	Collation string
	User      string
	Password  string
	// GrantOn is de database waarop User alle rechten krijgt; leeg geeft
	// geen rechten.
	GrantOn        string
	Path           string
	TrustFunctions bool
	Unless         string
}

// RecipeDBResult is wat een stap deed, voor het logboek. Het wachtwoord
// staat er nooit in.
type RecipeDBResult struct {
	Node    string
	Changed bool
	Message string
}

// RunRecipeDatabase voert de database-instructies uit op de database van
// het onderdeel id van cluster clusterID, in volgorde, en stopt bij de
// eerste fout.
func (s *Service) RunRecipeDatabase(ctx context.Context, clusterID, id uuid.UUID, steps []RecipeDBStep) ([]RecipeDBResult, error) {
	r, err := s.dbComponent(ctx, clusterID, id)
	if err != nil {
		return nil, err
	}
	t, err := s.targetOf(ctx, r)
	if err != nil {
		return nil, err
	}
	agents, err := s.agentProtocols(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if p := agents[t.node.NodeID]; p < protocol.RecipeStepsSince {
		return nil, &ConflictError{fmt.Sprintf("%s: de agent heeft protocol %d en recepten vragen %d; werk de agents bij (Nodes → Agents bijwerken)",
			t.node.Hostname, p, protocol.RecipeStepsSince)}
	}
	var hosts []string
	var out []RecipeDBResult
	for i, st := range steps {
		var req protocol.DBRequest
		msg := ""
		switch st.Kind {
		case "db_database":
			req = protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{Database: st.Database, Charset: st.Charset, Collation: st.Collation}}
			msg = "database " + st.Database + " bestaat"
		case "db_user":
			if hosts == nil {
				list, err := s.databases(ctx, t)
				if err != nil {
					return out, err
				}
				hosts = list.Hosts
				if len(hosts) == 0 && t.engine != protocol.DBPostgreSQL {
					return out, &ConflictError{"geen enkele node van het cluster heeft een adres"}
				}
			}
			req = protocol.DBRequest{Op: protocol.DBEnsure, Ensure: &protocol.DBEnsureRequest{
				Database: st.GrantOn, User: st.User, Hosts: hosts, Password: st.Password, Grant: st.GrantOn != "",
			}}
			msg = "gebruiker " + st.User + " met zijn wachtwoord"
			if st.GrantOn != "" {
				msg += " en alle rechten op " + st.GrantOn
			}
		case "db_import":
			req = protocol.DBRequest{Op: protocol.DBImport, Import: &protocol.DBImportRequest{
				Database: st.Database, Path: st.Path, TrustFunctions: st.TrustFunctions, Unless: st.Unless,
			}}
		default:
			return out, fmt.Errorf("stap %d: onbekende soort %q", i+1, st.Kind)
		}
		timeout := s.Timeout
		if req.Import != nil {
			timeout = max(timeout, protocol.DBImportTimeout)
		}
		res, err := s.dbRequestWithin(ctx, t, req, timeout)
		if err != nil {
			return out, fmt.Errorf("stap %d: %w", i+1, err)
		}
		changed := true
		if res.Import != nil {
			changed = res.Import.Imported
			msg = st.Path + " geladen in " + st.Database
			if !changed {
				msg = "niet geladen: " + res.Import.Skipped
			}
		}
		out = append(out, RecipeDBResult{Node: t.node.Hostname, Changed: changed, Message: msg})
	}
	return out, nil
}

// RecipeDBTimeout is hoelang de database-instructies van een samengesteld
// cluster na de uitrol mogen duren; een groot schema laden duurt even.
const RecipeDBTimeout = 45 * time.Minute

// recipeAfterAdd voert de database-instructies van een samengesteld cluster
// (Template.Database.Steps) uit zodra de database er is, en na het
// toepassen van een nieuwe revisie (rollout), zodat een nieuwe DATABASE of
// DBUSER er meteen bij komt. Ze zijn
// idempotent, dus ook na een tweede 'Database toevoegen' verandert er
// niets. Het resultaat staat in het logboek van het cluster.
func (s *Service) recipeAfterAdd(ctx context.Context, j store.Job) {
	if j.ClusterID == nil || j.Status != store.JobStatusSucceeded || s.dep == nil {
		return
	}
	var p jobParams
	if j.Kind == Kind {
		if err := json.Unmarshal(j.Params, &p); err != nil {
			return
		}
	}
	s.recipeDatabase(ctx, *j.ClusterID, p.ComponentID)
}

// RecipeDatabaseFor voert de database-instructies van een samengesteld
// cluster uit na een nieuwe revisie die op geen node iets verandert, zoals
// een nieuwe versie met alleen een extra DATABASE.
func (s *Service) RecipeDatabaseFor(ctx context.Context, clusterID uuid.UUID) {
	if s.dep != nil {
		s.recipeDatabase(ctx, clusterID, uuid.Nil)
	}
}

// recipeDatabase voert de instructies uit op het onderdeel compID, of op de
// database van het cluster als compID leeg is.
func (s *Service) recipeDatabase(ctx context.Context, clusterID, compID uuid.UUID) {
	d, err := s.dep.Desired(ctx, clusterID)
	if err != nil || d.Template.Database == nil || len(d.Template.Database.Steps) == 0 {
		return
	}
	tctx, err := d.Context()
	if err != nil {
		s.log.Error("database-instructies renderen mislukt", "cluster", clusterID, "err", err)
		return
	}
	steps, err := d.Template.RenderDBSteps(tctx)
	if err != nil {
		s.log.Error("database-instructies renderen mislukt", "cluster", clusterID, "err", err)
		return
	}
	run := make([]RecipeDBStep, len(steps))
	for i, st := range steps {
		run[i] = RecipeDBStep{Kind: st.Kind, Database: st.Database, Charset: st.Charset, Collation: st.Collation, User: st.User,
			Password: st.Password, GrantOn: st.GrantOn, Path: st.Path, TrustFunctions: st.TrustFunctions, Unless: st.Unless}
	}
	if compID == uuid.Nil {
		// Na een nieuwe versie toepassen: de database van het cluster, als
		// die er al is.
		rows, err := s.q.ListClusterComponents(ctx, clusterID)
		if err != nil {
			s.log.Error("onderdelen lezen mislukt", "cluster", clusterID, "err", err)
			return
		}
		for _, r := range rows {
			if r.ClusterComponent.Engine == d.Template.Database.Engine && r.ClusterComponent.AppliedRevision != nil {
				compID = r.ClusterComponent.ID
			}
		}
		if compID == uuid.Nil {
			return
		}
	}
	// Een schema laden duurt langer dan een taak mag wachten op wie hoort
	// dat ze klaar is.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RecipeDBTimeout)
		defer cancel()
		res, err := s.RunRecipeDatabase(ctx, clusterID, compID, run)
		var msgs []string
		for _, r := range res {
			msgs = append(msgs, r.Message)
		}
		ev := events.Event{Actor: events.System(), SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.recipe_database", Payload: map[string]any{"steps": msgs}}
		if err != nil {
			ev.Action = "cluster.recipe_database_failed"
			ev.Payload = map[string]any{"steps": msgs, "error": err.Error()}
			s.log.Error("database-instructies van het recept mislukt", "cluster", clusterID, "err", err)
		}
		if err := s.ev.Write(ctx, s.q, ev); err != nil {
			s.log.Error("event schrijven mislukt", "action", ev.Action, "err", err)
		}
		if s.Changed != nil {
			s.Changed()
		}
	}()
}

// WaitRecipes wacht tot de database-instructies die na een uitrol lopen,
// klaar zijn; voor tests en bij het stoppen.
func (s *Service) WaitRecipes() { s.wg.Wait() }
