package ocisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// graphSearchResult is the shape of a single entry in a Graph API users/groups collection
// response that we need to resolve a recipient: its id plus the fields ($search matches
// against) we use to confirm an exact match rather than trusting result ordering.
type graphSearchResult struct {
	ID          string `json:"id"`
	Mail        string `json:"mail"`
	DisplayName string `json:"displayName"`
}

// graphIDCollection is the shape of a Graph API list response we care about
// (e.g. GET /graph/v1.0/users, /graph/v1.0/groups).
type graphIDCollection struct {
	Value []graphSearchResult `json:"value"`
}

// searchGraphCollection queries a Graph API collection endpoint (users or groups) with the
// standard OData `$search` convention (a double-quoted term Graph matches against name/mail
// fields), then filters the results for an exact, case-insensitive match on mail or
// displayName against term. It reports found=false (no error) when nothing matches, so
// callers can fall back to another collection, but returns an error if the match is
// ambiguous (more than one exact match).
func (c *Client) searchGraphCollection(ctx context.Context, authHeader, collectionPath, term string) (id string, found bool, err error) {
	u := fmt.Sprintf("%s%s?$search=%s", c.baseURL, collectionPath, url.QueryEscape(`"`+term+`"`))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", authHeader)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("search %s for %q returned status %d", collectionPath, term, res.StatusCode)
	}

	var col graphIDCollection
	if err := json.NewDecoder(res.Body).Decode(&col); err != nil {
		return "", false, fmt.Errorf("decode %s search response: %w", collectionPath, err)
	}

	var matches []graphSearchResult
	for _, entry := range col.Value {
		if strings.EqualFold(entry.Mail, term) || strings.EqualFold(entry.DisplayName, term) {
			matches = append(matches, entry)
		}
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0].ID, true, nil
	default:
		return "", false, fmt.Errorf("search %s for %q matched %d entries exactly, expected 1", collectionPath, term, len(matches))
	}
}

// resolveRecipientID turns an email address or group name into the Graph user/group id the
// invite endpoint expects, plus which kind of recipient it resolved to ("user" or "group") so
// the caller can set the "@libre.graph.recipient.type" annotation the invite endpoint requires
// for group recipients. It tries a user lookup first, then falls back to a group lookup.
func (c *Client) resolveRecipientID(ctx context.Context, authHeader, recipient string) (id, recipientType string, err error) {
	userID, found, err := c.searchGraphCollection(ctx, authHeader, "/graph/v1.0/users", recipient)
	if err != nil {
		return "", "", fmt.Errorf("look up user %q: %w", recipient, err)
	}
	if found {
		return userID, "user", nil
	}

	groupID, found, err := c.searchGraphCollection(ctx, authHeader, "/graph/v1.0/groups", recipient)
	if err != nil {
		return "", "", fmt.Errorf("look up group %q: %w", recipient, err)
	}
	if !found {
		return "", "", fmt.Errorf("resolve recipient %q: no exact matching user or group", recipient)
	}
	return groupID, "group", nil
}

// roleDefinition is the subset of a Graph API `unifiedRoleDefinition` we need: its stable id
// and the human-readable name we match role names ("viewer"/"editor") against. Role ids are
// UUIDs that are not stable across oCIS deployments, so they must always be resolved by name
// rather than hardcoded.
type roleDefinition struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type roleDefinitionCollection struct {
	Value []roleDefinition `json:"value"`
}

// resolveRoleID resolves a role name (e.g. "viewer", "editor") to the role-definition UUID the
// invite endpoint expects, via GET /graph/v1beta1/roleManagement/permissions/roleDefinitions.
// It matches case-insensitively against each role definition's displayName (falling back to a
// substring match, since displayName values are not otherwise guaranteed) and returns an error
// if no definition matches.
func (c *Client) resolveRoleID(ctx context.Context, authHeader, role string) (string, error) {
	u := c.baseURL + "/graph/v1beta1/roleManagement/permissions/roleDefinitions"
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
		return "", fmt.Errorf("list role definitions returned status %d", res.StatusCode)
	}

	var col roleDefinitionCollection
	if err := json.NewDecoder(res.Body).Decode(&col); err != nil {
		return "", fmt.Errorf("decode role definitions response: %w", err)
	}

	role = strings.TrimSpace(role)
	for _, rd := range col.Value {
		if strings.EqualFold(rd.DisplayName, role) {
			return rd.ID, nil
		}
	}
	// Fall back to a substring match in case a deployment uses slightly different display
	// names (e.g. "Can view" instead of "Viewer").
	lowerRole := strings.ToLower(role)
	for _, rd := range col.Value {
		lowerName := strings.ToLower(rd.DisplayName)
		if strings.Contains(lowerName, lowerRole) || strings.Contains(lowerRole, lowerName) {
			return rd.ID, nil
		}
	}
	return "", fmt.Errorf("resolve role %q: no matching role definition", role)
}

type shareRecipient struct {
	ObjectID string `json:"objectId"`
	// LibreGraphRecipientType sets the "@libre.graph.recipient.type" annotation the invite
	// endpoint requires to disambiguate a group recipient from a user recipient (it defaults
	// to "user" when omitted, so this is only set for group matches).
	LibreGraphRecipientType string `json:"@libre.graph.recipient.type,omitempty"`
}

type shareInviteRequest struct {
	Recipients []shareRecipient `json:"recipients"`
	Roles      []string         `json:"roles"`
}

// Share grants role ("viewer" or "editor") access on itemID (a compound resourceId as
// returned by ResolveItemID, "<storageid>$<spaceid>!<opaqueid>") to the user or group
// identified by recipient (an email address or group name), via oCIS's Graph API
// Sharing-NG drive-item invite endpoint (POST /graph/v1beta1/drives/{driveID}/items/{itemID}/
// invite with a {recipients: [{objectId, "@libre.graph.recipient.type"}], roles: [<role-definition-uuid>]}
// body). The driveID is derived by taking everything before "!" in the compound resourceId,
// matching how that id is documented to be composed.
func (c *Client) Share(ctx context.Context, authHeader, itemID, recipient, role string) error {
	recipientID, recipientType, err := c.resolveRecipientID(ctx, authHeader, recipient)
	if err != nil {
		return err
	}

	roleID, err := c.resolveRoleID(ctx, authHeader, role)
	if err != nil {
		return err
	}

	driveID := itemID
	if idx := strings.Index(itemID, "!"); idx >= 0 {
		driveID = itemID[:idx]
	}

	shareRecip := shareRecipient{ObjectID: recipientID}
	if recipientType == "group" {
		shareRecip.LibreGraphRecipientType = "group"
	}

	body, err := json.Marshal(shareInviteRequest{
		Recipients: []shareRecipient{shareRecip},
		Roles:      []string{roleID},
	})
	if err != nil {
		return err
	}

	u := fmt.Sprintf("%s/graph/v1beta1/drives/%s/items/%s/invite", c.baseURL, url.PathEscape(driveID), url.PathEscape(itemID))
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
