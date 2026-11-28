package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Fase 49: Scannen. Alleen als Jonas op Scannen klikt leest ClusterForge
// NPM, Authentik en de doelen opnieuw en maakt het één plan in drie soorten:
// toevoegen (een webdienst zonder host), bijwerken (forward auth ontbreekt,
// verkeerd outpost-adres, verkeerd doel, verhuizen naar een andere cluster)
// en verwijderen (eerst uitschakelen met een kopie). Goedkeuren kan alles
// samen of per regel; het wordt één taak met een kopie van NPM vooraf, per
// host een echte aanvraag als controle en terugdraaien bij een fout.

// Acties van fase 49.
const (
	// ActScan voert regels van een scan uit (ScanID, Items).
	ActScan = "scan"
	// ActMove zet een naam die van een andere cluster is om naar een nieuwe
	// dienst (ServiceKey), met het oude doel als terugval.
	ActMove = "move"
	// ActOpenPaths zet de paden van een uitzondering open (geen Authentik).
	ActOpenPaths = "open_paths"
	// ActDisable schakelt een NPM-host uit, na een kopie.
	ActDisable = "disable"
	// ActAKDisable haalt een provider zonder NPM-host uit de outposts, na
	// een kopie van app en provider (AKSlug of AKProvider).
	ActAKDisable = "ak_disable"
	// ActRestoreDisabled zet iets uitgeschakeld weer aan (DisabledID).
	ActRestoreDisabled = "restore_disabled"
	// ActPurgeDisabled verwijdert iets uitgeschakeld definitief (DisabledID).
	ActPurgeDisabled = "purge_disabled"
	// ActDropFallback haalt de terugval na een verhuizing weg (HostID).
	ActDropFallback = "drop_fallback"
)

// Soorten regels.
const (
	ScanAdd    = "add"
	ScanUpdate = "update"
	ScanRemove = "remove"
)

// ScanItem is één regel van een scan.
type ScanItem struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// What: publish, move, target, vip, outpost, advanced, protect,
	// ak_repair, unprotect, paths, disable, ak_disable.
	What   string `json:"what"`
	Domain string `json:"domain"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Owner is de cluster of dienst waar de naam bij hoort (of na de
	// wijziging bij hoort).
	Owner  string `json:"owner,omitempty"`
	HostID int    `json:"host_id,omitempty"`
	Action Action `json:"action"`
}

// ScanCounts zijn de aantallen per soort.
type ScanCounts struct {
	Add    int `json:"add"`
	Update int `json:"update"`
	Remove int `json:"remove"`
}

// Scan is het plan van één klik op Scannen.
type Scan struct {
	ID        uuid.UUID  `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	Items     []ScanItem `json:"items"`
	// Notes: wat de scan bewust oversloeg, met de reden.
	Notes  []string   `json:"notes"`
	Counts ScanCounts `json:"counts"`
}

// ScanView is de kaart Scannen: de laatste scan, de uitzonderingen en wat
// uitgeschakeld is.
type ScanView struct {
	Scan     *Scan          `json:"scan,omitempty"`
	Policies []Policy       `json:"policies"`
	Disabled []DisabledItem `json:"disabled"`
	// ManualOnly: ClusterForge verandert niets vanzelf (alleen via Scannen).
	ManualOnly bool `json:"manual_only"`
}

// DisabledItem is iets dat een scan uitschakelde.
type DisabledItem struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
	Domain     string    `json:"domain"`
	HostID     int       `json:"host_id,omitempty"`
	AKSlug     string    `json:"ak_slug,omitempty"`
	AKProvider int       `json:"ak_provider,omitempty"`
	DisabledAt time.Time `json:"disabled_at"`
}

func counts(items []ScanItem) ScanCounts {
	var c ScanCounts
	for _, it := range items {
		switch it.Kind {
		case ScanAdd:
			c.Add++
		case ScanUpdate:
			c.Update++
		case ScanRemove:
			c.Remove++
		}
	}
	return c
}

