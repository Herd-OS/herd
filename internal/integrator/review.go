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

	// Skip if batch already complete
	if isBatchComplete(ms) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", ms.Number)
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
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

	// Use the PR diff from GitHub API — this shows exactly what will be
	// merged, excluding commits already on main. Using git diff was unreliable
	// because merging main into the batch branch caused the reviewer to see
	// changes that were already on main.
	diff, err := p.PullRequests().GetDiff(ctx, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("getting PR diff: %w", err)
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
		Strictness:         cfg.Integrator.ReviewStrictness,
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

	// Partition findings by severity
	highFindings, mediumFindings, lowFindings := filterFindingsBySeverity(reviewResult.Findings)

	// Handle approved
	if reviewResult.Approved {
		summaryComment := buildBatchSummaryComment(allIssues, reviewResult.Summary)
		_ = p.PullRequests().AddComment(ctx, pr.Number, summaryComment)
		_ = p.PullRequests().CreateReview(ctx, pr.Number, "", platform.ReviewApprove)
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
		for _, f := range reviewResult.Findings {
			comment += fmt.Sprintf("- **%s** %s\n", f.Severity, f.Description)
		}
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
	}

	// Safety valve — check HIGH findings count only
	if len(highFindings) > safetyValveLimit {
		comment := fmt.Sprintf("⚠️ **HerdOS Integrator**\n\nAgent review found %d high-severity issues in a single pass. "+
			"This exceeds the safety limit (%d). Creating fix workers was skipped to prevent runaway agent invocations.",
			len(highFindings), safetyValveLimit)
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		return &ReviewResult{MaxCyclesHit: true, BatchPRNumber: pr.Number}, nil
	}

	// No high-severity findings — approve with informational comment and batch summary
	if len(highFindings) == 0 {
		comment := buildReviewCycleComment(0, cfg.Integrator.ReviewMaxFixCycles, nil, highFindings, mediumFindings, lowFindings)
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		summaryComment := buildBatchSummaryComment(allIssues, reviewResult.Summary)
		_ = p.PullRequests().AddComment(ctx, pr.Number, summaryComment)
		_ = p.PullRequests().CreateReview(ctx, pr.Number, "", platform.ReviewApprove)
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Collect open fix issues for dedup
	var openFixIssues []*platform.Issue
	for _, iss := range allIssues {
		if iss.State == "closed" || issues.HasLabel(iss.Labels, issues.StatusDone) {
			continue
		}
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.Type == "fix" {
			openFixIssues = append(openFixIssues, iss)
		}
	}

	highFindings = dedupFindings(highFindings, openFixIssues)
	if len(highFindings) == 0 {
		// All findings are covered by existing fix issues — approve to unblock
		// any previous REQUEST_CHANGES review and post an informational comment.
		fmt.Println("All high-severity findings are duplicates of existing fix issues, approving.")
		comment := "✅ **HerdOS Agent Review**\n\nAll high-severity findings are already covered by existing fix workers. Approving to unblock the PR."
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		_ = p.PullRequests().CreateReview(ctx, pr.Number, "", platform.ReviewApprove)
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Create single batched fix issue with ALL high-severity findings
	nextCycle := currentCycle + 1

	var fixTaskBuilder strings.Builder
	fixTaskBuilder.WriteString("Fix the following issues found during agent review:\n\n")
	for i, f := range highFindings {
		fixTaskBuilder.WriteString(fmt.Sprintf("%d. %s\n", i+1, f.Description))
	}

	fixBody := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    ms.Number,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  pr.Number,
		},
		Task:    fixTaskBuilder.String(),
		Context: fmt.Sprintf("Found during agent review of batch PR #%d ([herd] %s), cycle %d.", pr.Number, ms.Title, nextCycle),
	})

	fixTitle := fmt.Sprintf("Review fixes (cycle %d)", nextCycle)

	defaultBranchForDispatch, _ := p.Repository().GetDefaultBranch(ctx)

	fixIssue, err := p.Issues().Create(ctx, fixTitle, fixBody,
		[]string{issues.TypeFix, issues.StatusInProgress}, &ms.Number)
	if err != nil {
		// Failed to create the fix issue
		return &ReviewResult{BatchPRNumber: pr.Number, AllCreatesFailed: true, FindingsCount: len(highFindings)}, nil
	}

	// Dispatch single fix worker
	_, _ = p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranchForDispatch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	})

	fixIssueNums := []int{fixIssue.Number}

	// Post structured findings comment
	findingsComment := buildReviewCycleComment(nextCycle, cfg.Integrator.ReviewMaxFixCycles, fixIssueNums, highFindings, mediumFindings, lowFindings)
	_ = p.PullRequests().AddComment(ctx, pr.Number, findingsComment)

	// Block merge with Request Changes review
	reviewBody := fmt.Sprintf("Found %d high-severity issues. Fix worker dispatched → #%d.", len(highFindings), fixIssue.Number)
	_ = p.PullRequests().CreateReview(ctx, pr.Number, reviewBody, platform.ReviewRequestChanges)

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

