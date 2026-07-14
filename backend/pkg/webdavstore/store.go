// Package webdavstore persists workflow definitions and execution history as JSON files
// via WebDAV, in a hidden folder inside the caller's own oCIS space — the same public API
// our action nodes call, using the caller's own forwarded token. This is user content, not
// this sidecar's own operational state, so it belongs in the user's space, not a local DB.
package webdavstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

const rootDir = ".workflows"
const definitionsDir = "definitions"
const executionsDir = "executions"

// Store reads and writes WorkflowDefinition JSON files via WebDAV.
type Store struct {
	ocisURL    string
	ocisClient *ocisclient.Client
	httpClient *http.Client
}

// New builds a Store for the given oCIS base URL.
func New(ocisURL string, ocisClient *ocisclient.Client, insecure bool) *Store {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only opt-in
	}
	return &Store{
		ocisURL:    strings.TrimRight(ocisURL, "/"),
		ocisClient: ocisClient,
		httpClient: &http.Client{Transport: transport, Timeout: 20 * time.Second},
	}
}

func (s *Store) davBase(userID string) string {
	return s.ocisURL + "/remote.php/dav/files/" + userID
}

func (s *Store) ensureDirs(ctx context.Context, token, userID string) error {
	base := s.davBase(userID)
	for _, segment := range []string{rootDir, rootDir + "/" + definitionsDir} {
		if err := s.mkcol(ctx, token, base+"/"+segment); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) mkcol(ctx context.Context, token, url string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusCreated, http.StatusMethodNotAllowed, http.StatusConflict:
		// 201 created; 405/409 usually mean it already exists — treat as success.
		return nil
	default:
		return fmt.Errorf("MKCOL %s returned status %d", url, res.StatusCode)
	}
}

func (s *Store) definitionPath(userID, id string) string {
	return fmt.Sprintf("%s/%s/%s/%s.json", s.davBase(userID), rootDir, definitionsDir, id)
}

func (s *Store) executionsDirURL(userID, workflowID string) string {
	return fmt.Sprintf("%s/%s/%s/%s", s.davBase(userID), rootDir, executionsDir, workflowID)
}

func (s *Store) executionPath(userID, workflowID, execID string) string {
	return fmt.Sprintf("%s/%s.json", s.executionsDirURL(userID, workflowID), execID)
}

// PutExecution creates or overwrites an execution record.
func (s *Store) PutExecution(ctx context.Context, token string, rec model.ExecutionRecord) error {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	base := s.davBase(userID)
	for _, segment := range []string{rootDir, rootDir + "/" + executionsDir, rootDir + "/" + executionsDir + "/" + rec.WorkflowID} {
		if err := s.mkcol(ctx, token, base+"/"+segment); err != nil {
			return fmt.Errorf("ensure execution storage folders: %w", err)
		}
	}

	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.executionPath(userID, rec.WorkflowID, rec.ID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	res, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("PUT execution record returned status %d", res.StatusCode)
	}
	return nil
}

// GetExecution returns a single execution record.
func (s *Store) GetExecution(ctx context.Context, token, workflowID, execID string) (*model.ExecutionRecord, error) {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.executionPath(userID, workflowID, execID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET execution record returned status %d", res.StatusCode)
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var rec model.ExecutionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode stored execution record: %w", err)
	}
	return &rec, nil
}

// ListExecutions returns every execution record for a workflow, most recent first.
func (s *Store) ListExecutions(ctx context.Context, token, workflowID string) ([]model.ExecutionRecord, error) {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}

	names, err := s.propfindJSONNames(ctx, token, s.executionsDirURL(userID, workflowID))
	if err != nil {
		// The executions folder for this workflow may not exist yet — that's an empty
		// history, not an error.
		return []model.ExecutionRecord{}, nil //nolint:nilerr // see comment above
	}

	records := make([]model.ExecutionRecord, 0, len(names))
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		rec, err := s.GetExecution(ctx, token, workflowID, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		records = append(records, *rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StartedDateTime > records[j].StartedDateTime })
	return records, nil
}

// List returns every workflow definition stored in the caller's space.
func (s *Store) List(ctx context.Context, token string) ([]model.WorkflowDefinition, error) {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}

	if err := s.ensureDirs(ctx, token, userID); err != nil {
		return nil, fmt.Errorf("ensure storage folders: %w", err)
	}

	dirURL := fmt.Sprintf("%s/%s/%s", s.davBase(userID), rootDir, definitionsDir)
	names, err := s.propfindJSONNames(ctx, token, dirURL)
	if err != nil {
		return nil, err
	}

	workflows := make([]model.WorkflowDefinition, 0, len(names))
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		wf, err := s.getByPath(ctx, token, s.definitionPath(userID, id))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		workflows = append(workflows, *wf)
	}
	return workflows, nil
}

// Get returns a single workflow definition by id.
func (s *Store) Get(ctx context.Context, token, id string) (*model.WorkflowDefinition, error) {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	return s.getByPath(ctx, token, s.definitionPath(userID, id))
}

// Put creates or overwrites a workflow definition.
func (s *Store) Put(ctx context.Context, token string, wf model.WorkflowDefinition) error {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	if err := s.ensureDirs(ctx, token, userID); err != nil {
		return fmt.Errorf("ensure storage folders: %w", err)
	}

	body, err := json.Marshal(wf)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.definitionPath(userID, wf.ID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	res, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("PUT workflow definition returned status %d", res.StatusCode)
	}
	return nil
}

// Delete removes a workflow definition by id.
func (s *Store) Delete(ctx context.Context, token, id string) error {
	userID, err := s.ocisClient.Me(ctx, token)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.definitionPath(userID, id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("DELETE workflow definition returned status %d", res.StatusCode)
	}
}

func (s *Store) getByPath(ctx context.Context, token, url string) (*model.WorkflowDefinition, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET workflow definition returned status %d", res.StatusCode)
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var wf model.WorkflowDefinition
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("decode stored workflow definition: %w", err)
	}
	return &wf, nil
}

// multistatus mirrors just enough of RFC 4918's PROPFIND response to list child hrefs.
type multistatus struct {
	XMLName   xml.Name   `xml:"multistatus"`
	Responses []response `xml:"response"`
}

type response struct {
	Href string `xml:"href"`
}

func (s *Store) propfindJSONNames(ctx context.Context, token, dirURL string) ([]string, error) {
	body := `<?xml version="1.0" encoding="utf-8" ?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", dirURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")

	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != 207 { // Multi-Status
		return nil, fmt.Errorf("PROPFIND %s returned status %d", dirURL, res.StatusCode)
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var ms multistatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, fmt.Errorf("decode PROPFIND response: %w", err)
	}

	names := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		name := path.Base(strings.TrimSuffix(r.Href, "/"))
		if strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	return names, nil
}
