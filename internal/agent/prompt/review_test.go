package prompt

import (
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/reviewdiff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReviewOutput_Approved(t *testing.T) {
	output := `{"approved": true, "comments": [], "summary": "All looks good"}`
	result, err := ParseReviewOutput(output)
	require.NoError(t, err)
	assert.True(t, result.Approved)
	assert.Empty(t, result.Comments)
	assert.Equal(t, "All looks good", result.Summary)
}

func TestParseReviewOutput_Rejected(t *testing.T) {
	output := `{"approved": false, "comments": ["SQL injection in auth.go", "Missing null check"], "summary": "Found 2 issues"}`
	result, err := ParseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Comments, 2)
	assert.Equal(t, "SQL injection in auth.go", result.Comments[0])
	assert.Equal(t, "Missing null check", result.Comments[1])
}

func TestParseReviewOutput_WithMarkdownFencing(t *testing.T) {
	output := "```json\n{\"approved\": true, \"comments\": [], \"summary\": \"clean\"}\n```"
	result, err := ParseReviewOutput(output)
	require.NoError(t, err)
	assert.True(t, result.Approved)
}

func TestParseReviewOutput_WithPreamble(t *testing.T) {
	output := "Here is my review:\n{\"approved\": false, \"comments\": [\"bug found\"], \"summary\": \"issues\"}\nThat's all."
	result, err := ParseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Comments, 1)
}

func TestParseReviewOutput_InvalidJSON(t *testing.T) {
	output := "this is not json at all"
	_, err := ParseReviewOutput(output)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing review JSON")
}

func TestParseReviewOutput_EmptyString(t *testing.T) {
	_, err := ParseReviewOutput("")
	assert.Error(t, err)
}

func TestRenderReviewPrompt_Basic(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"tests pass", "no regressions"},
		RepoRoot:           t.TempDir(),
	}

	prompt, err := RenderReviewPrompt("diff --git a/file.go", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "tests pass")
	assert.Contains(t, prompt, "no regressions")
	assert.Contains(t, prompt, "diff --git a/file.go")
	assert.Contains(t, prompt, "Respond with ONLY a JSON object")
}

func TestRenderReviewPrompt_EmbedsDiffAndCriteria(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"first criterion", "second criterion"},
	}
	prompt, err := RenderReviewPrompt("a diff body", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "a diff body")
	assert.Contains(t, prompt, "first criterion")
	assert.Contains(t, prompt, "second criterion")
}

func TestRenderReviewPrompt_DoesNotWrapRenderedReviewDiffInOuterFence(t *testing.T) {
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

	prompt, err := RenderReviewPrompt(rendered.Text, agent.ReviewOptions{
		AcceptanceCriteria: []string{"review rendered diff"},
	})
	require.NoError(t, err)

	diffStart := strings.Index(prompt, "## Diff")
	diffEnd := strings.Index(prompt, "Respond with ONLY a JSON object")
	require.GreaterOrEqual(t, diffStart, 0, "diff section must be present")
	require.Greater(t, diffEnd, diffStart, "response instructions must follow diff section")
	diffSection := prompt[diffStart:diffEnd]

	assert.Contains(t, diffSection, "# Review diff")
	assert.Contains(t, diffSection, "internal/review.go")
	assert.Equal(t, 1, strings.Count(diffSection, "```diff"), "prompt should preserve only the rendered per-file diff fence")
	assert.NotContains(t, diffSection, "## Diff\n\n```diff\n# Review diff", "prompt must not wrap rendered markdown in an outer diff fence")
}

func TestRenderReviewPrompt_EmptyCriteria(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: nil,
		RepoRoot:           t.TempDir(),
	}

	prompt, err := RenderReviewPrompt("some diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "some diff")
}

func TestRenderReviewPrompt_WithRoleInstructions(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"secure"},
		SystemPrompt:       "Pay extra attention to SQL queries",
	}

	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Pay extra attention to SQL queries")
	assert.Contains(t, prompt, "Project-Specific Review Instructions")
}

func TestRenderReviewPrompt_NoRoleInstructions(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
	}

	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.NotContains(t, prompt, "Project-Specific Review Instructions")
}

func TestParseReviewOutput_NewFindingsFormat(t *testing.T) {
	output := `{"approved": false, "findings": [{"severity": "HIGH", "description": "SQL injection"}, {"severity": "LOW", "description": "typo in comment"}], "summary": "Found issues"}`
	result, err := ParseReviewOutput(output)
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Findings, 2)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)
	assert.Equal(t, "SQL injection", result.Findings[0].Description)
	assert.Equal(t, "LOW", result.Findings[1].Severity)
	assert.Len(t, result.Comments, 2)
	assert.Equal(t, "SQL injection", result.Comments[0])
}

