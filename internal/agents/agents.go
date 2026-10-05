// Package agents regelt enrollmenttokens, het aanmelden van agents en het
// intrekken ervan.
package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nkeys"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	ErrInvalidToken = errors.New("ongeldig, verlopen of opgebruikt enrollmenttoken")
	ErrNotFound     = errors.New("niet gevonden")
)

type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

type ConflictError struct{ Msg string }

func (e ConflictError) Error() string { return e.Msg }

// Disconnecter verbreekt open NATS-verbindingen van een agent.
type Disconnecter interface {
	Disconnect(nkeyPublic string)
}

type Service struct {
	pool *pgxpool.Pool
	ev   *events.Writer
	bus  Disconnecter
}

func NewService(pool *pgxpool.Pool, ev *events.Writer, bus Disconnecter) *Service {
	return &Service{pool: pool, ev: ev, bus: bus}
}

const tokenPrefix = "cfe_"

var (
	hostnameRe  = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)
	machineIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return tokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

type TokenInput struct {
	Description string
	NodeID      *uuid.UUID
	ClusterID   *uuid.UUID
	MaxUses     int
	TTL         time.Duration
}

// CreateToken maakt een enrollmenttoken. Het token zelf wordt alleen hier
// teruggegeven; de database bewaart de hash.
func (s *Service) CreateToken(ctx context.Context, by uuid.UUID, in TokenInput) (string, store.EnrollmentToken, error) {
	in.Description = strings.TrimSpace(in.Description)
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	if in.TTL == 0 {
		in.TTL = 24 * time.Hour
	}
	switch {
	case len(in.Description) > 200:
		return "", store.EnrollmentToken{}, ValidationError{"beschrijving is hoogstens 200 tekens"}
	case in.MaxUses < 1 || in.MaxUses > 100:
		return "", store.EnrollmentToken{}, ValidationError{"aantal keer bruikbaar moet tussen 1 en 100 liggen"}
	case in.TTL < time.Hour || in.TTL > 30*24*time.Hour:
		return "", store.EnrollmentToken{}, ValidationError{"geldigheid moet tussen 1 uur en 30 dagen liggen"}
	case in.NodeID != nil && in.MaxUses != 1:
		return "", store.EnrollmentToken{}, ValidationError{"een token voor één node is maar één keer bruikbaar"}
	}
	token, err := newToken()
	if err != nil {
		return "", store.EnrollmentToken{}, err
	}
	var t store.EnrollmentToken
	err = s.tx(ctx, func(q *store.Queries) error {
		t, err = q.CreateEnrollmentToken(ctx, store.CreateEnrollmentTokenParams{
			TokenHash: hashToken(token), Description: in.Description, NodeID: in.NodeID, ClusterID: in.ClusterID,
			MaxUses: int32(in.MaxUses), ExpiresAt: time.Now().Add(in.TTL), CreatedBy: &by,
		})
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.User(by), SubjectType: "enrollment_token", SubjectID: t.ID.String(), ClusterID: in.ClusterID,
			Action: "enrollment_token.created",
			Payload: map[string]any{
				"description": t.Description, "node_id": t.NodeID, "max_uses": t.MaxUses, "expires_at": t.ExpiresAt,
			},
		})
	})
	return token, t, err
}

func (s *Service) DeleteToken(ctx context.Context, by uuid.UUID, id uuid.UUID) error {
	return s.tx(ctx, func(q *store.Queries) error {
		n, err := q.DeleteEnrollmentToken(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.User(by), SubjectType: "enrollment_token", SubjectID: id.String(),
			Action: "enrollment_token.deleted",
		})
	})
}

