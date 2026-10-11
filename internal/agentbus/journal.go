package agentbus

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// onEvents ontvangt het journaal (een request) en de verbindingen van een
// agent op protocol 5.
func (b *Bus) onEvents(m *nats.Msg) {
	received := time.Now()
	reply := func(err error) {
		if m.Reply == "" {
			return
		}
		ack := protocol.Ack{OK: err == nil}
		if err != nil {
			ack.Error = "niet verwerkt"
		}
		data, _ := json.Marshal(ack)
		_ = m.Respond(data)
	}
	nodeID, env, err := envelopeOf(m)
	if err != nil {
		b.log.Warn("ongeldig bericht op events", "subject", m.Subject, "err", err)
		reply(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch env.Type {
	case protocol.TypeChanges:
		var c protocol.Changes
		if err := json.Unmarshal(env.Body, &c); err != nil {
			b.log.Warn("ongeldig journaal", "node", nodeID, "err", err)
			reply(err)
			return
		}
		err = b.handleChanges(ctx, nodeID, c, clockOffset(env.TS, received), received)
		if err != nil {
			b.log.Error("journaal verwerken mislukt", "node", nodeID, "err", err)
		}
		reply(err)
	case protocol.TypeConns:
		var c protocol.Conns
		if err := json.Unmarshal(env.Body, &c); err != nil {
			b.log.Warn("ongeldige verbindingen", "node", nodeID, "err", err)
			return
		}
		if err := b.handleConns(ctx, nodeID, c, received); err != nil {
			b.log.Warn("verbindingen verwerken mislukt", "node", nodeID, "err", err)
		}
	default:
		b.log.Warn("onbekend berichttype op events", "node", nodeID, "type", env.Type)
		reply(errors.New("onbekend berichttype"))
	}
}

// handleChanges bewaart het journaal van een agent. De tijden worden
// gecorrigeerd met de klokafwijking uit hetzelfde bericht; een regel uit de
// toekomst krijgt de tijd van ontvangst, en een regel ouder dan
// JournalMaxAge plus een dag valt weg.
func (b *Bus) handleChanges(ctx context.Context, nodeID uuid.UUID, c protocol.Changes, offsetMs int32, received time.Time) error {
	if len(c.Entries) > protocol.MaxChanges {
		c.Entries = c.Entries[:protocol.MaxChanges]
	}
	offset := time.Duration(offsetMs) * time.Millisecond
	oldest := received.Add(-protocol.JournalMaxAge - 24*time.Hour)
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		ref, err := q.GetNodeRef(ctx, nodeID)
		if err != nil {
			return err
		}
		entries := make([]journal.Entry, 0, len(c.Entries)+1)
		for _, ch := range c.Entries {
			if !journal.AgentKinds[ch.Kind] || ch.Action == "" || ch.TS.IsZero() {
				continue
			}
			ts := ch.TS.Add(-offset)
			if ts.After(received.Add(time.Minute)) {
				ts = received
			}
			if ts.Before(oldest) {
				continue
			}
			entries = append(entries, journal.Sanitize(journal.Entry{
				NodeID: nodeID, ClusterID: ref.ClusterID, TS: ts, Source: journal.SourceAgent,
				Kind: ch.Kind, Subject: ch.Subject, Action: ch.Action, From: ch.From, To: ch.To, By: ch.By, Detail: ch.Detail,
			}))
		}
		if c.Truncated {
			entries = append(entries, journal.Entry{
				NodeID: nodeID, ClusterID: ref.ClusterID, TS: received, Source: journal.SourceServer,
				Kind: journal.KindAgent, Subject: "cf-agent", Action: "truncated",
			})
		}
		return journal.Record(ctx, q, entries...)
	})
}

