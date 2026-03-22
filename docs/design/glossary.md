# Glossary

Terms and naming conventions used throughout HerdOS.

## Core Concepts

### Agent
The AI coding tool that does the actual work. HerdOS ships with Claude Code support first, with more agents (Codex, Cursor, Gemini CLI, OpenCode) coming soon. Agents run in headless mode -- they read a task description, produce code changes, and commit. The agent is configured in `.herdos.yml` and abstracted behind an interface so agents are swappable.

### Planner
The local planner/orchestrator. Runs on your machine as part of the `herd` CLI. Uses the configured agent to decompose feature requests into discrete tasks (issues). Creates issues, labels, and milestones. Dispatches work.

### Worker
A GitHub Actions job running an agent in headless mode. Each worker is assigned a single issue, checks out the batch branch, executes the task, and pushes to a worker branch. Workers are stateless and ephemeral.

### Integrator
The consolidation, review, and merge management system. A GitHub Action that merges worker branches into the batch branch, manages tier-based execution, dispatches an agent to review the consolidated changes, and opens a single batch PR against `main` when all tasks are done. Can dispatch fix workers when the agent reviewer finds issues. Handles conflict resolution between parallel workers.

### Monitor
Health monitoring system. A GitHub Action (cron-scheduled and triggered on-demand by workers on failure) that patrols for problems: stale issues, failed Action runs, stuck batch PRs. Can re-dispatch failed work or escalate to the user.

### Batch
A group of related issues that form a delivery unit. Maps to a GitHub Milestone. A batch is "landed" when all its issues are closed and the batch PR is merged.

### Workflow
The end-to-end flow of a piece of work: user describes feature, Planner creates issues, workers execute in tiers, Integrator consolidates, single batch PR opened, agent reviews, human reviews (or auto-merge), batch lands. Not to be confused with GitHub Actions workflows (which are one component of a HerdOS workflow).

### Tier
A group of tasks within a batch that can execute in parallel. Tasks with no dependencies form Tier 0. Tasks that depend only on Tier 0 tasks form Tier 1, and so on. The Integrator executes tiers sequentially -- all workers in a tier must complete before the next tier starts.

### Agent Review
An automated review step where the Integrator dispatches an agent to review the consolidated batch PR. The agent checks acceptance criteria, looks for bugs and security issues, and posts a review. If issues are found, the Integrator dispatches fix workers and re-reviews (up to a configured limit, then escalates to human).

## Label Namespace

All HerdOS labels use the `herd/` prefix to avoid collisions with existing repo labels.

| Label | Meaning |
|-------|---------|
| `herd/status:ready` | Issue is ready for a worker to pick up |
| `herd/status:in-progress` | Worker is actively executing this issue |
| `herd/status:done` | Work complete, worker branch ready for consolidation |
| `herd/status:failed` | Worker failed, needs re-dispatch or manual intervention |
| `herd/status:blocked` | Issue depends on another issue that hasn't completed |
| `herd/status:cancelled` | Batch PR closed without merging — task was not completed |
| `herd/type:feature` | New functionality |
| `herd/type:bugfix` | Bug fix |
| `herd/type:fix` | Integrator-generated fix (review or conflict resolution) |
| `herd/type:manual` | Requires human action, not dispatched to workers |

## Naming Conventions

- **CLI binary:** `herd`
- **Config file:** `.herdos.yml`
- **Worker branch:** `herd/worker/<issue-number>-<slug>`
- **Batch branch:** `herd/batch/<milestone-id>-<slug>`
- **Batch PR title prefix:** `[herd]`
- **Action workflow files:** `herd-worker.yml`, `herd-monitor.yml`, `herd-integrator.yml`

### Slug Generation

Branch names use slugified titles:

1. Lowercase the source string (issue or milestone title)
2. Replace spaces and underscores with hyphens
3. Remove characters that aren't alphanumeric or hyphens
4. Collapse consecutive hyphens into one
5. Trim leading/trailing hyphens
6. Truncate to 50 characters (at a word boundary if possible)

Examples:
- Worker: `herd/worker/42-add-user-authentication-middleware`
- Batch: `herd/batch/5-add-jwt-authentication`
