package auth_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SimpletacticsGaming/sita-mcp/internal/auth"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

func TestFromHeader(t *testing.T) {
	tests := []struct {
		header string
		want   sita.Credentials
		ok     bool
	}{
		{"Basic YWxpY2U6c2VjcmV0", sita.Credentials{Username: "alice", Password: "secret"}, true},
		{"basic YWxpY2U6c2VjcmV0", sita.Credentials{Username: "alice", Password: "secret"}, true},
		{"Bearer 1234-abcd", sita.Credentials{Session: "1234-abcd"}, true},
		{"Basic YWxpY2U6", sita.Credentials{}, false}, // empty password
		{"Basic !!!", sita.Credentials{}, false},
		{"Bearer ", sita.Credentials{}, false},
		{"Token x", sita.Credentials{}, false},
		{"", sita.Credentials{}, false},
	}
	for _, tt := range tests {
		got, ok := auth.FromHeader(http.Header{"Authorization": {tt.header}})
		if got != tt.want || ok != tt.ok {
			t.Errorf("FromHeader(%q) = %+v, %v; want %+v, %v", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}

type verifierFunc func(sita.Credentials) error

func (f verifierFunc) Verify(_ context.Context, c sita.Credentials) error { return f(c) }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// alice/secret and the session "good" are valid, everything else fails.
var verifier = verifierFunc(func(c sita.Credentials) error {
	if c.Session == "good" || (c.Username == "alice" && c.Password == "secret") {
		return nil
	}
	if c.Session != "" {
		return sita.ErrUnauthorized
	}
	return sita.ErrLoginFailed
})

var teapot = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

func serve(h http.Handler, remote string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func basic(user, pw string) map[string]string {
	r := http.Request{Header: http.Header{}}
	r.SetBasicAuth(user, pw)
	return map[string]string{"Authorization": r.Header.Get("Authorization")}
}

func TestGuard(t *testing.T) {
	h := (&auth.Guard{Verifier: verifier, Logger: discard}).Wrap(teapot)

	for header, want := range map[string]int{
		"":             http.StatusUnauthorized,
		"Bearer bad":   http.StatusUnauthorized,
		"Bearer good":  http.StatusTeapot,
		"Basic broken": http.StatusUnauthorized,
	} {
		rec := serve(h, "192.0.2.1:1234", map[string]string{"Authorization": header})
		if rec.Code != want {
			t.Errorf("%q: status %d, want %d", header, rec.Code, want)
		}
		if want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%q: missing WWW-Authenticate", header)
		}
	}
}

func TestGuardBlocksIPAfterFailures(t *testing.T) {
	h := (&auth.Guard{Verifier: verifier, Limiter: auth.NewLimiter(3, time.Hour), Logger: discard}).Wrap(teapot)

	for i, user := range []string{"bob", "carol", "dave"} {
		if rec := serve(h, "192.0.2.1:1", basic(user, "guess")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, rec.Code)
		}
	}
	// The IP is blocked now, even for valid credentials.
	rec := serve(h, "192.0.2.1:1", basic("alice", "secret"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked IP: status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Other clients are not affected.
	if rec := serve(h, "198.51.100.7:1", basic("alice", "secret")); rec.Code != http.StatusTeapot {
		t.Fatalf("other IP: status %d", rec.Code)
	}
	// Missing credentials are not counted.
	h2 := (&auth.Guard{Verifier: verifier, Limiter: auth.NewLimiter(1, time.Hour), Logger: discard}).Wrap(teapot)
	serve(h2, "192.0.2.9:1", nil)
	if rec := serve(h2, "192.0.2.9:1", basic("alice", "secret")); rec.Code != http.StatusTeapot {
		t.Fatalf("missing credentials were counted: status %d", rec.Code)
	}
}

func TestGuardBlocksUsernameAcrossIPs(t *testing.T) {
	h := (&auth.Guard{Verifier: verifier, Limiter: auth.NewLimiter(2, time.Hour), Logger: discard}).Wrap(teapot)

	serve(h, "192.0.2.1:1", basic("alice", "wrong1"))
	serve(h, "192.0.2.2:1", basic("Alice", "wrong2"))
	if rec := serve(h, "192.0.2.3:1", basic("alice", "secret")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("username not blocked: status %d", rec.Code)
	}
}

func TestGuardUsesProxyHeader(t *testing.T) {
	h := (&auth.Guard{Verifier: verifier, Limiter: auth.NewLimiter(1, time.Hour), ClientIPHeader: "X-Forwarded-For", Logger: discard}).Wrap(teapot)

	// Both requests come through the same proxy but from different clients.
	serve(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "spoofed, 192.0.2.1", "Authorization": "Bearer bad"})
	rec := serve(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "192.0.2.2", "Authorization": "Bearer good"})
	if rec.Code != http.StatusTeapot {
		t.Fatalf("client behind same proxy was blocked: status %d", rec.Code)
	}
	rec = serve(h, "10.0.0.1:1", map[string]string{"X-Forwarded-For": "192.0.2.1", "Authorization": "Bearer good"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("failing client not blocked: status %d", rec.Code)
	}
}
