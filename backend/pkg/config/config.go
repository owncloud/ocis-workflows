// Package config holds the workflows backend's runtime configuration, loaded from
// environment variables (prefix WORKFLOWS_). There is no config file support and no
// dependency on oCIS's own config machinery — this is a standalone sidecar.
package config

import (
	"fmt"
	"os"
	"strconv"
)

const defaultLLMMaxTokens = 4096

// Config is the full set of settings the backend needs to run.
type Config struct {
	// HTTPAddr is the address the public API server listens on.
	HTTPAddr string
	// DebugAddr is the address the health/metrics server listens on.
	DebugAddr string
	// OCISURL is the base URL of the oCIS instance this sidecar talks to.
	OCISURL string
	// OCISInsecure skips TLS certificate verification for oCIS calls (dev only).
	OCISInsecure bool
	// AllowedOrigin is the only Origin header accepted on browser-facing requests.
	AllowedOrigin string
	// LLMEndpoint is the base URL of an OpenAI-compatible /chat/completions API this
	// backend calls directly — never exposed to any caller, no external proxy involved.
	LLMEndpoint string
	// LLMAPIKey authenticates this backend to LLMEndpoint. Server-side secret only.
	LLMAPIKey string
	// LLMModel is the model name sent with every completion request.
	LLMModel string
	// LLMMaxTokens caps max_tokens on every completion request, regardless of what a
	// workflow's llm node asks for.
	LLMMaxTokens int
	// DBPath is where the sidecar's own local operational database lives.
	DBPath string
	// LogLevel controls slog verbosity: debug, info, warn, error.
	LogLevel string
}

// FromEnv loads a Config from environment variables, applying sensible dev defaults for
// anything not explicitly set.
func FromEnv() (Config, error) {
	cfg := Config{
		HTTPAddr:      getEnv("WORKFLOWS_HTTP_ADDR", "0.0.0.0:9105"),
		DebugAddr:     getEnv("WORKFLOWS_DEBUG_ADDR", "0.0.0.0:9109"),
		OCISURL:       getEnv("WORKFLOWS_OCIS_URL", "https://host.docker.internal:9200"),
		OCISInsecure:  getEnvBool("WORKFLOWS_OCIS_INSECURE", false),
		AllowedOrigin: getEnv("WORKFLOWS_ALLOWED_ORIGIN", ""),
		LLMEndpoint:   getEnv("WORKFLOWS_LLM_ENDPOINT", ""),
		LLMAPIKey:     getEnv("WORKFLOWS_LLM_API_KEY", ""),
		LLMModel:      getEnv("WORKFLOWS_LLM_MODEL", ""),
		LLMMaxTokens:  getEnvInt("WORKFLOWS_LLM_MAX_TOKENS", defaultLLMMaxTokens),
		DBPath:        getEnv("WORKFLOWS_DB_PATH", "workflows.db"),
		LogLevel:      getEnv("WORKFLOWS_LOG_LEVEL", "info"),
	}

	if cfg.OCISURL == "" {
		return Config{}, fmt.Errorf("WORKFLOWS_OCIS_URL must not be empty")
	}
	if cfg.AllowedOrigin == "" {
		cfg.AllowedOrigin = cfg.OCISURL
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
