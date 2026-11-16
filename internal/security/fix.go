package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Veilige oplossingen (M6): de beheerder kiest per node regels uit
// protocol.FixableRules; de agent past ze toe met zijn eigen vaste
// inhoud. SSH-wijzigingen worden teruggedraaid tenzij bevestigd.

func init() {
	for i := range Rules {
		Rules[i].Auto = slices.Contains(protocol.FixableRules, Rules[i].ID)
	}
}

// Stand van een FixRun.
const (
	FixDone       = "klaar"
	FixFailed     = "mislukt"
	FixWaiting    = "wacht op bevestiging"
	FixConfirmed  = "bevestigd"
	FixRolledBack = "teruggedraaid"
	FixExpired    = "verlopen"
)

// FixRun is één klik op één node.
type FixRun struct {
	ID           uuid.UUID             `json:"id"`
	NodeID       uuid.UUID             `json:"node_id"`
	Hostname     string                `json:"hostname"`
	Rules        []string              `json:"rules"`
	Outcomes     []protocol.FixOutcome `json:"outcomes"`
	Error        string                `json:"error"`
	SSHPending   bool                  `json:"ssh_pending"`
	RollbackAt   *time.Time            `json:"rollback_at"`
	ConfirmedAt  *time.Time            `json:"confirmed_at"`
	RolledBackAt *time.Time            `json:"rolled_back_at"`
	CreatedBy    string                `json:"created_by"`
	CreatedAt    time.Time             `json:"created_at"`
	// Status: klaar, mislukt, wacht op bevestiging, bevestigd,
	// teruggedraaid of verlopen (de node zette SSH vanzelf terug).
	Status string `json:"status"`
}

// ErrFixUnsupported: de agent is te oud of offline.
var ErrFixUnsupported = errors.New("deze node kan nog geen oplossingen toepassen")

func (s *Service) fixNode(ctx context.Context, nodeID uuid.UUID) (Node, error) {
	list, err := s.nodes(ctx, "WHERE n.id = $1", nodeID)
	if err != nil {
		return Node{}, err
	}
	if len(list) == 0 {
		return Node{}, ErrNotFound
	}
	n := list[0]
	if !n.Online {
		return n, fmt.Errorf("%w: %s is offline", ErrFixUnsupported, n.Hostname)
	}
	if n.Protocol < protocol.SecurityFixSince {
		return n, fmt.Errorf("%w: de agent van %s is ouder dan protocol %d; werk hem eerst bij", ErrFixUnsupported, n.Hostname, protocol.SecurityFixSince)
	}
	return n, nil
}

