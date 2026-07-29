package integrator

import (
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

	latestClusters := reviewFindingEvidenceClusters(latestFindings)
	if len(latestClusters) == 0 {
		return fail("latest review has no concrete package/subsystem or ownership boundary")
	}

	clusterCycles := map[string][]reviewHistoryCycle{}
	for _, cycle := range completed {
		for key := range reviewFindingEvidenceClusters(sanitizedDistinctReviewFindings(cycle)) {
			if _, inLatest := latestClusters[key]; inLatest {
				clusterCycles[key] = append(clusterCycles[key], cycle)
			}
		}
	}

	evidenceByCycle := map[int]reviewHistoryCycle{}
	subsystemSet := map[string]struct{}{}
	termSet := map[string]struct{}{}
	for key, matching := range clusterCycles {
		if len(matching) < reviewOscillationMinEvidenceCycles {
			continue
		}
		cluster := latestClusters[key]
		subsystemSet[cluster.subsystem] = struct{}{}
		termSet[cluster.firstTerm] = struct{}{}
		termSet[cluster.secondTerm] = struct{}{}
		for _, cycle := range matching {
			evidenceByCycle[cycle.Cycle] = cycle
		}
	}
	out.RecurringSubsystems = sortedStringSet(subsystemSet)
	out.RecurringArchitecturalTerms = sortedStringSet(termSet)
	if len(out.RecurringSubsystems) == 0 {
		evidence, subsystem, terms, ok := reviewAlternatingBehavioralCluster(completed, latest)
		if !ok {
			return fail("no same-finding or alternating compatible architectural behavior cluster recurs in one subsystem across the latest review and three completed evidence cycles")
		}
		out.EvidenceCycles = evidence
		out.RecurringSubsystems = []string{subsystem}
		out.RecurringArchitecturalTerms = terms
		out.Eligible = true
		out.Rationale = "low-volume findings alternate between compatible architectural behaviors in one subsystem across distinct reviewed heads after a completed fix chain"
		return out
	}

	for _, cycle := range completed {
		if evidence, ok := evidenceByCycle[cycle.Cycle]; ok {
			out.EvidenceCycles = append(out.EvidenceCycles, evidence)
		}
	}
	out.Eligible = true
	out.Rationale = "low-volume findings recur across distinct reviewed heads after a completed fix chain with matching subsystem and architectural evidence"
	return out
}

