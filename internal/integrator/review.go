package integrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

const safetyValveLimit = 10

// Review runs an agent review on the batch PR.
// If approved, it optionally auto-merges. If changes are requested,
// it creates fix issues and dispatches fix workers.
func Review(ctx context.Context, p platform.Platform, ag agent.Agent, g *git.Git, cfg *config.Config, params ReviewParams) (*ReviewResult, error) {
	var pr *platform.PullRequest
	var ms *platform.Milestone
	var batchBranch string

	if params.PRNumber > 0 {
		// Direct PR lookup — used by pull_request_review trigger
		got, err := p.PullRequests().Get(ctx, params.PRNumber)
		if err != nil {
			return nil, fmt.Errorf("getting PR #%d: %w", params.PRNumber, err)
		}
		pr = got
		batchBranch = pr.Head

		msNumber, err := ParseBatchBranchMilestone(batchBranch)
		if err != nil {
			return nil, fmt.Errorf("parsing milestone from branch %s: %w", batchBranch, err)
		}
		got_ms, err := p.Milestones().Get(ctx, msNumber)
		if err != nil {
			return nil, fmt.Errorf("getting milestone #%d: %w", msNumber, err)
		}
		ms = got_ms
	} else if params.BatchNumber > 0 {
		// Batch-based lookup — used by advance-on-close
		got_ms, err := p.Milestones().Get(ctx, params.BatchNumber)
		if err != nil {
			return nil, fmt.Errorf("getting milestone #%d: %w", params.BatchNumber, err)
		}
		ms = got_ms
		batchBranch = fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

		prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
		if err != nil {
			return nil, fmt.Errorf("listing batch PRs: %w", err)
		}
		if len(prs) == 0 {
			return &ReviewResult{}, nil // No batch PR yet
		}
		pr = prs[0]
	} else {
		// Run-based lookup — used by workflow_run trigger
		run, err := p.Workflows().GetRun(ctx, params.RunID)
		if err != nil {
			return nil, fmt.Errorf("getting run %d: %w", params.RunID, err)
		}

		issueNumStr := run.Inputs["issue_number"]
		issueNumber, err := strconv.Atoi(issueNumStr)
		if err != nil {
			return nil, fmt.Errorf("invalid issue_number: %w", err)
		}

		issue, err := p.Issues().Get(ctx, issueNumber)
		if err != nil {
			return nil, fmt.Errorf("getting issue #%d: %w", issueNumber, err)
		}
		if issue.Milestone == nil {
			return nil, fmt.Errorf("issue #%d has no milestone", issueNumber)
		}

		ms = issue.Milestone
		batchBranch = fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

		// Find batch PR
		prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
		if err != nil {
			return nil, fmt.Errorf("listing batch PRs: %w", err)
		}
		if len(prs) == 0 {
			return &ReviewResult{}, nil // No batch PR yet
		}
		pr = prs[0]
	}

	// Check if review is enabled
	if !cfg.Integrator.Review {
		// Skip review, just check auto-merge
		if cfg.PullRequests.AutoMerge {
			if _, err := p.PullRequests().Merge(ctx, pr.Number, platform.MergeMethod(cfg.Integrator.Strategy)); err != nil {
				return nil, fmt.Errorf("auto-merging batch PR #%d: %w", pr.Number, err)
			}
			if err := postMergeCleanup(ctx, p, ms.Number, batchBranch); err != nil {
				return nil, fmt.Errorf("post-merge cleanup: %w", err)
			}
		}
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Fetch remote refs so the batch branch is available locally
	_ = g.Fetch("origin") // best-effort — may fail in test environments without a remote
	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting default branch: %w", err)
	}
	// Try remote refs first (Actions checkout), fall back to local refs (tests)
	diff, err := g.Diff("origin/"+defaultBranch, "origin/"+batchBranch)
	if err != nil {
		diff, err = g.Diff(defaultBranch, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("getting diff: %w", err)
		}
	}

	// Collect acceptance criteria from all milestone issues
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	// Skip review if fix workers from a previous cycle are still running.
	// Without this gate, every fix worker completion triggers a new review
	// that sees the remaining unfixed issues and creates duplicate fix issues.
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.Type == "fix" || parsed.FrontMatter.CIFixCycle > 0 {
			status := issues.StatusLabel(iss.Labels)
			if status == issues.StatusInProgress || status == issues.StatusReady {
				fmt.Printf("Skipping review: fix issue #%d is still %s\n", iss.Number, status)
				return &ReviewResult{BatchPRNumber: pr.Number}, nil
			}
		}
	}

	var allCriteria []string
	for _, iss := range allIssues {
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		allCriteria = append(allCriteria, parsed.Criteria...)
	}

	// Run agent review
	reviewOpts := agent.ReviewOptions{
		AcceptanceCriteria: allCriteria,
		RepoRoot:           params.RepoRoot,
	}

	// Load integrator role instructions
	ri, readErr := os.ReadFile(filepath.Join(params.RepoRoot, ".herd", "integrator.md"))
	if readErr == nil {
		reviewOpts.SystemPrompt = string(ri)
	}

	if params.ExtraInstructions != "" {
		if reviewOpts.SystemPrompt != "" {
			reviewOpts.SystemPrompt += "\n\n"
		}
		reviewOpts.SystemPrompt += params.ExtraInstructions
	}

	reviewResult, err := ag.Review(ctx, diff, reviewOpts)
	if err != nil {
		return nil, fmt.Errorf("agent review failed: %w", err)
	}

	// Handle approved
	if reviewResult.Approved {
		_ = p.PullRequests().AddComment(ctx, pr.Number,
			fmt.Sprintf("✅ **HerdOS Review**\n\n%s", reviewResult.Summary))
		_ = p.PullRequests().CreateReview(ctx, pr.Number, reviewResult.Summary, platform.ReviewApprove)
		if cfg.PullRequests.AutoMerge {
			if _, err := p.PullRequests().Merge(ctx, pr.Number, platform.MergeMethod(cfg.Integrator.Strategy)); err != nil {
				return nil, fmt.Errorf("auto-merging batch PR #%d: %w", pr.Number, err)
			}
			if err := postMergeCleanup(ctx, p, ms.Number, batchBranch); err != nil {
				return nil, fmt.Errorf("post-merge cleanup: %w", err)
			}
		}
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Handle changes requested — determine fix cycle
	currentCycle := findMaxFixCycle(allIssues)

	if cfg.Integrator.ReviewMaxFixCycles > 0 && currentCycle >= cfg.Integrator.ReviewMaxFixCycles {
		comment := fmt.Sprintf("⚠️ **HerdOS Integrator**\n\nAgent review found issues but max fix cycles (%d) reached. Manual intervention needed:\n\n",
			cfg.Integrator.ReviewMaxFixCycles)
		for _, c := range reviewResult.Comments {
			comment += fmt.Sprintf("- %s\n", c)
		}
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
	}

	// Safety valve
	if len(reviewResult.Comments) > safetyValveLimit {
		comment := fmt.Sprintf("⚠️ **HerdOS Integrator**\n\nAgent review found %d issues in a single pass. "+
			"This exceeds the safety limit (%d). Creating fix workers was skipped to prevent runaway agent invocations.",
			len(reviewResult.Comments), safetyValveLimit)
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
	}

	// Post findings comment (dispatch count posted after loop with accurate count)
	nextCycle := currentCycle + 1
	var findingsMsg strings.Builder
	findingsMsg.WriteString(fmt.Sprintf("🔍 **HerdOS Review** (cycle %d)\n\n", nextCycle))
	for _, comment := range reviewResult.Comments {
		findingsMsg.WriteString(fmt.Sprintf("- %s\n", comment))
	}

	// Create fix issues and dispatch workers
	var fixIssueNums []int

	defaultBranchForDispatch, _ := p.Repository().GetDefaultBranch(ctx)

	for _, comment := range reviewResult.Comments {
		body := issues.RenderBody(issues.IssueBody{
			FrontMatter: issues.FrontMatter{
				Version:  1,
				Batch:    ms.Number,
				Type:     "fix",
				FixCycle: nextCycle,
				BatchPR:  pr.Number,
			},
			Task:    comment,
			Context: fmt.Sprintf("Found during agent review of batch PR #%d ([herd] %s).", pr.Number, ms.Title),
		})

		fixIssue, err := p.Issues().Create(ctx, "Fix: "+truncate(comment, 60), body,
			[]string{issues.TypeFix, issues.StatusInProgress}, &ms.Number)
		if err != nil {
			continue
		}
		fixIssueNums = append(fixIssueNums, fixIssue.Number)

		// Dispatch fix worker
		_, _ = p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranchForDispatch, map[string]string{
			"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
			"batch_branch":    batchBranch,
			"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
			"runner_label":    cfg.Workers.RunnerLabel,
		})
	}

	n := len(fixIssueNums)
	if n == 0 {
		// All issue creates failed — nothing was dispatched. Return without
		// posting a comment to avoid a misleading "Dispatching 0 fix workers."
		// message and the double-comment situation when invoked via /herd review.
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
	}

	findingsMsg.WriteString(fmt.Sprintf("\nDispatching %d fix %s.", n, map[bool]string{true: "worker", false: "workers"}[n == 1]))
	_ = p.PullRequests().AddComment(ctx, pr.Number, findingsMsg.String())

	return &ReviewResult{
		FixIssues:     fixIssueNums,
		FixCycle:      nextCycle,
		BatchPRNumber: pr.Number,
	}, nil
}

