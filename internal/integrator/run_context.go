package integrator

import (
	"context"
	"fmt"
	"strconv"

	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// WorkerRunContext contains the metadata needed to process a completed Herd
// worker workflow run.
type WorkerRunContext struct {
	RunID           int64
	IssueNumber     int
	IssueTitle      string
	MilestoneNumber int
	MilestoneTitle  string
	BatchBranch     string
	WorkerBranch    string
	WorkflowName    string
	WorkflowPath    string
	HeadBranch      string
	HeadSHA         string
	Conclusion      string
	LockKey         string

	issue *platform.Issue
}

// ResolveWorkerRunContext resolves a completed Herd worker run to its batch
// metadata without trusting workflow_run.head_branch, which can be the default
// branch for workflow_dispatch workers.
func ResolveWorkerRunContext(ctx context.Context, p platform.Platform, runID int64) (*WorkerRunContext, error) {
	run, err := p.Workflows().GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("getting run %d: %w", runID, err)
	}
	if run == nil {
		return nil, fmt.Errorf("run %d not found", runID)
	}

	issueNumStr, ok := run.Inputs["issue_number"]
	if !ok || issueNumStr == "" {
		return nil, fmt.Errorf("run %d has no issue_number input", runID)
	}
	issueNumber, err := strconv.Atoi(issueNumStr)
	if err != nil {
		return nil, fmt.Errorf("invalid issue_number %q in run %d: %w", issueNumStr, runID, err)
	}

	issue, err := p.Issues().Get(ctx, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d for run %d: %w", issueNumber, runID, err)
	}
	if issue == nil {
		return nil, fmt.Errorf("issue #%d for run %d not found", issueNumber, runID)
	}
	if issue.Milestone == nil {
		return nil, fmt.Errorf("issue #%d for run %d has no milestone", issueNumber, runID)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))
	workerBranch := fmt.Sprintf("herd/worker/%d-%s", issue.Number, planner.Slugify(issue.Title))

	return &WorkerRunContext{
		RunID:           run.ID,
		IssueNumber:     issue.Number,
		IssueTitle:      issue.Title,
		MilestoneNumber: issue.Milestone.Number,
		MilestoneTitle:  issue.Milestone.Title,
		BatchBranch:     batchBranch,
		WorkerBranch:    workerBranch,
		WorkflowName:    run.WorkflowName,
		WorkflowPath:    run.WorkflowPath,
		HeadBranch:      run.HeadBranch,
		HeadSHA:         run.HeadSHA,
		Conclusion:      run.Conclusion,
		LockKey:         fmt.Sprintf("batch-%d", issue.Milestone.Number),
		issue:           issue,
	}, nil
}

func logWorkerRunContext(action string, c *WorkerRunContext) {
	if c == nil {
		return
	}
	fmt.Printf("resolved worker run context: action=%s run_id=%d issue=%d milestone=%d batch_branch=%s worker_branch=%s conclusion=%s lock_key=%s workflow=%q workflow_path=%q head_branch=%q head_sha=%s\n",
		action, c.RunID, c.IssueNumber, c.MilestoneNumber, c.BatchBranch, c.WorkerBranch, c.Conclusion, c.LockKey, c.WorkflowName, c.WorkflowPath, c.HeadBranch, c.HeadSHA)
}
