// Package sita is a small client for the sita-backend REST API.
//
// It logs in like the website does, but with a personal MCP token instead of
// username and password: POST /login/mcp returns a session id, which is then
// sent in the `authentication` header. Every call takes the caller's
// Credentials, so the backend's per-user wiki permissions apply.
// See docs/api-tokens.md.
package sita

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// sessionHeader is the header sita-backend reads the session id from
// (sita.backend.http.auth-token-header-name).
const sessionHeader = "authentication"

var (
	// ErrUnauthorized means the MCP token is unknown, expired or revoked.
	ErrUnauthorized = errors.New("sita-backend rejected the MCP token: it is invalid, expired or revoked")
	// ErrForbidden means the user lacks the permission for this wiki node.
	ErrForbidden = errors.New("no permission for this wiki node")
	// ErrNotFound means the wiki node does not exist.
	ErrNotFound = errors.New("wiki node not found")
)

// Credentials identify the user on whose behalf the backend is called.
type Credentials struct {
	// Token is the user's MCP token.
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

	mu       sync.Mutex
	sessions map[string]*sessionEntry
}

type sessionEntry struct {
	mu sync.Mutex
	id string
}

// NewClient creates a client for the backend at baseURL (e.g. https://simpletactics.de/backend).
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		http:     httpClient,
		sessions: map[string]*sessionEntry{},
	}
}

// Verify checks that the token in creds is accepted, logging in if there is no session yet.
func (c *Client) Verify(ctx context.Context, creds Credentials) error {
	_, err := c.sessionID(ctx, creds, "")
	return err
}

// getJSON performs an authenticated GET and decodes the JSON response into out.
//
// The backend answers requests with an unknown or expired session with 403,
// just like requests the user has no permission for. On 403 the client asks
// GET /loggedIn whether the session is still valid and, if not, logs in again
// and retries once.
func (c *Client) getJSON(ctx context.Context, creds Credentials, path string, out any) error {
	sid, err := c.sessionID(ctx, creds, "")
	if err != nil {
		return err
	}
	status, body, err := c.do(ctx, http.MethodGet, path, sid, nil)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		valid, err := c.sessionValid(ctx, sid)
		if err != nil {
			return err
		}
		if !valid {
			if sid, err = c.sessionID(ctx, creds, sid); err != nil {
				return err
			}
			if status, body, err = c.do(ctx, http.MethodGet, path, sid, nil); err != nil {
				return err
			}
		}
	}

	switch {
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

// sessionID returns a cached session for creds, logging in if there is none
// or if the cached one equals stale (the backend no longer accepts it).
func (c *Client) sessionID(ctx context.Context, creds Credentials, stale string) (string, error) {
	if creds.Token == "" {
		return "", ErrUnauthorized
	}

	key := creds.Key()
	c.mu.Lock()
	e, ok := c.sessions[key]
	if !ok {
		e = &sessionEntry{}
		c.sessions[key] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.id != "" && e.id != stale {
		return e.id, nil
	}
	id, err := c.login(ctx, creds)
	if err != nil {
		e.id = ""
		if errors.Is(err, ErrUnauthorized) {
			// Do not keep entries for invalid tokens around.
			c.mu.Lock()
			delete(c.sessions, key)
			c.mu.Unlock()
		}
		return "", err
	}
	e.id = id
	return id, nil
}

type loginRequest struct {
	Token string `json:"token"`
}

type loginResponse struct {
	Successful bool   `json:"successful"`
	SessionID  string `json:"sessionId"`
}

func (c *Client) login(ctx context.Context, creds Credentials) (string, error) {
	payload, err := json.Marshal(loginRequest(creds))
	if err != nil {
		return "", err
	}
	status, body, err := c.do(ctx, http.MethodPost, "/login/mcp", "", payload)
	if err != nil {
		return "", err
	}
	// Like /login, the backend answers 403 for tokens it does not accept.
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return "", ErrUnauthorized
	}
	if status < 200 || status > 299 {
		return "", &APIError{Method: http.MethodPost, Path: "/login/mcp", Status: status, Body: truncate(string(body), 300)}
	}
	var res loginResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if !res.Successful || res.SessionID == "" {
		return "", ErrUnauthorized
	}
	return res.SessionID, nil
}

// sessionValid asks the backend whether session is still valid.
func (c *Client) sessionValid(ctx context.Context, session string) (bool, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/loggedIn", session, nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, &APIError{Method: http.MethodGet, Path: "/loggedIn", Status: status, Body: truncate(string(body), 300)}
	}
	var valid bool
	if err := json.Unmarshal(body, &valid); err != nil {
		return false, fmt.Errorf("decode /loggedIn response: %w", err)
	}
	return valid, nil
}

func (c *Client) do(ctx context.Context, method, path, session string, payload []byte) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		req.Header.Set(sessionHeader, session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("sita-backend %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("read response of %s %s: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
