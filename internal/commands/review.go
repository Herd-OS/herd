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

	// integrator.Review already posts PR comments for all actionable outcomes
	// (approved, max cycles hit, findings). Return empty messages here to avoid
	// duplicate comments being posted by the CLI handler.
	if result.Approved || result.MaxCyclesHit || len(result.FixIssues) > 0 {
		return Result{}
	}
	if result.AllCreatesFailed {
		return Result{Error: fmt.Errorf("review found issues but all fix-issue creations failed")}
	}
	return Result{Message: "Review completed (no action taken)."}
}
