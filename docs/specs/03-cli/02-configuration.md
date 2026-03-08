# Configuration

HerdOS is configured via `.herdos.yml` at the repository root. This file is created by `herd init` and checked into version control.

## Default Configuration

```yaml
# .herdos.yml
version: 1

# Platform settings (where work is tracked and executed)
platform:
  provider: "github"             # github (gitlab, gitea coming soon)
  owner: "my-org"                # repo owner (org or user)
  repo: "my-project"             # repo name

# Agent settings (the AI coding tool)
agent:
  provider: "claude"             # claude (codex, cursor, gemini, opencode coming soon)
  binary: ""                     # path to binary (default: auto-detect)
  model: ""                      # model override (optional, agent-specific)

# Worker settings
workers:
  max_concurrent: 3
  runner_label: "herd-worker"
  timeout_minutes: 30

# Integration and merge management
integrator:
  strategy: "squash"           # squash | rebase | merge
  on_conflict: "notify"        # notify | dispatch-resolver
  max_conflict_resolution_attempts: 2   # max resolver attempts before falling back to notify
  require_ci: true
  review: true                 # agent reviews batch PRs before merge
  review_max_fix_cycles: 3     # max fix-and-re-review cycles
  ci_max_fix_cycles: 2         # max CI-failure fix cycles (0 = notify-only)

# Health monitoring
monitor:
  patrol_interval_minutes: 15
  stale_threshold_minutes: 30
  max_pr_age_hours: 24
  auto_redispatch: true
  max_redispatch_attempts: 3
  notify_on_failure: true            # Monitor comments on issue when worker fails
  notify_users: []                   # GitHub usernames to @mention on escalation

# PR settings
pull_requests:
  auto_merge: false               # Disabled by default — human reviews batch PRs
  co_author: true                 # Add Co-authored-by: herd-os[bot] trailer to worker commits
```

## Configuration Fields

### platform

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"github"` | Platform provider: `github` (`gitlab`, `gitea` coming soon) |
| `owner` | string | — | Repository owner (org or user), detected from git remote |
| `repo` | string | — | Repository name, detected from git remote |

These are populated by `herd init` from the git remote URL. They can be overridden manually for non-standard setups.

### agent

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"claude"` | Agent provider: `claude` (`codex`, `cursor`, `gemini`, `opencode` coming soon) |
| `binary` | string | `""` | Path to agent binary (auto-detected if empty) |
| `model` | string | `""` | Model override (optional, agent-specific) |

