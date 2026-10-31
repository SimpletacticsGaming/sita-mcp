package auth_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestRequire(t *testing.T) {
	v := verifierFunc(func(c sita.Credentials) error {
		if c.Session == "good" {
			return nil
		}
		return sita.ErrUnauthorized
	})
	h := auth.Require(v, slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	for header, want := range map[string]int{
		"":             http.StatusUnauthorized,
		"Bearer bad":   http.StatusUnauthorized,
		"Bearer good":  http.StatusTeapot,
		"Basic broken": http.StatusUnauthorized,
	} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%q: status %d, want %d", header, rec.Code, want)
		}
		if want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%q: missing WWW-Authenticate", header)
		}
	}
}
