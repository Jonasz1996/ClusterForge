package crowdsec

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ListEvery is hoe vaak de lijstronde draait.
const ListEvery = time.Minute

// ListRetryFailed is hoe lang een node na een mislukte import wacht.
const ListRetryFailed = 15 * time.Minute

// ListLoop draait de lijstronde: publieke lijsten ophalen, automatische
// regels, verlopen waarden, en de lijsten naar de nodes brengen. Apart van
// de poller, zodat een grote import de alerts niet ophoudt.
func (s *Service) ListLoop(ctx context.Context) {
	t := time.NewTicker(ListEvery)
	defer t.Stop()
	for {
		if err := s.ListTick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("blocklist-ronde mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.listKick:
		}
	}
}

// ListTick is één lijstronde.
func (s *Service) ListTick(ctx context.Context) error {
	s.listMu.Lock()
	defer s.listMu.Unlock()
	s.fetchDue(ctx)
	if err := s.autoBlock(ctx); err != nil {
		s.log.Warn("automatisch blokkeren mislukt", "err", err)
	}
	// Verlopen waarden: alleen als er iets verlopen is sinds de laatste
	// uitrekening, maar dat goedkoop nagaan is duurder dan gewoon rekenen
	// voor de eigen en automatische lijst.
	if err := s.recomputeExpiring(ctx); err != nil {
		return err
	}
	if err := s.syncNodes(ctx); err != nil {
		return err
	}
	s.notifyRound(ctx)
	return nil
}

