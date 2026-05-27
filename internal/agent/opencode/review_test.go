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
	script := dir + "/opencode.sh"
	content := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$@" > '%s'
echo '{"approved": true, "findings": [], "summary": "LGTM"}'
`, argvDump)
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

	// The final positional argument is the combined message; it must
	// begin with the ReviewSystemPrompt followed by the rendered review.
	message := argv[len(argv)-1]
	assert.True(t, strings.HasPrefix(message, prompt.ReviewSystemPrompt),
		"message must begin with ReviewSystemPrompt; got prefix: %q", message[:min(len(message), 80)])
	assert.Contains(t, message, "diff body",
		"message must contain the diff body from the rendered review prompt")

	// `run` must be the first argv element and --dangerously-skip-permissions
	// must be present.
	assert.Equal(t, "run", argv[0])
	assert.Contains(t, argv, "--dangerously-skip-permissions")
	assert.Contains(t, argv, "--model")
	assert.Contains(t, argv, "anthropic/claude-sonnet-4")
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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'Execution error'\n"), 0o755))

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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755))

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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
