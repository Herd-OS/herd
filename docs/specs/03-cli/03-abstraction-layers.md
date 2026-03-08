# Abstraction Layers

HerdOS has two abstraction boundaries: the **Platform** (where work is tracked and executed) and the **Agent** (what does the thinking). Both are interfaces with swappable implementations.

```
┌──────────────────────────────────┐
│  CLI Commands                    │  Agnostic to both
│  (plan, dispatch, status)        │
├────────────────┬─────────────────┤
│ Agent Interface│ Platform Interface│  ← boundaries
├────────────────┼─────────────────┤
│ Claude Code    │ GitHub           │  v1.0 implementations
│ (Codex, etc.   │ (GitLab,         │
│  coming soon)  │  Gitea — soon)   │
└────────────────┴─────────────────┘
```

## Design Principle

The CLI should not hardcode GitHub API calls or Claude Code invocations throughout the codebase. All platform interactions go through a `Platform` interface. All agent interactions go through an `Agent` interface. v1.0 ships with GitHub and Claude Code implementations. Other implementations can be added later without touching core logic.

---

## Agent Interface

The Agent interface abstracts the AI coding tool used for planning and task execution. Defined in `internal/agent/agent.go`:

```go
// Agent abstracts the AI coding tool (Claude Code, Codex, Cursor, Gemini CLI, etc.)
type Agent interface {
	// Plan launches an interactive planning session.
	// The user converses directly with the agent to flesh out a feature.
	// If initialPrompt is non-empty, it is sent as the first message.
	// The agent writes the plan to the output file specified in opts.
	// Returns the structured plan when the session ends.
	Plan(ctx context.Context, initialPrompt string, opts PlanOptions) (*Plan, error)

	// Execute runs a task in headless mode (used by workers).
	// Returns the paths of files that were modified.
	Execute(ctx context.Context, task TaskSpec) (*ExecResult, error)

	// Review runs a code review on a diff in headless mode (used by the Integrator).
	// Returns the review with approval status and comments.
	Review(ctx context.Context, diff string, opts ReviewOptions) (*ReviewResult, error)
}

type PlanOptions struct {
	RepoRoot    string            // Path to the repository
	SystemPrompt string           // Planning instructions and context
	Context     map[string]string // Additional context (file tree, existing issues, etc.)
}

type TaskSpec struct {
	Title              string
	Description        string
	AcceptanceCriteria []string
	Scope              []string  // Files/directories to focus on
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
	RunnerLabel        string // Runner label override (e.g., "herd-gpu"); empty = use config default
	DependsOn          []int  // Indices into the Tasks slice
}

type ExecResult struct {
	ModifiedFiles []string
	Summary       string
}

type ReviewOptions struct {
	AcceptanceCriteria []string // From the batch's issues
	RepoRoot           string
}

type ReviewResult struct {
	Approved bool
	Comments []string // Issues found
	Summary  string
}
```

### Claude Code Implementation

Lives in `internal/agent/claude/`:

```go
type ClaudeAgent struct {
	BinaryPath string // Path to `claude` CLI (default: "claude")
	Model      string // Model override (optional)
}

func (c *ClaudeAgent) Plan(ctx context.Context, initialPrompt string, opts PlanOptions) (*Plan, error) {
	// Launch: claude --system-prompt <planning prompt> [initialPrompt]
	// System prompt includes output path (.herd/plans/<plan-id>.json)
	// If initialPrompt is non-empty, passed as the first message
	// User interacts directly with Claude Code's TUI
	// On exit, read and parse plan from the output file
}

func (c *ClaudeAgent) Execute(ctx context.Context, task TaskSpec) (*ExecResult, error) {
	// Launch: claude -p "<task prompt>"
	// Return list of modified files
}

func (c *ClaudeAgent) Review(ctx context.Context, diff string, opts ReviewOptions) (*ReviewResult, error) {
	// Launch: claude -p "<review prompt + diff>"
	// Parse structured review from stdout (approved/rejected + issues found)
}
```

### Future Agent Implementations (coming soon)

| Agent | Binary | Planning (interactive) | Workers/Review (headless) |
|-------|--------|------------------------|---------------------------|
| Codex | `codex` | TBD | TBD |
| Cursor | `cursor` | `cursor [prompt]` | `cursor -p` |
| Gemini CLI | `gemini` | `gemini [prompt]` | `gemini -p` |
| OpenCode | `opencode` | TBD | TBD |

Each implementation wraps the agent's CLI with the appropriate flags and output parsing. Claude Code is the only implemented agent in v1.0.

### Agent Detection

The CLI determines which agent to use from `.herdos.yml`:

```yaml
agent:
  provider: "claude"    # claude (codex, cursor, gemini, opencode coming soon)
  binary: "claude"      # path to binary (optional, defaults based on provider)
  model: ""             # model override (optional, agent-specific)
```

