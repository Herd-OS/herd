package integrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/agent"
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
	Decision               reviewConvergenceDecision
	Confidence             float64
	Rationale              string
	Cycles                 []reviewHistoryCycle
	TrendCounts            []int
	CompletedFixIssues     []int
	InProgressFixIssues    []int
	PriorStrategyFixIssues []reviewPriorStrategyFixIssue
	Cluster                reviewConvergenceCluster
	LatestFindingCount     int
	EarliestFindingCount   int
}

type reviewPriorStrategyFixIssue struct {
	Number           int
	Cycle            int
	StatusLabel      string
	State            string
	Title            string
	Fingerprint      string
	HeadSHA          string
	ValidationStatus string
	WorkerReport     bool
	Summary          string
}

const reviewNonConvergenceMinLatestFindings = 8
const reviewNonConvergenceRequireIncreasingOrFlat = true
const reviewNonConvergenceRepeatedSubsystemFindingThreshold = 3
const reviewNonConvergenceRepeatedSubsystemCycleThreshold = 2
const reviewNonConvergenceRepeatedRootCauseFindingThreshold = 3
const reviewNonConvergenceRepeatedRootCauseCycleThreshold = 2
const reviewNonConvergenceFingerprintMarkerPrefix = "<!-- herd:review-nonconvergence "
const reviewNonConvergenceFingerprintMarkerSuffix = " -->"

var reviewSynthesisTimeout = 90 * time.Second
var reviewVerificationTimeout = 45 * time.Second

const reviewVerificationMinConfidence = 0.90

var (
	reviewCycleRE          = regexp.MustCompile(`(?i)\bcycle\s+(\d+)\b`)
	reviewAggregationRE    = regexp.MustCompile(`(?i)^-\s*(Raw findings before dedupe|Findings after dedupe|Stale PR-state findings ignored):\s*(\d+)\s*$`)
	reviewFoundCountRE     = regexp.MustCompile(`(?i)\bFound\s+(\d+)\s+issues?\b`)
	reviewFixIssueRE       = regexp.MustCompile(`(?i)(?:fix\s+#|fix worker dispatched\s*(?:→|->)\s*#|created strategy-level fix issue\s*#)(\d+)`)
	reviewDirectFindingRE  = regexp.MustCompile(`^\s*(?:[-*]|\d+\.)\s+\*\*(?:\[)?(HIGH|MEDIUM|LOW|CRITERIA)(?:\])?\*\*:?\s*(.*)$`)
	reviewNumberFindingRE  = regexp.MustCompile(`^\s*\d+\.\s+\*\*\[(HIGH|MEDIUM|LOW|CRITERIA)\]\*\*:?\s*(.*)$`)
	reviewPathRE           = regexp.MustCompile(`(?:^|[\s(\[` + "`" + `])([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+)(?::\d+)?`)
	reviewRootCauseSplitRE = regexp.MustCompile(`(?i)\b(Suggested fix|Tests|Constraints)\s*:`)
	reviewBareFractionRE   = regexp.MustCompile(`^[0-9]+/[0-9]+$`)
	reviewChunkLabelRE     = regexp.MustCompile(`(?i)\bchunks?(?:\s+reviewed)?\s*:?\s*[0-9]+/[0-9]+\b`)
	reviewBareChunkRE      = regexp.MustCompile(`(?i)^chunk\s+[0-9]+$`)
)

type reviewSynthesisDecision string

const (
	reviewSynthesisDecisionEscalate reviewSynthesisDecision = "escalate"
	reviewSynthesisDecisionFallback reviewSynthesisDecision = "fallback"
)

type reviewNonConvergenceFingerprintMarker struct {
	Version     int    `json:"version"`
	BatchPR     int    `json:"batch_pr"`
	Fingerprint string `json:"fingerprint"`
	HeadSHA     string `json:"head_sha,omitempty"`
}

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

