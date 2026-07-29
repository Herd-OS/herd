package integrator

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/issues"
)

const reviewOscillationMinLatestFindings = 2
const reviewOscillationMinEvidenceCycles = 3

type reviewOscillationEligibility struct {
	Eligible                   bool
	Rationale                  string
	CompletedCycleCount        int
	LatestFindingCount         int
	EvidenceCycles             []reviewHistoryCycle
	LatestCycle                int
	RecurringSubsystems        []string
	DistinctHeadSHAsConfirmed  bool
	CompletedFixChainConfirmed bool
}

func analyzeLowVolumeReviewOscillation(cycles []reviewHistoryCycle, minCompletedCycles int, synthesisEnabled bool) reviewOscillationEligibility {
	out := reviewOscillationEligibility{}
	fail := func(reason string) reviewOscillationEligibility {
		out.Rationale = reason
		return out
	}
	if !synthesisEnabled {
		return fail("low-volume oscillation synthesis is disabled")
	}
	if len(cycles) == 0 {
		return fail("no parsed review cycles available")
	}
	for i := 1; i < len(cycles); i++ {
		if cycles[i].Cycle <= cycles[i-1].Cycle {
			return fail("review cycles are not in strictly increasing chronological order")
		}
	}

	latest := cycles[len(cycles)-1]
	out.LatestCycle = latest.Cycle
	latestFindings := sanitizedDistinctReviewFindings(latest)
	out.LatestFindingCount = len(latestFindings)
	if out.LatestFindingCount < reviewOscillationMinLatestFindings {
		return fail(fmt.Sprintf("latest actionable finding count %d is below low-volume threshold %d", out.LatestFindingCount, reviewOscillationMinLatestFindings))
	}
	if out.LatestFindingCount >= reviewNonConvergenceMinLatestFindings {
		return fail(fmt.Sprintf("latest actionable finding count %d is reserved for high-volume non-convergence", out.LatestFindingCount))
	}

	requiredCompleted := reviewOscillationMinEvidenceCycles
	if minCompletedCycles > requiredCompleted {
		requiredCompleted = minCompletedCycles
	}
	completedStart := len(cycles) - 1
	for completedStart > 0 && reviewCycleHasCompletedFix(cycles[completedStart-1]) {
		completedStart--
	}
	completed := append([]reviewHistoryCycle(nil), cycles[completedStart:len(cycles)-1]...)
	out.CompletedCycleCount = len(completed)
	if len(completed) < requiredCompleted {
		return fail(fmt.Sprintf("latest contiguous review/fix chain has only %d completed cycles; need at least %d", len(completed), requiredCompleted))
	}
	out.CompletedFixChainConfirmed = true

	headSet := map[string]struct{}{}
	for _, cycle := range append(completed, latest) {
		head := strings.TrimSpace(cycle.HeadSHA)
		if head == "" {
			return fail(fmt.Sprintf("cycle %d has no reviewed head SHA", cycle.Cycle))
		}
		if _, exists := headSet[head]; exists {
			return fail(fmt.Sprintf("reviewed head SHA %s is reused in the completed review chain", head))
		}
		headSet[head] = struct{}{}
	}
	out.DistinctHeadSHAsConfirmed = true

	latestSubsystems := actionableReviewSubsystems(latestFindings)
	for _, subsystem := range sortedStringSet(latestSubsystems) {
		var evidence []reviewHistoryCycle
		for _, cycle := range completed {
			if _, recurs := actionableReviewSubsystems(sanitizedDistinctReviewFindings(cycle))[subsystem]; recurs {
				evidence = append(evidence, cycle)
			}
		}
		if len(evidence) >= requiredCompleted {
			out.RecurringSubsystems = append(out.RecurringSubsystems, subsystem)
			out.EvidenceCycles = evidence
			break
		}
	}
	if len(out.RecurringSubsystems) == 0 {
		return fail("no actionable finding recurs in one normalized subsystem across the required completed cycles and latest review")
	}
	out.Eligible = true
	out.Rationale = "low-volume actionable findings recur in one normalized subsystem across distinct reviewed heads after a completed fix chain; semantic coherence is deferred to synthesis and independent verification"
	return out
}

