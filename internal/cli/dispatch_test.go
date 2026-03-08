package cli

import (
	"context"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchIssue(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.issues.getResult = &platform.Issue{
		Number: 42,
		Labels: []string{issues.StatusReady, issues.TypeFeature},
		Milestone: &platform.Milestone{Number: 1, Title: "Test"},
	}

	cfg := &config.Config{
		Workers: config.Workers{
			MaxConcurrent:  3,
			TimeoutMinutes: 30,
			RunnerLabel:    "herd-worker",
		},
	}

	err := dispatchIssue(context.Background(), mock, cfg, 42, "herd/batch/1-test")
	require.NoError(t, err)

	// Verify labels were updated
	assert.Equal(t, []string{issues.StatusReady}, mock.issues.removedLabels[42])
	assert.Equal(t, []string{issues.StatusInProgress}, mock.issues.addedLabels[42])

	// Verify workflow was dispatched
	require.Len(t, mock.workflows.dispatched, 1)
	assert.Equal(t, "herd-worker.yml", mock.workflows.dispatched[0].file)
	assert.Equal(t, "42", mock.workflows.dispatched[0].inputs["issue_number"])
	assert.Equal(t, "herd/batch/1-test", mock.workflows.dispatched[0].inputs["batch_branch"])
}

func TestDispatchIssue_WrongState(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.issues.getResult = &platform.Issue{
		Number: 42,
		Labels: []string{issues.StatusInProgress},
	}

	cfg := &config.Config{}
	err := dispatchIssue(context.Background(), mock, cfg, 42, "herd/batch/1-test")
	assert.ErrorContains(t, err, "expected ready or failed")
}

func TestDispatchIssue_FailedIssueCanBeDispatched(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.issues.getResult = &platform.Issue{
		Number: 42,
		Labels: []string{issues.StatusFailed, issues.TypeFeature},
		Milestone: &platform.Milestone{Number: 1, Title: "Test"},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30}}
	err := dispatchIssue(context.Background(), mock, cfg, 42, "herd/batch/1-test")
	require.NoError(t, err)

	assert.Equal(t, []string{issues.StatusFailed}, mock.issues.removedLabels[42])
	assert.Equal(t, []string{issues.StatusInProgress}, mock.issues.addedLabels[42])
}

func TestCountActiveWorkers(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.workflows.runs = []*platform.Run{
		{ID: 1, Status: "in_progress"},
		{ID: 2, Status: "in_progress"},
	}

	count, err := countActiveWorkers(context.Background(), mock)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

// --- Mock Platform for dispatch tests ---

type mockDispatchPlatform struct {
	issues     *mockDispatchIssueService
	workflows  *mockDispatchWorkflowService
	milestones *mockDispatchMilestoneService
	repo       *mockDispatchRepoService
}

func newMockPlatformForDispatch() *mockDispatchPlatform {
	return &mockDispatchPlatform{
		issues:     &mockDispatchIssueService{addedLabels: map[int][]string{}, removedLabels: map[int][]string{}},
		workflows:  &mockDispatchWorkflowService{},
		milestones: &mockDispatchMilestoneService{},
		repo:       &mockDispatchRepoService{},
	}
}

func (m *mockDispatchPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockDispatchPlatform) PullRequests() platform.PullRequestService  { return nil }
func (m *mockDispatchPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockDispatchPlatform) Labels() platform.LabelService              { return nil }
func (m *mockDispatchPlatform) Milestones() platform.MilestoneService      { return m.milestones }
func (m *mockDispatchPlatform) Runners() platform.RunnerService            { return nil }
func (m *mockDispatchPlatform) Repository() platform.RepositoryService     { return m.repo }

// mockDispatchIssueService

type mockDispatchIssueService struct {
	getResult     *platform.Issue
	addedLabels   map[int][]string
	removedLabels map[int][]string
}

func (m *mockDispatchIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockDispatchIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) {
	return m.getResult, nil
}
func (m *mockDispatchIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return nil, nil
}
func (m *mockDispatchIssueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockDispatchIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	m.addedLabels[number] = labels
	return nil
}
func (m *mockDispatchIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = labels
	return nil
}
func (m *mockDispatchIssueService) AddComment(_ context.Context, _ int, _ string) error { return nil }

// mockDispatchWorkflowService

type dispatchedWorkflow struct {
	file   string
	ref    string
	inputs map[string]string
}

type mockDispatchWorkflowService struct {
	dispatched []dispatchedWorkflow
	runs       []*platform.Run
}

func (m *mockDispatchWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 1, nil
}
func (m *mockDispatchWorkflowService) Dispatch(_ context.Context, file, ref string, inputs map[string]string) (*platform.Run, error) {
	m.dispatched = append(m.dispatched, dispatchedWorkflow{file, ref, inputs})
	return nil, nil
}
func (m *mockDispatchWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *mockDispatchWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return m.runs, nil
}
func (m *mockDispatchWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

// mockDispatchMilestoneService

type mockDispatchMilestoneService struct{}

func (m *mockDispatchMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockDispatchMilestoneService) Get(_ context.Context, _ int) (*platform.Milestone, error) {
	return &platform.Milestone{Number: 1, Title: "Test"}, nil
}
func (m *mockDispatchMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *mockDispatchMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

// mockDispatchRepoService

type mockDispatchRepoService struct{}

func (m *mockDispatchRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: "main"}, nil
}
func (m *mockDispatchRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return "main", nil
}
func (m *mockDispatchRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *mockDispatchRepoService) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (m *mockDispatchRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}
