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

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/config"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/inventory"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/webui"
)

type Server struct {
	cfg          config.Config
	log          *slog.Logger
	pool         *pgxpool.Pool
	q            *store.Queries
	auth         *auth.Service
	inv          *inventory.Service
	version      string
	loginLimiter *ipLimiter
}

var _ gen.ServerInterface = (*Server)(nil)

func New(cfg config.Config, log *slog.Logger, pool *pgxpool.Pool, authSvc *auth.Service, version string) *Server {
	return &Server{
		cfg:     cfg,
		log:     log,
		pool:    pool,
		q:       store.New(pool),
		auth:    authSvc,
		inv:     inventory.NewService(pool, events.NewWriter(store.New(pool), log)),
		version: version,
		// 10 pogingen direct, daarna één per 6 seconden per IP-adres.
		loginLimiter: newIPLimiter(6*time.Second, 10),
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
	r.Handle("/*", webui.Handler())
	return r
}
