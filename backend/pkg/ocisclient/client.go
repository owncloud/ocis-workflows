// Package ocisclient is a thin client for the parts of oCIS's public Graph API this
// backend needs — currently just resolving the calling user's id, which WebDAV storage
// paths are keyed by.
package ocisclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client calls oCIS's public Graph API on behalf of a forwarded user bearer token.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New builds a Client for the given oCIS base URL.
func New(ocisURL string, insecure bool) *Client {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only opt-in
	}
	return &Client{
		baseURL:    strings.TrimRight(ocisURL, "/"),
		httpClient: &http.Client{Transport: transport, Timeout: 15 * time.Second},
	}
}

// BaseURL returns the configured oCIS base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

type meResponse struct {
	ID string `json:"id"`
}

// Me resolves the id of the user the given bearer token belongs to, via the Graph API.
func (c *Client) Me(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/graph/v1.0/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph /me returned status %d", res.StatusCode)
	}

	var me meResponse
	if err := json.NewDecoder(res.Body).Decode(&me); err != nil {
		return "", err
	}
	if me.ID == "" {
		return "", fmt.Errorf("graph /me response has no id")
	}
	return me.ID, nil
}
