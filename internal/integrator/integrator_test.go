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
	issues             platform.IssueService
	prs                platform.PullRequestService
	workflows          *mockWorkflowService
	repo               *mockRepoService
	milestones         *mockMilestoneService
	checks             platform.CheckService
	authenticatedLogin string
	authenticatedErr   error
}

func (m *mockPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService { return m.prs }
func (m *mockPlatform) Workflows() platform.WorkflowService       { return m.workflows }
func (m *mockPlatform) Labels() platform.LabelService             { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService     { return m.milestones }
func (m *mockPlatform) Runners() platform.RunnerService           { return nil }
func (m *mockPlatform) Repository() platform.RepositoryService    { return m.repo }
func (m *mockPlatform) Checks() platform.CheckService             { return m.checks }
func (m *mockPlatform) AuthenticatedLogin(_ context.Context) (string, error) {
	return m.authenticatedLogin, m.authenticatedErr
}

type mockIssueService struct {
	getResult              map[int]*platform.Issue
	listResult             []*platform.Issue
	addedLabels            map[int][]string
	removedLabels          map[int][]string
	updatedIssues          map[int]platform.IssueUpdate
	comments               map[int][]string
	storedComments         map[int][]*platform.Comment
	nextCommentID          int64
	listCommentsResult     []*platform.Comment
	listCommentsErr        error
	createResult           *platform.Issue
	createErr              error
	removeLabelsErr        error
	createdTitle           string
	createdBody            string
	respectCanceledContext bool
}

func newMockIssueService() *mockIssueService {
	return &mockIssueService{
		getResult:      make(map[int]*platform.Issue),
		addedLabels:    make(map[int][]string),
		removedLabels:  make(map[int][]string),
		updatedIssues:  make(map[int]platform.IssueUpdate),
		comments:       make(map[int][]string),
		storedComments: make(map[int][]*platform.Comment),
		nextCommentID:  1,
	}
}

func (m *mockIssueService) Create(ctx context.Context, title, body string, _ []string, _ *int) (*platform.Issue, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
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
func (m *mockIssueService) AddLabels(ctx context.Context, number int, labels []string) error {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	m.addedLabels[number] = append(m.addedLabels[number], labels...)
	return nil
}
func (m *mockIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = append(m.removedLabels[number], labels...)
	if m.removeLabelsErr != nil {
		return m.removeLabelsErr
	}
	return nil
}
func (m *mockIssueService) AddComment(ctx context.Context, number int, body string) error {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	m.comments[number] = append(m.comments[number], body)
	return nil
}
func (m *mockIssueService) AddCommentReturningID(ctx context.Context, number int, body string) (int64, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	id := m.nextCommentID
	m.nextCommentID++
	m.comments[number] = append(m.comments[number], body)
	m.storedComments[number] = append(m.storedComments[number], &platform.Comment{ID: id, Body: body})
	return id, nil
}
func (m *mockIssueService) UpdateComment(_ context.Context, commentID int64, body string) error {
	for number, comments := range m.storedComments {
		for _, c := range comments {
			if c.ID == commentID {
				oldBody := c.Body
				c.Body = body
				for j := range m.comments[number] {
					if m.comments[number][j] == oldBody {
						m.comments[number][j] = body
						break
					}
				}
				return nil
			}
		}
	}
	return nil
}
func (m *mockIssueService) DeleteComment(_ context.Context, commentID int64) error {
	for number, comments := range m.storedComments {
		for i, c := range comments {
			if c.ID != commentID {
				continue
			}
			m.storedComments[number] = append(comments[:i], comments[i+1:]...)
			for j := range m.comments[number] {
				if m.comments[number][j] == c.Body {
					m.comments[number] = append(m.comments[number][:j], m.comments[number][j+1:]...)
					break
				}
			}
			return nil
		}
	}
	return nil
}
func (m *mockIssueService) ListComments(_ context.Context, number int) ([]*platform.Comment, error) {
	if m.listCommentsErr != nil {
		return nil, m.listCommentsErr
	}
	result := append([]*platform.Comment{}, m.storedComments[number]...)
	result = append(result, m.listCommentsResult...)
	return result, nil
}
func (m *mockIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

type mockPRService struct {
	listResult             []*platform.PullRequest
	getResult              map[int]*platform.PullRequest
	created                *platform.PullRequest
	merged                 bool
	mergedNumber           int
	mergeMethod            platform.MergeMethod
	diffResult             string
	diffErr                error
	listFilesResult        []*platform.PullRequestFile
	listFilesErr           error
	getDiffCalled          bool
	listFilesCalled        bool
	comments               map[int][]string
	reviews                []capturedReview
	onCreateErr            error // if set, Create returns this error
	respectCanceledContext bool
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
func (m *mockPRService) Merge(ctx context.Context, number int, method platform.MergeMethod) (*platform.MergeResult, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	m.merged = true
	m.mergedNumber = number
	m.mergeMethod = method
	return &platform.MergeResult{Merged: true}, nil
}
func (m *mockPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *mockPRService) AddComment(ctx context.Context, number int, body string) error {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if m.comments == nil {
		m.comments = map[int][]string{}
	}
	m.comments[number] = append(m.comments[number], body)
	return nil
}
func (m *mockPRService) ListReviewComments(_ context.Context, _ int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (m *mockPRService) ListFiles(_ context.Context, _ int) ([]*platform.PullRequestFile, error) {
	m.listFilesCalled = true
	if m.listFilesErr != nil {
		return nil, m.listFilesErr
	}
	return m.listFilesResult, nil
}
func (m *mockPRService) GetDiff(_ context.Context, _ int) (string, error) {
	m.getDiffCalled = true
	if m.diffErr != nil {
		return "", m.diffErr
	}
	if m.diffResult != "" {
		return m.diffResult, nil
	}
	return "diff --git a/file.go b/file.go\n", nil
}
func (m *mockPRService) CreateReview(ctx context.Context, _ int, body string, event platform.ReviewEvent) error {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	m.reviews = append(m.reviews, capturedReview{body: body, event: event})
	return nil
}
func (m *mockPRService) Close(_ context.Context, _ int) error {
	return nil
}

type mockWorkflowService struct {
	runs                   map[int64]*platform.Run
	listResult             []*platform.Run
	listResultByStatus     map[string][]*platform.Run // optional: keyed by RunFilters.Status
	dispatched             []map[string]string
	onDispatch             func() // optional; called before recording each dispatch
	lastListRunFilter      platform.RunFilters
	listRunFilters         []platform.RunFilters
	respectCanceledContext bool
}

func (m *mockWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockWorkflowService) Dispatch(ctx context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
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
	m.listRunFilters = append(m.listRunFilters, filters)
	if m.listResultByStatus != nil {
		return m.listResultByStatus[filters.Status], nil
	}
	return m.listResult, nil
}
func (m *mockWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }
func (m *mockWorkflowService) GetRunDiagnostics(_ context.Context, _ int64) (*platform.WorkflowRunDiagnostics, error) {
	return nil, nil
}

type mockRepoService struct {
	defaultBranch          string
	branchExists           map[string]bool
	branchSHAs             map[string]string
	commitMessages         map[string]string
	commitParents          map[string]string
	markerCommitSeq        int
	deletedBranch          string
	deletedBranches        []string
	onGetBranchSHA         func(name string)
	onUpdateBranch         func(name, sha string)
	updateConflicts        int
	respectCanceledContext bool
}

func (m *mockRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (m *mockRepoService) GetDefaultBranch(ctx context.Context) (string, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	return m.defaultBranch, nil
}
func (m *mockRepoService) CreateBranch(_ context.Context, name, sha string) error {
	if m.branchExists == nil {
		return nil
	}
	if m.branchExists[name] {
		return fmt.Errorf("reference already exists")
	}
	m.branchExists[name] = true
	if m.branchSHAs == nil {
		m.branchSHAs = make(map[string]string)
	}
	m.branchSHAs[name] = sha
	return nil
}
func (m *mockRepoService) CreateBranchWithCommit(ctx context.Context, name, sha, message string) (string, error) {
	markerSHA, err := m.CreateCommit(ctx, sha, message)
	if err != nil {
		return "", err
	}
	if err := m.CreateBranch(ctx, name, markerSHA); err != nil {
		return "", err
	}
	return markerSHA, nil
}
func (m *mockRepoService) CreateCommit(ctx context.Context, parentSHA, message string) (string, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	m.markerCommitSeq++
	markerSHA := fmt.Sprintf("%s-lock-%d", parentSHA, m.markerCommitSeq)
	if m.commitMessages == nil {
		m.commitMessages = make(map[string]string)
	}
	if m.commitParents == nil {
		m.commitParents = make(map[string]string)
	}
	m.commitMessages[markerSHA] = message
	m.commitParents[markerSHA] = parentSHA
	return markerSHA, nil
}
func (m *mockRepoService) GetCommitMessage(_ context.Context, sha string) (string, error) {
	if m.commitMessages != nil {
		if msg, ok := m.commitMessages[sha]; ok {
			return msg, nil
		}
	}
	return "", fmt.Errorf("commit %s not found", sha)
}
func (m *mockRepoService) UpdateBranchToCommit(ctx context.Context, name, sha string, _ bool) error {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if m.onUpdateBranch != nil {
		m.onUpdateBranch(name, sha)
	}
	if m.updateConflicts > 0 {
		m.updateConflicts--
		return platform.ErrRefUpdateConflict
	}
	if m.branchExists == nil {
		m.branchExists = make(map[string]bool)
	}
	if m.branchSHAs == nil {
		m.branchSHAs = make(map[string]string)
	}
	if !m.branchExists[name] || m.branchSHAs[name] != m.commitParents[sha] {
		return platform.ErrRefUpdateConflict
	}
	m.branchExists[name] = true
	m.branchSHAs[name] = sha
	return nil
}
func (m *mockRepoService) DeleteBranch(_ context.Context, name string) error {
	m.deletedBranch = name
	m.deletedBranches = append(m.deletedBranches, name)
	if m.branchExists != nil {
		delete(m.branchExists, name)
	}
	if m.branchSHAs != nil {
		delete(m.branchSHAs, name)
	}
	return nil
}
func (m *mockRepoService) GetBranchSHA(ctx context.Context, name string) (string, error) {
	if m.respectCanceledContext {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if m.onGetBranchSHA != nil {
		m.onGetBranchSHA(name)
	}
	if m.branchExists != nil {
		if m.branchExists[name] {
			if m.branchSHAs != nil && m.branchSHAs[name] != "" {
				return m.branchSHAs[name], nil
			}
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
		Labels:    []string{issues.StatusDone},
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
		Labels:    []string{issues.StatusDone},
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
		Labels:    []string{issues.StatusDone},
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
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
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
		issues:    mockCreate,
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

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	_, g := initConflictRepo(t)

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
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
	// New detailed cascade comment goes on the batch PR, not the issue. PR
	// comments are posted via the Issues service (GitHub conflates them).
	require.NotEmpty(t, issueSvc.comments[500])
	assert.Contains(t, issueSvc.comments[500][0], "Conflict resolution cascade failed")
	assert.Contains(t, issueSvc.addedLabels[500], issues.CascadeFailed)
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

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	_, g := initConflictRepo(t)

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
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
	require.NotEmpty(t, issueSvc.comments[500])
	assert.Contains(t, issueSvc.comments[500][0], "@alice")
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
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
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
		inner:     prSvc,
		listCalls: 0,
		listByCall: map[int][]*platform.PullRequest{
			0: {},           // first List: no PR found
			1: {existingPR}, // second List (fallback): PR found
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
func (s *statefulMockPRService) ListFiles(ctx context.Context, n int) ([]*platform.PullRequestFile, error) {
	return s.inner.ListFiles(ctx, n)
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
			Body:   "---\nherd:\n  version: 1\n  batch: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/42-test\n    - herd/batch/1-batch\n---\n\n## Task\nResolve conflict\n",
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
			Body:   "---\nherd:\n  version: 1\n  batch: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/42-test\n    - herd/batch/1-batch\n---\n\n## Task\nResolve conflict\n",
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
		Labels:    []string{issues.StatusDone},
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

// initMultiWorkerRepo creates a bare-origin git repo, a batch branch for the
// given milestone slug, and one worker branch per (issueNumber, slug) pair.
// Each worker branch adds a unique file `worker<N>.txt` so callers can assert
// per-branch merges via filesystem state rather than mocking *git.Git.
func initMultiWorkerRepo(t *testing.T, batchBranch string, workers []struct {
	num  int
	slug string
}) (string, *git.Git) {
	t.Helper()

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

	runGit(t, dir, "checkout", "-b", batchBranch)
	runGit(t, dir, "push", "origin", batchBranch)

	for _, w := range workers {
		runGit(t, dir, "checkout", "main")
		branch := fmt.Sprintf("herd/worker/%d-%s", w.num, w.slug)
		runGit(t, dir, "checkout", "-b", branch)
		f := fmt.Sprintf("worker%d.txt", w.num)
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(fmt.Sprintf("worker %d", w.num)), 0644))
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-m", fmt.Sprintf("Complete #%d", w.num))
		runGit(t, dir, "push", "origin", branch)
	}

	runGit(t, dir, "checkout", batchBranch)
	return dir, git.New(dir)
}

// TestConsolidate_PicksUpStrandedDoneWorkers verifies that a single Consolidate
// call merges every done-labeled worker branch in the milestone, not just the
// triggering issue. This makes consolidation idempotent and self-healing.
func TestConsolidate_PicksUpStrandedDoneWorkers(t *testing.T) {
	ms := &platform.Milestone{Number: 5, Title: "multi"}
	batchBranch := "herd/batch/5-multi"

	dir, g := initMultiWorkerRepo(t, batchBranch, []struct {
		num  int
		slug string
	}{
		{100, "task-a"},
		{101, "task-b"},
		{102, "task-c"},
	})

	issue100 := &platform.Issue{Number: 100, Title: "Task A", Labels: []string{issues.StatusDone}, Milestone: ms}
	issue101 := &platform.Issue{Number: 101, Title: "Task B", Labels: []string{issues.StatusDone}, Milestone: ms}
	issue102 := &platform.Issue{Number: 102, Title: "Task C", Labels: []string{issues.StatusDone}, Milestone: ms}

	issueSvc := newMockIssueService()
	issueSvc.getResult[100] = issue100
	issueSvc.getResult[101] = issue101
	issueSvc.getResult[102] = issue102
	issueSvc.listResult = []*platform.Issue{issue100, issue101, issue102}

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists: map[string]bool{
			"herd/worker/100-task-a": true,
			"herd/worker/101-task-b": true,
			"herd/worker/102-task-c": true,
		},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				200: {ID: 200, Conclusion: "success", Inputs: map[string]string{"issue_number": "101"}},
			},
		},
		repo: repoSvc,
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 200, RepoRoot: dir})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 101, result.IssueNumber)
	assert.True(t, result.Merged, "trigger result should report Merged=true")

	// All three worker.txt files must be present on the batch branch.
	for _, n := range []int{100, 101, 102} {
		_, statErr := os.Stat(filepath.Join(dir, fmt.Sprintf("worker%d.txt", n)))
		assert.NoError(t, statErr, "worker%d.txt should be on batch branch (merge happened)", n)
	}

	// All three worker branches must have been deleted on the platform side.
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/100-task-a")
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/101-task-b")
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/102-task-c")
}

// TestConsolidate_SkipsFailedIssues verifies that worker branches whose issues
// are labeled failed (or any non-done status) are NOT merged or deleted, even
// when their branches still exist on the remote.
func TestConsolidate_SkipsFailedIssues(t *testing.T) {
	ms := &platform.Milestone{Number: 5, Title: "multi"}
	batchBranch := "herd/batch/5-multi"

	dir, g := initMultiWorkerRepo(t, batchBranch, []struct {
		num  int
		slug string
	}{
		{200, "task-a"},
		{201, "task-b"},
		{202, "task-c"},
	})

	issue200 := &platform.Issue{Number: 200, Title: "Task A", Labels: []string{issues.StatusDone}, Milestone: ms}
	issue201 := &platform.Issue{Number: 201, Title: "Task B", Labels: []string{issues.StatusFailed}, Milestone: ms}
	issue202 := &platform.Issue{Number: 202, Title: "Task C", Labels: []string{issues.StatusDone}, Milestone: ms}

	issueSvc := newMockIssueService()
	issueSvc.getResult[200] = issue200
	issueSvc.getResult[201] = issue201
	issueSvc.getResult[202] = issue202
	issueSvc.listResult = []*platform.Issue{issue200, issue201, issue202}

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists: map[string]bool{
			"herd/worker/200-task-a": true,
			"herd/worker/201-task-b": true,
			"herd/worker/202-task-c": true,
		},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				300: {ID: 300, Conclusion: "success", Inputs: map[string]string{"issue_number": "200"}},
			},
		},
		repo: repoSvc,
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 300, RepoRoot: dir})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Merged)

	// #200 and #202 must be on the batch branch; #201 must NOT.
	_, statErr200 := os.Stat(filepath.Join(dir, "worker200.txt"))
	assert.NoError(t, statErr200, "worker200.txt should be merged (issue done)")
	_, statErr202 := os.Stat(filepath.Join(dir, "worker202.txt"))
	assert.NoError(t, statErr202, "worker202.txt should be merged (issue done)")
	_, statErr201 := os.Stat(filepath.Join(dir, "worker201.txt"))
	assert.True(t, os.IsNotExist(statErr201), "worker201.txt must NOT be merged (issue failed)")

	// #201's worker branch must NOT have been deleted; #200 and #202 must have been.
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/200-task-a")
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/202-task-c")
	assert.NotContains(t, repoSvc.deletedBranches, "herd/worker/201-task-b")
}

// TestConsolidate_HandlesPartialConflict verifies that when one worker branch
// hits a merge conflict, the loop continues processing the remaining
// candidates instead of aborting, and a resolver issue is dispatched for the
// conflicting branch only.
func TestConsolidate_HandlesPartialConflict(t *testing.T) {
	ms := &platform.Milestone{Number: 5, Title: "multi"}
	batchBranch := "herd/batch/5-multi"

	// Build a custom repo: batch has shared.txt, #301 also modifies shared.txt
	// (conflicts), #300 and #302 add unique non-conflicting files.
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

	runGit(t, dir, "checkout", "-b", batchBranch)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from batch"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "batch change")
	runGit(t, dir, "push", "origin", batchBranch)

	// Worker #300 (non-conflicting): adds worker300.txt.
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/300-task-a")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker300.txt"), []byte("300"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "Complete #300")
	runGit(t, dir, "push", "origin", "herd/worker/300-task-a")

	// Worker #301 (conflicting): rewrites shared.txt.
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/301-task-b")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from worker 301"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "Complete #301")
	runGit(t, dir, "push", "origin", "herd/worker/301-task-b")

	// Worker #302 (non-conflicting): adds worker302.txt.
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/302-task-c")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker302.txt"), []byte("302"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "Complete #302")
	runGit(t, dir, "push", "origin", "herd/worker/302-task-c")

	runGit(t, dir, "checkout", batchBranch)
	g := git.New(dir)

	issue300 := &platform.Issue{Number: 300, Title: "Task A", Labels: []string{issues.StatusDone}, Milestone: ms}
	issue301 := &platform.Issue{Number: 301, Title: "Task B", Labels: []string{issues.StatusDone}, Milestone: ms}
	issue302 := &platform.Issue{Number: 302, Title: "Task C", Labels: []string{issues.StatusDone}, Milestone: ms}

	issueSvc := newMockIssueService()
	issueSvc.getResult[300] = issue300
	issueSvc.getResult[301] = issue301
	issueSvc.getResult[302] = issue302
	issueSvc.listResult = []*platform.Issue{issue300, issue301, issue302}

	createdIssues := []*platform.Issue{}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, _ string, _ []string, _ *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: 999, Title: title}
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists: map[string]bool{
			"herd/worker/300-task-a": true,
			"herd/worker/301-task-b": true,
			"herd/worker/302-task-c": true,
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			400: {ID: 400, Conclusion: "success", Inputs: map[string]string{"issue_number": "300"}},
		},
	}

	mock := &mockPlatform{
		issues:    mockCreate,
		workflows: wf,
		repo:      repoSvc,
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "dispatch-resolver", MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := Consolidate(context.Background(), mock, g, cfg, ConsolidateParams{RunID: 400, RepoRoot: dir})
	require.NoError(t, err, "loop should not abort on a single conflict under dispatch-resolver")
	require.NotNil(t, result)

	// Trigger #300 should report a successful merge.
	assert.Equal(t, 300, result.IssueNumber)
	assert.True(t, result.Merged)

	// Both non-conflicting workers must have been merged — proves the loop did
	// not abort after the conflict in the middle.
	_, statErr300 := os.Stat(filepath.Join(dir, "worker300.txt"))
	assert.NoError(t, statErr300, "worker300.txt should be merged")
	_, statErr302 := os.Stat(filepath.Join(dir, "worker302.txt"))
	assert.NoError(t, statErr302, "worker302.txt should be merged AFTER the #301 conflict (loop continues)")

	// A resolver issue must have been created — exactly one (only #301 conflicted).
	require.Len(t, createdIssues, 1, "exactly one resolver issue should have been created")
	assert.Contains(t, createdIssues[0].Title, "Resolve conflict: #301")

	// Successful workers' branches deleted; conflicting one not deleted.
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/300-task-a")
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/302-task-c")
	assert.NotContains(t, repoSvc.deletedBranches, "herd/worker/301-task-b",
		"conflicting branch must NOT be deleted — resolver issue references it")

	// #301 should be relabeled from done → failed.
	assert.Contains(t, issueSvc.removedLabels[301], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[301], issues.StatusFailed)

	// A resolver worker must have been dispatched.
	assert.Len(t, wf.dispatched, 1, "exactly one resolver worker dispatch (for #301's conflict)")
}

