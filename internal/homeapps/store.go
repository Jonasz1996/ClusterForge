package homeapps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store bewaart wat ClusterForge van de apps onthoudt.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Known leest de bewaarde apps.
func (s *Store) Known(ctx context.Context) ([]Known, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, name, grp, icon, cluster_name, first_seen, last_seen, override FROM home_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Known
	for rows.Next() {
		var k Known
		var o []byte
		if err := rows.Scan(&k.Key, &k.Name, &k.Group, &k.Icon, &k.Cluster, &k.FirstSeen, &k.LastSeen, &o); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(o, &k.Override)
		out = append(out, k)
	}
	return out, rows.Err()
}

// Decisions leest de keuzes bij twijfelgevallen.
func (s *Store) Decisions(ctx context.Context) (map[string]Decision, error) {
	rows, err := s.pool.Query(ctx, `SELECT service_key, decision, name FROM home_app_doubts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Decision{}
	for rows.Next() {
		var k string
		var d Decision
		if err := rows.Scan(&k, &d.Choice, &d.Name); err != nil {
			return nil, err
		}
		out[k] = d
	}
	return out, rows.Err()
}

// Save werkt last_seen bij van wat nu gevonden is en ruimt op wat langer
// dan GoneAfter weg is (behalve als Jonas er iets aan bijstuurde).
func (s *Store) Save(ctx context.Context, now time.Time, r Result) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, a := range r.Seen {
		if a.Gone {
			continue
		}
		cl := ""
		if a.Cluster != nil {
			cl = a.Cluster.Name
		}
		name, grp, icon := a.Name, a.Group, a.Icon
		// De automatische waarden bewaren, niet de bijgestuurde.
		if a.Override.Name != "" || a.Override.Group != "" || a.Override.Icon != "" {
			if e, ok := Lookup(a.Catalog); ok {
				name, grp, icon = e.Name, e.Group, e.Icon
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO home_apps (key, name, grp, icon, cluster_name, first_seen, last_seen)
			VALUES ($1, $2, $3, $4, $5, $6, $6)
			ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, grp = EXCLUDED.grp, icon = EXCLUDED.icon,
			cluster_name = EXCLUDED.cluster_name, last_seen = EXCLUDED.last_seen`,
			a.Key, name, grp, icon, cl, now); err != nil {
			return err
		}
	}
	if len(r.Expired) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM home_apps WHERE key = ANY($1) AND override = '{}'::jsonb`, r.Expired); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ErrNotFound: geen app met die key.
var ErrNotFound = errors.New("app niet gevonden")

// SetOverride bewaart de bijsturing van één app; een lege bijsturing zet
// alles terug op automatisch.
func (s *Store) SetOverride(ctx context.Context, key string, o Override) error {
	o.Name, o.Group, o.Icon, o.URL = strings.TrimSpace(o.Name), strings.TrimSpace(o.Group), strings.TrimSpace(o.Icon), strings.TrimSpace(o.URL)
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if o.IsZero() {
		data = []byte("{}")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE home_apps SET override = $2, updated_at = now() WHERE key = $1`, key, data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Decide bewaart een keuze bij een twijfelgeval; choice "" wist ze.
func (s *Store) Decide(ctx context.Context, serviceKey, choice, name string) error {
	if choice == "" {
		_, err := s.pool.Exec(ctx, `DELETE FROM home_app_doubts WHERE service_key = $1`, serviceKey)
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO home_app_doubts (service_key, decision, name) VALUES ($1, $2, $3)
		ON CONFLICT (service_key) DO UPDATE SET decision = EXCLUDED.decision, name = EXCLUDED.name, decided_at = now()`,
		serviceKey, choice, strings.TrimSpace(name))
	return err
}
