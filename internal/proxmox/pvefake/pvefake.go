// Package pvefake is een nep-Proxmox voor tests en lokale ontwikkeling. Hij
// kent net genoeg van de API om ClusterForge te bedienen: resources, power,
// snapshots, migratie, klonen, configuratie, de guest agent en taken.
package pvefake

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Host struct {
	Name   string
	Online bool
	MaxCPU int
	MaxMem int64
	CPU    float64
	Mem    int64
}

type Guest struct {
	Type      string // qemu of lxc
	VMID      int
	Name      string
	Node      string
	Status    string // running, stopped
	Template  bool
	MaxCPU    int
	MaxMem    int64
	Mem       int64
	CPU       float64
	Tags      string
	Snapshots []string
	// Config is de VM-configuratie zoals GET .../config hem geeft.
	Config map[string]string
	// Files zijn de bestanden die via de guest agent geschreven zijn.
	Files map[string]string
	// NoAgent: de guest agent antwoordt nooit.
	NoAgent bool

	startedAt time.Time
}

type Storage struct {
	Name    string
	Node    string
	Shared  bool
	Disk    int64
	MaxDisk int64
	// Content is wat erop mag, zoals "images,rootdir"; leeg is dat.
	Content string
}

type task struct {
	upid     string
	node     string
	done     time.Time
	exit     string
	log      []string
	apply    func()
	stopped  bool
	finished bool
}

// Server is de nep-Proxmox. Tests wijzigen hem via de methodes, die veilig
// zijn naast lopende requests.
type Server struct {
	mu sync.Mutex
	// token is de verwachte Authorization-header, zonder "PVEAPIToken=".
	token string
	// taskDuration is hoe lang een taak loopt voor hij klaar is.
	taskDuration time.Duration
	// fail laat de volgende taak van een soort mislukken.
	fail     map[string]string
	hosts    []*Host
	guests   []*Guest
	storages []Storage
	tasks    map[string]*task
	seq      int
	// Calls telt de aanroepen per pad, voor tests.
	calls []string
	// agentDelay is hoe lang de guest agent na het starten nog niet antwoordt.
	agentDelay time.Duration
	// onFileWrite wordt aangeroepen na een file-write via de guest agent.
	onFileWrite func(vmid int, name, file, content string)
}

// SetAgentDelay bepaalt hoe lang de guest agent na het starten van een VM
// nog niet antwoordt.
func (s *Server) SetAgentDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentDelay = d
}

// OnFileWrite laat tests reageren op een bestand dat ClusterForge via de
// guest agent schrijft, zoals het aanmeldbestand van cf-agent.
func (s *Server) OnFileWrite(f func(vmid int, name, file, content string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onFileWrite = f
}

func New(token string) *Server {
	return &Server{token: token, tasks: map[string]*task{}, fail: map[string]string{}}
}

// SetToken verandert het token dat de nep-Proxmox aanvaardt.
func (s *Server) SetToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// SetTaskDuration bepaalt hoe lang nieuwe taken lopen.
func (s *Server) SetTaskDuration(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskDuration = d
}

// FailNext laat de volgende taak waarvan de soort kind bevat (qmstart,
// qmmigrate, vzshutdown, ...) mislukken met exitstatus msg.
func (s *Server) FailNext(kind, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[kind] = msg
}

func (s *Server) AddHost(h Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts = append(s.hosts, &h)
}

func (s *Server) AddGuest(g Guest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guests = append(s.guests, &g)
}

func (s *Server) AddStorage(st Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storages = append(s.storages, st)
}

// Guest geeft een kopie van de huidige toestand van een VM of container.
func (s *Server) Guest(vmid int) (Guest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	for _, g := range s.guests {
		if g.VMID == vmid {
			c := *g
			c.Snapshots = slices.Clone(g.Snapshots)
			c.Config = maps.Clone(g.Config)
			c.Files = maps.Clone(g.Files)
			return c, true
		}
	}
	return Guest{}, false
}

// SetGuestStatus verandert de toestand van buitenaf, zoals iemand in de
// Proxmox-webinterface zou doen.
func (s *Server) SetGuestStatus(vmid int, status, node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.guests {
		if g.VMID == vmid {
			g.Status = status
			if node != "" {
				g.Node = node
			}
		}
	}
}

func (s *Server) RemoveGuest(vmid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guests = slices.DeleteFunc(s.guests, func(g *Guest) bool { return g.VMID == vmid })
}

// Calls geeft de schrijvende aanroepen tot nu toe, als "POST /pad".
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	p := "/api2/json"
	mux.HandleFunc("GET "+p+"/version", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]string{"version": "8.4.1", "release": "8.4", "repoid": "fake"})
	})
	mux.HandleFunc("GET "+p+"/cluster/resources", s.resources)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/status/{action}", s.power)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/snapshot", s.snapshot)
	mux.HandleFunc("GET "+p+"/nodes/{node}/{type}/{vmid}/snapshot", s.snapshots)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/migrate", s.migrate)
	mux.HandleFunc("GET "+p+"/cluster/nextid", s.nextID)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/clone", s.clone)
	mux.HandleFunc("GET "+p+"/nodes/{node}/{type}/{vmid}/config", s.config)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/config", s.setConfig)
	mux.HandleFunc("PUT "+p+"/nodes/{node}/{type}/{vmid}/resize", s.resize)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/agent/ping", s.agentPing)
	mux.HandleFunc("POST "+p+"/nodes/{node}/{type}/{vmid}/agent/file-write", s.fileWrite)
	mux.HandleFunc("GET "+p+"/nodes/{node}/tasks/{upid}/status", s.taskStatus)
	mux.HandleFunc("GET "+p+"/nodes/{node}/tasks/{upid}/log", s.taskLog)
	mux.HandleFunc("DELETE "+p+"/nodes/{node}/tasks/{upid}", s.stopTask)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		authorized := r.Header.Get("Authorization") == "PVEAPIToken="+s.token
		if authorized && r.Method != http.MethodGet {
			s.calls = append(s.calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, p))
		}
		s.mu.Unlock()
		if !authorized {
			fail(w, http.StatusUnauthorized, "authentication failure")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// fail antwoordt zoals Proxmox; die zet de reden ook in de statusregel, maar
// dat kan net/http niet.
func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": msg + "\n"})
}

