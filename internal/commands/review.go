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
		_, err := integrator.ReviewStandalone(hctx.Ctx, hctx.Platform, hctx.Agent, hctx.Config, integrator.ReviewStandaloneParams{
			PRNumber:          pr.Number,
			RepoRoot:          hctx.RepoRoot,
			ExtraInstructions: cmd.Prompt,
		})
		if err != nil {
			return Result{Error: err}
		}
		return Result{}
	}

	result, err := integrator.Review(hctx.Ctx, hctx.Platform, hctx.Agent, hctx.Git, hctx.Config, integrator.ReviewParams{
		PRNumber:          pr.Number,
		RepoRoot:          hctx.RepoRoot,
		ExtraInstructions: cmd.Prompt,
	})
	if err != nil {
		return Result{Error: err}
	}

	// integrator.Review already posts detailed comments on the PR.
	// Return empty Message to avoid the CLI posting a duplicate comment.
	if result.AllCreatesFailed {
		return Result{Error: fmt.Errorf("review found %d issues but all fix-issue creations failed", result.FindingsCount)}
	}
	return Result{}
}
