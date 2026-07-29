package integrator

import (
	"fmt"
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

	latest := cycles[len(cycles)-1]
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
	var completed []reviewHistoryCycle
	for _, cycle := range cycles[:len(cycles)-1] {
		if reviewCycleHasCompletedFix(cycle) {
			completed = append(completed, cycle)
		}
	}
	out.CompletedCycleCount = len(completed)
	if len(completed) < requiredCompleted {
		return fail(fmt.Sprintf("only %d completed review/fix cycles; need at least %d", len(completed), requiredCompleted))
	}
	out.CompletedFixChainConfirmed = true

	latestSubsystems := reviewFindingSubsystemSet(latestFindings)
	latestTerms := reviewFindingArchitecturalTermSet(latestFindings)
	if len(latestSubsystems) == 0 {
		return fail("latest review has no concrete package/subsystem or ownership boundary")
	}
	if len(latestTerms) == 0 {
		return fail("latest review has no concrete architectural concept")
	}

	subsystemCycles := map[string][]reviewHistoryCycle{}
	termCycles := map[string]map[int]struct{}{}
	for _, cycle := range completed {
		findings := sanitizedDistinctReviewFindings(cycle)
		if len(findings) == 0 {
			continue
		}
		for subsystem := range reviewFindingSubsystemSet(findings) {
			if _, inLatest := latestSubsystems[subsystem]; inLatest {
				subsystemCycles[subsystem] = append(subsystemCycles[subsystem], cycle)
			}
		}
		for term := range reviewFindingArchitecturalTermSet(findings) {
			if _, inLatest := latestTerms[term]; !inLatest {
				continue
			}
			if termCycles[term] == nil {
				termCycles[term] = map[int]struct{}{}
			}
			termCycles[term][cycle.Cycle] = struct{}{}
		}
	}

	evidenceByCycle := map[int]reviewHistoryCycle{}
	for subsystem, matching := range subsystemCycles {
		if len(matching) < reviewOscillationMinEvidenceCycles {
			continue
		}
		out.RecurringSubsystems = append(out.RecurringSubsystems, subsystem)
		for _, cycle := range matching {
			evidenceByCycle[cycle.Cycle] = cycle
		}
	}
	for term, matching := range termCycles {
		if len(matching) >= reviewOscillationMinEvidenceCycles {
			out.RecurringArchitecturalTerms = append(out.RecurringArchitecturalTerms, term)
		}
	}
	sort.Strings(out.RecurringSubsystems)
	sort.Strings(out.RecurringArchitecturalTerms)
	if len(out.RecurringSubsystems) == 0 {
		return fail("no concrete subsystem recurs in the latest review and three completed evidence cycles")
	}
	if len(out.RecurringArchitecturalTerms) == 0 {
		return fail("no architectural concept family recurs in the latest review and three completed evidence cycles")
	}

	for _, cycle := range completed {
		if _, ok := evidenceByCycle[cycle.Cycle]; !ok {
			continue
		}
		terms := reviewFindingArchitecturalTermSet(sanitizedDistinctReviewFindings(cycle))
		if stringSetIntersects(terms, sliceStringSet(out.RecurringArchitecturalTerms)) {
			out.EvidenceCycles = append(out.EvidenceCycles, cycle)
		}
	}
	if len(out.EvidenceCycles) < reviewOscillationMinEvidenceCycles {
		return fail("subsystem and architectural evidence do not co-occur in three completed cycles")
	}
	evidenceHeads := append([]reviewHistoryCycle(nil), out.EvidenceCycles...)
	evidenceHeads = append(evidenceHeads, latest)
	headSet := map[string]struct{}{}
	for _, cycle := range evidenceHeads {
		head := strings.TrimSpace(cycle.HeadSHA)
		if head == "" {
			return fail(fmt.Sprintf("cycle %d has no reviewed head SHA", cycle.Cycle))
		}
		if _, exists := headSet[head]; exists {
			return fail(fmt.Sprintf("reviewed head SHA %s is reused in the evidence chain", head))
		}
		headSet[head] = struct{}{}
	}
	out.DistinctHeadSHAsConfirmed = true
	out.Eligible = true
	out.Rationale = "low-volume findings recur across distinct reviewed heads after a completed fix chain with matching subsystem and architectural evidence"
	return out
}

