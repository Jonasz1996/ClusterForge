// Package pvefake is een nep-Proxmox voor tests en lokale ontwikkeling. Hij
// kent net genoeg van de API om ClusterForge te bedienen: resources, power,
// snapshots, migratie, klonen, configuratie, de guest agent, back-ups en
// taken.
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
	// Pool is de resource pool.
	Pool string
	// Hostname, OS en Filesystems zijn wat de guest agent meldt; leeg
	// geeft de naam van de VM, Debian 13 en één bestandssysteem.
	Hostname    string
	OS          string
	Filesystems []string

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

// Backup is een back-up op een storage. Een storage waarvan de naam met
// "pbs" begint, speelt Proxmox Backup Server.
type Backup struct {
	Storage   string
	VMID      int
	Type      string // qemu of lxc
	Time      time.Time
	Size      int64
	Notes     string
	Protected bool
	// Verify is de verificatie van Proxmox Backup Server: "", ok of failed.
	Verify string
	// Config is de VM-configuratie in de back-up; nil neemt die van de VM
	// met hetzelfde VMID, of een eenvoudige.
	Config map[string]string
}

func (b Backup) volid() string { return b.volume()["volid"].(string) }

func (b Backup) volume() map[string]any {
	typ := b.Type
	if typ == "" {
		typ = "qemu"
	}
	v := map[string]any{
		"content": "backup", "vmid": b.VMID, "subtype": typ, "ctime": b.Time.Unix(), "size": b.Size,
		"notes": b.Notes,
	}
	if b.Protected {
		v["protected"] = 1
	}
	if strings.HasPrefix(b.Storage, "pbs") {
		kind := map[string]string{"qemu": "vm", "lxc": "ct"}[typ]
		v["volid"] = fmt.Sprintf("%s:backup/%s/%d/%s", b.Storage, kind, b.VMID, b.Time.UTC().Format("2006-01-02T15:04:05Z"))
		v["format"] = "pbs-" + kind
		if b.Verify != "" {
			v["verification"] = map[string]any{"state": b.Verify, "upid": "UPID:pbs:verify"}
		}
		return v
	}
	ext := map[string]string{"qemu": "vma.zst", "lxc": "tar.zst"}[typ]
	v["volid"] = fmt.Sprintf("%s:backup/vzdump-%s-%d-%s.%s", b.Storage, typ, b.VMID, b.Time.UTC().Format("2006_01_02-15_04_05"), ext)
	v["format"] = ext
	return v
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
	backups     []Backup
	// hideBackups speelt een token zonder VM.Backup: Proxmox laat dan alle
	// back-ups weg uit de lijst, zonder fout.
	hideBackups bool
	// backupJobs zijn de VMID's die in een back-upjob zitten.
	backupJobs map[int]bool
	// pools zijn de resource pools die bestaan.
	pools map[string]bool
}

// AddPool maakt een resource pool, zoals pveum pool add.
func (s *Server) AddPool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pools == nil {
		s.pools = map[string]bool{}
	}
	s.pools[name] = true
}

// Guests geeft de VMID's van alle VM's en containers.
func (s *Server) Guests() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	var out []int
	for _, g := range s.guests {
		out = append(out, g.VMID)
	}
	return out
}

// SetGuestConfig zet één configsleutel van buitenaf.
func (s *Server) SetGuestConfig(vmid int, key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.guests {
		if g.VMID == vmid {
			if g.Config == nil {
				g.Config = map[string]string{}
			}
			g.Config[key] = value
		}
	}
}

// AddBackup zet een back-up op een storage.
func (s *Server) AddBackup(b Backup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backups = append(s.backups, b)
}

// RemoveBackups verwijdert de back-ups van een VM, zoals een prune.
func (s *Server) RemoveBackups(vmid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backups = slices.DeleteFunc(s.backups, func(b Backup) bool { return b.VMID == vmid })
}

// HideBackups speelt een token zonder het recht VM.Backup.
func (s *Server) HideBackups(hide bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hideBackups = hide
}

