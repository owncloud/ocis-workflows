//go:build e2e

// Package e2e contains black-box tests that call this backend's real HTTP API over the
// network (through Traefik, exactly as a real client would) against a real, running
// docker-compose stack — no mocks anywhere in the request path. Run via:
//
//	docker compose up -d
//	go test -tags=e2e ./tests/e2e/...
//
// Getting a real oCIS bearer token requires a real login: oCIS's built-in IdP sign-in page
// hashes credentials client-side, so there's no plain HTTP request to replay. login()
// below shells out to a small headless-Playwright script shared with the frontend e2e
// suite (../../../frontend/tests/e2e/support/get-token.ts) to acquire one via a real
// browser session, then every actual assertion in this package is plain Go net/http.
package e2e

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	ocisURL  = envOr("E2E_OCIS_URL", "https://host.docker.internal:9200")
	baseURL  = envOr("E2E_BASE_URL", "https://host.docker.internal:9200/workflows/api/v1beta1")
	username = envOr("E2E_USERNAME", "admin")
	password = envOr("E2E_PASSWORD", "admin")
)

func httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // dev-only self-signed certs
		Timeout:   30 * time.Second,
	}
}

type tokenResult struct {
	AccessToken string `json:"accessToken"`
}

// login acquires a real oCIS bearer token by driving a real headless-browser login.
func login(t *testing.T) string {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	scriptPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "frontend", "tests", "e2e", "support", "get-token.ts")

	cmd := exec.Command("node", scriptPath, ocisURL, username, password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("acquire e2e token via %s: %v\noutput: %s", scriptPath, err, out)
	}

	var result tokenResult
	// get-token.ts writes only the JSON object to stdout; CombinedOutput may also have
	// captured stray stderr noise from the browser, so decode just the JSON tail.
	jsonStart := strings.IndexByte(string(out), '{')
	if jsonStart < 0 {
		t.Fatalf("get-token.ts produced no JSON output: %s", out)
	}
	if err := json.Unmarshal(out[jsonStart:], &result); err != nil {
		t.Fatalf("decode get-token.ts output: %v\noutput: %s", err, out)
	}
	if result.AccessToken == "" {
		t.Fatalf("get-token.ts returned an empty access token: %s", out)
	}
	return result.AccessToken
}
