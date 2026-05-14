package cli

import (
	"strings"
	"testing"

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
		CIStatus:     "failure",
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
	assert.Contains(t, out, "```diff")
	assert.Contains(t, out, "CI status (head ref): unknown")
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
		CIStatus:     "success",
		RepoOwner:    "example",
		RepoName:     "repo",
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
