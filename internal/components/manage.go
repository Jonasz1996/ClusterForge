package components

// Beheer van een database, node voor node. Bij Galera: een node opnieuw
// synchroniseren; hij gooit zijn positie weg en haalt alle data opnieuw bij
// een node die Synced is (SST met mariabackup). Bij PostgreSQL met Patroni:
// een replica de primary maken (switchover), of een replica opnieuw
// opbouwen (reinitialize: alles opnieuw van de primary). Het overzicht leest
// alleen. Een actie is een taak component.manage, met de hash van het
// overzicht dat de gebruiker zag, zoals bij opnieuw starten.

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
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ManageKind is de taak die een actie uit Beheer uitvoert.
const ManageKind = "component.manage"

// De acties van Beheer.
const (
	// ManageResync: een Galera-node gooit zijn positie weg en haalt alle
	// data opnieuw bij een andere node.
	ManageResync = "resync"
	// ManageSwitchover: deze replica van Patroni wordt de primary.
	ManageSwitchover = "switchover"
	// ManageReinit: deze replica van Patroni gooit zijn data weg en
	// kopieert alles opnieuw van de primary.
	ManageReinit = "reinit"
)

var manageTitles = map[string]string{ManageResync: "Opnieuw synchroniseren", ManageSwitchover: "Primary maken", ManageReinit: "Opnieuw opbouwen"}

// resyncStep stopt MariaDB, haalt de positie weg zodat Galera de node
// helemaal opnieuw vult, en start MariaDB zonder te wachten: systemd wacht
// anders tot de hele kopie klaar is. Het merkteken van de taak zorgt dat
// het één keer gebeurt, ook als de taak na een herstart verder gaat.
func resyncStep(jobID uuid.UUID) templates.Step {
	marker := "/var/lib/clusterforge/galera-resync-" + jobID.String()
	return templates.Step{Step: protocol.Step{Command: &protocol.CommandStep{
		Run: "rm -f /var/lib/clusterforge/galera-resync-* && systemctl stop mariadb && rm -f /var/lib/mysql/grastate.dat && touch " + marker +
			" && systemctl start --no-block mariadb",
		Creates: marker,
	}}, Title: "MariaDB stoppen, de positie weghalen en weer starten"}
}

// ManageAction is een actie op een node: kan ze nu, en wat gebeurt er.
type ManageAction struct {
	Action  string
	Title   string
	Allowed bool
	// Reason zegt waarom het nu niet kan.
	Reason string
	// Effects zegt wat er gebeurt.
	Effects []string
}

// ManageNode is een datanode zoals Beheer hem toont.
type ManageNode struct {
	NodeID   uuid.UUID
	Hostname string
	Index    int
	Answered bool
	// State zegt in woorden hoe het met de node staat; Healthy is true als
	// hij meedoet: Synced bij Galera, de primary of een replica die bijhoudt
	// bij Patroni.
	State   string
	Healthy bool
	// Role is bij PostgreSQL primary of replica, Sync true voor de
	// synchrone replica; LagBytes is hoeveel WAL een replica nog niet heeft.
	Role           string
	Sync           bool
	LagBytes       *int64
	PendingRestart bool
	// VIP: de node heeft nu het VIP van de database.
	VIP     bool
	Actions []ManageAction
}

// ManageView is Beheer van een database.
type ManageView struct {
	ClusterID   uuid.UUID
	ClusterName string
	Slug        string
	Prod        bool
	Engine      string
	Title       string
	VIP         string
	Nodes       []ManageNode
	Hash        string
}

