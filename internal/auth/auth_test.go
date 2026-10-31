package auth_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
		{"Bearer sita_mcp_abc", sita.Credentials{Token: "sita_mcp_abc"}, true},
		{"bearer sita_mcp_abc ", sita.Credentials{Token: "sita_mcp_abc"}, true},
		{"Bearer ", sita.Credentials{}, false},
		{"Basic YWxpY2U6c2VjcmV0", sita.Credentials{}, false},
		{"sita_mcp_abc", sita.Credentials{}, false},
		{"", sita.Credentials{}, false},
	}
	for _, tt := range tests {
		got, ok := auth.FromHeader(http.Header{"Authorization": {tt.header}})
		if got != tt.want || ok != tt.ok {
			t.Errorf("FromHeader(%q) = %+v, %v; want %+v, %v", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}

// fakeVerifier accepts the token "good" and counts its calls.
type fakeVerifier struct{ calls atomic.Int32 }

func (f *fakeVerifier) Verify(_ context.Context, c sita.Credentials) error {
	f.calls.Add(1)
	if c.Token == "good" {
		return nil
	}
	return sita.ErrUnauthorized
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

var teapot = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

func serve(h http.Handler, remote string, headers map[string]string, path ...string) *httptest.ResponseRecorder {
	target := "/mcp"
	if len(path) > 0 {
		target = path[0]
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, target, nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestGuard(t *testing.T) {
	h := (&auth.Guard{Verifier: &fakeVerifier{}, Logger: discard}).Wrap(teapot)

	for header, want := range map[string]int{
		"":            http.StatusUnauthorized,
		"Bearer bad":  http.StatusUnauthorized,
		"Bearer good": http.StatusTeapot,
		"Basic YTpi":  http.StatusUnauthorized,
		"Bearer":      http.StatusUnauthorized,
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
	h := (&auth.Guard{Verifier: &fakeVerifier{}, Limiter: auth.NewLimiter(3, time.Hour), Logger: discard}).Wrap(teapot)

	for i := range 3 {
		if rec := serve(h, "192.0.2.1:1", bearer("guess")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, rec.Code)
		}
	}
	// The IP is blocked now, even for a valid token.
	rec := serve(h, "192.0.2.1:1", bearer("good"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked IP: status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Other clients are not affected.
	if rec := serve(h, "198.51.100.7:1", bearer("good")); rec.Code != http.StatusTeapot {
		t.Fatalf("other IP: status %d", rec.Code)
	}

	// Missing tokens are not counted.
	h2 := (&auth.Guard{Verifier: &fakeVerifier{}, Limiter: auth.NewLimiter(1, time.Hour), Logger: discard}).Wrap(teapot)
	serve(h2, "192.0.2.9:1", nil)
	if rec := serve(h2, "192.0.2.9:1", bearer("good")); rec.Code != http.StatusTeapot {
		t.Fatalf("missing token was counted: status %d", rec.Code)
	}
}

func TestGuardUsesProxyHeader(t *testing.T) {
	h := (&auth.Guard{Verifier: &fakeVerifier{}, Limiter: auth.NewLimiter(1, time.Hour), ClientIPHeader: "X-Forwarded-For", Logger: discard}).Wrap(teapot)

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

func TestGuardAcceptsTokenInURL(t *testing.T) {
	var seen *http.Request
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusTeapot)
	})
	mux := http.NewServeMux()
	guarded := (&auth.Guard{Verifier: &fakeVerifier{}, Logger: discard}).Wrap(next)
	mux.Handle("/mcp", guarded)
	mux.Handle("/mcp/{"+auth.TokenPathValue+"}", guarded)

	if rec := serve(mux, "192.0.2.1:1", nil, "/mcp/good"); rec.Code != http.StatusTeapot {
		t.Fatalf("token in URL: status %d", rec.Code)
	}
	// Downstream sees the token only in the header, not in the URL.
	if got := seen.Header.Get("Authorization"); got != "Bearer good" {
		t.Errorf("Authorization = %q", got)
	}
	if seen.URL.Path != "/mcp" || seen.RequestURI != "/mcp" || seen.PathValue(auth.TokenPathValue) != "" {
		t.Errorf("token still in request: path %q, uri %q, value %q", seen.URL.Path, seen.RequestURI, seen.PathValue(auth.TokenPathValue))
	}

	if rec := serve(mux, "192.0.2.1:1", nil, "/mcp/bad"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token in URL: status %d", rec.Code)
	}
	// The header wins over the URL.
	if rec := serve(mux, "192.0.2.1:1", bearer("bad"), "/mcp/good"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("header should take precedence: status %d", rec.Code)
	}
	if rec := serve(mux, "192.0.2.1:1", nil, "/mcp"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", rec.Code)
	}
}
