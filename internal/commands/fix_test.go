package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{
			name:  "short ASCII string unchanged",
			input: "hello",
			n:     60,
			want:  "hello",
		},
		{
			name:  "exactly n runes unchanged",
			input: "abcde",
			n:     5,
			want:  "abcde",
		},
		{
			name:  "longer than n ASCII string truncated",
			input: "this is a very long string that exceeds sixty characters for sure yes indeed",
			n:     60,
			want:  "this is a very long string that exceeds sixty characters for" + "...",
		},
		{
			name:  "multi-byte UTF-8 string truncated safely",
			input: "日本語のテキストはマルチバイト文字を含むためバイト境界での切り捨ては危険です",
			n:     10,
			want:  "日本語のテキストはマ" + "...",
		},
		{
			name:  "multi-byte UTF-8 string shorter than n unchanged",
			input: "こんにちは",
			n:     60,
			want:  "こんにちは",
		},
		{
			name:  "empty string unchanged",
			input: "",
			n:     60,
			want:  "",
		},
		{
			name:  "mixed ASCII and multi-byte truncated at rune boundary",
			input: "hello 世界 world more text here",
			n:     8,
			want:  "hello 世界" + "...",
		},
		{
			name:  "n=0 always truncates non-empty",
			input: "abc",
			n:     0,
			want:  "...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.input, tc.n)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLooksLikeConflict(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"merge conflict lowercase", "there is a merge conflict", true},
		{"merge conflict mixed case", "Merge Conflict detected", true},
		{"rebase conflict", "Rebase conflict on main", true},
		{"conflict with main", "PR has conflict with main", true},
		{"conflict with master", "conflict with master branch", true},
		{"conflicts with main", "this conflicts with main", true},
		{"conflicts with master", "this conflicts with master", true},
		{"no conflict keywords", "fix the broken test", false},
		{"empty string", "", false},
		{"partial match - conflict alone", "there is a conflict", false},
		{"partial match - merge alone", "merge the branches", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, looksLikeConflict(tc.input))
		})
	}
}

func TestAppendConflictInstructions(t *testing.T) {
	body := "original body"
	result := appendConflictInstructions(body, "main")

	assert.Contains(t, result, "original body")
	assert.Contains(t, result, "## Git Instructions")
	assert.Contains(t, result, "merge conflict")
	assert.Contains(t, result, "rebase conflict")
	assert.Contains(t, result, "git merge origin/main")
	assert.Contains(t, result, "git rebase origin/main")
	assert.Contains(t, result, "Do NOT rewrite files from scratch")
	assert.Contains(t, result, "git rebase --continue")
}

func TestAppendConflictInstructions_CustomBaseBranch(t *testing.T) {
	body := "body"
	result := appendConflictInstructions(body, "develop")

	assert.Contains(t, result, "git merge origin/develop")
	assert.Contains(t, result, "git rebase origin/develop")
	assert.NotContains(t, result, "origin/main")
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"single line no newline", "hello world", "hello world"},
		{"single line with trailing newline", "hello\n", "hello"},
		{"multiple lines", "first line\nsecond line\nthird", "first line"},
		{"only newline", "\nrest", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, firstLine(tc.input))
		})
	}
}

// TestHandleFix_BatchPR_UnchangedBehavior locks in the regression check:
// a batch-prefixed head must still take the existing batch fix flow.
func TestHandleFix_BatchPR_UnchangedBehavior(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Head: "herd/batch/12-foo"},
		},
	}
	p := &testPlatform{
		issues:    issueSvc,
		prs:       prSvc,
		workflows: wf,
		repo:      &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{
			getResult: map[int]*platform.Milestone{12: {Number: 12, Title: "Foo"}},
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
	result := handleFix(hctx, Command{Name: "fix", Prompt: "fix the thing"})

	require.NoError(t, result.Error)
	assert.True(t, strings.HasPrefix(result.Message, "🔧 Created fix issue"), "got: %s", result.Message)
	require.Len(t, issueSvc.createdIssues, 1)
	require.Len(t, issueSvc.createdMilestones, 1)
	require.NotNil(t, issueSvc.createdMilestones[0], "batch fix issue must have a milestone")
	assert.Equal(t, 12, *issueSvc.createdMilestones[0])
	assert.Contains(t, issueSvc.createdIssues[0].Labels, issues.TypeFix)

	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, "herd/batch/12-foo", wf.dispatched[0]["batch_branch"])
	_, hasMode := wf.dispatched[0]["mode"]
	assert.False(t, hasMode, "batch dispatch must not include mode input")
}

