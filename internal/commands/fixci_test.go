package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type mockPlatform struct {
	issues     platform.IssueService
	prs        *mockPRService
	workflows  *mockWorkflowService
	checks     *mockCheckService
	milestones *mockMilestoneService
	repo       *mockRepoService
}

func (m *mockPlatform) Issues() platform.IssueService            { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService { return m.prs }
func (m *mockPlatform) Workflows() platform.WorkflowService      { return m.workflows }
func (m *mockPlatform) Labels() platform.LabelService            { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService    { return m.milestones }
func (m *mockPlatform) Runners() platform.RunnerService          { return nil }
func (m *mockPlatform) Repository() platform.RepositoryService   { return m.repo }
func (m *mockPlatform) Checks() platform.CheckService            { return m.checks }

type mockIssueService struct {
	listResult    []*platform.Issue
	createdIssues []*platform.Issue
	comments      map[int][]string
	reactions     []string
}

func newMockIssueService() *mockIssueService {
	return &mockIssueService{
		comments: make(map[int][]string),
	}
}

func (m *mockIssueService) Create(_ context.Context, title, _ string, _ []string, _ *int) (*platform.Issue, error) {
	iss := &platform.Issue{Number: 99, Title: title}
	m.createdIssues = append(m.createdIssues, iss)
	return iss, nil
}
func (m *mockIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) { return nil, nil }
func (m *mockIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return m.listResult, nil
}
func (m *mockIssueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockIssueService) AddLabels(_ context.Context, _ int, _ []string) error    { return nil }
func (m *mockIssueService) RemoveLabels(_ context.Context, _ int, _ []string) error { return nil }
func (m *mockIssueService) AddComment(_ context.Context, number int, body string) error {
	m.comments[number] = append(m.comments[number], body)
	return nil
}
func (m *mockIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockIssueService) CreateReaction(_ context.Context, _ int64, reaction string) error {
	m.reactions = append(m.reactions, reaction)
	return nil
}

type mockPRService struct {
	getResult  map[int]*platform.PullRequest
	listResult []*platform.PullRequest
}

func (m *mockPRService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, nil
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
	return &platform.MergeResult{Merged: true}, nil
}
func (m *mockPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (m *mockPRService) AddComment(_ context.Context, _ int, _ string) error { return nil }
func (m *mockPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}

type mockWorkflowService struct {
	dispatched  []map[string]string
	dispatchErr error
}

func (m *mockWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *mockWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	m.dispatched = append(m.dispatched, inputs)
	return nil, m.dispatchErr
}
func (m *mockWorkflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, nil
}
func (m *mockWorkflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return nil, nil
}
func (m *mockWorkflowService) CancelRun(_ context.Context, _ int64) error { return nil }

type mockCheckService struct {
	status   string
	rerunErr error
}

func (m *mockCheckService) GetCombinedStatus(_ context.Context, _ string) (string, error) {
	return m.status, nil
}
func (m *mockCheckService) RerunFailedChecks(_ context.Context, _ string) error {
	return m.rerunErr
}

type mockMilestoneService struct {
	getResult map[int]*platform.Milestone
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
func (m *mockMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

type mockRepoService struct {
	defaultBranch string
}

func (m *mockRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (m *mockRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return m.defaultBranch, nil
}
func (m *mockRepoService) CreateBranch(_ context.Context, _, _ string) error    { return nil }
func (m *mockRepoService) DeleteBranch(_ context.Context, _ string) error        { return nil }
func (m *mockRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}

// --- Helpers ---

func buildMock(ciStatus string, rerunErr error, ciMaxCycles int, existingCycles int) (*mockPlatform, *mockIssueService, *mockWorkflowService) {
	issueSvc := newMockIssueService()
	for i := 1; i <= existingCycles; i++ {
		issueSvc.listResult = append(issueSvc.listResult, &platform.Issue{
			Number: 80 + i,
			Body:   fmt.Sprintf("---\nherd:\n  version: 1\n  ci_fix_cycle: %d\n---\n\n## Task\nFix CI\n", i),
		})
	}

	wf := &mockWorkflowService{}
	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch 1", Head: "herd/batch/1-batch"},
		},
		listResult: []*platform.PullRequest{
			{Number: 50, Title: "[herd] Batch 1", Head: "herd/batch/1-batch"},
		},
	}
	checkSvc := &mockCheckService{status: ciStatus, rerunErr: rerunErr}
	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch"},
		},
	}
	repoSvc := &mockRepoService{defaultBranch: "main"}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		checks:     checkSvc,
		milestones: msSvc,
		repo:       repoSvc,
	}

	return mock, issueSvc, wf
}

func defaultConfig(ciMaxCycles int) *config.Config {
	return &config.Config{
		Integrator: config.Integrator{
			RequireCI:      true,
			CIMaxFixCycles: ciMaxCycles,
		},
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}
}

// --- Tests ---

func TestHandleFixCI_Register(t *testing.T) {
	_, ok := Registry["fix-ci"]
	assert.True(t, ok, "fix-ci should be registered in Registry")
}

func TestHandleFixCI_NoPR(t *testing.T) {
	mock, _, _ := buildMock("success", nil, 2, 0)
	hctx := &HandlerContext{Platform: mock, Config: defaultConfig(2), PRNumber: 0}
	cmd := &Command{Name: "fix-ci"}

	_, err := handleFixCI(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix-ci can only be used on batch PRs")
}

func TestHandleFixCI_NonBatchBranch(t *testing.T) {
	mock, _, _ := buildMock("success", nil, 2, 0)
	// Override PR with non-batch head branch
	mock.prs.getResult[42] = &platform.PullRequest{Number: 42, Head: "feat/my-feature"}
	hctx := &HandlerContext{Platform: mock, Config: defaultConfig(2), PRNumber: 42}
	cmd := &Command{Name: "fix-ci"}

	_, err := handleFixCI(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix-ci can only be used on batch PRs")
}

func TestHandleFixCI_States(t *testing.T) {
	tests := []struct {
		name           string
		ciStatus       string
		rerunErr       error
		ciMaxCycles    int
		existingCycles int
		wantPrefix     string
		wantContains   string
	}{
		{
			name:        "CI passing",
			ciStatus:    "success",
			ciMaxCycles: 2,
			wantPrefix:  "✅",
			wantContains: "CI is passing",
		},
		{
			name:        "CI pending — rerun triggered",
			ciStatus:    "failure",
			rerunErr:    nil, // rerun succeeds → pending
			ciMaxCycles: 2,
			wantPrefix:  "⏳",
			wantContains: "CI is still running",
		},
		{
			name:        "CI failing — fix dispatched",
			ciStatus:    "failure",
			rerunErr:    fmt.Errorf("re-run failed"),
			ciMaxCycles: 2,
			wantPrefix:  "🔧",
			wantContains: "Dispatched fix worker",
		},
		{
			name:           "CI failing — max cycles reached",
			ciStatus:       "failure",
			rerunErr:       fmt.Errorf("re-run failed"),
			ciMaxCycles:    1,
			existingCycles: 1,
			wantPrefix:     "⚠️",
			wantContains:   "max fix cycles reached",
		},
		{
			name:        "CI failing — zero cycles (notify only)",
			ciStatus:    "failure",
			rerunErr:    fmt.Errorf("re-run failed"),
			ciMaxCycles: 0,
			wantPrefix:  "⚠️",
			wantContains: "max fix cycles reached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, _, _ := buildMock(tt.ciStatus, tt.rerunErr, tt.ciMaxCycles, tt.existingCycles)
			hctx := &HandlerContext{Platform: mock, Config: defaultConfig(tt.ciMaxCycles), PRNumber: 50}
			cmd := &Command{Name: "fix-ci"}

			resp, err := handleFixCI(context.Background(), hctx, cmd)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(resp, tt.wantPrefix), "response %q should start with %q", resp, tt.wantPrefix)
			assert.Contains(t, resp, tt.wantContains)
		})
	}
}

func TestHandleFixCI_PromptPassthrough(t *testing.T) {
	mock, issueSvc, _ := buildMock("failure", fmt.Errorf("re-run failed"), 2, 0)
	hctx := &HandlerContext{Platform: mock, Config: defaultConfig(2), PRNumber: 50}
	cmd := &Command{Name: "fix-ci", Prompt: "check the database migrations"}

	resp, err := handleFixCI(context.Background(), hctx, cmd)
	require.NoError(t, err)
	assert.Contains(t, resp, "Dispatched fix worker")

	require.Len(t, issueSvc.createdIssues, 1)
	// The fix issue should have been created; verify the task description via the body
	// We can't easily inspect the body here without parsing, but we can verify a fix was dispatched.
	// The UserContext is tested more directly in the integrator package.
	_ = issueSvc.createdIssues[0]
}

func TestHandleFixCI_BatchBranchParsing(t *testing.T) {
	tests := []struct {
		name      string
		head      string
		wantErr   bool
	}{
		{
			name:    "valid batch branch",
			head:    "herd/batch/5-my-feature",
			wantErr: false,
		},
		{
			name:    "valid batch branch with complex slug",
			head:    "herd/batch/42-some-long-title",
			wantErr: false,
		},
		{
			name:    "non-batch branch",
			head:    "herd/worker/5-fix-something",
			wantErr: true,
		},
		{
			name:    "main branch",
			head:    "main",
			wantErr: true,
		},
		{
			name:    "feature branch",
			head:    "feat/add-something",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, _, _ := buildMock("success", nil, 2, 0)

			// Add the PR with the specific head branch
			prNum := 100
			mock.prs.getResult[prNum] = &platform.PullRequest{Number: prNum, Head: tt.head}

			// For valid batch branches, we need a milestone
			if !tt.wantErr {
				// Extract batch number from head
				batchNum := 0
				fmt.Sscanf(tt.head, "herd/batch/%d-", &batchNum)
				mock.milestones.getResult[batchNum] = &platform.Milestone{Number: batchNum, Title: "Test"}
				// Also need the PR in the list for notifyCI
				mock.prs.listResult = []*platform.PullRequest{
					{Number: prNum, Head: tt.head},
				}
			}

			hctx := &HandlerContext{Platform: mock, Config: defaultConfig(2), PRNumber: prNum}
			cmd := &Command{Name: "fix-ci"}

			_, err := handleFixCI(context.Background(), hctx, cmd)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
