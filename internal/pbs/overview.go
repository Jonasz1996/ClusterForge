package pbs

// Het overzicht van een PBS (fase 32 M1): de machine, de datastores met
// gebruik en voorspelling, de jobs met hun laatste uitkomst, de mislukte
// taken van het laatste etmaal en wat aandacht vraagt.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Overview is wat de pagina Backup Server toont.
type Overview struct {
	Server     Server      `json:"server"`
	Status     *NodeStatus `json:"status,omitempty"`
	Version    string      `json:"version,omitempty"`
	Datastores []Datastore `json:"datastores"`
	Jobs       []Job       `json:"jobs"`
	// Failed zijn de mislukte taken van de laatste 24 uur.
	Failed       []Task    `json:"failed"`
	Running      int       `json:"running"`
	Subscription string    `json:"subscription,omitempty"`
	Warnings     []Warning `json:"warnings"`
	// Errors zijn de delen die niet te lezen waren.
	Errors []string `json:"errors"`
}

type NodeStatus struct {
	CPU     float64   `json:"cpu"`
	Wait    float64   `json:"wait"`
	Load    []float64 `json:"loadavg"`
	Memory  Usage     `json:"memory"`
	Swap    Usage     `json:"swap"`
	Root    Usage     `json:"root"`
	Uptime  int64     `json:"uptime"`
	Kernel  string    `json:"kversion"`
	CPUInfo struct {
		Model   string `json:"model"`
		CPUs    int    `json:"cpus"`
		Sockets int    `json:"sockets"`
	} `json:"cpuinfo"`
	Info struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"info"`
}

type Usage struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free,omitempty"`
	Avail int64 `json:"avail,omitempty"`
}

