package integrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestRepo creates a minimal git repo with main and batch branches for testing.
func initTestRepo(t *testing.T) (string, *git.Git) {
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

// --- Mock Agent ---

type mockReviewAgent struct {
	reviewResult *agent.ReviewResult
	reviewErr    error
}

func (m *mockReviewAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (m *mockReviewAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (m *mockReviewAgent) Review(_ context.Context, _ string, _ agent.ReviewOptions) (*agent.ReviewResult, error) {
	return m.reviewResult, m.reviewErr
}

// Helper to build a standard test platform for review tests
func newReviewTestPlatform(prList []*platform.PullRequest, milestoneIssues []*platform.Issue) *mockPlatform {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = milestoneIssues

	return &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: prList,
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}
}

func TestReview_NoBatchPR(t *testing.T) {
	mock := newReviewTestPlatform(nil, nil)

	result, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewParams{RunID: 100, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Equal(t, 0, result.BatchPRNumber)
}

func TestReview_Disabled(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		nil,
	)

	result, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator: config.Integrator{Review: false},
	}, ReviewParams{RunID: 100, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_Approved(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n\n## Acceptance Criteria\n\n- [ ] Works\n"},
		},
	)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_ChangesRequested_CreatesFixes(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}
	// Track created issues
	createdIssues := []*platform.Issue{}
	nextNum := 100

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	// Override Create on the issue service to track creations
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			iss := &platform.Issue{Number: nextNum, Title: title}
			nextNum++
			createdIssues = append(createdIssues, iss)
			return iss, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Tests not covering edge case"},
			},
			Comments: []string{"Missing error handling in auth.go", "Tests not covering edge case"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Equal(t, 1, result.FixCycle)
	assert.Len(t, result.FixIssues, 1)
	assert.Len(t, createdIssues, 1)
	assert.Len(t, wf.dispatched, 1)
	assert.Equal(t, "Review fixes (cycle 1)", createdIssues[0].Title)
}

func TestReview_SkipsWhenFixWorkersInProgress(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// A fix issue is still in-progress from a previous review cycle
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix: something", Labels: []string{issues.StatusInProgress},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix it\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "issue found"}},
			Comments: []string{"issue found"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// Should skip — no review ran, no approval, no fixes created
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_SkipsWhenFixWorkersReady(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// A fix issue is ready (not yet dispatched)
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix: something", Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix it\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "issue found"}},
			Comments: []string{"issue found"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues)
}

func TestReview_RunsWhenAllFixWorkersDone(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// All fix issues are done — review should proceed
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix: something", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix it\n"},
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		issueSvc.listResult,
	)
	// Override the issue service with our custom one
	mock.issues = issueSvc

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
}

func TestReview_SkipsWhenCIFixInProgress(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// A CI fix issue is in-progress
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix CI", Labels: []string{issues.StatusInProgress},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  ci_fix_cycle: 1\n---\n\n## Task\nFix CI\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues)
}

func TestReview_MaxCyclesHit(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
			// Existing fix issue at cycle 3
			{Number: 60, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nFix it\n",
				Labels: []string{issues.StatusDone}},
		},
	)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Still broken"},
			},
			Comments: []string{"Still broken"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.MaxCyclesHit)
}

func TestReview_SafetyValve(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	// Generate 11 HIGH findings (exceeds safety limit of 10)
	findings := make([]agent.ReviewFinding, 11)
	comments := make([]string, 11)
	for i := range findings {
		findings[i] = agent.ReviewFinding{Severity: "HIGH", Description: "issue found"}
		comments[i] = "issue found"
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: false, Findings: findings, Comments: comments},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 10},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.MaxCyclesHit)
}

func TestReview_AutoMerge(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRService{
		listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
	}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator:   config.Integrator{Review: true, Strategy: "squash", ReviewMaxFixCycles: 3},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.True(t, prSvc.merged)
}

