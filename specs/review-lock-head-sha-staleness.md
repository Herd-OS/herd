# Review Lock Head-SHA Staleness and Release Robustness

## Problem

HerdOS review locks serialize automated agent reviews per batch PR, but the lock currently only prevents concurrent review agents. It does not make the review result explicitly tied to the PR head SHA that was reviewed, and a stale locked state can make later review attempts skip without enough operator-facing explanation.

This showed up on ServiceKit PR `#891`:

- PR: `https://github.com/service-kit/service-kit/pull/891`
- Batch branch: `herd/batch/65-replication-backfill-tooling`
- Current PR head SHA: `3715a2c4776e62d69aa5742adfb060dccf5461f9`
- Review lock branch: `herd/review-lock/pr-891`
- Latest lock commit status: `locked`
- Lock recorded `batch_branch_sha`: `83ef887a027b197929a99fa40b95da011f450ce3`
- The PR branch later advanced through review fix `#895` to `3715a2c4776e62d69aa5742adfb060dccf5461f9`

The review lock branch is metadata only and must not be merged into the PR. It should only serialize review agents and record lock state.

## Correct Diagnosis

Do not make the fix assume that merge logic directly merges or consults the review-lock branch. Current `MergeApproved` fetches the PR by number and merges the PR, then cleans up `pr.Head`. That path should remain independent from review locks.

The issue to fix is the review lifecycle:

1. A review lock is acquired for a specific batch PR head SHA.
2. The review agent may take time to run.
3. A fix worker, manual push, or integrator action can advance the PR head while the review is in progress.
4. The review result may then be stale: it describes a diff that is no longer the PR head.
5. If Herd acts on that stale result, it can approve, request changes, create fix issues, or auto-merge based on the wrong commit.
6. If the process is cancelled or the review path returns early under a cancelled context, lock release can fail or never happen, leaving an active lock that causes later review attempts to skip.

## Required Invariants

1. **Review output is valid only for the exact PR head SHA it reviewed.**
   - A review that started at head SHA `A` must not approve, request changes, create fix issues, or auto-merge if the PR head is now SHA `B`.

2. **Review locks serialize review agents only.**
   - Merge approval should not depend on review-lock state.
   - Merge code must target the PR returned by GitHub metadata, not `herd/review-lock/pr-<n>`.

3. **Lock release must be best-effort even on early return and cancellation.**
   - Normal review returns should release the lock.
   - Error returns should release the lock.
   - Context cancellation should not make the release attempt immediately fail if a short fresh context can still unlock.

4. **Skipped review attempts must explain why.**
   - If Herd skips because another lock is active, log enough lock metadata to diagnose it.
   - If a manual `/herd review` skips because another review is active, post a PR comment with the owner/acquired/expires/head information when available.
   - If Herd discards a stale review result because the PR head changed, post/log an explicit message with old and new SHAs.

## Implementation Plan

### 1. Preserve the PR head SHA reviewed

In `internal/integrator/review.go`, capture the current PR head SHA immediately before acquiring the review lock and running the agent.

Current code gets the branch SHA through:

```go
lockFromSHA, err := p.Repository().GetBranchSHA(ctx, batchBranch)
```

Keep this as the reviewed head SHA and pass it into `acquireReviewLock` as today.

Rename locally if helpful:

```go
reviewedHeadSHA := lockFromSHA
```

Do not rely on `batch_branch_sha` only as metadata. The review function itself should hold the expected SHA and validate it before acting.

### 2. Re-check PR head before acting on review output

After `runReviewWithRetry` returns a parseable review result, but before any side effect based on that result, re-read the batch branch SHA:

```go
currentHeadSHA, err := p.Repository().GetBranchSHA(ctx, batchBranch)
```

If the SHA differs from `reviewedHeadSHA`:

- Do not partition findings.
- Do not add a summary comment.
- Do not approve.
- Do not request changes.
- Do not create fix issues.
- Do not dispatch workers.
- Do not auto-merge.
- Return a neutral `ReviewResult{BatchPRNumber: pr.Number}`.
- Post a PR comment explaining that the review was discarded because the PR head changed while review was running.

Suggested comment:

