package config_test

import (
	"testing"
	"time"

	"github.com/SimpletacticsGaming/sita-mcp/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{"SITA_TOKEN": "sita_mcp_x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != config.TransportStdio || cfg.BackendURL != "https://simpletactics.de/backend" ||
		cfg.Token != "sita_mcp_x" || cfg.IndexTTL != 10*time.Minute || cfg.IndexMaxNodes != 2000 ||
		cfg.AuthMaxFailures != 5 || cfg.AuthBlockWindow != 15*time.Minute || cfg.TokenCacheTTL != time.Minute {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadHTTPNeedsNoCredentials(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{"MCP_TRANSPORT": "HTTP", "SITA_BACKEND_URL": "http://sitabackend:8080/"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != config.TransportHTTP || cfg.BackendURL != "http://sitabackend:8080" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, e := range map[string]map[string]string{
		"stdio without token":   {},
		"unknown transport":     {"MCP_TRANSPORT": "sse", "SITA_TOKEN": "x"},
		"bad ttl":               {"WIKI_INDEX_TTL": "soon", "SITA_TOKEN": "x"},
		"bad max nodes":         {"WIKI_INDEX_MAX_NODES": "0", "SITA_TOKEN": "x"},
		"bad log level":         {"LOG_LEVEL": "loud", "SITA_TOKEN": "x"},
		"bad block window":      {"MCP_AUTH_BLOCK_WINDOW": "1x", "SITA_TOKEN": "x"},
		"negative max failures": {"MCP_AUTH_MAX_FAILURES": "-1", "SITA_TOKEN": "x"},
		"bad token cache ttl":   {"MCP_TOKEN_CACHE_TTL": "1x", "SITA_TOKEN": "x"},
	} {
		if _, err := config.Load(env(e)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
