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
	issues     *mockIssueService
	prs        *mockPRService
	workflows  *mockWorkflowService
	repo       *mockRepoService
	checks     *mockCheckService
	milestones *mockMilestoneService
}

func (m *mockPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService  { return m.prs }
func (m *mockPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockPlatform) Labels() platform.LabelService              { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService {
	if m.milestones != nil {
		return m.milestones
	}
	return &mockMilestoneService{}
}
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
	listByMilestone  map[int][]*platform.Issue    // keyed by milestone number
	getResults       map[int]*platform.Issue      // for Get by number
	getErr           error
	addedLabels      map[int][]string
	removedLabels    map[int][]string
	addLabelsErr     error
	comments         map[int][]string
	existingComments map[int][]*platform.Comment // for ListComments
	listCommentsErr  error
	deletedComments  []int64
	callLog          *[]string // if non-nil, records "issue:AddLabels" etc. for ordering assertions
	createResult     *platform.Issue
	createErr        error
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
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createResult != nil {
		return m.createResult, nil
	}
	return &platform.Issue{Number: 999}, nil
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
	if f.Milestone != nil && m.listByMilestone != nil {
		return m.listByMilestone[*f.Milestone], nil
	}
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
	if m.callLog != nil {
		*m.callLog = append(*m.callLog, "issue:AddLabels")
	}
	return nil
}
func (m *mockIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = append(m.removedLabels[number], labels...)
	return nil
}
func (m *mockIssueService) AddComment(_ context.Context, number int, body string) error {
	m.comments[number] = append(m.comments[number], body)
	if m.callLog != nil {
		*m.callLog = append(*m.callLog, "issue:AddComment")
	}
	return nil
}
func (m *mockIssueService) AddCommentReturningID(_ context.Context, _ int, body string) (int64, error) {
	return 0, nil
}
func (m *mockIssueService) UpdateComment(_ context.Context, _ int64, _ string) error {
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
	getResults map[int]*platform.PullRequest
	getErr     error
	comments   map[int][]string
	callLog    *[]string // if non-nil, records "pr:AddComment" etc. for ordering assertions
}

func newMockPRService() *mockPRService {
	return &mockPRService{comments: make(map[int][]string), getResults: make(map[int]*platform.PullRequest)}
}

func (m *mockPRService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) Get(_ context.Context, number int) (*platform.PullRequest, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if pr, ok := m.getResults[number]; ok {
		return pr, nil
	}
	return &platform.PullRequest{Number: number, Mergeable: true, MergeableKnown: true}, nil
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
	if m.callLog != nil {
		*m.callLog = append(*m.callLog, "pr:AddComment")
	}
	return nil
}
func (m *mockPRService) ListReviewComments(_ context.Context, _ int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (m *mockPRService) GetDiff(_ context.Context, _ int) (string, error) {
	return "", nil
}
func (m *mockPRService) Close(_ context.Context, _ int) error {
	return nil
}

type mockWorkflowService struct {
	activeRuns    []*platform.Run
	completedRuns []*platform.Run
	dispatched    []map[string]string
	cancelled     []int64
	dispatchErr   error
}

func (m *mockWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	if m.dispatchErr != nil {
		return nil, m.dispatchErr
	}
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

type mockMilestoneService struct {
	getResult map[int]*platform.Milestone
	getErr    error
}

func (m *mockMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneService) Get(_ context.Context, number int) (*platform.Milestone, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.getResult != nil {
		if ms, ok := m.getResult[number]; ok {
			return ms, nil
		}
	}
	return &platform.Milestone{Number: number, Title: fmt.Sprintf("Batch %d", number)}, nil
}
func (m *mockMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
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

func TestPatrol_FailedIssue_RedispatchWithRetryPendingLabel_NoDuplicateCommand(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusFailed] = []*platform.Issue{
		{
			Number: 42, Title: "Test", Labels: []string{issues.StatusFailed},
			Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
		},
	}
	// Simulate herd/retry-pending label already present from a prior patrol run.
	issueSvc.getResults[42] = &platform.Issue{Number: 42, Labels: []string{issues.RetryPending}}

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
	// No retry comment should be posted — label acts as dedup.
	assert.Equal(t, 0, result.RedispatchedCount)
	assert.Len(t, issueSvc.comments[42], 0)
	// No duplicate label addition.
	assert.Empty(t, issueSvc.addedLabels[42])
}

func TestPatrol_FailedIssue_RedispatchAddsRetryPendingLabelBeforeComment(t *testing.T) {
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

	var opLog []string
	issueSvc.callLog = &opLog

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

	_, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	// The label add must appear before the comment in the operation log.
	require.Len(t, opLog, 2, "expected exactly AddLabels then AddComment")
	assert.Equal(t, "issue:AddLabels", opLog[0], "label must be added first")
	assert.Equal(t, "issue:AddComment", opLog[1], "comment must be posted second")
	assert.Contains(t, issueSvc.addedLabels[42], issues.RetryPending)
	assert.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "/herd retry 42")
}

func TestHasRetryPendingLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected bool
	}{
		{
			name:     "no labels",
			labels:   nil,
			expected: false,
		},
		{
			name:     "unrelated label only",
			labels:   []string{issues.StatusFailed},
			expected: false,
		},
		{
			name:     "retry-pending label present",
			labels:   []string{issues.RetryPending},
			expected: true,
		},
		{
			name:     "retry-pending label among others",
			labels:   []string{issues.StatusFailed, issues.RetryPending, "other"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.getResults[42] = &platform.Issue{Number: 42, Labels: tt.labels}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: &mockWorkflowService{},
				repo:      &mockRepoService{defaultBranch: "main"},
			}
			assert.Equal(t, tt.expected, hasRetryPendingLabel(context.Background(), mock, 42))
		})
	}
}

