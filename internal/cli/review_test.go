package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/reviewdiff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderReviewSystemPrompt_AllSections(t *testing.T) {
	data := &reviewCmdPromptData{
		PRNumber:     42,
		PRTitle:      "Fix flaky integration test",
		PRURL:        "https://github.com/example/repo/pull/42",
		PRBaseBranch: "main",
		PRHeadBranch: "fix/flaky-test",
		Diff:         "diff --git a/foo.go b/foo.go\n+added line\n-removed line",
		CoverageSummary: "## Diff Coverage\n\n" +
			"- Source: github-raw-diff\n" +
			"- Review mode: full\n",
		CIStatus: "failure",
		Comments: []reviewCmdComment{
			{Author: "alice", Body: "Looks good overall."},
			{Author: "bob", Body: "Please add a regression test."},
		},
		InlineComments: []reviewCmdInlineComment{
			{Author: "carol", Path: "foo.go", Line: 12, DiffHunk: "@@ -10,3 +10,3 @@", Body: "This nil check seems wrong."},
			{Author: "dave", Path: "bar.go", Line: 7, DiffHunk: "@@ -5,2 +5,3 @@", Body: "Consider extracting helper."},
		},
		RoleInstructions: "Always be polite.",
		RepoOwner:        "example",
		RepoName:         "repo",
	}

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, out, "Pull Request #42:")
	assert.Contains(t, out, "Fix flaky integration test")
	assert.Contains(t, out, "CI status (head ref): failure")
	assert.Contains(t, out, "## Diff Coverage")
	assert.Contains(t, out, "- Review mode: full")
	assert.Contains(t, out, "```diff")
	assert.Contains(t, out, "diff --git a/foo.go b/foo.go")
	assert.Contains(t, out, "Looks good overall.")
	assert.Contains(t, out, "Please add a regression test.")
	assert.Contains(t, out, "This nil check seems wrong.")
	assert.Contains(t, out, "Consider extracting helper.")
	assert.Contains(t, out, "foo.go")
	assert.Contains(t, out, "bar.go")
	assert.Contains(t, out, "You MUST NOT:")
	assert.Contains(t, out, "## Project-Specific Reviewer Instructions")
	assert.Contains(t, out, "Always be polite.")
}

func TestRenderReviewSystemPrompt_OptionalSectionsOmitted(t *testing.T) {
	data := &reviewCmdPromptData{
		PRNumber:     7,
		PRTitle:      "No comments yet",
		PRURL:        "https://github.com/example/repo/pull/7",
		PRBaseBranch: "main",
		PRHeadBranch: "feature/x",
		Diff:         "diff content",
		CIStatus:     "success",
		RepoOwner:    "example",
		RepoName:     "repo",
	}

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)

	assert.NotContains(t, out, "## PR Conversation Comments")
	assert.NotContains(t, out, "## Inline Review Comments")
	assert.NotContains(t, out, "## Project-Specific Reviewer Instructions")
	assert.Contains(t, out, "## Your Role")
}

func TestRenderReviewSystemPrompt_EmptyDiffStillRenders(t *testing.T) {
	data := &reviewCmdPromptData{
		PRNumber:     1,
		PRTitle:      "Empty PR",
		PRURL:        "https://github.com/example/repo/pull/1",
		PRBaseBranch: "main",
		PRHeadBranch: "feature/empty",
		Diff:         "",
		CIStatus:     "unknown",
		RepoOwner:    "example",
		RepoName:     "repo",
	}

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)
	assert.Contains(t, out, "## Diff")
	assert.Contains(t, out, "CI status (head ref): unknown")
}

