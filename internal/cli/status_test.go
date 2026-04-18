package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetBatchStatus(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 1, Labels: []string{issues.StatusDone}},
			{Number: 2, Labels: []string{issues.StatusDone}},
			{Number: 3, Labels: []string{issues.StatusInProgress}},
			{Number: 4, Labels: []string{issues.StatusBlocked}},
			{Number: 5, Labels: []string{issues.StatusReady}},
		}},
		milestones: &mockStatusMilestoneService{},
		workflows:  &mockStatusWorkflowService{},
		runners:    &mockStatusRunnerService{},
		repo:       &mockDispatchRepoService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Test Batch"}
	bs, err := getBatchStatus(context.Background(), mock, ms)
	require.NoError(t, err)
	assert.Equal(t, 1, bs.Number)
	assert.Equal(t, "Test Batch", bs.Title)
	assert.Equal(t, 5, bs.Total)
	assert.Equal(t, 2, bs.Done)
	assert.Equal(t, 1, bs.Active)
	assert.Equal(t, 1, bs.Blocked)
	assert.Equal(t, 1, bs.Ready)
	assert.Equal(t, 0, bs.Failed)
}

func TestStatusOutputJSON(t *testing.T) {
	out := StatusOutput{
		Batches: []BatchStatus{
			{Number: 1, Title: "Test", Total: 3, Done: 2},
		},
		Workers: []WorkerRun{
			{RunID: 123, Status: "in_progress"},
		},
	}

	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)

	var parsed StatusOutput
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, out, parsed)
}

func TestBatchStatusAllDone(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 1, Labels: []string{issues.StatusDone}},
			{Number: 2, Labels: []string{issues.StatusDone}},
		}},
		milestones: &mockStatusMilestoneService{},
		workflows:  &mockStatusWorkflowService{},
		runners:    &mockStatusRunnerService{},
		repo:       &mockDispatchRepoService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Complete"}
	bs, err := getBatchStatus(context.Background(), mock, ms)
	require.NoError(t, err)
	assert.Equal(t, 2, bs.Done)
	assert.Equal(t, 2, bs.Total)
}

func TestBatchStatusWithFailures(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 1, Labels: []string{issues.StatusDone}},
			{Number: 2, Labels: []string{issues.StatusFailed}},
			{Number: 3, Labels: []string{issues.StatusFailed}},
		}},
		milestones: &mockStatusMilestoneService{},
		workflows:  &mockStatusWorkflowService{},
		runners:    &mockStatusRunnerService{},
		repo:       &mockDispatchRepoService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Failing"}
	bs, err := getBatchStatus(context.Background(), mock, ms)
	require.NoError(t, err)
	assert.Equal(t, 2, bs.Failed)
	assert.Equal(t, 1, bs.Done)
}

// --- Mock Platform for status tests ---

type mockStatusPlatform struct {
	issues     platform.IssueService
	milestones platform.MilestoneService
	workflows  platform.WorkflowService
	runners    platform.RunnerService
	repo       platform.RepositoryService
}

func (m *mockStatusPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockStatusPlatform) PullRequests() platform.PullRequestService  { return nil }
func (m *mockStatusPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockStatusPlatform) Labels() platform.LabelService              { return nil }
func (m *mockStatusPlatform) Milestones() platform.MilestoneService      { return m.milestones }
func (m *mockStatusPlatform) Runners() platform.RunnerService            { return m.runners }
func (m *mockStatusPlatform) Repository() platform.RepositoryService     { return m.repo }
func (m *mockStatusPlatform) Checks() platform.CheckService             { return nil }

type mockStatusIssueService struct {
	listResult []*platform.Issue
}

func (m *mockStatusIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockStatusIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockStatusIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return m.listResult, nil
}
func (m *mockStatusIssueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockStatusIssueService) AddLabels(_ context.Context, _ int, _ []string) error    { return nil }
func (m *mockStatusIssueService) RemoveLabels(_ context.Context, _ int, _ []string) error { return nil }
func (m *mockStatusIssueService) AddComment(_ context.Context, _ int, _ string) error { return nil }
func (m *mockStatusIssueService) AddCommentReturningID(_ context.Context, _ int, _ string) (int64, error) {
	return 0, nil
}
func (m *mockStatusIssueService) UpdateComment(_ context.Context, _ int64, _ string) error { return nil }
func (m *mockStatusIssueService) DeleteComment(_ context.Context, _ int64) error           { return nil }
func (m *mockStatusIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockStatusIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

type mockStatusMilestoneService struct{}

func (m *mockStatusMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockStatusMilestoneService) Get(_ context.Context, n int) (*platform.Milestone, error) {
	return &platform.Milestone{Number: n, Title: "Test"}, nil
}
func (m *mockStatusMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *mockStatusMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

type mockStatusWorkflowService struct{}

func (m *mockStatusWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *mockStatusWorkflowService) Dispatch(_ context.Context, _, _ string, _ map[string]string) (*platform.Run, error) {
	return nil, nil
}
func (m *mockStatusWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *mockStatusWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return nil, nil
}
func (m *mockStatusWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

type mockStatusRunnerService struct{}

func (m *mockStatusRunnerService) List(_ context.Context) ([]*platform.Runner, error) {
	return nil, nil
}
func (m *mockStatusRunnerService) Get(_ context.Context, _ int64) (*platform.Runner, error) {
	return nil, nil
}
