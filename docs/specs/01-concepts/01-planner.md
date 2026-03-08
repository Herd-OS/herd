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
      "description": "Add bcrypt and jsonwebtoken to package.json",
      "acceptance_criteria": ["bcrypt is in dependencies", "jsonwebtoken is in dependencies"],
      "scope": ["package.json"],
      "complexity": "low",
      "depends_on": []
    },
    {
      "title": "Create User model with password hashing",
      "description": "Create a User model in src/models/user.ts with bcrypt password hashing",
      "acceptance_criteria": ["User model exists", "Passwords are hashed with bcrypt", "Model exports cleanly"],
      "scope": ["src/models/user.ts"],
      "complexity": "medium",
      "depends_on": [0]
    }
  ]
}
```

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

## Relationship to Other Components

```
Planner ─── creates ──▶ Issues (with labels, milestones)
Planner ─── triggers ──▶ Workers (via workflow dispatch)
Planner ─── reads ─────▶ Action status, PR status, Issue state
Planner ─── creates ──▶ Batches (milestones)
```

The Planner has no direct communication with workers. All coordination happens through GitHub: Issues carry the task specification, labels carry the state, PRs carry the results.