// Manage toont de datanodes van een database met wat Beheer per node kan.
// Er verandert niets.
func (s *Service) Manage(ctx context.Context, clusterID, id uuid.UUID) (ManageView, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return ManageView{}, err
	}
	r, err := s.dbComponent(ctx, clusterID, id)
	if err != nil {
		return ManageView{}, err
	}
	spec, err := parseSpec(r.Spec)
	if err != nil {
		return ManageView{}, err
	}
	v := ManageView{
		ClusterID: clusterID, ClusterName: c.Name, Slug: c.Slug, Prod: c.Environment == store.EnvironmentProd, Engine: r.Engine, Title: title(spec),
		VIP: spec.vipAddress(),
	}
	holders := map[uuid.UUID]bool{}
	if spec.VIP != nil {
		hs, err := s.vipHolder(ctx, spec, s.Now().Add(-s.Fresh))
		if err != nil {
			return ManageView{}, err
		}
		for _, h := range hs {
			holders[h.NodeID] = true
		}
	}
	if r.Engine == protocol.DBPostgreSQL {
		if err := s.managePG(ctx, &v, spec, holders); err != nil {
			return ManageView{}, err
		}
	} else {
		s.manageGalera(ctx, &v, spec, holders)
	}
	v.Hash = manageHash(r, v)
	return v, nil
}

func (s *Service) manageGalera(ctx context.Context, v *ManageView, spec Spec, holders map[uuid.UUID]bool) {
	slug := spec.Cluster.Slug
	all := s.observe(ctx, dataNodes(spec, nil), false)
	synced := 0
	for _, o := range all {
		if o.synced(slug) {
			synced++
		}
	}
	for i, o := range all {
		n := ManageNode{
			NodeID: o.node.NodeID, Hostname: o.node.Hostname, Index: o.node.Index, Answered: o.answered(), State: o.describe(slug),
			Healthy: o.synced(slug), VIP: holders[o.node.NodeID],
		}
		var donors []string
		for j, x := range all {
			if j != i && x.synced(slug) {
				donors = append(donors, x.node.Hostname)
			}
		}
		host := n.Hostname
		a := ManageAction{Action: ManageResync, Title: manageTitles[ManageResync]}
		switch {
		case !o.answered():
			a.Reason = fmt.Sprintf("de agent op %s antwoordt niet: %v", host, o.err)
		case synced == 0:
			a.Reason = "geen enkele node is Synced, dus er is niets om de data van te halen; start de database eerst met Opnieuw starten"
		case len(donors) == 0:
			a.Reason = fmt.Sprintf("%s is de enige node die Synced is; zonder hem stopt de database, en er is geen andere node om de data van te halen", host)
		default:
			a.Allowed = true
			a.Effects = append(a.Effects, fmt.Sprintf("MariaDB op %s stopt, gooit zijn positie weg en haalt alle data opnieuw bij %s (SST met mariabackup). "+
				"De andere nodes werken door.", host, joinHosts(donors)))
			if n.Healthy {
				a.Effects = append(a.Effects, host+" is nu Synced; opnieuw synchroniseren is alleen nodig als zijn data niet klopt.")
			}
			if n.VIP {
				a.Effects = append(a.Effects, fmt.Sprintf("Het VIP %s gaat naar een andere node zolang %s niet Synced is.", spec.VIP.Address, host))
			}
			if len(donors) == 1 {
				a.Effects = append(a.Effects, fmt.Sprintf("Tijdens de kopie is %s de enige node met de database; valt die weg, dan stopt de database.", donors[0]))
			}
			a.Effects = append(a.Effects, fmt.Sprintf("Bij een grote database duurt de kopie even; ClusterForge wacht tot %s weer Synced is.", host))
		}
		n.Actions = []ManageAction{a}
		v.Nodes = append(v.Nodes, n)
	}
}

