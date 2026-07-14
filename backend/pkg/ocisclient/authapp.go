package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type meFullResponse struct {
	ID       string `json:"id"`
	Username string `json:"onPremisesSamAccountName"`
}

// Username resolves the login name (as used by HTTP Basic auth with an app-password) of
// the user the given auth header belongs to.
func (c *Client) Username(ctx context.Context, authHeader string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/graph/v1.0/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph /me returned status %d", res.StatusCode)
	}

	var me meFullResponse
	if err := json.NewDecoder(res.Body).Decode(&me); err != nil {
		return "", err
	}
	if me.Username == "" {
		return "", fmt.Errorf("graph /me response has no onPremisesSamAccountName")
	}
	return me.Username, nil
}

type authAppToken struct {
	Token          string    `json:"token"`
	ExpirationDate time.Time `json:"expiration_date"`
}

// MintAppPassword creates an oCIS app-password for the caller (identified by authHeader,
// their own live credential — this can only mint a token for "yourself", never for another
// user), valid for the given duration.
func (c *Client) MintAppPassword(ctx context.Context, authHeader string, expiry time.Duration, label string) (token string, expiresAt time.Time, err error) {
	q := url.Values{"expiry": {expiry.String()}, "label": {label}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth-app/tokens?"+q.Encode(), nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("mint app password returned status %d", res.StatusCode)
	}

	var parsed authAppToken
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("decode app password response: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, fmt.Errorf("mint app password: response has no token")
	}
	return parsed.Token, parsed.ExpirationDate, nil
}

// RevokeAppPassword invalidates a previously minted app-password.
func (c *Client) RevokeAppPassword(ctx context.Context, authHeader, token string) error {
	q := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/auth-app/tokens?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke app password returned status %d", res.StatusCode)
	}
	return nil
}
