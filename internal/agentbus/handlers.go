package agentbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func (b *Bus) subscribe() error {
	if _, err := b.nc.Subscribe("cf.node.*."+protocol.SubjectHeartbeat, b.onHeartbeat); err != nil {
		return err
	}
	if _, err := b.nc.Subscribe("cf.node.*."+protocol.SubjectFacts, b.onFacts); err != nil {
		return err
	}
	if _, err := b.nc.Subscribe("cf.node.*."+protocol.SubjectMetrics, b.onMetrics); err != nil {
		return err
	}
	if _, err := b.nc.Subscribe("cf.node.*."+protocol.SubjectEvents, b.onEvents); err != nil {
		return err
	}
	return b.nc.Flush()
}

// envelopeOf leest de envelop van een agent. De node-id komt uit het
// subject; de rechten in NATS zorgen dat een agent alleen onder zijn eigen
// node publiceert.
func envelopeOf(m *nats.Msg) (uuid.UUID, protocol.Envelope, error) {
	var env protocol.Envelope
	id, _, ok := protocol.ParseSubject(m.Subject)
	if !ok {
		return uuid.Nil, env, errors.New("ongeldig subject")
	}
	nodeID, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, env, err
	}
	if err := json.Unmarshal(m.Data, &env); err != nil {
		return uuid.Nil, env, err
	}
	return nodeID, env, nil
}

// decode leest een envelop van het verwachte type en geeft ook het tijdstip
// waarop de agent hem maakte.
func decode(m *nats.Msg, wantType string, body any) (uuid.UUID, time.Time, error) {
	nodeID, env, err := envelopeOf(m)
	if err != nil {
		return uuid.Nil, time.Time{}, err
	}
	if env.Type != wantType {
		return uuid.Nil, time.Time{}, errors.New("onverwacht berichttype " + env.Type)
	}
	return nodeID, env.TS, json.Unmarshal(env.Body, body)
}

// clockOffset is de tijd van de agent min die van de server, in
// milliseconden. Zonder tijd in de envelop is het nul.
func clockOffset(sent, received time.Time) int32 {
	if sent.IsZero() {
		return 0
	}
	ms := sent.Sub(received).Milliseconds()
	return int32(max(min(ms, math.MaxInt32), math.MinInt32))
}

func (b *Bus) onHeartbeat(m *nats.Msg) {
	received := time.Now()
	var hb protocol.Heartbeat
	nodeID, sent, err := decode(m, protocol.TypeHeartbeat, &hb)
	if err != nil {
		b.log.Warn("ongeldige heartbeat", "subject", m.Subject, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.handleHeartbeat(ctx, nodeID, hb, clockOffset(sent, received)); err != nil {
		b.log.Error("heartbeat verwerken mislukt", "node", nodeID, "err", err)
		return
	}
	b.changed()
}

func (b *Bus) changed() {
	if b.hooks.Changed != nil {
		b.hooks.Changed()
	}
}

func (b *Bus) handleHeartbeat(ctx context.Context, nodeID uuid.UUID, hb protocol.Heartbeat, offset int32) error {
	addrs := normalizeAddrs(hb.Addresses)
	services, err := json.Marshal(nonNilMap(hb.Services))
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		prev, err := q.GetHeartbeatPrevious(ctx, nodeID)
		known := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		version := limit(hb.AgentVersion, 64)
		err = q.TouchAgent(ctx, store.TouchAgentParams{
			NodeID: nodeID, Version: version, ProtocolVersion: int32(hb.ProtocolVersion),
			Arch: limit(hb.Arch, 16), BinarySha256: limit(hb.BinarySHA256, 64), UpgradeRolledBack: limit(hb.UpgradeRolledBack, 64),
		})
		if err != nil {
			return err
		}
		// De VIP-eigenaar en de status berekent internal/status hieruit.
		if err := q.UpsertNodeStatus(ctx, store.UpsertNodeStatusParams{
			NodeID: nodeID, UptimeSeconds: hb.UptimeSeconds,
			Load1: hb.Load[0], Load5: hb.Load[1], Load15: hb.Load[2],
			Addresses: addrs, Services: services, ClockOffsetMs: offset,
		}); err != nil {
			return err
		}
		if !known {
			return nil
		}
		return b.heartbeatChanges(ctx, q, nodeID, prev, hb, version)
	})
}