func (s *Service) managePG(ctx context.Context, v *ManageView, spec Spec, holders map[uuid.UUID]bool) error {
	slug := spec.Cluster.Slug
	nodes, err := s.pgNodes(ctx, v.ClusterID, spec)
	if err != nil {
		return err
	}
	primary, sync := -1, ""
	for i, p := range nodes {
		if p.err == nil && isPrimary(p.server, slug) {
			primary = i
		}
		if p.err == nil && p.server != nil && p.server.Patroni != nil && p.server.Patroni.Sync {
			sync = p.node.Hostname
		}
	}
	for i, p := range nodes {
		host := p.node.Hostname
		state, ok := patroniHealthy(p.server, slug)
		if p.err != nil {
			state = "antwoordt niet: " + p.err.Error()
		}
		n := ManageNode{NodeID: p.node.NodeID, Hostname: host, Index: p.node.Index, Answered: p.err == nil, State: state, Healthy: p.err == nil && ok, VIP: holders[p.node.NodeID]}
		if p.err == nil && p.server != nil && p.server.Patroni != nil {
			pt := p.server.Patroni
			n.Role, n.Sync, n.LagBytes, n.PendingRestart = pt.Role, pt.Sync, pt.LagBytes, pt.PendingRestart
		}
		if i == primary {
			n.Actions = []ManageAction{}
			v.Nodes = append(v.Nodes, n)
			continue
		}
		sw := ManageAction{Action: ManageSwitchover, Title: manageTitles[ManageSwitchover]}
		re := ManageAction{Action: ManageReinit, Title: manageTitles[ManageReinit]}
		common := ""
		switch {
		case p.err != nil:
			common = fmt.Sprintf("de agent op %s antwoordt niet: %v", host, p.err)
		case patroniProtocol(p) != nil:
			common = patroniProtocol(p).Error()
		}
		switch {
		case common != "":
			sw.Reason = common
		case primary < 0:
			sw.Reason = "er is geen primary om van over te schakelen; valt de primary weg, dan kiest Patroni zelf een nieuwe"
		case !n.Healthy:
			sw.Reason = fmt.Sprintf("%s is geen replica die bijhoudt (%s)", host, state)
		case !n.Sync && sync != "":
			sw.Reason = fmt.Sprintf("Patroni maakt alleen de synchrone replica primary, en dat is nu %s; zo gaat er geen bevestigde schrijfactie verloren", sync)
		case !n.Sync:
			sw.Reason = "er is nog geen synchrone replica; Patroni kiest er een zodra een replica bijhoudt, en alleen die kan primary worden"
		default:
			old := nodes[primary].node.Hostname
			sw.Allowed = true
			sw.Effects = append(sw.Effects,
				fmt.Sprintf("Patroni stopt de schrijvers op %s, wacht tot %s alles heeft en maakt %s de primary. De app kan een paar seconden niet schrijven "+
					"en verbindt daarna opnieuw.", old, host, host),
				fmt.Sprintf("%s wordt een replica van %s.", old, host))
			if spec.VIP != nil {
				sw.Effects = append(sw.Effects, fmt.Sprintf("Het VIP %s gaat mee naar %s.", spec.VIP.Address, host))
			}
		}
		switch {
		case common != "":
			re.Reason = common
		case p.server == nil || p.server.Patroni == nil || p.server.Patroni.Error != "":
			re.Reason = fmt.Sprintf("Patroni antwoordt niet op %s (%s); start het eerst met systemctl start patroni", host, state)
		case primary < 0:
			re.Reason = "er is geen primary om de data van te kopiëren"
		default:
			re.Allowed = true
			re.Effects = append(re.Effects, fmt.Sprintf("PostgreSQL op %s stopt, gooit zijn data weg en kopieert alles opnieuw van %s (pg_basebackup). "+
				"De primary werkt door.", host, nodes[primary].node.Hostname))
			if n.Sync {
				re.Effects = append(re.Effects, fmt.Sprintf("%s is nu de synchrone replica; zolang de kopie loopt, kiest Patroni een andere, of bevestigt de primary "+
					"schrijfacties zonder tweede kopie.", host))
			}
			re.Effects = append(re.Effects, fmt.Sprintf("Bij een grote database duurt de kopie even; ClusterForge wacht tot %s weer bijhoudt.", host))
		}
		n.Actions = []ManageAction{sw, re}
		v.Nodes = append(v.Nodes, n)
	}
	return nil
}

// isPrimary is true als Patroni zegt dat deze node de primary van het
// cluster slug is en PostgreSQL draait.
func isPrimary(srv *protocol.DBServer, slug string) bool {
	_, ok := patroniHealthy(srv, slug)
	return ok && srv.Patroni.Role == "primary"
}

