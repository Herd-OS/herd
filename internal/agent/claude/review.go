package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/herd-os/herd/internal/agent"
)

const reviewPromptTemplate = `Review the following code changes. Check each acceptance criterion and look for issues.
{{if .Strictness}}
## Review Strictness: {{.StrictnessUpper}}
{{if eq .Strictness "lenient"}}Only flag critical bugs and security vulnerabilities. Ignore style, code quality, and minor issues.{{end}}{{if eq .Strictness "standard"}}Flag real bugs, security issues, and missing error handling. Ignore style preferences and minor code quality issues.{{end}}{{if eq .Strictness "strict"}}Flag bugs, security issues, missing error handling, style issues, missing edge cases, and code quality improvements.{{end}}
{{end}}
## Acceptance Criteria
{{range .AcceptanceCriteria}}- {{.}}
{{end}}
{{if .PriorReviewComments}}
## Prior Review History
The following review comments were posted in previous cycles on this PR. Do NOT contradict prior review decisions. If a previous cycle requested a change and a worker implemented it, do not flag that change as an issue. Only flag genuinely new issues not covered by prior reviews:
{{range .PriorReviewComments}}
---
{{.}}
---
{{end}}
{{end}}
## Diff

` + "```diff" + `
{{.Diff}}
` + "```" + `

Respond with ONLY a JSON object (no markdown fencing, no extra text):
{"approved": true, "findings": [], "summary": "brief summary"}

If you find issues, set approved to false and classify each finding as HIGH, MEDIUM, or LOW severity:
{"approved": false, "findings": [{"severity": "HIGH", "description": "issue description"}, {"severity": "MEDIUM", "description": "minor issue"}], "summary": "brief summary of findings"}
Use severity "CRITERIA" only when the acceptance criterion itself is flawed, not the code.

Severity guide:
- HIGH: Bugs, security vulnerabilities, data loss risks, race conditions, missing critical error handling
- MEDIUM: Missing edge cases, suboptimal error handling, potential performance issues
- LOW: Style preferences, naming suggestions, minor code quality improvements
- CRITERIA: An acceptance criterion itself is wrong, incomplete, or contradictory. Flag what's wrong with the criterion and what it should say instead. Do NOT create fix issues for CRITERIA findings — they require human review.
{{if .RoleInstructions}}
## Project-Specific Review Instructions
{{.RoleInstructions}}
{{end}}`

const reviewSystemPrompt = `You are a HerdOS code reviewer. Your job is to review a batch of changes produced by AI workers and identify issues. Classify each finding by severity: HIGH (bugs, security), MEDIUM (edge cases, error handling), LOW (style, naming). Respond with JSON only.`

type reviewPromptData struct {
	AcceptanceCriteria  []string
	Diff                string
	RoleInstructions    string
	Strictness          string
	StrictnessUpper     string
	PriorReviewComments []string
}

// Review runs the configured agent in headless mode to review a diff.
// The agent checks acceptance criteria and looks for issues.
// Returns a structured review result parsed from the agent's JSON output.
func (c *ClaudeAgent) Review(ctx context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	prompt, err := renderReviewPrompt(diff, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering review prompt: %w", err)
	}

	// Pass prompt via stdin to avoid "argument list too long" on large diffs.
	args := []string{"--dangerously-skip-permissions", "--system-prompt", reviewSystemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, "-p")

	runOnce := func() (string, string, error) {
		cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
		cmd.Dir = opts.RepoRoot
		cmd.Stdin = strings.NewReader(prompt)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

		if err := cmd.Run(); err != nil {
			return "", stderr.String(), fmt.Errorf("agent review exited with error: %w\n%s", err, stderr.String())
		}
		return stdout.String(), stderr.String(), nil
	}

	stdout, stderr, err := runOnce()
	if err != nil {
		return nil, err
	}

	if isSuspiciousOutput(stdout) {
		fmt.Printf("Review agent returned suspicious output (len=%d), retrying in %s...\nstdout: %s\nstderr: %s\n",
			len(strings.TrimSpace(stdout)), retryDelay, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		time.Sleep(retryDelay)

		stdout, stderr, err = runOnce()
		if err != nil {
			return nil, err
		}
		if isSuspiciousOutput(stdout) {
			return nil, fmt.Errorf("review agent returned suspicious output after retry: stdout=%q stderr=%q",
				strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		}
	}

	result, err := parseReviewOutput(stdout)
	if err != nil {
		// If JSON parsing fails, treat as non-approved with raw output
		return &agent.ReviewResult{
			Approved: false,
			Summary:  fmt.Sprintf("Failed to parse agent output as JSON: %s\nRaw output: %s", err, stdout),
		}, nil
	}

	return result, nil
}

func renderReviewPrompt(diff string, opts agent.ReviewOptions) (string, error) {
	tmpl, err := template.New("review").Parse(reviewPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review template: %w", err)
	}

	strictness := opts.Strictness
	if strictness == "" {
		strictness = "standard"
	}

	data := reviewPromptData{
		AcceptanceCriteria:  opts.AcceptanceCriteria,
		Diff:                diff,
		Strictness:          strictness,
		StrictnessUpper:     strings.ToUpper(strictness),
		PriorReviewComments: opts.PriorReviewComments,
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

func parseReviewOutput(output string) (*agent.ReviewResult, error) {
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