func evaluateLowVolumeReviewSynthesis(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput, minConfidence float64, eligibility reviewOscillationEligibility) (reviewSynthesisDecision, string) {
	analysis := reviewConvergenceAnalysis{CompletedFixIssues: completedFixNumbers(eligibility.EvidenceCycles)}
	if decision, reason := evaluateReviewSynthesis(result, input, minConfidence, analysis); decision != reviewSynthesisDecisionEscalate {
		return decision, reason
	}
	if !eligibility.Eligible {
		return reviewSynthesisDecisionFallback, "deterministic low-volume oscillation analysis was not eligible"
	}
	title := sanitizedReviewSynthesisRootCauseTitle(result.RootCauseTitle)
	if !isMeaningfulLowVolumeRootCause(title, result) {
		return reviewSynthesisDecisionFallback, "synthesis root cause is generic, package-only, or unrelated to the recurring architectural evidence"
	}
	if !synthesisSymptomsReferenceEvidenceCycles(result.RecurringSymptoms, eligibility.EvidenceCycles) {
		return reviewSynthesisDecisionFallback, "synthesis symptoms contain invalid or duplicate cycle references, or do not span three evidence cycles"
	}
	symptoms := sanitizedReviewSynthesisSymptoms(result.RecurringSymptoms)
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

func validateReviewRequirementReinterpretation(result *agent.ReviewSynthesisResult, input agent.ReviewSynthesisInput) (bool, string) {
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
	originalEvidence := reviewOriginalRequirementEvidence(input.OriginalRequirements)
	acceptanceCriteriaEvidence := reviewOriginalAcceptanceCriteriaEvidence(input.OriginalRequirements)
	if !isClearlyPreservedSafetyProperty(value.PreservedSafetyProperty, acceptanceCriteriaEvidence) {
		return false, "requirement reinterpretation does not preserve a clear user-visible safety property traceable to the original requirements"
	}
	if !reviewTextAlignsWithEvidence(value.ConflictingRequirement, originalEvidence) {
		return false, "conflicting requirement is not traceable to an original requirement"
	}
	if !reviewTextAlignsWithEvidence(value.PreservedSafetyProperty, originalEvidence) {
		return false, "preserved safety property is not traceable to an original requirement"
	}
	if !reviewSafetyPropertyAlignsAffirmatively(value.PreservedSafetyProperty, acceptanceCriteriaEvidence) {
		return false, "preserved safety property is not affirmatively aligned with an original user-visible safety property"
	}
	if !reviewTextAlignsWithEvidence(value.PlatformConsistencyConstraint, reviewPlatformConstraintEvidence(input)) {
		return false, "platform consistency constraint is not supported by supplied specification or review history"
	}
	if !reviewSafetyTextEntails(value.PreservedSafetyProperty, value.CorrectedInvariant) {
		return false, "corrected invariant does not affirmatively entail the preserved safety property"
	}
	criteria := strings.Join(trimBlankStrings(result.AcceptanceCriteria), " ")
	if !reviewTextCoversRequirement(criteria, value.CorrectedInvariant) || !reviewTextCoversRequirement(criteria, value.PreservedSafetyProperty) {
		return false, "acceptance criteria do not cover both the corrected invariant and preserved safety property"
	}
	if !reviewSafetyTextEntails(value.PreservedSafetyProperty, criteria) {
		return false, "acceptance criteria do not affirmatively entail the preserved safety property"
	}
	return true, ""
}

func synthesisSymptomsReferenceEvidenceCycles(symptoms []agent.ReviewSynthesisSymptom, evidence []reviewHistoryCycle) bool {
	allowed := make(map[int]struct{}, len(evidence))
	for _, cycle := range evidence {
		allowed[cycle.Cycle] = struct{}{}
	}
	represented := map[int]struct{}{}
	for _, symptom := range symptoms {
		withinSymptom := map[int]struct{}{}
		for _, cycle := range symptom.Cycles {
			if _, ok := allowed[cycle]; !ok {
				return false
			}
			if _, duplicate := withinSymptom[cycle]; duplicate {
				return false
			}
			withinSymptom[cycle] = struct{}{}
			represented[cycle] = struct{}{}
		}
	}
	return len(represented) >= reviewOscillationMinEvidenceCycles
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
				if strings.Contains(normalized, " "+normalizeReviewSynthesisFingerprintText(keyword)+" ") {
					out[family] = struct{}{}
					break
				}
			}
		}
	}
	return out
}

type reviewFindingEvidenceCluster struct {
	subsystem  string
	firstTerm  string
	secondTerm string
	behavior   string
}

func reviewFindingEvidenceClusters(findings []string) map[string]reviewFindingEvidenceCluster {
	out := map[string]reviewFindingEvidenceCluster{}
	for _, finding := range findings {
		subsystems := reviewFindingSubsystemSet([]string{finding})
		terms := concreteReviewFindingArchitecturalTerms(finding)
		behavior := reviewFindingBehavioralIdentity(finding)
		if len(subsystems) == 0 || len(terms) < 2 || behavior == "" {
			continue
		}
		sortedTerms := sortedStringSet(terms)
		for subsystem := range subsystems {
			for i := 0; i < len(sortedTerms)-1; i++ {
				for j := i + 1; j < len(sortedTerms); j++ {
					cluster := reviewFindingEvidenceCluster{subsystem: subsystem, firstTerm: sortedTerms[i], secondTerm: sortedTerms[j], behavior: behavior}
					key := strings.Join([]string{cluster.subsystem, cluster.firstTerm, cluster.secondTerm, cluster.behavior}, "\x00")
					out[key] = cluster
				}
			}
		}
	}
	return out
}

type reviewBehavioralConceptSet struct {
	subsystem string
	terms     []string
}