func (b *Bus) onFacts(m *nats.Msg) {
	reply := func(err error) {
		if m.Reply == "" {
			return
		}
		ack := protocol.Ack{OK: err == nil}
		if err != nil {
			ack.Error = "facts niet verwerkt"
		}
		data, _ := json.Marshal(ack)
		_ = m.Respond(data)
	}
	var f protocol.Facts
	nodeID, _, err := decode(m, protocol.TypeFacts, &f)
	if err != nil {
		b.log.Warn("ongeldige facts", "subject", m.Subject, "err", err)
		reply(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = b.handleFacts(ctx, nodeID, f)
	if err != nil {
		b.log.Error("facts verwerken mislukt", "node", nodeID, "err", err)
	} else {
		b.changed()
	}
	reply(err)
}

func (b *Bus) onMetrics(m *nats.Msg) {
	if b.hooks.Metrics == nil {
		return
	}
	var batch protocol.Metrics
	nodeID, _, err := decode(m, protocol.TypeMetrics, &batch)
	if err != nil {
		b.log.Warn("ongeldige metrics", "subject", m.Subject, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.hooks.Metrics(ctx, nodeID, batch); err != nil {
		b.log.Warn("metrics verwerken mislukt", "node", nodeID, "err", err)
	}
}

func (b *Bus) handleFacts(ctx context.Context, nodeID uuid.UUID, f protocol.Facts) error {
	stable, err := json.Marshal(f.Stable())
	if err != nil {
		return err
	}
	sum := sha256.Sum256(stable)
	hash := hex.EncodeToString(sum[:])
	full, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		old, err := q.LockNodeFacts(ctx, nodeID)
		first := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !first {
			return err
		}
		if err := q.UpsertNodeFacts(ctx, store.UpsertNodeFactsParams{
			NodeID: nodeID, Hash: hash, Facts: full, CollectedAt: time.Now(),
		}); err != nil {
			return err
		}
		if ip, err := netip.ParseAddr(f.PrimaryAddress); err == nil && !ip.IsLoopback() {
			if err := q.SetNodePrimaryIPIfEmpty(ctx, store.SetNodePrimaryIPIfEmptyParams{ID: nodeID, PrimaryIp: &ip}); err != nil {
				return err
			}
		}
		if !first && old.Hash == hash {
			return nil
		}
		var keys []string
		if !first {
			keys = changedKeys(old.Facts, f)
		}
		node, err := q.GetNode(ctx, nodeID)
		if err != nil {
			return err
		}
		if !first {
			if err := b.factsChanges(ctx, q, node.Node, old, f); err != nil {
				return err
			}
		}
		return b.ev.Write(ctx, q, events.Event{
			Actor: events.Actor{Type: store.ActorTypeAgent, ID: nodeID.String()}, SubjectType: "node",
			SubjectID: nodeID.String(), ClusterID: node.Node.ClusterID, Action: "node.facts_changed",
			Payload: map[string]any{"hostname": node.Node.Hostname, "first": first, "changed": keys},
		})
	})
}

// changedKeys vergelijkt de stabiele velden van de oude en nieuwe facts.
func changedKeys(oldJSON []byte, newFacts protocol.Facts) []string {
	var oldFacts protocol.Facts
	if err := json.Unmarshal(oldJSON, &oldFacts); err != nil {
		return nil
	}
	toMap := func(f protocol.Facts) map[string]any {
		b, _ := json.Marshal(f.Stable())
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		return m
	}
	o, n := toMap(oldFacts), toMap(newFacts)
	var keys []string
	for k, v := range n {
		if !reflect.DeepEqual(o[k], v) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// normalizeAddrs houdt alleen geldige adressen over, in vaste schrijfwijze.
func normalizeAddrs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		s := ip.WithZone("").String()
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
		if len(out) == 256 {
			break
		}
	}
	return out
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func limit(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
