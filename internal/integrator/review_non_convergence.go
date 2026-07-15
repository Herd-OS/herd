package integrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

type reviewConvergenceDecision string

const (
	reviewDecisionContinueFixLoop           reviewConvergenceDecision = "continue_fix_loop"
	reviewDecisionRequestTargetedFix        reviewConvergenceDecision = "request_targeted_fix"
	reviewDecisionEscalateToArchitectureFix reviewConvergenceDecision = "escalate_to_architecture_fix"
)

type reviewHistoryCycle struct {
	Cycle                       int
	HeadSHA                     string
	RawFindingsBeforeDedupe     int
	FindingsAfterDedupe         int
	PostedFindingsCount         int
	StalePRStateFindingsIgnored int
	ChunkCoverageSummary        string
	FixIssueNumbers             []int
	Status                      string
	FindingsBySeverity          map[string][]string
	FixIssues                   []reviewHistoryFixIssue
}

type reviewHistoryFixIssue struct {
	Number           int
	StatusLabel      string
	WorkerReport     bool
	FilesSummary     []string
	ValidationStatus string
}

type reviewConvergenceCluster struct {
	PackageClusters []string
	RootCauseTerms  []string
	Fingerprint     string
	Summary         string
}

type reviewConvergenceAnalysis struct {
	Decision             reviewConvergenceDecision
	Confidence           float64
	Rationale            string
	Cycles               []reviewHistoryCycle
	TrendCounts          []int
	CompletedFixIssues   []int
	InProgressFixIssues  []int
	Cluster              reviewConvergenceCluster
	LatestFindingCount   int
	EarliestFindingCount int
}

const reviewNonConvergenceMinLatestFindings = 8
const reviewNonConvergenceRequireIncreasingOrFlat = true
const reviewNonConvergenceRepeatedSubsystemFindingThreshold = 3
const reviewNonConvergenceRepeatedSubsystemCycleThreshold = 2
const reviewNonConvergenceRepeatedRootCauseFindingThreshold = 3
const reviewNonConvergenceRepeatedRootCauseCycleThreshold = 2

var (
	reviewCycleRE          = regexp.MustCompile(`(?i)\bcycle\s+(\d+)\b`)
	reviewAggregationRE    = regexp.MustCompile(`(?i)^-\s*(Raw findings before dedupe|Findings after dedupe|Stale PR-state findings ignored):\s*(\d+)\s*$`)
	reviewFoundCountRE     = regexp.MustCompile(`(?i)\bFound\s+(\d+)\s+issues?\b`)
	reviewFixIssueRE       = regexp.MustCompile(`(?i)(?:fix\s+#|fix worker dispatched\s*(?:→|->)\s*#|created strategy-level fix issue\s*#)(\d+)`)
	reviewDirectFindingRE  = regexp.MustCompile(`^\s*(?:[-*]|\d+\.)\s+\*\*(?:\[)?(HIGH|MEDIUM|LOW|CRITERIA)(?:\])?\*\*:?\s*(.*)$`)
	reviewNumberFindingRE  = regexp.MustCompile(`^\s*\d+\.\s+\*\*\[(HIGH|MEDIUM|LOW|CRITERIA)\]\*\*:?\s*(.*)$`)
	reviewPathRE           = regexp.MustCompile(`(?:^|[\s(\[` + "`" + `])([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+)(?::\d+)?`)
	reviewRootCauseSplitRE = regexp.MustCompile(`(?i)\b(Suggested fix|Tests|Constraints)\s*:`)
)

type reviewClusterCounts struct {
	findings int
	cycles   map[int]struct{}
}

var reviewRootCauseVocabulary = []string{
	"github-visible",
	"side-effect",
	"side effect",
	"idempotency",
	"mutation",
	"started",
	"pre-call",
	"post-call",
	"unknown",
	"repair",
	"dispatch",
	"workflow",
	"retry",
	"durable",
}

func collectReviewHistoryFromComments(comments []*platform.Comment, allIssues []*platform.Issue, prNumber int, batchNumber int, headSHA string, window int, trustedHumanLogins ...string) []reviewHistoryCycle {
	cycles := make([]reviewHistoryCycle, 0, len(comments))
	for _, comment := range comments {
		cycle, ok := parseReviewHistoryCycle(comment, prNumber, batchNumber, headSHA, trustedHumanLogins...)
		if !ok {
			continue
		}
		cycles = append(cycles, cycle)
	}
	if len(cycles) == 0 {
		return nil
	}

	allHaveCycle := true
	for _, cycle := range cycles {
		if cycle.Cycle <= 0 {
			allHaveCycle = false
			break
		}
	}
	if allHaveCycle {
		sort.SliceStable(cycles, func(i, j int) bool {
			return cycles[i].Cycle < cycles[j].Cycle
		})
	}
	if window > 0 && len(cycles) > window {
		cycles = cycles[len(cycles)-window:]
	}
	return attachReviewFixIssueHistory(cycles, reviewHistoryIssuesForPR(allIssues, prNumber))
}