func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	var out []map[string]any
	for _, h := range s.hosts {
		st := "offline"
		if h.Online {
			st = "online"
		}
		out = append(out, map[string]any{
			"id": "node/" + h.Name, "type": "node", "node": h.Name, "status": st,
			"maxcpu": h.MaxCPU, "cpu": h.CPU, "maxmem": h.MaxMem, "mem": h.Mem, "uptime": 86400,
			"maxdisk": int64(100 << 30), "disk": int64(30 << 30),
		})
	}
	for _, g := range s.guests {
		m := map[string]any{
			"id": g.Type + "/" + strconv.Itoa(g.VMID), "type": g.Type, "vmid": g.VMID, "name": g.Name,
			"node": g.Node, "status": g.Status, "maxcpu": g.MaxCPU, "maxmem": g.MaxMem,
			"maxdisk": int64(32 << 30), "disk": 0, "tags": g.Tags,
		}
		if g.Template {
			m["template"] = 1
		}
		if g.Status == "running" {
			m["cpu"], m["mem"], m["uptime"] = g.CPU, g.Mem, 3600
		}
		out = append(out, m)
	}
	for _, st := range s.storages {
		shared := 0
		if st.Shared {
			shared = 1
		}
		content := st.Content
		if content == "" {
			content = "images,rootdir"
		}
		out = append(out, map[string]any{
			"id": "storage/" + st.Node + "/" + st.Name, "type": "storage", "storage": st.Name, "node": st.Node,
			"status": "available", "shared": shared, "disk": st.Disk, "maxdisk": st.MaxDisk, "plugintype": "dir",
			"content": content,
		})
	}
	ok(w, out)
}

// guest zoekt de VM uit het pad; die moet op de genoemde host staan, zoals
// bij Proxmox.
func (s *Server) guest(w http.ResponseWriter, r *http.Request) *Guest {
	vmid, _ := strconv.Atoi(r.PathValue("vmid"))
	for _, g := range s.guests {
		if g.VMID == vmid && g.Type == r.PathValue("type") {
			if g.Node != r.PathValue("node") {
				fail(w, http.StatusInternalServerError, "not on this node")
				return nil
			}
			return g
		}
	}
	fail(w, http.StatusInternalServerError, "does not exist")
	return nil
}

// newTask start een taak die na TaskDuration klaar is en dan apply uitvoert.
func (s *Server) newTask(node, kind string, vmid int, apply func(), log ...string) string {
	s.seq++
	upid := fmt.Sprintf("UPID:%s:%08X:%08X:%08X:%s:%d:clusterforge@pve!cf:", node, 1000+s.seq, s.seq, time.Now().Unix(), kind, vmid)
	t := &task{upid: upid, node: node, done: time.Now().Add(s.taskDuration), exit: "OK", apply: apply, log: log}
	for k, msg := range s.fail {
		if strings.Contains(kind, k) {
			t.exit, t.apply = msg, nil
			t.log = append(t.log, "TASK ERROR: "+msg)
			delete(s.fail, k)
		}
	}
	s.tasks[upid] = t
	s.finishTasks()
	return upid
}

