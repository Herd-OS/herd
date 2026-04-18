package commands

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleDispatch_ReadyIssue(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusReady, issues.TypeFeature},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	wf := &testWorkflowService{}
	mock := &testPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo:      &testRepoService{defaultBranch: "main"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    mock,
		Config:      &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}},
		IssueNumber: 10,
	}

	result := handleDispatch(hctx, Command{Name: "dispatch"})
	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "Dispatched worker for issue #10")
	assert.Contains(t, issueSvc.removedLabels[10], issues.StatusReady)
	assert.Contains(t, issueSvc.addedLabels[10], issues.StatusInProgress)
	assert.Len(t, wf.dispatched, 1)
}

func TestHandleDispatch_BlockedIssue(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusBlocked, issues.TypeFeature},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	wf := &testWorkflowService{}
	mock := &testPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo:      &testRepoService{defaultBranch: "main"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    mock,
		Config:      &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}},
		IssueNumber: 10,
	}

	result := handleDispatch(hctx, Command{Name: "dispatch"})
	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "Dispatched worker for issue #10")
	assert.Contains(t, issueSvc.removedLabels[10], issues.StatusBlocked)
	assert.Contains(t, issueSvc.addedLabels[10], issues.StatusInProgress)
}

func TestHandleDispatch_WithExplicitIssueNumber(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.getResult[20] = &platform.Issue{
		Number: 20, Title: "Task B",
		Labels:    []string{issues.StatusReady, issues.TypeFeature},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	wf := &testWorkflowService{}
	mock := &testPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo:      &testRepoService{defaultBranch: "main"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    mock,
		Config:      &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}},
		IssueNumber: 5,
	}

	result := handleDispatch(hctx, Command{Name: "dispatch", Args: []string{"20"}})
	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "Dispatched worker for issue #20")
}

func TestHandleDispatch_RejectsDoneIssue(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	mock := &testPlatform{
		issues: issueSvc,
		repo:   &testRepoService{defaultBranch: "main"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    mock,
		Config:      &config.Config{},
		IssueNumber: 10,
	}

	result := handleDispatch(hctx, Command{Name: "dispatch"})
	assert.Contains(t, result.Message, "not ready or blocked")
}

func TestHandleDispatch_RejectsPRWithoutArgs(t *testing.T) {
	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Config:      &config.Config{},
		IsPR:        true,
		IssueNumber: 50,
	}

	result := handleDispatch(hctx, Command{Name: "dispatch"})
	assert.Contains(t, result.Message, "requires an issue number")
}
