package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/commands"
	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/reconciler"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/runners"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/controlplane/workflowevents"
	"github.com/herd-os/herd/internal/service"
)

type productionDependencyOptions struct {
	ArtifactStore          artifacts.Store
	CommandDispatcher      commands.CommandDispatcher
	WorkflowEventProcessor workflowevents.Processor
	OIDCValidator          jobs.OIDCValidator
}

type productionStore interface {
	service.Store
	jobs.Store
	cpdispatch.Store
	review.StatusStore
	review.StatusIdempotencyStore
	review.StatusMutationStore
	review.ReviewMutationStore
	review.LockStore
	reconciler.Store
	cpgithub.RegistrationStore
	runners.Store
	workflowevents.Store
	commands.Store
}

func buildServiceDependencies(cfg service.Config, st *store.PostgresStore, logger *log.Logger) (service.Dependencies, error) {
	return buildServiceDependenciesWithOptions(cfg, st, logger, productionDependencyOptions{})
}

func buildServiceDependenciesWithOptions(cfg service.Config, st productionStore, logger *log.Logger, opts productionDependencyOptions) (service.Dependencies, error) {
	deps := service.Dependencies{
		Logger: logger,
		Store:  st,
	}
	if cfg.ReconcilerEnabled && st != nil {
		deps.Reconciler = &reconciler.Reconciler{Store: st}
	}
	if st == nil || !productionLike(cfg) {
		return deps, nil
	}

	appCfg := appauth.AppConfig{
		AppID:         cfg.GitHubAppID,
		PrivateKeyPEM: []byte(cfg.GitHubAppPrivateKey),
	}
	tokenSource, _, err := appauth.NewGitHubTokenSource(appCfg)
	if err != nil {
		return service.Dependencies{}, fmt.Errorf("configure GitHub App authentication: %w", err)
	}
	appLogin := strings.TrimSpace(cfg.AppLogin)
	reviewGitHub := review.AppGitHubClient{TokenSource: tokenSource, AppLogin: appLogin}
	workflowDispatcher := cpdispatch.Dispatcher{
		Store:  st,
		GitHub: cpdispatch.NewAppWorkflowClient(tokenSource),
	}
	reviewService := review.ReviewService{
		Status: review.StatusService{
			Store:  st,
			GitHub: reviewGitHub,
		},
		GitHub:     reviewGitHub,
		Mutations:  st,
		Locks:      st,
		Dispatcher: workflowDispatcher,
	}
	if opts.CommandDispatcher == nil {
		dispatcher := productionCommandDispatcher{
			Dispatcher:      workflowDispatcher,
			ControlPlaneURL: cfg.PublicURL,
			DefaultWorkflow: "herd-worker.yml",
			DefaultRef:      "main",
			DefaultRunner:   "",
			TimeoutMinutes:  0,
		}
		opts.CommandDispatcher = dispatcher
	}
	if opts.WorkflowEventProcessor == nil {
		opts.WorkflowEventProcessor = productionWorkflowEventProcessor{}
	}
	if opts.ArtifactStore == nil {
		opts.ArtifactStore = productionArtifactStore{tokenSource: tokenSource, store: st}
	}

	registerRoute, err := cpgithub.NewDefaultRegisterHandler(st, appCfg, appLogin, cfg.PublicURL)
	if err != nil {
		return service.Dependencies{}, fmt.Errorf("configure repository registration route: %w", err)
	}
	runnerRoute, err := runners.NewDefaultRegistrationTokenHandler(st, appCfg)
	if err != nil {
		return service.Dependencies{}, fmt.Errorf("configure runner registration route: %w", err)
	}
	validator := opts.OIDCValidator
	if validator == nil {
		validator = jobs.NewJWKSValidator(cfg.OIDCAudience)
	}

	deps.RegisterRepositoryRoute = registerRoute
	deps.RunnerRegistrationTokenRoute = runnerRoute
	deps.JobResultsRoute = jobs.NewHandler(jobs.HandlerOptions{
		Store:           st,
		Validator:       validator,
		Audience:        cfg.OIDCAudience,
		ArtifactStore:   opts.ArtifactStore,
		AppTokenSource:  tokenSource,
		AppLogin:        appLogin + "[bot]",
		AppEmail:        appLogin + "[bot]@users.noreply.github.com",
		ReviewProcessor: reviewService,
	})
	deps.WorkflowEventProcessor = opts.WorkflowEventProcessor
	deps.WorkflowEventsRoute = workflowevents.NewHandler(workflowevents.HandlerOptions{
		Store:     st,
		Validator: validator,
		Audience:  cfg.OIDCAudience,
		Processor: opts.WorkflowEventProcessor,
	})
	deps.IssueCommentCommandHandler = commands.Handler{
		AppLogin:   appLogin,
		Store:      st,
		GitHub:     commandGitHub{store: st, tokenSource: tokenSource},
		Dispatcher: opts.CommandDispatcher,
	}
	return deps, nil
}

