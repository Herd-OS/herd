package integrator

import (
	"regexp"
	"sort"
	"strings"

	"github.com/herd-os/herd/internal/agent"
)

type reviewFindingDedupeStats struct {
	RawFindings         int
	FindingsAfterDedupe int
	DedupedFindings     int
}

var (
	findingPathPrefixRE = regexp.MustCompile(`(?i)^\s*([A-Za-z0-9_.@~+/-]+\.(?:go|md|ya?ml|json|sh))(?::\d+){1,2}:?`)
	findingPathRE       = regexp.MustCompile(`(?i)(?:^|[\s"'([])([A-Za-z0-9_.@~+/-]+\.(?:go|md|ya?ml|json|sh))(?::\d+){0,2}`)
	findingSymbolRE     = regexp.MustCompile(`(?i)\b(?:function|func|method|symbol)\s+([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\b`)
	findingCallRE       = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*\(\)`)
	findingChunkRE      = regexp.MustCompile(`(?i)\bchunk\s+\d+(?:\s*/\s*\d+)?\b`)
	findingCycleRE      = regexp.MustCompile(`(?i)\bcycle\s+\d+\b`)
	findingPunctRE      = regexp.MustCompile(`[^a-z0-9_./ -]+`)
	findingSpaceRE      = regexp.MustCompile(`\s+`)
	findingLinePathRE   = regexp.MustCompile(`(?i)([A-Za-z0-9_.@~+/-]+\.(?:go|md|ya?ml|json|sh))(?::\d+){1,2}`)
	findingTokenRE      = regexp.MustCompile(`[a-z0-9_./-]+`)
)

var findingVolatilePhrases = []string{
	"in this chunk",
	"visible diff",
	"repository owner feedback says",
	"previous review says",
	"specific chunk",
	"this chunk",
}

var findingStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {}, "being": {},
	"by": {}, "can": {}, "did": {}, "do": {}, "does": {}, "for": {}, "from": {}, "has": {}, "have": {},
	"in": {}, "into": {}, "is": {}, "it": {}, "its": {}, "of": {}, "on": {}, "or": {}, "should": {},
	"still": {}, "that": {}, "the": {}, "their": {}, "them": {}, "this": {}, "to": {}, "was": {},
	"were": {}, "when": {}, "while": {}, "with": {},
	"func": {}, "function": {}, "method": {}, "symbol": {},
}

func dedupeReviewFindings(findings []agent.ReviewFinding) (deduped []agent.ReviewFinding, stats reviewFindingDedupeStats) {
	stats.RawFindings = len(findings)
	indexByFingerprint := make(map[string]int, len(findings))
	for _, finding := range findings {
		fingerprint := reviewFindingFingerprint(finding)
		if idx, ok := indexByFingerprint[fingerprint]; ok {
			deduped[idx] = betterFinding(deduped[idx], finding)
			continue
		}
		indexByFingerprint[fingerprint] = len(deduped)
		deduped = append(deduped, finding)
	}
	stats.FindingsAfterDedupe = len(deduped)
	stats.DedupedFindings = stats.RawFindings - stats.FindingsAfterDedupe
	return deduped, stats
}

func reviewFindingFingerprint(f agent.ReviewFinding) string {
	return strings.Join([]string{
		normalizeFindingSeverity(f.Severity),
		extractFindingPath(f.Description),
		extractFindingSymbol(f.Description),
		compactFindingClaim(f.Description),
	}, "\x00")
}

func extractFindingPath(desc string) string {
	if match := findingPathPrefixRE.FindStringSubmatch(desc); len(match) > 1 {
		return normalizeFindingPath(match[1])
	}
	if match := findingPathRE.FindStringSubmatch(desc); len(match) > 1 {
		return normalizeFindingPath(match[1])
	}
	return ""
}

func extractFindingSymbol(desc string) string {
	if match := findingSymbolRE.FindStringSubmatch(desc); len(match) > 1 {
		return strings.ToLower(match[1])
	}
	if match := findingCallRE.FindStringSubmatch(desc); len(match) > 1 {
		return strings.ToLower(match[1])
	}
	return ""
}

func compactFindingClaim(desc string) string {
	claim := strings.ToLower(desc)
	claim = findingLinePathRE.ReplaceAllString(claim, "$1")
	for _, phrase := range findingVolatilePhrases {
		claim = strings.ReplaceAll(claim, phrase, " ")
	}
	claim = findingChunkRE.ReplaceAllString(claim, " ")
	claim = findingCycleRE.ReplaceAllString(claim, " ")
	claim = strings.ReplaceAll(claim, "does not", "missing")
	claim = strings.ReplaceAll(claim, "doesn't", "missing")
	claim = strings.ReplaceAll(claim, "fails to", "missing")
	claim = strings.ReplaceAll(claim, "fail to", "missing")
	claim = strings.ReplaceAll(claim, "misses", "missing")
	claim = strings.ReplaceAll(claim, "cascade-failed", "cascade failed")
	if staleCascadeConflictClaim(claim) {
		return "stale cascade merge conflict"
	}
	claim = findingPunctRE.ReplaceAllString(claim, " ")
	claim = findingSpaceRE.ReplaceAllString(claim, " ")
	tokens := findingTokenRE.FindAllString(strings.TrimSpace(claim), -1)
	compacted := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		token = strings.Trim(token, "./-")
		if token == "" {
			continue
		}
		if _, ok := findingStopWords[token]; ok {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		compacted = append(compacted, token)
	}
	sort.Strings(compacted)
	return strings.Join(compacted, " ")
}

func betterFinding(a, b agent.ReviewFinding) agent.ReviewFinding {
	aScore := findingCompletenessScore(a)
	bScore := findingCompletenessScore(b)
	if bScore > aScore {
		return b
	}
	if aScore > bScore {
		return a
	}
	if findingSeverityRank(b.Severity) > findingSeverityRank(a.Severity) {
		return b
	}
	return a
}

func normalizeFindingSeverity(severity string) string {
	return strings.ToLower(strings.TrimSpace(severity))
}

func normalizeFindingPath(path string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(path), "`'\"(),[]"))
}

func staleCascadeConflictClaim(claim string) bool {
	hasCascade := strings.Contains(claim, "cascade failed") ||
		strings.Contains(claim, "cascade-failed") ||
		strings.Contains(claim, "conflict resolution cascade")
	hasConflict := strings.Contains(claim, "merge conflict") ||
		strings.Contains(claim, "merge conflicts") ||
		strings.Contains(claim, "unresolved conflict") ||
		strings.Contains(claim, "branch conflict") ||
		strings.Contains(claim, "conflict resolution cascade")
	hasHerd := strings.Contains(claim, "herd")
	return hasConflict && (hasCascade || hasHerd)
}

func findingCompletenessScore(f agent.ReviewFinding) int {
	desc := strings.TrimSpace(f.Description)
	if desc == "" {
		return 0
	}
	score := 1 + min(len(findingSpaceRE.ReplaceAllString(desc, " ")), 500)
	if extractFindingPath(desc) != "" {
		score += 80
	}
	if findingPathPrefixRE.MatchString(desc) {
		score += 30
	}
	if extractFindingSymbol(desc) != "" {
		score += 40
	}
	return score
}

func findingSeverityRank(severity string) int {
	switch normalizeFindingSeverity(severity) {
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "criteria":
		return 1
	default:
		return 0
	}
}