If not configured, `herd init` detects which agent CLIs are available and asks the user.

---

## Platform Interface

The Platform interface abstracts all interactions with the hosting platform (GitHub, GitLab, etc.). Defined in `internal/platform/platform.go`:

```go
type Platform interface {
	Issues() IssueService
	PullRequests() PullRequestService
	Workflows() WorkflowService
	Labels() LabelService
	Milestones() MilestoneService
	Runners() RunnerService
	Repository() RepositoryService
}

type IssueService interface {
	Create(ctx context.Context, title, body string, labels []string, milestone *int) (*Issue, error)
	Get(ctx context.Context, number int) (*Issue, error)
	List(ctx context.Context, filters IssueFilters) ([]*Issue, error)
	Update(ctx context.Context, number int, changes IssueUpdate) (*Issue, error)
	AddLabels(ctx context.Context, number int, labels []string) error
	RemoveLabels(ctx context.Context, number int, labels []string) error
	AddComment(ctx context.Context, number int, body string) error
}

type PullRequestService interface {
	Create(ctx context.Context, title, body, head, base string) (*PullRequest, error)
	Get(ctx context.Context, number int) (*PullRequest, error)
	List(ctx context.Context, filters PRFilters) ([]*PullRequest, error)
	Update(ctx context.Context, number int, title, body *string) (*PullRequest, error)
	Merge(ctx context.Context, number int, method MergeMethod) (*MergeResult, error)
	UpdateBranch(ctx context.Context, number int) error
	CreateReview(ctx context.Context, number int, body string, event ReviewEvent) error
}

type WorkflowService interface {
	GetWorkflow(ctx context.Context, filename string) (workflowID int64, err error) // resolve filename to numeric ID (for RunFilters)
	Dispatch(ctx context.Context, workflowFile, ref string, inputs map[string]string) (*Run, error) // workflowFile is the YAML filename (e.g., "herd-worker.yml")
	GetRun(ctx context.Context, runID int64) (*Run, error)
	ListRuns(ctx context.Context, filters RunFilters) ([]*Run, error)
	CancelRun(ctx context.Context, runID int64) error
}

type LabelService interface {
	Create(ctx context.Context, name, color, description string) error
	List(ctx context.Context) ([]*Label, error)
	Delete(ctx context.Context, name string) error
}

type MilestoneService interface {
	Create(ctx context.Context, title, description string, dueDate *time.Time) (*Milestone, error)
	Get(ctx context.Context, number int) (*Milestone, error)
	List(ctx context.Context) ([]*Milestone, error)
	Update(ctx context.Context, number int, changes MilestoneUpdate) (*Milestone, error)
}

type RunnerService interface {
	List(ctx context.Context) ([]*Runner, error)
	Get(ctx context.Context, id int64) (*Runner, error)
}

type RepositoryService interface {
	GetInfo(ctx context.Context) (*RepoInfo, error)
	GetDefaultBranch(ctx context.Context) (string, error)
	CreateBranch(ctx context.Context, name, fromSHA string) error
	DeleteBranch(ctx context.Context, name string) error
	GetBranchSHA(ctx context.Context, name string) (string, error)
}
```

### Data Types

Platform-agnostic types defined in `internal/platform/types.go`:

```go
type Issue struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed"
	Labels    []string
	Milestone *Milestone
	Assignees []string
	URL       string
}

type PullRequest struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed", "merged"
	Head      string // branch name
	Base      string // target branch
	Mergeable bool
	URL       string
}

type Run struct {
	ID         int64
	Status     string            // "queued", "in_progress", "completed"
	Conclusion string            // "success", "failure", "cancelled"
	Inputs     map[string]string // workflow_dispatch inputs (issue_number, batch_branch, etc.)
	URL        string
}

type Runner struct {
	ID     int64
	Name   string
	Status string // "online", "offline"
	Labels []string
	Busy   bool
}

type Label struct {
	Name        string
	Color       string
	Description string
}

type Milestone struct {
	Number      int
	Title       string
	Description string
	State       string // "open", "closed"
	DueDate     *time.Time
	OpenIssues  int
	ClosedIssues int
}

type RepoInfo struct {
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	URL           string
}

type MergeMethod string

const (
	MergeMethodMerge  MergeMethod = "merge"
	MergeMethodSquash MergeMethod = "squash"
	MergeMethodRebase MergeMethod = "rebase"
)

type ReviewEvent string

const (
	ReviewApprove        ReviewEvent = "APPROVE"
	ReviewRequestChanges ReviewEvent = "REQUEST_CHANGES"
	ReviewComment        ReviewEvent = "COMMENT"
)

type MergeResult struct {
	SHA     string
	Merged  bool
	Message string
}

// Filter and update types

type IssueFilters struct {
	State     string   // "open", "closed", "all"
	Labels    []string // Filter by labels
	Milestone *int     // Filter by milestone number
}

type IssueUpdate struct {
	Title     *string
	Body      *string
	State     *string  // "open", "closed"
	Milestone *int     // Set milestone (0 to clear)
}

type PRFilters struct {
	State string // "open", "closed", "all"
	Head  string // Filter by head branch
	Base  string // Filter by base branch
}

type RunFilters struct {
	WorkflowID int64
	Status     string // "queued", "in_progress", "completed"
	Branch     string
}

type MilestoneUpdate struct {
	Title       *string
	Description *string
	State       *string    // "open", "closed"
	DueDate     *time.Time // nil to clear
}
```