func TestHasRetryPendingLabel_ErrorFallback(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getErr = fmt.Errorf("API error")
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	// Should return false (fail open) when Issues().Get() errors.
	assert.False(t, hasRetryPendingLabel(context.Background(), mock, 42))
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
	assert.Len(t, issueSvc.comments[10], 1)
	assert.Contains(t, issueSvc.comments[10][0], "open for over 48 hours")
	assert.Len(t, issueSvc.comments[11], 0) // non-herd PR not flagged
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
	assert.Len(t, issueSvc.comments[10], 1)
	assert.Contains(t, issueSvc.comments[10][0], "/herd fix-ci")
	// Label must be added to the PR before the comment is posted so that a
	// concurrent patrol run racing past the hasCIFixPendingLabel check sees
	// the label and skips rather than posting a duplicate /herd fix-ci comment.
	assert.Contains(t, issueSvc.addedLabels[10], issues.CIFixPending)
}

func TestPatrol_CIFailureWithExistingCIFixPendingLabel_NoDuplicateCommand(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// Simulate herd/ci-fix-pending label already present from a prior fix cycle (added atomically
	// in handleFixCI.beforeDispatch before workers are dispatched).
	issueSvc.getResults[10] = &platform.Issue{Number: 10, Labels: []string{issues.CIFixPending}}

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
	// No duplicate label addition — label was already present.
	assert.Empty(t, issueSvc.addedLabels[10])
}

