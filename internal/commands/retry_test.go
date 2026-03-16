package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleRetry(t *testing.T) {
	defaultCfg := &config.Config{
		Workers: config.Workers{
			TimeoutMinutes: 30,
			RunnerLabel:    "herd-worker",
		},
	}

	tests := []struct {
		name           string
		args           string
		setupMock      func(*mockRetryPlatform)
		wantMsg        string
		wantErr        string
		wantDispatched int
	}{
		{
			name:    "empty args returns usage error",
			args:    "",
			wantErr: "Usage: `/herd retry <issue-number>`",
		},
		{
			name:    "whitespace-only args returns usage error",
			args:    "   ",
			wantErr: "Usage: `/herd retry <issue-number>`",
		},
		{
			name:    "non-numeric args returns usage error",
			args:    "abc",
			wantErr: "Usage: `/herd retry <issue-number>`",
		},
		{
			name:    "zero issue number returns usage error",
			args:    "0",
			wantErr: "Usage: `/herd retry <issue-number>`",
		},
		{
			name:    "negative issue number returns usage error",
			args:    "-1",
			wantErr: "Usage: `/herd retry <issue-number>`",
		},
		{
			name: "get issue fails returns error",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getErr = fmt.Errorf("not found")
			},
			wantErr: "getting issue #42",
		},
		{
			name: "issue not in failed state returns error",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getResult = &platform.Issue{
					Number: 42,
					Labels: []string{issues.StatusInProgress},
				}
			},
			wantErr: "Issue #42 is not in failed state (current: herd/status:in-progress)",
		},
		{
			name: "issue with ready status returns error",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getResult = &platform.Issue{
					Number: 42,
					Labels: []string{issues.StatusReady},
				}
			},
			wantErr: "Issue #42 is not in failed state (current: herd/status:ready)",
		},
		{
			name: "issue with no status returns error",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getResult = &platform.Issue{
					Number: 42,
					Labels: []string{"some-other-label"},
				}
			},
			wantErr: "Issue #42 is not in failed state (current: )",
		},
		{
			name: "issue with no milestone returns error",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getResult = &platform.Issue{
					Number:    42,
					Labels:    []string{issues.StatusFailed},
					Milestone: nil,
				}
			},
			wantErr: "Issue #42 has no milestone",
		},
		{
			name: "dispatch failure returns error",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getResult = &platform.Issue{
					Number:    42,
					Labels:    []string{issues.StatusFailed},
					Milestone: &platform.Milestone{Number: 5, Title: "My Batch"},
				}
				m.workflows.dispatchErr = fmt.Errorf("workflow trigger failed")
			},
			wantErr:        "dispatching workflow",
			wantDispatched: 0,
		},
		{
			name: "successful retry returns correct message",
			args: "42",
			setupMock: func(m *mockRetryPlatform) {
				m.issues.getResult = &platform.Issue{
					Number:    42,
					Labels:    []string{issues.StatusFailed, issues.TypeFeature},
					Milestone: &platform.Milestone{Number: 5, Title: "My Batch"},
				}
			},
			wantMsg:        "🔄 Redispatching worker for issue #42.",
			wantDispatched: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockRetryPlatform()
			if tc.setupMock != nil {
				tc.setupMock(mock)
			}

			hctx := &HandlerContext{
				Platform: mock,
				Config:      defaultCfg,
			}
			cmd := &Command{Name: "retry", Args: tc.args}

			msg, err := handleRetry(context.Background(), hctx, cmd)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Empty(t, msg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantMsg, msg)
			}

			assert.Len(t, mock.workflows.dispatched, tc.wantDispatched)
		})
	}
}

func TestHandleRetry_LabelsUpdatedOnSuccess(t *testing.T) {
	mock := newMockRetryPlatform()
	mock.issues.getResult = &platform.Issue{
		Number:    42,
		Labels:    []string{issues.StatusFailed, issues.TypeFeature},
		Milestone: &platform.Milestone{Number: 5, Title: "My Batch"},
	}

	hctx := &HandlerContext{
		Platform: mock,
		Config:      &config.Config{Workers: config.Workers{TimeoutMinutes: 30}},
	}
	_, err := handleRetry(context.Background(), hctx, &Command{Name: "retry", Args: "42"})
	require.NoError(t, err)

	assert.Equal(t, []string{issues.StatusFailed}, mock.issues.removedLabels[42])
	assert.Equal(t, []string{issues.StatusInProgress}, mock.issues.addedLabels[42])
}

func TestHandleRetry_DispatchInputs(t *testing.T) {
	mock := newMockRetryPlatform()
	mock.issues.getResult = &platform.Issue{
		Number:    42,
		Labels:    []string{issues.StatusFailed},
		Milestone: &platform.Milestone{Number: 7, Title: "Feature X"},
	}

	cfg := &config.Config{
		Workers: config.Workers{
			TimeoutMinutes: 45,
			RunnerLabel:    "my-runner",
		},
	}
	hctx := &HandlerContext{Platform: mock, Config: cfg}
	cmd := &Command{Name: "retry", Args: "42"}

	_, err := handleRetry(context.Background(), hctx, cmd)
	require.NoError(t, err)

	require.Len(t, mock.workflows.dispatched, 1)
	d := mock.workflows.dispatched[0]
	assert.Equal(t, "herd-worker.yml", d.file)
	assert.Equal(t, "main", d.ref)
	assert.Equal(t, "42", d.inputs["issue_number"])
	assert.Equal(t, "herd/batch/7-feature-x", d.inputs["batch_branch"])
	assert.Equal(t, "45", d.inputs["timeout_minutes"])
	assert.Equal(t, "my-runner", d.inputs["runner_label"])
}

