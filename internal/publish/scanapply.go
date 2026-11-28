package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De stappen van fase 49 die de gewone acties niet al kennen: verhuizen,
// open paden, uitschakelen met een kopie (NPM en Authentik), terugzetten,
// definitief verwijderen en de terugval weghalen.

// akStep is een stap in Authentik zonder NPM-host.
type akStep struct {
	// Op: disable (uit de outposts, met kopie), restore of purge.
	Op         string `json:"op"`
	Slug       string `json:"slug,omitempty"`
	ProviderPK int    `json:"provider_pk,omitempty"`
	// Outposts waar de provider in zat (om terug te zetten).
	Outposts []string `json:"outposts,omitempty"`
}

// akCopy is de kopie van een Authentik-app en haar provider.
type akCopy struct {
	Application *AKApplication   `json:"application,omitempty"`
	Provider    *AKProxyProvider `json:"provider,omitempty"`
	Outposts    []string         `json:"outposts"`
}

type disabledRow struct {
	ID         uuid.UUID
	Kind       string
	Domain     string
	HostID     int
	AKSlug     string
	AKProvider int
	Copy       json.RawMessage
}

func (s *Service) disabledRow(ctx context.Context, id string) (disabledRow, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return disabledRow{}, ErrNotFound
	}
	var d disabledRow
	err = s.pool.QueryRow(ctx, `SELECT id, kind, domain, coalesce(npm_host_id, 0), ak_slug, coalesce(ak_provider, 0), copy
		FROM publish_disabled WHERE id = $1 AND done IS NULL`, uid).Scan(&d.ID, &d.Kind, &d.Domain, &d.HostID, &d.AKSlug, &d.AKProvider, &d.Copy)
	if errors.Is(err, pgx.ErrNoRows) {
		return disabledRow{}, ErrNotFound
	}
	return d, err
}

