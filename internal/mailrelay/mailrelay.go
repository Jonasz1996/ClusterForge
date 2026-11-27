// Package mailrelay volgt de clusters met de template postfix-relay
// (testlijst 3, N30): de wachtrij en de test op open relay van elke node,
// en de knop "Testmail sturen".
package mailrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Template is de naam van de template.
const Template = "postfix-relay"

// KindTestMail is de taak die een testmail stuurt.
const KindTestMail = "mailrelay.testmail"

const ctl = "/usr/local/lib/clusterforge/postfix-ctl.sh"

var (
	ErrNotFound = errors.New("geen Postfix-relaycluster")
	mailRe      = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
)

// FieldError: een veld klopt niet.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// NodeStatus is wat één node zegt.
type NodeStatus struct {
	NodeID    uuid.UUID `json:"node_id"`
	Hostname  string    `json:"hostname"`
	ClusterID uuid.UUID `json:"cluster_id"`
	Cluster   string    `json:"cluster"`
	Online    bool      `json:"online"`
	Queue     int       `json:"queue"`
	Deferred  int       `json:"deferred"`
	Active    bool      `json:"active"`
	VIP       bool      `json:"vip"`
	// Relay is closed (goed), open of error (test niet gelukt).
	Relay     string `json:"relay"`
	QueueWarn int    `json:"queue_warn"`
	// Growing: de wachtrij groeide de laatste drie metingen.
	Growing bool      `json:"growing"`
	At      time.Time `json:"at"`
	Error   string    `json:"error,omitempty"`
}

// Notice is een melding voor "Wat moet ik doen".
type Notice struct {
	Level string
	Text  string
	Href  string
}

// Runner voert een script uit op een node.
type Runner interface {
	DoWith(ctx context.Context, log noderun.Log, ss noderun.StepState, nodeID uuid.UUID, spec protocol.RunSpec) (protocol.RunStatus, error)
}

// Service volgt de relays.
type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	Run  Runner
	// Every is hoe vaak de lus meet.
	Every time.Duration

	mu     sync.Mutex
	status map[uuid.UUID]NodeStatus
	hist   map[uuid.UUID][]int
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, run Runner) *Service {
	s := &Service{pool: pool, ev: ev, log: log, jobs: runner, Run: run, Every: 5 * time.Minute,
		status: map[uuid.UUID]NodeStatus{}, hist: map[uuid.UUID][]int{}}
	if runner != nil {
		runner.Register(KindTestMail, s.testMail)
	}
	return s
}

type node struct {
	id        uuid.UUID
	hostname  string
	clusterID uuid.UUID
	cluster   string
	online    bool
}

