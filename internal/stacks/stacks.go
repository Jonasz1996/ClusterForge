// Package stacks beheert de Docker Compose-projecten van een cluster als
// gewenste staat (fase 21). Een stack is een project met zijn map, zijn
// bestanden (compose, env, kleine configuratie; versleuteld bewaard), waar
// hij draait (op elke node, alleen op de node met het VIP, of op gekozen
// nodes) en of hij moet draaien. De agents schrijven de bestanden, starten
// of stoppen de containers en houden de lijst bij die keepalived (template
// docker-ha) gebruikt. Bestaande projecten neemt ClusterForge over zonder
// iets op de node te veranderen: hij leest hun bestanden in.
package stacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/cronexpr"
	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak die stacks toepast.
const Kind = "stacks.apply"

// SyncPrefix begint de naam van de sync van een stack in Cronjobs.
const SyncPrefix = "cf-stack-"

// Bus stuurt stack- en runverzoeken naar een agent.
type Bus interface {
	Stacks(ctx context.Context, nodeID uuid.UUID, req protocol.StacksRequest) (protocol.StacksResponse, error)
	noderun.Bus
}

// Syncs zet en verwijdert de syncs van stacks (Cronjobs).
type Syncs interface {
	EnsureTemplateSyncs(ctx context.Context, clusterID uuid.UUID, syncs []cronjobs.TemplateSync)
	DeleteJob(ctx context.Context, actor events.Actor, id uuid.UUID) error
}

// Service beheert de stacks.
type Service struct {
	pool   *pgxpool.Pool
	ev     *events.Writer
	runner *jobs.Runner
	bus    Bus
	log    *slog.Logger
	Run    *noderun.Runner
	// Box versleutelt de bestanden; zonder masterkey kan ClusterForge
	// geen stacks bewaren.
	Box   *secrets.Box
	Syncs Syncs
	Now   func() time.Time
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, ev: ev, runner: runner, bus: bus, log: log, Run: &noderun.Runner{Bus: bus}, Now: time.Now}
	if runner != nil {
		runner.Register(Kind, s.apply)
	}
	return s
}

// FieldError is een ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// ErrNotFound: de stack of het cluster bestaat niet.
var ErrNotFound = errors.New("niet gevonden")