func parseReviewHistoryCycle(comment *platform.Comment, prNumber int, batchNumber int, _ string, trustedHumanLogins ...string) (reviewHistoryCycle, bool) {
	if !isTrustedReviewResultMarkerComment(comment, trustedHumanLogins...) {
		return reviewHistoryCycle{}, false
	}
	body := comment.Body
	marker, hasMarker := parseReviewResultMarker(body)
	if hasMarker {
		if marker.PRNumber != prNumber || marker.BatchNumber != batchNumber {
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

	latestFixCycle, hasFixCycle := latestFixBearingReviewCycle(cycles)
	for _, cycle := range cycles {
		analysis.TrendCounts = append(analysis.TrendCounts, reviewFindingCount(cycle))
		for _, fix := range cycle.FixIssues {
			switch fix.StatusLabel {
			case issues.StatusDone:
				analysis.CompletedFixIssues = appendUniqueInt(analysis.CompletedFixIssues, fix.Number)
			case issues.StatusInProgress, issues.StatusReady:
				if hasFixCycle && cycle.Cycle == latestFixCycle {
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
	repeatedSubsystem := len(analysis.Cluster.PackageClusters) > 0
	repeatedRootCause := len(analysis.Cluster.RootCauseTerms) > 0
	if reviewNonConvergenceRequireIncreasingOrFlat && !trendIncreasingOrFlat && !repeatedRootCause {
		analysis.Rationale = "finding trend is decreasing and no persistent root-cause cluster met deterministic thresholds"
		return analysis
	}

	if repeatedSubsystem || repeatedRootCause {
		var eligible bool
		analysis, eligible = evaluateReviewNonConvergenceEligibility(analysis)
		if !eligible {
			return analysis
		}
		analysis.Decision = reviewDecisionEscalateToArchitectureFix
		analysis.Confidence = 0.86
		if repeatedRootCause && findingTrendTemporarilyDecreased(analysis.TrendCounts) {
			analysis.Rationale = "persistent root-cause cluster survived completed fix cycles despite a temporary finding-count decrease"
			return analysis
		}
		analysis.Rationale = "finding trend is increasing or flat after completed fix cycles and repeated subsystem/root-cause clusters were detected"
		return analysis
	}
	analysis.Rationale = "no repeated subsystem or root-cause cluster met deterministic thresholds"
	return analysis
}

func evaluateReviewNonConvergenceEligibility(analysis reviewConvergenceAnalysis) (reviewConvergenceAnalysis, bool) {
	analysis.Decision = reviewDecisionContinueFixLoop

	packages := concreteReviewPackageClusters(analysis.Cluster.PackageClusters)
	if len(packages) == 0 {
		analysis.Cluster.PackageClusters = nil
		analysis.Rationale = "non-convergence escalation skipped: no concrete dominant package clusters"
		return analysis, false
	}
	analysis.Cluster.PackageClusters = packages

	if !representativeFindingsShareConcretePackageCluster(analysis.Cycles, packages) {
		latestFindings := reviewActionableFindingDescriptions(analysis.Cycles[len(analysis.Cycles)-1])
		if len(latestFindings) == 1 {
			analysis.Rationale = "non-convergence escalation skipped: single finding without recurring concrete cluster"
		} else {
			analysis.Rationale = "non-convergence escalation skipped: latest findings do not share a recurring concrete cluster"
		}
		return analysis, false
	}

	terms := concreteReviewRootCauseTerms(analysis.Cluster.RootCauseTerms)
	if len(terms) == 0 {
		analysis.Cluster.RootCauseTerms = nil
		analysis.Rationale = "non-convergence escalation skipped: generic-only root-cause terms"
		return analysis, false
	}
	analysis.Cluster.RootCauseTerms = terms
	analysis.Cluster.Fingerprint = reviewNonConvergenceFingerprint(analysis.Cluster)
	analysis.Cluster.Summary = buildReviewClusterSummary(analysis.Cluster)
	return analysis, true
}

func concreteReviewPackageClusters(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		cluster := normalizeReviewPackagePath(value)
		if cluster == "" {
			cluster = normalizeReviewClusterTitleTerm(value)
		}
		if isConcreteReviewPackageCluster(cluster) {
			seen[cluster] = struct{}{}
		}
	}
	return sortedStringSet(seen)
}

func concreteReviewRootCauseTerms(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		term := normalizeReviewRootCauseTerm(value)
		if term == "" || isGenericReviewRootCauseTerm(term) {
			continue
		}
		seen[term] = struct{}{}
	}
	return sortedStringSet(seen)
}

func representativeFindingsShareConcretePackageCluster(cycles []reviewHistoryCycle, clusters []string) bool {
	if len(cycles) == 0 || len(clusters) == 0 {
		return false
	}
	clusterSet := map[string]struct{}{}
	for _, cluster := range concreteReviewPackageClusters(clusters) {
		clusterSet[cluster] = struct{}{}
	}
	if len(clusterSet) == 0 {
		return false
	}
	latestFindings := reviewActionableFindingDescriptions(cycles[len(cycles)-1])
	for _, finding := range latestFindings {
		cluster := packageClusterFromFinding(finding)
		if !isConcreteReviewPackageCluster(cluster) {
			continue
		}
		if _, ok := clusterSet[cluster]; ok {
			return true
		}
	}
	return false
}

func reviewActionableFindingDescriptions(cycle reviewHistoryCycle) []string {
	var out []string
	for _, finding := range allReviewFindingDescriptions(cycle) {
		finding = strings.TrimSpace(finding)
		if finding == "" || isReviewFindingMetadataNoise(finding) {
			continue
		}
		out = append(out, finding)
	}
	return out
}

func isGenericReviewRootCauseTerm(term string) bool {
	switch normalizeReviewRootCauseTerm(term) {
	case "durable", "idempotency", "mutation", "workflow", "retry", "repair", "side effect", "github-visible", "started", "unknown", "dispatch":
		return true
	default:
		return false
	}
}

func isConcreteReviewPackageCluster(value string) bool {
	value = normalizeReviewClusterTitleTerm(value)
	if value == "" || strings.EqualFold(value, "none") || isReviewClusterNoise(value) {
		return false
	}
	switch strings.ToLower(strings.Trim(value, "/")) {
	case "internal", "cmd", "docs", "doc", "test", "tests", "pkg", "src", "lib", "scripts", "tools":
		return false
	}
	return true
}

func findingTrendTemporarilyDecreased(counts []int) bool {
	for i := 1; i < len(counts); i++ {
		if counts[i] < counts[i-1] {
			return true
		}
	}
	return false
}

func latestFixBearingReviewCycle(cycles []reviewHistoryCycle) (int, bool) {
	latest := 0
	for _, cycle := range cycles {
		if len(cycle.FixIssues) == 0 {
			continue
		}
		if cycle.Cycle > latest {
			latest = cycle.Cycle
		}
	}
	return latest, latest > 0
}

func packageClusterFromFinding(description string) string {
	for _, match := range reviewPathRE.FindAllStringSubmatch(description, -1) {
		if len(match) < 2 {
			continue
		}
		token := strings.Trim(match[1], "`.,);:")
		if isReviewClusterNoise(token) {
			continue
		}
		cluster := normalizeReviewPackagePath(token)
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

	seen := map[string]struct{}{}
	for _, term := range reviewRootCauseVocabulary {
		if strings.Contains(normalized, term) {
			if normalizedTerm := normalizeReviewRootCauseTerm(term); normalizedTerm != "" {
				seen[normalizedTerm] = struct{}{}
			}
		}
	}
	return sortedStringSet(seen)
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

func buildStrategyFixIssueTitle(nextCycle int, cluster reviewConvergenceCluster) string {
	summary := reviewStrategyTitleSuffix(cluster)
	return truncateReviewStrategyTitle(fmt.Sprintf("Review strategy fix (cycle %d): %s", nextCycle, summary), 120)
}

func buildStrategyFixIssueBody(ms *platform.Milestone, pr *platform.PullRequest, nextCycle int, analysis reviewConvergenceAnalysis) string {
	analysis.Cluster.PackageClusters = concreteReviewPackageClusters(analysis.Cluster.PackageClusters)
	analysis.Cluster.RootCauseTerms = concreteReviewRootCauseTerms(analysis.Cluster.RootCauseTerms)
	batchNumber := 0
	if ms != nil {
		batchNumber = ms.Number
	}
	prNumber := 0
	if pr != nil {
		prNumber = pr.Number
	}

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    batchNumber,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  prNumber,
			Scope:    cleanReviewClusterValues(analysis.Cluster.PackageClusters),
		},
		Task:                  "Solve the shared architecture/design problem causing the non-converging review loop, then migrate the relevant paths to that strategy. Do not process each endpoint-level finding independently; use the clustered findings as symptoms of the same underlying invariant or state-transition failure.",
		ImplementationDetails: buildDeterministicStrategyImplementationDetails(analysis),
		Criteria: []string{
			"Architecture-level abstraction or invariant is documented in code.",
			"Clustered packages are migrated to the strategy or explicitly justified if a package is left unchanged.",
			"Idempotency and repair behavior is covered by regression tests.",
			"No duplicate endpoint-level loop behavior is introduced.",
			"Relevant package tests are run and reported.",
		},
		Context: buildStrategyFixIssueContext(analysis),
	})
	return appendReviewNonConvergenceFingerprint(body, analysis.Cluster.Fingerprint)
}

func buildReviewSynthesisInput(pr *platform.PullRequest, ms *platform.Milestone, currentHeadSHA string, prComments []*platform.Comment, history []reviewHistoryCycle, allIssues []*platform.Issue, workerNoOpVerdicts []string, currentPRMetadata string) agent.ReviewSynthesisInput {
	input := agent.ReviewSynthesisInput{
		HeadSHA:            currentHeadSHA,
		CurrentPRMetadata:  currentPRMetadata,
		WorkerNoOpVerdicts: append([]string(nil), workerNoOpVerdicts...),
	}
	if pr != nil {
		input.PRNumber = pr.Number
		input.HeadRef = pr.Head
	}
	if ms != nil {
		input.BatchNumber = ms.Number
	}
	for _, comment := range prComments {
		if comment == nil || strings.TrimSpace(comment.Body) == "" {
			continue
		}
		input.RecentReviewComments = append(input.RecentReviewComments, comment.Body)
	}

	affectedFiles := map[string]struct{}{}
	for _, cycle := range history {
		synthCycle := agent.ReviewSynthesisCycle{
			Cycle:                cycle.Cycle,
			HeadSHA:              cycle.HeadSHA,
			FindingsAfterDedupe:  reviewFindingCount(cycle),
			FindingsBySeverity:   copyReviewFindingsBySeverity(cycle.FindingsBySeverity),
			FixIssueNumbers:      append([]int(nil), cycle.FixIssueNumbers...),
			Status:               cycle.Status,
			ChunkCoverageSummary: cycle.ChunkCoverageSummary,
		}
		for _, finding := range allReviewFindingDescriptions(cycle) {
			for _, file := range reviewFilePathsFromText(finding) {
				affectedFiles[file] = struct{}{}
				synthCycle.AffectedFiles = appendUniqueString(synthCycle.AffectedFiles, file)
			}
		}
		sort.Strings(synthCycle.AffectedFiles)
		input.Cycles = append(input.Cycles, synthCycle)
		for findingIndex, finding := range sanitizedDistinctReviewFindings(cycle) {
			input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
				ID:      fmt.Sprintf("cycle:%d:finding:%d", cycle.Cycle, findingIndex),
				Kind:    "review_finding",
				Cycle:   cycle.Cycle,
				HeadSHA: cycle.HeadSHA,
				Excerpt: boundedReviewEvidenceExcerpt(finding),
			})
		}
	}

	fixByCycle := map[int][]agent.ReviewSynthesisFixIssue{}
	for _, issue := range allIssues {
		if issue == nil {
			continue
		}
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		fm := parsed.FrontMatter
		if fm.Type != "fix" {
			if ms == nil || fm.Batch != ms.Number || fm.Type != "" {
				continue
			}
			if strings.TrimSpace(parsed.Task) == "" && strings.TrimSpace(parsed.ImplementationDetails) == "" &&
				len(parsed.Criteria) == 0 && strings.TrimSpace(parsed.Context) == "" {
				continue
			}
			input.OriginalRequirements = append(input.OriginalRequirements, agent.ReviewSynthesisRequirement{
				IssueNumber:           issue.Number,
				Title:                 issue.Title,
				Task:                  parsed.Task,
				ImplementationDetails: parsed.ImplementationDetails,
				AcceptanceCriteria:    append([]string(nil), parsed.Criteria...),
				Context:               parsed.Context,
			})
			requirementSources := []struct {
				id, kind, text string
			}{
				{fmt.Sprintf("issue:%d:task", issue.Number), "requirement_task", parsed.Task},
				{fmt.Sprintf("issue:%d:implementation", issue.Number), "requirement_implementation", parsed.ImplementationDetails},
				{fmt.Sprintf("issue:%d:context", issue.Number), "requirement_context", parsed.Context},
			}
			for criterionIndex, criterion := range parsed.Criteria {
				requirementSources = append(requirementSources, struct{ id, kind, text string }{
					fmt.Sprintf("issue:%d:criterion:%d", issue.Number, criterionIndex), "requirement_criterion", criterion,
				})
			}
			for _, source := range requirementSources {
				if strings.TrimSpace(source.text) != "" {
					input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
						ID: source.id, Kind: source.kind, Excerpt: boundedReviewEvidenceExcerpt(source.text),
					})
				}
			}
			continue
		}
		if fm.BatchPR != input.PRNumber || fm.FixCycle <= 0 || fm.CIFixCycle > 0 || fm.ConflictResolution {
			continue
		}
		if issues.HasLabel(issue.Labels, issues.ReviewNonConverging) && strings.HasPrefix(issue.Title, "Review strategy fix") {
			marker, ok := parseReviewNonConvergenceFingerprintMarker(issue.Body)
			if !ok {
				continue
			}
			input.PriorStrategyFixIssues = append(input.PriorStrategyFixIssues, agent.ReviewSynthesisStrategyFixIssue{
				Number: issue.Number, Cycle: fm.FixCycle, Title: issue.Title,
				StatusLabel: issues.StatusLabel(issue.Labels), State: issue.State,
				Fingerprint: marker.Fingerprint, HeadSHA: marker.HeadSHA,
				Summary: extractReviewPriorStrategySummary(parsed),
			})
			continue
		}
		status := issues.StatusLabel(issue.Labels)
		if status != issues.StatusDone && !bodyHasWorkerReport(issue.Body) {
			continue
		}
		files := extractReviewFixFilesSummary(issue.Body, parsed.FilesToModify)
		for _, file := range files {
			affectedFiles[file] = struct{}{}
		}
		fix := agent.ReviewSynthesisFixIssue{
			Number:           issue.Number,
			Title:            issue.Title,
			Body:             issue.Body,
			StatusLabel:      status,
			FilesSummary:     files,
			ValidationStatus: extractReviewValidationStatus(issue.Body),
			WorkerReport:     bodyHasWorkerReport(issue.Body),
		}
		input.CompletedFixIssues = append(input.CompletedFixIssues, fix)
		fixByCycle[fm.FixCycle] = append(fixByCycle[fm.FixCycle], fix)
	}
	sort.SliceStable(input.OriginalRequirements, func(i, j int) bool {
		return input.OriginalRequirements[i].IssueNumber < input.OriginalRequirements[j].IssueNumber
	})
	sort.SliceStable(input.PriorStrategyFixIssues, func(i, j int) bool {
		if input.PriorStrategyFixIssues[i].Cycle != input.PriorStrategyFixIssues[j].Cycle {
			return input.PriorStrategyFixIssues[i].Cycle < input.PriorStrategyFixIssues[j].Cycle
		}
		return input.PriorStrategyFixIssues[i].Number < input.PriorStrategyFixIssues[j].Number
	})
	sort.SliceStable(input.CompletedFixIssues, func(i, j int) bool {
		return input.CompletedFixIssues[i].Number < input.CompletedFixIssues[j].Number
	})
	for i := range input.Cycles {
		input.Cycles[i].CompletedFixIssues = append([]agent.ReviewSynthesisFixIssue(nil), fixByCycle[input.Cycles[i].Cycle]...)
		sort.SliceStable(input.Cycles[i].CompletedFixIssues, func(a, b int) bool {
			return input.Cycles[i].CompletedFixIssues[a].Number < input.Cycles[i].CompletedFixIssues[b].Number
		})
	}
	input.AffectedFiles = sortedStringSet(affectedFiles)
	sort.SliceStable(input.EvidenceSources, func(i, j int) bool {
		leftRequirement := input.EvidenceSources[i].Cycle == 0
		rightRequirement := input.EvidenceSources[j].Cycle == 0
		if leftRequirement != rightRequirement {
			return leftRequirement
		}
		return input.EvidenceSources[i].ID < input.EvidenceSources[j].ID
	})
	return input
}