type productionCommandDispatcher struct {
	Dispatcher      cpdispatch.Dispatcher
	ControlPlaneURL string
	DefaultWorkflow string
	DefaultRef      string
	DefaultRunner   string
	TimeoutMinutes  int
}

func (d productionCommandDispatcher) DispatchCommand(ctx context.Context, cmd commands.DispatchCommand) error {
	kind, err := commandJobKind(cmd.Command.Kind)
	if err != nil {
		return err
	}
	_, err = d.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:          cmd.RepositoryID,
		Owner:           cmd.Owner,
		Repo:            cmd.Repo,
		InstallationID:  cmd.InstallationID,
		Kind:            kind,
		WorkflowFile:    firstNonEmptyString(d.DefaultWorkflow, "herd-worker.yml"),
		Ref:             firstNonEmptyString(d.DefaultRef, "main"),
		BatchNumber:     1,
		IssueNumber:     cmd.IssueNumber,
		PRNumber:        cmd.PRNumber,
		RunnerLabel:     d.DefaultRunner,
		TimeoutMinutes:  d.TimeoutMinutes,
		ControlPlaneURL: d.ControlPlaneURL,
		Reason:          "issue_comment_command",
	})
	return err
}

func commandJobKind(kind commands.CommandKind) (cpdispatch.JobKind, error) {
	switch kind {
	case commands.CommandReview:
		return cpdispatch.JobKindReview, nil
	case commands.CommandFix:
		return cpdispatch.JobKindReviewFix, nil
	case commands.CommandFixCI:
		return cpdispatch.JobKindCIFix, nil
	default:
		return "", fmt.Errorf("command %q is not dispatchable", kind)
	}
}

type productionWorkflowEventProcessor struct{}

func (productionWorkflowEventProcessor) ProcessWorkflowEvent(context.Context, store.Repository, workflowevents.Event) error {
	return nil
}

type productionArtifactStore struct {
	tokenSource appauth.TokenSource
	store       interface {
		GetRepository(ctx context.Context, owner string, name string) (store.Repository, error)
	}
}

func (s productionArtifactStore) OpenArtifact(context.Context, string) (io.ReadCloser, error) {
	if s.tokenSource == nil || s.store == nil {
		return nil, fmt.Errorf("production artifact store is not configured")
	}
	return nil, fmt.Errorf("production artifact fetching is not implemented")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func productionLike(cfg service.Config) bool {
	return cfg.Env == "production" || cfg.Env == "staging"
}

type commandGitHub struct {
	store interface {
		GetRepository(ctx context.Context, owner string, name string) (store.Repository, error)
	}
	tokenSource appauth.TokenSource
}

func (g commandGitHub) AddIssueComment(ctx context.Context, owner, repo string, issueNumber int, body string) (int64, error) {
	if g.store == nil {
		return 0, fmt.Errorf("command GitHub repository store is not configured")
	}
	registered, err := g.store.GetRepository(ctx, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("lookup repository for command acknowledgement: %w", err)
	}
	client, _, err := appauth.NewInstallationClient(ctx, g.tokenSource, registered.InstallationID)
	if err != nil {
		return 0, err
	}
	comment, _, err := client.Issues.CreateComment(ctx, owner, repo, issueNumber, &gh.IssueComment{Body: gh.Ptr(body)})
	if err != nil {
		return 0, fmt.Errorf("adding acknowledgement comment to issue #%d: %w", issueNumber, err)
	}
	return comment.GetID(), nil
}
