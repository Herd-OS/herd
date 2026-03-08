package agent

import "context"

// Agent abstracts the AI coding tool (Claude Code, Codex, Cursor, Gemini CLI, etc.)
type Agent interface {
	// Plan launches an interactive planning session.
	// If initialPrompt is non-empty, it is sent as the first message.
	// Returns the structured plan when the session ends.
	Plan(ctx context.Context, initialPrompt string, opts PlanOptions) (*Plan, error)

	// Execute runs a task in headless mode (used by workers).
	Execute(ctx context.Context, task TaskSpec) (*ExecResult, error)

	// Review runs a code review on a diff in headless mode (used by the Integrator).
	Review(ctx context.Context, diff string, opts ReviewOptions) (*ReviewResult, error)
}

type PlanOptions struct {
	RepoRoot     string
	OutputPath   string // Path where the agent writes the plan JSON
	SystemPrompt string
	Context      map[string]string
}

type TaskSpec struct {
	Title              string
	Description        string
	AcceptanceCriteria []string
	Scope              []string
}

type Plan struct {
	BatchName string        `json:"batch_name"`
	Tasks     []PlannedTask `json:"tasks"`
}

type PlannedTask struct {
	Title                   string   `json:"title"`
	Description             string   `json:"description"`
	ImplementationDetails   string   `json:"implementation_details"`
	AcceptanceCriteria      []string `json:"acceptance_criteria"`
	Scope                   []string `json:"scope"`
	Conventions             []string `json:"conventions"`
	ContextFromDependencies []string `json:"context_from_dependencies"`
	Complexity              string   `json:"complexity"`              // "low", "medium", "high"
	Type                    string   `json:"type"`                    // "feature", "bugfix" (default: "feature")
	RunnerLabel             string   `json:"runner_label"`            // Runner label override; empty = use config default
	DependsOn               []int    `json:"depends_on"`             // Indices into the Tasks slice
}

type ExecResult struct {
	ModifiedFiles []string
	Summary       string
}

type ReviewOptions struct {
	AcceptanceCriteria []string
	RepoRoot           string
}

type ReviewResult struct {
	Approved bool
	Comments []string
	Summary  string
}
