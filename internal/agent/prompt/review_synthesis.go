package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/herd-os/herd/internal/agent"
)

const ReviewSynthesisSystemPrompt = `You are a HerdOS review synthesis analyst running in a strict output mode.

## Strict Output Contract
Do NOT use any tools. Do NOT call gh, git, bash, or any external command. Do NOT create issues, comments, files, or pull requests. Do NOT mutate repository or GitHub state. Your ONLY output is a single JSON object matching the schema described in the user prompt.

Use only the supplied current review history. Do not invent issues, files, comments, fix attempts, or root causes. Prefer no escalation when evidence is weak or when repeated symptoms are not clearly tied to one supported root cause.

Respond with JSON only — no markdown fencing, no surrounding text.`

const ReviewSynthesisPromptTemplate = `Synthesize whether this PR's review-fix loop is failing to converge.

Your job is not to perform another code review. Do not use tools. Do not inspect the repository. Do not call gh, git, bash, or external commands. Do not create comments, issues, files, pull requests, commits, or labels. Do not mutate repository or GitHub state.

Only group findings supported by the current review history below. Do not invent issues. Distinguish repeated symptoms from one root cause: repeated symptoms are evidence, but escalate only when the history supports a coherent root cause and strategy. Prefer no escalation when evidence is weak, ambiguous, stale, or already resolved by worker reports/no-op verdicts.

Chunk labels and coverage bookkeeping are context only, not package/root-cause clusters. Do not use values such as "Chunk 1/9", "1/9", "Diff Coverage", "Review Aggregation", "Files reviewed", "Source: local-git", or synthetic coverage text in root_cause_title, recurring_symptoms.description, or affected_files.

## Current PR Metadata
- PR number: {{.PRNumber}}
- Batch number: {{.BatchNumber}}
- Head SHA: {{.HeadSHA}}
- Head ref: {{.HeadRef}}
{{if .CurrentPRMetadata}}
{{.CurrentPRMetadata}}
{{else}}
(none supplied)
{{end}}

## Recent Review Result Comments
{{if .RecentReviewComments}}{{range .RecentReviewComments}}
---
{{.}}
---
{{end}}{{else}}(none supplied)
{{end}}

## Deduplicated Findings By Cycle
{{if .Cycles}}{{range .Cycles}}
### Cycle {{.Cycle}}
- Head SHA: {{.HeadSHA}}
- Status: {{.Status}}
- Findings after dedupe: {{.FindingsAfterDedupe}}
- Fix issue numbers: {{range .FixIssueNumbers}}#{{.}} {{else}}(none){{end}}
- Affected files: {{range .AffectedFiles}}{{.}} {{else}}(none){{end}}
{{if .ChunkCoverageSummary}}- Chunk coverage summary: {{.ChunkCoverageSummary}}{{end}}
{{if .FindingsBySeverity}}
Findings by severity:
{{range $severity, $findings := .FindingsBySeverity}}{{range $findings}}- {{$severity}}: {{.}}
{{end}}{{end}}{{else}}Findings by severity: (none)
{{end}}
{{end}}{{else}}(none supplied)
{{end}}

## Completed Review-Fix Issues
{{if .CompletedFixIssues}}{{range .CompletedFixIssues}}
### #{{.Number}} {{.Title}}
- Status label: {{.StatusLabel}}
- Validation status: {{.ValidationStatus}}
- Worker report: {{.WorkerReport}}
- Files summary: {{range .FilesSummary}}{{.}} {{else}}(none){{end}}
Body:
{{.Body}}
{{end}}{{else}}(none supplied)
{{end}}

## Worker No-Op Verdicts
{{if .WorkerNoOpVerdicts}}{{range .WorkerNoOpVerdicts}}
---
{{.}}
---
{{end}}{{else}}(none supplied)
{{end}}

## Affected Files/Packages
{{if .AffectedFiles}}{{range .AffectedFiles}}- {{.}}
{{end}}{{else}}(none supplied)
{{end}}

{{if .RoleInstructions}}
## Project-Specific Synthesis Instructions
{{.RoleInstructions}}
{{end}}

## Output Contract
Return strict JSON only. Do not include markdown fences, commentary, or extra keys.

Use this shape when escalation is warranted:
{"should_escalate": true, "confidence": 0.86, "root_cause_title": "Shared review-fix strategy is missing the common state invariant", "root_cause_summary": "Cycles 2, 3, and 4 keep reporting different file-level symptoms that all point to one unsupported invariant in the shared review state transition.", "recurring_symptoms": [{"description": "Review keeps re-reporting stale findings after workers close targeted fixes.", "cycles": [2, 3, 4], "affected_files": ["internal/integrator/review.go"]}], "why_individual_fixes_are_not_converging": "Workers are fixing individual reports without a shared invariant, so each cycle moves the symptom instead of addressing the common state transition.", "proposed_strategy": "Create one strategy issue that defines the invariant, updates the shared helper, and validates all affected review paths together.", "acceptance_criteria": ["Define the invariant using the supplied review history only.", "Update the shared review path so all listed symptoms are resolved together.", "Add regression tests covering the recurring cycles and affected files."], "non_goals": ["Do not re-review unrelated files.", "Do not reopen completed fix issues unless current history proves they are still relevant."]}

Use this shape when escalation is not warranted:
{"should_escalate": false, "confidence": 0.72, "reason": "The repeated findings do not yet support one root cause. The recent worker report and no-op verdict explain why the apparent recurrence is stale, so the next normal review-fix cycle should continue."}
`

type ReviewSynthesisPromptData struct {
	PRNumber             int
	BatchNumber          int
	HeadSHA              string
	HeadRef              string
	CurrentPRMetadata    string
	RecentReviewComments []string
	Cycles               []agent.ReviewSynthesisCycle
	CompletedFixIssues   []agent.ReviewSynthesisFixIssue
	WorkerNoOpVerdicts   []string
	AffectedFiles        []string
	RoleInstructions     string
}

func RenderReviewSynthesisPrompt(input agent.ReviewSynthesisInput, opts agent.ReviewSynthesisOptions) (string, error) {
	tmpl, err := template.New("review_synthesis").Parse(ReviewSynthesisPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review synthesis template: %w", err)
	}

	data := ReviewSynthesisPromptData{
		PRNumber:             input.PRNumber,
		BatchNumber:          input.BatchNumber,
		HeadSHA:              input.HeadSHA,
		HeadRef:              input.HeadRef,
		CurrentPRMetadata:    input.CurrentPRMetadata,
		RecentReviewComments: input.RecentReviewComments,
		Cycles:               input.Cycles,
		CompletedFixIssues:   input.CompletedFixIssues,
		WorkerNoOpVerdicts:   input.WorkerNoOpVerdicts,
		AffectedFiles:        input.AffectedFiles,
		RoleInstructions:     opts.SystemPrompt,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing review synthesis template: %w", err)
	}
	return buf.String(), nil
}

func ParseReviewSynthesisOutput(output string) (*agent.ReviewSynthesisResult, error) {
	output = strings.TrimSpace(output)
	if idx := strings.Index(output, "{"); idx >= 0 {
		if end := strings.LastIndex(output, "}"); end >= idx {
			output = output[idx : end+1]
		}
	}

	var result agent.ReviewSynthesisResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("parsing review synthesis JSON: %w", err)
	}
	return &result, nil
}
