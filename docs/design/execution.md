# Execution Design

How work flows from plan to landed code. This document covers the end-to-end
execution pipeline: worker lifecycle, DAG-based tier execution, consolidation,
agent review, conflict resolution, monitoring, and batch management.

---

## 1. End-to-End Flow

The happy path from user request to merged code:

```mermaid
graph TD
    S1["1. PLAN<br>User describes a feature"] --> S2["2. DECOMPOSE<br>Planner breaks it into tasks (DAG)"]
    S2 --> S3["3. CREATE<br>Issues created with labels and milestone"]
    S3 --> S4["4. DISPATCH<br>Batch branch created, Tier 0 workers triggered"]
    S4 --> S5["5. EXECUTE<br>Workers run agent, push to worker branches"]
    S5 --> S6["6. CONSOLIDATE<br>Integrator merges worker branches into batch branch"]
    S6 --> S7["7. NEXT TIER<br>Dispatch workers for next tier, repeat 5–6"]
    S7 --> S8["8. PR<br>Single batch PR opened against main"]
    S8 --> S9["9. REVIEW<br>Agent reviews, dispatches fix workers if needed"]
    S9 --> S10["10. APPROVE<br>Human reviews (or auto-merge if enabled)"]
    S10 --> S11["11. LAND<br>Batch PR merged or closed, issues closed, cleanup"]
```

The user runs `herd plan` and the system handles everything from there. The
Planner decomposes work into a DAG, creates issues with a milestone, and
dispatches Tier 0. The Integrator advances tiers automatically. Come back when
the batch PR is ready for review (or already merged, if auto-merge is enabled).

---

## 2. Workers

A worker is a single GitHub Actions workflow run. It receives an issue number,
checks out the batch branch, reads the issue body, runs the agent headlessly,
commits the result to a worker branch, and exits. Workers are stateless and
ephemeral -- GitHub Actions handles scheduling, logging, and cleanup.

### Lifecycle

```mermaid
sequenceDiagram
    participant CLI
    participant GitHub Actions
    participant Runner

    CLI->>GitHub Actions: herd dispatch #42 (workflow_dispatch)
    GitHub Actions->>Runner: Action starts
    Note over Runner: 1. Checkout batch branch<br>2. herd worker exec 42:<br>  a. Read issue #42 body<br>  b. Label in-progress<br>  c. Create or resume worker branch<br>     herd/worker/42-slug<br>  d. Run agent headlessly<br>     (agent commits and pushes incrementally)<br>  e. Final push of worker branch<br>  f. Label done (or failed)<br>3. Exit
    Note over GitHub Actions: Integrator consolidates<br>into batch branch
```

### Headless Permissions

Workers run in fully automated CI with no human present. The agent must never
pause for permission prompts. This is safe because workers run on isolated
self-hosted runners (or ephemeral containers), operate on disposable worker
branches, and the Integrator reviews all changes before they reach main.

An optional `max_turns` setting limits agentic turns to prevent infinite loops
in headless mode. When set to zero (default), the agent uses its own limit.

### Commit Attribution

Worker commits attribute both the human and HerdOS:

- **Author**: the dispatching user (captured from `git config` at dispatch time)
- **Co-author**: HerdOS, via a `Co-authored-by` trailer

### Role Instructions

If `.herd/worker.md` exists in the repository, its contents are appended to the
worker's system prompt. Convention-based, no configuration needed.

### Pre-Push Validation

Before pushing changes, workers run validation commands:
1. `go build ./...`
2. `go test ./...`
3. `go vet ./...`
4. `golangci-lint run ./...` (if available)

If validation fails, the agent is re-invoked with the error output. If it fails again after retry, the worker is marked as failed.

Validation is Go-specific — it only runs when a `go.mod` file exists in the repository root.

### Worker Reports

After completing a task, workers post a structured report on the issue:
- Files changed (git diff stat)
- Summary of work done
- Validation results (build/test/vet/lint status)
- Full agent output in a collapsible details block

Workers also post a report on the no-op path (when no changes are needed). The
no-op report includes a "No changes were needed" message with the agent output
in a collapsible details block. Additionally, the worker posts a summary comment
on the batch PR so the reviewer can see why no changes were made. This prevents
the reviewer from creating fix issues for work that was already determined to be
unnecessary. Format: **Worker #N (no-op):** No changes needed. <explanation>

### Image Preprocessing