// planScanStep plant de acties van fase 49. ak zegt of Authentik nodig is.
func (s *Service) planScanStep(ctx context.Context, st Settings, npm *NPMRead, byID map[int]NPMHost, managed map[int]managedHost, a Action, p *Plan) (bool, error) {
	host := func() (NPMHost, string, error) {
		h, ok := byID[a.HostID]
		if !ok || len(h.DomainNames) == 0 {
			return NPMHost{}, "", ErrNotFound
		}
		return h, strings.ToLower(h.DomainNames[0]), nil
	}
	switch a.Action {
	case ActMove:
		h, name, err := host()
		if err != nil {
			return false, err
		}
		ov, err := s.Overview(ctx)
		if err != nil {
			return false, err
		}
		var svc *ServiceRow
		for i := range ov.Services {
			if ov.Services[i].Key == a.ServiceKey {
				svc = &ov.Services[i]
			}
		}
		if svc == nil {
			return false, invalid("die dienst draait niet meer")
		}
		scheme := svc.Kind
		if scheme != "https" {
			scheme = "http"
		}
		if strings.EqualFold(h.ForwardHost, svc.Target) && h.ForwardPort == svc.Port {
			return false, invalid("%s wijst al naar %s:%d", name, svc.Target, svc.Port)
		}
		owners, err := s.owners(ctx)
		if err != nil {
			return false, err
		}
		from := ""
		if o, ok := owners[name]; ok {
			from = o.Label()
		}
		for _, v := range ov.Hosts {
			if v.ID == h.ID && from == "" {
				from = v.ClusterName
			}
		}
		fb := &Fallback{Scheme: h.ForwardScheme, Host: h.ForwardHost, Port: h.ForwardPort, Cluster: from}
		paths, _ := s.extrasFor(ctx, name)
		have := h.fields()
		want := map[string]any{"forward_scheme": scheme, "forward_host": svc.Target, "forward_port": svc.Port,
			"advanced_config": decorate(h.AdvancedConfig, paths, fb)}
		body := diffFields(have, want)
		old := map[string]any{}
		for k := range body {
			old[k] = have[k]
		}
		owner := ownerOf(*svc)
		owner.Fallback = fb
		ph := PlannedHost{HostID: h.ID, Domain: name, Body: body, Old: old, Scheme: schemeOf(h), AllowOtherApp: true,
			ClusterID: svc.ClusterID, ServicePort: svc.Port, Fallback: fb, OpenPaths: paths, Owner: owner}
		if len(svc.Nodes) == 1 && !svc.TargetVIP {
			ph.NodeID = &svc.Nodes[0].ID
		}
		p.Title = "Verhuizen: " + name
		to := svc.ClusterName
		if to == "" {
			to = svc.Name
		}
		p.Lines = append(p.Lines,
			fmt.Sprintf("NPM: %s → %s://%s:%d (%s)", name, scheme, svc.Target, svc.Port, to),
			fmt.Sprintf("Terugval: geeft het nieuwe doel geen antwoord (502/504), dan het oude %s (%s), tot je de terugval weghaalt", fb.URL(), firstLine(from, "vorige eigenaar")),
			"Eigenaar van "+name+" wordt "+to)
		if _, ok := managed[h.ID]; !ok {
			p.Warnings = append(p.Warnings, name+": nog niet beheerd; ClusterForge neemt hem over na de wijziging")
		}
		p.Hosts = append(p.Hosts, ph)
	case ActOpenPaths:
		h, name, err := host()
		if err != nil {
			return false, err
		}
		paths, fb := s.extrasFor(ctx, name)
		if len(paths) == 0 {
			return false, invalid("geen uitzondering met paden voor %s", name)
		}
		if !hasSnippet(h.AdvancedConfig) {
			return false, invalid("%s heeft geen forward auth; zet hem eerst achter Authentik", name)
		}
		adv := decorate(h.AdvancedConfig, paths, fb)
		if adv == h.AdvancedConfig {
			return false, invalid("de paden staan al open")
		}
		ph := PlannedHost{HostID: h.ID, Domain: name, Scheme: schemeOf(h), OpenPaths: paths,
			Body: map[string]any{"advanced_config": adv}, Old: map[string]any{"advanced_config": h.AdvancedConfig}}
		p.Title = "Paden zonder Authentik: " + name
		p.Lines = append(p.Lines, fmt.Sprintf("NPM: in Advanced %s zonder Authentik, de rest blijft achter Authentik", strings.Join(paths, ", ")))
		p.Hosts = append(p.Hosts, ph)
	case ActDropFallback:
		h, name, err := host()
		if err != nil {
			return false, err
		}
		if !hasFallback(h.AdvancedConfig) {
			return false, invalid("%s heeft geen terugval in NPM", name)
		}
		paths, _ := s.extrasFor(ctx, name)
		adv := decorate(h.AdvancedConfig, paths, nil)
		ph := PlannedHost{HostID: h.ID, Domain: name, Scheme: schemeOf(h), OpenPaths: paths,
			Body: map[string]any{"advanced_config": adv}, Old: map[string]any{"advanced_config": h.AdvancedConfig},
			Owner: &OwnerSet{KeepOwner: true, ClearFallback: true}}
		p.Title = "Terugval weghalen: " + name
		p.Lines = append(p.Lines, "NPM: de terugval naar het vorige doel uit Advanced; daarna gaat alles naar het nieuwe doel")
		p.Hosts = append(p.Hosts, ph)
	case ActDisable:
		h, name, err := host()
		if err != nil {
			return false, err
		}
		if !h.IsEnabled() {
			return false, invalid("%s staat al uit", name)
		}
		// Scan 28/11: onbereikbaar in de scan is geen bewijs. Antwoordt het
		// doel nu wel vanaf de NPM-node, dan niet uitschakelen.
		if why := s.targetAnswers(ctx, st, h); why != "" {
			return false, invalid("%s niet uitgeschakeld: %s", name, why)
		}
		p.Title = "Uitschakelen: " + name
		p.Lines = append(p.Lines, fmt.Sprintf("NPM: %s uitschakelen (wijst naar %s:%d); een kopie van de host blijft in ClusterForge onder Uitgeschakeld (terugzetten of definitief verwijderen)", name, h.ForwardHost, h.ForwardPort))
		if m, ok := managed[h.ID]; ok && m.AKCreated {
			p.Lines = append(p.Lines, "Authentik: de app van ClusterForge blijft staan tot je de host definitief verwijdert")
		}
		p.Hosts = append(p.Hosts, PlannedHost{HostID: h.ID, Domain: name, Disable: true, Old: h.fields(), Scheme: schemeOf(h)})
	case ActAKDisable:
		if !st.AuthentikLinked() {
			return false, invalid("koppel eerst Authentik in Instellingen → Domeinen en publicatie")
		}
		var ak AKRead
		if _, _, err := s.read(ctx, "authentik", &ak); err != nil {
			return false, err
		}
		step := &akStep{Op: "disable", Slug: a.AKSlug, ProviderPK: a.AKProvider}
		label := a.AKSlug
		for _, app := range ak.Applications {
			if a.AKSlug != "" && app.Slug == a.AKSlug {
				label = app.Name
				if app.Provider != nil {
					step.ProviderPK = *app.Provider
				}
			}
		}
		if step.ProviderPK == 0 {
			return false, invalid("geen provider gevonden voor %s", label)
		}
		domain := ""
		for _, pr := range ak.Providers {
			if pr.PK == step.ProviderPK {
				domain = pr.Host()
				if label == "" {
					label = pr.Name
				}
			}
		}
		for _, h := range npm.Hosts {
			for _, d := range h.DomainNames {
				if domain != "" && strings.EqualFold(d, domain) {
					return false, invalid("%s heeft (weer) een host in NPM", domain)
				}
			}
		}
		var in []string
		for _, o := range ak.Outposts {
			if containsInt(o.Providers, step.ProviderPK) {
				in = append(in, o.Name)
			}
		}
		p.Title = "Authentik uitschakelen: " + label
		if len(in) > 0 {
			p.Lines = append(p.Lines, fmt.Sprintf("Authentik: provider %d uit de outpost %s; app en provider blijven bestaan", step.ProviderPK, strings.Join(in, ", ")))
		} else {
			p.Lines = append(p.Lines, fmt.Sprintf("Authentik: provider %d zit in geen outpost; alleen de kopie", step.ProviderPK))
		}
		p.Lines = append(p.Lines, "Kopie van app en provider in ClusterForge onder Uitgeschakeld (terugzetten of definitief verwijderen)")
		if domain == "" {
			domain = label
		}
		p.Hosts = append(p.Hosts, PlannedHost{Domain: domain, AK: step})
		return true, nil
	case ActRestoreDisabled, ActPurgeDisabled:
		d, err := s.disabledRow(ctx, a.DisabledID)
		if err != nil {
			return false, invalid("dat staat niet (meer) bij Uitgeschakeld")
		}
		restore := a.Action == ActRestoreDisabled
		switch d.Kind {
		case "npm":
			h, ok := byID[d.HostID]
			if !ok {
				return false, invalid("host %d (%s) bestaat niet meer in NPM; de kopie staat nog in ClusterForge", d.HostID, d.Domain)
			}
			if restore {
				p.Title = "Terugzetten: " + d.Domain
				p.Lines = append(p.Lines, "NPM: "+d.Domain+" weer aanzetten")
				p.Hosts = append(p.Hosts, PlannedHost{HostID: h.ID, Domain: d.Domain, Enable: true, DisabledID: d.ID.String(), Scheme: schemeOf(h)})
			} else {
				p.Title, p.Confirm = "Definitief verwijderen: "+d.Domain, d.Domain
				p.Lines = append(p.Lines, "NPM: "+d.Domain+" verwijderen; de kopie blijft in ClusterForge en op de NPM-node")
				p.Hosts = append(p.Hosts, PlannedHost{HostID: h.ID, Domain: d.Domain, Delete: true, DisabledID: d.ID.String(), Old: h.fields(), Scheme: schemeOf(h)})
			}
		case "authentik":
			if !st.AuthentikLinked() {
				return false, invalid("koppel eerst Authentik in Instellingen → Domeinen en publicatie")
			}
			var c akCopy
			_ = json.Unmarshal(d.Copy, &c)
			step := &akStep{Slug: d.AKSlug, ProviderPK: d.AKProvider, Outposts: c.Outposts}
			if restore {
				step.Op = "restore"
				p.Title = "Terugzetten in Authentik: " + d.Domain
				p.Lines = append(p.Lines, fmt.Sprintf("Authentik: provider %d terug in %d outpost(s)", d.AKProvider, len(c.Outposts)))
			} else {
				step.Op = "purge"
				p.Title, p.Confirm = "Definitief verwijderen in Authentik: "+d.Domain, firstLine(d.AKSlug, d.Domain)
				if d.AKSlug != "" {
					p.Lines = append(p.Lines, "Authentik: applicatie "+d.AKSlug+" verwijderen")
				}
				p.Lines = append(p.Lines, fmt.Sprintf("Authentik: proxy provider %d verwijderen; de kopie blijft in ClusterForge", d.AKProvider))
			}
			p.Hosts = append(p.Hosts, PlannedHost{Domain: d.Domain, AK: step, DisabledID: d.ID.String()})
			return true, nil
		}
	}
	return false, nil
}