```markdown
⚠️ **HerdOS Integrator** — Review result discarded

The batch PR changed while the agent review was running, so the review result was not applied.

- Reviewed head: `<old-sha>`
- Current head: `<new-sha>`

HerdOS will review the updated diff on the next trigger. You can also run `/herd review` manually.
```

Use a helper to avoid scattering string formatting through `Review`.

### 3. Use a fresh bounded context for lock release

Current review code releases with the same `ctx`:

```go
defer func() {
    if err := releaseReviewLock(ctx, p.Issues(), p.Repository(), reviewLock); err != nil {
        fmt.Printf("Warning: failed to release review lock for PR #%d: %s\n", pr.Number, err)
    }
}()
```

Change the defer to release with a short fresh context so a cancelled review context does not automatically prevent unlock:

```go
defer func() {
    releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    if err := releaseReviewLock(releaseCtx, p.Issues(), p.Repository(), reviewLock); err != nil {
        fmt.Printf("Warning: failed to release review lock for PR #%d: %s\n", pr.Number, err)
    }
}()
```

Keep the release best-effort. Do not turn release failure into review failure after the review has already completed.

### 4. Improve active-lock diagnostics

When `acquireReviewLock` returns `acquired=false`, `Review` currently prints:

```go
Review already in progress for PR #N; skipping duplicate review trigger.
```

This is not enough to diagnose stale locks.

Add a helper that can inspect the lock branch state for diagnostics without acquiring it. It should return data such as:

- status
- lock ID
- owner
- acquired_at
- expires_at
- batch_branch_sha
- whether the recorded `batch_branch_sha` differs from the current batch branch SHA

Suggested helper:

```go
func describeReviewLock(ctx context.Context, repoSvc platform.RepositoryService, prNumber int) (reviewLockState, bool, error)
```

This can reuse `readReviewLockHead` when the repository supports `reviewLockRepository`.

When review is skipped because a lock is active:

- Log the lock owner/acquired/expires/recorded SHA/current SHA.
- If `params.Manual` is true, post a PR comment explaining that review was skipped because another review is currently locked.
- If the lock is expired, acquisition should normally reclaim it. If acquisition still fails after retry/conflict, log/comment that lock acquisition conflicted repeatedly and will retry on the next trigger.

Suggested manual skip comment:

```markdown
⚠️ **HerdOS Integrator** — Review already in progress

Skipped this `/herd review` because another review lock is active.

- Owner: `<owner>`
- Acquired: `<acquired_at>`
- Expires: `<expires_at>`
- Lock head SHA: `<batch_branch_sha>`
- Current PR head SHA: `<current_head_sha>`

If this is stale, wait for expiry or rerun `/herd review` after the active review finishes.
```

Keep automatic duplicate triggers low-noise: logs are enough for automatic workflow triggers unless there is already a project convention for comments on skipped automation.

### 5. Treat stale lock metadata as diagnostic, not merge-blocking

If an active lock has `batch_branch_sha` different from the current PR head SHA, that does not by itself prove the lock is dead. It may mean an old review is still running while the PR head advanced.

Behavior should be:

- If lock is unexpired, do not acquire a second review lock. Another review may still be running. Return `acquired=false`.
- The running review should discard its output when it sees the head SHA changed.
- After the running review releases, the next trigger can review the new head.
- If the process died and the lock remains active, expiry recovery should reclaim it after `reviewLockExpiry`.

Do not blindly ignore an unexpired lock just because `batch_branch_sha` is stale. That would reintroduce duplicate review agents.

### 6. Keep merge code independent and add regression coverage

`internal/integrator/merge.go` should continue using:

- `p.PullRequests().Get(ctx, params.PRNumber)`
- `p.PullRequests().Merge(ctx, pr.Number, ...)`
- `postMergeCleanup(..., pr.Head)`

Add a regression test proving that a `herd/review-lock/pr-N` branch existing in the repository mock does not affect `MergeApproved`.

The test should make the mock repo expose a review-lock branch and a batch branch, call `MergeApproved`, and assert:

- the PR service merged PR number `N`
- cleanup used the PR head branch
- no merge path tried to parse or merge `herd/review-lock/pr-N`

## Tests Required

