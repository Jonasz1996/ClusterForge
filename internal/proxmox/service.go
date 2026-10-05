package proxmox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

var (
	ErrNotFound = errors.New("niet gevonden")
	// ErrNoMasterKey betekent dat de server geen sleutel heeft om het token
	// te versleutelen.
	ErrNoMasterKey = errors.New("de server heeft geen masterkey; zet CF_MASTER_KEY om Proxmox te koppelen")
)

// ValidationError is een fout in de invoer of in de verbinding die de
// gebruiker kan oplossen.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// UpstreamError is een fout van Proxmox zelf bij een rechtstreekse vraag.
type UpstreamError struct{ Err error }

func (e *UpstreamError) Error() string { return e.Err.Error() }
func (e *UpstreamError) Unwrap() error { return e.Err }

// ConflictError betekent dat de vraag botst met de huidige toestand, zoals
// een VM starten die al draait.
type ConflictError struct{ Msg string }

func (e ConflictError) Error() string { return e.Msg }

// Fields zijn de velden van een verbinding die een gebruiker beheert.
type Fields struct {
	Name    string `json:"name"`
	URL     string `json:"api_url"`
	TokenID string `json:"token_id"`
	// TokenSecret is bij wijzigen leeg als het niet verandert.
	TokenSecret string `json:"-"`
	Fingerprint string `json:"tls_fingerprint"`
}

var tokenIDPattern = regexp.MustCompile(`^[^\s@!]+@[^\s@!]+![A-Za-z][A-Za-z0-9._-]*$`)

func (f *Fields) normalize() error {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len(f.Name) > 100 {
		return ValidationError{"naam is verplicht en hoogstens 100 tekens"}
	}
	u, err := NormalizeURL(f.URL)
	if err != nil {
		return ValidationError{err.Error()}
	}
	f.URL = u
	f.TokenID = strings.TrimSpace(f.TokenID)
	if !tokenIDPattern.MatchString(f.TokenID) {
		return ValidationError{"token-id moet de vorm gebruiker@realm!naam hebben, zoals clusterforge@pve!cf"}
	}
	f.TokenSecret = strings.TrimSpace(f.TokenSecret)
	if len(f.TokenSecret) > 200 {
		return ValidationError{"token-secret is te lang"}
	}
	if strings.TrimSpace(f.Fingerprint) != "" {
		fp := NormalizeFingerprint(f.Fingerprint)
		if fp == "" {
			return ValidationError{"vingerafdruk moet een SHA-256 zijn (64 hex-tekens, met of zonder dubbele punten)"}
		}
		f.Fingerprint = fp
	} else {
		f.Fingerprint = ""
	}
	return nil
}

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	ev   *events.Writer
	log  *slog.Logger
	box  *secrets.Box
	jobs *jobs.Runner

	// Interval is de tijd tussen twee syncs.
	Interval time.Duration
	// TaskPoll is hoe vaak een taak de status van zijn Proxmox-taak opvraagt.
	TaskPoll time.Duration
	// Changed wordt aangeroepen als een gekoppelde VM van toestand verandert,
	// zodat de statusregels meteen opnieuw rekenen. Mag nil zijn.
	Changed func()
	// NewAPI maakt een client; tests vervangen hem.
	NewAPI func(Config) (API, error)

	mu      sync.Mutex
	clients map[uuid.UUID]cachedClient
	locks   map[uuid.UUID]*sync.Mutex
}

type cachedClient struct {
	updatedAt time.Time
	api       API
}

// NewService maakt de Proxmox-dienst. box mag nil zijn: dan kan er niets
// gekoppeld worden, maar blijft de rest werken.
func NewService(pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, box *secrets.Box, runner *jobs.Runner) *Service {
	s := &Service{
		pool: pool, q: store.New(pool), ev: ev, log: log, box: box, jobs: runner,
		Interval: 20 * time.Second, TaskPoll: 2 * time.Second,
		NewAPI:  func(c Config) (API, error) { return NewClient(c) },
		clients: map[uuid.UUID]cachedClient{}, locks: map[uuid.UUID]*sync.Mutex{},
	}
	if runner != nil {
		runner.Register(KindVMAction, s.runVMAction)
	}
	return s
}

// Enabled is false zonder masterkey.
func (s *Service) Enabled() bool { return s.box != nil }

func aad(id uuid.UUID) []byte { return []byte("proxmox:" + id.String()) }

