package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

func init() {
	Register("fix", handleFix)
}

func handleFix(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error) {
	if cmd.Prompt == "" {
		return "", fmt.Errorf("Usage: `/herd fix \"description of what to fix\"`")
	}

	if hctx.PRNumber == 0 {
		return "", fmt.Errorf("fix can only be used on batch PRs")
	}

	pr, err := hctx.Platform.PullRequests().Get(ctx, hctx.PRNumber)
	if err != nil {
		return "", fmt.Errorf("getting PR #%d: %w", hctx.PRNumber, err)
	}

	m := batchBranchRe.FindStringSubmatch(pr.Head)
	if m == nil {
		return "", fmt.Errorf("fix can only be used on batch PRs")
	}

	batchNum, err := strconv.Atoi(m[1])
	if err != nil {
		return "", fmt.Errorf("parsing batch number: %w", err)
	}

	ms, err := hctx.Platform.Milestones().Get(ctx, batchNum)
	if err != nil {
		return "", fmt.Errorf("getting milestone #%d: %w", batchNum, err)
	}

	allIssues, err := hctx.Platform.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return "", fmt.Errorf("listing milestone issues: %w", err)
	}

	currentCycle := maxFixCycle(allIssues)
	nextCycle := currentCycle + 1

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    batchNum,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  hctx.PRNumber,
		},
		Task:    cmd.Prompt,
		Context: fmt.Sprintf("Requested via /herd fix on batch PR #%d.", hctx.PRNumber),
	})

	title := "Fix: " + truncateTitle(cmd.Prompt, 60)
	fixIssue, err := hctx.Platform.Issues().Create(ctx, title, body,
		[]string{issues.TypeFix, issues.StatusInProgress}, &ms.Number)
	if err != nil {
		return "", fmt.Errorf("creating fix issue: %w", err)
	}

	defaultBranch, err := hctx.Platform.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return "", fmt.Errorf("getting default branch: %w", err)
	}

	_, err = hctx.Platform.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    pr.Head,
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	})
	if err != nil {
		return "", fmt.Errorf("dispatching worker: %w", err)
	}

	return fmt.Sprintf("🔧 Created fix issue #%d and dispatched worker.\n\nTask: %s", fixIssue.Number, cmd.Prompt), nil
}

func maxFixCycle(allIssues []*platform.Issue) int {
	max := 0
	for _, issue := range allIssues {
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		if parsed.FrontMatter.FixCycle > max {
			max = parsed.FrontMatter.FixCycle
		}
	}
	return max
}

func truncateTitle(s string, max int) string {
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