func (s *Service) nodes(ctx context.Context, cluster *uuid.UUID) ([]node, error) {
	rows, err := s.pool.Query(ctx, `
SELECT n.id, n.hostname, c.id, c.name,
       coalesce(st.heartbeat_at > now() - interval '2 minutes', false)
FROM clusters c
JOIN nodes n ON n.cluster_id = c.id
LEFT JOIN node_status st ON st.node_id = n.id
WHERE c.template_name = $1 AND ($2::uuid IS NULL OR c.id = $2)
ORDER BY lower(c.name), lower(n.hostname)`, Template, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []node
	for rows.Next() {
		var n node
		if err := rows.Scan(&n.id, &n.hostname, &n.clusterID, &n.cluster, &n.online); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type capture struct{ lines []string }

func (c *capture) Logf(format string, args ...any) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}
func (c *capture) Flush(context.Context) error { return nil }

type memState struct{ v []byte }

func (m *memState) State(v any) bool {
	if m.v == nil {
		return false
	}
	return json.Unmarshal(m.v, v) == nil
}

func (m *memState) SetState(_ context.Context, v any) error {
	b, err := json.Marshal(v)
	m.v = b
	return err
}

// Parse leest de regel CFPF {...} uit de uitvoer van status-json.
func Parse(lines []string) (NodeStatus, bool) {
	var st NodeStatus
	for _, l := range lines {
		i := strings.Index(l, "CFPF ")
		if i < 0 {
			continue
		}
		if json.Unmarshal([]byte(l[i+5:]), &st) == nil {
			return st, true
		}
	}
	return st, false
}

func (s *Service) measure(ctx context.Context, n node) NodeStatus {
	st := NodeStatus{NodeID: n.id, Hostname: n.hostname, ClusterID: n.clusterID, Cluster: n.cluster, Online: n.online, At: time.Now()}
	if !n.online || s.Run == nil {
		st.Error = "agent niet verbonden"
		return st
	}
	c := &capture{}
	spec := protocol.RunSpec{ID: "cf-postfix-" + uuid.NewString(), Kind: protocol.RunScript, Title: "postfix-status",
		Script: ctl + " status-json", TimeoutSeconds: 60}
	res, err := s.Run.DoWith(ctx, c, &memState{}, n.id, spec)
	switch {
	case err != nil:
		st.Error = err.Error()
		return st
	case res.State != protocol.RunOK:
		st.Error = "status-json: " + string(res.State)
		return st
	}
	got, ok := Parse(c.lines)
	if !ok {
		st.Error = "geen status van postfix-ctl.sh"
		return st
	}
	got.NodeID, got.Hostname, got.ClusterID, got.Cluster, got.Online, got.At = n.id, n.hostname, n.clusterID, n.cluster, true, st.At
	return got
}

// Refresh meet de nodes (van één cluster, of alle) opnieuw.
func (s *Service) Refresh(ctx context.Context, cluster *uuid.UUID) ([]NodeStatus, error) {
	ns, err := s.nodes(ctx, cluster)
	if err != nil {
		return nil, err
	}
	out := make([]NodeStatus, 0, len(ns))
	for _, n := range ns {
		st := s.measure(ctx, n)
		s.mu.Lock()
		if st.Error == "" {
			h := append(s.hist[n.id], st.Queue)
			if len(h) > 4 {
				h = h[len(h)-4:]
			}
			s.hist[n.id] = h
			st.Growing = Growing(h)
		}
		s.status[n.id] = st
		s.mu.Unlock()
		out = append(out, st)
	}
	return out, nil
}

// Growing: drie keer na elkaar groter.
func Growing(h []int) bool {
	if len(h) < 4 {
		return false
	}
	for i := len(h) - 3; i < len(h); i++ {
		if h[i] <= h[i-1] {
			return false
		}
	}
	return true
}

// Status geeft de laatste meting van een cluster; zonder meting meet het.
func (s *Service) Status(ctx context.Context, cluster uuid.UUID, fresh bool) ([]NodeStatus, error) {
	ns, err := s.nodes(ctx, &cluster)
	if err != nil {
		return nil, err
	}
	if len(ns) == 0 {
		return nil, ErrNotFound
	}
	if !fresh {
		s.mu.Lock()
		out := make([]NodeStatus, 0, len(ns))
		for _, n := range ns {
			if st, ok := s.status[n.id]; ok {
				out = append(out, st)
			}
		}
		s.mu.Unlock()
		if len(out) == len(ns) {
			return out, nil
		}
	}
	return s.Refresh(ctx, &cluster)
}

// Watch meet alle relays om de zoveel minuten.
func (s *Service) Watch(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 5 * time.Minute
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Refresh(ctx, nil); err != nil && ctx.Err() == nil {
			s.log.Warn("postfix-relays meten", "err", err)
		}
		t.Reset(every)
	}
}

// Notices zijn de meldingen voor "Wat moet ik doen".
func (s *Service) Notices() []Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Notice
	for _, st := range s.status {
		href := "/clusters/detail?id=" + st.ClusterID.String()
		switch {
		case st.Relay == "open":
			out = append(out, Notice{Level: "crit", Href: href,
				Text: fmt.Sprintf("Postfix-relay %s op %s is een open relay: kijk mynetworks na", st.Cluster, st.Hostname)})
		case st.Error != "":
			continue
		}
		if !st.Active {
			out = append(out, Notice{Level: "crit", Href: href, Text: fmt.Sprintf("Postfix draait niet op %s (%s)", st.Hostname, st.Cluster)})
		}
		warn := st.QueueWarn
		if warn <= 0 {
			warn = 20
		}
		switch {
		case st.Queue >= warn:
			out = append(out, Notice{Level: "warn", Href: href,
				Text: fmt.Sprintf("%d mails in de wachtrij op %s (%s), %d uitgesteld", st.Queue, st.Hostname, st.Cluster, st.Deferred)})
		case st.Growing:
			out = append(out, Notice{Level: "warn", Href: href,
				Text: fmt.Sprintf("De wachtrij op %s (%s) groeit: nu %d mails", st.Hostname, st.Cluster, st.Queue)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Text < out[j].Text })
	return out
}

type testParams struct {
	ClusterID uuid.UUID `json:"cluster_id"`
	To        string    `json:"to"`
}

// TestMail start de taak die een testmail via het VIP stuurt.
func (s *Service) TestMail(ctx context.Context, actor events.Actor, cluster uuid.UUID, to string) (store.Job, error) {
	to = strings.TrimSpace(to)
	if to != "" && !mailRe.MatchString(to) {
		return store.Job{}, &FieldError{"ongeldig mailadres"}
	}
	ns, err := s.nodes(ctx, &cluster)
	if err != nil {
		return store.Job{}, err
	}
	if len(ns) == 0 {
		return store.Job{}, ErrNotFound
	}
	title := "Testmail via " + ns[0].cluster
	if to != "" {
		title += " naar " + to
	}
	return s.jobs.Enqueue(ctx, jobs.Spec{Kind: KindTestMail, Title: title, ClusterID: &cluster, Actor: actor,
		Params: testParams{ClusterID: cluster, To: to}})
}

func (s *Service) testMail(ctx context.Context, j *jobs.Job) error {
	var p testParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	ns, err := s.nodes(ctx, &p.ClusterID)
	if err != nil {
		return err
	}
	return j.Step(ctx, "testmail", func(ctx context.Context, step *jobs.Step) error {
		sent := false
		for _, n := range ns {
			if !n.online {
				step.Logf("· %s: agent niet verbonden, overgeslagen", n.hostname)
				continue
			}
			step.Logf("· %s", n.hostname)
			c := &capture{}
			spec := protocol.RunSpec{ID: "cf-testmail-" + uuid.NewString(), Kind: protocol.RunScript, Title: "testmail",
				Script: ctl + " testmail-if-vip " + shellQuote(p.To), TimeoutSeconds: 120}
			res, err := s.Run.DoWith(ctx, c, &memState{}, n.id, spec)
			for _, l := range c.lines {
				step.Logf("  %s", l)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", n.hostname, err)
			}
			if res.State != protocol.RunOK {
				return fmt.Errorf("%s: de testmail is niet afgeleverd (zie de log)", n.hostname)
			}
			if !strings.Contains(strings.Join(c.lines, "\n"), "heeft het VIP niet") {
				sent = true
				break
			}
		}
		if !sent {
			return errors.New("geen enkele node heeft het VIP: de testmail is niet verstuurd")
		}
		return nil
	})
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
