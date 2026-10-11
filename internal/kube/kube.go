// Package kube praat met de Kubernetes-API van een cluster via de kube-brug
// van een agent: ClusterForge maakt zelf nooit een verbinding naar een
// node. Hij kiest een server-node met een verbonden agent, vraagt op, en
// vat de clusters elke 30 seconden samen in kube_clusters en kube_nodes.
package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Requester stuurt een kube-verzoek naar de agent van een node; de bus doet
// dat over NATS.
type Requester interface {
	Kube(ctx context.Context, nodeID uuid.UUID, req protocol.KubeRequest) (protocol.KubeResponse, error)
}

var (
	// ErrNotKubernetes: het cluster bestaat niet of is geen
	// Kubernetes-cluster.
	ErrNotKubernetes = errors.New("dit is geen Kubernetes-cluster")
	// ErrNotFound: de API kent het object niet.
	ErrNotFound = errors.New("niet gevonden in Kubernetes")
	// ErrInvalid: een ongeldige naam van een namespace, node of object.
	ErrInvalid = errors.New("ongeldige naam")
)

// UnavailableError zegt waarom geen agent de API kon bereiken.
type UnavailableError struct {
	// TooOld is true als er alleen agents zijn met een protocol van voor
	// de kube-brug.
	TooOld bool
	Msg    string
}

func (e *UnavailableError) Error() string { return e.Msg }

// APIError is een fout van de Kubernetes-API zelf, zoals Forbidden.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return "Kubernetes: " + e.Message
	}
	return fmt.Sprintf("Kubernetes antwoordde met HTTP %d", e.Status)
}

// cacheTTL is hoe lang een antwoord geldt: tien open tabbladen vragen zo
// niet tien keer hetzelfde.
const cacheTTL = 2 * time.Second

// Service praat met de Kubernetes-clusters.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	bus  Requester
	log  *slog.Logger
	// Timeout per verzoek aan de agent.
	Timeout time.Duration
	// Interval van de sync; nul zet hem uit.
	Interval time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]cached
	// last onthoudt per cluster de node die het laatst antwoordde.
	last map[uuid.UUID]uuid.UUID
}

type cached struct {
	at   time.Time
	body []byte
	via  uuid.UUID
}

func NewService(pool *pgxpool.Pool, bus Requester, log *slog.Logger) *Service {
	return &Service{
		pool: pool, q: store.New(pool), bus: bus, log: log, Timeout: 30 * time.Second, Interval: 30 * time.Second, Now: time.Now,
		cache: map[string]cached{}, last: map[uuid.UUID]uuid.UUID{},
	}
}

// bridgeNode is een node die een verzoek kan doorsturen.
type bridgeNode struct {
	ID       uuid.UUID
	Hostname string
	Protocol int
	Fresh    bool
	Kube     *protocol.KubeFacts
}

// candidates zijn de server-nodes met een agent op protocol 8 of hoger, de
// beste eerst: een API die antwoordt en een verse heartbeat.
func (s *Service) candidates(ctx context.Context, clusterID uuid.UUID) ([]bridgeNode, error) {
	rows, err := s.q.ListKubeBridgeNodes(ctx, &clusterID)
	if err != nil {
		return nil, err
	}
	var out, old []bridgeNode
	for _, r := range rows {
		n := bridgeNode{ID: r.ID, Hostname: r.Hostname, Protocol: int(r.AgentProtocol)}
		n.Fresh = r.HeartbeatAt != nil && s.Now().Sub(*r.HeartbeatAt) <= status.HeartbeatLate
		if len(r.Kube) > 0 && string(r.Kube) != "null" {
			var f protocol.KubeFacts
			if json.Unmarshal(r.Kube, &f) == nil {
				n.Kube = &f
			}
		}
		switch {
		case n.Protocol < protocol.KubeSince:
			old = append(old, n)
		case n.Kube != nil && n.Kube.Role == "server":
			out = append(out, n)
		}
	}
	s.mu.Lock()
	last := s.last[clusterID]
	s.mu.Unlock()
	score := func(n bridgeNode) int {
		v := 0
		if n.Fresh {
			v += 4
		}
		if n.Kube.APIReady {
			v += 2
		}
		if n.ID == last {
			v++
		}
		return v
	}
	slices.SortStableFunc(out, func(a, b bridgeNode) int { return cmp.Compare(score(b), score(a)) })
	if len(out) == 0 {
		if len(old) > 0 {
			names := make([]string, 0, len(old))
			for _, n := range old {
				names = append(names, n.Hostname)
			}
			return nil, &UnavailableError{TooOld: true, Msg: fmt.Sprintf(
				"de agent op %s is te oud voor Kubernetes (protocol %d, nodig is %d); werk hem bij met het installatiescript en --upgrade",
				strings.Join(names, ", "), old[0].Protocol, protocol.KubeSince)}
		}
		return nil, &UnavailableError{Msg: "geen server-node van dit cluster heeft een agent met k3s; zet cf-agent op een k3s-server"}
	}
	return out, nil
}

