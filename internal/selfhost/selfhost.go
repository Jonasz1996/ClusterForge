// Package selfhost laat ClusterForge zichzelf beheren (fase 45): bijwerken
// vanuit de webinterface en verhuizen naar een eigen HA-cluster (template
// clusterforge).
//
// Bijwerken loopt als taak clusterforge.update. Op één server (docker
// compose) voert de agent op de host scripts/compose-update.sh uit: image
// halen, back-up, compose-bestand, CF_IMAGE, up, gezondheid, en terugdraaien
// als de nieuwe versie niet gezond wordt. De server herstart halverwege; de
// run op de node loopt door en de taak leest na de herstart verder. In het
// cluster (CF_SELF_CLUSTER) halen eerst de nodes zonder VIP het image, dan
// werkt de VIP-node bij met clusterforge-ctl.sh bijwerken (zelfde stappen,
// pg_dump op Patroni) en daarna krijgen de andere nodes het nieuwe image.
//
// Verhuizen loopt als taak clusterforge.move op de oude server: een dump in
// de exportmap op de host, clusterforge-ctl.sh overnemen op de VIP-node van
// het cluster (die haalt hoofdsleutel, instellingen, dump en metrics met een
// eenmalige sleutel bij /selfexport), ontvang op de andere nodes, de agents
// omzetten naar het VIP en tot slot de oude server stoppen.
package selfhost

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentconf"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Soorten taken.
const (
	KindUpdate = "clusterforge.update"
	KindMove   = "clusterforge.move"
)

// TemplateName is de template van het eigen cluster.
const TemplateName = "clusterforge"

// Ctl is het script van de template op elke node.
const Ctl = "/usr/local/lib/clusterforge/clusterforge-ctl.sh"

// Modes.
const (
	ModeCompose = "compose"
	ModeCluster = "cluster"
)

// DefaultRepo is waar CI de images zet.
const DefaultRepo = "ghcr.io/jonasz1996/clusterforge-server"

// ExportDir is de map met de dump in de servercontainer (alleen lezen).
const ExportDir = "/var/lib/clusterforge/export"

//go:embed scripts/compose-update.sh
var composeUpdate string

//go:embed scripts/compose-export.sh
var composeExport string

//go:embed scripts/compose-stop.sh
var composeStop string

// CarriedSettings zijn de instellingen van de server die meeverhuizen naar
// het cluster; database, adressen en VictoriaMetrics zet de template.
var CarriedSettings = []string{
	"CF_SECURE_COOKIES", "CF_TRUST_PROXY_HEADERS", "CF_SESSION_TTL", "CF_SESSION_MAX_AGE",
	"CF_GRAFANA_NODE_URL", "CF_GRAFANA_CLUSTER_URL", "CF_DRIFT_INTERVAL", "CF_TEST_WINDOW",
	"CF_LOG_LEVEL", "CF_SANDBOX_STORAGE", "CF_SANDBOX_BOOT_TIMEOUT", "CF_JOURNAL_SSH_LOGINS",
	"CF_DOCS_USERNAMES",
}

// Commander stuurt een commando naar een agent.
type Commander interface {
	Command(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command) (protocol.Result, error)
}

// WebChecker laat een agent een adres opvragen (testlijst 3, N12).
type WebChecker interface {
	Web(ctx context.Context, nodeID uuid.UUID, req protocol.WebRequest) (protocol.WebResponse, error)
}

// Service doet het bijwerken en verhuizen.
type Service struct {
	// Web: nil slaat de poortcontrole per agent over.
	Web WebChecker
	pool   *pgxpool.Pool
	q      *store.Queries
	ev     *events.Writer
	log    *slog.Logger
	runner *jobs.Runner
	bus    Commander
	Run    *noderun.Runner
	box    *secrets.Box
	conf   *agentconf.Service

	// Version is de versie van deze server, Image het image uit CF_IMAGE.
	Version string
	Image   string
	// Cluster is CF_SELF_CLUSTER: de slug van het eigen cluster; leeg op
	// één server.
	Cluster string
	// Advertise is CF_NATS_ADVERTISE (om de host te herkennen).
	Advertise string
	// MasterKey gaat bij een verhuizing mee naar het cluster.
	MasterKey []byte
	// Settings zijn de meeverhuizende instellingen (CarriedSettings).
	Settings map[string]string
	// ExportDir is de exportmap; VMURL VictoriaMetrics van deze server.
	ExportDir string
	VMURL     string
	// Wait is hoe lang een nieuwe versie mag doen over gezond worden.
	Wait time.Duration
	// MoveWait is hoe lang de verhuizing wacht of agents terugkeren.
	MoveWait time.Duration
	HTTP     *http.Client
	Now      func() time.Time
}

// NewService registreert de taken.
func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Commander, run noderun.Bus, box *secrets.Box, conf *agentconf.Service) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, runner: runner, bus: bus, box: box, conf: conf,
		Run:       &noderun.Runner{Bus: run},
		ExportDir: ExportDir, Wait: 7 * time.Minute, MoveWait: protocol.ReconfigureRollbackAfter + 90*time.Second,
		HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now,
		Settings: map[string]string{},
	}
	if runner != nil {
		runner.Register(KindUpdate, s.update)
		runner.Register(KindMove, s.move)
	}
	return s
}

// LoadSettings leest de meeverhuizende instellingen uit de omgeving.
func (s *Service) LoadSettings() {
	for _, k := range CarriedSettings {
		if v := os.Getenv(k); v != "" {
			s.Settings[k] = v
		}
	}
}

// Mode zegt hoe deze server draait.
func (s *Service) Mode() string {
	if s.Cluster != "" {
		return ModeCluster
	}
	return ModeCompose
}

// CurrentImage is het image van deze server; zonder CF_IMAGE de tag van CI
// met de versie, als die een commit is.
func (s *Service) CurrentImage() string {
	if s.Image != "" {
		return s.Image
	}
	if commitRe.MatchString(s.Version) {
		return DefaultRepo + ":" + s.Version
	}
	return ""
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Repo is het image zonder tag.
func (s *Service) Repo() string {
	img := s.CurrentImage()
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		return img[:i]
	}
	if img != "" {
		return img
	}
	return DefaultRepo
}

