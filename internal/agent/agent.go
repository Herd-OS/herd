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

	// Discuss launches an interactive agent session with a fully rendered
	// system prompt and an optional initial user prompt. Unlike Plan, it has
	// no structured output — the agent is expected to converse with the user
	// and may make changes if the user asks. Returns nil on a clean exit.
	Discuss(ctx context.Context, opts DiscussOptions) error
}

// DiscussOptions configures an interactive discussion session.
// SystemPrompt is passed verbatim to the agent (caller is responsible for
// rendering / templating). InitialPrompt is optional; if non-empty it is
// sent as the first user message after the agent starts.
type DiscussOptions struct {
	RepoRoot      string
	SystemPrompt  string
	InitialPrompt string
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
	MaxTurns     int // Max agentic turns (0 = agent default)
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
	Manual                  bool     `json:"manual" yaml:"manual"`              // Requires human action, not dispatched to workers
}

type ExecResult struct {
	ModifiedFiles []string
	Summary       string
}

// ReviewFinding represents a single finding from a code review with severity.
type ReviewFinding struct {
	Severity    string `json:"severity"`    // "HIGH", "MEDIUM", "LOW"
	Description string `json:"description"`
}

type ReviewOptions struct {
	AcceptanceCriteria   []string
	RepoRoot             string
	SystemPrompt         string
	Strictness           string   // "standard", "strict", "lenient" — controls review aggressiveness
	MinFixSeverity       string   // minimum severity that blocks approval: "high", "medium", "low"
	PriorReviewComments  []string // Full text of previous HerdOS review comments on this PR
	UserFeedbackComments []string // User-authored comments on this PR (authoritative)
}

type ReviewResult struct {
	Approved bool            `json:"approved"`
	Findings []ReviewFinding `json:"findings"`
	Comments []string        `json:"comments"` // Deprecated: populated from Findings for backward compatibility
	Summary  string          `json:"summary"`
}
