package zabbixmon

import (
	"context"
	"time"
)

// SnapshotEvery: zo vaak gaat een momentopname naar de database.
const SnapshotEvery = 15 * time.Minute

// SnapshotKeep: zo lang blijven de momentopnames.
const SnapshotKeep = 30 * 24 * time.Hour

// Windows zijn de vensters voor de trend.
var Windows = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// TrendPoint is één momentopname.
type TrendPoint struct {
	At        time.Time `json:"at"`
	Disaster  int       `json:"disaster"`
	High      int       `json:"high"`
	Average   int       `json:"average"`
	Warning   int       `json:"warning"`
	Info      int       `json:"info"`
	Hosts     int       `json:"hosts"`
	HostsDown int       `json:"hosts_down"`
	NVPS      *float64  `json:"nvps"`
	Queue     *int      `json:"queue"`
}

func (s *Service) save(ctx context.Context, snap *Snapshot, now time.Time) {
	slot := now.UTC().Truncate(SnapshotEvery)
	// Binnen hetzelfde kwartier overschrijft een latere ronde de vorige.
	c := snap.Counts
	var nvps *float64
	var queue *int
	for _, h := range snap.Health {
		if h.NVPS != nil && nvps == nil {
			v := *h.NVPS
			nvps = &v
		}
		if h.Queue != nil {
			q := int(*h.Queue)
			if queue != nil {
				q += *queue
			}
			queue = &q
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO zabbix_snapshots (at, disaster, high, average, warning, info, hosts, hosts_down, hosts_unknown, nvps, queue)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (at) DO UPDATE SET disaster = excluded.disaster, high = excluded.high, average = excluded.average,
			warning = excluded.warning, info = excluded.info, hosts = excluded.hosts, hosts_down = excluded.hosts_down,
			hosts_unknown = excluded.hosts_unknown, nvps = excluded.nvps, queue = excluded.queue`,
		slot, c.Disaster, c.High, c.Average, c.Warning, c.Info, c.Hosts, c.HostsDown, c.HostsUnknown, nvps, queue); err != nil {
		s.log.Warn("Zabbix-momentopname bewaren mislukt", "err", err)
		return
	}
	if now.Sub(s.lastSaved) > time.Hour {
		s.lastSaved = now
		if _, err := s.pool.Exec(ctx, `DELETE FROM zabbix_snapshots WHERE at < $1`, now.Add(-SnapshotKeep)); err != nil {
			s.log.Warn("oude Zabbix-momentopnames wissen mislukt", "err", err)
		}
	}
}

// Trend geeft de momentopnames van het venster, oudste eerst. Voor 7 en 30
// dagen één punt per uur (het zwaarste van dat uur), anders elk kwartier.
func (s *Service) Trend(ctx context.Context, window time.Duration) ([]TrendPoint, error) {
	now := s.Now().UTC()
	bucket := "at"
	if window > 2*24*time.Hour {
		bucket = "date_trunc('hour', at)"
	}
	rows, err := s.pool.Query(ctx, `SELECT `+bucket+` AS b, max(disaster), max(high), max(average), max(warning), max(info),
		max(hosts), max(hosts_down), max(nvps), max(queue)
		FROM zabbix_snapshots WHERE at >= $1 GROUP BY b ORDER BY b`, now.Add(-window))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrendPoint{}
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.At, &p.Disaster, &p.High, &p.Average, &p.Warning, &p.Info, &p.Hosts, &p.HostsDown, &p.NVPS, &p.Queue); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
