# Planner

The Planner is HerdOS's local planner and orchestrator. It runs on your machine as part of the `herd` CLI and is your primary interface to the system.

## Responsibilities

1. **Work decomposition** — takes a feature description and breaks it into discrete, agent-executable tasks
2. **Issue creation** — creates issues with proper labels, body structure, and milestone assignment
3. **Dispatch** — triggers workers to execute tasks
4. **Progress monitoring** — queries GitHub for issue status, worker health, and batch progress

## Planning

```
$ herd plan
$ herd plan "fix the off-by-one error in pagination"
```

`herd plan` always launches an interactive agent session. The agent (Claude Code, Codex, Cursor, Gemini CLI, OpenCode) is started in interactive mode with a planning-focused system prompt and context about the repository.

When invoked with a description, the description is sent as the first message — the agent starts working on it immediately but stays interactive. If the description is clear, the agent produces a plan right away. If it's vague, the agent asks clarifying questions. Either way, the user can always follow up, refine, or redirect.

```
$ herd plan "I want to add user authentication"

Starting planning session...
(Analyzing repository structure)

Agent: A few questions before I plan this out:
  1. What auth method — session-based, JWT, or OAuth?
  2. Do you need registration, or just login?
  3. Are there existing user models or is this greenfield?

User: JWT, both registration and login, greenfield

Agent: Should I include password reset / email verification,
       or keep it to basic register + login for now?

User: Basic for now, we can add that later

Agent: Here's my proposed decomposition:

  1. Add auth dependencies (bcrypt, jsonwebtoken)
     Scope: package.json
     Complexity: low

  2. Create User model with password hashing
     Scope: src/models/user.ts
     Complexity: medium

  3. Create auth middleware for JWT validation
     Scope: src/middleware/auth.ts
     Complexity: medium

  4. Create login and register endpoints
     Scope: src/routes/auth.ts
     Complexity: medium
     Depends on: #2, #3

  5. Add auth integration tests
     Scope: tests/auth.test.ts
     Complexity: medium
     Depends on: #4

Plan saved. You can exit this session now.
(Press Ctrl+C or type /exit)

$ # After exiting the agent session, herd takes over:

Plan: "Add JWT authentication" (5 tasks)
  1. Add auth dependencies         low     Tier 0
  2. Create User model             medium  Tier 0
  3. Create auth middleware        medium  Tier 0
  4. Create login/register endpoints medium Tier 1 (depends on #2, #3)
  5. Add auth integration tests    medium  Tier 2 (depends on #4)

Create batch with 5 issues? [y/n/edit] y

✓ Created milestone #5: Add JWT authentication
✓ Created 5 issues (#42-#46)
✓ Created batch branch: herd/batch/5-add-jwt-authentication
Dispatching Tier 0 (3 issues with no dependencies):
  #42 Add auth dependencies              ✓ triggered
  #43 Create User model                  ✓ triggered
  #44 Create auth middleware              ✓ triggered
  (2 issues blocked, will dispatch when Tier 0 completes)
```

The conversation continues until the user and agent agree on the scope and decomposition. The agent has full context: it can read files, understand the codebase architecture, and ask informed questions.

### Dispatch behavior

After the user approves a plan, `herd plan` automatically creates the batch branch and dispatches workers for Tier 0 (all tasks with no dependencies). The Integrator handles subsequent tiers automatically as workers complete.

To plan without dispatching (e.g., to review issues before starting work):

```
$ herd plan --no-dispatch
$ herd plan "fix the typo in the header" --no-dispatch
```

## How the Agent Session Works

The `herd plan` command:

1. Gathers repository context (file tree, recent commits, existing issues)
2. Generates a unique plan ID and sets the output path: `.herd/plans/<plan-id>.json`
3. Launches the configured agent in interactive mode with a planning system prompt that includes the output path
4. If a description was provided (`herd plan "..."`), sends it as the first message
5. The user interacts with the agent until a plan is agreed on
6. The agent writes the structured plan (JSON) to the output path when the user approves
7. The agent tells the user the plan is saved and to exit the session (does not accept further prompts)
8. When the agent process exits, `herd` reads and parses the plan file
9. Presents the decomposition for user confirmation
10. On confirmation, creates GitHub Issues and dispatches Tier 0 workers (which creates the batch branch). With `--no-dispatch`, only creates issues — the batch branch is deferred to the first `herd dispatch` call.
11. Cleans up the plan file

The agent is launched as a subprocess. HerdOS doesn't implement its own chat loop — it delegates to whatever agent tool is configured. The agent's native interactive experience (autocomplete, file reading, context management) works as-is.

