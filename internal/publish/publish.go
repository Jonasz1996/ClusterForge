// Package publish is de publicatie van fase 35: ClusterForge weet welke
// diensten op welke node, poort en VIP draaien (ontdekking door de agents)
// en beheert Nginx Proxy Manager (proxy hosts, certificaten) en Authentik
// (proxy provider, applicatie, ingebouwde outpost) onder één hoofddomein.
//
// Alles loopt via de agent op een node van NPM of Authentik, die de API op
// 127.0.0.1 aanspreekt. ClusterForge schrijft nooit zelf in de database of
// de config van NPM: de API van NPM schrijft de database en maakt de config.
// Vóór elke wijziging maakt de agent een kopie, daarna volgen de controles
// (database, config, geen .err, nginx -t, het adres zelf); faalt er een, dan
// zet de taak de wijziging terug.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Bus praat met de agents.
type Bus interface {
	Web(ctx context.Context, nodeID uuid.UUID, req protocol.WebRequest) (protocol.WebResponse, error)
}

// SyncRunner start een beheerde cronjob meteen (de sync van NPM naar de
// andere nodes).
type SyncRunner interface {
	RunJob(ctx context.Context, actor events.Actor, jobID uuid.UUID) ([]string, error)
}

// ValidationError is een fout in een vraag.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error { return ValidationError{fmt.Sprintf(format, args...)} }

// ErrNotFound: geen host of dienst onder dat id.
var ErrNotFound = errors.New("niet gevonden")

const (
	// refreshEvery is hoe vaak ontdekking, NPM en Authentik gelezen worden.
	refreshEvery = 5 * time.Minute
	// callTimeout is hoe lang één aanroep via de agent mag duren.
	callTimeout = 60 * time.Second
)

// Service is de publicatie.
type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	log  *slog.Logger
	jobs *jobs.Runner
	bus  Bus
	Box  *secrets.Box
	// Sync start de sync NPM-configuratie meteen; nil slaat dat over.
	Sync SyncRunner

	mu       sync.Mutex
	npmToken npmToken
	kick     chan struct{}
	// Now is de klok; tests zetten hem vast.
	Now func() time.Time
	// SettleTime is hoe lang de taak na een wijziging wacht voor de
	// controle; nil is twee seconden. Tests zetten hem op nul.
	SettleTime *time.Duration
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, runner *jobs.Runner, bus Bus, box *secrets.Box) *Service {
	s := &Service{pool: pool, ev: ev, log: log, jobs: runner, bus: bus, Box: box, kick: make(chan struct{}, 1), Now: time.Now}
	if runner != nil {
		runner.Register(KindApply, s.runApply)
		runner.OnFinished(KindApply, func(context.Context, jobsDone) { s.Kick() })
	}
	return s
}

// Defaults zijn de standaarden voor een nieuwe host.
type Defaults struct {
	SSLForced     bool `json:"ssl_forced"`
	HTTP2         bool `json:"http2"`
	HSTS          bool `json:"hsts"`
	Websockets    bool `json:"websockets"`
	BlockExploits bool `json:"block_exploits"`
	// Authentik: nieuwe hosts standaard achter Authentik.
	Authentik bool `json:"authentik"`
	// AuthentikSkip zijn woorden in de naam, het image of de container van
	// diensten met een eigen login, die standaard niet achter Authentik gaan.
	AuthentikSkip []string `json:"authentik_skip"`
	// LetsEncrypt: zonder passend certificaat (wildcard of exact) vraagt
	// ClusterForge er een aan bij Let's Encrypt; anders alleen http.
	LetsEncrypt bool `json:"letsencrypt"`
	// LetsEncryptEmail is het e-mailadres voor Let's Encrypt (oudere NPM).
	LetsEncryptEmail string `json:"letsencrypt_email,omitempty"`
}

// DefaultDefaults zijn de standaarden die Jonas koos (17/11): SSL,
// websockets, exploits blokkeren, Authentik aan behalve voor apps met een
// eigen login.
func DefaultDefaults() Defaults {
	return Defaults{SSLForced: true, HTTP2: true, Websockets: true, BlockExploits: true, Authentik: true, LetsEncrypt: true,
		AuthentikSkip: []string{"vaultwarden", "vaultx", "homeassistant", "home-assistant", "plex", "nextcloud", "authentik", "proxmox", "opnsense", "jellyfin"}}
}

