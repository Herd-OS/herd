# Vision

## The Problem

Managing multiple AI coding agents is hard. As tools like Claude Code, Codex, and others become capable enough to handle real engineering tasks autonomously, a new problem emerges: how do you coordinate many of them working on the same codebase simultaneously?

Today's approach is ad-hoc. Developers open multiple terminal tabs, manually assign tasks, check back periodically, and hope nothing conflicts. This doesn't scale. The moment you have three or more agents working in parallel, you need:

- **Work decomposition** — breaking a feature into tasks agents can execute independently
- **Conflict management** — handling merge conflicts when agents touch overlapping code
- **Progress tracking** — knowing what's done, what's stuck, what failed
- **Health monitoring** — detecting and recovering from stalled or broken agents
- **Delivery coordination** — landing a set of related changes together

These are orchestration problems. They've been solved before — in CI/CD, in container orchestration, in distributed systems. But nobody has solved them specifically for AI coding agents in a way that's lightweight and practical.

## The Insight

GitHub already solves most of these problems. Issues track work. Actions execute jobs. Pull requests manage code review and merging. Milestones group related work into deliverable batches. The infrastructure exists — it just needs a thin orchestration layer on top.

Instead of building a custom orchestration runtime (with its own storage, its own UI, its own job queue), HerdOS uses GitHub as the orchestration backbone. The local CLI is just the entry point.

## What HerdOS Is

HerdOS is a GitHub-native orchestration platform for managing multiple agentic development systems. It consists of:

1. **A CLI tool** (`herd`) that runs locally — your interface to plan work, dispatch agents, and monitor progress
2. **GitHub Issues** as the work tracking layer — structured with labels and conventions so agents can read and update them
3. **GitHub Actions** as the execution layer — workers run agents in headless mode on self-hosted runners
4. **A set of reusable workflows** that handle merging, health monitoring, and delivery tracking

The user describes what they want. HerdOS breaks it into issues, dispatches workers, monitors progress, handles merges, and reports when everything lands.

## Positioning

HerdOS is the spiritual successor to Gastown, carrying forward its proven ideas about role decomposition, batch-based delivery, and autonomous workers. What makes HerdOS different from other orchestration approaches:

- **Lightweight** — no custom database, no local daemons, no persistent processes. The CLI is the only local component.
- **GitHub-native** — uses Issues for tracking, Actions for execution, and Milestones for batches. No new infrastructure to deploy or maintain.
- **Accessible** — everything is visible in the GitHub web UI. Monitor progress from anywhere with a browser.

## Target Users

- **Solo developers** using AI coding agents who want to parallelize their work — dispatch multiple agents while they focus on architecture or review
- **Small teams** coordinating AI agents across a shared codebase
- **Anyone already on GitHub** who wants agent orchestration without new infrastructure

## Non-Goals

- **Not a full operating system.** The "OS" in HerdOS is aspirational, not literal. It's an orchestration platform.
- **Not a replacement for GitHub.** HerdOS is a layer on top of GitHub. If GitHub adds native agent orchestration, HerdOS adapts or becomes unnecessary.
- **Not agent-specific.** Ships with Claude Code support first, with more agents coming soon. The architecture supports any agent that can read a task and produce code changes.
- **Not enterprise-first.** Start simple, for individuals. Complexity comes later.
- **Not a hosted service.** HerdOS runs locally and uses your GitHub account. No SaaS, no vendor lock-in beyond GitHub itself.

## Lessons from Gastown

Gastown is a multi-agent orchestration system built by Steve Yegge for managing multiple Claude Code instances. It uses Go binaries, tmux for session management, Dolt for data storage, and a custom polling mechanism (GUPP) for agent coordination. HerdOS is its spiritual successor.

### What Gastown Got Right

**Role Decomposition.** Gastown's taxonomy of agent roles — Mayor, Witness, Refinery, Polecats — is genuinely useful. Each role has a clear responsibility and lifecycle. HerdOS keeps this with different names: Planner, Worker, Monitor, Integrator.

**Nondeterministic Idempotence (NDI).** AI agents are unreliable, so the system must tolerate failures and achieve correct outcomes through retry and oversight. HerdOS keeps this — GitHub Actions has built-in retry, the Monitor patrols for failures, and issues persist regardless of worker state.

**Molecular Expression of Work (MEOW).** Breaking large goals into agent-executable chunks. The key insight: AI agents work best on focused, well-specified tasks with clear acceptance criteria. HerdOS keeps the concept, simplifies the implementation — issues and milestones replace Beads, Molecules, and Formulas.

**Batches.** Grouping related work into delivery units. HerdOS maps batches to GitHub Milestones.

**The Propulsion Principle.** "If there is work on your Hook, YOU MUST RUN IT." Agents execute immediately on dispatch — no manual confirmation step. The system is fire-and-forget by default.

### What Caused Problems

**Local polling.** Gastown's biggest operational problem. The Deacon daemon, Witness, and Beads constantly poll on heartbeat cycles, draining laptop battery. A session can't run for more than ~2 hours on battery. HerdOS replaces this with event-driven Action triggers — zero local compute for work tracking.

**tmux session management.** Each agent gets a tmux window, providing a crude dashboard that's fragile, hard to access remotely, and consumes local resources. HerdOS eliminates this — GitHub's web UI is the dashboard.

**Dolt database.** Running a SQL server on a laptop for work tracking is overkill. HerdOS eliminates this — all state lives in GitHub Issues, labels, milestones, and Action logs.

**Complex directory structure.** Town roots, rig directories, worktree hierarchies, symlinked directories. HerdOS has one config file, a `.herd/` directory, and workflow files.

### The Mapping

```
GASTOWN                          HERDOS
-------                          ------
~/gt/ (town root)           ->   Any git repo with .herdos.yml
~/gt/.beads/ (Dolt DB)      ->   GitHub Issues + Labels
gt (CLI binary)             ->   herd (CLI binary)
Dolt SQL Server             ->   GitHub API
tmux sessions               ->   GitHub Actions runners
GUPP heartbeat polling      ->   Event-driven Action triggers
Mayor (persistent agent)    ->   herd plan (one-shot CLI command)
Polecat (ephemeral worker)  ->   GitHub Action worker job
Witness (persistent patrol) ->   Scheduled + on-demand Action
Refinery (persistent agent) ->   Integrator (workflow_run-triggered Action)
Deacon (daemon)             ->   Not needed (GitHub manages infra)
Convoy (Dolt-tracked group) ->   GitHub Milestone
Molecule (chained Beads)    ->   Future: workflow templates
Formula (TOML template)     ->   Future: decomposition templates
```
