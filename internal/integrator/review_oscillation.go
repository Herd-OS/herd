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
	Eligible                    bool
	Rationale                   string
	CompletedCycleCount         int
	LatestFindingCount          int
	EvidenceCycles              []reviewHistoryCycle
	LatestCycle                 int
	RecurringSubsystems         []string
	RecurringArchitecturalTerms []string
	DistinctHeadSHAsConfirmed   bool
	CompletedFixChainConfirmed  bool
}

var reviewArchitecturalConceptKeywords = map[string][]string{
	"consistency-linearization": {"consistency", "consistent", "linearization", "linearizable", "atomic handoff", "commit point"},
	"durability-recovery":       {"durability", "durable", "recovery", "recover", "repair", "crash"},
	"invariant":                 {"invariant", "must always", "mutually incompatible", "cannot both", "contradict"},
	"lifecycle-state":           {"lifecycle", "state transition", "state machine", "terminal state", "transition"},
	"ownership-boundary":        {"ownership boundary", "owner", "ownership", "exclusive ownership", "authority boundary"},
	"side-effect-publication":   {"side effect", "side-effect", "publication", "publish", "visibility", "github-visible"},
	"synchronization":           {"synchronization", "synchronise", "synchronize", "lock", "race", "atomic", "mutex"},
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
	sources := make(map[string]agent.ReviewEvidenceSource, len(input.EvidenceSources))
	for _, source := range input.EvidenceSources {
		if strings.TrimSpace(source.ID) == "" {
			return false, "input contains evidence without a stable reference"
		}
		if _, duplicate := sources[source.ID]; duplicate {
			return false, "input contains duplicate evidence reference " + source.ID
		}
		sources[source.ID] = source
	}
	eligibleCycles := map[int]struct{}{}
	for _, cycle := range eligibility.EvidenceCycles {
		eligibleCycles[cycle.Cycle] = struct{}{}
	}
	eligibleCycles[eligibility.LatestCycle] = struct{}{}
	check := func(reference string, excerpts []agent.ReviewSourceExcerpt) bool {
		source, ok := sources[reference]
		if !ok {
			return false
		}
		if source.Kind == "truncation_marker" {
			return false
		}
		if source.Cycle > 0 {
			if _, eligible := eligibleCycles[source.Cycle]; !eligible {
				return false
			}
		}
		for _, excerpt := range excerpts {
			if excerpt.Reference == reference && (strings.TrimSpace(excerpt.Excerpt) == "" || !strings.Contains(source.Excerpt, excerpt.Excerpt)) {
				return false
			}
		}
		return true
	}
	represented := map[int]struct{}{}
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
		seen := map[string]struct{}{}
		for _, reference := range symptom.EvidenceReferences {
			if _, duplicate := seen[reference]; duplicate {
				return false, "synthesis symptom contains a duplicate evidence reference"
			}
			seen[reference] = struct{}{}
			if !check(reference, symptom.SourceExcerpts) {
				return false, "synthesis contains missing, foreign, stale, duplicate, or inexact evidence reference"
			}
			source := sources[reference]
			if source.Cycle == 0 {
				return false, "synthesis symptom references non-finding evidence"
			}
			if !containsInt(symptom.Cycles, source.Cycle) {
				return false, "synthesis symptom cycle does not match its evidence reference"
			}
			represented[source.Cycle] = struct{}{}
		}
	}
	if len(represented) < reviewOscillationMinEvidenceCycles {
		return false, "synthesis evidence does not span three eligible cycles"
	}
	if value := result.RequirementReinterpretation; value != nil {
		if ok, reason := validateReviewRequirementReinterpretation(result); !ok {
			return false, reason
		}
		seen := map[string]struct{}{}
		hasRequirement := false
		for _, reference := range value.EvidenceReferences {
			if _, duplicate := seen[reference]; duplicate {
				return false, "requirement reinterpretation contains a duplicate evidence reference"
			}
			seen[reference] = struct{}{}
			if !check(reference, value.SourceExcerpts) {
				return false, "requirement reinterpretation contains missing, foreign, stale, duplicate, or inexact evidence reference"
			}
			if sources[reference].Cycle == 0 {
				hasRequirement = true
			}
		}
		if !hasRequirement {
			return false, "requirement reinterpretation does not cite an original requirement"
		}
	}
	return true, ""
}

func verifyLowVolumeReviewSynthesis(ctx context.Context, configured agent.Agent, input agent.ReviewSynthesisInput, result *agent.ReviewSynthesisResult, repoRoot string) (bool, string) {
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
	var evidence []agent.ReviewEvidenceSource
	for _, source := range input.EvidenceSources {
		if _, cited := referenced[source.ID]; cited {
			evidence = append(evidence, source)
		}
	}
	verificationInput := agent.ReviewVerificationInput{
		PRNumber: input.PRNumber, BatchNumber: input.BatchNumber, HeadSHA: input.HeadSHA,
		EvidenceSources: evidence, Synthesis: *result,
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
	input.EvidenceSources = boundedLowVolumeEvidenceSources(sources)
	return input
}

func boundedLowVolumeEvidenceSources(sources []agent.ReviewEvidenceSource) []agent.ReviewEvidenceSource {
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
	appendWithinBudget := func(out []agent.ReviewEvidenceSource, values []agent.ReviewEvidenceSource, budget int, kind string) []agent.ReviewEvidenceSource {
		used := 0
		for index, source := range values {
			cost := len(source.ID) + len(source.Kind) + len(source.Excerpt) + 64
			if used+cost > budget {
				out = append(out, agent.ReviewEvidenceSource{
					ID: "truncation:" + kind, Kind: "truncation_marker",
					Excerpt: fmt.Sprintf("[TRUNCATED: %d additional %s evidence sources omitted by deterministic budget]", len(values)-index, kind),
				})
				break
			}
			out = append(out, source)
			used += cost
		}
		return out
	}
	out := appendWithinBudget(nil, requirements, requirementBudget, "requirement")
	return appendWithinBudget(out, findings, findingBudget, "finding")
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

func reviewFindingArchitecturalTermSet(findings []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, finding := range findings {
		normalized := " " + normalizeReviewSynthesisFingerprintText(finding) + " "
		for family, keywords := range reviewArchitecturalConceptKeywords {
			for _, keyword := range keywords {
				if strings.Contains(normalized, " "+normalizeReviewSynthesisFingerprintText(keyword)+" ") {
					out[family] = struct{}{}
					break
				}
			}
		}
	}
	return out
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
	for _, generic := range []string{
		"review workflow", "repeated review findings", "review issue", "fix loop", "internal integrator", "integrator",
		"architecture", "architectural issue", "atomic", "durable", "durability", "invariant", "lifecycle",
		"ownership", "publication", "recovery", "state machine", "synchronization",
	} {
		if normalized == generic {
			return false
		}
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
	terms := reviewFindingArchitecturalTermSet([]string{title})
	if len(terms) == 0 {
		return false
	}
	if len(terms) >= 3 {
		return true
	}
	for _, qualifier := range []string{
		"ambiguity", "bypass", "conflict", "divergence", "double", "duplicate", "failure", "gap", "leak",
		"lost", "loss", "mismatch", "missing", "ordering", "premature", "split", "stale", "violation", "window",
	} {
		if strings.Contains(" "+normalized+" ", " "+qualifier+" ") {
			return true
		}
	}
	return false
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
