// Package sita is a small client for the sita-backend REST API.
//
// It authenticates with a personal MCP API token that the user creates on the
// website. The token is sent as `Authorization: Bearer <token>` on every call,
// so the backend's per-user wiki permissions apply. See docs/api-tokens.md.
package sita

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	// ErrUnauthorized means the token is missing, unknown, expired or revoked.
	ErrUnauthorized = errors.New("sita-backend rejected the API token: it is invalid, expired or revoked")
	// ErrForbidden means the user lacks the permission for this wiki node.
	ErrForbidden = errors.New("no permission for this wiki node")
	// ErrNotFound means the wiki node does not exist.
	ErrNotFound = errors.New("wiki node not found")
)

// Credentials identify the user on whose behalf the backend is called.
type Credentials struct {
	// Token is the user's MCP API token.
	Token string
}

// Key identifies the token in caches without keeping the token itself around.
func (c Credentials) Key() string {
	sum := sha256.Sum256([]byte(c.Token))
	return hex.EncodeToString(sum[:])
}

// APIError is returned for unexpected backend responses.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sita-backend %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Client talks to sita-backend.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a client for the backend at baseURL (e.g. https://simpletactics.de/backend).
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// getJSON performs an authenticated GET and decodes the JSON response into out.
func (c *Client) getJSON(ctx context.Context, creds Credentials, path string, out any) error {
	if creds.Token == "" {
		return ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.Token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sita-backend GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("read response of GET %s: %w", path, err)
	}

	switch status := resp.StatusCode; {
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status == http.StatusForbidden:
		return ErrForbidden
	case status == http.StatusNotFound:
		return ErrNotFound
	case status < 200 || status > 299:
		return &APIError{Method: http.MethodGet, Path: path, Status: status, Body: truncate(string(body), 300)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response of GET %s: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