const reviewEvidenceExcerptBudget = 4096

func boundedReviewEvidenceExcerpt(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= reviewEvidenceExcerptBudget {
		return value
	}
	const marker = "\n[TRUNCATED: deterministic review evidence budget reached]\n"
	keep := reviewEvidenceExcerptBudget - len(marker)
	for keep > 0 && !utf8.RuneStart(value[keep]) {
		keep--
	}
	return value[:keep] + marker
}

func evaluateReviewSynthesis(result *agent.ReviewSynthesisResult, minConfidence float64, analysis reviewConvergenceAnalysis) (reviewSynthesisDecision, string) {
	if result == nil {
		return reviewSynthesisDecisionFallback, "synthesis returned nil result"
	}
	if !result.ShouldEscalate {
		return reviewSynthesisDecisionFallback, fallbackString(result.Reason, "synthesis chose not to escalate")
	}
	if result.Confidence < minConfidence {
		return reviewSynthesisDecisionFallback, fmt.Sprintf("synthesis confidence %.2f below threshold %.2f", result.Confidence, minConfidence)
	}
	sanitizedSymptoms := sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms)
	if len(sanitizedSymptoms) < 2 {
		return reviewSynthesisDecisionFallback, fmt.Sprintf("synthesis reported %d sanitized recurring symptom(s); need at least 2", len(sanitizedSymptoms))
	}
	if countReviewSynthesisSymptomCycles(sanitizedSymptoms) < 2 && len(analysis.CompletedFixIssues) < 2 {
		return reviewSynthesisDecisionFallback, "synthesis symptoms do not span two cycles and fewer than two completed fix attempts exist"
	}
	if strings.TrimSpace(result.RootCauseTitle) == "" {
		return reviewSynthesisDecisionFallback, "synthesis root cause title is empty"
	}
	if sanitizedReviewSynthesisRootCauseTitle(result.RootCauseTitle) == "" {
		return reviewSynthesisDecisionFallback, "synthesis root cause has unsafe/noisy title"
	}
	if len(trimBlankStrings(result.AcceptanceCriteria)) == 0 {
		return reviewSynthesisDecisionFallback, "synthesis acceptance criteria are empty"
	}
	if !hasValidReviewSynthesisSymptomDescription(sanitizedSymptoms) {
		return reviewSynthesisDecisionFallback, "synthesis recurring symptom descriptions are unsafe/noisy metadata"
	}
	if !hasValidReviewSynthesisAffectedFile(sanitizedSymptoms) {
		return reviewSynthesisDecisionFallback, "synthesis recurring symptom affected files are empty or unsafe/noisy"
	}
	if synthesizedReviewStrategyFingerprint(result) == "" {
		return reviewSynthesisDecisionFallback, "synthesis fingerprint is empty"
	}
	if ok, reason := validateReviewRequirementReinterpretation(result); !ok {
		return reviewSynthesisDecisionFallback, reason
	}
	return reviewSynthesisDecisionEscalate, "synthesis passed safety gates"
}

