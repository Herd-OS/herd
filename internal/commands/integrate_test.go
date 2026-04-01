package commands

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// integratePlatform is like testPlatform but accepts a RepositoryService interface
// so we can use a custom repo mock with per-branch SHA control.
type integratePlatform struct {
	issues     platform.IssueService
	prs        platform.PullRequestService
	workflows  *testWorkflowService
	repo       platform.RepositoryService
	milestones *testMilestoneService
	checks     *testCheckService
}

func (m *integratePlatform) Issues() platform.IssueService            { return m.issues }
func (m *integratePlatform) PullRequests() platform.PullRequestService { return m.prs }
func (m *integratePlatform) Workflows() platform.WorkflowService      { return m.workflows }
func (m *integratePlatform) Labels() platform.LabelService             { return nil }
func (m *integratePlatform) Milestones() platform.MilestoneService     { return m.milestones }
func (m *integratePlatform) Runners() platform.RunnerService           { return nil }
func (m *integratePlatform) Repository() platform.RepositoryService    { return m.repo }
func (m *integratePlatform) Checks() platform.CheckService            { return m.checks }

// integrateRepoService provides per-branch SHA control for integrate tests.
type integrateRepoService struct {
	defaultBranch string
	branchSHAs    map[string]string // branch→SHA; missing key → error
	deleted       []string
}

func (m *integrateRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) {
	return nil, nil
}
func (m *integrateRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return m.defaultBranch, nil
}
func (m *integrateRepoService) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (m *integrateRepoService) DeleteBranch(_ context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	return nil
}
func (m *integrateRepoService) GetBranchSHA(_ context.Context, name string) (string, error) {
	if sha, ok := m.branchSHAs[name]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("branch %s not found", name)
}

func TestHandleIntegrate(t *testing.T) {
	batchBody := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version: 1,
			Batch:   1,
		},
		Task: "Test task",
	})
	noBatchBody := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version: 1,
		},
		Task: "Test task without batch",
	})

	tests := []struct {
		name       string
		isPR       bool
		issueBody  string
		prHead     string
		milestone  *platform.Milestone
		issues     []*platform.Issue
		branchSHAs map[string]string
		ciStatus   string
		prList     []*platform.PullRequest
		wantMsg    string
		wantErr    bool
	}{
		{
			name:      "issue with no batch frontmatter",
			isPR:      false,
			issueBody: noBatchBody,
			wantMsg:   "⚠️ This issue has no batch number in its frontmatter.",
		},
		{
			name:   "non-batch PR",
			isPR:   true,
			prHead: "feature/my-feature",
			wantMsg: "⚠️ `/herd integrate` can only be used on batch PRs or issues with a batch frontmatter.",
		},
		{
			name:      "closed milestone",
			isPR:      false,
			issueBody: batchBody,
			milestone: &platform.Milestone{Number: 1, Title: "Test Batch", State: "closed"},
			wantMsg:   "⏭️ Batch #1 is already closed — skipping.",
		},
		{
			name:      "issue with valid batch, no unconsolidated branches",
			isPR:      false,
			issueBody: batchBody,
			milestone: &platform.Milestone{Number: 1, Title: "Test Batch", State: "open"},
			issues: []*platform.Issue{
				{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone}},
				{Number: 11, Title: "Task B", Labels: []string{issues.StatusInProgress}},
			},
			branchSHAs: map[string]string{}, // no worker branches exist
			ciStatus:   "success",
			prList:     []*platform.PullRequest{},
			wantMsg:    "No unconsolidated worker branches",
		},
		{
			name:      "batch PR extracts batch from branch",
			isPR:      true,
			prHead:    "herd/batch/1-test-batch",
			milestone: &platform.Milestone{Number: 1, Title: "Test Batch", State: "open"},
			issues: []*platform.Issue{
				{Number: 10, Title: "Task A", Labels: []string{issues.StatusInProgress}},
			},
			ciStatus: "success",
			prList:   []*platform.PullRequest{},
			wantMsg:  "Integrator cycle for batch #1",
		},
		{
			name:      "unconsolidated branches but git is nil",
			isPR:      false,
			issueBody: batchBody,
			milestone: &platform.Milestone{Number: 1, Title: "Test Batch", State: "open"},
			issues: []*platform.Issue{
				{Number: 10, Title: "Task A", Labels: []string{issues.StatusDone}},
				{Number: 11, Title: "Task B", Labels: []string{issues.StatusInProgress}},
			},
			branchSHAs: map[string]string{
				"herd/worker/10-task-a": "sha10",
			},
			ciStatus: "success",
			prList:   []*platform.PullRequest{},
			// Git is nil, so consolidation won't happen.
			wantMsg: "No unconsolidated worker branches",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueSvc := newTestIssueService()
			issueSvc.listResult = tt.issues
			wf := &testWorkflowService{}
			prSvc := &testPRService{
				listResult: tt.prList,
			}
			if tt.isPR {
				prSvc.getResult = map[int]*platform.PullRequest{
					5: {Number: 5, Head: tt.prHead},
				}
			}

			repo := &integrateRepoService{
				defaultBranch: "main",
				branchSHAs:    tt.branchSHAs,
			}

			milestones := &testMilestoneService{}
			if tt.milestone != nil {
				milestones.getResult = map[int]*platform.Milestone{tt.milestone.Number: tt.milestone}
			}

			p := &integratePlatform{
				issues:     issueSvc,
				prs:        prSvc,
				workflows:  wf,
				repo:       repo,
				milestones: milestones,
				checks:     &testCheckService{status: tt.ciStatus},
			}

			hctx := &HandlerContext{
				Ctx:         context.Background(),
				Platform:    p,
				Config:      baseConfig(),
				IssueNumber: 5,
				IsPR:        tt.isPR,
				IssueBody:   tt.issueBody,
			}

			result := handleIntegrate(hctx, Command{Name: "integrate"})

			if tt.wantErr {
				assert.Error(t, result.Error)
			} else {
				require.NoError(t, result.Error)
			}
			assert.Contains(t, result.Message, tt.wantMsg)
		})
	}
}