func evaluateLowVolumeReviewSynthesis(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput, minConfidence float64, eligibility reviewOscillationEligibility) (reviewSynthesisDecision, string) {
	if !eligibility.Eligible {
		return reviewSynthesisDecisionFallback, "deterministic low-volume oscillation analysis was not eligible"
	}
	if result == nil {
		return reviewSynthesisDecisionFallback, "synthesis returned nil result"
	}
	if !result.ShouldEscalate {
		return reviewSynthesisDecisionFallback, fallbackString(result.Reason, "synthesis chose not to escalate")
	}
	if result.Confidence < minConfidence {
		return reviewSynthesisDecisionFallback, fmt.Sprintf("synthesis confidence %.2f below threshold %.2f", result.Confidence, minConfidence)
	}
	if strings.TrimSpace(result.RootCauseTitle) == "" || len(sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms)) == 0 ||
		strings.TrimSpace(result.ProposedStrategy) == "" || len(trimBlankStrings(result.AcceptanceCriteria)) == 0 {
		return reviewSynthesisDecisionFallback, "synthesis omitted required strategy fields"
	}
	if ok, reason := validateLowVolumeSynthesisProvenance(result, input, eligibility); !ok {
		return reviewSynthesisDecisionFallback, reason
	}
	return reviewSynthesisDecisionEscalate, "low-volume synthesis passed deterministic schema and provenance gates"
}

func validateReviewRequirementReinterpretation(result *agent.ReviewSynthesisResult) (bool, string) {
	if result == nil || result.RequirementReinterpretation == nil {
		return true, ""
	}
	value := result.RequirementReinterpretation
	switch value.ConstraintKind {
	case agent.ReviewRequirementOverConstrained, agent.ReviewRequirementInternallyConflicting, agent.ReviewRequirementPlatformNonAtomic:
	default:
		return false, "requirement reinterpretation has unknown constraint kind"
	}
	scalars := []struct {
		name  string
		value string
	}{
		{"conflicting requirement", value.ConflictingRequirement},
		{"platform consistency constraint", value.PlatformConsistencyConstraint},
		{"preserved safety property", value.PreservedSafetyProperty},
		{"corrected invariant", value.CorrectedInvariant},
	}
	for _, scalar := range scalars {
		if !isConcreteReviewReinterpretationText(scalar.value) {
			return false, "requirement reinterpretation has invalid " + scalar.name
		}
	}
	if len(concreteReviewBoundaries(value.LinearizationBoundaries)) == 0 {
		return false, "requirement reinterpretation has no concrete linearization boundary"
	}
	if len(concreteReviewBoundaries(value.DurabilityBoundaries)) == 0 {
		return false, "requirement reinterpretation has no concrete durability boundary"
	}
	if len(value.EvidenceReferences) == 0 {
		return false, "requirement reinterpretation has no evidence references"
	}
	return true, ""
}

func actionableReviewSubsystems(findings []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, finding := range findings {
		description := finding
		files := reviewFilePathsFromText(finding)
		for _, file := range files {
			description = strings.ReplaceAll(description, file, " ")
		}
		if isReviewFindingMetadataNoise(description) || isReviewFindingDismissalText(description) ||
			len(strings.Fields(normalizeReviewSynthesisFingerprintText(description))) < 3 {
			continue
		}
		for _, file := range files {
			if subsystem := normalizeReviewPackagePath(file); isConcreteReviewPackageCluster(subsystem) {
				out[subsystem] = struct{}{}
			}
		}
	}
	return out
}

