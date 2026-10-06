// Package agentbus draait de ingebedde NATS-server waarmee agents verbinden.
//
// Elke agent logt in met zijn eigen nkey. De server controleert de handtekening
// en zoekt de sleutel op in de tabel agents; een ingetrokken of onbekende sleutel
// komt er niet in. Na het inloggen mag een agent alleen publiceren onder
// cf.node.<eigen node-id>, alleen luisteren op zijn eigen inbox en commando's,
// en alleen antwoorden op een commando dat hij kreeg.
package agentbus

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type Bus struct {
	log         *slog.Logger
	pool        *pgxpool.Pool
	q           *store.Queries
	ev          *events.Writer
	hooks       Hooks
	ns          *server.Server
	nc          *nats.Conn
	fingerprint string
	// internalToken is het wachtwoord van de eigen verbinding van de server.
	// Het bestaat alleen in het geheugen van dit proces.
	internalToken string
	// fpKey maakt de vingerafdrukken van bestanden in agent.command.
	fpKey []byte

	// refusedMu en refused houden bij wanneer het laatste event over een
	// geweigerde tweede verbinding per node geschreven werd.
	refusedMu sync.Mutex
	refused   map[uuid.UUID]time.Time

	// authed houdt per sleutel de adressen bij van verbindingen die door
	// Check kwamen. Connz toont ook een verbinding die nog inlogt of net
	// geweigerd wordt; Connections telt die niet mee.
	authedMu sync.Mutex
	authed   map[string]map[string]struct{}
}

// Hooks geven berichten van agents door aan de rest van de server. Elk veld
// mag nil zijn.
type Hooks struct {
	// Metrics verwerkt een batch metrics van een node.
	Metrics func(ctx context.Context, nodeID uuid.UUID, m protocol.Metrics) error
	// Changed wordt aangeroepen na elke heartbeat en nieuwe facts.
	Changed func()
}

