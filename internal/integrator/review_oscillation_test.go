package integrator

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/prompt"
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
		{name: "configured floor includes nonmatching cycle with missing head", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c = prependCompletedNonmatchingCycle(c)
			c[0].HeadSHA = ""
			return c
		}, minCycles: 4, enabled: true},
		{name: "configured floor includes nonmatching cycle with reused head", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c = prependCompletedNonmatchingCycle(c)
			c[0].HeadSHA = c[1].HeadSHA
			return c
		}, minCycles: 4, enabled: true},
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
		{name: "unrelated concept remains a conservative candidate", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			c[3].FindingsBySeverity["MEDIUM"] = []string{
				"internal/integrator/review.go: spelling is inconsistent",
				"internal/integrator/review.go: comment needs punctuation",
			}
			return c
		}, minCycles: 3, enabled: true, want: true},
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
		{name: "generic architectural tokens remain a conservative candidate", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			for i := range c {
				c[i].FindingsBySeverity["MEDIUM"] = []string{
					"internal/integrator/review.go: durable repair race lock atomic",
					"internal/integrator/review_non_convergence.go: durable repair race lock atomic",
				}
			}
			return c
		}, minCycles: 3, enabled: true, want: true},
		{name: "unrelated findings in one package remain a conservative candidate", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			values := [][]string{
				{"internal/integrator/review.go: lifecycle transition publishes before ownership is stored"},
				{"internal/integrator/review.go: lifecycle transition loses durable recovery metadata"},
				{"internal/integrator/review.go: lifecycle transition races synchronization during cleanup"},
				{
					"internal/integrator/review.go: lifecycle transition violates an ownership boundary",
					"internal/integrator/review_non_convergence.go: lifecycle transition loses durable recovery metadata",
				},
			}
			for i := range c {
				c[i].FindingsBySeverity["MEDIUM"] = values[i]
			}
			return c
		}, minCycles: 3, enabled: true, want: true},
		{name: "alternating unrelated behaviors in one package remain a conservative candidate", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			values := [][]string{
				{"internal/integrator/review.go: ownership boundary publishes an external side effect before authority is recorded"},
				{"internal/integrator/review.go: lifecycle transition races synchronization during cleanup"},
				{"internal/integrator/review.go: ownership boundary publishes an external side effect before authority is recorded"},
				{
					"internal/integrator/review.go: lifecycle transition races synchronization during cleanup",
					"internal/worker/run.go: retry fixture detail remains stale",
				},
			}
			for i := range c {
				c[i].FindingsBySeverity["MEDIUM"] = values[i]
			}
			return c
		}, minCycles: 3, enabled: true, want: true},
		{name: "subsystem and architecture split across findings remains a conservative candidate", mutate: func(c []reviewHistoryCycle) []reviewHistoryCycle {
			for i := range c {
				c[i].FindingsBySeverity["MEDIUM"] = []string{
					"internal/integrator/review.go: parser drops the requested label",
					"state machine publication invariant violates ordering",
				}
			}
			return c
		}, minCycles: 3, enabled: true, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cycles := cloneReviewHistoryCycles(valid)
			got := analyzeLowVolumeReviewOscillation(test.mutate(cycles), test.minCycles, test.enabled)
			assert.Equal(t, test.want, got.Eligible)
			assert.NotEmpty(t, got.Rationale)
			if test.want {
				assert.Equal(t, []string{"internal/integrator"}, got.RecurringSubsystems)
				assert.True(t, got.DistinctHeadSHAsConfirmed)
				assert.True(t, got.CompletedFixChainConfirmed)
				assert.Len(t, got.EvidenceCycles, 3)
			}
		})
	}
}

