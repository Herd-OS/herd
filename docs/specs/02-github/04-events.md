# Events

HerdOS uses GitHub's event system instead of polling. This document describes the event flow, triggers, and how to avoid infinite loops.

## Event Architecture

```
USER ACTION           GITHUB EVENT              HERDOS RESPONSE
───────────           ────────────              ───────────────

herd dispatch #42  →  workflow_dispatch       →  Worker starts
                                                      │
Worker completes   →  workflow_run.completed  →  Integrator consolidates
                                                      │
Tier complete      →  (integrator logic)          →  Dispatch next tier
                                                      │
All tiers done     →  (integrator logic)          →  Batch PR opened
                                                      │
Agent review       →  (integrator logic)          →  Approve or fix cycle
                                                      │
Human approves PR  →  pull_request_review     →  Integrator merges (if CI passes)
                                                      │
Batch PR merged    →  pull_request.closed     →  Issues closed
                                                      │
Cron fires         →  schedule                →  Monitor patrols
Worker fails       →  workflow_dispatch       →  Monitor patrols (immediate)
```

## Event Types Used

### workflow_dispatch

The primary dispatch mechanism. The CLI triggers this event to start a worker.

```bash
# CLI calls GitHub API:
POST /repos/{owner}/{repo}/actions/workflows/herd-worker.yml/dispatches
{
  "ref": "main",
  "inputs": {
    "issue_number": "42"
  }
}
```

Only users with write access to the repo can trigger `workflow_dispatch`. This is the security boundary — only authorized users can dispatch workers.

