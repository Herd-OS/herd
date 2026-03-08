package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/herd-os/herd/internal/agent"
)

const reviewPromptTemplate = `Review the following code changes. Check each acceptance criterion and look for bugs, security issues, missing edge cases, and style violations.

## Acceptance Criteria
{{range .AcceptanceCriteria}}- {{.}}
{{end}}
## Diff

` + "```diff" + `
{{.Diff}}
` + "```" + `

Respond with ONLY a JSON object (no markdown fencing, no extra text):
{"approved": true, "comments": [], "summary": "brief summary"}

If you find issues, set approved to false and list each issue in comments:
{"approved": false, "comments": ["issue 1 description", "issue 2 description"], "summary": "brief summary of findings"}
{{if .RoleInstructions}}
## Project-Specific Review Instructions
{{.RoleInstructions}}
{{end}}`

const reviewSystemPrompt = `You are a HerdOS code reviewer. Your job is to review a batch of changes produced by AI workers and identify bugs, security issues, missing edge cases, and style violations. Be thorough but practical — only flag real issues, not stylistic preferences. Respond with JSON only.`

type reviewPromptData struct {
	AcceptanceCriteria []string
	Diff               string
	RoleInstructions   string
}

// Review runs the configured agent in headless mode to review a diff.
// The agent checks acceptance criteria and looks for issues.
// Returns a structured review result parsed from the agent's JSON output.
func (c *ClaudeAgent) Review(ctx context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	prompt, err := renderReviewPrompt(diff, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering review prompt: %w", err)
	}

	args := []string{"-p", prompt, "--system-prompt", reviewSystemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("agent review exited with error: %w\n%s", err, stderr.String())
	}

	result, err := parseReviewOutput(stdout.String())
	if err != nil {
		// If JSON parsing fails, treat as non-approved with raw output
		return &agent.ReviewResult{
			Approved: false,
			Summary:  fmt.Sprintf("Failed to parse agent output as JSON: %s\nRaw output: %s", err, stdout.String()),
		}, nil
	}

	return result, nil
}

func renderReviewPrompt(diff string, opts agent.ReviewOptions) (string, error) {
	tmpl, err := template.New("review").Parse(reviewPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review template: %w", err)
	}

	data := reviewPromptData{
		AcceptanceCriteria: opts.AcceptanceCriteria,
		Diff:               diff,
	}

	// Load role instructions if available
	if opts.RepoRoot != "" {
		ri, readErr := os.ReadFile(filepath.Join(opts.RepoRoot, ".herd", "integrator.md"))
		if readErr == nil {
			data.RoleInstructions = string(ri)
		}
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
	return &result, nil
}