func TestRenderReviewSystemPrompt_DoesNotWrapRenderedReviewDiffInOuterFence(t *testing.T) {
	rendered := reviewdiff.RenderForReview(reviewdiff.DiffSet{
		Source: "github-files-api",
		Files: []reviewdiff.ChangedFile{
			{
				Path:      "internal/review.go",
				Status:    reviewdiff.ChangeModified,
				Additions: 1,
				Deletions: 1,
				Patch:     "@@ -1 +1 @@\n-old\n+new\n",
			},
		},
	}, reviewdiff.DefaultRenderOptions())
	require.Contains(t, rendered.Text, "```diff", "rendered review diff should contain an internal file fence")

	out, err := renderReviewSystemPrompt(&reviewCmdPromptData{
		PRNumber:     42,
		PRTitle:      "Rendered diff",
		PRURL:        "https://github.com/example/repo/pull/42",
		PRBaseBranch: "main",
		PRHeadBranch: "feature/rendered-diff",
		Diff:         rendered.Text,
		CIStatus:     "success",
		RepoOwner:    "example",
		RepoName:     "repo",
	})
	require.NoError(t, err)

	diffStart := strings.Index(out, "\n## Diff\n")
	diffEnd := strings.Index(out, "## Your Role")
	require.GreaterOrEqual(t, diffStart, 0, "diff section must be present")
	require.Greater(t, diffEnd, diffStart, "role section must follow diff section")
	diffSection := out[diffStart:diffEnd]

	assert.Contains(t, diffSection, "# Review diff")
	assert.Contains(t, diffSection, "internal/review.go")
	assert.Equal(t, 1, strings.Count(diffSection, "```diff"), "prompt should preserve only the rendered per-file diff fence")
	assert.NotContains(t, diffSection, "## Diff\n```diff\n# Review diff", "prompt must not wrap rendered markdown in an outer diff fence")
}

func TestRenderReviewSystemPrompt_MultiChunkCoverageWording(t *testing.T) {
	data := baseReviewData()
	data.Diff = "# Review diff\n\n## first.go\n\n```diff\n+first\n```\n"
	data.CoverageSummary = "## Diff Coverage\n\n- Review mode: chunked\n- Chunks reviewed: 1/3\n"
	data.ReviewMode = string(reviewdiff.CoverageModeChunked)
	data.ChunkIndex = 1
	data.TotalChunks = 3
	data.OnlyFirstChunkIncluded = true

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, out, "## Diff Coverage")
	assert.Contains(t, out, "- Review mode: chunked")
	assert.Contains(t, out, "Only chunk 1/3 is included in this interactive prompt")
	assert.Contains(t, out, "Additional chunks are not hidden as reviewed")

	diffStart := strings.Index(out, "\n## Diff\n")
	diffEnd := strings.Index(out, "## Your Role")
	require.GreaterOrEqual(t, diffStart, 0, "diff section must be present")
	require.Greater(t, diffEnd, diffStart, "role section must follow diff section")
	diffSection := out[diffStart:diffEnd]

	assert.Equal(t, 1, strings.Count(diffSection, "```diff"), "prompt should preserve only rendered per-file diff fences")
	assert.NotContains(t, diffSection, "## Diff\n```", "prompt must not wrap rendered markdown in an outer fence")
}

func TestReviewSystemPrompt_IncludesFixGuidance(t *testing.T) {
	data := &reviewCmdPromptData{
		PRNumber:     99,
		PRTitle:      "Some PR",
		PRURL:        "https://github.com/example/repo/pull/99",
		PRBaseBranch: "main",
		PRHeadBranch: "feature/fix-guidance",
		Diff:         "diff body",
		CIStatus:     "success",
		RepoOwner:    "example",
		RepoName:     "repo",
	}

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, out, "/herd fix", "drafting instruction should reference /herd fix")
	assert.True(t,
		strings.Contains(out, "draft") || strings.Contains(out, "Draft"),
		"prompt should include drafting language")
	assert.True(t,
		strings.Contains(out, "approval") || strings.Contains(out, "approve"),
		"prompt should ask for user approval")
	assert.Contains(t, out, "gh pr comment", "prompt should mention gh pr comment for posting")
	assert.Contains(t, out, "--repo example/repo", "owner/repo should be interpolated into post command")
	assert.Contains(t, out, "Acceptance criteria", "drafted comment template should include acceptance criteria")
	assert.True(t,
		strings.Contains(out, "informational") || strings.Contains(out, "do NOT"),
		"prompt should describe negative path: no draft for purely informational discussion")
}

