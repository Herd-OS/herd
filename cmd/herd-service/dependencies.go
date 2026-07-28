package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/herd-os/herd/internal/controlplane/mutations"
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
	if d.TokenSource == nil && d.PlatformFactory == nil {
		return fmt.Errorf("production command dispatch requires GitHub App token source")
	}
	if d.Dispatcher.Store == nil || d.Dispatcher.GitHub == nil {
		return fmt.Errorf("production command dispatch requires durable dispatcher store and GitHub client")
	}
	target, err := d.resolveCommandTarget(ctx, cmd)
	if err != nil {
		return err
	}
	reviewPrompt := ""
	manualReview := false
	manualDispatchKey := ""
	if kind == cpdispatch.JobKindReview {
		reviewPrompt = parsedCommandPrompt(cmd.Command)
		manualReview = true
		manualDispatchKey = commandManualDispatchKey(cmd)
	}
	_, err = d.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:            cmd.RepositoryID,
		Owner:             cmd.Owner,
		Repo:              cmd.Repo,
		InstallationID:    cmd.InstallationID,
		Kind:              kind,
		WorkflowFile:      commandWorkflowFile(kind),
		Ref:               target.Ref,
		BatchNumber:       target.BatchNumber,
		IssueNumber:       target.IssueNumber,
		PRNumber:          cmd.PRNumber,
		BatchBranch:       target.BatchBranch,
		BaseSHA:           target.BaseSHA,
		HeadSHA:           target.HeadSHA,
		ExpectedHeadSHA:   target.HeadSHA,
		Mode:              target.Mode,
		RunnerLabel:       d.DefaultRunner,
		TimeoutMinutes:    d.TimeoutMinutes,
		ControlPlaneURL:   d.ControlPlaneURL,
		Reason:            fmt.Sprintf("@herd-os %s comment %d by %s", cmd.Command.Kind, cmd.CommentID, cmd.Actor),
		ReviewPrompt:      reviewPrompt,
		ManualReview:      manualReview,
		ManualDispatchKey: manualDispatchKey,
	})
	if err != nil {
		return fmt.Errorf("dispatch %s command for PR #%d: %w", kind, cmd.PRNumber, err)
	}
	return nil
}

func commandManualDispatchKey(cmd commands.DispatchCommand) string {
	return fmt.Sprintf("comment:%d:command:%s", cmd.CommentID, cmd.Command.Kind)
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
		UserContext:    parsedCommandPrompt(cmd.Command),
	}
	fixIssueNumber, err := d.ensureConflictResolutionIssue(ctx, p, cmd, params)
	if err != nil {
		return err
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
		IssueNumber:     fixIssueNumber,
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
		_ = p.Issues().RemoveLabels(ctx, fixIssueNumber, []string{issues.StatusInProgress, issues.StatusReady})
		_ = p.Issues().AddLabels(ctx, fixIssueNumber, []string{issues.StatusFailed})
		_ = p.Issues().AddComment(ctx, fixIssueNumber, fmt.Sprintf("Failed to dispatch conflict-resolution worker: %v", err))
		return fmt.Errorf("dispatching conflict-resolution worker for issue #%d: %w", fixIssueNumber, err)
	}
	return addPRCommandResult(ctx, p, cmd.PRNumber, fmt.Sprintf("🔧 Created conflict-resolution issue #%d and dispatched worker.", fixIssueNumber))
}

