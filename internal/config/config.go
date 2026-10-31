// Package config reads the server configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Transport selects how MCP clients talk to the server.
type Transport string

const (
	// TransportStdio is used when the client starts the server as a subprocess,
	// e.g. `docker run -i ...` from Claude Code or Claude Desktop.
	TransportStdio Transport = "stdio"
	// TransportHTTP serves the Streamable HTTP transport for a long-running deployment.
	TransportHTTP Transport = "http"
)

// Config holds all runtime settings.
type Config struct {
	Transport Transport
	HTTPAddr  string

	BackendURL    string
	WikiPublicURL string
	AuthHeader    string

	// Credentials for stdio mode. In HTTP mode every client sends its own.
	Username string
	Password string
	Session  string

	IndexTTL      time.Duration
	IndexMaxNodes int

	// Failed-login throttling in HTTP mode, per client IP and per username.
	LoginMaxFailures int
	LoginBlockWindow time.Duration
	// ClientIPHeader is a header set by the trusted reverse proxy, e.g. X-Forwarded-For.
	ClientIPHeader string

	LogLevel slog.Level
}

// Load builds a Config from getenv, typically os.Getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Transport:     Transport(strings.ToLower(withDefault(getenv("MCP_TRANSPORT"), string(TransportStdio)))),
		HTTPAddr:      withDefault(getenv("MCP_HTTP_ADDR"), ":8080"),
		BackendURL:    strings.TrimRight(withDefault(getenv("SITA_BACKEND_URL"), "https://simpletactics.de/backend"), "/"),
		WikiPublicURL: withDefault(getenv("SITA_WIKI_PUBLIC_URL"), "https://simpletactics.de/wiki/?ar="),
		AuthHeader:    withDefault(getenv("SITA_AUTH_HEADER"), "authentication"),
		Username:      getenv("SITA_USERNAME"),
		Password:      getenv("SITA_PASSWORD"),
		Session:       getenv("SITA_SESSION"),

		ClientIPHeader: getenv("MCP_CLIENT_IP_HEADER"),
	}

	switch cfg.Transport {
	case TransportStdio, TransportHTTP:
	default:
		return Config{}, fmt.Errorf("MCP_TRANSPORT must be %q or %q, got %q", TransportStdio, TransportHTTP, cfg.Transport)
	}

	var err error
	if cfg.IndexTTL, err = time.ParseDuration(withDefault(getenv("WIKI_INDEX_TTL"), "10m")); err != nil {
		return Config{}, fmt.Errorf("WIKI_INDEX_TTL: %w", err)
	}
	if cfg.IndexMaxNodes, err = strconv.Atoi(withDefault(getenv("WIKI_INDEX_MAX_NODES"), "2000")); err != nil || cfg.IndexMaxNodes < 1 {
		return Config{}, fmt.Errorf("WIKI_INDEX_MAX_NODES must be a positive integer")
	}
	if cfg.LoginMaxFailures, err = strconv.Atoi(withDefault(getenv("MCP_LOGIN_MAX_FAILURES"), "5")); err != nil || cfg.LoginMaxFailures < 0 {
		return Config{}, fmt.Errorf("MCP_LOGIN_MAX_FAILURES must be a non-negative integer (0 disables the limit)")
	}
	if cfg.LoginBlockWindow, err = time.ParseDuration(withDefault(getenv("MCP_LOGIN_BLOCK_WINDOW"), "15m")); err != nil {
		return Config{}, fmt.Errorf("MCP_LOGIN_BLOCK_WINDOW: %w", err)
	}
	if err = cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}

	if cfg.Transport == TransportStdio && cfg.Session == "" && (cfg.Username == "" || cfg.Password == "") {
		return Config{}, fmt.Errorf("stdio mode needs SITA_USERNAME and SITA_PASSWORD (or SITA_SESSION)")
	}
	return cfg, nil
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
