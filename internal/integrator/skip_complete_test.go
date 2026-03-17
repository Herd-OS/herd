package integrator

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsBatchComplete(t *testing.T) {
	tests := []struct {
		name     string
		ms       *platform.Milestone
		expected bool
	}{
		{"nil milestone", nil, false},
		{"open milestone", &platform.Milestone{Number: 1, State: "open"}, false},
		{"closed milestone", &platform.Milestone{Number: 1, State: "closed"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isBatchComplete(tt.ms))
		})
	}
}

func TestConsolidate_SkipsCompletedBatch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch", State: "closed"},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	mock := &mockPlatform{
		issues:     issueSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{
		RunID:    100,
		RepoRoot: dir,
	})

	require.NoError(t, err)
	assert.True(t, result.NoOp)
	assert.False(t, result.Merged)
}

func TestAdvance_SkipsCompletedBatch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch", State: "closed"},
	}
	issueSvc.listResult = []*platform.Issue{}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	result, err := Advance(context.Background(), mock, g, &config.Config{
		Workers: config.Workers{MaxConcurrent: 3},
	}, AdvanceParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.TierComplete)
	assert.False(t, result.AllComplete)
	assert.Equal(t, 0, result.DispatchedCount)
}

func TestAdvanceByBatch_SkipsCompletedBatch(t *testing.T) {
	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch", State: "closed"},
		},
	}

	mock := &mockPlatform{
		issues:     newMockIssueService(),
		prs:        &mockPRService{},
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: msSvc,
	}

	_, g := initTestRepo(t)
	result, err := AdvanceByBatch(context.Background(), mock, g, &config.Config{
		Workers: config.Workers{MaxConcurrent: 3},
	}, 1)

	require.NoError(t, err)
	assert.False(t, result.TierComplete)
	assert.Equal(t, 0, result.DispatchedCount)
}

func TestCheckCI_SkipsCompletedBatch(t *testing.T) {
	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch", State: "closed"},
		},
	}

	mock := &mockPlatform{
		issues:     newMockIssueService(),
		prs:        &mockPRService{},
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: msSvc,
	}

	result, err := CheckCI(context.Background(), mock, &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}, CheckCIParams{BatchNumber: 1})

	require.NoError(t, err)
	assert.True(t, result.Skipped)
}
