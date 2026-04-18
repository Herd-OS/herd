package commands

import (
	"fmt"
	"strconv"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
)

func handleDispatch(hctx *HandlerContext, cmd Command) Result {
	// Determine which issue to dispatch — either from args or the current issue
	var issueNum int
	if len(cmd.Args) > 0 {
		n, err := strconv.Atoi(cmd.Args[0])
		if err != nil {
			return Result{Message: fmt.Sprintf("⚠️ Invalid issue number: %s", cmd.Args[0])}
		}
		issueNum = n
	} else {
		if hctx.IsPR {
			return Result{Message: "⚠️ `/herd dispatch` on a PR requires an issue number: `/herd dispatch <issue-number>`"}
		}
		issueNum = hctx.IssueNumber
	}

	issue, err := hctx.Platform.Issues().Get(hctx.Ctx, issueNum)
	if err != nil {
		return Result{Error: fmt.Errorf("getting issue #%d: %w", issueNum, err)}
	}

	status := issues.StatusLabel(issue.Labels)
	if status != issues.StatusReady && status != issues.StatusBlocked {
		return Result{Message: fmt.Sprintf("⚠️ Issue #%d is not ready or blocked (status: %s).", issueNum, status)}
	}
	if issue.Milestone == nil {
		return Result{Message: fmt.Sprintf("⚠️ Issue #%d has no milestone.", issueNum)}
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))

	defaultBranch, err := hctx.Platform.Repository().GetDefaultBranch(hctx.Ctx)
	if err != nil {
		return Result{Error: fmt.Errorf("getting default branch: %w", err)}
	}

	// Remove current status and set in-progress
	if status != "" {
		_ = hctx.Platform.Issues().RemoveLabels(hctx.Ctx, issueNum, []string{status})
	}
	_ = hctx.Platform.Issues().AddLabels(hctx.Ctx, issueNum, []string{issues.StatusInProgress})

	_, err = hctx.Platform.Workflows().Dispatch(hctx.Ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", issueNum),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	})
	if err != nil {
		_ = hctx.Platform.Issues().RemoveLabels(hctx.Ctx, issueNum, []string{issues.StatusInProgress})
		_ = hctx.Platform.Issues().AddLabels(hctx.Ctx, issueNum, []string{status})
		return Result{Error: fmt.Errorf("dispatching worker for #%d: %w", issueNum, err)}
	}

	return Result{Message: fmt.Sprintf("🚀 Dispatched worker for issue #%d.", issueNum)}
}
