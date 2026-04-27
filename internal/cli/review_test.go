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
	}

	out, err := renderReviewSystemPrompt(data)
	require.NoError(t, err)
	assert.Contains(t, out, "```diff")
	assert.Contains(t, out, "CI status (head ref): unknown")
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