// handleConns telt de verbindingen bij. Van een inkomende verbinding blijft
// de afzender alleen staan als het een bekende node is.
func (b *Bus) handleConns(ctx context.Context, nodeID uuid.UUID, c protocol.Conns, received time.Time) error {
	known, err := b.q.ListKnownNodeAddresses(ctx)
	if err != nil {
		return err
	}
	isNode := map[string]bool{}
	for _, a := range known {
		isNode[a] = true
	}
	type key struct {
		unit, dir, addr string
		peerPort, local int
	}
	sum := map[key]int64{}
	unit := func(s string) string { return limit(strings.TrimSpace(s), 100) }
	for i, o := range c.Out {
		if i == protocol.MaxConns {
			break
		}
		ip, err := netip.ParseAddr(o.Addr)
		if err != nil || o.Port <= 0 || o.Port > 65535 || o.Count <= 0 {
			continue
		}
		sum[key{unit(o.Unit), "out", ip.String(), o.Port, 0}] += int64(o.Count)
	}
	for i, in := range c.In {
		if i == protocol.MaxConns {
			break
		}
		if in.Port <= 0 || in.Port > 65535 || in.Count <= 0 {
			continue
		}
		peer := ""
		if ip, err := netip.ParseAddr(in.Peer); err == nil && isNode[ip.String()] {
			peer = ip.String()
		}
		sum[key{unit(in.Unit), "in", peer, 0, in.Port}] += int64(in.Count)
	}
	if len(sum) == 0 {
		return nil
	}
	params := make([]store.UpsertObservedConnectionsParams, 0, len(sum))
	for k, n := range sum {
		params = append(params, store.UpsertObservedConnectionsParams{
			NodeID: nodeID, Unit: k.unit, Direction: k.dir, PeerAddr: k.addr, PeerPort: int32(k.peerPort),
			LocalPort: int32(k.local), FirstSeen: received, Samples: n,
		})
	}
	var batchErr error
	b.q.UpsertObservedConnections(ctx, params).Exec(func(_ int, err error) {
		if err != nil && batchErr == nil {
			batchErr = err
		}
	})
	return batchErr
}

// heartbeatChanges schrijft wat de server zelf uit een heartbeat ziet: een
// nieuwe agentversie, en bij een agent zonder journaal de diensten die van
// toestand wisselden.
func (b *Bus) heartbeatChanges(ctx context.Context, q *store.Queries, nodeID uuid.UUID, prev store.GetHeartbeatPreviousRow,
	hb protocol.Heartbeat, version string) error {
	var entries []journal.Entry
	if prev.AgentVersion != "" && version != "" && prev.AgentVersion != version {
		entries = append(entries, journal.Entry{
			Kind: journal.KindAgent, Subject: "cf-agent", Action: "upgrade", From: prev.AgentVersion, To: version,
		})
	}
	if hb.ProtocolVersion < protocol.JournalSince && prev.HeartbeatAt != nil {
		var old map[string]string
		_ = json.Unmarshal(prev.Services, &old)
		entries = append(entries, journal.ServiceChanges(old, hb.Services)...)
	}
	if len(entries) == 0 {
		return nil
	}
	ref, err := q.GetNodeRef(ctx, nodeID)
	if err != nil {
		return err
	}
	now := time.Now()
	for i := range entries {
		entries[i].NodeID, entries[i].ClusterID, entries[i].TS, entries[i].Source = nodeID, ref.ClusterID, now, journal.SourceServer
		entries[i] = journal.Sanitize(entries[i])
	}
	return journal.Record(ctx, q, entries...)
}

// factsChanges schrijft de verschillen tussen de vorige en de nieuwe facts
// die de server zelf waarneemt.
func (b *Bus) factsChanges(ctx context.Context, q *store.Queries, node store.Node, old store.NodeFact, f protocol.Facts) error {
	var prev protocol.Facts
	if err := json.Unmarshal(old.Facts, &prev); err != nil {
		return nil
	}
	agentJournal := false
	if a, err := q.GetActiveAgentByNode(ctx, node.ID); err == nil {
		agentJournal = a.ProtocolVersion >= protocol.JournalSince
	}
	entries := journal.FactsChanges(prev, f, agentJournal)
	now := time.Now()
	for i := range entries {
		e := &entries[i]
		e.NodeID, e.ClusterID, e.Source = node.ID, node.ClusterID, journal.SourceServer
		if e.TS.IsZero() {
			e.TS = now
			// Ergens tussen de vorige facts en nu.
			if e.Detail == nil {
				e.Detail = map[string]string{}
			}
			e.Detail["since"] = old.CollectedAt.UTC().Format(time.RFC3339)
		}
		*e = journal.Sanitize(*e)
	}
	return journal.Record(ctx, q, entries...)
}

