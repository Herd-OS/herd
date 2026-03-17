package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Platform ---

type testPlatform struct {
	issues     platform.IssueService
	prs        platform.PullRequestService
	workflows  *testWorkflowService
	repo       *testRepoService
	milestones *testMilestoneService
	checks     *testCheckService
}

func (m *testPlatform) Issues() platform.IssueService            { return m.issues }
func (m *testPlatform) PullRequests() platform.PullRequestService { return m.prs }
func (m *testPlatform) Workflows() platform.WorkflowService       { return m.workflows }
func (m *testPlatform) Labels() platform.LabelService             { return nil }
func (m *testPlatform) Milestones() platform.MilestoneService     { return m.milestones }
func (m *testPlatform) Runners() platform.RunnerService           { return nil }
func (m *testPlatform) Repository() platform.RepositoryService    { return m.repo }
func (m *testPlatform) Checks() platform.CheckService            { return m.checks }

// --- Mock IssueService ---

type testIssueService struct {
	getResult     map[int]*platform.Issue
	listResult    []*platform.Issue
	addedLabels   map[int][]string
	removedLabels map[int][]string
	createdIssues []*platform.Issue
	nextIssueNum  int
	addLabelsErr  error
	createErr     error
}

func newTestIssueService() *testIssueService {
	return &testIssueService{
		getResult:    make(map[int]*platform.Issue),
		addedLabels:  make(map[int][]string),
		removedLabels: make(map[int][]string),
		nextIssueNum: 200,
	}
}

func (m *testIssueService) Create(_ context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	iss := &platform.Issue{Number: m.nextIssueNum, Title: title, Body: body, Labels: labels}
	m.nextIssueNum++
	m.createdIssues = append(m.createdIssues, iss)
	return iss, nil
}
func (m *testIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	if i, ok := m.getResult[number]; ok {
		return i, nil
	}
	return nil, fmt.Errorf("issue #%d not found", number)
}
func (m *testIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return m.listResult, nil
}
func (m *testIssueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *testIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	if m.addLabelsErr != nil {
		return m.addLabelsErr
	}
	m.addedLabels[number] = append(m.addedLabels[number], labels...)
	return nil
}
func (m *testIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	m.removedLabels[number] = append(m.removedLabels[number], labels...)
	return nil
}
func (m *testIssueService) AddComment(_ context.Context, _ int, _ string) error  { return nil }
func (m *testIssueService) DeleteComment(_ context.Context, _ int64) error       { return nil }
func (m *testIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *testIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

// --- Mock PRService ---

type testPRService struct {
	getResult  map[int]*platform.PullRequest
	listResult []*platform.PullRequest
	comments   []string
}

func (m *testPRService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *testPRService) Get(_ context.Context, number int) (*platform.PullRequest, error) {
	if m.getResult != nil {
		if pr, ok := m.getResult[number]; ok {
			return pr, nil
		}
	}
	return nil, fmt.Errorf("PR #%d not found", number)
}
func (m *testPRService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	return m.listResult, nil
}
func (m *testPRService) Update(_ context.Context, _ int, _, _ *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *testPRService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return &platform.MergeResult{Merged: true}, nil
}
func (m *testPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *testPRService) AddComment(_ context.Context, _ int, body string) error {
	m.comments = append(m.comments, body)
	return nil
}
func (m *testPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}

// --- Mock WorkflowService ---

type testWorkflowService struct {
	dispatched  []map[string]string
	dispatchErr error
}

func (m *testWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *testWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	if m.dispatchErr != nil {
		return nil, m.dispatchErr
	}
	m.dispatched = append(m.dispatched, inputs)
	return &platform.Run{ID: 999}, nil
}
func (m *testWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *testWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return nil, nil
}
func (m *testWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

// --- Mock RepoService ---

type testRepoService struct {
	defaultBranch    string
	defaultBranchErr error
}

func (m *testRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (m *testRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return m.defaultBranch, m.defaultBranchErr
}
func (m *testRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *testRepoService) DeleteBranch(_ context.Context, _ string) error     { return nil }
func (m *testRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}

// --- Mock MilestoneService ---

type testMilestoneService struct {
	getResult map[int]*platform.Milestone
}

func (m *testMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (m *testMilestoneService) Get(_ context.Context, number int) (*platform.Milestone, error) {
	if m.getResult != nil {
		if ms, ok := m.getResult[number]; ok {
			return ms, nil
		}
	}
	return nil, fmt.Errorf("milestone #%d not found", number)
}
func (m *testMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (m *testMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

// --- Mock CheckService ---

type testCheckService struct {
	status    string
	rerunErr  error
}

func (m *testCheckService) GetCombinedStatus(_ context.Context, _ string) (string, error) {
	return m.status, nil
}
func (m *testCheckService) RerunFailedChecks(_ context.Context, _ string) error {
	return m.rerunErr
}

// --- Mock Agent ---

type testAgent struct {
	reviewResult *agent.ReviewResult
	reviewErr    error
}

func (m *testAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (m *testAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (m *testAgent) Review(_ context.Context, _ string, _ agent.ReviewOptions) (*agent.ReviewResult, error) {
	return m.reviewResult, m.reviewErr
}

// initHandlerTestRepo creates a minimal git repo with main and batch branches.
func initHandlerTestRepo(t *testing.T) (string, *git.Git) {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "-b", "main", dir},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "init"},
		{"git", "-C", dir, "branch", "herd/batch/1-batch"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "cmd %v failed: %s", args, string(out))
	}
	return dir, git.New(dir)
}

// baseConfig returns a minimal config for tests.
func baseConfig() *config.Config {
	return &config.Config{
		Integrator: config.Integrator{
			RequireCI:          true,
			CIMaxFixCycles:     3,
			Review:             true,
			ReviewMaxFixCycles: 3,
		},
		Workers: config.Workers{
			TimeoutMinutes: 30,
			RunnerLabel:    "ubuntu-latest",
		},
	}
}

// --- Tests for handleFixCI ---

func TestHandleFixCI_ProceedsWhenCIFixPendingLabelAlreadySet(t *testing.T) {
	// Patrol adds herd/ci-fix-pending before posting /herd fix-ci, so the label
	// is always present when the handler runs. The handler must proceed and
	// dispatch workers rather than treating the label as a "already in progress"
	// signal. Dedup for the rare two-comment race is handled by the workflow
	// concurrency group serialising handlers and CheckCI's pending-status guard.
	issueSvc := newTestIssueService()
	issueSvc.getResult[10] = &platform.Issue{Number: 10, Labels: []string{issues.CIFixPending}}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			10: {Number: 10, Head: "herd/batch/1-batch"},
		},
		// listResult is needed by CheckCI to locate the batch PR.
		listResult: []*platform.PullRequest{
			{Number: 10, Head: "herd/batch/1-batch"},
		},
	}
	p := &testPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: wf,
		repo:      &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}},
		},
		checks: &testCheckService{status: "failure", rerunErr: fmt.Errorf("rerun failed")},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        true,
	}
	result := handleFixCI(hctx, Command{Name: "fix-ci"})

	require.NoError(t, result.Error)
	// Handler should proceed to dispatch workers, not return "already in progress".
	assert.Contains(t, result.Message, "dispatched fix workers")
	assert.NotEmpty(t, issueSvc.createdIssues)
	assert.NotEmpty(t, wf.dispatched)
}

