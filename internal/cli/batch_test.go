package cli

import (
	"context"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchCancel(t *testing.T) {
	mock := newMockPlatformForBatchCancel()
	mock.issues.listResult = []*platform.Issue{
		{Number: 1, Labels: []string{issues.StatusInProgress}},
		{Number: 2, Labels: []string{issues.StatusReady}},
		{Number: 3, Labels: []string{issues.StatusBlocked}},
	}
	mock.workflows.runs = []*platform.Run{
		{ID: 100, Status: "in_progress"},
	}
	mock.prs.listResult = []*platform.PullRequest{
		{Number: 10, Head: "herd/batch/1-test", State: "open"},
	}

	err := runBatchCancel(context.Background(), mock, 1, true)
	require.NoError(t, err)

	// Verify runs were cancelled
	assert.Equal(t, []int64{100}, mock.workflows.cancelledRuns)

	// Verify issues were labeled as cancelled
	for _, num := range []int{1, 2, 3} {
		assert.Contains(t, mock.issues.addedLabels[num], issues.StatusCancelled)
	}

	// Verify all issues were closed
	for _, num := range []int{1, 2, 3} {
		require.Contains(t, mock.issues.updatedIssues, num)
		assert.Equal(t, "closed", *mock.issues.updatedIssues[num].State)
	}

	// Verify batch PR was closed
	assert.Equal(t, []int{10}, mock.prs.closedPRs)

	// Verify milestone was closed
	require.NotNil(t, mock.milestones.updatedState)
	assert.Equal(t, "closed", *mock.milestones.updatedState)

	// Verify branch was deleted
	assert.Equal(t, "herd/batch/1-test", mock.repo.deletedBranch)
}

func TestBatchCancel_DoneIssueKeepsLabel(t *testing.T) {
	mock := newMockPlatformForBatchCancel()
	mock.issues.listResult = []*platform.Issue{
		{Number: 1, Labels: []string{issues.StatusDone}},
		{Number: 2, Labels: []string{issues.StatusInProgress}},
	}
	mock.prs.listResult = []*platform.PullRequest{}

	err := runBatchCancel(context.Background(), mock, 1, true)
	require.NoError(t, err)

	// Done issue should NOT get cancelled label
	assert.NotContains(t, mock.issues.addedLabels[1], issues.StatusCancelled)
	// Done issue should NOT have done label removed
	_, removed := mock.issues.removedLabels[1]
	assert.False(t, removed)

	// Non-done issue should get cancelled label
	assert.Contains(t, mock.issues.addedLabels[2], issues.StatusCancelled)

	// Both issues should be closed
	assert.Equal(t, "closed", *mock.issues.updatedIssues[1].State)
	assert.Equal(t, "closed", *mock.issues.updatedIssues[2].State)
}

func TestBatchCancel_ClosesBatchPR(t *testing.T) {
	mock := newMockPlatformForBatchCancel()
	mock.issues.listResult = []*platform.Issue{}
	mock.prs.listResult = []*platform.PullRequest{
		{Number: 10, Head: "herd/batch/1-test", State: "open"},
	}

	err := runBatchCancel(context.Background(), mock, 1, true)
	require.NoError(t, err)

	assert.Equal(t, []int{10}, mock.prs.closedPRs)
}

// --- Mock Platform for batch cancel tests ---

type mockBatchCancelPlatform struct {
	issues     *mockBatchCancelIssueService
	prs        *mockBatchCancelPRService
	workflows  *mockBatchCancelWorkflowService
	milestones *mockBatchCancelMilestoneService
	repo       *mockBatchCancelRepoService
}

func newMockPlatformForBatchCancel() *mockBatchCancelPlatform {
	return &mockBatchCancelPlatform{
		issues:     &mockBatchCancelIssueService{addedLabels: map[int][]string{}, removedLabels: map[int][]string{}, updatedIssues: map[int]platform.IssueUpdate{}},
		prs:        &mockBatchCancelPRService{},
		workflows:  &mockBatchCancelWorkflowService{},
		milestones: &mockBatchCancelMilestoneService{},
		repo:       &mockBatchCancelRepoService{},
	}
}

func (m *mockBatchCancelPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockBatchCancelPlatform) PullRequests() platform.PullRequestService  { return m.prs }
func (m *mockBatchCancelPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockBatchCancelPlatform) Labels() platform.LabelService              { return nil }
func (m *mockBatchCancelPlatform) Milestones() platform.MilestoneService      { return m.milestones }
func (m *mockBatchCancelPlatform) Runners() platform.RunnerService            { return nil }
func (m *mockBatchCancelPlatform) Repository() platform.RepositoryService     { return m.repo }
func (m *mockBatchCancelPlatform) Checks() platform.CheckService             { return nil }

type mockBatchCancelPRService struct {
	listResult []*platform.PullRequest
	closedPRs  []int
}

func (m *mockBatchCancelPRService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockBatchCancelPRService) Get(_ context.Context, _ int) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockBatchCancelPRService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	return m.listResult, nil
}
func (m *mockBatchCancelPRService) Update(_ context.Context, _ int, _, _ *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockBatchCancelPRService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, nil
}
func (m *mockBatchCancelPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *mockBatchCancelPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}
func (m *mockBatchCancelPRService) AddComment(_ context.Context, _ int, _ string) error { return nil }
func (m *mockBatchCancelPRService) GetDiff(_ context.Context, _ int) (string, error) {
	return "", nil
}
func (m *mockBatchCancelPRService) Close(_ context.Context, number int) error {
	m.closedPRs = append(m.closedPRs, number)
	return nil
}

