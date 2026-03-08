package integrator

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
	issues     platform.IssueService
	prs        *mockPRService
	workflows  *mockWorkflowService
	repo       *mockRepoService
	milestones *mockMilestoneService
}

func (m *mockPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService  { return m.prs }
func (m *mockPlatform) Workflows() platform.WorkflowService        { return m.workflows }
func (m *mockPlatform) Labels() platform.LabelService              { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService      { return m.milestones }
func (m *mockPlatform) Runners() platform.RunnerService            { return nil }
func (m *mockPlatform) Repository() platform.RepositoryService     { return m.repo }

type mockIssueService struct {
	getResult      map[int]*platform.Issue
	listResult     []*platform.Issue
	addedLabels    map[int][]string
	removedLabels  map[int][]string
	updatedIssues  map[int]platform.IssueUpdate
	comments       map[int][]string
}

func newMockIssueService() *mockIssueService {
	return &mockIssueService{
		getResult:     make(map[int]*platform.Issue),
		addedLabels:   make(map[int][]string),
		removedLabels: make(map[int][]string),
		updatedIssues: make(map[int]platform.IssueUpdate),
		comments:      make(map[int][]string),
	}
}

func (m *mockIssueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	if i, ok := m.getResult[number]; ok {
		return i, nil
	}
	return nil, nil
}
func (m *mockIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return m.listResult, nil
}
func (m *mockIssueService) Update(_ context.Context, number int, update platform.IssueUpdate) (*platform.Issue, error) {
	m.updatedIssues[number] = update
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
	created    *platform.PullRequest
	merged     bool
}

func (m *mockPRService) Create(_ context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	m.created = &platform.PullRequest{Number: 100, Title: title, Body: body, Head: head, Base: base}
	return m.created, nil
}
func (m *mockPRService) Get(_ context.Context, _ int) (*platform.PullRequest, error) { return nil, nil }
func (m *mockPRService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	return m.listResult, nil
}
func (m *mockPRService) Update(_ context.Context, _ int, _, _ *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPRService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	m.merged = true
	return &platform.MergeResult{Merged: true}, nil
}
func (m *mockPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *mockPRService) AddComment(_ context.Context, _ int, _ string) error { return nil }
func (m *mockPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}

type mockWorkflowService struct {
	runs         map[int64]*platform.Run
	listResult   []*platform.Run
	dispatched   []map[string]string
}

func (m *mockWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	m.dispatched = append(m.dispatched, inputs)
	return nil, nil
}
func (m *mockWorkflowService) GetRun(_ context.Context, id int64) (*platform.Run, error) {
	if r, ok := m.runs[id]; ok {
		return r, nil
	}
	return nil, nil
}
func (m *mockWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return m.listResult, nil
}
func (m *mockWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

type mockRepoService struct {
	defaultBranch string
	branchExists  map[string]bool
	deletedBranch string
}

func (m *mockRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (m *mockRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return m.defaultBranch, nil
}
func (m *mockRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *mockRepoService) DeleteBranch(_ context.Context, name string) error {
	m.deletedBranch = name
	return nil
}
func (m *mockRepoService) GetBranchSHA(_ context.Context, name string) (string, error) {
	if m.branchExists != nil {
		if _, ok := m.branchExists[name]; ok {
			return "abc123", nil
		}
	}
	return "", fmt.Errorf("branch %s not found", name)
}

type mockMilestoneService struct {
	updatedNumbers []int
	updatedStates  []string
}

func (m *mockMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneService) Get(_ context.Context, _ int) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneService) Update(_ context.Context, number int, changes platform.MilestoneUpdate) (*platform.Milestone, error) {
	m.updatedNumbers = append(m.updatedNumbers, number)
	if changes.State != nil {
		m.updatedStates = append(m.updatedStates, *changes.State)
	}
	return nil, nil
}

// --- Tests ---

func TestConsolidate_FailedRun(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "failure", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo: &mockRepoService{defaultBranch: "main"},
	}

	result, err := Consolidate(context.Background(), mock, nil, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.False(t, result.Merged)
	assert.Equal(t, 42, result.IssueNumber)
	// Should label as failed
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestConsolidate_NoOpWorker(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{}, // worker branch doesn't exist
		},
	}

	result, err := Consolidate(context.Background(), mock, nil, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.NoOp)
	assert.False(t, result.Merged)
}

func TestAdvance_TierComplete(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// Issues in the milestone
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 11, Title: "Task B", Labels: []string{issues.StatusBlocked},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo B\n"},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "10"}},
		},
		listResult: []*platform.Run{}, // no active workers
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       &mockPRService{},
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, nil, cfg, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.TierComplete)
	assert.Equal(t, 1, result.DispatchedCount)
	// Issue 11 should be unblocked and dispatched
	assert.Contains(t, issueSvc.removedLabels[11], issues.StatusBlocked)
	assert.Contains(t, issueSvc.addedLabels[11], issues.StatusInProgress)
	assert.Len(t, wf.dispatched, 1)
}

