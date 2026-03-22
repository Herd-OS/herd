# Planner Design

## What the Planner Does

The Planner is HerdOS's local planning and orchestration component. It runs on your machine as part of the `herd` CLI and is the primary interface to the system. Its responsibilities are:

1. **Work decomposition** -- takes a feature description and breaks it into discrete, agent-executable tasks.
2. **Issue creation** -- creates GitHub Issues with proper labels, body structure, and milestone assignment.
3. **Dispatch** -- triggers workers to execute tasks.
4. **Progress monitoring** -- queries GitHub for issue status, worker health, and batch progress.

The Planner is not a persistent process. It runs when you invoke a `herd` command and exits when done. There is no daemon, no background polling.

## Pre-Flight Git Checks

Before launching the agent session, `herd plan` runs pre-flight checks to ensure the local repository is in a good state. This prevents planning against stale or diverged code.

1. **Fetch from origin** — always runs; failure is non-fatal.
2. **Branch check** — prompts to switch to the default branch if on a different branch.
3. **Behind remote check** — prompts to pull if local is behind the remote.
4. **Dirty working tree** — warns that the planner will see uncommitted local state. Switching or pulling with a dirty tree is a hard error.

Use `--skip-preflight` to bypass all checks (e.g., when intentionally planning from a feature branch).

## Planning Modes

`herd plan` always launches an interactive agent session. The configured agent (Claude Code, Codex, Cursor, Gemini CLI, OpenCode) is started in interactive mode with a planning-focused system prompt and repository context.

**Interactive mode** (`herd plan`): Opens a conversational session where the user and agent collaborate on decomposition from scratch. The agent can read the codebase, ask clarifying questions, and propose alternatives before committing to a plan.

**Description mode** (`herd plan "description"`): The description is sent as the agent's first message, so it starts working immediately -- but the session remains interactive. If the description is clear, the agent produces a plan right away. If it is vague, the agent asks clarifying questions. The user can always follow up, refine, or redirect.

In both modes, the conversation continues until the user and agent agree on scope and decomposition. Once approved, `herd` presents the plan for confirmation, creates GitHub Issues, and dispatches Tier 0 workers (all tasks with no dependencies). Use `--no-dispatch` to create issues without dispatching.

### How the Agent Session Works

The `herd plan` command gathers repository context, generates a unique plan ID, and launches the configured agent as a subprocess with a planning system prompt. HerdOS does not implement its own chat loop -- it delegates to whatever agent is configured, preserving the agent's native interactive experience.

When the user approves a plan, the agent writes structured output to a known file path (`.herd/plans/<plan-id>.json`). After the agent process exits, `herd` reads and parses the plan file, presents it for confirmation, and creates issues and dispatches on approval.

The agent writes to a file rather than stdout because agent stdout is mixed with conversation output, formatting, and UI elements. Parsing structured data from that stream would be fragile. A known file path is reliable and works identically across all agents.

## Planning Quality Principles

Good decomposition is critical. The Planner should produce tasks that:

- **Are independent** where possible -- workers should not block each other.
- **Have clear boundaries** -- each task touches a specific set of files.
- **Cannot produce merge conflicts with parallel tasks.** If two tasks in the same tier might modify the same file, they must be combined into a single task or made sequential via a dependency edge. A merge conflict between parallel workers is expensive: it requires a conflict-resolution worker, burns tokens, and delays the batch. The Planner should prevent this by design.
- **Include acceptance criteria** -- the worker knows when it is done.
- **Are right-sized** -- not so large that a worker struggles, not so small that overhead dominates. Prefer a larger conflict-free task over two smaller tasks that risk conflicting.

The interactive session is key to achieving these properties. The agent has full codebase context and can reason about file boundaries, dependency relationships, and potential conflicts before producing a plan.

## Worker Permissions and Manual Tasks

Workers run as GitHub Actions with limited permissions. They can read/write repository contents, issues, and PRs. They **cannot**: manage secrets or repo settings, create repos, or interact with external services requiring authentication.

The Planner evaluates each task against these boundaries. Any task requiring elevated permissions or external setup must be marked as **manual** (`"manual": true`). Manual tasks are tracked as GitHub Issues but not dispatched to workers — a human must complete and close them.

**Manual tasks that grant permissions or set up external services must be in Tier 0.** These tasks unblock later tiers. If a worker needs a secret, API key, DNS record, or workflow permission to do its job, the manual task that provides it must complete first. Never place a setup task in a later tier than the tasks that depend on it.

Common manual tasks:
- Configuring external services (DNS, CDN, cloud providers, APIs)
- Setting up secrets, tokens, or environment variables
- Creating repositories or managing GitHub settings

## Self-Contained Issues

**The Planner does the thinking, the Worker does the typing.**

Every issue must be self-contained. A worker with zero context beyond the issue body and the repository should be able to execute it without exploring the codebase for answers. This is not optional. Workers run in fresh, isolated sessions with no memory of prior work.

The Planner pays the research cost once during the interactive planning session. It reads the codebase, understands the architecture, identifies patterns and conventions, and encodes all of that into the issue. Every worker benefits from this upfront investment.