func TestAnalyzeLowVolumeReviewOscillation_AlternatingBehavioralClusters(t *testing.T) {
	tests := []struct {
		name             string
		findingsPerCycle int
	}{
		{name: "two findings per review", findingsPerCycle: 2},
		{name: "three findings per review", findingsPerCycle: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligibility := analyzeLowVolumeReviewOscillation(alternatingLowVolumeOscillationCycles(test.findingsPerCycle), 3, true)

			require.True(t, eligibility.Eligible)
			assert.Equal(t, []string{"internal/integrator"}, eligibility.RecurringSubsystems)
			assert.Equal(t, []int{1, 2, 3}, reviewHistoryCycleNumbers(eligibility.EvidenceCycles))
			assert.True(t, eligibility.DistinctHeadSHAsConfirmed)
			assert.True(t, eligibility.CompletedFixChainConfirmed)
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
		{name: "affected package title", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle = "integrator package invariant"
		}, want: reviewSynthesisDecisionFallback},
		{name: "generic title", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle = "durability"
		}, want: reviewSynthesisDecisionFallback},
		{name: "cycle-derived title", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle = "cycle 39 publication invariant"
		}, want: reviewSynthesisDecisionFallback},
		{name: "chunk-derived title", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle = "chunk 1/9 publication invariant"
		}, want: reviewSynthesisDecisionFallback},
		{name: "coverage-derived title", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle = "diff coverage publication invariant"
		}, want: reviewSynthesisDecisionFallback},
		{name: "malformed path title", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RootCauseTitle = "internal/integrator/review.go"
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
		{name: "unknown cycle", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RecurringSymptoms[0].Cycles = []int{1, 2, 99}
		}, want: reviewSynthesisDecisionFallback},
		{name: "latest cycle is not historical evidence", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RecurringSymptoms[0].Cycles = []int{1, 2, 4}
		}, want: reviewSynthesisDecisionFallback},
		{name: "duplicate cycle within symptom", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RecurringSymptoms[0].Cycles = []int{1, 1, 2, 3}
		}, want: reviewSynthesisDecisionFallback},
		{name: "mixed valid and invalid cycles", mutate: func(r *agent.ReviewSynthesisResult) {
			r.RecurringSymptoms[0].Cycles = []int{1, 2, 99}
			r.RecurringSymptoms[1].Cycles = []int{1, 2, 3}
		}, want: reviewSynthesisDecisionFallback},
		{name: "unrelated subsystem", mutate: func(r *agent.ReviewSynthesisResult) {
			for i := range r.RecurringSymptoms {
				r.RecurringSymptoms[i].AffectedFiles = []string{"internal/worker/run.go"}
			}
		}, want: reviewSynthesisDecisionEscalate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			test.mutate(result)
			decision, reason := evaluateLowVolumeReviewSynthesis(result, validReviewSynthesisInput(), .75, eligibility)
			assert.Equal(t, test.want, decision)
			assert.NotEmpty(t, reason)
		})
	}
}

func TestValidateLowVolumeSynthesisProvenance(t *testing.T) {
	eligibility := analyzeLowVolumeReviewOscillation(lowVolumeOscillationCycles(), 3, true)
	require.True(t, eligibility.Eligible)
	tests := []struct {
		name      string
		mutate    func(*agent.ReviewSynthesisResult, *agent.ReviewSynthesisInput)
		wantValid bool
	}{
		{name: "valid omitted excerpts", mutate: func(*agent.ReviewSynthesisResult, *agent.ReviewSynthesisInput) {}, wantValid: true},
		{
			name: "valid exact excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:1:finding:0", Excerpt: "publication",
				}}
			},
			wantValid: true,
		},
		{
			name: "missing reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences = nil
			},
		},
		{
			name: "foreign reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences[0] = "issue:999:task"
			},
		},
		{
			name: "stale cycle reference",
			mutate: func(result *agent.ReviewSynthesisResult, input *agent.ReviewSynthesisInput) {
				input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
					ID: "cycle:99:finding:0", Kind: "review_finding", Cycle: 99, Excerpt: "stale finding",
				})
				result.RecurringSymptoms[0].EvidenceReferences[0] = "cycle:99:finding:0"
				result.RecurringSymptoms[0].Cycles = append(result.RecurringSymptoms[0].Cycles, 99)
			},
		},
		{
			name: "duplicate reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences = append(
					result.RecurringSymptoms[0].EvidenceReferences,
					result.RecurringSymptoms[0].EvidenceReferences[0],
				)
			},
		},
		{
			name: "latest review omitted",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences = result.RecurringSymptoms[0].EvidenceReferences[:3]
				result.RecurringSymptoms[0].Cycles = result.RecurringSymptoms[0].Cycles[:3]
			},
		},
		{
			name: "inexact excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:1:finding:0", Excerpt: "generated prose not in source",
				}}
			},
		},
		{
			name: "unknown excerpt reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:404:finding:0", Excerpt: "unknown",
				}}
			},
		},
		{
			name: "duplicate excerpt reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				excerpt := agent.ReviewSourceExcerpt{Reference: "cycle:1:finding:0", Excerpt: "publication"}
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{excerpt, excerpt}
			},
		},
		{
			name: "unreferenced excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "issue:1:criterion:0", Excerpt: "Exclusive ownership",
				}}
			},
		},
		{
			name: "empty excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:1:finding:0", Excerpt: " ",
				}}
			},
		},
		{
			name: "stale excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, input *agent.ReviewSynthesisInput) {
				input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
					ID: "cycle:99:finding:0", Kind: "review_finding", Cycle: 99, Excerpt: "stale finding",
				})
				result.RecurringSymptoms[0].EvidenceReferences = append(result.RecurringSymptoms[0].EvidenceReferences, "cycle:99:finding:0")
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:99:finding:0", Excerpt: "stale",
				}}
				result.RecurringSymptoms[0].Cycles = append(result.RecurringSymptoms[0].Cycles, 99)
			},
		},
		{
			name: "truncation marker excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, input *agent.ReviewSynthesisInput) {
				input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
					ID: "truncation:finding", Kind: "truncation_marker", Excerpt: "[TRUNCATED]",
				})
				result.RecurringSymptoms[0].EvidenceReferences = append(result.RecurringSymptoms[0].EvidenceReferences, "truncation:finding")
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "truncation:finding", Excerpt: "[TRUNCATED]",
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			input := validReviewSynthesisInput()
			test.mutate(result, &input)

			ok, reason := validateLowVolumeSynthesisProvenance(result, input, eligibility)

			assert.Equal(t, test.wantValid, ok)
			if test.wantValid {
				assert.Empty(t, reason)
			} else {
				assert.NotEmpty(t, reason)
			}
		})
	}
}

