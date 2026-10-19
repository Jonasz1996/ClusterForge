package components

// Een database bijwerken naar een kleinere versie, zoals MariaDB 11.8.3
// naar 11.8.4 of PostgreSQL 17.5 naar 17.6: node voor node, met de
// schrijver als laatste. Op elke node draait ClusterForge apt-get update en
// werkt het alleen de pakketten van de database bij, binnen de versie van
// Debian. Veranderde er iets, dan herstart de database daar, en ClusterForge
// wacht tot de node weer meedoet voor het de volgende neemt. Is een node de
// primary van Patroni, dan schakelt ClusterForge eerst over naar de
// synchrone replica.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/galera"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// UpdateKind is de taak die een database node voor node bijwerkt.
const UpdateKind = "component.update"

// updatePackages zijn per engine en rol de pakketten die ClusterForge
// bijwerkt, als patronen voor dpkg-query; alleen wat er staat, telt.
// updateUnit is de dienst die daarna herstart.
var (
	updatePackages = map[string]map[string][]string{
		protocol.DBMariaDB: {
			RoleData:    {"mariadb-*", "libmariadb*", "galera-*", "mysql-common"},
			RoleArbiter: {"galera-*"},
		},
		protocol.DBPostgreSQL: {
			RoleData: {"postgresql", "postgresql-*", "libpq*", "patroni", "python3-etcd"},
		},
	}
	updateUnit = map[string]map[string]string{
		protocol.DBMariaDB:    {RoleData: "mariadb", RoleArbiter: "garb"},
		protocol.DBPostgreSQL: {RoleData: "patroni"},
	}
)

// updatable zijn de pakketten uit packages die bij de database horen.
func updatable(engine, role string, packages []string) []string {
	var out []string
	for _, p := range packages {
		for _, pat := range updatePackages[engine][role] {
			if pkgMatch(pat, p) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// pkgMatch past een patroon van updatePackages toe, met alleen * aan het
// eind.
func pkgMatch(pattern, name string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, prefix)
	}
	return pattern == name
}

// UpdateNode is een node in het plan van bijwerken, op de volgorde van
// bijwerken.
type UpdateNode struct {
	NodeID   uuid.UUID
	Hostname string
	Role     string
	State    string
	Healthy  bool
	Writer   bool
	// Version is de versie die nu draait; leeg bij een scheidsrechter.
	Version string
	// Packages zijn de pakketten van de database waarvoor de node volgens
	// zijn laatste apt update een nieuwere versie heeft; nil als de agent
	// dat niet zei.
	Packages []string
	// Skip: de node heeft niets om bij te werken, zoals de scheidsrechter
	// van Patroni, die alleen etcd heeft.
	Skip bool
}

// UpdatePlan zegt wat bijwerken doet. Er verandert niets.
type UpdatePlan struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Prod        bool
	Engine      string
	Title       string
	Nodes       []UpdateNode
	Effects     []string
	Findings    []Finding
	Blocked     bool
	Hash        string

	order []uuid.UUID
}

