# Reliable CI self-healing for GitHub Actions workflows

## Goal

Make Herd reliably self-heal failed GitHub Actions CI on batch PRs.
Today Herd can create a CI fix issue through `/herd fix-ci` or the
scheduled monitor, but the event-driven path is unreliable for normal
GitHub Actions CI because it depends on `check_run`. GitHub does not
fire `check_run` workflows for some GitHub Actions-created check suites,
so a failed batch CI run can sit until a monitor run happens or a user
posts a manual fix command.

The new behavior should:

- Trigger CI self-heal from completed configured CI workflows.
- Avoid treating a known failed CI workflow as "pending" just because
  another workflow on the same branch is still running.
- Create CI fix issues with actionable failure context.
- Classify infrastructure/no-log failures separately from code failures.
- Keep the scheduled monitor as a fallback, not the primary CI failure
  detection path.

## Non-goals

- Do not change worker validation requirements or agent prompts except
  where needed to pass richer CI context to fix workers.
- Do not attempt to infer every CI workflow in a repository
  automatically. Provide explicit config with conservative defaults.
- Do not implement a full CI log storage system. Short excerpts and
  URLs are enough.
- Do not auto-merge PRs after CI passes.
- Do not remove `/herd fix-ci`; keep it as a manual override.

## Problem Details

In service-kit PR #844, the automatic CI fix issue #845 was created from
an issue-comment flow handling `/herd fix-ci`, not from the
`check_run`-based self-heal path. After Herd fixed and consolidated that
issue, CI ran again. `CI - ServiceKit Ruby` later failed, but no
event-driven Herd CI fix was created; a user had to post a targeted
`/herd fix` comment, creating #846.

Two issues contributed:

1. The installed `herd-integrator.yml` uses `check_run` for completed CI
   checks on `herd/batch/*` branches. This is not reliable for GitHub
   Actions-created checks.
2. `integrator.CheckCI` uses combined branch status. If one workflow has
   failed while another workflow is still running, the combined state can
   be `pending`, delaying or suppressing a fix even though a concrete
   failure is already known.

The CI fix issue body is also too generic. For #845 it only said CI was
failing on the batch branch. It did not include workflow names, job URLs,
annotations, or log excerpts, so workers had to rediscover the failure
from scratch.

## Config Shape

Add explicit CI workflow configuration under `integrator`:

```yaml
integrator:
  ci_workflows:
    - "CI - ServiceKit Ruby"
    - "CI - Accounts"
```

The examples here use ASCII hyphens. Existing repositories may use
Unicode dashes in workflow names, for example `CI — ServiceKit Ruby`;
the implementation must preserve the exact configured string.

### Defaults

Default `ci_workflows` to an empty list.

When empty:

- Keep existing behavior: `workflow_run` self-heal for CI workflows is
  not installed/rendered, and `/herd fix-ci` plus monitor behavior still
  work.
- `herd init --check` should not introduce workflow drift for existing
  repositories that do not opt in.

This avoids guessing which repository workflows are CI and avoids
accidentally triggering on Herd-managed workflows.

## Implementation Outline

### 1. Config Types

Update `internal/config/config.go`:

```go
type Integrator struct {
    Strategy                     string   `yaml:"strategy"`
    OnConflict                   string   `yaml:"on_conflict"`
    MaxConflictResolutionAttempts int     `yaml:"max_conflict_resolution_attempts"`
    RequireCI                    bool     `yaml:"require_ci"`
    Review                       bool     `yaml:"review"`
    ReviewMaxFixCycles           int      `yaml:"review_max_fix_cycles"`
    ReviewStrictness             string   `yaml:"review_strictness"`
    ReviewFixSeverity            string   `yaml:"review_fix_severity"`
    CIMaxFixCycles               int      `yaml:"ci_max_fix_cycles"`
    CIWorkflows                  []string `yaml:"ci_workflows"`
}
```

Use the actual existing field order/names from the current codebase when
editing; the snippet above is illustrative.

### 2. Defaults and Validation

Default:

```go
CIWorkflows: nil,
```

Validation:

- Allow nil/empty.
- Reject entries that are empty or whitespace.
- Preserve order.
- Do not de-duplicate silently. If duplicates are detected, report a
  validation error so the config remains explicit.

