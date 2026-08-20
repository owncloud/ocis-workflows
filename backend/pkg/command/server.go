package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/owncloud/ocis-workflows/pkg/auth"
	"github.com/owncloud/ocis-workflows/pkg/automation"
	"github.com/owncloud/ocis-workflows/pkg/config"
	"github.com/owncloud/ocis-workflows/pkg/executor"
	"github.com/owncloud/ocis-workflows/pkg/llm"
	"github.com/owncloud/ocis-workflows/pkg/localdb"
	"github.com/owncloud/ocis-workflows/pkg/logging"
	"github.com/owncloud/ocis-workflows/pkg/ocisclient"
	"github.com/owncloud/ocis-workflows/pkg/ratelimit"
	"github.com/owncloud/ocis-workflows/pkg/scheduler"
	debugserver "github.com/owncloud/ocis-workflows/pkg/server/debug"
	httpserver "github.com/owncloud/ocis-workflows/pkg/server/http"
	"github.com/owncloud/ocis-workflows/pkg/service"
	"github.com/owncloud/ocis-workflows/pkg/sse"
	"github.com/owncloud/ocis-workflows/pkg/webdavfile"
	"github.com/owncloud/ocis-workflows/pkg/webdavstore"
)

// scheduleTickInterval controls how often the scheduler checks for due schedule triggers.
const scheduleTickInterval = 10 * time.Second

// sseReconcileInterval controls how often the SSE manager checks which users need an
// active event-trigger consumer.
const sseReconcileInterval = 30 * time.Second

// renewalTickInterval controls how often the automation service checks for app-passwords
// nearing expiry. Daily is frequent enough given the 14-day renewal window and 90-day
// credential lifetime.
const renewalTickInterval = 24 * time.Hour

// webhookRateLimitMax/webhookRateLimitWindow bound how often a single webhook trigger
// token may be used. A compensating control for a route that, by design, bypasses
// Validator.Middleware's normal bearer-token auth (see pkg/server/http/server.go) — the
// URL's token is the only gate, so nothing else stops a caller from flooding a known
// (or guessed) one.
const webhookRateLimitMax = 30
const webhookRateLimitWindow = time.Minute

// RunServer starts the public API server, the debug server, and the background schedule
// evaluator, and blocks until any of them exits or the process receives an interrupt/
// termination signal.
func RunServer(cfg config.Config) error {
	log := logging.New(cfg.LogLevel)
	if cfg.EncryptionKeyGenerated() {
		log.Warn("WORKFLOWS_ENCRYPTION_KEY not set — using a randomly generated key for this run. " +
			"Automation (scheduled/event triggers) will need to be reconnected after every restart.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ocisClient := ocisclient.New(cfg.OCISURL, cfg.OCISInsecure)
	store := webdavstore.New(cfg.OCISURL, ocisClient, cfg.OCISInsecure)
	files := webdavfile.New(cfg.OCISURL, ocisClient, cfg.OCISInsecure)
	llmClient := llm.New(cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMMaxTokens)
	graphExecutor := executor.New(llmClient, files, ocisClient, log)
	validator := auth.NewValidator(cfg.OCISURL, cfg.AllowedOrigin, cfg.OCISInsecure)

	db, err := localdb.Open(cfg.DBPath, cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("open local database: %w", err)
	}
	defer db.Close()

	// sseManager is constructed before the handlers below so its Kick method can be wired
	// into them: both a workflow's event trigger being added and a user's automation being
	// connected should nudge the SSE manager to reconcile immediately instead of waiting for
	// its next periodic tick (up to sseReconcileInterval later).
	sseManager := sse.New(db, store, ocisClient, graphExecutor, cfg.OCISURL, cfg.OCISInsecure, sseReconcileInterval, log)

	automationService := automation.New(ocisClient, db, sseManager, log)

	workflowsHandler := service.NewWorkflowsHandler(store, graphExecutor, ocisClient, db, sseManager, log)
	automationHandler := service.NewAutomationHandler(automationService, ocisClient)
	webhookLimiter := ratelimit.New(webhookRateLimitMax, webhookRateLimitWindow)
	hooksHandler := service.NewHooksHandler(db, db, store, graphExecutor, webhookLimiter, log)

	apiHandler := httpserver.New(httpserver.Options{
		AllowedOrigin: cfg.AllowedOrigin,
		Validator:     validator,
		Workflows:     workflowsHandler,
		Automation:    automationHandler,
		Hooks:         hooksHandler,
		Logger:        log,
	})

	apiServer := &http.Server{Addr: cfg.HTTPAddr, Handler: apiHandler}
	debugSrv := &http.Server{Addr: cfg.DebugAddr, Handler: debugserver.New()}
	sched := scheduler.New(db, store, graphExecutor, scheduleTickInterval, log)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("starting api server", "addr", cfg.HTTPAddr)
		if err := apiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("api server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		log.Info("starting debug server", "addr", cfg.DebugAddr)
		if err := debugSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("debug server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		log.Info("starting schedule evaluator", "interval", scheduleTickInterval)
		sched.Start(gCtx)
		return nil
	})

	g.Go(func() error {
		log.Info("starting sse event-trigger manager", "reconcileInterval", sseReconcileInterval)
		sseManager.Start(gCtx)
		return nil
	})

	g.Go(func() error {
		log.Info("starting automation renewal loop", "interval", renewalTickInterval)
		automationService.StartRenewalLoop(gCtx, renewalTickInterval)
		return nil
	})

	g.Go(func() error {
		<-gCtx.Done()
		log.Info("shutting down")
		_ = apiServer.Shutdown(context.Background())
		_ = debugSrv.Shutdown(context.Background())
		return nil
	})

	return g.Wait()
}
