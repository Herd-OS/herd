package monitor

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

// --- Mock Platform ---

type mockPlatform struct {
	issues    *mockIssueService
	prs       *mockPRService
	workflows *mockWorkflowService
	repo      *mockRepoService
}

func (m *mockPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService  { return m.prs }
func (m *mockPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockPlatform) Labels() platform.LabelService              { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService      { return nil }
func (m *mockPlatform) Runners() platform.RunnerService            { return nil }
func (m *mockPlatform) Repository() platform.RepositoryService     { return m.repo }

type mockIssueService struct {
	listResults   map[string][]*platform.Issue // keyed by label
	addedLabels   map[int][]string
	removedLabels map[int][]string
	comments      map[int][]string
}

func newMockIssueService() *mockIssueService {
	return &mockIssueService{
		listResults:   make(map[string][]*platform.Issue),
		addedLabels:   make(map[int][]string),
		removedLabels: make(map[int][]string),
		comments:      make(map[int][]string),
	}
}

func (m *mockIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) { return nil, nil }
func (m *mockIssueService) List(_ context.Context, f platform.IssueFilters) ([]*platform.Issue, error) {
	if len(f.Labels) > 0 {
		return m.listResults[f.Labels[0]], nil
	}
	return nil, nil
}
func (m *mockIssueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	m.addedLabels[number] = append(m.addedLabels[number], labels...)
	return nil
}
func (m *mockIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = append(m.removedLabels[number], labels...)
	return nil
}
func (m *mockIssueService) AddComment(_ context.Context, number int, body string) error {
	m.comments[number] = append(m.comments[number], body)
	return nil
}

type mockPRService struct {
	listResult []*platform.PullRequest
	comments   map[int][]string
}

func newMockPRService() *mockPRService {
	return &mockPRService{comments: make(map[int][]string)}
}

func (m *mockPRService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) Get(_ context.Context, _ int) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	return m.listResult, nil
}
func (m *mockPRService) Update(_ context.Context, _ int, _, _ *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, nil
}
func (m *mockPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *mockPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}
func (m *mockPRService) AddComment(_ context.Context, number int, body string) error {
	m.comments[number] = append(m.comments[number], body)
	return nil
}

type mockWorkflowService struct {
	activeRuns    []*platform.Run
	completedRuns []*platform.Run
	dispatched    []map[string]string
	cancelled     []int64
}

func (m *mockWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	m.dispatched = append(m.dispatched, inputs)
	return nil, nil
}
func (m *mockWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *mockWorkflowService) ListRuns(_ context.Context, f platform.RunFilters) ([]*platform.Run, error) {
	if f.Status == "in_progress" {
		return m.activeRuns, nil
	}
	if f.Status == "completed" {
		return m.completedRuns, nil
	}
	return nil, nil
}
func (m *mockWorkflowService) CancelRun(_ context.Context, id int64) error {
	m.cancelled = append(m.cancelled, id)
	return nil
}

type mockRepoService struct {
	defaultBranch string
}

func (m *mockRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (m *mockRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return m.defaultBranch, nil
}
func (m *mockRepoService) CreateBranch(_ context.Context, _, _ string) error   { return nil }
func (m *mockRepoService) DeleteBranch(_ context.Context, _ string) error      { return nil }
func (m *mockRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}

// --- Tests ---

func TestPatrol_NoActiveIssues(t *testing.T) {
	issueSvc := newMockIssueService()
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	result, err := Patrol(context.Background(), mock, &config.Config{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.StaleIssues)
	assert.Equal(t, 0, result.FailedIssues)
}

func TestPatrol_StaleIssue(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusInProgress] = []*platform.Issue{
		{Number: 42, Title: "Test", Labels: []string{issues.StatusInProgress}},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{activeRuns: []*platform.Run{}}, // no active run for #42
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	result, err := Patrol(context.Background(), mock, &config.Config{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.StaleIssues)
	assert.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "HerdOS Monitor Alert")
}

func TestPatrol_FailedIssue_Redispatch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusFailed] = []*platform.Issue{
		{
			Number: 42, Title: "Test", Labels: []string{issues.StatusFailed},
			Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
		},
	}

	wf := &mockWorkflowService{
		completedRuns: []*platform.Run{
			{ID: 100, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Monitor: config.Monitor{AutoRedispatch: true, MaxRedispatchAttempts: 3},
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FailedIssues)
	assert.Equal(t, 1, result.RedispatchedCount)
	assert.Len(t, wf.dispatched, 1)
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusFailed)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusInProgress)
}

func TestPatrol_FailedIssue_BackoffNotElapsed(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusFailed] = []*platform.Issue{
		{
			Number: 42, Title: "Test", Labels: []string{issues.StatusFailed},
			Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
		},
	}

	wf := &mockWorkflowService{
		completedRuns: []*platform.Run{
			// Two failures, last one 5 minutes ago → backoff is 15min, not elapsed
			{ID: 100, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-1 * time.Hour)},
			{ID: 101, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-5 * time.Minute)},
		},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Monitor: config.Monitor{AutoRedispatch: true, MaxRedispatchAttempts: 5},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.RedispatchedCount)
	assert.Len(t, wf.dispatched, 0)
}

func TestPatrol_FailedIssue_MaxAttempts(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusFailed] = []*platform.Issue{
		{
			Number: 42, Title: "Test", Labels: []string{issues.StatusFailed},
			Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
		},
	}

	wf := &mockWorkflowService{
		completedRuns: []*platform.Run{
			{ID: 100, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-3 * time.Hour)},
			{ID: 101, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-2 * time.Hour)},
			{ID: 102, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-1 * time.Hour)},
		},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Monitor: config.Monitor{
			AutoRedispatch:        true,
			MaxRedispatchAttempts: 3,
			NotifyUsers:           []string{"alice"},
		},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.EscalatedCount)
	assert.Equal(t, 0, result.RedispatchedCount)
	assert.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "Max re-dispatch attempts reached")
	assert.Contains(t, issueSvc.comments[42][0], "@alice")
}

func TestPatrol_StuckPR(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", CreatedAt: time.Now().Add(-50 * time.Hour)},
		{Number: 11, Title: "Normal PR", CreatedAt: time.Now().Add(-50 * time.Hour)}, // not a herd PR
	}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Monitor: config.Monitor{MaxPRHAgeHours: 48},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.StuckPRs)
	assert.Len(t, prSvc.comments[10], 1)
	assert.Contains(t, prSvc.comments[10][0], "open for over 48 hours")
	assert.Len(t, prSvc.comments[11], 0) // non-herd PR not flagged
}

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		failures int
		expected time.Duration
	}{
		{1, 0},
		{2, 15 * time.Minute},
		{3, 1 * time.Hour},
		{4, 1 * time.Hour},
		{5, 1 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("failures=%d", tt.failures), func(t *testing.T) {
			assert.Equal(t, tt.expected, BackoffDelay(tt.failures))
		})
	}
}

func TestBuildMentions(t *testing.T) {
	assert.Equal(t, "", buildMentions(nil))
	assert.Equal(t, "", buildMentions([]string{}))
	assert.Equal(t, "/cc @alice", buildMentions([]string{"alice"}))
	assert.Equal(t, "/cc @alice @bob", buildMentions([]string{"alice", "bob"}))
}
