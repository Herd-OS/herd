package issues

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateIssueBody_FitsUnderLimit(t *testing.T) {
	body := strings.Repeat("a", 1000)
	truncated, overflow := TruncateIssueBody(body)
	assert.Equal(t, body, truncated)
	assert.Equal(t, "", overflow)
}

func TestTruncateIssueBody_ExactlyAtLimit(t *testing.T) {
	body := strings.Repeat("a", githubIssueBodyMaxChars)
	truncated, overflow := TruncateIssueBody(body)
	assert.Equal(t, body, truncated)
	assert.Equal(t, "", overflow)
}

func TestTruncateIssueBody_JustOverLimit(t *testing.T) {
	body := strings.Repeat("a", githubIssueBodyMaxChars+1)
	truncated, overflow := TruncateIssueBody(body)
	assert.True(t, strings.HasSuffix(truncated, truncationMarker), "truncated body should end with marker")
	assert.LessOrEqual(t, len(truncated), githubIssueBodyMaxChars)
	require.NotEmpty(t, overflow)
	assert.True(t, strings.HasPrefix(overflow, overflowHeader), "overflow should start with the header")
}

func TestTruncateIssueBody_OverLimit(t *testing.T) {
	body := strings.Repeat("x", 80000)
	truncated, overflow := TruncateIssueBody(body)
	assert.LessOrEqual(t, len(truncated), githubIssueBodyMaxChars)
	require.True(t, strings.HasSuffix(truncated, truncationMarker), "truncated body should end with marker")
	require.True(t, strings.HasPrefix(overflow, overflowHeader), "overflow should start with header")

	truncatedContent := strings.TrimSuffix(truncated, truncationMarker)
	overflowContent := strings.TrimPrefix(overflow, overflowHeader)
	// Reconstruct: truncated minus marker + overflow minus header == body
	assert.Equal(t, body, truncatedContent+overflowContent)
	// And the cut portion of the body lives verbatim in overflow after the header.
	assert.Equal(t, body[len(truncatedContent):], overflowContent)
}

func TestTruncateIssueBody_CleanBoundary(t *testing.T) {
	budget := githubIssueBodyMaxChars - len(truncationMarker)
	// Place a "\n\n" paragraph break ~50 chars before the cutoff.
	prefix := strings.Repeat("a", budget-50)
	suffix := strings.Repeat("b", 1000)
	body := prefix + "\n\n" + suffix

	truncated, overflow := TruncateIssueBody(body)

	require.True(t, strings.HasSuffix(truncated, truncationMarker))
	require.True(t, strings.HasPrefix(overflow, overflowHeader))

	beforeMarker := strings.TrimSuffix(truncated, truncationMarker)
	require.NotEmpty(t, beforeMarker)
	// Truncation landed on a newline boundary.
	assert.Equal(t, byte('\n'), beforeMarker[len(beforeMarker)-1])
	// Reconstruct original body.
	overflowContent := strings.TrimPrefix(overflow, overflowHeader)
	assert.Equal(t, body, beforeMarker+overflowContent)
}

func TestTruncateIssueBody_NoCleanBoundary(t *testing.T) {
	// 80000 chars of a single rune with no newlines anywhere → falls back to
	// a hard cut at the budget.
	body := strings.Repeat("z", 80000)
	budget := githubIssueBodyMaxChars - len(truncationMarker)

	truncated, overflow := TruncateIssueBody(body)
	require.True(t, strings.HasSuffix(truncated, truncationMarker))
	require.True(t, strings.HasPrefix(overflow, overflowHeader))

	truncatedContent := strings.TrimSuffix(truncated, truncationMarker)
	assert.Equal(t, budget, len(truncatedContent), "should cut exactly at budget when no boundary is available")
}

func TestTruncateIssueBody_UnicodeSafety(t *testing.T) {
	// Each "🐑" is 4 UTF-8 bytes. Repeating to 80000 bytes guarantees the
	// naive cut at budget lands inside a multi-byte rune.
	body := strings.Repeat("🐑", 20000)
	require.Equal(t, 80000, len(body))

	truncated, overflow := TruncateIssueBody(body)
	assert.True(t, utf8.ValidString(truncated), "truncated body must be valid UTF-8")
	assert.True(t, utf8.ValidString(overflow), "overflow must be valid UTF-8")

	truncatedContent := strings.TrimSuffix(truncated, truncationMarker)
	overflowContent := strings.TrimPrefix(overflow, overflowHeader)
	assert.True(t, utf8.ValidString(truncatedContent))
	assert.True(t, utf8.ValidString(overflowContent))
	assert.Equal(t, body, truncatedContent+overflowContent)
}

func TestTruncateIssueBody_OverflowSplit(t *testing.T) {
	body := strings.Repeat("x", 200000)
	truncated, overflow := TruncateIssueBody(body)
	require.True(t, strings.HasSuffix(truncated, truncationMarker))
	require.True(t, strings.HasPrefix(overflow, overflowHeader))

	chunks := SplitOverflowComments(overflow)
	require.Greater(t, len(chunks), 1, "200000-char body should yield multiple overflow chunks")

	var reconstructed strings.Builder
	headerPrefix := "_Part "
	for i, chunk := range chunks {
		assert.LessOrEqualf(t, len(chunk), githubIssueBodyMaxChars, "chunk %d exceeds GitHub limit", i)

		// Each chunk in a multi-chunk split starts with a "_Part N of M..._"
		// header followed by "\n\n". Strip both to recover the raw content.
		require.Truef(t, strings.HasPrefix(chunk, headerPrefix), "chunk %d missing Part header", i)
		expectedHeader := fmt.Sprintf("_Part %d of %d (continued from issue body)._\n\n", i+1, len(chunks))
		require.Truef(t, strings.HasPrefix(chunk, expectedHeader), "chunk %d has unexpected header", i)
		reconstructed.WriteString(strings.TrimPrefix(chunk, expectedHeader))
	}
	// Concatenating the raw chunk content (post-header) reproduces overflow.
	assert.Equal(t, overflow, reconstructed.String())
}

func TestSplitOverflowComments_Empty(t *testing.T) {
	assert.Nil(t, SplitOverflowComments(""))
}

func TestSplitOverflowComments_SingleChunk(t *testing.T) {
	overflow := strings.Repeat("y", 1000)
	result := SplitOverflowComments(overflow)
	require.Len(t, result, 1)
	assert.Equal(t, overflow, result[0])
}
