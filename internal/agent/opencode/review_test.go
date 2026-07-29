package opencode

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReview_PrependsReviewSystemPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	argvDump := dir + "/argv.bin"
	stdinDump := dir + "/stdin.txt"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$@" > '%s'
cat > '%s'
echo '{"approved": true, "findings": [], "summary": "LGTM"}'
`, argvDump, stdinDump)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "anthropic/claude-sonnet-4")
	result, err := a.Review(context.Background(), "diff body", agent.ReviewOptions{
		AcceptanceCriteria: []string{"tests pass"},
		RepoRoot:           dir,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Approved)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)

	// `run` must be the first argv element and --dangerously-skip-permissions
	// must be present. Model flag is also expected.
	assert.Equal(t, "run", argv[0])
	assert.Contains(t, argv, "--dangerously-skip-permissions")
	assert.Contains(t, argv, "--model")
	assert.Contains(t, argv, "anthropic/claude-sonnet-4")

	// The combined message goes via stdin, not argv. Verify the stdin
	// content begins with the ReviewSystemPrompt and contains the diff.
	stdinBytes, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	stdinContent := string(stdinBytes)
	assert.True(t, strings.HasPrefix(stdinContent, prompt.ReviewSystemPrompt),
		"stdin must begin with ReviewSystemPrompt; got prefix: %q",
		stdinContent[:min(len(stdinContent), 80)])
	assert.Contains(t, stdinContent, "diff body",
		"stdin must contain the diff body from the rendered review prompt")

	// The system prompt and diff must NOT leak into argv.
	for _, a := range argv {
		assert.NotContains(t, a, "diff body",
			"argv must not contain the diff body; argv element %q", a)
	}
}

func TestReview_NoModelOmitsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	argvDump := dir + "/argv.bin"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$@" > '%s'
cat > /dev/null
echo '{"approved": false, "findings": [{"severity": "HIGH", "description": "issue"}], "summary": "needs work"}'
`, argvDump)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	result, err := a.Review(context.Background(), "small diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	require.NoError(t, err)
	assert.False(t, result.Approved)
	assert.Len(t, result.Findings, 1)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)
	assert.NotContains(t, argv, "--model")
}

func TestReview_SuspiciousOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\necho 'Execution error'\n"), 0o755))

	a := New(script, "")
	_, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	assert.Error(t, reviewErr)
	assert.Contains(t, reviewErr.Error(), "suspicious output after retry")
}

func TestReview_EmptyOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat > /dev/null\nexit 0\n"), 0o755))

	a := New(script, "")
	_, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	assert.Error(t, reviewErr)
	assert.Contains(t, reviewErr.Error(), "suspicious output after retry")
}

func TestReview_RetrySucceedsOnSecondAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	marker := dir + "/attempt"
	content := fmt.Sprintf(`#!/bin/sh
cat > /dev/null
if [ -f "%s" ]; then
  echo '{"approved": true, "findings": [], "summary": "LGTM"}'
else
  touch "%s"
  echo "Execution error"
fi
`, marker, marker)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	result, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	require.NoError(t, reviewErr)
	assert.True(t, result.Approved)
}

func TestReview_SetsIsUnparseableOnBadOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	content := `#!/bin/sh
cat > /dev/null
echo 'this is not json at all and is long enough to pass the suspicious-output filter'
`
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	result, reviewErr := a.Review(context.Background(), "diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"works"},
		RepoRoot:           dir,
	})
	require.NoError(t, reviewErr)
	require.NotNil(t, result)
	assert.True(t, result.IsUnparseable, "expected IsUnparseable=true on bad output")
	assert.False(t, result.Approved, "expected Approved=false on bad output")
	assert.True(t, strings.HasPrefix(result.Summary, "Failed to parse"),
		"expected Summary to start with %q, got %q", "Failed to parse", result.Summary)
}

