// Package scheduler runs schedule-triggered workflows in the background, without any live
// user session — authenticating with each owner's stored app-password (see pkg/automation)
// instead of a forwarded bearer token.
package scheduler

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/LukasHirt/ocis-workflows/pkg/localdb"
	"github.com/LukasHirt/ocis-workflows/pkg/model"
)

// parser accepts both classic 5-field cron ("* * * * *", minute granularity) and an
// optional leading seconds field ("*/5 * * * * *") — the latter mainly so e2e tests (and
// anyone who genuinely wants sub-minute schedules) aren't stuck waiting on minute
// boundaries.
var parser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// TriggerStore is the subset of localdb.DB the scheduler reads.
type TriggerStore interface {
	ListScheduleTriggers(ctx context.Context) ([]localdb.TriggerIndexEntry, error)
	GetAutomation(ctx context.Context, userID string) (*localdb.Automation, error)
}

// WorkflowStore is the subset of webdavstore.Store the scheduler needs to run a workflow.
type WorkflowStore interface {
	Get(ctx context.Context, authHeader, id string) (*model.WorkflowDefinition, error)
	PutExecution(ctx context.Context, authHeader string, rec model.ExecutionRecord) error
}

// Executor runs a workflow's graph. Satisfied by *executor.Executor.
type Executor interface {
	Run(ctx context.Context, authHeader string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord
}

// Scheduler periodically checks for due schedule triggers and runs them.
type Scheduler struct {
	db       TriggerStore
	store    WorkflowStore
	executor Executor
	log      *slog.Logger
	interval time.Duration

	mu      sync.Mutex
	lastRun map[string]time.Time
}

// New builds a Scheduler that checks for due triggers every interval.
func New(db TriggerStore, store WorkflowStore, executor Executor, interval time.Duration, log *slog.Logger) *Scheduler {
	return &Scheduler{
		db:       db,
		store:    store,
		executor: executor,
		interval: interval,
		log:      log,
		lastRun:  map[string]time.Time{},
	}
}

// Start blocks, ticking every interval, until ctx is done.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	entries, err := s.db.ListScheduleTriggers(ctx)
	if err != nil {
		s.log.Error("scheduler: list schedule triggers", "error", err)
		return
	}

	now := time.Now()
	for _, e := range entries {
		schedule, err := parser.Parse(e.Schedule)
		if err != nil {
			s.log.Warn("scheduler: invalid cron expression", "workflowID", e.WorkflowID, "schedule", e.Schedule, "error", err)
			continue
		}

		last := s.lastRunFor(e.WorkflowID, now)
		next := schedule.Next(last)
		if next.After(now) {
			continue
		}

		s.setLastRun(e.WorkflowID, now)
		go s.runOne(ctx, e)
	}
}

func (s *Scheduler) lastRunFor(workflowID string, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.lastRun[workflowID]
	if !ok {
		// First time we've seen this workflow (just started, or just enabled) — treat
		// "now" as the baseline so we compute the *next* future occurrence, rather than
		// retroactively firing for whatever the schedule "missed" while we weren't
		// watching.
		s.lastRun[workflowID] = now
		return now
	}
	return last
}

func (s *Scheduler) setLastRun(workflowID string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRun[workflowID] = t
}

func (s *Scheduler) runOne(ctx context.Context, e localdb.TriggerIndexEntry) {
	automation, err := s.db.GetAutomation(ctx, e.UserID)
	if err != nil {
		s.log.Warn("scheduler: workflow has a schedule trigger but its owner has no automation connected",
			"workflowID", e.WorkflowID, "userID", e.UserID)
		return
	}

	authHeader := "Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", automation.Username, automation.AppPassword))

	wf, err := s.store.Get(ctx, authHeader, e.WorkflowID)
	if err != nil {
		s.log.Error("scheduler: load workflow", "workflowID", e.WorkflowID, "error", err)
		return
	}
	if !wf.Enabled {
		return
	}

	record := s.executor.Run(ctx, authHeader, *wf, "schedule", "")
	if err := s.store.PutExecution(ctx, authHeader, *record); err != nil {
		s.log.Error("scheduler: store execution record", "workflowID", e.WorkflowID, "error", err)
	}
}