// Stack is een stack zoals de API hem toont (zonder bestanden).
type Stack struct {
	ID           uuid.UUID   `json:"id"`
	ClusterID    uuid.UUID   `json:"cluster_id"`
	Name         string      `json:"name"`
	Dir          string      `json:"dir"`
	ComposeFiles []string    `json:"compose_files"`
	Placement    string      `json:"placement"`
	NodeIDs      []uuid.UUID `json:"node_ids"`
	Desired      string      `json:"desired"`
	Critical     bool        `json:"critical"`
	SyncPaths    []string    `json:"sync_paths"`
	SyncSchedule string      `json:"sync_schedule"`
	Revision     int         `json:"revision"`
	Notes        string      `json:"notes"`
	ImportedFrom *uuid.UUID  `json:"imported_from"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	// FileCount is het aantal bewaarde bestanden.
	FileCount int         `json:"file_count"`
	Nodes     []StackNode `json:"nodes"`

	filesEnc []byte
	keyID    string
	sha      string
}

// StackNode is de stand van een stack op één node.
type StackNode struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	// Should: de stack hoort op deze node te draaien.
	Should bool `json:"should"`
	// Revision is de versie die de node laatst kreeg; 0 is nog nooit.
	Revision  int        `json:"revision"`
	AppliedAt *time.Time `json:"applied_at"`
	Error     string     `json:"error"`
	FailedAt  *time.Time `json:"failed_at"`
	// Containers zijn de containers van de stack op de node nu (alleen in
	// het overzicht, als de node antwoordde).
	Containers []protocol.StackContainer `json:"containers"`
}

// Node is een node van het cluster.
type Node struct {
	ID        uuid.UUID `json:"id"`
	Hostname  string    `json:"hostname"`
	Online    bool      `json:"online"`
	Protocol  int       `json:"protocol"`
	Supported bool      `json:"supported"`
	HoldsVIP  bool      `json:"holds_vip"`
	// Docker: de node antwoordde en Docker draait (alleen in het
	// overzicht).
	Docker bool   `json:"docker"`
	Error  string `json:"error"`
}

// Cluster is wat het overzicht van een cluster weet.
type Cluster struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	Slug     string     `json:"slug"`
	Env      string     `json:"environment"`
	Template string     `json:"template"`
	VIP      string     `json:"vip"`
	VIPOwner *uuid.UUID `json:"vip_owner"`
}

// Unmanaged is een Compose-project op een node dat ClusterForge nog niet
// beheert.
type Unmanaged struct {
	NodeID     uuid.UUID                 `json:"node_id"`
	Hostname   string                    `json:"hostname"`
	Project    protocol.StackProjectInfo `json:"project"`
	Containers []protocol.StackContainer `json:"containers"`
	// SyncPaths is wat ClusterForge als sync zou voorstellen.
	SyncPaths []string `json:"sync_paths"`
}

// Overview is de pagina Containers van één cluster.
type Overview struct {
	Cluster   Cluster     `json:"cluster"`
	Nodes     []Node      `json:"nodes"`
	Stacks    []Stack     `json:"stacks"`
	Unmanaged []Unmanaged `json:"unmanaged"`
	// Others zijn containers zonder Compose-project, per node.
	Others []Unmanaged `json:"others"`
	// Available: er is een masterkey om stacks te bewaren.
	Available bool `json:"available"`
	// JobID is een stacktaak die nu loopt of wacht.
	JobID *uuid.UUID `json:"job_id"`
}

func (s *Service) event(ctx context.Context, actor events.Actor, clusterID uuid.UUID, subject, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	cid := clusterID
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "docker_stack", SubjectID: subject, ClusterID: &cid, Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// --- lezen ------------------------------------------------------------------

func (s *Service) cluster(ctx context.Context, id uuid.UUID) (Cluster, error) {
	var c Cluster
	err := s.pool.QueryRow(ctx, `SELECT id, name, slug, environment::text, coalesce(template_name, '') FROM clusters WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.Slug, &c.Env, &c.Template)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	// Het eerste VIP van het cluster (template docker-ha heeft er één).
	var addr netip.Addr
	err = s.pool.QueryRow(ctx, `SELECT address, owner_node_id FROM vips WHERE cluster_id = $1 ORDER BY created_at, address LIMIT 1`, id).Scan(&addr, &c.VIPOwner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return c, err
	default:
		c.VIP = addr.String()
	}
	return c, nil
}

func (s *Service) nodes(ctx context.Context, c Cluster) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT n.id, n.hostname, coalesce(a.protocol_version, 0), st.heartbeat_at
		FROM nodes n
		LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
		LEFT JOIN node_status st ON st.node_id = n.id
		WHERE n.cluster_id = $1 ORDER BY lower(n.hostname)`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var out []Node
	for rows.Next() {
		var n Node
		var hb *time.Time
		if err := rows.Scan(&n.ID, &n.Hostname, &n.Protocol, &hb); err != nil {
			return nil, err
		}
		n.Online = hb != nil && now.Sub(*hb) <= status.HeartbeatLate
		n.Supported = n.Protocol >= protocol.StacksSince
		n.HoldsVIP = c.VIPOwner != nil && *c.VIPOwner == n.ID
		out = append(out, n)
	}
	return out, rows.Err()
}

const stackCols = `id, cluster_id, name, dir, compose_files, placement, node_ids, desired, critical, sync_paths, sync_schedule,
	revision, notes, imported_from, created_at, updated_at, files_enc, files_key_id, files_sha256`

func scanStack(row pgx.Row) (Stack, error) {
	var st Stack
	err := row.Scan(&st.ID, &st.ClusterID, &st.Name, &st.Dir, &st.ComposeFiles, &st.Placement, &st.NodeIDs, &st.Desired, &st.Critical,
		&st.SyncPaths, &st.SyncSchedule, &st.Revision, &st.Notes, &st.ImportedFrom, &st.CreatedAt, &st.UpdatedAt, &st.filesEnc, &st.keyID, &st.sha)
	return st, err
}

func (s *Service) listStacks(ctx context.Context, clusterID uuid.UUID) ([]Stack, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+stackCols+` FROM docker_stacks WHERE cluster_id = $1 ORDER BY name`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stack
	for rows.Next() {
		st, err := scanStack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Get geeft één stack.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Stack, error) {
	st, err := scanStack(s.pool.QueryRow(ctx, `SELECT `+stackCols+` FROM docker_stacks WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}