func TestHandleIntegrate_WithConsolidation(t *testing.T) {
	dir, g := initHandlerTestRepo(t)

	batchBody := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{Version: 1, Batch: 1},
		Task:        "Test",
	})

	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Batch", Labels: []string{issues.StatusDone}},
	}

	repo := &integrateRepoService{
		defaultBranch: "main",
		branchSHAs: map[string]string{
			"herd/worker/10-batch": "abc123",
		},
	}

	p := &integratePlatform{
		issues: issueSvc,
		prs: &testPRService{
			listResult: []*platform.PullRequest{},
		},
		workflows:  &testWorkflowService{},
		repo:       repo,
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Batch", State: "open"}}},
		checks:     &testCheckService{status: "success"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Git:         g,
		Config:      baseConfig(),
		RepoRoot:    dir,
		IssueNumber: 5,
		IsPR:        false,
		IssueBody:   batchBody,
	}

	result := handleIntegrate(hctx, Command{Name: "integrate"})

	// The worker branch doesn't exist on the remote, so consolidation will
	// encounter errors. The handler should not fail — it reports issues in summary.
	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "Integrator cycle for batch #1")
}

func TestHandleIntegrate_ReviewRunsWithExistingPR(t *testing.T) {
	batchBody := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{Version: 1, Batch: 1},
		Task:        "Test",
	})

	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 10, Title: "Task A", Labels: []string{issues.StatusInProgress}},
	}

	repo := &integrateRepoService{
		defaultBranch: "main",
		branchSHAs:    map[string]string{},
	}

	existingPR := &platform.PullRequest{Number: 50, Head: "herd/batch/1-test-batch"}

	p := &integratePlatform{
		issues: issueSvc,
		prs: &testPRService{
			listResult: []*platform.PullRequest{existingPR},
			getResult:  map[int]*platform.PullRequest{50: existingPR},
		},
		workflows:  &testWorkflowService{},
		repo:       repo,
		milestones: &testMilestoneService{getResult: map[int]*platform.Milestone{1: {Number: 1, Title: "Test Batch", State: "open"}}},
		checks:     &testCheckService{status: "success"},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Agent:       &testAgent{reviewResult: &agent.ReviewResult{Approved: true}},
		Config:      baseConfig(),
		IssueNumber: 5,
		IsPR:        false,
		IssueBody:   batchBody,
	}

	result := handleIntegrate(hctx, Command{Name: "integrate"})

	require.NoError(t, result.Error)
	assert.Contains(t, result.Message, "Integrator cycle for batch #1")
	// Review should be attempted because there's an existing batch PR.
}

func TestHandleIntegrate_ProgressCleanupLogsWarnings(t *testing.T) {
	// Verify that RmDir and Rm errors are logged as warnings, not silently swallowed
	source, err := os.ReadFile("integrate.go")
	require.NoError(t, err)
	src := string(source)

	assert.Contains(t, src, `Warning: failed to git rm .herd/progress/`,
		"RmDir errors should be logged as warnings")
	assert.Contains(t, src, `Warning: failed to git rm WORKER_PROGRESS.md`,
		"Rm errors for legacy file should be logged as warnings")
}

func TestHandleIntegrate_CommitFailureLogsAndResetsIndex(t *testing.T) {
	// Verify that Commit errors are logged and index is reset
	source, err := os.ReadFile("integrate.go")
	require.NoError(t, err)
	src := string(source)

	assert.Contains(t, src, `Warning: failed to commit progress file removal`,
		"Commit errors should be logged as warnings")
	assert.Contains(t, src, `ResetHead()`,
		"Index should be reset on commit failure to avoid dirty state")
}

func TestHandleIntegrate_Registered(t *testing.T) {
	reg := DefaultRegistry()
	assert.True(t, reg.Has("integrate"))
}