Use table-driven tests in `internal/config/config_test.go` covering:

- missing `ci_workflows`
- empty list
- one valid workflow
- multiple valid workflows
- blank entry
- duplicate entry

### 3. Workflow Template

Update `internal/cli/workflows/herd-integrator.yml.tmpl`.

Keep existing triggers, but add a configured `workflow_run` trigger for
CI workflows when `integrator.ci_workflows` is non-empty:

```yaml
on:
  workflow_run:
    workflows:
      - "HerdOS Worker"
{{ range .IntegratorCIWorkflows }}
      - "{{ . }}"
{{ end }}
    types: [completed]
```

The existing `integrate` job must continue to run only for `HerdOS
Worker` completions.

Add a new job, or extend with careful `if:` conditions, for CI workflow
completion:

```yaml
check-ci-workflow-completion:
  if: >
    vars.HERD_ENABLED == 'true'
    && github.event_name == 'workflow_run'
    && github.event.workflow_run.conclusion != 'skipped'
    && startsWith(github.event.workflow_run.head_branch, 'herd/batch/')
    && github.event.workflow_run.name != 'HerdOS Worker'
```

This job should run:

```sh
herd integrator check-ci --ci-run-id "$RUN_ID"
```

where `RUN_ID` is `github.event.workflow_run.id`.

Keep the current `check_run` job only if it still has value for
third-party CI providers. Rename comments to make clear it is not the
primary GitHub Actions CI path.

Template tests:

- Existing default config renders byte-identical output when
  `ci_workflows` is empty.
- Config with two CI workflow names renders them under `workflow_run`.
- The `HerdOS Worker` workflow remains present exactly once.
- The CI workflow completion job is absent when no workflows are
  configured, if that is the chosen no-drift strategy.

### 4. CLI Command Parameters

Extend `herd integrator check-ci` with:

```sh
--ci-run-id <run id>
```

Rules:

- `--run-id`, `--batch`, and `--ci-run-id` are mutually exclusive.
- `--run-id` remains the Herd worker completion path.
- `--batch` remains the manual/monitor path.
- `--ci-run-id` is the completed CI workflow path.

For `--ci-run-id`, the command must:

1. Load the workflow run.
2. Verify the run head branch starts with `herd/batch/`.
3. Verify the run workflow name is configured in
   `integrator.ci_workflows`.
4. Parse the batch number from the branch.
5. Call `integrator.CheckCI` with a new parameter carrying the failed CI
   run details.

If the CI workflow completed successfully, `check-ci` should still be
allowed to inspect overall status and clean up CI-pending state if all
CI is now green, but it should not create a fix issue from a successful
triggering run.

### 5. CheckCI Trigger Context

Extend `internal/integrator/ci.go`.

Add a parameter struct for triggering CI run context. Use existing
platform types where possible.

Example shape:

```go
type CIFailureContext struct {
    RunID      int64
    Workflow   string
    HeadBranch string
    HeadSHA    string
    Conclusion string
    URL        string
}
```

Add to `CheckCIParams`:

```go
CIRun *CIFailureContext
```

Behavior:

- If `params.CIRun` is present and conclusion is failure/cancelled/timed
  out/action_required, treat CI as failed even if combined branch status
  is pending.
- If `params.CIRun` is present and conclusion is success, only create a
  fix issue if combined branch status is failure.
- If no triggering run is present, keep existing behavior.
- Preserve `Force` behavior for `/herd fix-ci`.

Tests in `internal/integrator/ci_test.go`:

- Failed triggering CI run creates a fix issue even when combined status
  is pending.
- Successful triggering CI run plus pending combined status does not
  create a fix issue.
- Successful triggering CI run plus failed combined status creates or
  leaves behavior consistent with current policy.
- Unconfigured workflow name is skipped.
- Non-batch branch is skipped.
- Existing max-cycle behavior still applies.
- Active fix worker still blocks dispatch.

### 6. CI Failure Details

Add a small CI diagnostics collector. Prefer keeping it behind platform
interfaces so tests do not shell out to `gh`.

Suggested platform additions:

