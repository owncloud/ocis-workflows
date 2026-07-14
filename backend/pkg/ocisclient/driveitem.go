package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type driveItemResponse struct {
	Name            string `json:"name"`
	ParentReference struct {
		Path string `json:"path"`
	} `json:"parentReference"`
}

// ItemPath resolves an SSE event's spaceID+itemID (see pkg/sse) to the WebDAV-style path
// (e.g. "/Invoices/foo.pdf") the rest of this backend's file operations expect. There is no
// "/me"-scoped get-item-by-id route in Graph — only /drives/{driveID}/items/{itemID}, which
// happens to accept exactly the spaceID SSE events already carry as the driveID.
func (c *Client) ItemPath(ctx context.Context, authHeader, spaceID, itemID string) (davPath string, err error) {
	u := fmt.Sprintf("%s/graph/v1.0/drives/%s/items/%s", c.baseURL, url.PathEscape(spaceID), url.PathEscape(itemID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return "", fmt.Errorf("resolve item path returned status %d", res.StatusCode)
	}

	var item driveItemResponse
	if err := json.NewDecoder(res.Body).Decode(&item); err != nil {
		return "", err
	}
	if item.Name == "" {
		return "", fmt.Errorf("resolve item path: response has no name")
	}

	parentPath := strings.TrimRight(item.ParentReference.Path, "/")
	return parentPath + "/" + item.Name, nil
}