// TestConsolidate_TriggeringIssueAlreadyMerged covers the case where the
// triggering issue's worker branch is already an ancestor of the batch branch
// (e.g. a previous integrator run merged it but failed to delete the branch).
// The loop should still pick up other unmerged candidates in the milestone.
func TestConsolidate_TriggeringIssueAlreadyMerged(t *testing.T) {
	ms := &platform.Milestone{Number: 5, Title: "multi"}
	batchBranch := "herd/batch/5-multi"

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

	// Create worker #400 with a unique file.
	runGit(t, dir, "checkout", "-b", "herd/worker/400-task-a")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker400.txt"), []byte("400"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "Complete #400")
	runGit(t, dir, "push", "origin", "herd/worker/400-task-a")

	// Create worker #401 with a different unique file off main (NOT yet merged).
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "herd/worker/401-task-b")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker401.txt"), []byte("401"), 0644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "Complete #401")
	runGit(t, dir, "push", "origin", "herd/worker/401-task-b")

	// Build batch branch already containing worker #400 (its tip is an ancestor).
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", batchBranch)
	runGit(t, dir, "merge", "--ff-only", "herd/worker/400-task-a")
	runGit(t, dir, "push", "origin", batchBranch)

	g := git.New(dir)

	issue400 := &platform.Issue{Number: 400, Title: "Task A", Labels: []string{issues.StatusDone}, Milestone: ms}
	issue401 := &platform.Issue{Number: 401, Title: "Task B", Labels: []string{issues.StatusDone}, Milestone: ms}

	issueSvc := newMockIssueService()
	issueSvc.getResult[400] = issue400
	issueSvc.getResult[401] = issue401
	issueSvc.listResult = []*platform.Issue{issue400, issue401}

	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists: map[string]bool{
			"herd/worker/400-task-a": true,
			"herd/worker/401-task-b": true,
		},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				500: {ID: 500, Conclusion: "success", Inputs: map[string]string{"issue_number": "400"}},
			},
		},
		repo: repoSvc,
	}

	result, err := Consolidate(context.Background(), mock, g, &config.Config{}, ConsolidateParams{RunID: 500, RepoRoot: dir})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Trigger result reflects the trigger's issue number even though its merge
	// was a no-op (already contained).
	assert.Equal(t, 400, result.IssueNumber)

	// worker401.txt must now be on batch (proves #401 was merged after the
	// trigger's already-merged short-circuit).
	_, statErr := os.Stat(filepath.Join(dir, "worker401.txt"))
	assert.NoError(t, statErr, "worker401.txt should be merged into batch")

	// Both worker branches deleted: #400 because it was already contained
	// (already-merged short-circuit deletes), #401 because it was just merged.
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/400-task-a")
	assert.Contains(t, repoSvc.deletedBranches, "herd/worker/401-task-b")
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

