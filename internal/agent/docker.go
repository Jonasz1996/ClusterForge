package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Docker zoals Portainer (fase 28): de agent leest de Docker Engine API over
// de lokale socket en verandert er iets aan in een run RunDockerAPI. De
// socket gaat nooit over het netwerk; de server praat alleen met de agent.

// dockerAPI praat HTTP over de unix-socket van Docker.
type dockerAPI struct {
	socket string
	client *http.Client
}

func newDockerAPI(root string) dockerAPI {
	sock := protocol.DockerSocket
	if root != "" {
		sock = filepath.Join(root, protocol.DockerSocket)
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return dockerAPI{socket: sock, client: &http.Client{Transport: tr}}
}

// errNoDocker: geen socket, dus geen Docker op deze node.
var errNoDocker = errors.New("geen Docker op deze node (" + protocol.DockerSocket + " ontbreekt)")

func (d dockerAPI) present() bool {
	fi, err := os.Stat(d.socket)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

func dockerURL(path string, query map[string]string) string {
	u := "http://docker" + path
	if len(query) > 0 {
		v := url.Values{}
		for k, val := range query {
			v.Set(k, val)
		}
		u += "?" + v.Encode()
	}
	return u
}

// do doet één aanroep; het antwoord is de body, tot max bytes.
func (d dockerAPI) do(ctx context.Context, method, path string, query map[string]string, body []byte, max int64) (int, []byte, error) {
	if !d.present() {
		return 0, nil, errNoDocker
	}
	var rd io.Reader
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, dockerURL(path, query), rd)
	if err != nil {
		return 0, nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := d.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Docker niet bereikbaar: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("antwoord van Docker lezen: %w", err)
	}
	if int64(len(data)) > max {
		return res.StatusCode, nil, fmt.Errorf("het antwoord van Docker is groter dan %d KB; filter de vraag", max>>10)
	}
	return res.StatusCode, data, nil
}

// dockerMessage haalt de foutmelding uit een antwoord {"message": …}.
func dockerMessage(status int, data []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &m) == nil && m.Message != "" {
		return m.Message
	}
	return fmt.Sprintf("Docker antwoordde %d", status)
}

