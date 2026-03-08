package agent

import "context"

// Agent abstracts the AI coding tool (Claude Code, Codex, Cursor, Gemini CLI, etc.)
type Agent interface {
	// Plan launches an interactive planning session.
	// If initialPrompt is non-empty, it is sent as the first message.
	// Returns the structured plan when the session ends.
	Plan(ctx context.Context, initialPrompt string, opts PlanOptions) (*Plan, error)

	// Execute runs a task in headless mode (used by workers).
	Execute(ctx context.Context, task TaskSpec, opts ExecOptions) (*ExecResult, error)

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
	IssueNumber        int      // GitHub issue number (for commit messages)
	Title              string
	Body               string   // Full issue body (raw markdown, passed verbatim to agent)
	AcceptanceCriteria []string
	Scope              []string
}

type ExecOptions struct {
	RepoRoot     string
	SystemPrompt string
}

type Plan struct {
	BatchName string        `json:"batch_name" yaml:"batch_name"`
	Tasks     []PlannedTask `json:"tasks" yaml:"tasks"`
}

type PlannedTask struct {
	Title                   string   `json:"title" yaml:"title"`
	Description             string   `json:"description" yaml:"description"`
	ImplementationDetails   string   `json:"implementation_details" yaml:"implementation_details"`
	AcceptanceCriteria      []string `json:"acceptance_criteria" yaml:"acceptance_criteria"`
	Scope                   []string `json:"scope" yaml:"scope"`
	Conventions             []string `json:"conventions" yaml:"conventions"`
	ContextFromDependencies []string `json:"context_from_dependencies" yaml:"context_from_dependencies"`
	Complexity              string   `json:"complexity" yaml:"complexity"`       // "low", "medium", "high"
	Type                    string   `json:"type" yaml:"type"`                   // "feature", "bugfix" (default: "feature")
	RunnerLabel             string   `json:"runner_label" yaml:"runner_label"`   // Runner label override; empty = use config default
	DependsOn               []int    `json:"depends_on" yaml:"depends_on"`      // Indices into the Tasks slice
}

type ExecResult struct {
	ModifiedFiles []string
	Summary       string
}

type ReviewOptions struct {
	AcceptanceCriteria []string
	RepoRoot           string
	SystemPrompt       string
}

type ReviewResult struct {
	Approved bool
	Comments []string
	Summary  string
}
