package claude

import (
	"context"
	"fmt"
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
echo '{"approved": true, "findings": [], "summary": "LGTM"}'
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
echo '{"approved": false, "findings": [{"severity": "HIGH", "description": "issue found"}], "summary": "needs work"}'
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
	assert.Len(t, result.Findings, 1)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)
}

func TestParseReviewOutput_NewFindingsFormat(t *testing.T) {
	output := `{"approved": false, "findings": [{"severity": "HIGH", "description": "SQL injection"}, {"severity": "LOW", "description": "typo in comment"}], "summary": "Found issues"}`
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Findings, 2)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)
	assert.Equal(t, "SQL injection", result.Findings[0].Description)
	assert.Equal(t, "LOW", result.Findings[1].Severity)
	// Backward compat: Comments populated from Findings
	assert.Len(t, result.Comments, 2)
	assert.Equal(t, "SQL injection", result.Comments[0])
}

func TestParseReviewOutput_OldCommentsFormat_BackwardCompat(t *testing.T) {
	// Old format with "comments" instead of "findings"
	output := `{"approved": false, "comments": ["bug found", "missing test"], "summary": "issues"}`
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.Len(t, result.Comments, 2)
	// Findings created from Comments with HIGH severity
	assert.Len(t, result.Findings, 2)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)
	assert.Equal(t, "bug found", result.Findings[0].Description)
}

func TestParseReviewOutput_ApprovedNewFormat(t *testing.T) {
	output := `{"approved": true, "findings": [], "summary": "All good"}`
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Empty(t, result.Findings)
	assert.Empty(t, result.Comments)
}

func TestRenderReviewPrompt_WithStrictness(t *testing.T) {
	tests := []struct {
		name       string
		strictness string
		contains   string
	}{
		{"standard", "standard", "Flag real bugs, security issues, and missing error handling"},
		{"strict", "strict", "Flag bugs, security issues, missing error handling, style issues"},
		{"lenient", "lenient", "Only flag critical bugs and security vulnerabilities"},
		{"empty defaults to standard", "", "Flag real bugs, security issues, and missing error handling"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := agent.ReviewOptions{
				AcceptanceCriteria: []string{"works"},
				Strictness:         tt.strictness,
			}
			prompt, err := renderReviewPrompt("diff", opts)
			require.NoError(t, err)
			assert.Contains(t, prompt, tt.contains)
		})
	}
}

func TestRenderReviewPrompt_SeverityGuide(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	prompt, err := renderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "HIGH: Bugs, security vulnerabilities")
	assert.Contains(t, prompt, "MEDIUM: Missing edge cases")
	assert.Contains(t, prompt, "LOW: Style preferences")
	assert.Contains(t, prompt, "CRITERIA: An acceptance criterion itself is wrong")
}

func TestRenderReviewPrompt_CriteriaSeverityGuide(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	prompt, err := renderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "acceptance criterion itself is wrong")
	assert.Contains(t, prompt, `Use severity "CRITERIA" only when the acceptance criterion itself is flawed`)
}

func TestParseReviewOutput_CriteriaSeverity(t *testing.T) {
	output := `{"approved": false, "findings": [{"severity": "CRITERIA", "description": "Criterion 'tests pass' is too vague"}], "summary": "criteria issue"}`
	result, err := parseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Findings, 1)
	assert.Equal(t, "CRITERIA", result.Findings[0].Severity)
	assert.Equal(t, "Criterion 'tests pass' is too vague", result.Findings[0].Description)
	assert.Equal(t, "criteria issue", result.Summary)
}