// ApplyFixes past de gekozen regels toe op één node en controleert de
// node daarna opnieuw.
func (s *Service) ApplyFixes(ctx context.Context, actor events.Actor, userID *uuid.UUID, nodeID uuid.UUID, rules []string, rollback time.Duration) (FixRun, error) {
	if len(rules) == 0 {
		return FixRun{}, &FieldError{"Kies minstens één regel."}
	}
	for _, r := range rules {
		if !slices.Contains(protocol.FixableRules, r) {
			return FixRun{}, &FieldError{fmt.Sprintf("Regel %s lost ClusterForge niet zelf op.", r)}
		}
	}
	if rollback == 0 {
		rollback = protocol.FixRollbackDefault
	}
	if rollback < protocol.FixRollbackMin || rollback > protocol.FixRollbackMax {
		return FixRun{}, &FieldError{"De terugdraaitimer moet tussen 2 en 60 minuten liggen."}
	}
	n, err := s.fixNode(ctx, nodeID)
	if err != nil {
		return FixRun{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, protocol.SecurityRequestTimeout)
	defer cancel()
	res, err := s.bus.Security(rctx, n.NodeID, protocol.SecurityRequest{Op: protocol.SecurityOpFix,
		Fix: &protocol.FixSpec{Rules: rules, RollbackAfter: int(rollback / time.Second)}})
	errText := ""
	if err != nil {
		errText = err.Error()
	} else if res.Error != "" {
		errText = res.Error
	}
	fr := &protocol.FixResult{Outcomes: []protocol.FixOutcome{}}
	if res.Fix != nil {
		fr = res.Fix
	}
	outcomes, _ := json.Marshal(fr.Outcomes)
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO security_fix_runs (node_id, rules, outcomes, error, ssh_pending, rollback_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, n.NodeID, rules, outcomes, errText, fr.SSHPending, fr.RollbackAt, userID).Scan(&id); err != nil {
		return FixRun{}, err
	}
	ok := 0
	for _, o := range fr.Outcomes {
		if o.OK {
			ok++
		}
	}
	s.event(ctx, actor, "node", n.NodeID.String(), n.ClusterID, "security.fix_applied", map[string]any{"hostname": n.Hostname, "rules": rules,
		"ok": ok, "error": errText, "ssh_pending": fr.SSHPending})
	// Opnieuw controleren zodat de bevindingen kloppen; een fout hier
	// verandert niets aan wat er gebeurde.
	if errText == "" {
		if err := s.harden(ctx, n); err != nil {
			s.log.Warn("verharding na oplossen nalezen mislukt", "node", n.Hostname, "err", err)
		}
	}
	return s.FixRun(ctx, id)
}

const fixRunsSQL = `
SELECT r.id, r.node_id, n.hostname, r.rules, r.outcomes, r.error, r.ssh_pending, r.rollback_at, r.confirmed_at, r.rolled_back_at,
       coalesce(u.username, ''), r.created_at
FROM security_fix_runs r
JOIN nodes n ON n.id = r.node_id
LEFT JOIN users u ON u.id = r.created_by
`

func (s *Service) fixRuns(ctx context.Context, where string, args ...any) ([]FixRun, error) {
	rows, err := s.pool.Query(ctx, fixRunsSQL+where+` ORDER BY r.created_at DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	out := []FixRun{}
	for rows.Next() {
		var f FixRun
		var outcomes []byte
		if err := rows.Scan(&f.ID, &f.NodeID, &f.Hostname, &f.Rules, &outcomes, &f.Error, &f.SSHPending, &f.RollbackAt, &f.ConfirmedAt,
			&f.RolledBackAt, &f.CreatedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.Outcomes = []protocol.FixOutcome{}
		_ = json.Unmarshal(outcomes, &f.Outcomes)
		f.Status = fixStatus(f, now)
		out = append(out, f)
	}
	return out, rows.Err()
}

func fixStatus(f FixRun, now time.Time) string {
	switch {
	case f.RolledBackAt != nil:
		return FixRolledBack
	case f.ConfirmedAt != nil:
		return FixConfirmed
	case f.SSHPending && f.RollbackAt != nil && now.After(*f.RollbackAt):
		return FixExpired
	case f.SSHPending:
		return FixWaiting
	case f.Error != "":
		return FixFailed
	}
	for _, o := range f.Outcomes {
		if !o.OK {
			return FixFailed
		}
	}
	return FixDone
}

// FixRuns zijn de laatste 50 kliks.
func (s *Service) FixRuns(ctx context.Context) ([]FixRun, error) {
	return s.fixRuns(ctx, "")
}

// FixRun is één klik.
func (s *Service) FixRun(ctx context.Context, id uuid.UUID) (FixRun, error) {
	list, err := s.fixRuns(ctx, "WHERE r.id = $1", id)
	if err != nil {
		return FixRun{}, err
	}
	if len(list) == 0 {
		return FixRun{}, ErrNotFound
	}
	return list[0], nil
}

// ErrFixNotWaiting: deze klik wacht niet (meer) op bevestiging.
var ErrFixNotWaiting = errors.New("deze wijziging wacht niet meer op bevestiging")

// FinishFix bevestigt (keep) of draait een SSH-wijziging meteen terug.
func (s *Service) FinishFix(ctx context.Context, actor events.Actor, id uuid.UUID, keep bool) (FixRun, error) {
	f, err := s.FixRun(ctx, id)
	if err != nil {
		return f, err
	}
	if f.Status != FixWaiting {
		return f, ErrFixNotWaiting
	}
	n, err := s.fixNode(ctx, f.NodeID)
	if err != nil {
		return f, err
	}
	op, col, action := protocol.SecurityOpFixRollback, "rolled_back_at", "security.fix_rolled_back"
	if keep {
		op, col, action = protocol.SecurityOpFixConfirm, "confirmed_at", "security.fix_confirmed"
	}
	rctx, cancel := context.WithTimeout(ctx, protocol.SecurityRequestTimeout)
	defer cancel()
	res, err := s.bus.Security(rctx, n.NodeID, protocol.SecurityRequest{Op: op})
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	if err != nil {
		// De timer liep al af: dan is het teruggedraaid.
		_, _ = s.pool.Exec(ctx, `UPDATE security_fix_runs SET rolled_back_at = now() WHERE id = $1 AND rollback_at < now()`, id)
		return f, &FieldError{err.Error()}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE security_fix_runs SET `+col+` = now() WHERE id = $1`, id); err != nil {
		return f, err
	}
	s.event(ctx, actor, "node", n.NodeID.String(), n.ClusterID, action, map[string]any{"hostname": n.Hostname, "rules": f.Rules})
	if !keep {
		if err := s.harden(ctx, n); err != nil {
			s.log.Warn("verharding na terugdraaien nalezen mislukt", "node", n.Hostname, "err", err)
		}
	}
	return s.FixRun(ctx, id)
}
