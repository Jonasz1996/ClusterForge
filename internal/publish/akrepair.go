package publish

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/jackc/pgx/v5"
)

// Authentik aanvullen (fase 42d): een host met forward auth in NPM maar
// zonder provider, applicatie of plaats in de outpost krijgt die erbij.
// NPM blijft zoals hij is; de taak controleert daarna of de host naar
// Authentik doorstuurt, en ruimt bij een fout op wat ze maakte.

// ActAKRepair vult Authentik aan voor half ingestelde hosts (leeg Hosts = alle).
const ActAKRepair = "ak_repair"

// akRetry is hoe lang het automatisch aanvullen wacht na een poging voor
// dezelfde host.
const akRetry = 6 * time.Hour

func (s *Service) planAKRepair(ctx context.Context, st Settings, managed map[int]managedHost, a Action, p *Plan) error {
	if !st.AuthentikLinked() {
		return invalid("koppel eerst Authentik in Instellingen → Domeinen en publicatie")
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return err
	}
	outpost, err := s.outpostFor(ctx, st)
	if err != nil {
		return err
	}
	p.Title = "Authentik aanvullen"
	noSnippet, typos := 0, 0
	for _, v := range ov.Hosts {
		if v.Authentik != AKHalf || len(v.Domains) == 0 || (len(a.Hosts) > 0 && !slices.Contains(a.Hosts, v.ID)) {
			continue
		}
		if v.Typo != "" {
			typos++
			continue
		}
		h, ok := npmHostByID(ov, v.ID)
		if !ok {
			continue
		}
		name := strings.ToLower(v.Domains[0])
		adv := h
		// Testlijst 3, N15: een host met provider maar zonder forward auth in
		// NPM krijgt die er ook bij; zo werkt "afwerken" met één klik.
		if !hasSnippet(h) {
			with, err := withSnippet(h, outpost)
			if err != nil {
				noSnippet++
				p.Warnings = append(p.Warnings, name+": "+err.Error())
				continue
			}
			adv = with
		}
		label := strings.TrimSuffix(name, "."+st.Domain)
		ph := PlannedHost{HostID: v.ID, Domain: name, Scheme: "http", Protect: true, Outpost: outpost,
			AppName: appName(label), AppSlug: slugFor(label),
			Body: map[string]any{"advanced_config": adv}, Old: map[string]any{"advanced_config": h}}
		if v.CertID > 0 {
			ph.Scheme = "https"
		}
		if adv != h {
			p.Lines = append(p.Lines, fmt.Sprintf("%s: %s aanmaken in Authentik, daarna forward auth in NPM", name, strings.TrimPrefix(v.AuthentikDetail, "ontbreekt: ")))
		} else {
			p.Lines = append(p.Lines, fmt.Sprintf("%s: %s aanmaken in Authentik (NPM blijft zoals hij is)", name, strings.TrimPrefix(v.AuthentikDetail, "ontbreekt: ")))
		}
		if _, ok := managed[v.ID]; !ok {
			p.Warnings = append(p.Warnings, name+": nog niet beheerd; ClusterForge neemt hem over")
		}
		p.Hosts = append(p.Hosts, ph)
	}
	if typos > 0 {
		p.Lines = append(p.Lines, fmt.Sprintf("%d hosts met een tikfout in de naam overgeslagen: zet eerst de naam recht (Proxy hosts → Naam rechtzetten)", typos))
	}
	if noSnippet > 0 {
		p.Lines = append(p.Lines, fmt.Sprintf("%d hosts konden geen forward auth krijgen (zie de waarschuwingen)", noSnippet))
	}
	if len(p.Hosts) == 0 {
		if typos > 0 {
			return invalid("alle half ingestelde hosts hebben een tikfout in de naam: zet eerst de naam recht")
		}
		return invalid("geen half ingestelde host met forward auth om aan te vullen")
	}
	if len(p.Hosts) > 1 {
		p.Lines = append(p.Lines, fmt.Sprintf("Host voor host; mislukt er een, dan ruimt ClusterForge op wat het voor die host maakte (%d hosts)", len(p.Hosts)))
	}
	return nil
}

// npmHostByID geeft de volledige Advanced van een host uit het overzicht.
func npmHostByID(ov *Overview, id int) (string, bool) {
	for _, h := range ov.rawHosts {
		if h.ID == id {
			return h.AdvancedConfig, true
		}
	}
	return "", false
}

// autoRepairAK vult na een lezing vanzelf aan wat half ingesteld is, als
// dat aan staat. Per host hoogstens één poging per akRetry.
func (s *Service) autoRepairAK(ctx context.Context, st Settings) {
	if !st.Defaults.AuthentikAutoRepair || !st.AuthentikLinked() || !st.NPMLinked() {
		return
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return
	}
	// Nooit naast een andere taak op NPM; de volgende lezing probeert opnieuw.
	if busy, _ := s.applyBusy(ctx); busy != nil {
		return
	}
	now := s.Now()
	var hosts []int
	s.mu.Lock()
	if s.akTried == nil {
		s.akTried = map[int]time.Time{}
	}
	for _, v := range ov.Hosts {
		adv, ok := npmHostByID(ov, v.ID)
		if v.Authentik != AKHalf || v.Typo != "" || !ok || !hasSnippet(adv) {
			continue
		}
		if t, ok := s.akTried[v.ID]; ok && now.Sub(t) < akRetry {
			continue
		}
		s.akTried[v.ID] = now
		hosts = append(hosts, v.ID)
	}
	s.mu.Unlock()
	if len(hosts) == 0 {
		return
	}
	actor := events.Actor{Type: events.System().Type, ID: "authentik-aanvullen"}
	if _, _, err := s.Start(ctx, actor, Action{Action: ActAKRepair, Hosts: hosts}); err != nil {
		s.log.Info("publicatie: Authentik automatisch aanvullen", "err", err)
	}
}

// applyBusy geeft de publicatietaak die nog wacht of loopt, of nil. Zo loopt
// er nooit meer dan één taak tegelijk op NPM, ook zonder NPM-cluster.
func (s *Service) applyBusy(ctx context.Context) (*jobs.BusyError, error) {
	var b jobs.BusyError
	err := s.pool.QueryRow(ctx, `SELECT id, title FROM jobs WHERE kind = $1 AND status IN ('queued', 'running') ORDER BY created_at LIMIT 1`, KindApply).Scan(&b.JobID, &b.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Cluster = "NPM"
	return &b, nil
}