```go
type WorkflowRunDiagnostics struct {
    RunID       int64
    Workflow    string
    URL         string
    Conclusion  string
    Jobs        []WorkflowJobDiagnostic
    Annotations []string
    LogExcerpt  string
    LogStatus   string // "available", "unavailable", "not_fetched"
}

type WorkflowJobDiagnostic struct {
    ID         int64
    Name       string
    URL        string
    Conclusion string
    Status     string
}
```

Add a method to the workflow service, or add a narrower method to an
appropriate service:

```go
GetRunDiagnostics(ctx context.Context, runID int64) (*platform.WorkflowRunDiagnostics, error)
```

Diagnostics should be best-effort:

- Return workflow/run/job URLs and conclusions whenever available.
- Include annotations when available.
- Include a short failed-log excerpt when available.
- If logs cannot be fetched, set `LogStatus` and include the API error
  in a short, non-fatal note.

Do not fail CI fix creation merely because logs are unavailable.

### 7. Fix Issue Body

Update CI fix issue rendering in `integrator.CheckCI`.

Include a `## CI Failure` section when diagnostics are available:

```md
## CI Failure

- Workflow: CI - ServiceKit Ruby
- Run: https://github.com/owner/repo/actions/runs/...
- Conclusion: failure
- Head branch: herd/batch/...
- Head SHA: abc123

### Failed Jobs

- Tests: failure - https://github.com/owner/repo/actions/runs/.../job/...

### Log Excerpt

```text
Failures:
...
```
```

If logs are unavailable:

```md
### Log Excerpt

Unavailable: GitHub did not return logs for this job.
```

If annotations indicate runner infrastructure failure, make that
explicit:

```md
## Failure Classification

This appears to be a CI infrastructure failure, not a code failure:
the runner lost communication with GitHub.
```

### 8. Infrastructure Failure Classification

Add a helper that classifies obvious infrastructure failures from
annotations/log text:

```go
func classifyCIFailure(diag *platform.WorkflowRunDiagnostics) string
```

Return values can be simple strings:

- `"code"`
- `"infrastructure"`
- `"unknown"`

Initial infrastructure patterns:

- runner lost communication
- logs unavailable for failed job
- cancelled before tests started
- runner shutdown
- out of disk space

Policy for infrastructure failures:

- Do not dispatch a code-fix worker by default.
- Post a PR comment explaining the infra failure and, if supported,
  rerun failed checks.
- If `/herd fix-ci` is used with `Force`, preserve manual override and
  allow a fix worker.

Tests:

- Runner lost communication annotation posts infra comment and does not
  create issue.
- Log unavailable without test failure context is infra/unknown and does
  not create issue by default.
- Normal RSpec failure creates issue.
- Manual forced `/herd fix-ci` still creates issue.

## Documentation

Update docs to explain:

- `integrator.ci_workflows`
- why GitHub Actions CI uses `workflow_run` instead of `check_run`
- monitor is fallback only
- `/herd fix-ci` remains a manual override
- CI fix issues now include diagnostics

Likely files:

- `docs/configuration.md`
- any existing integrator/CI automation docs
- `CHANGELOG.md`

## Acceptance Criteria

- A repository can configure CI workflow names in `.herdos.yml`.
- `herd init` renders a `workflow_run` trigger for those CI workflows.
- A failed configured CI workflow on a `herd/batch/*` branch creates a
  CI fix issue without waiting for all sibling workflows to finish.
- A failed CI workflow with another workflow still pending does not get
  suppressed as `pending`.
- CI fix issue bodies include workflow/run/job context and a short log
  excerpt or a clear "logs unavailable" note.
- Obvious infrastructure failures do not dispatch code-fix workers by
  default.
- Existing behavior remains unchanged when `integrator.ci_workflows` is
  empty.
- `/herd fix-ci` still works and can force a fix cycle.
- All new behavior is covered by unit tests.
- Before pushing, workers run:
  - `go build ./...`
  - `go test ./...`
  - `go vet ./...`
  - `golangci-lint run`

## Suggested Batch Shape

This is likely a 4-tier batch:

1. Config and workflow rendering for `integrator.ci_workflows`.
2. `--ci-run-id` command path and `CheckCI` trigger-context semantics.
3. CI diagnostics, failure classification, and richer fix issue bodies.
4. Documentation and changelog updates.

Keep tasks small enough that each worker can validate its own package
tests plus the final full Go validation suite.
