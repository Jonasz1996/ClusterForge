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
		internalToken: hex.EncodeToString(tok),
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
	c.RegisterUser(&server.User{Username: agent.NodeID.String(), Permissions: agentPermissions(agent.NodeID)})
	return true
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