func validateLowVolumeSynthesisProvenance(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput, eligibility reviewOscillationEligibility) (bool, string) {
	sources, ok, reason := indexedReviewEvidenceSources(input.EvidenceSources)
	if !ok {
		return false, reason
	}
	eligibleCycles := map[int]struct{}{}
	for _, cycle := range eligibility.EvidenceCycles {
		eligibleCycles[cycle.Cycle] = struct{}{}
	}
	allEligibleCycles := make(map[int]struct{}, len(eligibleCycles)+1)
	for cycle := range eligibleCycles {
		allEligibleCycles[cycle] = struct{}{}
	}
	allEligibleCycles[eligibility.LatestCycle] = struct{}{}
	representedHistorical := map[int]struct{}{}
	latestRepresented := false
	for _, symptom := range result.RecurringSymptoms {
		if len(symptom.EvidenceReferences) == 0 {
			return false, "synthesis symptom has no evidence references"
		}
		cyclesSeen := map[int]struct{}{}
		for _, cycle := range symptom.Cycles {
			if _, duplicate := cyclesSeen[cycle]; duplicate {
				return false, "synthesis symptom contains a duplicate cycle reference"
			}
			cyclesSeen[cycle] = struct{}{}
		}
		if ok, reason := validateReviewEvidenceObject(symptom.EvidenceReferences, symptom.SourceExcerpts, sources); !ok {
			return false, "synthesis symptom " + reason
		}
		for _, reference := range symptom.EvidenceReferences {
			source := sources[reference]
			if source.Kind != "review_finding" || source.Cycle == 0 {
				return false, "synthesis symptom references non-finding evidence"
			}
			if _, eligible := allEligibleCycles[source.Cycle]; !eligible {
				return false, "synthesis symptom references stale or ineligible finding evidence"
			}
			if !containsInt(symptom.Cycles, source.Cycle) {
				return false, "synthesis symptom cycle does not match its evidence reference"
			}
			if source.Cycle == eligibility.LatestCycle {
				latestRepresented = true
			} else {
				representedHistorical[source.Cycle] = struct{}{}
			}
		}
	}
	if len(representedHistorical) < reviewOscillationMinEvidenceCycles {
		return false, "synthesis evidence does not span three completed historical evidence cycles"
	}
	if !latestRepresented {
		return false, "synthesis evidence does not cite a finding from the latest review"
	}
	if value := result.RequirementReinterpretation; value != nil {
		if ok, reason := validateReviewRequirementReinterpretation(result); !ok {
			return false, reason
		}
		if ok, reason := validateRequirementReinterpretationProvenance(value, sources, allEligibleCycles); !ok {
			return false, reason
		}
	}
	return true, ""
}

func indexedReviewEvidenceSources(values []agent.ReviewEvidenceSource) (map[string]agent.ReviewEvidenceSource, bool, string) {
	sources := make(map[string]agent.ReviewEvidenceSource, len(values))
	for _, source := range values {
		if strings.TrimSpace(source.ID) == "" {
			return nil, false, "input contains evidence without a stable reference"
		}
		if _, duplicate := sources[source.ID]; duplicate {
			return nil, false, "input contains duplicate evidence reference " + source.ID
		}
		sources[source.ID] = source
	}
	return sources, true, ""
}

func validateReviewEvidenceObject(references []string, excerpts []agent.ReviewSourceExcerpt, sources map[string]agent.ReviewEvidenceSource) (bool, string) {
	referenceCounts := make(map[string]int, len(references))
	for _, reference := range references {
		referenceCounts[reference]++
		source, exists := sources[reference]
		if referenceCounts[reference] > 1 {
			return false, "contains a duplicate evidence reference"
		}
		if !exists || source.Kind == "truncation_marker" {
			return false, "contains a missing, foreign, stale, or truncation-marker evidence reference"
		}
	}
	return validateReviewSourceExcerpts(referenceCounts, excerpts, sources)
}

