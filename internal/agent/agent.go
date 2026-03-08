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
	BatchName string
	Tasks     []PlannedTask
}

type PlannedTask struct {
	Title              string
	Description        string
	AcceptanceCriteria []string
	Scope              []string
	Complexity         string // "low", "medium", "high"
	Type               string // "feature", "bugfix" (default: "feature")
	RunnerLabel        string // Runner label override; empty = use config default
	DependsOn          []int  // Indices into the Tasks slice
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
