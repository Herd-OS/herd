# Workflows

A workflow is the end-to-end path a piece of work takes from user request to merged code. This document describes the happy path and its failure modes.

## The Happy Path

```
1. PLAN        User describes a feature
                       │
                       ▼
2. DECOMPOSE   Planner breaks it into tasks (DAG)
                       │
                       ▼
3. CREATE      Issues created with labels and milestone
                       │
                       ▼
4. DISPATCH    Batch branch created, Tier 0 workers triggered
                       │
                       ▼
5. EXECUTE     Workers run agent, push to worker branches
                       │
                       ▼
6. CONSOLIDATE Integrator merges worker branches into batch branch
                       │
                       ▼
7. NEXT TIER   Dispatch workers for next tier, repeat 5-6
                       │
                       ▼
8. PR          Single batch PR opened against main
                       │
                       ▼
9. REVIEW      Agent reviews, dispatches fix workers if needed
                       │
                       ▼
10. APPROVE    Human reviews (or auto-merge if enabled)
                       │
                       ▼
11. LAND       Batch PR merged, issues closed, user notified
```

### Step by Step

**1. Plan.** The user runs `herd plan "Add search functionality"`. The Planner launches the configured agent in interactive mode with this as the first message. The agent reads the codebase, asks clarifying questions if needed, and produces a decomposition.

**2. Decompose.** The Planner identifies 4 tasks: add search index, build query API, create search UI, write tests. It maps dependencies: API depends on index, UI depends on index, tests depend on API and UI. This forms a DAG with tiers:
- Tier 0: search index (no deps)
- Tier 1: query API, search UI (both depend on index — run in parallel)
- Tier 2: tests (depends on API and UI)

**3. Create.** The CLI creates 4 GitHub Issues with:
- `herd/status:ready` label (or `herd/status:blocked` if dependencies exist)
- `herd/type:feature` or `herd/type:bugfix` label (determined by the agent during planning)
- Structured body with acceptance criteria
- Milestone assignment (the batch)

**4. Dispatch.** `herd plan` automatically creates the batch branch from `main` (`herd/batch/5-add-search`) and dispatches Tier 0 workers — in this case, only the search index task. The batch branch name is passed as the `batch_branch` input to the worker workflow. (Users can also dispatch manually with `herd dispatch` if they used `--no-dispatch` during planning.)

**5. Execute.** The worker Action starts on a runner. It checks out the batch branch, reads the issue body, runs the agent with the task specification, and commits the result to a worker branch `herd/worker/42-add-search-index`.

**6. Consolidate.** When the worker completes, the Integrator merges the worker branch into the batch branch.

**7. Next Tier.** With Tier 0 consolidated, the Integrator updates blocked issues to `herd/status:ready` and dispatches Tier 1 workers. These workers branch from the updated batch branch (which already contains the search index work). They run in parallel, and when all complete, the Integrator consolidates their branches into the batch branch.

**8. PR.** When all tiers are complete, the Integrator rebases the batch branch onto latest `main` and opens a single PR: batch branch → main. The PR title reflects the batch: "[herd] Add search functionality (4 tasks)".

**9. Agent Review.** The Integrator dispatches an agent to review the batch PR. The agent checks acceptance criteria, looks for bugs, security issues, and style violations. If it finds problems, it dispatches fix workers to address them on the batch branch, then re-reviews. This cycle repeats until the agent approves or `review_max_fix_cycles` is reached, after which the Integrator comments on the PR and waits for human intervention.

**10. Approve.** By default, a human reviews the pre-screened PR. Individual worker commits are visible in the PR history. If `auto_merge` is enabled, the PR merges automatically after agent review and CI pass — no human needed.

**11. Land.** The batch PR merges, all issues in the milestone auto-close, and the user is notified.

## Failure Modes

### Worker fails to complete task

```
Worker crashes or times out
        │
        ▼
Action run shows as failed
        │
        ▼
Worker triggers Monitor via workflow_dispatch (immediate response)
        │
        ▼
Monitor re-dispatches (if auto_redispatch enabled, up to max_redispatch_attempts)
   or
Monitor labels issue herd/status:failed and @mentions notify_users (max attempts reached or auto_redispatch disabled)
```

The batch branch is unaffected — the failed worker's branch is never merged into it.

### Worker produces code that doesn't build

```
Worker pushes to worker branch
        │
        ▼
Integrator attempts to consolidate into batch branch
        │
        ▼
CI runs on the updated batch branch, tests fail
        │
        ▼
Re-run the failed Action (transient/flaky failure filter)
        │
        ├── Passes → done, continue normally
        │
        └── Fails again → confirmed real failure
                │
                ▼
        Agent analyzes failure logs, creates fix issues
                │
                ▼
        Fix workers execute → re-consolidate → CI runs again
                │
                ├── Passes → done
                └── Fails → repeat up to ci_max_fix_cycles (default: 2)
                        │
                        ▼ (at cap)
                Integrator reverts the consolidation
                Issue labeled herd/status:failed, comment posted with CI failure details
                User investigates
```

The initial re-run filters out transient failures (flaky tests, infra issues) without wasting agent tokens. Only confirmed failures trigger the fix cycle. Set `ci_max_fix_cycles: 0` to disable auto-fix and always notify instead.

### Merge conflict between parallel workers

```
Worker A and Worker B complete in the same tier
        │
        ▼
Worker A merged into batch branch successfully
        │
        ▼
Worker B conflicts with Worker A's changes
        │
        ▼
Option A: Dispatch conflict-resolution worker (on_conflict: dispatch-resolver, capped at max_conflict_resolution_attempts)
Option B: Notify user (on_conflict: notify, or after resolver cap reached)
```

### Recovering from a stuck tier

When a tier is stuck (a worker failed and auto-redispatch is exhausted), the user has these options:

1. **Fix and re-dispatch.** Edit the issue description to clarify the task or reduce scope, then `herd dispatch <issue-number>` to try again.
2. **Cancel the batch.** `herd batch cancel <number>` stops everything and labels remaining issues as `failed`.

The user cannot skip a single failed issue or remove it from the batch in v1.0. If the failed issue blocks the entire batch and cannot be fixed, cancelling and re-planning is the recommended path.

### Dependency cycle or impossible task

```
Planner creates issue with unclear requirements
        │
        ▼
Worker can't determine what to do
        │
        ▼
Worker labels issue: herd/status:failed
Worker triggers Monitor via workflow_dispatch
        │
        ▼
Monitor comments on issue with diagnostics (from Action run logs)
Monitor @mentions notify_users
Tier is blocked — remaining tiers cannot proceed
User revises the issue or resolves manually
```

## Dispatch Model

In v1.0, the dispatch flow is:

1. **`herd plan` dispatches Tier 0** automatically after the user approves the plan. No separate command needed.
2. **`herd integrator advance` dispatches subsequent tiers** automatically when a tier completes (triggered by the `workflow_run` event after each worker finishes).
3. **The Monitor re-dispatches failed work** if `auto_redispatch` is enabled.

The user only needs to run `herd plan` — the system handles everything from there. Come back when the batch PR is ready for review (or merged, if `auto_merge` is enabled).

For manual control, `herd plan --no-dispatch` creates issues without dispatching. The user can then dispatch with `herd dispatch --batch <N>`.

If the planning session is interrupted before the user confirms the plan (Ctrl+C, agent crash, etc.), no issues or milestones are created. Issue creation only happens after explicit user approval.
