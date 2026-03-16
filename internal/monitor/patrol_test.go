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
	checks    *mockCheckService
}

func (m *mockPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService  { return m.prs }
func (m *mockPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockPlatform) Labels() platform.LabelService              { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService      { return nil }
func (m *mockPlatform) Runners() platform.RunnerService            { return nil }
func (m *mockPlatform) Repository() platform.RepositoryService     { return m.repo }
func (m *mockPlatform) Checks() platform.CheckService {
	if m.checks != nil {
		return m.checks
	}
	return &mockCheckService{status: "success"}
}

type mockCheckService struct {
	status string
}

func (m *mockCheckService) GetCombinedStatus(_ context.Context, _ string) (string, error) {
	return m.status, nil
}
func (m *mockCheckService) RerunFailedChecks(_ context.Context, _ string) error {
	return nil
}

type mockIssueService struct {
	listResults      map[string][]*platform.Issue // keyed by label
	getResults       map[int]*platform.Issue      // for Get by number
	getErr           error
	addedLabels      map[int][]string
	removedLabels    map[int][]string
	addLabelsErr     error
	comments         map[int][]string
	existingComments map[int][]*platform.Comment // for ListComments
	listCommentsErr  error
	deletedComments  []int64
}

func newMockIssueService() *mockIssueService {
	return &mockIssueService{
		listResults:   make(map[string][]*platform.Issue),
		getResults:    make(map[int]*platform.Issue),
		addedLabels:   make(map[int][]string),
		removedLabels: make(map[int][]string),
		comments:      make(map[int][]string),
	}
}

func (m *mockIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if issue, ok := m.getResults[number]; ok {
		return issue, nil
	}
	return &platform.Issue{Number: number}, nil
}
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
	if m.addLabelsErr != nil {
		return m.addLabelsErr
	}
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
func (m *mockIssueService) DeleteComment(_ context.Context, commentID int64) error {
	m.deletedComments = append(m.deletedComments, commentID)
	return nil
}
func (m *mockIssueService) ListComments(_ context.Context, number int) ([]*platform.Comment, error) {
	if m.listCommentsErr != nil {
		return nil, m.listCommentsErr
	}
	return m.existingComments[number], nil
}
func (m *mockIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
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
	// Should relabel to failed
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusInProgress)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestPatrol_TimeoutCancellation(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusInProgress] = []*platform.Issue{
		{Number: 42, Title: "Test", Labels: []string{issues.StatusInProgress}},
	}

	wf := &mockWorkflowService{
		activeRuns: []*platform.Run{
			{ID: 200, Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Workers: config.Workers{TimeoutMinutes: 60},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.StaleIssues)
	// Should cancel the run
	assert.Contains(t, wf.cancelled, int64(200))
	// Should label as failed
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusInProgress)
	// Should comment
	assert.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "exceeded timeout")
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
	assert.Len(t, wf.dispatched, 0) // monitor no longer dispatches directly
	assert.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "/herd retry 42")
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
	assert.Len(t, issueSvc.comments[42], 0)
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

func TestPatrol_CIFailureOnBatchPR(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "failure"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.CIFailures)
	assert.Len(t, prSvc.comments[10], 1)
	assert.Contains(t, prSvc.comments[10][0], "/herd fix-ci")
}

func TestPatrol_CIFailureWithExistingFixCIComment_NoDuplicateCommand(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// Simulate /herd fix-ci comment already present from a prior patrol cycle.
	issueSvc.existingComments = map[int][]*platform.Comment{
		10: {{ID: 1, Body: "/herd fix-ci"}},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "failure"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.CIFailures)
	// No new /herd fix-ci comment should be posted.
	assert.Len(t, prSvc.comments[10], 0)
}

func TestPatrol_CIPassingNoCIFixComment(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "success"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.CIFailures)
	assert.Len(t, prSvc.comments[10], 0)
}

func TestPatrol_CIPendingNoFixCIComment(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "pending"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.CIFailures)
	// No /herd fix-ci comment must be posted when CI is pending.
	assert.Len(t, prSvc.comments[10], 0)
}

func TestPatrol_CIPassingOnBatchPR(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "success"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.CIFailures)
	assert.Len(t, prSvc.comments[10], 0)
}

