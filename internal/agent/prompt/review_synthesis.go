package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/agent"
)

const ReviewSynthesisSystemPrompt = `You are a HerdOS review synthesis analyst running in a strict output mode.

## Strict Output Contract
Do NOT use any tools. Do NOT call gh, git, bash, or any external command. Do NOT create issues, comments, files, or pull requests. Do NOT mutate repository or GitHub state. Your ONLY output is a single JSON object matching the schema described in the user prompt.

Use only the supplied current review history. Do not invent issues, files, comments, fix attempts, requirements, or root causes. Prefer no escalation when evidence is weak or when repeated symptoms are not clearly tied to one supported root cause.

Respond with JSON only — no markdown fencing, no surrounding text.`

const ReviewSynthesisInputBudget = 64 * 1024
const ReviewPromptTruncationMarker = "\n[TRUNCATED: deterministic review evidence budget reached]\n"

const ReviewSynthesisPromptTemplate = `Synthesize whether this PR's review-fix loop is failing to converge.

Your job is not to perform another code review. Do not use tools. Do not inspect the repository. Do not call gh, git, bash, or external commands. Do not create comments, issues, files, pull requests, commits, or labels. Do not mutate repository or GitHub state.

Only group findings supported by the current review history below. Do not invent issues. Distinguish repeated symptoms from one root cause: repeated symptoms are evidence, but escalate only when the history supports one coherent, concrete subsystem, invariant, or state machine and a concrete strategy.

Every recurring symptom must cite its supporting stable IDs in evidence_references. If requirement_reinterpretation is present, it must cite all requirements and findings used for that judgment in evidence_references. Optional source_excerpts must contain an exact substring of the cited source. Never cite an ID not supplied below.

Read the cycles in the ascending chronological order supplied. A completed fix shown under a cycle happened after that cycle's findings and before the following review cycle. The following cycle's findings are the observed outcome of the preceding completed fix. Look for alternating fixes, symptoms that move between components, shared state machines, and common lifecycle, synchronization, durability, ownership, or linearization boundaries. Related behavior need not use identical text or occur on the same line, function, or file. Conversely, incompatible behaviors must not be grouped merely because their wording or locations overlap.

Reject generic metadata, broad directory coincidence, unrelated findings, empty findings, coverage/chunk headings, generated summaries, and no-issue/approved verdicts as symptoms or root-cause evidence. Chunk labels and coverage bookkeeping are context only, not package/root-cause clusters. Do not use values such as "Chunk 1/9", "1/9", "Diff Coverage", "Review Aggregation", "Files reviewed", "Source: local-git", or synthetic coverage text in root_cause_title, recurring_symptoms.description, or affected_files. Prefer no escalation when evidence is weak, ambiguous, stale, incompatible, or already resolved by worker reports/no-op verdicts.

## Current Review Identity
- PR number: {{.PRNumber}}
- Batch number: {{.BatchNumber}}
- Head SHA: {{.HeadSHA}}
- Head ref: {{.HeadRef}}

## Stable Evidence Sources (authoritative)
{{if .EvidenceSources}}{{range .EvidenceSources}}
- ID: {{.ID}}
  Kind: {{.Kind}}{{if .Cycle}}
  Cycle: {{.Cycle}}{{end}}{{if .HeadSHA}}
  Head SHA: {{.HeadSHA}}{{end}}
  Source: {{.Excerpt}}
{{end}}{{else}}(none supplied)
{{end}}

## Current PR Metadata
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
{{if .ValidationStatus}}{{.ValidationStatus}}{{else}}(outcome unavailable){{end}}
{{end}}{{else}}(none supplied)
{{end}}
{{end}}{{else}}(none supplied)
{{end}}

## Completed Review-Fix Outcomes
{{if .CompletedFixIssues}}{{range .CompletedFixIssues}}
### #{{.Number}} {{.Title}}
- Status label: {{.StatusLabel}}
- Validation status: {{.ValidationStatus}}
- Worker report: {{.WorkerReport}}
- Files summary: {{range .FilesSummary}}{{.}} {{else}}(none){{end}}
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
{"should_escalate": true, "confidence": 0.86, "root_cause_title": "Shared review-fix strategy is missing the common state invariant", "root_cause_summary": "Cycles 2, 3, and 4 keep reporting different file-level symptoms that all point to one unsupported invariant in the shared review state transition.", "recurring_symptoms": [{"description": "Review keeps re-reporting stale findings after workers close targeted fixes.", "cycles": [2, 3, 4], "affected_files": ["internal/integrator/review.go"], "evidence_references": ["cycle:2:finding:0", "cycle:3:finding:0", "cycle:4:finding:0"]}], "why_individual_fixes_are_not_converging": "Workers are fixing individual reports without a shared invariant, so each cycle moves the symptom instead of addressing the common state transition.", "proposed_strategy": "Create one strategy issue that defines the invariant, updates the shared helper, and validates all affected review paths together.", "acceptance_criteria": ["Define the invariant using the supplied review history only.", "Update the shared review path so all listed symptoms are resolved together.", "Add regression tests covering the recurring cycles and affected files."], "non_goals": ["Do not re-review unrelated files.", "Do not reopen completed fix issues unless current history proves they are still relevant."]}

Omit requirement_reinterpretation for ordinary synthesis, including requirements that are merely difficult, expensive, or repeatedly implemented incorrectly. It is never a general license to relax difficult acceptance criteria or silently weaken correctness. Include it only when a literal implementation requirement is genuinely over-constrained, internally conflicting, or non-atomic under the platform consistency model. When included, every required nested field and evidence_references are mandatory. Identify the literal conflicting requirement and platform constraint; preserve a user-visible safety property; state a materially different corrected invariant; and give explicit, non-empty linearization and durability boundary lists. The proposed acceptance criteria must test both the corrected invariant and the preserved safety property.

Exceptional escalation example with a justified reinterpretation:
{"should_escalate": true, "confidence": 0.94, "root_cause_title": "Cross-store transition cannot be atomic at the required boundary", "root_cause_summary": "The supplied requirement demands one atomic transition across two stores whose platform commits independently.", "recurring_symptoms": [{"description": "Alternating fixes preserve one store while exposing partial state in the other.", "cycles": [2, 3, 4], "affected_files": ["internal/state/transition.go", "internal/store/durable.go"], "evidence_references": ["cycle:2:finding:0", "cycle:3:finding:0", "cycle:4:finding:0"]}], "why_individual_fixes_are_not_converging": "Each fix chooses a different store as authoritative without defining recovery and visibility boundaries.", "proposed_strategy": "Use an intent record, make visibility linearize after both writes, and recover incomplete intents before serving state.", "acceptance_criteria": ["Concurrent readers never observe the protected resource as available after ownership is granted.", "Recovery completes or rolls back every durable intent before the corrected invariant is exposed."], "non_goals": ["Do not weaken exclusive ownership."], "requirement_reinterpretation": {"constraint_kind": "platform_non_atomic", "conflicting_requirement": "Commit both independent stores in one indivisible transaction.", "platform_consistency_constraint": "The platform has no atomic transaction spanning the two durable stores.", "preserved_safety_property": "Users never observe duplicate ownership of the protected resource.", "corrected_invariant": "A durable intent serializes ownership; visibility occurs only after both writes, and recovery resolves incomplete intents.", "linearization_boundaries": ["Durable intent creation serializes competing grants.", "The visibility marker linearizes successful ownership."], "durability_boundaries": ["The intent is durable before either store is mutated.", "Recovery resolves every durable intent before serving ownership state."], "evidence_references": ["issue:1:task", "issue:1:criterion:0", "cycle:2:finding:0"]}}

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
	EvidenceSources        []agent.ReviewEvidenceSource
	RoleInstructions       string
}

func RenderReviewSynthesisPrompt(input agent.ReviewSynthesisInput, opts agent.ReviewSynthesisOptions) (string, error) {
	tmpl, err := template.New("review_synthesis").Parse(ReviewSynthesisPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review synthesis template: %w", err)
	}
	for divisor := 1; divisor <= 1024; divisor *= 2 {
		data := budgetedReviewSynthesisPromptData(input, opts, divisor)
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return "", fmt.Errorf("executing review synthesis template: %w", err)
		}
		if buf.Len() <= ReviewSynthesisInputBudget {
			return buf.String(), nil
		}
	}
	return "", fmt.Errorf("authoritative review synthesis evidence and complete output contract exceed bounded input budget")
}

func budgetedReviewSynthesisPromptData(input agent.ReviewSynthesisInput, opts agent.ReviewSynthesisOptions, divisor int) ReviewSynthesisPromptData {
	limit := func(value int) int {
		if divisor <= 0 {
			return 0
		}
		return value / divisor
	}
	return ReviewSynthesisPromptData{
		PRNumber:               input.PRNumber,
		BatchNumber:            input.BatchNumber,
		HeadSHA:                input.HeadSHA,
		HeadRef:                input.HeadRef,
		CurrentPRMetadata:      budgetReviewPromptString(input.CurrentPRMetadata, limit(4096)),
		RecentReviewComments:   budgetReviewPromptStrings(input.RecentReviewComments, limit(6144)),
		OriginalRequirements:   input.OriginalRequirements,
		PriorStrategyFixIssues: budgetReviewStrategyFixIssues(input.PriorStrategyFixIssues, limit(4096)),
		Cycles:                 budgetReviewSynthesisCycles(input.Cycles, limit(8192)),
		CompletedFixIssues:     budgetReviewFixIssues(input.CompletedFixIssues, limit(6144)),
		WorkerNoOpVerdicts:     budgetReviewPromptStrings(input.WorkerNoOpVerdicts, limit(4096)),
		AffectedFiles:          budgetReviewPromptStrings(input.AffectedFiles, limit(3072)),
		EvidenceSources:        input.EvidenceSources,
		RoleInstructions:       budgetReviewPromptString(opts.SystemPrompt, limit(4096)),
	}
}

func budgetReviewPromptString(value string, budget int) string {
	if value == "" || len(value) <= budget {
		return value
	}
	marker := strings.TrimSpace(ReviewPromptTruncationMarker)
	if budget <= len(marker) {
		return marker
	}
	keep := budget - len(marker) - 1
	for keep > 0 && !utf8.RuneStart(value[keep]) {
		keep--
	}
	return value[:keep] + "\n" + marker
}

func budgetReviewPromptStrings(values []string, budget int) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	remaining := budget
	for index, value := range values {
		if len(value)+1 <= remaining {
			out = append(out, value)
			remaining -= len(value) + 1
			continue
		}
		if remaining > 0 {
			out = append(out, budgetReviewPromptString(value, remaining))
		} else {
			out = append(out, strings.TrimSpace(ReviewPromptTruncationMarker))
		}
		if index+1 < len(values) {
			out = append(out, fmt.Sprintf("[TRUNCATED: %d additional optional values omitted]", len(values)-index-1))
		}
		break
	}
	return out
}

func budgetReviewStrategyFixIssues(values []agent.ReviewSynthesisStrategyFixIssue, budget int) []agent.ReviewSynthesisStrategyFixIssue {
	out := append([]agent.ReviewSynthesisStrategyFixIssue(nil), values...)
	remaining := budget
	for index := range out {
		cost := len(out[index].Title) + len(out[index].Summary) + len(out[index].Fingerprint) + 64
		if cost <= remaining {
			remaining -= cost
			continue
		}
		out[index].Summary = budgetReviewPromptString(out[index].Summary, remaining)
		out = out[:index+1]
		out[index].Summary += fmt.Sprintf("\n[TRUNCATED: %d additional strategy summaries omitted]", len(values)-index-1)
		return out
	}
	return out
}

func budgetReviewSynthesisCycles(values []agent.ReviewSynthesisCycle, budget int) []agent.ReviewSynthesisCycle {
	out := make([]agent.ReviewSynthesisCycle, len(values))
	remaining := budget
	truncated := false
	for index, value := range values {
		out[index] = value
		out[index].FindingsBySeverity = budgetReviewFindingsBySeverity(value.FindingsBySeverity, remaining/3)
		out[index].CompletedFixIssues = budgetReviewFixIssues(value.CompletedFixIssues, remaining/3)
		out[index].AffectedFiles = budgetReviewPromptStrings(value.AffectedFiles, remaining/6)
		out[index].ChunkCoverageSummary = budgetReviewPromptString(value.ChunkCoverageSummary, remaining/6)
		cost := reviewFindingsLength(out[index].FindingsBySeverity) +
			reviewFixIssuesLength(out[index].CompletedFixIssues) +
			stringsLength(out[index].AffectedFiles) + len(out[index].ChunkCoverageSummary)
		if cost > remaining {
			truncated = true
			remaining = 0
		} else {
			remaining -= cost
		}
	}
	if truncated && len(out) > 0 {
		out[len(out)-1].ChunkCoverageSummary += "\n" + strings.TrimSpace(ReviewPromptTruncationMarker)
	}
	return out
}

func budgetReviewFindingsBySeverity(values map[string][]string, budget int) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string][]string, len(values))
	remaining := budget
	for _, key := range keys {
		out[key] = budgetReviewPromptStrings(values[key], remaining)
		remaining -= stringsLength(out[key])
		if remaining <= 0 {
			break
		}
	}
	return out
}

func reviewFindingsLength(values map[string][]string) int {
	total := 0
	for key, findings := range values {
		total += len(key) + stringsLength(findings)
	}
	return total
}

func reviewFixIssuesLength(values []agent.ReviewSynthesisFixIssue) int {
	total := 0
	for _, value := range values {
		total += len(value.Title) + len(value.ValidationStatus) + stringsLength(value.FilesSummary) + 64
	}
	return total
}

func budgetReviewFixIssues(values []agent.ReviewSynthesisFixIssue, budget int) []agent.ReviewSynthesisFixIssue {
	out := append([]agent.ReviewSynthesisFixIssue(nil), values...)
	remaining := budget
	for index := range out {
		out[index].Body = ""
		out[index].FilesSummary = budgetReviewPromptStrings(out[index].FilesSummary, remaining/3)
		out[index].ValidationStatus = budgetReviewPromptString(out[index].ValidationStatus, remaining/3)
		cost := len(out[index].Title) + len(out[index].ValidationStatus) + stringsLength(out[index].FilesSummary) + 64
		if cost <= remaining {
			remaining -= cost
			continue
		}
		out = out[:index+1]
		out[index].ValidationStatus = strings.TrimSpace(ReviewPromptTruncationMarker)
		return out
	}
	return out
}

func stringsLength(values []string) int {
	total := 0
	for _, value := range values {
		total += len(value)
	}
	return total
}

func ParseReviewSynthesisOutput(output string) (*agent.ReviewSynthesisResult, error) {
	output = strings.TrimSpace(output)
	if idx := strings.Index(output, "{"); idx >= 0 {
		if end := strings.LastIndex(output, "}"); end >= idx {
			output = output[idx : end+1]
		}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return nil, fmt.Errorf("parsing review synthesis JSON: %w", err)
	}
	for _, required := range []string{"should_escalate", "confidence"} {
		if _, ok := fields[required]; !ok {
			return nil, fmt.Errorf("parsing review synthesis JSON: missing required field %q", required)
		}
	}
	var result agent.ReviewSynthesisResult
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing review synthesis JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("parsing review synthesis JSON: %w", err)
	}
	if result.Confidence < 0 || result.Confidence > 1 {
		return nil, fmt.Errorf("parsing review synthesis JSON: confidence outside [0,1]")
	}
	for _, symptom := range result.RecurringSymptoms {
		if len(symptom.EvidenceReferences) == 0 {
			return nil, fmt.Errorf("parsing review synthesis JSON: recurring symptom missing evidence_references")
		}
	}
	if result.RequirementReinterpretation != nil && len(result.RequirementReinterpretation.EvidenceReferences) == 0 {
		return nil, fmt.Errorf("parsing review synthesis JSON: requirement reinterpretation missing evidence_references")
	}
	return &result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