Before invoking the agent, the worker scans the issue body for GitHub-hosted
attachment images (URLs matching `github.com/user-attachments/assets/*`,
`github.com/<owner>/<repo>/assets/*`, and
`private-user-images.githubusercontent.com/*`). Matched images are downloaded to
`.herd/tmp/images/` and the markdown URLs are replaced with local file paths so
the agent can view them directly. External image URLs are left unchanged for the
agent to handle. Downloading is best-effort -- if the HTTP client is unavailable
or a download fails, the original URL is preserved.

### Incremental Push and Progress Tracking

Workers push incrementally during execution to preserve partial work. The
agent's system prompt instructs it to run `git push` after completing each
file or logical unit. This ensures that if the worker times out or crashes,
the retry starts from where it left off rather than from scratch.

To track progress, the agent creates a `.herd/progress/<issue-number>.md` file
before its first push. Each worker writes to a unique file (e.g., issue #17
writes to `.herd/progress/17.md`), preventing merge conflicts between parallel
workers. This file contains a checklist of completed and remaining items. On
retry, the agent reads the existing file to understand what was already done.

Example `.herd/progress/17.md`:
```
- [x] Create auth model in internal/auth/model.go
- [x] Add validation helpers
- [ ] Write unit tests
- [ ] Update API handler
```

The Integrator removes the `.herd/progress/` directory during consolidation
(after merging the worker branch into the batch branch) so progress files
do not appear in the final batch PR. For backward compatibility, legacy
`WORKER_PROGRESS.md` files at the repo root are also removed.

#### Retry Resume

When a worker is re-dispatched for a previously timed-out task, it checks
whether the worker branch already exists on the remote. If it does, the
worker checks out the existing branch (which contains partial work from the
previous attempt) instead of creating a fresh branch from the batch branch.
The agent then reads `.herd/progress/<issue-number>.md` to continue where
the previous attempt stopped.

