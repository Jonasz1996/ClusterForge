package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De subcommando's van weed die als dienst draaien. Kortlopende commando's
// zoals shell, version of benchmark komen niet in de facts.
var seaweedRoles = []string{
	"master", "volume", "filer", "s3", "server", "mount", "webdav", "iam", "sftp",
	"mq.broker", "filer.sync", "filer.backup", "filer.replicate", "filer.remote.sync", "admin", "worker",
}

// Globale vlaggen van weed die vóór het subcommando een aparte waarde
// kunnen hebben (-v 1).
var seaweedGlobalValueFlags = []string{"v", "vmodule", "log_dir", "logdir", "log_backtrace_at", "config_dir", "options", "cpuprofile", "memprofile"}

// Vlaggen waarvan de waarde nooit de node verlaat.
var seaweedSecretFlag = regexp.MustCompile(`(?i)pass|secret|key|token|jwt|credential`)

// Waar weed zijn toml-bestanden zoekt, behalve de werkmap.
var seaweedConfigDirs = []string{"/etc/seaweedfs", "/usr/local/etc/seaweedfs", "/root/.seaweedfs"}

var seaweedConfigKinds = []string{"filer", "master", "security", "notification", "replication", "s3", "shell"}

var dockerCgroupID = regexp.MustCompile(`(?:docker[-/]|containerd[-/]|/libpod-)([0-9a-f]{64})`)

// seaweed beschrijft SeaweedFS op de node, of nil zonder weed.
func (c *Collector) seaweed(ctx context.Context) *protocol.SeaweedFacts {
	procs := c.seaweedProcesses()
	f := &protocol.SeaweedFacts{Processes: procs}
	seen := map[string]bool{}
	addConfigs := func(base, container string, dirs []string) {
		for _, dir := range dirs {
			for _, kind := range seaweedConfigKinds {
				p := path.Join(dir, kind+".toml")
				full := filepath.Join(base, p)
				real, err := filepath.EvalSymlinks(full)
				if err != nil || seen[real] {
					continue
				}
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				seen[real] = true
				cfg := parseSeaweedToml(data)
				cfg.Path, cfg.Kind, cfg.Container = p, kind, container
				f.Configs = append(f.Configs, cfg)
			}
		}
	}
	addConfigs(c.path("/"), "", seaweedConfigDirs)
	for _, p := range procs {
		base := c.path(fmt.Sprintf("/proc/%d/root", p.PID))
		dirs := slices.Clone(seaweedConfigDirs)
		if d := seaweedFlag(p.Args, "config_dir"); d != "" {
			dirs = append(dirs, d)
		}
		if cwd, err := os.Readlink(c.path(fmt.Sprintf("/proc/%d/cwd", p.PID))); err == nil && strings.HasPrefix(cwd, "/") {
			dirs = append(dirs, cwd)
		}
		if p.Container == "" {
			// Een proces op de host deelt de wortel; alleen zijn werkmap is nieuw.
			base = c.path("/")
		}
		addConfigs(base, p.Container, dirs)
	}
	if len(procs) == 0 && len(f.Configs) == 0 {
		return nil
	}
	c.seaweedLive(ctx, f)
	return f
}