func TestPatrol_CIFailure_LabelAddedBeforeCommentPosted(t *testing.T) {
	// Verifies the label-first ordering: patrol must add herd/ci-fix-pending
	// BEFORE posting the /herd fix-ci comment so that a concurrent patrol run
	// racing past the hasCIFixPendingLabel check sees the label and skips.
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	issueSvc := newMockIssueService()

	var opLog []string
	issueSvc.callLog = &opLog
	prSvc.callLog = &opLog

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

	_, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	// The label add must appear before the comment in the operation log.
	require.Len(t, opLog, 2, "expected exactly AddLabels then AddComment")
	assert.Equal(t, "issue:AddLabels", opLog[0], "label must be added first")
	assert.Equal(t, "issue:AddComment", opLog[1], "comment must be posted second")
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

func TestPatrol_CIPassingRemovesCIFixPendingLabel(t *testing.T) {
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
	// The CIFixPending label must be removed so future failures can re-trigger.
	assert.Contains(t, issueSvc.removedLabels[10], issues.CIFixPending)
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
	// No comments to delete, but label removal is always attempted on CI success.
	assert.Empty(t, issueSvc.deletedComments)
	assert.Contains(t, issueSvc.removedLabels[10], issues.CIFixPending)
}

// TestPatrol_CIPassingRemovesStuckLabelEvenWithNoComment is a regression test for the bug
// where deleteCIFixComments only called RemoveLabels when deleted > 0. If the /herd fix-ci
// comment was manually deleted (or missed due to pagination/timing), the herd/ci-fix-pending
// label would persist indefinitely, blocking all future automated CI fix triggering.
func TestPatrol_CIPassingRemovesStuckLabelEvenWithNoComment(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// The /herd fix-ci comment has been manually deleted, but herd/ci-fix-pending label persists.
	issueSvc.existingComments = map[int][]*platform.Comment{10: {}}

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
	// No comments to delete.
	assert.Empty(t, issueSvc.deletedComments)
	// Label must be removed unconditionally to unblock future fix-ci triggers.
	assert.Contains(t, issueSvc.removedLabels[10], issues.CIFixPending)
}

func TestPatrol_CIFailureAfterCIFixPendingLabelRemoved(t *testing.T) {
	// Regression test: after CI passes (label removed), a new CI failure
	// must post a fresh /herd fix-ci comment.
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()
	// No label — simulates state after a prior pass removed it.

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
	assert.Len(t, issueSvc.comments[10], 1)
	assert.Contains(t, issueSvc.comments[10][0], "/herd fix-ci")
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

func TestHasCIFixPendingLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected bool
	}{
		{
			name:     "no labels",
			labels:   nil,
			expected: false,
		},
		{
			name:     "unrelated label only",
			labels:   []string{"herd/status-in-progress"},
			expected: false,
		},
		{
			name:     "ci-fix-pending label present",
			labels:   []string{issues.CIFixPending},
			expected: true,
		},
		{
			name:     "ci-fix-pending label among others",
			labels:   []string{"herd/status-in-progress", issues.CIFixPending, "other"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.getResults[10] = &platform.Issue{Number: 10, Labels: tt.labels}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: &mockWorkflowService{},
				repo:      &mockRepoService{defaultBranch: "main"},
			}
			assert.Equal(t, tt.expected, hasCIFixPendingLabel(context.Background(), mock, 10))
		})
	}
}

func TestHasCIFixPendingLabel_ErrorFallback(t *testing.T) {
	// When Issues().Get() returns an error, hasCIFixPendingLabel should fail open (return false).
	issueSvc := newMockIssueService()
	issueSvc.getErr = fmt.Errorf("API error")
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	assert.False(t, hasCIFixPendingLabel(context.Background(), mock, 10))
}

func TestDeleteCIFixComments(t *testing.T) {
	tests := []struct {
		name             string
		comments         []*platform.Comment
		expectedDeleted  []int64
	}{
		{
			name:            "no comments",
			comments:        nil,
			expectedDeleted: nil,
		},
		{
			name:            "exact match deleted",
			comments:        []*platform.Comment{{ID: 1, Body: "/herd fix-ci"}},
			expectedDeleted: []int64{1},
		},
		{
			name:            "whitespace-only surrounding deleted",
			comments:        []*platform.Comment{{ID: 2, Body: "  /herd fix-ci\n"}},
			expectedDeleted: []int64{2},
		},
		{
			name:            "prose mention not deleted",
			comments:        []*platform.Comment{{ID: 3, Body: "I tried `/herd fix-ci` but nothing happened"}},
			expectedDeleted: nil,
		},
		{
			name:            "mid-sentence not deleted",
			comments:        []*platform.Comment{{ID: 4, Body: "running /herd fix-ci now"}},
			expectedDeleted: nil,
		},
		{
			name: "only exact-match comments deleted among mixed",
			comments: []*platform.Comment{
				{ID: 10, Body: "/herd fix-ci"},
				{ID: 11, Body: "some prose mentioning /herd fix-ci command"},
				{ID: 12, Body: "  /herd fix-ci  "},
			},
			expectedDeleted: []int64{10, 12},
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
			deleteCIFixComments(context.Background(), mock, 10)
			assert.Equal(t, tt.expectedDeleted, issueSvc.deletedComments)
		})
	}
}