func (d productionCommandDispatcher) ensureConflictResolutionIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams) (int, error) {
	key := conflictResolutionIssueKey(cmd, params)
	now := time.Now().UTC()
	created, err := d.Dispatcher.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "conflict_resolution_issue_create",
		Status:    mutations.PhaseIntentRecorded,
		CreatedAt: now,
	})
	if err != nil {
		return 0, fmt.Errorf("acquire conflict-resolution issue idempotency key: %w", err)
	}
	if !created {
		issueNumber, recovered, recoverErr := d.recoverConflictResolutionIssue(ctx, p, cmd, params, key)
		if recovered || recoverErr != nil {
			return issueNumber, recoverErr
		}
		record, err := d.Dispatcher.Store.GetIdempotencyKey(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("get conflict-resolution issue idempotency key: %w", err)
		}
		if record.Status == "completed" && strings.TrimSpace(record.ResultRef) != "" {
			issueNumber, ok := parseIssueResultRef(record.ResultRef)
			if !ok {
				return 0, fmt.Errorf("invalid conflict-resolution issue result ref %q", record.ResultRef)
			}
			return issueNumber, nil
		}
		if !mutations.IsPreCallRetryable(record.Status) && record.Status != "failed" {
			status := strings.TrimSpace(record.Status)
			if status == "" {
				status = "unknown"
			}
			return 0, fmt.Errorf("conflict-resolution issue idempotency key %q is %s without a completed issue result; retry after reconciliation", key, status)
		}
	}
	if err := d.recordIssueCreateMutationAttempt(ctx, key, cmd.RepositoryID, "conflict_resolution_issue_create", now); err != nil {
		_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseFailedPreCall+":"+err.Error())
		return 0, err
	}
	started, err := d.tryStartIssueCreateMutation(ctx, key, "conflict-resolution issue")
	if err != nil || !started {
		if err != nil {
			return 0, err
		}
		return d.waitForConflictResolutionIssue(ctx, p, cmd, params, key)
	}
	return d.createConflictResolutionIssue(ctx, p, cmd, params, key)
}

func (d productionCommandDispatcher) createConflictResolutionIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams, key string) (int, error) {
	body := injectConflictResolutionIssueMarker(integrator.BuildConflictResolutionIssueBody(params), cmd, params)
	truncatedBody, overflow := issues.TruncateIssueBody(body)
	milestoneNumber := params.Milestone.Number
	fixIssue, err := p.Issues().Create(ctx, integratorConflictResolutionTitle(params), truncatedBody, []string{issues.TypeFix, issues.StatusInProgress}, &milestoneNumber)
	if err != nil {
		_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseRepairRequired, nil, err)
		_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseRepairRequired+":"+err.Error())
		return 0, fmt.Errorf("creating conflict-resolution issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, fixIssue.Number, comment); cerr != nil {
			_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseRepairRequired, nil, cerr)
			_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseRepairRequired+":"+cerr.Error())
			return 0, fmt.Errorf("adding conflict-resolution overflow comment: %w", cerr)
		}
	}
	resultRef := fmt.Sprintf("issue:%d", fixIssue.Number)
	_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseCompleted, json.RawMessage(fmt.Sprintf(`{"issue_number":%d}`, fixIssue.Number)), nil)
	if err := d.Dispatcher.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
		return 0, fmt.Errorf("complete conflict-resolution issue idempotency key: %w", err)
	}
	return fixIssue.Number, nil
}