// applyScanStep voert de stappen van fase 49 uit die niet langs de gewone
// weg van applyHost gaan. done zegt of het een stap van fase 49 was.
func (s *Service) applyScanStep(ctx context.Context, j *jobs.Job, st Settings, node nodeRow, snapshot string, ph *PlannedHost) (bool, error) {
	switch {
	case ph.AK != nil:
		return true, s.applyAK(ctx, j, st, ph)
	case ph.Disable:
		return true, s.applyDisable(ctx, j, st, node, snapshot, ph)
	case ph.Enable:
		return true, s.applyEnable(ctx, j, st, node, snapshot, ph)
	}
	return false, nil
}

func (s *Service) npmToggle(ctx context.Context, st Settings, node nodeRow, id int, on bool) error {
	op := "disable"
	if on {
		op = "enable"
	}
	status, data, err := s.npmCall(ctx, st, node, http.MethodPost, fmt.Sprintf("/nginx/proxy-hosts/%d/%s", id, op), nil)
	if err != nil {
		return err
	}
	// NPM geeft 400 als de host al zo staat.
	already := status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(data)), "already")
	if status >= 300 && !already {
		return errNPM{fmt.Sprintf("NPM weigert %s van host %d: %s", op, id, npmMessage(status, data))}
	}
	return nil
}