// postMergeCleanup closes all issues in the milestone, closes the milestone,
// and deletes the batch branch.
func postMergeCleanup(ctx context.Context, p platform.Platform, msNumber int, batchBranch string) error {
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &msNumber,
	})
	if err != nil {
		return fmt.Errorf("listing milestone issues: %w", err)
	}

	closed := "closed"
	for _, issue := range allIssues {
		_, _ = p.Issues().Update(ctx, issue.Number, platform.IssueUpdate{State: &closed})
	}

	_, _ = p.Milestones().Update(ctx, msNumber, platform.MilestoneUpdate{State: &closed})

	_ = p.Repository().DeleteBranch(ctx, batchBranch)

	return nil
}

func findMaxFixCycle(allIssues []*platform.Issue) int {
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

// ParseBatchBranchMilestone extracts the milestone number from a batch branch name.
// Expected format: "herd/batch/{number}-{slug}"
func ParseBatchBranchMilestone(branch string) (int, error) {
	parts := strings.TrimPrefix(branch, "herd/batch/")
	if parts == branch {
		return 0, fmt.Errorf("not a batch branch: %s", branch)
	}
	idx := strings.Index(parts, "-")
	if idx < 0 {
		return 0, fmt.Errorf("invalid batch branch format: %s", branch)
	}
	return strconv.Atoi(parts[:idx])
}

func truncate(s string, max int) string {
	// Truncate to first line, then to max chars
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