func reviewAlternatingBehavioralCluster(completed []reviewHistoryCycle, latest reviewHistoryCycle) ([]reviewHistoryCycle, string, []string, bool) {
	if len(completed) < reviewOscillationMinEvidenceCycles {
		return nil, "", nil, false
	}
	evidence := append([]reviewHistoryCycle(nil), completed[len(completed)-reviewOscillationMinEvidenceCycles:]...)
	chain := append(append([]reviewHistoryCycle(nil), evidence...), latest)
	setsByCycle := make([]map[string]reviewBehavioralConceptSet, len(chain))
	for i, cycle := range chain {
		setsByCycle[i] = reviewFindingBehavioralConceptSets(sanitizedDistinctReviewFindings(cycle))
		if len(setsByCycle[i]) == 0 {
			return nil, "", nil, false
		}
	}

	for _, firstKey := range sortedReviewBehavioralConceptSetKeys(setsByCycle[0]) {
		first := setsByCycle[0][firstKey]
		for _, secondKey := range sortedReviewBehavioralConceptSetKeys(setsByCycle[1]) {
			second := setsByCycle[1][secondKey]
			if firstKey == secondKey || first.subsystem != second.subsystem || !reviewBehavioralConceptSetsCompatible(first.terms, second.terms) {
				continue
			}
			matches := true
			for i := 2; i < len(setsByCycle); i++ {
				wanted := firstKey
				if i%2 == 1 {
					wanted = secondKey
				}
				if _, ok := setsByCycle[i][wanted]; !ok {
					matches = false
					break
				}
			}
			if matches {
				terms := sliceStringSet(first.terms)
				for _, term := range second.terms {
					terms[term] = struct{}{}
				}
				return evidence, first.subsystem, sortedStringSet(terms), true
			}
		}
	}
	return nil, "", nil, false
}

func sortedReviewBehavioralConceptSetKeys(values map[string]reviewBehavioralConceptSet) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func reviewFindingBehavioralConceptSets(findings []string) map[string]reviewBehavioralConceptSet {
	out := map[string]reviewBehavioralConceptSet{}
	for _, finding := range findings {
		behavior := reviewFindingBehavioralIdentity(finding)
		if behavior == "" {
			continue
		}
		subsystems := reviewFindingSubsystemSet([]string{finding})
		terms := sortedStringSet(reviewFindingArchitecturalTermSet([]string{finding}))
		var conceptSets [][]string
		for i := 0; i < len(terms)-1; i++ {
			for j := i + 1; j < len(terms); j++ {
				conceptSets = append(conceptSets, []string{terms[i], terms[j]})
			}
		}
		if reviewFindingMatchesMultipleFamilyKeywords(finding, "durability-recovery") {
			conceptSets = append(conceptSets, []string{"durability-recovery"})
		}
		for subsystem := range subsystems {
			for _, conceptTerms := range conceptSets {
				key := subsystem + "\x00" + strings.Join(conceptTerms, "\x00") + "\x00" + behavior
				out[key] = reviewBehavioralConceptSet{subsystem: subsystem, terms: conceptTerms}
			}
		}
	}
	return out
}

func reviewFindingMatchesMultipleFamilyKeywords(finding, family string) bool {
	normalized := " " + normalizeReviewSynthesisFingerprintText(finding) + " "
	matches := 0
	for _, keyword := range reviewArchitecturalConceptKeywords[family] {
		if strings.Contains(normalized, " "+normalizeReviewSynthesisFingerprintText(keyword)+" ") {
			matches++
		}
	}
	return matches >= 2
}

func reviewBehavioralConceptSetsCompatible(left, right []string) bool {
	leftSet := sliceStringSet(left)
	rightSet := sliceStringSet(right)
	union := sliceStringSet(left)
	for _, term := range right {
		union[term] = struct{}{}
	}
	for term := range union {
		switch term {
		case "consistency-linearization", "durability-recovery", "invariant", "lifecycle-state", "ownership-boundary", "side-effect-publication":
		default:
			return false
		}
	}
	if stringSetIntersects(leftSet, rightSet) {
		return true
	}
	_, ownership := union["ownership-boundary"]
	_, publication := union["side-effect-publication"]
	_, durability := union["durability-recovery"]
	return ownership && publication && durability
}

