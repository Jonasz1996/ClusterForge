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
	// De tijdzones zitten in de binary, ook in een image zonder tzdata: het
	// testvenster rekent met zomer- en wintertijd.
	_ "time/tzdata"

	"github.com/google/uuid"
	"golang.org/x/term"

	"github.com/Jonasz1996/clusterforge/internal/agentbus"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/httpapi"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
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

	var box *secrets.Box
	if cfg.MasterKey != nil {
		if box, err = secrets.New(cfg.MasterKey); err != nil {
			return err
		}
	} else {
		log.Warn("CF_MASTER_KEY is niet gezet; Proxmox koppelen kan pas met een masterkey")
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
	bus.SetFingerprintKey(box.Derive(secrets.PurposeFile))
	hub := live.NewHub(pool, log)
	runner := jobs.NewRunner(pool, ev, log)
	pve := proxmox.NewService(pool, ev, log, box, runner)
	pve.Changed = eval.Kick
	life := lifecycle.NewService(pool, ev, log, runner, bus)
	life.Changed = eval.Kick
	dep := deploy.NewService(pool, ev, log, runner, box, pve, bus)
	dep.Changed = eval.Kick
	// Een cluster rendert alleen met de templateversie waarmee het is
	// uitgerold; zit die niet in deze binary, dan moet de beheerder dat weten.
	if missing, err := dep.MissingTemplates(ctx); err != nil {
		log.Error("templateversies controleren mislukt", "err", err)
	} else {
		for _, m := range missing {
			log.Error("templateversie ontbreekt in deze server; zet de server terug op een versie die haar kent", "cluster", m)
		}
	}
	bk := backups.NewService(pool, ev, log, pve)
	bk.SandboxStorage, bk.BootTimeout, bk.Window = cfg.SandboxStorage, cfg.SandboxBootTimeout, cfg.TestWindow
	bk.Running = dep.Running
	bk.EnableVerify(runner, bus)
	drf := drift.NewService(pool, ev, log, bus, dep, box.Derive(secrets.PurposeFile))
	drf.Interval = cfg.DriftInterval
	// Na een uitrol, een node- of VM-actie meteen opnieuw kijken.
	recheck := func(ctx context.Context, j store.Job) {
		switch {
		case j.NodeID != nil:
			drf.KickNode(*j.NodeID)
		case j.ClusterID != nil:
			if err := drf.Kick(ctx, j.ClusterID); err != nil {
				log.Warn("driftcontrole na taak plannen mislukt", "job", j.ID, "err", err)
			}
		}
	}
	fo := failover.NewService(pool, ev, log, runner, bus)
	fo.Changed = eval.Kick
	fo.Proxmox, fo.Window = pve, cfg.TestWindow
	// Geplande failovertests en back-upcontroles; als een test klaar is en
	// het testslot vrijkomt, kijkt de planner meteen of er een wacht.
	sched := planner.NewScheduler(pool, log,
		planner.Task{Name: "failovertests", Tick: fo.RunScheduled},
		planner.Task{Name: "back-upcontroles", Tick: bk.RunScheduled})
	for _, kind := range []string{failover.KindTest, backups.KindVerify} {
		runner.OnFinished(kind, func(context.Context, store.Job) { sched.Kick() })
	}
	ro := rollout.NewService(pool, ev, log, runner, bus, dep, drf)
	ro.Changed = eval.Kick
	for _, kind := range []string{deploy.Kind, lifecycle.KindNodeAction, proxmox.KindVMAction, failover.KindTest, failover.KindRestore} {
		runner.OnFinished(kind, recheck)
	}
	// De status en impact van de diensten rekenen na elke ronde van de
	// evaluator, na diens commit.
	dps := deps.NewService(pool, ev, log)
	eval.After = dps.Evaluate
	// GitOps leest de gekoppelde repository elke minuut; na een uitrol of
	// toepassing kijkt het meteen of een plan verouderd is.
	git := gitops.NewService(pool, ev, log, box, dep, ro)
	for _, kind := range []string{deploy.Kind, rollout.Kind} {
		runner.OnFinished(kind, func(context.Context, store.Job) { git.Kick(false) })
	}
	go hub.Run(ctx)
	go eval.Run(ctx)
	go ingest.Run(ctx)
	go pve.Run(ctx)
	go bk.Run(ctx)
	go drf.Run(ctx)
	go sched.Run(ctx)
	go dps.Run(ctx)
	if git.Enabled() {
		go git.Run(ctx)
	}
	jobsDone := make(chan struct{})
	go func() {
		defer close(jobsDone)
		runner.Run(ctx)
	}()

	log.Info("testvenster", "venster", cfg.TestWindow.String(), "tijdzone", time.Local.String())

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: httpapi.New(httpapi.Deps{
			Config: cfg, Log: log, Pool: pool, Auth: authSvc, Bus: bus, Hub: hub, Proxmox: pve, Jobs: runner,
			Lifecycle: life, Deploy: dep, Backups: bk, Drift: drf, Failover: fo, Deps: dps, Rollout: ro, GitOps: git,
			Version: version,
		}).Handler(),
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
		// Lopende taken zetten zichzelf terug in de wachtrij en gaan na de
		// herstart verder.
		select {
		case <-jobsDone:
		case <-shutdownCtx.Done():
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
