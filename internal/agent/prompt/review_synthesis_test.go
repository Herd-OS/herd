package prompt

import (
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderReviewSynthesisPrompt_RequiredSectionsAndEvidence(t *testing.T) {
	input := agent.ReviewSynthesisInput{
		PRNumber:             17,
		BatchNumber:          116,
		HeadSHA:              "abc123",
		HeadRef:              "feature/review-synthesis",
		CurrentPRMetadata:    "mergeable: false",
		RecentReviewComments: []string{"cycle 4 review comment"},
		OriginalRequirements: []agent.ReviewSynthesisRequirement{{
			IssueNumber:           1121,
			Title:                 "Preserve ownership",
			Task:                  "Keep a single owner through recovery.",
			ImplementationDetails: "Use the shared state machine.",
			AcceptanceCriteria:    []string{"Concurrent grants remain exclusive."},
			Context:               "The platform commits stores independently.",
		}},
		PriorStrategyFixIssues: []agent.ReviewSynthesisStrategyFixIssue{{
			Number:      77,
			Cycle:       3,
			Title:       "Serialize grants",
			StatusLabel: "status:done",
			State:       "closed",
			Fingerprint: "strategy-77",
			HeadSHA:     "before-abc123",
			Summary:     "Moved serialization to the durable store.",
		}},
		Cycles: []agent.ReviewSynthesisCycle{
			{
				Cycle:                3,
				HeadSHA:              "before-abc123",
				FindingsAfterDedupe:  1,
				FindingsBySeverity:   map[string][]string{"HIGH": {"ownership race before fix"}},
				FixIssueNumbers:      []int{90},
				Status:               "changes_requested",
				AffectedFiles:        []string{"internal/foo.go"},
				ChunkCoverageSummary: "2 chunks reviewed",
				CompletedFixIssues: []agent.ReviewSynthesisFixIssue{{
					Number:           90,
					Title:            "Fix ownership race",
					Body:             "cycle 3 worker changed the shared state machine",
					StatusLabel:      "status:done",
					FilesSummary:     []string{"internal/foo.go"},
					ValidationStatus: "validated-cycle-3",
					WorkerReport:     true,
				}},
			},
			{
				Cycle:               4,
				HeadSHA:             "abc123",
				FindingsAfterDedupe: 2,
				FindingsBySeverity:  map[string][]string{"HIGH": {"moved durability symptom after fix"}},
				FixIssueNumbers:     []int{91},
				Status:              "changes_requested",
				AffectedFiles:       []string{"internal/foo.go"},
			},
		},
		CompletedFixIssues: []agent.ReviewSynthesisFixIssue{{
			Number:           91,
			Title:            "Fix repeated review finding",
			Body:             "worker fixed one symptom",
			StatusLabel:      "status:done",
			FilesSummary:     []string{"internal/foo.go"},
			ValidationStatus: "validated",
			WorkerReport:     true,
		}},
		WorkerNoOpVerdicts: []string{"worker says no-op for stale symptom"},
		AffectedFiles:      []string{"internal/foo.go", "internal/bar"},
	}

	rendered, err := RenderReviewSynthesisPrompt(input, agent.ReviewSynthesisOptions{SystemPrompt: "prefer repo invariants"})
	require.NoError(t, err)

	for _, want := range []string{
		"## Current PR Metadata",
		"## Recent Review Result Comments",
		"## Original Batch Requirements",
		"## Prior Strategy Fix Attempts",
		"## Deduplicated Findings By Cycle",
		"## Completed Review-Fix Outcomes",
		"## Worker No-Op Verdicts",
		"## Affected Files/Packages",
		"## Output Contract",
		"Do not invent issues",
		"Only group findings supported by the current review history",
		"Distinguish repeated symptoms from one root cause",
		"Prefer no escalation when evidence is weak",
		"Return strict JSON only",
		"abc123",
		"feature/review-synthesis",
		"mergeable: false",
		"cycle 4 review comment",
		"Keep a single owner through recovery.",
		"Concurrent grants remain exclusive.",
		"Moved serialization to the durable store.",
		"ownership race before fix",
		"validated-cycle-3",
		"moved durability symptom after fix",
		"#91",
		"worker says no-op for stale symptom",
		"prefer repo invariants",
	} {
		assert.Contains(t, rendered, want)
	}
	assert.NotContains(t, rendered, "cycle 3 worker changed the shared state machine")

	cycle3 := strings.Index(rendered, "### Cycle 3")
	cycle3Fix := strings.Index(rendered, "#### Fix issue #90")
	cycle4 := strings.Index(rendered, "### Cycle 4")
	require.NotEqual(t, -1, cycle3)
	require.NotEqual(t, -1, cycle3Fix)
	require.NotEqual(t, -1, cycle4)
	assert.Less(t, cycle3, cycle3Fix)
	assert.Less(t, cycle3Fix, cycle4)
}

func TestReviewSynthesisPrompt_StrictJSONExamplesAndToolBan(t *testing.T) {
	for _, want := range []string{
		`"should_escalate": true`,
		`"root_cause_title"`,
		`"recurring_symptoms"`,
		`"why_individual_fixes_are_not_converging"`,
		`"acceptance_criteria"`,
		`"non_goals"`,
		`"should_escalate": false`,
		`"reason"`,
		`"requirement_reinterpretation"`,
		`"constraint_kind": "platform_non_atomic"`,
		"Do NOT use any tools",
		"Do NOT create issues, comments, files, or pull requests",
		"Do NOT mutate repository or GitHub state",
	} {
		assert.Contains(t, ReviewSynthesisSystemPrompt+"\n"+ReviewSynthesisPromptTemplate, want)
	}
}

func TestReviewSynthesisPrompt_EvidenceAndCorrectnessGuardrails(t *testing.T) {
	tests := []struct {
		name  string
		wants []string
	}{
		{
			name: "rejects generic and synthetic evidence",
			wants: []string{
				"generic metadata",
				"broad directory coincidence",
				"unrelated findings",
				"empty findings",
				"coverage/chunk headings",
				"generated summaries",
				"no-issue/approved verdicts",
				"Chunk labels and coverage bookkeeping are context only",
				`"Chunk 1/9"`,
				`"Diff Coverage"`,
				`"Review Aggregation"`,
				`"Source: local-git"`,
			},
		},
		{
			name: "recognizes semantic non-convergence",
			wants: []string{
				"alternating fixes",
				"symptoms that move between components",
				"shared state machines",
				"lifecycle, synchronization, durability, ownership, or linearization boundaries",
				"need not use identical text",
				"incompatible behaviors",
				"coherent, concrete subsystem",
			},
		},
		{
			name: "prevents silent correctness weakening",
			wants: []string{
				"Omit requirement_reinterpretation for ordinary synthesis",
				"merely difficult, expensive, or repeatedly implemented incorrectly",
				"never a general license to relax difficult acceptance criteria",
				"silently weaken correctness",
				"every required nested field and evidence_references are mandatory",
				"preserve a user-visible safety property",
				"materially different corrected invariant",
				"explicit, non-empty linearization and durability boundary lists",
				"test both the corrected invariant and the preserved safety property",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.wants {
				assert.Contains(t, ReviewSynthesisPromptTemplate, want)
			}
		})
	}
}

func TestRenderReviewSynthesisPrompt_DeterministicBudgetPreservesPriorityEvidence(t *testing.T) {
	input := agent.ReviewSynthesisInput{
		PRNumber: 1, BatchNumber: 2, HeadSHA: "head",
		EvidenceSources: []agent.ReviewEvidenceSource{
			{ID: "issue:10:criterion:0", Kind: "requirement_criterion", Excerpt: "Revoked grants cannot be used."},
			{ID: "cycle:4:finding:0", Kind: "review_finding", Cycle: 4, Excerpt: "internal/state/recovery.go: stale authorization survives recovery"},
		},
		RecentReviewComments: []string{strings.Repeat("older nonessential prose ", 10000)},
		CurrentPRMetadata:    strings.Repeat("nonessential metadata ", 10000),
	}

	first, err := RenderReviewSynthesisPrompt(input, agent.ReviewSynthesisOptions{})
	require.NoError(t, err)
	second, err := RenderReviewSynthesisPrompt(input, agent.ReviewSynthesisOptions{})
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Len(t, first, ReviewSynthesisInputBudget)
	assert.Contains(t, first, "issue:10:criterion:0")
	assert.Contains(t, first, "Revoked grants cannot be used.")
	assert.Contains(t, first, "cycle:4:finding:0")
	assert.Contains(t, first, "stale authorization survives recovery")
	assert.Contains(t, first, ReviewPromptTruncationMarker)
}

func TestParseReviewSynthesisOutput(t *testing.T) {
	tests := []struct {
		name           string
		output         string
		wantEscalate   bool
		wantConfidence float64
		wantReason     string
		wantSymptoms   int
	}{
		{
			name:           "escalation with surrounding text",
			output:         `text before {"should_escalate":true,"confidence":0.91,"root_cause_title":"Invariant missing","root_cause_summary":"same root cause","recurring_symptoms":[{"description":"same symptom","cycles":[2,3],"affected_files":["internal/foo.go"],"evidence_references":["cycle:2:finding:0"]}],"why_individual_fixes_are_not_converging":"fixes are too narrow","proposed_strategy":"fix shared helper","acceptance_criteria":["covers cycles"],"non_goals":["unrelated review"]} text after`,
			wantEscalate:   true,
			wantConfidence: 0.91,
			wantSymptoms:   1,
		},
		{
			name:           "no escalation",
			output:         `{"should_escalate":false,"confidence":0.66,"reason":"evidence is weak"}`,
			wantEscalate:   false,
			wantConfidence: 0.66,
			wantReason:     "evidence is weak",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReviewSynthesisOutput(tc.output)
			require.NoError(t, err)
			assert.Equal(t, tc.wantEscalate, got.ShouldEscalate)
			assert.Equal(t, tc.wantConfidence, got.Confidence)
			assert.Equal(t, tc.wantReason, got.Reason)
			assert.Len(t, got.RecurringSymptoms, tc.wantSymptoms)
		})
	}
}