// Enroll meldt een agent aan. De node is de node van het token, anders een
// bestaande node met dezelfde hostname, anders een nieuwe. Een eerdere agent
// van die node wordt ingetrokken.
func (s *Service) Enroll(ctx context.Context, req protocol.EnrollRequest) (store.Agent, error) {
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.MachineID = strings.ToLower(strings.TrimSpace(req.MachineID))
	switch {
	case !strings.HasPrefix(req.Token, tokenPrefix):
		return store.Agent{}, ErrInvalidToken
	case !nkeys.IsValidPublicUserKey(req.NkeyPublic):
		return store.Agent{}, ValidationError{"ongeldige publieke sleutel"}
	case !hostnameRe.MatchString(req.Hostname):
		return store.Agent{}, ValidationError{"ongeldige hostname"}
	case req.MachineID != "" && !machineIDRe.MatchString(req.MachineID):
		return store.Agent{}, ValidationError{"ongeldige machine-id"}
	case len(req.AgentVersion) > 64:
		return store.Agent{}, ValidationError{"agentversie te lang"}
	}
	agentID := uuid.New()
	actor := events.Actor{Type: store.ActorTypeAgent, ID: agentID.String()}
	var agent store.Agent
	var revoked []store.Agent
	err := s.tx(ctx, func(q *store.Queries) error {
		tok, err := q.LockEnrollmentTokenByHash(ctx, hashToken(req.Token))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if time.Now().After(tok.ExpiresAt) || tok.Uses >= tok.MaxUses {
			return ErrInvalidToken
		}

		var node store.Node
		created := false
		if tok.NodeID != nil {
			node, err = q.LockNode(ctx, *tok.NodeID)
		} else {
			node, err = q.FindNodeByHostname(ctx, req.Hostname)
			if errors.Is(err, pgx.ErrNoRows) {
				node, err = q.CreateNode(ctx, store.CreateNodeParams{
					ClusterID: tok.ClusterID, Hostname: req.Hostname, Lifecycle: store.NodeLifecycleActive, Tags: []string{},
				})
				created = true
			}
		}
		if err != nil {
			return err
		}

		// Een token zonder vaste node mag een node niet overnemen van een
		// andere machine met dezelfde hostname.
		if cur, err := q.GetActiveAgentByNode(ctx, node.ID); err == nil {
			if tok.NodeID == nil && cur.MachineID != "" && req.MachineID != "" && cur.MachineID != req.MachineID {
				return ConflictError{"de node " + node.Hostname + " hoort al bij een andere machine; maak een token voor die node als je hem wilt vervangen"}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revoked, err = q.RevokeAgentsOfNode(ctx, node.ID); err != nil {
			return err
		}
		agent, err = q.CreateAgent(ctx, store.CreateAgentParams{
			ID: agentID, NodeID: node.ID, NkeyPublic: req.NkeyPublic, MachineID: req.MachineID,
			Version: req.AgentVersion, ProtocolVersion: int32(req.ProtocolVersion),
		})
		if err != nil {
			return err
		}
		if err := q.UseEnrollmentToken(ctx, tok.ID); err != nil {
			return err
		}
		if created {
			err := s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "node", SubjectID: node.ID.String(), ClusterID: node.ClusterID,
				Action: "node.created", Payload: map[string]any{"hostname": node.Hostname, "via": "enrollment"},
			})
			if err != nil {
				return err
			}
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "agent", SubjectID: agentID.String(), ClusterID: node.ClusterID,
			Action: "agent.enrolled",
			Payload: map[string]any{
				"hostname": node.Hostname, "node_id": node.ID, "version": req.AgentVersion,
				"token_id": tok.ID, "replaced": len(revoked),
			},
		})
	})
	if err != nil {
		return store.Agent{}, err
	}
	for _, a := range revoked {
		s.bus.Disconnect(a.NkeyPublic)
	}
	return agent, nil
}

// Revoke trekt een agent in en verbreekt zijn verbinding.
func (s *Service) Revoke(ctx context.Context, by uuid.UUID, id uuid.UUID) error {
	var a store.Agent
	err := s.tx(ctx, func(q *store.Queries) error {
		var err error
		a, err = q.RevokeAgent(ctx, id)
		if err != nil {
			return err
		}
		node, err := q.GetNode(ctx, a.NodeID)
		if err != nil {
			return err
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: events.User(by), SubjectType: "agent", SubjectID: id.String(), ClusterID: node.Node.ClusterID,
			Action: "agent.revoked", Payload: map[string]any{"hostname": node.Node.Hostname, "node_id": a.NodeID},
		})
	})
	if err != nil {
		return err
	}
	s.bus.Disconnect(a.NkeyPublic)
	return nil
}

func (s *Service) tx(ctx context.Context, fn func(q *store.Queries) error) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(store.New(tx)) })
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "agents_nkey_public_key" {
		return ConflictError{"deze sleutel is al aangemeld"}
	}
	if errors.As(err, &pe) && pe.Code == "23503" && pe.ConstraintName == "enrollment_tokens_node_id_fkey" {
		return ValidationError{"node bestaat niet"}
	}
	if errors.As(err, &pe) && pe.Code == "23503" && pe.ConstraintName == "enrollment_tokens_cluster_id_fkey" {
		return ValidationError{"cluster bestaat niet"}
	}
	return err
}
