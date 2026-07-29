package integrator

import (
	"context"
	"fmt"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

type WorkerCompletionCycleParams struct {
	RunID    int64
	RepoRoot string
}

type WorkerCompletionCycleResult struct {
	Consolidate         *ConsolidateResult
	Advance             *AdvanceResult
	Review              *ReviewResult
	CheckCI             *CheckCIResult
	BatchLockSkipped    bool
	SkipReason          string
	PendingDrained      bool
	SideEffectsDeferred bool
}

func RunWorkerCompletionCycle(ctx context.Context, p platform.Platform, ag agent.Agent, g *git.Git, cfg *config.Config, params WorkerCompletionCycleParams) (*WorkerCompletionCycleResult, error) {
	runCtx, err := ResolveWorkerRunContext(ctx, p, params.RunID)
	if err != nil {
		return nil, err
	}
	logWorkerRunContext("WorkerCycle", runCtx)

	ms := runCtx.issue.Milestone
	if isBatchComplete(ms) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", ms.Number)
		return &WorkerCompletionCycleResult{}, nil
	}

	batchLock, acquired, err := AcquireBatchLock(ctx, p.Repository(), ms.Number, runCtx.BatchBranch, params.RunID, timeNowUTC())
	if err != nil {
		return nil, fmt.Errorf("acquiring batch lock for batch #%d: %w", ms.Number, err)
	}
	if !acquired {
		logActiveBatchLock(ctx, p.Repository(), ms.Number, runCtx.BatchBranch, params.RunID)
		if err := markWorkerCompletionPending(ctx, p, runCtx); err != nil {
			return nil, err
		}
		return &WorkerCompletionCycleResult{
			BatchLockSkipped: true,
			SkipReason:       batchLockSkipReason(ctx, p.Repository(), ms.Number, runCtx.BatchBranch, params.RunID),
		}, nil
	}
	defer releaseBatchLockDeferred(p, batchLock, ms.Number)

	lockedCtx := withHeldBatchLock(ctx, ms.Number, runCtx.BatchBranch, batchLock)
	result := &WorkerCompletionCycleResult{}

	consolidateResult, err := Consolidate(lockedCtx, p, g, cfg, ConsolidateParams(params))
	if err != nil {
		return nil, err
	}
	result.Consolidate = consolidateResult

	if runCtx.Conclusion != "success" {
		return result, nil
	}

	for attempt := 0; attempt < 5; attempt++ {
		drained, err := drainPendingWorkerCompletions(lockedCtx, p, g, cfg, params.RunID, ms.Number)
		if err != nil {
			return nil, err
		}
		if drained {
			result.PendingDrained = true
			continue
		}

		advanceResult, err := Advance(lockedCtx, p, g, cfg, AdvanceParams(params))
		if err != nil {
			return nil, err
		}
		result.Advance = advanceResult

		drained, err = drainPendingWorkerCompletions(lockedCtx, p, g, cfg, params.RunID, ms.Number)
		if err != nil {
			return nil, err
		}
		if drained {
			result.PendingDrained = true
			continue
		}

		pending, err := hasPendingWorkerCompletions(lockedCtx, p, ms.Number)
		if err != nil {
			return nil, err
		}
		if pending {
			result.PendingDrained = true
			continue
		}

		if advanceResult != nil && advanceResult.AllComplete && advanceResult.BatchPRNumber > 0 {
			clear, err := prepareHeldBatchLockSideEffects(lockedCtx, p.Repository(), ms.Number, runCtx.BatchBranch)
			if err != nil {
				return nil, err
			}
			if !clear {
				fmt.Printf("Pending worker completion published for batch #%d before review; deferring review and CI side effects until consolidation is refreshed.\n", ms.Number)
				result.SideEffectsDeferred = true
				return result, nil
			}

			reviewResult, err := Review(lockedCtx, p, ag, g, cfg, ReviewParams{
				RunID:    params.RunID,
				RepoRoot: params.RepoRoot,
			})
			if err != nil {
				return nil, err
			}
			result.Review = reviewResult

			clear, err = prepareHeldBatchLockSideEffects(lockedCtx, p.Repository(), ms.Number, runCtx.BatchBranch)
			if err != nil {
				return nil, err
			}
			if !clear {
				fmt.Printf("Pending worker completion published for batch #%d before CI check; deferring CI side effects until consolidation is refreshed.\n", ms.Number)
				result.SideEffectsDeferred = true
				return result, nil
			}

			checkResult, err := CheckCI(lockedCtx, p, cfg, CheckCIParams{
				RunID:    params.RunID,
				RepoRoot: params.RepoRoot,
			})
			if err != nil {
				return nil, err
			}
			result.CheckCI = checkResult
		}
		return result, nil
	}

	return nil, fmt.Errorf("pending worker completions for batch #%d did not quiesce", ms.Number)
}

