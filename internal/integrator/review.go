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

	// Fetch PR comments once for fix requests and prior review context
	prComments, commentErr := p.Issues().ListComments(ctx, pr.Number)
	if commentErr != nil {
		fmt.Printf("Warning: failed to list PR comments: %s\n", commentErr)
	}
	for _, fix := range collectFixRequestsFromComments(prComments) {
		allCriteria = append(allCriteria, "User requested: "+fix)
	}
	priorReviewComments := collectPriorReviewComments(prComments)

	// Run agent review
	reviewOpts := agent.ReviewOptions{
		AcceptanceCriteria:  allCriteria,
		RepoRoot:            params.RepoRoot,
		Strictness:          cfg.Integrator.ReviewStrictness,
		PriorReviewComments: priorReviewComments,
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
		// Agent failed (e.g., API error, suspicious output). Don't propagate the
		// error — return a neutral result so the workflow succeeds and the review
		// retries on the next trigger.
		fmt.Printf("Review agent failed: %s. Will retry on next trigger.\n", err)
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
	}

	// Guard against failed review that returned a result instead of an error
	// (backward compatibility with older claude package).
	if strings.HasPrefix(reviewResult.Summary, "Failed to parse") {
		fmt.Printf("Review agent returned unparseable output. Will retry on next trigger.\n")
		return &ReviewResult{BatchPRNumber: pr.Number}, nil
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

	// Combine HIGH and MEDIUM findings for fix dispatch — only LOW is informational
	actionableFindings := make([]agent.ReviewFinding, 0, len(highFindings)+len(mediumFindings))
	actionableFindings = append(actionableFindings, highFindings...)
	actionableFindings = append(actionableFindings, mediumFindings...)

	// No actionable findings — approve with informational comment and batch summary
	if len(actionableFindings) == 0 {
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
		if iss.State == "closed" {
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

	actionableFindings = dedupFindings(actionableFindings, openFixIssues)
	if len(actionableFindings) == 0 {
		// All findings are covered by existing fix issues — approve to unblock
		// any previous REQUEST_CHANGES review and post an informational comment.
		fmt.Println("All actionable findings are duplicates of existing fix issues, approving.")
		comment := "✅ **HerdOS Agent Review**\n\nAll findings are already covered by existing fix workers. Approving to unblock the PR."
		_ = p.PullRequests().AddComment(ctx, pr.Number, comment)
		_ = p.PullRequests().CreateReview(ctx, pr.Number, "", platform.ReviewApprove)
		return &ReviewResult{Approved: true, BatchPRNumber: pr.Number}, nil
	}

	// Create single batched fix issue with all actionable findings (HIGH + MEDIUM)
	nextCycle := currentCycle + 1

	var fixTaskBuilder strings.Builder
	fixTaskBuilder.WriteString("Fix the following issues found during agent review:\n\n")
	for i, f := range actionableFindings {
		fixTaskBuilder.WriteString(fmt.Sprintf("%d. **[%s]** %s\n", i+1, f.Severity, f.Description))
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
		return &ReviewResult{BatchPRNumber: pr.Number, AllCreatesFailed: true, FindingsCount: len(actionableFindings)}, nil
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
	reviewBody := fmt.Sprintf("Found %d actionable issues (HIGH+MEDIUM). Fix worker dispatched → #%d.", len(actionableFindings), fixIssue.Number)
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

// collectFixRequests fetches comments on the given issue/PR number and
// extracts the prompt/description from any /herd fix commands.
func collectFixRequests(ctx context.Context, p platform.Platform, prNumber int) []string {
	comments, err := p.Issues().ListComments(ctx, prNumber)
	if err != nil {
		fmt.Printf("Warning: failed to list PR comments for fix request collection: %s\n", err)
		return nil
	}
	return collectFixRequestsFromComments(comments)
}

// collectFixRequestsFromComments extracts the prompt/description from any
// /herd fix commands in the given comments.
func collectFixRequestsFromComments(comments []*platform.Comment) []string {
	var fixes []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(body, "/herd fix") {
			continue
		}
		// Ensure exact command match (not e.g. "/herd fixci")
		rest := strings.TrimPrefix(body, "/herd fix")
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' {
			continue
		}
		// Extract the description after "/herd fix"
		desc := strings.TrimSpace(rest)
		// Strip surrounding quotes if present
		if len(desc) >= 2 && desc[0] == '"' && desc[len(desc)-1] == '"' {
			desc = desc[1 : len(desc)-1]
		}
		if desc != "" {
			fixes = append(fixes, desc)
		}
	}
	return fixes
}

// collectPriorReviewComments returns the full body of any previous HerdOS
// agent review comments. These are identified by the emoji markers used in
// buildReviewCycleComment and buildBatchSummaryComment.
func collectPriorReviewComments(comments []*platform.Comment) []string {
	var prior []string
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if strings.HasPrefix(body, "🔍 **HerdOS Agent Review**") ||
			strings.HasPrefix(body, "✅ **HerdOS Agent Review**") {
			prior = append(prior, body)
		}
	}
	return prior
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
	reviewFixIssues := 0
	ciFixIssues := 0
	maxFixCycle := 0
	maxCIFixCycle := 0
	for _, iss := range allIssues {
		parsed, err := issues.ParseBody(iss.Body)
		if err != nil {
			continue
		}
		switch {
		case parsed.FrontMatter.CIFixCycle > 0:
			ciFixIssues++
		case parsed.FrontMatter.Type == "fix":
			reviewFixIssues++
		default:
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
	b.WriteString(fmt.Sprintf("- Review fix issues: %d\n", reviewFixIssues))
	b.WriteString(fmt.Sprintf("- CI fix issues: %d\n", ciFixIssues))
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
	if totalFindings == 0 {
		b.WriteString("No issues found.\n")
		return b.String()
	} else if totalFindings == 1 {
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
		if len(fixIssueNums) > 0 {
			nums := make([]string, len(fixIssueNums))
			for i, n := range fixIssueNums {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			b.WriteString(fmt.Sprintf("**MEDIUM** (fix worker dispatched → %s):\n", strings.Join(nums, ", ")))
		} else {
			b.WriteString("**MEDIUM**:\n")
		}
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

// extractFindingLines extracts individual finding descriptions from a fix
// issue body. Findings are stored as numbered list items ("1. description").
// Matching against individual lines avoids false-positive substring matches
// when multiple findings are concatenated in a single batched issue body.
func extractFindingLines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		// Strip leading number+dot prefix (e.g. "1. ", "12. ")
		if len(line) > 2 {
			dotIdx := strings.Index(line, ". ")
			if dotIdx > 0 && dotIdx <= 4 {
				prefix := line[:dotIdx]
				if _, err := strconv.Atoi(prefix); err == nil {
					text := strings.TrimSpace(line[dotIdx+2:])
					if text != "" {
						lines = append(lines, text)
					}
					continue
				}
			}
		}
		// Skip bare numbered-list markers (e.g. "1." with no content after
		// trimming). These arise when a list item like "1. " is trimmed to
		// "1." and falls through the dot-space detection above.
		if len(line) >= 2 && len(line) <= 5 && line[len(line)-1] == '.' {
			if _, err := strconv.Atoi(line[:len(line)-1]); err == nil {
				continue
			}
		}
		// Also include raw non-empty lines for simple (non-batched) bodies
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// minSubstringLen is the minimum description prefix length for substring
// matching in dedupFindings. Descriptions shorter than this use equality
// to avoid false positives (e.g. "bug" matching any line containing "bug").
const minSubstringLen = 20

// descriptionMatch reports whether text contains a match for descPrefix.
// For short prefixes (< minSubstringLen) it requires equality; for longer
// ones it uses substring containment.
func descriptionMatch(text, descPrefix string) bool {
	if len(descPrefix) < minSubstringLen {
		return text == descPrefix
	}
	return strings.Contains(text, descPrefix)
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
			if descriptionMatch(iss.Title, descPrefix) {
				fmt.Printf("Skipping duplicate finding: similar to #%d\n", iss.Number)
				duplicate = true
				break
			}
			// Match against individual finding lines rather than the
			// raw body to avoid false positives in batched issues.
			for _, line := range extractFindingLines(iss.Body) {
				if descriptionMatch(line, descPrefix) {
					fmt.Printf("Skipping duplicate finding: similar to #%d\n", iss.Number)
					duplicate = true
					break
				}
			}
			if duplicate {
				break
			}
		}
		if !duplicate {
			deduped = append(deduped, f)
		}
	}
	return deduped
}