func TestDispatchRebaseConflictWorker_TaskBodyKeepsAgentOnWorkerBranch(t *testing.T) {
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

	body := issueSvc.createdBody
	// The new body tells the agent to stay on its own worker branch and merge
	// origin/<defaultBranch>. It must NOT contain positive checkout steps,
	// rebase commands, or force-push commands.
	assertBodyInstructionsKeepAgentOnWorkerBranch(t, body, []string{
		"Do NOT push",
		"Stay on your current worker branch",
		"git merge origin/main",
		"git fetch origin",
		"conflict markers",
		// Negation warnings: prove the fix is in place. The body says
		// `do NOT run \`git checkout <batch>\` or \`git checkout <default>\``.
		"do NOT run `git checkout herd/batch/1-batch`",
		"or `git checkout main`",
	}, []string{
		// No positive `git checkout`/`git rebase`/`git push --force` instructions
		// (they would appear as a numbered step `<N>. \`git ...\``).
		"1. `git checkout",
		"2. `git checkout",
		"3. `git checkout",
		"`git rebase ",
		"`git push --force",
	})
}

func TestHandleRebaseConflictResolution_BlockedByCascadeLabel(t *testing.T) {
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{issues.CascadeFailed}},
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
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 3}}

	err := handleRebaseConflictResolution(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)

	// Must not create a new issue and must not dispatch a worker.
	assert.Empty(t, issueSvc.createdBody, "no rebase resolver issue must be created when batch PR is in cascade-failed state")
	assert.Empty(t, wf.dispatched, "no workflow dispatch when blocked")
	// Must post the paused-state comment on the batch PR.
	require.NotEmpty(t, issueSvc.comments[500])
	assert.Contains(t, issueSvc.comments[500][0], "Conflict resolution is paused")
}