type mockBatchCancelIssueService struct {
	listResult    []*platform.Issue
	addedLabels   map[int][]string
	removedLabels map[int][]string
	updatedIssues map[int]platform.IssueUpdate
}

func (m *mockBatchCancelIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockBatchCancelIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockBatchCancelIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return m.listResult, nil
}
func (m *mockBatchCancelIssueService) Update(_ context.Context, number int, changes platform.IssueUpdate) (*platform.Issue, error) {
	m.updatedIssues[number] = changes
	return nil, nil
}
func (m *mockBatchCancelIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	m.addedLabels[number] = labels
	return nil
}
func (m *mockBatchCancelIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = labels
	return nil
}
func (m *mockBatchCancelIssueService) AddComment(_ context.Context, _ int, _ string) error {
	return nil
}
func (m *mockBatchCancelIssueService) DeleteComment(_ context.Context, _ int64) error { return nil }
func (m *mockBatchCancelIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockBatchCancelIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

type mockBatchCancelWorkflowService struct {
	runs          []*platform.Run
	cancelledRuns []int64
}

func (m *mockBatchCancelWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *mockBatchCancelWorkflowService) Dispatch(_ context.Context, _, _ string, _ map[string]string) (*platform.Run, error) {
	return nil, nil
}
func (m *mockBatchCancelWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *mockBatchCancelWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return m.runs, nil
}
func (m *mockBatchCancelWorkflowService) CancelRun(_ context.Context, id int64) error {
	m.cancelledRuns = append(m.cancelledRuns, id)
	return nil
}

type mockBatchCancelMilestoneService struct {
	updatedState *string
}

func (m *mockBatchCancelMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockBatchCancelMilestoneService) Get(_ context.Context, n int) (*platform.Milestone, error) {
	return &platform.Milestone{Number: n, Title: "Test"}, nil
}
func (m *mockBatchCancelMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *mockBatchCancelMilestoneService) Update(_ context.Context, _ int, changes platform.MilestoneUpdate) (*platform.Milestone, error) {
	m.updatedState = changes.State
	return &platform.Milestone{}, nil
}

type mockBatchCancelRepoService struct {
	deletedBranch string
}

func (m *mockBatchCancelRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) {
	return nil, nil
}
func (m *mockBatchCancelRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return "main", nil
}
func (m *mockBatchCancelRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *mockBatchCancelRepoService) DeleteBranch(_ context.Context, name string) error {
	m.deletedBranch = name
	return nil
}
func (m *mockBatchCancelRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}