func (s *Server) finishTasks() {
	now := time.Now()
	for _, t := range s.tasks {
		if t.finished || t.stopped || now.Before(t.done) {
			continue
		}
		t.finished = true
		if t.apply != nil {
			t.apply()
		}
		if t.exit == "OK" {
			t.log = append(t.log, "TASK OK")
		}
	}
}

func (s *Server) power(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	action := r.PathValue("action")
	var to string
	switch action {
	case "start", "reboot":
		to = "running"
	case "stop", "shutdown":
		to = "stopped"
	default:
		fail(w, http.StatusNotImplemented, "Method not implemented")
		return
	}
	kind := map[string]string{"qemu": "qm", "lxc": "vz"}[g.Type] + action
	if action == "start" && g.Status == "running" {
		s.fail[kind] = fmt.Sprintf("VM %d already running", g.VMID)
	}
	if g.Template {
		s.fail[kind] = "you can't start a vm if it's a template"
	}
	ok(w, s.newTask(g.Node, kind, g.VMID, func() {
		if to == "running" && g.Status != "running" || action == "reboot" {
			g.startedAt = time.Now()
		}
		g.Status = to
	}, fmt.Sprintf("%s %d", action, g.VMID)))
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	_ = r.ParseForm()
	name := r.PostForm.Get("snapname")
	if name == "" {
		fail(w, http.StatusBadRequest, "Parameter verification failed.")
		return
	}
	ok(w, s.newTask(g.Node, map[string]string{"qemu": "qm", "lxc": "vz"}[g.Type]+"snapshot", g.VMID,
		func() { g.Snapshots = append(g.Snapshots, name) }, "snapshotting '"+name+"'"))
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	out := []map[string]any{}
	for i, name := range g.Snapshots {
		out = append(out, map[string]any{"name": name, "snaptime": 1700000000 + i, "description": ""})
	}
	out = append(out, map[string]any{"name": "current", "description": "You are here!"})
	ok(w, out)
}

func (s *Server) migrate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	_ = r.ParseForm()
	target := r.PostForm.Get("target")
	if !slices.ContainsFunc(s.hosts, func(h *Host) bool { return h.Name == target && h.Online }) || target == g.Node {
		fail(w, http.StatusInternalServerError, "target node is not online")
		return
	}
	kind := map[string]string{"qemu": "qm", "lxc": "vz"}[g.Type] + "migrate"
	ok(w, s.newTask(g.Node, kind, g.VMID, func() { g.Node = target },
		fmt.Sprintf("starting migration of VM %d to node '%s'", g.VMID, target)))
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) *task {
	s.finishTasks()
	t, found := s.tasks[r.PathValue("upid")]
	if !found || t.node != r.PathValue("node") {
		fail(w, http.StatusInternalServerError, "no such task")
		return nil
	}
	return t
}

func (s *Server) taskStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.task(w, r)
	if t == nil {
		return
	}
	switch {
	case t.stopped:
		ok(w, map[string]string{"status": "stopped", "exitstatus": "unexpected status"})
	case t.finished:
		ok(w, map[string]string{"status": "stopped", "exitstatus": t.exit})
	default:
		ok(w, map[string]string{"status": "running"})
	}
}

func (s *Server) taskLog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.task(w, r)
	if t == nil {
		return
	}
	out := []map[string]any{}
	for i, l := range t.log {
		out = append(out, map[string]any{"n": i + 1, "t": l})
	}
	ok(w, out)
}

func (s *Server) stopTask(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.task(w, r)
	if t == nil {
		return
	}
	if !t.finished {
		t.stopped = true
		t.log = append(t.log, "received interrupt", "TASK ERROR: interrupted by signal")
	}
	ok(w, nil)
}

func (s *Server) nextID(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	ok(w, strconv.Itoa(s.freeID()))
}

func (s *Server) freeID() int {
	for id := 100; ; id++ {
		if !slices.ContainsFunc(s.guests, func(g *Guest) bool { return g.VMID == id }) {
			return id
		}
	}
}

// diskStorage geeft de storage van de bootschijf, zoals local-lvm.
func diskStorage(cfg map[string]string) string {
	st, _, _ := strings.Cut(cfg["scsi0"], ":")
	return st
}