func evaluateLowVolumeReviewSynthesis(result *agent.ReviewSynthesisResult, minConfidence float64, eligibility reviewOscillationEligibility) (reviewSynthesisDecision, string) {
	analysis := reviewConvergenceAnalysis{CompletedFixIssues: completedFixNumbers(eligibility.EvidenceCycles)}
	if decision, reason := evaluateReviewSynthesis(result, minConfidence, analysis); decision != reviewSynthesisDecisionEscalate {
		return decision, reason
	}
	if !eligibility.Eligible {
		return reviewSynthesisDecisionFallback, "deterministic low-volume oscillation analysis was not eligible"
	}
	title := sanitizedReviewSynthesisRootCauseTitle(result.RootCauseTitle)
	if !isMeaningfulLowVolumeRootCause(title, result) {
		return reviewSynthesisDecisionFallback, "synthesis root cause is generic, package-only, or unrelated to the recurring architectural evidence"
	}
	symptoms := sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms)
	if countReviewSynthesisSymptomCycles(symptoms) < reviewOscillationMinEvidenceCycles {
		return reviewSynthesisDecisionFallback, "synthesis symptoms do not span three evidence cycles"
	}
	if !synthesisSymptomsAlignWithSubsystems(symptoms, eligibility.RecurringSubsystems) {
		return reviewSynthesisDecisionFallback, "synthesis affected files do not align with recurring subsystems"
	}
	if !synthesisRootCauseAlignsWithArchitecture(result, eligibility.RecurringArchitecturalTerms) {
		return reviewSynthesisDecisionFallback, "synthesis root cause does not align with recurring architectural evidence"
	}
	if !meaningfulLowVolumeAcceptanceCriteria(result.AcceptanceCriteria) {
		return reviewSynthesisDecisionFallback, "synthesis acceptance criteria are not meaningful"
	}
	return reviewSynthesisDecisionEscalate, "low-volume synthesis passed deterministic alignment and safety gates"
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
	if triviallyEquivalentReviewRequirement(value.ConflictingRequirement, value.CorrectedInvariant) {
		return false, "corrected invariant is equivalent to the conflicting requirement"
	}
	if len(concreteReviewBoundaries(value.LinearizationBoundaries)) == 0 {
		return false, "requirement reinterpretation has no concrete linearization boundary"
	}
	if len(concreteReviewBoundaries(value.DurabilityBoundaries)) == 0 {
		return false, "requirement reinterpretation has no concrete durability boundary"
	}
	if !isClearlyPreservedSafetyProperty(value.PreservedSafetyProperty) {
		return false, "requirement reinterpretation does not preserve a clear user-visible safety property"
	}
	criteria := strings.Join(trimBlankStrings(result.AcceptanceCriteria), " ")
	if !reviewTextCoversRequirement(criteria, value.CorrectedInvariant) || !reviewTextCoversRequirement(criteria, value.PreservedSafetyProperty) {
		return false, "acceptance criteria do not cover both the corrected invariant and preserved safety property"
	}
	return true, ""
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

func reviewFindingSubsystemSet(findings []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, finding := range findings {
		for _, file := range reviewFilePathsFromText(finding) {
			if subsystem := normalizeReviewPackagePath(file); isConcreteReviewPackageCluster(subsystem) {
				out[subsystem] = struct{}{}
			}
		}
	}
	return out
}

func reviewFindingArchitecturalTermSet(findings []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, finding := range findings {
		normalized := " " + normalizeReviewSynthesisFingerprintText(finding) + " "
		for family, keywords := range reviewArchitecturalConceptKeywords {
			for _, keyword := range keywords {
				if strings.Contains(normalized, normalizeReviewSynthesisFingerprintText(keyword)) {
					out[family] = struct{}{}
					break
				}
			}
		}
	}
	return out
}

func completedFixNumbers(cycles []reviewHistoryCycle) []int {
	var out []int
	for _, cycle := range cycles {
		for _, fix := range cycle.FixIssues {
			if fix.StatusLabel == issues.StatusDone || isSuccessfulWorkerReport(fix) {
				out = appendUniqueInt(out, fix.Number)
			}
		}
	}
	return out
}

func synthesisSymptomsAlignWithSubsystems(symptoms []agent.ReviewSynthesisSymptom, recurring []string) bool {
	wanted := sliceStringSet(recurring)
	for _, symptom := range symptoms {
		aligned := false
		for _, file := range symptom.AffectedFiles {
			if subsystem := normalizeReviewPackagePath(file); subsystem != "" {
				if _, ok := wanted[subsystem]; ok {
					aligned = true
				}
			}
		}
		if !aligned {
			return false
		}
	}
	return len(symptoms) > 0
}

