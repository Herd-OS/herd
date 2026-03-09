package integrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Platform ---

type mockPlatform struct {
	issues     platform.IssueService
	prs        platform.PullRequestService
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
func (m *mockPlatform) Checks() platform.CheckService             { return nil }

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
	getResult  map[int]*platform.PullRequest
	created    *platform.PullRequest
	merged     bool
}

func (m *mockPRService) Create(_ context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	m.created = &platform.PullRequest{Number: 100, Title: title, Body: body, Head: head, Base: base}
	return m.created, nil
}
func (m *mockPRService) Get(_ context.Context, number int) (*platform.PullRequest, error) {
	if m.getResult != nil {
		if pr, ok := m.getResult[number]; ok {
			return pr, nil
		}
	}
	return nil, fmt.Errorf("PR #%d not found", number)
}
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
	getResult      map[int]*platform.Milestone
	updatedNumbers []int
	updatedStates  []string
}

func (m *mockMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *mockMilestoneService) Get(_ context.Context, number int) (*platform.Milestone, error) {
	if m.getResult != nil {
		if ms, ok := m.getResult[number]; ok {
			return ms, nil
		}
	}
	return nil, fmt.Errorf("milestone #%d not found", number)
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

	result, err := Consolidate(context.Background(), mock, nil, &config.Config{}, ConsolidateParams{RunID: 100})
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

	result, err := Consolidate(context.Background(), mock, nil, &config.Config{}, ConsolidateParams{RunID: 100})
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

func TestConsolidate_ConflictNotify(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// mockGit that fails on merge
	dir, g := initConflictRepo(t)
	_ = dir

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "notify"},
	}

	result, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 100})
	assert.Error(t, err)
	assert.True(t, result.ConflictDetected)
	assert.Contains(t, issueSvc.comments[42][0], "Merge conflict detected")
}

func TestConsolidate_ConflictDispatchResolver(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test task",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{} // no existing conflict issues

	createdIssues := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 99, Title: title}
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	_, g := initConflictRepo(t)

	mock := &mockPlatform{
		issues: mockCreate,
		workflows: wf,
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{"herd/worker/42-test-task": true},
		},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "dispatch-resolver", MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.ConflictDetected)
	assert.Equal(t, 99, result.ConflictIssue)
	assert.Len(t, createdIssues, 1)
	assert.Contains(t, createdIssues[0].Title, "Resolve conflict")
	assert.Len(t, wf.dispatched, 1)
}

func TestConsolidate_ConflictMaxAttempts(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// Two existing conflict-resolution issues
	issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\nResolve\n"},
		{Number: 81, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\nResolve\n"},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	_, g := initConflictRepo(t)

	mock := &mockPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "dispatch-resolver", MaxConflictResolutionAttempts: 2},
	}

	result, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.ConflictDetected)
	assert.Equal(t, 0, result.ConflictIssue) // No issue created
	assert.Contains(t, issueSvc.comments[42][0], "max resolution attempts")
	assert.Len(t, wf.dispatched, 0) // No dispatch
}

func TestAdvance_AllComplete_RebaseFailure(t *testing.T) {
	// When all tiers complete, openBatchPR is called.
	// If rebase fails, the PR should still be created (without rebase).
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
	}

	prSvc := &mockPRService{}
	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "10"}},
		},
		listResult: []*platform.Run{},
	}

	// Create a repo with a bare origin so fetch works but rebase will conflict
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Initial commit
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch with a conflicting change to shared.txt
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Add a conflicting commit on main so rebase will fail
	runGit(t, dir, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("main diverged"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main diverge")
	runGit(t, dir, "push", "origin", "main")

	g := git.New(dir)

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, g, cfg, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.AllComplete)
	assert.True(t, result.TierComplete)
	// PR should still have been created despite rebase failure
	assert.NotNil(t, prSvc.created)
	assert.Contains(t, prSvc.created.Title, "[herd] Batch")
}

func TestConsolidate_PushFailure(t *testing.T) {
	// Merge succeeds locally but push fails — should return error
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// Create a repo where merge succeeds but push will fail (no remote)
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	// Create batch branch
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")

	// Create worker branch with non-conflicting change
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")

	// Go back to batch branch
	runGit(t, dir, "checkout", "herd/batch/1-batch")

	// Note: no remote "origin" configured, so fetch will use local refs
	// We need to add a fake remote that will fail on push
	runGit(t, dir, "remote", "add", "origin", "/nonexistent/path")

	g := git.New(dir)

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	_, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "fetching")
}