func TestLowVolumeReviewSynthesisInputUsesStableEvidenceWithoutDuplicateProse(t *testing.T) {
	eligibility := analyzeLowVolumeReviewOscillation(lowVolumeOscillationCycles(), 3, true)
	require.True(t, eligibility.Eligible)
	input := validReviewSynthesisInput()
	for cycle := 1; cycle <= 4; cycle++ {
		input.Cycles = append(input.Cycles, agent.ReviewSynthesisCycle{
			Cycle: cycle,
			FindingsBySeverity: map[string][]string{
				"MEDIUM": {"duplicated finding prose"},
			},
			CompletedFixIssues: []agent.ReviewSynthesisFixIssue{{
				Number: cycle + 100, Body: "duplicated completed fix body",
			}},
		})
	}

	bounded := lowVolumeReviewSynthesisInput(input, eligibility)

	assert.Empty(t, bounded.OriginalRequirements)
	require.Len(t, bounded.Cycles, 4)
	for _, cycle := range bounded.Cycles {
		assert.Empty(t, cycle.FindingsBySeverity)
	}
	for _, fix := range bounded.CompletedFixIssues {
		assert.Empty(t, fix.Body)
	}
	assert.Contains(t, bounded.EvidenceSources, agent.ReviewEvidenceSource{
		ID: "issue:1:criterion:0", Kind: "requirement_criterion",
		Excerpt: "Exclusive ownership remains user-visible during intent publication.",
	})
	assert.Contains(t, bounded.EvidenceSources, agent.ReviewEvidenceSource{
		ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1, Excerpt: "publication finding",
	})
}

func TestBoundedLowVolumeEvidenceSourcesPreservesLatestAndHistoricalCycles(t *testing.T) {
	eligibility := analyzeLowVolumeReviewOscillation(lowVolumeOscillationCycles(), 3, true)
	require.True(t, eligibility.Eligible)
	var sources []agent.ReviewEvidenceSource
	for cycle := 1; cycle <= 4; cycle++ {
		for finding := 0; finding < 4; finding++ {
			sources = append(sources, agent.ReviewEvidenceSource{
				ID: fmt.Sprintf("cycle:%d:finding:%d", cycle, finding), Kind: "review_finding", Cycle: cycle,
				Excerpt: strings.Repeat(fmt.Sprintf("cycle-%d ", cycle), 500),
			})
		}
	}

	bounded := boundedLowVolumeEvidenceSources(sources, eligibility)

	for cycle := 1; cycle <= 4; cycle++ {
		assert.Contains(t, bounded, agent.ReviewEvidenceSource{
			ID: fmt.Sprintf("cycle:%d:finding:0", cycle), Kind: "review_finding", Cycle: cycle,
			Excerpt: strings.Repeat(fmt.Sprintf("cycle-%d ", cycle), 500),
		})
	}
	assert.Contains(t, bounded, agent.ReviewEvidenceSource{
		ID: "truncation:finding", Kind: "truncation_marker",
		Excerpt: "[TRUNCATED: 10 additional finding evidence sources omitted by deterministic budget]",
	})
}