func TestHandleRetry_DispatchFailureRevertsLabels(t *testing.T) {
	mock := newMockRetryPlatform()
	mock.issues.getResult = &platform.Issue{
		Number:    10,
		Labels:    []string{issues.StatusFailed},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch One"},
	}
	mock.workflows.dispatchErr = fmt.Errorf("API error")

	hctx := &HandlerContext{
		Platform: mock,
		Config:      &config.Config{},
	}
	cmd := &Command{Name: "retry", Args: "10"}

	_, err := handleRetry(context.Background(), hctx, cmd)
	require.Error(t, err)

	// in-progress label removed on revert, failed label added back
	assert.Contains(t, mock.issues.removedLabels[10], issues.StatusInProgress)
	assert.Contains(t, mock.issues.addedLabels[10], issues.StatusFailed)
}

func TestHandleRetry_BatchBranchSlug(t *testing.T) {
	tests := []struct {
		milestoneTitle  string
		wantBatchBranch string
	}{
		{"Add JWT Auth", "herd/batch/3-add-jwt-auth"},
		{"My Batch", "herd/batch/1-my-batch"},
		{"Feature: New UI", "herd/batch/2-feature-new-ui"},
	}

	for _, tc := range tests {
		t.Run(tc.milestoneTitle, func(t *testing.T) {
			mock := newMockRetryPlatform()
			msNum := extractMilestoneNum(tc.wantBatchBranch)
			mock.issues.getResult = &platform.Issue{
				Number:    5,
				Labels:    []string{issues.StatusFailed},
				Milestone: &platform.Milestone{Number: msNum, Title: tc.milestoneTitle},
			}

			hctx := &HandlerContext{
				Platform: mock,
				Config:      &config.Config{},
			}
			_, err := handleRetry(context.Background(), hctx, &Command{Name: "retry", Args: "5"})
			require.NoError(t, err)

			require.Len(t, mock.workflows.dispatched, 1)
			assert.Equal(t, tc.wantBatchBranch, mock.workflows.dispatched[0].inputs["batch_branch"])
		})
	}
}

func TestRetryRegistered(t *testing.T) {
	_, exists := Registry["retry"]
	assert.True(t, exists, "retry command should be registered via init()")
}

// extractMilestoneNum parses the milestone number from a batch branch name like "herd/batch/3-add-jwt-auth".
func extractMilestoneNum(branch string) int {
	var n int
	fmt.Sscanf(branch, "herd/batch/%d-", &n)
	return n
}

// --- Mock Platform for retry tests ---

type mockRetryPlatform struct {
	issues    *mockRetryIssueService
	workflows *mockRetryWorkflowService
	repo      *mockRetryRepoService
}

func newMockRetryPlatform() *mockRetryPlatform {
	return &mockRetryPlatform{
		issues:    &mockRetryIssueService{addedLabels: map[int][]string{}, removedLabels: map[int][]string{}},
		workflows: &mockRetryWorkflowService{},
		repo:      &mockRetryRepoService{},
	}
}

func (m *mockRetryPlatform) Issues() platform.IssueService            { return m.issues }
func (m *mockRetryPlatform) PullRequests() platform.PullRequestService { return nil }
func (m *mockRetryPlatform) Workflows() platform.WorkflowService       { return m.workflows }
func (m *mockRetryPlatform) Labels() platform.LabelService             { return nil }
func (m *mockRetryPlatform) Milestones() platform.MilestoneService     { return nil }
func (m *mockRetryPlatform) Runners() platform.RunnerService           { return nil }
func (m *mockRetryPlatform) Repository() platform.RepositoryService    { return m.repo }
func (m *mockRetryPlatform) Checks() platform.CheckService             { return nil }

// mockRetryIssueService

type mockRetryIssueService struct {
	getResult     *platform.Issue
	getErr        error
	addedLabels   map[int][]string
	removedLabels map[int][]string
}

func (m *mockRetryIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockRetryIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) {
	return m.getResult, m.getErr
}
func (m *mockRetryIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return nil, nil
}
func (m *mockRetryIssueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockRetryIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	m.addedLabels[number] = append(m.addedLabels[number], labels...)
	return nil
}
func (m *mockRetryIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = append(m.removedLabels[number], labels...)
	return nil
}
func (m *mockRetryIssueService) AddComment(_ context.Context, _ int, _ string) error { return nil }
func (m *mockRetryIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockRetryIssueService) CreateReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

// mockRetryWorkflowService

type retryDispatch struct {
	file   string
	ref    string
	inputs map[string]string
}

type mockRetryWorkflowService struct {
	dispatched  []retryDispatch
	dispatchErr error
}

func (m *mockRetryWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 1, nil
}
func (m *mockRetryWorkflowService) Dispatch(_ context.Context, file, ref string, inputs map[string]string) (*platform.Run, error) {
	if m.dispatchErr != nil {
		return nil, m.dispatchErr
	}
	m.dispatched = append(m.dispatched, retryDispatch{file, ref, inputs})
	return &platform.Run{ID: 1}, nil
}
func (m *mockRetryWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *mockRetryWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return nil, nil
}
func (m *mockRetryWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

// mockRetryRepoService

type mockRetryRepoService struct{}

func (m *mockRetryRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: "main"}, nil
}
func (m *mockRetryRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return "main", nil
}
func (m *mockRetryRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *mockRetryRepoService) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (m *mockRetryRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}
