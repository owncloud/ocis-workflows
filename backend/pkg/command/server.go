package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/LukasHirt/ocis-workflows/pkg/auth"
	"github.com/LukasHirt/ocis-workflows/pkg/config"
	"github.com/LukasHirt/ocis-workflows/pkg/executor"
	"github.com/LukasHirt/ocis-workflows/pkg/llm"
	"github.com/LukasHirt/ocis-workflows/pkg/logging"
	"github.com/LukasHirt/ocis-workflows/pkg/ocisclient"
	debugserver "github.com/LukasHirt/ocis-workflows/pkg/server/debug"
	httpserver "github.com/LukasHirt/ocis-workflows/pkg/server/http"
	"github.com/LukasHirt/ocis-workflows/pkg/service"
	"github.com/LukasHirt/ocis-workflows/pkg/webdavfile"
	"github.com/LukasHirt/ocis-workflows/pkg/webdavstore"
)

// RunServer starts the public API server and the debug server, and blocks until either
// exits or the process receives an interrupt/termination signal.
func RunServer(cfg config.Config) error {
	log := logging.New(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ocisClient := ocisclient.New(cfg.OCISURL, cfg.OCISInsecure)
	store := webdavstore.New(cfg.OCISURL, ocisClient, cfg.OCISInsecure)
	files := webdavfile.New(cfg.OCISURL, ocisClient, cfg.OCISInsecure)
	llmClient := llm.New(cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMMaxTokens)
	graphExecutor := executor.New(llmClient, files, ocisClient, log)
	workflowsHandler := service.NewWorkflowsHandler(store, graphExecutor, log)
	validator := auth.NewValidator(cfg.OCISURL, cfg.AllowedOrigin, cfg.OCISInsecure)

	apiHandler := httpserver.New(httpserver.Options{
		AllowedOrigin: cfg.AllowedOrigin,
		Validator:     validator,
		Workflows:     workflowsHandler,
		Logger:        log,
	})

	apiServer := &http.Server{Addr: cfg.HTTPAddr, Handler: apiHandler}
	debugSrv := &http.Server{Addr: cfg.DebugAddr, Handler: debugserver.New()}

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
		<-gCtx.Done()
		log.Info("shutting down")
		_ = apiServer.Shutdown(context.Background())
		_ = debugSrv.Shutdown(context.Background())
		return nil
	})

	return g.Wait()
}