func TestAdvance_TierStuck(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusFailed},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusFailed},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 11, Title: "Task B", Labels: []string{issues.StatusBlocked},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo B\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "failure", Inputs: map[string]string{"issue_number": "10"}},
			},
		},
		repo: &mockRepoService{defaultBranch: "main"},
	}

	result, err := Advance(context.Background(), mock, nil, &config.Config{}, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	assert.False(t, result.TierComplete)
	assert.False(t, result.AllComplete)
}

func TestAdvance_DoubleDispatchPrevention(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 11, Title: "Task B", Labels: []string{issues.StatusInProgress}, // Already dispatched!
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo B\n"},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "10"}},
		},
		listResult: []*platform.Run{},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       &mockPRService{},
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, nil, cfg, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	// Issue 11 is already in-progress, should not be dispatched again
	assert.Equal(t, 0, result.DispatchedCount)
	assert.Len(t, wf.dispatched, 0)
}

func TestBuildBatchPRBody(t *testing.T) {
	ms := &platform.Milestone{Number: 5, Title: "Add auth"}
	allIssues := []*platform.Issue{
		{Number: 42, Title: "Add model", Labels: []string{issues.StatusDone}},
		{Number: 43, Title: "Add routes", Labels: []string{issues.StatusDone}},
	}
	tiers := [][]int{{42}, {43}}

	body := buildBatchPRBody(ms, allIssues, tiers)

	assert.Contains(t, body, "**Add auth**")
	assert.Contains(t, body, "2 tasks across 2 tiers")
	assert.Contains(t, body, "#42")
	assert.Contains(t, body, "#43")
	assert.Contains(t, body, "Add model")
	assert.Contains(t, body, "Add routes")
	assert.Contains(t, body, "herd/worker/42-add-model")
}

func TestBuildTiersFromIssues(t *testing.T) {
	allIssues := []*platform.Issue{
		{Number: 10, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nA\n"},
		{Number: 11, Body: "---\nherd:\n  version: 1\n  depends_on: [10]\n---\n\n## Task\nB\n"},
		{Number: 12, Body: "---\nherd:\n  version: 1\n  depends_on: [10]\n---\n\n## Task\nC\n"},
	}

	tiers, err := buildTiersFromIssues(allIssues)
	require.NoError(t, err)
	assert.Len(t, tiers, 2)
	assert.Contains(t, tiers[0], 10)
	assert.Contains(t, tiers[1], 11)
	assert.Contains(t, tiers[1], 12)
}

func TestFindIssue(t *testing.T) {
	allIssues := []*platform.Issue{
		{Number: 1}, {Number: 2}, {Number: 3},
	}
	assert.Equal(t, 2, findIssue(allIssues, 2).Number)
	assert.Nil(t, findIssue(allIssues, 99))
}
