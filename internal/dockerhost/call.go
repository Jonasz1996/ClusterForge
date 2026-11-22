package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Call is een wijziging vanuit de GUI: een of meer aanroepen op één node.
type Call struct {
	Calls []protocol.DockerCall `json:"calls"`
	// Title is de titel van de taak; leeg maakt de server er een.
	Title string `json:"title"`
	// Confirm is wat overgetikt werd bij een gevaarlijke aanroep.
	Confirm string `json:"confirm"`
}

// params zijn de params van een taak docker.action.
type params struct {
	NodeID   uuid.UUID             `json:"node_id"`
	Hostname string                `json:"hostname"`
	Calls    []protocol.DockerCall `json:"calls"`
}

// Danger zegt wat overgetikt moet worden voor deze aanroepen; leeg als
// niets gevaarlijk is. Verwijderen vraagt de naam van het ding (bij meer
// dingen tegelijk het woord verwijderen), opruimen het woord opruimen.
func Danger(calls []protocol.DockerCall) string {
	var names []string
	prune := false
	for _, c := range calls {
		switch {
		case strings.HasSuffix(c.Path, "/prune"):
			prune = true
		case c.Method == "DELETE":
			names = append(names, objectName(c.Path))
		case strings.HasSuffix(c.Path, "/kill"):
			names = append(names, objectName(strings.TrimSuffix(c.Path, "/kill")))
		}
	}
	switch {
	case prune:
		return "opruimen"
	case len(names) == 1:
		return names[0]
	case len(names) > 1:
		return "verwijderen"
	}
	return ""
}

// objectName is het laatste deel van een pad zoals /containers/web of
// /images/ghcr.io/x/y:1.
func objectName(p string) string {
	for _, prefix := range []string{"/containers/", "/images/", "/volumes/", "/networks/"} {
		if strings.HasPrefix(p, prefix) {
			n := strings.TrimPrefix(p, prefix)
			if u, err := url.PathUnescape(n); err == nil {
				return u
			}
			return n
		}
	}
	return p
}

// describe maakt een titel voor de taak.
func describe(c protocol.DockerCall) string {
	if c.Title != "" {
		return c.Title
	}
	verbs := map[string]string{
		"start": "starten", "stop": "stoppen", "restart": "herstarten", "kill": "hard stoppen", "pause": "pauzeren",
		"unpause": "hervatten", "rename": "hernoemen", "tag": "taggen", "connect": "verbinden", "disconnect": "loskoppelen",
	}
	switch {
	case c.Path == "/images/create":
		ref := c.Query["fromImage"]
		if t := c.Query["tag"]; t != "" {
			ref += ":" + t
		}
		return ref + " halen"
	case strings.HasSuffix(c.Path, "/prune"):
		what := strings.TrimSuffix(strings.TrimPrefix(c.Path, "/"), "/prune")
		nl := map[string]string{"containers": "gestopte containers", "images": "ongebruikte images", "volumes": "ongebruikte volumes", "networks": "ongebruikte netwerken", "build": "buildcache"}
		return nl[what] + " opruimen"
	case strings.HasSuffix(c.Path, "/create"):
		kind := strings.TrimSuffix(strings.TrimPrefix(c.Path, "/"), "/create")
		return map[string]string{"volumes": "volume", "networks": "netwerk"}[kind] + " maken"
	case c.Method == "DELETE":
		return objectName(c.Path) + " verwijderen"
	}
	i := strings.LastIndex(c.Path, "/")
	if v, ok := verbs[c.Path[i+1:]]; ok {
		return objectName(c.Path[:i]) + " " + v
	}
	return c.Method + " " + c.Path
}

// Title is de titel van een taak met deze aanroepen.
func Title(calls []protocol.DockerCall, host string) string {
	if len(calls) == 1 {
		return capitalize(describe(calls[0])) + " op " + host
	}
	return fmt.Sprintf("%d Docker-acties op %s", len(calls), host)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// StartCall zet een wijziging als taak in de wachtrij.
func (s *Service) StartCall(ctx context.Context, actor events.Actor, nodeID uuid.UUID, c Call) (store.Job, error) {
	if len(c.Calls) == 0 {
		return store.Job{}, ValidationError{"geen actie"}
	}
	run := protocol.DockerAPIRun{Calls: c.Calls}
	if err := run.Validate(); err != nil {
		return store.Job{}, ValidationError{err.Error()}
	}
	for i := range c.Calls {
		c.Calls[i].Title = describe(c.Calls[i])
	}
	if want := Danger(c.Calls); want != "" && strings.TrimSpace(c.Confirm) != want {
		return store.Job{}, ValidationError{fmt.Sprintf("tik %s over om te bevestigen", want)}
	}
	h, err := s.ready(ctx, nodeID)
	if err != nil {
		return store.Job{}, err
	}
	if err := s.guardStacks(ctx, h, c.Calls); err != nil {
		return store.Job{}, err
	}
	title := strings.TrimSpace(c.Title)
	if title == "" {
		title = Title(c.Calls, h.Hostname)
	}
	id := h.NodeID
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{
		Kind: Kind, Title: title, Actor: actor, NodeID: &id, ClusterID: h.ClusterID,
		Params: params{NodeID: h.NodeID, Hostname: h.Hostname, Calls: c.Calls},
	})
	if err != nil {
		return store.Job{}, err
	}
	paths := make([]string, len(c.Calls))
	for i, call := range c.Calls {
		paths[i] = call.Method + " " + call.Path
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: h.NodeID.String(), Action: "docker.action_requested",
		Payload: map[string]any{"host": h.Hostname, "title": title, "calls": paths, "job_id": j.ID.String()},
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
	return j, nil
}

func (s *Service) run(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	return j.Step(ctx, j.Title, func(ctx context.Context, st *jobs.Step) error {
		spec := protocol.RunSpec{ID: uuid.NewString(), Kind: protocol.RunDockerAPI, Title: j.Title, DockerAPI: &protocol.DockerAPIRun{Calls: p.Calls}}
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
	})
}
