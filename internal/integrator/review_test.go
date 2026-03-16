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
	assert.Len(t, result.FixIssues, 2)
	assert.Len(t, createdIssues, 2)
	assert.Len(t, wf.dispatched, 2)
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
		reviewResult: &agent.ReviewResult{Approved: false, Comments: []string{"issue found"}},
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
		reviewResult: &agent.ReviewResult{Approved: false, Comments: []string{"issue found"}},
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
			{Number: 60, Body: "---\nherd:\n  version: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nFix it\n"},
		},
	)

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
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

	// Generate 11 comments (exceeds safety limit of 10)
	comments := make([]string, 11)
	for i := range comments {
		comments[i] = "issue found"
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: false, Comments: comments},
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
	capturingAgent := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}
	// Override Review to capture opts
	origReview := capturingAgent.reviewResult
	captureAgent := &capturingMockAgent{
		result:       origReview,
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

// mockCapturingPRService wraps mockPRService and captures AddComment calls.
type mockCapturingPRService struct {
	*mockPRService
	comments []string
}

func (m *mockCapturingPRService) AddComment(_ context.Context, _ int, body string) error {
	m.comments = append(m.comments, body)
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

	callCount := 0
	// First Create succeeds, second fails
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			callCount++
			if callCount == 2 {
				return nil, fmt.Errorf("create failed")
			}
			return &platform.Issue{Number: 100 + callCount, Title: title}, nil
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
			Comments: []string{"Issue one", "Issue two"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	// Only 1 fix issue created (second Create failed)
	assert.Len(t, result.FixIssues, 1)

	// The findings comment should report 1 dispatched worker, not 2
	require.NotEmpty(t, prSvc.comments)
	findingsComment := ""
	for _, c := range prSvc.comments {
		if strings.HasPrefix(c, "🔍") {
			findingsComment = c
			break
		}
	}
	require.NotEmpty(t, findingsComment, "expected a findings comment")
	assert.Contains(t, findingsComment, "Dispatching 1 fix worker.")
	assert.NotContains(t, findingsComment, "Dispatching 2 fix workers.")
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
			Comments: []string{"Issue one", "Issue two"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})

	require.NoError(t, err)
	assert.Empty(t, result.FixIssues)

	// No findings comment should be posted when all creates fail
	for _, c := range prSvc.comments {
		assert.NotContains(t, c, "Dispatching 0 fix workers", "findings comment must not be posted when n=0")
		assert.False(t, strings.HasPrefix(c, "🔍"), "findings comment must not be posted when n=0")
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