func (d productionCommandDispatcher) recoverConflictResolutionIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams, key string) (int, bool, error) {
	body := injectConflictResolutionIssueMarker(integrator.BuildConflictResolutionIssueBody(params), cmd, params)
	_, overflow := issues.TruncateIssueBody(body)
	overflowComments := issues.SplitOverflowComments(overflow)
	if existing, err := integrator.FindActivePRConflictResolutionIssue(ctx, p, params.Milestone.Number, params.BatchPR, params.PRHeadSHA, params.BaseSHA); err != nil {
		return 0, false, err
	} else if existing != nil {
		if err := ensureIssueOverflowComments(ctx, p, existing.Number, overflowComments); err != nil {
			return 0, false, err
		}
		resultRef := fmt.Sprintf("issue:%d", existing.Number)
		if err := d.Dispatcher.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
			return 0, false, fmt.Errorf("complete recovered conflict-resolution issue idempotency key: %w", err)
		}
		return existing.Number, true, nil
	}
	found, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &params.Milestone.Number})
	if err != nil {
		return 0, false, fmt.Errorf("list conflict-resolution issues for recovery: %w", err)
	}
	marker := strings.TrimSpace(conflictResolutionIssueMarker(cmd, params))
	for _, issue := range found {
		if issue == nil || !strings.Contains(issue.Body, marker) {
			continue
		}
		if err := ensureIssueOverflowComments(ctx, p, issue.Number, overflowComments); err != nil {
			return 0, false, err
		}
		resultRef := fmt.Sprintf("issue:%d", issue.Number)
		if err := d.Dispatcher.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
			return 0, false, fmt.Errorf("complete recovered conflict-resolution issue idempotency key: %w", err)
		}
		return issue.Number, true, nil
	}
	return 0, false, nil
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
	if prompt := parsedCommandPrompt(cmd.Command); prompt != "" {
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

func conflictResolutionIssueKey(cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams) string {
	return commandStableKey("conflict-resolution-issue", cmd.RepositoryID, params.BatchPR, cmd.CommentID, cmd.Command.Kind, params.PRHeadSHA, params.BaseSHA)
}

func conflictResolutionIssueMarker(cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams) string {
	return fmt.Sprintf("\n\n<!-- herd:conflict-resolution {\"version\":1,\"repo_id\":%d,\"pr_number\":%d,\"comment_id\":%d,\"command\":\"%s\",\"head_sha\":\"%s\",\"base_sha\":\"%s\"} -->\n", cmd.RepositoryID, params.BatchPR, cmd.CommentID, cmd.Command.Kind, params.PRHeadSHA, params.BaseSHA)
}

func injectConflictResolutionIssueMarker(body string, cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams) string {
	marker := strings.TrimSpace(conflictResolutionIssueMarker(cmd, params))
	const frontMatterEnd = "---\n\n"
	idx := strings.Index(body, frontMatterEnd)
	if idx < 0 {
		return marker + "\n\n" + body
	}
	insertAt := idx + len(frontMatterEnd)
	return body[:insertAt] + marker + "\n\n" + body[insertAt:]
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
	Mode        string
}

func (d productionCommandDispatcher) resolveCommandTarget(ctx context.Context, cmd commands.DispatchCommand) (commandTarget, error) {
	if cmd.RepositoryID == 0 || cmd.InstallationID == 0 || strings.TrimSpace(cmd.Owner) == "" || strings.TrimSpace(cmd.Repo) == "" {
		return commandTarget{}, fmt.Errorf("production command dispatch requires durable repository context")
	}
	if cmd.PRNumber <= 0 {
		return commandTarget{}, fmt.Errorf("production command dispatch requires durable PR context")
	}
	p, err := d.commandPlatform(ctx, cmd)
	if err != nil {
		return commandTarget{}, err
	}
	pr, err := p.PullRequests().Get(ctx, cmd.PRNumber)
	if err != nil {
		return commandTarget{}, fmt.Errorf("lookup PR #%d for command dispatch: %w", cmd.PRNumber, err)
	}
	target, err := commandTargetFromPullRequest(cmd, pr)
	if err != nil {
		return commandTarget{}, err
	}
	if cmd.Command.Kind == commands.CommandFix || cmd.Command.Kind == commands.CommandFixCI {
		issueNumber, err := d.ensureCommandFixIssue(ctx, p, cmd, pr, target)
		if err != nil {
			return commandTarget{}, err
		}
		target.IssueNumber = issueNumber
		if !strings.HasPrefix(pr.Head, "herd/batch/") {
			target.Mode = "standalone"
		}
	}
	if err := validateCommandTarget(cmd, target); err != nil {
		return commandTarget{}, err
	}
	return target, nil
}

func commandTargetFromPullRequest(cmd commands.DispatchCommand, pr *platform.PullRequest) (commandTarget, error) {
	if pr == nil {
		return commandTarget{}, fmt.Errorf("production command dispatch requires PR #%d", cmd.PRNumber)
	}
	headSHA := pr.HeadSHA
	if strings.TrimSpace(headSHA) == "" {
		return commandTarget{}, fmt.Errorf("production command dispatch requires PR #%d head SHA", cmd.PRNumber)
	}
	batchBranch := pr.Head
	if strings.TrimSpace(batchBranch) == "" {
		return commandTarget{}, fmt.Errorf("production command dispatch requires PR #%d head branch", cmd.PRNumber)
	}
	batchNumber := 0
	if strings.HasPrefix(batchBranch, "herd/batch/") {
		if parsed, err := integrator.ParseBatchBranchMilestone(batchBranch); err == nil {
			batchNumber = parsed
		}
	}
	if batchNumber <= 0 {
		batchNumber = cmd.PRNumber
	}
	issueNumber := cmd.IssueNumber
	if issueNumber <= 0 {
		issueNumber = fallbackIssueNumber(cmd)
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

func fallbackIssueNumber(cmd commands.DispatchCommand) int {
	if cmd.Command.Kind == commands.CommandFix || cmd.Command.Kind == commands.CommandFixCI {
		return 0
	}
	return cmd.PRNumber
}

func validateCommandTarget(cmd commands.DispatchCommand, target commandTarget) error {
	if cmd.Command.Kind != commands.CommandFix && cmd.Command.Kind != commands.CommandFixCI {
		return nil
	}
	if target.IssueNumber <= 0 || target.IssueNumber == cmd.PRNumber {
		return fmt.Errorf("production %s command dispatch requires a durable fix issue number for PR #%d", cmd.Command.Kind, cmd.PRNumber)
	}
	return nil
}

func (d productionCommandDispatcher) ensureCommandFixIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, target commandTarget) (int, error) {
	key := commandFixIssueKey(cmd)
	now := time.Now().UTC()
	created, err := d.Dispatcher.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "command_fix_issue_create",
		Status:    mutations.PhaseIntentRecorded,
		CreatedAt: now,
	})
	if err != nil {
		return 0, fmt.Errorf("acquire command fix issue idempotency key: %w", err)
	}
	if !created {
		issueNumber, recovered, recoverErr := d.recoverCommandFixIssue(ctx, p, cmd, pr, target, key)
		if recovered || recoverErr != nil {
			return issueNumber, recoverErr
		}
		record, err := d.Dispatcher.Store.GetIdempotencyKey(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("get command fix issue idempotency key: %w", err)
		}
		if record.Status == "completed" && strings.TrimSpace(record.ResultRef) != "" {
			issueNumber, ok := parseIssueResultRef(record.ResultRef)
			if !ok {
				return 0, fmt.Errorf("invalid command fix issue result ref %q", record.ResultRef)
			}
			return issueNumber, nil
		}
		if mutations.IsPreCallRetryable(record.Status) || strings.HasPrefix(record.ResultRef, mutations.PhaseFailedPreCall+":") {
			if err := d.recordIssueCreateMutationAttempt(ctx, key, cmd.RepositoryID, "command_fix_issue_create", now); err != nil {
				_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseFailedPreCall+":"+err.Error())
				return 0, err
			}
			started, err := d.tryStartIssueCreateMutation(ctx, key, "command fix issue")
			if err != nil || !started {
				if err != nil {
					return 0, err
				}
				return d.waitForCommandFixIssue(ctx, p, cmd, pr, target, key)
			}
			return d.createCommandFixIssue(ctx, p, cmd, pr, target, key)
		}
		status := strings.TrimSpace(record.Status)
		if status == "" {
			status = "unknown"
		}
		return 0, fmt.Errorf("command fix issue idempotency key %q is %s without a completed issue result; retry after reconciliation", key, status)
	}
	if err := d.recordIssueCreateMutationAttempt(ctx, key, cmd.RepositoryID, "command_fix_issue_create", now); err != nil {
		_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseFailedPreCall+":"+err.Error())
		return 0, err
	}
	started, err := d.tryStartIssueCreateMutation(ctx, key, "command fix issue")
	if err != nil || !started {
		if err != nil {
			return 0, err
		}
		return d.waitForCommandFixIssue(ctx, p, cmd, pr, target, key)
	}
	return d.createCommandFixIssue(ctx, p, cmd, pr, target, key)
}

