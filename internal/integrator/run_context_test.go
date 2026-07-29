package integrator

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveWorkerRunContext(t *testing.T) {
	ms := &platform.Milestone{Number: 7, Title: "Batch: Ship It"}
	issue := &platform.Issue{Number: 42, Title: "Fix Login Flow!", Milestone: ms}
	run := &platform.Run{
		ID:           100,
		WorkflowName: "Herd Worker",
		WorkflowPath: ".github/workflows/herd-worker.yml",
		HeadBranch:   "main",
		HeadSHA:      "abc123",
		Conclusion:   "success",
		Inputs:       map[string]string{"issue_number": "42"},
	}
	mock := &mockPlatform{
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{100: run}},
		issues: &mockIssueService{getResult: map[int]*platform.Issue{
			42: issue,
		}},
	}

	got, err := ResolveWorkerRunContext(context.Background(), mock, 100)
	require.NoError(t, err)

	assert.Equal(t, int64(100), got.RunID)
	assert.Equal(t, 42, got.IssueNumber)
	assert.Equal(t, "Fix Login Flow!", got.IssueTitle)
	assert.Equal(t, 7, got.MilestoneNumber)
	assert.Equal(t, "Batch: Ship It", got.MilestoneTitle)
	assert.Equal(t, "herd/batch/7-batch-ship-it", got.BatchBranch)
	assert.Equal(t, "herd/worker/42-fix-login-flow", got.WorkerBranch)
	assert.Equal(t, "Herd Worker", got.WorkflowName)
	assert.Equal(t, ".github/workflows/herd-worker.yml", got.WorkflowPath)
	assert.Equal(t, "main", got.HeadBranch)
	assert.Equal(t, "abc123", got.HeadSHA)
	assert.Equal(t, "success", got.Conclusion)
	assert.Equal(t, "batch-7", got.LockKey)
	assert.Same(t, issue, got.issue)
}

func TestLogWorkerRunContextIncludesRunMetadataAndLockKey(t *testing.T) {
	runCtx := &WorkerRunContext{
		RunID:           100,
		IssueNumber:     42,
		MilestoneNumber: 7,
		BatchBranch:     "herd/batch/7-batch",
		WorkerBranch:    "herd/worker/42-task",
		WorkflowName:    "Herd Worker",
		WorkflowPath:    ".github/workflows/herd-worker.yml",
		HeadBranch:      "main",
		HeadSHA:         "abc123",
		Conclusion:      "success",
		LockKey:         "batch-7",
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})

	logWorkerRunContext("Review", runCtx)

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())

	assert.Contains(t, string(out), "run_id=100")
	assert.Contains(t, string(out), "issue=42")
	assert.Contains(t, string(out), "milestone=7")
	assert.Contains(t, string(out), "batch_branch=herd/batch/7-batch")
	assert.Contains(t, string(out), "worker_branch=herd/worker/42-task")
	assert.Contains(t, string(out), "conclusion=success")
	assert.Contains(t, string(out), "lock_key=batch-7")
	assert.Contains(t, string(out), `workflow="Herd Worker"`)
	assert.Contains(t, string(out), `workflow_path=".github/workflows/herd-worker.yml"`)
	assert.Contains(t, string(out), `head_branch="main"`)
	assert.Contains(t, string(out), "head_sha=abc123")
}

func TestResolveWorkerRunContextErrors(t *testing.T) {
	tests := []struct {
		name    string
		run     *platform.Run
		issue   *platform.Issue
		getErr  error
		wantErr string
	}{
		{
			name:    "missing issue number",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{}},
			wantErr: "run 100 has no issue_number input",
		},
		{
			name:    "non numeric issue number",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{"issue_number": "abc"}},
			wantErr: `invalid issue_number "abc" in run 100`,
		},
		{
			name:    "issue lookup error",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{"issue_number": "42"}},
			getErr:  errors.New("api unavailable"),
			wantErr: "getting issue #42 for run 100: api unavailable",
		},
		{
			name:    "issue without milestone",
			run:     &platform.Run{ID: 100, Inputs: map[string]string{"issue_number": "42"}},
			issue:   &platform.Issue{Number: 42, Title: "No Milestone"},
			wantErr: "issue #42 for run 100 has no milestone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := &mockIssueService{getErr: tt.getErr}
			if tt.issue != nil {
				issues.getResult = map[int]*platform.Issue{tt.issue.Number: tt.issue}
			}
			mock := &mockPlatform{
				workflows: &mockWorkflowService{runs: map[int64]*platform.Run{100: tt.run}},
				issues:    issues,
			}

			got, err := ResolveWorkerRunContext(context.Background(), mock, 100)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
