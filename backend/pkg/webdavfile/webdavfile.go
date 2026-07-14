// Package webdavfile implements the file operations workflow action nodes need (read
// content, move, copy, rename) over oCIS's public WebDAV API, in the caller's own space —
// the same API surface an end user's own WebDAV client would use.
package webdavfile

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/LukasHirt/ocis-workflows/pkg/ocisclient"
)

// Client performs WebDAV file operations against a resource path in the caller's own space.
type Client struct {
	ocisURL    string
	ocisClient *ocisclient.Client
	httpClient *http.Client
}

// New builds a Client for the given oCIS base URL.
func New(ocisURL string, ocisClient *ocisclient.Client, insecure bool) *Client {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only opt-in
	}
	return &Client{
		ocisURL:    strings.TrimRight(ocisURL, "/"),
		ocisClient: ocisClient,
		httpClient: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

func (c *Client) davURL(userID, davPath string) string {
	encoded := (&url.URL{Path: strings.Trim(davPath, "/")}).EscapedPath()
	return fmt.Sprintf("%s/remote.php/dav/files/%s/%s", c.ocisURL, userID, encoded)
}

// GetContent reads a file's content, and returns its base name.
func (c *Client) GetContent(ctx context.Context, token, davPath string) (content []byte, name string, err error) {
	userID, err := c.ocisClient.Me(ctx, token)
	if err != nil {
		return nil, "", fmt.Errorf("resolve current user: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.davURL(userID, davPath), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s returned status %d", davPath, res.StatusCode)
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	return data, path.Base(davPath), nil
}

// Move moves/renames a file from davPath to destDavPath.
func (c *Client) Move(ctx context.Context, token, davPath, destDavPath string) error {
	return c.copyOrMove(ctx, "MOVE", token, davPath, destDavPath)
}

// Copy copies a file from davPath to destDavPath.
func (c *Client) Copy(ctx context.Context, token, davPath, destDavPath string) error {
	return c.copyOrMove(ctx, "COPY", token, davPath, destDavPath)
}

func (c *Client) copyOrMove(ctx context.Context, method, token, davPath, destDavPath string) error {
	userID, err := c.ocisClient.Me(ctx, token)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.davURL(userID, davPath), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Destination", c.davURL(userID, destDavPath))
	req.Header.Set("Overwrite", "F")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusCreated, http.StatusNoContent:
		return nil
	default:
		return fmt.Errorf("%s %s -> %s returned status %d", method, davPath, destDavPath, res.StatusCode)
	}
}

// Comment appends a comment to a file. oCIS has no native file-comments API (unlike tags),
// so this is implemented as a JSON sidecar list stored alongside our own workflow data
// under .workflows/comments/ in the caller's space — real and retrievable, but not
// visible in oCIS Web's own UI. Documented limitation, not a stub.
func (c *Client) Comment(ctx context.Context, token, davPath, text string) error {
	userID, err := c.ocisClient.Me(ctx, token)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}

	if err := c.mkcol(ctx, token, userID, ".workflows"); err != nil {
		return err
	}
	if err := c.mkcol(ctx, token, userID, ".workflows/comments"); err != nil {
		return err
	}

	sidecarPath := ".workflows/comments/" + base64.RawURLEncoding.EncodeToString([]byte(davPath)) + ".json"

	var comments []string
	if existing, _, err := c.GetContent(ctx, token, sidecarPath); err == nil {
		_ = json.Unmarshal(existing, &comments)
	}
	comments = append(comments, text)

	body, err := json.Marshal(comments)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davURL(userID, sidecarPath), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("PUT comment sidecar returned status %d", res.StatusCode)
	}
	return nil
}

func (c *Client) mkcol(ctx context.Context, token, userID, davPath string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", c.davURL(userID, davPath), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusCreated, http.StatusMethodNotAllowed, http.StatusConflict:
		return nil
	default:
		return fmt.Errorf("MKCOL %s returned status %d", davPath, res.StatusCode)
	}
}