var imageRe = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// NormalizeImage maakt van een tag (zoals b9116ae) een volledig image met
// de repository van deze server; een volledig image blijft zoals het is.
func (s *Service) NormalizeImage(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", errors.New("geef een tag of een image")
	}
	if !strings.Contains(in, "/") {
		in = s.Repo() + ":" + strings.TrimPrefix(in, ":")
	}
	if !imageRe.MatchString(in) {
		return "", fmt.Errorf("%q is geen geldig image met tag (zoals %s:b9116ae)", in, s.Repo())
	}
	if strings.HasSuffix(in, "-scanner") {
		return "", errors.New("kies het image van de server, niet dat van de scanner")
	}
	return in, nil
}

// NodeRef is een node met een agent.
type NodeRef struct {
	ID       uuid.UUID `json:"id"`
	Hostname string    `json:"hostname"`
	Address  string    `json:"address,omitempty"`
	Online   bool      `json:"online"`
	Protocol int       `json:"protocol"`
}

// Target is een cluster met de template clusterforge.
type Target struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Environment string    `json:"environment"`
	Status      string    `json:"status"`
	Nodes       []NodeRef `json:"nodes"`
	VIP         string    `json:"vip,omitempty"`
}

// JobRef is een recente taak.
type JobRef struct {
	ID        uuid.UUID  `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"finished_at,omitempty"`
}

// Info is wat de pagina toont.
type Info struct {
	Mode      string     `json:"mode"`
	Version   string     `json:"version"`
	Image     string     `json:"image"`
	Repo      string     `json:"repo"`
	MasterKey bool       `json:"master_key"`
	Export    bool       `json:"export"`
	Host      *uuid.UUID `json:"host_node_id,omitempty"`
	Hosts     []NodeRef  `json:"hosts"`
	Cluster   *Target    `json:"cluster,omitempty"`
	Targets   []Target   `json:"targets"`
	Jobs      []JobRef   `json:"jobs"`
	// Stragglers zijn online agents die te oud zijn om om te zetten
	// (protocol < ReconfigureSince); Offline agents die niet verbonden zijn.
	Stragglers []NodeRef `json:"stragglers"`
	Offline    []NodeRef `json:"offline"`
	Protocol   int       `json:"protocol"`
}

type agentRow struct {
	agentconf.Agent
	Address string
	Cluster *uuid.UUID
}

func (s *Service) agents(ctx context.Context) ([]agentRow, error) {
	rows, err := s.q.ListReconfigureAgents(ctx)
	if err != nil {
		return nil, err
	}
	addrs := map[uuid.UUID]string{}
	r, err := s.pool.Query(ctx, "SELECT id, coalesce(host(primary_ip), '') FROM nodes")
	if err != nil {
		return nil, err
	}
	for r.Next() {
		var id uuid.UUID
		var a string
		if err := r.Scan(&id, &a); err != nil {
			r.Close()
			return nil, err
		}
		addrs[id] = a
	}
	r.Close()
	if err := r.Err(); err != nil {
		return nil, err
	}
	now := s.Now()
	out := make([]agentRow, 0, len(rows))
	for _, row := range rows {
		a := agentconf.Classify(row, agentconf.Endpoints{}, now)
		out = append(out, agentRow{Agent: a, Address: addrs[row.ID], Cluster: row.ClusterID})
	}
	return out, nil
}

func ref(a agentRow) NodeRef {
	return NodeRef{ID: a.NodeID, Hostname: a.Hostname, Address: a.Address, Online: a.Online, Protocol: a.Protocol}
}

// isScanner: de scannercontainer is een agent met hetzelfde IP als de host.
func isScanner(a agentRow) bool {
	return a.Hostname == "cf-scanner" || strings.HasPrefix(a.Hostname, "cf-scanner")
}

func (s *Service) advertiseHost() string {
	h := strings.TrimSpace(s.Advertise)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.Trim(h, "[]")
}

// Info verzamelt de gegevens voor de pagina.
func (s *Service) Info(ctx context.Context) (Info, error) {
	in := Info{
		Mode: s.Mode(), Version: s.Version, Image: s.CurrentImage(), Repo: s.Repo(), MasterKey: len(s.MasterKey) > 0,
		Hosts: []NodeRef{}, Targets: []Target{}, Jobs: []JobRef{}, Stragglers: []NodeRef{}, Offline: []NodeRef{},
		Protocol: protocol.ReconfigureSince,
	}
	if st, err := os.Stat(s.ExportDir); err == nil && st.IsDir() {
		in.Export = true
	}
	agents, err := s.agents(ctx)
	if err != nil {
		return in, err
	}
	host := s.advertiseHost()
	for _, a := range agents {
		switch {
		case !a.Online:
			in.Offline = append(in.Offline, ref(a))
		case a.Protocol < protocol.ReconfigureSince:
			in.Stragglers = append(in.Stragglers, ref(a))
		}
		if isScanner(a) || !a.Online {
			continue
		}
		if host != "" && a.Address == host {
			in.Hosts = append(in.Hosts, ref(a))
			if in.Host == nil {
				id := a.NodeID
				in.Host = &id
			}
		}
	}
	targets, err := s.targets(ctx, agents)
	if err != nil {
		return in, err
	}
	for _, t := range targets {
		if s.Cluster != "" && t.Slug == s.Cluster {
			c := t
			in.Cluster = &c
			continue
		}
		in.Targets = append(in.Targets, t)
	}
	// Zonder herkende host: alle online nodes buiten de doelclusters.
	if len(in.Hosts) == 0 && s.Mode() == ModeCompose {
		inTarget := map[uuid.UUID]bool{}
		for _, t := range targets {
			for _, n := range t.Nodes {
				inTarget[n.ID] = true
			}
		}
		for _, a := range agents {
			if a.Online && !isScanner(a) && !inTarget[a.NodeID] {
				in.Hosts = append(in.Hosts, ref(a))
			}
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id, kind, title, status::text, error, created_at, finished_at FROM jobs
		WHERE kind = ANY($1) ORDER BY created_at DESC LIMIT 10`, []string{KindUpdate, KindMove})
	if err != nil {
		return in, err
	}
	defer rows.Close()
	for rows.Next() {
		var j JobRef
		if err := rows.Scan(&j.ID, &j.Kind, &j.Title, &j.Status, &j.Error, &j.CreatedAt, &j.EndedAt); err != nil {
			return in, err
		}
		in.Jobs = append(in.Jobs, j)
	}
	return in, rows.Err()
}

func (s *Service) targets(ctx context.Context, agents []agentRow) ([]Target, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.name, c.slug, c.environment::text, c.status,
		coalesce((SELECT host(v.address) FROM vips v WHERE v.cluster_id = c.id ORDER BY v.created_at LIMIT 1), '')
		FROM clusters c WHERE c.template_name = $1 ORDER BY c.name`, TemplateName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.Environment, &t.Status, &t.VIP); err != nil {
			return nil, err
		}
		t.Nodes = []NodeRef{}
		for _, a := range agents {
			if a.Cluster != nil && *a.Cluster == t.ID {
				t.Nodes = append(t.Nodes, ref(a))
			}
		}
		slices.SortFunc(t.Nodes, func(a, b NodeRef) int { return strings.Compare(a.Hostname, b.Hostname) })
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- registry --------------------------------------------------------------

// CheckImage kijkt of het image in de registry staat (anoniem, zoals
// docker pull op de host).
func (s *Service) CheckImage(ctx context.Context, image string) (bool, error) {
	slash := strings.Index(image, "/")
	colon := strings.LastIndex(image, ":")
	if slash < 0 || colon < slash {
		return false, errors.New("geen registry in het image")
	}
	reg, repo, tag := image[:slash], image[slash+1:colon], image[colon+1:]
	if !strings.ContainsAny(reg, ".:") {
		reg, repo = "registry-1.docker.io", image[:colon]
	}
	u := "https://" + reg + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	head := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", strings.Join([]string{
			"application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json",
			"application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.docker.distribution.manifest.v2+json",
		}, ", "))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return s.HTTP.Do(req)
	}
	resp, err := head("")
	if err != nil {
		return false, fmt.Errorf("registry %s onbereikbaar: %w", reg, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		tok, err := s.registryToken(ctx, resp.Header.Get("WWW-Authenticate"), repo)
		if err != nil {
			return false, err
		}
		if resp, err = head(tok); err != nil {
			return false, fmt.Errorf("registry %s onbereikbaar: %w", reg, err)
		}
		_ = resp.Body.Close()
	}
	switch {
	case resp.StatusCode/100 == 2:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("de registry weigert (%d): bestaat de repository en is ze publiek?", resp.StatusCode)
	default:
		return false, fmt.Errorf("de registry antwoordt %d", resp.StatusCode)
	}
}

var authParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (s *Service) registryToken(ctx context.Context, challenge, repo string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", errors.New("de registry vraagt een login")
	}
	p := map[string]string{}
	for _, m := range authParam.FindAllStringSubmatch(challenge, -1) {
		p[strings.ToLower(m[1])] = m[2]
	}
	if p["realm"] == "" {
		return "", errors.New("de registry geeft geen tokenadres")
	}
	q := url.Values{}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("token van de registry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token van de registry (%d)", resp.StatusCode)
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	return t.Token, nil
}

// --- starten ---------------------------------------------------------------

// ConflictError: het kan nu niet.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

type updateParams struct {
	Image string     `json:"image"`
	Host  *uuid.UUID `json:"host_node_id,omitempty"`
	From  string     `json:"from_image,omitempty"`
	FromV string     `json:"from_version,omitempty"`
}

func (s *Service) busy(ctx context.Context) error {
	var title string
	err := s.pool.QueryRow(ctx, `SELECT title FROM jobs WHERE kind = ANY($1) AND status IN ('queued', 'running') LIMIT 1`,
		[]string{KindUpdate, KindMove}).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &ConflictError{"er loopt al een taak: " + title}
}

// StartUpdate zet een taak clusterforge.update in de wachtrij.
func (s *Service) StartUpdate(ctx context.Context, actor events.Actor, image string, host *uuid.UUID) (store.Job, error) {
	if err := s.busy(ctx); err != nil {
		return store.Job{}, err
	}
	p := updateParams{Image: image, From: s.CurrentImage(), FromV: s.Version}
	title := "ClusterForge bijwerken naar " + tagOf(image)
	if s.Mode() == ModeCompose {
		if host == nil {
			return store.Job{}, &ConflictError{"kies de node waarop ClusterForge draait"}
		}
		p.Host = host
	}
	spec := jobs.Spec{Kind: KindUpdate, Title: title, Params: p, Actor: actor, NodeID: host}
	j, err := s.runner.Enqueue(ctx, spec)
	if err != nil {
		return j, err
	}
	s.event(ctx, actor, "clusterforge.update_started", map[string]any{"image": image, "from_image": p.From, "from_version": s.Version, "job_id": j.ID})
	return j, nil
}

func tagOf(image string) string {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return image
}

type moveParams struct {
	Cluster   uuid.UUID `json:"cluster_id"`
	Host      uuid.UUID `json:"host_node_id"`
	Token     string    `json:"token_sealed"`
	KeyID     string    `json:"key_id"`
	TokenHash string    `json:"token_hash"`
	ServerURL string    `json:"server_url"`
	Force     bool      `json:"force,omitempty"`
	Metrics   bool      `json:"metrics"`
}

// StartMove zet een taak clusterforge.move in de wachtrij. serverURL is
// het adres waarop de nodes deze server bereiken.
func (s *Service) StartMove(ctx context.Context, actor events.Actor, cluster, host uuid.UUID, serverURL string, force, metrics bool) (store.Job, error) {
	if s.Mode() != ModeCompose {
		return store.Job{}, &ConflictError{"deze server draait al als cluster"}
	}
	if len(s.MasterKey) == 0 || s.box == nil {
		return store.Job{}, &ConflictError{"deze server heeft geen hoofdsleutel (CF_MASTER_KEY); zonder kan het cluster de geheimen niet lezen"}
	}
	if s.CurrentImage() == "" {
		return store.Job{}, &ConflictError{"het image van deze server is onbekend (zelf gebouwd?); werk eerst bij naar een image van CI"}
	}
	if st, err := os.Stat(s.ExportDir); err != nil || !st.IsDir() {
		return store.Job{}, &ConflictError{"de server heeft geen exportmap; werk eerst bij naar deze versie met het nieuwe compose-bestand (Bijwerken hierboven)"}
	}
	if serverURL == "" {
		return store.Job{}, &ConflictError{"het adres voor agents is onbekend; stel het in bij Instellingen → Adres voor agents"}
	}
	if err := s.busy(ctx); err != nil {
		return store.Job{}, err
	}
	var name string
	if err := s.pool.QueryRow(ctx, "SELECT name FROM clusters WHERE id = $1 AND template_name = $2", cluster, TemplateName).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Job{}, &ConflictError{"dat cluster is niet uitgerold met de template clusterforge"}
		}
		return store.Job{}, err
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return store.Job{}, err
	}
	token := hex.EncodeToString(tok)
	sum := sha256.Sum256([]byte(token))
	p := moveParams{
		Cluster: cluster, Host: host, Token: base64.StdEncoding.EncodeToString(s.box.Seal([]byte(token), []byte("selfhost.move"))),
		KeyID: s.box.KeyID, TokenHash: hex.EncodeToString(sum[:]), ServerURL: strings.TrimRight(serverURL, "/"), Force: force, Metrics: metrics,
	}
	spec := jobs.Spec{Kind: KindMove, Title: "ClusterForge verhuizen naar " + name, Params: p, Actor: actor, ClusterID: &cluster}
	j, err := s.runner.EnqueueForCluster(ctx, spec)
	if err != nil {
		return j, err
	}
	s.event(ctx, actor, "clusterforge.move_started", map[string]any{"cluster_id": cluster, "cluster": name, "job_id": j.ID})
	return j, nil
}

