// Package llm calls a directly-configured OpenAI-compatible /chat/completions endpoint.
// There is no external LLM proxy: this backend holds LLM_ENDPOINT/LLM_API_KEY itself and
// calls the provider server-to-server, regardless of what triggered the workflow run.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client calls an OpenAI-compatible chat completions API.
type Client struct {
	endpoint   string
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
}

// New builds a Client. endpoint is the base URL (e.g. "https://api.openai.com/v1" or a
// local/self-hosted OpenAI-compatible server), without a trailing "/chat/completions".
func New(endpoint, apiKey, model string, maxTokens int) *Client {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		apiKey:     apiKey,
		model:      model,
		maxTokens:  maxTokens,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Configured reports whether an LLM endpoint has been set.
func (c *Client) Configured() bool {
	return c != nil && c.endpoint != ""
}

type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Complete sends messages to the configured model and returns the assistant's reply text.
// Only model/messages/max_tokens are ever sent — no other caller-supplied fields, and
// max_tokens is always clamped to the server-configured ceiling regardless of what's asked.
func (c *Client) Complete(ctx context.Context, messages []Message, modelOverride string, maxTokens int) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("no LLM endpoint configured")
	}

	model := c.model
	if modelOverride != "" {
		model = modelOverride
	}
	if maxTokens <= 0 || maxTokens > c.maxTokens {
		maxTokens = c.maxTokens
	}

	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, MaxTokens: maxTokens})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("llm endpoint returned status %d: %s", res.StatusCode, string(data))
	}

	var parsed chatResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm response had no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
