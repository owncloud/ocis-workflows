// Package config holds the workflows backend's runtime configuration, loaded from
// environment variables (prefix WORKFLOWS_). There is no config file support and no
// dependency on oCIS's own config machinery — this is a standalone sidecar.
package config

import (
	"crypto/rand"
	"encoding/base64"
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
	// EncryptionKey (32 bytes) encrypts app-passwords at rest in the local database. If
	// unset, a random key is generated at startup — automation still works, but stored
	// app-passwords become undecryptable across a restart. Set this in any deployment
	// where automation (scheduled/event triggers) needs to survive a restart.
	EncryptionKey []byte
	// LogLevel controls slog verbosity: debug, info, warn, error.
	LogLevel string

	encryptionKeyGenerated bool
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

	key, generated, err := loadOrGenerateEncryptionKey()
	if err != nil {
		return Config{}, err
	}
	cfg.EncryptionKey = key
	cfg.encryptionKeyGenerated = generated

	return cfg, nil
}

// EncryptionKeyGenerated reports whether EncryptionKey was randomly generated (as opposed
// to loaded from WORKFLOWS_ENCRYPTION_KEY) — callers should log a warning in that case.
func (c Config) EncryptionKeyGenerated() bool {
	return c.encryptionKeyGenerated
}

func loadOrGenerateEncryptionKey() (key []byte, generated bool, err error) {
	if encoded := os.Getenv("WORKFLOWS_ENCRYPTION_KEY"); encoded != "" {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false, fmt.Errorf("WORKFLOWS_ENCRYPTION_KEY is not valid base64: %w", err)
		}
		if len(key) != 32 {
			return nil, false, fmt.Errorf("WORKFLOWS_ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key))
		}
		return key, false, nil
	}

	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("generate encryption key: %w", err)
	}
	return key, true, nil
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
