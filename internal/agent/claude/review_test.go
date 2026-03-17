package claude

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReviewOutput_Approved(t *testing.T) {
	output := `{"approved": true, "comments": [], "summary": "All looks good"}`
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Empty(t, result.Comments)
	assert.Equal(t, "All looks good", result.Summary)
}

func TestParseReviewOutput_Rejected(t *testing.T) {
	output := `{"approved": false, "comments": ["SQL injection in auth.go", "Missing null check"], "summary": "Found 2 issues"}`
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Comments, 2)
	assert.Equal(t, "SQL injection in auth.go", result.Comments[0])
	assert.Equal(t, "Missing null check", result.Comments[1])
}

func TestParseReviewOutput_WithMarkdownFencing(t *testing.T) {
	output := "```json\n{\"approved\": true, \"comments\": [], \"summary\": \"clean\"}\n```"
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.True(t, result.Approved)
}

func TestParseReviewOutput_WithPreamble(t *testing.T) {
	output := "Here is my review:\n{\"approved\": false, \"comments\": [\"bug found\"], \"summary\": \"issues\"}\nThat's all."
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Comments, 1)
}

func TestParseReviewOutput_InvalidJSON(t *testing.T) {
	output := "this is not json at all"
	_, err := parseReviewOutput(output)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing review JSON")
}

func TestParseReviewOutput_EmptyString(t *testing.T) {
	_, err := parseReviewOutput("")
	assert.Error(t, err)
}

func TestRenderReviewPrompt_Basic(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"tests pass", "no regressions"},
		RepoRoot:           t.TempDir(),
	}

	prompt, err := renderReviewPrompt("diff --git a/file.go", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "tests pass")
	assert.Contains(t, prompt, "no regressions")
	assert.Contains(t, prompt, "diff --git a/file.go")
	assert.Contains(t, prompt, "Respond with ONLY a JSON object")
}

func TestRenderReviewPrompt_EmptyCriteria(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: nil,
		RepoRoot:           t.TempDir(),
	}

	prompt, err := renderReviewPrompt("some diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "some diff")
}

func TestRenderReviewPrompt_WithRoleInstructions(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"secure"},
		SystemPrompt:       "Pay extra attention to SQL queries",
	}

	prompt, err := renderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Pay extra attention to SQL queries")
	assert.Contains(t, prompt, "Project-Specific Review Instructions")
}

func TestRenderReviewPrompt_NoRoleInstructions(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
	}

	prompt, err := renderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.NotContains(t, prompt, "Project-Specific Review Instructions")
}

func TestReview_LargeDiffPassedViaStdin(t *testing.T) {
	// Verify the review passes the prompt via stdin (not CLI args)
	// by checking that a large prompt doesn't cause "argument list too long"
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	// Script reads stdin and outputs approved JSON
	err := os.WriteFile(script, []byte(`#!/bin/sh
cat > /dev/null
echo '{"approved": true, "comments": [], "summary": "LGTM"}'
`), 0755)
	require.NoError(t, err)

	a := New(script, "")

	// Create a large diff (200KB)
	largeDiff := strings.Repeat("+ some added line\n", 12000)

	result, err := a.Review(context.Background(), largeDiff, agent.ReviewOptions{
		AcceptanceCriteria: []string{"tests pass"},
		RepoRoot:           dir,
	})
	require.NoError(t, err)
	assert.True(t, result.Approved)
}

func TestReview_StreamsOutputToStdout(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte(`#!/bin/sh
cat > /dev/null
echo '{"approved": false, "comments": ["issue found"], "summary": "needs work"}'
`), 0755)
	require.NoError(t, err)

	a := New(script, "")
	result, err := a.Review(context.Background(), "small diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Comments, 1)
}