func (d productionCommandDispatcher) createCommandFixIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, target commandTarget, key string) (int, error) {
	body, title, labels, milestone, err := d.commandFixIssueRequest(ctx, p, cmd, pr, target)
	if err != nil {
		_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseFailedPreCall, nil, err)
		_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseFailedPreCall+":"+err.Error())
		return 0, err
	}
	body = injectCommandFixIssueMarker(body, cmd)
	truncatedBody, overflow := issues.TruncateIssueBody(body)
	issue, err := p.Issues().Create(ctx, title, truncatedBody, labels, milestone)
	if err != nil {
		_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseRepairRequired, nil, err)
		_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseRepairRequired+":"+err.Error())
		return 0, fmt.Errorf("creating command fix issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, issue.Number, comment); cerr != nil {
			_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseRepairRequired, nil, cerr)
			_ = d.Dispatcher.Store.FailIdempotencyKey(ctx, key, mutations.PhaseRepairRequired+":"+cerr.Error())
			return 0, fmt.Errorf("adding command fix issue overflow comment: %w", cerr)
		}
	}
	resultRef := fmt.Sprintf("issue:%d", issue.Number)
	_ = d.completeIssueCreateMutation(ctx, key, mutations.PhaseCompleted, json.RawMessage(fmt.Sprintf(`{"issue_number":%d}`, issue.Number)), nil)
	if err := d.Dispatcher.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
		return 0, fmt.Errorf("complete command fix issue idempotency key: %w", err)
	}
	return issue.Number, nil
}

