package events

import (
	"context"
	"sync"
	"time"
)

// Throttle remt events die een aanvaller of een kapotte agent gratis kan
// laten schrijven, zoals mislukte aanmeldingen: per action en per IP
// hoogstens PerIP per venster, en per action hoogstens PerAction. Wat
// wegvalt, telt hij; aan het eind van het venster volgt één event
// audit.throttled met het aantal.
type Throttle struct {
	w         *Writer
	PerIP     int
	PerAction int
	Window    time.Duration
	// Now is de klok; tests zetten hem vooruit.
	Now func() time.Time
	// After plant de samenvatting; tests vervangen het.
	After func(d time.Duration, f func())

	mu      sync.Mutex
	windows map[string]*throttleWindow
}

type throttleWindow struct {
	start   time.Time
	total   int
	perIP   map[string]int
	omitted int
}

func NewThrottle(w *Writer) *Throttle {
	return &Throttle{
		w: w, PerIP: 3, PerAction: 10, Window: time.Minute, Now: time.Now,
		After:   func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		windows: map[string]*throttleWindow{},
	}
}

// Write schrijft e, tenzij de drossel hem tegenhoudt. ip is het adres van
// de aanvrager.
func (t *Throttle) Write(ctx context.Context, ip string, e Event) {
	t.mu.Lock()
	now := t.Now()
	win := t.windows[e.Action]
	if win == nil || now.Sub(win.start) >= t.Window {
		win = &throttleWindow{start: now, perIP: map[string]int{}}
		t.windows[e.Action] = win
	}
	allowed := win.total < t.PerAction && win.perIP[ip] < t.PerIP
	if allowed {
		win.total++
		win.perIP[ip]++
	} else {
		win.omitted++
		if win.omitted == 1 {
			action := e.Action
			t.After(win.start.Add(t.Window).Sub(now), func() { t.summarize(action, win) })
		}
	}
	t.mu.Unlock()
	if allowed {
		_ = t.w.Write(ctx, nil, e)
	}
}

func (t *Throttle) summarize(action string, win *throttleWindow) {
	t.mu.Lock()
	n := win.omitted
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.w.Write(ctx, nil, Event{
		Actor: System(), SubjectType: "audit", SubjectID: action, Action: "audit.throttled",
		Payload: map[string]any{"action": action, "omitted": n, "seconds": int(t.Window.Seconds())},
	})
}