func TestBoundedReviewVerificationInputTieringAndBudget(t *testing.T) {
	input := agent.ReviewSynthesisInput{PRNumber: 7, BatchNumber: 9, HeadSHA: "current-head"}
	for requirement := 0; requirement < 8; requirement++ {
		input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
			ID: fmt.Sprintf("issue:7:criterion:%d", requirement), Kind: "requirement_criterion",
			Excerpt: strings.Repeat(fmt.Sprintf("requirement-%d ", requirement), 250),
		})
	}
	for cycle := 1; cycle <= 12; cycle++ {
		for finding := 0; finding < 2; finding++ {
			head := fmt.Sprintf("head-%d", cycle)
			if cycle == 12 {
				head = input.HeadSHA
			}
			input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
				ID: fmt.Sprintf("cycle:%d:finding:%d", cycle, finding), Kind: "review_finding", Cycle: cycle, HeadSHA: head,
				Excerpt: strings.Repeat(fmt.Sprintf("cycle-%d-finding-%d λ ", cycle, finding), 180),
			})
		}
	}
	synthesis := agent.ReviewSynthesisResult{
		ShouldEscalate: true,
		RequirementReinterpretation: &agent.ReviewRequirementReinterpretation{
			EvidenceReferences: []string{"issue:7:criterion:0", "cycle:1:finding:0"},
		},
	}

	bounded, err := boundedReviewVerificationInput(input, synthesis,
		[]string{"cycle:1:finding:0", "issue:7:criterion:0"}, true)

	require.NoError(t, err)
	assert.Positive(t, bounded.OmittedEvidenceCount)
	assert.Contains(t, bounded.EvidenceSources, input.EvidenceSources[0])
	for _, source := range input.EvidenceSources {
		if source.HeadSHA == input.HeadSHA {
			assert.Contains(t, bounded.EvidenceSources, source)
		}
		if strings.HasPrefix(source.Kind, "requirement_") {
			assert.Contains(t, bounded.EvidenceSources, source)
		}
	}
	rendered, err := prompt.RenderReviewVerificationPrompt(bounded)
	require.NoError(t, err)
	assert.True(t, utf8.ValidString(rendered))
	assert.Contains(t, rendered, "optional authoritative source(s) were omitted")
	assert.LessOrEqual(t, len(prompt.ReviewVerificationSystemPrompt)+2+len(rendered), prompt.ReviewVerificationPromptBudget)
}

func TestBoundedReviewVerificationInputRejectsOversizedMandatoryEvidence(t *testing.T) {
	input := agent.ReviewSynthesisInput{
		HeadSHA: "current-head",
		EvidenceSources: []agent.ReviewEvidenceSource{
			{ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1, Excerpt: strings.Repeat("cited ", 6000)},
			{ID: "cycle:2:finding:0", Kind: "review_finding", Cycle: 2, Excerpt: strings.Repeat("cited ", 6000)},
		},
	}
	synthesis := agent.ReviewSynthesisResult{
		ShouldEscalate: true,
		RecurringSymptoms: []agent.ReviewSynthesisSymptom{{
			EvidenceReferences: []string{"cycle:1:finding:0", "cycle:2:finding:0"},
		}},
	}

	bounded, err := boundedReviewVerificationInput(input, synthesis,
		[]string{"cycle:1:finding:0", "cycle:2:finding:0"}, false)

	assert.ErrorContains(t, err, "mandatory review verification evidence exceeds")
	assert.Empty(t, bounded.EvidenceSources)
}

