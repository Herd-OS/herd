package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildFixMock(existingFixCycles int) (*mockPlatform, *mockIssueService, *mockWorkflowService) {
	issueSvc := newMockIssueService()
	for i := 1; i <= existingFixCycles; i++ {
		issueSvc.listResult = append(issueSvc.listResult, &platform.Issue{
			Number: 70 + i,
			Body:   fmt.Sprintf("---\nherd:\n  version: 1\n  fix_cycle: %d\n---\n\n## Task\nSome fix\n", i),
		})
	}

	wf := &mockWorkflowService{}
	prSvc := &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch 3", Head: "herd/batch/3-my-batch"},
		},
	}
	msSvc := &mockMilestoneService{
		getResult: map[int]*platform.Milestone{
			3: {Number: 3, Title: "My Batch"},
		},
	}
	repoSvc := &mockRepoService{defaultBranch: "main"}

	mock := &mockPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		milestones: msSvc,
		repo:       repoSvc,
	}
	return mock, issueSvc, wf
}

func fixConfig() *config.Config {
	return &config.Config{
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}
}

func TestHandleFix_Register(t *testing.T) {
	_, ok := Registry["fix"]
	assert.True(t, ok, "fix should be registered in Registry")
}

func TestHandleFix_EmptyPrompt(t *testing.T) {
	mock, _, _ := buildFixMock(0)
	hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 50}
	cmd := &Command{Name: "fix", Prompt: ""}

	_, err := handleFix(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Usage:")
	assert.Contains(t, err.Error(), "/herd fix")
}

func TestHandleFix_NoPR(t *testing.T) {
	mock, _, _ := buildFixMock(0)
	hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 0}
	cmd := &Command{Name: "fix", Prompt: "footer links are broken"}

	_, err := handleFix(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix can only be used on batch PRs")
}

func TestHandleFix_NonBatchBranch(t *testing.T) {
	mock, _, _ := buildFixMock(0)
	mock.prs.getResult[42] = &platform.PullRequest{Number: 42, Head: "feat/my-feature"}
	hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 42}
	cmd := &Command{Name: "fix", Prompt: "fix the broken thing"}

	_, err := handleFix(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix can only be used on batch PRs")
}

func TestHandleFix_Success(t *testing.T) {
	mock, issueSvc, wf := buildFixMock(0)
	hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 50}
	cmd := &Command{Name: "fix", Prompt: "footer links are broken"}

	resp, err := handleFix(context.Background(), hctx, cmd)
	require.NoError(t, err)
	assert.Contains(t, resp, "🔧")
	assert.Contains(t, resp, "Created fix issue #99")
	assert.Contains(t, resp, "footer links are broken")

	require.Len(t, issueSvc.createdIssues, 1)
	assert.Equal(t, "Fix: footer links are broken", issueSvc.createdIssues[0].Title)

	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "99", wf.dispatched[0]["issue_number"])
	assert.Equal(t, "herd/batch/3-my-batch", wf.dispatched[0]["batch_branch"])
	assert.Equal(t, "30", wf.dispatched[0]["timeout_minutes"])
	assert.Equal(t, "herd-worker", wf.dispatched[0]["runner_label"])
}

func TestHandleFix_TitleTruncation(t *testing.T) {
	mock, issueSvc, _ := buildFixMock(0)
	hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 50}
	longPrompt := strings.Repeat("a", 80)
	cmd := &Command{Name: "fix", Prompt: longPrompt}

	_, err := handleFix(context.Background(), hctx, cmd)
	require.NoError(t, err)
	require.Len(t, issueSvc.createdIssues, 1)
	// title is "Fix: " + truncated(80 chars) → max len is "Fix: " + 60 + "..." = 68
	assert.True(t, strings.HasSuffix(issueSvc.createdIssues[0].Title, "..."))
}

func TestHandleFix_FixCycleIncrement(t *testing.T) {
	tests := []struct {
		name           string
		existingCycles int
	}{
		{"no existing fix issues", 0},
		{"one existing fix cycle", 1},
		{"two existing fix cycles", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, _, _ := buildFixMock(tt.existingCycles)
			hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 50}
			cmd := &Command{Name: "fix", Prompt: "some fix needed"}

			resp, err := handleFix(context.Background(), hctx, cmd)
			require.NoError(t, err)
			assert.Contains(t, resp, "🔧")
		})
	}
}

func TestHandleFix_DispatchFailure(t *testing.T) {
	mock, issueSvc, wf := buildFixMock(0)
	wf.dispatchErr = fmt.Errorf("workflow dispatch failed")
	hctx := &HandlerContext{Platform: mock, Config: fixConfig(), PRNumber: 50}
	cmd := &Command{Name: "fix", Prompt: "fix something"}

	_, err := handleFix(context.Background(), hctx, cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatching worker")

	// Issue #99 is created by the mock; verify it is cleaned up to failed state.
	require.Len(t, issueSvc.createdIssues, 1)
	issueNum := issueSvc.createdIssues[0].Number
	assert.Contains(t, issueSvc.removedLabels[issueNum], issues.StatusInProgress)
	assert.Contains(t, issueSvc.addedLabels[issueNum], issues.StatusFailed)
}

func TestMaxFixCycle(t *testing.T) {
	tests := []struct {
		name      string
		issues    []*platform.Issue
		wantCycle int
	}{
		{
			name:      "no issues",
			issues:    nil,
			wantCycle: 0,
		},
		{
			name: "issues with no fix cycle",
			issues: []*platform.Issue{
				{Body: "---\nherd:\n  version: 1\n---\n\n## Task\nFoo\n"},
			},
			wantCycle: 0,
		},
		{
			name: "single fix cycle",
			issues: []*platform.Issue{
				{Body: "---\nherd:\n  version: 1\n  fix_cycle: 2\n---\n\n## Task\nFoo\n"},
			},
			wantCycle: 2,
		},
		{
			name: "multiple fix cycles returns max",
			issues: []*platform.Issue{
				{Body: "---\nherd:\n  version: 1\n  fix_cycle: 1\n---\n\n## Task\nFoo\n"},
				{Body: "---\nherd:\n  version: 1\n  fix_cycle: 3\n---\n\n## Task\nBar\n"},
				{Body: "---\nherd:\n  version: 1\n  fix_cycle: 2\n---\n\n## Task\nBaz\n"},
			},
			wantCycle: 3,
		},
		{
			name: "invalid body skipped",
			issues: []*platform.Issue{
				{Body: "not valid yaml front matter"},
				{Body: "---\nherd:\n  version: 1\n  fix_cycle: 5\n---\n\n## Task\nFoo\n"},
			},
			wantCycle: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maxFixCycle(tt.issues)
			assert.Equal(t, tt.wantCycle, got)
		})
	}
}

func TestTruncateTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		max   int
		want  string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"too long", "hello world", 5, "hello..."},
		{"multiline truncates at newline", "first line\nsecond line", 100, "first line"},
		{"multiline then truncated", "a very long first line here", 10, "a very lon..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateTitle(tt.input, tt.max)
			assert.Equal(t, tt.want, got)
		})
	}
}
