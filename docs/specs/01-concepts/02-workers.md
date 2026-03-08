# Workers

Workers are GitHub Actions jobs that run an agent in headless mode to execute tasks defined in GitHub Issues. HerdOS ships with Claude Code support, with more agents (Codex, Cursor, Gemini CLI, OpenCode) coming soon.

## Concept

A worker is a single GitHub Actions workflow run. It receives an issue number, checks out the batch branch, reads the issue, and executes the task. If changes are needed, it commits the result to a worker branch and pushes. If the work is already done (acceptance criteria already met), it labels the issue as done without creating a branch. Then it's done — workers are stateless and ephemeral. The Integrator handles consolidating worker branches into the batch branch and opening the final PR.

Workers are stateless and ephemeral — they have no persistent identity and require no local process management. GitHub Actions handles scheduling, logging, and cleanup.

## Worker Lifecycle

```
Dispatch                    Execution                    Completion
────────                    ─────────                    ──────────

herd dispatch #42           Action starts on runner
        │                          │
        ▼                          ▼
workflow_dispatch ──▶   1. Checkout batch branch
                        2. herd worker exec 42:
                           a. Read issue #42 body
                           b. Label issue: herd/status:in-progress
                           c. Create worker branch: herd/worker/42-add-css-vars
                           d. Run agent in headless mode
                              (agent commits as it works)
                           e. Push worker branch
                           f. Label issue: herd/status:done (or failed)
                        3. Exit
                        (Integrator consolidates into batch branch)
```

## Worker Execution

The core of a worker is `herd worker exec <issue>`, which invokes the configured agent in headless mode with a carefully constructed prompt:

```
You are a HerdOS worker executing a task.

## Task
<issue title and body>

## Acceptance Criteria
<parsed from issue body>

## Before You Start
- Check if the acceptance criteria are already satisfied by existing code. If so, report that no changes are needed and exit successfully without making any commits.

## Constraints
- Only modify files relevant to this task
- Commit your changes with clear messages referencing issue #<number>
- If you cannot complete the task, exit with a non-zero status and include the reason in your output

## Scope (only included if scope is non-empty in the issue front matter)
- Focus on these files/directories: <scope list>
- Do not modify files outside this scope
```

`herd worker exec` handles the full lifecycle: reading the issue, creating the worker branch, invoking the agent (which commits as it works), and pushing. Workers don't open PRs — the Integrator handles that after consolidating all worker branches into the batch branch.

## Concurrency

Multiple workers can run simultaneously — that's the whole point. Each worker operates on its own branch, isolated from others.

Concurrency is bounded by:
- **Runner availability** — how many self-hosted runners you have
- **Configuration** — `max_concurrent` setting in `.herdos.yml`
- **GitHub limits** — Actions concurrency limits per repo/org

The default `max_concurrent` is 3. This can be increased if you have enough runners and your codebase tolerates parallel changes.

## Runner Requirements

Workers run on GitHub Actions runners (self-hosted or GitHub-hosted). Requirements:

- The configured agent CLI installed and authenticated (e.g., `claude`, `codex`, `cursor`, `gemini`, `opencode`)
- `git` available
- Network access to GitHub API
- Sufficient disk for repo checkout

Self-hosted runners on your own machine are the simplest option — the agent is already installed and authenticated.

## Failure Modes

### Worker crashes mid-task
The Action run shows as failed. The worker triggers the Monitor via `workflow_dispatch` so it can respond immediately instead of waiting for the next cron cycle. The Monitor detects the failed issue and handles escalation (re-dispatch if enabled, or label `herd/status:failed` and notify).

### Worker produces bad code
The worker's branch is merged into the batch branch, but CI fails on the updated batch. The Integrator first re-runs the failed Action to filter out transient failures. If it fails again, the Integrator dispatches fix workers (up to `ci_max_fix_cycles` times, default 2). If the cap is reached, the Integrator reverts the consolidation, labels the issue `herd/status:failed`, and comments with the CI failure details.

### Worker can't complete the task
`herd worker exec` labels the issue `herd/status:failed` and triggers the Monitor via `workflow_dispatch`. The Monitor handles escalation — commenting on the issue with diagnostic info and `@mentioning` the configured `notify_users`.

### Work already done (no-op)
The worker reads the issue, analyzes the codebase, and determines the acceptance criteria are already satisfied (e.g., a previous tier's work already addressed this issue). The worker labels the issue `herd/status:done` without creating a worker branch or making any commits. The Integrator sees the issue as done with no branch to consolidate and advances normally.

### Runner is offline
The Action queues until a runner becomes available. GitHub shows the job as "queued." No special handling needed.

## Configuration

In `.herdos.yml`:

```yaml
workers:
  max_concurrent: 3
  runner_label: "herd-worker"
  timeout_minutes: 30
```

The agent and model are configured in the `agent:` section of `.herdos.yml`, not under `workers:`. See [configuration](../03-cli/02-configuration.md).