// should zegt of stack st op node n hoort te draaien.
func should(st Stack, n Node, c Cluster, nodeCount int) bool {
	switch st.Placement {
	case protocol.PlaceNode:
		return slices.Contains(st.NodeIDs, n.ID)
	case protocol.PlaceVIP:
		// Zonder VIP (één node) is vip hetzelfde als alle.
		if c.VIP == "" || nodeCount <= 1 {
			return true
		}
		return n.HoldsVIP
	}
	return true
}

// present zegt of stack st op node n hoort (bestanden en de lijst).
func present(st Stack, n Node) bool {
	return st.Placement != protocol.PlaceNode || slices.Contains(st.NodeIDs, n.ID)
}

func (s *Service) fillNodes(ctx context.Context, sts []Stack, nodes []Node, c Cluster) error {
	type key struct{ s, n uuid.UUID }
	got := map[key]StackNode{}
	rows, err := s.pool.Query(ctx, `SELECT sn.stack_id, sn.node_id, sn.revision, sn.applied_at, sn.error, sn.failed_at
		FROM docker_stack_nodes sn JOIN docker_stacks d ON d.id = sn.stack_id WHERE d.cluster_id = $1`, c.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sid uuid.UUID
		var sn StackNode
		if err := rows.Scan(&sid, &sn.NodeID, &sn.Revision, &sn.AppliedAt, &sn.Error, &sn.FailedAt); err != nil {
			rows.Close()
			return err
		}
		got[key{sid, sn.NodeID}] = sn
	}
	rows.Close()
	for i := range sts {
		files, _ := s.openFiles(sts[i])
		sts[i].FileCount = len(files)
		sts[i].Nodes = []StackNode{}
		for _, n := range nodes {
			if !present(sts[i], n) {
				continue
			}
			sn := got[key{sts[i].ID, n.ID}]
			sn.NodeID, sn.Hostname = n.ID, n.Hostname
			sn.Should = should(sts[i], n, c, len(nodes))
			sn.Containers = []protocol.StackContainer{}
			sts[i].Nodes = append(sts[i].Nodes, sn)
		}
	}
	return nil
}