func TestHandleRebaseConflictResolution_DispatchesWhenNotBlocked(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 777}
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
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
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	err := handleRebaseConflictResolution(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)

	// Dispatches when not blocked.
	assert.NotEmpty(t, issueSvc.createdBody, "rebase resolver issue must be created when not blocked")
	assert.Len(t, wf.dispatched, 1)
}

func TestMarkCascadeFailed_RebaseKindUsesResolverWorkerBranchWording(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
		},
	}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 2}}
	issue := &platform.Issue{Number: 200, Title: "Resolve rebase conflict", Milestone: ms, Labels: []string{issues.StatusDone}}

	// Caller passes the last resolver's worker branch — not the batch
	// branch — because force-pushing the batch branch is forbidden.
	markCascadeFailed(context.Background(), mock, cfg, ms, issue, "herd/worker/200-resolve-rebase-conflict", "main", cascadeKindRebase)

	require.NotEmpty(t, issueSvc.comments[500])
	body := issueSvc.comments[500][0]
	// Rebase-path wording: instructs the user to inspect/fix a worker
	// branch against the default branch. Must NOT instruct the user to
	// checkout or force-push the batch branch.
	assert.Contains(t, body, "Inspect the failing worker branch")
	assert.Contains(t, body, "git fetch origin && git checkout herd/worker/200-resolve-rebase-conflict")
	assert.Contains(t, body, "git push --force origin herd/worker/200-resolve-rebase-conflict")
	assert.Contains(t, body, "merge the latest `main` into the worker branch")
	assert.NotContains(t, body, "git checkout herd/batch/1-batch")
	assert.NotContains(t, body, "git push --force origin herd/batch/1-batch")
	assert.NotContains(t, body, "Rebase the batch branch")
	// Cascade-failed label still applied to PR.
	assert.Contains(t, issueSvc.addedLabels[500], issues.CascadeFailed)
}

func TestDispatchRebaseConflictWorker_CascadeUsesResolverWorkerBranch(t *testing.T) {
	// Two existing conflict-resolution issues (cap=2) — cascade triggers
	// at the cap. The recovery message must point at the LAST resolver's
	// worker branch, not the batch branch.
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 100, Title: "Resolve A", Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/batch/1-batch\n    - main\n---\n\n## Task\n"},
		{Number: 101, Title: "Resolve B", Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/batch/1-batch\n    - main\n---\n\n## Task\n"},
	}
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
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
	ms := &platform.Milestone{Number: 1, Title: "Batch 1"}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 2}}

	num, err := DispatchRebaseConflictWorker(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)
	assert.Equal(t, 0, num, "must return 0 at cap")
	assert.Empty(t, wf.dispatched, "no dispatch at cap")

	require.NotEmpty(t, issueSvc.comments[500])
	body := issueSvc.comments[500][0]
	// Points at the last resolver's worker branch (issue #101), not the batch.
	assert.Contains(t, body, "herd/worker/101-resolve-b")
	assert.Contains(t, body, "Inspect the failing worker branch")
	assert.NotContains(t, body, "git push --force origin herd/batch/1-batch")
	assert.NotContains(t, body, "Rebase the batch branch")
}

func TestMarkCascadeFailed_NilIssueOmitsCloseStep(t *testing.T) {
	// When issue is nil but the batch PR exists, the comment must not
	// include "close the original failing issue (#0)".
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
		},
	}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 2}}

	markCascadeFailed(context.Background(), mock, cfg, ms, nil, "herd/worker/42-test", "herd/batch/1-batch", cascadeKindMerge)

	require.NotEmpty(t, issueSvc.comments[500])
	body := issueSvc.comments[500][0]
	assert.NotContains(t, body, "#0)", "nil issue must not produce `#0` in the close step")
	assert.NotContains(t, body, "Or close the original failing issue", "step 3 must be omitted when no original issue is known")
	// Cascade-failed label still applied to PR.
	assert.Contains(t, issueSvc.addedLabels[500], issues.CascadeFailed)
}

// mockPRServiceWithErr returns a configurable error from List so we can
// verify the cascade-failed comment path logs and falls back when the
// batch-PR lookup transient-fails.
type mockPRServiceWithErr struct {
	mockPRService
	listErr error
}

func (m *mockPRServiceWithErr) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}

func TestMarkCascadeFailed_FindBatchPRErrorFallsBackToIssueComment(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	issueSvc := newMockIssueService()
	prSvc := &mockPRServiceWithErr{listErr: fmt.Errorf("simulated transient network failure")}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 2}}
	issue := &platform.Issue{Number: 42, Title: "Test", Milestone: ms, Labels: []string{issues.StatusDone}}

	markCascadeFailed(context.Background(), mock, cfg, ms, issue, "herd/worker/42-test", "herd/batch/1-batch", cascadeKindMerge)

	// Falls back to issue comment on lookup error.
	require.NotEmpty(t, issueSvc.comments[42])
	assert.Contains(t, issueSvc.comments[42][0], "Manual intervention required")
	// Issue still relabeled to failed.
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestParseWorkerBranchNumber(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   int
	}{
		{name: "valid worker branch", branch: "herd/worker/42-some-slug", want: 42},
		{name: "valid with multi-digit number", branch: "herd/worker/12345-foo", want: 12345},
		{name: "valid with single-digit number", branch: "herd/worker/1-foo", want: 1},
		{name: "no slug separator", branch: "herd/worker/42", want: 0},
		{name: "non-numeric prefix", branch: "herd/worker/foo-bar", want: 0},
		{name: "missing herd/worker prefix", branch: "feature/42-foo", want: 0},
		{name: "empty branch", branch: "", want: 0},
		{name: "batch branch", branch: "herd/batch/1-batch", want: 0},
		{name: "prefix not preceded by dash", branch: "herd/worker/-foo", want: 0},
		{name: "100 not matched by 10- prefix", branch: "herd/worker/100-foo", want: 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseWorkerBranchNumber(tc.branch))
		})
	}
}

