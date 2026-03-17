package commands

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
)

func handleFixCI(hctx *HandlerContext, cmd Command) Result {
	if !hctx.IsPR {
		return Result{Message: "⚠️ `/herd fix-ci` can only be used on pull requests."}
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

	// Ensure the label is present right before workers are dispatched. The patrol
	// already adds it before posting the /herd fix-ci comment, so this is typically
	// idempotent. If AddLabels fails we warn but still proceed.
	var labelWarning string
	beforeDispatch := func() {
		if labelErr := hctx.Platform.Issues().AddLabels(hctx.Ctx, hctx.IssueNumber, []string{issues.CIFixPending}); labelErr != nil {
			labelWarning = fmt.Sprintf("\n⚠️ Warning: failed to add %s label to PR #%d: %v", issues.CIFixPending, hctx.IssueNumber, labelErr)
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

	var msg string
	switch {
	case result.Skipped:
		msg = "CI checking is disabled (`require_ci: false`)."
	case result.Status == "success":
		msg = "✅ CI is passing."
	case result.Status == "pending":
		msg = "⏳ CI is pending — re-ran failed checks."
	case result.MaxCyclesHit:
		msg = "⚠️ CI failed — max fix cycles reached. Manual intervention needed."
	case len(result.FixIssues) > 0:
		nums := make([]string, len(result.FixIssues))
		for i, n := range result.FixIssues {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		msg = fmt.Sprintf("🔧 CI failed — dispatched fix workers: %s", strings.Join(nums, ", "))
	default:
		msg = fmt.Sprintf("CI status: %s", result.Status)
	}
	return Result{Message: msg + labelWarning}
}