**Why this matters for cost:** A vague issue forces the worker agent to explore the codebase, grep through files, read documentation, and infer patterns -- burning tokens on research the Planner already did. A well-written issue with exact signatures, file paths, and conventions lets the agent go straight to implementation.

### What Every Issue Must Include

1. **Exact file paths.** Not "create a config module" but "create `internal/config/config.go`, `internal/config/defaults.go`, `internal/config/validate.go`."
2. **Implementation details.** If the task involves implementing an interface, include the exact signatures. If it involves a specific algorithm, describe it. If there is a data format, show it. The worker should be implementing a specification, not designing one.
3. **Patterns and conventions.** If the codebase uses specific patterns (error handling, naming, struct layout, test style), state them explicitly. For example: "Use table-driven tests," "Stub methods return `errors.New(\"not implemented\")`, not panic," "Add compile-time interface checks."
4. **Context from related issues.** If this issue depends on types or functions created by another issue, include those types inline. Do not say "use the Issue type from #4" -- paste the type definition. Repetition across issues is fine and expected. The cost of a few extra tokens in the issue body is trivial compared to the cost of the worker exploring the codebase.
5. **Concrete acceptance criteria.** Not "tests pass" but "unit tests cover: loading valid config, missing file error, default values for omitted fields, env var overrides, validation of each field constraint."

### Example: Bad vs Good

**Bad issue:**

> Create the Platform interface and GitHub client scaffold.
> Acceptance criteria: all interfaces from the spec are defined; GitHub client connects and works; stubs for unimplemented methods.

This is vague. The worker must explore the codebase to figure out what interfaces exist, what methods they need, what patterns to follow, and how authentication should work. It will burn tokens on research the Planner already did.

**Good issue:**

> Define the Platform interface and all sub-service interfaces in `internal/platform/platform.go`. Define all platform-agnostic types in `internal/platform/types.go`. Scaffold the GitHub implementation in `internal/platform/github/client.go` with a working client constructor and stub methods.

The good version continues with exact interface signatures, type definitions, the authentication strategy (GITHUB_TOKEN, GH_TOKEN, `gh auth token` fallback), conventions (compile-time checks, stub error style), and concrete acceptance criteria for every deliverable. The worker can execute immediately without any research phase.

## Plan Output

The agent produces a structured plan containing:

- **Batch name** -- a short descriptive name for the overall unit of work, which becomes the GitHub Milestone title.
- **Tasks** -- an ordered list where each task includes:
  - **Title** -- concise name for the task, becomes the GitHub Issue title.
  - **Description** -- what to build (the "what").
  - **Implementation details** -- how to build it (the "how"): exact file paths, function signatures, algorithms, data formats. This is the core of making issues self-contained.
  - **Acceptance criteria** -- concrete, verifiable checks (the "done").
  - **Scope** -- the set of files this task will create or modify.
  - **Conventions** -- project-specific patterns the worker must follow.
  - **Context from dependencies** -- information from upstream tasks that this task needs, inlined so the worker never has to cross-reference other issues. The Planner already knows what each task produces and should tell downstream tasks explicitly.
  - **Complexity** -- low, medium, or high, used for estimation and worker resource allocation.
  - **Dependencies** -- references to other tasks in the plan that must complete first. These are translated to GitHub Issue numbers after issue creation.

The `implementation_details`, `conventions`, and `context_from_dependencies` fields encode the research the Planner has already done, so workers do not repeat it. When creating GitHub Issues from the plan, dependency indices are translated to issue numbers, and complexity maps to estimated complexity in the issue metadata.

## Automatic Repository Context

The planner automatically gathers repository context and injects it into the agent's system prompt, so the agent starts each planning session with a useful understanding of the project. Context gathering is best-effort — missing files or failed commands are silently skipped, and the corresponding section is omitted from the prompt.

### Context Sources

| Source | Description | Character Limit |
|--------|-------------|-----------------|
| Directory tree | 2-level deep listing, excluding `.git`, `node_modules`, `dist`, `vendor`, `bin` | 2000 |
| README.md | Project overview from the repository root | 2000 |
| Tech stack manifest | First of `go.mod`, `package.json`, or `Cargo.toml` found | 2000 |
| Git log | Last 10 commits in oneline format | 1000 |
| Active batches | Open GitHub milestones with issue counts | 1000 |

Per-section character limits prevent overwhelming the agent's context window. If a source exceeds its limit, the content is truncated.

Users can provide additional project-specific context via `.herd/planner.md` role instructions (see below).

## Role Instructions

If `.herd/planner.md` exists in the repository, its contents are appended to the planner's system prompt automatically. This is convention-based -- no configuration is needed. Drop the file in `.herd/` and it gets picked up.

Use this file to provide project-specific planning guidance: preferred decomposition patterns, naming conventions, architectural constraints, technology preferences, or any other context that should inform every planning session. The planning system prompt is a Go template that `herd` populates with repository context before passing to the agent; the role instructions file supplements this with project-specific knowledge.
