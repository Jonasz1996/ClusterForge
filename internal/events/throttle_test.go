package events

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/store/storetest"
)

func TestThrottle(t *testing.T) {
	pool := storetest.DB(t)
	ctx := context.Background()
	th := NewThrottle(NewWriter(store.New(pool), slog.New(slog.NewTextHandler(io.Discard, nil))))
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	th.Now = func() time.Time { return now }
	var planned []time.Duration
	var summaries []func()
	th.After = func(d time.Duration, f func()) {
		planned = append(planned, d)
		summaries = append(summaries, f)
	}
	write := func(ip, action string) {
		th.Write(ctx, ip, Event{Actor: System(), SubjectType: "auth", SubjectID: "login", Action: action})
	}
	count := func(action string) int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE action = $1", action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Per adres hoogstens drie, per action hoogstens tien.
	for range 4 {
		write("10.0.0.1", "auth.rate_limited")
	}
	now = now.Add(20 * time.Second)
	for i := range 12 {
		write(fmt.Sprintf("10.0.1.%d", i), "auth.rate_limited")
	}
	write("10.0.0.1", "agent.enroll_failed")
	if n := count("auth.rate_limited"); n != 10 {
		t.Fatalf("%d keer auth.rate_limited, verwacht 10", n)
	}
	if n := count("agent.enroll_failed"); n != 1 {
		t.Fatalf("een andere action heeft een eigen venster: %d", n)
	}
	// Eén samenvatting, aan het eind van het venster.
	if len(planned) != 1 || planned[0] != time.Minute {
		t.Fatalf("gepland: %v", planned)
	}
	summaries[0]()
	var payload string
	if err := pool.QueryRow(ctx, "SELECT payload::text FROM events WHERE action = 'audit.throttled'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"action": "auth.rate_limited", "omitted": 6, "seconds": 60}` {
		t.Fatalf("samenvatting: %s", payload)
	}

	// Een nieuw venster begint opnieuw te tellen.
	now = now.Add(time.Minute)
	write("10.0.0.1", "auth.rate_limited")
	if n := count("auth.rate_limited"); n != 11 {
		t.Fatalf("na het venster: %d", n)
	}
}
