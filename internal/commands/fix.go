package commands

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

func handleFix(hctx *HandlerContext, cmd Command) Result {
	if cmd.Prompt == "" {
		return Result{Message: "⚠️ Usage: `/herd fix \"description of what to fix\"`"}
	}
	if !hctx.IsPR {
		return Result{Message: "⚠️ `/herd fix` can only be used on pull requests."}
	}

	pr, err := hctx.Platform.PullRequests().Get(hctx.Ctx, hctx.IssueNumber)
	if err != nil {
		return Result{Error: fmt.Errorf("getting PR #%d: %w", hctx.IssueNumber, err)}
	}
	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return Result{Message: "⚠️ `/herd fix` can only be used on batch PRs."}
	}

	batchNum, err := integrator.ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return Result{Error: fmt.Errorf("parsing batch number from %s: %w", pr.Head, err)}
	}

	ms, err := hctx.Platform.Milestones().Get(hctx.Ctx, batchNum)
	if err != nil {
		return Result{Error: fmt.Errorf("getting milestone #%d: %w", batchNum, err)}
	}

	allIssues, err := hctx.Platform.Issues().List(hctx.Ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return Result{Error: fmt.Errorf("listing milestone issues: %w", err)}
	}
	currentCycle := 0
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.FixCycle > currentCycle {
			currentCycle = parsed.FrontMatter.FixCycle
		}
	}
	nextCycle := currentCycle + 1

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    ms.Number,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  pr.Number,
		},
		Task:    cmd.Prompt,
		Context: fmt.Sprintf("Requested by @%s via `/herd fix` on batch PR #%d.", hctx.AuthorLogin, pr.Number),
	})

	truncated := cmd.Prompt
	if len(truncated) > 60 {
		truncated = truncated[:60] + "..."
	}
	fixIssue, err := hctx.Platform.Issues().Create(hctx.Ctx,
		"Fix: "+truncated,
		body,
		[]string{issues.TypeFix, issues.StatusInProgress},
		&ms.Number,
	)
	if err != nil {
		return Result{Error: fmt.Errorf("creating fix issue: %w", err)}
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))
	defaultBranch, _ := hctx.Platform.Repository().GetDefaultBranch(hctx.Ctx)
	_, _ = hctx.Platform.Workflows().Dispatch(hctx.Ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", hctx.Config.Workers.TimeoutMinutes),
		"runner_label":    hctx.Config.Workers.RunnerLabel,
	})

	return Result{Message: fmt.Sprintf("🔧 Created fix issue #%d and dispatched worker.", fixIssue.Number)}
}
