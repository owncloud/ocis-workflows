package ocisclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGraphServer is a minimal in-memory stand-in for the parts of oCIS's Graph API Share()
// depends on: user/group search, role-definition listing, and the invite endpoint. It records
// the last invite request body so tests can assert on the resolved role id and recipient
// annotation.
type fakeGraphServer struct {
	users  []graphSearchResult
	groups []graphSearchResult
	roles  []roleDefinition

	lastInvitePath string
	lastInviteBody shareInviteRequest
	inviteStatus   int
}

func newFakeGraphServer() *fakeGraphServer {
	return &fakeGraphServer{
		roles: []roleDefinition{
			{ID: "b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5", DisplayName: "Viewer"},
			{ID: "fb6c3e19-e378-47e5-b277-9732f9de6e21", DisplayName: "Editor"},
		},
		inviteStatus: http.StatusOK,
	}
}

func (f *fakeGraphServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/graph/v1.0/users"):
		term := searchTerm(r)
		_ = json.NewEncoder(w).Encode(graphIDCollection{Value: filterSearch(f.users, term)})
	case strings.HasPrefix(r.URL.Path, "/graph/v1.0/groups"):
		term := searchTerm(r)
		_ = json.NewEncoder(w).Encode(graphIDCollection{Value: filterSearch(f.groups, term)})
	case r.URL.Path == "/graph/v1beta1/roleManagement/permissions/roleDefinitions":
		_ = json.NewEncoder(w).Encode(roleDefinitionCollection{Value: f.roles})
	case strings.Contains(r.URL.Path, "/invite"):
		f.lastInvitePath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&f.lastInviteBody)
		status := f.inviteStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// searchTerm extracts the raw $search term (without the surrounding quotes Graph's OData
// convention wraps it in) from a request built by searchGraphCollection.
func searchTerm(r *http.Request) string {
	raw := r.URL.Query().Get("$search")
	return strings.Trim(raw, `"`)
}

// filterSearch simulates a (loose) server-side $search match: real Graph implementations
// return substring/fuzzy matches, which is exactly why client-side exact-match filtering
// (searchGraphCollection) is required.
func filterSearch(entries []graphSearchResult, term string) []graphSearchResult {
	var out []graphSearchResult
	lowerTerm := strings.ToLower(term)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Mail), lowerTerm) || strings.Contains(strings.ToLower(e.DisplayName), lowerTerm) {
			out = append(out, e)
		}
	}
	return out
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(srv.URL, false)
}

func TestShareResolvesRoleUUIDAndInvitePath(t *testing.T) {
	fg := newFakeGraphServer()
	fg.users = []graphSearchResult{{ID: "user-1", Mail: "alice@example.com", DisplayName: "Alice"}}
	srv := httptest.NewServer(fg)
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.Share(t.Context(), "Bearer tok", "storage$space!item", "alice@example.com", "viewer")
	if err != nil {
		t.Fatalf("Share() returned error: %v", err)
	}

	if fg.lastInvitePath != "/graph/v1beta1/drives/storage$space/items/storage$space!item/invite" {
		t.Fatalf("unexpected invite path %q", fg.lastInvitePath)
	}
	if len(fg.lastInviteBody.Roles) != 1 || fg.lastInviteBody.Roles[0] != "b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5" {
		t.Fatalf("expected resolved viewer role UUID, got %v", fg.lastInviteBody.Roles)
	}
	if len(fg.lastInviteBody.Recipients) != 1 || fg.lastInviteBody.Recipients[0].ObjectID != "user-1" {
		t.Fatalf("unexpected recipients %+v", fg.lastInviteBody.Recipients)
	}
	if fg.lastInviteBody.Recipients[0].LibreGraphRecipientType != "" {
		t.Fatalf("expected no recipient-type annotation for a user match, got %q", fg.lastInviteBody.Recipients[0].LibreGraphRecipientType)
	}
}

func TestShareAnnotatesGroupRecipients(t *testing.T) {
	fg := newFakeGraphServer()
	fg.groups = []graphSearchResult{{ID: "group-1", DisplayName: "Engineering"}}
	srv := httptest.NewServer(fg)
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.Share(t.Context(), "Bearer tok", "storage$space!item", "Engineering", "editor"); err != nil {
		t.Fatalf("Share() returned error: %v", err)
	}

	if len(fg.lastInviteBody.Recipients) != 1 {
		t.Fatalf("expected one recipient, got %d", len(fg.lastInviteBody.Recipients))
	}
	recip := fg.lastInviteBody.Recipients[0]
	if recip.ObjectID != "group-1" {
		t.Fatalf("expected group-1, got %q", recip.ObjectID)
	}
	if recip.LibreGraphRecipientType != "group" {
		t.Fatalf(`expected "@libre.graph.recipient.type" = "group", got %q`, recip.LibreGraphRecipientType)
	}
	if len(fg.lastInviteBody.Roles) != 1 || fg.lastInviteBody.Roles[0] != "fb6c3e19-e378-47e5-b277-9732f9de6e21" {
		t.Fatalf("expected resolved editor role UUID, got %v", fg.lastInviteBody.Roles)
	}
}

func TestResolveRecipientIDRequiresExactMatch(t *testing.T) {
	fg := newFakeGraphServer()
	// "alice@example.com" substring-matches both entries under the fake server's loose
	// $search simulation, but only one is an exact match.
	fg.users = []graphSearchResult{
		{ID: "user-1", Mail: "alice@example.com", DisplayName: "Alice"},
		{ID: "user-2", Mail: "alice@example.com.evil.test", DisplayName: "Alice Evil"},
	}
	srv := httptest.NewServer(fg)
	defer srv.Close()

	c := newTestClient(t, srv)
	id, kind, err := c.resolveRecipientID(t.Context(), "Bearer tok", "alice@example.com")
	if err != nil {
		t.Fatalf("resolveRecipientID() returned error: %v", err)
	}
	if id != "user-1" || kind != "user" {
		t.Fatalf("expected exact match user-1/user, got %q/%q", id, kind)
	}
}

func TestResolveRecipientIDErrorsOnAmbiguousMatch(t *testing.T) {
	fg := newFakeGraphServer()
	fg.users = []graphSearchResult{
		{ID: "user-1", Mail: "dup@example.com", DisplayName: "Dup One"},
		{ID: "user-2", Mail: "dup@example.com", DisplayName: "Dup Two"},
	}
	srv := httptest.NewServer(fg)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, _, err := c.resolveRecipientID(t.Context(), "Bearer tok", "dup@example.com")
	if err == nil {
		t.Fatal("expected an error for an ambiguous exact match, got nil")
	}
}

func TestResolveRecipientIDErrorsOnNoMatch(t *testing.T) {
	fg := newFakeGraphServer()
	srv := httptest.NewServer(fg)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, _, err := c.resolveRecipientID(t.Context(), "Bearer tok", "nobody@example.com")
	if err == nil {
		t.Fatal("expected an error when no user or group matches, got nil")
	}
}

func TestResolveRoleIDErrorsOnNoMatch(t *testing.T) {
	fg := newFakeGraphServer()
	fg.roles = nil
	srv := httptest.NewServer(fg)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.resolveRoleID(t.Context(), "Bearer tok", "viewer")
	if err == nil {
		t.Fatal("expected an error when no role definition matches, got nil")
	}
}
