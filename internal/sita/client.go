// Package sita is a small client for the sita-backend REST API.
//
// It authenticates like the website does: POST /login returns a session UUID,
// which is then sent in the `authentication` header. Every call takes the
// caller's Credentials, so the backend's per-user wiki permissions apply.
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

var (
	// ErrLoginFailed means the backend rejected username or password.
	ErrLoginFailed = errors.New("sita login failed: wrong username or password")
	// ErrUnauthorized means the session is missing or expired.
	ErrUnauthorized = errors.New("not authenticated at sita-backend")
	// ErrForbidden means the user lacks the permission for this wiki node.
	ErrForbidden = errors.New("no permission for this wiki node")
	// ErrNotFound means the wiki node does not exist.
	ErrNotFound = errors.New("wiki node not found")
)

// Credentials identify the user on whose behalf the backend is called.
// Either Session or Username and Password are set.
type Credentials struct {
	Username string
	Password string
	Session  string
}

// key identifies the credentials in the session cache without keeping the password around.
func (c Credentials) key() string {
	sum := sha256.Sum256([]byte(c.Username + "\x00" + c.Password))
	return hex.EncodeToString(sum[:])
}

// Key identifies the user for per-user caches. It never contains the password.
func (c Credentials) Key() string {
	if c.Session != "" {
		sum := sha256.Sum256([]byte("session\x00" + c.Session))
		return hex.EncodeToString(sum[:])
	}
	return c.key()
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
	baseURL    string
	authHeader string
	http       *http.Client

	mu       sync.Mutex
	sessions map[string]*sessionEntry
}

type sessionEntry struct {
	mu sync.Mutex
	id string
}

// NewClient creates a client for the backend at baseURL (e.g. https://simpletactics.de/backend).
func NewClient(baseURL, authHeader string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: authHeader,
		http:       httpClient,
		sessions:   map[string]*sessionEntry{},
	}
}

// getJSON performs an authenticated GET and decodes the JSON response into out.
// A 401 with username/password credentials triggers one re-login and retry.
func (c *Client) getJSON(ctx context.Context, creds Credentials, path string, out any) error {
	sid, err := c.sessionID(ctx, creds, "")
	if err != nil {
		return err
	}
	status, body, err := c.do(ctx, http.MethodGet, path, sid, nil)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && creds.Session == "" {
		if sid, err = c.sessionID(ctx, creds, sid); err != nil {
			return err
		}
		if status, body, err = c.do(ctx, http.MethodGet, path, sid, nil); err != nil {
			return err
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
// or if the cached one equals stale (the backend just rejected it).
func (c *Client) sessionID(ctx context.Context, creds Credentials, stale string) (string, error) {
	if creds.Session != "" {
		return creds.Session, nil
	}
	if creds.Username == "" || creds.Password == "" {
		return "", ErrUnauthorized
	}

	key := creds.key()
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
		if errors.Is(err, ErrLoginFailed) {
			// Do not keep entries for wrong passwords around.
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
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Successful bool   `json:"successful"`
	SessionID  string `json:"sessionId"`
}

func (c *Client) login(ctx context.Context, creds Credentials) (string, error) {
	payload, err := json.Marshal(loginRequest{Username: creds.Username, Password: creds.Password})
	if err != nil {
		return "", err
	}
	status, body, err := c.do(ctx, http.MethodPost, "/login", "", payload)
	if err != nil {
		return "", err
	}
	// The backend answers 403 for unknown users and wrong passwords.
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return "", ErrLoginFailed
	}
	if status < 200 || status > 299 {
		return "", &APIError{Method: http.MethodPost, Path: "/login", Status: status, Body: truncate(string(body), 300)}
	}
	var res loginResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if !res.Successful || res.SessionID == "" {
		return "", ErrLoginFailed
	}
	return res.SessionID, nil
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
		req.Header.Set(c.authHeader, session)
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