// filterFindingsBySeverity partitions findings into high, medium, and low.
func filterFindingsBySeverity(findings []agent.ReviewFinding) (high, medium, low []agent.ReviewFinding) {
	for _, f := range findings {
		switch strings.ToUpper(f.Severity) {
		case "HIGH":
			high = append(high, f)
		case "MEDIUM":
			medium = append(medium, f)
		default:
			low = append(low, f)
		}
	}
	return
}

// buildBatchSummaryComment creates the approval comment with batch statistics.
func buildBatchSummaryComment(allIssues []*platform.Issue, reviewSummary string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("✅ **HerdOS Agent Review**\n\n%s\n", reviewSummary))

	// Count statistics from issues
	originalTasks := 0
	fixIssues := 0
	maxFixCycle := 0
	maxCIFixCycle := 0
	for _, iss := range allIssues {
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		if parsed.FrontMatter.Type == "fix" || parsed.FrontMatter.CIFixCycle > 0 {
			fixIssues++
		} else {
			originalTasks++
		}
		if parsed.FrontMatter.FixCycle > maxFixCycle {
			maxFixCycle = parsed.FrontMatter.FixCycle
		}
		if parsed.FrontMatter.CIFixCycle > maxCIFixCycle {
			maxCIFixCycle = parsed.FrontMatter.CIFixCycle
		}
	}

	b.WriteString("\n📊 **Batch Summary**\n\n")
	b.WriteString(fmt.Sprintf("- Original tasks: %d\n", originalTasks))
	b.WriteString(fmt.Sprintf("- Fix issues created: %d\n", fixIssues))
	b.WriteString(fmt.Sprintf("- Review cycles: %d\n", maxFixCycle))
	b.WriteString(fmt.Sprintf("- CI fix cycles: %d\n", maxCIFixCycle))
	b.WriteString(fmt.Sprintf("- Total issues: %d\n", len(allIssues)))

	return b.String()
}

// buildReviewCycleComment creates a structured PR comment for a review cycle.
func buildReviewCycleComment(cycle, maxCycles int, fixIssueNums []int, high, medium, low []agent.ReviewFinding) string {
	var b strings.Builder

	totalFindings := len(high) + len(medium) + len(low)

	if cycle > 0 {
		b.WriteString(fmt.Sprintf("🔍 **HerdOS Agent Review** (cycle %d of %d)\n\n", cycle, maxCycles))
	} else {
		b.WriteString("🔍 **HerdOS Agent Review**\n\n")
	}
	if totalFindings == 1 {
		b.WriteString("Found 1 issue:\n\n")
	} else {
		b.WriteString(fmt.Sprintf("Found %d issues:\n\n", totalFindings))
	}

	if len(high) > 0 {
		if len(fixIssueNums) > 0 {
			nums := make([]string, len(fixIssueNums))
			for i, n := range fixIssueNums {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			b.WriteString(fmt.Sprintf("**HIGH** (fix worker dispatched → %s):\n", strings.Join(nums, ", ")))
		} else {
			b.WriteString("**HIGH**:\n")
		}
		for _, f := range high {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
		b.WriteString("\n")
	}

	if len(medium) > 0 {
		b.WriteString("**MEDIUM** (informational):\n")
		for _, f := range medium {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
		b.WriteString("\n")
	}

	if len(low) > 0 {
		b.WriteString("**LOW** (informational):\n")
		for _, f := range low {
			b.WriteString(fmt.Sprintf("- %s\n", f.Description))
		}
	}

	return b.String()
}

// dedupFindings removes findings that are similar to existing open fix issues.
func dedupFindings(findings []agent.ReviewFinding, openFixIssues []*platform.Issue) []agent.ReviewFinding {
	var deduped []agent.ReviewFinding
	for _, f := range findings {
		descPrefix := f.Description
		if len(descPrefix) > 100 {
			descPrefix = descPrefix[:100]
		}
		duplicate := false
		for _, iss := range openFixIssues {
			if strings.Contains(iss.Title, descPrefix) || strings.Contains(iss.Body, descPrefix) {
				fmt.Printf("Skipping duplicate finding: similar to #%d\n", iss.Number)
				duplicate = true
				break
			}
		}
		if !duplicate {
			deduped = append(deduped, f)
		}
	}
	return deduped
}
