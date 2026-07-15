package main

import (
	"context"
	"fmt"
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

func buildServiceDependencies(cfg service.Config, st productionStore, logger *log.Logger) (service.Dependencies, error) {
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
		opts.CommandDispatcher = productionCommandDispatcher{
			Dispatcher:      workflowDispatcher,
			ControlPlaneURL: cfg.PublicURL,
			TokenSource:     tokenSource,
		}
	}
	if opts.WorkflowEventProcessor == nil {
		return service.Dependencies{}, fmt.Errorf("production workflow event processor is not configured")
	}
	if opts.ArtifactStore == nil {
		opts.ArtifactStore = artifacts.GitHubActionsStore{TokenSource: tokenSource}
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
	DefaultRunner   string
	TimeoutMinutes  int
	TokenSource     appauth.TokenSource
}

func (d productionCommandDispatcher) DispatchCommand(ctx context.Context, cmd commands.DispatchCommand) error {
	kind, err := commandJobKind(cmd.Command.Kind)
	if err != nil {
		return err
	}
	if d.TokenSource == nil {
		return fmt.Errorf("production command dispatch requires GitHub App token source")
	}
	if d.Dispatcher.Store == nil || d.Dispatcher.GitHub == nil {
		return fmt.Errorf("production command dispatch requires durable dispatcher store and GitHub client")
	}
	target, err := d.resolveCommandTarget(ctx, cmd)
	if err != nil {
		return err
	}
	_, err = d.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:          cmd.RepositoryID,
		Owner:           cmd.Owner,
		Repo:            cmd.Repo,
		InstallationID:  cmd.InstallationID,
		Kind:            kind,
		WorkflowFile:    commandWorkflowFile(kind),
		Ref:             target.Ref,
		BatchNumber:     target.BatchNumber,
		IssueNumber:     target.IssueNumber,
		PRNumber:        cmd.PRNumber,
		BatchBranch:     target.BatchBranch,
		BaseSHA:         target.BaseSHA,
		HeadSHA:         target.HeadSHA,
		ExpectedHeadSHA: target.HeadSHA,
		RunnerLabel:     d.DefaultRunner,
		TimeoutMinutes:  d.TimeoutMinutes,
		ControlPlaneURL: d.ControlPlaneURL,
		Reason:          fmt.Sprintf("@herd-os %s comment %d by %s", cmd.Command.Kind, cmd.CommentID, cmd.Actor),
	})
	if err != nil {
		return fmt.Errorf("dispatch %s command for PR #%d: %w", kind, cmd.PRNumber, err)
	}
	return nil
}

type commandTarget struct {
	BatchNumber int
	IssueNumber int
	BatchBranch string
	BaseSHA     string
	HeadSHA     string
	Ref         string
}

func (d productionCommandDispatcher) resolveCommandTarget(ctx context.Context, cmd commands.DispatchCommand) (commandTarget, error) {
	if cmd.RepositoryID == 0 || cmd.InstallationID == 0 || strings.TrimSpace(cmd.Owner) == "" || strings.TrimSpace(cmd.Repo) == "" {
		return commandTarget{}, fmt.Errorf("production command dispatch requires durable repository context")
	}
	if cmd.PRNumber <= 0 {
		return commandTarget{}, fmt.Errorf("production command dispatch requires durable PR context")
	}
	client, _, err := appauth.NewInstallationClient(ctx, d.TokenSource, cmd.InstallationID)
	if err != nil {
		return commandTarget{}, fmt.Errorf("create installation client for command dispatch: %w", err)
	}
	pr, _, err := client.PullRequests.Get(ctx, cmd.Owner, cmd.Repo, cmd.PRNumber)
	if err != nil {
		return commandTarget{}, fmt.Errorf("lookup PR #%d for command dispatch: %w", cmd.PRNumber, err)
	}
	return commandTargetFromPullRequest(cmd, pr)
}

func commandTargetFromPullRequest(cmd commands.DispatchCommand, pr *gh.PullRequest) (commandTarget, error) {
	if pr == nil {
		return commandTarget{}, fmt.Errorf("production command dispatch requires PR #%d", cmd.PRNumber)
	}
	head := pr.GetHead()
	headSHA := head.GetSHA()
	if strings.TrimSpace(headSHA) == "" {
		return commandTarget{}, fmt.Errorf("production command dispatch requires PR #%d head SHA", cmd.PRNumber)
	}
	batchBranch := head.GetRef()
	if strings.TrimSpace(batchBranch) == "" {
		return commandTarget{}, fmt.Errorf("production command dispatch requires PR #%d head branch", cmd.PRNumber)
	}
	batchNumber := 0
	if pr.Milestone != nil {
		batchNumber = pr.Milestone.GetNumber()
	}
	if batchNumber <= 0 {
		batchNumber = cmd.PRNumber
	}
	issueNumber := cmd.IssueNumber
	if issueNumber <= 0 {
		if cmd.Command.Kind == commands.CommandFix || cmd.Command.Kind == commands.CommandFixCI {
			return commandTarget{}, fmt.Errorf("production %s command dispatch requires a durable fix issue number for PR #%d", cmd.Command.Kind, cmd.PRNumber)
		}
		issueNumber = cmd.PRNumber
	}
	// The worker workflow checks out batch_branch and records that checkout SHA
	// as HERD_BASE_SHA in callbacks/artifact metadata. For command-dispatched
	// patch-producing jobs, the durable job base must match that checkout.
	return commandTarget{
		BatchNumber: batchNumber,
		IssueNumber: issueNumber,
		BatchBranch: batchBranch,
		BaseSHA:     headSHA,
		HeadSHA:     headSHA,
		Ref:         batchBranch,
	}, nil
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

func commandWorkflowFile(kind cpdispatch.JobKind) string {
	if kind == cpdispatch.JobKindReview {
		return "herd-review.yml"
	}
	return "herd-worker.yml"
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
