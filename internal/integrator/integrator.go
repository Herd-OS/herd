package integrator

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/dag"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// ConsolidateParams holds the parameters for consolidating a worker branch.
type ConsolidateParams struct {
	RunID    int64
	RepoRoot string
}

// ConsolidateResult holds the result of a consolidation.
type ConsolidateResult struct {
	IssueNumber  int
	WorkerBranch string
	Merged       bool
	NoOp         bool
}

// AdvanceParams holds the parameters for advancing tiers.
type AdvanceParams struct {
	RunID    int64
	RepoRoot string
}

// AdvanceResult holds the result of a tier advancement.
type AdvanceResult struct {
	TierComplete    bool
	AllComplete     bool
	DispatchedCount int
	BatchPRNumber   int
}

// ReviewParams holds the parameters for reviewing a batch PR.
type ReviewParams struct {
	RunID    int64
	PRNumber int    // Alternative to RunID — used by pull_request_review trigger
	RepoRoot string
}

// ReviewResult holds the result of a batch PR review.
type ReviewResult struct {
	Approved      bool
	FixIssues     []int
	FixCycle      int
	MaxCyclesHit  bool
	BatchPRNumber int
}

// Consolidate merges a completed worker branch into the batch branch.
// It resolves the worker branch from the workflow run, merges it, and cleans up.
func Consolidate(ctx context.Context, p platform.Platform, g *git.Git, params ConsolidateParams) (*ConsolidateResult, error) {
	// Get the completed run
	run, err := p.Workflows().GetRun(ctx, params.RunID)
	if err != nil {
		return nil, fmt.Errorf("getting run %d: %w", params.RunID, err)
	}

	// Extract issue number from run inputs
	issueNumStr, ok := run.Inputs["issue_number"]
	if !ok {
		return nil, fmt.Errorf("run %d has no issue_number input", params.RunID)
	}
	issueNumber, err := strconv.Atoi(issueNumStr)
	if err != nil {
		return nil, fmt.Errorf("invalid issue_number %q in run %d: %w", issueNumStr, params.RunID, err)
	}

	// Get the issue
	issue, err := p.Issues().Get(ctx, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", issueNumber, err)
	}
	if issue.Milestone == nil {
		return nil, fmt.Errorf("issue #%d has no milestone", issueNumber)
	}

	workerBranch := fmt.Sprintf("herd/worker/%d-%s", issueNumber, planner.Slugify(issue.Title))
	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))

	// Handle failed/cancelled runs
	if run.Conclusion == "failure" || run.Conclusion == "cancelled" {
		// Safety net: ensure issue is labeled failed
		status := issues.StatusLabel(issue.Labels)
		if status != issues.StatusFailed {
			_ = p.Issues().RemoveLabels(ctx, issueNumber, []string{status})
			_ = p.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusFailed})
		}
		return &ConsolidateResult{IssueNumber: issueNumber, Merged: false}, nil
	}

	// Check if worker branch exists (no-op worker = no branch)
	_, err = p.Repository().GetBranchSHA(ctx, workerBranch)
	if err != nil {
		// Branch doesn't exist — no-op worker (task already done)
		return &ConsolidateResult{IssueNumber: issueNumber, NoOp: true, Merged: false}, nil
	}

	// Merge worker branch into batch branch
	if err := g.Fetch("origin"); err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}
	if err := g.Checkout(batchBranch); err != nil {
		return nil, fmt.Errorf("checking out batch branch: %w", err)
	}
	if err := g.Merge("origin/" + workerBranch); err != nil {
		return nil, fmt.Errorf("merging worker branch %s into batch branch: %w", workerBranch, err)
	}
	if err := g.Push("origin", batchBranch); err != nil {
		return nil, fmt.Errorf("pushing batch branch: %w", err)
	}

	// Delete worker branch
	if err := p.Repository().DeleteBranch(ctx, workerBranch); err != nil {
		// Non-fatal — log but don't fail
		fmt.Printf("Warning: failed to delete worker branch %s: %v\n", workerBranch, err)
	}

	return &ConsolidateResult{
		IssueNumber:  issueNumber,
		WorkerBranch: workerBranch,
		Merged:       true,
	}, nil
}

