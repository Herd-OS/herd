package integrator

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// CIFailureContext describes a completed configured CI workflow_run trigger.
type CIFailureContext struct {
	RunID        int64
	WorkflowID   int64
	Workflow     string
	WorkflowPath string
	HeadBranch   string
	HeadSHA      string
	Conclusion   string
	URL          string
	Diagnostics  *platform.WorkflowRunDiagnostics
}

// CheckCIParams holds parameters for CI failure handling.
type CheckCIParams struct {
	RunID          int64
	CIRun          *CIFailureContext
	BatchNumber    int // Alternative to RunID — used by check_suite trigger
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
	} else if params.CIRun != nil {
		// CI workflow_run lookup — used by configured CI workflow completion triggers.
		if !isConfiguredCIWorkflow(params.CIRun.Workflow, cfg.Integrator.CIWorkflows) {
			fmt.Printf("Skipping CI workflow run %d: workflow %q is not configured for CI self-heal.\n", params.CIRun.RunID, params.CIRun.Workflow)
			return &CheckCIResult{Skipped: true}, nil
		}
		batchNumber, ok := parseBatchNumberFromBranch(params.CIRun.HeadBranch)
		if !ok {
			fmt.Printf("Skipping CI workflow run %d: head branch %q is not a herd batch branch.\n", params.CIRun.RunID, params.CIRun.HeadBranch)
			return &CheckCIResult{Skipped: true}, nil
		}
		got, err := p.Milestones().Get(ctx, batchNumber)
		if err != nil {
			return nil, fmt.Errorf("getting milestone #%d: %w", batchNumber, err)
		}
		ms = got
		batchBranch = params.CIRun.HeadBranch
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

	effectiveStatus := status
	if params.CIRun != nil && isFailedCIConclusion(params.CIRun.Conclusion) {
		effectiveStatus = "failure"
	}

	if effectiveStatus == "success" && !params.Force {
		return &CheckCIResult{Status: status}, nil
	}
	if effectiveStatus == "pending" && !params.Force {
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

	// Skip if any fix-type worker is already in progress in this milestone.
	// This includes:
	//   - review fix issues (FrontMatter.Type == "fix", no CIFixCycle, not ConflictResolution)
	//   - CI fix issues (FrontMatter.CIFixCycle > 0)
	//   - conflict resolution issues (FrontMatter.ConflictResolution == true)
	// Any of these still active should pause CI fix dispatch — the next workflow_run
	// trigger when that worker completes will re-run CheckCI.
	currentCycle := 0
	for _, iss := range allIssues {
		parsed, parseErr := issues.ParseBody(iss.Body)
		if parseErr != nil {
			continue
		}
		fm := parsed.FrontMatter

		isCIFix := fm.CIFixCycle > 0
		isConflictRes := fm.ConflictResolution
		isReviewFix := fm.Type == "fix" && !isCIFix && !isConflictRes

		if isCIFix || isConflictRes || isReviewFix {
			status := issues.StatusLabel(iss.Labels)
			if status == issues.StatusInProgress || status == issues.StatusReady {
				kind := "fix"
				switch {
				case isCIFix:
					kind = "CI fix"
				case isConflictRes:
					kind = "conflict resolution"
				case isReviewFix:
					kind = "review fix"
				}
				fmt.Printf("Skipping CI fix: %s worker #%d is still %s\n", kind, iss.Number, status)
				return &CheckCIResult{Status: "failure"}, nil
			}
		}

		if fm.CIFixCycle > currentCycle {
			currentCycle = fm.CIFixCycle
		}
	}

	// Check caps (0 = unlimited)
	if cfg.Integrator.CIMaxFixCycles > 0 && currentCycle >= cfg.Integrator.CIMaxFixCycles {
		notifyCI(ctx, p, batchBranch, fmt.Sprintf(
			"CI failed but max fix cycles (%d) reached. Manual intervention needed.", cfg.Integrator.CIMaxFixCycles))
		return &CheckCIResult{Status: "failure", MaxCyclesHit: true}, nil
	}

	// Find the batch PR to comment on
	prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err != nil || len(prs) == 0 {
		return nil, fmt.Errorf("no open batch PR found for %s", batchBranch)
	}
	batchPR := prs[0]

	classification := classifyCIFailure(diagnosticsFromContext(params.CIRun))
	if classification == "infrastructure" && !params.Force {
		message := "CI appears to have failed because of infrastructure rather than a code change. HerdOS will not dispatch a code-fix worker for this run."
		if params.CIRun != nil {
			message += "\n\n" + renderInfraFailureSummary(params.CIRun)
		}
		if rerunErr := p.Checks().RerunFailedChecks(ctx, batchBranch); rerunErr != nil {
			fmt.Printf("Warning: failed to rerun failed checks for %s: %v\n", batchBranch, rerunErr)
			message += fmt.Sprintf("\n\nWarning: failed to request a rerun automatically: %v", rerunErr)
		}
		_ = p.PullRequests().AddComment(ctx, batchPR.Number,
			fmt.Sprintf("⚠️ **HerdOS Integrator**\n\n%s", message))
		return &CheckCIResult{Status: "failure"}, nil
	}

	// Create a fix issue for CI failure
	nextCycle := currentCycle + 1

	taskText := "CI is failing on the batch branch. Investigate the failures, fix the issues, and ensure all tests pass."
	if params.UserContext != "" {
		taskText = params.UserContext + "\n\n" + taskText
	}
	contextText := fmt.Sprintf("CI failed on batch branch `%s` after consolidation (cycle %d).", batchBranch, nextCycle)
	if failureContext := renderCIFailureContext(params.CIRun); failureContext != "" {
		contextText += "\n\n" + failureContext
	}
	if classification == "infrastructure" || classification == "unknown" {
		contextText += "\n\n## Failure Classification\n\n"
		switch classification {
		case "infrastructure":
			contextText += "This failure looks like CI infrastructure. A fix worker is being dispatched because the check was forced."
		default:
			contextText += "HerdOS could not confidently classify this CI failure from the available diagnostics."
		}
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
		Context: contextText,
	})

	truncatedBody, overflow := issues.TruncateIssueBody(body)
	fixIssue, err := p.Issues().Create(ctx,
		fmt.Sprintf("Fix CI failure on %s (cycle %d)", batchBranch, nextCycle),
		truncatedBody,
		[]string{issues.TypeFix, issues.StatusInProgress},
		&ms.Number,
	)
	if err != nil {
		return nil, fmt.Errorf("creating CI fix issue: %w", err)
	}
	for _, comment := range issues.SplitOverflowComments(overflow) {
		if cerr := p.Issues().AddComment(ctx, fixIssue.Number, comment); cerr != nil {
			fmt.Printf("Warning: failed to post overflow comment on CI fix issue #%d: %v\n", fixIssue.Number, cerr)
		}
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

func diagnosticsFromContext(ctx *CIFailureContext) *platform.WorkflowRunDiagnostics {
	if ctx == nil {
		return nil
	}
	return ctx.Diagnostics
}

func renderInfraFailureSummary(ctx *CIFailureContext) string {
	var b strings.Builder
	if ctx.Workflow != "" {
		b.WriteString(fmt.Sprintf("- Workflow: %s\n", ctx.Workflow))
	}
	if ctx.URL != "" {
		b.WriteString(fmt.Sprintf("- Run: %s\n", ctx.URL))
	}
	if ctx.Diagnostics != nil && ctx.Diagnostics.LogStatus != "" {
		b.WriteString(fmt.Sprintf("- Log status: %s\n", ctx.Diagnostics.LogStatus))
	}
	return strings.TrimSpace(b.String())
}
