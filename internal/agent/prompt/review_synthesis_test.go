package prompt

import (
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
		Cycles: []agent.ReviewSynthesisCycle{{
			Cycle:                4,
			HeadSHA:              "abc123",
			FindingsAfterDedupe:  2,
			FindingsBySeverity:   map[string][]string{"HIGH": []string{"internal/foo.go: missing invariant"}},
			FixIssueNumbers:      []int{91},
			Status:               "changes_requested",
			AffectedFiles:        []string{"internal/foo.go"},
			ChunkCoverageSummary: "2 chunks reviewed",
		}},
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
		"## Deduplicated Findings By Cycle",
		"## Completed Review-Fix Issues",
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
		"internal/foo.go: missing invariant",
		"#91",
		"worker says no-op for stale symptom",
		"prefer repo invariants",
	} {
		assert.Contains(t, rendered, want)
	}
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
		"Do NOT use any tools",
		"Do NOT create issues, comments, files, or pull requests",
		"Do NOT mutate repository or GitHub state",
	} {
		assert.Contains(t, ReviewSynthesisSystemPrompt+"\n"+ReviewSynthesisPromptTemplate, want)
	}
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
			output:         `text before {"should_escalate":true,"confidence":0.91,"root_cause_title":"Invariant missing","root_cause_summary":"same root cause","recurring_symptoms":[{"description":"same symptom","cycles":[2,3],"affected_files":["internal/foo.go"]}],"why_individual_fixes_are_not_converging":"fixes are too narrow","proposed_strategy":"fix shared helper","acceptance_criteria":["covers cycles"],"non_goals":["unrelated review"]} text after`,
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
