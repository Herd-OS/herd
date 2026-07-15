package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/herd-os/herd/internal/appauth"
	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/runners"
	"github.com/herd-os/herd/internal/controlplane/workflowevents"
)

func registerAPIRoutes(mux *http.ServeMux, cfg Config, deps Dependencies) error {
	productionLike := cfg.Env == "production" || cfg.Env == "staging"
	webhookOptions := []cpgithub.Option{cpgithub.WithAppLogin(cfg.AppLogin)}
	if deps.IssueCommentCommandHandler != nil {
		webhookOptions = append(webhookOptions, cpgithub.WithIssueCommentCommandHandler(deps.IssueCommentCommandHandler))
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		if productionLike {
			return fmt.Errorf("webhook secret is not configured")
		}
		mux.Handle("POST /webhooks/github", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "webhook secret is not configured",
			})
		}))
	} else {
		mux.Handle("POST /webhooks/github", cpgithub.NewHandler(cfg.WebhookSecret, deps.Store, deps.Logger, webhookOptions...))
	}
	registerHandler := deps.RegisterRepositoryRoute
	if registerHandler == nil && deps.Store != nil {
		_, ok := deps.Store.(cpgithub.RegistrationStore)
		if !ok {
			registerHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "repository registration storage is not configured",
				})
			})
		}
	}
	if registerHandler == nil && deps.Store != nil {
		registrationStore := deps.Store.(cpgithub.RegistrationStore)
		var err error
		registerHandler, err = cpgithub.NewDefaultRegisterHandler(registrationStore, appauth.AppConfig{
			AppID:         cfg.GitHubAppID,
			PrivateKeyPEM: []byte(cfg.GitHubAppPrivateKey),
		}, cfg.AppLogin, cfg.PublicURL)
		if err != nil {
			if productionLike {
				return fmt.Errorf("repository registration GitHub App auth is not configured: %w", err)
			}
			registerHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": fmt.Sprintf("repository registration GitHub App auth is not configured: %s", err),
				})
			})
		}
	}
	if registerHandler == nil {
		registerHandler = http.HandlerFunc(notImplementedHandler)
	}
	mux.Handle("POST /api/v1/github/repositories/register", registerHandler)
	runnerTokenHandler := deps.RunnerRegistrationTokenRoute
	if runnerTokenHandler == nil && deps.Store != nil {
		runnerStore, ok := deps.Store.(runners.Store)
		if !ok {
			runnerTokenHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "runner registration storage is not configured",
				})
			})
		} else {
			var err error
			runnerTokenHandler, err = runners.NewDefaultRegistrationTokenHandler(runnerStore, appauth.AppConfig{
				AppID:         cfg.GitHubAppID,
				PrivateKeyPEM: []byte(cfg.GitHubAppPrivateKey),
			})
			if err != nil {
				if productionLike {
					return fmt.Errorf("runner registration GitHub App auth is not configured: %w", err)
				}
				runnerTokenHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, http.StatusInternalServerError, map[string]string{
						"error": fmt.Sprintf("runner registration GitHub App auth is not configured: %s", err),
					})
				})
			}
		}
	}
	if runnerTokenHandler == nil {
		runnerTokenHandler = http.HandlerFunc(notImplementedHandler)
	}
	mux.Handle("POST /api/v1/runners/registration-token", runnerTokenHandler)
	jobResultsHandler := deps.JobResultsRoute
	if jobResultsHandler == nil && deps.Store != nil {
		resultsStore, ok := deps.Store.(jobs.Store)
		if !ok {
			jobResultsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "job result storage is not configured",
				})
			})
		} else {
			jobResultsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "job result processors are not configured",
				})
			})
			_ = resultsStore
		}
	}
	if jobResultsHandler == nil {
		jobResultsHandler = http.HandlerFunc(notImplementedHandler)
	}
	mux.Handle("POST /api/v1/jobs/{job_id}/results", jobResultsHandler)
	workflowEventsHandler := deps.WorkflowEventsRoute
	if workflowEventsHandler == nil && deps.Store != nil {
		eventStore, ok := deps.Store.(workflowevents.Store)
		if !ok {
			workflowEventsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "workflow event storage is not configured",
				})
			})
		} else if deps.WorkflowEventProcessor == nil {
			workflowEventsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "workflow event processor is not configured",
				})
			})
			_ = eventStore
		} else {
			workflowEventsHandler = workflowevents.NewHandler(workflowevents.HandlerOptions{
				Store:     eventStore,
				Audience:  cfg.OIDCAudience,
				Processor: deps.WorkflowEventProcessor,
			})
		}
	}
	if workflowEventsHandler == nil {
		workflowEventsHandler = http.HandlerFunc(notImplementedHandler)
	}
	mux.Handle("POST /api/v1/workflow-events", workflowEventsHandler)
	return nil
}

func notImplementedHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
