// Package httpapi bevat de HTTP-server: de REST-API onder /api/v1 en de
// ingebedde webinterface op alle andere paden.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jonasz1996/clusterforge/internal/agentdist"
	"github.com/Jonasz1996/clusterforge/internal/agents"
	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/deploy"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/lifecycle"
	"github.com/Jonasz1996/clusterforge/internal/live"
	"github.com/Jonasz1996/clusterforge/internal/metrics"
	"github.com/Jonasz1996/clusterforge/internal/proxmox"
	"github.com/Jonasz1996/clusterforge/internal/store"
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
	version      string
	loginLimiter *ipLimiter
	// enrollLimiter remt het raden van enrollmenttokens.
	enrollLimiter *ipLimiter
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
	Version string
}

func New(d Deps) *Server {
	ev := events.NewWriter(store.New(d.Pool), d.Log)
	inv := inventory.NewService(d.Pool, ev)
	inv.Disconnect = d.Bus.Disconnect
	return &Server{
		cfg:     d.Config,
		log:     d.Log,
		pool:    d.Pool,
		q:       store.New(d.Pool),
		ev:      ev,
		auth:    d.Auth,
		audit:   audit.NewService(store.New(d.Pool)),
		inv:     inv,
		agents:  agents.NewService(d.Pool, ev, d.Bus),
		bus:     d.Bus,
		hub:     d.Hub,
		metrics: metrics.NewClient(d.Config.VictoriaMetricsURL),
		pve:     d.Proxmox,
		jobs:    d.Jobs,
		life:    d.Lifecycle,
		deploy:  d.Deploy,
		backups: d.Backups,
		version: d.Version,
		// 10 pogingen direct, daarna één per 6 seconden per IP-adres.
		loginLimiter:  newIPLimiter(6*time.Second, 10),
		enrollLimiter: newIPLimiter(6*time.Second, 20),
	}
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
