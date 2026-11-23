// Package dockerhost beheert Docker op elke node met een agent (fase 28,
// Docker zoals Portainer). De agent praat met de lokale Docker-socket; deze
// server leest via de agent (Get, Stats), houdt per node een samenvatting
// bij (Loop) en zet wijzigingen als taak in de wachtrij (Call).
package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de soort taak van een wijziging aan Docker.
const Kind = "docker.action"

// Bus praat met de agents.
type Bus interface {
	noderun.Bus
	Docker(ctx context.Context, nodeID uuid.UUID, req protocol.DockerRequest) (protocol.DockerResponse, error)
}

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrNotFound: geen node met Docker onder dat id.
var ErrNotFound = errors.New("geen node met Docker")

const (
	// cacheTTL is hoe lang een gelezen antwoord geldt.
	cacheTTL = 5 * time.Second
	// pollEvery is hoe vaak de samenvatting ververst.
	pollEvery = time.Minute
	// diskEvery is hoe vaak het schijfgebruik (system df, zwaar) erbij komt.
	diskEvery = 15 * time.Minute
)

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Bus
	Run  *noderun.Runner

	mu    sync.Mutex
	cache map[string]cached
	// diskAt onthoudt per node wanneer system df laatst liep.
	diskAt map[uuid.UUID]time.Time
	kick   chan struct{}
}

type cached struct {
	at   time.Time
	data json.RawMessage
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, jobs: runner, bus: bus,
		Run:   &noderun.Runner{Bus: bus},
		cache: map[string]cached{}, diskAt: map[uuid.UUID]time.Time{}, kick: make(chan struct{}, 1),
	}
	if runner != nil {
		runner.Register(Kind, s.run)
		runner.OnFinished(Kind, func(context.Context, store.Job) {
			s.clearCache()
			s.Kick()
		})
	}
	return s
}

// Host is een node met Docker.
type Host struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	Cluster     string     `json:"cluster,omitempty"`
	Environment string     `json:"environment,omitempty"`
	// VIPHolder: deze node heeft nu een VIP van zijn cluster.
	VIPHolder bool `json:"vip_holder"`
	// DockerVersion komt uit de facts (ook zonder nieuwe agent).
	DockerVersion string     `json:"docker_version,omitempty"`
	Protocol      int        `json:"protocol"`
	Agent         string     `json:"agent_version,omitempty"`
	Online        bool       `json:"online"`
	HeartbeatAt   *time.Time `json:"heartbeat_at,omitempty"`
	// Supported: de agent kent de Docker-API (protocol DockerSince).
	Supported bool     `json:"supported"`
	Summary   *Summary `json:"summary,omitempty"`
	// Stacks zijn de stacks die ClusterForge in het cluster van deze node
	// beheert (fase 21); hun containers stop of verwijder je via de stack.
	Stacks     []ManagedStack `json:"stacks,omitempty"`
	CheckError string         `json:"check_error,omitempty"`
	CheckedAt  *time.Time     `json:"checked_at,omitempty"`
}

// Ready zegt waarom de node geen Docker-verzoek kan doen; leeg als het kan.
func (h Host) Ready() string {
	switch {
	case !h.Online:
		return fmt.Sprintf("de agent op %s is offline", h.Hostname)
	case !h.Supported:
		return fmt.Sprintf("de agent op %s is te oud (protocol %d, %d nodig); werk hem bij via Nodes → Agents bijwerken", h.Hostname, h.Protocol, protocol.DockerSince)
	}
	return ""
}

// ManagedStack is een stack van fase 21.
type ManagedStack struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Summary is wat de pagina Docker per node bovenaan toont.
type Summary struct {
	Version       string `json:"version"`
	APIVersion    string `json:"api_version,omitempty"`
	OS            string `json:"os,omitempty"`
	Kernel        string `json:"kernel,omitempty"`
	StorageDriver string `json:"storage_driver,omitempty"`
	RootDir       string `json:"root_dir,omitempty"`
	CPUs          int    `json:"cpus"`
	MemTotal      int64  `json:"mem_total"`
	Containers    int    `json:"containers"`
	Running       int    `json:"running"`
	Paused        int    `json:"paused"`
	Stopped       int    `json:"stopped"`
	Restarting    int    `json:"restarting"`
	Unhealthy     int    `json:"unhealthy"`
	Images        int    `json:"images"`
	// Problems zijn containers die aandacht vragen: unhealthy, in een
	// herstartlus, of gestopt met een fout terwijl ze altijd moeten draaien.
	Problems []Problem `json:"problems,omitempty"`
	Disk     *Disk     `json:"disk,omitempty"`
	// Findings zijn risico's in hoe containers draaien (M6); ze worden
	// samen met Disk om de diskEvery bijgewerkt.
	Findings []Finding `json:"findings,omitempty"`
	// Warnings zijn de waarschuwingen van docker info.
	Warnings []string `json:"warnings,omitempty"`
}