// Overview geeft de stacks van een cluster met wat er nu op de nodes draait.
func (s *Service) Overview(ctx context.Context, clusterID uuid.UUID) (Overview, error) {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return Overview{}, err
	}
	nodes, err := s.nodes(ctx, c)
	if err != nil {
		return Overview{}, err
	}
	sts, err := s.listStacks(ctx, clusterID)
	if err != nil {
		return Overview{}, err
	}
	if err := s.fillNodes(ctx, sts, nodes, c); err != nil {
		return Overview{}, err
	}
	o := Overview{Cluster: c, Nodes: nodes, Stacks: sts, Unmanaged: []Unmanaged{}, Others: []Unmanaged{}, Available: s.Box != nil}
	if o.Stacks == nil {
		o.Stacks = []Stack{}
	}
	var jid uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM jobs WHERE kind = $1 AND cluster_id = $2 AND status IN ('queued', 'running')
		ORDER BY created_at DESC LIMIT 1`, Kind, clusterID).Scan(&jid); err == nil {
		o.JobID = &jid
	}
	// De nodes tegelijk bevragen.
	type answer struct {
		i   int
		res protocol.StacksResponse
		err error
	}
	ch := make(chan answer, len(nodes))
	asked := 0
	for i, n := range nodes {
		if !n.Online || !n.Supported {
			switch {
			case !n.Online:
				o.Nodes[i].Error = "offline"
			default:
				o.Nodes[i].Error = fmt.Sprintf("agent te oud (protocol %d nodig; Nodes → Agents bijwerken)", protocol.StacksSince)
			}
			continue
		}
		asked++
		go func(i int, id uuid.UUID) {
			rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			res, err := s.bus.Stacks(rctx, id, protocol.StacksRequest{Op: protocol.StacksOpList})
			ch <- answer{i, res, err}
		}(i, n.ID)
	}
	byName := map[string]int{}
	for i, st := range o.Stacks {
		byName[st.Name] = i
	}
	for range asked {
		a := <-ch
		n := &o.Nodes[a.i]
		switch {
		case a.err != nil:
			n.Error = a.err.Error()
			continue
		case a.res.Error != "":
			n.Error = a.res.Error
			continue
		}
		n.Docker = a.res.Docker
		for _, ct := range a.res.Containers {
			if si, ok := byName[ct.Project]; ok && ct.Project != "" {
				st := &o.Stacks[si]
				for j := range st.Nodes {
					if st.Nodes[j].NodeID == n.ID {
						st.Nodes[j].Containers = append(st.Nodes[j].Containers, ct)
					}
				}
				continue
			}
			if ct.Project == "" {
				o.Others = appendOther(o.Others, *n, ct)
			}
		}
		for _, p := range a.res.Projects {
			if _, ok := byName[p.Name]; ok {
				continue
			}
			u := Unmanaged{NodeID: n.ID, Hostname: n.Hostname, Project: p}
			for _, ct := range a.res.Containers {
				if ct.Project == p.Name {
					u.Containers = append(u.Containers, ct)
				}
			}
			u.SyncPaths = ProposeSyncPaths(p.WorkingDir, u.Containers)
			o.Unmanaged = append(o.Unmanaged, u)
		}
	}
	slices.SortFunc(o.Unmanaged, func(a, b Unmanaged) int {
		return strings.Compare(a.Project.Name+"/"+a.Hostname, b.Project.Name+"/"+b.Hostname)
	})
	return o, nil
}

func appendOther(xs []Unmanaged, n Node, ct protocol.StackContainer) []Unmanaged {
	for i := range xs {
		if xs[i].NodeID == n.ID {
			xs[i].Containers = append(xs[i].Containers, ct)
			return xs
		}
	}
	return append(xs, Unmanaged{NodeID: n.ID, Hostname: n.Hostname, Containers: []protocol.StackContainer{ct}})
}

// skipSync zijn mappen die nooit mee mogen in een sync.
var skipSync = []string{"/", "/etc", "/var", "/var/run", "/run", "/proc", "/sys", "/dev", "/usr", "/root", "/home", "/tmp", "/mnt", "/opt", "/srv"}

// ProposeSyncPaths stelt de mappen voor die de data van een stack hebben:
// de map van het project, gemapte mappen erbuiten en de named volumes.
// Sockets, losse bestanden buiten de map en systeemmappen vallen weg.
func ProposeSyncPaths(dir string, cs []protocol.StackContainer) []string {
	out := []string{}
	add := func(p string) {
		p = path.Clean(p)
		if slices.Contains(skipSync, p) || strings.HasPrefix(p, "/proc/") || strings.HasPrefix(p, "/sys/") || strings.HasPrefix(p, "/dev/") ||
			strings.HasPrefix(p, "/run/") || strings.HasPrefix(p, "/var/run/") || strings.HasPrefix(p, "/etc/") {
			return
		}
		for _, q := range out {
			if q == p || strings.HasPrefix(p, q+"/") {
				return
			}
		}
		// Een bredere map vervangt de smallere.
		out = slices.DeleteFunc(out, func(q string) bool { return strings.HasPrefix(q, p+"/") })
		out = append(out, p)
	}
	if protocol.ValidStackDir(dir) {
		add(dir)
	}
	for _, c := range cs {
		for _, m := range c.Mounts {
			switch {
			case m.Type == "volume" && strings.HasPrefix(m.Source, "/var/lib/docker/volumes/"):
				add(m.Source)
			case m.Type == "bind" && path.Ext(m.Source) == "" && !strings.HasSuffix(m.Source, ".sock"):
				add(m.Source)
			}
		}
	}
	slices.Sort(out)
	return out
}

// --- bestanden ---------------------------------------------------------------

func aad(id uuid.UUID) []byte { return []byte("docker-stack:" + id.String()) }

func (s *Service) sealFiles(id uuid.UUID, files []protocol.StackFile) ([]byte, string, string, error) {
	if s.Box == nil {
		return nil, "", "", &FieldError{"ClusterForge heeft geen masterkey (CF_MASTER_KEY); zonder kan hij de bestanden met wachtwoorden niet veilig bewaren"}
	}
	slices.SortFunc(files, func(a, b protocol.StackFile) int { return strings.Compare(a.Path, b.Path) })
	data, err := json.Marshal(files)
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(data)
	return s.Box.Seal(data, aad(id)), s.Box.KeyID, hex.EncodeToString(sum[:]), nil
}

func (s *Service) openFiles(st Stack) ([]protocol.StackFile, error) {
	if s.Box == nil {
		return nil, &FieldError{"ClusterForge heeft geen masterkey (CF_MASTER_KEY)"}
	}
	data, err := s.Box.Open(st.filesEnc, aad(st.ID), st.keyID)
	if err != nil {
		return nil, err
	}
	var files []protocol.StackFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, err
	}
	return files, nil
}

// MaskedFile is een bestand zoals de editor het toont.
type MaskedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
	Secrets int    `json:"secrets"`
}

// Revision is een versie van de bestanden.
type Revision struct {
	Revision  int        `json:"revision"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy *uuid.UUID `json:"created_by"`
	Username  string     `json:"username"`
}

