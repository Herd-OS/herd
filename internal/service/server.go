package service

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
	"github.com/herd-os/herd/internal/controlplane/reconciler"
	"github.com/herd-os/herd/internal/controlplane/workflowevents"
)

type Dependencies struct {
	Logger                       *log.Logger
	Store                        Store
	RegisterRepositoryRoute      http.Handler
	RunnerRegistrationTokenRoute http.Handler
	JobResultsRoute              http.Handler
	WorkflowEventsRoute          http.Handler
	IssueCommentCommandHandler   cpgithub.IssueCommentCommandHandler
	Reconciler                   *reconciler.Reconciler
	WorkflowEventProcessor       workflowevents.Processor
}

type Store interface {
	cpgithub.Store
	Health(ctx context.Context) error
}

func NewServer(cfg Config, deps Dependencies) (http.Handler, error) {
	if deps.Logger == nil {
		deps.Logger = log.New(os.Stderr, "herd-service: ", log.LstdFlags)
	}

	mux := http.NewServeMux()
	registerHealthRoutes(mux, cfg, deps)
	registerAPIRoutes(mux, cfg, deps)

	return mux, nil
}

func StartReconcilerLoop(ctx context.Context, cfg Config, deps Dependencies) (func() error, bool) {
	if !cfg.ReconcilerEnabled || deps.Reconciler == nil {
		return func() error { return nil }, false
	}
	interval := time.Duration(0)
	if cfg.ReconcilerInterval != "" {
		parsed, err := time.ParseDuration(cfg.ReconcilerInterval)
		if err != nil {
			return func() error { return err }, false
		}
		interval = parsed
	}
	if interval > 0 {
		deps.Reconciler.Config.Interval = interval
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		err := deps.Reconciler.Run(loopCtx)
		if err == context.Canceled {
			err = nil
		}
		done <- err
	}()
	return func() error {
		cancel()
		return <-done
	}, true
}