**Why a file, not stdout?** The agent's stdout is mixed with conversation output, formatting, and UI elements. Parsing structured JSON from that stream is fragile. Writing to a known file path is reliable, works the same way across all agents, and keeps `herd` to a single code path.

## Planning System Prompt

The agent receives a system prompt that includes:

- Its role: "You are a planning assistant for HerdOS"
- The repository structure
- Instructions to ask clarifying questions before decomposing
- The required output format for the plan (JSON schema)
- The output file path to write the plan to (`.herd/plans/<plan-id>.json`)
- Guidelines for good decomposition (independence, clear boundaries, acceptance criteria, right-sizing)
- End-of-session instructions: once the user approves the plan and the JSON file is written, inform the user the session is complete and they should exit. Do not accept further prompts after the plan is finalized.

The prompt is a Go template that `herd` populates with repo context before passing to the agent.

## Plan Output Format

The agent produces a structured plan that `herd` can parse:

```json
{
  "batch_name": "Add JWT authentication",
  "tasks": [
    {
      "title": "Add auth dependencies",
      "description": "Add bcrypt and jsonwebtoken to package.json as production dependencies.",
      "implementation_details": "Run `npm install bcrypt jsonwebtoken` and `npm install -D @types/bcrypt @types/jsonwebtoken`. Verify both appear in the `dependencies` section of package.json (not devDependencies).",
      "acceptance_criteria": [
        "bcrypt is in dependencies (not devDependencies)",
        "jsonwebtoken is in dependencies (not devDependencies)",
        "@types/bcrypt and @types/jsonwebtoken are in devDependencies",
        "npm install runs without errors",
        "No other dependencies are modified"
      ],
      "scope": ["package.json", "package-lock.json"],
      "conventions": [],
      "context_from_dependencies": [],
      "complexity": "low",
      "depends_on": []
    },
    {
      "title": "Create User model with password hashing",
      "description": "Create a User model in src/models/user.ts with bcrypt password hashing, following the existing model pattern in the codebase.",
      "implementation_details": "Create `src/models/user.ts` with a User class/interface. Fields: id (UUID), email (string, unique), passwordHash (string), createdAt (Date), updatedAt (Date). Add a static `hashPassword(plain: string): Promise<string>` method using bcrypt with 12 salt rounds. Add an instance method `verifyPassword(plain: string): Promise<boolean>`. Follow the existing model pattern in `src/models/` — use the same ORM setup, export style, and naming conventions already in the codebase.",
      "acceptance_criteria": [
        "User model exists at src/models/user.ts",
        "Fields: id, email, passwordHash, createdAt, updatedAt",
        "hashPassword uses bcrypt with 12 salt rounds",
        "verifyPassword compares against stored hash",
        "Model is exported from src/models/index.ts",
        "Unit tests in tests/models/user.test.ts cover hash and verify"
      ],
      "scope": ["src/models/user.ts", "src/models/index.ts", "tests/models/user.test.ts"],
      "conventions": [
        "Follow existing model pattern in src/models/ (same ORM, export style)",
        "Use 12 salt rounds for bcrypt (industry standard)"
      ],
      "context_from_dependencies": [
        "Task 0 adds bcrypt and jsonwebtoken to package.json — these are available as imports"
      ],
      "complexity": "medium",
      "depends_on": [0]
    }
  ]
}
```

Each task includes:
- `description` — what to build (the "what")
- `implementation_details` — how to build it (the "how"), including exact file paths, function signatures, algorithms, data formats. This is the core of making issues self-contained.
- `acceptance_criteria` — concrete, verifiable checks (the "done")
- `conventions` — project-specific patterns the worker must follow
- `context_from_dependencies` — information from dependency issues that this task needs, inlined to avoid cross-referencing. The Planner already knows what each task produces — it should tell downstream tasks explicitly.

The `implementation_details`, `conventions`, and `context_from_dependencies` fields are new. They encode the research the Planner has already done, so workers don't repeat it.

The agent writes this JSON to the output file specified in the system prompt (`.herd/plans/<plan-id>.json`). `herd` reads and parses it after the agent exits.

**Field translations** when creating GitHub Issues from the plan:
- `depends_on` indices → issue numbers (translated after issue creation)
- `complexity` → `estimated_complexity` in the issue YAML front matter
- `scope` → `scope` (no change)

## Lifecycle

The Planner is not a persistent process. It runs when you invoke a `herd` command and exits when done. There's no daemon, no background polling, no battery drain.

The planning session lasts as long as the user needs — from a quick single-exchange for simple tasks to a longer conversation for complex features. The process exits when planning is complete.

## Planning Quality

Good decomposition is critical. The Planner should produce tasks that:

