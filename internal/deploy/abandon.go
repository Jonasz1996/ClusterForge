package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// CleanupKind ruimt een mislukte uitrol op (fase 48, 4.2): de CT's en VM's
// die de uitrol maakte, de nodes, het VIP en het cluster zelf. Daarna is de
// slug weer vrij en zijn de adressen niet meer in gebruik.
const CleanupKind = "cluster.deploy_cleanup"

// ErrNotDeploy is een taak die geen uitrol is.
var ErrNotDeploy = errors.New("deze taak is geen uitrol")

// FailedNode is een node van een mislukte uitrol.
type FailedNode struct {
	ID        uuid.UUID `json:"id"`
	Hostname  string    `json:"hostname"`
	Address   string    `json:"address,omitempty"`
	Role      string    `json:"role"`
	Lifecycle string    `json:"lifecycle"`
	// Host en VMID zijn de Proxmox-host en het nummer van de CT of VM die
	// de uitrol maakte; VMID 0 is er (nog) geen.
	Host string `json:"host,omitempty"`
	VMID int    `json:"vmid,omitempty"`
	// Existing is een node die al bestond: die gaat alleen uit het cluster.
	Existing bool `json:"existing"`
	// Gone zegt dat de node al uit ClusterForge is.
	Gone bool `json:"gone"`
}

// Failed is een mislukte uitrol en wat er op te ruimen valt.
type Failed struct {
	JobID       uuid.UUID    `json:"job_id"`
	Status      string       `json:"status"`
	Error       string       `json:"error"`
	ClusterID   uuid.UUID    `json:"cluster_id"`
	ClusterName string       `json:"cluster_name"`
	Slug        string       `json:"slug"`
	VIP         string       `json:"vip,omitempty"`
	Exists      bool         `json:"cluster_exists"`
	Nodes       []FailedNode `json:"nodes"`
	// CanClean zegt of Opruimen kan; Why zegt anders waarom niet.
	CanClean bool   `json:"can_clean"`
	Why      string `json:"why,omitempty"`
	// Cleanup is de laatste opruimtaak van deze uitrol.
	Cleanup *uuid.UUID `json:"cleanup_job_id,omitempty"`
	// Request is de aanvraag zoals de wizard hem invult, zonder geheimen;
	// Secrets zijn de namen van de geheimen die opnieuw moeten.
	Request map[string]any `json:"request"`
	Secrets []string       `json:"secrets"`
}

