// Package databases leest de MariaDB- en PostgreSQL-servers op de nodes via
// de agent (agentprotocol 9). Elke minuut vraagt het elke node met een
// databaseserver naar zijn status, en bewaart die in db_servers en
// db_databases. Het overzicht groepeert de servers per cluster en engine,
// zoals een Galera-cluster of een primary met zijn replica's.
package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Requester stuurt een databaseverzoek naar de agent van een node; de bus
// doet dat over NATS.
type Requester interface {
	DB(ctx context.Context, nodeID uuid.UUID, req protocol.DBRequest) (protocol.DBResponse, error)
}

// Soorten fout in db_servers.error_kind.
const (
	// ErrOldAgent: de agent kent agentprotocol 9 nog niet.
	ErrOldAgent = "old_agent"
	// ErrOffline: de agent is niet verbonden.
	ErrOffline = "offline"
	// ErrNoAnswer: de agent antwoordde niet op tijd.
	ErrNoAnswer = "no_answer"
	// ErrAgent: de agent antwoordde met een fout.
	ErrAgent = "agent"
)

// parallel is hoeveel nodes de sync tegelijk vraagt.
const parallel = 8

// Service leest de databases.
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	bus  Requester
	log  *slog.Logger
	// Interval is hoe vaak Run alles leest; nul of minder zet het uit.
	Interval time.Duration
	// Timeout is hoe lang een node mag doen over zijn antwoord.
	Timeout time.Duration
	Now     func() time.Time
}

func NewService(pool *pgxpool.Pool, bus Requester, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), bus: bus, log: log, Interval: time.Minute, Timeout: protocol.DBTimeout, Now: time.Now}
}

// Run leest de databases elke Interval, tot ctx afloopt.
func (s *Service) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.SyncAll(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("databases lezen mislukt", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncAll vraagt elke node met een databaseserver naar zijn status. Een
// node die niet antwoordt, krijgt de fout in db_servers; de andere gaan
// door.
func (s *Service) SyncAll(ctx context.Context) error {
	nodes, err := s.q.ListDBCandidateNodes(ctx)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, n := range nodes {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := s.syncNode(ctx, n); err != nil && ctx.Err() == nil {
				s.log.Warn("databases van een node lezen mislukt", "node", n.Hostname, "err", err)
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

// expected zijn de engines die een node volgens zijn facts of de vorige
// sync heeft.
func expected(n store.ListDBCandidateNodesRow) []string {
	set := slices.Clone(n.KnownEngines)
	for _, u := range n.DbUnits {
		switch {
		case u == "mariadb" || u == "mysql":
			set = append(set, protocol.DBMariaDB)
		case u == "postgresql" || u == "patroni" || strings.HasPrefix(u, "postgresql@"):
			set = append(set, protocol.DBPostgreSQL)
		}
	}
	slices.Sort(set)
	return slices.Compact(set)
}

func (s *Service) syncNode(ctx context.Context, n store.ListDBCandidateNodesRow) error {
	now := s.Now()
	mark := func(kind, msg string) error {
		for _, engine := range expected(n) {
			if err := s.q.MarkDBServer(ctx, store.MarkDBServerParams{NodeID: n.ID, Engine: engine, Error: msg, ErrorKind: kind, SyncedAt: now}); err != nil {
				return err
			}
		}
		return nil
	}
	switch {
	case n.AgentProtocol < protocol.DBSince:
		return mark(ErrOldAgent, fmt.Sprintf("de agent op %s heeft protocol %d; databases lezen vraagt protocol %d", n.Hostname, n.AgentProtocol, protocol.DBSince))
	case n.HeartbeatAt == nil || now.Sub(*n.HeartbeatAt) > status.HeartbeatLate:
		return mark(ErrOffline, "de agent op "+n.Hostname+" is niet verbonden")
	}
	rctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	res, err := s.bus.DB(rctx, n.ID, protocol.DBRequest{Op: protocol.DBStatus})
	switch {
	case errors.Is(err, agentbus.ErrAgentOffline):
		return mark(ErrOffline, "de agent op "+n.Hostname+" is niet verbonden")
	case errors.Is(err, agentbus.ErrNoAnswer):
		return mark(ErrNoAnswer, "de agent op "+n.Hostname+" antwoordde niet op tijd")
	case err != nil:
		return mark(ErrAgent, err.Error())
	case res.Error != "":
		return mark(ErrAgent, res.Error)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		engines := []string{}
		for _, srv := range res.Servers {
			if srv.Engine != protocol.DBMariaDB && srv.Engine != protocol.DBPostgreSQL {
				continue
			}
			engines = append(engines, srv.Engine)
			dbs := srv.Databases
			srv.Databases = nil
			st, err := json.Marshal(srv)
			if err != nil {
				return err
			}
			if err := q.UpsertDBServer(ctx, store.UpsertDBServerParams{
				NodeID: n.ID, Engine: srv.Engine, Unit: srv.Unit, Active: srv.Active, Running: srv.Running, Version: srv.Version,
				Role: srv.Role, Status: st, DatabasesTotal: int32(srv.DatabasesTotal), Error: srv.Error, SyncedAt: now,
			}); err != nil {
				return err
			}
			if err := q.DeleteDBDatabases(ctx, store.DeleteDBDatabasesParams{NodeID: n.ID, Engine: srv.Engine}); err != nil {
				return err
			}
			// Een server die niet antwoordt, houdt geen lijst; die zou
			// verouderd zijn.
			for _, d := range dbs {
				if err := q.InsertDBDatabase(ctx, store.InsertDBDatabaseParams{
					NodeID: n.ID, Engine: srv.Engine, Name: d.Name, SizeBytes: d.SizeBytes, Tables: int32(d.Tables),
				}); err != nil {
					return err
				}
			}
		}
		return q.DeleteDBServersExcept(ctx, store.DeleteDBServersExceptParams{NodeID: n.ID, Engines: engines})
	})
}