func (a *Agent) onDocker(m *nats.Msg) {
	go func() {
		res := a.handleDocker(m.Data)
		data, err := envelope(protocol.TypeDockerResult, res)
		if err != nil {
			return
		}
		if len(data) > protocol.MaxDockerResponse+(64<<10) {
			data, _ = envelope(protocol.TypeDockerResult, protocol.DockerResponse{Error: "het antwoord is te groot voor ClusterForge"})
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op Docker-verzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) handleDocker(data []byte) protocol.DockerResponse {
	var env protocol.Envelope
	var req protocol.DockerRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeDockerRequest {
		return protocol.DockerResponse{Error: "ongeldig Docker-verzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.DockerResponse{Error: "ongeldig Docker-verzoek"}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.DockerResponse{Error: "Docker-verzoek verlopen; klopt de klok van deze node?"}
	}
	if err := req.Validate(); err != nil {
		return protocol.DockerResponse{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.DockerRequestTimeout)
	defer cancel()
	d := a.docker()
	switch req.Op {
	case protocol.DockerOpGet:
		q := req.Query
		if strings.HasSuffix(req.Path, "/stats") {
			q = map[string]string{"stream": "false"}
		}
		status, body, err := d.do(ctx, http.MethodGet, req.Path, q, nil, protocol.MaxDockerResponse)
		if err != nil {
			return protocol.DockerResponse{Error: err.Error(), Status: status}
		}
		if status >= 400 {
			return protocol.DockerResponse{Error: dockerMessage(status, body), Status: status}
		}
		if req.Path == "/events" {
			// /events is een stroom van losse objecten; maak er één lijst van.
			body = jsonLines(body)
		}
		if !json.Valid(body) {
			return protocol.DockerResponse{Error: "Docker gaf geen JSON", Status: status}
		}
		return protocol.DockerResponse{Status: status, Data: body}
	case protocol.DockerOpStats:
		stats, err := d.stats(ctx, req.Containers)
		if err != nil {
			return protocol.DockerResponse{Error: err.Error()}
		}
		return protocol.DockerResponse{Stats: stats}
	}
	return protocol.DockerResponse{Error: "onbekend Docker-verzoek " + req.Op}
}

func (a *Agent) docker() dockerAPI {
	a.dockerMu.Lock()
	defer a.dockerMu.Unlock()
	if a.dockerC == nil {
		d := newDockerAPI(a.Root)
		a.dockerC = &d
	}
	return *a.dockerC
}

// jsonLines maakt van objecten na elkaar één JSON-lijst.
func jsonLines(b []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(b))
	out := []json.RawMessage{}
	for {
		var m json.RawMessage
		if err := dec.Decode(&m); err != nil {
			break
		}
		out = append(out, m)
	}
	data, _ := json.Marshal(out)
	return data
}

// dockerStatsRaw is wat /containers/{id}/stats?stream=false geeft.
type dockerStatsRaw struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// toStat rekent zoals docker stats: CPU over het venster tussen precpu en
// cpu, geheugen zonder de paginacache.
func (r dockerStatsRaw) toStat() protocol.DockerStat {
	s := protocol.DockerStat{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), MemLimit: r.MemoryStats.Limit, PIDs: r.PidsStats.Current}
	cpu := float64(r.CPUStats.CPUUsage.TotalUsage) - float64(r.PreCPUStats.CPUUsage.TotalUsage)
	sys := float64(r.CPUStats.SystemCPUUsage) - float64(r.PreCPUStats.SystemCPUUsage)
	if cpu > 0 && sys > 0 {
		n := r.CPUStats.OnlineCPUs
		if n == 0 {
			n = 1
		}
		s.CPU = cpu / sys * float64(n) * 100
	}
	s.MemUsage = r.MemoryStats.Usage
	// cgroup v2: inactive_file, v1: total_inactive_file.
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if v, ok := r.MemoryStats.Stats[k]; ok && v < s.MemUsage {
			s.MemUsage -= v
			break
		}
	}
	for _, n := range r.Networks {
		s.NetRx += n.RxBytes
		s.NetTx += n.TxBytes
	}
	for _, b := range r.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			s.BlockRead += b.Value
		case "write":
			s.BlockWrite += b.Value
		}
	}
	return s
}