// patroniProtocol zegt waarom ClusterForge de agent van p Patroni niet kan
// laten beheren.
func patroniProtocol(p pgNode) error {
	if p.protocol < protocol.DBPatroniSince {
		return &ConflictError{fmt.Sprintf("de agent op %s heeft protocol %d; Patroni beheren vraagt protocol %d. Werk hem bij met het installatiescript en --upgrade",
			p.node.Hostname, p.protocol, protocol.DBPatroniSince)}
	}
	return nil
}

// manageHash vat het overzicht samen: wie meedoet, wie primary en
// synchroon is, wie het VIP heeft en wat er kan. De achterstand telt niet,
// die verandert steeds.
func manageHash(r store.ClusterComponent, v ManageView) string {
	type node struct {
		ID       uuid.UUID `json:"id"`
		Answered bool      `json:"answered"`
		Healthy  bool      `json:"healthy"`
		Role     string    `json:"role"`
		Sync     bool      `json:"sync"`
		VIP      bool      `json:"vip"`
		Allowed  []string  `json:"allowed"`
	}
	out := struct {
		Component uuid.UUID `json:"component"`
		Revision  int32     `json:"revision"`
		Nodes     []node    `json:"nodes"`
	}{Component: r.ID, Revision: deref(r.AppliedRevision)}
	for _, n := range v.Nodes {
		x := node{ID: n.NodeID, Answered: n.Answered, Healthy: n.Healthy, Role: n.Role, Sync: n.Sync, VIP: n.VIP}
		for _, a := range n.Actions {
			if a.Allowed {
				x.Allowed = append(x.Allowed, a.Action)
			}
		}
		out.Nodes = append(out.Nodes, x)
	}
	b, _ := json.Marshal(out)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ManageInput is een actie uit Beheer op een node, met de hash van het
// overzicht.
type ManageInput struct {
	Action string
	NodeID uuid.UUID
	Hash   string
}

// manageParams gaan mee met de taak.
type manageParams struct {
	ComponentID uuid.UUID `json:"component_id"`
	Action      string    `json:"action"`
	NodeID      uuid.UUID `json:"node_id"`
}

// ManageAct zet een actie uit Beheer in de wachtrij, als het overzicht nog
// is wat de gebruiker zag en de actie kan.
func (s *Service) ManageAct(ctx context.Context, actor events.Actor, clusterID, id uuid.UUID, in ManageInput) (store.Job, error) {
	if _, ok := manageTitles[in.Action]; !ok {
		return store.Job{}, &InvalidError{"action", "onbekende actie " + in.Action}
	}
	v, err := s.Manage(ctx, clusterID, id)
	if err != nil {
		return store.Job{}, err
	}
	if v.Hash != in.Hash {
		return store.Job{}, ErrStale
	}
	i := slices.IndexFunc(v.Nodes, func(n ManageNode) bool { return n.NodeID == in.NodeID })
	if i < 0 {
		return store.Job{}, &InvalidError{"node_id", "die node hoort niet bij de database"}
	}
	n := v.Nodes[i]
	j := slices.IndexFunc(n.Actions, func(a ManageAction) bool { return a.Action == in.Action })
	if j < 0 {
		return store.Job{}, &ConflictError{fmt.Sprintf("%s kan niet op %s", strings.ToLower(manageTitles[in.Action]), n.Hostname)}
	}
	if a := n.Actions[j]; !a.Allowed {
		return store.Job{}, &ConflictError{a.Reason}
	}
	params := manageParams{ComponentID: id, Action: in.Action, NodeID: in.NodeID}
	var job store.Job
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := s.get(ctx, q, clusterID, id, true); err != nil {
			return err
		}
		job, err = s.jobs.EnqueueForClusterTx(ctx, q, jobs.Spec{
			Kind: ManageKind, Title: fmt.Sprintf("%s: %s in %s op %s", manageTitles[in.Action], n.Hostname, v.Title, v.ClusterName),
			Params: params, ClusterID: &clusterID, Actor: actor,
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
	return job, err
}

// manageSent onthoudt dat de taak de actie al stuurde; een hervatte taak
// wacht dan verder in plaats van het opnieuw te doen. Old is bij een
// switchover de primary van ervoor.
type manageSent struct {
	Sent bool       `json:"sent"`
	At   time.Time  `json:"at"`
	Old  *uuid.UUID `json:"old,omitempty"`
}

func (s *Service) runManage(ctx context.Context, j *jobs.Job) error {
	var p manageParams
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
	i := slices.IndexFunc(spec.Nodes, func(n Node) bool { return n.NodeID == p.NodeID && n.Role == RoleData })
	if i < 0 {
		return errors.New("de node hoort niet meer bij de database")
	}
	n := spec.Nodes[i]
	switch p.Action {
	case ManageResync:
		return s.runResync(ctx, j, actor, comp, spec, n)
	case ManageSwitchover:
		return s.runSwitchover(ctx, j, actor, comp, spec, n)
	case ManageReinit:
		return s.runReinit(ctx, j, actor, comp, spec, n)
	}
	return fmt.Errorf("onbekende actie %q", p.Action)
}

// runResync synchroniseert een Galera-node opnieuw en wacht tot hij weer
// Synced is.
func (s *Service) runResync(ctx context.Context, j *jobs.Job, actor events.Actor, comp store.ClusterComponent, spec Spec, n Node) error {
	slug := spec.Cluster.Slug
	if err := j.Step(ctx, n.Hostname+": opnieuw synchroniseren", func(ctx context.Context, st *jobs.Step) error {
		var sent manageSent
		if !st.State(&sent) || !sent.Sent {
			donors, err := s.donors(ctx, spec, n)
			if err != nil {
				return err
			}
			st.Logf("%s gooit zijn positie weg en haalt alle data opnieuw bij %s", n.Hostname, joinHosts(donors))
			if _, err := deployApply(ctx, s, j, st, n, "-resync", []templates.Step{resyncStep(j.ID)}); err != nil {
				return err
			}
			if err := st.SetState(ctx, manageSent{Sent: true, At: s.Now()}); err != nil {
				return err
			}
		}
		return s.waitSynced(ctx, st, n, slug, 2)
	}); err != nil {
		return err
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if err := s.verifyMember(ctx, st, spec, n); err != nil {
			return err
		}
		if err := s.verifyVIP(ctx, st, spec); err != nil {
			return err
		}
		return s.manageDone(ctx, actor, j, comp, spec, "cluster.database_resynced", map[string]any{"hostname": n.Hostname})
	})
}

// donors zijn de andere datanodes die Synced zijn; zonder kan n niet
// opnieuw synchroniseren.
func (s *Service) donors(ctx context.Context, spec Spec, n Node) ([]string, error) {
	var out []string
	for _, x := range dataNodes(spec, nil) {
		if x.NodeID == n.NodeID {
			continue
		}
		if g, err := s.galera(ctx, x.NodeID); err == nil && healthy(g, spec.Cluster.Slug, 1) {
			out = append(out, x.Hostname)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("geen andere node is Synced; %s opnieuw synchroniseren zou de database stilleggen", n.Hostname)
	}
	return out, nil
}

// verifyMember controleert dat n Synced is in hetzelfde cluster als de
// andere nodes die Synced zijn.
func (s *Service) verifyMember(ctx context.Context, st *jobs.Step, spec Spec, n Node) error {
	slug := spec.Cluster.Slug
	g, err := s.galera(ctx, n.NodeID)
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", n.Hostname, err)
	case !healthy(g, slug, 1):
		return fmt.Errorf("%s is niet Synced in een Primary component", n.Hostname)
	}
	for _, x := range dataNodes(spec, nil) {
		if x.NodeID == n.NodeID {
			continue
		}
		if o, err := s.galera(ctx, x.NodeID); err == nil && healthy(o, slug, 1) && o.ClusterUUID != g.ClusterUUID {
			return fmt.Errorf("%s en %s zitten in verschillende clusters met dezelfde naam (%s en %s)", n.Hostname, x.Hostname, g.ClusterUUID, o.ClusterUUID)
		}
	}
	st.Logf("%s is Synced in het cluster met %d leden", n.Hostname, g.ClusterSize)
	return nil
}

// runSwitchover maakt cand de primary en wacht tot de oude primary een
// replica is die bijhoudt.
func (s *Service) runSwitchover(ctx context.Context, j *jobs.Job, actor events.Actor, comp store.ClusterComponent, spec Spec, cand Node) error {
	slug := spec.Cluster.Slug
	var sent manageSent
	step := cand.Hostname + ": primary maken"
	if err := j.Step(ctx, step, func(ctx context.Context, st *jobs.Step) error {
		st.State(&sent)
		if !sent.Sent {
			srv, _ := s.server(ctx, cand.NodeID, protocol.DBPostgreSQL)
			if isPrimary(srv, slug) {
				st.Logf("%s is al de primary", cand.Hostname)
				if sent.Old == nil {
					sent.Old = &cand.NodeID
				}
			} else {
				leader, err := s.switchoverCheck(ctx, comp.ClusterID, spec, cand)
				if err != nil {
					return err
				}
				sent.Old = &leader.NodeID
				if err := st.SetState(ctx, sent); err != nil {
					return err
				}
				if err := s.switchover(ctx, st, spec, leader, cand); err != nil {
					return err
				}
			}
			sent.Sent, sent.At = true, s.Now()
			if err := st.SetState(ctx, sent); err != nil {
				return err
			}
		}
		return s.waitPrimary(ctx, st, cand, slug)
	}); err != nil {
		return err
	}
	if sent.Old == nil {
		// De stap lukte al bij een eerdere poging.
		j.Done(step, &sent)
	}
	old := cand
	if sent.Old != nil {
		if i := slices.IndexFunc(spec.Nodes, func(n Node) bool { return n.NodeID == *sent.Old }); i >= 0 {
			old = spec.Nodes[i]
		}
	}
	if old.NodeID != cand.NodeID {
		if err := j.Step(ctx, old.Hostname+": replica", func(ctx context.Context, st *jobs.Step) error {
			return s.waitReplica(ctx, st, old, slug, time.Time{})
		}); err != nil {
			return err
		}
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if err := s.checkPrimary(ctx, st, spec, cand.Hostname); err != nil {
			return err
		}
		if spec.VIP != nil {
			if err := s.waitOwnVIP(ctx, st, spec, cand.Hostname); err != nil {
				return err
			}
		}
		return s.manageDone(ctx, actor, j, comp, spec, "cluster.database_switchover", map[string]any{"from": old.Hostname, "to": cand.Hostname})
	})
}

// switchoverCheck kijkt of cand nu primary kan worden, en geeft de primary.
func (s *Service) switchoverCheck(ctx context.Context, clusterID uuid.UUID, spec Spec, cand Node) (Node, error) {
	slug := spec.Cluster.Slug
	nodes, err := s.pgNodes(ctx, clusterID, spec)
	if err != nil {
		return Node{}, err
	}
	var leader *pgNode
	var c pgNode
	for i, p := range nodes {
		if p.err == nil && isPrimary(p.server, slug) {
			leader = &nodes[i]
		}
		if p.node.NodeID == cand.NodeID {
			c = p
		}
	}
	if leader == nil {
		return Node{}, errors.New("er is geen primary om van over te schakelen")
	}
	if c.err != nil {
		return Node{}, fmt.Errorf("%s: %w", cand.Hostname, c.err)
	}
	if err := patroniProtocol(c); err != nil {
		return Node{}, err
	}
	if state, ok := patroniHealthy(c.server, slug); !ok {
		return Node{}, fmt.Errorf("%s is geen replica die bijhoudt (%s)", cand.Hostname, state)
	}
	if !c.server.Patroni.Sync {
		return Node{}, fmt.Errorf("%s is niet de synchrone replica, en alleen die kan primary worden; bekijk Beheer opnieuw", cand.Hostname)
	}
	return leader.node, nil
}

// switchover vraagt Patroni via de agent op cand om cand de primary te
// maken in plaats van leader. Ging het antwoord verloren maar is cand de
// primary, dan is het goed.
func (s *Service) switchover(ctx context.Context, st *jobs.Step, spec Spec, leader, cand Node) error {
	st.Logf("%s is de primary; Patroni schakelt over naar %s", leader.Hostname, cand.Hostname)
	_ = st.Flush(ctx)
	res, err := s.patroniOp(ctx, cand, protocol.DBPatroniRequest{Action: protocol.DBPatroniSwitchover, Leader: leader.Hostname, Candidate: cand.Hostname})
	if err != nil {
		if srv, _ := s.server(ctx, cand.NodeID, protocol.DBPostgreSQL); !isPrimary(srv, spec.Cluster.Slug) {
			return err
		}
		st.Logf("%v; toch is %s nu de primary", err, cand.Hostname)
		return nil
	}
	if res.Message != "" {
		st.Logf("Patroni: %s", res.Message)
	}
	return nil
}

// waitPrimary wacht tot n de primary is.
func (s *Service) waitPrimary(ctx context.Context, st *jobs.Step, n Node, slug string) error {
	return s.waitServer(ctx, st, n.Hostname+" is de primary", n.NodeID, protocol.DBPostgreSQL, func(srv *protocol.DBServer, err error) (string, bool) {
		if err != nil {
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		}
		state, _ := patroniHealthy(srv, slug)
		return n.Hostname + ": " + state, isPrimary(srv, slug)
	})
}

// waitReplica wacht tot n een replica is die bijhoudt. Met since moet
// PostgreSQL daarna opnieuw gestart zijn, zoals na een reinitialize: tot
// Patroni begint, ziet de oude replica er nog gezond uit.
func (s *Service) waitReplica(ctx context.Context, st *jobs.Step, n Node, slug string, since time.Time) error {
	restarted := since.IsZero()
	return s.waitServer(ctx, st, n.Hostname+" is een replica die bijhoudt", n.NodeID, protocol.DBPostgreSQL, func(srv *protocol.DBServer, err error) (string, bool) {
		if err != nil {
			restarted = true
			return fmt.Sprintf("%s: %v", n.Hostname, err), false
		}
		state, ok := patroniHealthy(srv, slug)
		if !ok {
			restarted = true
		} else if !restarted && srv.Running && time.Duration(srv.UptimeSeconds)*time.Second < s.Now().Sub(since) {
			restarted = true
		}
		return n.Hostname + ": " + state, ok && restarted && srv.Patroni.Role != "primary"
	})
}

// checkPrimary controleert dat want de enige primary is, en zegt hoe het
// met de andere nodes staat.
func (s *Service) checkPrimary(ctx context.Context, st *jobs.Step, spec Spec, want string) error {
	slug := spec.Cluster.Slug
	var primaries []string
	for _, n := range dataNodes(spec, nil) {
		srv, err := s.server(ctx, n.NodeID, protocol.DBPostgreSQL)
		if err != nil {
			st.Logf("%s: %v", n.Hostname, err)
			continue
		}
		state, _ := patroniHealthy(srv, slug)
		st.Logf("%s: %s", n.Hostname, state)
		if isPrimary(srv, slug) {
			primaries = append(primaries, n.Hostname)
		}
	}
	switch {
	case len(primaries) > 1:
		return fmt.Errorf("%s zijn allemaal primary (split-brain); kijk of etcd op alle nodes draait", joinHosts(primaries))
	case len(primaries) == 0:
		return errors.New("geen enkele node is de primary")
	case want != "" && primaries[0] != want:
		return fmt.Errorf("%s is de primary in plaats van %s", primaries[0], want)
	}
	return nil
}

// runReinit bouwt een replica van Patroni opnieuw op en wacht tot hij weer
// bijhoudt.
func (s *Service) runReinit(ctx context.Context, j *jobs.Job, actor events.Actor, comp store.ClusterComponent, spec Spec, n Node) error {
	slug := spec.Cluster.Slug
	if err := j.Step(ctx, n.Hostname+": opnieuw opbouwen", func(ctx context.Context, st *jobs.Step) error {
		var sent manageSent
		if !st.State(&sent) || !sent.Sent {
			if err := s.reinitCheck(ctx, comp.ClusterID, spec, n); err != nil {
				return err
			}
			st.Logf("%s gooit zijn data weg en kopieert alles opnieuw van de primary", n.Hostname)
			_ = st.Flush(ctx)
			sent = manageSent{Sent: true, At: s.Now()}
			res, err := s.patroniOp(ctx, n, protocol.DBPatroniRequest{Action: protocol.DBPatroniReinit})
			if err != nil {
				return err
			}
			if res.Message != "" {
				st.Logf("Patroni: %s", res.Message)
			}
			if err := st.SetState(ctx, sent); err != nil {
				return err
			}
		}
		return s.waitReplica(ctx, st, n, slug, sent.At)
	}); err != nil {
		return err
	}
	return j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if err := s.checkPrimary(ctx, st, spec, ""); err != nil {
			return err
		}
		return s.manageDone(ctx, actor, j, comp, spec, "cluster.database_reinitialized", map[string]any{"hostname": n.Hostname})
	})
}

