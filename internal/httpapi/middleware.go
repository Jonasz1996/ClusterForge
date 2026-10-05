package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
)

const (
	sessionCookie = "cf_session"
	csrfHeader    = "X-CSRF-Token"
)

type ctxKey int

const principalKey ctxKey = iota

func principalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey).(auth.Principal)
	return p, ok
}

// publicRoutes hebben geen sessie nodig.
var publicRoutes = map[string]bool{
	"GET /api/v1/health":         true,
	"POST /api/v1/auth/login":    true,
	"POST /api/v1/agents/enroll": true,
}

// requireSession laat alleen requests met een geldige sessie door en eist
// een CSRF-token op alles wat iets kan wijzigen.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicRoutes[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r.WithContext(events.WithRequest(r.Context(), clientIP(r), nil)))
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "niet ingelogd")
			return
		}
		p, err := s.auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			if err != auth.ErrNoSession {
				s.log.ErrorContext(r.Context(), "sessie controleren", "err", err)
				writeError(w, http.StatusInternalServerError, "internal", "interne fout")
				return
			}
			s.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sessie verlopen")
			return
		}
		if !isSafeMethod(r.Method) {
			got := r.Header.Get(csrfHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(p.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "csrf", "ontbrekende of onjuiste CSRF-token")
				return
			}
		}
		if p.Refreshed {
			s.setSessionCookie(w, c.Value, p.ExpiresAt)
		}
		// Elk event van deze request krijgt het IP en de sessie mee.
		ctx := events.WithRequest(context.WithValue(r.Context(), principalKey, p), clientIP(r), p.SessionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		// De Next.js-export gebruikt inline scripts voor hydratatie.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.LogAttrs(r.Context(), slog.LevelDebug, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Duration("duration", time.Since(start)),
				slog.String("ip", clientIP(r)),
			)
		})
	}
}

// forwardedClientIP neemt het client-IP over uit X-Forwarded-For. Alleen het
// laatste adres telt: dat heeft de eigen reverse proxy toegevoegd, de
// adressen ervoor kan de client zelf verzinnen.
func forwardedClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipLimiter beperkt het aantal inlogpogingen per IP-adres.
type ipLimiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(every time.Duration, burst int) *ipLimiter {
	return &ipLimiter{every: rate.Every(every), burst: burst, entries: map[string]*limiterEntry{}}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.entries) > 10000 {
		for k, e := range l.entries {
			if now.Sub(e.seen) > time.Hour {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[ip]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(l.every, l.burst)}
		l.entries[ip] = e
	}
	e.seen = now
	return e.lim.Allow()
}