// UpdatePlan zegt wat bijwerken nu zou doen.
func (s *Service) UpdatePlan(ctx context.Context, clusterID, id uuid.UUID) (UpdatePlan, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return UpdatePlan{}, err
	}
	comp, err := s.dbComponent(ctx, clusterID, id)
	if err != nil {
		return UpdatePlan{}, err
	}
	spec, err := parseSpec(comp.Spec)
	if err != nil {
		return UpdatePlan{}, err
	}
	p := UpdatePlan{ClusterID: clusterID, ClusterName: c.Name, Slug: c.Slug, Prod: c.Environment == store.EnvironmentProd, Engine: comp.Engine, Title: title(spec)}
	add := func(level, code string, n *Node, msg, command string) {
		f := Finding{Level: level, Code: code, Message: msg, Command: command}
		if n != nil {
			f.NodeID, f.Hostname = &n.NodeID, n.Hostname
		}
		p.Findings = append(p.Findings, f)
	}
	if comp.AppliedRevision == nil {
		add(Blocked, "not_applied", nil, "de database is nog nooit helemaal uitgerold; kies eerst Opnieuw toepassen", "")
	}
	nodes, err := s.rolling(ctx, comp, spec)
	if err != nil {
		return UpdatePlan{}, err
	}
	rollingFindings(nodes, comp.Engine, "bijwerkt", add)
	known, found := false, false
	for _, r := range nodes {
		n := UpdateNode{NodeID: r.node.NodeID, Hostname: r.node.Hostname, Role: r.node.Role, State: r.state, Healthy: r.healthy, Writer: r.writer,
			Skip: len(updatePackages[comp.Engine][r.node.Role]) == 0}
		if r.server != nil {
			n.Version = r.server.Version
		}
		if r.facts != nil && r.facts.Upgrades != nil && !n.Skip {
			n.Packages = updatable(comp.Engine, r.node.Role, r.facts.Upgrades.Packages)
			if n.Packages == nil {
				n.Packages = []string{}
			}
			known = true
			found = found || len(n.Packages) > 0
		}
		if !n.Skip {
			p.order = append(p.order, n.NodeID)
		}
		p.Nodes = append(p.Nodes, n)
	}
	if comp.Engine == protocol.DBPostgreSQL {
		s.updatePrimary(ctx, comp, spec, nodes, add)
	}
	if known && !found {
		add(Info, "no_updates", nil, fmt.Sprintf("volgens de laatste apt update op de nodes is er geen nieuwere versie van %s. ClusterForge draait bij het bijwerken zelf apt-get update; staat er dan nog niets klaar, dan verandert er niets", p.Title), "")
	}
	p.Effects = updateEffects(p, spec, nodes)
	p.Blocked = slices.ContainsFunc(p.Findings, func(f Finding) bool { return f.Level == Blocked })
	p.Hash = updateHash(comp, p)
	return p, nil
}

// updatePrimary blokkeert bijwerken als ClusterForge niet van de primary kan
// overschakelen: dan zou PostgreSQL bij de herstart niet schrijven.
func (s *Service) updatePrimary(ctx context.Context, comp store.ClusterComponent, spec Spec, nodes []rollNode, add func(string, string, *Node, string, string)) {
	i := slices.IndexFunc(nodes, func(r rollNode) bool { return r.writer })
	if i < 0 {
		return
	}
	pg, err := s.pgNodes(ctx, comp.ClusterID, spec)
	if err != nil {
		return
	}
	if cand, why := syncStandby(pg, nodes[i].node, spec.Cluster.Slug); cand == nil {
		add(Blocked, "no_sync_standby", &nodes[i].node, fmt.Sprintf("ClusterForge kan niet overschakelen van %s, de primary: %s. Bijwerken zou PostgreSQL bij de herstart van %s stilleggen",
			nodes[i].node.Hostname, why, nodes[i].node.Hostname), "")
	}
}

// updateEffects zegt in gewone woorden wat er gebeurt.
func updateEffects(p UpdatePlan, spec Spec, nodes []rollNode) []string {
	writers, others := dataNames(nodes, true), dataNames(nodes, false)
	order := joinHosts(others)
	if len(writers) > 0 {
		order += " en als laatste " + joinHosts(writers)
	}
	var arbiters []string
	for _, r := range nodes {
		if r.node.Role != RoleData {
			arbiters = append(arbiters, r.node.Hostname)
		}
	}
	var out []string
	if p.Engine == protocol.DBPostgreSQL {
		out = append(out,
			fmt.Sprintf("Node voor node: %s. Op elke node draait ClusterForge apt-get update en werkt het alleen PostgreSQL, Patroni en hun bibliotheken bij, binnen de versie van Debian, zoals 17.5 naar 17.6.", order),
			"Veranderde er iets, dan herstart Patroni met PostgreSQL op die node, en ClusterForge wacht tot hij weer bijhoudt voor de volgende node.")
		if len(writers) > 0 {
			out = append(out, fmt.Sprintf("Voor %s schakelt ClusterForge eerst over naar de synchrone replica; de app kan dan een paar seconden niet schrijven. %s blijft daarna een replica.",
				joinHosts(writers), joinHosts(writers)))
		}
		if len(arbiters) > 0 {
			out = append(out, fmt.Sprintf("De scheidsrechter %s heeft alleen etcd; daar verandert niets.", joinHosts(arbiters)))
		}
	} else {
		if len(arbiters) > 0 {
			order += ", daarna de scheidsrechter " + joinHosts(arbiters)
		}
		out = append(out,
			fmt.Sprintf("Node voor node: %s. Op elke node draait ClusterForge apt-get update en werkt het alleen MariaDB en Galera bij, binnen de versie van Debian, zoals 11.8.3 naar 11.8.4. "+
				"Heeft de nieuwe versie de scripts van Galera in %s (MariaDB.org vanaf 12.3.2), dan installeert het dat mee, zodat de node weer aansluit.", order, galera.Package),
			"Veranderde er iets, dan herstart MariaDB op die node, en ClusterForge wacht tot hij weer Synced is voor de volgende node. De andere nodes werken door.")
		if len(writers) > 0 && spec.VIP != nil {
			out = append(out, fmt.Sprintf("Bij de herstart van %s gaat het VIP %s naar een andere node; de app kan een paar seconden niet schrijven.", joinHosts(writers), spec.VIP.Address))
		}
	}
	out = append(out, "Andere pakketten en een nieuwe hoofdversie blijven zoals ze zijn; zonder nieuwere versie verandert er niets en herstart er niets.")
	return out
}