func TestDeleteCIFixComments_ErrorFallback(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listCommentsErr = fmt.Errorf("API error")
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	// Should not panic on error, and must still remove the label
	deleteCIFixComments(context.Background(), mock, 10)
	assert.Empty(t, issueSvc.deletedComments)
	assert.Contains(t, issueSvc.removedLabels[10], issues.CIFixPending)
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

func TestPatrol_StaleReadyIssue_Dispatched(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusReady] = []*platform.Issue{
		{
			Number:    50,
			Title:     "Ready task",
			Labels:    []string{issues.StatusReady},
			Milestone: &platform.Milestone{Number: 5, Title: "Sprint Alpha"},
			Body: "---\nherd:\n  version: 1\n---\n## Task\nSome task",
			UpdatedAt: time.Now().Add(-1 * time.Hour),
		},
	}

	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Monitor: config.Monitor{AutoRedispatch: true, StaleThresholdMinutes: 10},
		Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.StaleReadyDispatched)
	assert.Contains(t, issueSvc.removedLabels[50], issues.StatusReady)
	assert.Contains(t, issueSvc.addedLabels[50], issues.StatusInProgress)
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "50", wf.dispatched[0]["issue_number"])
	assert.Equal(t, "herd/batch/5-sprint-alpha", wf.dispatched[0]["batch_branch"])
	assert.Equal(t, "30", wf.dispatched[0]["timeout_minutes"])
	assert.Equal(t, "herd-worker", wf.dispatched[0]["runner_label"])
}

func TestPatrol_StaleReadyIssue_SkipConditions(t *testing.T) {
	tests := []struct {
		name      string
		issue     *platform.Issue
		depIssue  *platform.Issue // if non-nil, added to getResults
		maxConc   int
		runs      []*platform.Run
		threshold int
	}{
		{
			name: "deps not complete",
			issue: &platform.Issue{
				Number:    50,
				Title:     "Ready task",
				Labels:    []string{issues.StatusReady},
				Milestone: &platform.Milestone{Number: 5, Title: "Sprint"},
				Body:      "---\nherd:\n  version: 1\n  depends_on: [10]\n---\n## Task\nSome task",
				UpdatedAt: time.Now().Add(-1 * time.Hour),
			},
			depIssue:  &platform.Issue{Number: 10, State: "open", Labels: []string{issues.StatusInProgress}},
			maxConc:   3,
			threshold: 10,
		},
		{
			name: "max concurrent reached",
			issue: &platform.Issue{
				Number:    50,
				Title:     "Ready task",
				Labels:    []string{issues.StatusReady},
				Milestone: &platform.Milestone{Number: 5, Title: "Sprint"},
				Body:      "---\nherd:\n  version: 1\n---\n## Task\nSome task",
				UpdatedAt: time.Now().Add(-1 * time.Hour),
			},
			maxConc: 1,
			runs: []*platform.Run{
				{ID: 300, Inputs: map[string]string{"issue_number": "99"}, CreatedAt: time.Now()},
			},
			threshold: 10,
		},
		{
			name: "below stale threshold",
			issue: &platform.Issue{
				Number:    50,
				Title:     "Ready task",
				Labels:    []string{issues.StatusReady},
				Milestone: &platform.Milestone{Number: 5, Title: "Sprint"},
				Body:      "---\nherd:\n  version: 1\n---\n## Task\nSome task",
				UpdatedAt: time.Now(), // just updated
			},
			maxConc:   3,
			threshold: 10,
		},
		{
			name: "manual task skipped",
			issue: &platform.Issue{
				Number:    50,
				Title:     "Manual task",
				Labels:    []string{issues.StatusReady, issues.TypeManual},
				Milestone: &platform.Milestone{Number: 5, Title: "Sprint"},
				Body:      "---\nherd:\n  version: 1\n---\n## Task\nSome task",
				UpdatedAt: time.Now().Add(-1 * time.Hour),
			},
			maxConc:   3,
			threshold: 10,
		},
		{
			name: "no milestone",
			issue: &platform.Issue{
				Number:    50,
				Title:     "No milestone task",
				Labels:    []string{issues.StatusReady},
				Body:      "---\nherd:\n  version: 1\n---\n## Task\nSome task",
				UpdatedAt: time.Now().Add(-1 * time.Hour),
			},
			maxConc:   3,
			threshold: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.listResults[issues.StatusReady] = []*platform.Issue{tt.issue}
			if tt.depIssue != nil {
				issueSvc.getResults[tt.depIssue.Number] = tt.depIssue
			}

			wf := &mockWorkflowService{activeRuns: tt.runs}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: wf,
				repo:      &mockRepoService{defaultBranch: "main"},
			}

			cfg := &config.Config{
				Monitor: config.Monitor{AutoRedispatch: true, StaleThresholdMinutes: tt.threshold},
				Workers: config.Workers{MaxConcurrent: tt.maxConc, TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
			}

			result, err := Patrol(context.Background(), mock, cfg)
			require.NoError(t, err)
			assert.Equal(t, 0, result.StaleReadyDispatched)
			assert.Len(t, wf.dispatched, 0)
		})
	}
}

