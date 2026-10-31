// Package auth extracts sita credentials from HTTP requests.
//
// HTTP clients authenticate with their own sita account, either
//
//	Authorization: Basic base64(username:password)
//
// or with an existing website session
//
//	Authorization: Bearer <sita session id>
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

// FromHeader returns the credentials in the Authorization header of h.
func FromHeader(h http.Header) (sita.Credentials, bool) {
	value := h.Get("Authorization")
	scheme, param, ok := strings.Cut(value, " ")
	if !ok {
		return sita.Credentials{}, false
	}
	param = strings.TrimSpace(param)

	switch strings.ToLower(scheme) {
	case "basic":
		r := http.Request{Header: http.Header{"Authorization": {"Basic " + param}}}
		user, pw, ok := r.BasicAuth()
		if !ok || user == "" || pw == "" {
			return sita.Credentials{}, false
		}
		return sita.Credentials{Username: user, Password: pw}, true
	case "bearer":
		if param == "" {
			return sita.Credentials{}, false
		}
		return sita.Credentials{Session: param}, true
	}
	return sita.Credentials{}, false
}

// Verifier checks credentials against the backend.
type Verifier interface {
	Verify(ctx context.Context, creds sita.Credentials) error
}

// Guard protects the MCP endpoint: it rejects requests without valid sita
// credentials with 401 and throttles clients that keep failing with 429.
type Guard struct {
	Verifier Verifier
	// Limiter counts failed logins per client IP and per username. Nil disables throttling.
	Limiter *Limiter
	// ClientIPHeader names a header set by a trusted reverse proxy that carries the
	// client IP, e.g. X-Forwarded-For or X-Real-IP. Empty uses the connection address.
	ClientIPHeader string
	Logger         *slog.Logger
}

// Wrap returns next guarded by g.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creds, ok := FromHeader(r.Header)
		ip := clientIP(r, g.ClientIPHeader)
		keys := []string{"ip:" + ip}
		if creds.Username != "" {
			keys = append(keys, "user:"+strings.ToLower(creds.Username))
		}

		if g.Limiter != nil {
			for _, k := range keys {
				if blocked, retry := g.Limiter.Blocked(k); blocked {
					w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
					http.Error(w, "too many failed logins, try again later", http.StatusTooManyRequests)
					return
				}
			}
		}

		if !ok {
			// Not a guess, so not counted: a misconfigured client must not lock out a shared IP.
			unauthorized(w, "missing credentials: send 'Authorization: Basic <user:password>' or 'Authorization: Bearer <sita session>'")
			return
		}
		if err := g.Verifier.Verify(r.Context(), creds); err != nil {
			if errors.Is(err, sita.ErrLoginFailed) || errors.Is(err, sita.ErrUnauthorized) {
				g.fail(keys, ip, creds)
				unauthorized(w, err.Error())
				return
			}
			g.Logger.Error("verifying credentials failed", "error", err)
			http.Error(w, "sita-backend not reachable", http.StatusBadGateway)
			return
		}
		// Only the username is reset: resetting the IP would let a client with one
		// valid account interleave unlimited guesses for other accounts.
		if g.Limiter != nil && creds.Username != "" {
			g.Limiter.Reset("user:" + strings.ToLower(creds.Username))
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Guard) fail(keys []string, ip string, creds sita.Credentials) {
	g.Logger.Warn("failed login", "client_ip", ip, "username", creds.Username)
	if g.Limiter == nil {
		return
	}
	for _, k := range keys {
		g.Limiter.Fail(k)
	}
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
	w.Header().Set("WWW-Authenticate", `Basic realm="sita-mcp", charset="UTF-8"`)
	http.Error(w, msg, http.StatusUnauthorized)
}