func TestReviewSystemPrompt_FixGuidanceWithHyphenatedOwner(t *testing.T) {
	data := &reviewCmdPromptData{
		PRNumber:     123,
		PRTitle:      "Multi-segment owner",
		PRURL:        "https://github.com/Herd-OS/herd/pull/123",
		PRBaseBranch: "main",
		PRHeadBranch: "feature/x",
		Diff:         "diff body",
		CIStatus:     "success",
		RepoOwner:    "Herd-OS",
		RepoName:     "herd",
	}

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, out, "--repo Herd-OS/herd", "hyphenated owner should be interpolated")
	assert.Contains(t, out, "gh pr comment 123", "PR number should be interpolated")
}

func TestNewReviewCmd_Setup(t *testing.T) {
	cmd := newReviewCmd()
	require.NotNil(t, cmd)

	assert.True(t, strings.HasPrefix(cmd.Use, "review "), "Use should start with 'review '")
	require.NotNil(t, cmd.Args)

	// ExactArgs(1): zero args fails, one arg succeeds.
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"42"}))

	flag := cmd.Flags().Lookup("prompt")
	require.NotNil(t, flag)
	assert.Equal(t, "p", flag.Shorthand)
}

func baseReviewData() *reviewCmdPromptData {
	return &reviewCmdPromptData{
		PRNumber:     42,
		PRTitle:      "Test PR",
		PRURL:        "https://github.com/example/repo/pull/42",
		PRBaseBranch: "main",
		PRHeadBranch: "feature/x",
		Diff:         "diff body",
		CoverageSummary: "## Diff Coverage\n\n" +
			"- Source: github-raw-diff\n" +
			"- Review mode: full\n",
		CIStatus:  "success",
		RepoOwner: "example",
		RepoName:  "repo",
	}
}

func TestReviewSystemPrompt_NoLocalEditPath(t *testing.T) {
	out, err := renderReviewSystemPrompt(baseReviewData())
	require.NoError(t, err)

	lower := strings.ToLower(out)
	assert.NotContains(t, lower, "make code changes locally",
		"prompt must not advertise a local-edit path")
	assert.NotContains(t, lower, "make changes if you ask",
		"prompt must not advertise a conditional local-edit path")
}

func TestReviewSystemPrompt_ExplicitNoEditEvenIfUserAsks(t *testing.T) {
	out, err := renderReviewSystemPrompt(baseReviewData())
	require.NoError(t, err)

	// The prompt must explicitly address the user-asks-for-local-edit case
	// and instruct the agent to draft /herd fix instead.
	assert.True(t,
		strings.Contains(out, "edit the file directly") ||
			strings.Contains(out, "do it locally") ||
			strings.Contains(out, "just make the change"),
		"prompt should enumerate the user-asks-for-local-edit phrasing")
	assert.Contains(t, out, "never edits files locally",
		"prompt should state herd review never edits files locally")
	assert.Contains(t, out, "/herd fix",
		"prompt should redirect the user to /herd fix")
}

func TestReviewSystemPrompt_MustNotIncludesFileMutation(t *testing.T) {
	out, err := renderReviewSystemPrompt(baseReviewData())
	require.NoError(t, err)

	mustNotIdx := strings.Index(out, "You MUST NOT:")
	require.NotEqual(t, -1, mustNotIdx, "prompt must contain a You MUST NOT: section")
	mustNotSection := out[mustNotIdx:]

	assert.Contains(t, mustNotSection, "Modify, create, or delete files",
		"MUST NOT section should forbid file mutation")
	assert.Contains(t, mustNotSection, "read-only on the working tree",
		"MUST NOT section should describe read-only working tree")
	assert.Contains(t, mustNotSection, "mutate state",
		"MUST NOT section should forbid mutating shell commands")
	assert.Contains(t, mustNotSection, "git commit",
		"MUST NOT section should call out git commit as forbidden")
	assert.Contains(t, mustNotSection, "gh pr comment",
		"MUST NOT section should mention the allowed gh pr comment exception")
}

