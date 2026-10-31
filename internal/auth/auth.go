// Package auth extracts sita credentials from HTTP requests.
//
// HTTP clients authenticate with their personal MCP API token from simpletactics.de:
//
//	Authorization: Bearer <token>
package auth

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

// FromHeader returns the token in the Authorization header of h.
func FromHeader(h http.Header) (sita.Credentials, bool) {
	scheme, token, ok := strings.Cut(h.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return sita.Credentials{}, false
	}
	return sita.Credentials{Token: token}, true
}

// Verifier checks credentials against the backend.
type Verifier interface {
	Verify(ctx context.Context, creds sita.Credentials) (sita.TokenInfo, error)
}

// Guard protects the MCP endpoint: it rejects requests without a valid token
// with 401 and throttles clients that keep sending invalid tokens with 429.
type Guard struct {
	Verifier Verifier
	// Limiter counts rejected tokens per client IP. Nil disables throttling.
	Limiter *Limiter
	// ClientIPHeader names a header set by a trusted reverse proxy that carries the
	// client IP, e.g. X-Forwarded-For or X-Real-IP. Empty uses the connection address.
	ClientIPHeader string
	// VerifyCacheTTL is how long an accepted token is trusted without asking the
	// backend again. It bounds how long a revoked token keeps working. Zero disables the cache.
	VerifyCacheTTL time.Duration
	Logger         *slog.Logger

	mu       sync.Mutex
	verified map[string]time.Time // token key → valid until
	now      func() time.Time
}

// Wrap returns next guarded by g.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, g.ClientIPHeader)
		if g.Limiter != nil {
			if blocked, retry := g.Limiter.Blocked(ip); blocked {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
				http.Error(w, "too many invalid tokens, try again later", http.StatusTooManyRequests)
				return
			}
		}

		creds, ok := FromHeader(r.Header)
		if !ok {
			// Not a guess, so not counted: a misconfigured client must not lock out a shared IP.
			unauthorized(w, "missing token: send 'Authorization: Bearer <MCP token>'; tokens are created on simpletactics.de")
			return
		}
		if !g.cached(creds) {
			info, err := g.Verifier.Verify(r.Context(), creds)
			if errors.Is(err, sita.ErrUnauthorized) {
				g.Logger.Warn("rejected token", "client_ip", ip)
				if g.Limiter != nil {
					g.Limiter.Fail(ip)
				}
				unauthorized(w, err.Error())
				return
			}
			if err != nil {
				g.Logger.Error("verifying token failed", "error", err)
				http.Error(w, "sita-backend not reachable", http.StatusBadGateway)
				return
			}
			g.Logger.Debug("accepted token", "client_ip", ip, "username", info.Username, "token_name", info.Name)
			g.remember(creds)
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Guard) cached(creds sita.Credentials) bool {
	if g.VerifyCacheTTL <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.verified[creds.Key()]
	return ok && g.clock().Before(until)
}

func (g *Guard) remember(creds sita.Credentials) {
	if g.VerifyCacheTTL <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if g.verified == nil {
		g.verified = map[string]time.Time{}
	}
	for k, until := range g.verified {
		if !now.Before(until) {
			delete(g.verified, k)
		}
	}
	g.verified[creds.Key()] = now.Add(g.VerifyCacheTTL)
}

func (g *Guard) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// clientIP returns the client address of r. With a header from a trusted proxy,
// the last entry is used: it is the one the proxy itself appended.
func clientIP(r *http.Request, header string) string {
	if header != "" {
		if v := r.Header.Get(header); v != "" {
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="sita-mcp"`)
	http.Error(w, msg, http.StatusUnauthorized)
}