func TestBuildCascadeChain_PrefixCollisionDoesNotMisidentifyParent(t *testing.T) {
	// Issue #10 has branch herd/worker/10-foo. Issue #100 has branch
	// herd/worker/100-foo. If we used `strings.HasPrefix` keyed on the
	// branch prefix, "herd/worker/100-foo" could match the prefix
	// "herd/worker/10-" depending on map iteration order. The exact-match
	// parser must resolve unambiguously.
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Body: "---\nherd:\n  version: 1\n---\n\n## Task\n"},
		{Number: 100, Body: "---\nherd:\n  version: 1\n---\n\n## Task\n"},
		{Number: 101, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-foo\n    - herd/batch/1-batch\n---\n\n## Task\n"},
	}
	mock := &mockPlatform{issues: issueSvc}

	current := &platform.Issue{Number: 101, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-foo\n    - herd/batch/1-batch\n---\n\n## Task\n"}
	chain, err := buildCascadeChain(context.Background(), mock, ms, current)
	require.NoError(t, err)
	assert.Equal(t, []int{100, 101}, chain, "parent must be #100, not #10, despite shared numeric prefix")
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

func TestAdvance_UnknownTriggerIssueReturnsNoOp(t *testing.T) {
	issueSvc := newMockIssueService()
	// Triggering issue #99 exists and has a milestone, but is NOT in listResult
	// (simulates partial API response where the issue is missing from the milestone listing).
	issueSvc.getResult[99] = &platform.Issue{
		Number: 99, Title: "Mystery Task",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		// Note: #99 is intentionally absent from the milestone list
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "99"}},
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
	require.NoError(t, err, "unknown trigger issue must NOT return an error")
	require.NotNil(t, result)
	assert.False(t, result.TierComplete)
	assert.False(t, result.AllComplete)
	assert.Equal(t, 0, result.DispatchedCount)
	assert.Equal(t, 0, result.BatchPRNumber)
	// No workers should have been dispatched
	assert.Empty(t, wf.dispatched)
}

func TestAdvanceByBatch_SkipsPROnPartialIssueList(t *testing.T) {
	tests := []struct {
		name         string
		openIssues   int
		closedIssues int
		listed       []*platform.Issue
		wantPRSkip   bool // true: PR creation must be skipped; false: PR is opened
	}{
		{
			name:         "partial list — fewer than expected",
			openIssues:   0,
			closedIssues: 3, // milestone reports 3 closed issues
			listed: []*platform.Issue{
				// Only 1 issue returned — clearly partial
				{Number: 10, Title: "Task A", State: "closed", Labels: []string{issues.StatusDone},
					Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
			},
			wantPRSkip: true,
		},
		{
			name:         "complete list — equal to expected",
			openIssues:   0,
			closedIssues: 1,
			listed: []*platform.Issue{
				{Number: 10, Title: "Task A", State: "closed", Labels: []string{issues.StatusDone},
					Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
			},
			wantPRSkip: false,
		},
		{
			name:         "list larger than expected — still proceeds",
			openIssues:   0,
			closedIssues: 1,
			listed: []*platform.Issue{
				{Number: 10, Title: "Task A", State: "closed", Labels: []string{issues.StatusDone},
					Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
				{Number: 11, Title: "Task B", State: "closed", Labels: []string{issues.StatusDone},
					Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo B\n"},
			},
			wantPRSkip: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.listResult = tc.listed

			prSvc := &mockPRService{}
			wf := &mockWorkflowService{listResult: []*platform.Run{}}

			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       prSvc,
				workflows: wf,
				repo:      &mockRepoService{defaultBranch: "main"},
				milestones: &mockMilestoneService{
					getResult: map[int]*platform.Milestone{
						1: {
							Number:       1,
							Title:        "Batch",
							OpenIssues:   tc.openIssues,
							ClosedIssues: tc.closedIssues,
						},
					},
				},
			}

			cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

			// On the complete-list paths, openBatchPR will run git operations
			// (fetch/checkout/rebase). Provide a real repo so they don't nil-panic.
			var g *git.Git
			if !tc.wantPRSkip {
				g = setupBatchRepo(t)
			}

			result, err := AdvanceByBatch(context.Background(), mock, g, cfg, 1)
			require.NoError(t, err)
			require.NotNil(t, result)

			if tc.wantPRSkip {
				assert.False(t, result.AllComplete, "AllComplete must be false when partial data is detected")
				assert.Equal(t, 0, result.BatchPRNumber, "no PR should be created on partial data")
				assert.Nil(t, prSvc.created, "PullRequests().Create must not be called")
			} else {
				assert.True(t, result.AllComplete, "AllComplete should be true when list is complete")
				assert.NotNil(t, prSvc.created, "PullRequests().Create must be called")
			}
		})
	}
}

// setupBatchRepo creates a bare-origin git repo with a herd/batch/1-batch
// branch suitable for tests that exercise openBatchPR's git operations.
func setupBatchRepo(t *testing.T) *git.Git {
	t.Helper()
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

	runGit(t, dir, "checkout", "-b", "herd/batch/1-batch")
	runGit(t, dir, "push", "origin", "herd/batch/1-batch")

	return git.New(dir)
}

func TestAdvance_AllComplete_SkipsPROnPartialIssueList(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[10] = &platform.Issue{
		Number: 10, Title: "Task A",
		Labels: []string{issues.StatusDone},
		Milestone: &platform.Milestone{
			Number:       1,
			Title:        "Batch",
			OpenIssues:   0,
			ClosedIssues: 5, // milestone reports 5 closed issues
		},
	}
	// Only 1 issue returned — partial response
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

	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}

	cfg := &config.Config{Workers: config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Advance(context.Background(), mock, nil, cfg, AdvanceParams{RunID: 100})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.AllComplete, "must NOT mark batch complete on partial data")
	assert.Equal(t, 0, result.BatchPRNumber)
	assert.Nil(t, prSvc.created, "PullRequests().Create must not be called")
}

// assertBodyInstructionsKeepAgentOnWorkerBranch asserts that a task body contains
// all expected substrings and contains none of the disallowed substrings.
func assertBodyInstructionsKeepAgentOnWorkerBranch(t *testing.T, body string, mustContain, mustNotContain []string) {
	t.Helper()
	for _, want := range mustContain {
		assert.Contains(t, body, want, "task body must contain %q", want)
	}
	for _, bad := range mustNotContain {
		assert.NotContains(t, body, bad, "task body must NOT contain %q", bad)
	}
}

func TestConsolidate_ConflictResolutionNoOp_RetriesOriginal(t *testing.T) {
	// Conflict-resolution issue #50: its worker branch is missing.
	// Original failed issue #100 should be re-dispatched.
	conflictBody := "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-original\n    - herd/batch/1-foo\n---\n\n## Task\nResolve conflict\n"

	conflictIssue := &platform.Issue{
		Number: 50, Title: "Resolve conflict: #100",
		Body:      conflictBody,
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Foo"},
	}
	origIssue := &platform.Issue{
		Number: 100, Title: "Original",
		Labels:    []string{issues.StatusFailed},
		Milestone: &platform.Milestone{Number: 1, Title: "Foo"},
	}

	issueSvc := newMockIssueService()
	issueSvc.getResult[50] = conflictIssue
	issueSvc.getResult[100] = origIssue

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			200: {ID: 200, Conclusion: "success", Inputs: map[string]string{"issue_number": "50"}},
		},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{}, // conflict-resolution worker branch doesn't exist
		},
	}

	cfg := &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Consolidate(context.Background(), mock, nil, cfg, ConsolidateParams{RunID: 200})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.NoOp, "should be NoOp when worker branch is missing")
	assert.False(t, result.Merged, "should not have merged")

	// Original issue #100 should have been relabeled and re-dispatched.
	assert.Contains(t, issueSvc.removedLabels[100], issues.StatusFailed,
		"original failed issue must have herd/status:failed removed")
	assert.Contains(t, issueSvc.addedLabels[100], issues.StatusInProgress,
		"original failed issue must have herd/status:in-progress added")

	require.Len(t, wf.dispatched, 1, "exactly one dispatch should fire (for original issue #100)")
	assert.Equal(t, "100", wf.dispatched[0]["issue_number"],
		"dispatched workflow must target original issue #100")
}

func TestConsolidate_ConflictResolutionNoOp_OriginalNotFailed_NoDispatch(t *testing.T) {
	// Same setup as the retry test, but the original issue is already done — no dispatch should fire.
	conflictBody := "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-original\n    - herd/batch/1-foo\n---\n\n## Task\nResolve conflict\n"

	conflictIssue := &platform.Issue{
		Number: 50, Title: "Resolve conflict: #100",
		Body:      conflictBody,
		Labels:    []string{issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 1, Title: "Foo"},
	}
	origIssue := &platform.Issue{
		Number: 100, Title: "Original",
		Labels:    []string{issues.StatusDone}, // already resolved by another path
		Milestone: &platform.Milestone{Number: 1, Title: "Foo"},
	}

	issueSvc := newMockIssueService()
	issueSvc.getResult[50] = conflictIssue
	issueSvc.getResult[100] = origIssue

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			200: {ID: 200, Conclusion: "success", Inputs: map[string]string{"issue_number": "50"}},
		},
	}

	mock := &mockPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo: &mockRepoService{
			defaultBranch: "main",
			branchExists:  map[string]bool{},
		},
	}

	cfg := &config.Config{Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}

	result, err := Consolidate(context.Background(), mock, nil, cfg, ConsolidateParams{RunID: 200})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.NoOp)
	assert.False(t, result.Merged)

	// retryConflictOriginIssues guards on StatusFailed — no dispatch must fire for #100.
	assert.Empty(t, wf.dispatched, "no dispatch should fire when original issue is not failed")
}