// Problem is een container die aandacht vraagt.
type Problem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Soorten bevindingen.
const (
	// FindPrivileged: de container draait privileged (root op de host).
	FindPrivileged = "privileged"
	// FindSocket: de Docker-socket zit in de container (root op de host).
	FindSocket = "docker-socket"
	// FindHostNet: de container deelt het netwerk van de host.
	FindHostNet = "host-network"
	// FindPublic: een poort luistert op alle adressen van de host.
	FindPublic = "public-port"
	// FindLatest: het image is latest of zonder tag; wat er draait, kan
	// bij een volgende pull onverwacht veranderen.
	FindLatest = "latest"
)

// Finding is een risico bij een draaiende container.
type Finding struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// Serious zegt of een bevinding in Aandacht nodig hoort: alleen wat de
// container root op de host geeft.
func (f Finding) Serious() bool { return f.Kind == FindPrivileged || f.Kind == FindSocket }

// Disk is het schijfgebruik van Docker (system df).
type Disk struct {
	Images      int64     `json:"images"`
	Containers  int64     `json:"containers"`
	Volumes     int64     `json:"volumes"`
	BuildCache  int64     `json:"build_cache"`
	Reclaimable int64     `json:"reclaimable"`
	At          time.Time `json:"at"`
}

// Hosts geeft elke node met Docker.
func (s *Service) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := s.q.ListDockerHosts(ctx)
	if err != nil {
		return nil, err
	}
	stacks, err := s.q.ListDockerManagedStacks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(rows))
	for _, r := range rows {
		h := Host{
			NodeID: r.ID, Hostname: r.Hostname, ClusterID: r.ClusterID, Cluster: r.ClusterName, Environment: r.Environment,
			VIPHolder: r.VipHolder, DockerVersion: r.DockerVersion, HeartbeatAt: r.HeartbeatAt, CheckedAt: r.CheckedAt,
		}
		if r.AgentProtocol != nil {
			h.Protocol = int(*r.AgentProtocol)
			h.Online = r.HeartbeatAt != nil && time.Since(*r.HeartbeatAt) < status.HeartbeatLate
		}
		if r.AgentVersion != nil {
			h.Agent = *r.AgentVersion
		}
		h.Supported = h.Protocol >= protocol.DockerSince
		if r.CheckError != nil {
			h.CheckError = *r.CheckError
		}
		for _, st := range stacks {
			if r.ClusterID != nil && st.ClusterID == *r.ClusterID {
				h.Stacks = append(h.Stacks, ManagedStack{ID: st.ID, Name: st.Name})
			}
		}
		if len(r.Summary) > 2 {
			var sum Summary
			if json.Unmarshal(r.Summary, &sum) == nil {
				h.Summary = &sum
			}
		}
		out = append(out, h)
	}
	return out, nil
}

// Host geeft de node met Docker onder id.
func (s *Service) Host(ctx context.Context, id uuid.UUID) (Host, error) {
	hosts, err := s.Hosts(ctx)
	if err != nil {
		return Host{}, err
	}
	for _, h := range hosts {
		if h.NodeID == id {
			return h, nil
		}
	}
	return Host{}, ErrNotFound
}

func (s *Service) ready(ctx context.Context, id uuid.UUID) (Host, error) {
	h, err := s.Host(ctx, id)
	if err != nil {
		return Host{}, err
	}
	if why := h.Ready(); why != "" {
		return Host{}, ValidationError{why}
	}
	return h, nil
}

func (s *Service) clearCache() {
	s.mu.Lock()
	s.cache = map[string]cached{}
	s.mu.Unlock()
}