func parseReviewHistoryCycle(comment *platform.Comment, prNumber int, batchNumber int, headSHA string, trustedHumanLogins ...string) (reviewHistoryCycle, bool) {
	if !isTrustedReviewResultMarkerComment(comment, trustedHumanLogins...) {
		return reviewHistoryCycle{}, false
	}
	body := comment.Body
	marker, hasMarker := parseReviewResultMarker(body)
	if hasMarker {
		if marker.PRNumber != prNumber || marker.BatchNumber != batchNumber {
			return reviewHistoryCycle{}, false
		}
		if marker.HeadSHA != "" && headSHA != "" && marker.HeadSHA != headSHA {
			return reviewHistoryCycle{}, false
		}
	}

	findingsBySeverity := extractReviewFindingsBySeverity(body)
	cycle := reviewHistoryCycle{
		FindingsBySeverity: findingsBySeverity,
		FixIssueNumbers:    extractReviewFixIssueNumbers(body),
	}
	if hasMarker {
		cycle.Cycle = marker.Cycle
		cycle.HeadSHA = marker.HeadSHA
		cycle.Status = marker.Status
		cycle.PostedFindingsCount = marker.FindingsCount
	}
	if cycle.Cycle == 0 {
		cycle.Cycle = extractReviewCycleNumber(body)
	}

	raw, after, stale := extractReviewAggregationCounts(body)
	cycle.RawFindingsBeforeDedupe = raw
	cycle.FindingsAfterDedupe = after
	cycle.StalePRStateFindingsIgnored = stale
	if cycle.PostedFindingsCount == 0 {
		cycle.PostedFindingsCount = extractVisiblePostedFindingCount(body, findingsBySeverity)
	}
	if cycle.ChunkCoverageSummary == "" {
		cycle.ChunkCoverageSummary = extractReviewCoverageSummary(body)
	}

	if hasMarker || cycle.Cycle > 0 || cycle.PostedFindingsCount > 0 || len(findingsBySeverity) > 0 || len(cycle.FixIssueNumbers) > 0 {
		return cycle, true
	}
	return reviewHistoryCycle{}, false
}

func attachReviewFixIssueHistory(cycles []reviewHistoryCycle, allIssues []*platform.Issue) []reviewHistoryCycle {
	if len(cycles) == 0 {
		return nil
	}
	out := make([]reviewHistoryCycle, len(cycles))
	copy(out, cycles)

	for _, issue := range allIssues {
		if issue == nil {
			continue
		}
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		fm := parsed.FrontMatter
		if fm.Type != "fix" || fm.BatchPR <= 0 || fm.FixCycle <= 0 || fm.CIFixCycle > 0 || fm.ConflictResolution {
			continue
		}
		fix := reviewHistoryFixIssue{
			Number:           issue.Number,
			StatusLabel:      issues.StatusLabel(issue.Labels),
			WorkerReport:     bodyHasWorkerReport(issue.Body),
			FilesSummary:     extractReviewFixFilesSummary(issue.Body, parsed.FilesToModify),
			ValidationStatus: extractReviewValidationStatus(issue.Body),
		}
		for i := range out {
			if intSliceContains(out[i].FixIssueNumbers, issue.Number) || (len(out[i].FixIssueNumbers) == 0 && out[i].Cycle == fm.FixCycle) {
				out[i].FixIssues = append(out[i].FixIssues, fix)
				if !intSliceContains(out[i].FixIssueNumbers, issue.Number) {
					out[i].FixIssueNumbers = append(out[i].FixIssueNumbers, issue.Number)
					sort.Ints(out[i].FixIssueNumbers)
				}
			}
		}
	}
	return out
}