func TestPatrol_StaleReadyIssue_DispatchFailure(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResults[issues.StatusReady] = []*platform.Issue{
		{
			Number:    50,
			Title:     "Ready task",
			Labels:    []string{issues.StatusReady},
			Milestone: &platform.Milestone{Number: 5, Title: "Sprint"},
			Body:      "---\nherd:\n  version: 1\n---\n## Task\nSome task",
			UpdatedAt: time.Now().Add(-1 * time.Hour),
		},
	}

	wf := &mockWorkflowService{dispatchErr: fmt.Errorf("dispatch failed")}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{
		Monitor: config.Monitor{AutoRedispatch: true, StaleThresholdMinutes: 10},
		Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.StaleReadyDispatched)
	// Should relabel to failed on dispatch error
	assert.Contains(t, issueSvc.removedLabels[50], issues.StatusInProgress)
	assert.Contains(t, issueSvc.addedLabels[50], issues.StatusFailed)
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

func TestHasRebasePendingLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		expected bool
	}{
		{
			name:     "no labels",
			labels:   nil,
			expected: false,
		},
		{
			name:     "unrelated label only",
			labels:   []string{issues.StatusFailed},
			expected: false,
		},
		{
			name:     "rebase-pending label present",
			labels:   []string{issues.RebasePending},
			expected: true,
		},
		{
			name:     "rebase-pending label among others",
			labels:   []string{issues.StatusFailed, issues.RebasePending, "other"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.getResults[42] = &platform.Issue{Number: 42, Labels: tt.labels}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       newMockPRService(),
				workflows: &mockWorkflowService{},
				repo:      &mockRepoService{defaultBranch: "main"},
			}
			assert.Equal(t, tt.expected, hasRebasePendingLabel(context.Background(), mock, 42))
		})
	}
}

func TestHasRebasePendingLabel_ErrorFallback(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getErr = fmt.Errorf("API error")
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       newMockPRService(),
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	// Should return false (fail open) when Issues().Get() errors.
	assert.False(t, hasRebasePendingLabel(context.Background(), mock, 42))
}

func TestPatrol_ConflictDetectedOnBatchPR(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	// Get returns non-mergeable PR
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: true}

	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 999}

	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.ConflictDetected)
	// RebasePending label should be added
	assert.Contains(t, issueSvc.addedLabels[10], issues.RebasePending)
	// A comment should be posted about the conflict
	require.Len(t, issueSvc.comments[10], 1)
	assert.Contains(t, issueSvc.comments[10][0], "merge conflicts")
	assert.Contains(t, issueSvc.comments[10][0], "HerdOS Monitor Alert")
	// A conflict resolution issue should be created and worker dispatched
	assert.Len(t, wf.dispatched, 1)
}