// Get leest een pad van de Docker-API op een node. Een kijker ziet geen
// waarden van omgevingsvariabelen.
func (s *Service) Get(ctx context.Context, nodeID uuid.UUID, path string, query map[string]string, admin bool) (json.RawMessage, error) {
	req := protocol.DockerRequest{Op: protocol.DockerOpGet, Path: path, Query: query}
	if err := req.Validate(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	if _, err := s.ready(ctx, nodeID); err != nil {
		return nil, err
	}
	data, err := s.get(ctx, nodeID, path, query)
	if err != nil {
		return nil, err
	}
	if !admin {
		data = maskEnv(path, data)
	}
	return data, nil
}

// get leest zonder controles, met de cache.
func (s *Service) get(ctx context.Context, nodeID uuid.UUID, path string, query map[string]string) (json.RawMessage, error) {
	key := nodeID.String() + path + "?" + canonical(query)
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(c.at) < cacheTTL {
		return c.data, nil
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.DockerRequestTimeout)
	defer cancel()
	res, err := s.bus.Docker(ctx, nodeID, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: path, Query: query})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		if res.Status == 404 {
			return nil, ValidationError{res.Error}
		}
		return nil, errors.New(res.Error)
	}
	s.mu.Lock()
	if len(s.cache) > 500 {
		s.cache = map[string]cached{}
	}
	s.cache[key] = cached{at: time.Now(), data: res.Data}
	s.mu.Unlock()
	return res.Data, nil
}

func canonical(m map[string]string) string {
	v := url.Values{}
	for k, val := range m {
		v.Set(k, val)
	}
	return v.Encode()
}

