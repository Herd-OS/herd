package issues

import (
	"fmt"
	"unicode/utf8"
)

// githubIssueBodyMaxChars is a safety margin under GitHub's hard 65536-char
// limit for issue/comment bodies. We leave room for the truncation marker
// and any UTF-8 boundary slack.
const githubIssueBodyMaxChars = 65000

// truncationMarker is appended to the truncated body. It points readers at
// the follow-up comment(s) where the rest of the content lives.
const truncationMarker = "\n\n---\n_⚠️ Body truncated to fit GitHub's 65536-char limit. See follow-up comment on this issue for the rest._\n"

// overflowHeader is prepended to the overflow content returned alongside a
// truncated body. It explains where the content came from.
const overflowHeader = "_Continued from issue body (content was truncated to fit GitHub's 65536-char limit)._\n\n"

// boundaryWindow is how far back we look for a clean newline boundary when
// truncating or splitting.
const boundaryWindow = 200

// TruncateIssueBody returns the body unchanged if it fits under
// githubIssueBodyMaxChars. Otherwise it returns a truncated body ending in
// truncationMarker plus the overflow content (everything that was cut,
// prefixed with a header explaining its origin).
//
// Truncation prefers a clean boundary: if a newline ("\n\n" preferred, then
// "\n") exists within ~200 chars before the cutoff, truncate there. Otherwise
// fall back to a hard cut at the cutoff. The cut is adjusted backwards if
// necessary so that it lands on a UTF-8 rune boundary.
func TruncateIssueBody(body string) (truncated string, overflow string) {
	if len(body) <= githubIssueBodyMaxChars {
		return body, ""
	}
	budget := githubIssueBodyMaxChars - len(truncationMarker)
	cut := findTruncationBoundary(body, budget)
	cut = runeBoundaryBefore(body, cut)
	truncated = body[:cut] + truncationMarker
	overflow = overflowHeader + body[cut:]
	return truncated, overflow
}

// SplitOverflowComments takes overflow content and returns a slice of comment
// bodies, each <= githubIssueBodyMaxChars. If overflow fits in one comment,
// returns a single-element slice with the content unchanged. Otherwise each
// comment is prefixed with a "_Part N of M (continued from issue body)._"
// header. Returns nil for an empty input.
func SplitOverflowComments(overflow string) []string {
	if overflow == "" {
		return nil
	}
	if len(overflow) <= githubIssueBodyMaxChars {
		return []string{overflow}
	}

	// Reserve room per chunk for the "_Part N of M..._" header.
	const headerReserve = 80
	chunkBudget := githubIssueBodyMaxChars - headerReserve

	var raw []string
	remaining := overflow
	for len(remaining) > 0 {
		if len(remaining) <= chunkBudget {
			raw = append(raw, remaining)
			break
		}
		cut := chunkBudget
		minCut := cut - boundaryWindow
		if minCut < 0 {
			minCut = 0
		}
		// Prefer the nearest preceding "\n" within boundaryWindow chars.
		for i := cut - 1; i >= minCut; i-- {
			if remaining[i] == '\n' {
				cut = i + 1
				break
			}
		}
		// Snap back to a UTF-8 rune boundary if needed.
		cut = runeBoundaryBefore(remaining, cut)
		if cut == 0 {
			// Defensive: never make zero progress, even with pathological input.
			cut = chunkBudget
		}
		raw = append(raw, remaining[:cut])
		remaining = remaining[cut:]
	}

	parts := len(raw)
	out := make([]string, parts)
	for i, chunk := range raw {
		out[i] = fmt.Sprintf("_Part %d of %d (continued from issue body)._\n\n", i+1, parts) + chunk
	}
	return out
}

// findTruncationBoundary scans backwards from budget up to boundaryWindow
// bytes looking for "\n\n" (preferred) or "\n". Returns the byte position to
// cut at (just after the boundary). If no boundary is found within the
// window, returns budget unchanged.
func findTruncationBoundary(body string, budget int) int {
	if budget > len(body) {
		budget = len(body)
	}
	if budget <= 0 {
		return 0
	}
	minCut := budget - boundaryWindow
	if minCut < 0 {
		minCut = 0
	}
	// Look for "\n\n" first; the cut lands right after both newlines.
	for i := budget - 2; i >= minCut; i-- {
		if body[i] == '\n' && body[i+1] == '\n' {
			return i + 2
		}
	}
	// Fall back to a single "\n"; cut right after the newline.
	for i := budget - 1; i >= minCut; i-- {
		if body[i] == '\n' {
			return i + 1
		}
	}
	return budget
}

// runeBoundaryBefore walks back from i to the nearest UTF-8 rune start so
// that s[:returned] is always valid UTF-8 for valid UTF-8 inputs.
func runeBoundaryBefore(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