func synthesizedReviewStrategyFingerprint(result *agent.ReviewSynthesisResult) string {
	if result == nil {
		return ""
	}
	root := normalizeReviewSynthesisFingerprintText(sanitizedReviewSynthesisRootCauseTitle(result.RootCauseTitle))
	if root == "" {
		return ""
	}
	var symptomEntries []string
	for _, symptom := range sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms) {
		description := normalizeReviewSynthesisFingerprintText(symptom.Description)
		if description == "" {
			continue
		}
		files := normalizeReviewSynthesisFiles(symptom.AffectedFiles)
		if len(files) == 0 {
			continue
		}
		cycles := uniqueSortedInts(symptom.Cycles)
		symptomEntries = append(symptomEntries, fmt.Sprintf("%s|cycles:%s|files:%s", description, formatIntList(cycles), strings.Join(files, ",")))
	}
	sort.Strings(symptomEntries)
	if len(symptomEntries) == 0 {
		return ""
	}
	parts := []string{root, strings.Join(symptomEntries, "\n")}
	if reinterpretation := result.RequirementReinterpretation; reinterpretation != nil {
		parts = append(parts,
			normalizeReviewSynthesisFingerprintText(string(reinterpretation.ConstraintKind)),
			normalizeReviewSynthesisFingerprintText(reinterpretation.ConflictingRequirement),
			normalizeReviewSynthesisFingerprintText(reinterpretation.PlatformConsistencyConstraint),
			normalizeReviewSynthesisFingerprintText(reinterpretation.PreservedSafetyProperty),
			normalizeReviewSynthesisFingerprintText(reinterpretation.CorrectedInvariant),
			strings.Join(normalizedSortedReviewStrings(reinterpretation.LinearizationBoundaries), ","),
			strings.Join(normalizedSortedReviewStrings(reinterpretation.DurabilityBoundaries), ","),
		)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

func buildSynthesizedStrategyFixIssueTitle(nextCycle int, result *agent.ReviewSynthesisResult) string {
	title := "repeated review findings"
	if result != nil {
		if sanitized := sanitizedReviewSynthesisRootCauseTitle(result.RootCauseTitle); sanitized != "" {
			title = sanitized
		}
	}
	return truncateReviewStrategyTitle(fmt.Sprintf("Review strategy fix (cycle %d): %s", nextCycle, title), 120)
}

func buildSynthesizedStrategyFixIssueBody(ms *platform.Milestone, pr *platform.PullRequest, nextCycle int, result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput, fingerprint string, prior []reviewPriorStrategyFixIssue) string {
	batchNumber := 0
	if ms != nil {
		batchNumber = ms.Number
	}
	prNumber := 0
	if pr != nil {
		prNumber = pr.Number
	}
	scope := synthesizedReviewAffectedScope(result)
	if len(scope) == 0 {
		scope = sanitizedReviewSynthesisFilePaths(input.AffectedFiles)
	}

	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    batchNumber,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  prNumber,
			Scope:    scope,
		},
		Task:                  "Implement a synthesized architectural/root-cause fix for the review/fix loop. This is not a normal individual review finding: fix the shared abstraction or root cause first, then verify the recurring symptoms below are covered by that strategy.",
		ImplementationDetails: buildSynthesizedStrategyImplementationDetails(result, input, prior),
		Criteria:              trimBlankStrings(result.AcceptanceCriteria),
		Context:               buildSynthesizedStrategyContext(input, prior),
	})
	return appendReviewNonConvergenceFingerprintWithHeadSHA(body, fingerprint, input.HeadSHA)
}

func buildSynthesizedReviewNonConvergencePRComment(result *agent.ReviewSynthesisResult, strategyIssueNumber int) string {
	var b strings.Builder
	b.WriteString("⚠️ **Herd review is not converging**\n\n")
	fmt.Fprintf(&b, "- Synthesized root cause: %s\n", fallbackString(strings.TrimSpace(result.RootCauseTitle), "unspecified"))
	fmt.Fprintf(&b, "- Confidence: %.2f\n", result.Confidence)
	fmt.Fprintf(&b, "- Recurring symptoms: %d\n", len(result.RecurringSymptoms))
	fmt.Fprintf(&b, "- Strategy fix issue: #%d\n", strategyIssueNumber)
	return b.String()
}

func buildReviewNonConvergencePRComment(analysis reviewConvergenceAnalysis, strategyIssueNumber int) string {
	analysis.Cluster.PackageClusters = concreteReviewPackageClusters(analysis.Cluster.PackageClusters)
	analysis.Cluster.RootCauseTerms = concreteReviewRootCauseTerms(analysis.Cluster.RootCauseTerms)
	fixIssues := append([]int{}, analysis.CompletedFixIssues...)
	fixIssues = append(fixIssues, analysis.InProgressFixIssues...)
	sort.Ints(fixIssues)

	var b strings.Builder
	b.WriteString("⚠️ **Herd review is not converging**\n\n")
	fmt.Fprintf(&b, "- Cycles analyzed: %s\n", formatReviewCycleNumbers(analysis.Cycles))
	fmt.Fprintf(&b, "- Finding count trend: %s\n", formatIntList(analysis.TrendCounts))
	fmt.Fprintf(&b, "- Fix issues considered: %s\n", formatIssueNumberList(fixIssues))
	if packages := concreteReviewPackageClusters(analysis.Cluster.PackageClusters); len(packages) > 0 {
		fmt.Fprintf(&b, "- Dominant package clusters: %s\n", formatStringList(packages))
	}
	fmt.Fprintf(&b, "- Dominant root-cause terms: %s\n", formatStringList(cleanReviewClusterValues(analysis.Cluster.RootCauseTerms)))
	fmt.Fprintf(&b, "- Escalation reason: %s\n", fallbackString(analysis.Rationale, "repeated review/fix cycles share the same cluster fingerprint"))
	fmt.Fprintf(&b, "- Strategy fix issue: #%d\n", strategyIssueNumber)
	return b.String()
}

func appendReviewNonConvergenceFingerprint(body string, fingerprint string) string {
	return appendReviewNonConvergenceFingerprintWithHeadSHA(body, fingerprint, "")
}

func appendReviewNonConvergenceFingerprintWithHeadSHA(body string, fingerprint string, headSHA string) string {
	payload := reviewNonConvergenceFingerprintMarker{
		Version:     1,
		BatchPR:     parseReviewFingerprintBatchPR(body),
		Fingerprint: fingerprint,
		HeadSHA:     strings.TrimSpace(headSHA),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimRight(body, "\n") + "\n"
	}
	return strings.TrimRight(body, "\n") + "\n\n" + reviewNonConvergenceFingerprintMarkerPrefix + string(encoded) + reviewNonConvergenceFingerprintMarkerSuffix + "\n"
}

func parseReviewNonConvergenceFingerprint(body string) (string, bool) {
	marker, ok := parseReviewNonConvergenceFingerprintMarker(body)
	if !ok {
		return "", false
	}
	return marker.Fingerprint, true
}