// Detail is een stack met zijn bestanden (gemaskeerd) en versies.
type Detail struct {
	Stack     Stack        `json:"stack"`
	Files     []MaskedFile `json:"files"`
	Revisions []Revision   `json:"revisions"`
}

// Detail geeft een stack met zijn bestanden; geheime waarden zijn
// plaatshouders.
func (s *Service) Detail(ctx context.Context, id uuid.UUID) (Detail, error) {
	st, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	files, err := s.openFiles(st)
	if err != nil {
		return Detail{}, err
	}
	st.FileCount = len(files)
	st.Nodes = []StackNode{}
	d := Detail{Stack: st, Files: []MaskedFile{}, Revisions: []Revision{}}
	for _, f := range files {
		m, sec := protocol.MaskFile(f.Content)
		d.Files = append(d.Files, MaskedFile{Path: f.Path, Content: m, Mode: f.Mode, Secrets: len(sec)})
	}
	rows, err := s.pool.Query(ctx, `SELECT r.revision, r.note, r.created_at, r.created_by, coalesce(u.username, '')
		FROM docker_stack_revisions r LEFT JOIN users u ON u.id = r.created_by WHERE r.stack_id = $1 ORDER BY r.revision DESC LIMIT 50`, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.Revision, &r.Note, &r.CreatedAt, &r.CreatedBy, &r.Username); err != nil {
			return d, err
		}
		d.Revisions = append(d.Revisions, r)
	}
	return d, rows.Err()
}

// --- overnemen ---------------------------------------------------------------

// ImportItem is een project dat ClusterForge overneemt.
type ImportItem struct {
	Name      string      `json:"name"`
	Placement string      `json:"placement"`
	NodeIDs   []uuid.UUID `json:"node_ids"`
	Critical  bool        `json:"critical"`
	// SyncPaths nil is het voorstel van ClusterForge.
	SyncPaths []string `json:"sync_paths"`
}

// ImportResult zegt per project wat er gebeurde.
type ImportResult struct {
	Name    string     `json:"name"`
	StackID *uuid.UUID `json:"stack_id"`
	Files   []string   `json:"files"`
	Skipped []string   `json:"skipped"`
	Error   string     `json:"error"`
}