func TestHandleFixCI_NotPR(t *testing.T) {
	p := &testPlatform{issues: newTestIssueService(), prs: &testPRService{}, workflows: &testWorkflowService{}, repo: &testRepoService{defaultBranch: "main"}, milestones: &testMilestoneService{}, checks: &testCheckService{}}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        false,
	}
	result := handleFixCI(hctx, Command{Name: "fix-ci"})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "can only be used on pull requests")
}

func TestHandleFixCI_NotBatchPR(t *testing.T) {
	issueSvc := newTestIssueService()
	// Populate the issue so the dedup path is traversed; no CIFixPending label
	// means the handler should proceed past the dedup check.
	issueSvc.getResult[10] = &platform.Issue{Number: 10, Labels: []string{}}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			10: {Number: 10, Head: "feature/my-feature"},
		},
	}
	p := &testPlatform{issues: issueSvc, prs: prSvc, workflows: &testWorkflowService{}, repo: &testRepoService{defaultBranch: "main"}, milestones: &testMilestoneService{}, checks: &testCheckService{}}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        true,
	}
	result := handleFixCI(hctx, Command{Name: "fix-ci"})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "can only be used on batch PRs")
}

func TestHandleFixCI_CISuccess(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			10: {Number: 10, Head: "herd/batch/1-batch"},
		},
		listResult: []*platform.PullRequest{{Number: 10, Head: "herd/batch/1-batch"}},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  &testWorkflowService{},
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}}},
		checks:     &testCheckService{status: "success"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        true,
	}
	result := handleFixCI(hctx, Command{Name: "fix-ci"})

	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "✅ CI is passing")
}