Add focused tests in `internal/integrator/review_test.go` and `internal/integrator/merge_test.go`.

### Review head-staleness tests

1. `TestReview_DiscardsReviewResultWhenHeadAdvances`
   - Initial batch branch SHA is `sha-old`.
   - Review lock is acquired for `sha-old`.
   - Mock review agent returns approved or findings.
   - Before Herd acts on the result, mock repository reports `sha-new`.
   - Assert no approve review is created.
   - Assert no request-changes review is created.
   - Assert no fix issue is created.
   - Assert no worker dispatch happens.
   - Assert a PR comment explains old/new SHA.
   - Assert lock is released.

2. `TestReview_DiscardsFindingsWhenHeadAdvances`
   - Same as above, but review result contains actionable HIGH/MEDIUM findings.
   - Assert no fix issue and no dispatch.

3. `TestReview_AutoMergeDoesNotRunWhenHeadAdvances`
   - `pull_requests.auto_merge=true`.
   - Review result is approved.
   - Head changes before action.
   - Assert merge is not called.

### Release robustness tests

4. `TestReview_ReleasesLockWithFreshContextAfterReviewContextCancelled`
   - Use a context that is cancelled before `Review` returns, or simulate cancellation during/after agent review.
   - Mock repository should fail lock-release operations if they receive the cancelled review context, but succeed with a fresh context.
   - Assert release succeeds and lock head becomes `unlocked`.

5. `TestReview_ReleasesLockOnEarlyReturn`
   - Cover an early return after lock acquisition, such as stable-disagreement skip or active fix worker skip.
   - Assert the lock is released.

Existing tests may already cover some early returns. Keep them and add the missing cancellation/fresh-context behavior.

### Active-lock diagnostics tests

6. `TestReview_ManualReviewActiveLockPostsDiagnosticComment`
   - Existing active lock is unexpired.
   - Run `Review` with `Manual: true`.
   - Assert review agent is not called.
   - Assert PR comment includes owner/acquired/expires and recorded/current head SHAs.

7. `TestReview_AutomaticActiveLockLogsWithoutComment`
   - Existing active lock is unexpired.
   - Run automatic review.
   - Assert no noisy PR comment is posted.
   - Assert review agent is not called.

### Merge regression tests

8. `TestMergeApproved_IgnoresReviewLockBranch`
   - Mock repository has both:
     - `herd/batch/65-replication-backfill-tooling`
     - `herd/review-lock/pr-891`
   - Mock PR `#891` head is the batch branch.
   - Call `MergeApproved`.
   - Assert merge called with PR number `891`.
   - Assert cleanup/delete branch target is the PR head branch, not the review-lock branch.

9. `TestMergeApproved_UsesPRHeadForCleanup`
   - Explicitly assert post-merge cleanup receives `pr.Head`.
   - This can be combined with test 8 if the existing mocks expose deleted branch names.

## Acceptance Criteria

- A review result is discarded if the PR head SHA changed after the review lock was acquired and before review side effects are applied.
- Discarded stale review results produce an explicit PR comment or log with old and new SHAs.
- Review locks are released with a fresh bounded context on normal returns, error returns, and early returns after acquisition.
- Manual `/herd review` attempts that skip because of an active lock produce a diagnostic PR comment.
- Automatic duplicate review triggers do not spam PR comments.
- Merge code remains independent from review-lock state.
- Tests cover stale head, auto-merge suppression, lock release with cancelled review context, active-lock diagnostics, and merge ignoring review-lock branches.
- `go test ./internal/integrator ./internal/platform/github ./internal/cli ./internal/commands ./internal` passes.
- `go test ./...` passes.
- After implementing the code and documentation changes, delete this spec file. Keep `specs/.keep` so the `specs/` directory remains tracked.

## Non-Goals

- Do not merge `herd/review-lock/pr-<n>` into a batch PR.
- Do not make merge approval require reading or releasing review locks.
- Do not ignore unexpired active locks just because their recorded `batch_branch_sha` differs from the current PR head.
- Do not delete/recreate review lock branches as a normal recovery path.
- Do not reintroduce conditional Git ref deletion or `If-Match`-based lock correctness.