func TestParseReviewSynthesisOutput_InvalidJSONReturnsError(t *testing.T) {
	tests := []string{
		"",
		"not json and long enough to be parse attempted",
		`{"should_escalate":true,`,
	}

	for _, output := range tests {
		t.Run(output, func(t *testing.T) {
			_, err := ParseReviewSynthesisOutput(output)
			assert.Error(t, err)
		})
	}
}

func TestParseReviewSynthesisOutput_RequirementReinterpretation(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		wantKind *agent.ReviewRequirementConstraintKind
	}{
		{
			name:     "ordinary synthesis omits reinterpretation",
			fragment: "",
		},
		{
			name:     "over constrained",
			fragment: `"requirement_reinterpretation":` + reinterpretationJSON(agent.ReviewRequirementOverConstrained),
			wantKind: constraintKindPointer(agent.ReviewRequirementOverConstrained),
		},
		{
			name:     "internally conflicting",
			fragment: `"requirement_reinterpretation":` + reinterpretationJSON(agent.ReviewRequirementInternallyConflicting),
			wantKind: constraintKindPointer(agent.ReviewRequirementInternallyConflicting),
		},
		{
			name:     "platform non atomic",
			fragment: `"requirement_reinterpretation":` + reinterpretationJSON(agent.ReviewRequirementPlatformNonAtomic),
			wantKind: constraintKindPointer(agent.ReviewRequirementPlatformNonAtomic),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := `{"should_escalate":true,"confidence":0.9`
			if tc.fragment != "" {
				output += "," + tc.fragment
			}
			output += "}"

			got, err := ParseReviewSynthesisOutput(output)
			require.NoError(t, err)
			if tc.wantKind == nil {
				assert.Nil(t, got.RequirementReinterpretation)
				return
			}

			require.NotNil(t, got.RequirementReinterpretation)
			assert.Equal(t, *tc.wantKind, got.RequirementReinterpretation.ConstraintKind)
			assert.Equal(t, "literal atomic commit", got.RequirementReinterpretation.ConflictingRequirement)
			assert.Equal(t, "independent stores", got.RequirementReinterpretation.PlatformConsistencyConstraint)
			assert.Equal(t, "exclusive ownership", got.RequirementReinterpretation.PreservedSafetyProperty)
			assert.Equal(t, "intent-based visibility", got.RequirementReinterpretation.CorrectedInvariant)
			assert.Equal(t, []string{"intent creation", "visibility marker"}, got.RequirementReinterpretation.LinearizationBoundaries)
			assert.Equal(t, []string{"intent persisted", "recovery completed"}, got.RequirementReinterpretation.DurabilityBoundaries)
		})
	}
}

func reinterpretationJSON(kind agent.ReviewRequirementConstraintKind) string {
	return `{"constraint_kind":"` + string(kind) + `","conflicting_requirement":"literal atomic commit","platform_consistency_constraint":"independent stores","preserved_safety_property":"exclusive ownership","corrected_invariant":"intent-based visibility","linearization_boundaries":["intent creation","visibility marker"],"durability_boundaries":["intent persisted","recovery completed"],"evidence_references":["issue:1:task"]}`
}

func constraintKindPointer(kind agent.ReviewRequirementConstraintKind) *agent.ReviewRequirementConstraintKind {
	return &kind
}