// Start start NATS op listen (host:poort; poort 0 kiest een vrije poort) en
// verbindt de server er zelf mee.
func Start(ctx context.Context, listen string, pool *pgxpool.Pool, ev *events.Writer, log *slog.Logger, hooks Hooks) (*Bus, error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("NATS-adres %q: %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("NATS-poort %q: %w", portStr, err)
	}
	if port == 0 {
		port = server.RANDOM_PORT
	}
	q := store.New(pool)
	cert, fingerprint, err := loadOrCreateCert(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("NATS-certificaat: %w", err)
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	b := &Bus{
		log: log, pool: pool, q: q, ev: ev, hooks: hooks, fingerprint: fingerprint,
		internalToken: hex.EncodeToString(tok), refused: map[uuid.UUID]time.Time{},
		authed: map[string]map[string]struct{}{},
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	opts := &server.Options{
		ServerName:                 "clusterforge",
		Host:                       host,
		Port:                       port,
		TLSConfig:                  tlsConfig,
		TLSTimeout:                 5,
		AlwaysEnableNonce:          true,
		CustomClientAuthentication: b,
		NoSigs:                     true,
		MaxPayload:                 1 << 20,
		MaxControlLine:             4096,
		WriteDeadline:              10 * time.Second,
		PingInterval:               30 * time.Second,
		MaxPingsOut:                3,
		// Geen monitoringpoort, geen clustering, geen JetStream (nog niet nodig).
		HTTPPort: 0,
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	ns.SetLoggerV2(natsLogger{log}, false, false, false)
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		return nil, errors.New("NATS-server start niet")
	}
	b.ns = ns

	nc, err := nats.Connect("",
		nats.InProcessServer(ns),
		nats.Token(b.internalToken),
		nats.Name("clusterforge-server"),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("interne NATS-verbinding: %w", err)
	}
	b.nc = nc
	if err := b.subscribe(); err != nil {
		b.Close()
		return nil, err
	}
	log.Info("NATS gestart", "listen", ns.Addr().String(), "fingerprint", fingerprint)
	return b, nil
}

// Fingerprint is de sha256 van het TLS-certificaat; agents pinnen die.
func (b *Bus) Fingerprint() string { return b.fingerprint }

// Port is de poort waarop NATS werkelijk luistert.
func (b *Bus) Port() int {
	if a, ok := b.ns.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// Disconnect sluit alle open verbindingen van een agent, bijvoorbeeld na het
// intrekken. Hij komt niet meer binnen omdat de sleutel niet meer geldig is.
func (b *Bus) Disconnect(nkeyPublic string) {
	conns, err := b.ns.Connz(&server.ConnzOptions{User: nkeyPublic, Limit: 100})
	if err != nil {
		b.log.Warn("NATS-verbindingen opzoeken mislukt", "err", err)
		return
	}
	for _, c := range conns.Conns {
		_ = b.ns.DisconnectClientByID(c.Cid)
	}
}

// Connections telt de open, ingelogde verbindingen met de sleutel van een
// agent. Een verbinding die nog inlogt of geweigerd wordt, telt niet mee.
func (b *Bus) Connections(nkeyPublic string) (int, error) {
	return b.authedConnections(nkeyPublic, "")
}

// authedConnections telt de ingelogde verbindingen van een sleutel, voegt
// add toe als die niet leeg is en vergeet adressen die niet meer open zijn.
func (b *Bus) authedConnections(nkeyPublic, add string) (int, error) {
	conns, err := b.ns.Connz(&server.ConnzOptions{User: nkeyPublic, Limit: 100})
	if err != nil {
		return 0, err
	}
	b.authedMu.Lock()
	defer b.authedMu.Unlock()
	known := b.authed[nkeyPublic]
	live := map[string]struct{}{}
	for _, ci := range conns.Conns {
		addr := net.JoinHostPort(ci.IP, strconv.Itoa(ci.Port))
		if _, ok := known[addr]; ok || addr == add {
			live[addr] = struct{}{}
		}
	}
	if len(live) == 0 {
		delete(b.authed, nkeyPublic)
	} else {
		b.authed[nkeyPublic] = live
	}
	return len(live), nil
}

func (b *Bus) Close() {
	if b.nc != nil {
		_ = b.nc.Drain()
	}
	if b.ns != nil {
		b.ns.Shutdown()
		b.ns.WaitForShutdown()
	}
}

// Check is de authenticatie van NATS; zie de packagebeschrijving.
func (b *Bus) Check(c server.ClientAuthentication) bool {
	if c.Kind() != server.CLIENT {
		return false
	}
	o := c.GetOpts()
	if o.Token != "" {
		if subtle.ConstantTimeCompare([]byte(o.Token), []byte(b.internalToken)) == 1 {
			c.RegisterUser(&server.User{Username: "clusterforge-server"})
			return true
		}
		return false
	}
	if o.Nkey == "" || o.Sig == "" || !nkeys.IsValidPublicUserKey(o.Nkey) {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(o.Sig)
	if err != nil {
		if sig, err = base64.StdEncoding.DecodeString(o.Sig); err != nil {
			return false
		}
	}
	pub, err := nkeys.FromPublicKey(o.Nkey)
	if err != nil || pub.Verify(c.GetNonce(), sig) != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agent, err := b.q.GetActiveAgentByNkey(ctx, o.Nkey)
	if err != nil {
		b.log.Warn("agent geweigerd: onbekende of ingetrokken sleutel", "nkey", o.Nkey, "remote", c.RemoteAddress().String())
		return false
	}
	if b.secondDuringSandbox(ctx, agent.NodeID, o.Nkey, c.RemoteAddress()) {
		return false
	}
	if ra := c.RemoteAddress(); ra != nil {
		if _, err := b.authedConnections(o.Nkey, ra.String()); err != nil {
			b.log.Warn("NATS-verbindingen opzoeken mislukt", "err", err)
		}
	}
	c.RegisterUser(&server.User{Username: agent.NodeID.String(), Permissions: agentPermissions(agent.NodeID)})
	return true
}

// secondDuringSandbox is true als dit een tweede gelijktijdige verbinding
// met de sleutel van een node is terwijl er een sandbox van zijn VM bestaat.
// Die teruggezette kopie draagt dezelfde agent.json; kwam ze binnen, dan
// kreeg ze de commando's van de echte node.
func (b *Bus) secondDuringSandbox(ctx context.Context, nodeID uuid.UUID, nkey string, self net.Addr) bool {
	conns, err := b.ns.Connz(&server.ConnzOptions{User: nkey, Limit: 100})
	if err != nil {
		b.log.Warn("NATS-verbindingen opzoeken mislukt", "err", err)
		return false
	}
	others := 0
	for _, ci := range conns.Conns {
		// De verbinding die nu inlogt, staat zelf ook in de lijst.
		if self != nil && net.JoinHostPort(ci.IP, strconv.Itoa(ci.Port)) == self.String() {
			continue
		}
		others++
	}
	if others == 0 {
		return false
	}
	active, err := b.q.SandboxActiveForNode(ctx, &nodeID)
	if err != nil {
		// Bij twijfel weigeren: de agent probeert het straks opnieuw.
		b.log.Warn("sandbox-register lezen mislukt; tweede verbinding geweigerd", "node", nodeID, "err", err)
		return true
	}
	if !active {
		return false
	}
	remote := ""
	if self != nil {
		remote = self.String()
	}
	b.log.Warn("tweede agentverbinding geweigerd tijdens een back-upcontrole", "node", nodeID, "remote", remote)
	b.refusedEvent(nodeID, remote)
	return true
}

// refusedEvent schrijft hoogstens één event per node per tien minuten, los
// van de NATS-goroutine.
func (b *Bus) refusedEvent(nodeID uuid.UUID, remote string) {
	b.refusedMu.Lock()
	last, seen := b.refused[nodeID]
	now := time.Now()
	if seen && now.Sub(last) < 10*time.Minute {
		b.refusedMu.Unlock()
		return
	}
	b.refused[nodeID] = now
	b.refusedMu.Unlock()
	if b.ev == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e := events.Event{
			Actor: events.System(), SubjectType: "node", SubjectID: nodeID.String(),
			Action: "backup.sandbox_connection_refused", Payload: map[string]any{"remote": remote},
		}
		if n, err := b.q.GetNode(ctx, nodeID); err == nil {
			e.ClusterID = n.Node.ClusterID
			e.Payload["hostname"] = n.Node.Hostname
		}
		if err := b.ev.Write(ctx, nil, e); err != nil {
			b.log.Warn("event schrijven mislukt", "err", err)
		}
	}()
}

func agentPermissions(nodeID uuid.UUID) *server.Permissions {
	id := nodeID.String()
	return &server.Permissions{
		Publish: &server.SubjectPermission{Allow: []string{protocol.NodePrefix(id) + ".>"}},
		Subscribe: &server.SubjectPermission{Allow: []string{
			protocol.Subject(id, protocol.SubjectCommands),
			protocol.InboxPrefix(id) + ".>",
		}},
		// Eén antwoord op elk commando van de server, naar diens inbox.
		Response: &server.ResponsePermission{MaxMsgs: 1, Expires: 5 * time.Minute},
	}
}

// natsLogger stuurt de logregels van NATS naar slog.
type natsLogger struct{ log *slog.Logger }

func (l natsLogger) Noticef(format string, v ...any) {
	l.log.Debug("nats: " + fmt.Sprintf(format, v...))
}
func (l natsLogger) Warnf(format string, v ...any) { l.log.Warn("nats: " + fmt.Sprintf(format, v...)) }
func (l natsLogger) Fatalf(format string, v ...any) {
	l.log.Error("nats: " + fmt.Sprintf(format, v...))
}
func (l natsLogger) Errorf(format string, v ...any) { l.log.Warn("nats: " + fmt.Sprintf(format, v...)) }
func (l natsLogger) Debugf(format string, v ...any) {
	l.log.Debug("nats: " + fmt.Sprintf(format, v...))
}
func (l natsLogger) Tracef(string, ...any) {}