func TestBoundedReviewVerificationInputLowVolumeRequiresCompleteRetainedEvidence(t *testing.T) {
	input := agent.ReviewSynthesisInput{
		HeadSHA: "current-head",
		EvidenceSources: []agent.ReviewEvidenceSource{
			{ID: "issue:7:criterion:0", Kind: "requirement_criterion", Excerpt: "Preserve publication safety."},
			{ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1, HeadSHA: "head-1", Excerpt: "A historical transition publishes too early."},
			{ID: "cycle:2:finding:0", Kind: "review_finding", Cycle: 2, HeadSHA: "current-head", Excerpt: "The cited current finding supports the proposed ordering gap."},
			{ID: "cycle:2:finding:1", Kind: "review_finding", Cycle: 2, HeadSHA: "current-head", Excerpt: "The uncited current finding contradicts the proposed root cause."},
			{ID: "truncation:finding", Kind: "truncation_marker", Excerpt: "[TRUNCATED]"},
		},
	}
	synthesis := agent.ReviewSynthesisResult{
		ShouldEscalate: true,
		RecurringSymptoms: []agent.ReviewSynthesisSymptom{{
			EvidenceReferences: []string{"cycle:1:finding:0", "cycle:2:finding:0"},
		}},
	}

	bounded, err := boundedReviewVerificationInput(input, synthesis,
		[]string{"cycle:1:finding:0", "cycle:2:finding:0"}, false)

	require.NoError(t, err)
	assert.Zero(t, bounded.OmittedEvidenceCount)
	assert.ElementsMatch(t, completeLowVolumeReviewVerificationEvidence(input.EvidenceSources), bounded.EvidenceSources)
	assert.Contains(t, bounded.EvidenceSources, input.EvidenceSources[3])
	assert.NotContains(t, bounded.EvidenceSources, input.EvidenceSources[4])
	rendered, err := prompt.RenderReviewVerificationPrompt(bounded)
	require.NoError(t, err)
	assert.Contains(t, rendered, "All eligible evidence is included.")
	assert.Contains(t, rendered, "uncited current finding contradicts")
}

func TestBoundedReviewVerificationInputLowVolumeRejectsOversizedUncitedEvidence(t *testing.T) {
	input := agent.ReviewSynthesisInput{
		HeadSHA: "current-head",
		EvidenceSources: []agent.ReviewEvidenceSource{
			{ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1, Excerpt: "cited historical evidence"},
			{ID: "cycle:2:finding:0", Kind: "review_finding", Cycle: 2, HeadSHA: "current-head", Excerpt: "cited current evidence"},
			{
				ID: "cycle:2:finding:1", Kind: "review_finding", Cycle: 2, HeadSHA: "current-head",
				Excerpt: strings.Repeat("uncited contradictory current evidence λ ", 4000),
			},
		},
	}
	synthesis := agent.ReviewSynthesisResult{
		ShouldEscalate: true,
		RecurringSymptoms: []agent.ReviewSynthesisSymptom{{
			EvidenceReferences: []string{"cycle:1:finding:0", "cycle:2:finding:0"},
		}},
	}

	bounded, err := boundedReviewVerificationInput(input, synthesis,
		[]string{"cycle:1:finding:0", "cycle:2:finding:0"}, false)

	assert.ErrorContains(t, err, "mandatory review verification evidence exceeds")
	assert.Empty(t, bounded.EvidenceSources)
}

func TestCompleteLowVolumeReviewVerificationEvidence(t *testing.T) {
	sources := []agent.ReviewEvidenceSource{
		{ID: "requirement", Kind: "requirement_task"},
		{ID: "finding", Kind: "review_finding"},
		{ID: "marker", Kind: "truncation_marker"},
	}

	assert.Equal(t, sources[:2], completeLowVolumeReviewVerificationEvidence(sources))
	assert.Empty(t, completeLowVolumeReviewVerificationEvidence(nil))
}

func TestRepresentativeReviewVerificationEvidenceUsesCycleBreadth(t *testing.T) {
	sources := []agent.ReviewEvidenceSource{
		{ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1},
		{ID: "cycle:1:finding:1", Kind: "review_finding", Cycle: 1},
		{ID: "cycle:2:finding:0", Kind: "review_finding", Cycle: 2},
		{ID: "cycle:2:finding:1", Kind: "review_finding", Cycle: 2},
		{ID: "cycle:3:finding:0", Kind: "review_finding", Cycle: 3},
		{ID: "cycle:3:finding:1", Kind: "review_finding", Cycle: 3},
	}

	ordered := representativeReviewVerificationEvidence(sources, map[string]struct{}{})

	require.Len(t, ordered, 6)
	assert.Equal(t, []string{
		"cycle:1:finding:0", "cycle:2:finding:0", "cycle:3:finding:0",
		"cycle:1:finding:1", "cycle:2:finding:1", "cycle:3:finding:1",
	}, []string{ordered[0].ID, ordered[1].ID, ordered[2].ID, ordered[3].ID, ordered[4].ID, ordered[5].ID})
}