func TestParseReviewOutput_OldCommentsFormat_BackwardCompat(t *testing.T) {
	output := `{"approved": false, "comments": ["bug found", "missing test"], "summary": "issues"}`
	result, err := ParseReviewOutput(output)
	require.NoError(t, err)
	assert.Len(t, result.Comments, 2)
	assert.Len(t, result.Findings, 2)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)
	assert.Equal(t, "bug found", result.Findings[0].Description)
}

func TestParseReviewOutput_ApprovedNewFormat(t *testing.T) {
	output := `{"approved": true, "findings": [], "summary": "All good"}`
	result, err := ParseReviewOutput(output)
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
			prompt, err := RenderReviewPrompt("diff", opts)
			require.NoError(t, err)
			assert.Contains(t, prompt, tt.contains)
		})
	}
}

func TestRenderReviewPrompt_SeverityGuide(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "HIGH: Bugs, security vulnerabilities")
	assert.Contains(t, prompt, "MEDIUM: Missing edge cases")
	assert.Contains(t, prompt, "LOW: Style preferences")
	assert.Contains(t, prompt, "CRITERIA: An acceptance criterion itself is wrong")
}

func TestRenderReviewPrompt_ActionableFindingGuidance(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)

	assert.Contains(t, prompt, "detailed enough for a fix worker to act without rediscovering the problem")
	assert.Contains(t, prompt, "file/line, function, symbol, or behavior")
	assert.Contains(t, prompt, "root cause and the failure scenario")
	assert.Contains(t, prompt, "suggested fix")
	assert.Contains(t, prompt, "Tests or verification")
	assert.Contains(t, prompt, `constraints or "do not" notes`)
	assert.Contains(t, prompt, "what invariant the fix must preserve")
}

func TestRenderReviewPrompt_MinFixSeverity(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "MEDIUM or HIGH severity")

	opts.MinFixSeverity = "low"
	prompt, err = RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "LOW severity or higher")

	opts.MinFixSeverity = "high"
	prompt, err = RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "HIGH severity or higher")
}

func TestRenderReviewPrompt_CriteriaSeverityGuide(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.Contains(t, prompt, "acceptance criterion itself is wrong")
	assert.Contains(t, prompt, `Use severity "CRITERIA" only when the acceptance criterion itself is flawed`)
}

func TestParseReviewOutput_CriteriaSeverity(t *testing.T) {
	output := `{"approved": false, "findings": [{"severity": "CRITERIA", "description": "Criterion 'tests pass' is too vague"}], "summary": "criteria issue"}`
	result, err := ParseReviewOutput(output)
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
			prompt, err := RenderReviewPrompt("diff", opts)
			require.NoError(t, err)

			assert.NotContains(t, prompt, "## User-Requested Fixes")
			assert.Contains(t, prompt, "## Acceptance Criteria")
			if tt.wantFix {
				assert.Contains(t, prompt, "- User requested: make logo bigger")
			}
		})
	}
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
			prompt, err := RenderReviewPrompt("diff", opts)
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
			prompt, err := RenderReviewPrompt("diff", opts)
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

func TestReviewPrompt_ForbidsActions(t *testing.T) {
	opts := agent.ReviewOptions{AcceptanceCriteria: []string{"works"}}
	got, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)

	userPromptWants := []string{
		"Self-Check Before Returning",
		"single JSON object",
		"no surrounding text",
	}
	for _, want := range userPromptWants {
		t.Run("user_prompt_contains_"+want, func(t *testing.T) {
			assert.Contains(t, got, want, "user prompt missing %q", want)
		})
	}

	systemPromptWants := []string{
		"Do NOT use any tools",
		"Do NOT call gh, git, bash",
		"Your ONLY output",
		"JSON",
	}
	for _, want := range systemPromptWants {
		t.Run("system_prompt_contains_"+want, func(t *testing.T) {
			assert.Contains(t, ReviewSystemPrompt, want, "system prompt missing %q", want)
		})
	}
}

func TestReviewPrompt_OmitsWorkerNoOpSectionWhenEmpty(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		WorkerNoOpVerdicts: nil,
	}
	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)
	assert.NotContains(t, prompt, "Worker No-Op Verdicts")
}

