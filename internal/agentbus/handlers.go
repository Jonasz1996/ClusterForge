package agentbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	return b.nc.Flush()
}

// decode leest een envelop van een agent. De node-id komt uit het subject; de
// rechten in NATS zorgen dat een agent alleen onder zijn eigen node publiceert.
func decode(m *nats.Msg, wantType string, body any) (uuid.UUID, error) {
	id, _, ok := protocol.ParseSubject(m.Subject)
	if !ok {
		return uuid.Nil, errors.New("ongeldig subject")
	}
	nodeID, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(m.Data, &env); err != nil {
		return uuid.Nil, err
	}
	if env.Type != wantType {
		return uuid.Nil, errors.New("onverwacht berichttype " + env.Type)
	}
	return nodeID, json.Unmarshal(env.Body, body)
}

func (b *Bus) onHeartbeat(m *nats.Msg) {
	var hb protocol.Heartbeat
	nodeID, err := decode(m, protocol.TypeHeartbeat, &hb)
	if err != nil {
		b.log.Warn("ongeldige heartbeat", "subject", m.Subject, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.handleHeartbeat(ctx, nodeID, hb); err != nil {
		b.log.Error("heartbeat verwerken mislukt", "node", nodeID, "err", err)
	}
}

func (b *Bus) handleHeartbeat(ctx context.Context, nodeID uuid.UUID, hb protocol.Heartbeat) error {
	addrs := normalizeAddrs(hb.Addresses)
	services, err := json.Marshal(nonNilMap(hb.Services))
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		err := q.TouchAgent(ctx, store.TouchAgentParams{
			NodeID: nodeID, Version: limit(hb.AgentVersion, 64), ProtocolVersion: int32(hb.ProtocolVersion),
		})
		if err != nil {
			return err
		}
		err = q.UpsertNodeStatus(ctx, store.UpsertNodeStatusParams{
			NodeID: nodeID, UptimeSeconds: hb.UptimeSeconds,
			Load1: hb.Load[0], Load5: hb.Load[1], Load15: hb.Load[2],
			Addresses: addrs, Services: services,
		})
		if err != nil {
			return err
		}
		return b.reconcileVIPs(ctx, q, nodeID, addrs)
	})
}

// reconcileVIPs zet de eigenaar van de VIP's in het cluster van de node: heeft
// de node het adres op een interface, dan is hij eigenaar; is hij eigenaar maar
// heeft hij het adres niet meer, dan is er (voorlopig) geen eigenaar.
func (b *Bus) reconcileVIPs(ctx context.Context, q *store.Queries, nodeID uuid.UUID, addrs []string) error {
	vips, err := q.ListVIPsForOwnership(ctx, nodeID)
	if err != nil {
		return err
	}
	for _, v := range vips {
		has := slices.Contains(addrs, v.Address.String())
		owned := v.OwnerNodeID != nil && *v.OwnerNodeID == nodeID
		var newOwner *uuid.UUID
		switch {
		case has && !owned:
			// Een VIP die de node niet bezit komt volgens de query altijd uit
			// zijn eigen cluster.
			newOwner = &nodeID
		case !has && owned:
			newOwner = nil
		default:
			continue
		}
		if err := q.SetVIPOwner(ctx, store.SetVIPOwnerParams{ID: v.ID, OwnerNodeID: newOwner}); err != nil {
			return err
		}
		payload := map[string]any{"address": v.Address.String(), "from": v.OwnerNodeID, "to": newOwner}
		cid := v.ClusterID
		err := b.ev.Write(ctx, q, events.Event{
			Actor: events.Actor{Type: store.ActorTypeAgent, ID: nodeID.String()}, SubjectType: "vip",
			SubjectID: v.ID.String(), ClusterID: &cid, Action: "vip.owner_changed", Payload: payload,
		})
		if err != nil {
			return err
		}
	}
	return nil
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
	nodeID, err := decode(m, protocol.TypeFacts, &f)
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
	}
	reply(err)
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