// FailedDeploy zegt wat er van een uitrol over is, en geeft de aanvraag
// terug om opnieuw te proberen met andere instellingen.
func (s *Service) FailedDeploy(ctx context.Context, jobID uuid.UUID) (Failed, error) {
	jr, err := s.q.GetJob(ctx, jobID)
	if err != nil {
		return Failed{}, err
	}
	j := jr.Job
	if j.Kind != Kind {
		return Failed{}, ErrNotDeploy
	}
	var p params
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return Failed{}, err
	}
	f := Failed{
		JobID: j.ID, Status: string(j.Status), Error: j.Error, ClusterID: p.ClusterID, ClusterName: p.Cluster.Name,
		Slug: p.Cluster.Slug, VIP: p.VIP, Nodes: []FailedNode{}, Request: p.request(), Secrets: append([]string{}, p.Secrets...),
	}
	slices.Sort(f.Secrets)
	if c, err := s.q.GetCluster(ctx, p.ClusterID); err == nil {
		f.Exists, f.ClusterName = true, c.Name
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Failed{}, err
	}
	if f.Nodes, err = s.failedNodes(ctx, p); err != nil {
		return Failed{}, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT id FROM jobs WHERE kind = $1 AND params->>'job_id' = $2
		ORDER BY created_at DESC LIMIT 1`, CleanupKind, j.ID.String()).Scan(&f.Cleanup); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Failed{}, err
	}
	f.Why = s.whyNot(ctx, j, f)
	f.CanClean = f.Why == ""
	return f, nil
}

// request is de aanvraag in de vorm van de API (DeployInput), zonder
// geheimen.
func (p params) request() map[string]any {
	values := map[string]any{}
	for k, v := range p.Params {
		if !slices.Contains(p.Secrets, k) {
			values[k] = v
		}
	}
	out := map[string]any{
		"template": p.Template,
		"version":  p.Version,
		"cluster":  map[string]any{"name": p.Cluster.Name, "slug": p.Cluster.Slug, "environment": p.Cluster.Environment},
		"params":   values,
	}
	if p.Target.Network == NetworkExisting {
		nodes := []map[string]any{}
		for _, n := range p.Nodes {
			nodes = append(nodes, map[string]any{"node_id": n.ID, "role": n.Role})
		}
		out["existing_nodes"] = nodes
		return out
	}
	out["target"] = p.Target
	out["server_url"] = p.ServerURL
	return out
}

func (s *Service) failedNodes(ctx context.Context, p params) ([]FailedNode, error) {
	out := []FailedNode{}
	for _, n := range p.Nodes {
		fn := FailedNode{ID: n.ID, Hostname: n.Hostname, Address: n.Address, Role: n.Role, Host: n.Host,
			Existing: p.Target.Network == NetworkExisting}
		nr, err := s.q.GetNode(ctx, n.ID)
		row := nr.Node
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			fn.Gone = true
		case err != nil:
			return nil, err
		default:
			fn.Lifecycle = string(row.Lifecycle)
			if row.ClusterID == nil || *row.ClusterID != p.ClusterID {
				// Hij is intussen ergens anders: niet van deze uitrol.
				fn.Gone = true
			}
			if row.PveVmid != nil {
				fn.VMID = int(*row.PveVmid)
			}
		}
		out = append(out, fn)
	}
	return out, nil
}

func (s *Service) whyNot(ctx context.Context, j store.Job, f Failed) string {
	switch j.Status {
	case store.JobStatusFailed, store.JobStatusCanceled:
	case store.JobStatusSucceeded:
		return "deze uitrol is gelukt; verwijder het cluster op zijn pagina als je het niet meer wilt"
	default:
		return "de uitrol loopt nog; wacht tot hij klaar is of annuleer hem"
	}
	if f.Cleanup != nil {
		if c, err := s.q.GetJob(ctx, *f.Cleanup); err == nil && (c.Job.Status == store.JobStatusQueued || c.Job.Status == store.JobStatusRunning) {
			return "het opruimen loopt al"
		}
	}
	if !f.Exists && !slices.ContainsFunc(f.Nodes, func(n FailedNode) bool { return !n.Gone }) {
		return "er is niets meer op te ruimen"
	}
	if f.Exists {
		var busy bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE cluster_id = $1 AND id <> $2
			AND status IN ('queued', 'running'))`, f.ClusterID, j.ID).Scan(&busy); err == nil && busy {
			return "er loopt nog een taak op dit cluster"
		}
		// Een latere uitrol (opnieuw proberen) is de stand van het cluster.
		var later bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE cluster_id = $1 AND kind = $2
			AND created_at > $3)`, f.ClusterID, Kind, j.CreatedAt).Scan(&later); err == nil && later {
			return "er is na deze een nieuwere uitrol van dit cluster; ruim die op"
		}
	}
	for _, n := range f.Nodes {
		if !n.Gone && !n.Existing && n.Lifecycle == string(store.NodeLifecycleActive) {
			return fmt.Sprintf("%s is al in gebruik genomen; verwijder het cluster op zijn pagina", n.Hostname)
		}
	}
	return ""
}

// CleanupRequest is een aanvraag om een mislukte uitrol op te ruimen.
type CleanupRequest struct {
	// Confirm is de slug van het cluster, overgetikt.
	Confirm string
	// Guests haalt ook de CT's en VM's weg die de uitrol maakte.
	Guests bool
}

type cleanupParams struct {
	JobID     uuid.UUID    `json:"job_id"`
	ClusterID uuid.UUID    `json:"cluster_id"`
	Name      string       `json:"name"`
	ConnID    uuid.UUID    `json:"proxmox_id"`
	Guests    bool         `json:"guests"`
	Nodes     []FailedNode `json:"nodes"`
}

// StartCleanup zet het opruimen van een mislukte uitrol in de wachtrij.
func (s *Service) StartCleanup(ctx context.Context, actor events.Actor, jobID uuid.UUID, req CleanupRequest) (store.Job, error) {
	f, err := s.FailedDeploy(ctx, jobID)
	if err != nil {
		return store.Job{}, err
	}
	if !f.CanClean {
		return store.Job{}, ValidationError{Msg: f.Why}
	}
	if strings.TrimSpace(req.Confirm) != f.Slug {
		return store.Job{}, ValidationError{Field: "confirm", Msg: fmt.Sprintf("tik de slug (%s) over om op te ruimen", f.Slug)}
	}
	jr, err := s.q.GetJob(ctx, jobID)
	if err != nil {
		return store.Job{}, err
	}
	var p params
	if err := json.Unmarshal(jr.Job.Params, &p); err != nil {
		return store.Job{}, err
	}
	spec := jobs.Spec{
		Kind: CleanupKind, Title: "Mislukte uitrol opruimen: " + f.ClusterName, Actor: actor,
		Params: cleanupParams{JobID: jobID, ClusterID: f.ClusterID, Name: f.ClusterName, ConnID: p.Target.ProxmoxID,
			Guests: req.Guests && p.Target.Network != NetworkExisting, Nodes: f.Nodes},
	}
	if p.Target.ProxmoxID != uuid.Nil {
		spec.ProxmoxID = &p.Target.ProxmoxID
	}
	if f.Exists {
		spec.ClusterID = &f.ClusterID
	}
	out, err := s.jobs.Enqueue(ctx, spec)
	if err != nil {
		return store.Job{}, err
	}
	s.jobs.Kick()
	return out, nil
}

// keyState bewaart per sleutel de state van een run op een host.
type cleanState struct {
	Runs map[string]json.RawMessage `json:"runs"`
}

type keyState struct {
	st  *jobs.Step
	cs  *cleanState
	key string
}

func (k keyState) State(v any) bool {
	raw := k.cs.Runs[k.key]
	return len(raw) > 0 && json.Unmarshal(raw, v) == nil
}

func (k keyState) SetState(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	k.cs.Runs[k.key] = b
	return k.st.SetState(ctx, *k.cs)
}

var _ noderun.StepState = keyState{}

func (s *Service) cleanup(ctx context.Context, j *jobs.Job) error {
	var p cleanupParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	if p.Guests {
		if err := j.Step(ctx, "CT's en VM's van de uitrol weghalen", func(ctx context.Context, st *jobs.Step) error {
			return s.removeGuests(ctx, st, p)
		}); err != nil {
			return err
		}
	}
	return j.Step(ctx, "Nodes, VIP en cluster uit ClusterForge halen", func(ctx context.Context, st *jobs.Step) error {
		var keys []string
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			for _, n := range p.Nodes {
				if n.Gone {
					continue
				}
				row, err := q.LockNode(ctx, n.ID)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				if row.ClusterID == nil || *row.ClusterID != p.ClusterID {
					st.Logf("%s zit niet meer in dit cluster; die blijft", n.Hostname)
					continue
				}
				if n.Existing {
					if _, err := s.inv.UpdateNodeTx(ctx, q, actor, n.ID, func(f *inventory.NodeFields) { f.ClusterID = nil }); err != nil {
						return err
					}
					st.Logf("%s bestond al: alleen uit het cluster gehaald (wat de uitrol erop zette, blijft staan)", n.Hostname)
					continue
				}
				key, err := s.inv.DeleteNodeTx(ctx, q, actor, n.ID, map[string]any{"reason": "mislukte uitrol opgeruimd", "job_id": j.ID.String()})
				if err != nil {
					return err
				}
				keys = append(keys, key)
				st.Logf("Node %s verwijderd", n.Hostname)
			}
			if _, err := q.LockCluster(ctx, p.ClusterID); errors.Is(err, pgx.ErrNoRows) {
				st.Logf("Het cluster was al weg")
				return nil
			} else if err != nil {
				return err
			}
			if _, err := q.DeleteCluster(ctx, p.ClusterID); err != nil {
				return err
			}
			st.Logf("Cluster %s verwijderd, met zijn VIP, gewenste staat en geheimen; de slug is weer vrij", p.Name)
			return s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "cluster", SubjectID: p.ClusterID.String(), Action: "cluster.deleted",
				Payload: map[string]any{"name": p.Name, "reason": "mislukte uitrol opgeruimd", "deploy_job_id": p.JobID.String()},
			})
		})
		for _, k := range keys {
			s.inv.Disconnected(k, err)
		}
		if err == nil && s.Changed != nil {
			s.Changed()
		}
		return err
	})
}

// removeGuests zet de CT's en VM's van de uitrol uit en verwijdert ze, via
// de agent op hun host. Alleen een gast met de hostname van de node: een
// ander nummer of een andere naam blijft staan.
func (s *Service) removeGuests(ctx context.Context, st *jobs.Step, p cleanupParams) error {
	cs := cleanState{Runs: map[string]json.RawMessage{}}
	st.State(&cs)
	if cs.Runs == nil {
		cs.Runs = map[string]json.RawMessage{}
	}
	if s.Hosts == nil {
		st.Logf("Deze server heeft geen agents op de Proxmox-hosts; verwijder de CT's en VM's zelf in Proxmox.")
		return nil
	}
	hosts, err := s.Hosts.ForConnection(ctx, p.ConnID)
	if err != nil {
		return err
	}
	var failed []string
	for _, n := range p.Nodes {
		if n.Existing || n.VMID == 0 {
			continue
		}
		h, ok := hosts[n.Host]
		if !ok || h.Ready() != "" {
			st.Logf("%d (%s) op %s: die host heeft geen werkende agent; verwijder hem zelf in Proxmox", n.VMID, n.Hostname, n.Host)
			failed = append(failed, n.Hostname)
			continue
		}
		inv, err := s.Hosts.Inventory(ctx, h.NodeID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(inv.Guests, func(g protocol.PVEGuest) bool { return g.VMID == n.VMID })
		if i < 0 {
			st.Logf("%d (%s) staat niet (meer) op %s", n.VMID, n.Hostname, n.Host)
			continue
		}
		g := inv.Guests[i]
		if g.Name != n.Hostname || g.Template {
			st.Logf("%d op %s heet %s, niet %s: die laat ClusterForge staan", n.VMID, n.Host, g.Name, n.Hostname)
			continue
		}
		key := strconv.Itoa(n.VMID)
		if g.Status != "stopped" {
			res, err := s.Hosts.Do(ctx, st, keyState{st, &cs, "stop:" + key}, h.NodeID,
				protocol.PVERun{Action: protocol.PVEStop, Type: g.Type, VMID: n.VMID}, fmt.Sprintf("%d uitzetten op %s", n.VMID, n.Host))
			if err != nil {
				return err
			}
			if res.State != protocol.RunOK {
				st.Logf("%d uitzetten mislukt: %s", n.VMID, noderun.Describe(res))
				failed = append(failed, n.Hostname)
				continue
			}
		}
		res, err := s.Hosts.Do(ctx, st, keyState{st, &cs, "destroy:" + key}, h.NodeID,
			protocol.PVERun{Action: protocol.PVEDestroy, Type: g.Type, VMID: n.VMID}, fmt.Sprintf("%d verwijderen op %s", n.VMID, n.Host))
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			st.Logf("%d verwijderen mislukt: %s", n.VMID, noderun.Describe(res))
			failed = append(failed, n.Hostname)
			continue
		}
		st.Logf("%d (%s) verwijderd op %s", n.VMID, n.Hostname, n.Host)
	}
	if len(failed) > 0 {
		// Niet verder: anders verdwijnt de node uit ClusterForge terwijl zijn
		// CT nog op het adres draait.
		return fmt.Errorf("niet alles is weg (%s); los het op in Proxmox en probeer opnieuw, of ruim op zonder de CT's en VM's", strings.Join(failed, ", "))
	}
	return nil
}
