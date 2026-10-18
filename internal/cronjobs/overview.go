package cronjobs

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/cronexpr"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Group zijn de cronjobs van één cluster, of van de nodes zonder cluster.
type Group struct {
	ClusterID   *uuid.UUID
	ClusterName string
	ClusterSlug string
	ClusterEnv  string
	Nodes       []NodeRead
	Jobs        []Job
}

// NodeRead zegt hoe het lezen van een node ging.
type NodeRead struct {
	NodeID    uuid.UUID
	Hostname  string
	Daemon    string
	Logging   bool
	Journal   bool
	Truncated bool
	Error     string
	ErrorKind string
	SyncedAt  time.Time
	OKAt      *time.Time
}

// Job is één cronjob of timer, met de nodes waarop hij staat.
type Job struct {
	Key      string
	Source   string
	File     string
	Line     int
	User     string
	Schedule string
	// Describe is het schema in gewone taal; leeg als ClusterForge het niet
	// kan lezen (zoals de monotone tijden van een timer).
	Describe     string
	Command      string
	Unit         string
	Description  string
	Package      string
	ClusterForge bool
	Nodes        []JobNode
	// NextRun is de eerste volgende run over de nodes.
	NextRun *time.Time
	// Differs zijn de nodes waar dezelfde job (zelfde bestand en
	// gebruiker) anders staat.
	Differs []Variant
	// Warnings zijn de waarschuwingen bij deze job.
	Warnings []Warning
}

// JobNode is een job op één node.
type JobNode struct {
	NodeID     uuid.UUID
	Hostname   string
	Enabled    bool
	LastRun    *time.Time
	LastEnd    *time.Time
	LastExit   *int
	LastResult string
	Runs       int
	Failures   int
	NextRun    *time.Time
}

// Overview geeft de cronjobs per cluster. Een cluster zonder gelezen node
// staat er niet in.
func (s *Service) Overview(ctx context.Context) ([]Group, error) {
	rows, err := s.q.ListNodeCrons(ctx)
	if err != nil {
		return nil, err
	}
	reads, err := s.q.ListCronReads(ctx)
	if err != nil {
		return nil, err
	}
	comps, err := s.q.ListCronComponents(ctx)
	if err != nil {
		return nil, err
	}
	var cr []engineRow
	for _, c := range comps {
		cr = append(cr, engineRow{c.ClusterID, c.Engine})
	}
	ctxs := contexts(cr)
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}
	now := s.Now().In(loc)
	groups := map[uuid.UUID]*Group{}
	var none Group
	group := func(id *uuid.UUID) *Group {
		if id == nil {
			return &none
		}
		g := groups[*id]
		if g == nil {
			g = &Group{ClusterID: id}
			groups[*id] = g
		}
		return g
	}
	for _, r := range reads {
		g := group(r.ClusterID)
		g.ClusterName, g.ClusterSlug, g.ClusterEnv = deref(r.ClusterName), deref(r.ClusterSlug), string(r.ClusterEnvironment.Environment)
		g.Nodes = append(g.Nodes, NodeRead{
			NodeID: r.NodeID, Hostname: r.Hostname, Daemon: r.Daemon, Logging: r.Logging, Journal: r.Journal, Truncated: r.Truncated,
			Error: r.Error, ErrorKind: r.ErrorKind, SyncedAt: r.SyncedAt, OKAt: r.OkAt,
		})
	}
	jobs := map[*Group]map[string]*Job{}
	for _, r := range rows {
		// De timers van beheerde jobs staan bij de beheerde jobs.
		if r.Clusterforge {
			continue
		}
		g := group(r.ClusterID)
		if jobs[g] == nil {
			jobs[g] = map[string]*Job{}
		}
		j := jobs[g][r.Key]
		if j == nil {
			j = &Job{
				Key: r.Key, Source: r.Source, File: r.File, Line: int(r.Line), User: r.UserName, Schedule: r.Schedule,
				Describe: Describe(r.Source, r.Schedule), Command: r.Command, Unit: r.Unit, Description: r.Description,
				Package: r.Package, ClusterForge: r.Clusterforge,
			}
			jobs[g][r.Key] = j
		}
		jn := JobNode{
			NodeID: r.NodeID, Hostname: r.Hostname, Enabled: r.Enabled, LastRun: r.LastRunAt, LastEnd: r.LastEndAt,
			LastResult: r.LastResult, Runs: int(r.Runs), Failures: int(r.Failures), NextRun: r.NextRunAt,
		}
		if r.LastExit != nil {
			x := int(*r.LastExit)
			jn.LastExit = &x
		}
		if jn.NextRun == nil && jn.Enabled && r.Source != protocol.CronSourceTimer {
			jn.NextRun = NextRun(r.Schedule, now)
		}
		j.Nodes = append(j.Nodes, jn)
		if jn.NextRun != nil && (j.NextRun == nil || jn.NextRun.Before(*j.NextRun)) {
			j.NextRun = jn.NextRun
		}
	}
	var out []Group
	all := []*Group{&none}
	for _, g := range groups {
		all = append(all, g)
	}
	for _, g := range all {
		if len(g.Nodes) == 0 {
			continue
		}
		for _, j := range jobs[g] {
			slices.SortFunc(j.Nodes, func(a, b JobNode) int {
				return strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname))
			})
			g.Jobs = append(g.Jobs, *j)
		}
		slices.SortFunc(g.Jobs, func(a, b Job) int {
			return cmp.Or(cmp.Compare(sourceOrder(a.Source), sourceOrder(b.Source)), strings.Compare(a.File, b.File),
				cmp.Compare(a.Line, b.Line), strings.Compare(a.Command, b.Command))
		})
		slices.SortFunc(g.Nodes, func(a, b NodeRead) int {
			return strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname))
		})
		cc := clusterContext{components: map[string]bool{}}
		if g.ClusterID != nil {
			if c, ok := ctxs[*g.ClusterID]; ok {
				cc = c
			}
		}
		analyze(g, cc)
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b Group) int {
		// Nodes zonder cluster achteraan.
		if (a.ClusterID == nil) != (b.ClusterID == nil) {
			if a.ClusterID == nil {
				return 1
			}
			return -1
		}
		return strings.Compare(strings.ToLower(a.ClusterName), strings.ToLower(b.ClusterName))
	})
	return out, nil
}