func parseReviewNonConvergenceFingerprintMarker(body string) (reviewNonConvergenceFingerprintMarker, bool) {
	start := strings.Index(body, reviewNonConvergenceFingerprintMarkerPrefix)
	if start < 0 {
		return reviewNonConvergenceFingerprintMarker{}, false
	}
	payloadStart := start + len(reviewNonConvergenceFingerprintMarkerPrefix)
	end := strings.Index(body[payloadStart:], reviewNonConvergenceFingerprintMarkerSuffix)
	if end < 0 {
		return reviewNonConvergenceFingerprintMarker{}, false
	}
	payload := body[payloadStart : payloadStart+end]
	var marker reviewNonConvergenceFingerprintMarker
	if err := json.Unmarshal([]byte(payload), &marker); err != nil || marker.Fingerprint == "" {
		return reviewNonConvergenceFingerprintMarker{}, false
	}
	return marker, true
}

func findDuplicateStrategyFixIssue(allIssues []*platform.Issue, prNumber int, fingerprint string) (*platform.Issue, bool) {
	if fingerprint == "" {
		return nil, false
	}
	for _, issue := range allIssues {
		if issue == nil || !isActiveReviewStrategyIssue(issue) {
			continue
		}
		if !issues.HasLabel(issue.Labels, issues.ReviewNonConverging) || !strings.HasPrefix(issue.Title, "Review strategy fix") {
			continue
		}
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		if parsed.FrontMatter.Type != "fix" || parsed.FrontMatter.BatchPR != prNumber {
			continue
		}
		if parsedFingerprint, ok := parseReviewNonConvergenceFingerprint(issue.Body); ok && parsedFingerprint == fingerprint {
			return issue, true
		}
		if strings.Contains(issue.Body, fingerprint) {
			return issue, true
		}
	}
	return nil, false
}

//nolint:unparam // currentHeadSHA is retained for the existing duplicate helper signature; completed same-head issues are reported as prior attempts.
func findDuplicateSynthesizedStrategyFixIssue(allIssues []*platform.Issue, prNumber int, fingerprint string, currentHeadSHA string) (*platform.Issue, bool) {
	if fingerprint == "" {
		return nil, false
	}
	for _, issue := range allIssues {
		if issue == nil || !isActiveReviewStrategyIssue(issue) || !issues.HasLabel(issue.Labels, issues.ReviewNonConverging) || !strings.HasPrefix(issue.Title, "Review strategy fix") {
			continue
		}
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		if parsed.FrontMatter.Type != "fix" || parsed.FrontMatter.BatchPR != prNumber {
			continue
		}
		marker, ok := parseReviewNonConvergenceFingerprintMarker(issue.Body)
		if !ok || marker.Fingerprint != fingerprint {
			continue
		}
		return issue, true
	}
	return nil, false
}

func findPriorCompletedStrategyFixIssues(allIssues []*platform.Issue, prNumber int, fingerprint string) []reviewPriorStrategyFixIssue {
	if fingerprint == "" {
		return nil
	}
	var prior []reviewPriorStrategyFixIssue
	for _, issue := range allIssues {
		if issue == nil || !issues.HasLabel(issue.Labels, issues.ReviewNonConverging) || !strings.HasPrefix(issue.Title, "Review strategy fix") {
			continue
		}
		parsed, err := issues.ParseBody(issue.Body)
		if err != nil {
			continue
		}
		fm := parsed.FrontMatter
		if fm.Type != "fix" || fm.BatchPR != prNumber {
			continue
		}
		marker, ok := parseReviewNonConvergenceFingerprintMarker(issue.Body)
		if !ok || marker.Fingerprint != fingerprint {
			continue
		}
		status := issues.StatusLabel(issue.Labels)
		if !isCompletedPriorReviewStrategyIssue(issue, status) {
			continue
		}
		prior = append(prior, reviewPriorStrategyFixIssue{
			Number:           issue.Number,
			Cycle:            fm.FixCycle,
			StatusLabel:      status,
			State:            issue.State,
			Title:            issue.Title,
			Fingerprint:      marker.Fingerprint,
			HeadSHA:          marker.HeadSHA,
			ValidationStatus: extractReviewValidationStatus(issue.Body),
			WorkerReport:     bodyHasWorkerReport(issue.Body),
			Summary:          extractReviewPriorStrategySummary(parsed),
		})
	}
	sort.SliceStable(prior, func(i, j int) bool {
		if prior[i].Cycle != prior[j].Cycle {
			return prior[i].Cycle < prior[j].Cycle
		}
		return prior[i].Number < prior[j].Number
	})
	return prior
}

