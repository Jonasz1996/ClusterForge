// Package live stuurt wijzigingen als Server-Sent Events naar de webinterface.
//
// Elk event in de database gaat na de commit als notificatie op het kanaal
// cf_events (zie migratie 00004). De hub luistert daarop en geeft elke
// notificatie door aan alle open streams; de webinterface laadt daarop de
// betrokken gegevens opnieuw.
package live

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const channel = "cf_events"

const (
	pingInterval = 25 * time.Second
	// maxStream is hoe lang één stream openblijft. De browser verbindt daarna
	// vanzelf opnieuw, en de sessie wordt dan opnieuw gecontroleerd.
	maxStream = 5 * time.Minute
)

type Hub struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu   sync.Mutex
	subs map[chan string]struct{}
	done chan struct{}
}

func NewHub(pool *pgxpool.Pool, log *slog.Logger) *Hub {
	return &Hub{pool: pool, log: log, subs: map[chan string]struct{}{}, done: make(chan struct{})}
}

// Run luistert op PostgreSQL tot ctx afloopt en sluit daarna alle streams.
func (h *Hub) Run(ctx context.Context) {
	defer close(h.done)
	wait := time.Second
	for ctx.Err() == nil {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		h.log.Warn("luisteren naar events onderbroken", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, 30*time.Second)
	}
}

func (h *Hub) listen(ctx context.Context) error {
	pc, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// Een verbinding met LISTEN gaat niet terug naar de pool.
	conn := pc.Hijack()
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		h.publish(n.Payload)
	}
}

func (h *Hub) publish(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			// Een trage browser mist een bericht; de volgende brengt hem weer
			// bij, en de webinterface ververst ook zelf af en toe.
		}
	}
}

func (h *Hub) subscribe() chan string {
	ch := make(chan string, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// ServeHTTP houdt een stream open met een event "change" per wijziging.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// De server heeft een schrijftimeout voor gewone requests; deze stream
	// blijft langer open.
	_ = rc.SetWriteDeadline(time.Now().Add(maxStream + time.Minute))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// nginx buffert anders de hele stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := h.subscribe()
	defer h.unsubscribe(ch)
	if _, err := w.Write([]byte("retry: 3000\n\n")); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	end := time.NewTimer(maxStream)
	defer end.Stop()
	for {
		var out string
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case <-end.C:
			return
		case <-ping.C:
			out = ": ping\n\n"
		case msg := <-ch:
			out = "event: change\ndata: " + msg + "\n\n"
		}
		if _, err := w.Write([]byte(out)); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
