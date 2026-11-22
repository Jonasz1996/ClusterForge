package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPLimiter(t *testing.T) {
	l := newIPLimiter(time.Hour, 3)
	for i := 0; i < 3; i++ {
		if !l.allow("10.0.0.1") {
			t.Fatalf("poging %d geweigerd binnen de burst", i+1)
		}
	}
	if l.allow("10.0.0.1") {
		t.Fatal("vierde poging toegestaan")
	}
	if !l.allow("10.0.0.2") {
		t.Fatal("ander IP-adres geweigerd")
	}
}

func TestForwardedClientIP(t *testing.T) {
	cases := map[string]string{
		"":                     "192.0.2.1",
		"203.0.113.7":          "203.0.113.7",
		"1.1.1.1, 203.0.113.7": "203.0.113.7",
		"203.0.113.7, onzin":   "192.0.2.1",
		"2001:db8::1":          "2001:db8::1",
	}
	for xff, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "192.0.2.1:5000"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		var got string
		forwardedClientIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = clientIP(r) })).
			ServeHTTP(httptest.NewRecorder(), r)
		if got != want {
			t.Errorf("X-Forwarded-For %q: %s, want %s", xff, got, want)
		}
	}
}
