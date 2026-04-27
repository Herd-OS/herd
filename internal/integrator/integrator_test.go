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
	getResult          map[int]*platform.Issue
	listResult         []*platform.Issue
	addedLabels        map[int][]string
	removedLabels      map[int][]string
	updatedIssues      map[int]platform.IssueUpdate
	comments           map[int][]string
	listCommentsResult []*platform.Comment
	createResult       *platform.Issue
	createErr          error
	createdTitle       string
	createdBody        string
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

func (m *mockIssueService) Create(_ context.Context, title, body string, _ []string, _ *int) (*platform.Issue, error) {
	m.createdTitle = title
	m.createdBody = body
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createResult != nil {
		return m.createResult, nil
	}
	return &platform.Issue{Number: 999}, nil
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
func (m *mockIssueService) AddCommentReturningID(_ context.Context, _ int, body string) (int64, error) {
	return 0, nil
}
func (m *mockIssueService) UpdateComment(_ context.Context, _ int64, _ string) error {
	return nil
}
func (m *mockIssueService) DeleteComment(_ context.Context, _ int64) error { return nil }
func (m *mockIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return m.listCommentsResult, nil
}
func (m *mockIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

type mockPRService struct {
	listResult  []*platform.PullRequest
	getResult   map[int]*platform.PullRequest
	created     *platform.PullRequest
	merged      bool
	diffResult  string
	comments    map[int][]string
	onCreateErr error // if set, Create returns this error
}

func (m *mockPRService) Create(_ context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	if m.onCreateErr != nil {
		return nil, m.onCreateErr
	}
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
func (m *mockPRService) AddComment(_ context.Context, number int, body string) error {
	if m.comments == nil {
		m.comments = map[int][]string{}
	}
	m.comments[number] = append(m.comments[number], body)
	return nil
}
func (m *mockPRService) ListReviewComments(_ context.Context, _ int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (m *mockPRService) GetDiff(_ context.Context, _ int) (string, error) {
	if m.diffResult != "" {
		return m.diffResult, nil
	}
	return "diff --git a/file.go b/file.go\n", nil
}
func (m *mockPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}
func (m *mockPRService) Close(_ context.Context, _ int) error {
	return nil
}

type mockWorkflowService struct {
	runs              map[int64]*platform.Run
	listResult        []*platform.Run
	dispatched        []map[string]string
	onDispatch        func() // optional; called before recording each dispatch
	lastListRunFilter platform.RunFilters
}

func (m *mockWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	if m.onDispatch != nil {
		m.onDispatch()
	}
	m.dispatched = append(m.dispatched, inputs)
	return nil, nil
}
func (m *mockWorkflowService) GetRun(_ context.Context, id int64) (*platform.Run, error) {
	if r, ok := m.runs[id]; ok {
		return r, nil
	}
	return nil, nil
}
func (m *mockWorkflowService) ListRuns(_ context.Context, filters platform.RunFilters) ([]*platform.Run, error) {
	m.lastListRunFilter = filters
	return m.listResult, nil
}
func (m *mockWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

type mockRepoService struct {
	defaultBranch  string
	branchExists   map[string]bool
	deletedBranch  string
	deletedBranches []string
}

func (m *mockRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (m *mockRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return m.defaultBranch, nil
}
func (m *mockRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *mockRepoService) DeleteBranch(_ context.Context, name string) error {
	m.deletedBranch = name
	m.deletedBranches = append(m.deletedBranches, name)
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

func TestConsolidate_CancelledRun(t *testing.T) {
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
				100: {ID: 100, Conclusion: "cancelled", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo: &mockRepoService{defaultBranch: "main"},
	}

	result, err := Consolidate(context.Background(), mock, nil, &config.Config{}, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.False(t, result.Merged)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestConsolidate_SuccessfulMerge(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// Create repo with batch and worker branches
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Create worker branch with non-conflicting change
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

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

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.Merged)
	assert.Equal(t, "herd/worker/42-test", result.WorkerBranch)

	// Verify worker.txt exists on batch branch
	_, statErr := os.Stat(filepath.Join(dir, "worker.txt"))
	assert.NoError(t, statErr)
}

func TestConsolidate_RemovesWorkerProgressFile(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// Create repo with batch and worker branches, worker branch has WORKER_PROGRESS.md
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Create worker branch with WORKER_PROGRESS.md
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "WORKER_PROGRESS.md"), []byte("- [x] done"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".herd", "progress"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "progress", "42.md"), []byte("- [x] done"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change with progress file")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

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

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	assert.True(t, result.Merged)

	// WORKER_PROGRESS.md should not exist on disk after consolidation
	_, statErr := os.Stat(filepath.Join(dir, "WORKER_PROGRESS.md"))
	assert.True(t, os.IsNotExist(statErr), "WORKER_PROGRESS.md should be removed after consolidation")

	// .herd/progress/42.md should not exist on disk after consolidation
	_, statErr = os.Stat(filepath.Join(dir, ".herd", "progress", "42.md"))
	assert.True(t, os.IsNotExist(statErr), ".herd/progress/42.md should be removed after consolidation")

	// worker.txt should still exist
	_, statErr = os.Stat(filepath.Join(dir, "worker.txt"))
	assert.NoError(t, statErr, "worker.txt should still exist")

	// Repo should be clean (no dirty index)
	dirty, dirtyErr := g.IsDirty()
	require.NoError(t, dirtyErr)
	assert.False(t, dirty, "repo should be clean after consolidation")
}

func TestConsolidate_ProgressCleanupUseSeparateCommit(t *testing.T) {
	source, err := os.ReadFile("integrator.go")
	require.NoError(t, err)
	src := string(source)

	assert.Contains(t, src, `g.Commit("Remove worker progress tracking files")`,
		"Progress cleanup should use a separate commit, not amend")
	assert.NotContains(t, src, "AmendNoEdit",
		"Progress cleanup should not use AmendNoEdit")
}

func TestConsolidate_ConfiguresGitIdentity(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// Create repo WITHOUT git identity configured
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	// Deliberately NOT setting user.email/user.name

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	runGit(t, dir, "config", "user.email", "temp@temp.com") // needed for initial commit
	runGit(t, dir, "config", "user.name", "Temp")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Now unset the identity to simulate the runner environment
	runGit(t, dir, "config", "--unset", "user.email")
	runGit(t, dir, "config", "--unset", "user.name")

	// Create branches
	runGit(t, dir, "config", "user.email", "temp@temp.com")
	runGit(t, dir, "config", "user.name", "Temp")
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

	// Unset identity again
	runGit(t, dir, "config", "--unset", "user.email")
	runGit(t, dir, "config", "--unset", "user.name")

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

	// This would previously fail with "unable to auto-detect email address"
	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.Merged)
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
	// Concurrency check should filter by worker workflow only
	assert.Equal(t, "herd-worker.yml", wf.lastListRunFilter.WorkflowFileName)
}

func TestAdvance_DispatchesReadyIssues(t *testing.T) {
	// When advance previously left issues as ready (capacity limited),
	// subsequent advances should dispatch them.
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 11, Title: "Task B", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo B\n"},
		// These two were left as ready by a previous capacity-limited advance
		{Number: 12, Title: "Task C", Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo C\n"},
		{Number: 13, Title: "Task D", Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [11]\n---\n\n## Task\nDo D\n"},
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
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 5, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, nil, cfg, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.TierComplete)
	// Both ready issues should be dispatched
	assert.Equal(t, 2, result.DispatchedCount)
	assert.Contains(t, issueSvc.removedLabels[12], issues.StatusReady)
	assert.Contains(t, issueSvc.addedLabels[12], issues.StatusInProgress)
	assert.Contains(t, issueSvc.removedLabels[13], issues.StatusReady)
	assert.Contains(t, issueSvc.addedLabels[13], issues.StatusInProgress)
}

func TestAdvance_DispatchesRemainingInSameTier(t *testing.T) {
	// When a worker completes but other issues in the same tier are still ready
	// (because concurrency limits prevented dispatching them earlier), advance
	// should dispatch the remaining ready issues.
	issueSvc := newMockIssueService()
	issueSvc.getResult[11] = &platform.Issue{
		Number: 11, Title: "Task B",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		// Tier 0: done
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		// Tier 1: triggering issue is done, but two others still ready
		{Number: 11, Title: "Task B", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo B\n"},
		{Number: 12, Title: "Task C", Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo C\n"},
		{Number: 13, Title: "Task D", Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo D\n"},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			200: {ID: 200, Conclusion: "success", Inputs: map[string]string{"issue_number": "11"}},
		},
		listResult: []*platform.Run{}, // no active workers
	}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 5, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, nil, cfg, AdvanceParams{RunID: 200})
	require.NoError(t, err)
	assert.False(t, result.TierComplete)
	assert.Equal(t, 2, result.DispatchedCount)
	// Both ready issues should be dispatched
	assert.Contains(t, issueSvc.removedLabels[12], issues.StatusReady)
	assert.Contains(t, issueSvc.addedLabels[12], issues.StatusInProgress)
	assert.Contains(t, issueSvc.removedLabels[13], issues.StatusReady)
	assert.Contains(t, issueSvc.addedLabels[13], issues.StatusInProgress)
	assert.Len(t, wf.dispatched, 2)
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
	assert.NoError(t, err)
	assert.True(t, result.ConflictDetected)
	assert.Contains(t, issueSvc.comments[42][0], "Merge conflict detected")
	// Should relabel from done → failed to block tier advancement
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestConsolidate_ConflictNotify_MentionsUsers(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	_, g := initConflictRepo(t)

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
		Monitor:    config.Monitor{NotifyUsers: []string{"alice", "bob"}},
	}

	_, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 100})
	assert.NoError(t, err)
	assert.Contains(t, issueSvc.comments[42][0], "@alice")
	assert.Contains(t, issueSvc.comments[42][0], "@bob")
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
	// Original issue should be relabeled from done → failed to block tier advancement
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
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
	// Should relabel from done → failed to block tier advancement
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestConsolidate_ConflictMaxAttempts_MentionsUsers(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
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
		Monitor:    config.Monitor{NotifyUsers: []string{"alice"}},
	}

	_, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.Contains(t, issueSvc.comments[42][0], "@alice")
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
func initConflictRepo(t *testing.T) (string, *git.Git) { //nolint:unparam // dir used by some callers
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

func TestIsIssueComplete(t *testing.T) {
	tests := []struct {
		name     string
		issue    *platform.Issue
		expected bool
	}{
		{"closed issue", &platform.Issue{State: "closed", Labels: []string{}}, true},
		{"done label", &platform.Issue{State: "open", Labels: []string{issues.StatusDone}}, true},
		{"closed with done", &platform.Issue{State: "closed", Labels: []string{issues.StatusDone}}, true},
		{"open in-progress", &platform.Issue{State: "open", Labels: []string{issues.StatusInProgress}}, false},
		{"open ready", &platform.Issue{State: "open", Labels: []string{issues.StatusReady}}, false},
		{"open blocked", &platform.Issue{State: "open", Labels: []string{issues.StatusBlocked}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isIssueComplete(tt.issue))
		})
	}
}

func TestAdvance_SkipsManualTasks(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 11, Title: "Manual Task", Labels: []string{issues.StatusBlocked, issues.TypeManual},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo B manually\n"},
		{Number: 12, Title: "Auto Task", Labels: []string{issues.StatusBlocked},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo C\n"},
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
	assert.True(t, result.TierComplete)
	// Only issue 12 should be dispatched, not 11 (manual)
	assert.Equal(t, 1, result.DispatchedCount)
	assert.Len(t, wf.dispatched, 1)
	assert.Equal(t, "12", wf.dispatched[0]["issue_number"])
	// Manual task should be unblocked (blocked removed, ready added)
	assert.Contains(t, issueSvc.removedLabels[11], issues.StatusBlocked)
	assert.Contains(t, issueSvc.addedLabels[11], issues.StatusReady)
}

func TestAdvance_ClosedIssueCountsAsComplete(t *testing.T) {
	issueSvc := newMockIssueService()
	// Issue 10 is closed but doesn't have herd/status:done label (manual task scenario)
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Manual Task", State: "closed",
		Labels:    []string{issues.TypeManual},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Manual Task", State: "closed", Labels: []string{issues.TypeManual},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo manually\n"},
		{Number: 11, Title: "Auto Task", Labels: []string{issues.StatusBlocked},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo auto\n"},
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
	assert.True(t, result.TierComplete)
	assert.Equal(t, 1, result.DispatchedCount)
}

func TestAdvanceByBatch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Manual Task", State: "closed", Labels: []string{issues.TypeManual},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo manually\n"},
		{Number: 11, Title: "Auto Task", Labels: []string{issues.StatusBlocked},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo auto\n"},
	}

	wf := &mockWorkflowService{
		listResult: []*platform.Run{},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       &mockPRService{},
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{
			getResult: map[int]*platform.Milestone{
				1: {Number: 1, Title: "Batch"},
			},
		},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := AdvanceByBatch(context.Background(), mock, nil, cfg, 1)
	require.NoError(t, err)
	assert.True(t, result.TierComplete)
	assert.Equal(t, 1, result.DispatchedCount)
	assert.Len(t, wf.dispatched, 1)
	assert.Equal(t, "11", wf.dispatched[0]["issue_number"])
}

func TestAdvance_AllComplete_PRAlreadyExists(t *testing.T) {
	// Test that when openBatchPR hits a 422 race (PR already exists),
	// it falls back to listing and returns the existing PR number.
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

	existingPR := &platform.PullRequest{Number: 42, Head: "herd/batch/1-batch"}
	prSvc := &mockPRService{
		onCreateErr: fmt.Errorf("creating pull request: A pull request already exists for owner:herd/batch/1-batch"),
	}

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

	// Create batch branch
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	g := git.New(dir)

	// Initially List returns empty (simulating the race: first List sees no PR),
	// but after Create fails with 422, the retry List will find the PR.
	// We use a counter to track calls.
	listCallCount := 0
	originalList := prSvc.listResult
	prSvc.listResult = nil // First List returns empty

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	// Override List to return empty first, then the existing PR on retry
	_ = originalList
	_ = listCallCount
	// The mock List always returns listResult. For the race scenario:
	// - First call to List (in openBatchPR) should return empty -> proceeds to Create
	// - Create fails with 422
	// - Second call to List (fallback) should return the existing PR
	// We need a stateful mock for this. Let's use a wrapper.
	statefulPR := &statefulMockPRService{
		inner:      prSvc,
		listCalls:  0,
		listByCall: map[int][]*platform.PullRequest{
			0: {},                            // first List: no PR found
			1: {existingPR},                  // second List (fallback): PR found
		},
	}
	mock.prs = statefulPR

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, g, cfg, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.AllComplete)
	assert.Equal(t, 42, result.BatchPRNumber)
}

func TestAdvance_AllComplete_PRAlreadyOpen(t *testing.T) {
	// Test that when a PR already exists (found by List), openBatchPR returns it without calling Create.
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

	existingPR := &platform.PullRequest{Number: 42, Head: "herd/batch/1-batch"}
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{existingPR},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "10"}},
		},
		listResult: []*platform.Run{},
	}

	// Create a repo with a bare origin
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("batch content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

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
	assert.Equal(t, 42, result.BatchPRNumber)
	// Create should NOT have been called since List found the existing PR
	assert.Nil(t, prSvc.created)
}

// statefulMockPRService wraps mockPRService but returns different List results on each call.
type statefulMockPRService struct {
	inner      *mockPRService
	listCalls  int
	listByCall map[int][]*platform.PullRequest
}

func (s *statefulMockPRService) Create(ctx context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	return s.inner.Create(ctx, title, body, head, base)
}
func (s *statefulMockPRService) Get(ctx context.Context, number int) (*platform.PullRequest, error) {
	return s.inner.Get(ctx, number)
}
func (s *statefulMockPRService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	result := s.listByCall[s.listCalls]
	s.listCalls++
	return result, nil
}
func (s *statefulMockPRService) Update(ctx context.Context, n int, t2, b *string) (*platform.PullRequest, error) {
	return s.inner.Update(ctx, n, t2, b)
}
func (s *statefulMockPRService) Merge(ctx context.Context, n int, m platform.MergeMethod) (*platform.MergeResult, error) {
	return s.inner.Merge(ctx, n, m)
}
func (s *statefulMockPRService) UpdateBranch(ctx context.Context, n int) error {
	return s.inner.UpdateBranch(ctx, n)
}
func (s *statefulMockPRService) AddComment(ctx context.Context, number int, body string) error {
	return s.inner.AddComment(ctx, number, body)
}
func (s *statefulMockPRService) ListReviewComments(ctx context.Context, n int) ([]*platform.ReviewComment, error) {
	return s.inner.ListReviewComments(ctx, n)
}
func (s *statefulMockPRService) GetDiff(ctx context.Context, n int) (string, error) {
	return s.inner.GetDiff(ctx, n)
}
func (s *statefulMockPRService) CreateReview(ctx context.Context, n int, body string, event platform.ReviewEvent) error {
	return s.inner.CreateReview(ctx, n, body, event)
}
func (s *statefulMockPRService) Close(ctx context.Context, number int) error {
	return s.inner.Close(ctx, number)
}

func TestConsolidate_CleansUpWorkerProgress(t *testing.T) {
	source, err := os.ReadFile("integrator.go")
	require.NoError(t, err)
	src := string(source)
	assert.Contains(t, src, ".herd/progress/",
		"Consolidate must clean up .herd/progress/ directory after merge")
	assert.Contains(t, src, "WORKER_PROGRESS.md",
		"Consolidate must clean up legacy WORKER_PROGRESS.md for backward compat")
	assert.Contains(t, src, "g.Rm(",
		"Should use g.Rm to remove the progress file")
	assert.Contains(t, src, `g.Commit(`,
		"Should use a separate commit to remove progress files")
}

func TestConsolidate_RmErrorsAreLogged(t *testing.T) {
	// Verify that RmDir and Rm errors are logged as warnings, not silently swallowed
	source, err := os.ReadFile("integrator.go")
	require.NoError(t, err)
	src := string(source)

	assert.Contains(t, src, `Warning: failed to git rm .herd/progress/`,
		"RmDir errors should be logged as warnings")
	assert.Contains(t, src, `Warning: failed to git rm WORKER_PROGRESS.md`,
		"Rm errors for legacy file should be logged as warnings")
}

func TestConsolidate_CommitFailureResetsIndex(t *testing.T) {
	// Verify that when Commit fails, ResetHead is called to clean up staged removals
	source, err := os.ReadFile("integrator.go")
	require.NoError(t, err)
	src := string(source)

	assert.Contains(t, src, `Warning: failed to commit progress file removal`,
		"Commit errors should be logged as warnings")
	assert.Contains(t, src, `ResetHead()`,
		"Index should be reset on commit failure to avoid dirty state affecting subsequent push")
}

func initPushFailRepo(t *testing.T) (string, *git.Git) {
	t.Helper()

	// Create bare repo as "origin"
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	// Clone to working dir
	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Initial commit
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch and push
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Create worker branch with non-conflicting change and push
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

	// Install a pre-receive hook on the bare repo that rejects pushes to the batch branch.
	// This simulates a non-fast-forward rejection after the merge succeeds locally.
	hookDir := filepath.Join(bareDir, "hooks")
	hookPath := filepath.Join(hookDir, "pre-receive")
	hookScript := "#!/bin/sh\nwhile read old new ref; do\n  case \"$ref\" in\n    refs/heads/herd/batch/*) exit 1;;\n  esac\ndone\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(hookScript), 0755))

	// Back in first clone, checkout batch branch
	runGit(t, dir, "checkout", "herd/batch/1-batch")

	return dir, git.New(dir)
}

func initStaleCheckoutRepo(t *testing.T) (string, *git.Git) {
	t.Helper()

	// Create bare repo as "origin"
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	// Clone to working dir
	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Initial commit
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch and push
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Create worker branch with non-conflicting change and push
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker content"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

	// Advance remote batch branch from a second clone
	dir2 := t.TempDir()
	runGit(t, "", "clone", bareDir, dir2)
	runGit(t, dir2, "config", "user.email", "test@test.com")
	runGit(t, dir2, "config", "user.name", "Test")
	runGit(t, dir2, "checkout", "herd/batch/1-batch")
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "other.txt"), []byte("other content"), 0644))
	runGit(t, dir2, "add", ".")
	runGit(t, dir2, "commit", "-m", "advance remote batch")
	runGit(t, dir2, "push", "origin", "herd/batch/1-batch")

	// Back in first clone, checkout batch branch (now stale/behind remote)
	runGit(t, dir, "checkout", "herd/batch/1-batch")

	return dir, git.New(dir)
}

func TestConsolidate_PushFailure_LabelsIssueFailed(t *testing.T) {
	dir, g := initPushFailRepo(t)

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
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	assert.False(t, result.Merged)

	// Verify relabeling
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)

	// Verify comment
	require.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "Could not push consolidated batch branch")
}

