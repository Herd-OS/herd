package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	legacycommands "github.com/herd-os/herd/internal/commands"
	"github.com/herd-os/herd/internal/config"
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
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
	platformgithub "github.com/herd-os/herd/internal/platform/github"
	"github.com/herd-os/herd/internal/service"
)

const (
	resolveConflictsMergeabilityAttempts = 4
	resolveConflictsMergeabilityDelay    = 5 * time.Second
)

var resolveConflictsSleep = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

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
		defaultWorker := config.Default().Workers
		opts.CommandDispatcher = productionCommandDispatcher{
			Dispatcher:      workflowDispatcher,
			ControlPlaneURL: cfg.PublicURL,
			DefaultRunner:   defaultWorker.RunnerLabel,
			TimeoutMinutes:  defaultWorker.TimeoutMinutes,
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
	PlatformFactory func(ctx context.Context, cmd commands.DispatchCommand) (platform.Platform, error)
}

func (d productionCommandDispatcher) DispatchCommand(ctx context.Context, cmd commands.DispatchCommand) error {
	if cmd.Command.Kind == commands.CommandResolveConflicts {
		return d.dispatchResolveConflictsCommand(ctx, cmd)
	}
	if cmd.Command.Kind == commands.CommandDispatch {
		return d.dispatchIssueCommand(ctx, cmd)
	}
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

func (d productionCommandDispatcher) dispatchResolveConflictsCommand(ctx context.Context, cmd commands.DispatchCommand) error {
	if cmd.PRNumber <= 0 {
		return fmt.Errorf("@herd-os resolve-conflicts can only be used on pull requests")
	}
	p, err := d.commandPlatform(ctx, cmd)
	if err != nil {
		return err
	}
	pr, err := p.PullRequests().Get(ctx, cmd.PRNumber)
	if err != nil {
		return fmt.Errorf("getting PR #%d: %w", cmd.PRNumber, err)
	}
	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return addPRCommandResult(ctx, p, cmd.PRNumber, "⚠️ `@herd-os resolve-conflicts` can only be used on Herd batch PRs.")
	}
	if prReportsNonConflictBlocker(pr) {
		return addPRCommandResult(ctx, p, cmd.PRNumber, "ℹ️ PR is not currently conflicting with base.")
	}
	batchNum, err := integrator.ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)
	}
	ms, err := p.Milestones().Get(ctx, batchNum)
	if err != nil {
		return fmt.Errorf("getting milestone #%d: %w", batchNum, err)
	}
	pr, known, err := latestPRWithKnownMergeabilityFrom(ctx, p.PullRequests(), cmd.PRNumber, pr)
	if err != nil {
		return fmt.Errorf("getting PR #%d mergeability: %w", cmd.PRNumber, err)
	}
	if !known {
		return addPRCommandResult(ctx, p, cmd.PRNumber, "⚠️ Herd could not determine whether this PR is currently conflicting with base yet. Please retry `@herd-os resolve-conflicts` in a moment.")
	}
	if legacycommands.PRReportsClean(pr) || !legacycommands.PRReportsConflict(pr) {
		return addPRCommandResult(ctx, p, cmd.PRNumber, "ℹ️ PR is not currently conflicting with base.")
	}
	existing, err := integrator.FindActivePRConflictResolutionIssue(ctx, p, ms.Number, pr.Number, pr.HeadSHA, pr.BaseSHA)
	if err != nil {
		return err
	}
	if existing != nil && (issues.HasLabel(existing.Labels, issues.StatusInProgress) || issues.HasLabel(existing.Labels, issues.StatusReady)) {
		return addPRCommandResult(ctx, p, cmd.PRNumber, fmt.Sprintf("⚠️ A conflict-resolution issue is already active for this PR (#%d).", existing.Number))
	}
	params := integrator.ConflictResolutionIssueParams{
		Kind:           integrator.ConflictResolutionKindPRBase,
		Milestone:      ms,
		BatchPR:        pr.Number,
		PRHeadBranch:   pr.Head,
		PRHeadSHA:      pr.HeadSHA,
		BaseBranch:     pr.Base,
		BaseSHA:        pr.BaseSHA,
		TriggerAuthor:  cmd.Actor,
		TriggerComment: commandTriggerComment(cmd),
		UserContext:    strings.TrimSpace(strings.Join(cmd.Command.Args, " ")),
	}
	body := integrator.BuildConflictResolutionIssueBody(params)
	truncatedBody, overflow := issues.TruncateIssueBody(body)
	milestoneNumber := ms.Number
	fixIssue, err := p.Issues().Create(ctx, integratorConflictResolutionTitle(params), truncatedBody, []string{issues.TypeFix, issues.StatusInProgress}, &milestoneNumber)
	if err != nil {
		return fmt.Errorf("creating conflict-resolution issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, fixIssue.Number, comment); cerr != nil {
			return fmt.Errorf("adding conflict-resolution overflow comment: %w", cerr)
		}
	}
	headSHA := pr.HeadSHA
	if strings.TrimSpace(headSHA) == "" {
		return fmt.Errorf("PR #%d head SHA is required for conflict-resolution dispatch", pr.Number)
	}
	_, err = d.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:          cmd.RepositoryID,
		Owner:           cmd.Owner,
		Repo:            cmd.Repo,
		InstallationID:  cmd.InstallationID,
		Kind:            cpdispatch.JobKindConflictResolution,
		WorkflowFile:    "herd-worker.yml",
		Ref:             pr.Head,
		BatchNumber:     ms.Number,
		IssueNumber:     fixIssue.Number,
		PRNumber:        pr.Number,
		BatchBranch:     pr.Head,
		BaseSHA:         headSHA,
		HeadSHA:         headSHA,
		ExpectedHeadSHA: headSHA,
		RunnerLabel:     d.DefaultRunner,
		TimeoutMinutes:  d.TimeoutMinutes,
		ControlPlaneURL: d.ControlPlaneURL,
		Reason:          fmt.Sprintf("@herd-os resolve-conflicts comment %d by %s", cmd.CommentID, cmd.Actor),
	})
	if err != nil {
		_ = p.Issues().RemoveLabels(ctx, fixIssue.Number, []string{issues.StatusInProgress, issues.StatusReady})
		_ = p.Issues().AddLabels(ctx, fixIssue.Number, []string{issues.StatusFailed})
		_ = p.Issues().AddComment(ctx, fixIssue.Number, fmt.Sprintf("Failed to dispatch conflict-resolution worker: %v", err))
		return fmt.Errorf("dispatching conflict-resolution worker for issue #%d: %w", fixIssue.Number, err)
	}
	return addPRCommandResult(ctx, p, cmd.PRNumber, fmt.Sprintf("🔧 Created conflict-resolution issue #%d and dispatched worker.", fixIssue.Number))
}

