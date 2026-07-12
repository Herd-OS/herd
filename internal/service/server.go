package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
	"github.com/herd-os/herd/internal/controlplane/reconciler"
	"github.com/herd-os/herd/internal/controlplane/runners"
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
	if err := validateProductionDependencies(cfg, deps); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	registerHealthRoutes(mux, cfg, deps)
	if err := registerAPIRoutes(mux, cfg, deps); err != nil {
		return nil, err
	}

	return mux, nil
}

func validateProductionDependencies(cfg Config, deps Dependencies) error {
	env := strings.TrimSpace(cfg.Env)
	if env != "production" && env != "staging" {
		return nil
	}
	var errs []error
	if deps.Store == nil {
		errs = append(errs, errors.New("production service store is not configured"))
	}
	if deps.IssueCommentCommandHandler == nil {
		errs = append(errs, errors.New("production issue-comment command handler is not configured"))
	}
	if deps.JobResultsRoute == nil {
		errs = append(errs, errors.New("production job result processor route is not configured"))
	}
	if deps.WorkflowEventsRoute == nil && deps.WorkflowEventProcessor == nil {
		errs = append(errs, errors.New("production workflow event processor is not configured"))
	}
	if deps.RegisterRepositoryRoute == nil {
		if deps.Store == nil {
			errs = append(errs, errors.New("production repository registration route is not configured"))
		} else if _, ok := deps.Store.(cpgithub.RegistrationStore); !ok {
			errs = append(errs, errors.New("production repository registration storage is not configured"))
		}
	}
	if deps.RunnerRegistrationTokenRoute == nil {
		if deps.Store == nil {
			errs = append(errs, errors.New("production runner registration route is not configured"))
		} else if _, ok := deps.Store.(runners.Store); !ok {
			errs = append(errs, errors.New("production runner registration storage is not configured"))
		}
	}
	if cfg.ReconcilerEnabled && deps.Reconciler == nil {
		errs = append(errs, errors.New("production reconciler is enabled but not configured"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("production service dependencies: %w", err)
	}
	return nil
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