func TestConsolidate_CheckoutTracksRemote(t *testing.T) {
	dir, g := initStaleCheckoutRepo(t)

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
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	assert.True(t, result.Merged)
}

func TestConsolidate_SkipsAlreadyMergedWorkerBranch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// Create repo where worker branch is an ancestor of batch branch
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "push", "origin", "main")

	// Create worker branch with a commit
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.txt"), []byte("worker"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "worker change")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

	// Create batch branch that already contains the worker branch
	// (merge worker into batch so worker is an ancestor)
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "merge", "herd/worker/42-test")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	g := git.New(dir)

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/worker/42-test": true},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo: repoSvc,
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100})
	require.NoError(t, err)
	assert.True(t, result.NoOp, "should be NoOp when worker is already merged")
	assert.False(t, result.Merged, "should not report as merged")
	assert.Equal(t, "herd/worker/42-test", result.WorkerBranch)
	// DeleteBranch should have been called for the worker branch
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/42-test")
}

func TestCloseStaleConflictIssues(t *testing.T) {
	issueSvc := newMockIssueService()
	// Conflict issue whose worker branch is gone
	issueSvc.listResult = []*platform.Issue{
		{
			Number: 99, Title: "Resolve conflict: #42",
			State:  "open",
			Labels: []string{issues.TypeFix},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/42-test\n    - herd/batch/1-batch\n---\n\n## Task\nResolve conflict\n",
		},
	}

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{}, // worker branch is gone
	}

	mock := &mockPlatform{
		issues: issueSvc,
		repo:   repoSvc,
	}

	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	closeStaleConflictIssues(context.Background(), mock, ms)

	// Issue should be closed
	update, ok := issueSvc.updatedIssues[99]
	require.True(t, ok, "issue #99 should have been updated")
	require.NotNil(t, update.State)
	assert.Equal(t, "closed", *update.State)

	// Should have comment
	assert.Len(t, issueSvc.comments[99], 1)
	assert.Equal(t, "Automatically closed — batch branch is already up to date.", issueSvc.comments[99][0])
}

