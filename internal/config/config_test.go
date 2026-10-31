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
	cfg, err := config.Load(env(map[string]string{"SITA_USERNAME": "alice", "SITA_PASSWORD": "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != config.TransportStdio || cfg.BackendURL != "https://simpletactics.de/backend" ||
		cfg.AuthHeader != "authentication" || cfg.IndexTTL != 10*time.Minute || cfg.IndexMaxNodes != 2000 {
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
		"stdio without credentials": {},
		"unknown transport":         {"MCP_TRANSPORT": "sse", "SITA_SESSION": "x"},
		"bad ttl":                   {"WIKI_INDEX_TTL": "soon", "SITA_SESSION": "x"},
		"bad max nodes":             {"WIKI_INDEX_MAX_NODES": "0", "SITA_SESSION": "x"},
		"bad log level":             {"LOG_LEVEL": "loud", "SITA_SESSION": "x"},
	} {
		if _, err := config.Load(env(e)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
