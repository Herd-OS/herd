package prompt

import (
	"strings"
	"testing"

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
		Synthesis: agent.ReviewSynthesisResult{
			ShouldEscalate: true, Confidence: .95, RootCauseTitle: "publication ordering gap",
		},
	}

	rendered, err := RenderReviewVerificationPrompt(input)

	require.NoError(t, err)
	assert.Contains(t, rendered, "cycle:3:finding:0")
	assert.Contains(t, rendered, "publication precedes durable ownership")
	assert.Contains(t, rendered, "References cited by the synthesis")
	assert.Contains(t, rendered, "All bounded eligible source evidence")
	assert.Contains(t, rendered, "cycle:4:finding:0")
	assert.Contains(t, rendered, "current review contradicts")
	assert.Contains(t, rendered, `"root_cause_title":"publication ordering gap"`)
	assert.Contains(t, strings.ToLower(rendered), "do not use tools")
	assert.LessOrEqual(t, len(rendered), ReviewSynthesisInputBudget)
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