// updateHash vat het plan samen: de volgorde en de bevindingen, zonder de
// pakketten, die met elke apt update kunnen veranderen.
func updateHash(comp store.ClusterComponent, p UpdatePlan) string {
	type finding struct {
		Level, Code string
		Node        *uuid.UUID
	}
	v := struct {
		Component uuid.UUID   `json:"component"`
		Revision  int32       `json:"revision"`
		Order     []uuid.UUID `json:"order"`
		Findings  []finding   `json:"findings"`
	}{Component: comp.ID, Revision: comp.Revision, Order: p.order}
	for _, f := range p.Findings {
		v.Findings = append(v.Findings, finding{f.Level, f.Code, f.NodeID})
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// updateParams gaan mee met de taak.
type updateParams struct {
	ComponentID uuid.UUID   `json:"component_id"`
	Order       []uuid.UUID `json:"order"`
}

// Update zet het bijwerken in de wachtrij, als het plan nog is wat de
// gebruiker zag en niets het tegenhoudt.
func (s *Service) Update(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, hash string) (store.Job, error) {
	p, err := s.UpdatePlan(ctx, clusterID, id)
	if err != nil {
		return store.Job{}, err
	}
	if p.Hash != hash {
		return store.Job{}, ErrStale
	}
	for _, f := range p.Findings {
		if f.Level == Blocked {
			return store.Job{}, &ConflictError{f.Message}
		}
	}
	var j store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := s.get(ctx, q, clusterID, id, true); err != nil {
			return err
		}
		j, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: UpdateKind, Title: fmt.Sprintf("Bijwerken: %s op %s", p.Title, p.ClusterName),
			Params: updateParams{ComponentID: id, Order: p.order}, ClusterID: &clusterID, Actor: actor,
		})
		return err
	})
	var busy jobs.BusyError
	if errors.As(err, &busy) {
		return store.Job{}, &ConflictError{busy.Error()}
	}
	if err == nil {
		s.jobs.Kick()
	}
	return j, err
}

// updateResult is wat het bijwerken op een node deed; het is ook de state
// van zijn stap.
type updateResult struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Updated bool   `json:"updated"`
}

func (s *Service) runUpdate(ctx context.Context, j *jobs.Job) error {
	var p updateParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	comp, err := s.q.GetClusterComponent(ctx, p.ComponentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("het onderdeel bestaat niet meer")
	}
	if err != nil {
		return err
	}
	if comp.AppliedRevision == nil {
		return errors.New("deze database is nog nooit helemaal uitgerold")
	}
	spec, err := parseSpec(comp.Spec)
	if err != nil {
		return err
	}
	results := map[string]updateResult{}
	var hosts []string
	for _, n := range spec.ordered(p.Order) {
		if len(updatePackages[comp.Engine][n.Role]) == 0 {
			continue
		}
		name := n.Hostname + ": bijwerken"
		var res updateResult
		if err := j.Step(ctx, name, func(ctx context.Context, st *jobs.Step) error {
			return s.updateNode(ctx, j, st, actor, comp, spec, n, &res)
		}); err != nil {
			return err
		}
		if res == (updateResult{}) {
			// De stap lukte al bij een eerdere poging.
			j.Done(name, &res)
		}
		results[n.Hostname] = res
		hosts = append(hosts, n.Hostname)
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if comp.Engine == protocol.DBPostgreSQL {
			primary, err := s.verifyPatroni(ctx, st, spec)
			if err != nil {
				return err
			}
			if err := s.waitOwnVIP(ctx, st, spec, primary); err != nil {
				return err
			}
		} else {
			if err := s.verify(ctx, st, spec); err != nil {
				return err
			}
			if err := s.verifyVIP(ctx, st, spec); err != nil {
				return err
			}
		}
		var nodes []map[string]any
		var updated []string
		for _, h := range hosts {
			r := results[h]
			nodes = append(nodes, map[string]any{"hostname": h, "from": r.From, "to": r.To, "updated": r.Updated})
			if r.Updated {
				updated = append(updated, h)
			}
		}
		if len(updated) == 0 {
			st.Logf("er was geen nieuwere versie; er is niets veranderd")
		} else {
			st.Logf("bijgewerkt op %s", joinHosts(updated))
		}
		return s.manageDone(ctx, actor, j, comp, spec, "cluster.database_updated", map[string]any{"nodes": nodes, "updated": updated})
	})
}

