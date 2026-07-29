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

Use only the supplied current review history. Do not invent issues, files, comments, fix attempts, requirements, or root causes. Prefer no escalation when evidence is weak or when repeated symptoms are not clearly tied to one supported root cause.

Respond with JSON only — no markdown fencing, no surrounding text.`

const ReviewSynthesisPromptTemplate = `Synthesize whether this PR's review-fix loop is failing to converge.

Your job is not to perform another code review. Do not use tools. Do not inspect the repository. Do not call gh, git, bash, or external commands. Do not create comments, issues, files, pull requests, commits, or labels. Do not mutate repository or GitHub state.

Only group findings supported by the current review history below. Do not invent issues. Distinguish repeated symptoms from one root cause: repeated symptoms are evidence, but escalate only when the history supports one coherent, concrete subsystem, invariant, or state machine and a concrete strategy.

Read the cycles in the ascending chronological order supplied. A completed fix shown under a cycle happened after that cycle's findings and before the following review cycle. The following cycle's findings are the observed outcome of the preceding completed fix. Look for alternating fixes, symptoms that move between components, shared state machines, and common lifecycle, synchronization, durability, ownership, or linearization boundaries. Related behavior need not use identical text or occur on the same line, function, or file. Conversely, incompatible behaviors must not be grouped merely because their wording or locations overlap.

Reject generic metadata, broad directory coincidence, unrelated findings, empty findings, coverage/chunk headings, generated summaries, and no-issue/approved verdicts as symptoms or root-cause evidence. Chunk labels and coverage bookkeeping are context only, not package/root-cause clusters. Do not use values such as "Chunk 1/9", "1/9", "Diff Coverage", "Review Aggregation", "Files reviewed", "Source: local-git", or synthetic coverage text in root_cause_title, recurring_symptoms.description, or affected_files. Prefer no escalation when evidence is weak, ambiguous, stale, incompatible, or already resolved by worker reports/no-op verdicts.

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

## Original Batch Requirements
{{if .OriginalRequirements}}{{range .OriginalRequirements}}
### Issue #{{.IssueNumber}}: {{.Title}}
Task:
{{if .Task}}{{.Task}}{{else}}(none supplied){{end}}
Implementation details:
{{if .ImplementationDetails}}{{.ImplementationDetails}}{{else}}(none supplied){{end}}
Acceptance criteria:
{{if .AcceptanceCriteria}}{{range .AcceptanceCriteria}}- {{.}}
{{end}}{{else}}(none supplied)
{{end}}Context:
{{if .Context}}{{.Context}}{{else}}(none supplied){{end}}
{{end}}{{else}}(none supplied)
{{end}}

## Prior Strategy Fix Attempts
{{if .PriorStrategyFixIssues}}{{range .PriorStrategyFixIssues}}
### Strategy fix #{{.Number}}: {{.Title}}
- Cycle: {{.Cycle}}
- Status label: {{.StatusLabel}}
- State: {{.State}}
- Fingerprint: {{.Fingerprint}}
- Head SHA: {{.HeadSHA}}
- Summary: {{.Summary}}
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
Completed fixes after cycle {{.Cycle}} and before the following review cycle:
{{if .CompletedFixIssues}}{{range .CompletedFixIssues}}
#### Fix issue #{{.Number}}: {{.Title}}
- Status label: {{.StatusLabel}}
- Worker report: {{.WorkerReport}}
- Validation result: {{.ValidationStatus}}
- Files: {{range .FilesSummary}}{{.}} {{else}}(none){{end}}
Task/body:
{{if .Body}}{{.Body}}{{else}}(none supplied){{end}}
{{end}}{{else}}(none supplied)
{{end}}
{{end}}{{else}}(none supplied)
{{end}}

## Completed Review-Fix Issues (Global Backward-Compatible Summary)
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

Normally, use this escalation shape without requirement_reinterpretation:
{"should_escalate": true, "confidence": 0.86, "root_cause_title": "Shared review-fix strategy is missing the common state invariant", "root_cause_summary": "Cycles 2, 3, and 4 keep reporting different file-level symptoms that all point to one unsupported invariant in the shared review state transition.", "recurring_symptoms": [{"description": "Review keeps re-reporting stale findings after workers close targeted fixes.", "cycles": [2, 3, 4], "affected_files": ["internal/integrator/review.go"]}], "why_individual_fixes_are_not_converging": "Workers are fixing individual reports without a shared invariant, so each cycle moves the symptom instead of addressing the common state transition.", "proposed_strategy": "Create one strategy issue that defines the invariant, updates the shared helper, and validates all affected review paths together.", "acceptance_criteria": ["Define the invariant using the supplied review history only.", "Update the shared review path so all listed symptoms are resolved together.", "Add regression tests covering the recurring cycles and affected files."], "non_goals": ["Do not re-review unrelated files.", "Do not reopen completed fix issues unless current history proves they are still relevant."]}

