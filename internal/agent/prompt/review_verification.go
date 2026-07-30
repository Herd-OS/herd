package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/agent"
)

const ReviewVerificationSystemPrompt = `You are an independent HerdOS review-strategy verifier in strict output mode.

Do not use tools, inspect a repository, or perform another code review. Use only the supplied source evidence and synthesis. Return exactly one JSON object with approved, confidence, and reason. Reject when evidence is ambiguous, unrelated, generic, contradictory, or insufficient.`

const ReviewVerificationPromptBudget = ReviewSynthesisInputBudget

const reviewVerificationPromptTemplate = `Independently verify the proposed review strategy.

Approve only if:
1. The cited findings describe one coherent recurring architectural behavior or root cause, not unrelated findings sharing a package or generic vocabulary. Compare cited evidence with all uncited eligible evidence and reject cherry-picking, contradictions, or an unrelated current review.
2. The proposed strategy and acceptance criteria address that root cause.
3. If requirement_reinterpretation is present, its corrected invariant and generated criteria preserve and entail the cited original user-visible safety property without weakening or contradiction.

Do not use tools or re-review the repository. Stable source IDs and exact source text follow.

PR: {{.PRNumber}}
Batch: {{.BatchNumber}}
Head SHA: {{.HeadSHA}}

References cited by the synthesis:
{{range .CitedEvidenceReferences}}- {{.}}
{{else}}(none)
{{end}}

Bounded eligible source evidence:
{{if .OmittedEvidenceCount}}This is not all eligible evidence. {{.OmittedEvidenceCount}} optional authoritative source(s) were omitted by the deterministic prompt budget.{{else}}All eligible evidence is included.{{end}}
{{range .EvidenceSources}}- {{.ID}} [{{.Kind}}{{if .Cycle}}, cycle {{.Cycle}}{{end}}{{if .HeadSHA}}, head {{.HeadSHA}}{{end}}]: {{.Excerpt}}
{{else}}(none)
{{end}}

Synthesis JSON:
{{.SynthesisJSON}}

Return strict JSON only:
{"approved": true, "confidence": 0.93, "reason": "The cited alternating findings share one lifecycle invariant and the proposed criteria test it."}
`

type reviewVerificationPromptData struct {
	PRNumber                int
	BatchNumber             int
	HeadSHA                 string
	EvidenceSources         []agent.ReviewEvidenceSource
	CitedEvidenceReferences []string
	OmittedEvidenceCount    int
	SynthesisJSON           string
}

func RenderReviewVerificationPrompt(input agent.ReviewVerificationInput) (string, error) {
	synthesisJSON, err := json.Marshal(input.Synthesis)
	if err != nil {
		return "", fmt.Errorf("marshalling review synthesis: %w", err)
	}
	tmpl, err := template.New("review_verification").Parse(reviewVerificationPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review verification template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, reviewVerificationPromptData{
		PRNumber: input.PRNumber, BatchNumber: input.BatchNumber, HeadSHA: input.HeadSHA,
		EvidenceSources: input.EvidenceSources, CitedEvidenceReferences: input.CitedEvidenceReferences,
		OmittedEvidenceCount: input.OmittedEvidenceCount, SynthesisJSON: string(synthesisJSON),
	}); err != nil {
		return "", fmt.Errorf("executing review verification template: %w", err)
	}
	rendered := buf.String()
	if !utf8.ValidString(rendered) {
		return "", fmt.Errorf("review verification prompt contains invalid UTF-8")
	}
	if len(ReviewVerificationSystemPrompt)+2+len(rendered) > ReviewVerificationPromptBudget {
		return "", fmt.Errorf("review verification prompt exceeds bounded input budget")
	}
	return rendered, nil
}

func ParseReviewVerificationOutput(output string) (*agent.ReviewVerificationResult, error) {
	output = strings.TrimSpace(output)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return nil, fmt.Errorf("parsing review verification JSON: %w", err)
	}
	for _, required := range []string{"approved", "confidence", "reason"} {
		if _, ok := fields[required]; !ok {
			return nil, fmt.Errorf("parsing review verification JSON: missing required field %q", required)
		}
	}
	var result agent.ReviewVerificationResult
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing review verification JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("parsing review verification JSON: %w", err)
	}
	if strings.TrimSpace(result.Reason) == "" || result.Confidence < 0 || result.Confidence > 1 {
		return nil, fmt.Errorf("review verification result has invalid confidence or empty reason")
	}
	return &result, nil
}
