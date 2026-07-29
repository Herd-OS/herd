package integrator

import (
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/issues"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyzeLowVolumeReviewOscillation_Prerequisites(t *testing.T) {
	valid := lowVolumeOscillationCycles()
	tests := []struct {
		name      string
		mutate    func([]reviewHistoryCycle) []reviewHistoryCycle
		minCycles int
		enabled   bool
		want      bool
	}{
		{name: "eligible", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle { return c }, minCycles: 3, enabled: true, want: true},
		{name: "synthesis disabled", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle { return c }, minCycles: 3},
		{name: "configured floor", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle { return c }, minCycles: 4, enabled: true},
		{name: "fewer than three completed", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle { return c[1:] }, minCycles: 3, enabled: true},
		{name: "missing fix chain", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[1].FixIssues = nil
			return c
		}, minCycles: 3, enabled: true},
		{name: "gap before latest review", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[2].FixIssues = nil
			return c
		}, minCycles: 3, enabled: true},
		{name: "out of order cycles", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[1].Cycle, c[2].Cycle = c[2].Cycle, c[1].Cycle
			return c
		}, minCycles: 3, enabled: true},
		{name: "duplicate cycle numbers", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[2].Cycle = c[1].Cycle
			return c
		}, minCycles: 3, enabled: true},
		{name: "missing head", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[0].HeadSHA = ""
			return c
		}, minCycles: 3, enabled: true},
		{name: "reused head", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[2].HeadSHA = c[1].HeadSHA
			return c
		}, minCycles: 3, enabled: true},
		{name: "one latest finding", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[3].FindingsBySeverity["MEDIUM"] = c[3].FindingsBySeverity["MEDIUM"][:1]
			return c
		}, minCycles: 3, enabled: true},
		{name: "high volume reserved", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			for i := 0; i < 8; i++ {
				c[3].FindingsBySeverity["MEDIUM"] = append(c[3].FindingsBySeverity["MEDIUM"], "internal/integrator/review.go: state transition invariant variant "+string(rune('a'+i)))
			}
			return c
		}, minCycles: 3, enabled: true},
		{name: "unrelated concept", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[3].FindingsBySeverity["MEDIUM"] = []string{
				"internal/integrator/review.go: spelling is inconsistent",
				"internal/integrator/review.go: comment needs punctuation",
			}
			return c
		}, minCycles: 3, enabled: true},
		{name: "broad directory", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			for i := range c {
				c[i].FindingsBySeverity["MEDIUM"] = []string{"docs/guide.md: state machine invariant is violated", "docs/readme.md: lifecycle transition is invalid"}
			}
			return c
		}, minCycles: 3, enabled: true},
		{name: "metadata and dismissal", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[3].FindingsBySeverity["MEDIUM"] = []string{"Files reviewed: 3", "No issue found"}
			return c
		}, minCycles: 3, enabled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cycles := cloneReviewHistoryCycles(valid)
			got := analyzeLowVolumeReviewOscillation(test.mutate(cycles), test.minCycles, test.enabled)
			assert.Equal(t, test.want, got.Eligible)
			assert.NotEmpty(t, got.Rationale)
			if test.want {
				assert.Equal(t, []string{"internal/integrator"}, got.RecurringSubsystems)
				assert.Contains(t, got.RecurringArchitecturalTerms, "lifecycle-state")
				assert.True(t, got.DistinctHeadSHAsConfirmed)
				assert.True(t, got.CompletedFixChainConfirmed)
				assert.Len(t, got.EvidenceCycles, 3)
			}
		})
	}
}

func TestEvaluateLowVolumeReviewSynthesis(t *testing.T) {
	eligibility := analyzeLowVolumeReviewOscillation(lowVolumeOscillationCycles(), 3, true)
	require.True(t, eligibility.Eligible)
	tests := []struct {
		name   string
		mutate func(*agent.ReviewSynthesisResult)
		want   reviewSynthesisDecision
	}{
		{name: "valid", mutate: func(*agent.ReviewSynthesisResult) {}, want: reviewSynthesisDecisionEscalate},
		{name: "low confidence", mutate: func(r *agent.ReviewSynthesisResult) { r.Confidence = .2 }, want: reviewSynthesisDecisionFallback},
		{name: "generic root", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle, r.RootCauseSummary, r.ProposedStrategy = "review workflow", "", ""
		}, want: reviewSynthesisDecisionFallback},
		{name: "missing files", mutate: func(r *agent.ReviewSynthesisResult) {
			for i := range r.RecurringSymptoms {
				r.RecurringSymptoms[i].AffectedFiles = nil
			}
		}, want: reviewSynthesisDecisionFallback},
		{name: "fewer than three cycles", mutate: func(r *agent.ReviewSynthesisResult) {
			for i := range r.RecurringSymptoms {
				r.RecurringSymptoms[i].Cycles = []int{1, 2}
			}
		}, want: reviewSynthesisDecisionFallback},
		{name: "unrelated subsystem", mutate: func(r *agent.ReviewSynthesisResult) {
			for i := range r.RecurringSymptoms {
				r.RecurringSymptoms[i].AffectedFiles = []string{"internal/worker/run.go"}
			}
		}, want: reviewSynthesisDecisionFallback},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			test.mutate(result)
			decision, reason := evaluateLowVolumeReviewSynthesis(result, .75, eligibility)
			assert.Equal(t, test.want, decision)
			assert.NotEmpty(t, reason)
		})
	}
}

