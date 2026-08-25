package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Drive is an oCIS space as returned by the Graph API's list-drives endpoint.
type Drive struct {
	ID        string
	Name      string
	DriveType string
}

type drivesResponse struct {
	Value []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		DriveType string `json:"driveType"`
	} `json:"value"`
}

// ListDrives returns every space (drive) the caller (identified by authHeader) can access,
// via oCIS's Graph API.
func (c *Client) ListDrives(ctx context.Context, authHeader string) ([]Drive, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/graph/v1.0/me/drives", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list drives returned status %d", res.StatusCode)
	}

	var parsed drivesResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	drives := make([]Drive, 0, len(parsed.Value))
	for _, d := range parsed.Value {
		drives = append(drives, Drive{ID: d.ID, Name: d.Name, DriveType: d.DriveType})
	}
	return drives, nil
}