func TestValidateHighVolumeSynthesisSourceExcerpts(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*agent.ReviewSynthesisResult, *agent.ReviewSynthesisInput)
		wantValid bool
	}{
		{name: "omitted excerpts", mutate: func(*agent.ReviewSynthesisResult, *agent.ReviewSynthesisInput) {}, wantValid: true},
		{
			name: "missing evidence references",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences = nil
			},
		},
		{
			name: "empty evidence references",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences = []string{}
			},
		},
		{
			name: "exact excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:1:finding:0", Excerpt: "publication",
				}}
			},
			wantValid: true,
		},
		{
			name: "missing reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences[0] = ""
			},
		},
		{
			name: "foreign reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences[0] = "issue:999:task"
			},
		},
		{
			name: "resolved non-finding reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences[0] = "issue:1:criterion:0"
			},
		},
		{
			name: "stale reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences[0] = "cycle:99:finding:0"
			},
		},
		{
			name: "duplicate reference",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].EvidenceReferences = append(
					result.RecurringSymptoms[0].EvidenceReferences,
					result.RecurringSymptoms[0].EvidenceReferences[0],
				)
			},
		},
		{
			name: "truncation marker reference",
			mutate: func(result *agent.ReviewSynthesisResult, input *agent.ReviewSynthesisInput) {
				input.EvidenceSources = append(input.EvidenceSources, agent.ReviewEvidenceSource{
					ID: "truncation:finding", Kind: "truncation_marker", Excerpt: "[TRUNCATED]",
				})
				result.RecurringSymptoms[0].EvidenceReferences[0] = "truncation:finding"
			},
		},
		{
			name: "unknown excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "cycle:99:finding:0", Excerpt: "unknown",
				}}
			},
		},
		{
			name: "unreferenced excerpt",
			mutate: func(result *agent.ReviewSynthesisResult, _ *agent.ReviewSynthesisInput) {
				result.RecurringSymptoms[0].SourceExcerpts = []agent.ReviewSourceExcerpt{{
					Reference: "issue:1:criterion:0", Excerpt: "Exclusive ownership",
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			input := validReviewSynthesisInput()
			test.mutate(result, &input)

			ok, reason := validateHighVolumeSynthesisSourceExcerpts(result, input)

			assert.Equal(t, test.wantValid, ok)
			if test.wantValid {
				assert.Empty(t, reason)
			} else {
				assert.NotEmpty(t, reason)
			}
		})
	}
}

