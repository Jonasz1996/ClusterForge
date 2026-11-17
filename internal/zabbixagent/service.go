// Package zabbixagent installeert zabbix-agent2 op de nodes met een
// ClusterForge-agent, geeft elke node een eigen PSK en zet die sleutel ook
// bij de host in Zabbix, via de API. Een nieuwe sleutel gaat eerst naar de
// node en meteen daarna naar Zabbix, zodat de host maar enkele seconden
// onbereikbaar is.
package zabbixagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Kind is de taak die zabbix-agent2 op een node toepast.
const Kind = "zabbixagent.apply"

// RetryFailed is hoe lang een mislukte automatische toepassing wacht.
const RetryFailed = time.Hour

// Service beheert zabbix-agent2 op de nodes.
type Service struct {
	pool   *pgxpool.Pool
	ev     *events.Writer
	runner *jobs.Runner
	log    *slog.Logger
	Run    *noderun.Runner
	// Box versleutelt de PSK's en het API-token; nil zonder masterkey.
	Box *secrets.Box
	// API maakt de client voor de Zabbix API; tests zetten een eigen.
	API       func(url, token string) *Client
	Now       func() time.Time
	PollEvery time.Duration
	tick      sync.Mutex
	Changed   func()
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, runner *jobs.Runner, bus noderun.Bus, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		pool: pool, ev: ev, runner: runner, log: log, Run: &noderun.Runner{Bus: bus},
		API: func(u, t string) *Client { return &Client{URL: u, Token: t} },
		Now: time.Now, PollEvery: time.Minute,
	}
	if runner != nil {
		runner.Register(Kind, s.apply)
	}
	return s
}

// FieldError: een ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// ErrNoMasterKey: zonder masterkey kan de server geen sleutels bewaren.
var ErrNoMasterKey = &FieldError{"de server heeft geen masterkey (CF_MASTER_KEY), dus hij kan geen PSK's en API-token bewaren"}

var tokenAAD = []byte("zabbixagent:token")

func pskAAD(id uuid.UUID) []byte { return []byte("zabbixagent:psk:" + id.String()) }

