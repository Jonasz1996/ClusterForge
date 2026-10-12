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
	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/disaster"
	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/httpapi"
	"github.com/Jonasz1996/clusterforge/internal/incidents"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/kubenodes"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/planner"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/reports"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
	"github.com/Jonasz1996/clusterforge/internal/secrets"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/internal/whatbroke"
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
	bus.SetJournalLogins(cfg.JournalLogins)
	hub := live.NewHub(pool, log)
	runner := jobs.NewRunner(pool, ev, log)
	pve := proxmox.NewService(pool, ev, log, box, runner)
	pve.Changed = eval.Kick
	life := lifecycle.NewService(pool, ev, log, runner, bus)
	life.Changed = eval.Kick
	dep := deploy.NewService(pool, ev, log, runner, box, pve, bus)
	dep.Changed = eval.Kick
	// Kubernetes via de kube-brug van de agents; ook de controle kube_ready
	// na een uitrol.
	kub := kube.NewService(pool, bus, log)
	kub.EnableDrain(runner, ev)
	kub.Metrics, kub.Changed = ingest, eval.Kick
	dep.Kube = kub
	// Een node van een Kubernetes-cluster gaat eerst leeg voor onderhoud of
	// een herstart.
	life.Kube = kub
	// Een worker weghalen: leegmaken, de VM uit en weg, en de node uit
	// ClusterForge.
	kinv := inventory.NewService(pool, ev)
	kinv.Disconnect = bus.Disconnect
	kn := kubenodes.NewService(pool, ev, runner, kub, pve, kinv, dep)
	// De MariaDB- en PostgreSQL-servers op de nodes, via agentprotocol 9.
	dbs := databases.NewService(pool, bus, log)
	// Een database als onderdeel van een cluster: MariaDB Galera op de
	// nodes die er al zijn, met het geheugengebruik uit VictoriaMetrics in
	// het plan.
	cmps := components.NewService(pool, ev, log, runner, bus, dep)
	cmps.Changed = eval.Kick
	cmps.DBChanged = dbs.Kick
	// Een database verhuizen begint bij een server onder Databases.
	cmps.Overview = dbs.Overview
	memory := metrics.NewClient(cfg.VictoriaMetricsURL)
	cmps.Memory = func(ctx context.Context) (map[uuid.UUID]float64, error) { return memory.MemoryP95(ctx, time.Now()) }
	runner.OnFinished(components.Kind, func(context.Context, store.Job) { dbs.Kick() })
	kn.Changed = eval.Kick
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
	drf.Components = cmps
	// Na een uitrol, een node- of VM-actie meteen opnieuw kijken; na een
	// rampoefening in elk cluster dat ze raakte.
	recheck := func(ctx context.Context, j store.Job) {
		switch {
		case j.NodeID != nil:
			drf.KickNode(*j.NodeID)
		case j.ClusterID != nil:
			for _, id := range append([]uuid.UUID{*j.ClusterID}, j.SlotClusters...) {
				if err := drf.Kick(ctx, &id); err != nil {
					log.Warn("driftcontrole na taak plannen mislukt", "job", j.ID, "err", err)
				}
			}
		}
	}
	fo := failover.NewService(pool, ev, log, runner, bus)
	fo.Changed = eval.Kick
	fo.Proxmox, fo.Window = pve, cfg.TestWindow
	// De assistent; zonder masterkey kan hij niet aan en zeggen de
	// instellingen waarom.
	asst := assistant.NewService(pool, ev, log, box)
	// De health score rekent elke minuut; de planner legt elke dag een
	// momentopname vast voor de trend.
	rsk := risk.NewService(pool, ev, log, metrics.NewClient(cfg.VictoriaMetricsURL), bk)
	// Het rapport Herstelbaarheid; de planner bewaart elke eerste van de
	// maand dat van de maand ervoor.
	rep := reports.NewService(pool, log)
	rep.Backups, rep.Risk = bk, rsk
	// Geplande failovertests, back-upcontroles en DR-repetities; als een
	// test klaar is en het testslot vrijkomt, kijkt de planner meteen of er
	// een wacht.
	var dis *disaster.Service
	sched := planner.NewScheduler(pool, log,
		planner.Task{Name: "failovertests", Tick: fo.RunScheduled},
		planner.Task{Name: "back-upcontroles", Tick: bk.RunScheduled},
		planner.Task{Name: "DR-repetities", Tick: func(ctx context.Context, now time.Time) error { return dis.RunScheduled(ctx, now) }},
		planner.Task{Name: "journaal opruimen", Tick: journal.NewPruner(q).Tick},
		planner.Task{Name: "gesprekken opruimen", Tick: asst.Prune},
		planner.Task{Name: "incidenten automatisch analyseren", Tick: asst.AutoAnalyze},
		planner.Task{Name: "health score vastleggen", Tick: func(ctx context.Context, now time.Time) error { return rsk.Snapshot(ctx, now) }},
		planner.Task{Name: "rapport Herstelbaarheid bewaren", Tick: rep.Save})
	// Rampoefeningen in lab en test; de wat-als voorspelt, de meting kijkt
	// of het klopte.
	dis = disaster.NewService(pool, ev, log, runner, bus)
	dis.Changed, dis.Probes = eval.Kick, fo.ProbeFor
	dis.Proxmox, dis.World = pve, rsk.World
	// De DR-repetitie draait als back-upcontrole van een heel cluster, ook
	// gepland in het testvenster.
	dis.Backups, dis.Window = bk, cfg.TestWindow
	for _, kind := range []string{failover.KindTest, backups.KindVerify, disaster.KindRun, backups.KindRehearsal} {
		runner.OnFinished(kind, func(context.Context, store.Job) { sched.Kick() })
	}
	ro := rollout.NewService(pool, ev, log, runner, bus, dep, drf)
	ro.Changed = eval.Kick
	// Een node die een nieuwe k3s krijgt, gaat eerst leeg.
	ro.Kube = kub
	for _, kind := range []string{deploy.Kind, lifecycle.KindNodeAction, proxmox.KindVMAction, failover.KindTest, failover.KindRestore,
		disaster.KindRun, disaster.KindRestore, components.Kind} {
		runner.OnFinished(kind, recheck)
	}
	// De status en impact van de diensten, en daarna de incidenten, rekenen
	// na elke ronde van de evaluator, na diens commit.
	dps := deps.NewService(pool, ev, log)
	// De wat-als en de SPOF's lezen de infrastructuur uit de Proxmox-sync en
	// de afhankelijkheden.
	tp := topo.NewService(pool, ev, log, cfg.NATSAdvertise)
	// Verbindingen naar NATS en de webinterface worden nooit een
	// afhankelijkheid.
	dps.Self = func(ctx context.Context) deps.Skip {
		urls, _ := q.ListGitServerURLs(ctx)
		return deps.SelfSkip(tp.OwnIPs(ctx), deps.Ports(append([]string{cfg.Listen, cfg.NATSListen, cfg.NATSAdvertise}, urls...)...))
	}
	rsk.Topo, rsk.Deps = tp, dps
	det := incidents.NewDetector(pool, ev, log)
	eval.After = func(ctx context.Context) error {
		return errors.Join(dps.Evaluate(ctx), det.Detect(ctx))
	}
	// What Broke rekent de oorzaak van elk incident, meteen als de detector
	// iets veranderde en daarna elke 30 s tot een uur na het einde.
	analyzer := whatbroke.NewAnalyzer(pool, metrics.NewClient(cfg.VictoriaMetricsURL), log)
	// De health score rekent ook meteen als een incident opende,
	// veranderde of sloot.
	det.Changed = func() {
		analyzer.Kick()
		rsk.Kick()
	}
	// GitOps leest de gekoppelde repository elke minuut; na een uitrol of
	// toepassing kijkt het meteen of een plan verouderd is.
	git := gitops.NewService(pool, ev, log, box, dep, ro)
	for _, kind := range []string{deploy.Kind, rollout.Kind} {
		runner.OnFinished(kind, func(context.Context, store.Job) { git.Kick(false) })
	}
	// De documentatie schrijft zichzelf opnieuw tien minuten na een
	// wijziging en elke nacht.
	dcs := docs.NewService(pool, log)
	dcs.Deploy, dcs.GitOps, dcs.Backups, dcs.Risk, dcs.Box, dcs.Events = dep, git, bk, rsk, box, ev
	dcs.Opts = docs.Opts{Usernames: cfg.DocsUsernames}
	go hub.Run(ctx)
	go eval.Run(ctx)
	go ingest.Run(ctx)
	go pve.Run(ctx)
	go bk.Run(ctx)
	go drf.Run(ctx)
	go sched.Run(ctx)
	go dps.Run(ctx)
	go analyzer.Run(ctx)
	go rsk.Run(ctx)
	go dcs.Run(ctx)
	go kub.Run(ctx)
	go dbs.Run(ctx)
	go cmps.Run(ctx)
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
			Lifecycle: life, Deploy: dep, Backups: bk, Drift: drf, Failover: fo, Disaster: dis, Deps: dps, Rollout: ro,
			GitOps: git, Assistant: asst, Risk: rsk, Topo: tp, Docs: dcs, Reports: rep, Kube: kub, KubeNodes: kn, Databases: dbs,
			Components: cmps, Version: version,
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
