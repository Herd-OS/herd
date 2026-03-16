package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
)

func handleFixCI(hctx *HandlerContext, cmd Command) Result {
	if !hctx.IsPR {
		return Result{Message: "⚠️ `/herd fix-ci` can only be used on pull requests."}
	}

	// Label-based dedup: if the CI fix label is already present, a prior invocation
	// of this handler has already claimed the fix cycle. Return early to avoid
	// dispatching duplicate workers. This handles the race where two concurrent
	// patrol runs both post /herd fix-ci before either comment is handled — the
	// first handler adds the label via beforeDispatch; the second sees it here.
	if prIssue, getErr := hctx.Platform.Issues().Get(hctx.Ctx, hctx.IssueNumber); getErr == nil {
		for _, label := range prIssue.Labels {
			if label == issues.CIFixPending {
				return Result{Message: "ℹ️ CI fix already in progress."}
			}
		}
	}

	pr, err := hctx.Platform.PullRequests().Get(hctx.Ctx, hctx.IssueNumber)
	if err != nil {
		return Result{Error: fmt.Errorf("getting PR #%d: %w", hctx.IssueNumber, err)}
	}
	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return Result{Message: "⚠️ `/herd fix-ci` can only be used on batch PRs."}
	}

	batchNum, err := integrator.ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return Result{Error: fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)}
	}

	// Add the label BEFORE workers are dispatched (label-first pattern, matching
	// monitor/patrol.go). If AddLabels fails we warn but still proceed — dedup
	// via the label may be broken, but we must not silently drop the user's command.
	beforeDispatch := func() {
		if labelErr := hctx.Platform.Issues().AddLabels(hctx.Ctx, hctx.IssueNumber, []string{issues.CIFixPending}); labelErr != nil {
			fmt.Fprintf(os.Stderr, "warning: adding %s label to PR #%d: %v\n", issues.CIFixPending, hctx.IssueNumber, labelErr)
		}
	}

	result, err := integrator.CheckCI(hctx.Ctx, hctx.Platform, hctx.Config, integrator.CheckCIParams{
		BatchNumber:    batchNum,
		RepoRoot:       hctx.RepoRoot,
		UserContext:    cmd.Prompt,
		BeforeDispatch: beforeDispatch,
	})
	if err != nil {
		return Result{Error: err}
	}

	if result.Skipped {
		return Result{Message: "CI checking is disabled (`require_ci: false`)."}
	}
	if result.Status == "success" {
		return Result{Message: "✅ CI is passing."}
	}
	if result.Status == "pending" {
		return Result{Message: "⏳ CI is pending — re-ran failed checks."}
	}
	if result.MaxCyclesHit {
		return Result{Message: "⚠️ CI failed — max fix cycles reached. Manual intervention needed."}
	}
	if len(result.FixIssues) > 0 {
		nums := make([]string, len(result.FixIssues))
		for i, n := range result.FixIssues {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		return Result{Message: fmt.Sprintf("🔧 CI failed — dispatched fix workers: %s", strings.Join(nums, ", "))}
	}
	return Result{Message: fmt.Sprintf("CI status: %s", result.Status)}
}