func validateReviewSourceExcerpts(referenceCounts map[string]int, excerpts []agent.ReviewSourceExcerpt, sources map[string]agent.ReviewEvidenceSource) (bool, string) {
	excerptReferences := make(map[string]struct{}, len(excerpts))
	for _, excerpt := range excerpts {
		if _, duplicate := excerptReferences[excerpt.Reference]; duplicate {
			return false, "contains a duplicate source excerpt reference"
		}
		excerptReferences[excerpt.Reference] = struct{}{}
		if referenceCounts[excerpt.Reference] != 1 {
			return false, "contains an unknown or unreferenced source excerpt"
		}
		source, exists := sources[excerpt.Reference]
		if !exists || source.Kind == "truncation_marker" {
			return false, "contains a stale, foreign, or truncation-marker source excerpt"
		}
		if strings.TrimSpace(excerpt.Excerpt) == "" {
			return false, "contains an empty source excerpt"
		}
		if !strings.Contains(source.Excerpt, excerpt.Excerpt) {
			return false, "contains an inexact source excerpt"
		}
	}
	return true, ""
}

func validateRequirementReinterpretationProvenance(value *agent.ReviewRequirementReinterpretation, sources map[string]agent.ReviewEvidenceSource, eligibleCycles map[int]struct{}) (bool, string) {
	if ok, reason := validateReviewEvidenceObject(value.EvidenceReferences, value.SourceExcerpts, sources); !ok {
		return false, "requirement reinterpretation " + reason
	}
	hasRequirement := false
	for _, reference := range value.EvidenceReferences {
		source := sources[reference]
		if source.Cycle == 0 && strings.HasPrefix(source.Kind, "requirement_") {
			hasRequirement = true
		}
		if source.Cycle > 0 {
			if source.Kind != "review_finding" {
				return false, "requirement reinterpretation references non-finding cycle evidence"
			}
			if eligibleCycles != nil {
				if _, eligible := eligibleCycles[source.Cycle]; !eligible {
					return false, "requirement reinterpretation references stale finding evidence"
				}
			}
		}
	}
	if !hasRequirement {
		return false, "requirement reinterpretation does not cite an original requirement"
	}
	return true, ""
}

func validateHighVolumeRequirementReinterpretationProvenance(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput) (bool, string) {
	if result == nil || result.RequirementReinterpretation == nil {
		return true, ""
	}
	sources, ok, reason := indexedReviewEvidenceSources(input.EvidenceSources)
	if !ok {
		return false, reason
	}
	return validateRequirementReinterpretationProvenance(result.RequirementReinterpretation, sources, nil)
}

func validateHighVolumeSynthesisSourceExcerpts(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput) (bool, string) {
	if result == nil {
		return true, ""
	}
	sources, ok, reason := indexedReviewEvidenceSources(input.EvidenceSources)
	if !ok {
		return false, reason
	}
	for _, symptom := range result.RecurringSymptoms {
		if len(symptom.SourceExcerpts) == 0 {
			continue
		}
		referenceCounts := make(map[string]int, len(symptom.EvidenceReferences))
		for _, reference := range symptom.EvidenceReferences {
			referenceCounts[reference]++
		}
		if ok, reason := validateReviewSourceExcerpts(referenceCounts, symptom.SourceExcerpts, sources); !ok {
			return false, "synthesis symptom " + reason
		}
	}
	return true, ""
}

