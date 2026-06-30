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
  provider: "claude"             # claude | opencode | codex (cursor, gemini coming soon)
  binary: ""                     # path to agent binary (auto-detect if empty)
  model: ""                      # model override (optional, agent-specific)
  codex_reasoning_effort: "medium" # minimal | low | medium | high (codex provider only)
  codex_sandbox: ""              # read-only | workspace-write | danger-full-access (codex only; empty = role/context-aware default)
  max_turns: 0                   # max agentic turns in headless mode (0 = agent default; ignored by opencode)
  exec: "local"                  # local | docker — where `herd plan` runs the agent (local default)
  exec_image: ""                 # override image for exec: docker (default ghcr.io/herd-os/herd-runner-base:<herd-version>)
  planner:                       # optional sparse overrides for herd plan only
    provider: "claude"           # provider, binary, model, max_turns, codex_reasoning_effort, codex_sandbox
  workers:                       # optional sparse overrides for workers, integrator, and review
    codex_sandbox: "danger-full-access"

workers:
  max_concurrent: 3              # max simultaneous worker Actions
  runner_label: "herd-worker"    # GitHub runner label for worker jobs
  timeout_minutes: 30            # max time per worker run
  progress_interval_seconds: 30  # post progress updates to issue (0 = disabled)
  extra_env: []                  # GitHub Actions secret names to pass through to the worker workflow

image_publish:
  runs_on: ["ubuntu-latest"]      # runner labels for herd-publish-runner.yml

integrator:
  strategy: "squash"             # squash | rebase | merge
  on_conflict: "notify"          # notify | dispatch-resolver
  max_conflict_resolution_attempts: 2  # when exhausted, batch enters cascade-failed state
                                       # (see design/execution.md#when-cascades-fail)
  require_ci: true
  ci_workflows: []              # explicit GitHub Actions CI workflow names for workflow_run self-heal
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

## Agent Providers