// reinitCheck kijkt of n nu opnieuw opgebouwd kan worden: een replica, met
// een primary om van te kopiëren.
func (s *Service) reinitCheck(ctx context.Context, clusterID uuid.UUID, spec Spec, n Node) error {
	slug := spec.Cluster.Slug
	nodes, err := s.pgNodes(ctx, clusterID, spec)
	if err != nil {
		return err
	}
	leader := ""
	for _, p := range nodes {
		if p.err == nil && isPrimary(p.server, slug) {
			leader = p.node.Hostname
		}
		if p.node.NodeID != n.NodeID {
			continue
		}
		if p.err != nil {
			return fmt.Errorf("%s: %w", n.Hostname, p.err)
		}
		if err := patroniProtocol(p); err != nil {
			return err
		}
		if isPrimary(p.server, slug) {
			return fmt.Errorf("%s is de primary; maak eerst een andere node primary", n.Hostname)
		}
	}
	if leader == "" {
		return errors.New("er is geen primary om de data van te kopiëren")
	}
	return nil
}

// patroniOp stuurt een DBPatroniOp naar de agent op n. Een switchover
// wacht tot de nieuwe primary er is, dus dit mag langer duren dan een
// gewoon databaseverzoek.
func (s *Service) patroniOp(ctx context.Context, n Node, req protocol.DBPatroniRequest) (*protocol.DBPatroniResult, error) {
	timeout := protocol.DBPatroniTimeout + s.Timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := s.bus.DB(ctx, n.NodeID, protocol.DBRequest{
		ID: uuid.NewString(), Op: protocol.DBPatroniOp, Engine: protocol.DBPostgreSQL, Deadline: time.Now().Add(timeout), Patroni: &req,
	})
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s: %w", n.Hostname, err)
	case strings.HasPrefix(res.Error, "onbekend databaseverzoek"):
		return nil, fmt.Errorf("%s: de agent kent dit verzoek nog niet; werk hem bij met het installatiescript en --upgrade", n.Hostname)
	case res.Error != "":
		return nil, fmt.Errorf("%s: %s", n.Hostname, res.Error)
	case res.Patroni == nil:
		return nil, fmt.Errorf("%s: de agent gaf geen antwoord van Patroni", n.Hostname)
	}
	return res.Patroni, nil
}

// manageDone schrijft het event van een gelukte actie.
func (s *Service) manageDone(ctx context.Context, actor events.Actor, j *jobs.Job, comp store.ClusterComponent, spec Spec, action string, payload map[string]any) error {
	payload["name"], payload["component"], payload["job_id"], payload["title"] = spec.Cluster.Name, title(spec), j.ID, j.Title
	if err := s.ev.Write(ctx, s.q, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: comp.ClusterID.String(), ClusterID: &comp.ClusterID, Action: action, Payload: payload,
	}); err != nil {
		return err
	}
	if s.Changed != nil {
		s.Changed()
	}
	return nil
}
