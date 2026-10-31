// Package auth extracts sita credentials from HTTP requests.
//
// HTTP clients authenticate with their personal MCP token from simpletactics.de,
// either in a header (Claude Code)
//
//	Authorization: Bearer <token>
//
// or in the URL, for clients that cannot set headers (Claude Desktop, claude.ai)
//
//	https://simpletactics.de/mcp/<token>
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

	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
)

// TokenPathValue is the name of the path wildcard that carries the token, as in "/mcp/{token}".
const TokenPathValue = "token"

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
	Verify(ctx context.Context, creds sita.Credentials) error
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
	Logger         *slog.Logger
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
			creds, ok = fromPath(r)
		}
		if !ok {
			// Not a guess, so not counted: a misconfigured client must not lock out a shared IP.
			unauthorized(w, "missing token: send 'Authorization: Bearer <MCP token>' or use the URL /mcp/<MCP token>; tokens are created on simpletactics.de")
			return
		}
		// The client caches the session per token, so this only reaches the backend on the first request.
		err := g.Verifier.Verify(r.Context(), creds)
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
		// Tool handlers read the token from the Authorization header, wherever it came from.
		// The token is also removed from the URL so that nothing downstream can log it.
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+creds.Token)
		r.URL.Path, r.URL.RawPath, r.RequestURI = "/mcp", "", "/mcp"
		r.SetPathValue(TokenPathValue, "")
		next.ServeHTTP(w, r)
	})
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

// fromPath returns the token from the "/mcp/{token}" route, if any.
func fromPath(r *http.Request) (sita.Credentials, bool) {
	token := strings.TrimSpace(r.PathValue(TokenPathValue))
	if token == "" {
		return sita.Credentials{}, false
	}
	return sita.Credentials{Token: token}, true
}