func TestPostMergeCleanup(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 10}, {Number: 11},
	}

	msSvc := &mockMilestoneService{}
	repoSvc := &mockRepoService{}
	mock := &mockPlatform{
		issues:     issueSvc,
		milestones: msSvc,
		repo:       repoSvc,
	}

	err := postMergeCleanup(context.Background(), mock, 1, "herd/batch/1-test")
	require.NoError(t, err)

	// Should close all issues
	assert.Contains(t, issueSvc.updatedIssues, 10)
	assert.Contains(t, issueSvc.updatedIssues, 11)
	assert.Equal(t, "closed", *issueSvc.updatedIssues[10].State)
	assert.Equal(t, "closed", *issueSvc.updatedIssues[11].State)

	// Should close milestone
	assert.Contains(t, msSvc.updatedNumbers, 1)
	assert.Contains(t, msSvc.updatedStates, "closed")

	// Should delete batch branch
	assert.Equal(t, "herd/batch/1-test", repoSvc.deletedBranch)
}

func TestReview_LoadsRoleInstructions(t *testing.T) {
	dir, g := initTestRepo(t)

	// Create .herd/integrator.md
	require.NoError(t, os.MkdirAll(dir+"/.herd", 0755))
	require.NoError(t, os.WriteFile(dir+"/.herd/integrator.md", []byte("Be strict about error handling"), 0644))

	// Use a capturing agent to verify system prompt is passed
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, "Be strict about error handling", capturedOpts.SystemPrompt)
}

func TestReview_ByPRNumber(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
	}

	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			1: {Number: 1, Title: "Batch"},
		},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: msSvc,
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_BatchLookup(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 60, Title: "[herd] Batch", Head: "herd/batch/1-batch"}},
		},
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{
			getResult: map[int]*platform.Milestone{
				1: {Number: 1, Title: "Batch"},
			},
		},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{BatchNumber: 1, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Equal(t, 60, result.BatchPRNumber)
}

func TestReview_BatchLookup_NoPR(t *testing.T) {
	mock := &mockPlatform{
		issues: newMockIssueService(),
		prs: &mockPRService{
			listResult: []*platform.PullRequest{},
		},
		workflows: &mockWorkflowService{},
		repo:      &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{
			getResult: map[int]*platform.Milestone{
				5: {Number: 5, Title: "My Feature"},
			},
		},
	}

	result, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator: config.Integrator{Review: true},
	}, ReviewParams{BatchNumber: 5, RepoRoot: t.TempDir()})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Equal(t, 0, result.BatchPRNumber)
}

