package opnsense

// Aandacht nodig (fase 34 M6): wat er op de firewalls een actie vraagt.

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notice is één melding voor Aandacht nodig.
type Notice struct {
	ID     string
	Red    bool
	Title  string
	Detail string
	Names  []string
	Href   string
	Count  int
}

// BackupStale is hoe oud de laatste back-up in ClusterForge mag zijn.
const BackupStale = 48 * time.Hour

// Notices geeft per firewallcluster de problemen (crit is rood),
// een firewall die in onderhoudsmodus blijft staan en back-ups die te oud
// zijn.
func (s *Service) Notices(ctx context.Context) ([]Notice, error) {
	cs, err := s.Clusters(ctx)
	if err != nil {
		return nil, err
	}
	var out []Notice
	for _, c := range cs {
		href := "/opnsense?fw=" + c.Key
		var crit, warn []string
		for _, p := range c.Problems {
			if p.Level == "crit" {
				crit = append(crit, p.Text)
			} else {
				warn = append(warn, p.Text)
			}
		}
		if len(crit) > 0 {
			out = append(out, Notice{ID: "opnsense-crit-" + c.Key, Red: true, Title: "Firewall " + c.Name + ": " + crit[0], Names: crit[1:], Href: href, Count: len(crit)})
		}
		if len(warn) > 0 {
			t := "Firewall " + c.Name + ": " + warn[0]
			if len(warn) > 1 {
				t = "Firewall " + c.Name + ": " + strconv.Itoa(len(warn)) + " waarschuwingen"
			}
			out = append(out, Notice{ID: "opnsense-warn-" + c.Key, Title: t, Names: warn, Href: href, Count: len(warn)})
		}
		var maint, stale []string
		for _, n := range c.Nodes {
			if n.Facts != nil && n.Facts.CARP.Maintenance {
				maint = append(maint, n.Hostname)
			}
			if s.Box == nil || n.Why != "" {
				continue
			}
			last, err := s.q.LatestOpnsenseBackup(ctx, n.NodeID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				stale = append(stale, n.Hostname+" (nog geen)")
			case err == nil && s.Now().Sub(last.TakenAt) > BackupStale:
				stale = append(stale, n.Hostname+" (laatste "+last.TakenAt.Format("02/01 15:04")+")")
			}
		}
		if len(maint) > 0 {
			out = append(out, Notice{ID: "opnsense-maint-" + c.Key, Title: "Firewall in onderhoudsmodus op " + c.Name, Names: maint, Href: "/opnsense/ha?fw=" + c.Key, Count: len(maint)})
		}
		if len(stale) > 0 {
			out = append(out, Notice{ID: "opnsense-backup-" + c.Key, Title: "Geen recente back-up van " + c.Name + " in ClusterForge", Names: stale, Href: "/opnsense/ha?fw=" + c.Key, Count: len(stale)})
		}
	}
	return out, nil
}