// TestHandleFix_NonBatchPR_CreatesStandaloneIssue covers the new standalone
// path: a non-batch PR head creates a tracking issue with no milestone, the
// expected labels, frontmatter for target_pr/target_branch, and dispatches a
// worker with mode=standalone and no batch_branch key.
func TestHandleFix_NonBatchPR_CreatesStandaloneIssue(t *testing.T) {
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{} // no concurrent standalone fix
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			77: {Number: 77, Head: "feature/foo"},
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 77,
		AuthorLogin: "octocat",
		IsPR:        true,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: "tighten up auth validation"})

	require.NoError(t, result.Error)
	assert.True(t, strings.HasPrefix(result.Message, "🔧 Created standalone fix issue"), "got: %s", result.Message)

	require.Len(t, issueSvc.createdIssues, 1)
	created := issueSvc.createdIssues[0]
	assert.Contains(t, created.Labels, issues.TypeStandaloneFix)
	assert.Contains(t, created.Labels, issues.StatusInProgress)

	require.Len(t, issueSvc.createdMilestones, 1)
	assert.Nil(t, issueSvc.createdMilestones[0], "standalone fix issue must have no milestone")

	assert.Contains(t, created.Body, "target_pr: 77")
	assert.Contains(t, created.Body, "target_branch: feature/foo")
	assert.Contains(t, created.Title, "Standalone fix:")

	require.Len(t, wf.dispatched, 1)
	dispatched := wf.dispatched[0]
	assert.Equal(t, "standalone", dispatched["mode"])
	_, hasBatchBranch := dispatched["batch_branch"]
	assert.False(t, hasBatchBranch, "standalone dispatch must not include batch_branch input")
	_, hasTargetBranch := dispatched["target_branch"]
	assert.False(t, hasTargetBranch, "standalone dispatch must not include target_branch input (worker reads from frontmatter)")
	assert.Equal(t, "200", dispatched["issue_number"])
	assert.Equal(t, "30", dispatched["timeout_minutes"])
	assert.Equal(t, "ubuntu-latest", dispatched["runner_label"])
}

// TestHandleFix_NonBatchPR_BlockedByConcurrentFix verifies that posting
// /herd fix while a previous standalone fix is still in progress returns the
// "already in progress" guard and does not create or dispatch anything new.
func TestHandleFix_NonBatchPR_BlockedByConcurrentFix(t *testing.T) {
	existingBody := "---\nherd:\n  version: 1\n  type: standalone-fix\n  target_pr: 77\n  target_branch: feature/foo\n---\n\n## Task\nprior fix\n"
	issueSvc := newTestIssueService()
	issueSvc.listResult = []*platform.Issue{
		{
			Number: 401,
			Body:   existingBody,
			Labels: []string{issues.TypeStandaloneFix, issues.StatusInProgress},
			State:  "open",
		},
	}
	wf := &testWorkflowService{}
	prSvc := &testPRService{
		getResult: map[int]*platform.PullRequest{
			77: {Number: 77, Head: "feature/foo"},
		},
	}
	p := &testPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       &testRepoService{defaultBranch: "main"},
		milestones: &testMilestoneService{},
	}

	hctx := &HandlerContext{
		Ctx:         context.Background(),
		Platform:    p,
		Config:      baseConfig(),
		IssueNumber: 77,
		AuthorLogin: "octocat",
		IsPR:        true,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: "another fix"})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "already in progress")
	assert.Contains(t, result.Message, "#401")
	assert.Empty(t, issueSvc.createdIssues, "must not create a new issue while one is in progress")
	assert.Empty(t, wf.dispatched, "must not dispatch a worker while one is in progress")
}

// TestHandleFix_OnIssueNotPR confirms the existing guard still wins for issues.
func TestHandleFix_OnIssueNotPR(t *testing.T) {
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
	result := handleFix(hctx, Command{Name: "fix", Prompt: "do the thing"})

	assert.NoError(t, result.Error)
	assert.Equal(t, "⚠️ `/herd fix` can only be used on pull requests.", result.Message)
}

// TestHandleFix_EmptyPrompt confirms the existing usage message still fires.
func TestHandleFix_EmptyPrompt(t *testing.T) {
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
		IsPR:        true,
	}
	result := handleFix(hctx, Command{Name: "fix", Prompt: ""})

	assert.NoError(t, result.Error)
	assert.Contains(t, result.Message, "Usage: `/herd fix")
}