func TestReview_AutoMergeFailure(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRServiceWithMergeErr{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		mergeErr: fmt.Errorf("merge conflict on GitHub"),
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator:   config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	// Should propagate the merge error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auto-merging batch PR")
	// Post-merge cleanup should NOT have run (milestone not closed)
	assert.Empty(t, issueSvc.updatedIssues)
}

func TestReview_DisabledAutoMergeFailure(t *testing.T) {
	// When review is disabled but auto-merge fails, error should propagate
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{}

	prSvc := &mockPRServiceWithMergeErr{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		mergeErr: fmt.Errorf("branch protection"),
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs:    prSvc,
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	_, err := Review(context.Background(), mock, &mockReviewAgent{}, nil, &config.Config{
		Integrator:   config.Integrator{Review: false},
		PullRequests: config.PullRequests{AutoMerge: true},
	}, ReviewParams{RunID: 100, RepoRoot: t.TempDir()})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auto-merging batch PR")
	assert.Empty(t, issueSvc.updatedIssues) // No cleanup ran
}

func TestReview_AgentError(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	ag := &mockReviewAgent{
		reviewErr: fmt.Errorf("agent crashed"),
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent review failed")
}

// mockCapturingPRService wraps mockPRService and captures AddComment and CreateReview calls.
type mockCapturingPRService struct {
	*mockPRService
	comments []string
	reviews  []capturedReview
}

type capturedReview struct {
	body  string
	event platform.ReviewEvent
}

func (m *mockCapturingPRService) AddComment(_ context.Context, _ int, body string) error {
	m.comments = append(m.comments, body)
	return nil
}

func (m *mockCapturingPRService) CreateReview(_ context.Context, _ int, body string, event platform.ReviewEvent) error {
	m.reviews = append(m.reviews, capturedReview{body: body, event: event})
	return nil
}

func TestReview_DispatchCountAccurateWhenSomeCreatesFail(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	// Create always succeeds (single batched issue now)
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Issue one"},
				{Severity: "HIGH", Description: "Issue two"},
			},
			Comments: []string{"Issue one", "Issue two"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// One batched fix issue
	assert.Len(t, result.FixIssues, 1)

	// The findings comment should contain structured HIGH section
	require.NotEmpty(t, prSvc.comments)
	findingsComment := ""
	for _, c := range prSvc.comments {
		if strings.HasPrefix(c, "🔍") {
			findingsComment = c
			break
		}
	}
	require.NotEmpty(t, findingsComment, "expected a findings comment")
	assert.Contains(t, findingsComment, "**HIGH**")
}

func TestReview_NoCommentWhenAllCreatesFail(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	// All Creates fail
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return nil, fmt.Errorf("create failed")
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Issue one"},
				{Severity: "HIGH", Description: "Issue two"},
			},
			Comments: []string{"Issue one", "Issue two"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Empty(t, result.FixIssues)
	assert.True(t, result.AllCreatesFailed, "AllCreatesFailed must be true when all issue creates fail")
	assert.Equal(t, 2, result.FindingsCount, "FindingsCount must reflect the number of high findings")

	// No findings comment should be posted when all creates fail
	for _, c := range prSvc.comments {
		assert.False(t, strings.HasPrefix(c, "🔍"), "findings comment must not be posted when create fails")
	}
}

func TestParseBatchBranchMilestone(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		want    int
		wantErr bool
	}{
		{"valid", "herd/batch/4-some-slug", 4, false},
		{"valid single digit", "herd/batch/1-batch", 1, false},
		{"valid multi digit", "herd/batch/42-long-name-here", 42, false},
		{"not a batch branch", "herd/worker/10-task", 0, true},
		{"no dash", "herd/batch/4", 0, true},
		{"not a number", "herd/batch/abc-slug", 0, true},
		{"random string", "main", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBatchBranchMilestone(tt.branch)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 3))
	assert.Equal(t, "first line", truncate("first line\nsecond line", 60))
}

// capturingMockAgent captures ReviewOptions for assertions
type capturingMockAgent struct {
	result       *agent.ReviewResult
	capturedOpts *agent.ReviewOptions
}

func (m *capturingMockAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (m *capturingMockAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (m *capturingMockAgent) Review(_ context.Context, _ string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	*m.capturedOpts = opts
	return m.result, nil
}

// mockPRServiceWithMergeErr wraps mockPRService to fail on Merge
type mockPRServiceWithMergeErr struct {
	*mockPRService
	mergeErr error
}

func (m *mockPRServiceWithMergeErr) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, m.mergeErr
}

// mockIssueServiceWithCreate wraps mockIssueService to override Create
type mockIssueServiceWithCreate struct {
	*mockIssueService
	onCreate func(title, body string, labels []string, milestone *int) (*platform.Issue, error)
}

func (m *mockIssueServiceWithCreate) Create(_ context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	if m.onCreate != nil {
		return m.onCreate(title, body, labels, milestone)
	}
	return nil, nil
}

// --- New Tests ---

func TestReview_OnlyMediumLowFindings_Approves(t *testing.T) {
	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "Could handle edge case"},
				{Severity: "LOW", Description: "Typo in comment"},
			},
			Comments: []string{"Could handle edge case", "Typo in comment"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved, "Should approve when no HIGH findings")
}

func TestReview_RequestChangesReview(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Critical bug"},
			},
			Comments: []string{"Critical bug"},
		},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// Verify CreateReview was called with REQUEST_CHANGES
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewRequestChanges, prSvc.reviews[0].event)
	assert.Contains(t, prSvc.reviews[0].body, "high-severity issues")
}

