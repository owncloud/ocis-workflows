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
	"github.com/owncloud/ocis-workflows/pkg/reconcile"
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

// reconcileGracePeriod is both the minimum cursor age before a reconciliation pass runs
// and the debounce window for flapping SSE reconnects.
const reconcileGracePeriod = 5 * time.Second

// reconcileOverlapWindow is subtracted from a stored cursor before querying activitylog,
// trading a rare double-fire for never missing a boundary event.
const reconcileOverlapWindow = 5 * time.Second

// reconcileFirstConnectLookback is how far back a (user, drive) pair with no prior cursor
// looks on its very first reconciliation pass — generous relative to realistic SSE
// reconnect delays (covering a brand-new trigger racing the first SSE connection), but far
// short of activitylog's retention (so it doesn't flood-dispatch a busy drive's history the
// first time it's ever checked).
const reconcileFirstConnectLookback = 5 * time.Minute

// reconcileMaxConcurrent bounds how many reconciliation passes run at once instance-wide,
// so a fleet-wide SSE reconnect (e.g. oCIS's own sse service restarting) can't fire
// unbounded simultaneous activitylog queries.
const reconcileMaxConcurrent = 10

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

	reconciler := reconcile.New(db, ocisClient, ocisClient, ocisClient, graphExecutor, store,
		reconcileGracePeriod, reconcileOverlapWindow, reconcileFirstConnectLookback, reconcileMaxConcurrent, log)

	// sseManager is constructed before the handlers below so its Kick method can be wired
	// into them: both a workflow's event trigger being added and a user's automation being
	// connected should nudge the SSE manager to reconcile immediately instead of waiting for
	// its next periodic tick (up to sseReconcileInterval later).
	sseManager := sse.New(db, store, ocisClient, graphExecutor, reconciler, cfg.OCISURL, cfg.OCISInsecure, sseReconcileInterval, log)

	automationService := automation.New(ocisClient, db, sseManager, log)

	workflowsHandler := service.NewWorkflowsHandler(store, graphExecutor, ocisClient, db, sseManager, log)
	automationHandler := service.NewAutomationHandler(automationService, ocisClient)
	spacesHandler := service.NewSpacesHandler(ocisClient)

	apiHandler := httpserver.New(httpserver.Options{
		AllowedOrigin: cfg.AllowedOrigin,
		Validator:     validator,
		Workflows:     workflowsHandler,
		Automation:    automationHandler,
		Spaces:        spacesHandler,
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
