package aiverslag

// M4: AI-verslag krijgt context uit ClusterForge. De extensie ext56 (in
// ext/ext56.py, ook op een eigen tak van de aiverslag-repo) zet de servers,
// VIP's, onderhoud en recente wijzigingen om in regels van de kennisbank
// van AI-verslag. ClusterForge installeert ext56 zelf (run aiv_install, met
// terugzetten als de webdienst daarna niet start) en stuurt de context elke
// ronde (aiv_post).

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

//go:embed ext/ext56.py
var ext56 string

// ExtName is de extensie in AI-verslag.
const ExtName = "ext56"

// InstallKind is de soort taak die ext56 installeert.
const InstallKind = "aiverslag.install"

var extVersionRe = regexp.MustCompile(`(?m)^VERSIE = "([^"]+)"`)

// ExtVersion is de versie van ext56 die in ClusterForge zit.
func ExtVersion() string {
	if m := extVersionRe.FindStringSubmatch(ext56); m != nil {
		return m[1]
	}
	return ""
}

func extSpec() protocol.AIVExtension {
	sum := sha256.Sum256([]byte(ext56))
	return protocol.AIVExtension{Name: ExtName, Code: ext56, SHA256: hex.EncodeToString(sum[:])}
}

// ExtStatus is de stand van de koppeling in AI-verslag.
type ExtStatus struct {
	// Installed: AI-verslag kent /api/inzicht/clusterforge.
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Bundled is de versie die ClusterForge meebrengt; Update als die
	// anders is dan de geïnstalleerde.
	Bundled string `json:"bundled"`
	Update  bool   `json:"update"`
	// Rules is het aantal regels cf-* in de kennisbank.
	Rules int `json:"rules"`
	// Stand is wat ext56 na de laatste context bewaarde.
	Stand json.RawMessage `json:"stand,omitempty"`
	// PushedAt en PushError gaan over de laatste ronde van ClusterForge.
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
	PushError string     `json:"push_error,omitempty"`
}

func parseExt(data json.RawMessage) ExtStatus {
	st := ExtStatus{Bundled: ExtVersion()}
	var raw struct {
		Ext    string          `json:"ext"`
		Regels int             `json:"regels"`
		Stand  json.RawMessage `json:"stand"`
	}
	if json.Unmarshal(data, &raw) == nil && raw.Ext != "" {
		st.Installed, st.Version, st.Rules, st.Stand = true, raw.Ext, raw.Regels, raw.Stand
		st.Update = raw.Ext != st.Bundled
	}
	return st
}

// Context is wat ClusterForge naar ext56 stuurt.
type Context struct {
	Bron        string         `json:"bron"`
	Versie      string         `json:"versie"`
	TS          int64          `json:"ts"`
	Nodes       []CtxNode      `json:"nodes"`
	VIPs        []CtxVIP       `json:"vips"`
	Onderhoud   []CtxOnderhoud `json:"onderhoud"`
	Wijzigingen []CtxWijziging `json:"wijzigingen"`
}

type CtxNode struct {
	Naam     string   `json:"naam"`
	IPs      []string `json:"ips"`
	Cluster  string   `json:"cluster,omitempty"`
	Omgeving string   `json:"omgeving,omitempty"`
	Rol      string   `json:"rol,omitempty"`
}

type CtxVIP struct {
	IP       string `json:"ip"`
	Cluster  string `json:"cluster"`
	Omgeving string `json:"omgeving,omitempty"`
	Eigenaar string `json:"eigenaar,omitempty"`
}

type CtxOnderhoud struct {
	Naam     string   `json:"naam"`
	Nodes    []string `json:"nodes,omitempty"`
	Clusters []string `json:"clusters,omitempty"`
	Van      int64    `json:"van"`
	Tot      int64    `json:"tot"`
	Reden    string   `json:"reden"`
}

type CtxWijziging struct {
	TS      int64  `json:"ts"`
	Node    string `json:"node"`
	Cluster string `json:"cluster,omitempty"`
	Tekst   string `json:"tekst"`
}

var envLabel = map[string]string{"prod": "productie", "test": "test", "lab": "lab"}