The `ref` parameter (`"main"` above) tells GitHub which branch to find the workflow YAML on — it is NOT the branch the worker checks out (that's controlled by the `batch_branch` input). Always use the default branch for `ref` when dispatching, including from the Integrator.

### workflow_run

Triggers the Integrator when a worker completes. The Integrator consolidates the worker's branch into the batch branch, checks if the current tier is complete, and dispatches the next tier if so.

```yaml
on:
  workflow_run:
    workflows: ["HerdOS Worker"]
    types: [completed]
```

### pull_request_review

Triggers the Integrator when a review is submitted on a PR. The Integrator checks: is this an approval on a `[herd]` batch PR? Is CI passing? If yes, merges the PR. This lets humans approve and have the system handle the merge automatically.

```yaml
on:
  pull_request_review:
    types: [submitted]
```

### pull_request

The batch PR is a standard GitHub PR. HerdOS does not add a workflow trigger for `pull_request` events — the Integrator creates the batch PR and posts the agent review via `workflow_run`, not `pull_request`.

When the batch PR merges, issues auto-close via GitHub's native "Closes #N" references in the PR description (added by `herd integrator advance` when creating the PR).

### schedule

Triggers the Monitor patrol. GitHub runs cron-scheduled workflows on the default branch.

```yaml
on:
  schedule:
    - cron: '*/15 * * * *'
```

**Caveat:** GitHub may delay or skip scheduled runs during high load. The Monitor is designed to be tolerant of missed runs — it's stateless and catches up on the next patrol.

### Dependency unblocking

When an issue closes, blocked issues that depend on it may become unblockable. This is handled in two ways:

1. **Integrator** — `herd integrator advance` checks tier completion and unblocks the next tier's issues after each worker completes. This is the primary mechanism.
2. **Monitor** — catches any stragglers on the next patrol cycle (up to 15 minutes delay).

No additional workflow is needed. The Integrator handles within-batch dependencies via tier advancement, and the Monitor provides a safety net.

## Avoiding Infinite Loops

HerdOS has several automated feedback loops where one Action triggers another. Each has explicit termination conditions.

### Issue-label event loops

When Actions modify issues (adding labels, posting comments), they can trigger other Actions that listen for issue events.

**Prevention:**

1. **Use `GITHUB_TOKEN` for automated changes.** Actions triggered by `GITHUB_TOKEN` do NOT trigger further workflow runs. This is GitHub's built-in loop prevention.

2. **Filter on specific labels.** Only react to HerdOS-managed labels:
   ```yaml
   if: startsWith(github.event.label.name, 'herd/')
   ```

3. **Guard clauses.** Check if the action was performed by a bot:
   ```yaml
   if: github.actor != 'github-actions[bot]'
   ```

4. **Idempotent operations.** All state changes should be safe to repeat. Labeling an already-labeled issue is a no-op.

### Worker → Integrator → Worker chain

The primary automated chain: a worker completes → `workflow_run.completed` fires → Integrator consolidates and advances → dispatches next-tier workers → they complete → Integrator fires again. This terminates because the DAG is finite — each tier is consumed exactly once, and once all tiers are done, the Integrator opens the batch PR instead of dispatching more workers.

The agent review cycle can dispatch fix workers (which trigger the chain again), but this is capped by `review_max_fix_cycles` (default: 3). After that, the Integrator stops and waits for human intervention.

### All automated loop caps

| Loop | Terminates because | Config cap |
|------|-------------------|------------|
| Tier advancement | DAG is finite | — |
| Agent review → fix → re-review | `review_max_fix_cycles` | Default: 3 |
| Monitor re-dispatch | `max_redispatch_attempts` | Default: 3 |
| Conflict resolution | `max_conflict_resolution_attempts` | Default: 2, then falls back to `notify` |
| CI failure after consolidation | Re-run once, then fix workers up to `ci_max_fix_cycles` | Default: 2 (0 = notify-only) |

## Event Flow: Complete Example

```
1. User: herd plan "Add auth"
   → Agent launches interactive session with "Add auth" as first message
   → User and agent discuss, agent produces plan
   → User approves and exits session

2. CLI creates issues and dispatches:
   → Creates issues #10, #11, #12
   → Issues labeled herd/status:ready (no deps) or herd/status:blocked
   → Creates batch branch: herd/batch/5-add-auth
   → workflow_dispatch for #10 (ready, Tier 0), passes batch_branch
   → #11, #12 are blocked (Tier 1), skipped

3. GitHub: workflow_dispatch triggers herd-worker.yml
   → Worker checks out batch branch
   → Worker executes #10 on branch herd/worker/10-add-user-model
   → Worker pushes, labels #10 as herd/status:done

4. GitHub: workflow_run.completed triggers herd-integrator.yml
   → Integrator merges herd/worker/10-add-user-model into batch branch
   → Tier 0 complete — #11, #12 unblocked to herd/status:ready
   → Integrator dispatches workers for #11, #12 (Tier 1)

5. Workers #11 and #12 execute in parallel
   → Both branch from updated batch branch (has #10's work)
   → Both push to their own worker branches

6. GitHub: workflow_run.completed for each worker
   → Integrator consolidates both into batch branch
   → All tiers complete

7. Integrator opens batch PR: herd/batch/5-add-auth → main
   → "[herd] Add auth (3 tasks)"

8. Agent reviews the batch PR
   → Checks acceptance criteria, bugs, security issues
   → If issues found: dispatch fix workers, re-review
   → If clean: approve

9. Human reviews (or auto-merge if enabled)
   → Batch PR merged → issues #10, #11, #12 auto-close

10. GitHub: schedule triggers herd-monitor.yml (every 15 min)
   → Monitor checks all in-progress batches
   → Everything healthy, no action needed
```

## Concurrency and Race Conditions

Multiple events can fire in quick succession (e.g., several workers completing around the same time, each triggering a `workflow_run` event). HerdOS handles this through idempotency and atomic guards.

### Concurrent consolidation

When two workers in the same tier complete near-simultaneously, two Integrator runs start and both try to merge into the batch branch. The second push will be rejected (non-fast-forward). The `consolidate` command handles this by pulling the latest batch branch, retrying the merge, and pushing again. If the retry also fails (unlikely — would require a third concurrent merge), it exits and the next Integrator trigger picks it up.

### Double-dispatch prevention

When two Integrator runs both see a tier as complete, both could try to dispatch the next tier's workers. To prevent duplicate worker runs, `advance` uses the issue label as an atomic guard: it sets `herd/status:in-progress` before dispatching, and skips any issue that is already `in-progress`. Since label transitions are atomic via the GitHub API, only one `advance` call will successfully transition a given issue.

### Fix cycle tier logic

The `advance` command distinguishes fix issues from original DAG issues by checking the `fix_cycle` front matter field. When fix issues exist in the milestone, `advance` checks whether all open fix issues for the current cycle are `done` before triggering a re-review. Original DAG tiers are tracked separately — fix issues do not participate in tier numbering.

### General rule

All state changes are idempotent — labeling an already-labeled issue is a no-op, dispatching an already-in-progress issue is skipped, and merging an already-merged branch is detected and skipped.
