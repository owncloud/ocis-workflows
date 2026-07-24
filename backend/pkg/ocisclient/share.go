package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// graphIDCollection is the shape of a Graph API list response we only care about the ids of
// (e.g. GET /graph/v1.0/users, /graph/v1.0/groups).
type graphIDCollection struct {
	Value []struct {
		ID string `json:"id"`
	} `json:"value"`
}

// searchGraphCollection queries a Graph API collection endpoint (users or groups) with the
// standard OData `$search` convention (a double-quoted term Graph matches against name/mail
// fields) and returns the id of the first match, or "" if there is none.
func (c *Client) searchGraphCollection(ctx context.Context, authHeader, collectionPath, term string) (string, error) {
	u := fmt.Sprintf("%s%s?$search=%s", c.baseURL, collectionPath, url.QueryEscape(`"`+term+`"`))
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
		return "", fmt.Errorf("search %s for %q returned status %d", collectionPath, term, res.StatusCode)
	}

	var col graphIDCollection
	if err := json.NewDecoder(res.Body).Decode(&col); err != nil {
		return "", fmt.Errorf("decode %s search response: %w", collectionPath, err)
	}
	if len(col.Value) == 0 {
		return "", nil
	}
	return col.Value[0].ID, nil
}

// resolveRecipientID turns an email address or group name into the Graph user/group id the
// invite endpoint expects. It tries a user lookup first, then falls back to a group lookup.
func (c *Client) resolveRecipientID(ctx context.Context, authHeader, recipient string) (string, error) {
	userID, err := c.searchGraphCollection(ctx, authHeader, "/graph/v1.0/users", recipient)
	if err != nil {
		return "", fmt.Errorf("look up user %q: %w", recipient, err)
	}
	if userID != "" {
		return userID, nil
	}

	groupID, err := c.searchGraphCollection(ctx, authHeader, "/graph/v1.0/groups", recipient)
	if err != nil {
		return "", fmt.Errorf("look up group %q: %w", recipient, err)
	}
	if groupID == "" {
		return "", fmt.Errorf("resolve recipient %q: no matching user or group", recipient)
	}
	return groupID, nil
}

type shareRecipient struct {
	ObjectID string `json:"objectId"`
}

type shareInviteRequest struct {
	Recipients []shareRecipient `json:"recipients"`
	Roles      []string         `json:"roles"`
}

// Share grants role ("viewer" or "editor") access on itemID (a compound resourceId as
// returned by ResolveItemID, "<storageid>$<spaceid>!<opaqueid>") to the user or group
// identified by recipient (an email address or group name), via oCIS's Graph API drive-item
// invite endpoint.
//
// NOTE ON VERIFICATION: this endpoint shape — POST /graph/v1.0/drives/{driveID}/items/{itemID}/
// invite with a {recipients: [{objectId}], roles: [...]}  body — follows the general oCIS/
// libre-graph API sharing convention and this package's existing URL-construction pattern (see
// ItemPath in driveitem.go, which builds the same /graph/v1.0/drives/{id}/items/{id} route; the
// driveID here is derived by taking everything before "!" in the compound resourceId, matching
// how that id is documented to be composed). It has NOT been verified against a live oCIS
// instance — in particular, whether "roles" expects the literal strings "viewer"/"editor" or
// role-definition UUIDs (as returned by /graph/v1.0/roleManagement/permissions/roleDefinitions)
// is unconfirmed and should be checked before relying on this in production.
func (c *Client) Share(ctx context.Context, authHeader, itemID, recipient, role string) error {
	recipientID, err := c.resolveRecipientID(ctx, authHeader, recipient)
	if err != nil {
		return err
	}

	driveID := itemID
	if idx := strings.Index(itemID, "!"); idx >= 0 {
		driveID = itemID[:idx]
	}

	body, err := json.Marshal(shareInviteRequest{
		Recipients: []shareRecipient{{ObjectID: recipientID}},
		Roles:      []string{role},
	})
	if err != nil {
		return err
	}

	u := fmt.Sprintf("%s/graph/v1.0/drives/%s/items/%s/invite", c.baseURL, url.PathEscape(driveID), url.PathEscape(itemID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
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

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return fmt.Errorf("share item with %q returned status %d", recipient, res.StatusCode)
	}
	return nil
}