// stats meet de draaiende containers, acht tegelijk. Docker wacht per
// container een seconde voor de CPU-meting; parallel blijft het kort.
func (d dockerAPI) stats(ctx context.Context, only []string) ([]protocol.DockerStat, error) {
	ids := only
	if len(ids) == 0 {
		status, body, err := d.do(ctx, http.MethodGet, "/containers/json", nil, nil, protocol.MaxDockerResponse)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, errors.New(dockerMessage(status, body))
		}
		var list []struct {
			ID string `json:"Id"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("containerlijst lezen: %w", err)
		}
		for _, c := range list {
			ids = append(ids, c.ID)
		}
	}
	var (
		mu  sync.Mutex
		out = make([]protocol.DockerStat, 0, len(ids))
		wg  sync.WaitGroup
		sem = make(chan struct{}, 8)
	)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			status, body, err := d.do(ctx, http.MethodGet, "/containers/"+id+"/stats", map[string]string{"stream": "false"}, nil, 256<<10)
			if err != nil || status >= 400 {
				return
			}
			var raw dockerStatsRaw
			if json.Unmarshal(body, &raw) != nil {
				return
			}
			st := raw.toStat()
			if st.ID == "" {
				st.ID = id
			}
			mu.Lock()
			out = append(out, st)
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// dockerAPIRun voert de aanroepen van een run RunDockerAPI uit, een voor
// een; de eerste fout stopt de run. Een image halen gaat met docker pull,
// zodat de inloggegevens van de node (docker login, credential helpers)
// gelden.
func (r *RunExecutor) dockerAPIRun(ctx context.Context, spec *protocol.DockerAPIRun, out io.Writer) (int, error) {
	d := newDockerAPI(r.Root)
	if !d.present() {
		return 1, errNoDocker
	}
	for i, c := range spec.Calls {
		title := c.Title
		if title == "" {
			title = c.Method + " " + c.Path
		}
		_, _ = fmt.Fprintf(out, "[%d/%d] %s\n", i+1, len(spec.Calls), title)
		if c.Method == http.MethodPost && c.Path == "/images/create" {
			ref := c.Query["fromImage"]
			if t := c.Query["tag"]; t != "" && !strings.Contains(ref, "@") {
				ref += ":" + t
			}
			if ref == "" || strings.HasPrefix(ref, "-") {
				return 1, errors.New("pull zonder image")
			}
			code, err := r.cmd(ctx, "", []string{"docker", "pull", ref}, nil, out)
			if err != nil {
				return code, err
			}
			if code != 0 {
				return code, fmt.Errorf("docker pull %s mislukte", ref)
			}
			continue
		}
		status, body, err := d.do(ctx, c.Method, c.Path, c.Query, c.Body, 4<<20)
		if err != nil {
			return 1, err
		}
		switch {
		case status == http.StatusNotFound && c.IgnoreMissing:
			_, _ = fmt.Fprintln(out, "  al weg")
		case status == http.StatusNotModified:
			_, _ = fmt.Fprintln(out, "  stond al zo")
		case status >= 400:
			return 1, fmt.Errorf("%s: %s", title, dockerMessage(status, body))
		default:
			writeDockerResult(out, body)
		}
	}
	_, _ = fmt.Fprintln(out, "klaar")
	return 0, nil
}

// writeDockerResult toont wat een aanroep teruggaf: bij opruimen wat er weg
// is en hoeveel ruimte vrijkwam.
func writeDockerResult(out io.Writer, body []byte) {
	if len(bytes.TrimSpace(body)) == 0 {
		_, _ = fmt.Fprintln(out, "  ok")
		return
	}
	var prune struct {
		ContainersDeleted []string         `json:"ContainersDeleted"`
		ImagesDeleted     []map[string]any `json:"ImagesDeleted"`
		VolumesDeleted    []string         `json:"VolumesDeleted"`
		NetworksDeleted   []string         `json:"NetworksDeleted"`
		CachesDeleted     []string         `json:"CachesDeleted"`
		SpaceReclaimed    *uint64          `json:"SpaceReclaimed"`
	}
	if json.Unmarshal(body, &prune) == nil && prune.SpaceReclaimed != nil {
		n := len(prune.ContainersDeleted) + len(prune.ImagesDeleted) + len(prune.VolumesDeleted) + len(prune.NetworksDeleted) + len(prune.CachesDeleted)
		_, _ = fmt.Fprintf(out, "  %d weg, %s vrij\n", n, humanBytes(*prune.SpaceReclaimed))
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	for i := 0; sc.Scan() && i < 20; i++ {
		_, _ = fmt.Fprintf(out, "  %s\n", sc.Text())
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// dockerConsoleCommand is docker exec met een terminal in de container.
func dockerConsoleCommand(req protocol.ConsoleRequest) *exec.Cmd {
	args := []string{"exec", "-it", "-e", "TERM=xterm-256color"}
	if req.User != "" {
		args = append(args, "-u", req.User)
	}
	args = append(args, req.Container)
	switch req.Shell {
	case protocol.DockerShellBash:
		args = append(args, "bash", "-l")
	case protocol.DockerShellSh:
		args = append(args, "sh")
	default:
		args = append(args, "sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh; fi")
	}
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	return cmd
}

// dockerRunning controleert dat de container bestaat en draait.
func (a *Agent) dockerRunning(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, body, err := a.docker().do(ctx, http.MethodGet, "/containers/"+name+"/json", nil, nil, 4<<20)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("container %s bestaat niet op deze node", name)
	}
	if status >= 400 {
		return errors.New(dockerMessage(status, body))
	}
	var c struct {
		State struct {
			Running bool `json:"Running"`
			Paused  bool `json:"Paused"`
		} `json:"State"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return fmt.Errorf("container lezen: %w", err)
	}
	if !c.State.Running || c.State.Paused {
		return fmt.Errorf("container %s draait niet; start hem eerst", name)
	}
	return nil
}
