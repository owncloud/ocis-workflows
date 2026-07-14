// Command fakellm is a minimal OpenAI-compatible /chat/completions stub, used only by the
// local dev/e2e docker-compose stack so tests don't depend on a real LLM provider being
// reachable. It always returns the same deterministic response.
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
}

const fakeReply = "This is a fake LLM response for testing."

func main() {
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message message `json:"message"`
			}{{Message: message{Role: "assistant", Content: fakeReply}}},
		})
	})

	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil)) //nolint:gosec // dev-only fixture, no timeouts needed
}