func TestHandleConflictResolution_TaskBodyKeepsAgentOnWorkerBranch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.createResult = &platform.Issue{Number: 777}
	wf := &mockWorkflowService{}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        &mockPRService{},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ms := &platform.Milestone{Number: 1, Title: "Batch 1"}
	issue := &platform.Issue{
		Number: 42, Title: "Some task",
		Milestone: ms,
	}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}

	workerBranch := "herd/worker/42-some-task"
	batchBranch := "herd/batch/1-batch-1"

	_, err := handleConflictResolution(context.Background(), mock, cfg, issue, ms, workerBranch, batchBranch)
	require.NoError(t, err)

	body := issueSvc.createdBody
	// The new body tells the agent to stay on its own worker branch and merge
	// origin/<workerBranch>. The negation warning ("do NOT run `git checkout
	// <batch>`") must be present, but no positive checkout/push step.
	assertBodyInstructionsKeepAgentOnWorkerBranch(t, body, []string{
		"Do NOT push",
		"Stay on your current worker branch",
		"git merge origin/" + workerBranch,
		// Negation warning: proves the bug is fixed.
		"do NOT run `git checkout " + batchBranch + "`",
	}, []string{
		// No positive checkout step or push to the batch branch.
		"1. `git checkout",
		"2. `git checkout",
		"3. `git checkout",
		"git push origin " + batchBranch,
	})
}

func TestDispatchReadyIssues_SkipsAlreadyInProgress(t *testing.T) {
	type wantDispatch struct {
		issueNumber int
	}
	type wantLabel struct {
		issueNumber int
		label       string
		removed     bool // true if expected to be in removedLabels, false in addedLabels
	}

	tests := []struct {
		name           string
		tierIssues     []int
		issues         []*platform.Issue
		inProgressRuns []*platform.Run
		queuedRuns     []*platform.Run
		maxConcurrent  int
		wantDispatched int
		wantDispatches []wantDispatch
		wantLabels     []wantLabel // labels that MUST be present (added or removed)
		// noLabelChange lists issues that must have NO label changes recorded.
		noLabelChange []int
	}{
		{
			name:       "issue with active in_progress run is skipped",
			tierIssues: []int{42},
			issues: []*platform.Issue{
				{Number: 42, Labels: []string{issues.StatusReady}},
			},
			inProgressRuns: []*platform.Run{
				{ID: 1, Status: "in_progress", Inputs: map[string]string{"issue_number": "42"}},
			},
			maxConcurrent:  3,
			wantDispatched: 0,
			noLabelChange:  []int{42},
		},
		{
			name:       "issue with active queued run is skipped",
			tierIssues: []int{42},
			issues: []*platform.Issue{
				{Number: 42, Labels: []string{issues.StatusReady}},
			},
			queuedRuns: []*platform.Run{
				{ID: 1, Status: "queued", Inputs: map[string]string{"issue_number": "42"}},
			},
			maxConcurrent:  3,
			wantDispatched: 0,
			noLabelChange:  []int{42},
		},
		{
			name:       "issue with no active run dispatches normally",
			tierIssues: []int{42},
			issues: []*platform.Issue{
				{Number: 42, Labels: []string{issues.StatusReady}},
			},
			maxConcurrent:  3,
			wantDispatched: 1,
			wantDispatches: []wantDispatch{{issueNumber: 42}},
			wantLabels: []wantLabel{
				{issueNumber: 42, label: issues.StatusReady, removed: true},
				{issueNumber: 42, label: issues.StatusInProgress, removed: false},
			},
		},
		{
			name:       "mixed batch dispatches only issue without active run",
			tierIssues: []int{42, 43},
			issues: []*platform.Issue{
				{Number: 42, Labels: []string{issues.StatusReady}},
				{Number: 43, Labels: []string{issues.StatusReady}},
			},
			inProgressRuns: []*platform.Run{
				{ID: 1, Status: "in_progress", Inputs: map[string]string{"issue_number": "42"}},
			},
			maxConcurrent:  3,
			wantDispatched: 1,
			wantDispatches: []wantDispatch{{issueNumber: 43}},
			wantLabels: []wantLabel{
				{issueNumber: 43, label: issues.StatusReady, removed: true},
				{issueNumber: 43, label: issues.StatusInProgress, removed: false},
			},
			noLabelChange: []int{42},
		},
		{
			name:       "active runs at MaxConcurrent prevents any dispatch",
			tierIssues: []int{50, 51},
			issues: []*platform.Issue{
				{Number: 50, Labels: []string{issues.StatusReady}},
				{Number: 51, Labels: []string{issues.StatusReady}},
			},
			inProgressRuns: []*platform.Run{
				{ID: 1, Status: "in_progress", Inputs: map[string]string{"issue_number": "10"}},
				{ID: 2, Status: "in_progress", Inputs: map[string]string{"issue_number": "11"}},
				{ID: 3, Status: "in_progress", Inputs: map[string]string{"issue_number": "12"}},
			},
			maxConcurrent:  3,
			wantDispatched: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			wf := &mockWorkflowService{
				listResultByStatus: map[string][]*platform.Run{
					"in_progress": tc.inProgressRuns,
					"queued":      tc.queuedRuns,
				},
			}
			mock := &mockPlatform{
				issues:    issueSvc,
				prs:       &mockPRService{},
				workflows: wf,
				repo:      &mockRepoService{defaultBranch: "main"},
			}
			cfg := &config.Config{Workers: config.Workers{
				MaxConcurrent:  tc.maxConcurrent,
				TimeoutMinutes: 30,
				RunnerLabel:    "herd-worker",
			}}

			dispatched, err := dispatchReadyIssues(
				context.Background(), mock, cfg, tc.tierIssues, tc.issues, "herd/batch/1-batch",
			)
			require.NoError(t, err)
			assert.Equal(t, tc.wantDispatched, dispatched, "dispatched count")
			assert.Len(t, wf.dispatched, len(tc.wantDispatches), "dispatch call count")

			dispatchedNums := map[string]bool{}
			for _, d := range wf.dispatched {
				dispatchedNums[d["issue_number"]] = true
			}
			for _, want := range tc.wantDispatches {
				key := fmt.Sprintf("%d", want.issueNumber)
				assert.True(t, dispatchedNums[key], "expected dispatch for issue #%d", want.issueNumber)
			}

			for _, want := range tc.wantLabels {
				if want.removed {
					assert.Contains(t, issueSvc.removedLabels[want.issueNumber], want.label,
						"issue #%d should have %s removed", want.issueNumber, want.label)
				} else {
					assert.Contains(t, issueSvc.addedLabels[want.issueNumber], want.label,
						"issue #%d should have %s added", want.issueNumber, want.label)
				}
			}

			for _, num := range tc.noLabelChange {
				assert.Empty(t, issueSvc.addedLabels[num], "issue #%d should not have labels added", num)
				assert.Empty(t, issueSvc.removedLabels[num], "issue #%d should not have labels removed", num)
			}

			// Verify both in_progress and queued were queried, with the worker workflow filter.
			require.Len(t, wf.listRunFilters, 2, "should call ListRuns twice (in_progress + queued)")
			seenStatuses := map[string]bool{}
			for _, f := range wf.listRunFilters {
				assert.Equal(t, "herd-worker.yml", f.WorkflowFileName)
				seenStatuses[f.Status] = true
			}
			assert.True(t, seenStatuses["in_progress"], "should query in_progress runs")
			assert.True(t, seenStatuses["queued"], "should query queued runs")
		})
	}
}

// --- Cascade-failure tests ---

