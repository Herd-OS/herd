# Integrator

The Integrator consolidates work from multiple workers into a single batch PR, reviews it using an agent, and manages the merge to `main`. Instead of one PR per worker, all work in a batch lands as a single, reviewable pull request.

## The Problem

When three workers run in parallel, they each produce changes on separate branches. Without consolidation, a reviewer would need to review 3 separate PRs for what is conceptually one feature. This is noisy, hard to reason about, and makes it difficult to see the complete picture.

Additionally, workers branching from the same `main` commit can produce conflicting changes. The Integrator handles this too.

## How It Works

### Batch Branch

Every batch gets a single long-lived branch where all worker output is consolidated:

```
herd/batch/<milestone-id>-<slug>
```

For example: `herd/batch/5-add-jwt-authentication`

### DAG Execution

Tasks in a batch form a directed acyclic graph (DAG) based on their dependencies. The Integrator executes them in tiers:

```
                Task 1 (add models)           ← Tier 0: no deps
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
    Task 2       Task 3      Task 4           ← Tier 1: all depend on 1
    (API)        (UI)        (tests)
        │           │           │
        └───────────┼───────────┘
                    ▼
               Task 5 (integration)           ← Tier 2: depends on 2,3,4
```

Each tier runs in parallel. Workers in the same tier branch from the current batch branch (which already contains all previous tiers' work). When a tier completes, the batch branch is updated with all their output before the next tier begins.

The batch branch is **not** rebased onto `main` between tiers — only when all tiers are complete and the batch PR is about to be opened. This means later-tier workers see prior tiers' work but not changes that landed on `main` after the batch started. This is intentional: mid-batch rebasing would invalidate prior tiers' work and introduce unpredictable conflicts.

#### Tier Assignment Algorithm

Tiers are computed from issue dependencies using a topological sort (Kahn's algorithm):

1. Build a dependency graph from each issue's `depends_on` field (issue numbers in the YAML front matter)
2. Issues with no dependencies are Tier 0
3. Issues whose dependencies are all in Tier N or earlier are assigned Tier N+1
4. If a cycle is detected (no issues with zero in-degree remain but unassigned issues exist), `herd` reports an error listing the circular dependencies and refuses to dispatch

This runs in `herd dispatch`, `herd plan`, and `herd integrator advance` — anywhere tier membership matters.

**Cross-batch dependencies are not supported.** All `depends_on` references must point to issues within the same milestone. The CLI validates this during planning and dispatch, and reports an error if a dependency points to an issue in a different batch (or no batch).


#### Tier Completion

A tier is **complete** when all its issues are `herd/status:done`. If any issue is `herd/status:failed`, the tier is **stuck** — the Integrator does not advance to the next tier. The Monitor detects stuck tiers and can re-dispatch failed issues (if `auto_redispatch` is enabled, up to `max_redispatch_attempts` times) or escalate to the user.

The `herd integrator advance` command:
1. Gets the completed run's issue number from the workflow run inputs
2. Looks up the issue's milestone to identify the batch
3. Rebuilds the DAG from all issues in the milestone
4. Determines the current tier (the lowest tier with any non-`done` issues)
5. If all issues in the current tier are `done`, labels next-tier issues as `herd/status:ready` and dispatches workers for them (respecting `workers.max_concurrent` globally across all batches — remaining issues stay `ready` for the next `advance` call or manual `herd dispatch`). Uses the label transition as an atomic guard to prevent double-dispatch from concurrent `advance` calls (see [events.md](../02-github/04-events.md#double-dispatch-prevention))
6. If all tiers are complete, opens the batch PR and runs agent review

### Flow

```
Batch created (e.g., milestone "Add JWT auth" with 5 tasks)
        │
        ▼
herd plan (or herd dispatch) creates batch branch from main:
  herd/batch/5-add-jwt-authentication
        │
        ▼
Tier 0: dispatch workers with no dependencies
  Workers branch from the batch branch
  Workers push to their own branches: herd/worker/42-add-models
        │
        ▼
Tier 0 complete:
  Integrator merges worker branches into the batch branch
  Resolves any conflicts between parallel workers
        │
        ▼
Tier 1: dispatch workers whose dependencies are satisfied
  Workers branch from the updated batch branch
  (which now contains Tier 0's work)
        │
        ▼
Tier 1 complete:
  Integrator merges worker branches into the batch branch
        │
        ▼
  ... continues until all tiers complete ...
        │
        ▼
All tasks done:
  Single PR opened: batch branch → main
  Agent reviews the batch PR
        │
    ┌───┴───┐
    │       │
  Clean   Issues found
    │       │
    ▼       ▼
  Ready   Dispatch fix workers
  to      (update batch branch,
  merge    re-review)
        │
        ▼
  Human reviews (or auto-merge if enabled)
        │
        ▼
  Merge to main
```

### Batch Branch Creation

The batch branch is created when workers are first dispatched for a batch — by `herd plan` (default) or `herd dispatch` (if `--no-dispatch` was used during planning). The command:

1. Looks up the issue's milestone to identify the batch
2. Derives the branch name: `herd/batch/<milestone-id>-<slug>`
3. If the branch doesn't exist, creates it from the current `main`
4. Passes the branch name as the `batch_branch` input to the worker workflow

Subsequent dispatches for the same batch reuse the existing branch. Workers always check out the batch branch (via the `batch_branch` workflow input) and create their own worker branch from it.

### Branch Cleanup

**Worker branches** are deleted after successful consolidation into the batch branch. The Integrator deletes them via the Platform API (`RepositoryService.DeleteBranch`) once the merge is confirmed. Failed worker branches are kept for debugging until the issue is re-dispatched or the batch is cancelled.

**Batch branches** are deleted in two cases:
- **On cancel:** `herd batch cancel` deletes the batch branch (documented in [batches.md](05-batches.md))
- **On merge:** When the batch PR merges, GitHub can auto-delete the head branch (if the repo setting is enabled). If not, the Integrator deletes it after confirming the merge.

### Run-to-Branch Resolution

`herd integrator consolidate --run-id <id>` needs to find the worker branch from a completed workflow run:

1. Query the GitHub API for the run's inputs (the `workflow_dispatch` event payload)
2. Extract the `issue_number` input
3. Derive the worker branch name from convention: `herd/worker/<issue_number>-<slug>` (slug from the issue title)
4. Check the run's conclusion: `success` → look for worker branch; `failure` → update issue labels, skip merge
5. If the worker branch exists, merge into batch branch. If no worker branch exists (no-op worker — the task was already done), skip merge. Either way, the issue is `done` and counts toward tier completion.

### Batch PR Format

When all tiers are complete, the Integrator rebases the batch branch onto the latest `main` and opens a PR. If the rebase fails due to conflicts and `on_conflict` is `notify`, the Integrator opens the PR anyway (in its un-rebased state) and comments with the conflict details so the user can resolve manually. If `on_conflict` is `dispatch-resolver`, it dispatches a conflict-resolution worker first (see [Conflict between batch branch and main](#conflict-between-batch-branch-and-main)).

The PR follows this template:

**Title:** `[herd] <batch name> (<N> tasks)`

Example: `[herd] Add JWT authentication (5 tasks)`

**Body:**

```markdown
## Summary

Batch **Add JWT authentication** — 5 tasks across 2 tiers.

## Tasks

| Issue | Title | Tier | Status |
|-------|-------|------|--------|
| #42 | Add user model with password hashing | 0 | done |
| #43 | Create auth middleware for JWT validation | 0 | done |
| #44 | Create login and register endpoints | 1 | done |
| #45 | Add auth integration tests | 1 | done |
| #46 | Add auth dependencies | 0 | done |

## Worker branches

- `herd/worker/42-add-user-model` (4 commits)
- `herd/worker/43-create-auth-middleware` (3 commits)
- `herd/worker/44-create-login-register` (5 commits)
- `herd/worker/45-add-auth-integration-tests` (2 commits)
- `herd/worker/46-add-auth-dependencies` (1 commit)

No-op workers (issues where the work was already done) are omitted from this list.
```

The title prefix `[herd]` is hardcoded.

### Issue Closure

The Integrator **explicitly closes issues via the Platform API** after the batch PR merges — it does not rely on GitHub's `Closes #N` auto-close syntax. This is intentional:

1. **Portability.** Auto-close keywords vary across platforms (GitHub, GitLab, Gitea have different syntax and behavior). Explicit API calls work identically everywhere.
2. **Reliability.** GitHub's keyword parsing is fragile with formatting variations (comma-separated lists, squash merges collapsing commit messages, etc.). API calls are deterministic.
3. **Control.** The Integrator can verify each issue's final state, add a closing comment with a summary, and handle edge cases (e.g., fix issues added during review cycles).

After confirming the batch PR merge, the Integrator:
1. Lists all issues in the batch milestone
2. Closes each issue via `IssueService.Update(number, {State: "closed"})`
3. Closes the milestone via `MilestoneService.Update(number, {State: "closed"})`

## Role Instructions

If `.herd/integrator.md` exists in the repository, its contents are appended to the Integrator's agent prompts (both the review prompt and the conflict-resolution prompt). This is convention-based — no configuration is needed. Drop the file in `.herd/` and it gets picked up automatically. Use this to provide project-specific review guidance: areas requiring extra scrutiny, known fragile code paths, or merge policies.

## Agent Review

When all tiers are complete and the batch PR is opened, the Integrator dispatches an agent to review the consolidated changes. The agent:

1. Reads the full diff (batch branch vs main)
2. Checks each task's acceptance criteria against the actual changes
3. Looks for bugs, security issues, missing edge cases, and style violations
4. Posts a review on the batch PR

### Review outcomes

| Result | Action |
|--------|--------|
| Approved | PR is ready for human review (or auto-merge) |
| Changes requested | Integrator dispatches fix workers for each issue found |

If the fix cycle reaches `review_max_fix_cycles` without approval, the Integrator comments on the PR with the remaining issues and waits for human intervention.

### Fix cycle

When the agent reviewer finds issues, the Integrator creates fix issues and dispatches workers:

```
Agent review finds 2 issues
        │
        ▼
Integrator creates fix issues in the same milestone
  #50 "Fix SQL injection in auth endpoint"
  #51 "Add missing null check in user lookup"
        │
        ▼
Workers execute fixes, push to worker branches
  herd/worker/50-fix-sql-injection
  herd/worker/51-add-null-check
        │
        ▼
Integrator consolidates fixes into batch branch
        │
        ▼
Agent re-reviews
        │
        ▼
Clean → ready to merge
```

This cycle repeats until the agent approves or `review_max_fix_cycles` is hit.

#### Fix issue format

Fix issues are standard GitHub Issues with the normal front matter format. They belong to the same milestone as the batch. They have no `depends_on` (they're all independent fixes) and are labeled `herd/type:fix` and `herd/status:ready`.

```markdown
---
herd:
  version: 1
  type: fix
  scope: ["src/routes/auth.ts"]
  depends_on: []
  fix_cycle: 1
  batch_pr: 48
---

## Task

Fix SQL injection vulnerability in the auth endpoint.

## Context

Found during agent review of batch PR #48 ([herd] Add JWT authentication).

The `loginUser` function in `src/routes/auth.ts` interpolates user input directly into a SQL query on line 45.

## Acceptance criteria

- User input is parameterized, not interpolated
- Existing auth tests still pass
```

The `fix_cycle` field tracks which review cycle spawned this issue (1 = first review, 2 = second, etc.). The `batch_pr` field links back to the PR being reviewed.

Fix workers branch from the current batch branch (which has all prior work) and push to standard worker branches. The normal `consolidate → advance` flow handles them. Since fix issues have no dependencies and all belong to the same implicit "fix tier," they run in parallel.

After all fix workers in a cycle complete and are consolidated, the Integrator triggers a new agent review. If the review finds more issues and `fix_cycle < review_max_fix_cycles`, another cycle begins. If the limit is reached, the Integrator comments on the PR with the remaining issues and waits for human intervention.

**Safety valve:** If a single review cycle finds more than 10 issues, the Integrator does not create fix workers. Instead, it comments on the PR with all issues found and escalates to the user. This prevents a confused or overzealous agent from generating dozens of fix workers in one pass.

Fix issues are closed by the Integrator via the Platform API after the batch PR merges, along with all other issues in the milestone.

### Interaction with auto-merge

- **`review: true, auto_merge: false` (default):** Agent reviews first, then human reviews. The human gets a pre-screened PR with issues already fixed or flagged.
- **`review: true, auto_merge: true`:** Agent is the gatekeeper. If it approves and CI passes, the PR merges. If it finds issues, it blocks the merge and dispatches fix workers. Only clean PRs auto-merge.
- **`review: false, auto_merge: true`:** No agent review. The PR auto-merges as soon as CI passes. Use only with strong CI coverage.
- **`review: false, auto_merge: false`:** No agent review. Human reviews the batch PR directly.

### Configuration

```yaml
integrator:
  review: true                   # enable agent review of batch PRs
  review_max_fix_cycles: 3       # max fix-and-re-review cycles before escalating to user
```

## Merging

By default, the batch PR is created for **human review**. The reviewer sees the complete feature as one diff, with individual worker commits preserving the history of who did what.

### After human approval

When a human approves the batch PR, the Integrator detects the `pull_request_review` event and merges the PR automatically (if CI passes). The human's job is to review and approve — the system handles the merge. No extra click needed.

### Auto-merge (skip human review)

When `auto_merge` is enabled in `.herdos.yml`, the batch PR is merged automatically after agent review passes and CI passes — no human approval needed. This is opt-in for users who trust their pipeline and want a fully autonomous workflow.

```yaml
pull_requests:
  auto_merge: false  # default: human reviews batch PRs
```

## Conflict Resolution

Conflicts can occur in two places:

### Between parallel workers (same tier)

When workers in the same tier modify overlapping files, the Integrator needs to reconcile their changes when consolidating into the batch branch. Options in order of preference:

1. **Auto-rebase.** If the changes don't textually conflict, rebase succeeds automatically.
2. **Dispatch a conflict-resolution worker.** Create a task: "Resolve merge conflicts between #42 and #43." The worker reads both branches, understands the intent, and produces a resolution.
3. **Notify the user.** Comment on the relevant issues with the conflict details. The user resolves manually.

### Between the batch branch and main

Main may have moved forward while the batch was executing. Before opening the final PR, the Integrator rebases the batch branch onto the latest `main`. If this produces conflicts, the same resolution strategies apply.

The default strategy is configurable:

```yaml
integrator:
  on_conflict: "notify"  # notify | dispatch-resolver
  require_ci: true
```

### dispatch-resolver in detail

When `on_conflict: dispatch-resolver`, the Integrator creates a conflict-resolution issue and dispatches a worker for it:

1. **Detect conflict.** The Integrator attempts to merge the worker branch into the batch branch and gets a merge conflict.
2. **Create resolution issue.** The Integrator creates a new GitHub Issue in the same milestone:

```markdown
---
herd:
  version: 1
  type: fix
  scope: []
  depends_on: []
  conflict_resolution: true
  conflicting_branches:
    - herd/worker/42-add-auth-routes
    - herd/worker/43-add-auth-middleware
---

## Task

Resolve merge conflicts between #42 and #43 on the batch branch `herd/batch/5-add-jwt-authentication`.

### Conflicting files

- src/routes/index.ts
- src/middleware/auth.ts

### Context

Both workers modified overlapping files. Worker #42 added auth routes; worker #43 added auth middleware.
Review both branches, understand the intent of each change, and produce a merged result that preserves both contributions.

### Acceptance criteria

- All changes from both workers are preserved (no dropped functionality)
- No merge conflict markers remain
- Code compiles and existing tests pass
```

3. **Label and dispatch.** The issue is labeled `herd/status:ready` and `herd/type:fix`. The Integrator dispatches a worker for it immediately.
4. **Worker resolves.** The conflict-resolution worker checks out the batch branch (which has the last successful merge state), reads both worker branches, and produces the resolved merge. It pushes to its own worker branch (`herd/worker/<N>-resolve-conflicts`).
5. **Consolidate resolution.** When the resolution worker completes, the normal `consolidate → advance` flow runs. The Integrator merges the resolution branch into the batch branch, which now contains both workers' changes, resolved. If the resolution branch itself conflicts (e.g., another worker was consolidated while the resolver was running), the Integrator retries conflict resolution up to `max_conflict_resolution_attempts` (default: 2). After that, it falls back to `notify` regardless of the configured strategy.

The resolution issue belongs to the same milestone and same tier as the conflicting workers. It does not affect tier advancement — the tier is already stuck waiting for consolidation, and the resolution unblocks it.

### Conflict between batch branch and main

When auto-rebase onto `main` fails, the same `on_conflict` strategy applies (including the same `max_conflict_resolution_attempts` limit). The resolution issue mentions the batch branch and `main` as the conflicting branches instead of two worker branches. The worker checks out `main`, reads the batch branch diff, and produces a clean rebase. The result is force-pushed to the batch branch (this is the one case where force-push is acceptable — the batch branch is owned by HerdOS).

If the configured strategy is `notify`, the Integrator comments on the batch PR with the conflict details and waits for human intervention.

## Merge Strategy

How the final batch PR is merged:

| Strategy | Method | Result |
|----------|--------|--------|
| `squash` | Squash merge | Single commit on main, clean history (default) |
| `rebase` | Rebase merge | Individual worker commits preserved on main |
| `merge` | Merge commit | Merge commit, worker commits in branch |

```yaml
integrator:
  strategy: "squash"  # squash | rebase | merge
```

## Configuration

Full Integrator configuration in `.herdos.yml`:

```yaml
integrator:
  strategy: "squash"                       # squash | rebase | merge
  on_conflict: "notify"                    # notify | dispatch-resolver
  max_conflict_resolution_attempts: 2      # max resolver attempts before falling back to notify
  require_ci: true                         # require CI to pass before merge
  ci_max_fix_cycles: 2                     # max CI-failure fix cycles (0 = notify-only)
  review: true                             # enable agent review of batch PRs
  review_max_fix_cycles: 3                 # max fix-and-re-review cycles

pull_requests:
  auto_merge: false               # default: human reviews batch PRs
```

## Runaway Loop Protection

HerdOS has several automated feedback loops. Each has a hard cap to prevent runaway agent invocations:

| Loop | Cap | Default | What happens at limit |
|------|-----|---------|-----------------------|
| Agent review → fix workers → re-review | `review_max_fix_cycles` | 3 | Comments on PR, waits for human |
| Monitor re-dispatch of failed workers | `max_redispatch_attempts` | 3 | Labels issue `failed`, stops |
| Conflict resolution attempts | `max_conflict_resolution_attempts` | 2 | Falls back to `notify` |
| CI failure after consolidation | `ci_max_fix_cycles` | 2 | Re-runs failed Action once; if still failing, dispatches fix workers. At cap, notifies user. Set to 0 for notify-only. |

**Worst-case agent invocations per batch** (all defaults): 3 review cycles × N fix issues per cycle × 3 Monitor retries each, plus 3 reviewer invocations, plus 2 conflict resolution workers per conflict. For a typical batch of 5 tasks, this is bounded but can be expensive with pay-per-token billing. Reduce `review_max_fix_cycles` or `max_redispatch_attempts` if cost is a concern.

## Future: Batch-then-Bisect

For repos with many concurrent workers (10+) where individual CI runs are expensive, a batch-then-bisect strategy can test the entire batch branch tip first, then binary-bisect on failure to find the breaking worker. This is overkill for the typical 2-5 worker case.
