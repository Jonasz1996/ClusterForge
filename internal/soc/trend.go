package soc

import (
	"context"
	"time"
)

// SnapshotKeep is hoe lang de momentopnames blijven.
const SnapshotKeep = 90 * 24 * time.Hour

// snapshot bewaart de score van dit uur; een latere meting in hetzelfde uur
// vervangt de vorige.
func (s *Service) snapshot(ctx context.Context, sc Score, lvl Level, now time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO soc_snapshots (hour, score, level) VALUES ($1, $2, $3)
		ON CONFLICT (hour) DO UPDATE SET score = excluded.score, level = excluded.level`,
		now.Truncate(time.Hour), max(0, min(100, sc.Score)), lvl.Level)
	return err
}

// trend vult Previous en Trend uit de momentopnames.
func (s *Service) trend(ctx context.Context, sc *Score, w Window, now time.Time) error {
	size := w.bucket()
	n := int(w.Duration() / size)
	start := now.Truncate(size).Add(-time.Duration(n-1) * size)
	rows, err := s.pool.Query(ctx, `SELECT hour, score FROM soc_snapshots WHERE hour >= $1 ORDER BY hour`, start.Add(-time.Hour))
	if err != nil {
		return err
	}
	defer rows.Close()
	var prev *int
	last := map[int]TrendPoint{}
	for rows.Next() {
		var h time.Time
		var v int32
		if err := rows.Scan(&h, &v); err != nil {
			return err
		}
		score := int(v)
		// Previous: de laatste opname van een venster geleden (op een uur na).
		if !h.After(now.Add(-w.Duration())) {
			prev = &score
		}
		if h.Before(start) {
			continue
		}
		last[int(h.Sub(start)/size)] = TrendPoint{At: start.Add(time.Duration(int(h.Sub(start)/size)) * size), Score: score}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sc.Previous = prev
	sc.Trend = []TrendPoint{}
	for i := range n {
		if p, ok := last[i]; ok {
			sc.Trend = append(sc.Trend, p)
		}
	}
	// Het huidige punt is de score van nu, ook als het uur nog niet bewaard is.
	cur := TrendPoint{At: start.Add(time.Duration(n-1) * size), Score: sc.Score}
	if len(sc.Trend) > 0 && sc.Trend[len(sc.Trend)-1].At.Equal(cur.At) {
		sc.Trend[len(sc.Trend)-1] = cur
	} else {
		sc.Trend = append(sc.Trend, cur)
	}
	return nil
}

// Run maakt elk kwartier een momentopname en ruimt oude op, tot ctx stopt.
func (s *Service) Run(ctx context.Context) {
	tick := func() {
		o, err := s.build(ctx, Window24h, s.Now())
		if err == nil {
			err = s.snapshot(ctx, o.Score, o.Level, o.GeneratedAt)
		}
		if err == nil {
			_, err = s.pool.Exec(ctx, `DELETE FROM soc_snapshots WHERE hour < $1`, s.Now().Add(-SnapshotKeep))
		}
		if err != nil && ctx.Err() == nil {
			s.log.Warn("soc: momentopname mislukt", "err", err)
		}
	}
	tick()
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
