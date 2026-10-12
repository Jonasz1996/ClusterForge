package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De etcd-snapshots van k3s. Elke server maakt er elke twaalf uur een en
// houdt de laatste vijf, in protocol.KubeSnapshotDir. k3s meldt ze in de
// API als ETCDSnapshotFile; ClusterForge leest ze daar, en maakt er een met
// Snapshot nu via de agent op een server.

// snapshotsPath is de lijst met ETCDSnapshotFiles; ze horen bij geen
// namespace.
const snapshotsPath = "/apis/k3s.cattle.io/v1/etcdsnapshotfiles"

// SnapshotName is de naam waaronder ClusterForge een snapshot maakt; k3s
// zet de node en het tijdstip erachter. SnapshotBeforeRestore is die van
// de snapshot vlak voor het terugzetten.
const (
	SnapshotName          = "clusterforge"
	SnapshotBeforeRestore = "clusterforge-voor-terugzetten"
)

// SnapshotStale: een cluster waarvan de nieuwste snapshot ouder is, heeft
// geen verse snapshot meer. k3s maakt er twee per dag.
const SnapshotStale = 24 * time.Hour

// etcdSnapshotFile is een ETCDSnapshotFile van k3s.
type etcdSnapshotFile struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		SnapshotName string          `json:"snapshotName"`
		NodeName     string          `json:"nodeName"`
		Location     string          `json:"location"`
		S3           json.RawMessage `json:"s3"`
	} `json:"spec"`
	Status struct {
		ReadyToUse   *bool      `json:"readyToUse"`
		CreationTime *time.Time `json:"creationTime"`
		Size         any        `json:"size"`
		Error        *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"status"`
}

// Snapshot is een etcd-snapshot op een server.
type Snapshot = store.KubeSnapshot

// SnapshotKind zegt wie een snapshot maakte: scheduled (k3s zelf, elke
// twaalf uur), clusterforge, of on_demand (met de hand).
func SnapshotKind(file string) string {
	switch {
	case strings.HasPrefix(file, "etcd-snapshot-"):
		return "scheduled"
	case strings.HasPrefix(file, SnapshotName+"-"):
		return "clusterforge"
	}
	return "on_demand"
}

func snapshotFromFile(clusterID uuid.UUID, f etcdSnapshotFile) Snapshot {
	out := Snapshot{
		ClusterID: clusterID, Name: f.Metadata.Name, Node: f.Spec.NodeName, Location: f.Spec.Location,
		S3:      len(f.Spec.S3) > 0 && string(f.Spec.S3) != "null",
		TakenAt: f.Status.CreationTime, Ready: f.Status.ReadyToUse != nil && *f.Status.ReadyToUse,
	}
	out.File = f.Spec.SnapshotName
	if u, ok := strings.CutPrefix(f.Spec.Location, "file://"); ok {
		out.File = path.Base(u)
	}
	var size string
	switch v := f.Status.Size.(type) {
	case string:
		size = v
	case float64:
		size = fmt.Sprintf("%.0f", v)
	}
	if size != "" {
		b := Bytes(size)
		out.SizeBytes = &b
	}
	if f.Status.Error != nil {
		out.Error = f.Status.Error.Message
	}
	if out.TakenAt == nil {
		t := f.Metadata.CreationTimestamp
		out.TakenAt = &t
	}
	return out
}

// RefreshSnapshots leest de snapshots uit de API en bewaart ze. Lukt
// lezen niet, dan blijft de vorige lijst staan, met de fout erbij.
func (s *Service) RefreshSnapshots(ctx context.Context, clusterID uuid.UUID) error {
	s.mu.Lock()
	s.snapAt[clusterID] = s.Now()
	s.mu.Unlock()
	files, err := List[etcdSnapshotFile](ctx, s, clusterID, snapshotsPath, nil)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, ErrNotFound) {
			msg = "k3s meldt geen snapshots (ETCDSnapshotFile); draait het cluster met etcd en k3s 1.29 of nieuwer?"
		}
		if serr := s.q.SetKubeSnapshotsRead(ctx, store.SetKubeSnapshotsReadParams{ClusterID: clusterID, Error: msg}); serr != nil {
			return serr
		}
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.DeleteKubeSnapshots(ctx, clusterID); err != nil {
			return err
		}
		for _, f := range files {
			sn := snapshotFromFile(clusterID, f)
			if err := q.InsertKubeSnapshot(ctx, store.InsertKubeSnapshotParams(sn)); err != nil {
				return err
			}
		}
		return q.SetKubeSnapshotsRead(ctx, store.SetKubeSnapshotsReadParams{ClusterID: clusterID})
	})
}

// syncSnapshots leest de snapshots om de SnapshotInterval.
func (s *Service) syncSnapshots(ctx context.Context, clusterID uuid.UUID) {
	s.mu.Lock()
	due := s.Now().Sub(s.snapAt[clusterID]) >= s.SnapshotInterval
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.RefreshSnapshots(ctx, clusterID); err != nil && !errors.Is(err, ErrNotFound) {
		s.log.Warn("etcd-snapshots lezen mislukt", "cluster", clusterID, "err", err)
	}
}

// ClusterSnapshots zijn de snapshots van een cluster.
type ClusterSnapshots struct {
	Items []Snapshot
	// ReadAt is wanneer de lijst laatst gelezen werd, Error waarom dat
	// daarna niet lukte.
	ReadAt *time.Time
	Error  string
	// Init is de server in Kubernetes die het cluster begon, en
	// InitHostname die node in ClusterForge; leeg als geen agent het
	// meldt. Alleen een snapshot daar kan terug.
	Init, InitHostname string
	// Hostnames zijn de ClusterForge-nodes van de servers in Kubernetes.
	Hostnames map[string]string
}

// Snapshots geeft de snapshots van een cluster. Met live leest het ze
// eerst opnieuw.
func (s *Service) Snapshots(ctx context.Context, clusterID uuid.UUID, live bool) (ClusterSnapshots, error) {
	if _, err := s.Cluster(ctx, clusterID); err != nil {
		return ClusterSnapshots{}, err
	}
	if live {
		// Een fout staat daarna bij de lijst.
		_ = s.RefreshSnapshots(ctx, clusterID)
	}
	var out ClusterSnapshots
	var err error
	if out.Items, err = s.q.ListKubeSnapshots(ctx, &clusterID); err != nil {
		return out, err
	}
	rows, err := s.q.ListKubeSnapshotClusters(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		if r.ID == clusterID {
			out.ReadAt, out.Error = r.SnapshotsAt, r.SnapshotsError
		}
	}
	servers, err := s.servers(ctx, clusterID)
	if err != nil {
		return out, err
	}
	out.Hostnames = map[string]string{}
	for _, n := range servers {
		out.Hostnames[n.Kube.NodeName] = n.Hostname
		if n.Kube.Init {
			out.Init, out.InitHostname = n.Kube.NodeName, n.Hostname
		}
	}
	return out, nil
}

// servers zijn de nodes met een agent die k3s als server meldt, ook als
// die agent te oud is voor de kube-brug.
func (s *Service) servers(ctx context.Context, clusterID uuid.UUID) ([]bridgeNode, error) {
	rows, err := s.q.ListKubeBridgeNodes(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	var out []bridgeNode
	for _, r := range rows {
		var f protocol.KubeFacts
		if len(r.Kube) == 0 || json.Unmarshal(r.Kube, &f) != nil || f.Role != "server" {
			continue
		}
		n := bridgeNode{ID: r.ID, Hostname: r.Hostname, Protocol: int(r.AgentProtocol), IP: r.PrimaryIp, Kube: &f}
		n.Fresh = r.HeartbeatAt != nil && s.Now().Sub(*r.HeartbeatAt) <= status.HeartbeatLate
		out = append(out, n)
	}
	return out, nil
}

// NodeError is een stap die op een node mislukte, zoals k3s etcd-snapshot
// save.
type NodeError struct{ Hostname, Msg string }

func (e *NodeError) Error() string { return e.Hostname + ": " + e.Msg }

// sendTo stuurt een snapshotstap naar de agent op één node en wacht tot
// hij klaar is.
func (s *Service) sendTo(ctx context.Context, clusterID uuid.UUID, n bridgeNode, sr protocol.KubeSnapshotRequest) (protocol.KubeSnapshotResult, error) {
	var out protocol.KubeSnapshotResult
	if n.Protocol < protocol.KubeSince {
		return out, &UnavailableError{TooOld: true, Msg: fmt.Sprintf(
			"de agent op %s is te oud voor Kubernetes (protocol %d, nodig is %d); werk hem bij met het installatiescript en --upgrade",
			n.Hostname, n.Protocol, protocol.KubeSince)}
	}
	// De agent krijgt zelf KubeSnapshotTimeout; hier komt er een minuut bij
	// voor het antwoord.
	rctx, cancel := context.WithTimeout(ctx, protocol.KubeSnapshotTimeout+time.Minute)
	defer cancel()
	res, err := s.bus.Kube(rctx, n.ID, protocol.KubeRequest{
		ID: uuid.NewString(), Op: protocol.KubeSnapshot, Snapshot: &sr, Deadline: time.Now().Add(protocol.KubeSnapshotTimeout),
	})
	switch {
	case errors.Is(err, agentbus.ErrAgentOffline):
		return out, &UnavailableError{Msg: "de agent op " + n.Hostname + " is niet verbonden"}
	case errors.Is(err, agentbus.ErrNoAnswer):
		return out, &UnavailableError{Msg: "de agent op " + n.Hostname + " antwoordde niet op tijd"}
	case err != nil:
		return out, err
	case res.Status == 0 && strings.HasPrefix(res.Error, "onbekend kube-verzoek"):
		return out, &UnavailableError{TooOld: true, Msg: "de agent op " + n.Hostname +
			" kent snapshots nog niet; werk hem bij met het installatiescript en --upgrade"}
	case res.Status == 0 || res.Error != "":
		return out, &NodeError{Hostname: n.Hostname, Msg: res.Error}
	}
	s.forget(clusterID)
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return out, fmt.Errorf("antwoord van %s: %w", n.Hostname, err)
	}
	return out, nil
}

// Saved is een snapshot die Snapshot nu maakte. Snapshot is leeg als k3s
// hem nog niet in de API meldt; Output zegt dan wat k3s schreef.
type Saved struct {
	Snapshot *Snapshot
	Hostname string
	Output   []string
}

// SaveSnapshot maakt een snapshot op de server die het cluster begon, want
// alleen daar kan ClusterForge hem terugzetten; is die er niet, dan op een
// andere server.
func (s *Service) SaveSnapshot(ctx context.Context, actor events.Actor, clusterID uuid.UUID) (Saved, error) {
	n, err := s.saveNode(ctx, clusterID)
	if err != nil {
		return Saved{}, err
	}
	saved, err := s.save(ctx, clusterID, n, SnapshotName)
	if err != nil {
		return saved, err
	}
	payload := map[string]any{"node": n.Kube.NodeName, "hostname": n.Hostname}
	if saved.Snapshot != nil {
		payload["file"] = saved.Snapshot.File
		if saved.Snapshot.SizeBytes != nil {
			payload["size_bytes"] = *saved.Snapshot.SizeBytes
		}
	}
	return saved, s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID, Action: "kube.snapshot_saved",
		Payload: payload,
	})
}

// saveNode kiest de server voor Snapshot nu: die met cluster-init als zijn
// agent verbonden is, anders de beste voor de kube-brug.
func (s *Service) saveNode(ctx context.Context, clusterID uuid.UUID) (bridgeNode, error) {
	servers, err := s.servers(ctx, clusterID)
	if err != nil {
		return bridgeNode{}, err
	}
	for _, n := range servers {
		if n.Kube.Init && n.Fresh && n.Protocol >= protocol.KubeSince {
			return n, nil
		}
	}
	nodes, err := s.candidates(ctx, clusterID)
	if err != nil {
		return bridgeNode{}, err
	}
	return nodes[0], nil
}

// save maakt een snapshot met naam op node n, en zoekt hem daarna in de
// API.
func (s *Service) save(ctx context.Context, clusterID uuid.UUID, n bridgeNode, name string) (Saved, error) {
	out := Saved{Hostname: n.Hostname}
	start := s.Now()
	res, err := s.sendTo(ctx, clusterID, n, protocol.KubeSnapshotRequest{Action: protocol.KubeSnapshotSave, Name: name})
	out.Output = res.Output
	if err != nil {
		return out, err
	}
	if err := s.RefreshSnapshots(ctx, clusterID); err != nil {
		return out, nil //nolint:nilerr // de snapshot is gemaakt; de lijst volgt bij de volgende sync
	}
	items, err := s.q.ListKubeSnapshots(ctx, &clusterID)
	if err != nil {
		return out, err
	}
	prefix := name + "-" + n.Kube.NodeName + "-"
	for _, it := range items {
		// De nieuwste eerst; k3s zet het tijdstip in seconden.
		if strings.HasPrefix(it.File, prefix) && it.Node == n.Kube.NodeName && it.TakenAt != nil && !it.TakenAt.Before(start.Add(-time.Minute)) {
			out.Snapshot = &it
			break
		}
	}
	return out, nil
}

// Fresh zegt hoe het met de snapshots van een cluster staat: ok als de
// nieuwste jonger is dan SnapshotStale, stale als hij ouder is, none als
// er geen is, en unknown als de lijst niet te lezen is.
func Fresh(items []Snapshot, readErr string, now time.Time) string {
	var newest *time.Time
	for _, it := range items {
		if it.Ready && it.TakenAt != nil && (newest == nil || it.TakenAt.After(*newest)) {
			newest = it.TakenAt
		}
	}
	switch {
	case newest != nil && now.Sub(*newest) <= SnapshotStale:
		return "ok"
	case newest != nil:
		return "stale"
	case readErr != "":
		return "unknown"
	}
	return "none"
}

// SnapshotOverview is per Kubernetes-cluster hoe het met de snapshots
// staat, voor Back-ups.
type SnapshotOverview struct {
	Cluster store.ListKubeSnapshotClustersRow
	Items   []Snapshot
}

// SnapshotsOverview geeft de snapshots van elk Kubernetes-cluster zoals de sync ze
// laatst las.
func (s *Service) SnapshotsOverview(ctx context.Context) ([]SnapshotOverview, error) {
	rows, err := s.q.ListKubeSnapshotClusters(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.q.ListKubeSnapshots(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotOverview, 0, len(rows))
	for _, r := range rows {
		o := SnapshotOverview{Cluster: r}
		for _, it := range items {
			if it.ClusterID == r.ID {
				o.Items = append(o.Items, it)
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// ServerSnapshots is per server de nieuwste bruikbare snapshot en hoeveel
// er zijn.
type ServerSnapshots struct {
	Node   string
	Newest *time.Time
	Count  int
}

// PerServer vat de snapshots samen per server, op naam van de node.
func PerServer(items []Snapshot) []ServerSnapshots {
	var out []ServerSnapshots
	for _, it := range items {
		i := slices.IndexFunc(out, func(o ServerSnapshots) bool { return o.Node == it.Node })
		if i < 0 {
			out = append(out, ServerSnapshots{Node: it.Node})
			i = len(out) - 1
		}
		out[i].Count++
		if it.Ready && it.TakenAt != nil && (out[i].Newest == nil || it.TakenAt.After(*out[i].Newest)) {
			out[i].Newest = it.TakenAt
		}
	}
	slices.SortFunc(out, func(a, b ServerSnapshots) int { return strings.Compare(a.Node, b.Node) })
	return out
}

// RestoreNote zegt waarom ClusterForge een snapshot niet kan terugzetten;
// leeg als het kan. init is de server in Kubernetes die het cluster begon.
// Terugzetten controleert daarna nog of elke server een agent heeft.
func RestoreNote(env store.Environment, init string, sn Snapshot) string {
	switch {
	case env == store.EnvironmentProd:
		return "op prod zet ClusterForge niets terug; volg het runbook"
	case !sn.Ready:
		if sn.Error != "" {
			return "k3s meldt een fout: " + sn.Error
		}
		return "k3s meldt deze snapshot nog niet als klaar"
	case sn.S3:
		return "deze snapshot staat in S3"
	case init == "":
		return "geen agent meldt de server die het cluster begon"
	case sn.Node != init:
		return "alleen een snapshot op " + init + ", de server die het cluster begon, kan terug"
	case !protocol.KubeSnapshotFilePattern.MatchString(sn.File) || sn.Location != "file://"+protocol.KubeSnapshotDir+"/"+sn.File:
		return "deze snapshot staat niet in " + protocol.KubeSnapshotDir
	}
	return ""
}