// lastHeartbeat geeft wanneer de laatste heartbeat van een node binnenkwam,
// of nul. Check leest het voor de agent erin mag, zodat zijn eerste nieuwe
// heartbeat het niet al overschreven heeft.
func (b *Bus) lastHeartbeat(ctx context.Context, nodeID uuid.UUID) time.Time {
	st, err := b.q.GetNodeStatus(ctx, nodeID)
	if err != nil {
		return time.Time{}
	}
	return st.HeartbeatAt
}

// noteReconnect schrijft bij het verbinden van een agent hoe lang er geen
// contact was, als dat langer dan gapMin duurde. Een onderbreking die
// begon voor deze server startte, komt door de server zelf en telt niet.
func (b *Bus) noteReconnect(nodeID uuid.UUID, last time.Time) {
	now := time.Now()
	gap := now.Sub(last)
	if last.IsZero() || gap < gapMin || last.Before(b.started) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ref, err := b.q.GetNodeRef(ctx, nodeID)
	if err != nil {
		return
	}
	e := journal.Entry{
		NodeID: nodeID, ClusterID: ref.ClusterID, TS: last, Source: journal.SourceServer,
		Kind: journal.KindAgent, Subject: "cf-agent", Action: "gap",
		Detail: map[string]string{"until": now.UTC().Format(time.RFC3339), "seconds": strconv.Itoa(int(gap.Seconds()))},
	}
	if err := journal.Record(ctx, b.q, e); err != nil {
		b.log.Warn("onderbreking in het journaal schrijven mislukt", "node", nodeID, "err", err)
	}
}

// gapMin is hoe lang een agent weg moet zijn voor een regel: drie gemiste
// heartbeats.
const gapMin = 3 * protocol.HeartbeatInterval

// recordCommand schrijft wat ClusterForge met een geslaagd commando op de
// node veranderde, met de taak uit ctx.
func (b *Bus) recordCommand(ctx context.Context, nodeID uuid.UUID, cmd protocol.Command, res protocol.Result) {
	if !res.OK {
		return
	}
	var entries []journal.Entry
	add := func(kind, subject, action string, detail map[string]string) {
		entries = append(entries, journal.Entry{Kind: kind, Subject: subject, Action: action, By: "clusterforge", Detail: detail})
	}
	switch cmd.Action {
	case protocol.CmdApply:
		for i, st := range cmd.Steps {
			if i >= len(res.Steps) || !res.Steps[i].Changed {
				continue
			}
			switch {
			case st.Package != nil:
				action := "install"
				if st.Package.State == "absent" {
					action = "remove"
				}
				add(journal.KindPackage, strings.Join(st.Package.Names, ", "), action, nil)
			case st.File != nil:
				add(journal.KindFile, st.File.Path, "written", nil)
			case st.Directory != nil:
				add(journal.KindFile, st.Directory.Path, "created", nil)
			case st.Service != nil:
				action := st.Service.State
				if action == "" && st.Service.Enabled != nil {
					action = map[bool]string{true: "enabled", false: "disabled"}[*st.Service.Enabled]
				}
				if action != "" {
					add(journal.KindService, st.Service.Name, action, nil)
				}
			case st.User != nil:
				add(journal.KindUser, st.User.Name, "created", nil)
			case st.Command != nil:
				add(journal.KindCommand, st.Command.Creates, "ran", nil)
			}
		}
	case protocol.CmdMaintenanceEnter:
		add(journal.KindService, "keepalived", "stopped", map[string]string{"reason": "onderhoud"})
	case protocol.CmdMaintenanceExit:
		add(journal.KindService, "keepalived", "started", map[string]string{"reason": "einde onderhoud"})
	case protocol.CmdReboot:
		add(journal.KindBoot, "boot", "requested", nil)
	case protocol.CmdShutdown:
		add(journal.KindBoot, "boot", "shutdown_requested", nil)
	}
	if len(entries) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ref, err := b.q.GetNodeRef(rctx, nodeID)
	if err != nil {
		return
	}
	jobID := events.OriginFrom(ctx).JobID
	now := time.Now()
	for i := range entries {
		e := &entries[i]
		e.NodeID, e.ClusterID, e.TS, e.Source, e.JobID = nodeID, ref.ClusterID, now, journal.SourceClusterForge, jobID
		*e = journal.Sanitize(*e)
	}
	if err := journal.Record(rctx, b.q, entries...); err != nil {
		b.log.Warn("wijziging door ClusterForge in het journaal schrijven mislukt", "node", nodeID, "err", err)
	}
}