// Advance checks if the current tier is complete and dispatches the next tier.
// If all tiers are complete, it opens the batch PR.
func Advance(ctx context.Context, p platform.Platform, g *git.Git, cfg *config.Config, params AdvanceParams) (*AdvanceResult, error) {
	// Get the run to find the issue and milestone
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

	ms := issue.Milestone
	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

	// List all issues in the milestone
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	// Build DAG from issue dependencies
	tiers, err := buildTiersFromIssues(allIssues)
	if err != nil {
		return nil, fmt.Errorf("building tiers: %w", err)
	}

	// Find the tier that the triggering issue belongs to
	triggerTier := -1
	for t, tier := range tiers {
		for _, num := range tier {
			if num == issueNumber {
				triggerTier = t
				break
			}
		}
		if triggerTier >= 0 {
			break
		}
	}
	if triggerTier < 0 {
		return nil, fmt.Errorf("issue #%d not found in any tier", issueNumber)
	}

	// Check if the triggering issue's tier is complete
	tierComplete := true
	tierStuck := false
	for _, num := range tiers[triggerTier] {
		iss := findIssue(allIssues, num)
		if iss == nil {
			continue
		}
		status := issues.StatusLabel(iss.Labels)
		if status == issues.StatusFailed {
			tierStuck = true
			tierComplete = false
			break
		}
		if status != issues.StatusDone {
			tierComplete = false
		}
	}

	if tierStuck || !tierComplete {
		return &AdvanceResult{TierComplete: false}, nil
	}

	// Tier is complete — check if this was the last tier
	if triggerTier+1 >= len(tiers) {
		// All tiers done — open batch PR
		prNum, err := openBatchPR(ctx, p, g, ms, allIssues, tiers, batchBranch)
		if err != nil {
			return nil, fmt.Errorf("opening batch PR: %w", err)
		}
		return &AdvanceResult{AllComplete: true, TierComplete: true, BatchPRNumber: prNum}, nil
	}

	// Dispatch next tier
	nextTier := tiers[triggerTier+1]
	dispatched := 0

	// Count active workers for concurrency limit
	activeRuns, err := p.Workflows().ListRuns(ctx, platform.RunFilters{Status: "in_progress"})
	if err != nil {
		return nil, fmt.Errorf("counting active workers: %w", err)
	}
	remaining := cfg.Workers.MaxConcurrent - len(activeRuns)

	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting default branch: %w", err)
	}

	for _, num := range nextTier {
		issue := findIssue(allIssues, num)
		if issue == nil {
			continue
		}

		status := issues.StatusLabel(issue.Labels)
		// Double-dispatch prevention: only dispatch blocked issues
		if status != issues.StatusBlocked {
			continue
		}

		// Unblock: blocked → ready
		_ = p.Issues().RemoveLabels(ctx, num, []string{issues.StatusBlocked})

		if dispatched >= remaining {
			// At capacity — just mark ready, don't dispatch
			_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusReady})
			continue
		}

		// Dispatch: ready → in-progress
		_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusInProgress})
		_, err := p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
			"issue_number":    fmt.Sprintf("%d", num),
			"batch_branch":    batchBranch,
			"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
			"runner_label":    cfg.Workers.RunnerLabel,
		})
		if err != nil {
			// Failed to dispatch — label as failed
			_ = p.Issues().RemoveLabels(ctx, num, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, num, []string{issues.StatusFailed})
			continue
		}
		dispatched++
	}

	return &AdvanceResult{
		TierComplete:    true,
		DispatchedCount: dispatched,
	}, nil
}

// buildTiersFromIssues parses issue front matter to build a DAG and compute tiers.
// Returns tiers as slices of issue numbers.
func buildTiersFromIssues(allIssues []*platform.Issue) ([][]int, error) {
	d := dag.New()
	for _, issue := range allIssues {
		d.AddNode(issue.Number)
	}

	for _, issue := range allIssues {
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue // Skip unparseable issues
		}
		for _, dep := range parsed.FrontMatter.DependsOn {
			d.AddEdge(issue.Number, dep)
		}
	}

	return d.Tiers()
}

func findIssue(allIssues []*platform.Issue, number int) *platform.Issue {
	for _, issue := range allIssues {
		if issue.Number == number {
			return issue
		}
	}
	return nil
}

func openBatchPR(ctx context.Context, p platform.Platform, g *git.Git, ms *platform.Milestone, allIssues []*platform.Issue, tiers [][]int, batchBranch string) (int, error) {
	// Check if PR already exists
	existing, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err == nil && len(existing) > 0 {
		return existing[0].Number, nil // PR already opened
	}

	defaultBranch, err := p.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting default branch: %w", err)
	}

	// Rebase batch branch onto main
	if err := g.Fetch("origin"); err != nil {
		return 0, fmt.Errorf("fetching: %w", err)
	}
	if err := g.Checkout(batchBranch); err != nil {
		return 0, fmt.Errorf("checking out batch branch: %w", err)
	}
	if err := g.Rebase("origin/" + defaultBranch); err != nil {
		// Rebase failed — push un-rebased for now (on_conflict: notify)
		fmt.Printf("Warning: rebase onto %s failed, opening PR without rebase: %v\n", defaultBranch, err)
	} else {
		// Force push rebased branch (batch branch is HerdOS-owned)
		if err := g.ForcePush("origin", batchBranch); err != nil {
			return 0, fmt.Errorf("force-pushing rebased batch branch: %w", err)
		}
	}

	// Build PR title and body
	title := fmt.Sprintf("[herd] %s (%d tasks)", ms.Title, len(allIssues))
	body := buildBatchPRBody(ms, allIssues, tiers)

	pr, err := p.PullRequests().Create(ctx, title, body, batchBranch, defaultBranch)
	if err != nil {
		return 0, fmt.Errorf("creating batch PR: %w", err)
	}

	return pr.Number, nil
}

// buildBatchPRBody creates the markdown body for the batch PR.
func buildBatchPRBody(ms *platform.Milestone, allIssues []*platform.Issue, tiers [][]int) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("## Summary\n\nBatch **%s** — %d tasks across %d tiers.\n\n", ms.Title, len(allIssues), len(tiers)))

	// Tasks table
	b.WriteString("## Tasks\n\n")
	b.WriteString("| Issue | Title | Tier | Status |\n")
	b.WriteString("|-------|-------|------|--------|\n")

	for _, issue := range allIssues {
		tier := tierForIssue(issue.Number, tiers)
		status := issues.StatusLabel(issue.Labels)
		if status == "" {
			status = "unknown"
		} else {
			// Strip the prefix for readability
			status = strings.TrimPrefix(status, "herd/status:")
		}
		b.WriteString(fmt.Sprintf("| #%d | %s | %d | %s |\n", issue.Number, issue.Title, tier, status))
	}

	// Worker branches
	b.WriteString("\n## Worker branches\n\n")
	for _, issue := range allIssues {
		branch := fmt.Sprintf("herd/worker/%d-%s", issue.Number, planner.Slugify(issue.Title))
		b.WriteString(fmt.Sprintf("- `%s`\n", branch))
	}

	return b.String()
}

func tierForIssue(number int, tiers [][]int) int {
	for t, tier := range tiers {
		for _, n := range tier {
			if n == number {
				return t
			}
		}
	}
	return 0
}
