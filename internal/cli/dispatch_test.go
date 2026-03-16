package cli

import (
	"context"
	"fmt"
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

func TestEnsureBatchBranch_AlreadyExists(t *testing.T) {
	mock := newMockPlatformForDispatch()
	err := ensureBatchBranch(context.Background(), mock, "herd/batch/1-test")
	require.NoError(t, err)
	assert.Empty(t, mock.repo.createdBranches, "should not create branch if it already exists")
}

func TestEnsureBatchBranch_CreatesFromDefault(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.repo.branchNotFound = "herd/batch/1-test" // simulate branch not found
	err := ensureBatchBranch(context.Background(), mock, "herd/batch/1-test")
	require.NoError(t, err)
	require.Len(t, mock.repo.createdBranches, 1)
	assert.Equal(t, "herd/batch/1-test", mock.repo.createdBranches[0].name)
	assert.Equal(t, "abc123", mock.repo.createdBranches[0].sha)
}

func TestDispatchSingle_ManualTaskSkipped(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.issues.getResult = &platform.Issue{
		Number: 42,
		Labels: []string{issues.StatusReady, issues.TypeManual},
		Milestone: &platform.Milestone{Number: 1, Title: "Test"},
	}

	cfg := &config.Config{}
	err := runDispatchSingle(context.Background(), mock, cfg, 42, false)
	require.NoError(t, err)

	// Should not have dispatched anything
	assert.Empty(t, mock.workflows.dispatched)
}

func TestDispatchBatch_SkipsManualTasks(t *testing.T) {
	mock := newMockPlatformForDispatch()
	mock.issues.listResult = []*platform.Issue{
		{Number: 1, Title: "Normal", Labels: []string{issues.StatusReady, issues.TypeFeature}, Milestone: &platform.Milestone{Number: 1, Title: "Test"}},
		{Number: 2, Title: "Manual", Labels: []string{issues.StatusReady, issues.TypeManual}, Milestone: &platform.Milestone{Number: 1, Title: "Test"}},
		{Number: 3, Title: "Also normal", Labels: []string{issues.StatusReady, issues.TypeFeature}, Milestone: &platform.Milestone{Number: 1, Title: "Test"}},
	}
	// dispatchIssue will call Get for each issue
	mock.issues.getByNumber = map[int]*platform.Issue{
		1: mock.issues.listResult[0],
		3: mock.issues.listResult[2],
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 10, TimeoutMinutes: 30}}
	err := runDispatchBatch(context.Background(), mock, cfg, 1, true, false)
	require.NoError(t, err)

	// Should have dispatched 2 (skipped the manual one)
	assert.Len(t, mock.workflows.dispatched, 2)
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
func (m *mockDispatchPlatform) Checks() platform.CheckService             { return nil }

// mockDispatchIssueService

type mockDispatchIssueService struct {
	getResult     *platform.Issue
	getByNumber   map[int]*platform.Issue
	listResult    []*platform.Issue
	addedLabels   map[int][]string
	removedLabels map[int][]string
}

func (m *mockDispatchIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockDispatchIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	if m.getByNumber != nil {
		if iss, ok := m.getByNumber[number]; ok {
			return iss, nil
		}
	}
	return m.getResult, nil
}
func (m *mockDispatchIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return m.listResult, nil
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
func (m *mockDispatchIssueService) DeleteComment(_ context.Context, _ int64) error       { return nil }
func (m *mockDispatchIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockDispatchIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

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

type createdBranch struct {
	name string
	sha  string
}

type mockDispatchRepoService struct {
	branchNotFound  string // if set, GetBranchSHA returns error for this branch
	createdBranches []createdBranch
}

func (m *mockDispatchRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: "main"}, nil
}
func (m *mockDispatchRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return "main", nil
}
func (m *mockDispatchRepoService) CreateBranch(_ context.Context, name, sha string) error {
	m.createdBranches = append(m.createdBranches, createdBranch{name, sha})
	return nil
}
func (m *mockDispatchRepoService) DeleteBranch(_ context.Context, _ string) error { return nil }
func (m *mockDispatchRepoService) GetBranchSHA(_ context.Context, branch string) (string, error) {
	if m.branchNotFound != "" && branch == m.branchNotFound {
		return "", fmt.Errorf("branch %s not found", branch)
	}
	return "abc123", nil
}