`agent.provider` selects which AI coding agent the worker shells out to. Valid values are `claude` (Anthropic Claude Code, default), `opencode` ([OpenCode](https://opencode.ai)), and `codex` (OpenAI Codex CLI, API-key auth).

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `agent.provider` | string | `claude` | `claude`, `opencode`, or `codex` |
| `agent.binary` | string | `claude` for the `claude` provider, `opencode` for the `opencode` provider, `codex` for the `codex` provider | Path to the agent CLI binary. Empty falls back to the provider default and resolves via `PATH`. |
| `agent.model` | string | `""` (provider default) | Model override. For `opencode`, use the provider/model form, e.g. `anthropic/claude-sonnet-4` or `openai/gpt-5`. For `codex`, use a **bare** model ID, e.g. `gpt-5-codex` or `gpt-5.2` (see [Codex model IDs](#codex-model-ids)). |
| `agent.codex_reasoning_effort` | string | `medium` | Reasoning effort for the `codex` provider only: `minimal`, `low`, `medium`, or `high`. See [Codex reasoning effort](#codex-reasoning-effort). |
| `agent.codex_sandbox` | string | `""` (role/context-aware) | Codex CLI sandbox policy: `read-only`, `workspace-write`, or `danger-full-access`. Empty is resolved by HerdOS from the role and execution context: local planning leaves it empty so Codex uses `workspace-write`; Docker planning and worker-like roles use `danger-full-access`. See [Codex sandbox](#codex-sandbox). |
| `agent.max_turns` | int | `0` (agent default) | Maximum agentic turns in headless mode. **Ignored by the `opencode` provider** — OpenCode's `run` subcommand has no max-turns flag. |
| `agent.exec` | string | `local` | `local` runs the agent on your machine (requires the agent CLI installed locally). `docker` runs `herd plan` inside `ghcr.io/herd-os/herd-runner-base`, which already carries all agent CLIs — no local agent install needed beyond Docker + the herd binary. See [Local vs Docker Agent Execution](#local-vs-docker-agent-execution). |
| `agent.exec_image` | string | `ghcr.io/herd-os/herd-runner-base:<herd-version>` | Override the image used by `exec: docker`. Empty defaults to the version-pinned base image (falls back to `:latest` on dev builds). |

### Per-role agent overrides

Base `agent` fields apply to every role. Optional `agent.planner` and `agent.workers` blocks are sparse overrides: include only the fields that should differ from the base. The fields that can be overridden per role are `provider`, `binary`, `model`, `max_turns`, `codex_reasoning_effort`, and `codex_sandbox`.

`agent.exec` and `agent.exec_image` stay base-only. They control where `herd plan` runs and are not valid under role blocks. The integrator and agent review use the `workers` role.

```yaml
agent:
  provider: codex
  model: gpt-5-codex
  planner:
    provider: claude
    model: sonnet
  workers:
    codex_sandbox: danger-full-access
```

The `herd config list`, `herd config get`, and `herd config set` commands surface explicit role override fields only. They do not print or write resolved role defaults.

### Example: OpenCode

```yaml
agent:
  provider: "opencode"
  binary: ""                       # defaults to "opencode"
  model: "anthropic/claude-sonnet-4"
  max_turns: 0                     # ignored by opencode
```

The runner environment must have an API key for whichever provider the model resolves to — e.g. `ANTHROPIC_API_KEY` for `anthropic/...` models, `OPENAI_API_KEY` for `openai/...` models. See [runners.md](runners.md#2-agent-authentication) for the authentication setup.

### Example: Codex

```yaml
agent:
  provider: "codex"
  binary: ""                       # defaults to "codex"
  model: "gpt-5-codex"             # bare model ID, NOT openai/gpt-5
  codex_reasoning_effort: "medium" # minimal | low | medium | high
```

The Codex provider uses API-key auth: set `OPENAI_API_KEY` (herd maps it to `CODEX_API_KEY` at invocation time when `CODEX_API_KEY` is unset) or set `CODEX_API_KEY` directly. See [runners.md](runners.md#codex-provider-agentprovider-codex) for the authentication setup.

A minimal Codex config can omit role blocks:

```yaml
agent:
  provider: codex
  model: gpt-5-codex
```

With empty or local `agent.exec`, the planner leaves `codex_sandbox` empty and Codex uses its own `workspace-write` default. Workers, integrator, and review resolve to `danger-full-access` because they run in the runner container. If `agent.exec: docker`, the planner also resolves to `danger-full-access`. Explicit base or role `codex_sandbox` values override these smart defaults.

#### Codex model IDs

Unlike the `opencode` provider, the `codex` provider expects a **bare** model ID in `agent.model` — e.g. `gpt-5-codex`, `gpt-5.2`, or `gpt-5.1` — **not** a provider-prefixed form like `openai/gpt-5`. The bare ID is passed straight through to the Codex CLI via `codex exec --model <id>`.

The IDs referenced by the source fixtures — `gpt-5-codex`, `gpt-5.2`, `gpt-5.1`, and `gpt-5.1-codex-max` — are known-good. <!-- TODO(verify): the full accepted Codex model registry is not exhaustively documented here. Confirm a candidate ID by running `codex exec --model <candidate>` before relying on it. -->

#### Codex reasoning effort

`agent.codex_reasoning_effort` controls how much reasoning the Codex model applies. It applies to the `codex` provider only.

| Value | Notes |
|-------|-------|
| `minimal` | Least reasoning. |
| `low` | |
| `medium` | **Default.** |
| `high` | Most reasoning. |

The configured value maps to `-c model_reasoning_effort=<value>` on every Codex invocation (`exec`, plan, and review). An invalid value is rejected by config validation.

#### Codex sandbox

`agent.codex_sandbox` selects the Codex CLI's sandbox policy. Maps to `--sandbox <value>` on every `codex exec` invocation. Applies to the `codex` provider only.

| Value | Notes |
|-------|-------|
| `read-only` | Codex can read files but not modify them. Bubblewrap-based. |
| `workspace-write` | **Codex CLI default.** Codex can write within the workspace; network blocked. Bubblewrap-based. |
| `danger-full-access` | No bubblewrap layer. Codex runs commands and edits files with full process privileges (the outer container or host is the boundary). |
| `""` (empty) | HerdOS resolves by role and execution context. Local/empty-exec planning stays empty, so Codex uses `workspace-write`; Docker planning and worker-like roles use `danger-full-access`. |

When unset, HerdOS never produces `read-only` as a smart default. Workers, the integrator, and agent review default to `danger-full-access` because they run inside the runner container. The bubblewrap-based modes need an unprivileged user namespace, which most container hosts (notably TrueNAS SCALE Apps, default Docker without `sysctl kernel.unprivileged_userns_clone=1`) do not grant. The container is already the security boundary in worker contexts, so disabling Codex's inner sandbox there is the correct trade-off.

For local `agent.exec: local` planning, leave it empty unless you specifically know your host can't run bubblewrap. If you previously set a broad base `agent.codex_sandbox: danger-full-access`, you can keep it for zero behavior change after upgrading. If you want safer local planning while keeping container workers on full access, move that value into `agent.workers.codex_sandbox` instead:

```yaml
agent:
  provider: codex
  model: gpt-5-codex
  workers:
    codex_sandbox: danger-full-access
```

## Local vs Docker Agent Execution

`agent.exec` controls where `herd plan` runs the coding agent. The default is `local`: the agent runs directly on your machine. Power users who already have the agent CLIs installed prefer this — there is no container overhead.

Setting `agent.exec: docker` runs `herd plan` inside the published runner image (`ghcr.io/herd-os/herd-runner-base`). herd mounts your current repo at `/work` inside the container and runs the same agent toolchain the workers use, so you need zero local agent install beyond Docker + the herd binary.

```yaml
agent:
  provider: "claude"
  exec: "docker"                   # run `herd plan` inside the runner image
  exec_image: ""                   # optional: override the default base image
```

### Override precedence

For one-off runs you can override the configured value. Precedence, highest first:

```
--exec local|docker  (flag)  >  HERD_EXEC env  >  agent.exec config  >  local (default)
```

### Recursion guard

When herd runs inside the container it sets `HERD_INSIDE_CONTAINER=1` and forces `local` execution for that process. This means a mounted `.herdos.yml` that says `exec: docker` cannot cause infinite docker-in-docker recursion — the inner herd always runs the agent locally inside the container. A single warning is logged when the guard fires.

### Behavior under `exec: docker`

- **First-run image pull**: the first run pulls the multi-minute base image (Docker caches it afterward). herd prints a one-line `Pulling …` hint before the pull so you know what is happening.
- **File ownership**: herd runs the container with `--user $(id -u):$(id -g)`, so files the agent creates in your worktree are owned by you, not root.
- **gh auth**: if `~/.config/gh` exists it is mounted read-only, so the in-container herd can call the GitHub API with your existing `gh` credentials.
- **`$EDITOR` caveat**: if the agent shells out to `$EDITOR` (e.g. Claude Code for some prompts), that editor runs **inside** the container, which does not carry your host editor. Avoid editor-dependent flows under `exec: docker`, or use `exec: local` for those.

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

`integrator.require_ci` enables CI failure detection on the batch branch. `integrator.ci_max_fix_cycles` caps how many CI-failure fix cycles the Integrator will dispatch (0 = unlimited).

`integrator.ci_workflows` defaults to an empty list. When non-empty, `herd init` renders `workflow_run` triggers for those exact GitHub Actions workflow names, and failed completed runs on `herd/batch/` branches can self-heal without waiting for the Monitor. The strings are matched exactly and preserved as configured, including punctuation and Unicode dashes.

GitHub Actions CI uses `workflow_run` because `check_run` events are unreliable for some Actions-created check suites. The existing `check_run` path remains as a fallback for third-party check providers. The scheduled Monitor also remains a fallback for batch PR CI failures, and `/herd fix-ci` remains a manual override that can force a CI fix cycle.

CI fix issues include the workflow/run URL, failed job URLs, head branch, head SHA, annotations when available, and either a short log excerpt or a logs-unavailable note. Obvious runner/log infrastructure failures are classified separately: Herd comments on the batch PR and does not dispatch code-fix workers for those by default.

CheckCI pauses dispatching a new CI fix worker if any fix-type worker — review fix, CI fix, or conflict resolution — is still in progress in the same batch milestone. The next `workflow_run` trigger (when that worker completes) re-runs CheckCI, which then proceeds with dispatch if CI is still failing. This prevents the Integrator from creating overlapping fix workers that would race on the same batch branch.

## Worker Extra Env

`workers.extra_env` is a list of GitHub Actions secret names to surface as environment variables in the worker workflow's `Execute task` step.

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

## Image Publish Runner

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `image_publish.runs_on` | string list | `["ubuntu-latest"]` | Runner labels for the `herd-publish-runner.yml` workflow. Override with `["self-hosted", "<label>"]` to build on your own host. See [Publishing on a self-hosted runner](runners.md#publishing-on-a-self-hosted-runner) for prerequisites. Not settable via `herd config <key> <value>`; edit `.herdos.yml` directly or use `herd config edit`. |

## Worker Timeouts

`workers.timeout_minutes` is the outer GitHub Actions job timeout rendered into
`.github/workflows/herd-worker.yml`. GitHub Actions cancels the whole worker job
when this limit is reached.

HerdOS gives the agent a shorter inner execution deadline so the worker has time
to clean up before the outer job is cancelled. The cleanup window is used to stop
the agent process, inspect git state, create timeout checkpoints when needed,
report diagnostics, and push resumable checkpoint state.

```yaml
workers:
  timeout_minutes: 30
```

On inner timeout, HerdOS asks the agent process to stop. On Unix-like runners,
the shared process helper starts agent commands in their own process group and
terminates that group where possible. This matters for Codex because the Node
wrapper can spawn a native `codex` child process. On Windows, HerdOS falls back
to terminating the direct child process.

Timeout recovery depends on what the worker can find:

- In batch mode, committed work on the worker branch is preserved for retry.
  Uncommitted work is staged into a checkpoint commit on the worker branch and
  pushed so the Monitor retry can resume from the remote branch. The timed-out
  attempt still fails: it does not record a validation success marker, label the
  issue `herd/status:done`, or allow Integrator consolidation as a completed
  worker.
- In standalone fix mode, uncommitted work is checkpointed and pushed directly to
  the PR head branch for preservation, then HerdOS reports timeout diagnostics
  and leaves the tracking issue retryable/failed instead of marking it done.
- If no committed or uncommitted work exists, HerdOS posts a diagnostic failure
  comment and lets the Monitor retry the issue instead of silently leaving it
  without useful output.

Progress files under `.herd/progress/<issue>.md` are still recommended for
agent-authored progress and live issue updates. Timeout checkpoint commits are
the worker's fallback for preserving repository state when the agent times out
before it can make its own commit and push.

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
| `HERD_EXEC` | `agent.exec` (one-off; the `--exec` flag still wins) |

Environment variables take precedence over `.herdos.yml`.
