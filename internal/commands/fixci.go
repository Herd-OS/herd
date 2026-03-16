package commands

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
)

func handleFixCI(hctx *HandlerContext, cmd Command) Result {
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

	result, err := integrator.CheckCI(hctx.Ctx, hctx.Platform, hctx.Config, integrator.CheckCIParams{
		BatchNumber: batchNum,
		RepoRoot:    hctx.RepoRoot,
		UserContext: cmd.Prompt,
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