func verifyReviewSynthesis(ctx context.Context, configured agent.Agent, input agent.ReviewSynthesisInput, result *agent.ReviewSynthesisResult, repoRoot string) (bool, string) {
	verifier, ok := configured.(agent.ReviewVerifier)
	if !ok {
		return false, "configured agent provider does not support independent verification"
	}
	referenced := map[string]struct{}{}
	for _, symptom := range result.RecurringSymptoms {
		for _, reference := range symptom.EvidenceReferences {
			referenced[reference] = struct{}{}
		}
	}
	if result.RequirementReinterpretation != nil {
		for _, reference := range result.RequirementReinterpretation.EvidenceReferences {
			referenced[reference] = struct{}{}
		}
	}
	citedReferences := sortedStringSet(referenced)
	evidence := boundedReviewVerificationEvidence(input.EvidenceSources, referenced)
	verificationInput := agent.ReviewVerificationInput{
		PRNumber: input.PRNumber, BatchNumber: input.BatchNumber, HeadSHA: input.HeadSHA,
		EvidenceSources: evidence, CitedEvidenceReferences: citedReferences, Synthesis: *result,
	}
	verifyCtx, cancel := context.WithTimeout(ctx, reviewVerificationTimeout)
	defer cancel()
	verification, err := verifier.VerifyReviewNonConvergence(verifyCtx, verificationInput, agent.ReviewSynthesisOptions{RepoRoot: repoRoot})
	if err != nil {
		return false, err.Error()
	}
	if verification == nil {
		return false, "verification returned nil result"
	}
	if !verification.Approved {
		return false, fallbackString(verification.Reason, "verification rejected the synthesis")
	}
	if verification.Confidence < reviewVerificationMinConfidence {
		return false, fmt.Sprintf("verification confidence %.2f below threshold %.2f", verification.Confidence, reviewVerificationMinConfidence)
	}
	if strings.TrimSpace(verification.Reason) == "" {
		return false, "verification returned an empty reason"
	}
	return true, verification.Reason
}

func boundedReviewVerificationEvidence(sources []agent.ReviewEvidenceSource, cited map[string]struct{}) []agent.ReviewEvidenceSource {
	const budget = 40 * 1024
	selected := make(map[string]struct{}, len(sources))
	used := 0
	for _, source := range sources {
		if _, required := cited[source.ID]; !required || source.Kind == "truncation_marker" {
			continue
		}
		selected[source.ID] = struct{}{}
		used += len(source.ID) + len(source.Kind) + len(source.Excerpt) + 64
	}
	for _, source := range sources {
		if source.Kind == "truncation_marker" {
			continue
		}
		if _, exists := selected[source.ID]; exists {
			continue
		}
		cost := len(source.ID) + len(source.Kind) + len(source.Excerpt) + 64
		if used+cost <= budget {
			selected[source.ID] = struct{}{}
			used += cost
		}
	}
	out := make([]agent.ReviewEvidenceSource, 0, len(selected))
	for _, source := range sources {
		if _, include := selected[source.ID]; include {
			out = append(out, source)
		}
	}
	return out
}

func lowVolumeReviewSynthesisInput(input agent.ReviewSynthesisInput, eligibility reviewOscillationEligibility) agent.ReviewSynthesisInput {
	allowed := map[int]struct{}{eligibility.LatestCycle: {}}
	for _, cycle := range eligibility.EvidenceCycles {
		allowed[cycle.Cycle] = struct{}{}
	}
	var cycles []agent.ReviewSynthesisCycle
	var fixes []agent.ReviewSynthesisFixIssue
	fixNumbers := map[int]struct{}{}
	for _, cycle := range input.Cycles {
		if _, ok := allowed[cycle.Cycle]; !ok {
			continue
		}
		// Stable evidence sources carry exact finding text and IDs. Keep only
		// lifecycle metadata here instead of rendering every finding twice.
		cycle.FindingsBySeverity = nil
		cycles = append(cycles, cycle)
		for _, fix := range cycle.CompletedFixIssues {
			if _, duplicate := fixNumbers[fix.Number]; !duplicate {
				fixNumbers[fix.Number] = struct{}{}
				fix.Body = ""
				fixes = append(fixes, fix)
			}
		}
	}
	var sources []agent.ReviewEvidenceSource
	for _, source := range input.EvidenceSources {
		if source.Cycle == 0 {
			sources = append(sources, source)
			continue
		}
		if _, ok := allowed[source.Cycle]; ok {
			sources = append(sources, source)
		}
	}
	input.Cycles = cycles
	input.CompletedFixIssues = fixes
	// Stable requirement evidence is authoritative for this route. Rendering
	// the structured copy too would spend the bounded prompt twice.
	input.OriginalRequirements = nil
	input.EvidenceSources = boundedLowVolumeEvidenceSources(sources, eligibility)
	return input
}