func TestBuildReviewPromptDataFallsBackWhenRawDiffTooLarge(t *testing.T) {
	prs := &mockReviewPromptPRService{
		diffErr: platform.ErrPullRequestDiffTooLarge,
		files: []*platform.PullRequestFile{
			{
				Path:      "src/review.go",
				Status:    "modified",
				Additions: 1,
				Deletions: 1,
				Changes:   2,
				Patch:     "@@ -1 +1 @@\n-old\n+bounded\n",
			},
		},
	}
	client := &mockReviewPromptPlatform{
		prs:    prs,
		issues: &mockReviewPromptIssueService{},
		checks: &mockReviewPromptCheckService{},
	}

	data, err := buildReviewPromptData(context.Background(), client, 42, "example", "repo", t.TempDir(), reviewdiff.DefaultChunkOptions())

	require.NoError(t, err)
	assert.True(t, prs.getDiffCalled)
	assert.True(t, prs.listFilesCalled)
	assert.Equal(t, "Example PR", data.PRTitle)
	assert.Contains(t, data.Diff, "Source: github-files-api")
	assert.Contains(t, data.Diff, "src/review.go")
	assert.Contains(t, data.Diff, "+bounded")
	assert.Contains(t, data.CoverageSummary, "## Diff Coverage")
	assert.Contains(t, data.CoverageSummary, "- Source: github-files-api")
}

func TestBuildReviewPromptDataUsesChunkPlanningForInteractivePrompt(t *testing.T) {
	prs := &mockReviewPromptPRService{
		diffErr: platform.ErrPullRequestDiffTooLarge,
		files: []*platform.PullRequestFile{
			reviewPromptFile("src/first.go", "+first chunk body\n"),
			reviewPromptFile("src/second.go", "+second chunk body\n"),
			reviewPromptFile("src/third.go", "+third chunk body\n"),
		},
	}
	client := &mockReviewPromptPlatform{
		prs:    prs,
		issues: &mockReviewPromptIssueService{},
		checks: &mockReviewPromptCheckService{},
	}

	data, err := buildReviewPromptData(context.Background(), client, 42, "example", "repo", t.TempDir(), reviewdiff.ChunkOptions{
		MaxChunkBytes:            1000,
		MaxFileDiffBytes:         1000,
		MaxFilesPerChunk:         1,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: reviewdiff.DefaultMaxOmittedSummaryEntries,
	})
	require.NoError(t, err)

	assert.Equal(t, string(reviewdiff.CoverageModeChunked), data.ReviewMode)
	assert.Equal(t, 1, data.ChunkIndex)
	assert.Equal(t, 3, data.TotalChunks)
	assert.True(t, data.OnlyFirstChunkIncluded)
	assert.False(t, data.PartialReview)
	assert.Contains(t, data.CoverageSummary, "## Diff Coverage")
	assert.Contains(t, data.CoverageSummary, "- Review mode: chunked")
	assert.Contains(t, data.CoverageSummary, "- Chunks reviewed: 1/3")
	assert.Contains(t, data.CoverageSummary, "- Chunk 1/3: 1 files")
	assert.Contains(t, data.CoverageSummary, "- Chunk 2/3: 1 files")
	assert.Contains(t, data.Diff, "src/first.go")
	assert.Contains(t, data.Diff, "+first chunk body")
	assert.NotContains(t, data.Diff, "+second chunk body")
	assert.NotContains(t, data.Diff, "+third chunk body")

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)
	assert.Contains(t, out, "Only chunk 1/3 is included in this interactive prompt")
	assert.Contains(t, out, "Additional chunks are not hidden as reviewed")
	assert.NotContains(t, out, "+second chunk body")
	assert.NotContains(t, out, "+third chunk body")
	assert.NotContains(t, out, "## Diff\n```", "rendered diff markdown must not be wrapped in an outer fence")
}