func TestPatrol_CIPassingDeletesExistingFixCIComment(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// A /herd fix-ci comment exists from a prior fix cycle.
	issueSvc.existingComments = map[int][]*platform.Comment{
		10: {{ID: 42, Body: "/herd fix-ci"}},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "success"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.CIFailures)
	// The stale /herd fix-ci comment must be deleted to allow future re-triggering.
	assert.Contains(t, issueSvc.deletedComments, int64(42))
}

func TestPatrol_CIPassingNoFixCICommentNoDeleteCalled(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// No existing fix-ci comment.

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "success"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.CIFailures)
	assert.Empty(t, issueSvc.deletedComments)
}

func TestPatrol_CIFailureAfterFixCICommentDeleted(t *testing.T) {
	// Regression test: after CI passes (comment deleted), a new CI failure
	// must post a fresh /herd fix-ci comment.
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// No existing comment — simulates state after a prior pass deleted it.

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "failure"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: true},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.CIFailures)
	assert.Len(t, prSvc.comments[10], 1)
	assert.Contains(t, prSvc.comments[10][0], "/herd fix-ci")
}

func TestPatrol_CINotCheckedWhenRequireCIFalse(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	mock := &mockPlatform{
		issues:    newMockIssueService(),
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		checks:    &mockCheckService{status: "failure"},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{RequireCI: false},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.CIFailures)
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

func TestHasMonitorComment(t *testing.T) {
	tests := []struct {
		name     string
		comments []*platform.Comment
		expected bool
	}{
		{
			"no comments",
			nil,
			false,
		},
		{
			"unrelated comment",
			[]*platform.Comment{{ID: 1, Body: "This looks good!"}},
			false,
		},
		{
			"monitor comment exists",
			[]*platform.Comment{
				{ID: 1, Body: "Nice work"},
				{ID: 2, Body: "⚠️ **HerdOS Monitor Alert**\n\nIssue #42 has been stale."},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			if tt.comments != nil {
				issueSvc.existingComments = map[int][]*platform.Comment{42: tt.comments}
			}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: &mockWorkflowService{},
				repo:      &mockRepoService{defaultBranch: "main"},
			}
			assert.Equal(t, tt.expected, hasMonitorComment(context.Background(), mock, 42))
		})
	}
}

func TestHasMonitorComment_ErrorFallback(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listCommentsErr = fmt.Errorf("API error")
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	// Should return false (fail open) when ListComments errors
	assert.False(t, hasMonitorComment(context.Background(), mock, 42))
}

func TestHasCIFixComment(t *testing.T) {
	tests := []struct {
		name     string
		comments []*platform.Comment
		expected bool
	}{
		{
			name:     "no comments",
			comments: nil,
			expected: false,
		},
		{
			name:     "unrelated comment only",
			comments: []*platform.Comment{{ID: 1, Body: "looks good!"}},
			expected: false,
		},
		{
			name:     "fix-ci comment present",
			comments: []*platform.Comment{{ID: 1, Body: "/herd fix-ci"}},
			expected: true,
		},
		{
			name:     "fix-ci comment with surrounding whitespace",
			comments: []*platform.Comment{{ID: 1, Body: "  /herd fix-ci\n"}},
			expected: true,
		},
		{
			name:     "fix-ci comment among others",
			comments: []*platform.Comment{
				{ID: 1, Body: "some other comment"},
				{ID: 2, Body: "/herd fix-ci"},
			},
			expected: true,
		},
		{
			name:     "mention of command in prose should not match",
			comments: []*platform.Comment{{ID: 1, Body: "I tried `/herd fix-ci` but nothing happened"}},
			expected: false,
		},
		{
			name:     "command embedded mid-sentence should not match",
			comments: []*platform.Comment{{ID: 1, Body: "running /herd fix-ci now"}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			if tt.comments != nil {
				issueSvc.existingComments = map[int][]*platform.Comment{10: tt.comments}
			}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: &mockWorkflowService{},
				repo:      &mockRepoService{defaultBranch: "main"},
			}
			assert.Equal(t, tt.expected, hasCIFixComment(context.Background(), mock, 10))
		})
	}
}