func (s *Server) clone(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	src := s.guest(w, r)
	if src == nil {
		return
	}
	_ = r.ParseForm()
	newID, _ := strconv.Atoi(r.PostForm.Get("newid"))
	if newID < 100 || slices.ContainsFunc(s.guests, func(g *Guest) bool { return g.VMID == newID }) {
		fail(w, http.StatusInternalServerError, fmt.Sprintf("unable to create VM %d: config file already exists", newID))
		return
	}
	target := r.PostForm.Get("target")
	if target == "" {
		target = src.Node
	}
	if !slices.ContainsFunc(s.hosts, func(h *Host) bool { return h.Name == target && h.Online }) {
		fail(w, http.StatusInternalServerError, "target node is not online")
		return
	}
	shared := slices.ContainsFunc(s.storages, func(st Storage) bool { return st.Name == diskStorage(src.Config) && st.Shared })
	if target != src.Node && !shared {
		fail(w, http.StatusInternalServerError, "Can't clone to non-shared storage '"+diskStorage(src.Config)+"'")
		return
	}
	cfg := maps.Clone(src.Config)
	if cfg == nil {
		cfg = map[string]string{}
	}
	if st := r.PostForm.Get("storage"); st != "" {
		_, rest, _ := strings.Cut(cfg["scsi0"], ":")
		cfg["scsi0"] = st + ":" + rest
	}
	name := r.PostForm.Get("name")
	cfg["name"] = name
	// Het id is meteen bezet, ook al loopt de kloon nog.
	g := &Guest{
		Type: "qemu", VMID: newID, Name: name, Node: target, Status: "stopped", MaxCPU: src.MaxCPU, MaxMem: src.MaxMem,
		Config: cfg, Files: map[string]string{}, NoAgent: src.NoAgent,
	}
	s.guests = append(s.guests, g)
	upid := s.newTask(src.Node, "qmclone", src.VMID, nil, fmt.Sprintf("create full clone of drive scsi0 (%s)", cfg["scsi0"]))
	if t := s.tasks[upid]; t.exit != "OK" {
		// Een mislukte kloon laat geen VM achter.
		s.guests = slices.DeleteFunc(s.guests, func(x *Guest) bool { return x == g })
	}
	ok(w, upid)
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	out := map[string]any{}
	for k, v := range g.Config {
		out[k] = v
	}
	if g.Template {
		out["template"] = 1
	}
	ok(w, out)
}

func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	_ = r.ParseForm()
	if g.Config == nil {
		g.Config = map[string]string{}
	}
	for k, v := range r.PostForm {
		if k == "sshkeys" {
			// Proxmox wil de sleutels nog eens URL-gecodeerd.
			if d, err := url.PathUnescape(v[0]); err == nil {
				v = []string{d}
			}
		}
		g.Config[k] = v[0]
		switch k {
		case "cores":
			g.MaxCPU, _ = strconv.Atoi(v[0])
		case "memory":
			mb, _ := strconv.ParseInt(v[0], 10, 64)
			g.MaxMem = mb << 20
		case "name":
			g.Name = v[0]
		case "tags":
			g.Tags = v[0]
		}
	}
	ok(w, s.newTask(g.Node, "qmconfig", g.VMID, nil, "update VM "+strconv.Itoa(g.VMID)))
}

func (s *Server) resize(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	_ = r.ParseForm()
	disk, size := r.PostForm.Get("disk"), r.PostForm.Get("size")
	cur, found := g.Config[disk]
	if !found {
		fail(w, http.StatusBadRequest, "disk '"+disk+"' does not exist")
		return
	}
	parts := strings.Split(cur, ",")
	for i, p := range parts {
		if strings.HasPrefix(p, "size=") {
			parts[i] = "size=" + size
		}
	}
	g.Config[disk] = strings.Join(parts, ",")
	ok(w, nil)
}

func (s *Server) agentReady(g *Guest) bool {
	return g.Status == "running" && !g.NoAgent && time.Since(g.startedAt) >= s.agentDelay
}

func (s *Server) agentPing(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	if !s.agentReady(g) {
		fail(w, http.StatusInternalServerError, "QEMU guest agent is not running")
		return
	}
	ok(w, map[string]any{})
}

func (s *Server) fileWrite(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		s.mu.Unlock()
		return
	}
	if !s.agentReady(g) {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "QEMU guest agent is not running")
		return
	}
	_ = r.ParseForm()
	file, content := r.PostForm.Get("file"), r.PostForm.Get("content")
	if g.Files == nil {
		g.Files = map[string]string{}
	}
	g.Files[file] = content
	hook, vmid, name := s.onFileWrite, g.VMID, g.Name
	s.mu.Unlock()
	if hook != nil {
		go hook(vmid, name, file, content)
	}
	ok(w, nil)
}
