package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Agent houdt de verbinding met de server, stuurt heartbeats, metrics en
// facts, en voert de commando's van de server uit.
type Agent struct {
	Config    *Config
	Version   string
	Log       *slog.Logger
	Collector *Collector
	// Intervallen; nul gebruikt de standaard uit protocol. Tests maken ze kort.
	HeartbeatInterval time.Duration
	MetricsInterval   time.Duration
	FactsInterval     time.Duration
	// StatePath is het bestand waarin de agent onderhoud en power-commando's
	// onthoudt; leeg gebruikt DefaultStatePath.
	StatePath string
	// Exec voert de systeemcommando's van een commando uit (systemctl); nil
	// gebruikt exec. Tests vervangen het.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	nc        *nats.Conn
	factsNow  chan struct{}
	connected chan struct{}
}

// Connect verbindt met NATS. Is de server nog niet bereikbaar, dan blijft de
// verbinding het op de achtergrond proberen.
func (a *Agent) Connect() error {
	kp, err := nkeys.FromSeed([]byte(a.Config.NkeySeed))
	if err != nil {
		return err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return err
	}
	if a.Collector == nil {
		a.Collector = &Collector{}
	}
	if a.Collector.PrimaryAddress == nil {
		a.Collector.PrimaryAddress = func() string { return outboundAddress(a.Config.NatsURL) }
	}
	a.factsNow = make(chan struct{}, 1)
	a.connected = make(chan struct{}, 1)
	a.nc, err = nats.Connect(a.Config.NatsURL,
		nats.Nkey(pub, kp.Sign),
		nats.Secure(protocol.PinnedTLSConfig(a.Config.NatsCertSHA256)),
		nats.CustomInboxPrefix(protocol.InboxPrefix(a.Config.NodeID)),
		nats.Name("cf-agent "+a.Version),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.ReconnectJitter(time.Second, time.Second),
		nats.RetryOnFailedConnect(true),
		nats.ConnectHandler(func(*nats.Conn) {
			a.Log.Info("verbonden met ClusterForge", "url", a.Config.NatsURL)
			a.signal(a.connected)
			a.signal(a.factsNow)
		}),
		nats.ReconnectHandler(func(*nats.Conn) {
			a.Log.Info("opnieuw verbonden met ClusterForge")
			a.signal(a.factsNow)
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				a.Log.Warn("verbinding met ClusterForge verbroken", "err", err)
			}
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) {
				a.Log.Error("ClusterForge weigert deze agent; is hij ingetrokken? Meld hem opnieuw aan met cf-agent enroll", "err", err)
				return
			}
			a.Log.Warn("NATS-fout", "err", err)
		}),
	)
	if err != nil {
		return err
	}
	// Een subscription overleeft herverbindingen; nats.go zet hem zelf terug.
	_, err = a.nc.Subscribe(protocol.Subject(a.Config.NodeID, protocol.SubjectCommands), a.onCommand)
	return err
}

func (a *Agent) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run stuurt heartbeats, metrics en facts tot ctx afloopt.
func (a *Agent) Run(ctx context.Context) error {
	if a.nc == nil {
		if err := a.Connect(); err != nil {
			return err
		}
	}
	defer a.nc.Close()
	if a.nc.IsConnected() {
		a.signal(a.factsNow)
	}
	hbEvery, metricsEvery, factsEvery := a.HeartbeatInterval, a.MetricsInterval, a.FactsInterval
	if hbEvery == 0 {
		hbEvery = protocol.HeartbeatInterval
	}
	if metricsEvery == 0 {
		metricsEvery = protocol.MetricsInterval
	}
	if factsEvery == 0 {
		factsEvery = protocol.FactsInterval
	}

	go a.factsLoop(ctx, factsEvery)
	go a.metricsLoop(ctx, metricsEvery)

	t := time.NewTicker(hbEvery)
	defer t.Stop()
	a.sendHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.connected:
			a.sendHeartbeat(ctx)
		case <-t.C:
			a.sendHeartbeat(ctx)
		}
	}
}

func (a *Agent) sendHeartbeat(ctx context.Context) {
	if !a.nc.IsConnected() {
		return
	}
	hb := a.Collector.Heartbeat(ctx, a.Version)
	if err := a.publish(protocol.SubjectHeartbeat, protocol.TypeHeartbeat, hb); err != nil {
		a.Log.Warn("heartbeat sturen mislukt", "err", err)
	}
}

// metricsLoop stuurt elke interval een batch metrics. Een batch die niet weg
// kan, vervalt: de tellers lopen door en de volgende batch is weer actueel.
func (a *Agent) metricsLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !a.nc.IsConnected() {
			continue
		}
		if err := a.publish(protocol.SubjectMetrics, protocol.TypeMetrics, a.Collector.Metrics()); err != nil {
			a.Log.Warn("metrics sturen mislukt", "err", err)
		}
	}
}

// factsLoop stuurt facts bij (her)verbinden en elke interval. Lukt het niet,
// dan probeert hij het na een halve minuut opnieuw.
func (a *Agent) factsLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.factsNow:
		case <-t.C:
		case <-retry:
		}
		retry = nil
		if err := a.sendFacts(ctx); err != nil {
			a.Log.Warn("facts sturen mislukt", "err", err)
			retry = time.After(30 * time.Second)
		}
	}
}

func (a *Agent) sendFacts(ctx context.Context) error {
	if !a.nc.IsConnected() {
		return errors.New("niet verbonden")
	}
	f := a.Collector.Facts(ctx)
	data, err := envelope(protocol.TypeFacts, f)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	msg, err := a.nc.RequestWithContext(rctx, protocol.Subject(a.Config.NodeID, protocol.SubjectFacts), data)
	if err != nil {
		return err
	}
	var ack protocol.Ack
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return err
	}
	if !ack.OK {
		return errors.New("server: " + ack.Error)
	}
	return nil
}

func (a *Agent) publish(kind, typ string, body any) error {
	data, err := envelope(typ, body)
	if err != nil {
		return err
	}
	return a.nc.Publish(protocol.Subject(a.Config.NodeID, kind), data)
}

func envelope(typ string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return json.Marshal(protocol.Envelope{V: protocol.Version, ID: hex.EncodeToString(id), Type: typ, TS: time.Now().UTC(), Body: b})
}

// outboundAddress is het lokale adres waarmee de node de server bereikt.
// Er wordt niets verstuurd: een UDP-"verbinding" kiest alleen de route.
func outboundAddress(natsURL string) string {
	u, err := url.Parse(natsURL)
	if err != nil {
		return ""
	}
	conn, err := net.Dial("udp", u.Host)
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
