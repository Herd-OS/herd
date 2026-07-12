package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/herd-os/herd/internal/agent"
)

// ReviewPromptTemplate is the text/template source rendered for a headless
// code-review session.
const ReviewPromptTemplate = `Review the following code changes. Check each acceptance criterion and look for issues.
{{if .Strictness}}
## Review Strictness: {{.StrictnessUpper}}
{{if eq .Strictness "lenient"}}Only flag critical bugs and security vulnerabilities. Ignore style, code quality, and minor issues.{{end}}{{if eq .Strictness "standard"}}Flag real bugs, security issues, and missing error handling. Ignore style preferences and minor code quality issues.{{end}}{{if eq .Strictness "strict"}}Flag bugs, security issues, missing error handling, style issues, missing edge cases, and code quality improvements.{{end}}
{{end}}
## Acceptance Criteria
{{range .AcceptanceCriteria}}- {{.}}
{{end}}
When an acceptance criterion says no other files are modified or lists specific files in scope, allow supporting changes to configuration files, test helpers, test fixtures, and infrastructure files if they are clearly required for the primary task to work. For example, adding a test host to a config file so that new request specs can run, or updating a test helper to support new test patterns. Only flag changes to files that are truly unrelated to the task. Use your judgment — if removing the extra change would break the primary task, it is a necessary supporting change, not a violation.

{{if .PriorReviewComments}}
## Prior Review History
The following review comments were posted in previous cycles on this PR. Do NOT contradict prior review decisions. If a previous cycle requested a change and a worker implemented it, do not flag that change as an issue. Only flag genuinely new issues not covered by prior reviews:
{{range .PriorReviewComments}}
---
{{.}}
---
{{end}}
{{end}}
{{if .UserFeedbackComments}}
## User Feedback
The following comments were left by users (repository owners/collaborators) on this PR. Treat user feedback as authoritative:
- If a user says a finding is a false positive, do NOT re-flag it.
- If a user provides context explaining why code is correct, accept their explanation.
- If a user requests a specific change, treat it as a requirement.
{{range .UserFeedbackComments}}
---
{{.}}
---
{{end}}
{{end}}
{{if .WorkerNoOpVerdicts}}
## Worker No-Op Verdicts

The following comments were posted by fix workers in previous cycles. The worker read the issue body, verified the code matches what was described, and concluded NO code change was needed. Treat these verdicts as authoritative — they are the result of an agent reading the actual source files:

- If a worker no-op verdict explains why a finding does not require a fix, do NOT re-flag the same finding.
- If your new review would produce a finding that a worker already no-op'd, only re-flag it if you have NEW concrete evidence the worker was wrong (e.g., a specific file:line that contradicts the worker's verdict).

{{range .WorkerNoOpVerdicts}}
---
{{.}}
---
{{end}}
{{end}}
{{if .ChunkedReview}}
## Review Chunk
- Chunk: {{.ChunkIndex}} of {{.TotalChunks}}
{{if .ChunkIncludedPathRange}}- Included path range: {{.ChunkIncludedPathRange}}{{end}}
- Scope: Review only the included diffs in this chunk. Do not assume files from other chunks are present here; they are reviewed in separate strict-output runs.
{{end}}
{{if .CoverageSummary}}
## Review Coverage Context
{{.CoverageSummary}}
{{end}}
## Diff

{{.Diff}}

Respond with ONLY a JSON object (no markdown fencing, no extra text):
{"approved": true, "findings": [], "summary": "brief summary"}

If you find issues, classify each finding as HIGH, MEDIUM, or LOW severity.
{{if .MinFixSeverity}}Set approved to false if ANY finding is {{.MinFixSeverityDesc}} severity or higher. Only set approved to true if all findings are below {{.MinFixSeverityDesc}} severity.{{else}}Set approved to false if any finding is MEDIUM or HIGH severity.{{end}}
For each actionable finding, make the description detailed enough for a fix worker to act without rediscovering the problem. Include:
- The specific file/line, function, symbol, or behavior involved.
- The root cause and the failure scenario or user-visible impact.
- A suggested fix that names relevant local helpers, APIs, or patterns when you can infer them from the diff.
- Tests or verification that should be added or run.
- Any constraints or "do not" notes that would prevent a shallow or regressive fix.
Do not pad findings with generic advice. If you are uncertain about the exact fix, say what must be investigated and what invariant the fix must preserve.
{"approved": false, "findings": [{"severity": "HIGH", "description": "internal/foo/bar.go:123: Root cause: ... Impact: ... Suggested fix: ... Tests: ... Constraints: ..."}, {"severity": "MEDIUM", "description": "minor issue with root cause, impact, suggested fix, and tests"}], "summary": "brief summary of findings"}
Use severity "CRITERIA" only when the acceptance criterion itself is flawed, not the code.

Severity guide:
- HIGH: Bugs, security vulnerabilities, data loss risks, race conditions, missing critical error handling
- MEDIUM: Missing edge cases, suboptimal error handling, potential performance issues
- LOW: Style preferences, naming suggestions, minor code quality improvements
- CRITERIA: An acceptance criterion itself is wrong, incomplete, or contradictory. Flag what's wrong with the criterion and what it should say instead. Do NOT create fix issues for CRITERIA findings — they require human review.
{{if .RoleInstructions}}
## Project-Specific Review Instructions
{{.RoleInstructions}}
{{end}}

## Self-Check Before Returning
Before returning, verify:
1. Your output is a single JSON object with no surrounding text, markdown fencing, or commentary.
2. You have not used any tools, called gh/git/bash, created issues, or modified files.

If you have already taken any action (issue creation, file writes, tool calls), the run is invalid — return JSON with approved=false and a single CRITERIA finding describing what went wrong so a human can investigate. Example:
{"approved": false, "findings": [{"severity": "CRITERIA", "description": "Reviewer took action (created issue #N) instead of returning JSON. Manual investigation required."}], "summary": "Review aborted — reviewer violated strict output contract."}
`