func boolPtr(b bool) *bool { return &b }

func hasProblem(v HostView, codes ...string) bool {
	for _, p := range v.Problems {
		if slices.Contains(codes, p.Code) {
			return true
		}
	}
	return false
}

// buildScan maakt de regels van een scan. Puur, zodat het te testen is.
// disabled zijn de namen en apps die al uitgeschakeld zijn.
func buildScan(st Settings, ov *Overview, pols map[string]Policy, owners map[string]Owner, disabled map[string]bool) ([]ScanItem, []string) {
	items := []ScanItem{}
	notes := []string{}
	ak := st.AuthentikLinked() && ov.Authentik.ReadAt != nil
	raw := map[int]NPMHost{}
	hostByName := map[string]HostView{}
	for _, h := range ov.rawHosts {
		raw[h.ID] = h
	}
	for _, v := range ov.Hosts {
		for _, d := range v.Domains {
			hostByName[strings.ToLower(d)] = v
		}
	}
	ownerText := func(d string) string {
		if o, ok := owners[d]; ok {
			return o.Label()
		}
		return ""
	}

	// 1. Toevoegen en verhuizen.
	names := map[string]int{}
	type cand struct {
		r    ServiceRow
		name string
	}
	var cands []cand
	skipped := map[string][]string{}
	for _, r := range ov.Services {
		if !r.Web || r.Internal || len(r.Published) > 0 {
			continue
		}
		label := r.Title
		if label == "" {
			label = r.Name
		}
		switch {
		case r.ClusterID == nil:
			skipped["draait niet in een cluster"] = append(skipped["draait niet in een cluster"], label)
			continue
		case r.Target == "" || (!r.TargetVIP && len(r.Nodes) != 1):
			skipped["meerdere nodes zonder VIP (doel zou een gok zijn)"] = append(skipped["meerdere nodes zonder VIP (doel zou een gok zijn)"], label)
			continue
		}
		name, err := FullName(r.DefaultName, st.Domain)
		if r.DefaultName == "" || err != nil {
			skipped["geen geldige naam"] = append(skipped["geen geldige naam"], label)
			continue
		}
		names[name]++
		cands = append(cands, cand{r, name})
	}
	for _, c := range cands {
		r, name := c.r, c.name
		svcName := r.Title
		if svcName == "" {
			svcName = r.Name
		}
		if names[name] > 1 {
			skipped["meerdere diensten zouden dezelfde naam krijgen (publiceer met de hand)"] = append(skipped["meerdere diensten zouden dezelfde naam krijgen (publiceer met de hand)"], name)
			continue
		}
		pol := effectivePolicy(pols, st.Defaults, name, r.Container, r.Image, r.Unit, r.Title)
		target := fmt.Sprintf("%s://%s:%d", r.Kind, r.Target, r.Port)
		owner := r.ClusterName
		if v, taken := hostByName[name]; taken {
			if !v.Enabled {
				skipped["naam staat op een uitgeschakelde host (zet die eerst terug of verwijder hem)"] = append(skipped["naam staat op een uitgeschakelde host (zet die eerst terug of verwijder hem)"], name)
				continue
			}
			from := ownerText(name)
			if from == "" {
				from = v.ClusterName
			}
			if from == "" {
				from = fmt.Sprintf("%s:%d", v.ForwardHost, v.ForwardPort)
			}
			items = append(items, ScanItem{
				ID: "move:" + r.Key, Kind: ScanUpdate, What: "move", Domain: name, HostID: v.ID, Owner: owner,
				Title:  fmt.Sprintf("%s verhuizen van %s naar %s", name, from, owner),
				Detail: fmt.Sprintf("doel %s://%s:%d → %s; het oude doel blijft terugval tot je het weghaalt", v.ForwardScheme, v.ForwardHost, v.ForwardPort, target),
				Action: Action{Action: ActMove, HostID: v.ID, ServiceKey: r.Key},
			})
			continue
		}
		protect := ak && pol.WantsAuthentik()
		detail := target + " · " + pol.ModeText()
		if !ak && pol.WantsAuthentik() {
			detail = target + " · zonder Authentik (niet gekoppeld)"
		}
		if r.Environment == "test" || r.Environment == "lab" {
			detail += " · " + r.Environment + "cluster: eigen subdomein"
		}
		items = append(items, ScanItem{
			ID: "add:" + r.Key, Kind: ScanAdd, What: "publish", Domain: name, Owner: owner,
			Title:  fmt.Sprintf("%s publiceren (%s)", name, svcName),
			Detail: detail,
			Action: Action{Action: ActPublish, ServiceKey: r.Key, Label: name, Authentik: boolPtr(protect)},
		})
	}

	// 2. Bijwerken en 3. verwijderen, per host.
	for _, v := range ov.Hosts {
		if !v.Enabled || len(v.Domains) == 0 {
			continue
		}
		name := strings.ToLower(v.Domains[0])
		if disabled[name] {
			continue
		}
		owner := ownerText(name)
		if owner == "" {
			owner = v.ClusterName
		}
		add := func(kind, what, title, detail string, a Action) {
			items = append(items, ScanItem{ID: fmt.Sprintf("%s:%d", what, v.ID), Kind: kind, What: what, Domain: name, HostID: v.ID,
				Owner: owner, Title: title, Detail: detail, Action: a})
		}
		cur := fmt.Sprintf("%s://%s:%d", v.ForwardScheme, v.ForwardHost, v.ForwardPort)
		retarget := false
		switch {
		case v.SuggestVIP != "":
			add(ScanUpdate, "vip", name+": naar het VIP", fmt.Sprintf("%s → %s (draait op alle nodes)", v.ForwardHost, v.SuggestVIP),
				Action{Action: ActRetarget, HostID: v.ID, ForwardHost: v.SuggestVIP})
			retarget = true
		case v.Suggest != nil && v.Suggest.Sure:
			add(ScanUpdate, "target", name+": verkeerd doel", fmt.Sprintf("%s → %s://%s:%d (%s, %s)", cur, v.Suggest.Scheme, v.Suggest.Host, v.Suggest.Port, v.Suggest.ServiceName, v.Suggest.Why),
				Action{Action: ActRetargetMany, Hosts: []int{v.ID}})
			retarget = true
		case v.Suggest != nil:
			notes = append(notes, fmt.Sprintf("%s: nieuw doel is maar een gok (%s); zet het met de hand via Proxy hosts", name, v.Suggest.Why))
		case v.ServiceKey == "" && v.TargetProbe != nil && v.TargetAlive && v.TargetProbe.Status == 404:
			// Het doel antwoordt, maar met 404 (zoals switch): een dienst met
			// dezelfde naam is het juiste doel als die zeker is.
			if sg := suggestTarget(name, v.ForwardPort, ov.Services); sg != nil && sg.Sure && (!strings.EqualFold(sg.Host, v.ForwardHost) || sg.Port != v.ForwardPort) {
				add(ScanUpdate, "target", name+": verkeerd doel (geeft 404)", fmt.Sprintf("%s → %s://%s:%d (%s, %s)", cur, sg.Scheme, sg.Host, sg.Port, sg.ServiceName, sg.Why),
					Action{Action: ActUpdate, HostID: v.ID, ForwardScheme: sg.Scheme, ForwardHost: sg.Host, ForwardPort: sg.Port})
				retarget = true
			} else {
				notes = append(notes, fmt.Sprintf("%s: het doel %s geeft 404 en ClusterForge kent geen zekere dienst met die naam; kijk het doel na", name, cur))
			}
		}
		// Een verkeerd outpost-adres of oud adres in Advanced. Met een gok als
		// doel niet: retarget_many zou die gok dan meenemen.
		if !retarget && len(v.AdvancedFix) > 0 && v.Suggest == nil {
			what, title := "advanced", name+": oud adres in Advanced"
			var parts []string
			for _, f := range v.AdvancedFix {
				if strings.Contains(f.Why, "outpost") {
					what, title = "outpost", name+": verkeerd outpost-adres"
				}
				parts = append(parts, f.Old+" → "+f.New)
			}
			add(ScanUpdate, what, title, strings.Join(parts, ", "), Action{Action: ActRetargetMany, Hosts: []int{v.ID}})
		}
		// Verwijderen: het doel antwoordt niet en er is geen voorstel.
		dead := v.Cleanup || (hasProblem(v, "doel_weg") && !v.TargetAlive && v.Suggest == nil && v.SuggestVIP == "")
		if dead && !retarget {
			why := "op " + cur + " antwoordt niets meer"
			if v.TargetProbe != nil && v.TargetProbe.Error != "" {
				why += " (" + trimText(v.TargetProbe.Error, 60) + ")"
			}
			add(ScanRemove, "disable", name+": dienst verdwenen", why+"; eerst uitschakelen met een kopie, definitief verwijderen kan later",
				Action{Action: ActDisable, HostID: v.ID})
			continue
		}
		// Authentik volgens de uitzondering.
		if !ak || v.Authentik == AKUnknown || !inDomain(name, st.Domain) {
			continue
		}
		pol := effectivePolicy(pols, st.Defaults, name, v.ServiceName)
		h := raw[v.ID]
		cfSnippet := strings.Contains(h.AdvancedConfig, snippetBegin) || hasSnippet(themeBlock(h.AdvancedConfig))
		switch {
		case pol.WantsAuthentik() && v.Authentik == AKOpen:
			if locationRoot.MatchString(withoutTheme(h.AdvancedConfig)) {
				notes = append(notes, name+": forward auth ontbreekt, maar Advanced heeft een eigen 'location /'; zet Authentik er met de hand op")
				break
			}
			add(ScanUpdate, "protect", name+": forward auth ontbreekt", "proxy provider, app en outpost in Authentik, forward auth in NPM ("+pol.ModeText()+")",
				Action{Action: ActProtect, HostID: v.ID})
		case pol.WantsAuthentik() && v.Authentik == AKHalf && v.Typo == "":
			add(ScanUpdate, "ak_repair", name+": Authentik half ingesteld", v.AuthentikDetail, Action{Action: ActAKRepair, Hosts: []int{v.ID}})
		case !pol.WantsAuthentik() && hasSnippet(h.AdvancedConfig) && cfSnippet:
			add(ScanUpdate, "unprotect", name+": hoort niet achter Authentik", pol.ModeText()+"; forward auth weghalen", Action{Action: ActUnprotect, HostID: v.ID})
		case !pol.WantsAuthentik() && hasSnippet(h.AdvancedConfig):
			notes = append(notes, name+": "+pol.ModeText()+", maar de forward auth is niet door ClusterForge gezet; haal hem in NPM weg")
		}
		if pol.Mode == PolicyExceptPaths && hasSnippet(h.AdvancedConfig) && v.Authentik != AKOpen {
			if !slices.Equal(extrasPaths(h.AdvancedConfig), pol.Paths) {
				add(ScanUpdate, "paths", name+": paden zonder Authentik", strings.Join(pol.Paths, ", ")+" open, de rest achter Authentik",
					Action{Action: ActOpenPaths, HostID: v.ID})
			}
		}
	}

	// 3b. Authentik-apps en -providers zonder NPM-host.
	if ak {
		provByPK := map[int]AKProxyProvider{}
		for _, p := range ov.rawAK.Providers {
			provByPK[p.PK] = p
		}
		withApp := map[int]bool{}
		for _, a := range ov.rawAK.Applications {
			if a.Provider != nil {
				withApp[*a.Provider] = true
			}
		}
		own := 0
		for _, a := range ov.Apps {
			if a.HostID != 0 || disabled["ak:"+a.Slug] {
				continue
			}
			if a.Mode == "proxy" {
				own++
				continue
			}
			var pk int
			for _, x := range ov.rawAK.Applications {
				if x.Slug == a.Slug && x.Provider != nil {
					pk = *x.Provider
				}
			}
			d := strings.ToLower(provByPK[pk].Host())
			detail := "geen proxy host in NPM voor " + a.External
			if !a.InOutpost {
				detail += " (zit al in geen outpost)"
			}
			items = append(items, ScanItem{ID: "ak:" + a.Slug, Kind: ScanRemove, What: "ak_disable", Domain: d, Owner: ownerText(d),
				Title:  fmt.Sprintf("Authentik-app %s zonder NPM-host", a.Name),
				Detail: detail + "; eerst uit de outpost met een kopie van app en provider, definitief verwijderen kan later",
				Action: Action{Action: ActAKDisable, AKSlug: a.Slug, AKProvider: pk}})
		}
		for _, p := range ov.rawAK.Providers {
			if withApp[p.PK] || disabled[fmt.Sprintf("akp:%d", p.PK)] || !ov.rawAK.InOutpost(p.PK) {
				continue
			}
			if _, ok := hostByName[p.Host()]; ok {
				continue
			}
			items = append(items, ScanItem{ID: fmt.Sprintf("akp:%d", p.PK), Kind: ScanRemove, What: "ak_disable", Domain: p.Host(), Owner: ownerText(p.Host()),
				Title:  fmt.Sprintf("Authentik-provider %s zonder app en zonder NPM-host", p.Name),
				Detail: "eerst uit de outpost met een kopie, definitief verwijderen kan later",
				Action: Action{Action: ActAKDisable, AKProvider: p.PK}})
		}
		if own > 0 {
			notes = append(notes, fmt.Sprintf("%d Authentik-apps in proxy-modus (eigen outpost) zonder NPM-host blijven staan: die kunnen via een andere proxy werken", own))
		}
	}
	for why, list := range skipped {
		sort.Strings(list)
		notes = append(notes, fmt.Sprintf("Niet toegevoegd, %s: %s", why, strings.Join(list, ", ")))
	}
	sort.Strings(notes)
	order := map[string]int{ScanAdd: 0, ScanUpdate: 1, ScanRemove: 2}
	sort.SliceStable(items, func(i, j int) bool {
		if order[items[i].Kind] != order[items[j].Kind] {
			return order[items[i].Kind] < order[items[j].Kind]
		}
		return items[i].Domain < items[j].Domain
	})
	return items, notes
}