// initConflictRepo creates a git repo with a bare "origin" remote, a batch branch,
// and a conflicting worker branch pushed to origin, so that Consolidate's
// fetch → checkout → merge("origin/worker") flow works and produces a conflict.
func initConflictRepo(t *testing.T) (string, *git.Git) {
	t.Helper()

	// Create bare repo as "origin"
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	// Create working repo
	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Initial commit with a file
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch and modify the file
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Create worker branch from main with conflicting change and push
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("worker content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

	// Also create variant for "test-task" slug
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test-task")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("worker content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test-task")

	// Go back to batch branch for consolidate
	runGit(t, dir, "checkout", "herd/batch/1-batch")

	return dir, git.New(dir)
}

func TestOpenBatchPR_RebaseConflict_DispatchResolver(t *testing.T) {
	// Create a repo where rebase will fail due to diverged main
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Batch branch with conflicting change
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Diverge main
	runGit(t, dir, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("main diverged"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main diverge")
	runGit(t, dir, "push", "origin", "main")

	g := git.New(dir)

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{} // no existing conflict issues

	createdIssues := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 99, Title: title}
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	wf := &mockWorkflowService{}
	prSvc := &mockPRService{}
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	allIssues := []*platform.Issue{
		{Number: 10, Title: "Task", Labels: []string{issues.StatusDone}},
	}
	tiers := [][]int{{10}}

	mock := &mockPlatform{
		issues:     mockCreate,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{
			OnConflict:                    "dispatch-resolver",
			MaxConflictResolutionAttempts: 3,
		},
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	prNum, err := openBatchPR(context.Background(), mock, g, cfg, ms, allIssues, tiers, "herd/batch/1-batch")
	require.NoError(t, err)
	assert.NotZero(t, prNum)
	// PR was still created (un-rebased)
	assert.NotNil(t, prSvc.created)
	// Conflict-resolution issue was created
	assert.Len(t, createdIssues, 1)
	assert.Contains(t, createdIssues[0].Title, "Resolve rebase conflict")
	// Worker was dispatched
	assert.Len(t, wf.dispatched, 1)
}

func TestOpenBatchPR_RebaseConflict_MaxAttempts(t *testing.T) {
	// Same diverged repo setup
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	runGit(t, dir, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("main diverged"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main diverge")
	runGit(t, dir, "push", "origin", "main")

	g := git.New(dir)

	issueSvc := newMockIssueService()
	// Two existing conflict-resolution issues — at max
	issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\nResolve\n"},
		{Number: 81, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\nResolve\n"},
	}

	wf := &mockWorkflowService{}
	prSvc := &mockPRService{}
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	allIssues := []*platform.Issue{
		{Number: 10, Title: "Task", Labels: []string{issues.StatusDone}},
	}
	tiers := [][]int{{10}}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{
			OnConflict:                    "dispatch-resolver",
			MaxConflictResolutionAttempts: 2,
		},
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	prNum, err := openBatchPR(context.Background(), mock, g, cfg, ms, allIssues, tiers, "herd/batch/1-batch")
	require.NoError(t, err)
	assert.NotZero(t, prNum)
	// PR was still created
	assert.NotNil(t, prSvc.created)
	// No resolver dispatched (at cap)
	assert.Len(t, wf.dispatched, 0)
}

func TestOpenBatchPR_RebaseConflict_Notify(t *testing.T) {
	// Same diverged repo setup
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	runGit(t, dir, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("main diverged"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main diverge")
	runGit(t, dir, "push", "origin", "main")

	g := git.New(dir)

	issueSvc := newMockIssueService()
	wf := &mockWorkflowService{}
	prSvc := &mockPRService{}
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	allIssues := []*platform.Issue{
		{Number: 10, Title: "Task", Labels: []string{issues.StatusDone}},
	}
	tiers := [][]int{{10}}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "notify"},
	}

	prNum, err := openBatchPR(context.Background(), mock, g, cfg, ms, allIssues, tiers, "herd/batch/1-batch")
	require.NoError(t, err)
	assert.NotZero(t, prNum)
	// PR created, no resolver dispatched
	assert.NotNil(t, prSvc.created)
	assert.Len(t, wf.dispatched, 0)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}
