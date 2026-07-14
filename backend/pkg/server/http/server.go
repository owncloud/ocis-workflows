// Package http builds the public-facing chi router: CORS, bearer-token auth, and the
// Graph-shaped /me/workflows routes. This is the whole surface reachable through Traefik.
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/LukasHirt/ocis-workflows/pkg/auth"
	"github.com/LukasHirt/ocis-workflows/pkg/service"
)

// Options configures the HTTP server's router.
type Options struct {
	AllowedOrigin string
	Validator     *auth.Validator
	Workflows     *service.WorkflowsHandler
	Logger        *slog.Logger
}

// New builds the router for the workflows public API.
func New(opts Options) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	// Generous enough to cover a synchronous workflow run (LLM calls alone are allowed up
	// to 60s by pkg/llm), not just simple CRUD requests.
	r.Use(middleware.Timeout(90 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{opts.AllowedOrigin},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Route("/api/v1beta1", func(r chi.Router) {
		r.Use(opts.Validator.Middleware)

		r.Route("/me/workflows", func(r chi.Router) {
			r.Get("/", opts.Workflows.List)
			r.Post("/", opts.Workflows.Create)

			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", opts.Workflows.Get)
				r.Patch("/", opts.Workflows.Patch)
				r.Delete("/", opts.Workflows.Delete)
				r.Post("/run", opts.Workflows.Run)

				r.Route("/executions", func(r chi.Router) {
					r.Get("/", opts.Workflows.ListExecutions)
					r.Get("/{execId}", opts.Workflows.GetExecution)
				})
			})
		})
	})

	return r
}
