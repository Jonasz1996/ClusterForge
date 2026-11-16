package crowdsec

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De CrowdSec Console (app.crowdsec.net): met één enroll-sleutel meldt
// ClusterForge elke beheerde node aan, zodra CrowdSec er draait. De sleutel
// staat versleuteld met de masterkey in de database en gaat alleen in het
// enroll-verzoek naar de agent: niet in een script, een taak of een event.

var consoleAAD = []byte("crowdsec:console")

// ErrNoMasterKey: zonder masterkey kan de server de sleutel niet bewaren.
var ErrNoMasterKey = &FieldError{"de server heeft geen masterkey (CF_MASTER_KEY), dus hij kan de enroll-sleutel niet bewaren"}

func (s *Service) consoleHash(key string) string {
	return secrets.Fingerprint(s.Box.Derive("crowdsec console"), []byte(key))[:16]
}

// SetConsoleKey bewaart de enroll-sleutel; leeg haalt hem weg. Met een
// nieuwe sleutel meldt de poller alle beheerde nodes opnieuw aan.
func (s *Service) SetConsoleKey(ctx context.Context, actor events.Actor, userID *uuid.UUID, key string) (Settings, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET console_key_enc = NULL, console_key_id = '', console_key_hash = '',
			updated_at = now(), updated_by = $1`, userID); err != nil {
			return Settings{}, err
		}
		s.event(ctx, actor, nil, "crowdsec.console_key_changed", map[string]any{"set": false})
		return s.Settings(ctx)
	}
	if s.Box == nil {
		return Settings{}, ErrNoMasterKey
	}
	if !protocol.ValidEnrollKey(key) {
		return Settings{}, &FieldError{"dat lijkt geen enroll-sleutel: alleen letters, cijfers, - en _ (zoals in cscli console enroll <sleutel>)"}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET console_key_enc = $1, console_key_id = $2, console_key_hash = $3,
		updated_at = now(), updated_by = $4`, s.Box.Seal([]byte(key), consoleAAD), s.Box.KeyID, s.consoleHash(key), userID); err != nil {
		return Settings{}, err
	}
	// Een nieuwe sleutel: eerdere fouten tellen niet meer.
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_error = '', console_failed_at = NULL`); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "crowdsec.console_key_changed", map[string]any{"set": true})
	return s.Settings(ctx)
}

// consoleKey ontsleutelt de sleutel; leeg als er geen is.
func (s *Service) consoleKey(set Settings) (string, error) {
	if len(set.consoleKeyEnc) == 0 {
		return "", nil
	}
	if s.Box == nil {
		return "", errors.New("de enroll-sleutel is niet te lezen zonder masterkey")
	}
	b, err := s.Box.Open(set.consoleKeyEnc, consoleAAD, set.consoleKeyID)
	if err != nil {
		return "", errors.New("de enroll-sleutel is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul hem opnieuw in")
	}
	return string(b), nil
}

// ConsoleEnroll meldt de node (of alle beheerde nodes bij nil) opnieuw aan
// bij de volgende ronde van de poller. Geeft het aantal nodes.
func (s *Service) ConsoleEnroll(ctx context.Context, actor events.Actor, id *uuid.UUID) (int, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	if !set.ConsoleKeySet {
		return 0, &FieldError{"vul eerst de enroll-sleutel van de CrowdSec Console in (CrowdSec → Instellingen)"}
	}
	q := `UPDATE crowdsec_nodes SET console_hash = '', console_error = '', console_failed_at = NULL WHERE managed`
	args := []any{}
	if id != nil {
		q += ` AND node_id = $1`
		args = append(args, *id)
	}
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	if id != nil && tag.RowsAffected() == 0 {
		return 0, &FieldError{"CrowdSec wordt op deze node niet beheerd"}
	}
	return int(tag.RowsAffected()), nil
}

// wantsConsole: de node moet (opnieuw) aangemeld worden.
func (n Node) wantsConsole(keyHash string) bool {
	return keyHash != "" && n.consoleHash != keyHash && n.Protocol >= protocol.CrowdSecEnrollSince &&
		n.Status != nil && n.Status.Installed && n.Status.Active == "active" && !n.ApplyRunning
}

// enroll meldt één node aan en bewaart de uitkomst.
func (s *Service) enroll(ctx context.Context, n Node, key, keyHash string) error {
	_, err := s.request(ctx, n, protocol.CrowdSecRequest{Op: protocol.CrowdSecOpEnroll,
		Enroll: &protocol.CrowdSecEnroll{Key: key, Name: consoleName(n.Hostname), Overwrite: true}})
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), key, "…")
		_, _ = s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_error = $2, console_failed_at = now() WHERE node_id = $1`, n.NodeID, msg)
		s.event(ctx, events.System(), &n, "crowdsec.console_enroll_failed", map[string]any{"error": msg})
		return errors.New(msg)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_nodes SET console_hash = $2, console_enrolled_at = now(), console_error = '',
		console_failed_at = NULL WHERE node_id = $1`, n.NodeID, keyHash); err != nil {
		return err
	}
	s.event(ctx, events.System(), &n, "crowdsec.console_enrolled", nil)
	return nil
}

// consoleName maakt van een hostnaam een geldige naam voor de Console.
func consoleName(h string) string {
	var b strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.TrimLeft(b.String(), "._-")
	if len(out) > 63 {
		out = out[:63]
	}
	if out == "" {
		out = "node"
	}
	return out
}