// Import neemt projecten van een node over: de agent leest hun bestanden en
// ClusterForge bewaart ze versleuteld. Op de node verandert niets.
func (s *Service) Import(ctx context.Context, actor events.Actor, userID *uuid.UUID, clusterID, nodeID uuid.UUID, items []ImportItem) ([]ImportResult, error) {
	c, err := s.cluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodes(ctx, c)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(nodes, func(n Node) bool { return n.ID == nodeID })
	switch {
	case i < 0:
		return nil, &FieldError{"die node hoort niet bij dit cluster"}
	case !nodes[i].Online:
		return nil, &FieldError{nodes[i].Hostname + " is offline"}
	case !nodes[i].Supported:
		return nil, &FieldError{fmt.Sprintf("%s: agent te oud (protocol %d nodig; Nodes → Agents bijwerken)", nodes[i].Hostname, protocol.StacksSince)}
	case s.Box == nil:
		return nil, &FieldError{"ClusterForge heeft geen masterkey (CF_MASTER_KEY); zonder kan hij de bestanden met wachtwoorden niet veilig bewaren"}
	case len(items) == 0:
		return nil, &FieldError{"kies minstens één project"}
	}
	node := nodes[i]
	var out []ImportResult
	for _, it := range items {
		r := ImportResult{Name: it.Name, Files: []string{}, Skipped: []string{}}
		id, err := s.importOne(ctx, actor, userID, c, nodes, node, it, &r)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.StackID = &id
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) checkPlacement(placement string, ids []uuid.UUID, nodes []Node) error {
	switch placement {
	case protocol.PlaceAll, protocol.PlaceVIP:
	case protocol.PlaceNode:
		if len(ids) == 0 {
			return &FieldError{"kies bij plaatsing node minstens één node"}
		}
		for _, id := range ids {
			if !slices.ContainsFunc(nodes, func(n Node) bool { return n.ID == id }) {
				return &FieldError{"een gekozen node hoort niet bij dit cluster"}
			}
		}
	default:
		return &FieldError{"plaatsing is alle, vip of node"}
	}
	return nil
}

func checkSyncPaths(ps []string) error {
	if len(ps) > 20 {
		return &FieldError{"hoogstens 20 syncmappen per stack"}
	}
	for _, p := range ps {
		if !protocol.ValidSyncPath(p+"/") || slices.Contains(skipSync, path.Clean(p)) || path.Clean(p) != p {
			return &FieldError{"ongeldige syncmap " + p}
		}
	}
	return nil
}

func (s *Service) importOne(ctx context.Context, actor events.Actor, userID *uuid.UUID, c Cluster, nodes []Node, node Node, it ImportItem, r *ImportResult) (uuid.UUID, error) {
	if !protocol.ValidStackName(it.Name) {
		return uuid.Nil, &FieldError{"ongeldige projectnaam"}
	}
	if it.Placement == "" {
		it.Placement = protocol.PlaceAll
		if c.VIP != "" && len(nodes) > 1 {
			it.Placement = protocol.PlaceVIP
		}
	}
	if it.Placement == protocol.PlaceNode && len(it.NodeIDs) == 0 {
		it.NodeIDs = []uuid.UUID{node.ID}
	}
	if err := s.checkPlacement(it.Placement, it.NodeIDs, nodes); err != nil {
		return uuid.Nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, protocol.StacksRequestTimeout)
	defer cancel()
	res, err := s.bus.Stacks(rctx, node.ID, protocol.StacksRequest{Op: protocol.StacksOpList, Project: it.Name, Files: true})
	if err != nil {
		return uuid.Nil, err
	}
	if res.Error != "" {
		return uuid.Nil, errors.New(res.Error)
	}
	pi := slices.IndexFunc(res.Projects, func(p protocol.StackProjectInfo) bool { return p.Name == it.Name })
	if pi < 0 {
		return uuid.Nil, &FieldError{"project " + it.Name + " staat niet op " + node.Hostname}
	}
	p := res.Projects[pi]
	if err := protocol.ValidateStackFiles(res.Files); err != nil {
		return uuid.Nil, err
	}
	var compose []string
	for _, f := range p.ConfigFiles {
		rel, ok := strings.CutPrefix(f, p.WorkingDir+"/")
		if !ok || !protocol.ValidStackFile(rel) {
			return uuid.Nil, &FieldError{f + " staat niet in de map van het project"}
		}
		compose = append(compose, rel)
	}
	if len(compose) == 0 {
		return uuid.Nil, &FieldError{"het project heeft geen compose-bestand"}
	}
	syncPaths := it.SyncPaths
	if syncPaths == nil {
		syncPaths = ProposeSyncPaths(p.WorkingDir, res.Containers)
	}
	if err := checkSyncPaths(syncPaths); err != nil {
		return uuid.Nil, err
	}
	for _, f := range res.Files {
		r.Files = append(r.Files, f.Path)
	}
	r.Skipped = append(r.Skipped, res.Skipped...)
	id := uuid.New()
	enc, keyID, sha, err := s.sealFiles(id, res.Files)
	if err != nil {
		return uuid.Nil, err
	}
	note := "overgenomen van " + node.Hostname
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO docker_stacks (id, cluster_id, name, dir, compose_files, files_enc, files_key_id, files_sha256,
			placement, node_ids, critical, sync_paths, imported_from, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)`,
			id, c.ID, it.Name, p.WorkingDir, compose, enc, keyID, sha, it.Placement, nonNilIDs(it.NodeIDs), it.Critical, syncPaths, node.ID, userID); err != nil {
			if strings.Contains(err.Error(), "docker_stacks_cluster_id_name_key") {
				return &FieldError{"ClusterForge beheert " + it.Name + " al in dit cluster"}
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO docker_stack_revisions (stack_id, revision, files_enc, files_key_id, files_sha256, note, created_by)
			VALUES ($1, 1, $2, $3, $4, $5, $6)`, id, enc, keyID, sha, note, userID); err != nil {
			return err
		}
		// De bron heeft deze versie al.
		_, err := tx.Exec(ctx, `INSERT INTO docker_stack_nodes (stack_id, node_id, revision, applied_at) VALUES ($1, $2, 1, now())`, id, node.ID)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.event(ctx, actor, c.ID, id.String(), "stack.imported", map[string]any{
		"name": it.Name, "node": node.Hostname, "dir": p.WorkingDir, "files": r.Files, "placement": it.Placement,
	})
	return id, nil
}

func nonNilIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// --- aanpassen ---------------------------------------------------------------

// Update is een wijziging van een stack. Files nil laat de bestanden
// zoals ze zijn; anders zijn het alle bestanden, met de plaatshouders van
// Detail.
type Update struct {
	Files        []MaskedFile `json:"files"`
	ComposeFiles []string     `json:"compose_files"`
	Placement    string       `json:"placement"`
	NodeIDs      []uuid.UUID  `json:"node_ids"`
	Desired      string       `json:"desired"`
	// Critical nil laat het zoals het is.
	Critical     *bool    `json:"critical"`
	SyncPaths    []string `json:"sync_paths"`
	SyncSchedule string   `json:"sync_schedule"`
	Notes        string   `json:"notes"`
	// Note zegt bij nieuwe bestanden wat er veranderde.
	Note string `json:"note"`
}

// Save past een stack aan. Wat op de nodes moet veranderen, doet pas
// Toepassen.
func (s *Service) Save(ctx context.Context, actor events.Actor, userID *uuid.UUID, id uuid.UUID, in Update) (Stack, error) {
	st, err := s.Get(ctx, id)
	if err != nil {
		return st, err
	}
	c, err := s.cluster(ctx, st.ClusterID)
	if err != nil {
		return st, err
	}
	nodes, err := s.nodes(ctx, c)
	if err != nil {
		return st, err
	}
	if in.Placement == "" {
		in.Placement = st.Placement
	}
	if in.Desired == "" {
		in.Desired = st.Desired
	}
	if in.SyncSchedule == "" {
		in.SyncSchedule = st.SyncSchedule
	}
	if in.ComposeFiles == nil {
		in.ComposeFiles = st.ComposeFiles
	}
	if in.SyncPaths == nil {
		in.SyncPaths = st.SyncPaths
	}
	critical := st.Critical
	if in.Critical != nil {
		critical = *in.Critical
	}
	if in.Placement != protocol.PlaceNode {
		in.NodeIDs = nil
	}
	switch {
	case in.Desired != "running" && in.Desired != "stopped":
		return st, &FieldError{"gewenste stand is running of stopped"}
	case len(in.Notes) > 2000 || len(in.Note) > 300:
		return st, &FieldError{"de notitie is te lang"}
	}
	if _, err := cronexpr.Parse(in.SyncSchedule); err != nil {
		return st, &FieldError{"ongeldig schema van de sync: " + err.Error()}
	}
	if err := s.checkPlacement(in.Placement, in.NodeIDs, nodes); err != nil {
		return st, err
	}
	if err := checkSyncPaths(in.SyncPaths); err != nil {
		return st, err
	}
	for _, f := range in.ComposeFiles {
		if !protocol.ValidStackFile(f) {
			return st, &FieldError{"ongeldig compose-bestand " + f}
		}
	}
	if len(in.ComposeFiles) == 0 {
		return st, &FieldError{"minstens één compose-bestand"}
	}
	enc, keyID, sha := st.filesEnc, st.keyID, st.sha
	newRev := st.Revision
	if in.Files != nil {
		cur, err := s.openFiles(st)
		if err != nil {
			return st, err
		}
		var files []protocol.StackFile
		for _, f := range in.Files {
			var secrets []string
			old := slices.IndexFunc(cur, func(c protocol.StackFile) bool { return c.Path == f.Path })
			if old >= 0 {
				_, secrets = protocol.MaskFile(cur[old].Content)
			}
			content, err := protocol.UnmaskFile(f.Content, secrets)
			if err != nil {
				return st, &FieldError{f.Path + ": " + err.Error()}
			}
			mode := f.Mode
			if mode == 0 && old >= 0 {
				mode = cur[old].Mode
			}
			files = append(files, protocol.StackFile{Path: f.Path, Content: content, Mode: mode})
		}
		if err := protocol.ValidateStackFiles(files); err != nil {
			return st, &FieldError{err.Error()}
		}
		for _, cf := range in.ComposeFiles {
			if !slices.ContainsFunc(files, func(f protocol.StackFile) bool { return f.Path == cf }) {
				return st, &FieldError{"het compose-bestand " + cf + " staat niet bij de bestanden"}
			}
		}
		e, k, h, err := s.sealFiles(st.ID, files)
		if err != nil {
			return st, err
		}
		if h != st.sha {
			enc, keyID, sha = e, k, h
			newRev++
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE docker_stacks SET compose_files = $2, files_enc = $3, files_key_id = $4, files_sha256 = $5,
			placement = $6, node_ids = $7, desired = $8, critical = $9, sync_paths = $10, sync_schedule = $11, notes = $12,
			revision = $13, updated_at = now(), updated_by = $14 WHERE id = $1`,
			id, in.ComposeFiles, enc, keyID, sha, in.Placement, nonNilIDs(in.NodeIDs), in.Desired, critical, in.SyncPaths,
			in.SyncSchedule, in.Notes, newRev, userID); err != nil {
			return err
		}
		if newRev == st.Revision {
			return nil
		}
		note := strings.TrimSpace(in.Note)
		if note == "" {
			note = "aangepast in de GUI"
		}
		_, err := tx.Exec(ctx, `INSERT INTO docker_stack_revisions (stack_id, revision, files_enc, files_key_id, files_sha256, note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, newRev, enc, keyID, sha, note, userID)
		return err
	})
	if err != nil {
		return st, err
	}
	s.event(ctx, actor, st.ClusterID, id.String(), "stack.saved", map[string]any{
		"name": st.Name, "revision": newRev, "files_changed": newRev != st.Revision, "placement": in.Placement, "desired": in.Desired, "critical": critical,
	})
	return s.Get(ctx, id)
}

