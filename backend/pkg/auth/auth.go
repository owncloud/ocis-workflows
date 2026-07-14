// Package auth validates incoming requests the same way ai-llm-proxy does: independently,
// by calling the oCIS IdP's userinfo endpoint, rather than trusting any oCIS-internal
// proxy-minted header. This backend is a sidecar, not a service registered in oCIS's own
// proxy trust chain, so it cannot rely on oCIS's internal x-access-token/reva-JWT mechanism.
package auth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type contextKey string

const tokenContextKey contextKey = "workflows-token"

// Validator validates bearer tokens against an oCIS instance's OIDC discovery document.
type Validator struct {
	ocisURL       string
	allowedOrigin string
	httpClient    *http.Client

	mu               sync.RWMutex
	userinfoEndpoint string
}

// NewValidator builds a Validator for the given oCIS base URL. When insecure is true, TLS
// certificate verification is skipped for calls to oCIS — dev/self-signed-cert use only.
func NewValidator(ocisURL, allowedOrigin string, insecure bool) *Validator {
	transport := &http.Transport{}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only opt-in
	}

	return &Validator{
		ocisURL:       strings.TrimRight(ocisURL, "/"),
		allowedOrigin: allowedOrigin,
		httpClient:    &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
}

type discoveryDocument struct {
	UserinfoEndpoint string `json:"userinfo_endpoint"`
}

func (v *Validator) discover(ctx context.Context) (string, error) {
	v.mu.RLock()
	if v.userinfoEndpoint != "" {
		endpoint := v.userinfoEndpoint
		v.mu.RUnlock()
		return endpoint, nil
	}
	v.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		v.ocisURL+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}

	res, err := v.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc discovery returned status %d", res.StatusCode)
	}

	var doc discoveryDocument
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return "", err
	}
	if doc.UserinfoEndpoint == "" {
		return "", fmt.Errorf("oidc discovery document has no userinfo_endpoint")
	}

	v.mu.Lock()
	v.userinfoEndpoint = doc.UserinfoEndpoint
	v.mu.Unlock()

	return doc.UserinfoEndpoint, nil
}

// Validate checks a bearer token against the oCIS IdP's userinfo endpoint.
func (v *Validator) Validate(ctx context.Context, token string) error {
	endpoint, err := v.discover(ctx)
	if err != nil {
		return fmt.Errorf("discover userinfo endpoint: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := v.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("userinfo endpoint returned status %d", res.StatusCode)
	}
	return nil
}

// OriginAllowed reports whether the given Origin header value matches the allowed origin.
func (v *Validator) OriginAllowed(origin string) bool {
	if origin == "" {
		return true // non-browser clients (server-to-server, curl, the e2e suite) send no Origin
	}
	allowed, err := url.Parse(v.allowedOrigin)
	if err != nil {
		return false
	}
	got, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return got.Scheme == allowed.Scheme && got.Host == allowed.Host
}

// Middleware validates the Origin header and the bearer token on every request, and
// attaches the validated token to the request context for downstream handlers to forward
// to oCIS's own APIs (WebDAV, Graph) on the caller's behalf.
func (v *Validator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); !v.OriginAllowed(origin) {
			writeError(w, http.StatusForbidden, "originNotAllowed", "request origin is not allowed")
			return
		}

		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
			return
		}

		if err := v.Validate(r.Context(), token); err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid or expired bearer token")
			return
		}

		ctx := context.WithValue(r.Context(), tokenContextKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}

// TokenFromContext returns the bearer token attached by Middleware, if any.
func TokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(tokenContextKey).(string)
	return token, ok && token != ""
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
