# Architecture

## System Overview

HerdOS has two halves: a local CLI (the Planner) and GitHub infrastructure (everything else). The CLI plans and dispatches. GitHub executes, tracks, and reports.

```
┌─────────────────────────────────────────────────────────────────┐
│                        YOUR MACHINE                             │
│                                                                 │
│  ┌──────────────────────┐                                       │
│  │     herd CLI          │                                       │
│  │     (Planner)         │                                       │
│  │                       │                                       │
│  │  - Decomposes work    │         ┌──────────────────────┐     │
│  │  - Creates issues     │────────▶│   Self-Hosted Runner  │     │
│  │  - Dispatches workers │         │   (optional, on same  │     │
│  │  - Monitors progress  │         │    or different host)  │    │
│  └───────────┬───────────┘         └──────────────────────┘     │
│              │                                                   │
└──────────────┼───────────────────────────────────────────────────┘
               │ GitHub API
               ▼
┌─────────────────────────────────────────────────────────────────┐
│                         GITHUB                                   │
│                                                                  │
│  ┌─────────────┐    ┌──────────────┐    ┌──────────────────┐    │
│  │   Issues     │    │   Actions     │    │   Batch PR        │    │
│  │             │    │              │    │                  │    │
│  │  Work items  │───▶│  Workers      │───▶│  Consolidated     │   │
│  │  with labels │    │  (agent)      │    │  code changes     │   │
│  │  & structure │    │              │    │  ready for review │   │
│  └─────────────┘    └──────────────┘    └────────┬─────────┘    │
│                                                   │              │
│  ┌─────────────┐    ┌──────────────┐             │              │
│  │  Milestones  │    │  Monitor      │             │              │
│  │              │    │  (cron+on-demand)│             │              │
│  │             │    │              │             ▼              │
│  │  Batch      │    │  Health       │    ┌──────────────────┐    │
│  │  tracking    │    │  monitoring   │    │  Integrator        │    │
│  └─────────────┘    └──────────────┘    │  Consolidate       │    │
│                                          │  Review & merge    │    │
│                                          └──────────────────┘    │
└──────────────────────────────────────────────────────────────────┘
```

## Data Flow

### Default flow (plan → dispatch → land)

```
User describes feature
        │
        ▼
   herd plan "Add dark mode support"
        │
        │  Interactive session: agent asks questions,
        │  user refines, agent produces plan
        ▼
   User approves → creates GitHub Issues, batch branch, dispatches Tier 0
   ┌─────────────────────────────────────┐
   │  #42 [herd/status:ready] Add CSS    │  ← dispatched
   │  #43 [herd/status:ready] Add toggle │  ← dispatched
   │  #44 [herd/status:blocked] Add tests│  ← waiting on #42, #43
   └─────────────────────────────────────┘
        │
        │  User walks away
        ▼
   Workers execute on runners
   Integrator consolidates worker branches into batch branch
   When a tier completes, Integrator dispatches next tier
        │
        ▼
   Cycle continues until all tiers are done
        │
        ▼
   Batch PR opened, agent reviews, human reviews (or auto-merged if enabled)
   User notified
```

The entire flow after `herd plan` is self-driving. The Planner dispatches Tier 0 automatically, the Integrator advances tiers, and the user only intervenes if something fails or when the batch PR is ready for human review.

For manual control, `herd plan --no-dispatch` creates issues without dispatching. The user can then dispatch with `herd dispatch --batch <N>`.

### Worker → Consolidate → PR flow (same in both modes)

```
   Batch branch created from main:
   herd/batch/5-add-dark-mode
         │
         ▼
   Tier 0: Workers with no dependencies
   ┌──────────┐  ┌──────────┐
   │ Worker 1  │  │ Worker 2  │
   │ Issue #42 │  │ Issue #43 │
   └─────┬────┘  └─────┬────┘
         │             │
         └──────┬──────┘
                ▼
   Integrator consolidates into batch branch
                │
                ▼
   Tier 1: Workers whose dependencies are done
   ┌──────────┐
   │ Worker 3  │
   │ Issue #44 │  (depends on #42, #43)
   └─────┬────┘
         │
         ▼
   Integrator consolidates into batch branch
                │
                ▼
   Single PR: batch branch → main
   "[herd] Add dark mode (3 tasks)"
                │
                ▼
   Agent reviews on the PR
   (dispatches fix workers if needed)
                │
                ▼
   Human reviews (or auto-merge if enabled)
   Issues closed, batch landed
```

## Component Boundaries

### Local (your machine)

| Component | Responsibility |
|-----------|---------------|
| `herd` CLI | User interface. Plans work, creates issues, dispatches workers, shows status. |
| Planner logic | Work decomposition. Uses the configured agent locally to break features into issues. |
| Config | `.herdos.yml` — repo-level settings for workers, labels, runners. |

### GitHub (cloud)

| Component | Implemented As | Responsibility |
|-----------|---------------|---------------|
| Work items | Issues + Labels | Track what needs doing, who's doing it, what state it's in. |
| Workers | Actions (workflow_dispatch) | Execute tasks. Each worker reads an issue, runs the agent in headless mode, pushes to a worker branch. |
| Integrator | Action (workflow_run + pull_request_review) | Consolidate worker branches into batch branch, agent-review the result, open single batch PR, handle conflicts, merge after human approval. |
| Monitor | Action (schedule + workflow_dispatch) | Health patrol. Detect stale issues, failed runs, stuck PRs. Triggered by cron and on-demand by workers on failure. |
| Batches | Milestones | Group related issues. Track delivery progress. |

### Self-Hosted Runners (your machine or cloud)

Workers need a runner with the agent CLI installed. Options:

1. **Self-hosted on your machine** — free, uses your hardware, needs the agent CLI installed
2. **Self-hosted on a cloud VM** — scalable, costs money, good for teams
3. **GitHub-hosted runners** — simplest setup, but need the agent CLI in the environment

The runner is where the agent actually executes. The Action workflow orchestrates the checkout, issue reading, and branch pushing around it.

## What Runs Where

```
LOCAL                          GITHUB                       RUNNER
─────                          ──────                       ──────

herd plan ──────────────▶ Issues created + Tier 0 dispatched
                                │
                                ▼
                         workflow_dispatch ─────▶ Worker starts
                                                  agent runs
                                                  commits pushed
                           Worker done ◀───────── pushes to worker branch
                           Integrator consolidates into batch branch
                           tier.complete ──▶ dispatch next tier
                           all tiers done ──▶ batch PR opened
                           agent reviews on PR
                           batch PR merged ──▶ issues closed
                           Monitor patrols (cron or worker-triggered)

herd status ◀────────────  reads Issues/PRs/Actions
```

## Key Design Decisions

1. **GitHub is the source of truth.** No local database. All state lives in Issues, PRs, and Action logs.
2. **Event-driven, not polling.** Workers trigger on `workflow_dispatch`. Integrator triggers on `workflow_run`. Monitor triggers on `schedule` and `workflow_dispatch` (workers trigger it on failure for immediate response). No busy-waiting.
3. **Workers are stateless.** Each worker gets a fresh checkout of the batch branch, reads its issue, does work, and pushes to a worker branch. No persistent state between runs.
4. **The CLI is thin.** It's a GitHub API client that delegates to the configured agent for planning. All heavy lifting happens on GitHub.
5. **Fail-safe by default.** Workers can't push to main. All changes go through PRs. Branch protection enforced.
