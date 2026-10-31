// Command sita-mcp is an MCP server that gives LLM clients read access to the
// Simpletactics wiki through sita-backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SimpletacticsGaming/sita-mcp/internal/auth"
	"github.com/SimpletacticsGaming/sita-mcp/internal/config"
	"github.com/SimpletacticsGaming/sita-mcp/internal/mcpserver"
	"github.com/SimpletacticsGaming/sita-mcp/internal/sita"
	"github.com/SimpletacticsGaming/sita-mcp/internal/wiki"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd := run
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		cmd = healthcheck
	}
	if err := cmd(); err != nil {
		fmt.Fprintln(os.Stderr, "sita-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	// stdout carries the MCP protocol in stdio mode, so logs always go to stderr.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := sita.NewClient(cfg.BackendURL, &http.Client{Timeout: 15 * time.Second})
	svc := wiki.NewService(client, wiki.Options{
		PublicURL: cfg.WikiPublicURL,
		IndexTTL:  cfg.IndexTTL,
		MaxNodes:  cfg.IndexMaxNodes,
	})
	logger.Info("starting sita-mcp", "version", version, "transport", cfg.Transport, "backend", cfg.BackendURL)

	if cfg.Transport == config.TransportStdio {
		creds := sita.Credentials{Token: cfg.Token}
		server := mcpserver.New(svc, func(*mcp.CallToolRequest) (sita.Credentials, error) { return creds, nil }, version)
		return server.Run(ctx, &mcp.StdioTransport{})
	}
	return serveHTTP(ctx, cfg, client, svc, logger)
}

func serveHTTP(ctx context.Context, cfg config.Config, client *sita.Client, svc *wiki.Service, logger *slog.Logger) error {
	// Every request carries its own token, checked by the guard before it reaches the server.
	server := mcpserver.New(svc, func(req *mcp.CallToolRequest) (sita.Credentials, error) {
		if req.Extra != nil {
			if creds, ok := auth.FromHeader(req.Extra.Header); ok {
				return creds, nil
			}
		}
		return sita.Credentials{}, errors.New("missing MCP token in Authorization header")
	}, version)

	// Stateless: no session affinity is needed, so the service can run with several replicas.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true,
		Logger:    logger,
	})

	guard := &auth.Guard{Verifier: client, ClientIPHeader: cfg.ClientIPHeader, VerifyCacheTTL: cfg.TokenCacheTTL, Logger: logger}
	if cfg.AuthMaxFailures > 0 {
		guard.Limiter = auth.NewLimiter(cfg.AuthMaxFailures, cfg.AuthBlockWindow)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", guard.Wrap(mcpHandler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logger.Info("shutting down")
		return httpServer.Shutdown(shutdownCtx)
	}
}