func TestBuildReviewPromptDataMarksPartialCoverage(t *testing.T) {
	prs := &mockReviewPromptPRService{
		diffErr: platform.ErrPullRequestDiffTooLarge,
		files: []*platform.PullRequestFile{
			reviewPromptFile("src/reviewed.go", "+reviewed body\n"),
			reviewPromptFile("src/not-reviewed.go", "+not reviewed body\n"),
		},
	}
	client := &mockReviewPromptPlatform{
		prs:    prs,
		issues: &mockReviewPromptIssueService{},
		checks: &mockReviewPromptCheckService{},
	}

	data, err := buildReviewPromptData(context.Background(), client, 42, "example", "repo", t.TempDir(), reviewdiff.ChunkOptions{
		MaxChunkBytes:            1000,
		MaxFileDiffBytes:         1000,
		MaxFilesPerChunk:         1,
		MaxChunks:                1,
		MaxOmittedSummaryEntries: reviewdiff.DefaultMaxOmittedSummaryEntries,
	})
	require.NoError(t, err)

	assert.Equal(t, string(reviewdiff.CoverageModePartial), data.ReviewMode)
	assert.Equal(t, 2, data.TotalChunks)
	assert.True(t, data.OnlyFirstChunkIncluded)
	assert.True(t, data.PartialReview)
	assert.Contains(t, data.CoverageSummary, "- Review mode: partial")
	assert.Contains(t, data.CoverageSummary, "- Chunks reviewed: 1/2")
	assert.Contains(t, data.CoverageSummary, "- This is a partial review")
	assert.Contains(t, data.CoverageSummary, "maximum planned review chunks exceeded")
	assert.Contains(t, data.CoverageSummary, "- Required chunks: 2; max chunks: 1")
	assert.Contains(t, data.Diff, "+reviewed body")
	assert.NotContains(t, data.Diff, "+not reviewed body")

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)
	assert.Contains(t, out, "Only chunk 1/2 is included in this interactive prompt")
	assert.Contains(t, out, "Review only the included diffs in this chunk")
}

func TestReviewDiffChunkOptions(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ReviewDiff
		want reviewdiff.ChunkOptions
	}{
		{
			name: "maps configured limits",
			cfg: config.ReviewDiff{
				MaxChunkBytes:    123,
				MaxFileBytes:     45,
				MaxFilesPerChunk: 6,
				MaxChunks:        7,
			},
			want: reviewdiff.ChunkOptions{
				MaxChunkBytes:            123,
				MaxFileDiffBytes:         45,
				MaxFilesPerChunk:         6,
				MaxChunks:                7,
				MaxOmittedSummaryEntries: reviewdiff.DefaultMaxOmittedSummaryEntries,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reviewDiffChunkOptions(tt.cfg))
		})
	}
}

func reviewPromptFile(path, addedLine string) *platform.PullRequestFile {
	return &platform.PullRequestFile{
		Path:      path,
		Status:    "modified",
		Additions: 1,
		Deletions: 0,
		Changes:   1,
		Patch:     "@@ -1,0 +1,1 @@\n" + addedLine,
	}
}

type mockReviewPromptPlatform struct {
	prs    platform.PullRequestService
	issues platform.IssueService
	checks platform.CheckService
}

func (m *mockReviewPromptPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockReviewPromptPlatform) PullRequests() platform.PullRequestService { return m.prs }
func (m *mockReviewPromptPlatform) Workflows() platform.WorkflowService       { return nil }
func (m *mockReviewPromptPlatform) Labels() platform.LabelService             { return nil }
func (m *mockReviewPromptPlatform) Milestones() platform.MilestoneService     { return nil }
func (m *mockReviewPromptPlatform) Runners() platform.RunnerService           { return nil }
func (m *mockReviewPromptPlatform) Repository() platform.RepositoryService    { return nil }
func (m *mockReviewPromptPlatform) Checks() platform.CheckService             { return m.checks }

type mockReviewPromptPRService struct {
	diffErr         error
	files           []*platform.PullRequestFile
	getDiffCalled   bool
	listFilesCalled bool
}