// test controleert of Proxmox met deze gegevens antwoordt.
func (s *Service) test(ctx context.Context, f Fields) (Version, error) {
	api, err := s.NewAPI(Config{URL: f.URL, TokenID: f.TokenID, TokenSecret: f.TokenSecret, Fingerprint: f.Fingerprint, Timeout: 15 * time.Second})
	if err != nil {
		return Version{}, ValidationError{err.Error()}
	}
	if c, ok := api.(*Client); ok {
		defer c.CloseIdle()
	}
	v, err := api.Version(ctx)
	if err != nil {
		var fe *FingerprintError
		if errors.As(err, &fe) {
			return v, ValidationError{err.Error() + ". Klopt die met Proxmox, vul hem dan in."}
		}
		if strings.Contains(err.Error(), "certificate") {
			return v, ValidationError{"het certificaat van Proxmox is niet vertrouwd; haal de vingerafdruk op en vul hem in"}
		}
		return v, ValidationError{"Proxmox antwoordt niet zoals verwacht: " + err.Error()}
	}
	return v, nil
}

func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "proxmox_connections_name_key" {
		return ConflictError{"er is al een Proxmox-koppeling met deze naam"}
	}
	return err
}

func (s *Service) Create(ctx context.Context, actor events.Actor, f Fields) (store.ProxmoxConnection, error) {
	if s.box == nil {
		return store.ProxmoxConnection{}, ErrNoMasterKey
	}
	if err := f.normalize(); err != nil {
		return store.ProxmoxConnection{}, err
	}
	if f.TokenSecret == "" {
		return store.ProxmoxConnection{}, ValidationError{"token-secret is verplicht"}
	}
	v, err := s.test(ctx, f)
	if err != nil {
		return store.ProxmoxConnection{}, err
	}
	id := uuid.New()
	var c store.ProxmoxConnection
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		c, err = q.CreateProxmoxConnection(ctx, store.CreateProxmoxConnectionParams{
			ID: id, Name: f.Name, ApiUrl: f.URL, TokenID: f.TokenID,
			TokenSecretEnc: s.box.Seal([]byte(f.TokenSecret), aad(id)), KeyID: s.box.KeyID,
			TlsFingerprint: f.Fingerprint, PveVersion: v.Version,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "proxmox", SubjectID: id.String(), Action: "proxmox.created",
			Payload: map[string]any{"name": f.Name, "api_url": f.URL, "token_id": f.TokenID},
		})
	})
	if err != nil {
		return c, translate(err)
	}
	// De eerste sync meteen, zodat de lijst niet leeg blijft tot de volgende ronde.
	_ = s.Sync(ctx, id)
	return s.q.GetProxmoxConnection(ctx, id)
}

// Update wijzigt een verbinding; als het adres of het token verandert, test
// de server eerst de nieuwe gegevens.
func (s *Service) Update(ctx context.Context, actor events.Actor, id uuid.UUID, change func(*Fields)) (store.ProxmoxConnection, error) {
	if s.box == nil {
		return store.ProxmoxConnection{}, ErrNoMasterKey
	}
	var c store.ProxmoxConnection
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockProxmoxConnection(ctx, id)
		if err != nil {
			return err
		}
		before := Fields{Name: cur.Name, URL: cur.ApiUrl, TokenID: cur.TokenID, Fingerprint: cur.TlsFingerprint}
		after := before
		change(&after)
		if err := after.normalize(); err != nil {
			return err
		}
		d := map[string]any{}
		for k, v := range map[string][2]string{
			"name": {before.Name, after.Name}, "api_url": {before.URL, after.URL},
			"token_id": {before.TokenID, after.TokenID}, "tls_fingerprint": {before.Fingerprint, after.Fingerprint},
		} {
			if v[0] != v[1] {
				d[k] = map[string]any{"from": v[0], "to": v[1]}
			}
		}
		secretEnc, keyID := cur.TokenSecretEnc, cur.KeyID
		if after.TokenSecret != "" {
			d["token_secret"] = "gewijzigd"
			secretEnc, keyID = s.box.Seal([]byte(after.TokenSecret), aad(id)), s.box.KeyID
		}
		c = cur
		if len(d) == 0 {
			return nil
		}
		if d["api_url"] != nil || d["token_id"] != nil || d["tls_fingerprint"] != nil || d["token_secret"] != nil {
			if after.TokenSecret == "" {
				secret, err := s.box.Open(cur.TokenSecretEnc, aad(id), cur.KeyID)
				if err != nil {
					return ValidationError{"het bewaarde token-secret is niet te ontsleutelen (" + err.Error() + "); vul het opnieuw in"}
				}
				after.TokenSecret = string(secret)
			}
			if _, err := s.test(ctx, after); err != nil {
				return err
			}
		}
		c, err = q.UpdateProxmoxConnection(ctx, store.UpdateProxmoxConnectionParams{
			ID: id, Name: after.Name, ApiUrl: after.URL, TokenID: after.TokenID, TokenSecretEnc: secretEnc,
			KeyID: keyID, TlsFingerprint: after.Fingerprint,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "proxmox", SubjectID: id.String(), Action: "proxmox.updated", Payload: d,
		})
	})
	if err != nil {
		return c, translate(err)
	}
	s.dropClient(id)
	return c, nil
}