### workers

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_concurrent` | number | 3 | Maximum simultaneous worker Actions |
| `runner_label` | string | `"herd-worker"` | GitHub runner label for worker jobs |
| `timeout_minutes` | number | 30 | Max time per worker run |

### integrator

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `strategy` | string | `"squash"` | How the batch PR is merged: `squash`, `rebase`, `merge` |
| `on_conflict` | string | `"notify"` | Conflict resolution: `notify`, `dispatch-resolver` |
| `max_conflict_resolution_attempts` | number | 2 | Max resolver attempts before falling back to `notify` |
| `require_ci` | boolean | true | Require CI to pass before merge |
| `review` | boolean | true | Agent reviews batch PRs before merge |
| `review_max_fix_cycles` | number | 3 | Max fix-and-re-review cycles before escalating to user |
| `ci_max_fix_cycles` | number | 2 | Max CI-failure fix cycles before escalating to user (0 = notify-only) |

### monitor

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `patrol_interval_minutes` | number | 15 | How often the Monitor runs |
| `stale_threshold_minutes` | number | 30 | Time before an issue is considered stale |
| `max_pr_age_hours` | number | 24 | Time before a PR is considered stuck |
| `auto_redispatch` | boolean | true | Dispatch a new Action run for failed workers |
| `max_redispatch_attempts` | number | 3 | Max re-dispatch attempts by the Monitor |
| `notify_on_failure` | boolean | true | Monitor comments on issue when worker fails |
| `notify_users` | list | `[]` | GitHub usernames to `@mention` on escalation |

### pull_requests

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `auto_merge` | boolean | false | Auto-merge batch PRs after agent review passes |
| `co_author` | boolean | true | Add `Co-authored-by: herd-os[bot]` trailer to worker commits |

Branch naming conventions (`herd/worker/<issue>-<slug>`, `herd/batch/<milestone>-<slug>`) and the PR title prefix (`[herd]`) are hardcoded and not configurable.

## Init Process

`herd init` performs these steps:

1. **Check prerequisites**
   - Git repository exists
   - GitHub remote is configured
   - GitHub API authentication available: `GITHUB_TOKEN` env var (recommended — create a [Personal Access Token](https://github.com/settings/tokens) with `repo` scope) or `gh auth login` (convenience — `herd` reads the stored token)

2. **Create configuration**
   - Write `.herdos.yml` with defaults
   - Create `.herd/` directory (tracked in git) with role instruction files: `planner.md`, `worker.md`, `integrator.md`, `monitor.md` (empty, created only if they don't already exist)
   - Create `.herd/state/` subdirectory for local runtime state (plan JSON and other transient data)
   - Add `.herd/state/` to `.gitignore`
   - Prompt for customization (model, max workers, etc.)

3. **Set up GitHub labels**
   - Create all `herd/*` labels via GitHub API
   - Skip existing labels (idempotent)

4. **Install workflow files**
   - Write `herd-worker.yml`, `herd-monitor.yml`, `herd-integrator.yml` to `.github/workflows/`
   - Substitute config values into workflow YAML: `workers.runner_label` → default `runner_label` input in worker workflow, `monitor.patrol_interval_minutes` → cron schedule
   - Note: changing `runner_label` or `patrol_interval_minutes` after init requires re-running `herd init --skip-labels` to regenerate workflow files. Per-issue `runner_label` overrides (set in issue front matter) do not require workflow regeneration.

5. **Set up secrets**
   - Prompt to set up agent credentials as a repository secret
   - Guide user through `claude setup-token` for OAuth (recommended) or API key setup
   - Provide URL to the repo secrets settings page

6. **Verify**
   - Confirm labels exist
   - Print next steps (including Docker runner setup)

## Config Validation

The CLI validates `.herdos.yml` on every command:

- `version` must be a supported version (current: `1`)
- `platform.provider` must be one of: `github` (`gitlab`, `gitea` coming soon)
- `agent.provider` must be one of: `claude` (`codex`, `cursor`, `gemini`, `opencode` coming soon)
- `workers.max_concurrent` must be > 0
- `workers.timeout_minutes` must be > 0
- `integrator.strategy` must be one of: `squash`, `rebase`, `merge`
- `integrator.on_conflict` must be one of: `notify`, `dispatch-resolver`
- `integrator.review_max_fix_cycles` must be > 0
- `integrator.ci_max_fix_cycles` must be >= 0 (0 = notify-only)
- `integrator.max_conflict_resolution_attempts` must be > 0
- `monitor.patrol_interval_minutes` must be >= 5
- `monitor.stale_threshold_minutes` must be > 0
- `monitor.max_pr_age_hours` must be > 0
- `monitor.max_redispatch_attempts` must be > 0

Note: `stale_threshold_minutes` should be greater than `timeout_minutes` to avoid false stale detections during normal worker timeouts. The CLI warns (but does not error) if `stale_threshold_minutes <= timeout_minutes`.

Invalid config produces a clear error:

```
Error: Invalid .herdos.yml at line 5:
  workers.max_concurrent must be > 0, got -1
```

## Config Migration

The `version` field in `.herdos.yml` is an integer (`1`, `2`, `3`, etc.) that tracks breaking schema changes.

**When the version does NOT change:**
- Adding a new optional field with a default — the CLI uses the default if the field is missing. No migration needed, no version bump. Users can add the field manually if they want to customize it.

**When the version bumps:**
- Field renamed, removed, or semantics changed — anything where the CLI can't infer the right behavior from an older config.

When the CLI detects an outdated `version`, it prompts to migrate:

```
$ herd status
Your .herdos.yml is version 1, but herd requires version 2.
Migrate? [y/n] y

Migrating .herdos.yml: version 1 → 2
  ✓ Renamed workers.model → agent.model
  ✓ Added integrator.ci_max_fix_cycles: 2 (new default)

Done. Review the changes with `git diff .herdos.yml`.
```

Migrations are applied sequentially (1→2, 2→3, etc.) and handle field renames, new fields with defaults, and removed fields. The file is committed in git, so changes are easy to review and revert.

The CLI refuses to run with an outdated config — the user must either migrate or manually update the `version` field.

## Environment Variables

Configuration can be overridden via environment variables:

| Variable | Overrides |
|----------|-----------|
| `HERD_MAX_WORKERS` | `workers.max_concurrent` |
| `HERD_RUNNER_LABEL` | `workers.runner_label` |
| `HERD_MODEL` | `agent.model` |
| `HERD_TIMEOUT` | `workers.timeout_minutes` |

Environment variables take precedence over `.herdos.yml`. This is useful for CI or when running with different settings temporarily.

## Role Instruction Files

Each HerdOS role can receive project-specific instructions via convention-based files in the `.herd/` directory:

| File | Appended to |
|------|-------------|
| `.herd/planner.md` | Planner's system prompt |
| `.herd/worker.md` | Worker's system prompt |
| `.herd/integrator.md` | Integrator's agent prompts (review and conflict resolution) |
| `.herd/monitor.md` | Monitor's agent prompt |

No configuration is needed — if the file exists, its contents are appended to the corresponding role's prompt. These files are checked into version control and shared across the team. The `.herd/` directory itself is tracked in git; only `.herd/state/` is gitignored (for transient data like plan JSON files). Empty instruction files are created by `herd init`.

## Multiple Repos

Each repo has its own `.herdos.yml`. There's no global HerdOS configuration in v1.0. If you use HerdOS across multiple repos, each is configured independently.

Cross-repo coordination (sharing runners, unified status) is a future feature.

## Concurrent Users

Multiple users can run `herd plan` simultaneously in the same repo. Each plan creates its own batch (milestone) and branch. Concurrent batches share the global `workers.max_concurrent` pool — if one batch is using all runner slots, the other batch's workers queue until slots free up. No coordination between users is needed.
