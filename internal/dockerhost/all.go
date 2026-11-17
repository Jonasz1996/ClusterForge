package dockerhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// allPaths zijn de lijsten die over alle nodes tegelijk gelezen mogen
// worden.
var allPaths = map[string]bool{
	"/containers/json": true, "/images/json": true, "/volumes": true, "/networks": true, "/system/df": true, "/info": true,
}

// Result is het antwoord van één node bij All.
type Result struct {
	NodeID   uuid.UUID       `json:"node_id"`
	Hostname string          `json:"hostname"`
	Data     json.RawMessage `json:"data,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// All leest een lijst op elke node met Docker (of alleen op nodes), acht
// tegelijk. Een node die niet antwoordt, geeft een fout in zijn Result; de
// rest komt gewoon.
func (s *Service) All(ctx context.Context, path string, query map[string]string, nodes []uuid.UUID) ([]Result, error) {
	if !allPaths[path] {
		return nil, ValidationError{fmt.Sprintf("%s kan niet over alle nodes", path)}
	}
	req := protocol.DockerRequest{Op: protocol.DockerOpGet, Path: path, Query: query}
	if err := req.Validate(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	hosts, err := s.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	want := map[uuid.UUID]bool{}
	for _, n := range nodes {
		want[n] = true
	}
	out := []Result{}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 8)
	)
	for _, h := range hosts {
		if len(want) > 0 && !want[h.NodeID] {
			continue
		}
		i := len(out)
		out = append(out, Result{NodeID: h.NodeID, Hostname: h.Hostname})
		if why := h.Ready(); why != "" {
			out[i].Error = why
			continue
		}
		wg.Add(1)
		go func(i int, h Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := s.get(ctx, h.NodeID, path, query)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			out[i].Data = data
		}(i, h)
	}
	wg.Wait()
	return out, nil
}

// guardStacks houdt stoppen, pauzeren en verwijderen tegen van een
// container die bij een stack van ClusterForge hoort: die gaat via de
// stack, anders zet ClusterForge hem bij de volgende run gewoon terug.
// Herstarten mag wel.
func (s *Service) guardStacks(ctx context.Context, h Host, calls []protocol.DockerCall) error {
	if len(h.Stacks) == 0 {
		return nil
	}
	for _, c := range calls {
		if !strings.HasPrefix(c.Path, "/containers/") || strings.HasSuffix(c.Path, "/prune") {
			continue
		}
		rest := strings.TrimPrefix(c.Path, "/containers/")
		name, verb, _ := strings.Cut(rest, "/")
		if c.Method == "POST" && verb != "stop" && verb != "kill" && verb != "pause" && verb != "rename" {
			continue
		}
		data, err := s.get(ctx, h.NodeID, "/containers/"+name+"/json", nil)
		if err != nil {
			continue
		}
		var info struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
		}
		if json.Unmarshal(data, &info) != nil {
			continue
		}
		project := info.Config.Labels["com.docker.compose.project"]
		for _, st := range h.Stacks {
			if st.Name == project {
				return ValidationError{fmt.Sprintf("%s hoort bij stack %s die ClusterForge beheert; stop, pauzeer of verwijder hem via Docker → Stacks, anders zet de stack hem terug", name, project)}
			}
		}
	}
	return nil
}