func (d productionCommandDispatcher) dispatchIssueCommand(ctx context.Context, cmd commands.DispatchCommand) error {
	p, err := d.commandPlatform(ctx, cmd)
	if err != nil {
		return err
	}
	issueNumber, err := dispatchIssueNumber(cmd)
	if err != nil {
		return err
	}
	issue, err := p.Issues().Get(ctx, issueNumber)
	if err != nil {
		return fmt.Errorf("getting issue #%d: %w", issueNumber, err)
	}
	if issue.Milestone == nil {
		return fmt.Errorf("issue #%d has no milestone (not part of a batch)", issueNumber)
	}
	if issues.HasLabel(issue.Labels, issues.TypeManual) {
		return addIssueCommandResult(ctx, p, cmd.IssueNumber, fmt.Sprintf("Issue #%d is a manual task and cannot be dispatched to a worker.", issueNumber))
	}
	status := issues.StatusLabel(issue.Labels)
	if status != issues.StatusReady && status != issues.StatusFailed {
		return fmt.Errorf("issue #%d is %q, expected ready or failed", issueNumber, status)
	}
	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))
	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return fmt.Errorf("getting default branch: %w", err)
	}
	headSHA, err := p.Repository().GetBranchSHA(ctx, batchBranch)
	if err != nil {
		return fmt.Errorf("getting %s SHA: %w", batchBranch, err)
	}
	if status != "" {
		if err := p.Issues().RemoveLabels(ctx, issueNumber, []string{status}); err != nil {
			return fmt.Errorf("removing label: %w", err)
		}
	}
	if err := p.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusInProgress}); err != nil {
		return fmt.Errorf("adding in-progress label: %w", err)
	}
	_, err = d.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:          cmd.RepositoryID,
		Owner:           cmd.Owner,
		Repo:            cmd.Repo,
		InstallationID:  cmd.InstallationID,
		Kind:            cpdispatch.JobKindWorker,
		WorkflowFile:    "herd-worker.yml",
		Ref:             defaultBranch,
		BatchNumber:     issue.Milestone.Number,
		IssueNumber:     issueNumber,
		PRNumber:        cmd.PRNumber,
		BatchBranch:     batchBranch,
		BaseSHA:         headSHA,
		HeadSHA:         headSHA,
		ExpectedHeadSHA: headSHA,
		RunnerLabel:     d.DefaultRunner,
		TimeoutMinutes:  d.TimeoutMinutes,
		ControlPlaneURL: d.ControlPlaneURL,
		Reason:          fmt.Sprintf("@herd-os dispatch comment %d by %s", cmd.CommentID, cmd.Actor),
	})
	if err != nil {
		_ = p.Issues().RemoveLabels(ctx, issueNumber, []string{issues.StatusInProgress})
		_ = p.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusFailed})
		return fmt.Errorf("dispatching issue #%d worker: %w", issueNumber, err)
	}
	return addIssueCommandResult(ctx, p, cmd.IssueNumber, fmt.Sprintf("🔧 Dispatched worker for issue #%d.", issueNumber))
}