func (d productionCommandDispatcher) commandFixIssueRequest(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, target commandTarget) (string, string, []string, *int, error) {
	prompt := parsedCommandPrompt(cmd.Command)
	if prompt == "" && cmd.Command.Kind == commands.CommandFix {
		return "", "", nil, nil, fmt.Errorf("@herd-os fix requires a description")
	}
	if strings.HasPrefix(pr.Head, "herd/batch/") {
		return d.batchCommandFixIssueRequest(ctx, p, cmd, pr, target, prompt)
	}
	if cmd.Command.Kind == commands.CommandFixCI {
		return "", "", nil, nil, fmt.Errorf("@herd-os fix-ci can only be used on Herd batch PRs")
	}
	return d.standaloneCommandFixIssueRequest(ctx, p, cmd, pr, prompt)
}

func (d productionCommandDispatcher) batchCommandFixIssueRequest(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, target commandTarget, prompt string) (string, string, []string, *int, error) {
	batchNumber, err := integrator.ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)
	}
	ms, err := p.Milestones().Get(ctx, batchNumber)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("getting milestone #%d: %w", batchNumber, err)
	}
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &ms.Number})
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("listing milestone issues: %w", err)
	}
	cycle := nextCommandFixCycle(allIssues, cmd.Command.Kind)
	contextText := fmt.Sprintf("Requested by @%s via `@herd-os %s` on batch PR #%d.", cmd.Actor, cmd.Command.Kind, pr.Number)
	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:    1,
			Batch:      ms.Number,
			Type:       "fix",
			FixCycle:   reviewFixCycle(cmd.Command.Kind, cycle),
			CIFixCycle: ciFixCycle(cmd.Command.Kind, cycle),
			BatchPR:    pr.Number,
			PRHeadSHA:  pr.HeadSHA,
			PRBaseSHA:  pr.BaseSHA,
		},
		Task:                commandFixTask(cmd.Command.Kind, prompt),
		Context:             contextText,
		ConversationHistory: commandConversationHistory(ctx, p, cmd.PRNumber),
	})
	if cmd.Command.Kind == commands.CommandFix && commandLooksLikeConflict(prompt) {
		body = appendCommandConflictInstructions(body, pr.Base)
	}
	title := "Fix: " + truncateCommandRunes(firstCommandLine(commandFixTitleText(cmd.Command.Kind, prompt, target.BatchBranch, cycle)), 70)
	if cmd.Command.Kind == commands.CommandFixCI {
		title = fmt.Sprintf("Fix CI failure on %s (cycle %d)", target.BatchBranch, cycle)
	}
	return body, title, []string{issues.TypeFix, issues.StatusInProgress}, &ms.Number, nil
}

