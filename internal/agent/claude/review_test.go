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

func TestReview_SetsIsUnparseableOnBadOutput(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/test-agent.sh"
	// Stub binary emits non-JSON output that is long enough to bypass
	// the suspicious-output detector but cannot be parsed as JSON.
	err := os.WriteFile(script, []byte(`#!/bin/sh
cat > /dev/null
echo 'this is not json at all and is long enough to pass the suspicious-output filter'
`), 0755)
	require.NoError(t, err)

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
