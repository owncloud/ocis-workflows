// Package debug exposes a minimal health endpoint, separate from the public API server.
package debug

import (
	"net/http"
)

// New builds the debug server's handler.
func New() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}