func TestCloseStaleConflictIssues_BranchStillExists(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{
			Number: 99, Title: "Resolve conflict: #42",
			State:  "open",
			Labels: []string{issues.TypeFix},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/42-test\n    - herd/batch/1-batch\n---\n\n## Task\nResolve conflict\n",
		},
	}

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/worker/42-test": true}, // branch still exists
	}

	mock := &mockPlatform{
		issues: issueSvc,
		repo:   repoSvc,
	}

	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	closeStaleConflictIssues(context.Background(), mock, ms)

	// Issue should NOT be closed
	_, ok := issueSvc.updatedIssues[99]
	assert.False(t, ok, "issue #99 should not have been updated")
	assert.Empty(t, issueSvc.comments[99], "no comment should be added")
}

func TestConsolidate_PushFailure_ReturnsSuccessWithWarning(t *testing.T) {
	dir, g := initPushFailRepo(t)

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
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err, "push failure should not return an error")
	assert.False(t, result.Merged, "Merged should be false on push failure")
	assert.Equal(t, 42, result.IssueNumber)
	assert.Equal(t, "herd/worker/42-test", result.WorkerBranch)

	// Issue should be relabeled as failed
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)

	// Comment should be posted about push failure
	require.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "Could not push consolidated batch branch")
}

func TestConsolidate_MergeConflict_NotifyMode_ReturnsSuccessWithWarning(t *testing.T) {
	_, g := initConflictRepo(t)

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
			branchExists:  map[string]bool{"herd/worker/42-test": true},
		},
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "notify"},
	}

	result, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 100})
	require.NoError(t, err, "merge conflict in notify mode should not return an error")
	assert.True(t, result.ConflictDetected, "ConflictDetected should be true")
	assert.Equal(t, 42, result.IssueNumber)

	// Issue should be relabeled from done → failed
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)

	// Comment should be posted about the conflict
	require.Len(t, issueSvc.comments[42], 1)
	assert.Contains(t, issueSvc.comments[42][0], "Merge conflict detected")
}

