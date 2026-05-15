# Configuration

HerdOS is configured via `.herdos.yml` at the repository root. Created by `herd init` and checked into version control.

## Full Reference

```yaml
# .herdos.yml
version: 1

platform:
  provider: "github"             # github (gitlab, gitea coming soon)
  owner: "my-org"                # repo owner — auto-detected from git remote
  repo: "my-project"             # repo name — auto-detected from git remote

agent:
  provider: "claude"             # claude (codex, cursor, gemini, opencode coming soon)
  binary: ""                     # path to agent binary (auto-detect if empty)
  model: ""                      # model override (optional, agent-specific)
  max_turns: 0                   # max agentic turns in headless mode (0 = agent default)

workers:
  max_concurrent: 3              # max simultaneous worker Actions
  runner_label: "herd-worker"    # GitHub runner label for worker jobs
  timeout_minutes: 30            # max time per worker run
  progress_interval_seconds: 30  # post progress updates to issue (0 = disabled)
  extra_env: []                  # GitHub Actions secret names to pass through to the worker workflow

integrator:
  strategy: "squash"             # squash | rebase | merge
  on_conflict: "notify"          # notify | dispatch-resolver
  max_conflict_resolution_attempts: 2  # when exhausted, batch enters cascade-failed state
                                       # (see design/execution.md#when-cascades-fail)
  require_ci: true
  review: true                   # agent reviews batch PRs before merge
  review_max_fix_cycles: 0       # max fix-and-re-review cycles (0 = unlimited)
  review_strictness: "standard"  # standard | strict | lenient
  review_fix_severity: "low"     # minimum severity to create fix workers: high | medium | low
  ci_max_fix_cycles: 0           # max CI-failure fix cycles (0 = unlimited)

monitor:
  patrol_interval_minutes: 15
  stale_threshold_minutes: 30
  max_pr_age_hours: 24
  auto_redispatch: true
  max_redispatch_attempts: 3
  notify_on_failure: true
  notify_users: []               # GitHub usernames to @mention on escalation

pull_requests:
  auto_merge: false              # auto-merge batch PRs after review passes
  co_author_email: ""            # Co-authored-by email (set after installing the GitHub App)
```

## Review Strictness

Controls how aggressively the agent reviewer flags issues:

| Level | Behavior |
|-------|----------|
| `standard` (default) | Flags bugs, security issues, missing error handling. Ignores style preferences. |
| `strict` | Also flags style issues, missing edge cases, code quality improvements. |
| `lenient` | Only flags critical bugs and security vulnerabilities. |

Findings are classified by severity:
- **HIGH**: Bugs, security vulnerabilities, race conditions, missing critical error handling — triggers fix workers
- **MEDIUM**: Missing edge cases, suboptimal error handling — triggers fix workers
- **LOW**: Style preferences, naming suggestions — informational only

HIGH and MEDIUM severity findings create fix issues and dispatch workers. LOW findings are listed in the PR comment for reference.

## Agent Review

The review agent runs in a strict output mode. It is instructed not to take any actions — no tool calls, no `gh`/`git`/`bash` invocations, no issue or comment creation, no file edits. Its only output is a single JSON object describing findings. Any mention of `.herd/integrator.md` or extra review instructions is appended to that contract; it does not loosen it.

If the agent returns unparseable output (e.g., the JSON cannot be decoded, or the output is empty/error-like), the integrator retries once after a 5-second delay within the same invocation. If both attempts fail, the integrator posts the following comment on the batch PR and sets the review aside without creating fix workers:

```
⚠️ **HerdOS Integrator** — Agent review failed to produce valid output after 2 attempts. Run `/herd review` manually to retry.
```

When you see that comment, run `/herd review` (optionally with a focus area) on the batch PR to trigger a fresh review. The integrator does not silently drop the review or auto-approve the PR.

## CI Fix Loop

`integrator.require_ci` enables CI failure detection on the batch branch, and `integrator.ci_max_fix_cycles` caps how many CI-failure fix cycles the Integrator will dispatch (0 = unlimited).

CheckCI pauses dispatching a new CI fix worker if any fix-type worker — review fix, CI fix, or conflict resolution — is still in progress in the same batch milestone. The next `workflow_run` trigger (when that worker completes) re-runs CheckCI, which then proceeds with dispatch if CI is still failing. This prevents the Integrator from creating overlapping fix workers that would race on the same batch branch.

## Worker Extra Env

`workers.extra_env` is a list of GitHub Actions secret names to surface as environment variables in the worker workflow's `Execute task` step, in addition to the built-in AI provider keys.

| Field | Type | Default |
|-------|------|---------|
| `workers.extra_env` | `[]string` (list of GitHub Actions secret names) | `[]` |

Each entry must be the exact name of a GitHub Actions repository or organization secret. herd does **not** create the secret — you must add it under **Settings → Secrets and variables → Actions** before referencing it here.

```yaml
workers:
  max_concurrent: 3
  runner_label: herd-worker
  timeout_minutes: 30
  extra_env:
    - BUNDLE_RUBYGEMS__PKG__GITHUB__COM
    - NPM_TOKEN
```

For each name, the rendered worker workflow gets one line in the `env:` block of the form:

```yaml
BUNDLE_RUBYGEMS__PKG__GITHUB__COM: ${{ secrets.BUNDLE_RUBYGEMS__PKG__GITHUB__COM }}
```

The value comes from the GitHub Actions secret of the same name at workflow runtime.

This passthrough applies only to the worker workflow. The integrator and monitor workflows are not affected.

The workflow file is generated by `herd init`, so re-run `herd init` after editing `extra_env` to regenerate `.github/workflows/herd-worker.yml` with the new entries.

## Managing Configuration

```bash
herd config list                              # show all settings
herd config get workers.max_concurrent        # get a specific value
herd config set workers.max_concurrent 5      # set a value
herd config edit                              # open in $EDITOR
```

## Environment Variable Overrides

| Variable | Overrides |
|----------|-----------|
| `HERD_MAX_WORKERS` | `workers.max_concurrent` |
| `HERD_RUNNER_LABEL` | `workers.runner_label` |
| `HERD_MODEL` | `agent.model` |
| `HERD_TIMEOUT` | `workers.timeout_minutes` |
| `HERD_REVIEW_STRICTNESS` | `integrator.review_strictness` |

Environment variables take precedence over `.herdos.yml`.