func TestReview_BatchFixIssue_SingleIssue(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	var createdTitle string
	var createdBody string
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdTitle = title
			createdBody = body
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Bug A"},
				{Severity: "HIGH", Description: "Bug B"},
				{Severity: "HIGH", Description: "Bug C"},
			},
			Comments: []string{"Bug A", "Bug B", "Bug C"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Len(t, result.FixIssues, 1, "Should create ONE fix issue per cycle")
	assert.Equal(t, "Review fixes (cycle 1)", createdTitle)
	assert.Contains(t, createdBody, "Bug A")
	assert.Contains(t, createdBody, "Bug B")
	assert.Contains(t, createdBody, "Bug C")
}

func TestReview_DedupFindings(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// Include an open fix issue that matches one of the findings
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix: Missing error handling in auth.go\n"},
	}

	createCalled := false
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createCalled = true
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Race condition in worker pool"},
			},
			Comments: []string{"Missing error handling in auth.go", "Race condition in worker pool"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, createCalled, "Should create fix issue for non-duplicate finding")
	assert.Len(t, result.FixIssues, 1)
}

func TestReview_AllFindingsDeduped_Approves(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// Open fix issue whose body matches ALL high findings
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		{Number: 80, State: "open", Title: "Review fixes (cycle 1)",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix: Missing error handling in auth.go\nFix: Race condition in worker pool\n"},
	}

	createCalled := false
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createCalled = true
			return &platform.Issue{Number: 100, Title: title}, nil
		},
	}

	prSvc := &mockCapturingPRService{
		mockPRService: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs:    prSvc,
		workflows: &mockWorkflowService{runs: map[int64]*platform.Run{
			100: {ID: 100, Inputs: map[string]string{"issue_number": "42"}},
		}},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing error handling in auth.go"},
				{Severity: "HIGH", Description: "Race condition in worker pool"},
			},
			Comments: []string{"Missing error handling in auth.go", "Race condition in worker pool"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved, "Should approve when all findings are deduped")
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.False(t, createCalled, "Should not create new fix issues")

	// Should post an informational comment
	require.NotEmpty(t, prSvc.comments)
	assert.Contains(t, prSvc.comments[0], "already covered by existing fix workers")

	// Should submit an approval review
	require.Len(t, prSvc.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, prSvc.reviews[0].event)
}

func TestReview_SkipsCompletedBatch(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch", State: "closed"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved, "Should not mark as approved when skipping completed batch")
	assert.Equal(t, 50, result.BatchPRNumber)
	assert.Nil(t, result.FixIssues)
}