func isMeaningfulLowVolumeRootCause(title string, result *agent.ReviewSynthesisResult) bool {
	normalized := normalizeReviewSynthesisFingerprintText(title)
	if normalized == "" || isReviewFindingMetadataNoise(title) || isReviewFindingDismissalText(title) {
		return false
	}
	if reviewCycleRE.MatchString(title) || reviewChunkLabelRE.MatchString(title) ||
		strings.Contains(normalized, "coverage") || strings.Contains(normalized, "files reviewed") {
		return false
	}
	for _, generic := range []string{"review workflow", "repeated review findings", "review issue", "fix loop", "internal integrator", "integrator"} {
		if normalized == generic {
			return false
		}
	}
	combined := normalized
	if result != nil {
		combined += " " + normalizeReviewSynthesisFingerprintText(result.RootCauseSummary+" "+result.ProposedStrategy)
	}
	return len(reviewFindingArchitecturalTermSet([]string{combined})) > 0
}

func synthesisRootCauseAlignsWithArchitecture(result *agent.ReviewSynthesisResult, recurring []string) bool {
	if result == nil {
		return false
	}
	terms := reviewFindingArchitecturalTermSet([]string{result.RootCauseTitle + " " + result.RootCauseSummary + " " + result.ProposedStrategy})
	return stringSetIntersects(terms, sliceStringSet(recurring))
}

func meaningfulLowVolumeAcceptanceCriteria(criteria []string) bool {
	clean := trimBlankStrings(criteria)
	if len(clean) < 2 {
		return false
	}
	architectural := false
	for _, criterion := range clean {
		if len(significantReviewWords(criterion)) < 2 || isReviewFindingMetadataNoise(criterion) || isReviewFindingDismissalText(criterion) {
			return false
		}
		if len(reviewFindingArchitecturalTermSet([]string{criterion})) > 0 {
			architectural = true
		}
	}
	return architectural
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

func triviallyEquivalentReviewRequirement(left, right string) bool {
	a := normalizeReviewSynthesisFingerprintText(left)
	b := normalizeReviewSynthesisFingerprintText(right)
	if a == b || strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	leftWords := canonicalReviewRequirementWords(a)
	rightWords := canonicalReviewRequirementWords(b)
	if len(leftWords) == 0 || len(rightWords) == 0 {
		return false
	}
	matches := 0
	for word := range leftWords {
		if _, ok := rightWords[word]; ok {
			matches++
		}
	}
	shorter := len(leftWords)
	if len(rightWords) < shorter {
		shorter = len(rightWords)
	}
	return matches*5 >= shorter*4
}

func canonicalReviewRequirementWords(value string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, word := range strings.Fields(value) {
		switch word {
		case "the", "a", "an", "must", "should", "shall":
			continue
		case "atomically":
			word = "atomic"
		}
		if strings.HasSuffix(word, "ing") && len(word) > 6 {
			word = strings.TrimSuffix(word, "ing")
		}
		out[word] = struct{}{}
	}
	return out
}

func isClearlyPreservedSafetyProperty(value string) bool {
	normalized := normalizeReviewSynthesisFingerprintText(value)
	for _, term := range []string{"visible", "user", "duplicate", "loss", "corrupt", "exclusive", "ownership", "consistent", "durable", "safety", "exactly once"} {
		if strings.Contains(normalized, term) {
			return true
		}
	}
	return false
}

func reviewTextCoversRequirement(criteria, requirement string) bool {
	criteriaWords := sliceStringSet(significantReviewWords(criteria))
	requirementWords := significantReviewWords(requirement)
	matches := 0
	for _, word := range requirementWords {
		if _, ok := criteriaWords[word]; ok {
			matches++
		}
	}
	return matches >= 2
}

func significantReviewWords(value string) []string {
	var out []string
	for _, word := range strings.Fields(normalizeReviewSynthesisFingerprintText(value)) {
		if len(word) >= 5 {
			out = append(out, word)
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

func sliceStringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func stringSetIntersects(left, right map[string]struct{}) bool {
	for value := range left {
		if _, ok := right[value]; ok {
			return true
		}
	}
	return false
}
