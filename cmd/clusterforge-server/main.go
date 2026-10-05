// Command clusterforge-server is de centrale server van ClusterForge.
//
//	clusterforge-server serve                     start de server (voert migraties uit)
//	clusterforge-server migrate                   voert alleen de migraties uit
//	clusterforge-server admin create -username X  maakt een beheerder aan
//	clusterforge-server version                   toont de versie
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/term"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// version wordt bij het bouwen gezet met -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fout:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "serve":
		return serve()
	case "migrate":
		return migrateOnly()
	case "admin":
		if len(args) < 2 || args[1] != "create" {
			return usage()
		}
		return adminCreate(args[2:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		return usage()
	}
}

func usage() error {
	return errors.New(`gebruik: clusterforge-server <serve|migrate|admin create|version>`)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func serve() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}

	q := store.New(pool)
	ev := events.NewWriter(q, log)
	authSvc, err := auth.NewService(q, ev, cfg.SessionTTL)
	if err != nil {
		return err
	}
	if n, err := q.CountUsers(ctx); err == nil && n == 0 {
		log.Warn("er bestaan nog geen gebruikers; maak een beheerder aan met: clusterforge-server admin create -username <naam>")
	}

	eval := status.NewEvaluator(pool, ev, log)
	ingest := metrics.NewIngester(cfg.VictoriaMetricsURL, q, log)
	if !ingest.Enabled() {
		log.Warn("CF_VICTORIAMETRICS_URL is niet gezet; metrics van agents worden niet bewaard")
	}
	bus, err := agentbus.Start(ctx, cfg.NATSListen, pool, ev, log, agentbus.Hooks{
		Metrics: func(ctx context.Context, nodeID uuid.UUID, m protocol.Metrics) error {
			return ingest.Ingest(ctx, nodeID, m, time.Now())
		},
		Changed: eval.Kick,
	})
	if err != nil {
		return err
	}
	defer bus.Close()
	hub := live.NewHub(pool, log)
	go hub.Run(ctx)
	go eval.Run(ctx)
	go ingest.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           httpapi.New(cfg, log, pool, authSvc, bus, hub, version).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go cleanupSessions(ctx, log, authSvc)

	errc := make(chan error, 1)
	go func() {
		log.Info("clusterforge-server gestart", "listen", cfg.Listen, "version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("afsluiten")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

func cleanupSessions(ctx context.Context, log *slog.Logger, a *auth.Service) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := a.CleanupSessions(ctx); err != nil {
				log.Error("verlopen sessies opruimen", "err", err)
			} else if n > 0 {
				log.Info("verlopen sessies opgeruimd", "count", n)
			}
		}
	}
}

func migrateOnly() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	fmt.Println("migraties uitgevoerd")
	return nil
}

func adminCreate(args []string) error {
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	username := fs.String("username", "", "gebruikersnaam (verplicht)")
	role := fs.String("role", "admin", "rol: admin of viewer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("-username is verplicht")
	}
	password, err := readPassword()
	if err != nil {
		return err
	}

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	ctx := context.Background()
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	q := store.New(pool)
	authSvc, err := auth.NewService(q, events.NewWriter(q, log), cfg.SessionTTL)
	if err != nil {
		return err
	}
	u, err := authSvc.CreateUser(ctx, events.System(), *username, password, store.UserRole(*role))
	if err != nil {
		return err
	}
	fmt.Printf("gebruiker %s aangemaakt met rol %s\n", u.Username, u.Role)
	return nil
}

// readPassword vraagt het wachtwoord twee keer in een terminal, of leest één
// regel van stdin als die geen terminal is (handig in scripts).
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("wachtwoord lezen van stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Wachtwoord: ")
	p1, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Herhaal wachtwoord: ")
	p2, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(p1) != string(p2) {
		return "", errors.New("wachtwoorden komen niet overeen")
	}
	return string(p1), nil
}
