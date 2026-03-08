package cli

import (
	"context"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestRenderRunners(t *testing.T) {
	mock := &mockStatusPlatform{
		issues:     &mockStatusIssueService{},
		milestones: &mockStatusMilestoneService{},
		workflows:  &mockStatusWorkflowService{},
		runners: &mockRunnerServiceWithData{runners: []*platform.Runner{
			{Name: "worker-1", Status: "online", Labels: []string{"herd-worker"}, Busy: true},
			{Name: "worker-2", Status: "offline", Labels: []string{"herd-worker"}, Busy: false},
		}},
		repo: &mockDispatchRepoService{},
	}

	err := renderRunners(context.Background(), mock)
	require.NoError(t, err)
}

func TestRenderRunnersEmpty(t *testing.T) {
	mock := &mockStatusPlatform{
		issues:     &mockStatusIssueService{},
		milestones: &mockStatusMilestoneService{},
		workflows:  &mockStatusWorkflowService{},
		runners:    &mockRunnerServiceWithData{runners: nil},
		repo:       &mockDispatchRepoService{},
	}

	err := renderRunners(context.Background(), mock)
	require.NoError(t, err)
}

func TestRenderBatchDetail(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 1, Title: "Task A", Labels: []string{issues.StatusDone}, State: "closed"},
			{Number: 2, Title: "Task B", Labels: []string{issues.StatusInProgress}, State: "open"},
		}},
		milestones: &mockMilestoneServiceWithData{milestones: []*platform.Milestone{
			{Number: 1, Title: "Test Batch", State: "open"},
		}},
		workflows: &mockStatusWorkflowService{},
		runners:   &mockStatusRunnerService{},
		repo:      &mockDispatchRepoService{},
	}

	err := renderBatchDetail(context.Background(), mock, 1, false)
	require.NoError(t, err)
}

func TestRenderBatchDetailJSON(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 1, Title: "Task A", Labels: []string{issues.StatusDone}},
		}},
		milestones: &mockMilestoneServiceWithData{milestones: []*platform.Milestone{
			{Number: 1, Title: "Test", State: "open"},
		}},
		workflows: &mockStatusWorkflowService{},
		runners:   &mockStatusRunnerService{},
		repo:      &mockDispatchRepoService{},
	}

	err := renderBatchDetail(context.Background(), mock, 1, true)
	require.NoError(t, err)
}

func TestRenderOverview(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 1, Labels: []string{issues.StatusDone}},
			{Number: 2, Labels: []string{issues.StatusReady}},
		}},
		milestones: &mockMilestoneServiceWithData{milestones: []*platform.Milestone{
			{Number: 1, Title: "Batch 1", State: "open"},
		}},
		workflows: &mockStatusWorkflowService{},
		runners:   &mockStatusRunnerService{},
		repo:      &mockDispatchRepoService{},
	}

	err := renderOverview(context.Background(), mock, false)
	require.NoError(t, err)
}

func TestRunBatchList(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 10, Labels: []string{issues.StatusDone}},
		}},
		milestones: &mockMilestoneServiceWithData{milestones: []*platform.Milestone{
			{Number: 1, Title: "Auth", State: "open"},
			{Number: 2, Title: "Done batch", State: "closed"},
		}},
		workflows: &mockStatusWorkflowService{},
		runners:   &mockStatusRunnerService{},
		repo:      &mockDispatchRepoService{},
	}

	// Without --all, should only show open
	err := runBatchList(context.Background(), mock, false, false)
	require.NoError(t, err)
}

func TestRunBatchListAll(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{}},
		milestones: &mockMilestoneServiceWithData{milestones: []*platform.Milestone{
			{Number: 1, Title: "Auth", State: "open"},
			{Number: 2, Title: "Done", State: "closed"},
		}},
		workflows: &mockStatusWorkflowService{},
		runners:   &mockStatusRunnerService{},
		repo:      &mockDispatchRepoService{},
	}

	err := runBatchList(context.Background(), mock, true, false)
	require.NoError(t, err)
}

func TestRunBatchListJSON(t *testing.T) {
	mock := &mockStatusPlatform{
		issues: &mockStatusIssueService{listResult: []*platform.Issue{
			{Number: 10, Labels: []string{issues.StatusDone}},
		}},
		milestones: &mockMilestoneServiceWithData{milestones: []*platform.Milestone{
			{Number: 1, Title: "Auth", State: "open"},
		}},
		workflows: &mockStatusWorkflowService{},
		runners:   &mockStatusRunnerService{},
		repo:      &mockDispatchRepoService{},
	}

	err := runBatchList(context.Background(), mock, false, true)
	require.NoError(t, err)
}

// Additional mock types

type mockRunnerServiceWithData struct {
	runners []*platform.Runner
}

func (m *mockRunnerServiceWithData) List(_ context.Context) ([]*platform.Runner, error) {
	return m.runners, nil
}
func (m *mockRunnerServiceWithData) Get(_ context.Context, _ int64) (*platform.Runner, error) {
	return nil, nil
}

type mockMilestoneServiceWithData struct {
	milestones []*platform.Milestone
}

func (m *mockMilestoneServiceWithData) List(_ context.Context) ([]*platform.Milestone, error) {
	return m.milestones, nil
}
func (m *mockMilestoneServiceWithData) Get(_ context.Context, n int) (*platform.Milestone, error) {
	for _, ms := range m.milestones {
		if ms.Number == n {
			return ms, nil
		}
	}
	return &platform.Milestone{Number: n, Title: "Test"}, nil
}
func (m *mockMilestoneServiceWithData) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneServiceWithData) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}
