// Package httpapi bevat de HTTP-server: de REST-API onder /api/v1 en de
// ingebedde webinterface op alle andere paden.
package httpapi

import (
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentdist"
	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/components"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/cronjobs"
	"github.com/Jonasz1996/clusterforge/internal/databases"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/disaster"
	"github.com/Jonasz1996/clusterforge/internal/docs"
	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/failover"
	"github.com/Jonasz1996/clusterforge/internal/gitops"
	"github.com/Jonasz1996/clusterforge/internal/heal"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/kubelessons"
	"github.com/Jonasz1996/clusterforge/internal/kubenodes"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/reports"
	"github.com/Jonasz1996/clusterforge/internal/risk"
	"github.com/Jonasz1996/clusterforge/internal/rollout"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/topo"
	"github.com/Jonasz1996/clusterforge/internal/webui"
)

type Server struct {
	cfg          config.Config
	log          *slog.Logger
	pool         *pgxpool.Pool
	q            *store.Queries
	ev           *events.Writer
	auth         *auth.Service
	audit        *audit.Service
	inv          *inventory.Service
	agents       *agents.Service
	bus          AgentBus
	hub          *live.Hub
	metrics      *metrics.Client
	pve          *proxmox.Service
	jobs         *jobs.Runner
	life         *lifecycle.Service
	deploy       *deploy.Service
	backups      *backups.Service
	drift        *drift.Service
	failover     *failover.Service
	disaster     *disaster.Service
	deps         *deps.Service
	rollout      *rollout.Service
	git          *gitops.Service
	assistant    *assistant.Service
	risk         *risk.Service
	topo         *topo.Service
	docs         *docs.Service
	reports      *reports.Service
	kube         *kube.Service
	databases    *databases.Service
	cronjobs     *cronjobs.Service
	heal         *heal.Service
	components   *components.Service
	kubeNodes    *kubenodes.Service
	lessons      *kubelessons.Engine
	version      string
	loginLimiter *ipLimiter
	// loginTimeout en sessionTimeout begrenzen het databasewerk van een login
	// en van de sessiecontrole voor elke API-call. Een volle pool of een slot
	// in PostgreSQL geeft dan een fout in plaats van een request die blijft
	// hangen.
	loginTimeout   time.Duration
	sessionTimeout time.Duration
	// enrollLimiter remt het raden van enrollmenttokens.
	enrollLimiter *ipLimiter
	// throttle remt events die een aanvaller gratis kan laten schrijven.
	throttle *events.Throttle
}

var _ gen.ServerInterface = (*Server)(nil)

// Deps zijn de onderdelen die de server gebruikt; main maakt ze aan en
// start hun achtergrondwerk.
type Deps struct {
	Config  config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Auth    *auth.Service
	Bus     AgentBus
	Hub     *live.Hub
	Proxmox *proxmox.Service
	Jobs    *jobs.Runner
	// Lifecycle voert acties op nodes uit via hun agent.
	Lifecycle *lifecycle.Service
	// Deploy rolt clusters uit templates uit.
	Deploy *deploy.Service
	// Backups leest de back-ups uit Proxmox en bewaakt hun versheid.
	Backups *backups.Service
	// Drift vergelijkt de nodes met de gewenste staat.
	Drift *drift.Service
	// Failover voert failovertests uit.
	Failover *failover.Service
	// Disaster voert rampoefeningen uit.
	Disaster *disaster.Service
	// Deps beheert diensten en afhankelijkheden.
	Deps *deps.Service
	// Rollout past wijzigingen toe met cluster.apply, zoals herstel.
	Rollout *rollout.Service
	// GitOps leest clusters uit een Git-repository en plant wijzigingen.
	GitOps *gitops.Service
	// Assistant beantwoordt vragen met de Anthropic API; de server geeft
	// hem zijn tools.
	Assistant *assistant.Service
	// Risk rekent de health score.
	Risk *risk.Service
	// Topo leest de infrastructuur voor de wat-als.
	Topo *topo.Service
	// Docs schrijft en bewaart de documentatie.
	Docs *docs.Service
	// Reports maakt de rapporten op /rapporten.
	Reports *reports.Service
	// Kube praat met de Kubernetes-clusters via de kube-brug.
	Kube *kube.Service
	// KubeNodes haalt workers weg uit een Kubernetes-cluster.
	KubeNodes *kubenodes.Service
	// Databases leest de MariaDB- en PostgreSQL-servers op de nodes.
	Databases *databases.Service
	Cronjobs  *cronjobs.Service
	// Heal is Zelfherstel: problemen met de databases vaststellen en
	// herstellen.
	Heal *heal.Service
	// Components zet een database op de nodes van een cluster.
	Components *components.Service
	Version    string
}

func New(d Deps) *Server {
	ev := events.NewWriter(store.New(d.Pool), d.Log)
	inv := inventory.NewService(d.Pool, ev)
	inv.Disconnect = d.Bus.Disconnect
	s := &Server{
		cfg:      d.Config,
		log:      d.Log,
		pool:     d.Pool,
		q:        store.New(d.Pool),
		ev:       ev,
		auth:     d.Auth,
		audit:    audit.NewService(store.New(d.Pool)),
		inv:      inv,
		agents:   agents.NewService(d.Pool, ev, d.Bus),
		bus:      d.Bus,
		hub:      d.Hub,
		metrics:  metrics.NewClient(d.Config.VictoriaMetricsURL),
		pve:      d.Proxmox,
		jobs:     d.Jobs,
		life:     d.Lifecycle,
		deploy:   d.Deploy,
		backups:  d.Backups,
		drift:    d.Drift,
		failover: d.Failover,
		disaster: d.Disaster,
		deps:     d.Deps,
		rollout:  d.Rollout,
		git:      d.GitOps,
		version:  d.Version,
		// 10 pogingen direct, daarna één per 6 seconden per IP-adres.
		loginLimiter:   newIPLimiter(6*time.Second, 10),
		loginTimeout:   20 * time.Second,
		sessionTimeout: 10 * time.Second,
		enrollLimiter:  newIPLimiter(6*time.Second, 20),
		throttle:       events.NewThrottle(ev),
		assistant:      d.Assistant,
		risk:           d.Risk,
		topo:           d.Topo,
		docs:           d.Docs,
		reports:        d.Reports,
		kube:           d.Kube,
		databases:      d.Databases,
		cronjobs:       d.Cronjobs,
		heal:           d.Heal,
		components:     d.Components,
		kubeNodes:      d.KubeNodes,
	}
	s.assistant.Tools = slices.Concat(s.assistantTools(), s.kubeTools(), s.databaseTools(), s.cronTools())
	s.lessons = kubelessons.New(s.kube, s.q)
	return s
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxyHeaders {
		r.Use(forwardedClientIP)
	}
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(s.log))
	r.Use(securityHeaders)

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(middleware.NoCache)
		gen.HandlerWithOptions(s, gen.ChiServerOptions{
			BaseRouter:  api,
			Middlewares: []gen.MiddlewareFunc{s.requireSession},
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			},
		})
		api.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "onbekend endpoint")
		})
		api.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "methode niet toegestaan")
		})
	})
	r.Get("/install/agent.sh", agentdist.InstallScript)
	r.Get("/install/golden-image.sh", agentdist.GoldenImageScript)
	r.Get("/downloads/*", agentdist.Downloads(s.cfg.AgentDir))
	r.Head("/downloads/*", agentdist.Downloads(s.cfg.AgentDir))
	r.Handle("/*", webui.Handler())
	return r
}
