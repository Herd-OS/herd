package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
)

func init() {
	Register("retry", handleRetry)
}

func handleRetry(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error) {
	// 1. Parse issue number from Args
	args := strings.TrimSpace(cmd.Args)
	if args == "" {
		return "", fmt.Errorf("Usage: `/herd retry <issue-number>`")
	}
	issueNum, err := strconv.Atoi(args)
	if err != nil || issueNum <= 0 {
		return "", fmt.Errorf("Usage: `/herd retry <issue-number>`")
	}

	// 2. Get the issue
	issue, err := hctx.Platform.Issues().Get(ctx, issueNum)
	if err != nil {
		return "", fmt.Errorf("getting issue #%d: %w", issueNum, err)
	}

	// 3. Validate failed label
	status := issues.StatusLabel(issue.Labels)
	if status != issues.StatusFailed {
		return "", fmt.Errorf("Issue #%d is not in failed state (current: %s)", issueNum, status)
	}

	// 4. Validate milestone
	if issue.Milestone == nil {
		return "", fmt.Errorf("Issue #%d has no milestone", issueNum)
	}

	// 5. Build batch branch name
	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))

	// 6. Remove failed label, add in-progress
	if err := hctx.Platform.Issues().RemoveLabels(ctx, issueNum, []string{issues.StatusFailed}); err != nil {
		return "", fmt.Errorf("removing failed label: %w", err)
	}
	if err := hctx.Platform.Issues().AddLabels(ctx, issueNum, []string{issues.StatusInProgress}); err != nil {
		_ = hctx.Platform.Issues().AddLabels(ctx, issueNum, []string{issues.StatusFailed})
		return "", fmt.Errorf("adding in-progress label: %w", err)
	}

	// 7. Get default branch for workflow dispatch ref
	defaultBranch, err := hctx.Platform.Repository().GetDefaultBranch(ctx)
	if err != nil {
		_ = hctx.Platform.Issues().RemoveLabels(ctx, issueNum, []string{issues.StatusInProgress})
		_ = hctx.Platform.Issues().AddLabels(ctx, issueNum, []string{issues.StatusFailed})
		return "", fmt.Errorf("getting default branch: %w", err)
	}

	// 8. Dispatch worker workflow
	_, err = hctx.Platform.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", issueNum),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	})
	if err != nil {
		// 9. Revert labels if dispatch fails
		_ = hctx.Platform.Issues().RemoveLabels(ctx, issueNum, []string{issues.StatusInProgress})
		_ = hctx.Platform.Issues().AddLabels(ctx, issueNum, []string{issues.StatusFailed})
		return "", fmt.Errorf("dispatching workflow: %w", err)
	}

	// 10. Return success
	return fmt.Sprintf("🔄 Redispatching worker for issue #%d.", issueNum), nil
}