// Delete laat een stack los: ClusterForge beheert hem niet meer, op de
// nodes blijft alles staan en draaien. De lijst van keepalived en de syncs
// past een taak aan.
func (s *Service) Delete(ctx context.Context, actor events.Actor, id uuid.UUID) (*uuid.UUID, error) {
	st, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM docker_stacks WHERE id = $1`, id); err != nil {
		return nil, err
	}
	s.event(ctx, actor, st.ClusterID, id.String(), "stack.released", map[string]any{"name": st.Name})
	jid, err := s.Enqueue(ctx, actor, st.ClusterID, Params{Action: ActionLists, Title: "Containers: " + st.Name + " losgelaten"})
	if err != nil {
		var be jobs.BusyError
		if errors.As(err, &be) {
			return nil, nil
		}
		return nil, err
	}
	return &jid, nil
}

// Logs geeft de laatste regels van een container op een node.
func (s *Service) Logs(ctx context.Context, clusterID, nodeID uuid.UUID, container string, tail int) (string, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = $1 AND cluster_id = $2)`, nodeID, clusterID).Scan(&ok); err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}
	if !protocol.ValidContainerName(container) {
		return "", &FieldError{"ongeldige containernaam"}
	}
	res, err := s.bus.Stacks(ctx, nodeID, protocol.StacksRequest{Op: protocol.StacksOpLogs, Container: container, Tail: tail})
	if err != nil {
		return "", err
	}
	if res.Error != "" {
		return "", errors.New(res.Error)
	}
	return res.Logs, nil
}
