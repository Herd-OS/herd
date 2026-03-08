# Lessons from Gastown

Gastown is a multi-agent orchestration system built by Steve Yegge for managing multiple Claude Code instances simultaneously. It uses Go binaries (`gt` and `bd`), tmux for session management, Dolt for data storage, and a custom polling mechanism (GUPP) for agent coordination.

HerdOS is its spiritual successor. This document captures what Gastown got right, what caused problems, and how HerdOS addresses each.

## What Gastown Got Right

### Role Decomposition

Gastown's taxonomy of agent roles — Mayor, Witness, Refinery, Polecats — is genuinely useful. Each role has a clear responsibility and lifecycle. The Planner plans. Workers execute. The Monitor monitors. The Integrator integrates and reviews.

**HerdOS keeps this.** Same roles, different implementations. Planner runs locally in the CLI. Workers run as GitHub Actions. Monitor runs as a scheduled Action (also triggered on-demand by workers on failure). Integrator runs as a workflow-triggered Action.

### Nondeterministic Idempotence (NDI)

Gastown's core insight: AI agents are unreliable, so the system must tolerate failures and achieve correct outcomes through retry and oversight. Persistent work tracking (Beads) plus monitoring agents (Witness, Deacon) ensure workflows eventually complete even when individual steps fail.

**HerdOS keeps this.** GitHub Actions has built-in retry. The Monitor Action patrols for failures. Issues persist regardless of worker state. The system recovers from any individual worker failure.

### MEOW (Molecular Expression of Work)

Breaking large goals into agent-executable chunks. Gastown formalized this with Beads, Epics, Molecules, and Formulas. The key insight: AI agents work best on focused, well-specified tasks with clear acceptance criteria.

**HerdOS keeps the concept, simplifies the implementation.** The Planner decomposes work into GitHub Issues. Each issue is a self-contained task with acceptance criteria. No Molecules or Formulas in v1.0 — issues and milestones provide enough structure.

### Batches

Grouping related work into delivery units. A batch tracks a set of tasks from dispatch to landing. You can see what's in flight, what's done, and what's stuck — all in one view.

**HerdOS keeps this.** Batches map to GitHub Milestones. The CLI shows batch progress. Batch completion triggers notification.

### The Propulsion Principle

"If there is work on your Hook, YOU MUST RUN IT." Agents don't wait for confirmation — they execute immediately. This eliminates coordination overhead and makes the system self-driving.

**HerdOS keeps the spirit.** Workers execute immediately on dispatch. No manual confirmation step between dispatch and execution. The system is fire-and-forget by default.

## What Caused Problems

### Local Polling (GUPP + Beads)

Gastown's biggest operational problem. The Deacon daemon polls on a heartbeat cycle. The Witness polls polecat status. Beads constantly queries the Dolt database. All of this runs on the developer's laptop, draining battery. A Gastown session can't run for more than ~2 hours on battery power.

**HerdOS replaces this entirely.** GitHub Issues are stored in the cloud — zero local compute for work tracking. Actions trigger on events, not polling. The Monitor runs on a cron schedule in GitHub's infrastructure. The developer's machine only does work when the user explicitly runs a `herd` command.

### tmux Session Management

Gastown uses tmux to manage agent sessions. Each polecat gets a tmux window. The Witness runs in a pane. The Refinery has its own session. This provides a crude but functional dashboard, but it's fragile (sessions crash), hard to access remotely, and consumes local resources for every agent.

**HerdOS eliminates this.** Workers run as GitHub Actions — visible in the Actions tab. Issues show in the Issues tab. PRs show in the Pull Requests tab. GitHub's web UI is the dashboard, accessible from any browser, any device.

### Dolt Database

Gastown stores all Beads data in a Dolt SQL server per "town." This provides Git-like versioning for structured data, which is powerful but heavyweight. Running a SQL server on a laptop for work tracking is overkill. The server must be managed, monitored (by the Deacon), and restarted when it crashes.

**HerdOS eliminates this.** All state lives in GitHub: Issues for work items, labels for status, milestones for batches, Action logs for execution history. No local database at all.

### Complex Directory Structure

Gastown requires a specific directory layout: town root, rig directories, worktree hierarchies, symlinked Beads directories, redirect files. Getting the structure wrong causes subtle failures. New contributors find it confusing.

**HerdOS has minimal directory structure.** It's a CLI that works in any Git repository. One config file (`.herdos.yml`), a `.herd/` runtime directory (gitignored, for transient data like plan files), a few GitHub Action workflow files, and you're done.

### Agent Identity System

Gastown gives every agent a persistent identity (`gastown/polecats/Toast`) with a CV chain, work history, and performance tracking. This is interesting but adds significant complexity. Identity must be preserved across sessions, tracked in Dolt, and used for attribution.

**HerdOS drops persistent agent identity for v1.0.** GitHub Actions provides sufficient attribution: which workflow run produced which PR. Worker identity is the Action run ID. Performance tracking can be built later from Action logs.

## The Mapping

How each Gastown component maps to HerdOS infrastructure:

```
GASTOWN                          HERDOS
───────                          ──────
~/gt/ (town root)           →    Any git repo with .herdos.yml
~/gt/.beads/ (Dolt DB)      →    GitHub Issues + Labels
gt (CLI binary)             →    herd (CLI binary)
bd (Beads CLI)              →    GitHub Issues API
Dolt SQL Server             →    GitHub API
tmux sessions               →    GitHub Actions runners
GUPP heartbeat polling      →    Event-driven Action triggers (workflow_dispatch, workflow_run, schedule)
Mayor (persistent agent)    →    herd plan (one-shot CLI command)
Polecat (ephemeral worker)  →    GitHub Action worker job
Witness (persistent patrol) →    Scheduled + on-demand Action
Refinery (persistent agent) →    Integrator (workflow_run-triggered Action)
Deacon (daemon)             →    Not needed (GitHub manages infra)
Dogs (infra workers)        →    Not needed
Convoy (Dolt-tracked group) →    GitHub Milestone
Molecule (chained Beads)    →    Future: workflow templates
Formula (TOML template)     →    Future: decomposition templates
gt sling (dispatch)         →    herd dispatch
gt nudge (inter-agent msg)  →    Action events / issue comments
gt seance (query history)   →    GitHub API (issue/PR history)
```

## What's Deferred

These Gastown features are valuable but not needed for v1.0:

- **Formulas** — reusable work decomposition templates. Useful once the basic dispatch loop is proven.
- **Molecules** — multi-step chained workflows. Issues with dependencies handle simple cases.
- **Agent identity / CV** — performance tracking per agent. Can be built from Action logs later.
- **Cross-rig work** — working across multiple repositories simultaneously. v1.0 is single-repo.
- **Model A/B testing** — running different AI models on similar tasks to compare. Requires identity and metrics infrastructure.
- **Federation** — coordinating HerdOS across organizations. Far future.