func (s *Service) token(p moveParams) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(p.Token)
	if err != nil {
		return "", err
	}
	b, err := s.box.Open(raw, []byte("selfhost.move"), p.KeyID)
	if err != nil {
		return "", fmt.Errorf("sleutel van de verhuizing: %w", err)
	}
	return string(b), nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
		Actor: actor, SubjectType: "server", SubjectID: "clusterforge", Action: action, Payload: payload,
	}); err != nil {
		s.log.Warn("logboek schrijven mislukt", "err", err)
	}
}

func actorOf(j *jobs.Job) events.Actor {
	if j.RequestedBy != nil {
		return events.User(*j.RequestedBy)
	}
	return events.System()
}

// --- scripts ---------------------------------------------------------------

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func header(vars ...string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for i := 0; i+1 < len(vars); i += 2 {
		fmt.Fprintf(&b, "%s=%s\n", vars[i], shq(vars[i+1]))
	}
	return b.String()
}

// UpdateScript is het script voor bijwerken op één server.
func UpdateScript(image string, wait time.Duration, dry bool) string {
	d := "0"
	if dry {
		d = "1"
	}
	return header("IMAGE", image, "WAIT", strconv.Itoa(int(wait.Seconds())), "KEEP", "5", "DRY", d) + composeUpdate
}

// ExportScript maakt de dump voor de verhuizing.
func ExportScript(job uuid.UUID) string { return header("JOB", job.String()) + composeExport }

// StopScript stopt de oude server.
func StopScript() string { return header() + composeStop }

func ctl(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shq(a)
	}
	return "#!/bin/sh\nset -e\n[ -x " + Ctl + " ] || { echo 'FOUT: " + Ctl + " ontbreekt; is deze node uitgerold met de template clusterforge?' >&2; exit 1; }\nexec " + Ctl + " " + strings.Join(q, " ") + "\n"
}

// capture houdt de regels van een run bij en zet ze ook in de stap.
type capture struct {
	st    *jobs.Step
	lines []string
}

func (c *capture) Logf(format string, args ...any) {
	l := fmt.Sprintf(format, args...)
	c.lines = append(c.lines, l)
	if c.st != nil {
		c.st.Logf("%s", l)
	}
}
func (c *capture) Flush(ctx context.Context) error {
	if c.st != nil {
		return c.st.Flush(ctx)
	}
	return nil
}

type memState struct{ b []byte }

func (m *memState) State(v any) bool { return len(m.b) > 0 && json.Unmarshal(m.b, v) == nil }
func (m *memState) SetState(_ context.Context, v any) error {
	b, err := json.Marshal(v)
	m.b = b
	return err
}

// runOn voert een script uit in stap st; een run die niet ok eindigt is een
// fout.
func (s *Service) runOn(ctx context.Context, st *jobs.Step, node uuid.UUID, id, title, script string, timeout time.Duration) error {
	res, err := s.Run.Do(ctx, st, node, protocol.RunSpec{ID: id, Kind: protocol.RunScript, Title: title, Script: script, TimeoutSeconds: int(timeout.Seconds())})
	if err != nil {
		return err
	}
	if res.State != protocol.RunOK {
		return errors.New(noderun.Describe(res))
	}
	return nil
}

// Role is wat clusterforge-ctl.sh rol over een node zegt.
type Role struct {
	Node     uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Index    int       `json:"index"`
	VIP      bool      `json:"vip"`
	Moved    bool      `json:"moved"`
	Active   bool      `json:"active"`
	Image    string    `json:"image"`
}

func parseRole(lines []string) Role {
	var r Role
	for _, l := range lines {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "index":
			r.Index, _ = strconv.Atoi(v)
		case "vip":
			r.VIP = v == "1"
		case "verhuisd":
			r.Moved = v == "1"
		case "actief":
			r.Active = v == "1"
		case "image":
			r.Image = v
		}
	}
	return r
}

// roles vraagt elke node van het cluster naar zijn rol.
func (s *Service) roles(ctx context.Context, st *jobs.Step, cluster uuid.UUID) ([]Role, error) {
	agents, err := s.agents(ctx)
	if err != nil {
		return nil, err
	}
	var out []Role
	for _, a := range agents {
		if a.Cluster == nil || *a.Cluster != cluster {
			continue
		}
		if !a.Online {
			st.Logf("%s: niet verbonden; overgeslagen", a.Hostname)
			continue
		}
		c := &capture{}
		spec := protocol.RunSpec{ID: "cf-rol-" + uuid.NewString(), Kind: protocol.RunScript, Title: "rol", Script: ctl("rol"), TimeoutSeconds: 60}
		res, err := s.Run.DoWith(ctx, c, &memState{}, a.NodeID, spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.Hostname, err)
		}
		if res.State != protocol.RunOK {
			for _, l := range c.lines {
				st.Logf("%s: %s", a.Hostname, l)
			}
			return nil, fmt.Errorf("%s: %s", a.Hostname, noderun.Describe(res))
		}
		r := parseRole(c.lines)
		r.Node, r.Hostname = a.NodeID, a.Hostname
		st.Logf("%s: node %d, VIP %s, %s, server %s%s", r.Hostname, r.Index, yes(r.VIP, "hier", "elders"),
			yes(r.Moved, "verhuisd", "nog niet verhuisd"), yes(r.Active, "actief", "gestopt"), yes(r.Image != "", ", "+r.Image, ""))
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Role) int { return a.Index - b.Index })
	return out, nil
}

func yes(b bool, a, c string) string {
	if b {
		return a
	}
	return c
}