func TestHandleFixCI_WithFixDispatch(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			10: {Number: 10, Head: "herd/batch/1-batch"},
		},
		listResult: []*platform.PullRequest{{Number: 10, Head: "herd/batch/1-batch"}},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}}},
		// status=failure, rerunErr=non-nil → proceeds to create fix issue
		checks: &testCheckService{status: "failure", rerunErr: fmt.Errorf("rerun failed")},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        true,
	}
	result := handleFixCI(hctx, Command{Name: "fix-ci"})

	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "🔧 CI failed — dispatched fix workers")
	assert.Contains(t, result.Message, "#200")
	assert.Len(t, issueSvc.createdIssues, 1)
	assert.Len(t, wf.dispatched, 1)
	assert.Contains(t, issueSvc.addedLabels[10], issues.CIFixPending)
}

func TestHandleFixCI_AddLabelsError(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	issueSvc.addLabelsErr = fmt.Errorf("API rate limit exceeded")
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			10: {Number: 10, Head: "herd/batch/1-batch"},
		},
		listResult: []*platform.PullRequest{{Number: 10, Head: "herd/batch/1-batch"}},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}}},
		checks:     &testCheckService{status: "failure", rerunErr: fmt.Errorf("rerun failed")},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        true,
	}
	result := handleFixCI(hctx, Command{Name: "fix-ci"})

	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "🔧 CI failed — dispatched fix workers")
	// Workers were dispatched despite the label error
	assert.Len(t, issueSvc.createdIssues, 1)
	assert.Len(t, wf.dispatched, 1)
}

// --- Tests for handleRetry ---

func TestHandleRetry(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		setupIssue  *platform.Issue
		dispatchErr error
		wantMsg     string
		wantErr     bool
	}{
		{
			name:    "missing arg",
			args:    []string{},
			wantMsg: "Usage: `/herd retry",
		},
		{
			name:    "invalid issue number",
			args:    []string{"abc"},
			wantMsg: "Invalid issue number",
		},
		{
			name: "issue not failed",
			args: []string{"42"},
			setupIssue: &platform.Issue{
				Number: 42, Labels: []string{issues.StatusInProgress},
				Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
			},
			wantMsg: "not failed",
		},
		{
			name: "issue has no milestone",
			args: []string{"42"},
			setupIssue: &platform.Issue{
				Number: 42, Labels: []string{issues.StatusFailed},
			},
			wantMsg: "no milestone",
		},
		{
			name: "successful redispatch",
			args: []string{"42"},
			setupIssue: &platform.Issue{
				Number: 42, Labels: []string{issues.StatusFailed},
				Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
			},
			wantMsg: "Re-dispatched worker for issue #42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newTestIssueService()
			if tt.setupIssue != nil {
				issueSvc.getResult[42] = tt.setupIssue
			}
			wf := &testWorkflowService{dispatchErr: tt.dispatchErr}
			p := &testPlatform{
				issues:    issueSvc,
				workflows: wf,
				repo:      &testRepoService{defaultBranch: "main"},
			}

			hctx := &HandlerContext{
				Ctx:      context.Background(),
				Platform: p,
				Config:   baseConfig(),
			}
			result := handleRetry(hctx, Command{Name: "retry", Args: tt.args})

			if tt.wantErr {
				assert.Error(t, result.Error)
			} else {
				assert.NoError(t, result.Error)
			}
			assert.Contains(t, result.Message, tt.wantMsg)
		})
	}
}

func TestHandleRetry_DispatchError(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Labels: []string{issues.StatusFailed},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	wf := &testWorkflowService{dispatchErr: fmt.Errorf("workflow dispatch failed")}
	p := &testPlatform{
		issues:    issueSvc,
		workflows: wf,
		repo:      &testRepoService{defaultBranch: "main"},
	}

	hctx := &HandlerContext{
		Ctx:      context.Background(),
		Platform: p,
		Config:   baseConfig(),
	}
	result := handleRetry(hctx, Command{Name: "retry", Args: []string{"42"}})

	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "dispatching worker for #42")
	// Labels should be rolled back
	assert.Contains(t, issueSvc.addedLabels[42], issues.StatusFailed)
	assert.Contains(t, issueSvc.removedLabels[42], issues.StatusInProgress)
}

// --- Tests for handleReview ---

func TestHandleReview_NotPR(t *testing.T) {
	p := &testPlatform{
		prs:        &testPRService{},
		issues:     newTestIssueService(),
		workflows:  &testWorkflowService{},
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        false,
	}
	result := handleReview(hctx, Command{Name: "review"})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "can only be used on pull requests")
}

func TestHandleReview_NotBatchPR(t *testing.T) {
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			10: {Number: 10, Head: "feature/something"},
		},
	}
	p := &testPlatform{
		prs:        prSvc,
		issues:     newTestIssueService(),
		workflows:  &testWorkflowService{},
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        true,
	}
	result := handleReview(hctx, Command{Name: "review"})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "can only be used on batch PRs")
}