// cascadeFailureFixture wires a milestone, issue service, PR service, and
// workflow service for cascade-failure scenarios. The caller fills in the
// issue/PR list state before invoking handleConflictResolution.
type cascadeFailureFixture struct {
	ms       *platform.Milestone
	issueSvc *mockIssueService
	prSvc    *mockPRService
	wf       *mockWorkflowService
	mock     *mockPlatform
	cfg      *config.Config
	issue    *platform.Issue
}

func newCascadeFailureFixture(maxAttempts int, prLabels []string) *cascadeFailureFixture {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: prLabels},
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
		Integrator: config.Integrator{MaxConflictResolutionAttempts: maxAttempts},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}
	issue := &platform.Issue{
		Number: 102, Title: "Failing worker",
		Labels:    []string{issues.StatusDone},
		Milestone: ms,
	}
	return &cascadeFailureFixture{ms: ms, issueSvc: issueSvc, prSvc: prSvc, wf: wf, mock: mock, cfg: cfg, issue: issue}
}

func TestConflictCascadeFailure_AddsLabelToBatchPR(t *testing.T) {
	fx := newCascadeFailureFixture(2, []string{})
	// Two existing conflict-resolution issues — cap reached.
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
		{Number: 81, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
	}

	_, err := handleConflictResolution(context.Background(), fx.mock, fx.cfg, fx.issue, fx.ms, "herd/worker/102-failing-worker", "herd/batch/1-batch")
	require.NoError(t, err)

	assert.Contains(t, fx.issueSvc.addedLabels[500], issues.CascadeFailed,
		"batch PR must be labeled herd/cascade-failed")
	assert.Contains(t, fx.issueSvc.removedLabels[102], issues.StatusDone)
	assert.Contains(t, fx.issueSvc.addedLabels[102], issues.StatusFailed)
}

func TestConflictCascadeFailure_PostsBatchPRComment(t *testing.T) {
	fx := newCascadeFailureFixture(2, []string{})
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
		{Number: 81, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
	}

	_, err := handleConflictResolution(context.Background(), fx.mock, fx.cfg, fx.issue, fx.ms, "herd/worker/102-failing-worker", "herd/batch/1-batch")
	require.NoError(t, err)

	require.NotEmpty(t, fx.issueSvc.comments[500], "comment must be posted on PR #500, not on the issue")
	body := fx.issueSvc.comments[500][0]
	assert.Contains(t, body, "Conflict resolution cascade failed")
	assert.Contains(t, body, "git fetch origin && git checkout")
	assert.Contains(t, body, "herd/worker/102-failing-worker")
	assert.Contains(t, body, "herd/cascade-failed")
}

func TestConflictCascadeFailure_BuildsChainCorrectly(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	// Three issues that form a cascade chain: #100 → #101 → #102.
	original := &platform.Issue{
		Number: 100, Title: "Foo",
		Labels:    []string{issues.StatusFailed},
		Milestone: ms,
		Body:      "---\nherd:\n  version: 1\n---\n\n## Task\nDo foo\n",
	}
	resolver1 := &platform.Issue{
		Number: 101, Title: "Bar",
		Labels:    []string{issues.StatusFailed},
		Milestone: ms,
		Body:      "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-foo\n    - herd/batch/1-batch\n---\n\n## Task\nResolve\n",
	}
	currentFailing := &platform.Issue{
		Number: 102, Title: "Baz",
		Labels:    []string{issues.StatusDone},
		Milestone: ms,
		Body:      "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/101-bar\n    - herd/batch/1-batch\n---\n\n## Task\nResolve\n",
	}

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{original, resolver1, currentFailing}
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}},
		},
	}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 2},
	}

	_, err := handleConflictResolution(context.Background(), mock, cfg, currentFailing, ms, "herd/worker/102-baz", "herd/batch/1-batch")
	require.NoError(t, err)

	require.NotEmpty(t, issueSvc.comments[500])
	assert.Contains(t, issueSvc.comments[500][0], "#100 → #101 → #102 (failed)")
}

func TestConflictCascadeFailure_TagsNotifyUsers(t *testing.T) {
	fx := newCascadeFailureFixture(2, []string{})
	fx.cfg.Monitor = config.Monitor{NotifyUsers: []string{"alice", "bob"}}
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
		{Number: 81, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
	}

	_, err := handleConflictResolution(context.Background(), fx.mock, fx.cfg, fx.issue, fx.ms, "herd/worker/102-failing-worker", "herd/batch/1-batch")
	require.NoError(t, err)

	require.NotEmpty(t, fx.issueSvc.comments[500])
	body := fx.issueSvc.comments[500][0]
	// /cc must be at the end of the body and include both users.
	assert.True(t, len(body) > len("/cc @alice @bob") &&
		body[len(body)-len("/cc @alice @bob"):] == "/cc @alice @bob",
		"PR comment must end with `/cc @alice @bob`, got tail: %q", body[max(0, len(body)-40):])
}

func TestHandleConflictResolution_BlockedByCascadeLabel(t *testing.T) {
	fx := newCascadeFailureFixture(3, []string{issues.CascadeFailed})
	// One existing conflict-resolution issue, so count is below the cap.
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
	}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: fx.issueSvc,
		onCreate: func(string, string, []string, *int) (*platform.Issue, error) {
			t.Fatal("Issues().Create must not be called when PR is in cascade-failed state")
			return nil, nil
		},
	}
	fx.mock.issues = mockCreate

	result, err := handleConflictResolution(context.Background(), fx.mock, fx.cfg, fx.issue, fx.ms, "herd/worker/102-failing-worker", "herd/batch/1-batch")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ConflictDetected)
	// Callers key on WorkerBranch for follow-up logging/cleanup; the circuit-breaker
	// path must populate it like the cap-exhaustion and success paths do.
	assert.Equal(t, "herd/worker/102-failing-worker", result.WorkerBranch)

	require.NotEmpty(t, fx.issueSvc.comments[500])
	assert.Contains(t, fx.issueSvc.comments[500][0], "Conflict resolution is paused")
	assert.Empty(t, fx.wf.dispatched, "no workflow dispatch must occur when blocked")
	assert.Contains(t, fx.issueSvc.removedLabels[102], issues.StatusDone)
	assert.Contains(t, fx.issueSvc.addedLabels[102], issues.StatusFailed)
}

func TestHandleConflictResolution_ResumesAfterLabelRemoved(t *testing.T) {
	fx := newCascadeFailureFixture(3, []string{}) // no cascade-failed label
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
	}
	createCalls := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: fx.issueSvc,
		onCreate: func(title, _ string, _ []string, _ *int) (*platform.Issue, error) {
			createCalls++
			return &platform.Issue{Number: 999, Title: title}, nil
		},
	}
	fx.mock.issues = mockCreate

	_, err := handleConflictResolution(context.Background(), fx.mock, fx.cfg, fx.issue, fx.ms, "herd/worker/102-failing-worker", "herd/batch/1-batch")
	require.NoError(t, err)

	assert.Equal(t, 1, createCalls, "Issues().Create must be called when not blocked")
	assert.Len(t, fx.wf.dispatched, 1, "Workflows().Dispatch must be called when not blocked")
}

func TestMarkCascadeFailed_NoPRFallsBackToIssueComment(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	issueSvc := newMockIssueService()
	// PR service returns no PRs.
	prSvc := &mockPRService{listResult: nil}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	cfg := &config.Config{
		Integrator: config.Integrator{MaxConflictResolutionAttempts: 2},
	}
	issue := &platform.Issue{Number: 42, Title: "Test", Milestone: ms, Labels: []string{issues.StatusDone}}

	// Must not panic and must fall back to a comment on the issue.
	markCascadeFailed(context.Background(), mock, cfg, ms, issue, "herd/worker/42-test", "herd/batch/1-batch", cascadeKindMerge)

	require.NotEmpty(t, issueSvc.comments[42])
	assert.Contains(t, issueSvc.comments[42][0], "Manual intervention required")
	// Issue still relabeled.
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusDone)
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
}