func (s *Service) event(ctx context.Context, actor events.Actor, n *Node, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	e := events.Event{Actor: actor, SubjectType: "zabbixagent", SubjectID: "settings", Action: action, Payload: payload}
	if n != nil {
		e.SubjectType, e.SubjectID, e.ClusterID = "node", n.NodeID.String(), n.ClusterID
		if e.Payload == nil {
			e.Payload = map[string]any{}
		}
		e.Payload["hostname"] = n.Hostname
	}
	if err := s.ev.Write(ctx, nil, e); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// SettingsInput is wat de beheerder bewaart. Token nil laat het token
// staan; een lege string haalt het weg.
type SettingsInput struct {
	Settings
	Token *string
}

// UpdateSettings bewaart de instellingen. Een gewijzigde Server of versie
// maakt alle nodes "nog toe te passen".
func (s *Service) UpdateSettings(ctx context.Context, actor events.Actor, userID *uuid.UUID, in SettingsInput) (Settings, error) {
	in.Server, in.ServerActive = clean(in.Server), clean(in.ServerActive)
	in.Version, in.HostMetadata = strings.TrimSpace(in.Version), strings.TrimSpace(in.HostMetadata)
	in.APIURL, in.HostGroup = strings.TrimSpace(in.APIURL), strings.TrimSpace(in.HostGroup)
	in.DockerTemplate, in.Proxy = strings.TrimSpace(in.DockerTemplate), strings.TrimSpace(in.Proxy)
	var tpls []string
	for _, t := range in.Templates {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(tpls, t) {
			tpls = append(tpls, t)
		}
	}
	if tpls == nil {
		tpls = []string{}
	}
	if in.Server == "" && in.ServerActive != "" {
		in.Server = in.ServerActive
	}
	if in.ServerActive == "" && in.Server != "" {
		in.ServerActive = in.Server
	}
	check := Config{Hostname: "x", Server: in.Server, ServerActive: in.ServerActive, Version: in.Version, HostMetadata: in.HostMetadata}
	if in.Server == "" {
		check.Server, check.ServerActive = "x", "x"
	}
	if err := check.Validate(); err != nil {
		return Settings{}, &FieldError{err.Error()}
	}
	if in.APIURL != "" {
		u, err := url.Parse(in.APIURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Settings{}, &FieldError{"API-adres zoals http://192.168.199.50/api_jsonrpc.php"}
		}
		if !strings.HasSuffix(u.Path, "api_jsonrpc.php") {
			u.Path = strings.TrimSuffix(u.Path, "/") + "/api_jsonrpc.php"
			in.APIURL = u.String()
		}
	}
	for _, v := range append([]string{in.HostGroup, in.DockerTemplate, in.Proxy}, tpls...) {
		if len(v) > 255 || strings.ContainsAny(v, "\n\r") {
			return Settings{}, &FieldError{"namen in Zabbix: hoogstens 255 tekens op één regel"}
		}
	}
	if in.HostGroup == "" {
		in.HostGroup = "ClusterForge"
	}
	if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_settings SET auto_apply = $1, auto_prod = $2, server = $3, server_active = $4,
		version = $5, host_metadata = $6, api_url = $7, host_group = $8, templates = $9, docker_template = $10, proxy = $11,
		updated_at = now(), updated_by = $12`,
		in.AutoApply, in.AutoProd, in.Server, in.ServerActive, in.Version, in.HostMetadata, in.APIURL, in.HostGroup, tpls,
		in.DockerTemplate, in.Proxy, userID); err != nil {
		return Settings{}, err
	}
	tokenChanged := false
	if in.Token != nil {
		tok := strings.TrimSpace(*in.Token)
		switch {
		case tok == "":
			if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_settings SET api_token_enc = NULL, api_token_id = ''`); err != nil {
				return Settings{}, err
			}
		case s.Box == nil:
			return Settings{}, ErrNoMasterKey
		case len(tok) > 512 || strings.ContainsAny(tok, " \n\r\t"):
			return Settings{}, &FieldError{"dat lijkt geen API-token van Zabbix"}
		default:
			if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_settings SET api_token_enc = $1, api_token_id = $2`,
				s.Box.Seal([]byte(tok), tokenAAD), s.Box.KeyID); err != nil {
				return Settings{}, err
			}
		}
		tokenChanged = true
	}
	// Nieuwe instellingen: eerdere fouten tellen niet meer.
	if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_nodes SET apply_failed_at = NULL`); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "zabbixagent.settings_changed", map[string]any{
		"auto_apply": in.AutoApply, "auto_prod": in.AutoProd, "server": in.Server, "server_active": in.ServerActive,
		"version": in.Version, "api_url": in.APIURL, "token_changed": tokenChanged, "host_group": in.HostGroup, "proxy": in.Proxy,
	})
	return s.Settings(ctx)
}

func clean(v string) string {
	v = strings.Join(strings.Fields(v), "")
	return strings.Trim(v, ",")
}

func (s *Service) token(set Settings) (string, error) {
	if len(set.tokenEnc) == 0 {
		return "", &FieldError{"vul eerst het API-token van Zabbix in"}
	}
	if s.Box == nil {
		return "", ErrNoMasterKey
	}
	b, err := s.Box.Open(set.tokenEnc, tokenAAD, set.tokenID)
	if err != nil {
		return "", errors.New("het API-token is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul het opnieuw in")
	}
	return string(b), nil
}

// Client geeft de client voor de Zabbix API met het bewaarde token.
func (s *Service) client(set Settings) (*Client, error) {
	if set.APIURL == "" {
		return nil, &FieldError{"vul eerst het adres van de Zabbix API in"}
	}
	tok, err := s.token(set)
	if err != nil {
		return nil, err
	}
	return s.API(set.APIURL, tok), nil
}