func sourceOrder(s string) int {
	switch s {
	case protocol.CronSourceUser:
		return 0
	case protocol.CronSourceCronD:
		return 1
	case protocol.CronSourceCrontab:
		return 2
	case protocol.CronSourceTimer:
		return 3
	}
	return 4
}

// Describe geeft het schema van een job in gewone taal, of leeg.
func Describe(source, schedule string) string {
	switch source {
	case protocol.CronSourceTimer:
		return describeTimer(schedule)
	case protocol.CronSourcePeriodic:
		switch schedule {
		case "hourly":
			return "elk uur (cron.hourly)"
		case "daily":
			return "elke dag (cron.daily)"
		case "weekly":
			return "elke week (cron.weekly)"
		case "monthly":
			return "elke maand (cron.monthly)"
		}
	}
	sc, err := cronexpr.Parse(schedule)
	if err != nil {
		return ""
	}
	return sc.Describe()
}

// describeTimer leest een paar vaste vormen van een timer, zoals
// "OnCalendar=daily" of "OnUnitActiveSec=6h".
func describeTimer(schedule string) string {
	var parts []string
	for p := range strings.SplitSeq(schedule, "; ") {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		switch k {
		case "OnCalendar":
			switch strings.TrimSpace(v) {
			case "minutely", "*-*-* *:*:00":
				parts = append(parts, "elke minuut")
			case "hourly", "*-*-* *:00:00":
				parts = append(parts, "elk uur")
			case "daily", "*-*-* 00:00:00":
				parts = append(parts, "elke dag om 00:00")
			case "weekly", "Mon *-*-* 00:00:00":
				parts = append(parts, "elke maandag om 00:00")
			case "monthly", "*-*-01 00:00:00":
				parts = append(parts, "elke maand op de 1e om 00:00")
			default:
				parts = append(parts, "volgens "+strings.TrimSpace(v))
			}
		case "OnBootSec", "OnStartupSec":
			parts = append(parts, v+" na het opstarten")
		case "OnUnitActiveSec", "OnUnitInactiveSec":
			parts = append(parts, "elke "+v)
		}
	}
	return strings.Join(parts, ", ")
}

// NextRun is de volgende run van een cronschema na now, of nil.
func NextRun(schedule string, now time.Time) *time.Time {
	sc, err := cronexpr.Parse(schedule)
	if err != nil {
		return nil
	}
	next := sc.Next(now)
	if next.IsZero() {
		return nil
	}
	return &next
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
