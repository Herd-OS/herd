package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// ExecParams holds the parameters for executing a worker task.
type ExecParams struct {
	IssueNumber int
	RepoRoot    string
}

// ExecResult holds the result of a worker execution.
type ExecResult struct {
	WorkerBranch string
	NoOp         bool // true if no changes were needed
}

const workerPromptTemplate = `You are a HerdOS worker executing a task.

## Task

{{.Title}}

{{.Body}}

## Instructions

- The issue body is your primary source of context. Start there.
- If the issue includes Implementation Details, Conventions, or Context
  from Dependencies sections, follow them closely — the Planner wrote
  them specifically for you.
- If the issue lacks information you need, explore the codebase to fill
  the gaps. But prefer what the issue says over what you infer.
- Check if the acceptance criteria are already satisfied by existing
  code. If so, report that no changes are needed and exit successfully
  without making any commits.
- Focus on files listed in the Scope or Files to Modify sections. You
  may modify other files if necessary to satisfy acceptance criteria.
- Commit your changes with clear messages referencing issue #{{.IssueNumber}}.
- Do not add features, refactor code, or make improvements beyond
  what is specified in the issue.
- You are running in a fully automated CI environment with no human
  present. Do not pause, ask questions, or wait for confirmation.
  Figure it out. If something is broken, try to fix it. If a tool
  fails, try a different approach. Only exit with a non-zero status
  if the task is genuinely impossible (e.g., the issue references
  code that doesn't exist and can't be inferred).
- If you cannot complete the task after exhausting alternatives,
  exit with a non-zero status and include the reason in your output.
{{if .RoleInstructions}}
## Project-Specific Instructions
{{.RoleInstructions}}
{{end}}`

type promptData struct {
	Title            string
	Body             string
	IssueNumber      int
	RoleInstructions string
}

// Exec runs the full worker lifecycle: reads the issue, creates a worker branch,
// invokes the agent, pushes the branch, and updates labels.
func Exec(ctx context.Context, p platform.Platform, ag agent.Agent, cfg *config.Config, params ExecParams) (result *ExecResult, err error) {
	// Deferred failure handling — must be registered before any error returns
	defer func() {
		if err != nil {
			_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress})
			_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusFailed})
			// Trigger monitor for immediate escalation
			defaultBranch, dbErr := p.Repository().GetDefaultBranch(ctx)
			if dbErr == nil {
				_, _ = p.Workflows().Dispatch(ctx, "herd-monitor.yml", defaultBranch, nil)
			}
		}
	}()

	// Get issue
	issue, err := p.Issues().Get(ctx, params.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", params.IssueNumber, err)
	}

	if issue.Milestone == nil {
		return nil, fmt.Errorf("issue #%d has no milestone (not part of a batch)", params.IssueNumber)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))
	workerBranch := fmt.Sprintf("herd/worker/%d-%s", params.IssueNumber, planner.Slugify(issue.Title))

	// Git setup: fetch, checkout batch branch, create worker branch
	g := git.New(params.RepoRoot)
	if err = g.Fetch("origin"); err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}
	if err = g.Checkout(batchBranch); err != nil {
		return nil, fmt.Errorf("checking out batch branch: %w", err)
	}
	if err = g.CreateBranch(workerBranch, batchBranch); err != nil {
		return nil, fmt.Errorf("creating worker branch: %w", err)
	}

	// Build system prompt
	systemPrompt, err := renderWorkerPrompt(issue.Title, issue.Body, params.IssueNumber, params.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("rendering worker prompt: %w", err)
	}

	// Execute task
	taskSpec := agent.TaskSpec{
		IssueNumber: params.IssueNumber,
		Title:       issue.Title,
		Body:        issue.Body,
	}
	execOpts := agent.ExecOptions{
		RepoRoot:     params.RepoRoot,
		SystemPrompt: systemPrompt,
		MaxTurns:     cfg.Agent.MaxTurns,
	}

	if _, err = ag.Execute(ctx, taskSpec, execOpts); err != nil {
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}

	// Check for no-op (no commits made)
	diff, diffErr := g.Diff(batchBranch, "HEAD")
	if diffErr != nil {
		return nil, fmt.Errorf("checking for changes: %w", diffErr)
	}

	if diff == "" {
		// No changes — label done without pushing
		_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress})
		_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})
		return &ExecResult{NoOp: true}, nil
	}

	// Push worker branch
	if err = g.Push("origin", workerBranch); err != nil {
		return nil, fmt.Errorf("pushing worker branch: %w", err)
	}

	// Label issue as done
	_ = p.Issues().RemoveLabels(ctx, params.IssueNumber, []string{issues.StatusInProgress})
	_ = p.Issues().AddLabels(ctx, params.IssueNumber, []string{issues.StatusDone})

	return &ExecResult{
		WorkerBranch: workerBranch,
	}, nil
}

func renderWorkerPrompt(title, body string, issueNumber int, repoRoot string) (string, error) {
	tmpl, err := template.New("worker").Parse(workerPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing worker prompt template: %w", err)
	}

	data := promptData{
		Title:       title,
		Body:        body,
		IssueNumber: issueNumber,
	}

	// Load role instructions from .herd/worker.md if it exists
	ri, readErr := os.ReadFile(filepath.Join(repoRoot, ".herd", "worker.md"))
	if readErr == nil {
		data.RoleInstructions = string(ri)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing worker prompt template: %w", err)
	}
	return buf.String(), nil
}