func markWorkerCompletionPending(ctx context.Context, p platform.Platform, runCtx *WorkerRunContext) error {
	if runCtx == nil || runCtx.IssueNumber == 0 {
		return nil
	}
	if err := publishPendingWorkerCompletion(ctx, p.Repository(), runCtx.issue.Milestone.Number, runCtx.BatchBranch); err != nil {
		return err
	}
	if err := p.Issues().AddLabels(ctx, runCtx.IssueNumber, []string{issues.IntegratorPending}); err != nil {
		return fmt.Errorf("marking issue #%d pending integrator recovery: %w", runCtx.IssueNumber, err)
	}
	return nil
}

func drainPendingWorkerCompletions(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, runID int64, batchNumber int) (bool, error) {
	pending, err := hasPendingWorkerCompletions(ctx, p, batchNumber)
	if err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}

	fmt.Printf("Pending worker completion detected for batch #%d; running fresh broad consolidation scan before continuing.\n", batchNumber)
	if _, err := Consolidate(ctx, p, g, cfg, ConsolidateParams{RunID: runID}); err != nil {
		return false, err
	}
	if err := clearResolvedPendingWorkerCompletions(ctx, p, batchNumber); err != nil {
		return false, err
	}
	if err := clearHeldBatchLockPendingCompletions(ctx, p.Repository(), batchNumber); err != nil {
		return false, err
	}
	return true, nil
}

func publishPendingWorkerCompletion(ctx context.Context, repoSvc platform.RepositoryService, batchNumber int, batchBranch string) error {
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return fmt.Errorf("repository service does not support append-only batch locks")
	}
	lockBranch := BatchLockBranch(batchNumber)
	for attempt := 0; attempt < batchLockMaxAttempts; attempt++ {
		headSHA, state, stateOK, err := readBatchLockHead(ctx, repoSvc, repo, lockBranch)
		if err != nil {
			return err
		}
		if !stateOK || state.Status != "locked" || state.BatchNumber != batchNumber || state.BatchBranch != batchBranch || state.LockID == "" {
			return nil
		}
		state.PendingCompletions++
		state.PendingGeneration++
		message, err := buildBatchLockCommitMessage(state)
		if err != nil {
			return err
		}
		commitSHA, err := repo.CreateCommit(ctx, headSHA, message)
		if err != nil {
			return fmt.Errorf("creating pending worker completion marker for %s: %w", lockBranch, err)
		}
		if err := repo.UpdateBranchToCommit(ctx, lockBranch, commitSHA, false); err != nil {
			if platform.IsRefUpdateConflict(err) {
				continue
			}
			return fmt.Errorf("updating pending worker completion marker %s: %w", lockBranch, err)
		}
		return nil
	}
	return fmt.Errorf("publishing pending worker completion for batch #%d: exceeded retry attempts", batchNumber)
}

func hasHeldBatchLockPendingCompletions(ctx context.Context, p platform.Platform, batchNumber int) (bool, error) {
	if pending, err := hasPendingWorkerCompletions(ctx, p, batchNumber); err != nil || pending {
		return pending, err
	}
	held, ok := ctx.Value(heldBatchLockContextKey{}).(heldBatchLockContext)
	if !ok || held.lockID == "" {
		return false, nil
	}
	state, ok, err := DescribeBatchLock(ctx, p.Repository(), batchNumber)
	if err != nil || !ok {
		return false, err
	}
	return state.Status == "locked" && state.LockID == held.lockID && state.PendingCompletions > 0, nil
}

