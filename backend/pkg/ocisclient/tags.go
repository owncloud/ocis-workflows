package ocisclient

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"context"
)

// ResolveItemID resolves a WebDAV-style path (as used by webdavstore/webdavfile, e.g.
// "/Invoices/foo.pdf") to the compound resourceId ("<storageid>$<spaceid>!<opaqueid>") the
// Graph tags API expects. There is no path-based lookup in the Graph API itself — this
// works by PROPFIND-ing the file's oc:fileid WebDAV property, which is already returned in
// exactly that compound format.
func (c *Client) ResolveItemID(ctx context.Context, authHeader, davPath string) (string, error) {
	userID, err := c.Me(ctx, authHeader)
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}

	encoded := (&url.URL{Path: strings.Trim(davPath, "/")}).EscapedPath()
	davURL := fmt.Sprintf("%s/remote.php/dav/files/%s/%s", c.baseURL, userID, encoded)

	body := `<?xml version="1.0" encoding="utf-8" ?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/></d:prop></d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", davURL, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != 207 { // Multi-Status
		return "", fmt.Errorf("resolve item id for %q returned status %d", davPath, res.StatusCode)
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	var ms struct {
		Responses []struct {
			Propstat struct {
				Prop struct {
					FileID string `xml:"fileid"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(data, &ms); err != nil {
		return "", fmt.Errorf("decode PROPFIND response: %w", err)
	}
	if len(ms.Responses) == 0 || ms.Responses[0].Propstat.Prop.FileID == "" {
		return "", fmt.Errorf("resolve item id for %q: no oc:fileid in PROPFIND response", davPath)
	}
	return ms.Responses[0].Propstat.Prop.FileID, nil
}

type tagAssignment struct {
	ResourceID string   `json:"resourceId"`
	Tags       []string `json:"tags"`
}

// AssignTag adds a tag to the given resource via the Graph tags extension.
func (c *Client) AssignTag(ctx context.Context, authHeader, itemID, tag string) error {
	body, err := json.Marshal(tagAssignment{ResourceID: itemID, Tags: []string{tag}})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/graph/v1.0/extensions/org.libregraph/tags", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("assign tag %q returned status %d", tag, res.StatusCode)
	}
	return nil
}