func TestConsolidate_ProgressOnlyWorkerBranch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	// Create repo with batch and worker branches
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runGit(t, "", "clone", bareDir, dir)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "push", "origin", "main")

	// Create batch branch
	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	// Create worker branch with ONLY progress files
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/42-test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "WORKER_PROGRESS.md"), []byte("- [x] done"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".herd", "progress"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "progress", "42.md"), []byte("progress"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "progress files only")
	runGit(t, dir, "push", "origin", "herd/worker/42-test")

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

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{
		RunID:    100,
		RepoRoot: dir,
	})
	require.NoError(t, err)
	assert.True(t, result.Merged)

	// Progress files should be removed
	_, statErr := os.Stat(filepath.Join(dir, "WORKER_PROGRESS.md"))
	assert.True(t, os.IsNotExist(statErr), "WORKER_PROGRESS.md should be removed")
	_, statErr = os.Stat(filepath.Join(dir, ".herd", "progress", "42.md"))
	assert.True(t, os.IsNotExist(statErr), ".herd/progress/42.md should be removed")

	// Repo should be clean
	dirty, err := g.IsDirty()
	require.NoError(t, err)
	assert.False(t, dirty, "repo should be clean after consolidation")
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

func TestDispatchRebaseConflictWorker_CreatesIssueAndDispatches(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 555}
	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Batch 1"}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	issueNum, err := DispatchRebaseConflictWorker(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)
	assert.Equal(t, 555, issueNum)
	assert.Len(t, wf.dispatched, 1)
	assert.Equal(t, "555", wf.dispatched[0]["issue_number"])
}