### Git Operations vs Platform API

The Platform interface covers **API operations** (issues, PRs, workflows, labels, milestones, runners, branch creation/deletion). **Git operations** (checkout, merge, rebase, push, force-push) are done via `git` CLI subprocess calls in a shared `internal/git/` package.

This split exists because:
- Git operations are the same across GitHub, GitLab, and Gitea (all use the git protocol)
- Branch merging and rebasing require a working directory — not available via REST API
- The Platform API is used for branch creation/deletion (lightweight ref operations), but content-modifying operations go through `git`

**When to use which for branches:**
- `RepositoryService.CreateBranch(name, fromSHA)` — creates a branch on the remote via API, without needing a local checkout. Used by `herd plan` / `herd dispatch` to create the batch branch before any worker runs.
- `RepositoryService.DeleteBranch(name)` — deletes a remote branch via API. Used for cleanup after consolidation or cancel.
- `Git.CreateBranch(name, from)` — creates a local branch in the working directory. Used by workers to create their worker branch from the checked-out batch branch.

The `internal/git/` package wraps `exec.Command("git", ...)` with helpers:

```go
// internal/git/git.go
type Git struct {
	WorkDir string // repository root
}

func (g *Git) Checkout(branch string) error
func (g *Git) CreateBranch(name, from string) error
func (g *Git) Fetch(remote string) error
func (g *Git) Merge(branch string) error          // returns error on conflict
func (g *Git) Rebase(onto string) error            // returns error on conflict
func (g *Git) Push(remote, branch string) error
func (g *Git) ForcePush(remote, branch string) error
func (g *Git) Pull(remote, branch string) error    // fetch + merge (for consolidation retry)
func (g *Git) Diff(base, head string) (string, error) // unified diff between refs
func (g *Git) CurrentBranch() (string, error)
func (g *Git) HeadSHA() (string, error)
func (g *Git) HasConflicts() bool
```

Workers use `internal/git/` to create worker branches and push. The Integrator uses it to merge worker branches into the batch branch, rebase onto main, and force-push when resolving conflicts.

### GitHub Implementation

The GitHub implementation lives in `internal/platform/github/` and uses the [`google/go-github`](https://github.com/google/go-github) library.

Key mappings:
- `Issues.Create` → `POST /repos/{owner}/{repo}/issues`
- `Workflows.Dispatch` → `POST /repos/{owner}/{repo}/actions/workflows/{id}/dispatches`
- `PullRequests.Merge` → `PUT /repos/{owner}/{repo}/pulls/{number}/merge`
- `Milestones.Create` → `POST /repos/{owner}/{repo}/milestones`

Authentication: `GITHUB_TOKEN` environment variable (primary) or `gh auth` token (convenience fallback). The `gh` CLI is not a hard requirement.

### Future: GitLab Implementation (coming soon)

GitLab has equivalent concepts with different API shapes:

| HerdOS | GitHub | GitLab |
|--------|--------|--------|
| Issue | Issue | Issue |
| Pull Request | Pull Request | Merge Request |
| Milestone | Milestone | Milestone |
| Workflow Dispatch | `workflow_dispatch` | Pipeline trigger |
| Runner | Self-hosted runner | Runner |
| Label | Label | Label |

A GitLab implementation would map the same `Platform` interface to GitLab's REST API.

### Platform Detection

The CLI detects the platform from the Git remote URL:

```
git@github.com:org/repo.git     → GitHub
https://github.com/org/repo.git → GitHub
git@gitlab.com:org/repo.git     → GitLab (coming soon)
https://gitea.example.com/...   → Gitea (coming soon)
```

If the platform can't be detected, the CLI asks the user or falls back to GitHub.

---

## v1.0 Scope

For v1.0:
- Agent: Define the interface, implement Claude Code
- Platform: Define the interface, implement GitHub
- Don't implement other agents or platforms yet

Both interfaces exist from day one so that core logic never imports agent-specific or platform-specific packages. This prevents large refactors when adding support later.