// Datastore is één datastore met gebruik (status/datastore-usage).
type Datastore struct {
	Store string `json:"store"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
	Avail int64  `json:"avail"`
	// History is het gebruik als fractie (0-1) per HistoryDelta seconden.
	History      []*float64 `json:"history,omitempty"`
	HistoryStart int64      `json:"history-start,omitempty"`
	HistoryDelta int64      `json:"history-delta,omitempty"`
	// EstimatedFull is het moment (unix) waarop hij vol is; 0 onbekend,
	// -1 nooit.
	EstimatedFull int64           `json:"estimated-full-date,omitempty"`
	GCStatus      json.RawMessage `json:"gc-status,omitempty"`
	MountStatus   string          `json:"mount-status,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// Job is een prune-, verify-, sync- of GC-job met zijn laatste uitkomst.
type Job struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Store     string `json:"store"`
	Schedule  string `json:"schedule,omitempty"`
	Disabled  bool   `json:"disable,omitempty"`
	LastState string `json:"last-run-state,omitempty"`
	LastEnd   int64  `json:"last-run-endtime,omitempty"`
	LastUPID  string `json:"last-run-upid,omitempty"`
	NextRun   int64  `json:"next-run,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Comment   string `json:"comment,omitempty"`
}

// Task is een taak van PBS (/nodes/localhost/tasks).
type Task struct {
	UPID       string `json:"upid"`
	WorkerType string `json:"worker_type"`
	WorkerID   string `json:"worker_id,omitempty"`
	User       string `json:"user"`
	StartTime  int64  `json:"starttime"`
	EndTime    *int64 `json:"endtime,omitempty"`
	Status     string `json:"status,omitempty"`
}

type Warning struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	Area  string `json:"area"`
}

// Overview leest alles van één PBS tegelijk.
func (s *Service) Overview(ctx context.Context, id uuid.UUID) (Overview, error) {
	v, err := s.Server(ctx, id)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Server: v, Datastores: []Datastore{}, Jobs: []Job{}, Failed: []Task{}, Warnings: []Warning{}, Errors: []string{}}
	if v.Why != "" {
		return o, ValidationError{v.Why}
	}
	now := s.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup
	fail := func(what string, err error) {
		mu.Lock()
		o.Errors = append(o.Errors, what+": "+err.Error())
		mu.Unlock()
	}
	read := func(what, path string, params map[string]string, into any) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.GetInto(ctx, id, path, params, into); err != nil {
				fail(what, err)
			}
		}()
	}
	var st NodeStatus
	var ver struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}
	var sub struct {
		Status string `json:"status"`
	}
	var running, recent []Task
	jobLists := map[string]*[]Job{"gc": {}, "prune": {}, "verify": {}, "sync": {}}
	read("status", "/nodes/localhost/status", nil, &st)
	read("versie", "/version", nil, &ver)
	read("datastores", "/status/datastore-usage", nil, &o.Datastores)
	read("abonnement", "/nodes/localhost/subscription", nil, &sub)
	read("lopende taken", "/nodes/localhost/tasks", map[string]string{"running": "1", "limit": "200"}, &running)
	read("taken", "/nodes/localhost/tasks", map[string]string{"errors": "1", "since": fmt.Sprint(now.Add(-24 * time.Hour).Unix()), "limit": "200"}, &recent)
	for kind, list := range jobLists {
		read(kind+"-jobs", "/admin/"+kind, nil, list)
	}
	wg.Wait()
	if st.Memory.Total > 0 || st.Uptime > 0 {
		o.Status = &st
	}
	o.Version = ver.Version
	if ver.Release != "" {
		o.Version += "." + ver.Release
	}
	o.Subscription = sub.Status
	o.Running = len(running)
	for _, t := range recent {
		if t.EndTime != nil && t.Status != "OK" && !strings.HasPrefix(t.Status, "WARNINGS") {
			o.Failed = append(o.Failed, t)
		}
	}
	sort.Slice(o.Failed, func(i, j int) bool { return o.Failed[i].StartTime > o.Failed[j].StartTime })
	for kind, list := range jobLists {
		for _, j := range *list {
			j.Kind = kind
			if kind == "gc" && j.ID == "" {
				j.ID = j.Store
			}
			o.Jobs = append(o.Jobs, j)
		}
	}
	sort.Slice(o.Jobs, func(i, j int) bool {
		if o.Jobs[i].Kind != o.Jobs[j].Kind {
			return o.Jobs[i].Kind < o.Jobs[j].Kind
		}
		return o.Jobs[i].ID < o.Jobs[j].ID
	})
	sort.Slice(o.Datastores, func(i, j int) bool { return o.Datastores[i].Store < o.Datastores[j].Store })
	sort.Strings(o.Errors)
	o.Warnings = warnings(o, now)
	return o, nil
}

func pct(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

// warnings zegt wat aandacht vraagt.
func warnings(o Overview, now time.Time) []Warning {
	out := []Warning{}
	add := func(level, area, f string, a ...any) {
		out = append(out, Warning{Level: level, Area: area, Text: fmt.Sprintf(f, a...)})
	}
	for _, d := range o.Datastores {
		if d.Error != "" {
			add("crit", "datastore", "Datastore %s: %s", d.Store, d.Error)
			continue
		}
		if d.MountStatus == "notmounted" {
			add("warn", "datastore", "Datastore %s is niet aangekoppeld", d.Store)
			continue
		}
		p := pct(d.Used, d.Total)
		switch {
		case p >= 95:
			add("crit", "datastore", "Datastore %s is %.0f%% vol", d.Store, p)
		case p >= 85:
			add("warn", "datastore", "Datastore %s is %.0f%% vol", d.Store, p)
		}
		if d.EstimatedFull > 0 {
			days := time.Unix(d.EstimatedFull, 0).Sub(now).Hours() / 24
			switch {
			case days < 14:
				add("crit", "datastore", "Datastore %s is volgens de trend over %.0f dagen vol", d.Store, max(days, 0))
			case days < 60:
				add("warn", "datastore", "Datastore %s is volgens de trend over %.0f dagen vol", d.Store, days)
			}
		}
	}
	for _, j := range o.Jobs {
		if j.Disabled || j.LastState == "" || j.LastState == "OK" || strings.HasPrefix(j.LastState, "WARNINGS") {
			continue
		}
		add("crit", "job", "%s %s (%s) mislukte de laatste keer: %s", jobLabel(j.Kind), j.ID, j.Store, j.LastState)
	}
	gcSeen := map[string]bool{}
	for _, j := range o.Jobs {
		if j.Kind == "gc" {
			gcSeen[j.Store] = true
			if j.Schedule == "" && j.LastEnd == 0 {
				add("warn", "job", "Datastore %s heeft geen schema voor garbage collection: verwijderde back-ups geven geen ruimte terug", j.Store)
			}
		}
	}
	verified := map[string]bool{}
	for _, j := range o.Jobs {
		if j.Kind == "verify" && !j.Disabled {
			verified[j.Store] = true
		}
	}
	for _, d := range o.Datastores {
		if d.Error == "" && !verified[d.Store] {
			add("warn", "job", "Datastore %s heeft geen verify-job: een kapotte back-up valt pas op bij terugzetten", d.Store)
		}
	}
	if n := len(o.Failed); n > 0 {
		types := map[string]int{}
		for _, t := range o.Failed {
			types[taskLabel(t.WorkerType)]++
		}
		var parts []string
		for k, c := range types {
			parts = append(parts, fmt.Sprintf("%d× %s", c, k))
		}
		sort.Strings(parts)
		add("crit", "taak", "%d mislukte taken in het laatste etmaal: %s", n, strings.Join(parts, ", "))
	}
	if o.Status != nil {
		if p := pct(o.Status.Root.Used, o.Status.Root.Total); p >= 90 {
			add("warn", "systeem", "De systeemschijf van PBS is %.0f%% vol", p)
		}
		if p := pct(o.Status.Memory.Used, o.Status.Memory.Total); p >= 95 {
			add("warn", "systeem", "Het geheugen van PBS is %.0f%% vol", p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level == "crit" && out[j].Level != "crit" })
	return out
}

func jobLabel(kind string) string {
	switch kind {
	case "gc":
		return "Garbage collection"
	case "prune":
		return "Prune-job"
	case "verify":
		return "Verify-job"
	case "sync":
		return "Sync-job"
	}
	return kind
}

var taskLabels = map[string]string{
	"backup": "back-up", "reader": "terugzetten/lezen", "garbage_collection": "garbage collection",
	"verify": "verificatie", "verificationjob": "verify-job", "verify_snapshot": "verificatie snapshot", "verify_group": "verificatie groep",
	"prune": "prune", "prunejob": "prune-job", "syncjob": "sync-job", "sync": "sync",
	"aptupdate": "pakketlijsten", "create-datastore": "datastore maken", "delete-datastore": "datastore verwijderen",
	"zfscreate": "ZFS-pool maken", "dircreate": "map maken", "srvreload": "dienst herladen", "srvrestart": "dienst herstarten",
	"acme-new-cert": "ACME-certificaat", "logrotate": "logrotatie", "mount-device": "aankoppelen", "unmount-device": "loskoppelen",
}

func taskLabel(t string) string {
	if l, ok := taskLabels[t]; ok {
		return l
	}
	return t
}