Omit requirement_reinterpretation for ordinary synthesis, including requirements that are merely difficult, expensive, or repeatedly implemented incorrectly. It is never a general license to relax difficult acceptance criteria or silently weaken correctness. Include it only when a literal implementation requirement is genuinely over-constrained, internally conflicting, or non-atomic under the platform consistency model. When included, all seven nested fields are mandatory. Identify the literal conflicting requirement and platform constraint; preserve a user-visible safety property; state a materially different corrected invariant; and give explicit, non-empty linearization and durability boundary lists. The proposed acceptance criteria must test both the corrected invariant and the preserved safety property.

Exceptional escalation example with a justified reinterpretation:
{"should_escalate": true, "confidence": 0.94, "root_cause_title": "Cross-store transition cannot be atomic at the required boundary", "root_cause_summary": "The supplied requirement demands one atomic transition across two stores whose platform commits independently.", "recurring_symptoms": [{"description": "Alternating fixes preserve one store while exposing partial state in the other.", "cycles": [2, 3, 4], "affected_files": ["internal/state/transition.go", "internal/store/durable.go"]}], "why_individual_fixes_are_not_converging": "Each fix chooses a different store as authoritative without defining recovery and visibility boundaries.", "proposed_strategy": "Use an intent record, make visibility linearize after both writes, and recover incomplete intents before serving state.", "acceptance_criteria": ["Concurrent readers never observe the protected resource as available after ownership is granted.", "Recovery completes or rolls back every durable intent before the corrected invariant is exposed."], "non_goals": ["Do not weaken exclusive ownership."], "requirement_reinterpretation": {"constraint_kind": "platform_non_atomic", "conflicting_requirement": "Commit both independent stores in one indivisible transaction.", "platform_consistency_constraint": "The platform has no atomic transaction spanning the two durable stores.", "preserved_safety_property": "Users never observe duplicate ownership of the protected resource.", "corrected_invariant": "A durable intent serializes ownership; visibility occurs only after both writes, and recovery resolves incomplete intents.", "linearization_boundaries": ["Durable intent creation serializes competing grants.", "The visibility marker linearizes successful ownership."], "durability_boundaries": ["The intent is durable before either store is mutated.", "Recovery resolves every durable intent before serving ownership state."]}}

Non-escalation example (requirement_reinterpretation remains omitted):
{"should_escalate": false, "confidence": 0.72, "reason": "The repeated findings do not yet support one root cause. The recent worker report and no-op verdict explain why the apparent recurrence is stale, so the next normal review-fix cycle should continue."}
`

type ReviewSynthesisPromptData struct {
	PRNumber               int
	BatchNumber            int
	HeadSHA                string
	HeadRef                string
	CurrentPRMetadata      string
	RecentReviewComments   []string
	OriginalRequirements   []agent.ReviewSynthesisRequirement
	PriorStrategyFixIssues []agent.ReviewSynthesisStrategyFixIssue
	Cycles                 []agent.ReviewSynthesisCycle
	CompletedFixIssues     []agent.ReviewSynthesisFixIssue
	WorkerNoOpVerdicts     []string
	AffectedFiles          []string
	RoleInstructions       string
}

func RenderReviewSynthesisPrompt(input agent.ReviewSynthesisInput, opts agent.ReviewSynthesisOptions) (string, error) {
	tmpl, err := template.New("review_synthesis").Parse(ReviewSynthesisPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review synthesis template: %w", err)
	}

	data := ReviewSynthesisPromptData{
		PRNumber:               input.PRNumber,
		BatchNumber:            input.BatchNumber,
		HeadSHA:                input.HeadSHA,
		HeadRef:                input.HeadRef,
		CurrentPRMetadata:      input.CurrentPRMetadata,
		RecentReviewComments:   input.RecentReviewComments,
		OriginalRequirements:   input.OriginalRequirements,
		PriorStrategyFixIssues: input.PriorStrategyFixIssues,
		Cycles:                 input.Cycles,
		CompletedFixIssues:     input.CompletedFixIssues,
		WorkerNoOpVerdicts:     input.WorkerNoOpVerdicts,
		AffectedFiles:          input.AffectedFiles,
		RoleInstructions:       opts.SystemPrompt,
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
