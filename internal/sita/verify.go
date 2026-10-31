package sita

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Verify checks that creds are accepted by the backend.
// Username and password are checked by logging in (the session is cached),
// a session id via GET /loggedIn.
func (c *Client) Verify(ctx context.Context, creds Credentials) error {
	if creds.Session == "" {
		_, err := c.sessionID(ctx, creds, "")
		return err
	}
	status, body, err := c.do(ctx, http.MethodGet, "/loggedIn", creds.Session, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &APIError{Method: http.MethodGet, Path: "/loggedIn", Status: status, Body: truncate(string(body), 300)}
	}
	var ok bool
	if err := json.Unmarshal(body, &ok); err != nil {
		return fmt.Errorf("decode /loggedIn response: %w", err)
	}
	if !ok {
		return ErrUnauthorized
	}
	return nil
}
