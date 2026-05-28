package integrator

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// injectedFindingsMarker / injectedFindingsCloseMarker key an injected block
// to its source manual issue so injection is idempotent. These markers are
// herd-internal — humans never author them.
func injectedFindingsMarker(sourceIssue int) string {
	return fmt.Sprintf("<!-- herd:injected-findings:%d -->", sourceIssue)
}

func injectedFindingsCloseMarker(sourceIssue int) string {
	return fmt.Sprintf("<!-- /herd:injected-findings:%d -->", sourceIssue)
}

// perDepFindingsCap is the max bytes of extracted findings forwarded per manual
// dependency. Content beyond this is truncated with a notice.
const perDepFindingsCap = 8192

// herdAutomatedCommentPrefixes lists trimmed-body prefixes of comments that herd
// itself posts. extractFindings excludes any comment whose trimmed body starts
// with one of these (they are not human findings).
var herdAutomatedCommentPrefixes = []string{
	"⚠️ **HerdOS",
	"🔧",
	"📋 **Worker Progress**",
	"🔍 **HerdOS Agent Review**",
	"✅ **HerdOS Agent Review**",
	"🔄 **Integrator",
	"👋 **Manual task**",
	"📋 **Worker",
}

// extractFindings builds the forwarded findings text for a manual task. It
// takes the manual issue's raw body and its comments (chronological order).
// It strips the YAML frontmatter from the body (keeping the rest verbatim),
// then appends every human-authored comment — excluding comments whose author
// login ends in "[bot]" and comments whose trimmed body starts with a known
// herd automated prefix. The pieces are joined chronologically with a
// separator. If the result exceeds perDepFindingsCap it is truncated and a
// notice referencing the source issue is appended. If there is no human
// content beyond boilerplate (empty after trimming), it returns ("", false).
func extractFindings(sourceIssue int, body string, comments []*platform.Comment) (string, bool) {
	md := strings.TrimSpace(issues.StripFrontMatter(body))

	var parts []string
	if md != "" {
		parts = append(parts, md)
	}

	for _, c := range comments {
		if c == nil {
			continue
		}
		if strings.HasSuffix(c.AuthorLogin, "[bot]") {
			continue
		}
		trimmed := strings.TrimSpace(c.Body)
		if trimmed == "" {
			continue
		}
		if hasHerdAutomatedPrefix(trimmed) {
			continue
		}
		parts = append(parts, trimmed)
	}

	if len(parts) == 0 {
		return "", false
	}

	combined := strings.Join(parts, "\n\n---\n\n")
	if strings.TrimSpace(combined) == "" {
		return "", false
	}

	if len(combined) > perDepFindingsCap {
		cut := runeBoundaryBefore(combined, perDepFindingsCap)
		notice := fmt.Sprintf("\n\n_...truncated; see #%d for full findings._", sourceIssue)
		combined = combined[:cut] + notice
	}

	return combined, true
}

// hasHerdAutomatedPrefix reports whether trimmed begins with any of the
// herd-authored comment prefixes that extractFindings filters out.
func hasHerdAutomatedPrefix(trimmed string) bool {
	for _, p := range herdAutomatedCommentPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// injectFindings appends a keyed findings block for sourceIssue to issueBody.
// It is idempotent: if a block for sourceIssue is already present (detected via
// the open marker), it returns (issueBody, false). The injected block format:
//
//	<!-- herd:injected-findings:<N> -->
//	## Context from #<N> (manual task)
//	<findings>
//	<!-- /herd:injected-findings:<N> -->
func injectFindings(issueBody string, sourceIssue int, findings string) (string, bool) {
	if strings.Contains(issueBody, injectedFindingsMarker(sourceIssue)) {
		return issueBody, false
	}
	block := fmt.Sprintf("%s\n## Context from #%d (manual task)\n\n%s\n%s\n",
		injectedFindingsMarker(sourceIssue), sourceIssue, findings, injectedFindingsCloseMarker(sourceIssue))
	newBody := strings.TrimRight(issueBody, "\n") + "\n\n" + block
	return newBody, true
}

// runeBoundaryBefore walks back from i to the nearest UTF-8 rune start so
// that s[:returned] is always valid UTF-8 for valid UTF-8 inputs. Mirrors the
// helper in internal/issues/body.go.
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