func buildPriorStrategyFixIssueSummary(prior []reviewPriorStrategyFixIssue) string {
	if len(prior) == 0 {
		return ""
	}
	var lines []string
	for _, issue := range prior {
		if issue.Number <= 0 {
			continue
		}
		line := fmt.Sprintf("- Previous strategy fix #%d was completed but the same root-cause cluster reappeared, so that fix was incomplete.", issue.Number)
		var details []string
		if issue.Cycle > 0 {
			details = append(details, fmt.Sprintf("cycle %d", issue.Cycle))
		}
		if issue.StatusLabel != "" {
			details = append(details, issue.StatusLabel)
		}
		if issue.State != "" {
			details = append(details, "state "+issue.State)
		}
		if issue.HeadSHA != "" {
			details = append(details, "head "+issue.HeadSHA)
		}
		if issue.ValidationStatus != "" {
			details = append(details, "validation "+issue.ValidationStatus)
		}
		if issue.WorkerReport {
			details = append(details, "worker report present")
		}
		if issue.Summary != "" {
			details = append(details, "summary: "+issue.Summary)
		}
		if len(details) > 0 {
			line += " (" + strings.Join(details, "; ") + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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
		if strings.HasPrefix(line, "##") {
			currentSeverity = ""
			continue
		}
		upper := strings.ToUpper(line)
		for _, severity := range []string{"HIGH", "MEDIUM", "LOW", "CRITERIA"} {
			if strings.HasPrefix(upper, "**"+severity+"**") {
				currentSeverity = severity
			}
		}
		if match := reviewNumberFindingRE.FindStringSubmatch(line); len(match) == 3 {
			if !isReviewFindingMetadataNoise(match[2]) {
				addReviewFinding(out, match[1], match[2])
			}
			continue
		}
		if match := reviewDirectFindingRE.FindStringSubmatch(line); len(match) == 3 {
			severity := strings.ToUpper(match[1])
			text := strings.TrimSpace(match[2])
			if text != "" && !isReviewFindingMetadataNoise(text) {
				addReviewFinding(out, severity, text)
			}
			currentSeverity = severity
			continue
		}
		if currentSeverity != "" && strings.HasPrefix(line, "- ") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if !isReviewFindingMetadataNoise(text) {
				addReviewFinding(out, currentSeverity, text)
			}
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
		if isValidReviewSynthesisFilePath(file) {
			seen[file] = struct{}{}
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if isReviewFindingMetadataNoise(line) {
			continue
		}
		for _, match := range reviewPathRE.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			file := strings.Trim(match[1], "`.,);:")
			if looksLikeReviewFilePath(file) && !isReviewClusterNoise(file) {
				seen[file] = struct{}{}
			}
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
	if cleaned == "" || strings.Contains(cleaned, "://") || !strings.Contains(cleaned, "/") || isReviewClusterNoise(cleaned) {
		return ""
	}
	dir := cleaned
	if looksLikeReviewFilePath(cleaned) {
		dir = path.Dir(cleaned)
	}
	if dir == "." || dir == "/" || dir == "" || isReviewClusterNoise(dir) {
		return ""
	}
	parts := strings.Split(dir, "/")
	if parts[0] == "internal" && len(parts) >= 3 {
		return strings.Join(parts[:3], "/")
	}
	switch strings.ToLower(parts[0]) {
	case "internal", "cmd", "docs", "doc", "test", "tests", "pkg", "src", "lib", "scripts", "tools":
		if len(parts) < 2 {
			return dir
		}
		return strings.Join(parts[:2], "/")
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

func copyReviewFindingsBySeverity(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func reviewFilePathsFromText(text string) []string {
	seen := map[string]struct{}{}
	for _, match := range reviewPathRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		file := strings.Trim(match[1], "`.,);:")
		if looksLikeReviewFilePath(file) && !isReviewClusterNoise(file) {
			seen[file] = struct{}{}
		}
	}
	return sortedStringSet(seen)
}

func countReviewSynthesisSymptomCycles(symptoms []agent.ReviewSynthesisSymptom) int {
	seen := map[int]struct{}{}
	for _, symptom := range symptoms {
		for _, cycle := range symptom.Cycles {
			if cycle > 0 {
				seen[cycle] = struct{}{}
			}
		}
	}
	return len(seen)
}

func normalizeReviewSynthesisFingerprintText(value string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}), " ")
}

func sanitizedReviewSynthesisRootCauseTitle(title string) string {
	return normalizeReviewClusterTitleTerm(title)
}

func isValidReviewSynthesisFilePath(file string) bool {
	file = strings.TrimSpace(strings.Trim(file, "`.,);:"))
	return looksLikeReviewFilePath(file) && !isReviewClusterNoise(file)
}

func sanitizedReviewSynthesisFilePaths(files []string) []string {
	seen := map[string]struct{}{}
	for _, file := range files {
		file = strings.TrimSpace(strings.Trim(file, "`.,);:"))
		if isValidReviewSynthesisFilePath(file) {
			seen[file] = struct{}{}
		}
	}
	return sortedStringSet(seen)
}

func hasValidReviewSynthesisSymptomDescription(symptoms []agent.ReviewSynthesisSymptom) bool {
	for _, symptom := range symptoms {
		description := strings.TrimSpace(symptom.Description)
		if description != "" && !isReviewFindingMetadataNoise(description) && !isReviewFindingDismissalText(description) && normalizeReviewSynthesisFingerprintText(description) != "" {
			return true
		}
	}
	return false
}

func hasValidReviewSynthesisAffectedFile(symptoms []agent.ReviewSynthesisSymptom) bool {
	for _, symptom := range symptoms {
		if len(sanitizedReviewSynthesisFilePaths(symptom.AffectedFiles)) > 0 {
			return true
		}
	}
	return false
}

func normalizeReviewSynthesisFiles(files []string) []string {
	seen := map[string]struct{}{}
	for _, file := range sanitizedReviewSynthesisFilePaths(files) {
		normalized := normalizeReviewSynthesisFingerprintText(file)
		if normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	return sortedStringSet(seen)
}

func sanitizedReviewSynthesisSymptoms(symptoms []agent.ReviewSynthesisSymptom) []agent.ReviewSynthesisSymptom {
	var sanitized []agent.ReviewSynthesisSymptom
	for _, symptom := range symptoms {
		description := strings.TrimSpace(symptom.Description)
		if description == "" || isReviewFindingMetadataNoise(description) || isReviewFindingDismissalText(description) || normalizeReviewSynthesisFingerprintText(description) == "" {
			continue
		}
		files := sanitizedReviewSynthesisFilePaths(symptom.AffectedFiles)
		if len(files) == 0 {
			continue
		}
		sanitized = append(sanitized, agent.ReviewSynthesisSymptom{
			Description:   description,
			Cycles:        uniqueSortedInts(symptom.Cycles),
			AffectedFiles: files,
		})
	}
	return sanitized
}

func synthesizedReviewAffectedScope(result *agent.ReviewSynthesisResult) []string {
	seen := map[string]struct{}{}
	if result != nil {
		for _, symptom := range sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms) {
			for _, file := range symptom.AffectedFiles {
				seen[file] = struct{}{}
			}
		}
	}
	return sortedStringSet(seen)
}

func buildSynthesizedStrategyImplementationDetails(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput, prior []reviewPriorStrategyFixIssue) string {
	var sections []string
	sections = append(sections, "Root cause title: "+strings.TrimSpace(result.RootCauseTitle))
	sections = append(sections, "Root cause summary:\n"+fallbackString(strings.TrimSpace(result.RootCauseSummary), "No summary provided."))
	sections = append(sections, "Recurring symptoms:\n"+formatReviewSynthesisSymptoms(sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms)))
	sections = append(sections, "Why previous individual fixes did not converge:\n"+fallbackString(strings.TrimSpace(result.WhyIndividualFixesAreNotConverging), "No rationale provided."))
	sections = append(sections, "Proposed strategy:\n"+fallbackString(strings.TrimSpace(result.ProposedStrategy), "No strategy provided."))
	sections = append(sections, "Acceptance criteria:\n"+formatBullets(trimBlankStrings(result.AcceptanceCriteria)))
	sections = append(sections, "Non-goals:\n"+formatBullets(trimBlankStrings(result.NonGoals)))
	if reinterpretation := result.RequirementReinterpretation; reinterpretation != nil {
		sections = append(sections, formatReviewRequirementReinterpretation(reinterpretation))
	}
	if summary := buildPriorStrategyFixIssueSummary(prior); summary != "" {
		sections = append(sections, "Prior strategy fix attempts:\n"+summary)
	}
	sections = append(sections, "Source review result comments/context:\n"+formatReviewSynthesisSourceContext(input))
	return strings.Join(sections, "\n\n")
}

func buildSynthesizedStrategyContext(input agent.ReviewSynthesisInput, prior []reviewPriorStrategyFixIssue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- PR: #%d\n", input.PRNumber)
	fmt.Fprintf(&b, "- Batch: %d\n", input.BatchNumber)
	fmt.Fprintf(&b, "- Head SHA: %s\n", fallbackString(input.HeadSHA, "unknown"))
	fmt.Fprintf(&b, "- Head branch: %s\n", fallbackString(input.HeadRef, "unknown"))
	fmt.Fprintf(&b, "- Cycles analyzed: %s\n", formatReviewSynthesisCycleList(input.Cycles))
	fmt.Fprintf(&b, "- Completed fix issues: %s\n", formatReviewSynthesisFixIssueList(input.CompletedFixIssues))
	fmt.Fprintf(&b, "- Prior completed strategy fix issues: %s\n", formatPriorStrategyFixIssueNumberList(prior))
	fmt.Fprintf(&b, "- Affected files: %s\n", formatStringList(sanitizedReviewSynthesisFilePaths(input.AffectedFiles)))
	if strings.TrimSpace(input.CurrentPRMetadata) != "" {
		fmt.Fprintf(&b, "\nCurrent PR metadata:\n%s\n", strings.TrimSpace(input.CurrentPRMetadata))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatReviewSynthesisSymptoms(symptoms []agent.ReviewSynthesisSymptom) string {
	if len(symptoms) == 0 {
		return "- none"
	}
	var lines []string
	for _, symptom := range symptoms {
		description := fallbackString(strings.TrimSpace(symptom.Description), "unspecified symptom")
		lines = append(lines, fmt.Sprintf("- %s (cycles: %s; affected files: %s)", description, formatIntList(uniqueSortedInts(symptom.Cycles)), formatStringList(sanitizedReviewSynthesisFilePaths(symptom.AffectedFiles))))
	}
	return strings.Join(lines, "\n")
}

func formatReviewSynthesisSourceContext(input agent.ReviewSynthesisInput) string {
	var lines []string
	for _, cycle := range input.Cycles {
		lines = append(lines, fmt.Sprintf("- Cycle %d (%s): %d findings; fix issues %s; files %s", cycle.Cycle, fallbackString(cycle.Status, "unknown"), cycle.FindingsAfterDedupe, formatIntList(cycle.FixIssueNumbers), formatStringList(cycle.AffectedFiles)))
	}
	for i, comment := range input.RecentReviewComments {
		trimmed := strings.TrimSpace(comment)
		if trimmed == "" {
			continue
		}
		if len([]rune(trimmed)) > 600 {
			trimmed = string([]rune(trimmed)[:600]) + "..."
		}
		lines = append(lines, fmt.Sprintf("- Review comment %d excerpt:\n%s", i+1, trimmed))
		if len(lines) >= len(input.Cycles)+3 {
			break
		}
	}
	if len(lines) == 0 {
		return "- none"
	}
	return strings.Join(lines, "\n")
}

func formatReviewSynthesisCycleList(cycles []agent.ReviewSynthesisCycle) string {
	if len(cycles) == 0 {
		return "none"
	}
	nums := make([]int, 0, len(cycles))
	for _, cycle := range cycles {
		nums = append(nums, cycle.Cycle)
	}
	return formatIntList(nums)
}

func formatReviewSynthesisFixIssueList(fixes []agent.ReviewSynthesisFixIssue) string {
	if len(fixes) == 0 {
		return "none"
	}
	nums := make([]int, 0, len(fixes))
	for _, fix := range fixes {
		nums = append(nums, fix.Number)
	}
	return formatIssueNumberList(nums)
}

func formatPriorStrategyFixIssueNumberList(prior []reviewPriorStrategyFixIssue) string {
	if len(prior) == 0 {
		return "none"
	}
	nums := make([]int, 0, len(prior))
	for _, issue := range prior {
		nums = append(nums, issue.Number)
	}
	return formatIssueNumberList(nums)
}

func formatBullets(values []string) string {
	if len(values) == 0 {
		return "- none"
	}
	lines := make([]string, len(values))
	for i, value := range values {
		lines[i] = "- " + strings.TrimSpace(value)
	}
	return strings.Join(lines, "\n")
}

func uniqueSortedInts(nums []int) []int {
	seen := map[int]struct{}{}
	for _, n := range nums {
		if n > 0 {
			seen[n] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

func trimBlankStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sortedStringSet(seen map[string]struct{}) []string {
	values := make([]string, 0, len(seen))
	for value := range seen {
		if strings.TrimSpace(value) != "" {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func buildStrategyFixIssueContext(analysis reviewConvergenceAnalysis) string {
	analysis.Cluster.PackageClusters = concreteReviewPackageClusters(analysis.Cluster.PackageClusters)
	analysis.Cluster.RootCauseTerms = concreteReviewRootCauseTerms(analysis.Cluster.RootCauseTerms)
	var b strings.Builder
	fmt.Fprintf(&b, "- Cycles analyzed: %s\n", formatReviewCycleNumbers(analysis.Cycles))
	fmt.Fprintf(&b, "- Finding count trend: %s\n", formatIntList(analysis.TrendCounts))
	fmt.Fprintf(&b, "- Completed fix issues: %s\n", formatIssueNumberList(analysis.CompletedFixIssues))
	fmt.Fprintf(&b, "- In-progress fix issues: %s\n", formatIssueNumberList(analysis.InProgressFixIssues))
	if packages := concreteReviewPackageClusters(analysis.Cluster.PackageClusters); len(packages) > 0 {
		fmt.Fprintf(&b, "- Dominant package clusters: %s\n", formatStringList(packages))
	}
	fmt.Fprintf(&b, "- Dominant root-cause terms: %s\n", formatStringList(cleanReviewClusterValues(analysis.Cluster.RootCauseTerms)))
	fmt.Fprintf(&b, "- Rationale: %s\n", fallbackString(analysis.Rationale, "repeated review/fix cycles share the same cluster fingerprint"))
	return strings.TrimRight(b.String(), "\n")
}

func buildDeterministicStrategyImplementationDetails(analysis reviewConvergenceAnalysis) string {
	sections := []string{
		"### Repeated Pattern\n\n" + formatRepeatedReviewPattern(analysis.Cluster),
		"### Representative Findings\n\n" + formatRepresentativeReviewFindings(representativeReviewFindings(analysis.Cycles, 5)),
		"### Prior Fix Attempts\n\n" + formatCombinedPriorReviewFixAttempts(analysis),
		"### Strategy Guidance\n\nFix the shared invariant or state transition that lets the same review pattern recur. Treat endpoint-level findings as symptoms: repair the common boundary first, migrate affected packages to it, then add regression coverage that proves retries, redeliveries, and unknown-state repairs converge without duplicate observable effects.",
	}
	return strings.Join(sections, "\n\n")
}

func formatCombinedPriorReviewFixAttempts(analysis reviewConvergenceAnalysis) string {
	cycleAttempts := formatPriorReviewFixAttempts(analysis.Cycles)
	strategyAttempts := buildPriorStrategyFixIssueSummary(analysis.PriorStrategyFixIssues)
	if strategyAttempts == "" {
		return cycleAttempts
	}
	if cycleAttempts == "" {
		return strategyAttempts
	}
	return cycleAttempts + "\n" + strategyAttempts
}

func formatRepeatedReviewPattern(cluster reviewConvergenceCluster) string {
	packages := concreteReviewPackageClusters(cluster.PackageClusters)
	terms := concreteReviewRootCauseTerms(cluster.RootCauseTerms)
	var paragraphs []string
	if len(packages) > 0 {
		paragraphs = append(paragraphs, fmt.Sprintf("Repeated findings cluster around package/subsystem scope %s with root-cause terms %s. Use these as signals for one shared architecture/design failure, not as a queue of independent review cleanups.", formatStringList(packages), formatStringList(terms)))
	} else {
		paragraphs = append(paragraphs, fmt.Sprintf("Repeated findings lack a concrete package/subsystem scope and have root-cause terms %s. Use these only as diagnostic context until a concrete recurring scope is available.", formatStringList(terms)))
	}
	if reviewTermsNeedDurableMutationGuidance(terms) {
		paragraphs = append(paragraphs, "Define durable GitHub-visible mutation boundaries with explicit pre-call, call-started, post-call-unknown, completed, failed-pre-call, and repair-required states. Retry and redelivery paths must consult that durable state before creating comments, issues, or workflow dispatches so repeated attempts converge without duplicates.")
	}
	return strings.Join(paragraphs, "\n\n")
}

func reviewTermsNeedDurableMutationGuidance(terms []string) bool {
	watched := map[string]struct{}{
		"dispatch":    {},
		"durable":     {},
		"idempotency": {},
		"mutation":    {},
		"post-call":   {},
		"pre-call":    {},
		"repair":      {},
		"retry":       {},
		"unknown":     {},
		"workflow":    {},
	}
	for _, term := range terms {
		if _, ok := watched[term]; ok {
			return true
		}
	}
	return false
}

func representativeReviewFindings(cycles []reviewHistoryCycle, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, limit)
	for i := len(cycles) - 1; i >= 0 && len(out) < limit; i-- {
		cycle := cycles[i]
		cycleID := cycle.Cycle
		if cycleID == 0 {
			cycleID = i + 1
		}
		for _, severity := range []string{"HIGH", "MEDIUM", "LOW", "CRITERIA"} {
			for _, finding := range cycle.FindingsBySeverity[severity] {
				text := strings.TrimSpace(finding)
				if text == "" || isReviewFindingMetadataNoise(text) || isReviewFindingDismissalText(text) {
					continue
				}
				key := strings.ToLower(text)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, fmt.Sprintf("- Cycle %d %s: %s", cycleID, severity, text))
				if len(out) == limit {
					return out
				}
			}
		}
	}
	return out
}

func formatRepresentativeReviewFindings(findings []string) string {
	if len(findings) == 0 {
		return "- No representative findings were parsed from the reviewed cycles."
	}
	return strings.Join(findings, "\n")
}

func formatPriorReviewFixAttempts(cycles []reviewHistoryCycle) string {
	var lines []string
	seen := map[int]struct{}{}
	for i, cycle := range cycles {
		cycleID := cycle.Cycle
		if cycleID == 0 {
			cycleID = i + 1
		}
		for _, fix := range cycle.FixIssues {
			if fix.Number == 0 {
				continue
			}
			if fix.StatusLabel != issues.StatusDone && !isSuccessfulWorkerReport(fix) {
				continue
			}
			if _, ok := seen[fix.Number]; ok {
				continue
			}
			seen[fix.Number] = struct{}{}
			status := fallbackString(fix.StatusLabel, "unknown status")
			validation := fallbackString(fix.ValidationStatus, "validation not reported")
			report := "worker report absent"
			if fix.WorkerReport {
				report = "worker report present"
			}
			files := cleanReviewClusterValues(fix.FilesSummary)
			if len(files) > 3 {
				files = files[:3]
			}
			line := fmt.Sprintf("- #%d (cycle %d): %s; %s; %s", fix.Number, cycleID, status, validation, report)
			if len(files) > 0 {
				line += "; files: " + strings.Join(files, ", ")
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "- No completed prior fix attempts were parsed from the reviewed cycles."
	}
	return strings.Join(lines, "\n")
}

func reviewStrategyTitleSuffix(cluster reviewConvergenceCluster) string {
	terms := concreteReviewRootCauseTerms(cluster.RootCauseTerms)
	packages := concreteReviewPackageClusters(cluster.PackageClusters)
	if title := reviewRootCauseTitle(terms); title != "" {
		return title
	}
	if len(packages) > 0 {
		var parts []string
		if subsystem := reviewPackageTitleTerm(packages[0]); subsystem != "" {
			parts = append(parts, subsystem)
		}
		if len(terms) > 0 {
			parts = append(parts, terms...)
		}
		if title := normalizeReviewClusterTitleTerm(strings.Join(parts, " ")); title != "" {
			return title
		}
	}
	if len(terms) >= 2 {
		return normalizeReviewClusterTitleTerm(strings.Join(terms[:2], " ") + " gaps")
	}
	if len(terms) == 1 {
		return normalizeReviewClusterTitleTerm(terms[0] + " gaps")
	}
	return "repeated review findings"
}

func reviewRootCauseTitle(terms []string) string {
	termSet := map[string]struct{}{}
	for _, term := range terms {
		termSet[term] = struct{}{}
	}
	has := func(term string) bool {
		_, ok := termSet[term]
		return ok
	}
	switch {
	case has("durable") && has("mutation"):
		return "durable mutation boundary gaps"
	case has("retry") && (has("dispatch") || has("workflow")):
		return "repeated control-plane retry failures"
	}
	return ""
}

func reviewPackageTitleTerm(pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" || isReviewClusterNoise(pkg) {
		return ""
	}
	if strings.Contains(pkg, "hosted") || strings.Contains(pkg, "app") {
		return "hosted App"
	}
	if strings.HasPrefix(pkg, "internal/controlplane") {
		return "control-plane"
	}
	base := path.Base(pkg)
	if base == "." || base == "/" {
		return ""
	}
	return strings.ReplaceAll(base, "-", " ")
}

func cleanReviewClusterValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		term := normalizeReviewClusterTitleTerm(value)
		if term != "" {
			out = append(out, term)
		}
	}
	sort.Strings(out)
	return trimBlankStrings(out)
}

func normalizeReviewClusterTitleTerm(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "`.,);:"))
	value = strings.TrimPrefix(value, "- ")
	value = strings.TrimSpace(value)
	if value == "" || isReviewClusterNoise(value) {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func normalizeReviewRootCauseTerm(value string) string {
	value = normalizeReviewClusterTitleTerm(strings.ToLower(strings.ReplaceAll(value, "_", "-")))
	if value == "side-effect" {
		return "side effect"
	}
	return value
}

func isReviewClusterNoise(value string) bool {
	value = strings.TrimSpace(strings.Trim(value, "`.,);:"))
	value = strings.TrimPrefix(value, "- ")
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	lower := strings.ToLower(value)
	if reviewBareFractionRE.MatchString(value) || reviewChunkLabelRE.MatchString(value) || reviewBareChunkRE.MatchString(value) {
		return true
	}
	exactNoiseLabels := []string{
		"none",
		"coverage",
		"diff coverage",
	}
	for _, label := range exactNoiseLabels {
		if lower == label {
			return true
		}
	}
	noiseLabels := []string{
		"diff coverage",
		"review aggregation",
		"files reviewed",
		"source: local-git",
		"raw findings before dedupe",
		"findings after dedupe",
		"stale pr-state findings ignored",
		"chunks reviewed",
	}
	for _, label := range noiseLabels {
		if strings.Contains(lower, label) {
			return true
		}
	}
	return false
}

func isReviewFindingMetadataNoise(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(text, "- "))
	if isReviewClusterNoise(text) {
		return true
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "reviewed ") && strings.Contains(lower, "chunk") {
		return true
	}
	return strings.HasPrefix(lower, "generated summary") ||
		strings.HasPrefix(lower, "generated review summary") ||
		strings.HasPrefix(lower, "summary generated")
}

func isReviewFindingDismissalText(text string) bool {
	text = strings.TrimSpace(strings.TrimPrefix(text, "- "))
	text = strings.Trim(text, "`*_ ")
	text = strings.TrimSpace(strings.TrimRight(text, ".!"))
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"no issue here",
		"no issue found",
		"no problem here",
		"already fixed",
		"false positive",
		"not an issue",
		"no change needed",
		"no actionable issue",
	} {
		if lower == phrase || strings.HasPrefix(lower, phrase+":") {
			return true
		}
	}
	return false
}

func formatReviewCycleNumbers(cycles []reviewHistoryCycle) string {
	if len(cycles) == 0 {
		return "none"
	}
	nums := make([]int, 0, len(cycles))
	for i, cycle := range cycles {
		if cycle.Cycle > 0 {
			nums = append(nums, cycle.Cycle)
		} else {
			nums = append(nums, i+1)
		}
	}
	return formatIntList(nums)
}

func formatIntList(nums []int) string {
	if len(nums) == 0 {
		return "none"
	}
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func formatIssueNumberList(nums []int) string {
	if len(nums) == 0 {
		return "none"
	}
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}

func formatStringList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func truncateReviewStrategyTitle(title string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(title)
	if len(runes) <= limit {
		return title
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func parseReviewFingerprintBatchPR(body string) int {
	parsed, err := issues.ParseBody(body)
	if err != nil {
		return 0
	}
	return parsed.FrontMatter.BatchPR
}

func isActiveReviewStrategyIssue(issue *platform.Issue) bool {
	if issue.State != "" && issue.State != "open" {
		return false
	}
	status := issues.StatusLabel(issue.Labels)
	if status == issues.StatusReady || status == issues.StatusInProgress {
		return true
	}
	if status == issues.StatusDone || status == issues.StatusCancelled {
		return false
	}
	return status == ""
}

func isCompletedPriorReviewStrategyIssue(issue *platform.Issue, status string) bool {
	if status == issues.StatusFailed || status == issues.StatusCancelled || status == issues.StatusBlocked || status == issues.StatusReady || status == issues.StatusInProgress {
		return false
	}
	return status == issues.StatusDone || issue.State == "closed"
}

func extractReviewPriorStrategySummary(parsed *issues.IssueBody) string {
	if parsed == nil {
		return ""
	}
	if summary := firstNonBlankLine(parsed.Task); summary != "" {
		return summary
	}
	return firstNonBlankLine(parsed.Context)
}

func firstNonBlankLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
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
