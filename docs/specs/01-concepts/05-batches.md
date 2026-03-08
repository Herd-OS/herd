# Batches

A batch is a group of related issues that together form a delivery unit. When all issues in a batch are done and the batch PR is merged, the batch has "landed."

## Concept

When you ask HerdOS to implement a feature, the Planner typically creates multiple issues. These issues are related — they're all part of the same feature. A batch groups them so you can track the feature as a whole, not just individual tasks.

```
Batch: "Add dark mode support"
├── #42 Add CSS custom properties for theme colors    [done]
├── #43 Create theme toggle component                 [in-progress]
├── #44 Add theme persistence to localStorage         [ready]
└── #45 Add dark mode tests                           [blocked by #43]

Progress: 1/4 done, 1/4 in progress
```

## GitHub Implementation

Batches map to **GitHub Milestones**. Milestones already provide:

- A title and description
- A set of associated issues
- A progress bar (open vs closed issues)
- A due date (optional)
- API access for querying status

When the Planner creates issues for a feature, it also creates a Milestone and assigns all issues to it.

### Batch Branches

Each batch has a corresponding Git branch: `herd/batch/<milestone-id>-<slug>`. This branch is created from `main` when workers are first dispatched — by `herd plan` (default) or `herd dispatch` (if `--no-dispatch` was used during planning). Workers branch from it, and the Integrator consolidates their work back into it as they complete. When all tiers are done, the batch branch becomes the source of the single batch PR against `main`.

### Why Milestones Over Projects

| Feature | Milestones | Projects |
|---------|-----------|----------|
| Setup complexity | Zero (built-in) | Requires project board creation |
| API simplicity | Simple REST endpoints | GraphQL-heavy |
| Progress tracking | Built-in percentage | Requires custom views |
| Issue association | Direct field on issue | Requires adding to project |
| Suitable for | Task batches with clear completion | Ongoing work streams |

Milestones are the right fit for batches because batches have a clear end state: all issues closed.

Cross-repo batches are a future goal (see [roadmap](../04-implementation/01-roadmap.md)) but will not necessarily use GitHub Projects.

## CLI Interface

```bash
# List active batches
$ herd batch list
  #5  Auth system refactor    3/5 done    2 workers active
  #7  Add dark mode support   0/4 done    dispatching...

# Show batch details
$ herd batch show 7
Batch: Add dark mode support
Status: 1/4 complete

  #42 Add CSS custom properties    ✓ consolidated
  #43 Create theme toggle          ⟳ worker active (run 12345)
  #44 Add theme persistence        ○ ready
  #45 Add dark mode tests          ◌ blocked by #43

# Dispatch all ready and failed issues in a batch
$ herd dispatch --batch 7
Dispatching 1 issue: #44
(#45 blocked by #43, skipping)
```

## Batch Lifecycle

```
Created ──▶ In Progress ──▶ Landed
              │      ▲
              │      │
              ▼      │
           Stalled (Monitor detects)
```

- **Created**: Milestone exists, issues created, nothing dispatched yet
- **In Progress**: At least one worker is active or one issue is `done`
- **Stalled**: Issues are stuck (failed workers, unresolved conflicts) — the Monitor escalates
- **Landed**: All issues done, batch PR merged. The batch is complete.

When a batch lands, the Integrator:
- Closes the milestone
- Updates the milestone description with a summary of the landed batch

## Dependencies Between Issues

Issues within a batch can have dependencies. An issue is `herd/status:blocked` until its dependencies are resolved.

Dependencies are declared in the issue body:

```yaml
---
herd:
  depends_on:
    - 42
    - 43
---
```

When issue #42 is done and its tier completes, the Integrator (`herd integrator advance`) unblocks dependent issues by updating their labels from `herd/status:blocked` to `herd/status:ready`. The Monitor acts as a safety net, catching any stragglers on the next patrol cycle.

`herd dispatch --batch` dispatches both `ready` and `failed` issues, skipping `blocked` and `in-progress` issues automatically.

## Cancellation

Cancel a batch with `herd batch cancel <number>`. This:

1. Cancels any active workflow runs for the batch's issues
2. Labels remaining open issues as `herd/status:failed`
3. Closes the milestone
4. Deletes the batch branch

Active workers may take a moment to stop — GitHub Actions cancellation is asynchronous.
