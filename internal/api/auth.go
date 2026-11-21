package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Signing in lives in device.go. What is left here is ending a session.

// logoutTimeout is short: signing out must not hang the dashboard on a
// platform that accepts the connection and never answers.
var logoutTimeout = 10 * time.Second

// Logout ends the session on the platform, not only here. A session token
// copied off the machine before signing out otherwise keeps working until
// it expires.
//
// The session goes as a bearer and as the cookie, so an older platform that
// only reads the cookie still revokes it. A 401 is success: a session the
// platform already refuses is already over.
func (c *Client) Logout(ctx context.Context) error {
	if c.token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, logoutTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: c.token}) //nolint:gosec // G124: an outgoing request cookie; the attributes are for the server to set
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