func TestBuildCascadeChain(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}

	tests := []struct {
		name         string
		listResult   []*platform.Issue
		currentIssue *platform.Issue
		want         []int
	}{
		{
			name:         "non-conflict issue returns only itself",
			listResult:   []*platform.Issue{{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\n"}},
			currentIssue: &platform.Issue{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\n"},
			want:         []int{42},
		},
		{
			name: "single resolver returns parent then current",
			listResult: []*platform.Issue{
				{Number: 100, Body: "---\nherd:\n  version: 1\n---\n\n## Task\n"},
				{Number: 101, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-foo\n    - herd/batch/1-batch\n---\n\n## Task\n"},
			},
			currentIssue: &platform.Issue{Number: 101, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/100-foo\n    - herd/batch/1-batch\n---\n\n## Task\n"},
			want:         []int{100, 101},
		},
		{
			name: "cycle terminates without infinite loop",
			listResult: []*platform.Issue{
				{Number: 200, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/201-b\n    - herd/batch/1-batch\n---\n\n## Task\n"},
				{Number: 201, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/200-a\n    - herd/batch/1-batch\n---\n\n## Task\n"},
			},
			currentIssue: &platform.Issue{Number: 200, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n  conflicting_branches:\n    - herd/worker/201-b\n    - herd/batch/1-batch\n---\n\n## Task\n"},
			want:         []int{201, 200},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.listResult = tc.listResult
			mock := &mockPlatform{issues: issueSvc}

			chain, err := buildCascadeChain(context.Background(), mock, ms, tc.currentIssue)
			require.NoError(t, err)
			assert.Equal(t, tc.want, chain)
		})
	}
}

func TestCascadeFailedPR(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Batch"}

	tests := []struct {
		name       string
		listResult []*platform.PullRequest
		wantPR     bool
	}{
		{
			name:       "label present returns PR",
			listResult: []*platform.PullRequest{{Number: 500, Head: "herd/batch/1-batch", Labels: []string{issues.CascadeFailed}}},
			wantPR:     true,
		},
		{
			name:       "label absent returns nil",
			listResult: []*platform.PullRequest{{Number: 500, Head: "herd/batch/1-batch", Labels: []string{}}},
			wantPR:     false,
		},
		{
			name:       "no PR returns nil",
			listResult: nil,
			wantPR:     false,
		},
		{
			name:       "unrelated labels returns nil",
			listResult: []*platform.PullRequest{{Number: 500, Head: "herd/batch/1-batch", Labels: []string{"bug", "wontfix"}}},
			wantPR:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prSvc := &mockPRService{listResult: tc.listResult}
			mock := &mockPlatform{
				issues: newMockIssueService(),
				prs:    prSvc,
			}
			pr := cascadeFailedPR(context.Background(), mock, ms)
			if tc.wantPR {
				require.NotNil(t, pr)
				assert.Equal(t, 500, pr.Number)
			} else {
				assert.Nil(t, pr)
			}
		})
	}
}

func TestCascadeFailedPR_NilMilestone(t *testing.T) {
	mock := &mockPlatform{issues: newMockIssueService(), prs: &mockPRService{}}
	assert.Nil(t, cascadeFailedPR(context.Background(), mock, nil))
}

func TestPostCascadePausedNotice(t *testing.T) {
	tests := []struct {
		name          string
		existing      []*platform.Comment
		wantPostCount int
	}{
		{
			name:          "no prior comments posts the notice",
			existing:      nil,
			wantPostCount: 1,
		},
		{
			name: "unrelated prior comments still posts the notice",
			existing: []*platform.Comment{
				{Body: "Looks good to me"},
				{Body: "Re-running CI"},
			},
			wantPostCount: 1,
		},
		{
			name: "paused notice already present skips posting",
			existing: []*platform.Comment{
				{Body: cascadePausedComment},
			},
			wantPostCount: 0,
		},
		{
			name: "paused notice after most recent cascade-failed marker skips posting",
			existing: []*platform.Comment{
				{Body: "⚠️ Conflict resolution cascade failed\n\nDetails about the cascade…"},
				{Body: "Some unrelated comment"},
				{Body: cascadePausedComment},
			},
			wantPostCount: 0,
		},
		{
			name: "new cascade-failed marker after a paused notice re-enables posting",
			existing: []*platform.Comment{
				{Body: cascadePausedComment},
				{Body: "Manual intervention happened"},
				{Body: "⚠️ Conflict resolution cascade failed\n\nA fresh cascade occurred"},
			},
			wantPostCount: 1,
		},
		{
			name: "only earliest comment is a paused notice and many comments after still skips",
			existing: []*platform.Comment{
				{Body: cascadePausedComment},
				{Body: "ok"},
				{Body: "still working"},
			},
			wantPostCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issueSvc := newMockIssueService()
			issueSvc.listCommentsResult = tc.existing
			mock := &mockPlatform{issues: issueSvc}

			postCascadePausedNotice(context.Background(), mock, 500)

			assert.Len(t, issueSvc.comments[500], tc.wantPostCount)
			if tc.wantPostCount > 0 {
				assert.Equal(t, cascadePausedComment, issueSvc.comments[500][0])
			}
		})
	}
}

// listCommentsErrorIssueService overrides ListComments to return an error so
// we can verify postCascadePausedNotice degrades to posting (rather than
// silently swallowing the notice) when the comment-history lookup fails.
type listCommentsErrorIssueService struct {
	*mockIssueService
}

func (m *listCommentsErrorIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, fmt.Errorf("transient API failure")
}

func TestPostCascadePausedNotice_ListCommentsErrorStillPosts(t *testing.T) {
	inner := newMockIssueService()
	issueSvc := &listCommentsErrorIssueService{mockIssueService: inner}
	mock := &mockPlatform{issues: issueSvc}

	postCascadePausedNotice(context.Background(), mock, 500)

	assert.Len(t, inner.comments[500], 1)
	assert.Equal(t, cascadePausedComment, inner.comments[500][0])
}

func TestHandleConflictResolution_BlockedByCascadeLabelDoesNotDuplicateComment(t *testing.T) {
	fx := newCascadeFailureFixture(3, []string{issues.CascadeFailed})
	fx.issueSvc.listResult = []*platform.Issue{
		{Number: 80, Body: "---\nherd:\n  version: 1\n  conflict_resolution: true\n---\n\n## Task\n"},
	}
	// Simulate a paused notice already on the PR from a prior workflow_run.
	fx.issueSvc.listCommentsResult = []*platform.Comment{
		{Body: cascadePausedComment},
	}

	_, err := handleConflictResolution(context.Background(), fx.mock, fx.cfg, fx.issue, fx.ms, "herd/worker/102-failing-worker", "herd/batch/1-batch")
	require.NoError(t, err)

	assert.Empty(t, fx.issueSvc.comments[500], "must not post a duplicate paused notice when one already exists")
	// Issue is still relabeled even when the notice is suppressed.
	assert.Contains(t, fx.issueSvc.removedLabels[102], issues.StatusDone)
	assert.Contains(t, fx.issueSvc.addedLabels[102], issues.StatusFailed)
}

func TestDispatchRebaseConflictWorker_BlockedByCascadeLabelDoesNotDuplicateComment(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listCommentsResult = []*platform.Comment{
		{Body: cascadePausedComment},
	}
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{issues.CascadeFailed}},
		},
	}
	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 3}}

	num, err := DispatchRebaseConflictWorker(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)
	assert.Equal(t, 0, num)
	assert.Empty(t, issueSvc.comments[500], "must not post a duplicate paused notice when one already exists")
	assert.Empty(t, wf.dispatched, "no workflow dispatch when blocked")
}

func TestHandleRebaseConflictResolution_DelegatesCircuitBreakerToDispatch(t *testing.T) {
	// Single round-trip: handleRebaseConflictResolution must rely on
	// DispatchRebaseConflictWorker for the cascade-failed check, not perform
	// its own duplicate findBatchPR lookup.
	issueSvc := newMockIssueService()
	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{
			{Number: 500, Head: "herd/batch/1-batch", Labels: []string{issues.CascadeFailed}},
		},
	}
	wf := &mockWorkflowService{}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	ms := &platform.Milestone{Number: 1, Title: "Batch"}
	cfg := &config.Config{Integrator: config.Integrator{MaxConflictResolutionAttempts: 3}}

	err := handleRebaseConflictResolution(context.Background(), mock, cfg, ms, "herd/batch/1-batch", "main")
	require.NoError(t, err)

	// Single notice posted (by Dispatch's circuit breaker), no duplicate
	// from a separate check in handleRebaseConflictResolution.
	require.Len(t, issueSvc.comments[500], 1)
	assert.Contains(t, issueSvc.comments[500][0], "Conflict resolution is paused")
	assert.Empty(t, wf.dispatched)
}