// TestReview_LargeDiffDoesNotExceedArgMax verifies that Review succeeds with
// a ~200KB diff — mirroring TestReview_LargeDiffPassedViaStdin in the claude
// provider. The diff is piped via stdin, so argv stays small.
func TestReview_LargeDiffDoesNotExceedArgMax(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	stdinDump := dir + "/stdin.txt"
	argvDump := dir + "/argv.bin"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$@" > '%s'
cat > '%s'
echo '{"approved": true, "findings": [], "summary": "diff reviewed"}'
`, argvDump, stdinDump)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	// ~200KB diff — well above typical ARG_MAX limits when passed via argv.
	largeDiff := strings.Repeat("+ added line of code that pushes the diff over the ARG_MAX limit\n", 3500)
	require.Greater(t, len(largeDiff), 200_000,
		"sanity check: large diff must exceed 200KB to exercise the ARG_MAX guard")

	a := New(script, "")
	result, err := a.Review(context.Background(), largeDiff, agent.ReviewOptions{
		AcceptanceCriteria: []string{"diff reviewed"},
		RepoRoot:           dir,
	})
	require.NoError(t, err, "Review must succeed with a 200KB+ diff")
	require.NotNil(t, result)
	assert.True(t, result.Approved)
	assert.False(t, result.IsUnparseable)

	// The diff content must be in the stdin payload, not in argv.
	stdinBytes, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Contains(t, string(stdinBytes), "added line of code that pushes the diff",
		"stub must have received the diff body via stdin")

	argv := readArgvDump(t, argvDump)
	for _, a := range argv {
		assert.NotContains(t, a, "added line of code that pushes the diff",
			"diff body must not appear in argv; argv element %q", a)
	}
}

func TestSynthesizeReviewNonConvergence_PrependsSystemPromptAndParsesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	argvDump := dir + "/argv.bin"
	stdinDump := dir + "/stdin.txt"
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$@" > '%s'
cat > '%s'
echo '{"should_escalate":true,"confidence":0.64,"root_cause_title":"Cross-store invariant","requirement_reinterpretation":{"constraint_kind":"over_constrained","conflicting_requirement":"instant visibility everywhere","platform_consistency_constraint":"replicas converge asynchronously","preserved_safety_property":"revoked grants cannot be used","corrected_invariant":"revocation linearizes before asynchronous cleanup","linearization_boundaries":["revocation marker"],"durability_boundaries":["marker persisted"],"evidence_references":["issue:1121:task"]}}'
`, argvDump, stdinDump)
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "anthropic/claude-sonnet-4")
	result, err := a.SynthesizeReviewNonConvergence(context.Background(), agent.ReviewSynthesisInput{
		PRNumber:          21,
		BatchNumber:       116,
		HeadSHA:           "sha-21",
		CurrentPRMetadata: "metadata marker",
		OriginalRequirements: []agent.ReviewSynthesisRequirement{{
			IssueNumber: 1121,
			Title:       "OpenCode original requirement marker",
			Task:        "preserve revocation safety",
		}},
		PriorStrategyFixIssues: []agent.ReviewSynthesisStrategyFixIssue{{
			Number:  70,
			Cycle:   1,
			Title:   "OpenCode prior strategy marker",
			HeadSHA: "sha-20",
		}},
		Cycles: []agent.ReviewSynthesisCycle{{
			Cycle:   1,
			HeadSHA: "sha-20",
			CompletedFixIssues: []agent.ReviewSynthesisFixIssue{{
				Number:           71,
				Title:            "OpenCode cycle fix marker",
				Body:             "cycle fix body",
				ValidationStatus: "passed cycle validation",
				WorkerReport:     true,
			}},
		}},
	}, agent.ReviewSynthesisOptions{RepoRoot: dir})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldEscalate)
	assert.Equal(t, 0.64, result.Confidence)
	assert.Equal(t, "Cross-store invariant", result.RootCauseTitle)
	require.NotNil(t, result.RequirementReinterpretation)
	assert.Equal(t, agent.ReviewRequirementOverConstrained, result.RequirementReinterpretation.ConstraintKind)

	stdinBytes, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	stdinContent := string(stdinBytes)
	assert.True(t, strings.HasPrefix(stdinContent, prompt.ReviewSynthesisSystemPrompt))
	assert.Contains(t, stdinContent, "sha-21")
	assert.Contains(t, stdinContent, "metadata marker")
	assert.Contains(t, stdinContent, "OpenCode original requirement marker")
	assert.Contains(t, stdinContent, "OpenCode prior strategy marker")
	assert.Contains(t, stdinContent, "OpenCode cycle fix marker")
	assert.Contains(t, stdinContent, "passed cycle validation")

	argv := readArgvDump(t, argvDump)
	assert.Equal(t, "run", argv[0])
	assert.Contains(t, argv, "--model")
	assert.Contains(t, argv, "anthropic/claude-sonnet-4")
}

func TestVerifyReviewNonConvergence_UsesStrictOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}
	dir := t.TempDir()
	script := dir + "/verify-opencode.sh"
	require.NoError(t, os.WriteFile(script, []byte(`#!/bin/sh
cat >/dev/null
echo '{"approved":false,"confidence":0.98,"reason":"unrelated findings"}'
`), 0o755))

	result, err := New(script, "").VerifyReviewNonConvergence(
		context.Background(), agent.ReviewVerificationInput{}, agent.ReviewSynthesisOptions{RepoRoot: dir},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, .98, result.Confidence)
}

func TestSynthesizeReviewNonConvergence_InvalidOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	dir := t.TempDir()
	script := dir + "/opencode.sh"
	content := `#!/bin/sh
cat > /dev/null
echo 'this is not json at all and is long enough to pass the suspicious-output filter'
`
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

	a := New(script, "")
	result, err := a.SynthesizeReviewNonConvergence(context.Background(), agent.ReviewSynthesisInput{}, agent.ReviewSynthesisOptions{RepoRoot: dir})
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "parsing review synthesis output")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