func TestReview_SkipsWhenSomeFixWorkersStillRunning(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// 3 fix issues: 2 done, 1 in-progress → should skip
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix 1", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 1\n"},
		{Number: 81, Title: "Fix 2", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 2\n"},
		{Number: 82, Title: "Fix 3", Labels: []string{issues.StatusInProgress},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 3\n"},
	}

	mock := &mockPlatform{
		issues: issueSvc,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main"},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{{Severity: "HIGH", Description: "something"}},
			Comments: []string{"something"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Nil(t, result.FixIssues, "Should skip review when fix worker is still in progress")
	assert.Equal(t, 50, result.BatchPRNumber)
}

func TestReview_ProceedsWhenAllFixWorkersDone_MultipleIssues(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	// 3 fix issues: all done → should proceed
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Title: "Task", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		{Number: 80, Title: "Fix 1", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 1\n"},
		{Number: 81, Title: "Fix 2", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 2\n"},
		{Number: 82, Title: "Fix 3", Labels: []string{issues.StatusDone},
			Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 1\n---\n\n## Task\nFix 3\n"},
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		issueSvc.listResult,
	)
	mock.issues = issueSvc

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "All good"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.True(t, result.Approved, "Should proceed and approve when all fix workers are done")
}

func TestReview_StrictnessPassedToAgent(t *testing.T) {
	var capturedOpts agent.ReviewOptions
	captureAgent := &capturingMockAgent{
		result:       &agent.ReviewResult{Approved: true, Summary: "LGTM"},
		capturedOpts: &capturedOpts,
	}

	mock := newReviewTestPlatform(
		[]*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		[]*platform.Issue{
			{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
		},
	)

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, captureAgent, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3, ReviewStrictness: "strict"},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Equal(t, "strict", capturedOpts.Strictness)
}

func TestFilterFindingsBySeverity(t *testing.T) {
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "bug"},
		{Severity: "MEDIUM", Description: "edge case"},
		{Severity: "LOW", Description: "style"},
		{Severity: "high", Description: "another bug"}, // case insensitive
		{Severity: "", Description: "unknown defaults to low"},
	}
	high, medium, low := filterFindingsBySeverity(findings)
	assert.Len(t, high, 2)
	assert.Len(t, medium, 1)
	assert.Len(t, low, 2) // empty severity defaults to low
}

func TestDedupFindings(t *testing.T) {
	findings := []agent.ReviewFinding{
		{Severity: "HIGH", Description: "Missing error handling in auth.go"},
		{Severity: "HIGH", Description: "Race condition in worker pool"},
	}
	openFixes := []*platform.Issue{
		{Number: 80, Title: "Fix: Missing error handling in auth.go", Body: "Fix it"},
	}
	result := dedupFindings(findings, openFixes)
	assert.Len(t, result, 1)
	assert.Equal(t, "Race condition in worker pool", result[0].Description)
}

func TestBuildReviewCycleComment(t *testing.T) {
	high := []agent.ReviewFinding{{Severity: "HIGH", Description: "bug"}}
	medium := []agent.ReviewFinding{{Severity: "MEDIUM", Description: "edge case"}}
	low := []agent.ReviewFinding{{Severity: "LOW", Description: "style"}}

	comment := buildReviewCycleComment(2, 5, []int{100}, high, medium, low)
	assert.Contains(t, comment, "cycle 2 of 5")
	assert.Contains(t, comment, "Found 3 issues")
	assert.Contains(t, comment, "**HIGH** (fix worker dispatched → #100)")
	assert.Contains(t, comment, "**MEDIUM** (informational)")
	assert.Contains(t, comment, "**LOW** (informational)")
}

func TestBuildReviewCycleComment_NoCycle(t *testing.T) {
	medium := []agent.ReviewFinding{{Severity: "MEDIUM", Description: "edge case"}}
	comment := buildReviewCycleComment(0, 3, nil, nil, medium, nil)
	assert.Contains(t, comment, "🔍 **HerdOS Agent Review**\n\n")
	assert.NotContains(t, comment, "cycle")
	assert.Contains(t, comment, "Found 1 issues")
}

func TestBuildBatchSummaryComment(t *testing.T) {
	allIssues := []*platform.Issue{
		{Number: 1, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo A\n"},
		{Number: 2, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo B\n"},
		{Number: 3, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 2\n---\n\n## Task\nFix\n"},
		{Number: 4, Body: "---\nherd:\n  version: 1\n  ci_fix_cycle: 1\n---\n\n## Task\nCI Fix\n"},
	}

	comment := buildBatchSummaryComment(allIssues, "All looks good")
	assert.Contains(t, comment, "✅ **HerdOS Agent Review**")
	assert.Contains(t, comment, "All looks good")
	assert.Contains(t, comment, "Original tasks: 2")
	assert.Contains(t, comment, "Fix issues created: 2")
	assert.Contains(t, comment, "Review cycles: 2")
	assert.Contains(t, comment, "CI fix cycles: 1")
	assert.Contains(t, comment, "Total issues: 4")
}