func TestDispatchRebaseConflictWorker_AtCap(t *testing.T) {
	issueSvc := newMockIssueService()
	// Simulate existing conflict resolution issue
	issueSvc.listResult = []*platform.Issue{
		{Number: 100, Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  conflict_resolution: true\n---\n\n## Task\nResolve"},
	}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  &mockWorkflowService{},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Batch 1"}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 1},
	}

	issueNum, err := DispatchRebaseConflictWorker(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)
	assert.Equal(t, 0, issueNum)
}

func TestDispatchRebaseConflictWorker_TaskDescriptionContainsGitInstructions(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 555}
	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Batch 1"}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}

	_, err := DispatchRebaseConflictWorker(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)

	assert.Len(t, wf.dispatched, 1)

	// Verify the created issue body contains git rebase instructions
	body := issueSvc.createdBody
	assert.Contains(t, body, "git fetch origin")
	assert.Contains(t, body, "git checkout herd/batch/1-batch")
	assert.Contains(t, body, "git rebase origin/main")
	assert.Contains(t, body, "git push --force origin herd/batch/1-batch")
	assert.Contains(t, body, "conflict markers")
	assert.Contains(t, body, "git rebase --continue")
}

func TestRetryConflictOriginIssues(t *testing.T) {
	// Issue 50 is a conflict resolution issue referencing worker branch for issue 42
	conflictIssue := &platform.Issue{
		Number: 50, Title: "Resolve conflict: #42",
		Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/42-some-task\n    - herd/batch/1-batch\n---\n\n## Task\nResolve conflict\n",
	}
	// Issue 42 is the original failed issue
	origIssue := &platform.Issue{
		Number: 42, Title: "Some task",
		Labels:    []string{issues.StatusFailed},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = origIssue

	wf := &mockWorkflowService{
		listResult: []*platform.Run{},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	retryConflictOriginIssues(context.Background(), mock, cfg, conflictIssue, "herd/batch/1-batch")

	// Original issue should be relabeled in-progress and dispatched
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusFailed)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusInProgress)
	assert.Len(t, wf.dispatched, 1)
}

func TestRetryConflictOriginIssues_SkipsNonFailed(t *testing.T) {
	conflictIssue := &platform.Issue{
		Number: 50, Title: "Resolve conflict: #42",
		Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/42-some-task\n    - herd/batch/1-batch\n---\n\n## Task\nResolve conflict\n",
	}
	origIssue := &platform.Issue{
		Number: 42, Title: "Some task",
		Labels:    []string{issues.StatusDone}, // already done, should skip
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}

	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = origIssue

	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	retryConflictOriginIssues(context.Background(), mock, cfg, conflictIssue, "herd/batch/1-batch")

	// Should NOT dispatch — issue is not failed
	assert.Empty(t, wf.dispatched)
}

func TestRetryConflictOriginIssues_SkipsNonConflictIssue(t *testing.T) {
	regularIssue := &platform.Issue{
		Number: 50, Title: "Regular task",
		Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo something\n",
	}

	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:    newMockIssueService(),
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{}

	retryConflictOriginIssues(context.Background(), mock, cfg, regularIssue, "herd/batch/1-batch")

	// Should NOT dispatch — not a conflict resolution issue
	assert.Empty(t, wf.dispatched)
}
