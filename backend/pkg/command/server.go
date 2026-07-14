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

	automationService := automation.New(ocisClient, db, log)

	workflowsHandler := service.NewWorkflowsHandler(store, graphExecutor, ocisClient, db, log)
	automationHandler := service.NewAutomationHandler(automationService, ocisClient)

	apiHandler := httpserver.New(httpserver.Options{
		AllowedOrigin: cfg.AllowedOrigin,
		Validator:     validator,
		Workflows:     workflowsHandler,
		Automation:    automationHandler,
		Logger:        log,
	})

	apiServer := &http.Server{Addr: cfg.HTTPAddr, Handler: apiHandler}
	debugSrv := &http.Server{Addr: cfg.DebugAddr, Handler: debugserver.New()}
	sched := scheduler.New(db, store, graphExecutor, scheduleTickInterval, log)
	sseManager := sse.New(db, store, ocisClient, graphExecutor, cfg.OCISURL, cfg.OCISInsecure, sseReconcileInterval, log)

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
		<-gCtx.Done()
		log.Info("shutting down")
		_ = apiServer.Shutdown(context.Background())
		_ = debugSrv.Shutdown(context.Background())
		return nil
	})

	return g.Wait()
}