func TestValidateReviewRequirementReinterpretation(t *testing.T) {
	valid := lowVolumeSynthesisResult()
	valid.RequirementReinterpretation = validReviewReinterpretation()
	ok, reason := validateReviewRequirementReinterpretation(valid)
	assert.True(t, ok)
	assert.Empty(t, reason)

	tests := []struct {
		name   string
		mutate func(*agent.ReviewRequirementReinterpretation, *agent.ReviewSynthesisResult)
	}{
		{name: "unknown kind", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.ConstraintKind = "other"
		}},
		{name: "blank scalar", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.CorrectedInvariant = ""
		}},
		{name: "equivalent invariant", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.CorrectedInvariant = v.ConflictingRequirement
		}},
		{name: "missing linearization", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.LinearizationBoundaries = nil
		}},
		{name: "missing durability", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.DurabilityBoundaries = nil
		}},
		{name: "generic safety", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.PreservedSafetyProperty = "good behavior"
		}},
		{name: "criteria omit safety", mutate: func(_ *agent.ReviewRequirementReinterpretation, r *agent.ReviewSynthesisResult) {
			r.AcceptanceCriteria = []string{"The intent visibility invariant is tested.", "Recovery is tested."}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			result.RequirementReinterpretation = validReviewReinterpretation()
			test.mutate(result.RequirementReinterpretation, result)
			ok, reason := validateReviewRequirementReinterpretation(result)
			assert.False(t, ok)
			assert.NotEmpty(t, reason)
		})
	}
}

func TestLowVolumeTitleBodyAndFingerprint(t *testing.T) {
	result := lowVolumeSynthesisResult()
	assert.Equal(t, "Review strategy fix: state machine publication invariant", buildLowVolumeSynthesizedStrategyFixIssueTitle(result))
	assert.NotContains(t, buildLowVolumeSynthesizedStrategyFixIssueTitle(result), "cycle")

	before := synthesizedReviewStrategyFingerprint(result)
	result.RequirementReinterpretation = validReviewReinterpretation()
	after := synthesizedReviewStrategyFingerprint(result)
	assert.NotEqual(t, before, after)
	details := buildSynthesizedStrategyImplementationDetails(result, agent.ReviewSynthesisInput{}, nil)
	assert.Contains(t, details, "Requirement reinterpretation")
	assert.Contains(t, details, "Linearization boundaries")
	assert.Contains(t, details, "Durability boundaries")
}

func lowVolumeOscillationCycles() []reviewHistoryCycle {
	findings := [][]string{
		{"internal/integrator/review.go: lifecycle state transition can publish before ownership is recorded"},
		{"internal/integrator/review.go: state machine moves to done before publication becomes visible"},
		{"internal/integrator/review_non_convergence.go: terminal state transition violates the publication invariant"},
		{
			"internal/integrator/review.go: publication lifecycle transition loses the durable state invariant",
			"internal/integrator/review_non_convergence.go: state machine visibility is inconsistent after handoff",
		},
	}
	var cycles []reviewHistoryCycle
	for i, values := range findings {
		cycle := reviewHistoryCycle{
			Cycle: i + 1, HeadSHA: "head-" + string(rune('a'+i)),
			FindingsAfterDedupe: len(values), FindingsBySeverity: map[string][]string{"MEDIUM": values},
		}
		if i < 3 {
			cycle.FixIssues = []reviewHistoryFixIssue{{Number: 100 + i, StatusLabel: issues.StatusDone}}
		}
		cycles = append(cycles, cycle)
	}
	return cycles
}

func cloneReviewHistoryCycles(in []reviewHistoryCycle) []reviewHistoryCycle {
	out := make([]reviewHistoryCycle, len(in))
	for i, cycle := range in {
		out[i] = cycle
		out[i].FixIssues = append([]reviewHistoryFixIssue(nil), cycle.FixIssues...)
		out[i].FindingsBySeverity = copyReviewFindingsBySeverity(cycle.FindingsBySeverity)
	}
	return out
}

func lowVolumeSynthesisResult() *agent.ReviewSynthesisResult {
	return &agent.ReviewSynthesisResult{
		ShouldEscalate: true, Confidence: .95,
		RootCauseTitle:   "state machine publication invariant",
		RootCauseSummary: "The lifecycle state transition publishes side effects before the ownership invariant is durable.",
		ProposedStrategy: "Define one linearized lifecycle transition and durable recovery state.",
		RecurringSymptoms: []agent.ReviewSynthesisSymptom{
			{Description: "Publication precedes the durable state transition", Cycles: []int{1, 2, 3}, AffectedFiles: []string{"internal/integrator/review.go"}},
			{Description: "Recovery sees an inconsistent lifecycle state", Cycles: []int{1, 2, 3}, AffectedFiles: []string{"internal/integrator/review_non_convergence.go"}},
		},
		AcceptanceCriteria: []string{
			"The intent visibility corrected invariant is enforced and tested.",
			"Exclusive ownership safety remains user-visible and is tested.",
		},
	}
}

func validReviewReinterpretation() *agent.ReviewRequirementReinterpretation {
	return &agent.ReviewRequirementReinterpretation{
		ConstraintKind:                agent.ReviewRequirementOverConstrained,
		ConflictingRequirement:        "atomically publish both independent records",
		PlatformConsistencyConstraint: "the platform stores cannot commit atomically",
		PreservedSafetyProperty:       "exclusive ownership remains user-visible",
		CorrectedInvariant:            "intent visibility precedes external publication",
		LinearizationBoundaries:       []string{"intent creation commit", "external visibility marker"},
		DurabilityBoundaries:          []string{"intent record persisted", "recovery state completed"},
	}
}
