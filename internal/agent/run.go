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
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	// Root is de bestandssysteemwortel voor deploystappen; leeg is "/".
	// Tests gebruiken een map.
	Root string
	// Journal is het wijzigingsjournaal; nil maakt er een met de standaard
	// paden.
	Journal *Journal
	// JournalInterval, ConnsSample en ConnsInterval; nul gebruikt de
	// standaard uit protocol.
	JournalInterval time.Duration
	ConnsSample     time.Duration
	ConnsInterval   time.Duration
	// AddressPoll is hoe vaak de agent naar zijn adressen kijkt; bij een
	// wijziging stuurt hij meteen een heartbeat. Nul is één seconde.
	AddressPoll time.Duration
	// KubeletHealthz is de healthz van de kubelet; leeg is die op
	// 127.0.0.1:10248. Tests vervangen het.
	KubeletHealthz string

	nc    *nats.Conn
	conns ConnTracker
	// hbMu bewaakt lastAddrs en lastHB.
	hbMu      sync.Mutex
	lastAddrs string
	lastHB    time.Time
	factsNow  chan struct{}
	connected chan struct{}
	// stateMu bewaakt StatePath; zie updateState.
	stateMu sync.Mutex
	faults  *Faults
	kube    *kubeBridge
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
	if a.Journal == nil {
		// Tests geven de agent een eigen wortel en een nep-systemctl; het
		// journaal leest dan daar.
		root := a.Root
		if root == "" {
			root = a.Collector.Root
		}
		a.Journal = &Journal{Root: root, Run: a.Collector.Run, Log: a.Log}
		if a.StatePath != "" {
			a.Journal.Path = filepath.Join(filepath.Dir(a.StatePath), "journal.json")
		}
	}
	if a.Collector.OnUnits == nil {
		a.Collector.OnUnits = a.Journal.ObserveUnits
	}
	a.initFaults()
	a.initKube()
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
	if _, err = a.nc.Subscribe(protocol.Subject(a.Config.NodeID, protocol.SubjectCommands), a.onCommand); err != nil {
		return err
	}
	_, err = a.nc.Subscribe(protocol.Subject(a.Config.NodeID, protocol.SubjectKube), a.onKube)
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

	// Storingen die er nog staan, bijvoorbeeld na een crash, gaan eerst weg.
	if res := a.faults.ClearAll(ctx); !res.OK {
		a.Log.Error("achtergebleven storingen terugdraaien mislukt", "err", res.Error)
	} else if len(res.Output) > 0 {
		a.Log.Warn("achtergebleven storingen teruggedraaid", "output", strings.Join(res.Output, "; "))
	}
	go a.faultLoop(ctx)
	go a.factsLoop(ctx, factsEvery)
	go a.metricsLoop(ctx, metricsEvery)
	go a.journalLoop(ctx, or(a.JournalInterval, protocol.JournalInterval))
	go a.connsLoop(ctx, or(a.ConnsSample, protocol.ConnsSample), or(a.ConnsInterval, protocol.ConnsInterval))

	t := time.NewTicker(hbEvery)
	defer t.Stop()
	addr := time.NewTicker(or(a.AddressPoll, time.Second))
	defer addr.Stop()
	a.sendHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.connected:
			a.sendHeartbeat(ctx)
		case <-t.C:
			a.sendHeartbeat(ctx)
		case <-addr.C:
			// Een VIP die komt of gaat, staat zo binnen een seconde in de
			// server in plaats van bij de volgende heartbeat.
			if a.addressesChanged() {
				a.sendHeartbeat(ctx)
			}
		}
	}
}

func (a *Agent) addresses() []string {
	if a.Collector.Addresses != nil {
		return a.Collector.Addresses()
	}
	return hostAddresses()
}

func addrKey(addrs []string) string {
	s := slices.Clone(addrs)
	slices.Sort(s)
	return strings.Join(s, ",")
}

// addressesChanged is true als de adressen anders zijn dan in de laatste
// heartbeat, hoogstens één keer per seconde.
func (a *Agent) addressesChanged() bool {
	key := addrKey(a.addresses())
	a.hbMu.Lock()
	defer a.hbMu.Unlock()
	return key != a.lastAddrs && time.Since(a.lastHB) >= time.Second
}

func (a *Agent) sendHeartbeat(ctx context.Context) {
	if !a.nc.IsConnected() {
		return
	}
	hb := a.Collector.Heartbeat(ctx, a.Version)
	if err := a.publish(protocol.SubjectHeartbeat, protocol.TypeHeartbeat, hb); err != nil {
		a.Log.Warn("heartbeat sturen mislukt", "err", err)
		return
	}
	a.hbMu.Lock()
	a.lastAddrs, a.lastHB = addrKey(hb.Addresses), time.Now()
	a.hbMu.Unlock()
}

// journalLoop leest het journaal en stuurt wat er wacht, bij elke interval
// en na een (her)verbinding.
func (a *Agent) journalLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := a.Journal.Collect(ctx); err != nil {
			a.Log.Warn("journaal bewaren mislukt", "err", err)
		}
		if err := a.sendChanges(ctx); err != nil {
			if errors.Is(err, nats.ErrNoResponders) {
				a.Log.Debug("de server neemt het journaal nog niet aan", "err", err)
			} else {
				a.Log.Warn("journaal sturen mislukt", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sendChanges stuurt de wachtende regels in porties van MaxChanges, elk als
// request; pas na de Ack vergeet de agent ze.
func (a *Agent) sendChanges(ctx context.Context) error {
	for ctx.Err() == nil {
		if !a.nc.IsConnected() {
			return nil
		}
		batch := a.Journal.Next()
		if len(batch.Entries) == 0 {
			return nil
		}
		data, err := envelope(protocol.TypeChanges, batch)
		if err != nil {
			return err
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		msg, err := a.nc.RequestWithContext(rctx, protocol.Subject(a.Config.NodeID, protocol.SubjectEvents), data)
		cancel()
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
		if err := a.Journal.Sent(batch); err != nil {
			return err
		}
	}
	return nil
}

// connsLoop bekijkt de verbindingen elke sample en stuurt elke interval een
// samenvatting. Een samenvatting die niet weg kan, vervalt.
func (a *Agent) connsLoop(ctx context.Context, sample, every time.Duration) {
	st := time.NewTicker(sample)
	defer st.Stop()
	send := time.NewTicker(every)
	defer send.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-st.C:
			a.Collector.SampleConns(&a.conns, now)
		case now := <-send.C:
			c, ok := a.conns.Take(now)
			if !ok || !a.nc.IsConnected() {
				continue
			}
			if err := a.publish(protocol.SubjectEvents, protocol.TypeConns, c); err != nil {
				a.Log.Warn("verbindingen sturen mislukt", "err", err)
			}
		}
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
	f.Faults = a.faults.Active()
	f.Kube = a.kubeFacts(ctx, f.Hostname)
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