// updateMarker is het begin van de merktekens van een taak op een node.
func updateMarker(jobID uuid.UUID) string { return "/var/lib/clusterforge/db-update-" + jobID.String() }

// updatePkgs zet in $pkgs de pakketten van de database die op de node
// staan.
func updatePkgs(patterns []string) string {
	q := make([]string, len(patterns))
	for i, p := range patterns {
		q[i] = "'" + p + "'"
	}
	return `pkgs=$(dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' ` + strings.Join(q, " ") + ` 2>/dev/null | awk '$1=="ii"{print $2}')`
}

// galeraExtra zet $extra op galera.Package in de versie die apt voor
// mariadb-server zou installeren, als Galera op de node staat, dat pakket
// nog niet, en de pakketbron het in die versie heeft: sinds MariaDB 12.3.2
// (MariaDB.org) zitten de scripts van Galera daarin. Het moet mee in
// dezelfde apt-get als het bijwerken: mariadb-server stopt en start MariaDB
// zelf, en zonder de scripts sluit hij niet aan, faalt die start en dus apt.
func galeraExtra(apt string) string {
	return `extra=; if dpkg-query -W -f='${db:Status-Abbrev}' galera-4 2>/dev/null | grep -q '^ii' && ` +
		`! dpkg-query -W -f='${db:Status-Abbrev}' ` + galera.Package + ` 2>/dev/null | grep -q '^ii'; then ` +
		`cand=$(LC_ALL=C apt-cache policy mariadb-server 2>/dev/null | awk '/Candidate:/{print $2}'); ` +
		`[ -z "$cand" ] || [ "$cand" = "(none)" ] || ! ` + apt + ` -s -o Debug::NoLocking=1 install --no-remove "` + galera.Package + `=$cand" >/dev/null 2>&1 || ` +
		`extra="` + galera.Package + `=$cand"; fi`
}

// galeraScripts controleert na het bijwerken of Galera zijn scripts nog
// heeft, en installeert anders galera.Package in de versie van
// mariadb-server. Heeft de pakketbron die versie niet, dan stopt de stap,
// voor de herstart.
func galeraScripts(apt string) string {
	pin := `"` + galera.Package + `=$(dpkg-query -W -f='${Version}' mariadb-server)"`
	return `{ ! dpkg-query -W -f='${db:Status-Abbrev}' galera-4 2>/dev/null | grep -q '^ii' || command -v galera_recovery >/dev/null || ` +
		`{ { ` + apt + ` -s -o Debug::NoLocking=1 install --no-remove ` + pin + ` >/dev/null 2>&1 || ` +
		`{ echo "` + galera.Package + ` staat niet in de pakketbron in de versie van mariadb-server; zonder dat pakket sluit deze node niet meer aan als MariaDB opnieuw start" >&2; exit 1; }; } && ` +
		`echo "nieuw: ` + galera.Package + ` (sinds MariaDB 12.3.2 apart)" && ` +
		`DEBIAN_FRONTEND=noninteractive ` + apt + ` install -y --no-install-recommends --no-remove -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold ` + pin + `; }; }`
}

