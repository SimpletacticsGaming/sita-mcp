package sita

import (
	"context"
	"errors"
	"time"
)

// TokenInfo describes an API token, as returned by GET /api/tokens/current.
type TokenInfo struct {
	Name      string     `json:"name"`
	Username  string     `json:"username"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// Verify checks that the token in creds is accepted by the backend and returns its details.
func (c *Client) Verify(ctx context.Context, creds Credentials) (TokenInfo, error) {
	var info TokenInfo
	err := c.getJSON(ctx, creds, "/api/tokens/current", &info)
	// Every valid token may call this endpoint, so 403 can only mean the backend did not
	// authenticate the request. Spring Security answers anonymous requests with 403 by default.
	if errors.Is(err, ErrForbidden) {
		return TokenInfo{}, ErrUnauthorized
	}
	return info, err
}
