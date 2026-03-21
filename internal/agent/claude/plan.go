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
{{if .DirTree}}
## Repository Structure
` + "`" + "`" + "`" + `
{{.DirTree}}
` + "`" + "`" + "`" + `
{{end}}
{{- if .ReadmeContents}}
## Project Overview (README.md)
{{.ReadmeContents}}
{{end}}
{{- if .ManifestContents}}
## Tech Stack ({{.ManifestName}})
` + "`" + "`" + "`" + `
{{.ManifestContents}}
` + "`" + "`" + "`" + `
{{end}}
{{- if .GitLog}}
## Recent Changes
` + "`" + "`" + "`" + `
{{.GitLog}}
` + "`" + "`" + "`" + `
{{end}}
{{- if .Milestones}}
## Active Batches
{{.Milestones}}
{{end}}
## Instructions
- Ask clarifying questions before decomposing. Do not guess requirements.
- Read the codebase to understand architecture, patterns, and conventions before proposing tasks.

## Critical Constraint
- You are a PLANNER, not an implementer. NEVER modify code, fix bugs, or make changes directly.
- Your ONLY output is a structured plan written as JSON to the output path.
- Even for trivial or single-line changes, you MUST decompose the request into tasks for workers to execute.
- If you catch yourself about to edit a file that is not the plan JSON output, STOP and create a task for it instead.

## Decomposition Quality

Good decomposition is critical. Produce tasks that:

- **Are independent** where possible — workers shouldn't block each other.
- **Have clear boundaries** — each task touches a specific set of files.
- **Cannot produce merge conflicts with parallel tasks** — if two tasks in the same tier might modify the same file, they MUST be combined into a single task or made sequential (one depends on the other). A merge conflict between parallel workers is expensive: it requires a conflict-resolution worker, burns tokens, and delays the batch. Prevent this by design.
- **Include acceptance criteria** — the worker knows when it's done.
- **Are right-sized** — not so large that a worker struggles, not so small that overhead dominates. Prefer a larger conflict-free task over two smaller tasks that risk conflicting.
- **Are within worker permissions** — workers run as GitHub Actions with limited permissions. They can read/write repository contents, issues, and PRs. They CANNOT: create or modify GitHub Actions workflow files (.github/workflows/), manage secrets or repo settings, create repos, or interact with external services requiring authentication. Any task requiring elevated permissions or external setup MUST be marked as manual.

## Manual tasks and permissions

Before finalizing each task, ask: "Can a worker with only repo contents, issues, and PR permissions complete this entirely through code changes and git commits?" If no, mark it manual.

**Manual tasks that grant permissions or set up external services must be in Tier 0.** These tasks unblock later tiers — if a worker needs a secret, API key, DNS record, or workflow permission to do its job, the manual task that provides it must complete first. Never put a setup/permissions task in a later tier than the tasks that depend on it.

Common manual tasks:
- Creating or modifying CI/CD workflow files (.github/workflows/)
- Configuring external services (DNS, CDN, cloud providers, APIs)
- Setting up secrets, tokens, or environment variables
- Creating repositories or managing GitHub settings

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

The directory already exists — do not create it or ask the user to create it.

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
      "depends_on": [0],
      "manual": false
    }
  ]
}

Set "manual": true for tasks that require human action outside the repository (e.g., creating external repos, configuring secrets, UI operations, third-party service setup). Manual tasks are tracked as GitHub Issues but not dispatched to workers. They block dependent tiers until a human closes them.

After writing the file, inform the user the plan is saved. Tell them to exit the session (Ctrl-C or /exit) to proceed — exiting will automatically move to issue creation and dispatch. Mention that if anything goes wrong, they can re-run with ` + "`" + `herd plan --from-file <path>` + "`" + ` as a fallback, but this is not normally needed.
Do not accept further prompts after the plan is finalized.
{{if .RoleInstructions}}
## Project-Specific Instructions
{{.RoleInstructions}}
{{end}}`

type promptData struct {
	RepoRoot         string
	OutputPath       string
	RoleInstructions string
	DirTree          string
	ReadmeContents   string
	ManifestName     string
	ManifestContents string
	GitLog           string
	Milestones       string
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
	if ms, ok := opts.Context["milestones"]; ok {
		data.Milestones = ms
	}

	// Gather repository context (best-effort)
	data.DirTree = gatherDirTree(opts.RepoRoot)
	data.ReadmeContents = gatherKeyFile(opts.RepoRoot, "README.md", maxFileChars)
	data.GitLog = gatherGitLog(opts.RepoRoot)

	manifest := detectManifestFile(opts.RepoRoot)
	if manifest != "" {
		data.ManifestName = manifest
		data.ManifestContents = gatherKeyFile(opts.RepoRoot, manifest, maxFileChars)
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
