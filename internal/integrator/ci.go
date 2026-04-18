package integrator

import (
	"context"
	"fmt"
	"strconv"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// CheckCIParams holds parameters for CI failure handling.
type CheckCIParams struct {
	RunID          int64
	BatchNumber    int    // Alternative to RunID — used by check_suite trigger
	RepoRoot       string
	UserContext    string // Optional hint from the user, prepended to fix issue body
	BeforeDispatch func() // optional; called once, right before worker dispatch
	Force          bool   // skip pending/success early return — treat any non-success as failure
}

// CheckCIResult holds the result of CI checking.
type CheckCIResult struct {
	Status       string // "success", "failure", "pending"
	FixIssues    []int
	FixCycle     int
	MaxCyclesHit bool
	Skipped      bool // true if require_ci is false or no batch branch found
}

// CheckCI checks CI status on the batch branch after consolidation.
// If CI fails, it dispatches fix workers up to ci_max_fix_cycles.
// If ci_max_fix_cycles is 0, it only notifies.
func CheckCI(ctx context.Context, p platform.Platform, cfg *config.Config, params CheckCIParams) (*CheckCIResult, error) {
	if !cfg.Integrator.RequireCI {
		return &CheckCIResult{Skipped: true}, nil
	}

	var ms *platform.Milestone
	var batchBranch string

	if params.BatchNumber > 0 {
		// Batch-based lookup — used by check_suite trigger
		got, err := p.Milestones().Get(ctx, params.BatchNumber)
		if err != nil {
			return nil, fmt.Errorf("getting milestone #%d: %w", params.BatchNumber, err)
		}
		ms = got
		batchBranch = fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))
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
	}

	if isBatchComplete(ms) {
		fmt.Printf("Batch already complete (milestone #%d closed), skipping.\n", ms.Number)
		return &CheckCIResult{Skipped: true}, nil
	}

	// Get CI status
	status, err := p.Checks().GetCombinedStatus(ctx, batchBranch)
	if err != nil {
		return nil, fmt.Errorf("getting CI status: %w", err)
	}

	if status == "success" {
		return &CheckCIResult{Status: status}, nil
	}
	if status == "pending" && !params.Force {
		return &CheckCIResult{Status: status}, nil
	}

	// Count existing CI fix cycles in the milestone
	allIssues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return nil, fmt.Errorf("listing milestone issues: %w", err)
	}

	currentCycle := 0
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		if parsed.FrontMatter.CIFixCycle > currentCycle {
			currentCycle = parsed.FrontMatter.CIFixCycle
		}
	}

	// Check caps (0 = unlimited)
	if cfg.Integrator.CIMaxFixCycles > 0 && currentCycle >= cfg.Integrator.CIMaxFixCycles {
		notifyCI(ctx, p, batchBranch, fmt.Sprintf(
			"CI failed but max fix cycles (%d) reached. Manual intervention needed.", cfg.Integrator.CIMaxFixCycles))
		return &CheckCIResult{Status: "failure", MaxCyclesHit: true}, nil
	}

	// Create a fix issue for CI failure
	nextCycle := currentCycle + 1

	// Find the batch PR to comment on
	prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err != nil || len(prs) == 0 {
		return nil, fmt.Errorf("no open batch PR found for %s", batchBranch)
	}
	batchPR := prs[0]

	taskText := "CI is failing on the batch branch. Investigate the failures, fix the issues, and ensure all tests pass."
	if params.UserContext != "" {
		taskText = params.UserContext + "\n\n" + taskText
	}

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:    1,
			Batch:      ms.Number,
			Type:       "fix",
			CIFixCycle: nextCycle,
			BatchPR:    batchPR.Number,
		},
		Task:    taskText,
		Context: fmt.Sprintf("CI failed on batch branch `%s` after consolidation (cycle %d).", batchBranch, nextCycle),
	})

	fixIssue, err := p.Issues().Create(ctx,
		fmt.Sprintf("Fix CI failure on %s (cycle %d)", batchBranch, nextCycle),
		body,
		[]string{issues.TypeFix, issues.StatusInProgress},
		&ms.Number,
	)
	if err != nil {
		return nil, fmt.Errorf("creating CI fix issue: %w", err)
	}

	// Dispatch fix worker
	if params.BeforeDispatch != nil {
		params.BeforeDispatch()
	}
	defaultBranch, _ := p.Repository().GetDefaultBranch(ctx)
	_, _ = p.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", fixIssue.Number),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	})

	return &CheckCIResult{
		Status:    "failure",
		FixIssues: []int{fixIssue.Number},
		FixCycle:  nextCycle,
	}, nil
}

func notifyCI(ctx context.Context, p platform.Platform, batchBranch, message string) {
	prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err != nil || len(prs) == 0 {
		return
	}
	_ = p.PullRequests().AddComment(ctx, prs[0].Number,
		fmt.Sprintf("⚠️ **HerdOS Integrator**\n\n%s", message))
}