func boundedLowVolumeEvidenceSources(sources []agent.ReviewEvidenceSource, eligibility reviewOscillationEligibility) []agent.ReviewEvidenceSource {
	const requirementBudget = 16 * 1024
	const findingBudget = 24 * 1024
	requirements := make([]agent.ReviewEvidenceSource, 0, len(sources))
	findings := make([]agent.ReviewEvidenceSource, 0, len(sources))
	for _, source := range sources {
		if source.Cycle == 0 {
			requirements = append(requirements, source)
		} else {
			findings = append(findings, source)
		}
	}
	appendWithinBudget := func(out []agent.ReviewEvidenceSource, values []agent.ReviewEvidenceSource, budget int, kind string, priorityCycles []int) []agent.ReviewEvidenceSource {
		selected := make(map[string]struct{}, len(values))
		used := 0
		trySelect := func(source agent.ReviewEvidenceSource) {
			cost := len(source.ID) + len(source.Kind) + len(source.Excerpt) + 64
			if used+cost <= budget {
				selected[source.ID] = struct{}{}
				used += cost
			}
		}
		for _, cycle := range priorityCycles {
			for _, source := range values {
				if source.Cycle == cycle {
					trySelect(source)
					break
				}
			}
		}
		for _, source := range values {
			if _, exists := selected[source.ID]; !exists {
				trySelect(source)
			}
		}
		for _, source := range values {
			if _, exists := selected[source.ID]; exists {
				out = append(out, source)
			}
		}
		if len(selected) < len(values) {
			out = append(out, agent.ReviewEvidenceSource{
				ID: "truncation:" + kind, Kind: "truncation_marker",
				Excerpt: fmt.Sprintf("[TRUNCATED: %d additional %s evidence sources omitted by deterministic budget]", len(values)-len(selected), kind),
			})
		}
		return out
	}
	out := appendWithinBudget(nil, requirements, requirementBudget, "requirement", nil)
	priorityCycles := []int{eligibility.LatestCycle}
	for _, cycle := range eligibility.EvidenceCycles {
		priorityCycles = append(priorityCycles, cycle.Cycle)
	}
	return appendWithinBudget(out, findings, findingBudget, "finding", priorityCycles)
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func buildLowVolumeSynthesizedStrategyFixIssueTitle(result *agent.ReviewSynthesisResult) string {
	suffix := ""
	if result != nil {
		suffix = sanitizedReviewSynthesisRootCauseTitle(result.RootCauseTitle)
		if !isMeaningfulLowVolumeRootCause(suffix, result) {
			suffix = "recurring architectural invariant failure"
		}
	}
	if suffix == "" {
		suffix = "recurring architectural invariant failure"
	}
	return truncateReviewStrategyTitle("Review strategy fix: "+suffix, 120)
}

func sanitizedDistinctReviewFindings(cycle reviewHistoryCycle) []string {
	seen := map[string]string{}
	for _, finding := range reviewActionableFindingDescriptions(cycle) {
		if isReviewFindingDismissalText(finding) {
			continue
		}
		normalized := normalizeReviewSynthesisFingerprintText(finding)
		if normalized == "" || strings.Contains(normalized, "generated summary") {
			continue
		}
		seen[normalized] = strings.TrimSpace(finding)
	}
	keys := sortedStringSetFromValues(seen)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

func sortedStringSetFromValues(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func reviewCycleHasCompletedFix(cycle reviewHistoryCycle) bool {
	for _, fix := range cycle.FixIssues {
		if fix.StatusLabel == issues.StatusDone || isSuccessfulWorkerReport(fix) {
			return true
		}
	}
	return false
}

func isMeaningfulLowVolumeRootCause(title string, result *agent.ReviewSynthesisResult) bool {
	normalized := normalizeReviewSynthesisFingerprintText(title)
	if normalized == "" || isReviewFindingMetadataNoise(title) || isReviewFindingDismissalText(title) {
		return false
	}
	if strings.ContainsAny(title, `/\`) || looksLikeReviewFilePath(strings.TrimSpace(title)) {
		return false
	}
	if reviewCycleRE.MatchString(title) || reviewChunkLabelRE.MatchString(title) ||
		strings.Contains(normalized, "coverage") || strings.Contains(normalized, "files reviewed") {
		return false
	}
	if len(strings.Fields(normalized)) < 3 {
		return false
	}
	if result != nil {
		for _, symptom := range result.RecurringSymptoms {
			for _, file := range symptom.AffectedFiles {
				subsystem := normalizeReviewPackagePath(file)
				base := normalizeReviewSynthesisFingerprintText(path.Base(subsystem))
				if base != "" && (normalized == base || strings.HasPrefix(normalized, base+" package ") ||
					strings.HasPrefix(normalized, base+" module ") || strings.HasPrefix(normalized, base+" component ")) {
					return false
				}
			}
		}
	}
	// This is only a syntactic/noise guard for an already verified result.
	// Semantic coherence and root-cause specificity belong to the verifier.
	return true
}

func isConcreteReviewReinterpretationText(value string) bool {
	value = strings.TrimSpace(value)
	normalized := normalizeReviewSynthesisFingerprintText(value)
	if normalized == "" || isReviewFindingMetadataNoise(value) || isReviewFindingDismissalText(value) {
		return false
	}
	lower := strings.ToLower(value)
	if strings.ContainsAny(value, "<>") || strings.Contains(lower, "ignore previous") ||
		strings.Contains(lower, "system prompt") || strings.HasPrefix(lower, "#") {
		return false
	}
	switch normalized {
	case "none", "unknown", "n a", "true", "false", "requirement", "constraint", "invariant", "safety", "atomic", "durable",
		"boundary", "commit point", "linearization boundary", "durability boundary", "platform constraint":
		return false
	}
	return len(strings.Fields(normalized)) >= 2
}

func concreteReviewBoundaries(values []string) []string {
	var out []string
	for _, value := range values {
		if isConcreteReviewReinterpretationText(value) {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func normalizedSortedReviewStrings(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if normalized := normalizeReviewSynthesisFingerprintText(value); normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	return sortedStringSet(set)
}

func formatReviewRequirementReinterpretation(value *agent.ReviewRequirementReinterpretation) string {
	if value == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Requirement reinterpretation\n")
	fmt.Fprintf(&b, "- Constraint kind: %s\n", value.ConstraintKind)
	fmt.Fprintf(&b, "- Conflicting requirement: %s\n", strings.TrimSpace(value.ConflictingRequirement))
	fmt.Fprintf(&b, "- Platform consistency constraint: %s\n", strings.TrimSpace(value.PlatformConsistencyConstraint))
	fmt.Fprintf(&b, "- Preserved safety property: %s\n", strings.TrimSpace(value.PreservedSafetyProperty))
	fmt.Fprintf(&b, "- Corrected invariant: %s\n", strings.TrimSpace(value.CorrectedInvariant))
	b.WriteString("- Linearization boundaries:\n" + formatBullets(concreteReviewBoundaries(value.LinearizationBoundaries)) + "\n")
	b.WriteString("- Durability boundaries:\n" + formatBullets(concreteReviewBoundaries(value.DurabilityBoundaries)))
	return b.String()
}