func reviewHistoryIssuesForPR(allIssues []*platform.Issue, prNumber int) []*platform.Issue {
	if prNumber == 0 {
		return allIssues
	}
	filtered := make([]*platform.Issue, 0, len(allIssues))
	for _, issue := range allIssues {
		if issue == nil {
			continue
		}
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		if parsed.FrontMatter.BatchPR == prNumber {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

func analyzeReviewConvergence(cycles []reviewHistoryCycle, minCompletedCycles int) reviewConvergenceAnalysis {
	analysis := reviewConvergenceAnalysis{
		Decision:   reviewDecisionContinueFixLoop,
		Confidence: 0.55,
		Cycles:     append([]reviewHistoryCycle(nil), cycles...),
		Cluster:    buildReviewConvergenceCluster(cycles),
	}
	if len(cycles) == 0 {
		analysis.Rationale = "no parsed review cycles available"
		return analysis
	}

	for _, cycle := range cycles {
		analysis.TrendCounts = append(analysis.TrendCounts, reviewFindingCount(cycle))
		for _, fix := range cycle.FixIssues {
			switch fix.StatusLabel {
			case issues.StatusDone:
				analysis.CompletedFixIssues = appendUniqueInt(analysis.CompletedFixIssues, fix.Number)
			case issues.StatusInProgress, issues.StatusReady:
				if cycle.Cycle == cycles[len(cycles)-1].Cycle {
					analysis.InProgressFixIssues = appendUniqueInt(analysis.InProgressFixIssues, fix.Number)
				}
			}
			if fix.StatusLabel != issues.StatusDone && isSuccessfulWorkerReport(fix) {
				analysis.CompletedFixIssues = appendUniqueInt(analysis.CompletedFixIssues, fix.Number)
			}
		}
	}
	if len(analysis.TrendCounts) > 0 {
		analysis.EarliestFindingCount = analysis.TrendCounts[0]
		analysis.LatestFindingCount = analysis.TrendCounts[len(analysis.TrendCounts)-1]
	}
	if len(analysis.InProgressFixIssues) > 0 {
		analysis.Rationale = "latest review cycle still has ready or in-progress fix issues; synthesis is deferred"
		return analysis
	}

	completedCycles := countCompletedReviewCycles(cycles)
	if completedCycles < minCompletedCycles {
		analysis.Rationale = fmt.Sprintf("only %d completed fix cycles; need at least %d", completedCycles, minCompletedCycles)
		return analysis
	}
	if analysis.LatestFindingCount < reviewNonConvergenceMinLatestFindings {
		analysis.Rationale = fmt.Sprintf("latest finding count %d is below non-convergence threshold %d", analysis.LatestFindingCount, reviewNonConvergenceMinLatestFindings)
		return analysis
	}

	trendIncreasingOrFlat := analysis.LatestFindingCount >= analysis.EarliestFindingCount && completedCycles > 0
	if reviewNonConvergenceRequireIncreasingOrFlat && !trendIncreasingOrFlat {
		analysis.Rationale = "finding trend is decreasing after completed fix cycles"
		return analysis
	}

	repeatedSubsystem := len(analysis.Cluster.PackageClusters) > 0
	repeatedRootCause := len(analysis.Cluster.RootCauseTerms) > 0
	if repeatedSubsystem || repeatedRootCause {
		analysis.Decision = reviewDecisionEscalateToArchitectureFix
		analysis.Confidence = 0.86
		analysis.Rationale = "finding trend is increasing or flat after completed fix cycles and repeated subsystem/root-cause clusters were detected"
		return analysis
	}
	analysis.Rationale = "no repeated subsystem or root-cause cluster met deterministic thresholds"
	return analysis
}

func packageClusterFromFinding(description string) string {
	for _, match := range reviewPathRE.FindAllStringSubmatch(description, -1) {
		if len(match) < 2 {
			continue
		}
		cluster := normalizeReviewPackagePath(match[1])
		if cluster != "" {
			return cluster
		}
	}
	return ""
}

func rootCauseTermsFromFinding(description string) []string {
	beforeBoilerplate := reviewRootCauseSplitRE.Split(description, 2)[0]
	normalized := strings.ToLower(beforeBoilerplate)
	normalized = strings.ReplaceAll(normalized, "_", "-")

	var terms []string
	for _, term := range reviewRootCauseVocabulary {
		if strings.Contains(normalized, term) {
			terms = append(terms, term)
		}
	}
	sort.Strings(terms)
	return terms
}

func buildReviewConvergenceCluster(cycles []reviewHistoryCycle) reviewConvergenceCluster {
	packages := map[string]*reviewClusterCounts{}
	terms := map[string]*reviewClusterCounts{}

	for i, cycle := range cycles {
		cycleID := cycle.Cycle
		if cycleID == 0 {
			cycleID = i + 1
		}
		for _, finding := range allReviewFindingDescriptions(cycle) {
			if pkg := packageClusterFromFinding(finding); pkg != "" {
				addReviewClusterCount(packages, pkg, cycleID)
			}
			for _, term := range rootCauseTermsFromFinding(finding) {
				addReviewClusterCount(terms, term, cycleID)
			}
		}
	}

	cluster := reviewConvergenceCluster{
		PackageClusters: qualifyingReviewClusterKeys(packages, reviewNonConvergenceRepeatedSubsystemFindingThreshold, reviewNonConvergenceRepeatedSubsystemCycleThreshold),
		RootCauseTerms:  qualifyingReviewClusterKeys(terms, reviewNonConvergenceRepeatedRootCauseFindingThreshold, reviewNonConvergenceRepeatedRootCauseCycleThreshold),
	}
	cluster.Fingerprint = reviewNonConvergenceFingerprint(cluster)
	cluster.Summary = buildReviewClusterSummary(cluster)
	return cluster
}

func reviewNonConvergenceFingerprint(cluster reviewConvergenceCluster) string {
	parts := append([]string{}, cluster.PackageClusters...)
	parts = append(parts, cluster.RootCauseTerms...)
	sort.Strings(parts)
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

func extractReviewCycleNumber(body string) int {
	match := reviewCycleRE.FindStringSubmatch(body)
	if len(match) != 2 {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

func extractReviewAggregationCounts(body string) (int, int, int) {
	var raw, after, stale int
	for _, line := range strings.Split(body, "\n") {
		match := reviewAggregationRE.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 3 {
			continue
		}
		n, _ := strconv.Atoi(match[2])
		switch strings.ToLower(match[1]) {
		case "raw findings before dedupe":
			raw = n
		case "findings after dedupe":
			after = n
		case "stale pr-state findings ignored":
			stale = n
		}
	}
	return raw, after, stale
}

func extractReviewFixIssueNumbers(body string) []int {
	seen := map[int]struct{}{}
	for _, match := range reviewFixIssueRE.FindAllStringSubmatch(body, -1) {
		if len(match) != 2 {
			continue
		}
		n, _ := strconv.Atoi(match[1])
		if n > 0 {
			seen[n] = struct{}{}
		}
	}
	var nums []int
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums
}

func extractReviewFindingsBySeverity(body string) map[string][]string {
	out := map[string][]string{}
	currentSeverity := ""
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		upper := strings.ToUpper(line)
		for _, severity := range []string{"HIGH", "MEDIUM", "LOW", "CRITERIA"} {
			if strings.HasPrefix(upper, "**"+severity+"**") {
				currentSeverity = severity
			}
		}
		if match := reviewNumberFindingRE.FindStringSubmatch(line); len(match) == 3 {
			addReviewFinding(out, match[1], match[2])
			continue
		}
		if match := reviewDirectFindingRE.FindStringSubmatch(line); len(match) == 3 {
			severity := strings.ToUpper(match[1])
			text := strings.TrimSpace(match[2])
			if text != "" {
				addReviewFinding(out, severity, text)
			}
			currentSeverity = severity
			continue
		}
		if currentSeverity != "" && strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "- Raw findings") && !strings.HasPrefix(line, "- Findings after") && !strings.HasPrefix(line, "- Stale PR-state") {
			addReviewFinding(out, currentSeverity, strings.TrimPrefix(line, "- "))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractVisiblePostedFindingCount(body string, findings map[string][]string) int {
	if match := reviewFoundCountRE.FindStringSubmatch(body); len(match) == 2 {
		n, _ := strconv.Atoi(match[1])
		return n
	}
	total := 0
	for _, list := range findings {
		total += len(list)
	}
	return total
}

func extractReviewCoverageSummary(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.Contains(line, "## Review Aggregation") || strings.Contains(line, "## Diff Coverage") || strings.Contains(strings.ToLower(line), "chunked") && strings.Contains(strings.ToLower(line), "coverage") {
			end := len(lines)
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "## ") && j > i+1 {
					end = j
					break
				}
				if strings.HasPrefix(lines[j], reviewResultMarkerPrefix) {
					end = j
					break
				}
			}
			return strings.TrimSpace(strings.Join(lines[i:end], "\n"))
		}
	}
	return ""
}

func bodyHasWorkerReport(body string) bool {
	return strings.Contains(body, "Worker Report") || strings.Contains(body, "Validation") || strings.Contains(body, "## Summary")
}

func extractReviewValidationStatus(body string) string {
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "validation") && (strings.Contains(lower, "success") || strings.Contains(lower, "passed")):
		return "success"
	case strings.Contains(lower, "validation") && (strings.Contains(lower, "fail") || strings.Contains(lower, "error")):
		return "failed"
	default:
		return ""
	}
}

func extractReviewFixFilesSummary(body string, parsedFiles []string) []string {
	seen := map[string]struct{}{}
	for _, file := range parsedFiles {
		if file != "" {
			seen[file] = struct{}{}
		}
	}
	for _, match := range reviewPathRE.FindAllStringSubmatch(body, -1) {
		if len(match) < 2 {
			continue
		}
		file := strings.Trim(match[1], "`.,);:")
		if looksLikeReviewFilePath(file) {
			seen[file] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

func isSuccessfulWorkerReport(fix reviewHistoryFixIssue) bool {
	return fix.WorkerReport && fix.ValidationStatus == "success"
}

func countCompletedReviewCycles(cycles []reviewHistoryCycle) int {
	completed := 0
	for _, cycle := range cycles {
		for _, fix := range cycle.FixIssues {
			if fix.StatusLabel == issues.StatusDone || isSuccessfulWorkerReport(fix) {
				completed++
				break
			}
		}
	}
	return completed
}

func reviewFindingCount(cycle reviewHistoryCycle) int {
	if cycle.FindingsAfterDedupe > 0 {
		return cycle.FindingsAfterDedupe
	}
	if cycle.PostedFindingsCount > 0 {
		return cycle.PostedFindingsCount
	}
	total := 0
	for _, findings := range cycle.FindingsBySeverity {
		total += len(findings)
	}
	return total
}

func normalizeReviewPackagePath(raw string) string {
	cleaned := strings.Trim(raw, "`.,);:")
	cleaned = strings.TrimPrefix(cleaned, "./")
	if cleaned == "" || strings.Contains(cleaned, "://") || !strings.Contains(cleaned, "/") {
		return ""
	}
	dir := cleaned
	if looksLikeReviewFilePath(cleaned) {
		dir = path.Dir(cleaned)
	}
	if dir == "." || dir == "/" || dir == "" {
		return ""
	}
	parts := strings.Split(dir, "/")
	if parts[0] == "internal" && len(parts) >= 3 {
		return strings.Join(parts[:3], "/")
	}
	return dir
}

func looksLikeReviewFilePath(pathValue string) bool {
	base := path.Base(pathValue)
	return strings.Contains(base, ".")
}

func allReviewFindingDescriptions(cycle reviewHistoryCycle) []string {
	var out []string
	for _, severity := range []string{"HIGH", "MEDIUM", "LOW", "CRITERIA"} {
		out = append(out, cycle.FindingsBySeverity[severity]...)
	}
	return out
}

func addReviewFinding(out map[string][]string, severity, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	out[strings.ToUpper(severity)] = append(out[strings.ToUpper(severity)], text)
}

func qualifyingReviewClusterKeys(index map[string]*reviewClusterCounts, findingThreshold, cycleThreshold int) []string {
	keys := make([]string, 0, len(index))
	for key, count := range index {
		if count.findings >= findingThreshold || len(count.cycles) >= cycleThreshold {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func addReviewClusterCount(index map[string]*reviewClusterCounts, key string, cycle int) {
	if key == "" {
		return
	}
	if index[key] == nil {
		index[key] = &reviewClusterCounts{cycles: map[int]struct{}{}}
	}
	index[key].findings++
	index[key].cycles[cycle] = struct{}{}
}

func buildReviewClusterSummary(cluster reviewConvergenceCluster) string {
	var parts []string
	if len(cluster.PackageClusters) > 0 {
		parts = append(parts, "packages: "+strings.Join(cluster.PackageClusters, ", "))
	}
	if len(cluster.RootCauseTerms) > 0 {
		parts = append(parts, "root causes: "+strings.Join(cluster.RootCauseTerms, ", "))
	}
	return strings.Join(parts, "; ")
}

func intSliceContains(nums []int, n int) bool {
	for _, num := range nums {
		if num == n {
			return true
		}
	}
	return false
}

func appendUniqueInt(nums []int, n int) []int {
	if n == 0 || intSliceContains(nums, n) {
		return nums
	}
	nums = append(nums, n)
	sort.Ints(nums)
	return nums
}
