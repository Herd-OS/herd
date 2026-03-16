package commands

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
)

func handleReview(hctx *HandlerContext, cmd Command) Result {
	pr, err := hctx.Platform.PullRequests().Get(hctx.Ctx, hctx.IssueNumber)
	if err != nil {
		return Result{Error: fmt.Errorf("getting PR #%d: %w", hctx.IssueNumber, err)}
	}
	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return Result{Message: "⚠️ `/herd review` can only be used on batch PRs."}
	}

	result, err := integrator.Review(hctx.Ctx, hctx.Platform, hctx.Agent, hctx.Git, hctx.Config, integrator.ReviewParams{
		PRNumber:          pr.Number,
		RepoRoot:          hctx.RepoRoot,
		ExtraInstructions: cmd.Prompt,
	})
	if err != nil {
		return Result{Error: err}
	}

	if result.Approved {
		return Result{Message: "✅ Review passed — batch PR approved."}
	}
	if result.MaxCyclesHit {
		return Result{Message: "⚠️ Max fix cycles reached. Manual intervention needed."}
	}
	if len(result.FixIssues) > 0 {
		nums := make([]string, len(result.FixIssues))
		for i, n := range result.FixIssues {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		return Result{Message: fmt.Sprintf("🔍 Review found issues — dispatched fix workers: %s", strings.Join(nums, ", "))}
	}
	return Result{Message: "Review completed (no action taken)."}
}