// Settings zijn de instellingen, zonder geheimen.
type Settings struct {
	Domain   string   `json:"domain"`
	Defaults Defaults `json:"defaults"`

	NPMClusterID *uuid.UUID `json:"npm_cluster_id,omitempty"`
	NPMNodeID    *uuid.UUID `json:"npm_node_id,omitempty"`
	NPMPort      int        `json:"npm_port"`
	NPMIdentity  string     `json:"npm_identity"`
	NPMHasSecret bool       `json:"npm_has_secret"`

	AuthentikClusterID *uuid.UUID `json:"authentik_cluster_id,omitempty"`
	AuthentikNodeID    *uuid.UUID `json:"authentik_node_id,omitempty"`
	AuthentikPort      int        `json:"authentik_port"`
	AuthentikHasToken  bool       `json:"authentik_has_token"`
	AuthentikOutpost   string     `json:"authentik_outpost"`

	UpdatedAt time.Time `json:"updated_at"`

	npmSecretEnc []byte
	npmKeyID     string
	akTokenEnc   []byte
	akKeyID      string
}

// NPMLinked zegt of NPM gekoppeld is.
func (s Settings) NPMLinked() bool {
	return (s.NPMClusterID != nil || s.NPMNodeID != nil) && s.NPMIdentity != "" && s.NPMHasSecret
}

// AuthentikLinked zegt of Authentik gekoppeld is.
func (s Settings) AuthentikLinked() bool {
	return (s.AuthentikClusterID != nil || s.AuthentikNodeID != nil) && s.AuthentikHasToken
}

var (
	npmAAD = []byte("publish:npm")
	akAAD  = []byte("publish:authentik")
)

