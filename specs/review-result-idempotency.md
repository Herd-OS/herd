# Review Result Idempotency

## Problem

Herd can run the agent reviewer multiple times for the same PR head SHA after the PR has already been approved by an earlier Herd review.

This happened on service-kit PR #891:

- PR: https://github.com/service-kit/service-kit/pull/891
- Batch branch: `herd/batch/65-replication-backfill-tooling`
- Final PR head SHA before merge: `c3e3a6ebf5d51547dc53c14b96d2c7caba796ea3`
- Herd posted three final clean review comments against that same head:
  - `2026-07-02T02:27:51Z`
  - `2026-07-02T02:28:51Z`
  - `2026-07-02T02:35:21Z`

The three reviews were not concurrent. They came from separate serialized Integrator runs:

1. A normal `integrate` job reviewed after consolidating the final review-fix worker.
2. A `check-ci-workflow-completion` job ran after one configured CI workflow completed and reviewed again.
3. Another `check-ci-workflow-completion` job ran after another configured CI workflow completed and reviewed again.

The relevant workflow behavior is that the generated `check-ci-workflow-completion` job runs:

```bash
herd integrator check-ci --ci-run-id "$RUN_ID"
BATCH=$(echo "$HEAD_BRANCH" | grep -oP 'herd/batch/\K[0-9]+')
if [ -n "$BATCH" ]; then
  herd integrator review --batch "$BATCH"
fi
```

So every configured CI workflow completion for the batch branch can invoke `herd integrator review --batch`, even if the current PR head SHA was already cleanly approved by a prior Herd review.

This wastes reviewer tokens, delays merge, clutters PR comments, and makes users think multiple reviewers are racing or disagreeing when the system is actually repeating the same approval.

## Important Distinction From Review Locks

Review locks and review result idempotency solve different problems.

Review locks prevent concurrent review work and protect against races while a review is running.

Review result idempotency prevents duplicate sequential review work after Herd has already reached a stable review result for the current PR head.

The stale review-lock/head-SHA work in `specs/review-lock-head-sha-staleness.md` is still valuable, but it does not fully solve this issue. A lock can serialize the three service-kit #891 review invocations and still allow all three to run one after another.

This spec should be implemented as a separate change.

## Goal

Make automatic Herd review idempotent per PR head SHA.

Once Herd has posted an approved review result for a PR head SHA, later automatic review triggers for that same PR head SHA should skip the agent review and log a clear reason.

Manual user intent should remain respected. A human should still be able to force a fresh review with `/herd review` unless the project maintainers intentionally choose stricter behavior and document it.

## Current Trigger Paths To Consider

At minimum, audit and cover these paths:

- `integrate` workflow path:
  - `herd integrator consolidate --run-id "$RUN_ID"`
  - `herd integrator advance --run-id "$RUN_ID"`
  - `herd integrator review --run-id "$RUN_ID"`
  - `herd integrator check-ci --run-id "$RUN_ID"`
- `check-ci-workflow-completion` workflow path:
  - `herd integrator check-ci --ci-run-id "$RUN_ID"`
  - `herd integrator review --batch "$BATCH"`
- `check-ci-on-completion` workflow path, if it can lead to review through batch state or future changes.
- `advance-on-close` workflow path:
  - `herd integrator advance --batch "$BATCH"`
  - `herd integrator review --batch "$BATCH"`
  - `herd integrator check-ci --batch "$BATCH"`
- manual comment path:
  - `/herd review`
  - `/herd integrate`
  - `/herd fix` followed by worker completion and automatic review

The exact workflow names may differ between the checked-in generated workflow and the template. Update both the template and tests when changing generated workflow behavior.

## Desired Behavior

### Automatic Review

Before spending tokens on the agent reviewer, Herd should determine the current batch PR head SHA from GitHub PR metadata.

If the latest relevant Herd review result for that PR head SHA is already approved, Herd should skip the agent review.

The skip should be explicit in logs. For example:

```text
Skipping agent review for PR #891: head c3e3a6ebf5d51547dc53c14b96d2c7caba796ea3 already has an approved Herd review result.
```

The skip should not post another PR comment by default. Repeated "skipped duplicate review" comments would create a different kind of PR noise. Logging is sufficient unless the command was manually invoked by a user and a visible response is expected.