func (s *Service) recomputeExpiring(ctx context.Context) error {
	g, err := s.guard(ctx)
	if err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT list_id FROM crowdsec_list_entries WHERE expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		l, err := s.list(ctx, tx, id)
		if err == nil {
			// Verlopen waarden gaan weg; de versie houdt ze bij.
			_, err = tx.Exec(ctx, `DELETE FROM crowdsec_list_entries WHERE list_id = $1 AND expires_at <= now()`, id)
		}
		if err == nil {
			err = s.recompute(ctx, tx, l, g)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// syncNodes brengt elke lijst naar de nodes waar hij hoort en haalt oude
// tags weg.
func (s *Service) syncNodes(ctx context.Context) error {
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	lists, err := s.allLists(ctx)
	if err != nil {
		return err
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return err
	}
	state, err := s.listNodeState(ctx)
	if err != nil {
		return err
	}
	values := map[uuid.UUID][]string{}
	now := s.Now()
	for _, n := range nodes {
		if !n.Enabled || !n.Managed || !n.Online || n.Protocol < protocol.CrowdSecListsSince || n.Status == nil || !n.Status.Installed ||
			n.Status.Active != "active" || n.ApplyRunning {
			continue
		}
		if set.ConsoleOff && !n.ConsoleOffDone {
			s.consoleOff(ctx, n)
		}
		keep := map[string]bool{}
		var drop []string
		for _, l := range lists {
			st, has := state[[2]uuid.UUID{n.NodeID, l.ID}]
			if !l.wants(n) || l.Count == 0 {
				if has {
					drop = append(drop, st.Tag)
					if _, err := s.pool.Exec(ctx, `DELETE FROM crowdsec_list_nodes WHERE node_id = $1 AND list_id = $2`, n.NodeID, l.ID); err != nil {
						return err
					}
				}
				continue
			}
			need := !has || st.gen != l.Generation || st.AppliedAt == nil || now.Sub(*st.AppliedAt) >= ListRefresh
			// Is CrowdSec leeggemaakt (of een nieuwe database), dan staat
			// de tag er niet meer: opnieuw zetten.
			if !need && n.Lists != nil && n.Lists[st.Tag] < st.Count/2 && now.Sub(*st.AppliedAt) >= 10*time.Minute {
				need = true
			}
			if need && st.FailedAt != nil && st.gen == l.Generation && now.Sub(*st.FailedAt) < ListRetryFailed {
				need = false
			}
			if !need {
				keep[st.Tag] = true
				continue
			}
			vals, ok := values[l.ID]
			if !ok {
				if vals, _, err = s.EffectiveValues(ctx, l.ID); err != nil {
					return err
				}
				values[l.ID] = vals
			}
			tag := listTag(l.Slug, l.Generation, int(now.Unix()/60))
			if tag == st.Tag {
				tag = listTag(l.Slug, l.Generation, int(now.Unix()/60)+1)
			}
			if err := s.importList(ctx, n, tag, vals); err != nil {
				s.log.Warn("blocklist zetten mislukt", "node", n.Hostname, "list", l.Slug, "err", err)
				if _, err2 := s.pool.Exec(ctx, `INSERT INTO crowdsec_list_nodes (node_id, list_id, tag, error, failed_at) VALUES ($1, $2, $3, $4, now())
					ON CONFLICT (node_id, list_id) DO UPDATE SET error = $4, failed_at = now()`, n.NodeID, l.ID, st.Tag, err.Error()); err2 != nil {
					return err2
				}
				// Een half gezette tag mag weg.
				drop = append(drop, tag)
				if st.Tag != "" {
					keep[st.Tag] = true
				}
				s.notifyKey(ctx, "list-sync:"+n.NodeID.String(), 6*time.Hour, "Blocklist niet gezet",
					fmt.Sprintf("%s op %s: %s", l.Name, n.Hostname, err), "warning")
				continue
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO crowdsec_list_nodes (node_id, list_id, tag, count, applied_at, error, failed_at)
				VALUES ($1, $2, $3, $4, now(), '', NULL) ON CONFLICT (node_id, list_id) DO UPDATE SET tag = $3, count = $4, applied_at = now(),
				error = '', failed_at = NULL`, n.NodeID, l.ID, tag, len(vals)); err != nil {
				return err
			}
			keep[tag] = true
			if st.Tag != "" {
				drop = append(drop, st.Tag)
			}
		}
		// Alles wat de node van ClusterForge heeft en niet meer hoort.
		for tag := range n.Lists {
			if protocol.ValidListTag(tag) && !keep[tag] {
				drop = append(drop, tag)
			}
		}
		drop = cleanDrop(drop, keep)
		if len(drop) > 0 {
			if _, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpListDrop, Drop: drop}); err != nil {
				s.log.Warn("oude blocklist weghalen mislukt", "node", n.Hostname, "err", err)
			} else if n.Lists != nil {
				for _, t := range drop {
					delete(n.Lists, t)
				}
			}
		}
	}
	return nil
}

func cleanDrop(drop []string, keep map[string]bool) []string {
	out := []string{}
	for _, t := range drop {
		if t != "" && !keep[t] && protocol.ValidListTag(t) && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// importList zet de waarden in stukken op de node.
func (s *Service) importList(ctx context.Context, n Node, tag string, vals []string) error {
	for i := 0; i < len(vals); i += protocol.MaxCrowdSecListChunk {
		chunk := vals[i:min(i+protocol.MaxCrowdSecListChunk, len(vals))]
		cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		res, err := s.bus.CrowdSec(cctx, n.NodeID, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpListImport,
			List: &protocol.CrowdSecListChunk{Tag: tag, Duration: ListDuration, Values: chunk}})
		cancel()
		if err == nil && res.Error != "" {
			err = fmt.Errorf("%s", res.Error)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// consoleOff zet de Console uit op één node.
func (s *Service) consoleOff(ctx context.Context, n Node) {
	if _, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpConsoleOff}); err != nil {
		s.log.Warn("Console uitzetten mislukt", "node", n.Hostname, "err", err)
		_, _ = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_error = $2, console_failed_at = now() WHERE node_id = $1`, n.NodeID,
			"Console uitzetten: "+err.Error())
		return
	}
	_, _ = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_off_done = true, console_error = '', console_failed_at = NULL,
		console_hash = '' WHERE node_id = $1`, n.NodeID)
	s.event(ctx, events.System(), &n, "crowdsec.console_disconnected", nil)
}

// DisconnectConsole koppelt ClusterForge los van de CrowdSec Console: de
// enroll-sleutel gaat weg en op elke node gaat het delen uit. Detectie,
// bans en de community-blocklist blijven lokaal werken. Met on=false mag
// er weer een sleutel komen (de nodes melden dan opnieuw aan).
func (s *Service) DisconnectConsole(ctx context.Context, actor events.Actor, userID *uuid.UUID, on bool) (Settings, error) {
	if on {
		if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET console_off = true, console_key_enc = NULL, console_key_id = '',
			console_key_hash = '', updated_at = now(), updated_by = $1`, userID); err != nil {
			return Settings{}, err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_off_done = false`); err != nil {
			return Settings{}, err
		}
	} else if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET console_off = false, updated_at = now(), updated_by = $1`, userID); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "crowdsec.console_off_changed", map[string]any{"console_off": on})
	s.kickLists()
	return s.Settings(ctx)
}