func (s *Service) applyDisable(ctx context.Context, j *jobs.Job, st Settings, node nodeRow, snapshot string, ph *PlannedHost) error {
	var rowID uuid.UUID
	err := j.Step(ctx, "Uitschakelen: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		s.lookBefore(ctx, step, node, ph)
		h, err := s.npmHost(ctx, st, node, ph.HostID)
		if err != nil {
			return err
		}
		if h == nil {
			return fmt.Errorf("host %d bestaat niet in NPM", ph.HostID)
		}
		cp, _ := json.Marshal(h)
		jobID := j.ID
		if err := s.pool.QueryRow(ctx, `INSERT INTO publish_disabled (kind, domain, npm_host_id, copy, job_id, disabled_by)
			VALUES ('npm', $1, $2, $3, $4, $5) RETURNING id`, ph.Domain, ph.HostID, cp, jobID, j.RequestedBy).Scan(&rowID); err != nil {
			return err
		}
		step.Logf("Kopie van host %d in ClusterForge bewaard", ph.HostID)
		if err := s.npmToggle(ctx, st, node, ph.HostID, false); err != nil {
			_, _ = s.pool.Exec(ctx, `DELETE FROM publish_disabled WHERE id = $1`, rowID)
			return err
		}
		step.Logf("Host %d (%s) uitgeschakeld", ph.HostID, ph.Domain)
		return nil
	})
	if err != nil {
		return err
	}
	checkErr := j.Step(ctx, "Controle: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		time.Sleep(s.settle())
		var failed []string
		res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{}})
		if err != nil {
			return err
		}
		for _, c := range res.NPM.Checks {
			mark := "✓"
			if !c.OK {
				mark = "✗"
				failed = append(failed, c.Name+": "+c.Detail)
			}
			step.Logf("%s %s: %s", mark, c.Name, c.Detail)
		}
		h, err := s.npmHost(ctx, st, node, ph.HostID)
		switch {
		case err != nil:
			failed = append(failed, "host teruglezen: "+err.Error())
		case h == nil:
			failed = append(failed, fmt.Sprintf("host %d is weg in plaats van uitgeschakeld", ph.HostID))
		case h.IsEnabled():
			failed = append(failed, fmt.Sprintf("host %d staat nog aan", ph.HostID))
		default:
			step.Logf("✓ API: host %d staat uit", ph.HostID)
		}
		// Een echte aanvraag: de naam mag de app van vooraf niet meer tonen.
		u := beforeURL(ph)
		cr, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: u, Connect: "127.0.0.1"}})
		switch {
		case err != nil || cr.Check == nil:
			step.Logf("· %s niet te bekijken: %v", u, err)
		case cr.Check.Error != "":
			step.Logf("✓ %s: geen antwoord meer (%s)", u, trimText(cr.Check.Error, 100))
		case ph.Before != nil && ph.Before.Title != "" && normTitle(cr.Check.Title) == normTitle(ph.Before.Title) && cr.Check.Status < 400:
			failed = append(failed, fmt.Sprintf("%s toont nog %q", u, cr.Check.Title))
		default:
			step.Logf("✓ %s: HTTP %d (standaardpagina van NPM)", u, cr.Check.Status)
		}
		if len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	})
	if checkErr != nil {
		_ = j.Step(ctx, "Terugdraaien: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
			_, _ = s.pool.Exec(ctx, `DELETE FROM publish_disabled WHERE id = $1`, rowID)
			if err := s.npmToggle(ctx, st, node, ph.HostID, true); err == nil {
				time.Sleep(s.settle())
				if res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMCheck, NPM: &protocol.WebNPM{}}); err == nil && res.NPM.AllOK() {
					step.Logf("✓ host %d weer aan, nginx -t en database in orde", ph.HostID)
					return nil
				}
			}
			step.Logf("Aanzetten via de API volstaat niet; de kopie %s gaat terug", snapshot)
			return s.restoreSnapshot(ctx, step, node, snapshot)
		})
		return fmt.Errorf("%s: controle mislukt, teruggedraaid: %w", ph.Domain, checkErr)
	}
	s.event(ctx, events.System(), "publish.disabled", map[string]any{"domain": ph.Domain, "host_id": ph.HostID, "job_id": j.ID.String()})
	return nil
}