// Test probeert de Zabbix API met de bewaarde instellingen; geeft de versie.
func (s *Service) Test(ctx context.Context) (string, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	c, err := s.client(set)
	if err != nil {
		return "", err
	}
	return c.Check(ctx)
}

func newPSK() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Service) pskHash(psk string) string {
	return secrets.Fingerprint(s.Box.Derive("zabbixagent psk"), []byte(psk))[:16]
}

// Enable zet het beheer aan op de nodes; een node zonder sleutel krijgt er
// een. Toepassen volgt bij de volgende ronde als automatisch aan staat, of
// met Apply.
func (s *Service) Enable(ctx context.Context, actor events.Actor, ids []uuid.UUID) ([]string, error) {
	if s.Box == nil {
		return nil, ErrNoMasterKey
	}
	var skipped []string
	for _, id := range ids {
		n, err := s.Node(ctx, id)
		if err != nil {
			return nil, err
		}
		if !n.Supported {
			skipped = append(skipped, n.Hostname+": agent te oud (Nodes → Agents bijwerken)")
			continue
		}
		if err := s.enable(ctx, n); err != nil {
			return nil, err
		}
		s.event(ctx, actor, &n, "zabbixagent.node_managed", nil)
	}
	return skipped, nil
}

func (s *Service) enable(ctx context.Context, n Node) error {
	if n.Enabled {
		_, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_nodes SET managed = true, apply_failed_at = NULL WHERE node_id = $1`, n.NodeID)
		return err
	}
	psk := newPSK()
	_, err := s.pool.Exec(ctx, `INSERT INTO zabbix_agent_nodes (node_id, psk_identity, psk_enc, psk_key_id, psk_hash)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (node_id) DO UPDATE SET managed = true`,
		n.NodeID, Identity(n.Hostname), s.Box.Seal([]byte(psk), pskAAD(n.NodeID)), s.Box.KeyID, s.pskHash(psk))
	return err
}

// Disable stopt het beheer; zabbix-agent2 blijft op de node staan.
func (s *Service) Disable(ctx context.Context, actor events.Actor, ids []uuid.UUID) error {
	for _, id := range ids {
		n, err := s.Node(ctx, id)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_nodes SET managed = false WHERE node_id = $1`, id); err != nil {
			return err
		}
		s.event(ctx, actor, &n, "zabbixagent.node_unmanaged", nil)
	}
	return nil
}

// Rotate geeft de node een nieuwe sleutel; toepassen zet hem op de node en
// in Zabbix.
func (s *Service) Rotate(ctx context.Context, actor events.Actor, id uuid.UUID) (Node, error) {
	if s.Box == nil {
		return Node{}, ErrNoMasterKey
	}
	n, err := s.Node(ctx, id)
	if err != nil {
		return Node{}, err
	}
	if !n.Enabled {
		return Node{}, &FieldError{"zet de Zabbix-agent eerst aan op " + n.Hostname}
	}
	if n.ApplyRunning {
		return Node{}, &FieldError{"op " + n.Hostname + " loopt al een Zabbix-agent-taak"}
	}
	psk := newPSK()
	if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_nodes SET psk_enc = $2, psk_key_id = $3, psk_hash = $4, psk_created_at = now(),
		apply_failed_at = NULL WHERE node_id = $1`, id, s.Box.Seal([]byte(psk), pskAAD(id)), s.Box.KeyID, s.pskHash(psk)); err != nil {
		return Node{}, err
	}
	s.event(ctx, actor, &n, "zabbixagent.psk_rotated", nil)
	return s.Node(ctx, id)
}

type applyParams struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
}

// Apply start de taak die zabbix-agent2 op de node zet en de host in
// Zabbix bijwerkt.
func (s *Service) Apply(ctx context.Context, actor events.Actor, id uuid.UUID) (uuid.UUID, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	switch {
	case !set.Ready():
		return uuid.Nil, &FieldError{"vul eerst Server, het adres van de Zabbix API en het API-token in"}
	case !n.Supported:
		return uuid.Nil, &FieldError{n.Hostname + ": agent te oud (Nodes → Agents bijwerken)"}
	case !n.Online:
		return uuid.Nil, &FieldError{n.Hostname + " is offline"}
	case n.ApplyRunning:
		return uuid.Nil, &FieldError{"op " + n.Hostname + " loopt al een Zabbix-agent-taak"}
	}
	if err := n.Config(set).Validate(); err != nil {
		return uuid.Nil, &FieldError{err.Error()}
	}
	if !n.Enabled || !n.Managed {
		if s.Box == nil {
			return uuid.Nil, ErrNoMasterKey
		}
		if err := s.enable(ctx, n); err != nil {
			return uuid.Nil, err
		}
	}
	j, err := s.runner.Enqueue(ctx, jobs.Spec{Kind: Kind, Title: "Zabbix-agent toepassen op " + n.Hostname, NodeID: &n.NodeID,
		ClusterID: n.ClusterID, Actor: actor, Params: applyParams{NodeID: n.NodeID, Hostname: n.Hostname}})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE zabbix_agent_nodes SET apply_job_id = $2 WHERE node_id = $1`, n.NodeID, j.ID); err != nil {
		return uuid.Nil, err
	}
	return j.ID, nil
}