// seaweedProcesses zoekt de weed-processen in /proc.
func (c *Collector) seaweedProcesses() []protocol.SeaweedProcess {
	entries, err := os.ReadDir(c.path("/proc"))
	if err != nil {
		return nil
	}
	var out []protocol.SeaweedProcess
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile(c.path(fmt.Sprintf("/proc/%d/cmdline", pid)))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		p, ok := parseSeaweedCmdline(args)
		if !ok {
			continue
		}
		p.PID = pid
		p.Unit, p.Container = cgroupOwner(c.read(fmt.Sprintf("/proc/%d/cgroup", pid)))
		for _, d := range seaweedDataDirs(p) {
			full := c.path(d)
			if p.Container != "" {
				full = c.path(filepath.Join(fmt.Sprintf("/proc/%d/root", pid), d))
			}
			if st, ok := c.statfs(full); ok {
				p.Disks = append(p.Disks, protocol.SeaweedDisk{Dir: d, Total: st.size, Free: st.avail})
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func seaweedDataDirs(p protocol.SeaweedProcess) []string {
	dirs := slices.Clone(p.Dirs)
	if p.MetaDir != "" && !slices.Contains(dirs, p.MetaDir) {
		dirs = append(dirs, p.MetaDir)
	}
	if p.Role == "mount" {
		return nil
	}
	return dirs
}

// cgroupOwner haalt de systemd-unit of de Docker-container uit
// /proc/<pid>/cgroup.
func cgroupOwner(cgroup string) (unit, container string) {
	for line := range strings.SplitSeq(cgroup, "\n") {
		if m := dockerCgroupID.FindStringSubmatch(line); m != nil {
			return "", m[1][:12]
		}
	}
	for line := range strings.SplitSeq(cgroup, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, el := range slices.Backward(strings.Split(parts[2], "/")) {
			if strings.HasSuffix(el, ".service") {
				return el, ""
			}
		}
	}
	return "", ""
}

// parseSeaweedCmdline herkent "weed [globale vlaggen] <rol> [vlaggen]".
func parseSeaweedCmdline(args []string) (protocol.SeaweedProcess, bool) {
	var p protocol.SeaweedProcess
	if len(args) < 2 || path.Base(args[0]) != "weed" {
		return p, false
	}
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		name := strings.TrimLeft(a, "-")
		if !strings.Contains(name, "=") && slices.Contains(seaweedGlobalValueFlags, name) {
			i++
		}
	}
	if i >= len(args) || !slices.Contains(seaweedRoles, args[i]) {
		return p, false
	}
	p.Role = args[i]
	p.Args = maskSeaweedArgs(args[1:])
	flags := args[i+1:]
	get := func(names ...string) string {
		for _, n := range names {
			if v := seaweedFlag(flags, n); v != "" {
				return v
			}
		}
		return ""
	}
	list := func(names ...string) []string {
		var out []string
		for s := range strings.SplitSeq(get(names...), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	port := func(def int, names ...string) int {
		if n, err := strconv.Atoi(get(names...)); err == nil && n > 0 {
			return n
		}
		return def
	}
	on := func(name string) bool {
		v, ok := seaweedFlagSet(flags, name)
		return ok && v != "false"
	}
	p.Ports = map[string]int{}
	p.IP = get("ip")
	p.DataCenter = get("dataCenter")
	p.Rack = get("rack")
	switch p.Role {
	case "master":
		p.Ports["master"] = port(9333, "port")
		p.MetaDir = get("mdir")
		p.Peers = list("peers")
		p.DefaultReplication = get("defaultReplication")
		if p.DefaultReplication == "" {
			p.DefaultReplication = "000"
		}
	case "volume":
		p.Ports["volume"] = port(8080, "port")
		p.Dirs = list("dir")
		if len(p.Dirs) == 0 {
			p.Dirs = []string{os.TempDir()}
		}
		p.Masters = list("mserver", "master")
		if len(p.Masters) == 0 {
			p.Masters = []string{"localhost:9333"}
		}
	case "filer":
		p.Ports["filer"] = port(8888, "port")
		p.Masters = list("master")
		if on("s3") {
			p.Ports["s3"] = port(8333, "s3.port")
		}
		if on("webdav") {
			p.Ports["webdav"] = port(7333, "webdav.port")
		}
	case "s3":
		p.Ports["s3"] = port(8333, "port")
	case "webdav":
		p.Ports["webdav"] = port(7333, "port")
	case "server":
		p.Ports["master"] = port(9333, "master.port")
		p.Ports["volume"] = port(8080, "volume.port")
		p.Dirs = list("dir")
		if len(p.Dirs) == 0 {
			p.Dirs = []string{os.TempDir()}
		}
		p.MetaDir = get("master.dir", "mdir")
		p.Peers = list("master.peers")
		p.DefaultReplication = get("master.defaultReplication", "defaultReplication")
		if p.DefaultReplication == "" {
			p.DefaultReplication = "000"
		}
		if on("filer") {
			p.Ports["filer"] = port(8888, "filer.port")
		}
		if on("s3") {
			p.Ports["s3"] = port(8333, "s3.port")
		}
		if on("webdav") {
			p.Ports["webdav"] = port(7333, "webdav.port")
		}
		if on("volume.disabled") {
			delete(p.Ports, "volume")
			p.Dirs = nil
		}
	case "mount":
		p.Dirs = list("dir")
	}
	if len(p.Ports) == 0 {
		p.Ports = nil
	}
	return p, true
}

// seaweedFlagSet zoekt -name=waarde, --name=waarde of -name waarde.
func seaweedFlagSet(args []string, name string) (string, bool) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		n := strings.TrimLeft(a, "-")
		if k, v, ok := strings.Cut(n, "="); ok {
			if k == name {
				return v, true
			}
			continue
		}
		if n != name {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1], true
		}
		return "", true
	}
	return "", false
}

func seaweedFlag(args []string, name string) string {
	v, _ := seaweedFlagSet(args, name)
	return v
}

// maskSeaweedArgs vervangt de waarde van geheime vlaggen door ***.
func maskSeaweedArgs(args []string) []string {
	out := slices.Clone(args)
	for i, a := range out {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		n := strings.TrimLeft(a, "-")
		if k, _, ok := strings.Cut(n, "="); ok {
			if seaweedSecretFlag.MatchString(k) {
				out[i] = a[:len(a)-len(n)] + k + "=***"
			}
			continue
		}
		if seaweedSecretFlag.MatchString(n) && i+1 < len(out) && !strings.HasPrefix(out[i+1], "-") {
			out[i+1] = "***"
		}
	}
	return out
}

// parseSeaweedToml leest alleen de namen van secties met enabled = true, en
// of er een JWT-sleutel staat. Waarden komen er nooit uit.
func parseSeaweedToml(data []byte) protocol.SeaweedConfig {
	var cfg protocol.SeaweedConfig
	section := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[] ")
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		switch {
		case k == "enabled" && v == "true" && section != "":
			cfg.Enabled = append(cfg.Enabled, section)
		case k == "key" && strings.HasPrefix(section, "jwt.") && strings.Trim(v, `"'`) != "":
			cfg.JWT = true
		}
	}
	return cfg
}

