package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// logoutTimeout bounds the sign-out call. Signing out must not hang on a
// platform that accepts the connection and never answers: the local sign-out
// goes ahead either way, this only decides how long it waits to say whether
// the platform got the message.
const logoutTimeout = 10 * time.Second

// WithBaseURL points the client somewhere else, for tests.
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = u
	return c
}

// Logout ends the session on the platform (POST /auth/logout), so the token
// stops working everywhere and not only on this machine. Deleting
// session.json alone leaves any copy of the token valid until it expires.
//
// The session rides as a bearer, which is how the platform recognises a CLI
// session on logout, and also as the nan_session cookie, which is how every
// other call of this client presents it: a platform that predates bearer
// logout still revokes it. Both name the same session; it is revoked once.
//
// A 401 means the platform already refuses the session, which is the outcome
// wanted, so it is not an error. Anything else that is not 2xx is: nothing was
// revoked, and the caller should say so.
func (c *Client) Logout() error {
	if c.token == "" {
		return nil
	}
	base := c.baseURL
	if base == "" {
		base = BaseURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auth/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Cookie", "nan_session="+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "nan-cli")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
