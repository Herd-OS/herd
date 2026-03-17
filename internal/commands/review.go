package commands

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
)

func handleReview(hctx *HandlerContext, cmd Command) Result {
	if !hctx.IsPR {
		return Result{Message: "⚠️ `/herd review` can only be used on pull requests."}
	}
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
		return Result{Message: fmt.Sprintf("✅ Review approved batch PR #%d.", result.BatchPRNumber)}
	}
	if result.MaxCyclesHit {
		return Result{Message: fmt.Sprintf("⚠️ Review found issues on batch PR #%d but max fix cycles reached. See PR for details.", result.BatchPRNumber)}
	}
	if len(result.FixIssues) > 0 {
		return Result{Message: fmt.Sprintf("🔍 Review found %d %s on batch PR #%d. Fix workers dispatched.",
			len(result.FixIssues), pluralize("issue", len(result.FixIssues)), result.BatchPRNumber)}
	}
	if result.AllCreatesFailed {
		return Result{Error: fmt.Errorf("review found %d issues but all fix-issue creations failed", result.FindingsCount)}
	}
	return Result{Message: "Review completed (no action taken)."}
}

func pluralize(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