// seaweedLive vraagt de master en de volume server op deze node naar hun
// toestand. Wat niet antwoordt, blijft leeg.
func (c *Collector) seaweedLive(ctx context.Context, f *protocol.SeaweedFacts) {
	for _, p := range f.Processes {
		hosts := []string{"127.0.0.1"}
		if p.IP != "" && p.IP != "127.0.0.1" && p.IP != "localhost" {
			hosts = append(hosts, p.IP)
		}
		if port := p.Ports["master"]; port > 0 && f.Master == nil {
			for _, h := range hosts {
				base := "http://" + net.JoinHostPort(h, strconv.Itoa(port))
				if m, v, err := c.seaweedMaster(ctx, base); err == nil {
					f.Master = m
					if f.Version == "" {
						f.Version = v
					}
					break
				}
			}
		}
		if port := p.Ports["volume"]; port > 0 && f.Volume == nil {
			for _, h := range hosts {
				base := "http://" + net.JoinHostPort(h, strconv.Itoa(port))
				if v, ver, err := c.seaweedVolume(ctx, base); err == nil {
					f.Volume = v
					if f.Version == "" {
						f.Version = ver
					}
					break
				}
			}
		}
	}
}

func (c *Collector) seaweedGet(ctx context.Context, url string, into any) error {
	if c.HTTPGet != nil {
		data, err := c.HTTPGet(ctx, url)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, into)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	return json.NewDecoder(bytes.NewReader(data)).Decode(into)
}