func (d productionCommandDispatcher) standaloneCommandFixIssueRequest(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, prompt string) (string, string, []string, *int, error) {
	existing, err := p.Issues().List(ctx, platform.IssueFilters{State: "open", Labels: []string{issues.TypeStandaloneFix}})
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("listing standalone fix issues: %w", err)
	}
	for _, iss := range existing {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil || parsed.FrontMatter.TargetPR != pr.Number {
			continue
		}
		if issues.HasLabel(iss.Labels, issues.StatusInProgress) || issues.HasLabel(iss.Labels, issues.StatusReady) {
			return "", "", nil, nil, fmt.Errorf("standalone fix issue #%d is already active for PR #%d", iss.Number, pr.Number)
		}
	}
	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:      1,
			Type:         "standalone-fix",
			TargetPR:     pr.Number,
			TargetBranch: pr.Head,
			PRHeadSHA:    pr.HeadSHA,
			PRBaseSHA:    pr.BaseSHA,
		},
		Task:    prompt,
		Context: fmt.Sprintf("Requested by @%s via `@herd-os fix` on PR #%d.", cmd.Actor, pr.Number),
	})
	return body, "Standalone fix: " + truncateCommandRunes(firstCommandLine(prompt), 70), []string{issues.TypeStandaloneFix, issues.StatusInProgress}, nil, nil
}

func parsedCommandPrompt(cmd commands.ParsedCommand) string {
	if prompt := strings.TrimSpace(cmd.Prompt); prompt != "" {
		return prompt
	}
	return strings.TrimSpace(strings.Join(cmd.Args, " "))
}

func (d productionCommandDispatcher) recoverCommandFixIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, target commandTarget, key string) (int, bool, error) {
	body, _, _, _, err := d.commandFixIssueRequest(ctx, p, cmd, pr, target)
	if err != nil {
		return 0, false, err
	}
	body = injectCommandFixIssueMarker(body, cmd)
	_, overflow := issues.TruncateIssueBody(body)
	overflowComments := issues.SplitOverflowComments(overflow)
	filters := platform.IssueFilters{State: "all"}
	if strings.HasPrefix(pr.Head, "herd/batch/") {
		milestone := target.BatchNumber
		if parsed, err := integrator.ParseBatchBranchMilestone(pr.Head); err == nil {
			milestone = parsed
		}
		filters.Milestone = &milestone
	} else {
		filters.Labels = []string{issues.TypeStandaloneFix}
	}
	found, err := p.Issues().List(ctx, filters)
	if err != nil {
		return 0, false, fmt.Errorf("list command fix issues for recovery: %w", err)
	}
	marker := strings.TrimSpace(commandFixIssueMarker(cmd))
	for _, issue := range found {
		if issue == nil || !strings.Contains(issue.Body, marker) {
			continue
		}
		if err := ensureIssueOverflowComments(ctx, p, issue.Number, overflowComments); err != nil {
			return 0, false, err
		}
		resultRef := fmt.Sprintf("issue:%d", issue.Number)
		if err := d.Dispatcher.Store.CompleteIdempotencyKey(ctx, key, resultRef); err != nil {
			return 0, false, fmt.Errorf("complete recovered command fix issue idempotency key: %w", err)
		}
		return issue.Number, true, nil
	}
	return 0, false, nil
}

func ensureIssueOverflowComments(ctx context.Context, p platform.Platform, issueNumber int, expected []string) error {
	if len(expected) == 0 {
		return nil
	}
	existing, err := p.Issues().ListComments(ctx, issueNumber)
	if err != nil {
		return fmt.Errorf("list issue overflow comments: %w", err)
	}
	remaining := append([]string(nil), expected...)
	for _, comment := range existing {
		if comment == nil {
			continue
		}
		for i, want := range remaining {
			if comment.Body == want {
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
			}
		}
	}
	for _, comment := range remaining {
		if err := p.Issues().AddComment(ctx, issueNumber, comment); err != nil {
			return fmt.Errorf("add recovered issue overflow comment: %w", err)
		}
	}
	return nil
}

func commandFixIssueKey(cmd commands.DispatchCommand) string {
	return commandStableKey("command-fix-issue", cmd.RepositoryID, cmd.PRNumber, cmd.CommentID, string(cmd.Command.Kind))
}

func commandStableKey(parts ...any) string {
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		text = append(text, fmt.Sprint(part))
	}
	sum := sha256.Sum256([]byte(strings.Join(text, ":")))
	return "command:" + hex.EncodeToString(sum[:])
}

func commandFixIssueMarker(cmd commands.DispatchCommand) string {
	return fmt.Sprintf("\n\n<!-- herd:command-fix {\"version\":1,\"repo_id\":%d,\"pr_number\":%d,\"comment_id\":%d,\"command\":\"%s\"} -->\n", cmd.RepositoryID, cmd.PRNumber, cmd.CommentID, cmd.Command.Kind)
}