func (m *mockReviewPromptPRService) Create(context.Context, string, string, string, string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockReviewPromptPRService) Get(context.Context, int) (*platform.PullRequest, error) {
	return &platform.PullRequest{
		Number: 42,
		Title:  "Example PR",
		URL:    "https://github.com/example/repo/pull/42",
		Base:   "main",
		Head:   "feature/review",
	}, nil
}
func (m *mockReviewPromptPRService) List(context.Context, platform.PRFilters) ([]*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockReviewPromptPRService) Update(context.Context, int, *string, *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockReviewPromptPRService) Merge(context.Context, int, platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, nil
}
func (m *mockReviewPromptPRService) UpdateBranch(context.Context, int) error { return nil }
func (m *mockReviewPromptPRService) CreateReview(context.Context, int, string, platform.ReviewEvent) error {
	return nil
}
func (m *mockReviewPromptPRService) AddComment(context.Context, int, string) error { return nil }
func (m *mockReviewPromptPRService) ListReviewComments(context.Context, int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (m *mockReviewPromptPRService) ListFiles(context.Context, int) ([]*platform.PullRequestFile, error) {
	m.listFilesCalled = true
	return m.files, nil
}
func (m *mockReviewPromptPRService) GetDiff(context.Context, int) (string, error) {
	m.getDiffCalled = true
	if m.diffErr != nil {
		return "", m.diffErr
	}
	return "diff --git a/raw.go b/raw.go\n", nil
}
func (m *mockReviewPromptPRService) Close(context.Context, int) error { return nil }

type mockReviewPromptIssueService struct{}

func (m *mockReviewPromptIssueService) Create(context.Context, string, string, []string, *int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockReviewPromptIssueService) Get(context.Context, int) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockReviewPromptIssueService) List(context.Context, platform.IssueFilters) ([]*platform.Issue, error) {
	return nil, nil
}
func (m *mockReviewPromptIssueService) Update(context.Context, int, platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (m *mockReviewPromptIssueService) AddLabels(context.Context, int, []string) error { return nil }
func (m *mockReviewPromptIssueService) RemoveLabels(context.Context, int, []string) error {
	return nil
}
func (m *mockReviewPromptIssueService) AddComment(context.Context, int, string) error { return nil }
func (m *mockReviewPromptIssueService) AddCommentReturningID(context.Context, int, string) (int64, error) {
	return 0, nil
}
func (m *mockReviewPromptIssueService) UpdateComment(context.Context, int64, string) error {
	return nil
}
func (m *mockReviewPromptIssueService) DeleteComment(context.Context, int64) error { return nil }
func (m *mockReviewPromptIssueService) ListComments(context.Context, int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockReviewPromptIssueService) CreateCommentReaction(context.Context, int64, string) error {
	return nil
}

type mockReviewPromptCheckService struct{}

func (m *mockReviewPromptCheckService) GetCombinedStatus(context.Context, string) (string, error) {
	return "success", nil
}
func (m *mockReviewPromptCheckService) RerunFailedChecks(context.Context, string) error {
	return nil
}

func TestReviewSystemPrompt_RationaleIncluded(t *testing.T) {
	out, err := renderReviewSystemPrompt(baseReviewData())
	require.NoError(t, err)

	assert.Contains(t, out, "managed by herd's batch workers",
		"prompt should state the PR is managed by herd's batch workers")
	assert.Contains(t, out, "phantom commits",
		"prompt should mention phantom commits as a consequence of local edits")
	assert.Contains(t, out, "in-flight fix workers",
		"prompt should mention conflicts with in-flight fix workers")
}

func TestParsePRArg(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantN   int
		wantErr bool
	}{
		{name: "valid positive integer", input: "42", wantN: 42, wantErr: false},
		{name: "another valid positive integer", input: "1", wantN: 1, wantErr: false},
		{name: "non-numeric", input: "abc", wantErr: true},
		{name: "negative", input: "-1", wantErr: true},
		{name: "zero", input: "0", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
		{name: "float-like", input: "3.14", wantErr: true},
		{name: "whitespace", input: " 42", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := parsePRArg(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid PR number")
				assert.Equal(t, 0, n)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantN, n)
			}
		})
	}
}
