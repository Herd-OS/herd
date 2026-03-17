package commands

import (
	"fmt"
	"strconv"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
)

func handleRetry(hctx *HandlerContext, cmd Command) Result {
	if cmd.ParseErr != nil {
		return Result{Message: "⚠️ Could not parse command: " + cmd.ParseErr.Error()}
	}
	if len(cmd.Args) < 1 {
		return Result{Message: "⚠️ Usage: `/herd retry <issue-number>`"}
	}
	issueNum, err := strconv.Atoi(cmd.Args[0])
	if err != nil {
		return Result{Message: fmt.Sprintf("⚠️ Invalid issue number: %s", cmd.Args[0])}
	}

	issue, err := hctx.Platform.Issues().Get(hctx.Ctx, issueNum)
	if err != nil {
		return Result{Error: fmt.Errorf("getting issue #%d: %w", issueNum, err)}
	}

	// Always remove the retry-pending label so the monitor can post a new
	// retry comment if this attempt fails and the issue returns to failed.
	_ = hctx.Platform.Issues().RemoveLabels(hctx.Ctx, issueNum, []string{issues.RetryPending})

	status := issues.StatusLabel(issue.Labels)
	if status != issues.StatusFailed {
		return Result{Message: fmt.Sprintf("⚠️ Issue #%d is not failed (status: %s).", issueNum, status)}
	}
	if issue.Milestone == nil {
		return Result{Message: fmt.Sprintf("⚠️ Issue #%d has no milestone.", issueNum)}
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))

	defaultBranch, err := hctx.Platform.Repository().GetDefaultBranch(hctx.Ctx)
	if err != nil {
		return Result{Error: fmt.Errorf("getting default branch: %w", err)}
	}

	_ = hctx.Platform.Issues().RemoveLabels(hctx.Ctx, issueNum, []string{issues.StatusFailed})
	_ = hctx.Platform.Issues().AddLabels(hctx.Ctx, issueNum, []string{issues.StatusInProgress})

	_, err = hctx.Platform.Workflows().Dispatch(hctx.Ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", issueNum),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	})
	if err != nil {
		_ = hctx.Platform.Issues().RemoveLabels(hctx.Ctx, issueNum, []string{issues.StatusInProgress})
		_ = hctx.Platform.Issues().AddLabels(hctx.Ctx, issueNum, []string{issues.StatusFailed})
		return Result{Error: fmt.Errorf("dispatching worker for #%d: %w", issueNum, err)}
	}

	return Result{Message: fmt.Sprintf("🔄 Re-dispatched worker for issue #%d.", issueNum)}
}
