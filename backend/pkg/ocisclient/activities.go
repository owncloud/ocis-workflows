package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ActivityResource is the resource an activitylog entry refers to. ID arrives pre-formatted
// as the compound "storageid$spaceid!opaqueid" resourceId — the same format ItemPath
// already consumes — but can be empty for some entry types (e.g. observed live: a
// freshly-added file's own "resource" variable sometimes has no id, only a name).
type ActivityResource struct {
	ID   string
	Name string
}

// Activity is one entry from oCIS's activitylog service. Message is one of a small, fixed
// set of untranslated template strings (verified against oCIS source: the message is never
// translated server-side, only some auxiliary variables are) — safe to match by exact
// string equality.
type Activity struct {
	ID           string
	Message      string
	Resource     ActivityResource
	RecordedTime time.Time
}

type activitiesResponse struct {
	Value []struct {
		ID       string `json:"id"`
		Template struct {
			Message   string `json:"message"`
			Variables struct {
				Resource struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"resource"`
			} `json:"variables"`
		} `json:"template"`
		Times struct {
			RecordedTime time.Time `json:"recordedTime"`
		} `json:"times"`
	} `json:"value"`
}

// ListActivities returns every activitylog entry recorded for driveID (and everything
// under it, via depth:-1) strictly after since, via oCIS's activitylog Graph extension.
func (c *Client) ListActivities(ctx context.Context, authHeader, driveID string, since time.Time) ([]Activity, error) {
	kql := fmt.Sprintf("itemid:%s AND depth:-1 AND timestamp>%s", driveID, since.UTC().Format(time.RFC3339))
	// Encode spaces as +, which is standard for URL query strings, preserving KQL syntax characters.
	kqlForURL := strings.ReplaceAll(kql, " ", "+")
	u := c.baseURL + "/graph/v1beta1/extensions/org.libregraph/activities?kql=" + kqlForURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return nil, fmt.Errorf("list activities returned status %d", res.StatusCode)
	}

	var parsed activitiesResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	activities := make([]Activity, 0, len(parsed.Value))
	for _, v := range parsed.Value {
		activities = append(activities, Activity{
			ID:      v.ID,
			Message: v.Template.Message,
			Resource: ActivityResource{
				ID:   v.Template.Variables.Resource.ID,
				Name: v.Template.Variables.Resource.Name,
			},
			RecordedTime: v.Times.RecordedTime,
		})
	}
	return activities, nil
}