// BuildContext maakt de context uit de inventaris en het journaal.
func (s *Service) BuildContext(ctx context.Context, version string, now time.Time) (Context, error) {
	c := Context{Bron: "ClusterForge", Versie: version, TS: now.Unix(), Nodes: []CtxNode{}, VIPs: []CtxVIP{}, Onderhoud: []CtxOnderhoud{}, Wijzigingen: []CtxWijziging{}}
	nodes, err := s.q.AIVContextNodes(ctx)
	if err != nil {
		return c, err
	}
	for _, n := range nodes {
		cn := CtxNode{Naam: n.Hostname, IPs: []string{}, Cluster: n.ClusterName, Omgeving: envLabel[n.Environment], Rol: n.Role}
		if n.Address != "" {
			cn.IPs = append(cn.IPs, n.Address)
		}
		c.Nodes = append(c.Nodes, cn)
		if n.Lifecycle == string(store.NodeLifecycleMaintenance) || n.Lifecycle == string(store.NodeLifecycleDraining) {
			reden := "onderhoudsmodus in ClusterForge"
			if n.Lifecycle == string(store.NodeLifecycleDraining) {
				reden = "node wordt leeggemaakt (drain) in ClusterForge"
			}
			// Elke ronde opnieuw: zolang de node in onderhoud staat, loopt het venster.
			c.Onderhoud = append(c.Onderhoud, CtxOnderhoud{Naam: n.Hostname, Nodes: []string{n.Hostname}, Van: now.Add(-time.Minute).Unix(), Tot: now.Add(30 * time.Minute).Unix(), Reden: reden})
		}
	}
	vips, err := s.q.AIVContextVIPs(ctx)
	if err != nil {
		return c, err
	}
	for _, v := range vips {
		c.VIPs = append(c.VIPs, CtxVIP{IP: v.Address, Cluster: v.ClusterName, Omgeving: envLabel[v.Environment], Eigenaar: v.Owner})
	}
	changes, err := s.q.AIVContextChanges(ctx)
	if err != nil {
		return c, err
	}
	for _, ch := range changes {
		t := strings.TrimSpace(ch.Summary)
		if ch.Source == "clusterforge" {
			t += " (via ClusterForge)"
		}
		c.Wijzigingen = append(c.Wijzigingen, CtxWijziging{TS: ch.Ts.Unix(), Node: ch.Hostname, Cluster: ch.ClusterName, Tekst: t})
	}
	return c, nil
}

// ExtStatus leest de stand van ext56 in AI-verslag.
func (s *Service) ExtStatus(ctx context.Context, id uuid.UUID) (ExtStatus, error) {
	data, err := s.get(ctx, id, "/api/inzicht/clusterforge", nil)
	if err != nil {
		if strings.Contains(err.Error(), "(404)") {
			return ExtStatus{Bundled: ExtVersion()}, nil
		}
		return ExtStatus{Bundled: ExtVersion()}, err
	}
	return parseExt(data), nil
}

// PushContext stuurt de context naar ext56 op de machine van id.
func (s *Service) PushContext(ctx context.Context, id uuid.UUID, c Context) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	req := protocol.PVERequest{Op: protocol.AIVOpPost, Path: "/api/inzicht/clusterforge/context", Args: body}
	if err := req.ValidateAIVPost(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, id, req)
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

type installParams struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
}

// StartInstall zet ext56 installeren (of bijwerken) als taak in de wachtrij.
func (s *Service) StartInstall(ctx context.Context, actor events.Actor, id uuid.UUID) (store.Job, error) {
	v, err := s.Instance(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if v.Why != "" {
		return store.Job{}, ValidationError{v.Why}
	}
	nodeID := id
	title := fmt.Sprintf("Koppeling ClusterForge (%s %s) in AI-verslag installeren", ExtName, ExtVersion())
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{Kind: InstallKind, Title: title, Actor: actor, NodeID: &nodeID, Params: installParams{NodeID: id, Hostname: v.Hostname}})
	if err != nil {
		return store.Job{}, err
	}
	if s.ev != nil {
		if err := s.ev.Write(ctx, nil, events.Event{
			Actor: actor, SubjectType: "node", SubjectID: id.String(), Action: "aiverslag.ext_install_requested",
			Payload: map[string]any{"host": v.Hostname, "ext": ExtName, "version": ExtVersion(), "job_id": j.ID.String()},
		}); err != nil {
			s.log.Warn("event schrijven mislukt", "err", err)
		}
	}
	s.jobs.Kick()
	return j, nil
}

func (s *Service) runInstall(ctx context.Context, j *jobs.Job) error {
	var p installParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.cache.clear()
	if err := j.Step(ctx, "Extensie installeren en de webdienst herstarten", func(ctx context.Context, st *jobs.Step) error {
		ext := extSpec()
		op := protocol.PVERun{Action: protocol.AIVInstall, AIVExt: &ext}
		spec := protocol.RunSpec{ID: uuid.NewString(), Kind: protocol.RunPVE, Title: j.Title, PVE: &op}
		if err := spec.Validate(); err != nil {
			return err
		}
		res, err := s.Run.Do(ctx, st, p.NodeID, spec)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	}); err != nil {
		return err
	}
	return j.Step(ctx, "Eerste context sturen", func(ctx context.Context, st *jobs.Step) error {
		c, err := s.BuildContext(ctx, s.Version, time.Now())
		if err != nil {
			return err
		}
		if err := s.PushContext(ctx, p.NodeID, c); err != nil {
			return err
		}
		st.Logf("%d servers, %d VIP's, %d in onderhoud, %d wijzigingen naar AI-verslag gestuurd.", len(c.Nodes), len(c.VIPs), len(c.Onderhoud), len(c.Wijzigingen))
		s.setPushed(p.NodeID, nil)
		return nil
	})
}
