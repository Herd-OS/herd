package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"text/template"

	"github.com/herd-os/herd/internal/agent"
)

const planningPromptTemplate = `You are a planning assistant for HerdOS. Your job is to decompose a feature request into discrete, agent-executable tasks.

## Repository
Working directory: {{.RepoRoot}}

## Instructions
- Ask clarifying questions before decomposing. Do not guess requirements.
- Read the codebase to understand architecture, patterns, and conventions before proposing tasks.

## Decomposition Quality

Good decomposition is critical. Produce tasks that:

- **Are independent** where possible — workers shouldn't block each other.
- **Have clear boundaries** — each task touches a specific set of files.
- **Cannot produce merge conflicts with parallel tasks** — if two tasks in the same tier might modify the same file, they MUST be combined into a single task or made sequential (one depends on the other). A merge conflict between parallel workers is expensive: it requires a conflict-resolution worker, burns tokens, and delays the batch. Prevent this by design.
- **Include acceptance criteria** — the worker knows when it's done.
- **Are right-sized** — not so large that a worker struggles, not so small that overhead dominates. Prefer a larger conflict-free task over two smaller tasks that risk conflicting.

## Self-Contained Issues

**You do the thinking, the Worker does the typing.**

Every task must be self-contained — a worker with zero context beyond the task description and the repository should be able to execute it without exploring the codebase for context. Workers run in fresh, isolated sessions with no memory of prior work.

You pay the research cost once during this planning session. Read the codebase, understand the architecture, identify patterns and conventions, and encode all of that into each task.

### What every task must include

1. **Exact file paths.** Not "create a config module" but "create internal/config/config.go, internal/config/defaults.go, internal/config/validate.go."

2. **Implementation details.** If the task involves implementing an interface, include the exact signatures. If it involves a specific algorithm, describe it. If there's a data format, show it. The worker should not be designing — it should be implementing a specification you write.

3. **Patterns and conventions.** If the codebase uses specific patterns (error handling, naming, struct layout, test style), state them explicitly.

4. **Context from related tasks.** If a task depends on types or functions created by another task, include those types inline in context_from_dependencies. Don't say "use the type from task 0" — paste the definition. Repetition across tasks is expected and cheap.

5. **Concrete acceptance criteria.** Not "tests pass" but "unit tests cover: loading valid config, missing file error, default values for omitted fields."

## Output
When the user approves the plan, write the structured plan as JSON to:
  {{.OutputPath}}

The JSON schema:
{
  "batch_name": "Feature name",
  "tasks": [
    {
      "title": "Task title",
      "description": "What to build",
      "implementation_details": "How to build it — exact file paths, function signatures, algorithms",
      "acceptance_criteria": ["Concrete, verifiable checks"],
      "scope": ["file/paths that will be created or modified"],
      "conventions": ["Project patterns the worker must follow"],
      "context_from_dependencies": ["Inline type definitions and details from tasks this depends on"],
      "complexity": "low|medium|high",
      "type": "feature|bugfix",
      "runner_label": "",
      "depends_on": [0]
    }
  ]
}

After writing the file, inform the user the plan is saved and they should exit the session.
Do not accept further prompts after the plan is finalized.
{{if .RoleInstructions}}
## Project-Specific Instructions
{{.RoleInstructions}}
{{end}}`

type promptData struct {
	RepoRoot         string
	OutputPath       string
	RoleInstructions string
}

// Plan launches Claude Code in interactive mode for a planning session.
// After the agent exits, it reads and parses the plan JSON from opts.OutputPath.
func (c *ClaudeAgent) Plan(ctx context.Context, initialPrompt string, opts agent.PlanOptions) (*agent.Plan, error) {
	systemPrompt, err := renderPrompt(opts)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}

	args := []string{"--system-prompt", systemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if initialPrompt != "" {
		args = append(args, "--initial-prompt", initialPrompt)
	}

	cmd := exec.CommandContext(ctx, c.BinaryPath, args...)
	cmd.Dir = opts.RepoRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude exited with error: %w", err)
	}

	plan, err := readPlanFile(opts.OutputPath)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func renderPrompt(opts agent.PlanOptions) (string, error) {
	tmpl, err := template.New("plan").Parse(planningPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing prompt template: %w", err)
	}

	data := promptData{
		RepoRoot:   opts.RepoRoot,
		OutputPath: opts.OutputPath,
	}
	if ri, ok := opts.Context["role_instructions"]; ok {
		data.RoleInstructions = ri
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}
	return buf.String(), nil
}

func readPlanFile(path string) (*agent.Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("agent did not produce a plan file at %s", path)
		}
		return nil, fmt.Errorf("reading plan file: %w", err)
	}

	var plan agent.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parsing plan JSON: %w", err)
	}

	if err := validatePlan(&plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func validatePlan(plan *agent.Plan) error {
	if plan.BatchName == "" {
		return fmt.Errorf("plan has empty batch_name")
	}
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("plan has no tasks")
	}
	return nil
}