func (s *Service) restoreSnapshot(ctx context.Context, step *jobs.Step, node nodeRow, snapshot string) error {
	if snapshot == "" {
		return errors.New("geen kopie om terug te zetten")
	}
	res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpNPMRestore, NPM: &protocol.WebNPM{Snapshot: snapshot}})
	if err != nil {
		return err
	}
	for _, c := range res.NPM.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		step.Logf("%s %s: %s", mark, c.Name, c.Detail)
	}
	if !res.NPM.AllOK() {
		return errors.New("NPM antwoordt niet na het terugzetten van de kopie")
	}
	return nil
}

func (s *Service) applyEnable(ctx context.Context, j *jobs.Job, st Settings, node nodeRow, snapshot string, ph *PlannedHost) error {
	if err := j.Step(ctx, "Aanzetten: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		if err := s.npmToggle(ctx, st, node, ph.HostID, true); err != nil {
			return err
		}
		step.Logf("Host %d (%s) weer aan", ph.HostID, ph.Domain)
		return nil
	}); err != nil {
		return err
	}
	checkErr := j.Step(ctx, "Controle: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		return s.verify(ctx, step, st, node, ph.HostID, ph, false)
	})
	if checkErr != nil {
		_ = j.Step(ctx, "Terugdraaien: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
			if err := s.npmToggle(ctx, st, node, ph.HostID, false); err != nil {
				return s.restoreSnapshot(ctx, step, node, snapshot)
			}
			step.Logf("Host %d weer uit", ph.HostID)
			return nil
		})
		return fmt.Errorf("%s: controle mislukt, weer uitgeschakeld: %w", ph.Domain, checkErr)
	}
	s.markDisabled(ctx, ph.DisabledID, "restored")
	return nil
}

func (s *Service) markDisabled(ctx context.Context, id, done string) {
	if id == "" {
		return
	}
	if uid, err := uuid.Parse(id); err == nil {
		if _, err := s.pool.Exec(ctx, `UPDATE publish_disabled SET done = $2, done_at = now() WHERE id = $1`, uid, done); err != nil {
			s.log.Warn("publicatie: uitgeschakeld bijwerken", "id", id, "err", err)
		}
	}
}

// setOutposts zet de providers van een outpost.
func (s *Service) setOutposts(ctx context.Context, st Settings, node nodeRow, pk string, providers []int) error {
	if providers == nil {
		providers = []int{}
	}
	status, data, err := s.akCall(ctx, st, node, http.MethodPatch, "/outposts/instances/"+pk+"/", map[string]any{"providers": providers})
	if err == nil && status >= 300 {
		err = fmt.Errorf("outpost %s: %s", pk, akMessage(status, data))
	}
	return err
}

