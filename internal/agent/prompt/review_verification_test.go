package prompt

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderReviewVerificationPrompt_BoundedAndNoTools(t *testing.T) {
	input := agent.ReviewVerificationInput{
		PRNumber: 7, BatchNumber: 9, HeadSHA: "head-9",
		EvidenceSources: []agent.ReviewEvidenceSource{
			{
				ID: "cycle:3:finding:0", Kind: "review_finding", Cycle: 3,
				Excerpt: "internal/state/recovery.go: publication precedes durable ownership",
			},
			{
				ID: "cycle:4:finding:0", Kind: "review_finding", Cycle: 4,
				Excerpt: "internal/state/current.go: current review contradicts the proposed ordering",
			},
		},
		CitedEvidenceReferences: []string{"cycle:3:finding:0"},
		OmittedEvidenceCount:    7,
		Synthesis: agent.ReviewSynthesisResult{
			ShouldEscalate: true, Confidence: .95, RootCauseTitle: "publication ordering gap",
		},
	}

	rendered, err := RenderReviewVerificationPrompt(input)

	require.NoError(t, err)
	assert.Contains(t, rendered, "cycle:3:finding:0")
	assert.Contains(t, rendered, "publication precedes durable ownership")
	assert.Contains(t, rendered, "References cited by the synthesis")
	assert.Contains(t, rendered, "Bounded eligible source evidence")
	assert.Contains(t, rendered, "not all eligible evidence")
	assert.Contains(t, rendered, "7 optional authoritative source(s) were omitted")
	assert.Contains(t, rendered, "cycle:4:finding:0")
	assert.Contains(t, rendered, "current review contradicts")
	assert.Contains(t, rendered, `"root_cause_title":"publication ordering gap"`)
	assert.Contains(t, strings.ToLower(rendered), "do not use tools")
	assert.True(t, utf8.ValidString(rendered))
	assert.Contains(t, rendered, `{"approved": true, "confidence": 0.93, "reason":`)
	assert.LessOrEqual(t, len(ReviewVerificationSystemPrompt)+2+len(rendered), ReviewVerificationPromptBudget)
}

func TestRenderReviewVerificationPrompt_RejectsOversizedCompletePrompt(t *testing.T) {
	input := agent.ReviewVerificationInput{
		EvidenceSources: []agent.ReviewEvidenceSource{{
			ID: "cycle:1:finding:0", Kind: "review_finding", Cycle: 1,
			Excerpt: strings.Repeat("oversized λ evidence ", ReviewVerificationPromptBudget),
		}},
		Synthesis: agent.ReviewSynthesisResult{ShouldEscalate: true},
	}

	rendered, err := RenderReviewVerificationPrompt(input)

	assert.ErrorContains(t, err, "exceeds bounded input budget")
	assert.Empty(t, rendered)
}

func TestRenderReviewVerificationPrompt_CompleteEvidenceCallsOutUncitedContradiction(t *testing.T) {
	input := agent.ReviewVerificationInput{
		HeadSHA: "current-head",
		EvidenceSources: []agent.ReviewEvidenceSource{
			{ID: "cycle:4:finding:0", Kind: "review_finding", Cycle: 4, HeadSHA: "current-head", Excerpt: "The cited finding supports the synthesis."},
			{ID: "cycle:4:finding:1", Kind: "review_finding", Cycle: 4, HeadSHA: "current-head", Excerpt: "The uncited finding contradicts the synthesis."},
		},
		CitedEvidenceReferences: []string{"cycle:4:finding:0"},
		Synthesis:               agent.ReviewSynthesisResult{ShouldEscalate: true},
	}

	rendered, err := RenderReviewVerificationPrompt(input)

	require.NoError(t, err)
	assert.Contains(t, rendered, "Compare cited evidence with all uncited eligible evidence")
	assert.Contains(t, rendered, "All eligible evidence is included.")
	assert.Contains(t, rendered, "cycle:4:finding:1")
	assert.Contains(t, rendered, "uncited finding contradicts")
}

func TestParseReviewVerificationOutput_Strict(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "approved", output: `{"approved":true,"confidence":0.94,"reason":"coherent evidence"}`},
		{name: "rejected", output: `{"approved":false,"confidence":0.99,"reason":"unrelated findings"}`},
		{name: "unknown field", output: `{"approved":true,"confidence":0.94,"reason":"ok","extra":1}`, wantErr: true},
		{name: "missing approval", output: `{"confidence":0.94,"reason":"ok"}`, wantErr: true},
		{name: "missing confidence", output: `{"approved":true,"reason":"ok"}`, wantErr: true},
		{name: "empty reason", output: `{"approved":true,"confidence":0.94,"reason":""}`, wantErr: true},
		{name: "confidence out of range", output: `{"approved":true,"confidence":1.1,"reason":"ok"}`, wantErr: true},
		{name: "nil", output: `null`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ParseReviewVerificationOutput(test.output)
			if test.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
		})
	}
}