// ReviewSystemPrompt is the system prompt passed verbatim to the agent for
// headless review sessions.
const ReviewSystemPrompt = `You are a HerdOS code reviewer running in a strict output mode.

## Strict Output Contract
Do NOT use any tools. Do NOT call gh, git, bash, or any external command. Do NOT create issues, comments, files, or pull requests. Your ONLY output is a single JSON object matching the schema described in the user prompt.

If you find yourself wanting to take action — creating an issue, writing a file, running a command — STOP. Describe what should happen in the JSON "description" fields and return.

Classify each finding by severity: HIGH (bugs, security), MEDIUM (edge cases, error handling), LOW (style, naming), CRITERIA (the acceptance criterion itself is wrong).

Respond with JSON only — no markdown fencing, no surrounding text.`

// ReviewPromptData is the template input for [ReviewPromptTemplate].
type ReviewPromptData struct {
	AcceptanceCriteria     []string
	Diff                   string
	RoleInstructions       string
	Strictness             string
	StrictnessUpper        string
	MinFixSeverity         string
	MinFixSeverityDesc     string
	PriorReviewComments    []string
	UserFeedbackComments   []string
	WorkerNoOpVerdicts     []string
	ChunkIndex             int
	TotalChunks            int
	ChunkIncludedPathRange string
	CoverageSummary        string
	ChunkedReview          bool
	PartialReview          bool
}

// RenderReviewPrompt renders [ReviewPromptTemplate] for the given diff and
// review options.
func RenderReviewPrompt(diff string, opts agent.ReviewOptions) (string, error) {
	tmpl, err := template.New("review").Parse(ReviewPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review template: %w", err)
	}

	strictness := opts.Strictness
	if strictness == "" {
		strictness = "standard"
	}

	minSev := strings.ToUpper(opts.MinFixSeverity)
	sevDesc := ""
	switch minSev {
	case "LOW":
		sevDesc = "LOW"
	case "HIGH":
		sevDesc = "HIGH"
	default:
		minSev = ""
		sevDesc = ""
	}

	data := ReviewPromptData{
		AcceptanceCriteria:     opts.AcceptanceCriteria,
		Diff:                   diff,
		Strictness:             strictness,
		StrictnessUpper:        strings.ToUpper(strictness),
		MinFixSeverity:         minSev,
		MinFixSeverityDesc:     sevDesc,
		PriorReviewComments:    opts.PriorReviewComments,
		UserFeedbackComments:   opts.UserFeedbackComments,
		WorkerNoOpVerdicts:     opts.WorkerNoOpVerdicts,
		ChunkIndex:             opts.ChunkIndex,
		TotalChunks:            opts.TotalChunks,
		ChunkIncludedPathRange: opts.ChunkIncludedPathRange,
		CoverageSummary:        opts.CoverageSummary,
		ChunkedReview:          opts.ChunkedReview,
		PartialReview:          opts.PartialReview,
	}

	// Use role instructions passed by the caller (integrator loads .herd/integrator.md)
	if opts.SystemPrompt != "" {
		data.RoleInstructions = opts.SystemPrompt
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing review template: %w", err)
	}
	return buf.String(), nil
}

// ParseReviewOutput parses the agent's JSON review output, extracting a JSON
// object that may be wrapped in surrounding text or markdown fencing. It also
// applies backward-compatibility mapping between the legacy Comments field
// and the current Findings field.
func ParseReviewOutput(output string) (*agent.ReviewResult, error) {
	output = strings.TrimSpace(output)

	// Try to extract JSON from the output (agent might wrap it in markdown fencing)
	if idx := strings.Index(output, "{"); idx >= 0 {
		if end := strings.LastIndex(output, "}"); end >= idx {
			output = output[idx : end+1]
		}
	}

	var result agent.ReviewResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("parsing review JSON: %w", err)
	}

	// Backward compatibility: if Findings is populated but Comments is not,
	// populate Comments from Findings descriptions.
	if len(result.Findings) > 0 && len(result.Comments) == 0 {
		for _, f := range result.Findings {
			result.Comments = append(result.Comments, f.Description)
		}
	}

	// Backward compatibility: if Comments is populated but Findings is not
	// (old agent format), create Findings with HIGH severity for each.
	if len(result.Comments) > 0 && len(result.Findings) == 0 {
		for _, c := range result.Comments {
			result.Findings = append(result.Findings, agent.ReviewFinding{
				Severity:    "HIGH",
				Description: c,
			})
		}
	}

	return &result, nil
}
