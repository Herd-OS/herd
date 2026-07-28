package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

const resolveConflictsMergeabilityAttempts = 4
const resolveConflictsMergeabilityDelay = 5 * time.Second

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

func handleResolveConflicts(hctx *HandlerContext, cmd Command) Result {
	if !hctx.IsPR {
		return Result{Message: "⚠️ `/herd resolve-conflicts` can only be used on pull requests."}
	}

	pr, err := hctx.Platform.PullRequests().Get(hctx.Ctx, hctx.IssueNumber)
	if err != nil {
		return Result{Error: fmt.Errorf("getting PR #%d: %w", hctx.IssueNumber, err)}
	}
	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return Result{Message: "⚠️ `/herd resolve-conflicts` can only be used on Herd batch PRs."}
	}

	batchNum, err := integrator.ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return Result{Error: fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)}
	}

	ms, err := hctx.Platform.Milestones().Get(hctx.Ctx, batchNum)
	if err != nil {
		return Result{Error: fmt.Errorf("getting milestone #%d: %w", batchNum, err)}
	}

	pr, known, err := latestPRWithKnownMergeabilityFrom(hctx.Ctx, hctx.Platform.PullRequests(), hctx.IssueNumber, pr)
	if err != nil {
		return Result{Error: fmt.Errorf("getting PR #%d mergeability: %w", hctx.IssueNumber, err)}
	}
	if !known {
		return Result{Message: "⚠️ Herd could not determine whether this PR is currently conflicting with base yet. Please retry `/herd resolve-conflicts` in a moment."}
	}
	if prReportsClean(pr) {
		return Result{Message: "ℹ️ PR is not currently conflicting with base."}
	}
	if !prReportsConflict(pr) {
		return Result{Message: "ℹ️ PR is not currently conflicting with base."}
	}

	existing, err := integrator.FindActivePRConflictResolutionIssue(hctx.Ctx, hctx.Platform, ms.Number, pr.Number, pr.HeadSHA, pr.BaseSHA)
	if err != nil {
		return Result{Error: err}
	}
	if existing != nil && (issues.HasLabel(existing.Labels, issues.StatusInProgress) || issues.HasLabel(existing.Labels, issues.StatusReady)) {
		return Result{Message: fmt.Sprintf("⚠️ A conflict-resolution issue is already active for this PR (#%d).", existing.Number)}
	}

	triggerComment := "/herd resolve-conflicts"
	if prompt := strings.TrimSpace(cmd.Prompt); prompt != "" {
		triggerComment += "\n\n" + prompt
	}
	if body := strings.TrimSpace(hctx.IssueBody); strings.Contains(body, "/herd resolve-conflicts") {
		triggerComment = body
	}

	params := integrator.ConflictResolutionIssueParams{
		Kind:           integrator.ConflictResolutionKindPRBase,
		Milestone:      ms,
		BatchPR:        pr.Number,
		PRHeadBranch:   pr.Head,
		PRHeadSHA:      pr.HeadSHA,
		BaseBranch:     pr.Base,
		BaseSHA:        pr.BaseSHA,
		TriggerAuthor:  hctx.AuthorLogin,
		TriggerComment: triggerComment,
		UserContext:    strings.TrimSpace(cmd.Prompt),
	}
	dispatch, err := integrator.DispatchConflictResolutionIssue(hctx.Ctx, hctx.Platform, hctx.Config, params, []string{issues.TypeFix, issues.StatusInProgress}, pr.Head)
	if err != nil {
		return Result{Error: err}
	}
	if dispatch.Duplicated {
		return Result{Message: fmt.Sprintf("⚠️ A conflict-resolution issue is already active for this PR (#%d).", dispatch.IssueNumber)}
	}

	return Result{Message: fmt.Sprintf("🔧 Created conflict-resolution issue #%d and dispatched worker.", dispatch.IssueNumber)}
}

func latestPRWithKnownMergeability(ctx context.Context, prs platform.PullRequestService, prNumber int) (*platform.PullRequest, bool, error) {
	return latestPRWithKnownMergeabilityFrom(ctx, prs, prNumber, nil)
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
		if prMergeabilityKnown(pr) {
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

func prReportsConflict(pr *platform.PullRequest) bool {
	if pr == nil {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(pr.MergeStateStatus))
	switch status {
	case "DIRTY", "CONFLICTING":
		return true
	case "CLEAN", "HAS_HOOKS", "UNSTABLE", "BEHIND":
		return false
	}
	return false
}

func prReportsClean(pr *platform.PullRequest) bool {
	if pr == nil {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(pr.MergeStateStatus))
	switch status {
	case "CLEAN", "HAS_HOOKS", "UNSTABLE", "BEHIND":
		return true
	}
	return pr.MergeableKnown && pr.Mergeable
}

func prMergeabilityKnown(pr *platform.PullRequest) bool {
	if pr == nil {
		return false
	}
	if pr.MergeableKnown {
		return true
	}
	return prReportsConflict(pr) || prReportsClean(pr)
}