func TestHandleReview_Approved(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch", Base: "main"},
		},
	}
	p := &testPlatform{
		issues:  issueSvc,
		prs:     prSvc,
		workflows: &testWorkflowService{},
		repo:    &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}},
		},
	}

	dir, g := initHandlerTestRepo(t)
	ag := &testAgent{reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"}}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Agent:       ag,
		Git:         g,
		Config:      baseConfig(),
		IssueNumber: 50,
		RepoRoot:    dir,
		IsPR:        true,
	}
	result := handleReview(hctx, Command{Name: "review"})

	require.NoError(t, result.Error)
	// integrator.Review already posted the ✅ comment; handler returns empty message
	assert.Empty(t, result.Message)
}

func TestHandleReview_WithFixes(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch", Base: "main"},
		},
	}
	wf := &testWorkflowService{}
	p := &testPlatform{
		issues:  issueSvc,
		prs:     prSvc,
		workflows: wf,
		repo:    &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}},
		},
	}

	dir, g := initHandlerTestRepo(t)
	ag := &testAgent{reviewResult: &agent.ReviewResult{
		Approved: false,
		Comments: []string{"Fix error handling", "Add tests"},
	}}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Agent:       ag,
		Git:         g,
		Config:      baseConfig(),
		IssueNumber: 50,
		RepoRoot:    dir,
		IsPR:        true,
	}
	result := handleReview(hctx, Command{Name: "review"})

	require.NoError(t, result.Error)
	// integrator.Review already posted the 🔍 findings comment; handler returns empty message
	assert.Empty(t, result.Message)
	assert.Len(t, issueSvc.createdIssues, 2)
}

func TestHandleReview_MaxCyclesHit(t *testing.T) {
	// An issue with fix_cycle: 3 causes findMaxFixCycle to return 3,
	// which equals ReviewMaxFixCycles (3) → integrator takes MaxCyclesHit path.
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{
			Number: 42,
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nA prior fix.\n",
		},
	}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch", Base: "main"},
		},
	}
	p := &testPlatform{
		issues:  issueSvc,
		prs:     prSvc,
		workflows: &testWorkflowService{},
		repo:    &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}},
		},
	}

	dir, g := initHandlerTestRepo(t)
	ag := &testAgent{reviewResult: &agent.ReviewResult{
		Approved: false,
		Comments: []string{"Fix error handling"},
	}}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Agent:       ag,
		Git:         g,
		Config:      baseConfig(),
		IssueNumber: 50,
		RepoRoot:    dir,
		IsPR:        true,
	}
	result := handleReview(hctx, Command{Name: "review"})

	require.NoError(t, result.Error)
	// integrator.Review posts the max-cycles warning directly on the PR; handler
	// must return an empty message to avoid posting a duplicate comment.
	assert.Empty(t, result.Message)
	// The PR should have received the warning comment from integrator.Review.
	require.NotEmpty(t, prSvc.comments, "expected integrator to post a PR comment on MaxCyclesHit")
	assert.Contains(t, prSvc.comments[0], "max fix cycles")
}

func TestHandleReview_AllCreatesFailed(t *testing.T) {
	// The agent finds issues but every Issues().Create call fails.
	// handleReview must return an error rather than the misleading
	// "Review completed (no action taken)." message.
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	issueSvc.createErr = fmt.Errorf("API error: issue creation failed")

	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch", Base: "main"},
		},
	}
	p := &testPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &testWorkflowService{},
		repo:      &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}},
		},
	}

	dir, g := initHandlerTestRepo(t)
	ag := &testAgent{reviewResult: &agent.ReviewResult{
		Approved: false,
		Comments: []string{"Fix error handling"},
	}}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Agent:       ag,
		Git:         g,
		Config:      baseConfig(),
		IssueNumber: 50,
		RepoRoot:    dir,
		IsPR:        true,
	}
	result := handleReview(hctx, Command{Name: "review"})

	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "all fix-issue creations failed")
	assert.Empty(t, result.Message)
}

// --- Tests for handleFix ---

func TestHandleFix_NotPR(t *testing.T) {
	p := &testPlatform{
		prs:        &testPRService{},
		issues:     newTestIssueService(),
		workflows:  &testWorkflowService{},
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{},
	}
	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 10,
		IsPR:        false,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: "fix something"})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "can only be used on pull requests")
}