func concreteReviewFindingArchitecturalTerms(finding string) map[string]struct{} {
	terms := reviewFindingArchitecturalTermSet([]string{finding})
	if len(terms) < 2 || !reviewFindingHasConcreteArchitecturalDescription(finding) {
		return nil
	}
	return terms
}

func reviewFindingHasConcreteArchitecturalDescription(finding string) bool {
	return reviewFindingBehavioralIdentity(finding) != ""
}

func reviewFindingBehavioralIdentity(finding string) string {
	description := finding
	for _, file := range reviewFilePathsFromText(finding) {
		description = strings.ReplaceAll(description, file, " ")
	}
	architecturalWords := map[string]struct{}{}
	for _, keywords := range reviewArchitecturalConceptKeywords {
		for _, keyword := range keywords {
			for _, word := range strings.Fields(normalizeReviewSynthesisFingerprintText(keyword)) {
				architecturalWords[word] = struct{}{}
			}
		}
	}
	behaviorWords := map[string]struct{}{}
	hasConcreteRelation := false
	for _, word := range strings.Fields(normalizeReviewSynthesisFingerprintText(description)) {
		if len(word) < 4 {
			continue
		}
		if _, architectural := architecturalWords[word]; architectural {
			continue
		}
		switch word {
		case "after", "again", "arbitrary", "because", "behavior", "behaviour", "continues", "despite", "fails",
			"failure", "finding", "issue", "problem", "review", "still", "thing", "works", "wrong":
			continue
		default:
			behaviorWords[word] = struct{}{}
			switch word {
			case "accepts", "bypasses", "conflicts", "diverges", "duplicates", "emits", "ignores", "inconsistent",
				"leaks", "loses", "missing", "moves", "precedes", "publishes", "reappears", "records", "rejects",
				"repeats", "resurrects", "skips", "violates":
				hasConcreteRelation = true
			}
		}
	}
	if len(behaviorWords) < 2 || !hasConcreteRelation {
		return ""
	}
	return strings.Join(sortedStringSet(behaviorWords), " ")
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

func isClearlyPreservedSafetyProperty(value string, originalEvidence []string) bool {
	normalized := normalizeReviewSynthesisFingerprintText(value)
	if reviewSafetyTextIsAmbiguous(normalized) || !isConcreteReviewReinterpretationText(value) ||
		!reviewSafetyTextIsAffirmative(normalized) || !reviewTextAlignsWithEvidence(value, originalEvidence) {
		return false
	}
	for _, source := range originalEvidence {
		if reviewSafetyTextEntails(source, value) {
			return true
		}
	}
	return false
}

func reviewSafetyTextIsAffirmative(value string) bool {
	normalized := normalizeReviewSynthesisFingerprintText(value)
	for _, phrase := range []string{
		" cannot ", " must ", " never ", " no ", " only ", " always ", " exactly once", " without ",
		"prevent", "reject", "deny", "remain", "preserv", "protect", "retain", "surviv", "visible",
	} {
		if strings.Contains(" "+normalized+" ", phrase) {
			return true
		}
	}
	return false
}

func reviewSafetyTextIsAmbiguous(value string) bool {
	normalized := normalizeReviewSynthesisFingerprintText(value)
	for _, phrase := range []string{
		"not guaranteed", "not required", "need not", "does not ensure", "do not ensure", "cannot ensure",
		"may not", "might not", "can fail", "may fail", "might fail", "can be violated", "may be violated",
		"best effort", "when possible", "where possible", "if possible", "to the extent possible",
		"subject to", "not always",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	for _, word := range strings.Fields(normalized) {
		switch word {
		case "can", "could", "may", "might", "should", "sometimes", "usually", "generally", "except", "unless":
			return true
		}
	}
	return false
}

func reviewSafetyPropertyAlignsAffirmatively(property string, evidence []string) bool {
	if !isClearlyPreservedSafetyProperty(property, evidence) {
		return false
	}
	for _, source := range evidence {
		if reviewSafetyTextEntails(source, property) {
			return true
		}
	}
	return false
}

type reviewSafetyRelation struct {
	subjects       []string
	hazards        []string
	safePredicates []string
	unsafeClauses  []string
}

var reviewSafetyRelations = []reviewSafetyRelation{
	{subjects: []string{"revoked grant", "revoked grants"}, hazards: []string{"used", "usable", "use"}, safePredicates: []string{"reject", "deny", "prevent"}, unsafeClauses: []string{"remain usable", "remains usable", "are usable", "is usable"}},
	{subjects: []string{"duplicate ownership", "multiple ownership"}, hazards: []string{"allowed", "accepted", "permit"}, safePredicates: []string{"reject", "deny", "prevent", "exclusive"}, unsafeClauses: []string{"ownership is allowed", "ownership remains allowed", "ownership is accepted"}},
	{subjects: []string{"stale authorization", "stale authorisation"}, hazards: []string{"accepted", "allowed", "permit", "access"}, safePredicates: []string{"reject", "deny", "prevent"}, unsafeClauses: []string{"authorization is accepted", "authorisation is accepted", "authorization is allowed", "authorisation is allowed"}},
	{subjects: []string{"deleted record", "deleted records"}, hazards: []string{"reappear", "resurrect", "restored"}, safePredicates: []string{"prevent", "reject", "deny"}, unsafeClauses: []string{"may reappear", "might reappear", "can reappear", "may resurrect", "might resurrect", "can resurrect"}},
}

type reviewSafetyPolarity int

const (
	reviewSafetyPolarityUnknown reviewSafetyPolarity = iota
	reviewSafetyPolarityProtected
	reviewSafetyPolarityUnsafe
)

func reviewSafetyTextEntails(source, candidate string) bool {
	if reviewSafetyTextIsAmbiguous(candidate) || !reviewRequirementsMateriallyOverlap(source, candidate) {
		return false
	}
	if !reviewSafetyCoreIsPreserved(source, candidate) {
		return false
	}
	sourceNormalized := normalizeReviewSynthesisFingerprintText(source)
	candidateNormalized := normalizeReviewSynthesisFingerprintText(candidate)
	matchedRelation := false
	for _, relation := range reviewSafetyRelations {
		sourcePolarity := reviewSafetyRelationPolarity(sourceNormalized, relation)
		if sourcePolarity == reviewSafetyPolarityUnknown {
			continue
		}
		matchedRelation = true
		if sourcePolarity != reviewSafetyPolarityProtected ||
			reviewSafetyRelationPolarity(candidateNormalized, relation) != reviewSafetyPolarityProtected {
			return false
		}
	}
	if reviewSafetyTextHasUnsafeRelation(candidateNormalized) {
		return false
	}
	if matchedRelation {
		return true
	}
	return reviewSafetyTextIsAffirmative(candidateNormalized)
}

func reviewSafetyCoreIsPreserved(source, candidate string) bool {
	sourceWords := traceableReviewRequirementWords(source)
	candidateWords := traceableReviewRequirementWords(candidate)
	for _, contextual := range []string{
		"after", "before", "corrected", "during", "enforce", "enforces", "guarantee", "guarantees",
		"intent", "publication", "recovery", "test", "tested", "tests", "transition", "verify", "verifies",
	} {
		delete(sourceWords, contextual)
	}
	if len(sourceWords) < 2 {
		return false
	}
	for word := range sourceWords {
		if _, ok := candidateWords[word]; !ok {
			return false
		}
	}
	return true
}

func reviewSafetyTextHasUnsafeRelation(value string) bool {
	for _, relation := range reviewSafetyRelations {
		if reviewSafetyRelationPolarity(value, relation) == reviewSafetyPolarityUnsafe {
			return true
		}
	}
	return false
}

func reviewSafetyRelationPolarity(value string, relation reviewSafetyRelation) reviewSafetyPolarity {
	if !reviewSafetyContainsAny(value, relation.subjects) {
		return reviewSafetyPolarityUnknown
	}
	if reviewSafetyContainsAny(value, relation.unsafeClauses) {
		return reviewSafetyPolarityUnsafe
	}
	hazard := reviewSafetyContainsAny(value, relation.hazards)
	safePredicate := reviewSafetyContainsAny(value, relation.safePredicates)
	denied := reviewSafetyContainsAny(value, []string{
		" cannot ", " can not ", " must not ", " never ", " not ", " no ", " reject", "deni", "prevent", "forbid",
	})
	permitted := reviewSafetyContainsAny(value, []string{
		" allow", " accept", " may ", " might ", " could ", " permit",
	})
	if hazard && permitted {
		return reviewSafetyPolarityUnsafe
	}
	if safePredicate || (hazard && denied) {
		return reviewSafetyPolarityProtected
	}
	if hazard {
		return reviewSafetyPolarityUnsafe
	}
	return reviewSafetyPolarityUnknown
}

func reviewSafetyContainsAny(value string, phrases []string) bool {
	padded := " " + value + " "
	for _, phrase := range phrases {
		if strings.Contains(padded, phrase) {
			return true
		}
	}
	return false
}

func reviewOriginalRequirementEvidence(requirements []agent.ReviewSynthesisRequirement) []string {
	var out []string
	for _, requirement := range requirements {
		out = append(out, requirement.Title, requirement.Task, requirement.ImplementationDetails, requirement.Context)
		out = append(out, requirement.AcceptanceCriteria...)
	}
	return trimBlankStrings(out)
}

func reviewOriginalAcceptanceCriteriaEvidence(requirements []agent.ReviewSynthesisRequirement) []string {
	var out []string
	for _, requirement := range requirements {
		out = append(out, requirement.AcceptanceCriteria...)
	}
	return trimBlankStrings(out)
}

func reviewPlatformConstraintEvidence(input agent.ReviewSynthesisInput) []string {
	out := reviewOriginalRequirementEvidence(input.OriginalRequirements)
	out = append(out, input.CurrentPRMetadata)
	out = append(out, input.RecentReviewComments...)
	out = append(out, input.WorkerNoOpVerdicts...)
	for _, cycle := range input.Cycles {
		out = append(out, cycle.ChunkCoverageSummary)
		for _, findings := range cycle.FindingsBySeverity {
			out = append(out, findings...)
		}
		for _, fix := range cycle.CompletedFixIssues {
			out = append(out, fix.Title, fix.Body, fix.ValidationStatus)
		}
	}
	for _, fix := range input.CompletedFixIssues {
		out = append(out, fix.Title, fix.Body, fix.ValidationStatus)
	}
	return trimBlankStrings(out)
}

func reviewTextAlignsWithEvidence(value string, evidence []string) bool {
	valueWords := traceableReviewRequirementWords(value)
	if len(valueWords) < 2 {
		return false
	}
	for _, source := range evidence {
		sourceWords := traceableReviewRequirementWords(source)
		matches := 0
		for word := range valueWords {
			if _, ok := sourceWords[word]; ok {
				matches++
			}
		}
		required := (len(valueWords) + 1) / 2
		if required < 2 {
			required = 2
		}
		if matches >= required {
			return true
		}
	}
	return false
}

func reviewRequirementsMateriallyOverlap(left, right string) bool {
	if reviewSafetyTextIsAmbiguous(left) || reviewSafetyTextIsAmbiguous(right) {
		return false
	}
	leftWords := traceableReviewRequirementWords(left)
	matches := 0
	for word := range traceableReviewRequirementWords(right) {
		if _, ok := leftWords[word]; ok {
			matches++
		}
	}
	return matches >= 2
}

func traceableReviewRequirementWords(value string) map[string]struct{} {
	out := map[string]struct{}{}
	for word := range canonicalReviewRequirementWords(normalizeReviewSynthesisFingerprintText(value)) {
		switch word {
		case "both", "cannot", "could", "from", "into", "remain", "remains", "requirement", "platform", "property",
			"recorded", "safety", "shall", "should", "their", "there", "these", "this", "those", "while", "with":
			continue
		case "visible":
			word = "visibility"
		case "records":
			word = "record"
		case "stores":
			word = "store"
		}
		if len(word) >= 4 {
			out[word] = struct{}{}
		}
	}
	return out
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