// Get doet een GET op de API van een cluster en geeft de JSON. Een
// antwoord van minder dan twee seconden oud komt uit de cache.
func (s *Service) Get(ctx context.Context, clusterID uuid.UUID, path string, query url.Values) ([]byte, error) {
	body, _, err := s.get(ctx, clusterID, path, query)
	return body, err
}

func (s *Service) get(ctx context.Context, clusterID uuid.UUID, path string, query url.Values) ([]byte, uuid.UUID, error) {
	key := clusterID.String() + " " + path + "?" + query.Encode()
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && s.Now().Sub(c.at) < cacheTTL {
		s.mu.Unlock()
		return c.body, c.via, nil
	}
	s.mu.Unlock()
	nodes, err := s.candidates(ctx, clusterID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	q := map[string]string{}
	for k, v := range query {
		if len(v) > 0 {
			q[k] = v[0]
		}
	}
	var last error
	for _, n := range nodes {
		rctx, cancel := context.WithTimeout(ctx, s.Timeout)
		res, err := s.bus.Kube(rctx, n.ID, protocol.KubeRequest{ID: uuid.NewString(), Op: protocol.KubeRead, Path: path, Query: q})
		cancel()
		switch {
		case errors.Is(err, agentbus.ErrAgentOffline):
			last = &UnavailableError{Msg: "de agent op " + n.Hostname + " is niet verbonden"}
			continue
		case errors.Is(err, agentbus.ErrNoAnswer):
			last = &UnavailableError{Msg: "de agent op " + n.Hostname + " antwoordde niet op tijd"}
			continue
		case err != nil:
			return nil, uuid.Nil, err
		case res.Status == 0:
			// De agent stuurde het verzoek niet door: geen kubeconfig, of
			// de API op die node antwoordt niet. Een andere server kan
			// het wel.
			last = &UnavailableError{Msg: n.Hostname + ": " + res.Error}
			continue
		}
		s.mu.Lock()
		s.last[clusterID] = n.ID
		s.mu.Unlock()
		if res.Status != http.StatusOK {
			return nil, n.ID, apiError(res)
		}
		if res.Error != "" {
			return nil, n.ID, &APIError{Status: res.Status, Message: res.Error}
		}
		s.mu.Lock()
		s.cache[key] = cached{at: s.Now(), body: res.Body, via: n.ID}
		if len(s.cache) > 512 {
			for k, c := range s.cache {
				if s.Now().Sub(c.at) >= cacheTTL {
					delete(s.cache, k)
				}
			}
		}
		s.mu.Unlock()
		return res.Body, n.ID, nil
	}
	return nil, uuid.Nil, last
}

func apiError(res protocol.KubeResponse) error {
	var st struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(res.Body, &st)
	if res.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, st.Message)
	}
	if st.Message == "" {
		st.Message = res.Error
	}
	return &APIError{Status: res.Status, Reason: st.Reason, Message: st.Message}
}

// maxListPages begrenst een lijst: 20 pagina's van 250 objecten.
const maxListPages = 20

// List haalt alle objecten van een lijst op, pagina voor pagina, en
// decodeert ze in T.
func List[T any](ctx context.Context, s *Service, clusterID uuid.UUID, path string, query url.Values) ([]T, error) {
	var out []T
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	for range maxListPages {
		body, err := s.Get(ctx, clusterID, path, q)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items    []T `json:"items"`
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, page.Items...)
		if page.Metadata.Continue == "" {
			return out, nil
		}
		q.Set("continue", page.Metadata.Continue)
	}
	return out, &APIError{Message: fmt.Sprintf("%s heeft meer dan %d objecten; filter op namespace", path, maxListPages*protocol.KubePageSize)}
}

// GetInto haalt één object op en decodeert het.
func GetInto[T any](ctx context.Context, s *Service, clusterID uuid.UUID, path string) (T, error) {
	var v T
	body, err := s.Get(ctx, clusterID, path, nil)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(body, &v)
}

func parseVersion(body []byte) (string, error) {
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("/version: %w", err)
	}
	return v.GitVersion, nil
}

// Cluster controleert dat een cluster een Kubernetes-cluster is.
func (s *Service) Cluster(ctx context.Context, clusterID uuid.UUID) (store.Cluster, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil || c.Type != "kubernetes" {
		return c, ErrNotKubernetes
	}
	return c, nil
}