// maskEnv vervangt de waarden van omgevingsvariabelen in een inspect van
// een container of image door •••.
func maskEnv(path string, data json.RawMessage) json.RawMessage {
	inspect := strings.HasPrefix(path, "/containers/") || strings.HasPrefix(path, "/images/")
	if !strings.HasSuffix(path, "/json") || !inspect || path == "/containers/json" || path == "/images/json" {
		return data
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	for _, k := range []string{"Config", "ContainerConfig"} {
		cfg, ok := m[k].(map[string]any)
		if !ok {
			continue
		}
		env, ok := cfg["Env"].([]any)
		if !ok {
			continue
		}
		for i, e := range env {
			if str, ok := e.(string); ok {
				name, _, _ := strings.Cut(str, "=")
				env[i] = name + "=•••"
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return out
}

// Stats meet de draaiende containers van een node.
func (s *Service) Stats(ctx context.Context, nodeID uuid.UUID, containers []string) ([]protocol.DockerStat, error) {
	req := protocol.DockerRequest{Op: protocol.DockerOpStats, Containers: containers}
	if err := req.Validate(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	if _, err := s.ready(ctx, nodeID); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.DockerRequestTimeout)
	defer cancel()
	res, err := s.bus.Docker(ctx, nodeID, req)
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	if res.Stats == nil {
		res.Stats = []protocol.DockerStat{}
	}
	return res.Stats, nil
}

// Kick ververst de samenvattingen meteen (na een taak).
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Loop ververst elke minuut de samenvatting van elke node met Docker.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		s.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Refresh leest de samenvatting van elke node die het kan, vier tegelijk.
func (s *Service) Refresh(ctx context.Context) {
	hosts, err := s.Hosts(ctx)
	if err != nil {
		s.log.Warn("Docker-nodes lezen mislukt", "err", err)
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, h := range hosts {
		if h.Ready() != "" {
			continue
		}
		wg.Add(1)
		go func(h Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sum, err := s.summarize(ctx, h)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			data, _ := json.Marshal(sum)
			if err := s.q.UpsertDockerHost(ctx, store.UpsertDockerHostParams{NodeID: h.NodeID, Summary: data, Error: msg}); err != nil {
				s.log.Warn("Docker-samenvatting bewaren mislukt", "node", h.Hostname, "err", err)
			}
		}(h)
	}
	wg.Wait()
}

// dockerInfo is wat de samenvatting uit /info haalt.
type dockerInfo struct {
	ServerVersion   string   `json:"ServerVersion"`
	OperatingSystem string   `json:"OperatingSystem"`
	KernelVersion   string   `json:"KernelVersion"`
	Driver          string   `json:"Driver"`
	DockerRootDir   string   `json:"DockerRootDir"`
	NCPU            int      `json:"NCPU"`
	MemTotal        int64    `json:"MemTotal"`
	Images          int      `json:"Images"`
	Warnings        []string `json:"Warnings"`
}

// ContainerSummary is een rij uit /containers/json.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	Mounts []struct {
		Type   string `json:"Type"`
		Source string `json:"Source"`
	} `json:"Mounts"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// Name is de naam zonder /.
func (c ContainerSummary) Name() string {
	if len(c.Names) == 0 {
		return c.ID[:min(12, len(c.ID))]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

func (s *Service) summarize(ctx context.Context, h Host) (Summary, error) {
	var sum Summary
	if h.Summary != nil {
		sum.Disk = h.Summary.Disk
		sum.Findings = h.Summary.Findings
	}
	data, err := s.get(ctx, h.NodeID, "/info", nil)
	if err != nil {
		return sum, err
	}
	var info dockerInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return sum, fmt.Errorf("docker info lezen: %w", err)
	}
	sum.Version, sum.OS, sum.Kernel, sum.StorageDriver, sum.RootDir = info.ServerVersion, info.OperatingSystem, info.KernelVersion, info.Driver, info.DockerRootDir
	sum.CPUs, sum.MemTotal, sum.Images, sum.Warnings = info.NCPU, info.MemTotal, info.Images, info.Warnings
	if v, err := s.get(ctx, h.NodeID, "/version", nil); err == nil {
		var ver struct {
			APIVersion string `json:"ApiVersion"`
		}
		_ = json.Unmarshal(v, &ver)
		sum.APIVersion = ver.APIVersion
	}
	data, err = s.get(ctx, h.NodeID, "/containers/json", map[string]string{"all": "1"})
	if err != nil {
		return sum, err
	}
	var list []ContainerSummary
	if err := json.Unmarshal(data, &list); err != nil {
		return sum, fmt.Errorf("containerlijst lezen: %w", err)
	}
	count(&sum, list)
	s.mu.Lock()
	last := s.diskAt[h.NodeID]
	s.mu.Unlock()
	if sum.Disk == nil || time.Since(last) > diskEvery {
		if d, err := s.disk(ctx, h.NodeID); err == nil {
			sum.Disk = d
			s.mu.Lock()
			s.diskAt[h.NodeID] = time.Now()
			s.mu.Unlock()
		}
		sum.Findings = s.findings(ctx, h.NodeID, list)
	}
	return sum, nil
}

// maxInspect is hoeveel draaiende containers findings per keer inspecteert.
const maxInspect = 100

// findings zoekt de risico's bij de draaiende containers. Privileged staat
// alleen in inspect; de rest staat al in de lijst.
func (s *Service) findings(ctx context.Context, nodeID uuid.UUID, list []ContainerSummary) []Finding {
	var out []Finding
	inspected := 0
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		add := func(kind, detail string) {
			out = append(out, Finding{ID: c.ID, Name: c.Name(), Kind: kind, Detail: detail})
		}
		out = append(out, listFindings(c)...)
		if inspected >= maxInspect {
			continue
		}
		inspected++
		raw, err := s.get(ctx, nodeID, "/containers/"+c.ID+"/json", nil)
		if err != nil {
			continue
		}
		var in struct {
			HostConfig struct {
				Privileged bool `json:"Privileged"`
			} `json:"HostConfig"`
		}
		if json.Unmarshal(raw, &in) == nil && in.HostConfig.Privileged {
			add(FindPrivileged, "")
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// listFindings zijn de bevindingen die al in /containers/json staan.
func listFindings(c ContainerSummary) []Finding {
	var out []Finding
	add := func(kind, detail string) {
		out = append(out, Finding{ID: c.ID, Name: c.Name(), Kind: kind, Detail: detail})
	}
	for _, m := range c.Mounts {
		if m.Type == "bind" && (m.Source == "/var/run/docker.sock" || m.Source == "/run/docker.sock") {
			add(FindSocket, m.Source)
			break
		}
	}
	if c.HostConfig.NetworkMode == "host" {
		add(FindHostNet, "")
	}
	seen := map[string]bool{}
	var public []string
	for _, p := range c.Ports {
		if p.PublicPort == 0 || (p.IP != "0.0.0.0" && p.IP != "::" && p.IP != "") {
			continue
		}
		k := fmt.Sprintf("%d/%s", p.PublicPort, p.Type)
		if !seen[k] {
			seen[k] = true
			public = append(public, k)
		}
	}
	if len(public) > 0 {
		sort.Strings(public)
		add(FindPublic, strings.Join(public, ", "))
	}
	if img := c.Image; !strings.HasPrefix(img, "sha256:") && !strings.Contains(img, "@") {
		name := img[strings.LastIndex(img, "/")+1:]
		if !strings.Contains(name, ":") || strings.HasSuffix(name, ":latest") {
			add(FindLatest, img)
		}
	}
	return out
}

// count telt de containers en zoekt de problemen.
func count(sum *Summary, list []ContainerSummary) {
	sum.Containers = len(list)
	for _, c := range list {
		switch c.State {
		case "running":
			sum.Running++
		case "paused":
			sum.Paused++
		case "restarting":
			sum.Restarting++
		default:
			sum.Stopped++
		}
		reason := ""
		switch {
		case strings.Contains(c.Status, "(unhealthy)"):
			sum.Unhealthy++
			reason = "unhealthy"
		case c.State == "restarting":
			reason = "herstartlus"
		case c.State == "exited" && !strings.HasPrefix(c.Status, "Exited (0)") && c.Labels["com.docker.compose.oneoff"] != "True":
			reason = "gestopt met een fout"
		case c.State == "dead":
			reason = "dood"
		}
		if reason != "" {
			sum.Problems = append(sum.Problems, Problem{ID: c.ID, Name: c.Name(), State: c.State, Status: c.Status, Reason: reason})
		}
	}
	sort.Slice(sum.Problems, func(i, j int) bool { return sum.Problems[i].Name < sum.Problems[j].Name })
}

// disk leest system df.
func (s *Service) disk(ctx context.Context, nodeID uuid.UUID) (*Disk, error) {
	data, err := s.get(ctx, nodeID, "/system/df", nil)
	if err != nil {
		return nil, err
	}
	var df struct {
		LayersSize int64 `json:"LayersSize"`
		Images     []struct {
			Size       int64 `json:"Size"`
			SharedSize int64 `json:"SharedSize"`
			Containers int   `json:"Containers"`
		} `json:"Images"`
		Containers []struct {
			SizeRw int64  `json:"SizeRw"`
			State  string `json:"State"`
		} `json:"Containers"`
		Volumes []struct {
			UsageData *struct {
				Size     int64 `json:"Size"`
				RefCount int   `json:"RefCount"`
			} `json:"UsageData"`
		} `json:"Volumes"`
		BuildCache []struct {
			Size   int64 `json:"Size"`
			InUse  bool  `json:"InUse"`
			Shared bool  `json:"Shared"`
		} `json:"BuildCache"`
	}
	if err := json.Unmarshal(data, &df); err != nil {
		return nil, fmt.Errorf("system df lezen: %w", err)
	}
	d := &Disk{Images: df.LayersSize, At: time.Now().UTC()}
	var used int64
	for _, im := range df.Images {
		if im.Containers > 0 {
			used += im.Size - im.SharedSize
		}
	}
	if free := df.LayersSize - used; free > 0 {
		d.Reclaimable += free
	}
	for _, c := range df.Containers {
		d.Containers += c.SizeRw
		if c.State != "running" {
			d.Reclaimable += c.SizeRw
		}
	}
	for _, v := range df.Volumes {
		if v.UsageData != nil && v.UsageData.Size > 0 {
			d.Volumes += v.UsageData.Size
			if v.UsageData.RefCount == 0 {
				d.Reclaimable += v.UsageData.Size
			}
		}
	}
	for _, b := range df.BuildCache {
		if !b.Shared {
			d.BuildCache += b.Size
			if !b.InUse {
				d.Reclaimable += b.Size
			}
		}
	}
	return d, nil
}

// Running geeft de stand van elke container op een node, zonder cache:
// "running", "healthy", "starting", "unhealthy" of de Docker-stand
// (exited, restarting, ...). Na een update van Docker of containerd wacht
// een rolling update hierop per node (testlijst 1.8).
func (s *Service) Running(ctx context.Context, nodeID uuid.UUID) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, protocol.DockerRequestTimeout)
	defer cancel()
	res, err := s.bus.Docker(ctx, nodeID, protocol.DockerRequest{Op: protocol.DockerOpGet, Path: "/containers/json", Query: map[string]string{"all": "1"}})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	var list []ContainerSummary
	if err := json.Unmarshal(res.Data, &list); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, c := range list {
		out[c.Name()] = ContainerState(c.State, c.Status)
	}
	return out, nil
}

// ContainerState vat State en Status van de Docker-API samen.
func ContainerState(state, status string) string {
	if state != "running" {
		return state
	}
	switch {
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	}
	return "running"
}