func injectCommandFixIssueMarker(body string, cmd commands.DispatchCommand) string {
	marker := strings.TrimSpace(commandFixIssueMarker(cmd))
	const frontMatterEnd = "---\n\n"
	idx := strings.Index(body, frontMatterEnd)
	if idx < 0 {
		return marker + "\n\n" + body
	}
	insertAt := idx + len(frontMatterEnd)
	return body[:insertAt] + marker + "\n\n" + body[insertAt:]
}

func commandConversationHistory(ctx context.Context, p platform.Platform, prNumber int) string {
	comments, err := p.Issues().ListComments(ctx, prNumber)
	if err != nil || len(comments) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range comments {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		b.WriteString(fmt.Sprintf("**@%s:**\n\n%s\n", c.AuthorLogin, c.Body))
	}
	return b.String()
}

func nextCommandFixCycle(allIssues []*platform.Issue, kind commands.CommandKind) int {
	current := 0
	for _, iss := range allIssues {
		if iss == nil {
			continue
		}
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		if kind == commands.CommandFixCI {
			if parsed.FrontMatter.CIFixCycle > current {
				current = parsed.FrontMatter.CIFixCycle
			}
			continue
		}
		if parsed.FrontMatter.FixCycle > current {
			current = parsed.FrontMatter.FixCycle
		}
	}
	return current + 1
}

func reviewFixCycle(kind commands.CommandKind, cycle int) int {
	if kind == commands.CommandFix {
		return cycle
	}
	return 0
}

func ciFixCycle(kind commands.CommandKind, cycle int) int {
	if kind == commands.CommandFixCI {
		return cycle
	}
	return 0
}

func commandFixTask(kind commands.CommandKind, prompt string) string {
	if kind == commands.CommandFixCI {
		task := "CI is failing on the batch branch. Investigate the failures, fix the issues, and ensure all tests pass."
		if strings.TrimSpace(prompt) != "" {
			return strings.TrimSpace(prompt) + "\n\n" + task
		}
		return task
	}
	return prompt
}

func commandFixTitleText(kind commands.CommandKind, prompt string, batchBranch string, cycle int) string {
	if kind == commands.CommandFixCI {
		return fmt.Sprintf("CI failure on %s cycle %d", batchBranch, cycle)
	}
	return prompt
}

func commandLooksLikeConflict(description string) bool {
	lower := strings.ToLower(description)
	keywords := []string{"merge conflict", "rebase conflict", "conflict with main", "conflict with master", "conflicts with main", "conflicts with master"}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func appendCommandConflictInstructions(body, baseBranch string) string {
	instructions := fmt.Sprintf("\n\n## Git Instructions\n\n"+
		"This task involves a merge or rebase conflict. Follow these steps:\n\n"+
		"**For merge conflicts:**\n"+
		"1. `git fetch origin`\n"+
		"2. `git merge origin/%s`\n"+
		"3. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch.\n"+
		"4. `git add <resolved files>`\n"+
		"5. `git commit`\n\n"+
		"**For rebase conflicts:**\n"+
		"1. `git fetch origin`\n"+
		"2. `git rebase origin/%s`\n"+
		"3. Resolve conflict markers in the affected files. Do NOT rewrite files from scratch.\n"+
		"4. `git add <resolved files>`\n"+
		"5. `git rebase --continue`\n"+
		"6. Repeat steps 3-5 for each conflicting commit.\n",
		baseBranch, baseBranch)
	return body + instructions
}

func truncateCommandRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func firstCommandLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func parseIssueResultRef(ref string) (int, bool) {
	var number int
	if _, err := fmt.Sscanf(ref, "issue:%d", &number); err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

type issueCreateMutationStore interface {
	cpdispatch.MutationRecorder
	cpdispatch.MutationStarter
	cpdispatch.MutationReader
}

func (d productionCommandDispatcher) issueCreateMutationStore() (issueCreateMutationStore, error) {
	mutationsStore, ok := d.Dispatcher.Store.(issueCreateMutationStore)
	if !ok {
		return nil, fmt.Errorf("dispatcher store does not support guarded GitHub mutation attempts")
	}
	return mutationsStore, nil
}

func (d productionCommandDispatcher) recordIssueCreateMutationAttempt(ctx context.Context, key string, repoID int64, mutationType string, now time.Time) error {
	mutationsStore, err := d.issueCreateMutationStore()
	if err != nil {
		return err
	}
	err = mutationsStore.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
		IdempotencyKey: key,
		RepositoryID:   repoID,
		MutationType:   mutationType,
		Status:         mutations.PhaseIntentRecorded,
		CreatedAt:      now,
	})
	if err == nil || errors.Is(err, store.ErrAlreadyExists) {
		return nil
	}
	return fmt.Errorf("record %s mutation attempt: %w", mutationType, err)
}

