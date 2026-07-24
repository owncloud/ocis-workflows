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

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/service"
)

// Options configures the HTTP server's router.
type Options struct {
	AllowedOrigin string
	Validator     *auth.Validator
	Workflows     *service.WorkflowsHandler
	Automation    *service.AutomationHandler
	// Hooks serves the webhook trigger route, POST /hooks/{workflowId}/{token} —
	// deliberately mounted outside the /api/v1beta1 group below and its
	// Validator.Middleware bearer-token gate. See the route registration for why.
	Hooks  *service.HooksHandler
	Logger *slog.Logger
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

				// Reveal/rotate the webhook trigger's token — unlike the public
				// /hooks/... route below, these live behind the normal bearer-token
				// auth: they're how the *owner* fetches their own token to configure an
				// external caller with, not how the external caller reaches oCIS.
				r.Get("/webhook-token", opts.Workflows.WebhookToken)
				r.Post("/webhook-token/rotate", opts.Workflows.RotateWebhookToken)

				r.Route("/executions", func(r chi.Router) {
					r.Get("/", opts.Workflows.ListExecutions)
					r.Get("/{execId}", opts.Workflows.GetExecution)
				})
			})
		})

		r.Route("/me/automation", func(r chi.Router) {
			r.Get("/", opts.Automation.Get)
			r.Post("/", opts.Automation.Connect)
			r.Delete("/", opts.Automation.Disconnect)
		})
	})

	// The webhook trigger route is deliberately outside the /api/v1beta1 group above: it
	// bypasses Validator.Middleware's bearer-token check by design. An external caller
	// triggering this (a CI pipeline, another SaaS's outgoing webhook, a form submission)
	// has no oCIS session to present a bearer token from — the per-workflow token embedded
	// in the URL itself is the credential instead, checked in constant time against the
	// stored value inside HooksHandler.Trigger, with a request-rate limiter as the
	// compensating control against token-guessing/flooding that normal bearer auth would
	// otherwise have provided incidentally.
	r.Post("/hooks/{workflowId}/{token}", opts.Hooks.Trigger)

	return r
}