If the merge of the batch branch into the resumed worker branch fails (e.g.,
because the batch branch has diverged with conflicting changes from other
workers' consolidation), the worker aborts the merge, deletes the stale worker
branch (both locally and on the remote), removes the
`.herd/progress/<issue-number>.md` file, and creates a fresh worker branch from
the current batch branch. A warning is logged:
"Merge conflict when updating resumed worker branch, starting fresh from batch
branch." The previous partial work is lost, but this is preferable to crashing
and leaving the issue stuck as failed.

### Concurrency

Multiple workers run simultaneously on separate branches. Concurrency is bounded
by runner availability, the `max_concurrent` config setting (default 3), and
GitHub Actions limits.

### Failure Modes

| Failure | Response |
|---------|----------|
| Worker crashes mid-task | Partial work preserved via incremental pushes; Action fails; worker triggers Monitor for immediate response; Monitor re-dispatches; retried worker resumes from existing branch and `.herd/progress/<issue-number>.md`; if the batch branch has diverged and merge conflicts, the worker falls back to a fresh branch (partial work is lost) |
| Worker produces bad code | Integrator dispatches fix workers up to the CI fix cap; at cap, reverts consolidation and labels issue failed |
| Worker can't complete task | Labels issue failed, triggers Monitor; Monitor comments diagnostics and @mentions notify_users |
| Work already done (no-op) | Posts a Worker Report comment ("No changes were needed"), labels issue done without creating a branch; Integrator advances normally |
| Stale conflict resolution issue | Automatically closed after successful consolidation; worker closes no-op conflict issues directly; non-fast-forward errors on stale branches don't block advance/review |
| Runner offline | Action queues until a runner is available; no special handling |

---

## 3. DAG and Tiers

Tasks in a batch form a directed acyclic graph based on their `depends_on`
declarations. The DAG determines execution order:

```mermaid
graph TD
    T1["Task 1 (add models)<br>Tier 0: no deps"] --> T2["Task 2 (API)"]
    T1 --> T3["Task 3 (UI)"]
    T1 --> T4["Task 4 (tests)"]

    subgraph Tier1["Tier 1 — all depend on Task 1"]
        T2
        T3
        T4
    end

    T2 --> T5["Task 5 (integration)<br>Tier 2: depends on 2, 3, 4"]
    T3 --> T5
    T4 --> T5
```

Tasks within a tier run in parallel. Tiers execute sequentially.

### Tier Assignment (Kahn's Algorithm)

1. Build a dependency graph from each issue's `depends_on` field
2. Issues with no dependencies are Tier 0
3. Issues whose dependencies are all in Tier N or earlier are Tier N+1
4. If a cycle is detected (no zero-in-degree issues remain but unassigned issues
   exist), the CLI reports the circular dependencies and refuses to dispatch

Cross-batch dependencies are not supported. All `depends_on` references must
point to issues within the same milestone; the CLI validates this during
planning and dispatch.

### Tier Completion

A tier is **complete** when all its issues are `herd/status:done`. If any issue
is `herd/status:failed`, the tier is **stuck** -- the Integrator does not
advance. The Monitor detects stuck tiers and can re-dispatch failed issues or
escalate.

### No Mid-Batch Rebase

The batch branch is not rebased onto main between tiers -- only when all tiers
are complete and the batch PR is about to open. Later-tier workers see prior
tiers' work but not changes that landed on main after the batch started. This
is intentional: mid-batch rebasing would invalidate prior tiers' work and
introduce unpredictable conflicts.

---

## 4. Consolidation

### Batch Branch Lifecycle

Every batch gets a long-lived branch: `herd/batch/<milestone-id>-<slug>`. It is
created from main when workers are first dispatched (by `herd plan` or
`herd dispatch`). Workers branch from it; the Integrator merges their work back
into it. When all tiers complete, this branch becomes the source of the single
batch PR against main.

### Consolidation Flow

```mermaid
graph TD
    A["Tier 0 workers complete"] --> B["Integrator merges worker branches<br>into batch branch<br>Resolves any conflicts between<br>parallel workers"]
    B --> C["Tier 1 workers branch from<br>updated batch branch<br>(contains Tier 0's work)"]
    C --> D["... continues until all tiers complete ..."]
    D --> E["Rebase batch branch onto latest main"]
    E --> F["Open single PR: batch branch → main"]
```

Opening the batch PR is idempotent: if concurrent advance-on-close triggers race, the second call detects the existing PR (via listing or by handling a 422 "already exists" error) and returns its number instead of failing.

### Run-to-Branch Resolution

Given a completed workflow run ID, the Integrator resolves the worker branch:

1. Query the run's workflow_dispatch inputs to extract the issue number
2. Derive the worker branch name from convention: `herd/worker/<number>-<slug>`
3. Check the run conclusion: success means look for a worker branch; failure
   means update issue labels and skip merge
4. If the worker branch exists, merge into batch branch. If no branch exists
   (no-op worker), skip merge. Either way, the issue counts toward tier
   completion.

### Post-Merge Failure Handling

If the merge succeeds but the push is rejected (e.g., non-fast-forward), the
Integrator relabels the issue from `herd/status:done` to `herd/status:failed`
and posts a diagnostic comment. This ensures the issue is retried automatically.
The Integrator resets the local batch branch to match the remote before each
checkout, preventing stale-branch issues.

**Non-fatal consolidation failures do not block advance and review.** Push
failures on stale branches and merge conflicts in notify mode are treated as
warnings — the issue is relabeled as failed (for the Monitor to handle), but the
consolidate command returns success so the workflow continues to run advance and
review. Only truly fatal failures (git unavailable, authentication errors,
corrupted state) cause the pipeline to stop.

### Stale Conflict Issue Cleanup

After a successful consolidation push, the Integrator scans for open conflict
resolution issues in the same milestone whose worker branches no longer exist
(already consolidated or deleted). These stale issues are automatically closed
with the comment: "Automatically closed — batch branch is already up to date."

This prevents the Monitor from retrying stale conflict resolution issues that
would fail with non-fast-forward errors and block the integrator pipeline.

Additionally, if a conflict resolution worker completes with no changes (the
batch branch is already up to date), the worker closes the issue directly
instead of marking it as done.

### Already-Merged Branch Detection

Before attempting a merge, the Integrator checks if the worker branch's changes
are already in the batch branch (the merge base equals the worker branch tip).
If so, the merge is skipped, the worker branch is deleted, and the result is
treated as a no-op. This handles cases where a previous integrator run already
merged the branch but the issue was re-triggered.

### Branch Cleanup

**Worker branches** are deleted after successful consolidation. Failed worker
branches are kept for debugging until re-dispatch or batch cancellation.

**Batch branches** are deleted on cancel (`herd batch cancel`), on merge
(GitHub auto-delete or Integrator cleanup), or when the batch PR is closed
without merging (Integrator cleanup).

---

## 5. Agent Review and Fix Cycles

When all tiers complete and the batch PR opens, the Integrator dispatches an
agent to review the consolidated diff. The agent checks acceptance criteria,
looks for bugs, security issues, and style violations. Before reviewing, the
reviewer collects any `/herd fix` comments from the batch PR and appends them
to the acceptance criteria list as `"User requested: <description>"`. This
ensures the reviewer checks user-requested changes equally alongside original
acceptance criteria, rather than treating them as a separate prompt section.

### Severity-Based Filtering

Review findings are classified by severity:

| Severity | Examples | Action |
|----------|----------|--------|
| HIGH | Bugs, security vulnerabilities, race conditions, missing critical error handling | Creates fix issues, dispatches workers |
| MEDIUM | Missing edge cases, suboptimal error handling | Creates fix issues, dispatches workers |
| LOW | Style preferences, naming suggestions | Listed in PR comment, no fix workers |
| CRITERIA | Acceptance criterion is wrong, incomplete, or contradictory | Listed in PR comment as requiring human review, no fix workers |

The CRITERIA severity is distinct from code issues. When the reviewer identifies
that an acceptance criterion itself is flawed (not the code), it flags it as
CRITERIA. These findings appear in the PR comment under a separate
"**CRITERIA** (requires human review)" section but never generate fix issues,
because changing acceptance criteria requires human judgment.

The `review_strictness` setting (standard/strict/lenient) controls which issues the agent flags. See [Configuration](../configuration.md#review-strictness) for details.

### Review Outcomes

| Result | Action |
|--------|--------|
| Approved | Agent posts a batch summary with statistics (files reviewed, findings by severity, fix cycles used). PR is ready for human review (or auto-merge). |
| Changes requested | Agent submits a Request Changes review to block merge. Integrator creates a single batch fix issue for all HIGH findings. |

### Fix Cycle Flow

```mermaid
graph TD
    A["Agent review finds HIGH severity issues"] --> B["Integrator creates one batch fix issue<br>(all HIGH findings in a single issue)"]
    B --> C["Worker executes fixes,<br>pushes to worker branch"]
    C --> D["Integrator consolidates fixes<br>into batch branch"]
    D --> E["Agent re-reviews<br>(with prior review comments as context)"]
    E --> F["Clean → approved with batch summary"]
```

Fix issues are labeled `herd/type:fix`, have no dependencies (run in parallel),
and track which review cycle spawned them via a `fix_cycle` field and a
`batch_pr` reference back to the PR. Findings are deduplicated against open fix
issues to avoid creating duplicate work.

When a worker is dispatched for a fix issue, its system prompt includes an
additional instruction prioritizing the reviewer's findings over original
acceptance criteria. This prevents fix workers from checking the original
criteria, finding them satisfied, and no-oping — ignoring the reviewer's
concern entirely. If the fix worker genuinely believes the reviewer is wrong
after careful analysis, it explains its reasoning in detail rather than
silently doing nothing.

When `/herd fix` creates a fix issue, all comments from the batch PR are
included as a `## Conversation History` section in the issue body. Each comment
is formatted as `**@author:**` followed by the comment body, separated by `---`.
This gives the fix worker full context of prior fix requests and review feedback.

On each re-review, the reviewer receives its prior review comments as context to
maintain consistency and avoid contradicting previous decisions. This cycle
repeats until the agent approves or `review_max_fix_cycles` (default 3) is
reached, at which point the Integrator comments on the PR with the remaining
issues and waits for human intervention.

### Safety Valve

If a single review cycle finds more than **10 issues**, the Integrator does not
create fix workers. Instead, it comments on the PR with all issues found and
escalates to the user. This prevents a confused or overzealous agent from
generating dozens of fix workers in one pass.

### Interaction with Auto-Merge

| review | auto_merge | Behavior |
|--------|------------|----------|
| true | false | Agent reviews first, then human. Human gets a pre-screened PR. (Default) |
| true | true | Agent is gatekeeper. Approves + CI pass = auto-merge. Issues block merge and trigger fix workers. |
| false | true | No agent review. PR auto-merges as soon as CI passes. |
| false | false | No agent review. Human reviews the batch PR directly. |

---

## 6. Conflict Resolution

Conflicts can occur in two places.

### Between Parallel Workers (Same Tier)

When workers in the same tier modify overlapping files, the Integrator
reconciles during consolidation:

1. **Auto-rebase.** If changes don't textually conflict, rebase succeeds.
2. **Dispatch a conflict-resolution worker.** The Integrator creates a fix issue
   describing the conflict, the conflicting files, and the intent of each
   worker. The resolver checks out the batch branch, reads both worker branches,
   and produces a merged result. It pushes to its own worker branch and the
   normal consolidate/advance flow handles it.
3. **Notify the user.** Comment on the relevant issues with conflict details.
   The user resolves manually.

### Between Batch Branch and Main

Main may advance while the batch executes. Before opening the PR, the Integrator
rebases the batch branch onto latest main. If this conflicts, the same
resolution strategies apply. The resolver force-pushes to the batch branch (the
one acceptable force-push -- HerdOS owns the branch).

The configured strategy (`on_conflict: notify | dispatch-resolver`) controls
which path is taken. The resolver is capped at `max_conflict_resolution_attempts`
(default 2); after that, it falls back to notify regardless of config.

---

## 7. Monitor

The Monitor is a scheduled GitHub Action that audits system state and takes
corrective action. It is completely stateless -- each patrol cycle recomputes
everything from the GitHub API.

### Patrol Responsibilities

Each cycle:

```mermaid
graph TD
    A["Monitor Action starts"] --> B["Query: all issues with herd/* labels"]
    B --> C{"No active issues?"}
    C -->|Yes| EXIT["Exit early"]
    C -->|No| D["For each in-progress issue"]

    D --> D1{"Active Action run?"}
    D1 -->|No| D1a["Mark stale, re-dispatch or escalate"]
    D1 -->|Completed| D2{"Run conclusion?"}
    D2 -->|Success, issue open| D2a["Check for PR"]
    D2 -->|Failure| D2b["Re-dispatch or escalate"]
    D1 -->|Running too long| D3["Cancel and re-dispatch"]

    D --> E["For each open batch PR"]
    E --> E1{"Open > max_pr_age?"}
    E1 -->|Yes| E1a["Comment asking for review/merge"]
    E --> E2{"CI failing?"}
    E2 -->|fix-ci comment present| E2a["Skip (dedup)"]
    E2 -->|fix-ci comment absent| E2b["Post /herd fix-ci comment"]

    E --> F["Done — patrol complete"]
```

### Exponential Backoff

For repeatedly failing issues, the Monitor spaces out re-dispatch attempts:

- 1st failure: re-dispatch immediately
- 2nd failure: wait 15 minutes
- 3rd failure: wait 1 hour
- After `max_redispatch_attempts` (default 3): label `herd/status:failed`, stop

Backoff is enforced statelessly by comparing the most recent failed run's
timestamp against the required delay. The natural patrol interval (default
15 minutes) handles spacing; for the 1-hour wait, the Monitor skips ~3 cycles.

### Stateless Enforcement

The Monitor stores no state. Failure counts come from querying all completed
worker workflow runs filtered by issue number and counting those with
`conclusion: "failure"`. Backoff delays are enforced by timestamp comparison.
This means the Monitor can be restarted, re-deployed, or run on different
runners without losing track of anything.

### Escalation

When auto-resolution fails, the Monitor comments on the issue with diagnostics
(Action run URL, error logs, time elapsed), @mentions the configured
`notify_users`, and labels the issue `herd/status:failed`. It uses GitHub's
native notification system exclusively -- no Slack, email, or external
integrations.

---

## 8. Batches

A batch is a group of related issues forming a delivery unit, mapped to a GitHub
Milestone.

### Why Milestones Over Projects

| Feature | Milestones | Projects |
|---------|-----------|----------|
| Setup complexity | Zero (built-in) | Requires project board creation |
| API simplicity | Simple REST endpoints | GraphQL-heavy |
| Progress tracking | Built-in percentage | Requires custom views |
| Issue association | Direct field on issue | Requires adding to project |
| Suitable for | Task batches with clear completion | Ongoing work streams |

Milestones fit because batches have a clear end state: all issues closed.

### Lifecycle

```mermaid
graph LR
    Created --> InProgress["In Progress"]
    InProgress --> Landed
    InProgress --> Cancelled["Cancelled<br>(PR closed without merge)"]
    InProgress --> Stalled["Stalled<br>(Monitor detects)"]
    Stalled --> InProgress
```

- **Created**: Milestone exists, issues created, nothing dispatched
- **In Progress**: At least one worker active or one issue done
- **Stalled**: Issues stuck (failed workers, unresolved conflicts); Monitor
  escalates
- **Landed**: All issues done, batch PR merged, milestone closed
- **Cancelled**: Batch PR closed without merging. Non-done issues are labelled
  `herd/status:cancelled` and closed. Done issues are closed without relabelling.
  Milestone is closed, branch is deleted.

### Cancellation

There are two ways a batch can be cancelled:

**CLI cancellation** (`herd batch cancel <number>`):

1. Cancels any active workflow runs for the batch's issues
2. Labels non-done open issues as `herd/status:cancelled` and closes all
   milestone issues. Issues already `herd/status:done` are closed without
   relabelling.
3. Closes the batch PR if one exists
4. Closes the milestone
5. Deletes the batch branch

Active workers may take a moment to stop -- Actions cancellation is asynchronous.

**Closing the batch PR without merging:**

1. Non-done issues are labelled `herd/status:cancelled` and closed. Issues
   already `herd/status:done` are closed without relabelling.
2. Milestone is closed
3. Branch is deleted

Both paths now use `herd/status:cancelled` for non-done issues, ensuring the
monitor does not redispatch them. The cancelled status is terminal.

---

## 9. Manual Tasks

Some tasks require human action (infrastructure setup, external service config,
approval gates). These are labeled `herd/type:manual` by the Planner and marked
with 👤 in status output.

Manual tasks participate fully in the DAG:

- **Not dispatched** -- `herd dispatch` skips them, and the internal `dispatchIssue` helper (used by `herd plan` and the Integrator) also skips them
- **Unblocked on tier advancement** -- when the previous tier completes, manual tasks transition from `blocked` to `ready` like any other task, but are not dispatched to workers
- **Completed by closing** -- a human closes the issue (or labels it
  `herd/status:done`); the Integrator's `advance-on-close` job detects the
  close event, advances the tier, and runs agent review if all tiers are done
- **Tier-aware** -- if a manual task is in Tier 0, Tier 1 won't dispatch until
  it's closed. Manual tasks that grant permissions or set up external services should always be in Tier 0 so they unblock automated tasks that depend on them
- **Notifications** -- when `notify_users` is configured, the Planner @mentions
  those users on manual task issues for visibility

---

## 10. Agent Error Resilience

The claude agent package validates output from every agent invocation. When Claude Code exits with code 0 but returns suspicious output (empty, "Execution error", or very short single-line output under 20 characters), the system:

1. **Retries once** after a 5-second delay
2. If the retry also returns suspicious output, **returns an error** instead of treating it as a successful result
3. The worker posts a **"Worker failed"** comment on the issue explaining what happened
4. The deferred error handler labels the issue as `failed` and triggers the Monitor

This prevents the system from marking issues as done when the agent didn't actually do any work (e.g., during API instability). The integrator also handles review agent failures gracefully -- if the review agent fails, the review cycle is skipped entirely (no approval, no fix issues) and will retry on the next trigger.

---

## 11. Failure Modes

### Worker Fails to Complete

```mermaid
graph TD
    A["Worker crashes or times out"] --> B["Action run shows as failed"]
    B --> C["Worker triggers Monitor via<br>workflow_dispatch (immediate response)"]
    C --> D["Monitor re-dispatches<br>(if auto_redispatch enabled,<br>up to max_redispatch_attempts)"]
    C --> E["Monitor labels issue failed<br>and @mentions notify_users"]
```

The batch branch is unaffected -- the failed worker's branch is never merged.

### Worker Produces Code That Doesn't Build

```mermaid
graph TD
    A["Worker pushes to worker branch"] --> B["Integrator consolidates into batch branch"]
    B --> C["CI runs on updated batch branch"]
    C --> D["check_suite.completed triggers Integrator"]
    D -->|CI passed| DONE["Done, continue normally"]
    D -->|CI failed| G["Agent analyzes failure logs,<br>creates fix issues"]
    G --> H["Fix workers execute →<br>re-consolidate → CI runs again"]
    H -->|Passes| DONE2["Done"]
    H -->|Fails| I{"ci_max_fix_cycles<br>reached? (default: 2)"}
    I -->|No| G
    I -->|Yes| J["Integrator reverts consolidation<br>Issue labeled failed,<br>comment with CI details"]
```

### Merge Conflict Between Parallel Workers

```mermaid
graph TD
    A["Worker A and Worker B<br>complete in the same tier"] --> B["Worker A merged into<br>batch branch successfully"]
    B --> C["Worker B conflicts with<br>Worker A's changes"]
    C --> D["Option A: Dispatch conflict-resolution worker<br>(on_conflict: dispatch-resolver,<br>capped at max_conflict_resolution_attempts)"]
    C --> E["Option B: Notify user<br>(on_conflict: notify,<br>or after resolver cap reached)"]
```

### Recovering from a Stuck Tier

When a tier is stuck (worker failed and auto-redispatch exhausted):

1. **Fix and re-dispatch.** Edit the issue to clarify/reduce scope, then
   `herd dispatch <issue-number>`.
2. **Cancel the batch.** `herd batch cancel <number>` stops everything.

In v1.0, you cannot skip a single failed issue or remove it from the batch. If
it blocks everything and can't be fixed, cancel and re-plan.

---

## 11. Dispatch Model

Three actors dispatch work:

1. **`herd plan` dispatches Tier 0** automatically after the user approves the
   plan. The batch branch is created and Tier 0 workers are triggered. No
   separate command needed.

2. **`herd integrator advance` dispatches subsequent tiers** automatically when
   a tier completes (triggered by `workflow_run` events after worker completion).
   It rebuilds the DAG, identifies the current tier, labels next-tier issues as
   `herd/status:ready`, and dispatches workers (respecting `max_concurrent`
   globally across all batches).

3. **The Monitor re-dispatches failed work** if `auto_redispatch` is enabled,
   with exponential backoff.

For manual control, `herd plan --no-dispatch` creates issues without dispatching.
The user can then dispatch with `herd dispatch --batch <N>`.

---

## 12. Comment Commands

HerdOS supports `/herd` commands posted as comments on issues and PRs. This provides a unified entry point for both human and automated interactions.

### Architecture

The comment command system is in `internal/commands/`. It is designed as a set of composable functions called through a registry, with two entry points:

1. **Phase 1 (current):** The `issue_comment` webhook triggers the `handle-comment` job in the integrator workflow, which calls `herd integrator handle-comment`. This parses the command and dispatches to the registered handler.
2. **Phase 2 (future GitHub App):** An agent interprets natural language and calls the same handler functions as tool calls.

### Permission Model

Commands are accepted from users with `OWNER`, `MEMBER`, or `COLLABORATOR` association on the repository, plus bot users (login ending in `[bot]`). Other commenters are silently ignored.

### Acknowledgment Flow

1. User posts `/herd <command>` as a comment
2. Workflow reacts with 👀 on the comment
3. Handler executes the command
4. Result posted as a reply comment (success message or error)

### Monitor Integration

The Monitor posts `/herd retry <N>` and `/herd fix-ci` comments instead of dispatching workflows directly. This ensures all command execution flows through the same handler, maintaining single responsibility.

### Failure Recovery

When integrator steps fail, the CLI posts a comment on the relevant issue or batch PR:

```
⚠️ **Integrator failed** during <step>: <error>

You can retry with `/herd integrate` on this issue or the batch PR.
```

The `/herd integrate` command manually triggers the full integrator cycle for a batch. It can be posted on:
- **Any issue belonging to a batch** — extracts the batch number from the issue's YAML frontmatter
- **A batch PR** — extracts the batch number from the `herd/batch/<N>-<slug>` branch name

The cycle runs: consolidate any remaining worker branches → check CI → advance tiers → review. This replaces the previous workaround of relabeling a done issue as failed to re-trigger the integrator.

Comments are posted to the issue being processed (for run-based triggers) or the batch PR (for batch-based triggers).

---

## Runaway Loop Protection

Every automated feedback loop has a hard cap:

| Loop | Config Key | Default | At Limit |
|------|-----------|---------|----------|
| Agent review / fix / re-review | review_max_fix_cycles | 3 | Comments on PR, waits for human |
| Monitor re-dispatch | max_redispatch_attempts | 3 | Labels issue failed, stops |
| Conflict resolution | max_conflict_resolution_attempts | 2 | Falls back to notify |
| CI failure fix cycles | ci_max_fix_cycles | 2 | Reverts consolidation, notifies user (0 = notify-only) |

### Merge Strategy

How the final batch PR lands on main:

| Strategy | Method | Result |
|----------|--------|--------|
| squash | Squash merge | Single commit on main, clean history (default) |
| rebase | Rebase merge | Individual worker commits preserved on main |
| merge | Merge commit | Merge commit, worker commits in branch |