func clearHeldBatchLockPendingCompletions(ctx context.Context, repoSvc platform.RepositoryService, batchNumber int) error {
	held, ok := ctx.Value(heldBatchLockContextKey{}).(heldBatchLockContext)
	if !ok || held.lockID == "" {
		return nil
	}
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return fmt.Errorf("repository service does not support append-only batch locks")
	}
	lockBranch := BatchLockBranch(batchNumber)
	for attempt := 0; attempt < batchLockMaxAttempts; attempt++ {
		headSHA, state, stateOK, err := readBatchLockHead(ctx, repoSvc, repo, lockBranch)
		if err != nil {
			return err
		}
		if !stateOK || state.Status != "locked" || state.LockID != held.lockID || state.PendingCompletions == 0 {
			return nil
		}
		state.PendingCompletions = 0
		message, err := buildBatchLockCommitMessage(state)
		if err != nil {
			return err
		}
		commitSHA, err := repo.CreateCommit(ctx, headSHA, message)
		if err != nil {
			return fmt.Errorf("creating pending worker completion clear marker for %s: %w", lockBranch, err)
		}
		if err := repo.UpdateBranchToCommit(ctx, lockBranch, commitSHA, false); err != nil {
			if platform.IsRefUpdateConflict(err) {
				continue
			}
			return fmt.Errorf("updating pending worker completion clear marker %s: %w", lockBranch, err)
		}
		return nil
	}
	return fmt.Errorf("clearing pending worker completions for batch #%d: exceeded retry attempts", batchNumber)
}

func hasPendingWorkerCompletions(ctx context.Context, p platform.Platform, batchNumber int) (bool, error) {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &batchNumber})
	if err != nil {
		return false, fmt.Errorf("listing milestone issues for pending integrator completions: %w", err)
	}
	return hasPendingWorkerCompletionInIssues(ctx, p, allIssues), nil
}

func hasPendingWorkerCompletionInIssues(ctx context.Context, p platform.Platform, allIssues []*platform.Issue) bool {
	for _, iss := range allIssues {
		if iss == nil || issues.StatusLabel(iss.Labels) != issues.StatusDone || !issues.HasLabel(iss.Labels, issues.IntegratorPending) {
			continue
		}
		workerBranch := fmt.Sprintf("herd/worker/%d-%s", iss.Number, planner.Slugify(iss.Title))
		if _, err := p.Repository().GetBranchSHA(ctx, workerBranch); err == nil {
			return true
		}
	}
	return false
}

func clearResolvedPendingWorkerCompletions(ctx context.Context, p platform.Platform, batchNumber int) error {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &batchNumber})
	if err != nil {
		return fmt.Errorf("listing milestone issues to clear pending integrator completions: %w", err)
	}
	for _, iss := range allIssues {
		if iss == nil || !issues.HasLabel(iss.Labels, issues.IntegratorPending) {
			continue
		}
		workerBranch := fmt.Sprintf("herd/worker/%d-%s", iss.Number, planner.Slugify(iss.Title))
		_, branchErr := p.Repository().GetBranchSHA(ctx, workerBranch)
		if branchErr == nil && issues.StatusLabel(iss.Labels) == issues.StatusDone {
			continue
		}
		if err := p.Issues().RemoveLabels(ctx, iss.Number, []string{issues.IntegratorPending}); err != nil {
			return fmt.Errorf("clearing pending integrator label from issue #%d: %w", iss.Number, err)
		}
	}
	return nil
}

func clearPendingWorkerCompletionLabel(ctx context.Context, p platform.Platform, iss *platform.Issue) {
	if iss == nil || !issues.HasLabel(iss.Labels, issues.IntegratorPending) {
		return
	}
	if err := p.Issues().RemoveLabels(ctx, iss.Number, []string{issues.IntegratorPending}); err != nil {
		fmt.Printf("Warning: failed to clear pending integrator label from issue #%d: %v\n", iss.Number, err)
	}
}