func TestRenderReviewPrompt_FixRequestsInCriteria(t *testing.T) {
	tests := []struct {
		name     string
		criteria []string
		wantFix  bool
	}{
		{
			name:     "no fix requests in criteria",
			criteria: []string{"works"},
			wantFix:  false,
		},
		{
			name:     "fix request appears in criteria section",
			criteria: []string{"works", "User requested: make logo bigger"},
			wantFix:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := agent.ReviewOptions{
				AcceptanceCriteria: tt.criteria,
			}
			prompt, err := renderReviewPrompt("diff", opts)
			require.NoError(t, err)

			assert.NotContains(t, prompt, "## User-Requested Fixes")
			assert.Contains(t, prompt, "## Acceptance Criteria")
			if tt.wantFix {
				assert.Contains(t, prompt, "- User requested: make logo bigger")
			}
		})
	}
}

func TestReview_SuspiciousOutputReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'Execution error'"), 0755)
	require.NoError(t, err)

	a := New(script, "")
	_, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	assert.Error(t, reviewErr)
	assert.Contains(t, reviewErr.Error(), "suspicious output after retry")
}

func TestReview_EmptyOutputReturnsError(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	err := os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null"), 0755)
	require.NoError(t, err)

	a := New(script, "")
	_, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	assert.Error(t, reviewErr)
	assert.Contains(t, reviewErr.Error(), "suspicious output after retry")
}

func TestReview_RetrySucceedsOnSecondAttempt(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	marker := dir + "/attempt"
	err := os.WriteFile(script, []byte(fmt.Sprintf(`#!/bin/sh
cat > /dev/null
if [ -f "%s" ]; then
  echo '{"approved": true, "findings": [], "summary": "LGTM"}'
else
  touch "%s"
  echo "Execution error"
fi
`, marker, marker)), 0755)
	require.NoError(t, err)

	a := New(script, "")
	result, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	require.NoError(t, reviewErr)
	assert.True(t, result.Approved)
}

func TestRenderReviewPrompt_SupportingChangesInstruction(t *testing.T) {
	tests := []struct {
		name       string
		strictness string
	}{
		{"standard", "standard"},
		{"strict", "strict"},
		{"lenient", "lenient"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := agent.ReviewOptions{
				AcceptanceCriteria: []string{"no other files modified"},
				Strictness:         tt.strictness,
			}
			prompt, err := renderReviewPrompt("diff", opts)
			require.NoError(t, err)
			assert.Contains(t, prompt, "allow supporting changes to configuration files, test helpers, test fixtures, and infrastructure files")
			assert.Contains(t, prompt, "if removing the extra change would break the primary task, it is a necessary supporting change, not a violation")
		})
	}
}

func TestRenderReviewPrompt_PriorReviewComments(t *testing.T) {
	tests := []struct {
		name                string
		priorReviewComments []string
		wantSection         bool
	}{
		{
			name:                "nil omits section",
			priorReviewComments: nil,
			wantSection:         false,
		},
		{
			name:                "empty slice omits section",
			priorReviewComments: []string{},
			wantSection:         false,
		},
		{
			name:                "one comment includes section",
			priorReviewComments: []string{"🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue:\n\n**HIGH**:\n- Missing error handling"},
			wantSection:         true,
		},
		{
			name:                "multiple comments lists all",
			priorReviewComments: []string{"🔍 **HerdOS Agent Review** (cycle 1 of 3)\n\nFound 1 issue", "✅ **HerdOS Agent Review** (cycle 2 of 3)\n\nAll good"},
			wantSection:         true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := agent.ReviewOptions{
				AcceptanceCriteria:  []string{"works"},
				PriorReviewComments: tt.priorReviewComments,
			}
			prompt, err := renderReviewPrompt("diff", opts)
			require.NoError(t, err)

			if tt.wantSection {
				assert.Contains(t, prompt, "## Prior Review History")
				assert.Contains(t, prompt, "Do NOT contradict prior review decisions")
				for _, comment := range tt.priorReviewComments {
					assert.Contains(t, prompt, comment)
				}
			} else {
				assert.NotContains(t, prompt, "## Prior Review History")
				assert.NotContains(t, prompt, "Do NOT contradict prior review decisions")
			}
		})
	}
}