- **Are independent** where possible — workers shouldn't block each other
- **Have clear boundaries** — each task touches a specific set of files
- **Include acceptance criteria** — the worker knows when it's done
- **Are right-sized** — not so large that a worker struggles, not so small that overhead dominates

The interactive session is key — the agent can ask questions, clarify requirements, and propose alternatives before committing to a plan.

### Self-Contained Issues

**The Planner does the thinking, the Worker does the typing.**

Every issue must be self-contained — a worker with zero context beyond the issue body and the repository should be able to execute it without exploring the codebase for context. This is not optional. Workers run in fresh, isolated sessions with no memory of prior work.

The Planner pays the research cost once during the interactive planning session. It reads the codebase, understands the architecture, identifies patterns and conventions, and encodes all of that into the issue. Every worker benefits from this upfront investment.

**Why this matters for cost:** A vague issue like "Create the Platform interface" forces the worker agent to explore the codebase, grep through files, read documentation, and infer patterns — burning tokens on research the Planner already did. A well-written issue with exact signatures, file paths, and conventions lets the agent go straight to implementation.

#### What every issue must include

1. **Exact file paths.** Not "create a config module" but "create `internal/config/config.go`, `internal/config/defaults.go`, `internal/config/validate.go`."

2. **Implementation details.** If the task involves implementing an interface, include the exact signatures. If it involves a specific algorithm, describe it. If there's a data format, show it. The worker should not be designing — it should be implementing a specification written by the Planner.

3. **Patterns and conventions.** If the codebase uses specific patterns (error handling, naming, struct layout, test style), state them explicitly. Examples:
   - "Use `var _ Interface = (*Impl)(nil)` for compile-time interface checks"
   - "Stub methods return `errors.New(\"not implemented\")`, not panic"
   - "Tests use `github.com/stretchr/testify/assert` and `require`"
   - "Use table-driven tests for validation rules"

4. **Context from related issues.** If this issue depends on types or functions created by another issue, include those types inline. Don't say "use the Issue type from #4" — paste the type definition. Repetition across issues is fine and expected. The cost of a few extra tokens in the issue body is trivial compared to the cost of the worker exploring the codebase to find the type.

5. **Concrete acceptance criteria.** Not "tests pass" but "unit tests cover: loading valid config, missing file error, default values for omitted fields, env var overrides, validation of each field constraint."

#### Example: bad vs good

**Bad:**
```markdown
## Task
Create the Platform interface and GitHub client scaffold.

## Acceptance Criteria
- [ ] All interfaces from the spec are defined
- [ ] GitHub client connects and works
- [ ] Stubs for unimplemented methods
```

**Good:**
```markdown
## Task
Define the Platform interface and all sub-service interfaces in
`internal/platform/platform.go`. Define all platform-agnostic types in
`internal/platform/types.go`. Scaffold the GitHub implementation in
`internal/platform/github/client.go` with a working client constructor
and stub methods.

## Implementation Details

### `internal/platform/platform.go`

Define these interfaces:

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
        // ... all methods ...
    }

### `internal/platform/types.go`

Define these types:

    type Issue struct {
        Number    int
        Title     string
        Body      string
        // ... all fields ...
    }

### `internal/platform/github/client.go`

Auth strategy: try GITHUB_TOKEN env var → GH_TOKEN env var →
`gh auth token` CLI output as fallback.

Use `github.com/google/go-github/v68` and `golang.org/x/oauth2`.

### Conventions

- Add compile-time interface check: `var _ platform.Platform = (*Client)(nil)`
- Stub methods return `errors.New("not implemented")`, not panic
- Only `RepositoryService.GetInfo()` needs a real implementation
- Delete the placeholder `doc.go` when adding real files

## Related Issues

- #5 (Agent interface) follows the same patterns

## Acceptance Criteria

- [ ] All 7 service interfaces defined with exact method signatures
- [ ] All platform types defined (Issue, PullRequest, Run, Runner, etc.)
- [ ] GitHub client authenticates via GITHUB_TOKEN → GH_TOKEN → gh auth
- [ ] RepositoryService.GetInfo() returns real data from GitHub API
- [ ] All other methods return "not implemented" error
- [ ] Compile-time check passes
- [ ] `go vet ./...` clean
```

The second version takes more effort to write during planning, but produces a worker that can execute immediately without any research phase.

## Relationship to Other Components

```
Planner ─── creates ──▶ Issues (with labels, milestones)
Planner ─── triggers ──▶ Workers (via workflow dispatch)
Planner ─── reads ─────▶ Action status, PR status, Issue state
Planner ─── creates ──▶ Batches (milestones)
```

The Planner has no direct communication with workers. All coordination happens through GitHub: Issues carry the task specification, labels carry the state, PRs carry the results.