// updateSteps zijn de stappen van bijwerken op een node, elk met een
// merkteken van de taak, zodat een hervatte taak niets twee keer doet:
//
//   - check: apt-get update, en kijken of er een nieuwere versie is; zo ja,
//     dan komt er een merkteken .found;
//   - found: niets, maar alleen als .found er is; zo weet ClusterForge het;
//   - install: de pakketten bijwerken, bij MariaDB met Galera ook
//     galera.Package als het nodig werd (galeraScripts). Veranderde er een
//     versie, dan komt in .restart de tijd van voor het bijwerken;
//   - restart: de dienst herstarten, tenzij het pakket dat na die tijd al
//     deed.
func updateSteps(jobID uuid.UUID, patterns []string, unit string) (check, found, install, restart templates.Step) {
	m := updateMarker(jobID)
	pkgs := updatePkgs(patterns)
	apt := "apt-get -q -o DPkg::Lock::Timeout=300"
	check = templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: fmt.Sprintf(`find /var/lib/clusterforge -maxdepth 1 -name 'db-update-*' ! -name 'db-update-%s*' -delete; %s; `+
			`[ -n "$pkgs" ] || { echo "geen pakketten van de database gevonden"; exit 1; }; `+
			`%s update && { if %s -s -o Debug::NoLocking=1 install --only-upgrade $pkgs | grep '^Inst '; then touch %s.found; else echo "alles is al bijgewerkt"; fi; } && touch %s.checked`,
			jobID, pkgs, apt, apt, m, m),
		Creates: m + ".checked",
	}}, Title: "apt-get update en kijken of er een nieuwere versie is"}
	found = templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: "true", Unless: fmt.Sprintf("test ! -e %s.found", m),
	}}, Title: "er is een nieuwere versie"}
	const opts = "--no-remove -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
	upgrade := "DEBIAN_FRONTEND=noninteractive " + apt + " install -y --only-upgrade " + opts + " $pkgs && "
	if unit == "mariadb" {
		// Zonder --only-upgrade, want dat slaat een pakket over dat er nog
		// niet staat; $pkgs staat er allemaal al.
		upgrade = galeraExtra(apt) + `; if [ -n "$extra" ]; then echo "nieuw: $extra (sinds MariaDB 12.3.2 apart)"; ` +
			"DEBIAN_FRONTEND=noninteractive " + apt + " install -y --no-install-recommends " + opts + ` $pkgs "$extra"; ` +
			"else DEBIAN_FRONTEND=noninteractive " + apt + " install -y --only-upgrade " + opts + " $pkgs; fi && " + galeraScripts(apt) + " && "
	}
	install = templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: fmt.Sprintf(`%s; [ -e %[2]s.t0 ] || date +%%s > %[2]s.t0; [ -e %[2]s.before ] || dpkg-query -W -f='${Package}=${Version}\n' $pkgs > %[2]s.before; %[4]s`+
			`dpkg-query -W -f='${Package}=${Version}\n' $pkgs > %[2]s.after && `+
			`{ cmp -s %[2]s.before %[2]s.after || { diff %[2]s.before %[2]s.after | sed -n 's/^> /nieuw: /p'; cp %[2]s.t0 %[2]s.restart; }; } && touch %[2]s.done`,
			pkgs, m, apt, upgrade),
		Creates: m + ".done",
	}}, Title: "de pakketten van de database bijwerken"}
	// MariaDB kan bij een nieuwe node lang starten (een kopie van alle
	// data), dus die herstart zonder te wachten; ClusterForge wacht zelf.
	run := "systemctl restart " + unit
	if unit == "mariadb" {
		run = "systemctl restart --no-block " + unit
	}
	restart = templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: fmt.Sprintf("%s && rm -f %s.restart", run, m),
		Unless: fmt.Sprintf(`test ! -e %[1]s.restart || { s=$(date -d "$(systemctl show -p ActiveEnterTimestamp --value %[2]s)" +%%s 2>/dev/null) && [ "$s" -ge "$(cat %[1]s.restart)" ] && rm -f %[1]s.restart; }`,
			m, unit),
	}}, Title: unit + " herstarten"}
	return check, found, install, restart
}