func (s *Service) applyAK(ctx context.Context, j *jobs.Job, st Settings, ph *PlannedHost) error {
	a := ph.AK
	node, err := s.akNode(ctx, st)
	if err != nil {
		return err
	}
	title := map[string]string{"disable": "Authentik uitschakelen: ", "restore": "Authentik terugzetten: ", "purge": "Authentik definitief weg: "}[a.Op] + ph.Domain
	var rowID uuid.UUID
	var changed map[string][]int
	err = j.Step(ctx, title, func(ctx context.Context, step *jobs.Step) error {
		r, err := s.readAuthentik(ctx, st)
		if err != nil {
			return err
		}
		switch a.Op {
		case "disable":
			c := akCopy{Outposts: []string{}}
			for i, app := range r.Applications {
				if a.Slug != "" && app.Slug == a.Slug {
					c.Application = &r.Applications[i]
				}
			}
			for i, p := range r.Providers {
				if p.PK == a.ProviderPK {
					c.Provider = &r.Providers[i]
				}
			}
			if c.Provider == nil {
				return fmt.Errorf("provider %d bestaat niet (meer) in Authentik", a.ProviderPK)
			}
			changed = map[string][]int{}
			for _, o := range r.Outposts {
				if containsInt(o.Providers, a.ProviderPK) {
					c.Outposts = append(c.Outposts, o.PK)
					changed[o.PK] = o.Providers
				}
			}
			cp, _ := json.Marshal(c)
			var prov *int
			if a.ProviderPK > 0 {
				prov = &a.ProviderPK
			}
			if err := s.pool.QueryRow(ctx, `INSERT INTO publish_disabled (kind, domain, ak_slug, ak_provider, copy, job_id, disabled_by)
				VALUES ('authentik', $1, $2, $3, $4, $5, $6) RETURNING id`, ph.Domain, a.Slug, prov, cp, j.ID, j.RequestedBy).Scan(&rowID); err != nil {
				return err
			}
			step.Logf("Kopie van app %q en provider %d in ClusterForge bewaard", a.Slug, a.ProviderPK)
			for pk, provs := range changed {
				var rest []int
				for _, x := range provs {
					if x != a.ProviderPK {
						rest = append(rest, x)
					}
				}
				if err := s.setOutposts(ctx, st, node, pk, rest); err != nil {
					return err
				}
				step.Logf("Provider %d uit outpost %s", a.ProviderPK, pk)
			}
		case "restore":
			changed = map[string][]int{}
			for _, o := range r.Outposts {
				if containsStr(a.Outposts, o.PK) && !containsInt(o.Providers, a.ProviderPK) {
					changed[o.PK] = o.Providers
					if err := s.setOutposts(ctx, st, node, o.PK, append(append([]int{}, o.Providers...), a.ProviderPK)); err != nil {
						return err
					}
					step.Logf("Provider %d terug in outpost %s", a.ProviderPK, o.Name)
				}
			}
		case "purge":
			if a.Slug != "" {
				status, data, err := s.akCall(ctx, st, node, http.MethodDelete, "/core/applications/"+url.PathEscape(a.Slug)+"/", nil)
				if err == nil && status >= 300 && status != http.StatusNotFound {
					err = fmt.Errorf("applicatie verwijderen: %s", akMessage(status, data))
				}
				if err != nil {
					return err
				}
				step.Logf("Applicatie %s verwijderd", a.Slug)
			}
			if a.ProviderPK > 0 {
				status, data, err := s.akCall(ctx, st, node, http.MethodDelete, fmt.Sprintf("/providers/proxy/%d/", a.ProviderPK), nil)
				if err == nil && status >= 300 && status != http.StatusNotFound {
					err = fmt.Errorf("provider verwijderen: %s", akMessage(status, data))
				}
				if err != nil {
					return err
				}
				step.Logf("Proxy provider %d verwijderd", a.ProviderPK)
			}
		}
		return nil
	})
	undo := func(step *jobs.Step) {
		for pk, provs := range changed {
			if err := s.setOutposts(ctx, st, node, pk, provs); err != nil {
				step.Logf("✗ outpost %s terugzetten: %v", pk, err)
			} else {
				step.Logf("Outpost %s terug zoals vooraf", pk)
			}
		}
		if rowID != uuid.Nil {
			_, _ = s.pool.Exec(ctx, `DELETE FROM publish_disabled WHERE id = $1`, rowID)
		}
	}
	if err != nil {
		_ = j.Step(ctx, "Terugdraaien: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error { undo(step); return nil })
		return fmt.Errorf("%s: %w", ph.Domain, err)
	}
	checkErr := j.Step(ctx, "Controle: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error {
		r, err := s.readAuthentik(ctx, st)
		if err != nil {
			return err
		}
		in := r.InOutpost(a.ProviderPK)
		exists := false
		for _, p := range r.Providers {
			if p.PK == a.ProviderPK {
				exists = true
			}
		}
		switch {
		case a.Op == "disable" && in:
			return fmt.Errorf("provider %d zit nog in een outpost", a.ProviderPK)
		case a.Op == "restore" && !in:
			return fmt.Errorf("provider %d zit in geen outpost", a.ProviderPK)
		case a.Op == "purge" && exists:
			return fmt.Errorf("provider %d bestaat nog", a.ProviderPK)
		}
		step.Logf("✓ Authentik opnieuw gelezen: klopt")
		return nil
	})
	if checkErr != nil {
		if a.Op != "purge" {
			_ = j.Step(ctx, "Terugdraaien: "+ph.Domain, func(ctx context.Context, step *jobs.Step) error { undo(step); return nil })
		}
		return fmt.Errorf("%s: controle mislukt: %w", ph.Domain, checkErr)
	}
	switch a.Op {
	case "restore":
		s.markDisabled(ctx, ph.DisabledID, "restored")
	case "purge":
		s.markDisabled(ctx, ph.DisabledID, "removed")
	}
	s.event(ctx, events.System(), "publish.ak_"+a.Op, map[string]any{"domain": ph.Domain, "slug": a.Slug, "provider": a.ProviderPK, "job_id": j.ID.String()})
	return nil
}