// disabledSet geeft wat al uitgeschakeld is (niet opnieuw voorstellen).
func (s *Service) disabledSet(ctx context.Context) (map[string]bool, error) {
	list, err := s.disabledList(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, d := range list {
		switch d.Kind {
		case "npm":
			out[d.Domain] = true
		case "authentik":
			if d.AKSlug != "" {
				out["ak:"+d.AKSlug] = true
			}
			if d.AKProvider > 0 {
				out[fmt.Sprintf("akp:%d", d.AKProvider)] = true
			}
		}
	}
	return out, nil
}

func (s *Service) disabledList(ctx context.Context) ([]DisabledItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, kind, domain, coalesce(npm_host_id, 0), ak_slug, coalesce(ak_provider, 0), disabled_at
		FROM publish_disabled WHERE done IS NULL ORDER BY disabled_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DisabledItem{}
	for rows.Next() {
		var d DisabledItem
		if err := rows.Scan(&d.ID, &d.Kind, &d.Domain, &d.HostID, &d.AKSlug, &d.AKProvider, &d.DisabledAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RunScan leest NPM, Authentik en de doelen opnieuw en maakt het plan.
func (s *Service) RunScan(ctx context.Context, actor events.Actor, userID *uuid.UUID) (*Scan, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if !st.NPMLinked() {
		return nil, invalid("koppel eerst NPM in Instellingen → Domeinen en publicatie")
	}
	if err := s.refreshForScan(ctx, st); err != nil {
		return nil, err
	}
	ov, err := s.Overview(ctx)
	if err != nil {
		return nil, err
	}
	pols, err := s.policyMap(ctx)
	if err != nil {
		return nil, err
	}
	owners, err := s.owners(ctx)
	if err != nil {
		return nil, err
	}
	s.recordOwners(ctx, ov, owners)
	disabled, err := s.disabledSet(ctx)
	if err != nil {
		return nil, err
	}
	items, notes := buildScan(st, ov, pols, owners, disabled)
	if ov.NPM.Error != "" {
		notes = append([]string{"NPM niet te lezen: " + ov.NPM.Error}, notes...)
	}
	if ov.Authentik.Linked && ov.Authentik.Error != "" {
		notes = append([]string{"Authentik niet te lezen: " + ov.Authentik.Error}, notes...)
	}
	sc := &Scan{Items: items, Notes: notes, Counts: counts(items)}
	ib, _ := json.Marshal(items)
	nb, _ := json.Marshal(notes)
	if err := s.pool.QueryRow(ctx, `INSERT INTO publish_scans (created_by, items, notes) VALUES ($1, $2, $3) RETURNING id, created_at`,
		userID, ib, nb).Scan(&sc.ID, &sc.CreatedAt); err != nil {
		return nil, err
	}
	// Alleen de laatste tien bewaren.
	_, _ = s.pool.Exec(ctx, `DELETE FROM publish_scans WHERE id NOT IN (SELECT id FROM publish_scans ORDER BY created_at DESC LIMIT 10)`)
	s.event(ctx, actor, "publish.scanned", map[string]any{"scan_id": sc.ID.String(), "add": sc.Counts.Add, "update": sc.Counts.Update, "remove": sc.Counts.Remove})
	return sc, nil
}

// refreshForScan leest NPM en Authentik nu en bekijkt de doelen, en wacht
// daarop (anders zou de scan op oude gegevens rusten).
func (s *Service) refreshForScan(ctx context.Context, st Settings) error {
	s.refreshServices(ctx)
	data, err := s.readNPM(ctx, st)
	s.saveRead(ctx, "npm", data, err)
	if err != nil {
		return fmt.Errorf("NPM lezen: %w", err)
	}
	if st.AuthentikLinked() {
		data, err := s.readAuthentik(ctx, st)
		s.saveRead(ctx, "authentik", data, err)
		if err != nil {
			return fmt.Errorf("authentik lezen: %w", err)
		}
	}
	if s.probing.CompareAndSwap(false, true) {
		defer s.probing.Store(false)
		s.ProbeTargets(ctx, st)
	}
	return nil
}

// scan leest een bewaarde scan.
func (s *Service) scan(ctx context.Context, id string) (*Scan, error) {
	var sc Scan
	var ib, nb []byte
	var err error
	if id == "" {
		err = s.pool.QueryRow(ctx, `SELECT id, created_at, items, notes FROM publish_scans ORDER BY created_at DESC LIMIT 1`).Scan(&sc.ID, &sc.CreatedAt, &ib, &nb)
	} else {
		uid, perr := uuid.Parse(id)
		if perr != nil {
			return nil, ErrNotFound
		}
		err = s.pool.QueryRow(ctx, `SELECT id, created_at, items, notes FROM publish_scans WHERE id = $1`, uid).Scan(&sc.ID, &sc.CreatedAt, &ib, &nb)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(ib, &sc.Items)
	_ = json.Unmarshal(nb, &sc.Notes)
	if sc.Items == nil {
		sc.Items = []ScanItem{}
	}
	if sc.Notes == nil {
		sc.Notes = []string{}
	}
	sc.Counts = counts(sc.Items)
	return &sc, nil
}

// ScanOverview geeft de kaart Scannen.
func (s *Service) ScanOverview(ctx context.Context) (*ScanView, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	v := &ScanView{ManualOnly: st.Defaults.ManualOnly}
	sc, err := s.scan(ctx, "")
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return nil, err
	default:
		v.Scan = sc
	}
	if v.Policies, err = s.Policies(ctx); err != nil {
		return nil, err
	}
	if v.Disabled, err = s.disabledList(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

type planCtxKey struct{}

type planCtx struct {
	inner bool
	npm   *NPMRead
}

// innerPlan zegt of dit plan een regel van een scan is.
func innerPlan(ctx context.Context) bool {
	pc, _ := ctx.Value(planCtxKey{}).(*planCtx)
	return pc != nil && pc.inner
}

// readNPMOnce leest NPM één keer per plan van een scan (anders per regel).
func (s *Service) readNPMOnce(ctx context.Context, st Settings) (*NPMRead, error) {
	pc, _ := ctx.Value(planCtxKey{}).(*planCtx)
	if pc != nil && pc.npm != nil {
		return pc.npm, nil
	}
	npm, err := s.readNPM(ctx, st)
	if err == nil && pc != nil {
		pc.npm = npm
	}
	return npm, err
}

// planScan maakt het plan van de gekozen regels van een scan: elke regel
// wordt opnieuw berekend op wat NPM en Authentik nu zijn. Wat niet meer
// klopt, slaat het plan over met de reden.
func (s *Service) planScan(ctx context.Context, a Action, p *Plan) error {
	sc, err := s.scan(ctx, a.ScanID)
	if err != nil {
		return invalid("deze scan bestaat niet meer; scan opnieuw")
	}
	pick := map[string]bool{}
	for _, id := range a.Items {
		pick[id] = true
	}
	var chosen []ScanItem
	for _, it := range sc.Items {
		if len(a.Items) == 0 || pick[it.ID] {
			chosen = append(chosen, it)
		}
	}
	if len(chosen) == 0 {
		return invalid("kies minstens één regel")
	}
	ctx = context.WithValue(ctx, planCtxKey{}, &planCtx{inner: true})
	touched := map[int]string{}
	c := ScanCounts{}
	for _, it := range chosen {
		sub, err := s.Plan(ctx, it.Action)
		if err != nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: overgeslagen, %v", it.Title, err))
			continue
		}
		var keep []PlannedHost
		for _, ph := range sub.Hosts {
			_, advChange := ph.Body["advanced_config"]
			if ph.HostID > 0 && advChange {
				if prev, ok := touched[ph.HostID]; ok {
					p.Warnings = append(p.Warnings, fmt.Sprintf("%s: overgeslagen, %s past Advanced van deze host al aan; scan na de taak opnieuw", it.Title, prev))
					continue
				}
				touched[ph.HostID] = it.Title
			}
			// De extra's (open paden, terugval) gaan mee in elke nieuwe Advanced.
			if adv, ok := ph.Body["advanced_config"].(string); ok && ph.HostID > 0 && !ph.Disable {
				paths, fb := s.extrasFor(ctx, ph.Domain)
				if ph.Fallback != nil {
					fb = ph.Fallback
				}
				ph.Body["advanced_config"] = decorate(adv, paths, fb)
				ph.OpenPaths = paths
			}
			keep = append(keep, ph)
		}
		if len(keep) == 0 {
			continue
		}
		switch it.Kind {
		case ScanAdd:
			c.Add++
		case ScanUpdate:
			c.Update++
		case ScanRemove:
			c.Remove++
		}
		p.Lines = append(p.Lines, "— "+it.Title)
		p.Lines = append(p.Lines, sub.Lines...)
		p.Warnings = append(p.Warnings, sub.Warnings...)
		p.Hosts = append(p.Hosts, keep...)
	}
	if len(p.Hosts) == 0 {
		return invalid("geen enkele gekozen regel is nog toe te passen: %s", strings.Join(p.Warnings, " "))
	}
	p.Title = fmt.Sprintf("Scan toepassen: %d toevoegen, %d bijwerken, %d verwijderen (uitschakelen)", c.Add, c.Update, c.Remove)
	p.Lines = append(p.Lines, fmt.Sprintf("Regel voor regel, elk met een eigen kopie van NPM en een echte aanvraag als controle; mislukt er een, dan gaat alleen die terug en gaan de andere door (%d stappen)", len(p.Hosts)))
	return nil
}