// updateNode werkt één node bij en wacht tot hij weer meedoet.
func (s *Service) updateNode(ctx context.Context, j *jobs.Job, st *jobs.Step, actor events.Actor, comp store.ClusterComponent, spec Spec, n Node, res *updateResult) error {
	slug := spec.Cluster.Slug
	st.State(res)
	if res.From == "" && n.Role == RoleData {
		if srv, err := s.server(ctx, n.NodeID, comp.Engine); err == nil && srv != nil {
			res.From = srv.Version
			if err := st.SetState(ctx, res); err != nil {
				return err
			}
		}
	}
	check, found, install, restart := updateSteps(j.ID, updatePackages[comp.Engine][n.Role], updateUnit[comp.Engine][n.Role])
	if _, err := deployApply(ctx, s, j, st, n, "-update-check", []templates.Step{check}); err != nil {
		return err
	}
	ok, err := deployApply(ctx, s, j, st, n, "-update-found", []templates.Step{found})
	if err != nil {
		return err
	}
	if ok == 0 {
		st.Logf("%s heeft geen nieuwere versie; er verandert niets", n.Hostname)
		res.To = res.From
		return st.SetState(ctx, res)
	}
	if comp.Engine == protocol.DBPostgreSQL {
		if err := s.switchAway(ctx, st, actor, j, comp, spec, n, false, "update"); err != nil {
			return err
		}
	}
	if _, err := deployApply(ctx, s, j, st, n, "-update", []templates.Step{install}); err != nil {
		return err
	}
	since := s.Now()
	restarted, err := deployApply(ctx, s, j, st, n, "-update-restart", []templates.Step{restart})
	if err != nil {
		return err
	}
	if restarted == 0 {
		// Het pakket herstartte de dienst al, of er veranderde niets.
		since = time.Time{}
	}
	res.Updated = true
	if n.Role != RoleData {
		if err := s.waitArbiter(ctx, st, spec, n); err != nil {
			return err
		}
		return st.SetState(ctx, res)
	}
	if err := s.waitBack(ctx, st, n, comp.Engine, slug, spec.count(RoleData), since); err != nil {
		return err
	}
	if srv, err := s.server(ctx, n.NodeID, comp.Engine); err == nil && srv != nil {
		res.To = srv.Version
		if res.From != "" && res.To != res.From {
			st.Logf("%s draait nu %s in plaats van %s", n.Hostname, res.To, res.From)
		}
	}
	return st.SetState(ctx, res)
}

// waitBack wacht na een herstart tot n weer meedoet: Synced in een cluster
// van minstens want leden bij Galera, de primary of een replica die
// bijhoudt bij Patroni. Zonder since telt alleen dat; met since moet de
// database daarna opnieuw gestart zijn, want vlak na een herstart zonder
// wachten draait de oude nog.
func (s *Service) waitBack(ctx context.Context, st *jobs.Step, n Node, engine, slug string, want int, since time.Time) error {
	restarted := since.IsZero()
	return s.waitServer(ctx, st, n.Hostname+" doet weer mee", n.NodeID, engine, func(srv *protocol.DBServer, err error) (string, bool) {
		if err != nil || srv == nil {
			restarted = true
			if err == nil {
				err = errors.New("de database draait niet")
			}
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		}
		var state string
		var ok bool
		if engine == protocol.DBPostgreSQL {
			state, ok = patroniHealthy(srv, slug)
		} else {
			state, ok = galeraState(srv, slug), healthy(srv.Galera, slug, want)
		}
		if !ok || (srv.Running && time.Duration(srv.UptimeSeconds)*time.Second < s.Now().Sub(since)) {
			restarted = true
		}
		if ok && !restarted {
			state += ", nog niet herstart"
		}
		return n.Hostname + ": " + state, ok && restarted
	})
}

// waitArbiter wacht tot het Galera-cluster de scheidsrechter n weer
// meetelt.
func (s *Service) waitArbiter(ctx context.Context, st *jobs.Step, spec Spec, n Node) error {
	var first Node
	for _, x := range spec.order() {
		if x.Role == RoleData {
			first = x
			break
		}
	}
	want := len(spec.Nodes)
	return s.wait(ctx, st, n.Hostname+" telt mee", func(g *protocol.DBGalera, err error) (string, bool) {
		if err != nil {
			return fmt.Sprintf("%s niet te lezen: %v", first.Hostname, err), false
		}
		if g == nil {
			return first.Hostname + " draait geen Galera", false
		}
		return fmt.Sprintf("het cluster heeft %d leden volgens %s", g.ClusterSize, first.Hostname), healthy(g, spec.Cluster.Slug, want)
	}, first.NodeID)
}