func TestHandleFix(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		prHead  string
		wantMsg string
	}{
		{
			name:    "empty prompt",
			prompt:  "",
			prHead:  "herd/batch/1-batch",
			wantMsg: "Usage: `/herd fix",
		},
		{
			name:    "not batch PR",
			prompt:  "fix something",
			prHead:  "feature/foo",
			wantMsg: "can only be used on batch PRs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prSvc := &testPRService{
				getResult: map[int]*platform.PullRequest{
					10: {Number: 10, Head: tt.prHead},
				},
			}
			p := &testPlatform{
				prs:        prSvc,
				issues:     newTestIssueService(),
				workflows:  &testWorkflowService{},
				repo:       &testRepoService{defaultBranch: "main"},
				milestones: &testMilestoneService{},
			}
			hctx := &HandlerContext{
				Ctx:         context.Background(),
				Platform:    p,
				Config:      baseConfig(),
				IssueNumber: 10,
				IsPR:        true,
			}
			result := handleFix(hctx, Command{Name: "fix", Prompt: tt.prompt})

			assert.NoError(t, result.Error)
			assert.Contains(t, result.Message, tt.wantMsg)
		})
	}
}

func TestHandleFix_GetDefaultBranchError(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch"},
		},
	}
	p := &testPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &testWorkflowService{},
		repo:      &testRepoService{defaultBranchErr: fmt.Errorf("branch lookup failed")},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "My Batch"}},
		},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 50,
		AuthorLogin: "octocat",
		IsPR:        true,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: "fix something"})

	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "getting default branch")
	assert.Contains(t, result.Error.Error(), "branch lookup failed")
}

func TestHandleFix_DispatchError(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch"},
		},
	}
	p := &testPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: &testWorkflowService{dispatchErr: fmt.Errorf("workflow dispatch failed")},
		repo:      &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "My Batch"}},
		},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 50,
		AuthorLogin: "octocat",
		IsPR:        true,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: "fix something"})

	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "dispatching worker for fix issue")
	assert.Contains(t, result.Error.Error(), "workflow dispatch failed")
}

func TestHandleFix_Success(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch"},
		},
	}
	p := &testPlatform{
		issues:  issueSvc,
		prs:     prSvc,
		workflows: wf,
		repo:    &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "My Batch"}},
		},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 50,
		AuthorLogin: "octocat",
		IsPR:        true,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: "Add missing validation to the auth handler"})

	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "🔧 Created fix issue #200")
	assert.Len(t, issueSvc.createdIssues, 1)
	assert.Len(t, wf.dispatched, 1)
}

// --- Tests for HandlerContext.IsPR ---

func TestHandlerContext_IsPR(t *testing.T) {
	tests := []struct {
		name string
		isPR bool
	}{
		{"issue context", false},
		{"PR context", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hctx := &HandlerContext{IsPR: tt.isPR}
			assert.Equal(t, tt.isPR, hctx.IsPR)
		})
	}
}

// --- Tests for DefaultRegistry ---

func TestDefaultRegistry(t *testing.T) {
	reg := DefaultRegistry()

	assert.True(t, reg.Has("fix-ci"))
	assert.True(t, reg.Has("retry"))
	assert.True(t, reg.Has("review"))
	assert.True(t, reg.Has("fix"))
}

// --- Tests for ExtraInstructions in review ---

func TestHandleReview_ExtraInstructions(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/1-batch", Base: "main"},
		},
	}

	var capturedOpts agent.ReviewOptions
	capturingAg := &capturingTestAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	p := &testPlatform{
		issues:  issueSvc,
		prs:     prSvc,
		workflows: &testWorkflowService{},
		repo:    &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch"}},
		},
	}

	dir, g := initHandlerTestRepo(t)

	// Create .herd/integrator.md so the system prompt is set before extra instructions
	require.NoError(t, os.MkdirAll(dir+"/.herd", 0755))
	require.NoError(t, os.WriteFile(dir+"/.herd/integrator.md", []byte("Base instructions"), 0644))

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Agent:       capturingAg,
		Git:         g,
		Config:      baseConfig(),
		IssueNumber: 50,
		RepoRoot:    dir,
		IsPR:        true,
	}
	result := handleReview(hctx, Command{Name: "review", Prompt: "Focus on security issues"})

	require.NoError(t, result.Error)
	assert.Contains(t, capturedOpts.SystemPrompt, "Base instructions")
	assert.Contains(t, capturedOpts.SystemPrompt, "Focus on security issues")
}

type capturingTestAgent struct {
	result       *agent.ReviewResult
	capturedOpts *agent.ReviewOptions
}

func (m *capturingTestAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (m *capturingTestAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (m *capturingTestAgent) Review(_ context.Context, _ string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	*m.capturedOpts = opts
	return m.result, nil
}