func (c *Collector) seaweedMaster(ctx context.Context, base string) (*protocol.SeaweedMaster, string, error) {
	var cs struct {
		IsLeader bool            `json:"IsLeader"`
		Leader   string          `json:"Leader"`
		Peers    json.RawMessage `json:"Peers"`
	}
	if err := c.seaweedGet(ctx, base+"/cluster/status", &cs); err != nil {
		return nil, "", err
	}
	m := &protocol.SeaweedMaster{IsLeader: cs.IsLeader, Leader: cs.Leader, Peers: stringsOrKeys(cs.Peers)}
	var ds struct {
		Version  string `json:"Version"`
		Topology struct {
			Max         int `json:"Max"`
			Free        int `json:"Free"`
			DataCenters []struct {
				ID    string `json:"Id"`
				Racks []struct {
					ID        string `json:"Id"`
					DataNodes []struct {
						URL     string `json:"Url"`
						Volumes int    `json:"Volumes"`
						Max     int    `json:"Max"`
					} `json:"DataNodes"`
				} `json:"Racks"`
			} `json:"DataCenters"`
			Layouts []struct {
				Collection  string `json:"collection"`
				Replication string `json:"replication"`
				Writables   []int  `json:"writables"`
			} `json:"Layouts"`
		} `json:"Topology"`
	}
	if err := c.seaweedGet(ctx, base+"/dir/status", &ds); err != nil {
		// Een volger stuurt /dir/status soms door naar de leider; dan weten
		// we alleen het raft-deel.
		return m, "", nil
	}
	m.MaxVolumes, m.FreeVolumes = ds.Topology.Max, ds.Topology.Free
	for _, dc := range ds.Topology.DataCenters {
		for _, r := range dc.Racks {
			for _, n := range r.DataNodes {
				m.DataNodes = append(m.DataNodes, protocol.SeaweedDataNode{URL: n.URL, DataCenter: dc.ID, Rack: r.ID, Volumes: n.Volumes, Max: n.Max})
			}
		}
	}
	for _, l := range ds.Topology.Layouts {
		m.Layouts = append(m.Layouts, protocol.SeaweedLayout{Collection: l.Collection, Replication: l.Replication, Writable: len(l.Writables)})
	}
	return m, ds.Version, nil
}

func (c *Collector) seaweedVolume(ctx context.Context, base string) (*protocol.SeaweedVolume, string, error) {
	var st struct {
		Version string `json:"Version"`
		Volumes []struct {
			Size             uint64          `json:"Size"`
			FileCount        uint64          `json:"FileCount"`
			ReadOnly         bool            `json:"ReadOnly"`
			Collection       string          `json:"Collection"`
			ReplicaPlacement json.RawMessage `json:"ReplicaPlacement"`
		} `json:"Volumes"`
	}
	if err := c.seaweedGet(ctx, base+"/status", &st); err != nil {
		return nil, "", err
	}
	v := &protocol.SeaweedVolume{Volumes: len(st.Volumes), Replications: map[string]int{}}
	for _, vol := range st.Volumes {
		v.Bytes += vol.Size
		v.Files += vol.FileCount
		if vol.ReadOnly {
			v.ReadOnly++
		}
		v.Replications[replicaPlacement(vol.ReplicaPlacement)]++
		if vol.Collection != "" && !slices.Contains(v.Collections, vol.Collection) {
			v.Collections = append(v.Collections, vol.Collection)
		}
	}
	if len(v.Replications) == 0 {
		v.Replications = nil
	}
	slices.Sort(v.Collections)
	return v, st.Version, nil
}

// replicaPlacement maakt van {"dc":0,"rack":0,"node":2} of "002" de
// gewone schrijfwijze xyz.
func replicaPlacement(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var m map[string]int
	if json.Unmarshal(raw, &m) != nil {
		return "000"
	}
	pick := func(keys ...string) int {
		for _, k := range keys {
			if n, ok := m[k]; ok {
				return n
			}
		}
		return 0
	}
	return fmt.Sprintf("%d%d%d",
		pick("dc", "DiffDataCenterCount"), pick("rack", "DiffRackCount"), pick("node", "SameRackCount"))
}

// stringsOrKeys leest een lijst van strings, of de sleutels van een object.
func stringsOrKeys(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for k := range obj {
			list = append(list, k)
		}
		slices.Sort(list)
	}
	return list
}