func TestReviewPrompt_IncludesWorkerNoOpSection(t *testing.T) {
	verdicts := []string{
		"**Worker #42 — no-op verdict**\n\nFindings reviewed against the current code:\n\n- **Foo**: bar\n\nConclusion: ok.",
		"**Worker #43 — no-op verdict**\n\n…second body…",
	}
	tests := []struct {
		name                 string
		priorReviewComments  []string
		userFeedbackComments []string
		precedingHeading     string
	}{
		{
			name:                 "after user feedback when present",
			priorReviewComments:  []string{"prior review body"},
			userFeedbackComments: []string{"user feedback body"},
			precedingHeading:     "## User Feedback",
		},
		{
			name:                 "after prior review when user feedback empty",
			priorReviewComments:  []string{"prior review body"},
			userFeedbackComments: nil,
			precedingHeading:     "## Prior Review History",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := agent.ReviewOptions{
				AcceptanceCriteria:   []string{"works"},
				PriorReviewComments:  tt.priorReviewComments,
				UserFeedbackComments: tt.userFeedbackComments,
				WorkerNoOpVerdicts:   verdicts,
			}
			prompt, err := RenderReviewPrompt("diff", opts)
			require.NoError(t, err)

			assert.Contains(t, prompt, "## Worker No-Op Verdicts")
			for _, v := range verdicts {
				assert.Contains(t, prompt, v)
				wrapped := "---\n" + v + "\n---"
				assert.Contains(t, prompt, wrapped, "verdict body must be wrapped in --- separators")
			}

			precedingIdx := strings.Index(prompt, tt.precedingHeading)
			workerIdx := strings.Index(prompt, "## Worker No-Op Verdicts")
			diffIdx := strings.Index(prompt, "## Diff")
			require.GreaterOrEqual(t, precedingIdx, 0, "preceding section must be present")
			require.GreaterOrEqual(t, workerIdx, 0, "worker no-op section must be present")
			require.GreaterOrEqual(t, diffIdx, 0, "diff section must be present")
			assert.Greater(t, workerIdx, precedingIdx, "worker no-op section must appear after %s", tt.precedingHeading)
			assert.Less(t, workerIdx, diffIdx, "worker no-op section must appear before ## Diff")
		})
	}
}

func TestReviewPrompt_WorkerNoOpSectionPositioning(t *testing.T) {
	opts := agent.ReviewOptions{
		AcceptanceCriteria:   []string{"works"},
		PriorReviewComments:  []string{"prior review body"},
		UserFeedbackComments: []string{"user feedback body"},
		WorkerNoOpVerdicts:   []string{"worker no-op body"},
	}
	prompt, err := RenderReviewPrompt("diff", opts)
	require.NoError(t, err)

	priorIdx := strings.Index(prompt, "## Prior Review History")
	userIdx := strings.Index(prompt, "## User Feedback")
	workerIdx := strings.Index(prompt, "## Worker No-Op Verdicts")
	diffIdx := strings.Index(prompt, "## Diff")

	require.GreaterOrEqual(t, priorIdx, 0, "prior review section must be present")
	require.GreaterOrEqual(t, userIdx, 0, "user feedback section must be present")
	require.GreaterOrEqual(t, workerIdx, 0, "worker no-op section must be present")
	require.GreaterOrEqual(t, diffIdx, 0, "diff section must be present")

	assert.Less(t, priorIdx, userIdx, "prior review must come before user feedback")
	assert.Less(t, userIdx, workerIdx, "user feedback must come before worker no-op verdicts")
	assert.Less(t, workerIdx, diffIdx, "worker no-op verdicts must come before diff")
}

func TestRenderReviewPrompt_UserFeedbackComments(t *testing.T) {
	tests := []struct {
		name                 string
		userFeedbackComments []string
		wantSection          bool
	}{
		{
			name:                 "nil omits section",
			userFeedbackComments: nil,
			wantSection:          false,
		},
		{
			name:                 "empty slice omits section",
			userFeedbackComments: []string{},
			wantSection:          false,
		},
		{
			name:                 "one comment includes section",
			userFeedbackComments: []string{"The nil check finding is a false positive"},
			wantSection:          true,
		},
		{
			name:                 "multiple comments lists all",
			userFeedbackComments: []string{"False positive on auth.go", "The error handling is intentional"},
			wantSection:          true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := agent.ReviewOptions{
				AcceptanceCriteria:   []string{"works"},
				UserFeedbackComments: tt.userFeedbackComments,
			}
			prompt, err := RenderReviewPrompt("diff", opts)
			require.NoError(t, err)

			if tt.wantSection {
				assert.Contains(t, prompt, "## User Feedback")
				assert.Contains(t, prompt, "Treat user feedback as authoritative")
				assert.Contains(t, prompt, "do NOT re-flag it")
				for _, comment := range tt.userFeedbackComments {
					assert.Contains(t, prompt, comment)
				}
			} else {
				assert.NotContains(t, prompt, "## User Feedback")
				assert.NotContains(t, prompt, "Treat user feedback as authoritative")
			}
		})
	}
}