func vipOf(roles []Role) (Role, []Role, error) {
	var vip []Role
	var rest []Role
	for _, r := range roles {
		if r.VIP {
			vip = append(vip, r)
		} else {
			rest = append(rest, r)
		}
	}
	switch len(vip) {
	case 1:
		return vip[0], rest, nil
	case 0:
		return Role{}, nil, errors.New("geen enkele node heeft het VIP; kijk keepalived na (clusterforge-ctl.sh status)")
	default:
		return Role{}, nil, errors.New("meer dan één node heeft het VIP (split-brain); wacht tot keepalived dat oplost")
	}
}

// --- bijwerken -------------------------------------------------------------

type rolesState struct {
	VIP  Role   `json:"vip"`
	Rest []Role `json:"rest"`
}

func (s *Service) update(ctx context.Context, j *jobs.Job) error {
	var p updateParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := actorOf(j)
	var err error
	if p.Host != nil {
		err = s.updateCompose(ctx, j, p)
	} else {
		err = s.updateCluster(ctx, j, p)
	}
	if err != nil {
		s.event(ctx, actor, "clusterforge.update_failed", map[string]any{"image": p.Image, "error": err.Error(), "job_id": j.ID})
		return err
	}
	s.event(ctx, actor, "clusterforge.updated", map[string]any{"image": p.Image, "version": s.Version, "from_image": p.From, "from_version": p.FromV, "job_id": j.ID})
	return nil
}

func (s *Service) updateCompose(ctx context.Context, j *jobs.Job, p updateParams) error {
	if err := j.Step(ctx, "Bijwerken op de host (back-up, image, herstart)", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("Deze server herstart halverwege; de taak leest daarna verder.")
		return s.runOn(ctx, st, *p.Host, "cf-update-"+j.ID.String(), j.Title, UpdateScript(p.Image, s.Wait, false), time.Hour)
	}); err != nil {
		return err
	}
	return j.Step(ctx, "Nieuwe versie nakijken", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("ClusterForge draait versie %s (image %s).", s.Version, s.CurrentImage())
		return nil
	})
}

func (s *Service) updateCluster(ctx context.Context, j *jobs.Job, p updateParams) error {
	cluster, err := s.ownCluster(ctx)
	if err != nil {
		return err
	}
	var rs rolesState
	if err := j.Step(ctx, "Nodes van het cluster", func(ctx context.Context, st *jobs.Step) error {
		roles, err := s.roles(ctx, st, cluster)
		if err != nil {
			return err
		}
		vip, rest, err := vipOf(roles)
		if err != nil {
			return err
		}
		if !vip.Active {
			return errors.New(vip.Hostname + " heeft het VIP maar de server draait er niet")
		}
		rs = rolesState{VIP: vip, Rest: rest}
		return st.SetState(ctx, rs)
	}); err != nil {
		return err
	}
	if rs.VIP.Hostname == "" && !j.Done("Nodes van het cluster", &rs) {
		return errors.New("stand van de nodes onbekend")
	}
	for _, r := range rs.Rest {
		if err := j.Step(ctx, r.Hostname+": image vooraf halen", func(ctx context.Context, st *jobs.Step) error {
			return s.runOn(ctx, st, r.Node, "cf-pull-"+j.ID.String()+"-"+r.Node.String(), "image halen", ctl("pull", p.Image), 30*time.Minute)
		}); err != nil {
			return err
		}
	}
	if err := j.Step(ctx, rs.VIP.Hostname+": bijwerken (back-up, image, herstart)", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("De server herstart halverwege; de taak leest daarna verder.")
		return s.runOn(ctx, st, rs.VIP.Node, "cf-update-"+j.ID.String(), j.Title,
			ctl("bijwerken", p.Image, strconv.Itoa(int(s.Wait.Seconds()))), time.Hour)
	}); err != nil {
		return err
	}
	for _, r := range rs.Rest {
		if err := j.Step(ctx, r.Hostname+": nieuw image klaarzetten", func(ctx context.Context, st *jobs.Step) error {
			return s.runOn(ctx, st, r.Node, "cf-setimage-"+j.ID.String()+"-"+r.Node.String(), "image zetten", ctl("set-image", p.Image), 5*time.Minute)
		}); err != nil {
			return err
		}
	}
	return j.Step(ctx, "Nieuwe versie nakijken", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("ClusterForge draait versie %s (image %s).", s.Version, s.CurrentImage())
		return nil
	})
}

func (s *Service) ownCluster(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, "SELECT id FROM clusters WHERE slug = $1", s.Cluster).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, fmt.Errorf("het eigen cluster %q (CF_SELF_CLUSTER) bestaat niet", s.Cluster)
	}
	return id, err
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// --- verhuizen -------------------------------------------------------------

type exportState struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Schema string `json:"schema"`
}

type reconfState struct {
	SentAt time.Time            `json:"sent_at"`
	Sent   []uuid.UUID          `json:"sent"`
	Names  map[uuid.UUID]string `json:"names"`
	Failed []string             `json:"failed"`
}

func (s *Service) move(ctx context.Context, j *jobs.Job) error {
	var p moveParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := actorOf(j)
	// Testlijst 3, N17: de dump is genomen terwijl deze taak liep. Op het
	// cluster (de nieuwe server) staat hij dus nog op "bezig"; daar niets
	// opnieuw doen, alleen netjes afsluiten.
	if s.onTarget(ctx, p) {
		return s.finishMoved(ctx, j)
	}
	err := s.doMove(ctx, j, p)
	if err != nil {
		s.event(ctx, actor, "clusterforge.move_failed", map[string]any{"cluster_id": p.Cluster, "error": err.Error(), "job_id": j.ID})
	}
	return err
}

// onTarget: deze server draait al op het cluster waar de verhuizing heen
// ging.
func (s *Service) onTarget(ctx context.Context, p moveParams) bool {
	if s.Cluster == "" {
		return false
	}
	id, err := s.ownCluster(ctx)
	return err == nil && id == p.Cluster
}

// finishMoved sluit de verhuistaak af op de nieuwe server: de stap die liep
// toen de dump genomen werd, is gelukt (deze server draait op die dump); de
// stappen daarna liepen op de oude server.
func (s *Service) finishMoved(ctx context.Context, j *jobs.Job) error {
	if _, err := s.pool.Exec(ctx, `UPDATE job_steps SET status = 'succeeded', finished_at = coalesce(finished_at, now()),
		log = log || $2::text[] WHERE job_id = $1 AND status IN ('running', 'queued')`, j.ID,
		[]string{"Deze stap liep nog toen de dump genomen werd; de dump is goed aangekomen: deze server draait erop."}); err != nil {
		return err
	}
	return j.Step(ctx, "Afgerond op het cluster", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("De verhuizing is gelukt: deze server draait op het cluster met de database van de oude server.")
		st.Logf("Overnemen, agents omzetten en de oude server stoppen liepen op de oude server; hun log stond daar.")
		return nil
	})
}