func TestValidateReviewRequirementReinterpretation(t *testing.T) {
	valid := lowVolumeSynthesisResult()
	valid.RequirementReinterpretation = validReviewReinterpretation()
	ok, reason := validateReviewRequirementReinterpretation(valid)
	assert.True(t, ok)
	assert.Empty(t, reason)

	t.Run("omitted", func(t *testing.T) {
		ok, reason := validateReviewRequirementReinterpretation(lowVolumeSynthesisResult())
		assert.True(t, ok)
		assert.Empty(t, reason)
	})
	for _, kind := range []agent.ReviewRequirementConstraintKind{
		agent.ReviewRequirementOverConstrained,
		agent.ReviewRequirementInternallyConflicting,
		agent.ReviewRequirementPlatformNonAtomic,
	} {
		t.Run("allowed kind "+string(kind), func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			result.RequirementReinterpretation = validReviewReinterpretation()
			result.RequirementReinterpretation.ConstraintKind = kind
			ok, reason := validateReviewRequirementReinterpretation(result)
			assert.True(t, ok)
			assert.Empty(t, reason)
		})
	}

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
		{name: "missing linearization", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.LinearizationBoundaries = nil
		}},
		{name: "missing durability", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.DurabilityBoundaries = nil
		}},
		{name: "missing evidence references", mutate: func(v *agent.ReviewRequirementReinterpretation, _ *agent.ReviewSynthesisResult) {
			v.EvidenceReferences = nil
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

func TestValidateReviewRequirementReinterpretation_AcceptsConcreteTextWithoutSemanticJudgment(t *testing.T) {
	tests := []struct {
		name     string
		property string
	}{
		{name: "revoked grants", property: "revoked grants cannot be used"},
		{name: "deletion non resurrection", property: "deleted records never reappear after recovery"},
		{name: "stale authorization", property: "stale authorization is rejected before access"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			result.RequirementReinterpretation = validReviewReinterpretation()
			result.RequirementReinterpretation.PreservedSafetyProperty = test.property

			ok, reason := validateReviewRequirementReinterpretation(result)

			assert.True(t, ok, reason)
			assert.Empty(t, reason)
		})
	}
}

func TestValidateLowVolumeSynthesisProvenance_RequirementSourceExcerpts(t *testing.T) {
	eligibility := analyzeLowVolumeReviewOscillation(lowVolumeOscillationCycles(), 3, true)
	require.True(t, eligibility.Eligible)
	tests := []struct {
		name      string
		excerpts  []agent.ReviewSourceExcerpt
		wantValid bool
	}{
		{name: "omitted", wantValid: true},
		{
			name: "exact",
			excerpts: []agent.ReviewSourceExcerpt{{
				Reference: "issue:1:criterion:0", Excerpt: "Exclusive ownership",
			}},
			wantValid: true,
		},
		{
			name: "unknown",
			excerpts: []agent.ReviewSourceExcerpt{{
				Reference: "issue:999:criterion:0", Excerpt: "unknown",
			}},
		},
		{
			name: "duplicate",
			excerpts: []agent.ReviewSourceExcerpt{
				{Reference: "issue:1:criterion:0", Excerpt: "Exclusive ownership"},
				{Reference: "issue:1:criterion:0", Excerpt: "ownership remains"},
			},
		},
		{
			name: "unreferenced",
			excerpts: []agent.ReviewSourceExcerpt{{
				Reference: "cycle:2:finding:0", Excerpt: "recovery",
			}},
		},
		{
			name: "empty",
			excerpts: []agent.ReviewSourceExcerpt{{
				Reference: "issue:1:criterion:0", Excerpt: "",
			}},
		},
		{
			name: "inexact",
			excerpts: []agent.ReviewSourceExcerpt{{
				Reference: "issue:1:criterion:0", Excerpt: "invented requirement prose",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			result.RequirementReinterpretation = validReviewReinterpretation()
			result.RequirementReinterpretation.SourceExcerpts = test.excerpts

			ok, reason := validateLowVolumeSynthesisProvenance(result, validReviewSynthesisInput(), eligibility)

			assert.Equal(t, test.wantValid, ok)
			if test.wantValid {
				assert.Empty(t, reason)
			} else {
				assert.NotEmpty(t, reason)
			}
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

func TestBuildLowVolumeSynthesizedStrategyFixIssueTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{name: "valid architectural title", title: "state machine publication invariant", want: "Review strategy fix: state machine publication invariant"},
		{name: "arbitrary package path is not rescued", title: "internal/state", want: "Review strategy fix: internal/state"},
		{name: "package path plus generic label is not rescued", title: "pkg/scheduler invariant", want: "Review strategy fix: pkg/scheduler invariant"},
		{name: "affected package name is not rescued", title: "integrator package invariant", want: "Review strategy fix: integrator package invariant"},
		{name: "arbitrary package name plus generic label is not rescued", title: "scheduler invariant", want: "Review strategy fix: scheduler invariant"},
		{name: "verified specific title is not lexically classified", title: "scheduler durability invariant", want: "Review strategy fix: scheduler durability invariant"},
		{name: "generic architectural label is not rescued", title: "durability", want: "Review strategy fix: durability"},
		{name: "valid specific cause", title: "durability ordering gap", want: "Review strategy fix: durability ordering gap"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := lowVolumeSynthesisResult()
			result.RootCauseTitle = test.title
			assert.Equal(t, test.want, buildLowVolumeSynthesizedStrategyFixIssueTitle(result))
		})
	}

	assert.Equal(t, "Review strategy fix (cycle 39): state machine publication invariant",
		buildSynthesizedStrategyFixIssueTitle(39, lowVolumeSynthesisResult()))
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

func alternatingLowVolumeOscillationCycles(findingsPerCycle int) []reviewHistoryCycle {
	aspects := []string{
		"ownership boundary publishes an external side effect before authority is recorded",
		"durable recovery repair loses the crash marker for the attempted transition",
		"ownership boundary publishes an external side effect before authority is recorded",
		"durable recovery repair loses the crash marker for the attempted transition",
	}
	var cycles []reviewHistoryCycle
	for i, aspect := range aspects {
		findings := []string{"internal/integrator/review.go: " + aspect}
		for j := 1; j < findingsPerCycle; j++ {
			findings = append(findings, "internal/worker/run.go: retry fixture detail "+string(rune('a'+i))+string(rune('a'+j)))
		}
		cycle := reviewHistoryCycle{
			Cycle: i + 1, HeadSHA: "alternating-head-" + string(rune('a'+i)),
			FindingsAfterDedupe: len(findings), FindingsBySeverity: map[string][]string{"MEDIUM": findings},
		}
		if i < len(aspects)-1 {
			cycle.FixIssues = []reviewHistoryFixIssue{{Number: 200 + i, StatusLabel: issues.StatusDone}}
		}
		cycles = append(cycles, cycle)
	}
	return cycles
}

func reviewHistoryCycleNumbers(cycles []reviewHistoryCycle) []int {
	out := make([]int, 0, len(cycles))
	for _, cycle := range cycles {
		out = append(out, cycle.Cycle)
	}
	return out
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

func prependCompletedNonmatchingCycle(in []reviewHistoryCycle) []reviewHistoryCycle {
	first := reviewHistoryCycle{
		Cycle:               0,
		HeadSHA:             "head-floor",
		FindingsAfterDedupe: 1,
		FindingsBySeverity:  map[string][]string{"MEDIUM": {"internal/worker/run.go: retry delay is too short"}},
		FixIssues:           []reviewHistoryFixIssue{{Number: 99, StatusLabel: issues.StatusDone}},
	}
	return append([]reviewHistoryCycle{first}, in...)
}

func lowVolumeSynthesisResult() *agent.ReviewSynthesisResult {
	return &agent.ReviewSynthesisResult{
		ShouldEscalate: true, Confidence: .95,
		RootCauseTitle:   "state machine publication invariant",
		RootCauseSummary: "The lifecycle state transition publishes side effects before the ownership invariant is durable.",
		ProposedStrategy: "Define one linearized lifecycle transition and durable recovery state.",
		RecurringSymptoms: []agent.ReviewSynthesisSymptom{
			{Description: "Publication precedes the durable state transition", Cycles: []int{1, 2, 3, 4}, AffectedFiles: []string{"internal/integrator/review.go"}, EvidenceReferences: []string{"cycle:1:finding:0", "cycle:2:finding:0", "cycle:3:finding:0", "cycle:4:finding:0"}},
			{Description: "Recovery sees an inconsistent lifecycle state", Cycles: []int{1, 2, 3}, AffectedFiles: []string{"internal/integrator/review_non_convergence.go"}, EvidenceReferences: []string{"cycle:1:finding:0", "cycle:2:finding:0", "cycle:3:finding:0"}},
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
		CorrectedInvariant:            "intent visibility precedes external publication while exclusive ownership remains user-visible",
		LinearizationBoundaries:       []string{"intent creation commit", "external visibility marker"},
		DurabilityBoundaries:          []string{"intent record persisted", "recovery state completed"},
		EvidenceReferences:            []string{"issue:1:criterion:0", "cycle:1:finding:0"},
	}
}

func validReviewSynthesisInput() agent.ReviewSynthesisInput {
	return agent.ReviewSynthesisInput{
		EvidenceSources: []agent.ReviewEvidenceSource{
			{ID: "issue:1:criterion:0", Kind: "requirement_criterion", Excerpt: "Exclusive ownership remains user-visible during intent publication."},
			{ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1, Excerpt: "publication finding"},
			{ID: "cycle:2:finding:0", Kind: "review_finding", Cycle: 2, Excerpt: "recovery finding"},
			{ID: "cycle:3:finding:0", Kind: "review_finding", Cycle: 3, Excerpt: "publication finding"},
			{ID: "cycle:4:finding:0", Kind: "review_finding", Cycle: 4, Excerpt: "current review finding"},
		},
		OriginalRequirements: []agent.ReviewSynthesisRequirement{
			{
				IssueNumber: 1,
				Title:       "Atomic intent publication",
				Task:        "Atomically publish both independent records even though the platform stores cannot commit atomically.",
				AcceptanceCriteria: []string{
					"Exclusive ownership remains user-visible during intent publication.",
				},
			},
		},
	}
}
