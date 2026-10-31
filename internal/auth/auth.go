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
	"net/http"
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

// Require rejects requests without valid sita credentials with 401.
func Require(v Verifier, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creds, ok := FromHeader(r.Header)
		if !ok {
			unauthorized(w, "missing credentials: send 'Authorization: Basic <user:password>' or 'Authorization: Bearer <sita session>'")
			return
		}
		if err := v.Verify(r.Context(), creds); err != nil {
			if errors.Is(err, sita.ErrLoginFailed) || errors.Is(err, sita.ErrUnauthorized) {
				unauthorized(w, err.Error())
				return
			}
			logger.Error("verifying credentials failed", "error", err)
			http.Error(w, "sita-backend not reachable", http.StatusBadGateway)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="sita-mcp", charset="UTF-8"`)
	http.Error(w, msg, http.StatusUnauthorized)
}