// isTimeout: het adres antwoordt niet eens met een weigering (firewall).
func isTimeout(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "timeout") || strings.Contains(m, "timed out") || strings.Contains(m, "deadline")
}

// portCheck laat elke verbonden agent de poorten van het VIP opvragen
// (testlijst 3, N12: na de verhuizing haalde Domotica-PI zijn update niet
// via 8080, alleen 4222 was nagekeken). Vooraf draait er nog niets op het
// VIP: een weigering is dan goed (de firewall laat door), alleen een
// time-out wijst op een firewall. Achteraf moet 8080 echt antwoorden.
func (s *Service) portCheck(ctx context.Context, st *jobs.Step, vip string, after bool) []string {
	if s.Web == nil || vip == "" {
		return nil
	}
	agents, err := s.agents(ctx)
	if err != nil {
		return nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var bad []string
	for _, a := range agents {
		if isScanner(a) || !a.Online || a.Protocol < protocol.WebSince {
			continue
		}
		wg.Add(1)
		go func(a agentRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var miss []string
			for _, u := range []string{"http://" + net.JoinHostPort(vip, "8080") + "/healthz", "https://" + net.JoinHostPort(vip, "4222") + "/"} {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				res, err := s.Web.Web(cctx, a.NodeID, protocol.WebRequest{Op: protocol.WebOpCheckURL, Check: &protocol.WebCheck{URL: u}})
				cancel()
				msg := ""
				switch {
				case err != nil:
					msg = err.Error()
				case res.Check != nil:
					msg = res.Check.Error
				}
				port := "8080"
				if strings.Contains(u, ":4222") {
					port = "4222"
				}
				switch {
				case isTimeout(msg):
					miss = append(miss, port)
				case after && port == "8080" && msg != "":
					miss = append(miss, port)
				}
			}
			if len(miss) > 0 {
				mu.Lock()
				bad = append(bad, fmt.Sprintf("%s (%s)", a.Hostname, strings.Join(miss, ", ")))
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	slices.Sort(bad)
	if len(bad) > 0 {
		st.Logf("LET OP: deze nodes bereiken %s niet op poort 4222 en/of 8080 (firewall? in OPNsense beide naar %s toelaten): %s", vip, vip, strings.Join(bad, "; "))
	} else {
		st.Logf("Alle verbonden agents bereiken %s op 4222 en 8080.", vip)
	}
	return bad
}

// stopOld stopt de oude server. Een agent die tijdens de verhuizing
// herstartte, kwam met zijn oude agent.json terug bij deze (oude) server
// (testlijst 3, N3): die krijgt eerst alsnog het nieuwe adres, en het
// stoppen wacht tot de agent van de host weer verbonden is.
func (s *Service) stopOld(ctx context.Context, j *jobs.Job, st *jobs.Step, host uuid.UUID, server, nats string, sent []uuid.UUID) error {
	deadline := s.Now().Add(10 * time.Minute)
	redirected := map[uuid.UUID]bool{}
	for {
		agents, err := s.agents(ctx)
		if err != nil {
			return err
		}
		hostOnline := false
		for _, a := range agents {
			if a.NodeID == host && a.Online {
				hostOnline = true
			}
			if isScanner(a) || !a.Online || a.Protocol < protocol.ReconfigureSince || slices.Contains(sent, a.NodeID) || redirected[a.NodeID] ||
				a.NodeID == host || a.NatsURL == nats {
				continue
			}
			cmd := protocol.Command{
				ID: "move-again-" + j.ID.String() + "-" + a.NodeID.String(), Action: protocol.CmdAgentReconfigure,
				Deadline: s.Now().Add(protocol.ReconfigureTimeout), Reason: j.Title,
				Reconfigure: &protocol.AgentReconfigure{ServerURL: server, NatsURL: nats},
			}
			cctx, cancel := context.WithTimeout(ctx, protocol.ReconfigureTimeout+30*time.Second)
			res, err := s.bus.Command(cctx, a.NodeID, cmd)
			cancel()
			switch {
			case err != nil:
				st.Logf("%s kwam terug bij de oude server; omzetten mislukt: %v", a.Hostname, err)
			case !res.OK:
				st.Logf("%s kwam terug bij de oude server; omzetten mislukt: %s", a.Hostname, res.Error)
			default:
				st.Logf("%s kwam terug bij de oude server (herstart tijdens de verhuizing?) en is alsnog omgezet", a.Hostname)
				redirected[a.NodeID] = true
			}
		}
		if hostOnline {
			err := s.runOn(ctx, st, host, "cf-stop-"+j.ID.String(), "oude server stoppen", StopScript(), 5*time.Minute)
			if err == nil {
				return nil
			}
			if !strings.Contains(err.Error(), "niet verbonden") || s.Now().After(deadline) {
				return err
			}
		}
		if s.Now().After(deadline) {
			return errors.New("de agent van de oude server is na 10 minuten nog niet verbonden; de oude server draait nog. Stop hem met de hand (op de host: systemctl stop clusterforge of docker compose down in /opt/clusterforge/deploy), anders draaien er twee servers")
		}
		st.Logf("de agent van de oude server is (nog) niet verbonden; over 20 s opnieuw")
		_ = st.Flush(ctx)
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(20 * time.Second):
		}
	}
}

func (s *Service) doMove(ctx context.Context, j *jobs.Job, p moveParams) error {
	token, err := s.token(p)
	if err != nil {
		return err
	}
	var rs rolesState
	if err := j.Step(ctx, "Controle: nodes van het cluster en agents", func(ctx context.Context, st *jobs.Step) error {
		roles, err := s.roles(ctx, st, p.Cluster)
		if err != nil {
			return err
		}
		if len(roles) == 0 {
			return errors.New("geen node van het cluster is verbonden")
		}
		vip, rest, err := vipOf(roles)
		if err != nil {
			return err
		}
		if vip.Moved {
			return errors.New(vip.Hostname + " heeft al een ClusterForge; er is niets veranderd")
		}
		agents, err := s.agents(ctx)
		if err != nil {
			return err
		}
		var old, off []string
		for _, a := range agents {
			switch {
			case isScanner(a):
			case !a.Online:
				off = append(off, a.Hostname)
			case a.Protocol < protocol.ReconfigureSince:
				old = append(old, a.Hostname)
			}
		}
		if len(old) > 0 {
			msg := fmt.Sprintf("deze agents zijn te oud om om te zetten (protocol < %d): %s. Werk ze eerst bij (Nodes → Agents bijwerken)", protocol.ReconfigureSince, strings.Join(old, ", "))
			if !p.Force {
				return errors.New(msg)
			}
			st.Logf("LET OP: %s; ze blijven naar de oude server wijzen.", msg)
		}
		if len(off) > 0 {
			st.Logf("LET OP: niet verbonden, dus niet om te zetten: %s. Die installeer je later opnieuw met het commando van het cluster.", strings.Join(off, ", "))
		}
		rs = rolesState{VIP: vip, Rest: rest}
		st.Logf("VIP-node: %s; andere nodes: %d", vip.Hostname, len(rest))
		var addr string
		_ = s.pool.QueryRow(ctx, `SELECT coalesce((SELECT host(address) FROM vips WHERE cluster_id = $1 ORDER BY created_at LIMIT 1), '')`, p.Cluster).Scan(&addr)
		s.portCheck(ctx, st, addr, false)
		return st.SetState(ctx, rs)
	}); err != nil {
		return err
	}
	if rs.VIP.Hostname == "" && !j.Done("Controle: nodes van het cluster en agents", &rs) {
		return errors.New("stand van de nodes onbekend")
	}
	// Fase 48: de oude server is vervangen door het cluster. Vóór de dump,
	// zodat het record meeverhuist: daaruit volgen de voorstellen voor de
	// firewall (4222 en 8080 naar het VIP) en NPM, en na 14 dagen de vraag om
	// de oude server op te ruimen.
	if _, err := s.pool.Exec(ctx, `INSERT INTO replacements (cluster_id, old_node_id, old_hostname, old_address, vip, reason, job_id)
		SELECT $1, n.id, n.hostname, n.primary_ip,
		(SELECT v.address FROM vips v WHERE v.cluster_id = $1 ORDER BY v.created_at LIMIT 1), 'clusterforge', $3
		FROM nodes n WHERE n.id = $2 AND n.primary_ip IS NOT NULL
		ON CONFLICT (cluster_id, old_address) WHERE state IN ('actief', 'opruimen') DO NOTHING`, p.Cluster, p.Host, j.ID); err != nil {
		s.log.Warn("vervanging vastleggen", "err", err)
	}
	var ex exportState
	if err := j.Step(ctx, "Dump van de database op de host", func(ctx context.Context, st *jobs.Step) error {
		if err := s.runOn(ctx, st, p.Host, "cf-export-"+j.ID.String(), "dump", ExportScript(j.ID), time.Hour); err != nil {
			return err
		}
		sum, size, err := s.sumDump(j.ID)
		if err != nil {
			return err
		}
		ex = exportState{SHA256: sum, Size: size}
		_ = s.pool.QueryRow(ctx, "SELECT coalesce(max(version_id), 0)::text FROM goose_db_version WHERE is_applied").Scan(&ex.Schema)
		st.Logf("dump: %d bytes, sha256 %s", size, sum)
		return st.SetState(ctx, ex)
	}); err != nil {
		return err
	}
	url := p.ServerURL
	args := []string{"overnemen", url, token}
	if !p.Metrics {
		args = append(args, "--zonder-metrics")
	}
	if err := j.Step(ctx, rs.VIP.Hostname+": overnemen (database, hoofdsleutel, server starten)", func(ctx context.Context, st *jobs.Step) error {
		return s.runOn(ctx, st, rs.VIP.Node, "cf-overnemen-"+j.ID.String(), "overnemen", ctl(args...), 2*time.Hour)
	}); err != nil {
		return err
	}
	for _, r := range rs.Rest {
		if err := j.Step(ctx, r.Hostname+": hoofdsleutel en instellingen", func(ctx context.Context, st *jobs.Step) error {
			return s.runOn(ctx, st, r.Node, "cf-ontvang-"+j.ID.String()+"-"+r.Node.String(), "ontvang", ctl("ontvang", url, token), 30*time.Minute)
		}); err != nil {
			return err
		}
	}
	var vip string
	if err := s.pool.QueryRow(ctx, `SELECT coalesce((SELECT host(address) FROM vips WHERE cluster_id = $1 AND owner_node_id = $2 LIMIT 1),
		(SELECT host(address) FROM vips WHERE cluster_id = $1 ORDER BY created_at LIMIT 1), '')`, p.Cluster, rs.VIP.Node).Scan(&vip); err != nil {
		return err
	}
	if vip == "" {
		return errors.New("het VIP van het cluster is onbekend")
	}
	server := "http://" + net.JoinHostPort(vip, "8080")
	nats := "tls://" + net.JoinHostPort(vip, "4222")
	var rc reconfState
	if err := j.Step(ctx, "Agents omzetten naar het VIP "+vip, func(ctx context.Context, st *jobs.Step) error {
		return s.reconfigureAll(ctx, j, st, server, nats, &rc)
	}); err != nil {
		return err
	}
	j.Done("Agents omzetten naar het VIP "+vip, &rc)
	if len(rc.Failed) > 0 && !p.Force {
		return fmt.Errorf("niet alle agents staan op het cluster (%s); de oude server blijft draaien. Zet ze om bij Instellingen → Adres voor agents, of start opnieuw met \"toch verhuizen\"", strings.Join(rc.Failed, ", "))
	}
	_ = j.Step(ctx, "Controle: agents bereiken 4222 en 8080 op "+vip, func(ctx context.Context, st *jobs.Step) error {
		s.portCheck(ctx, st, vip, true)
		return nil
	})
	if err := j.Step(ctx, "Oude server stoppen", func(ctx context.Context, st *jobs.Step) error {
		s.event(ctx, actorOf(j), "clusterforge.moved", map[string]any{"cluster_id": p.Cluster, "vip": vip, "job_id": j.ID})
		st.Logf("ClusterForge draait nu op %s. Log daar opnieuw in.", server)
		st.Logf("Nog te doen, als voorstel bij het cluster (Vervangen en het VIP): de firewallregel voor 4222 en 8080 naar %s, en de NPM-host van ClusterForge naar %s:8080 met het wildcard-certificaat.", vip, vip)
		return s.stopOld(ctx, j, st, p.Host, server, nats, rc.Sent)
	}); err != nil {
		return err
	}
	return nil
}

func (s *Service) dumpPath(job uuid.UUID) string {
	return filepath.Join(s.ExportDir, "clusterforge-"+job.String()+".dump")
}

func (s *Service) sumDump(job uuid.UUID) (string, int64, error) {
	f, err := os.Open(s.dumpPath(job))
	if err != nil {
		return "", 0, fmt.Errorf("de dump is niet zichtbaar voor de server: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	if n == 0 {
		return "", 0, errors.New("de dump is leeg")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// reconfigureAll zet elke verbonden agent om naar het cluster. Een agent
// test het nieuwe adres eerst en zet zijn oude agent.json terug als hij de
// server daar niet bereikt; dan komt hij hier terug. Wie na de wachttijd
// niet terug is, praat met het cluster.
func (s *Service) reconfigureAll(ctx context.Context, j *jobs.Job, st *jobs.Step, server, nats string, rc *reconfState) error {
	if !st.State(rc) || rc.SentAt.IsZero() {
		agents, err := s.agents(ctx)
		if err != nil {
			return err
		}
		rc.SentAt = s.Now()
		rc.Names = map[uuid.UUID]string{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, a := range agents {
			if isScanner(a) || !a.Online || a.Protocol < protocol.ReconfigureSince {
				continue
			}
			wg.Add(1)
			go func(a agentRow) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				cmd := protocol.Command{
					ID: "move-" + j.ID.String() + "-" + a.NodeID.String(), Action: protocol.CmdAgentReconfigure,
					Deadline: rc.SentAt.Add(protocol.ReconfigureTimeout), Reason: j.Title,
					Reconfigure: &protocol.AgentReconfigure{ServerURL: server, NatsURL: nats},
				}
				cctx, cancel := context.WithTimeout(ctx, protocol.ReconfigureTimeout+30*time.Second)
				res, err := s.bus.Command(cctx, a.NodeID, cmd)
				cancel()
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					st.Logf("%s: %v", a.Hostname, err)
					rc.Failed = append(rc.Failed, a.Hostname)
				case !res.OK:
					st.Logf("%s: %s", a.Hostname, res.Error)
					rc.Failed = append(rc.Failed, a.Hostname)
				default:
					st.Logf("%s: omgezet, test het cluster", a.Hostname)
					rc.Sent = append(rc.Sent, a.NodeID)
					rc.Names[a.NodeID] = a.Hostname
				}
			}(a)
		}
		wg.Wait()
		if err := st.SetState(ctx, rc); err != nil {
			return err
		}
	}
	st.Logf("wachten (%s) of er agents terugkomen omdat ze het cluster niet bereikten", s.MoveWait.Round(time.Second))
	_ = st.Flush(ctx)
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(time.Until(rc.SentAt.Add(s.MoveWait))):
	}
	for _, id := range rc.Sent {
		cur, err := s.q.GetReconfigureAgent(ctx, id)
		if err != nil {
			continue
		}
		back := cur.HeartbeatAt != nil && cur.HeartbeatAt.After(rc.SentAt.Add(protocol.ReconfigureRollbackAfter)) && s.Now().Sub(*cur.HeartbeatAt) < status.HeartbeatLate
		if back || cur.ReconfigureRolledBack == nats {
			st.Logf("%s: kwam terug naar deze server (bereikte het cluster niet)", rc.Names[id])
			rc.Failed = append(rc.Failed, rc.Names[id])
		}
	}
	moved := 0
	for _, id := range rc.Sent {
		if !slices.Contains(rc.Failed, rc.Names[id]) {
			moved++
		}
	}
	st.Logf("%d agents praten nu met het cluster, %d niet", moved, len(rc.Failed))
	return st.SetState(ctx, rc)
}

// --- export ----------------------------------------------------------------

// ErrNoMove: geen lopende verhuizing met deze sleutel.
var ErrNoMove = errors.New("geen lopende verhuizing met deze sleutel")

// exportJob zoekt de lopende verhuizing bij de sleutel.
func (s *Service) exportJob(ctx context.Context, token string) (uuid.UUID, error) {
	if len(token) != 64 {
		return uuid.Nil, ErrNoMove
	}
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])
	rows, err := s.pool.Query(ctx, "SELECT id, params FROM jobs WHERE kind = $1 AND status = 'running'", KindMove)
	if err != nil {
		return uuid.Nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return uuid.Nil, err
		}
		var p moveParams
		if json.Unmarshal(raw, &p) == nil && subtle.ConstantTimeCompare([]byte(p.TokenHash), []byte(want)) == 1 {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}
	return uuid.Nil, ErrNoMove
}

// ServeExport is /selfexport/{token}/{meta|dump|metrics}, zonder sessie:
// de sleutel van de lopende verhuizing is de toegang.
func (s *Service) ServeExport(w http.ResponseWriter, r *http.Request, token, what string) {
	ctx := r.Context()
	job, err := s.exportJob(ctx, token)
	if err != nil {
		if !errors.Is(err, ErrNoMove) {
			s.log.Warn("verhuizing: sleutel nakijken", "err", err)
		}
		http.Error(w, "geen lopende verhuizing met deze sleutel", http.StatusForbidden)
		return
	}
	s.log.Info("verhuizing: export opgehaald", "what", what, "from", r.RemoteAddr, "job", job)
	switch what {
	case "meta":
		sum, _, err := s.sumDump(job)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var schema string
		_ = s.pool.QueryRow(ctx, "SELECT coalesce(max(version_id), 0)::text FROM goose_db_version WHERE is_applied").Scan(&schema)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		bw := bufio.NewWriter(w)
		_, _ = fmt.Fprintf(bw, "job=%s\nversion=%s\nimage=%s\nschema=%s\nsha256=%s\nmaster_key=%s\n",
			job, s.Version, s.CurrentImage(), schema, sum, base64.StdEncoding.EncodeToString(s.MasterKey))
		keys := make([]string, 0, len(s.Settings))
		for k := range s.Settings {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			v := strings.NewReplacer("\n", " ", "\r", " ").Replace(s.Settings[k])
			_, _ = fmt.Fprintf(bw, "env.%s=%s\n", k, v)
		}
		_ = bw.Flush()
	case "dump":
		f, err := os.Open(s.dumpPath(job))
		if err != nil {
			http.Error(w, "de dump is er niet", http.StatusConflict)
			return
		}
		defer func() { _ = f.Close() }()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.Copy(w, f)
	case "metrics":
		if s.VMURL == "" {
			http.Error(w, "geen VictoriaMetrics", http.StatusConflict)
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.VMURL, "/")+`/api/v1/export/native?match[]={__name__!=""}`, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			http.Error(w, "VictoriaMetrics: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "VictoriaMetrics antwoordt "+resp.Status, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, resp.Body)
	default:
		http.NotFound(w, r)
	}
}