func (d productionCommandDispatcher) tryStartIssueCreateMutation(ctx context.Context, key, label string) (bool, error) {
	mutationsStore, err := d.issueCreateMutationStore()
	if err != nil {
		return false, err
	}
	start, err := mutationsStore.TryStartGitHubMutationAttempt(ctx, key, []string{mutations.PhaseIntentRecorded, mutations.PhaseFailedPreCall}, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("start %s mutation attempt: %w", label, err)
	}
	if start.Started {
		return true, nil
	}
	switch mutations.Normalize(start.Attempt.Status) {
	case mutations.PhaseCompleted:
		return false, nil
	case mutations.PhaseCallStarted, mutations.PhaseRepairRequired:
		return false, nil
	default:
		return false, fmt.Errorf("%s mutation attempt for %q is %s; retry after reconciliation", label, key, start.Attempt.Status)
	}
}

func (d productionCommandDispatcher) completeIssueCreateMutation(ctx context.Context, key, status string, response json.RawMessage, mutationErr error) error {
	mutationsStore, err := d.issueCreateMutationStore()
	if err != nil {
		return err
	}
	errMsg := ""
	if mutationErr != nil {
		errMsg = mutationErr.Error()
	}
	return mutationsStore.CompleteGitHubMutationAttempt(ctx, key, status, response, errMsg, time.Now().UTC())
}

func (d productionCommandDispatcher) waitForCommandFixIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, pr *platform.PullRequest, target commandTarget, key string) (int, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		issueNumber, recovered, err := d.recoverCommandFixIssue(ctx, p, cmd, pr, target, key)
		if recovered || err != nil {
			return issueNumber, err
		}
		record, err := d.Dispatcher.Store.GetIdempotencyKey(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("get command fix issue idempotency key: %w", err)
		}
		if record.Status == mutations.PhaseCompleted && strings.TrimSpace(record.ResultRef) != "" {
			issueNumber, ok := parseIssueResultRef(record.ResultRef)
			if !ok {
				return 0, fmt.Errorf("invalid command fix issue result ref %q", record.ResultRef)
			}
			return issueNumber, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("command fix issue creation for %q is in progress; retry after current attempt completes", key)
		}
		if err := sleepContext(ctx, 25*time.Millisecond); err != nil {
			return 0, err
		}
	}
}

func (d productionCommandDispatcher) waitForConflictResolutionIssue(ctx context.Context, p platform.Platform, cmd commands.DispatchCommand, params integrator.ConflictResolutionIssueParams, key string) (int, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		issueNumber, recovered, err := d.recoverConflictResolutionIssue(ctx, p, cmd, params, key)
		if recovered || err != nil {
			return issueNumber, err
		}
		record, err := d.Dispatcher.Store.GetIdempotencyKey(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("get conflict-resolution issue idempotency key: %w", err)
		}
		if record.Status == mutations.PhaseCompleted && strings.TrimSpace(record.ResultRef) != "" {
			issueNumber, ok := parseIssueResultRef(record.ResultRef)
			if !ok {
				return 0, fmt.Errorf("invalid conflict-resolution issue result ref %q", record.ResultRef)
			}
			return issueNumber, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("conflict-resolution issue creation for %q is in progress; retry after current attempt completes", key)
		}
		if err := sleepContext(ctx, 25*time.Millisecond); err != nil {
			return 0, err
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