func (d productionCommandDispatcher) commandPlatform(ctx context.Context, cmd commands.DispatchCommand) (platform.Platform, error) {
	if d.PlatformFactory != nil {
		return d.PlatformFactory(ctx, cmd)
	}
	if cmd.RepositoryID == 0 || cmd.InstallationID == 0 || strings.TrimSpace(cmd.Owner) == "" || strings.TrimSpace(cmd.Repo) == "" {
		return nil, fmt.Errorf("production command dispatch requires durable repository context")
	}
	if d.TokenSource == nil {
		return nil, fmt.Errorf("production command dispatch requires GitHub App token source")
	}
	client, _, err := appauth.NewInstallationClient(ctx, d.TokenSource, cmd.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("create installation client for command dispatch: %w", err)
	}
	p, err := platformgithub.NewWithClient(cmd.Owner, cmd.Repo, client)
	if err != nil {
		return nil, fmt.Errorf("create platform client for command dispatch: %w", err)
	}
	return p, nil
}

func dispatchIssueNumber(cmd commands.DispatchCommand) (int, error) {
	if len(cmd.Command.Args) > 1 {
		return 0, fmt.Errorf("@herd-os dispatch accepts at most one issue number")
	}
	if len(cmd.Command.Args) == 1 {
		number, err := strconv.Atoi(cmd.Command.Args[0])
		if err != nil || number <= 0 {
			return 0, fmt.Errorf("invalid issue number: %s", cmd.Command.Args[0])
		}
		return number, nil
	}
	if cmd.IssueNumber <= 0 {
		return 0, fmt.Errorf("@herd-os dispatch requires an issue number")
	}
	return cmd.IssueNumber, nil
}

func commandTriggerComment(cmd commands.DispatchCommand) string {
	triggerComment := "@herd-os resolve-conflicts"
	if prompt := strings.TrimSpace(strings.Join(cmd.Command.Args, " ")); prompt != "" {
		triggerComment += "\n\n" + prompt
	}
	return triggerComment
}

func addPRCommandResult(ctx context.Context, p platform.Platform, prNumber int, body string) error {
	if err := p.PullRequests().AddComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting PR command result: %w", err)
	}
	return nil
}

func addIssueCommandResult(ctx context.Context, p platform.Platform, issueNumber int, body string) error {
	if issueNumber <= 0 {
		return nil
	}
	if err := p.Issues().AddComment(ctx, issueNumber, body); err != nil {
		return fmt.Errorf("posting issue command result: %w", err)
	}
	return nil
}

func latestPRWithKnownMergeabilityFrom(ctx context.Context, prs platform.PullRequestService, prNumber int, initial *platform.PullRequest) (*platform.PullRequest, bool, error) {
	var latest *platform.PullRequest
	for attempt := 0; attempt < resolveConflictsMergeabilityAttempts; attempt++ {
		pr := initial
		if attempt > 0 || pr == nil {
			var err error
			pr, err = prs.Get(ctx, prNumber)
			if err != nil {
				return nil, false, err
			}
		}
		initial = nil
		latest = pr
		if legacycommands.PRMergeabilityKnown(pr) {
			return pr, true, nil
		}
		if prReportsNonConflictBlocker(pr) {
			return pr, true, nil
		}
		if attempt == resolveConflictsMergeabilityAttempts-1 {
			break
		}
		if err := resolveConflictsSleep(ctx, resolveConflictsMergeabilityDelay); err != nil {
			return latest, false, err
		}
	}
	return latest, false, nil
}

func integratorConflictResolutionTitle(params integrator.ConflictResolutionIssueParams) string {
	return fmt.Sprintf("Resolve PR conflict: #%d (%s onto %s)", params.BatchPR, params.PRHeadBranch, params.BaseBranch)
}

func prReportsNonConflictBlocker(pr *platform.PullRequest) bool {
	if pr == nil {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(pr.MergeStateStatus)) {
	case "BLOCKED":
		return true
	default:
		return false
	}
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