// Settings leest de instellingen.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var st Settings
	var defaults []byte
	err := s.pool.QueryRow(ctx, `SELECT domain, defaults, npm_cluster_id, npm_node_id, npm_port, npm_identity, npm_secret_enc, npm_key_id,
		authentik_cluster_id, authentik_node_id, authentik_port, authentik_token_enc, authentik_key_id, authentik_outpost, updated_at
		FROM publish_settings`).Scan(&st.Domain, &defaults, &st.NPMClusterID, &st.NPMNodeID, &st.NPMPort, &st.NPMIdentity, &st.npmSecretEnc, &st.npmKeyID,
		&st.AuthentikClusterID, &st.AuthentikNodeID, &st.AuthentikPort, &st.akTokenEnc, &st.akKeyID, &st.AuthentikOutpost, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO publish_settings DEFAULT VALUES ON CONFLICT DO NOTHING`); err != nil {
			return Settings{}, err
		}
		return s.Settings(ctx)
	}
	if err != nil {
		return Settings{}, err
	}
	st.Defaults = DefaultDefaults()
	if len(defaults) > 2 {
		_ = json.Unmarshal(defaults, &st.Defaults)
	}
	st.NPMHasSecret = len(st.npmSecretEnc) > 0
	st.AuthentikHasToken = len(st.akTokenEnc) > 0
	return st, nil
}

// SettingsInput verandert de instellingen. Een leeg geheim laat het
// bestaande staan; Clear* haalt het weg.
type SettingsInput struct {
	Domain   string    `json:"domain"`
	Defaults *Defaults `json:"defaults,omitempty"`

	NPMClusterID *uuid.UUID `json:"npm_cluster_id,omitempty"`
	NPMNodeID    *uuid.UUID `json:"npm_node_id,omitempty"`
	NPMPort      int        `json:"npm_port,omitempty"`
	NPMIdentity  string     `json:"npm_identity"`
	NPMSecret    string     `json:"npm_secret,omitempty"`
	ClearNPM     bool       `json:"clear_npm,omitempty"`

	AuthentikClusterID *uuid.UUID `json:"authentik_cluster_id,omitempty"`
	AuthentikNodeID    *uuid.UUID `json:"authentik_node_id,omitempty"`
	AuthentikPort      int        `json:"authentik_port,omitempty"`
	AuthentikToken     string     `json:"authentik_token,omitempty"`
	AuthentikOutpost   string     `json:"authentik_outpost"`
	ClearAuthentik     bool       `json:"clear_authentik,omitempty"`
}

// SaveSettings bewaart de instellingen. Het hoofddomein veranderen hernoemt
// niets: dat doet DomainChange (M6) met een voorbeeld vooraf.
func (s *Service) SaveSettings(ctx context.Context, actor events.Actor, userID *uuid.UUID, in SettingsInput) (Settings, error) {
	cur, err := s.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	in.Domain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(in.Domain, ".")))
	if in.Domain == "" {
		in.Domain = cur.Domain
	}
	if !protocol.ValidDomain(in.Domain) || strings.HasPrefix(in.Domain, "*.") {
		return Settings{}, invalid("%q is geen geldig domein (zoals jbogaert.be)", in.Domain)
	}
	defaults := cur.Defaults
	if in.Defaults != nil {
		defaults = *in.Defaults
		for i, w := range defaults.AuthentikSkip {
			defaults.AuthentikSkip[i] = strings.ToLower(strings.TrimSpace(w))
		}
		defaults.AuthentikSkip = compact(defaults.AuthentikSkip)
		if e := strings.TrimSpace(defaults.LetsEncryptEmail); e != "" && (!strings.Contains(e, "@") || strings.ContainsAny(e, " \r\n")) {
			return Settings{}, invalid("%q is geen e-mailadres", e)
		}
	}
	if in.NPMPort == 0 {
		in.NPMPort = cur.NPMPort
	}
	if in.AuthentikPort == 0 {
		in.AuthentikPort = cur.AuthentikPort
	}
	if in.NPMPort < 1 || in.NPMPort > 65535 || in.AuthentikPort < 1 || in.AuthentikPort > 65535 {
		return Settings{}, invalid("ongeldige poort")
	}
	in.NPMIdentity = strings.TrimSpace(in.NPMIdentity)
	in.AuthentikOutpost = strings.TrimRight(strings.TrimSpace(in.AuthentikOutpost), "/")
	if in.AuthentikOutpost != "" && !validOutpost(in.AuthentikOutpost) {
		return Settings{}, invalid("het adres van de outpost moet zijn als http://192.168.103.244:9000")
	}
	if in.NPMClusterID != nil && in.NPMNodeID != nil {
		in.NPMNodeID = nil
	}
	if in.AuthentikClusterID != nil && in.AuthentikNodeID != nil {
		in.AuthentikNodeID = nil
	}
	npmEnc, npmKey := cur.npmSecretEnc, cur.npmKeyID
	akEnc, akKey := cur.akTokenEnc, cur.akKeyID
	if (in.NPMSecret != "" || in.AuthentikToken != "") && s.Box == nil {
		return Settings{}, invalid("de server heeft geen masterkey (CF_MASTER_KEY), dus hij kan geen wachtwoord of token bewaren")
	}
	if in.NPMSecret != "" {
		npmEnc, npmKey = s.Box.Seal([]byte(in.NPMSecret), npmAAD), s.Box.KeyID
	}
	if in.ClearNPM {
		npmEnc, npmKey = nil, ""
	}
	if t := strings.TrimSpace(in.AuthentikToken); t != "" {
		if strings.ContainsAny(t, " \r\n") || len(t) > 512 {
			return Settings{}, invalid("dat lijkt geen API-token van Authentik")
		}
		akEnc, akKey = s.Box.Seal([]byte(t), akAAD), s.Box.KeyID
	}
	if in.ClearAuthentik {
		akEnc, akKey = nil, ""
	}
	defJSON, _ := json.Marshal(defaults)
	if _, err := s.pool.Exec(ctx, `UPDATE publish_settings SET domain = $1, defaults = $2,
		npm_cluster_id = $3, npm_node_id = $4, npm_port = $5, npm_identity = $6, npm_secret_enc = $7, npm_key_id = $8,
		authentik_cluster_id = $9, authentik_node_id = $10, authentik_port = $11, authentik_token_enc = $12, authentik_key_id = $13,
		authentik_outpost = $14, updated_at = now(), updated_by = $15`,
		in.Domain, defJSON, in.NPMClusterID, in.NPMNodeID, in.NPMPort, in.NPMIdentity, npmEnc, npmKey,
		in.AuthentikClusterID, in.AuthentikNodeID, in.AuthentikPort, akEnc, akKey, in.AuthentikOutpost, userID); err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.npmToken = npmToken{}
	s.mu.Unlock()
	s.event(ctx, actor, "publish.settings_changed", map[string]any{
		"domain": in.Domain, "npm_secret_changed": in.NPMSecret != "" || in.ClearNPM, "authentik_token_changed": in.AuthentikToken != "" || in.ClearAuthentik,
	})
	s.Kick()
	return s.Settings(ctx)
}

func validOutpost(u string) bool {
	rest, ok := strings.CutPrefix(u, "http://")
	if !ok {
		rest, ok = strings.CutPrefix(u, "https://")
	}
	if !ok || rest == "" || strings.ContainsAny(rest, " /;{}\"'\\$\r\n") {
		return false
	}
	return true
}

func compact(in []string) []string {
	out := in[:0]
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) npmSecret(st Settings) (string, error) {
	if len(st.npmSecretEnc) == 0 {
		return "", invalid("NPM is niet gekoppeld: vul in Instellingen → Domeinen en publicatie de NPM-gebruiker en het wachtwoord in")
	}
	if s.Box == nil {
		return "", errors.New("het wachtwoord van NPM is niet te lezen zonder masterkey")
	}
	b, err := s.Box.Open(st.npmSecretEnc, npmAAD, st.npmKeyID)
	if err != nil {
		return "", errors.New("het wachtwoord van NPM is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul het opnieuw in")
	}
	return string(b), nil
}

func (s *Service) authentikToken(st Settings) (string, error) {
	if len(st.akTokenEnc) == 0 {
		return "", invalid("Authentik is niet gekoppeld: vul in Instellingen → Domeinen en publicatie een API-token in")
	}
	if s.Box == nil {
		return "", errors.New("het token van Authentik is niet te lezen zonder masterkey")
	}
	b, err := s.Box.Open(st.akTokenEnc, akAAD, st.akKeyID)
	if err != nil {
		return "", errors.New("het token van Authentik is niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul het opnieuw in")
	}
	return string(b), nil
}

func (s *Service) event(ctx context.Context, actor events.Actor, action string, payload map[string]any) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(ctx, nil, events.Event{Actor: actor, SubjectType: "publish", SubjectID: "settings", Action: action, Payload: payload}); err != nil {
		s.log.Warn("event schrijven mislukt", "action", action, "err", err)
	}
}

// Kick ververst meteen.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Loop leest elke vijf minuten de diensten, NPM en Authentik.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		s.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Refresh leest alles één keer.
func (s *Service) Refresh(ctx context.Context) {
	s.refreshServices(ctx)
	st, err := s.Settings(ctx)
	if err != nil {
		s.log.Warn("publicatie: instellingen lezen", "err", err)
		return
	}
	if st.NPMLinked() {
		data, err := s.readNPM(ctx, st)
		s.saveRead(ctx, "npm", data, err)
	}
	if st.AuthentikLinked() {
		data, err := s.readAuthentik(ctx, st)
		s.saveRead(ctx, "authentik", data, err)
	}
}

func (s *Service) saveRead(ctx context.Context, kind string, data any, rerr error) {
	msg := ""
	if rerr != nil {
		msg = rerr.Error()
		if _, err := s.pool.Exec(ctx, `INSERT INTO publish_reads (kind, error, read_at) VALUES ($1, $2, now())
			ON CONFLICT (kind) DO UPDATE SET error = EXCLUDED.error, read_at = now()`, kind, msg); err != nil {
			s.log.Warn("publicatie bewaren", "kind", kind, "err", err)
		}
		return
	}
	b, _ := json.Marshal(data)
	if _, err := s.pool.Exec(ctx, `INSERT INTO publish_reads (kind, data, error, read_at) VALUES ($1, $2, '', now())
		ON CONFLICT (kind) DO UPDATE SET data = EXCLUDED.data, error = '', read_at = now()`, kind, b); err != nil {
		s.log.Warn("publicatie bewaren", "kind", kind, "err", err)
	}
}

// read leest wat de laatste ronde in NPM of Authentik las.
func (s *Service) read(ctx context.Context, kind string, v any) (time.Time, string, error) {
	var data []byte
	var msg string
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT data, error, read_at FROM publish_reads WHERE kind = $1`, kind).Scan(&data, &msg, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, "", nil
	}
	if err != nil {
		return time.Time{}, "", err
	}
	if len(data) > 2 {
		_ = json.Unmarshal(data, v)
	}
	return at, msg, nil
}