// SetBackupJobs bepaalt welke VM's in een back-upjob zitten; de rest staat
// in /cluster/backup-info/not-backed-up.
func (s *Server) SetBackupJobs(vmids ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backupJobs = map[int]bool{}
	for _, id := range vmids {
		s.backupJobs[id] = true
	}
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

// SetHostOnline zet een host aan of uit.
func (s *Server) SetHostOnline(name string, online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if h.Name == name {
			h.Online = online
		}
	}
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
	mux.HandleFunc("GET "+p+"/nodes/{node}/storage/{storage}/content", s.storageContent)
	mux.HandleFunc("GET "+p+"/cluster/backup-info/not-backed-up", s.notBackedUp)
	mux.HandleFunc("GET "+p+"/nodes/{node}/vzdump/extractconfig", s.extractConfig)
	mux.HandleFunc("POST "+p+"/nodes/{node}/qemu", s.restore)
	mux.HandleFunc("DELETE "+p+"/nodes/{node}/{type}/{vmid}", s.destroy)
	mux.HandleFunc("GET "+p+"/nodes/{node}/{type}/{vmid}/agent/{command}", s.agentInfo)
	mux.HandleFunc("GET "+p+"/nodes/{node}/tasks/{upid}/status", s.taskStatus)
	mux.HandleFunc("GET "+p+"/nodes/{node}/tasks/{upid}/log", s.taskLog)
	mux.HandleFunc("DELETE "+p+"/nodes/{node}/tasks/{upid}", s.stopTask)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		authorized := r.Header.Get("Authorization") == "PVEAPIToken="+s.token
		// Het teruglezen van een configuratie telt ook, zodat een test de
		// volgorde wijzigen, teruglezen, starten kan bewijzen.
		if authorized && (r.Method != http.MethodGet || strings.HasSuffix(r.URL.Path, "/config")) {
			call := r.Method + " " + strings.TrimPrefix(r.URL.Path, p)
			if r.URL.RawQuery != "" && r.Method == http.MethodDelete {
				call += "?" + r.URL.RawQuery
			}
			s.calls = append(s.calls, call)
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
		if g.Pool != "" {
			m["pool"] = g.Pool
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

func (s *Server) storageContent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, node := r.PathValue("storage"), r.PathValue("node")
	if !slices.ContainsFunc(s.storages, func(st Storage) bool { return st.Name == name && (st.Shared || st.Node == node) }) {
		fail(w, http.StatusInternalServerError, fmt.Sprintf("storage '%s' does not exist", name))
		return
	}
	out := []map[string]any{}
	if c := r.URL.Query().Get("content"); c != "" && c != "backup" || s.hideBackups {
		ok(w, out)
		return
	}
	for _, b := range s.backups {
		if b.Storage == name {
			out = append(out, b.volume())
		}
	}
	ok(w, out)
}

func (s *Server) notBackedUp(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, g := range s.guests {
		if !g.Template && !s.backupJobs[g.VMID] {
			out = append(out, map[string]any{"vmid": g.VMID, "name": g.Name, "type": g.Type})
		}
	}
	ok(w, out)
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
	for _, k := range strings.Split(r.PostForm.Get("delete"), ",") {
		delete(g.Config, strings.TrimSpace(k))
	}
	for k, v := range r.PostForm {
		if k == "delete" {
			continue
		}
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

// configText geeft een configuratie in de tekstvorm van Proxmox.
func configText(cfg map[string]string) string {
	keys := slices.Sorted(maps.Keys(cfg))
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, cfg[k])
	}
	return b.String()
}

// backupConfig is de configuratie in een back-up.
func (s *Server) backupConfig(b Backup) map[string]string {
	if b.Config != nil {
		return maps.Clone(b.Config)
	}
	for _, g := range s.guests {
		if g.VMID == b.VMID && g.Config != nil {
			cfg := maps.Clone(g.Config)
			cfg["name"] = g.Name
			return cfg
		}
	}
	return map[string]string{
		"name": fmt.Sprintf("vm%d", b.VMID), "cores": "2", "memory": "2048", "ostype": "l26", "agent": "1",
		"scsi0":  fmt.Sprintf("local-lvm:vm-%d-disk-0,size=32G", b.VMID),
		"net0":   "virtio=BC:24:11:00:00:01,bridge=vmbr0",
		"scsihw": "virtio-scsi-single", "boot": "order=scsi0",
	}
}

func (s *Server) findBackup(volid string) (Backup, bool) {
	for _, b := range s.backups {
		if b.volid() == volid {
			return b, true
		}
	}
	return Backup{}, false
}

func (s *Server) reachable(storage, node string) bool {
	return slices.ContainsFunc(s.storages, func(st Storage) bool { return st.Name == storage && (st.Shared || st.Node == node) })
}

func (s *Server) extractConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	volid := r.URL.Query().Get("volume")
	b, found := s.findBackup(volid)
	if !found || !s.reachable(b.Storage, r.PathValue("node")) {
		fail(w, http.StatusInternalServerError, "unable to parse volume ID '"+volid+"'")
		return
	}
	ok(w, configText(s.backupConfig(b)))
}

// isDisk is true voor een configsleutel die een schijf is.
func isDisk(k string) bool {
	for _, p := range []string{"scsi", "virtio", "sata", "ide", "efidisk", "tpmstate", "unused"} {
		if rest, found := strings.CutPrefix(k, p); found && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// restore speelt qmrestore: een nieuwe VM uit een back-up, nooit over een
// bestaande.
func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	_ = r.ParseForm()
	node := r.PathValue("node")
	archive, storage, pool := r.PostForm.Get("archive"), r.PostForm.Get("storage"), r.PostForm.Get("pool")
	vmid, _ := strconv.Atoi(r.PostForm.Get("vmid"))
	if r.PostForm.Get("force") != "" {
		fail(w, http.StatusBadRequest, "force is not allowed in this fake")
		return
	}
	if archive == "" {
		fail(w, http.StatusNotImplemented, "creating a VM without archive is not supported by this fake")
		return
	}
	if vmid < 100 || slices.ContainsFunc(s.guests, func(g *Guest) bool { return g.VMID == vmid }) {
		fail(w, http.StatusInternalServerError, fmt.Sprintf("unable to restore VM %d - VM %d already exists", vmid, vmid))
		return
	}
	b, found := s.findBackup(archive)
	if !found || !s.reachable(b.Storage, node) {
		fail(w, http.StatusInternalServerError, "unable to parse volume ID '"+archive+"'")
		return
	}
	if pool != "" && !s.pools[pool] {
		fail(w, http.StatusInternalServerError, "pool '"+pool+"' does not exist")
		return
	}
	if !s.reachable(storage, node) {
		fail(w, http.StatusInternalServerError, "storage '"+storage+"' does not exist")
		return
	}
	cfg := s.backupConfig(b)
	n := 0
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		v := cfg[k]
		vol, opts, _ := strings.Cut(v, ",")
		if !isDisk(k) || vol == "none" || strings.Contains(v, "media=cdrom") && !strings.Contains(v, "cloudinit") {
			continue
		}
		name := fmt.Sprintf("vm-%d-disk-%d", vmid, n)
		if strings.Contains(v, "cloudinit") {
			name = fmt.Sprintf("vm-%d-cloudinit", vmid)
		} else {
			n++
		}
		cfg[k] = storage + ":" + name
		if opts != "" {
			cfg[k] += "," + opts
		}
	}
	if r.PostForm.Get("unique") == "1" {
		for k, v := range cfg {
			if rest, found := strings.CutPrefix(k, "net"); found && strings.Trim(rest, "0123456789") == "" {
				// Een nieuw MAC-adres, zoals unique=1 bij Proxmox.
				model, after, _ := strings.Cut(v, "=")
				_, opts, _ := strings.Cut(after, ",")
				cfg[k] = fmt.Sprintf("%s=BC:24:11:%02X:%02X:7A", model, vmid>>8&0xff, vmid&0xff)
				if opts != "" {
					cfg[k] += "," + opts
				}
			}
		}
	}
	name := cfg["name"]
	src, _ := s.guestByID(b.VMID)
	g := &Guest{
		Type: "qemu", VMID: vmid, Name: name, Node: node, Status: "stopped", Config: cfg, Files: map[string]string{},
		Pool: pool, NoAgent: cfg["agent"] == "" || strings.HasPrefix(cfg["agent"], "0") || strings.Contains(cfg["agent"], "enabled=0"),
	}
	if c, err := strconv.Atoi(cfg["cores"]); err == nil {
		g.MaxCPU = c
	}
	if m, err := strconv.ParseInt(cfg["memory"], 10, 64); err == nil {
		g.MaxMem = m << 20
	}
	if src != nil {
		g.Hostname, g.OS, g.Filesystems = src.Hostname, src.OS, slices.Clone(src.Filesystems)
		if g.Hostname == "" {
			g.Hostname = src.Name
		}
	}
	s.guests = append(s.guests, g)
	upid := s.newTask(node, "qmrestore", vmid, nil, "restore vma archive: "+archive, "map 'drive-scsi0' to '"+cfg["scsi0"]+"'")
	if t := s.tasks[upid]; t.exit != "OK" {
		// Een mislukt terugzetten laat geen VM achter.
		s.guests = slices.DeleteFunc(s.guests, func(x *Guest) bool { return x == g })
	}
	ok(w, upid)
}

func (s *Server) guestByID(vmid int) (*Guest, bool) {
	for _, g := range s.guests {
		if g.VMID == vmid {
			return g, true
		}
	}
	return nil, false
}

func (s *Server) destroy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishTasks()
	g := s.guest(w, r)
	if g == nil {
		return
	}
	if r.URL.Query().Get("destroy-unreferenced-disks") == "1" {
		fail(w, http.StatusBadRequest, "destroy-unreferenced-disks is not allowed in this fake")
		return
	}
	if g.Config["protection"] == "1" {
		fail(w, http.StatusInternalServerError, fmt.Sprintf("can't remove VM %d - protection mode enabled", g.VMID))
		return
	}
	if g.Status == "running" {
		fail(w, http.StatusInternalServerError, fmt.Sprintf("VM %d is running - destroy failed", g.VMID))
		return
	}
	kind := map[string]string{"qemu": "qm", "lxc": "vz"}[g.Type] + "destroy"
	ok(w, s.newTask(g.Node, kind, g.VMID, func() {
		s.guests = slices.DeleteFunc(s.guests, func(x *Guest) bool { return x == g })
	}, fmt.Sprintf("destroy VM %d", g.VMID)))
}

func (s *Server) agentInfo(w http.ResponseWriter, r *http.Request) {
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
	var result any
	switch r.PathValue("command") {
	case "get-host-name":
		h := g.Hostname
		if h == "" {
			h = g.Name
		}
		result = map[string]any{"host-name": h}
	case "get-osinfo":
		pretty := g.OS
		if pretty == "" {
			pretty = "Debian GNU/Linux 13 (trixie)"
		}
		result = map[string]any{"id": "debian", "name": "Debian GNU/Linux", "pretty-name": pretty, "version-id": "13"}
	case "get-fsinfo":
		mounts := g.Filesystems
		if mounts == nil {
			mounts = []string{"/"}
		}
		fs := []map[string]any{}
		for i, m := range mounts {
			fs = append(fs, map[string]any{"name": fmt.Sprintf("sda%d", i+1), "mountpoint": m, "type": "ext4",
				"total-bytes": int64(30 << 30), "used-bytes": int64(4 << 30)})
		}
		result = fs
	default:
		fail(w, http.StatusNotImplemented, "Method not implemented")
		return
	}
	ok(w, map[string]any{"result": result})
}