### Manual `/herd review`

Manual `/herd review` should be treated as explicit user intent to re-run review, even if the current head SHA already has an approved Herd review result.

This gives users a straightforward escape hatch when they distrust an earlier review, change review instructions, or want a second pass after external context changes without pushing a new commit.

If the implementation instead chooses to skip manual duplicate reviews, the command must post a visible PR comment explaining that the current head SHA is already approved and explaining how to force a re-review. Prefer allowing manual `/herd review` to force review because it is simpler and matches user intent.

### PR Head Changes

If the PR head SHA changes after an approval, the previous approval must not suppress review of the new head.

Any new commit on the batch branch invalidates previous approved-review idempotency for automatic review purposes.

### Findings And Fix Cycles

If the latest Herd review result for the current head SHA has actionable findings, automatic review should not be suppressed merely because a review result marker exists.

Expected behavior:

- If a review result for the current head SHA is approved, skip duplicate automatic reviews for that head.
- If a review result for the current head SHA has findings and fix workers were dispatched, existing max-cycle and fix-worker logic should continue to govern the loop.
- If the PR head advances because a fix worker lands, review must run again for the new head SHA.
- If max review cycles are hit, preserve existing behavior. Do not hide max-cycle state behind idempotency.

### CI Completion

CI completion handling should remain correct.

`herd integrator check-ci --ci-run-id` still needs to run for each configured CI workflow completion so Herd can observe failures and dispatch CI-fix workers when appropriate.

The fix should prevent the follow-up agent review from repeating for the same approved head SHA. It should not suppress CI failure detection.

In other words, this is acceptable:

```text
CI workflow completed -> check-ci runs -> current head already approved -> review skips cheaply
```

This is not acceptable:

```text
CI workflow completed -> whole job exits before check-ci can notice a failure
```

### Merge Interaction

Do not require an additional review after all CI workflows finish if the current PR head SHA already has an approved Herd review result.

Merge eligibility should be based on:

- current PR head SHA
- current CI/check state
- current Herd review result for that same head SHA
- existing max-cycle/manual intervention rules

If a PR has an approved Herd review result for its current head and CI is green, duplicate CI-completion review triggers should not delay merge by running another agent review.

## Recommended Implementation Shape

Use a machine-readable review result marker in Herd review comments.

For example, append a hidden HTML comment to every Herd review result comment:

```html
<!-- herd:review-result {"version":1,"pr_number":891,"batch_number":65,"head_sha":"c3e3a6ebf5d51547dc53c14b96d2c7caba796ea3","status":"approved","cycle":8,"findings_count":0,"created_at":"2026-07-02T02:27:51Z"} -->
```

For a review with findings:

```html
<!-- herd:review-result {"version":1,"pr_number":891,"batch_number":65,"head_sha":"abc123...","status":"changes_requested","cycle":7,"findings_count":1,"created_at":"2026-07-02T02:17:25Z"} -->
```

Keep the marker small, stable, and explicitly versioned.

Suggested fields:

- `version`
- `pr_number`
- `batch_number`
- `head_sha`
- `status`
  - `approved`
  - `changes_requested`
  - `max_cycles_hit`
  - any other existing review terminal state that needs to be represented
- `cycle`
- `findings_count`
- `created_at`

The marker should be added by the code that posts Herd review result comments, not by the agent prompt. The agent should not be responsible for metadata correctness.

Add parsing helpers similar in spirit to existing marker parsing code:

- Parse only Herd-authored comments.
- Ignore malformed markers without failing the review command.
- If multiple markers exist for the same PR/head, use the newest relevant one by comment creation time or marker timestamp.
- Treat markers for a different PR number, batch number, or head SHA as irrelevant.

The duplicate-review skip should happen before acquiring an expensive agent reviewer session when possible. It is acceptable to acquire the review lock first if that makes concurrency semantics simpler, but the agent process must not start for a duplicate approved head.

## Concurrency Expectations

The idempotency check should be safe with concurrent or nearly concurrent triggers.

Review lock remains the primary guard against two reviews running at the same time.

A reasonable sequence is:

1. Resolve PR and current head SHA.
2. Acquire review lock, unless manual mode intentionally bypasses duplicate suppression but still uses the lock.
3. Re-check current head SHA and existing review result markers after acquiring the lock.
4. If current head already has an approved result and this is an automatic review, release the lock and skip.
5. Otherwise run the agent review.
6. Before posting side effects, preserve existing stale-head protections from the review-lock/head-SHA work.
7. Post the review result comment with a machine-readable marker.
8. Release the lock in an ensure/finally path.

The second check after acquiring the lock matters: another serialized Integrator run may have posted approval while this run was waiting for the lock.

## Manual Versus Automatic Source

The implementation needs to know whether a review invocation is automatic or user-requested.

Do not infer manual intent only from `--pr` or `--batch`; those can be used by workflows too.

Possible approaches:

- Add an explicit flag to `herd integrator review`, for example `--force` or `--manual`, and have the `/herd review` comment path pass it.
- Add an internal `ReviewParams.Force` or `ReviewParams.Manual` field.
- Keep automatic workflow calls using the default non-forcing behavior.

Prefer explicit source/force metadata over brittle event-name inference.

If adding a CLI flag, update CLI tests and generated workflow tests.

## Tests

Add focused unit tests around the Integrator review logic and workflow generation.

Required tests:

- Review result comments include a hidden `herd:review-result` marker for approved reviews.
- Review result comments include a hidden `herd:review-result` marker for reviews with findings.
- Marker parsing ignores malformed, unrelated, wrong-PR, wrong-batch, and wrong-head markers.
- Automatic `Review` skips agent execution when the current PR head SHA already has an approved marker.
- The skip happens without posting another approval comment.
- The skip logs or returns an explicit skipped reason that the CLI can print.
- Manual or forced review still runs even when the current head SHA already has an approved marker.
- If the PR head SHA differs from the marker head SHA, review runs normally.
- If the marker for the current head is `changes_requested`, review is not suppressed by idempotency.
- If two serialized review invocations happen for the same head, the first can post approval and the second skips after re-checking markers.
- `check-ci-workflow-completion` still runs `check-ci` for each CI completion, but duplicate automatic review for an already-approved head does not start the agent.
- Generated workflow tests still cover the intended shell commands, or are updated if the review command gains an automatic/force flag.

Also add or update tests around CLI output:

- Approved review still prints the existing success message.
- Duplicate automatic approved-head review prints an explicit skip message.
- Max-cycle and fix-worker messages remain unchanged.

## Documentation

Update design docs to explain that Herd has two layers of review protection:

- review locks serialize active review attempts
- review result markers make automatic review idempotent per PR head SHA

Suggested docs:

- `docs/design/github-integration.md`
- `docs/design/execution.md`, if it already describes review/fix/re-review cycles
- any generated workflow/concurrency discussion that currently implies locking alone prevents duplicate review attempts

Document that manual `/herd review` intentionally forces a fresh review unless the implementation chooses a different explicit user-facing behavior.

## Out Of Scope

- Do not remove review locks.
- Do not make CI workflow completion handling skip `check-ci`.
- Do not change the reviewer prompt for this task.
- Do not change review strictness, severity rules, or fix issue generation.
- Do not merge or otherwise use `herd/review-lock/pr-N` branches as approval state.
- Do not depend on GitHub's formal PR review API for this feature. Herd review summaries are issue comments in current behavior, and this feature should work with that model.

## Acceptance Criteria

- Automatic review is idempotent for an already-approved current PR head SHA.
- Repeated CI workflow completions for the same approved head do not start additional agent reviewer sessions.
- Repeated CI workflow completions still run CI state handling and can dispatch CI-fix workers for failures.
- Manual `/herd review` can force a fresh review, or the alternative behavior is explicitly documented and visible to the user.
- A new commit on the PR branch invalidates the prior approved review result and allows review to run again.
- Review comments include stable machine-readable review-result markers.
- Duplicate-review skips are visible in logs or CLI output and are not silent no-ops.
- Tests cover approved markers, finding markers, malformed markers, wrong-head markers, automatic skip, manual force, and CI completion behavior.
- Documentation explains the difference between review locks and review-result idempotency.
- After implementing the code and documentation changes, delete this spec file. Keep `specs/.keep` so the `specs/` directory remains tracked.