func TestPatrol_MergeableUnknown_SkipsConflictCheck(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	// GitHub hasn't computed mergeability yet (e.g., right after a force push)
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: false}

	issueSvc := newMockIssueService()
	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	// Should NOT detect a conflict when mergeability is unknown
	assert.Equal(t, 0, result.ConflictDetected)
	assert.Empty(t, issueSvc.addedLabels[10])
	assert.Empty(t, wf.dispatched)
}

func TestPatrol_ConflictResolved_LabelRemoved(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	// Get returns mergeable PR
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: true, MergeableKnown: true}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ConflictDetected)
	// RebasePending label should be removed (cleanup)
	assert.Contains(t, issueSvc.removedLabels[10], issues.RebasePending)
}

func TestPatrol_ConflictWithRebasePendingLabel_NoDuplicate(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: true}

	issueSvc := newMockIssueService()
	// Simulate herd/rebase-pending label already present
	issueSvc.getResults[10] = &platform.Issue{Number: 10, Labels: []string{issues.RebasePending}}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ConflictDetected)
	// No new label additions, no comments
	assert.Empty(t, issueSvc.addedLabels[10])
	assert.Empty(t, issueSvc.comments[10])
}

func TestPatrol_ConflictDispatchFailure_LabelRemoved(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: true}

	issueSvc := newMockIssueService()
	// Make issue creation fail, which will cause the dispatch to fail
	issueSvc.createErr = fmt.Errorf("create failed")

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ConflictDetected)
	// Label was added then removed on failure
	assert.Contains(t, issueSvc.addedLabels[10], issues.RebasePending)
	assert.Contains(t, issueSvc.removedLabels[10], issues.RebasePending)
}

func TestPatrol_ConflictDetection_NonBatchPR_Skipped(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Manual PR", Head: "feature/manual", CreatedAt: time.Now()},
	}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ConflictDetected)
}

func TestPatrol_ConflictDetection_PRGetError_Continues(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	prSvc.getErr = fmt.Errorf("API error")

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ConflictDetected)
}

func TestPatrol_ConflictDetection_LabelAddedBeforeDispatch(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: true}

	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 999}

	var opLog []string
	issueSvc.callLog = &opLog

	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}

	_, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	// The label add must appear before the comment
	require.True(t, len(opLog) >= 2, "expected at least AddLabels then AddComment")
	assert.Equal(t, "issue:AddLabels", opLog[0], "label must be added first")
	// The comment should come after the label
	lastIdx := len(opLog) - 1
	assert.Equal(t, "issue:AddComment", opLog[lastIdx], "comment must be posted last")
}

func TestPatrol_ConflictDetection_NonHerdPR_Ignored(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "Normal PR", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	// If Get were called, it would return non-mergeable — but it should never be called
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: true}

	issueSvc := newMockIssueService()

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ConflictDetected)
	// No labels or comments should be added — the PR was skipped entirely
	assert.Empty(t, issueSvc.addedLabels[10])
	assert.Empty(t, issueSvc.comments[10])
}

func TestPatrol_ConflictDetection_MaxAttemptsReached(t *testing.T) {
	prSvc := newMockPRService()
	prSvc.listResult = []*platform.PullRequest{
		{Number: 10, Title: "[herd] Batch 1", Head: "herd/batch/1-batch", CreatedAt: time.Now()},
	}
	prSvc.getResults[10] = &platform.PullRequest{Number: 10, Head: "herd/batch/1-batch", Mergeable: false, MergeableKnown: true}

	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 999}
	// Simulate existing conflict-resolution issue at the cap
	issueSvc.listByMilestone = map[int][]*platform.Issue{
		1: {
			{Number: 100, Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  conflict_resolution: true\n---\n\n## Task\nResolve"},
		},
	}

	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 1},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}

	result, err := Patrol(context.Background(), mock, cfg)
	require.NoError(t, err)
	// Dispatch returns 0 (cap reached), so patrol should NOT count it as detected
	assert.Equal(t, 0, result.ConflictDetected)
	// Label was added then removed when cap was reached
	assert.Contains(t, issueSvc.removedLabels[10], issues.RebasePending)
	// No new workflow dispatch since cap was reached
	assert.Len(t, wf.dispatched, 0)
}
