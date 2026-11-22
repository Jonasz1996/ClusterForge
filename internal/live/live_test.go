package live

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestPanicInRunKeepsStreamsOpen(t *testing.T) {
	// Zonder pool loopt listen meteen op een panic, zoals een fout in de lus.
	h := NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(t.Context())
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("verwachtte een panic")
			}
		}()
		h.Run(ctx)
	}()
	select {
	case <-h.done:
		t.Fatal("een panic sloot alle streams")
	default:
	}

	// Bij het afsluiten gaan de streams wel dicht, ook als Run daarna nog
	// eens stopt.
	cancel()
	h.Run(ctx)
	h.Run(ctx)
	select {
	case <-h.done:
	default:
		t.Fatal("streams bleven open na het afsluiten")
	}
}