// Delete verwijdert een verbinding; gekoppelde nodes blijven bestaan.
func (s *Service) Delete(ctx context.Context, actor events.Actor, id uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockProxmoxConnection(ctx, id)
		if err != nil {
			return err
		}
		if err := q.UnlinkProxmoxNodes(ctx, &id); err != nil {
			return err
		}
		if _, err := q.DeleteProxmoxConnection(ctx, id); err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "proxmox", SubjectID: id.String(), Action: "proxmox.deleted",
			Payload: map[string]any{"name": cur.Name},
		})
	})
	if err != nil {
		return translate(err)
	}
	s.dropClient(id)
	return nil
}

// api geeft een client voor een verbinding; die blijft bewaard zolang de
// verbinding niet verandert.
func (s *Service) api(c store.ProxmoxConnection) (API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cc, ok := s.clients[c.ID]; ok && cc.updatedAt.Equal(c.UpdatedAt) {
		return cc.api, nil
	}
	if s.box == nil {
		return nil, ErrNoMasterKey
	}
	secret, err := s.box.Open(c.TokenSecretEnc, aad(c.ID), c.KeyID)
	if err != nil {
		return nil, fmt.Errorf("het token-secret is niet te ontsleutelen (%w); is CF_MASTER_KEY veranderd? Vul het secret opnieuw in via Bewerken", err)
	}
	api, err := s.NewAPI(Config{URL: c.ApiUrl, TokenID: c.TokenID, TokenSecret: string(secret), Fingerprint: c.TlsFingerprint})
	if err != nil {
		return nil, err
	}
	if old, ok := s.clients[c.ID]; ok {
		if cl, ok := old.api.(*Client); ok {
			cl.CloseIdle()
		}
	}
	s.clients[c.ID] = cachedClient{updatedAt: c.UpdatedAt, api: api}
	return api, nil
}

func (s *Service) apiByID(ctx context.Context, id uuid.UUID) (API, store.ProxmoxConnection, error) {
	c, err := s.q.GetProxmoxConnection(ctx, id)
	if err != nil {
		return nil, c, translate(err)
	}
	api, err := s.api(c)
	return api, c, err
}

// APIFor geeft de client en de gegevens van een verbinding, voor andere
// diensten die Proxmox lezen.
func (s *Service) APIFor(ctx context.Context, id uuid.UUID) (API, store.ProxmoxConnection, error) {
	return s.apiByID(ctx, id)
}

func (s *Service) dropClient(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cc, ok := s.clients[id]; ok {
		if cl, ok := cc.api.(*Client); ok {
			cl.CloseIdle()
		}
		delete(s.clients, id)
	}
}

func (s *Service) lock(id uuid.UUID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

// Snapshots haalt de snapshots van een VM rechtstreeks uit Proxmox.
func (s *Service) Snapshots(ctx context.Context, connID uuid.UUID, vmid int) ([]Snapshot, error) {
	api, _, err := s.apiByID(ctx, connID)
	if err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, ErrNoMasterKey
	}
	g, err := s.q.GetProxmoxGuest(ctx, store.GetProxmoxGuestParams{ConnectionID: connID, Vmid: int32p(vmid)})
	if err != nil {
		return nil, translate(err)
	}
	snaps, err := api.Snapshots(ctx, Guest{Type: g.Type, Node: g.PveNode, VMID: vmid})
	if err != nil {
		return nil, &UpstreamError{err}
	}
	return snaps, nil
}

func int32p(n int) *int32 {
	v := int32(n)
	return &v
}