// checkOpenPaths doet per open pad een echte aanvraag: die mag niet naar
// Authentik doorsturen.
func (s *Service) checkOpenPaths(ctx context.Context, step *jobs.Step, node nodeRow, ph *PlannedHost) []string {
	var failed []string
	for _, p := range ph.OpenPaths {
		u := "https://" + firstName(ph) + p
		if ph.Scheme != "https" {
			u = "http://" + firstName(ph) + p
		}
		res, err := s.web(ctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: u, Connect: "127.0.0.1"}})
		switch {
		case err != nil || res.Check == nil:
			failed = append(failed, fmt.Sprintf("open pad %s: %v", u, err))
		case res.Check.Error != "":
			failed = append(failed, "open pad "+u+": "+res.Check.Error)
		case strings.Contains(res.Check.Location, "outpost.goauthentik.io") || strings.Contains(res.Check.Location, "/application/o/"):
			failed = append(failed, "open pad "+u+" stuurt nog door naar Authentik")
			step.Logf("✗ %s stuurt door naar Authentik", u)
		case res.Check.Status >= 500:
			failed = append(failed, fmt.Sprintf("open pad %s: HTTP %d", u, res.Check.Status))
		default:
			step.Logf("✓ open pad %s: HTTP %d zonder Authentik", u, res.Check.Status)
		}
	}
	return failed
}

// targetAnswers kijkt vlak voor het uitschakelen nog eens vanaf de NPM-node
// of het doel antwoordt. Leeg als het niet antwoordt (of niet te controleren
// is); anders de reden om de host te laten staan.
func (s *Service) targetAnswers(ctx context.Context, st Settings, h NPMHost) string {
	node, err := s.npmNode(ctx, st)
	if err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u := targetURL(h.ForwardScheme, h.ForwardHost, h.ForwardPort)
	res, err := s.web(cctx, node, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: checkFor(u)})
	if err != nil || res.Check == nil || res.Check.Error != "" || res.Check.Status == 0 {
		return ""
	}
	return fmt.Sprintf("het doel %s antwoordt nu wel vanaf %s (HTTP %d)", u, node.Hostname, res.Check.Status)
}
