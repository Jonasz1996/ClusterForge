package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Importsleutels (fase 43b): een pentestsysteem stuurt zelf zijn
// nmap-rapporten naar ClusterForge met een eigen sleutel (cfi_…). De
// sleutel kan alleen rapporten inlezen, niets anders.

// ImportTokenPrefix staat voor elke importsleutel.
const ImportTokenPrefix = "cfi_"

// ImportToken is een sleutel zonder het geheim.
type ImportToken struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip"`
	Imports    int        `json:"imports"`
}

func hashImportToken(t string) []byte {
	sum := sha256.Sum256([]byte(t))
	return sum[:]
}

// ImportTokens geeft de sleutels.
func (s *Service) ImportTokens(ctx context.Context) ([]ImportToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at, last_used_at, last_used_ip, imports FROM security_import_tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImportToken{}
	for rows.Next() {
		var t ImportToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.LastUsedIP, &t.Imports); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateImportToken maakt een sleutel; het geheim komt maar één keer terug.
func (s *Service) CreateImportToken(ctx context.Context, actor events.Actor, userID *uuid.UUID, name string) (ImportToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "pentest"
	}
	if len(name) > 60 {
		return ImportToken{}, "", &FieldError{"De naam is te lang (hoogstens 60 tekens)."}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return ImportToken{}, "", err
	}
	secret := ImportTokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	var t ImportToken
	if err := s.pool.QueryRow(ctx, `INSERT INTO security_import_tokens (name, token_hash, created_by) VALUES ($1, $2, $3)
		RETURNING id, name, created_at, last_used_at, last_used_ip, imports`, name, hashImportToken(secret), userID).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.LastUsedIP, &t.Imports); err != nil {
		return ImportToken{}, "", err
	}
	s.event(ctx, actor, "security_import_token", t.ID.String(), nil, "security.import_token_created", map[string]any{"name": name})
	return t, secret, nil
}

// RevokeImportToken trekt een sleutel in.
func (s *Service) RevokeImportToken(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	var name string
	err := s.pool.QueryRow(ctx, `DELETE FROM security_import_tokens WHERE id = $1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	s.event(ctx, actor, "security_import_token", id.String(), nil, "security.import_token_revoked", map[string]any{"name": name})
	return nil
}

// CheckImportToken zoekt de sleutel bij een geheim; ok=false als die niet bestaat.
// Alleen bij count telt het als een import (lezen telt niet).
func (s *Service) CheckImportToken(ctx context.Context, secret, ip string, count bool) (ImportToken, bool, error) {
	if !strings.HasPrefix(secret, ImportTokenPrefix) {
		return ImportToken{}, false, nil
	}
	var t ImportToken
	err := s.pool.QueryRow(ctx, `UPDATE security_import_tokens SET last_used_at = now(), last_used_ip = $2, imports = imports + CASE WHEN $3 THEN 1 ELSE 0 END
		WHERE token_hash = $1 RETURNING id, name, created_at, last_used_at, last_used_ip, imports`, hashImportToken(secret), ip, count).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.LastUsedIP, &t.Imports)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImportToken{}, false, nil
	}
	if err != nil {
		return ImportToken{}, false, err
	}
	return t, true, nil
}