// hostState is de state van de stap die de host in Zabbix opzoekt.
type hostState struct {
	HostID string `json:"host_id"`
}

func (s *Service) apply(ctx context.Context, j *jobs.Job) error {
	var p applyParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	err := s.applySteps(ctx, j, p)
	bg := context.WithoutCancel(ctx)
	n, nerr := s.Node(bg, p.NodeID)
	if nerr != nil {
		return errors.Join(err, nerr)
	}
	actor := events.System()
	if j.RequestedBy != nil {
		actor = events.User(*j.RequestedBy)
	}
	if err != nil {
		_, _ = s.pool.Exec(bg, `UPDATE zabbix_agent_nodes SET apply_error = $2, apply_failed_at = now() WHERE node_id = $1`, p.NodeID, err.Error())
		s.event(bg, actor, &n, "zabbixagent.apply_failed", map[string]any{"job_id": j.ID.String(), "error": err.Error()})
		return err
	}
	s.event(bg, actor, &n, "zabbixagent.applied", map[string]any{"job_id": j.ID.String(), "zabbix_host_id": n.ZabbixHostID})
	if s.Changed != nil {
		s.Changed()
	}
	return nil
}

func (s *Service) applySteps(ctx context.Context, j *jobs.Job, p applyParams) error {
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	n, err := s.Node(ctx, p.NodeID)
	if err != nil {
		return err
	}
	if !n.Enabled {
		return &FieldError{"de Zabbix-agent staat niet aan op " + n.Hostname}
	}
	cfg := n.Config(set)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if s.Box == nil {
		return ErrNoMasterKey
	}
	raw, err := s.Box.Open(n.pskEnc, pskAAD(n.NodeID), n.pskKey)
	if err != nil {
		return errors.New("de PSK van deze node is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); maak een nieuwe sleutel")
	}
	psk := string(raw)
	api, err := s.client(set)
	if err != nil {
		return err
	}
	// Eerst Zabbix: is de API bereikbaar en bestaat de host? Zo faalt de
	// taak voordat de node een sleutel heeft die Zabbix niet kent.
	var hs hostState
	looked := false
	if err := j.Step(ctx, "Zabbix: host opzoeken", func(ctx context.Context, st *jobs.Step) error {
		looked = true
		v, err := api.Version(ctx)
		if err != nil {
			return err
		}
		id, err := api.HostID(ctx, n.Hostname)
		if err != nil {
			return err
		}
		if id == "" {
			st.Logf("Zabbix %s: host %s bestaat nog niet; ClusterForge maakt hem aan na de installatie (groep %s).", v, n.Hostname, set.HostGroup)
			if n.IP == "" {
				return fmt.Errorf("%s heeft geen primair IP-adres (Nodes → Bewerken) voor de agent-interface in Zabbix", n.Hostname)
			}
		} else {
			st.Logf("Zabbix %s: host %s gevonden (hostid %s); de PSK wordt na de installatie bijgewerkt.", v, n.Hostname, id)
		}
		hs.HostID = id
		return st.SetState(ctx, hs)
	}); err != nil {
		return err
	}
	// Bij een herhaalde poging is de stap al gelukt: de state zegt de host.
	if !looked && !j.Done("Zabbix: host opzoeken", &hs) {
		return errors.New("stand van de host in Zabbix onbekend")
	}
	spec := protocol.RunSpec{ID: "zabbixagent-" + j.ID.String(), Kind: protocol.RunScript, Title: j.Title, TimeoutSeconds: 1800, Script: Script(cfg, psk)}
	if err := j.Step(ctx, n.Hostname+": zabbix-agent2", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("Server=%s ServerActive=%s Hostname=%s PSK-identiteit %s", cfg.Server, cfg.ServerActive, cfg.Hostname, cfg.PSKIdentity)
		res, err := s.Run.Do(ctx, st, n.NodeID, spec)
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
	hostID := hs.HostID
	if err := j.Step(ctx, "Zabbix: PSK op de host", func(ctx context.Context, st *jobs.Step) error {
		if hostID != "" {
			if err := api.SetPSK(ctx, hostID, cfg.PSKIdentity, psk); err != nil {
				return err
			}
			st.Logf("Host %s: PSK-identiteit %s, verbindingen alleen met PSK.", n.Hostname, cfg.PSKIdentity)
			return nil
		}
		tpls := slices.Clone(set.Templates)
		if n.Docker && set.DockerTemplate != "" {
			tpls = append(tpls, set.DockerTemplate)
		}
		id, err := api.CreateHost(ctx, NewHost{Name: n.Hostname, IP: n.IP, Group: set.HostGroup, Templates: tpls,
			Proxy: set.Proxy, Identity: cfg.PSKIdentity, PSK: psk})
		if err != nil {
			return err
		}
		hostID = id
		st.Logf("Host %s aangemaakt (hostid %s) in groep %s met %s.", n.Hostname, id, set.HostGroup, strings.Join(tpls, ", "))
		return nil
	}); err != nil {
		return err
	}
	_, err = s.pool.Exec(context.WithoutCancel(ctx), `UPDATE zabbix_agent_nodes SET applied_hash = $2, applied_at = now(), apply_error = '',
		apply_failed_at = NULL, zabbix_host_id = coalesce(nullif($3, ''), zabbix_host_id) WHERE node_id = $1`, n.NodeID, cfg.Hash(), hostID)
	return err
}

// Loop past elke PollEvery automatisch toe.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTicker(s.PollEvery)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("Zabbix-agent-ronde mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is één ronde: met automatisch toepassen krijgt elke node met een
// agent zabbix-agent2 (prod alleen als dat ook aan staat), en een node met
// gewijzigde instellingen of een nieuwe sleutel krijgt ze opnieuw.
func (s *Service) Tick(ctx context.Context) error {
	s.tick.Lock()
	defer s.tick.Unlock()
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if !set.AutoApply || !set.Ready() || s.Box == nil {
		return nil
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	for _, n := range nodes {
		if !n.Online || !n.Supported || (n.Prod() && !set.AutoProd) || n.ApplyRunning {
			continue
		}
		if n.Enabled && !n.Managed {
			continue
		}
		if n.ApplyFailedAt != nil && now.Sub(*n.ApplyFailedAt) < RetryFailed {
			continue
		}
		if n.Enabled && !n.Pending {
			continue
		}
		if !n.Enabled {
			if err := s.enable(ctx, n); err != nil {
				return err
			}
		}
		if _, err := s.Apply(ctx, events.System(), n.NodeID); err != nil {
			s.log.Warn("Zabbix-agent automatisch toepassen mislukt", "node", n.Hostname, "err", err)
		}
	}
	return nil
}