func TestHasCIFixComment_ErrorFallback(t *testing.T) {
	// When ListComments returns an error, hasCIFixComment should fail open (return false).
	issueSvc := newMockIssueService()
	issueSvc.listCommentsErr = fmt.Errorf("API error")
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	assert.False(t, hasCIFixComment(context.Background(), mock, 10))
}

func TestPatrol_NoDuplicateComments(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusInProgress] = []*platform.Issue{
		{Number: 42, Title: "Test", Labels: []string{issues.StatusInProgress}},
	}
	// Simulate existing monitor comment on issue 42
	issueSvc.existingComments = map[int][]*platform.Comment{
		42: {{ID: 1, Body: "⚠️ **HerdOS Monitor Alert**\n\nIssue #42 has been in-progress with no active workflow run."}},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{activeRuns: []*platform.Run{}},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	result, err := Patrol(context.Background(), mock, &config.Config{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.StaleIssues)
	// No new comment should be posted since one already exists
	assert.Len(t, issueSvc.comments[42], 0)
}

func TestPatrol_StaleIssueRelabeled(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusInProgress] = []*platform.Issue{
		{Number: 42, Title: "Test", Labels: []string{issues.StatusInProgress}},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{activeRuns: []*platform.Run{}},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	result, err := Patrol(context.Background(), mock, &config.Config{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.StaleIssues)
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusInProgress)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestPatrol_StaleIssueRedispatchedNextCycle(t *testing.T) {
	// First patrol: stale issue gets relabeled to failed
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusInProgress] = []*platform.Issue{
		{Number: 42, Title: "Test", Labels: []string{issues.StatusInProgress},
			Milestone: &platform.Milestone{Number: 1, Title: "Batch"}},
	}

	wf := &mockWorkflowService{activeRuns: []*platform.Run{}}
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
	assert.Equal(t, 1, result.StaleIssues)
	assert.Equal(t, 0, result.RedispatchedCount) // Not redispatched in same cycle
	assert.Len(t, wf.dispatched, 0)

	// Second patrol: issue is now failed, should be redispatched
	issueSvc2 := newMockIssueService()
	issueSvc2.listResults[issues.StatusFailed] = []*platform.Issue{
		{Number: 42, Title: "Test", Labels: []string{issues.StatusFailed},
			Milestone: &platform.Milestone{Number: 1, Title: "Batch"}},
	}
	// Simulate one past failure run
	wf2 := &mockWorkflowService{
		completedRuns: []*platform.Run{
			{ID: 100, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}
	mock2 := &mockPlatform{
		issues:    issueSvc2,
		prs:       newMockPRService(),
		workflows: wf2,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	result2, err := Patrol(context.Background(), mock2, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result2.RedispatchedCount)
	assert.Len(t, wf2.dispatched, 0) // monitor no longer dispatches directly
	assert.Len(t, issueSvc2.comments[42], 1)
	assert.Contains(t, issueSvc2.comments[42][0], "/herd retry 42")
}

func TestPatrol_TimeoutAndStale_BothRelabel(t *testing.T) {
	tests := []struct {
		name       string
		activeRuns []*platform.Run
		timeout    int
	}{
		{
			"timeout exceeded",
			[]*platform.Run{
				{ID: 200, Inputs: map[string]string{"issue_number": "42"}, CreatedAt: time.Now().Add(-2 * time.Hour)},
			},
			60,
		},
		{
			"no active run",
			[]*platform.Run{},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.listResults[issues.StatusInProgress] = []*platform.Issue{
				{Number: 42, Title: "Test", Labels: []string{issues.StatusInProgress}},
			}

			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: &mockWorkflowService{activeRuns: tt.activeRuns},
				repo:      &mockRepoService{defaultBranch: "main"},
			}

			cfg := &config.Config{
				Workers: config.Workers{TimeoutMinutes: tt.timeout},
			}

			result, err := Patrol(context.Background(), mock, cfg)
			require.NoError(t, err)
			assert.Equal(t, 1, result.StaleIssues)
			// Both paths should result in the same label transition
			assert.Contains(t, issueSvc.removedLabels[42], issues.StatusInProgress)
			assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
		})
	}
}
