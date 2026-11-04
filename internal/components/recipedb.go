package components

// De database-instructies van een recept (fase 9): DATABASE, DBUSER en
// IMPORT. Ze lopen op één datanode, die waar ClusterForge ook databases en
// gebruikers beheert (de VIP-houder als hij Synced is); Galera zet het
// resultaat zelf op de andere nodes. Elke stap is idempotent, dus een
// tweede uitrol verandert niets.

import (
	"context"
	"fmt"

	"github.com/google/uuid"

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
