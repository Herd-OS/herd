# CLI Commands

Full reference for the `herd` CLI. All commands interact with the GitHub API — the CLI is a thin client.

**Global flags:** `--version` (print version and exit), `--help` (user commands), `--help-all` (all commands including internal).

## Command Overview

### User Commands

| Command | Purpose |
|---------|---------|
| `herd init` | Set up a repo for HerdOS |
| `herd plan` | Decompose a feature into issues and dispatch workers |
| `herd dispatch` | Manually dispatch workers (re-dispatch, catch-up) |
| `herd status` | Show current state of workers, issues, PRs |
| `herd batch` | Manage batches (milestones) |
| `herd runner` | Check runner status |
| `herd config` | View/edit configuration |

### Internal Commands (used by GitHub Actions)

These are invoked by workflow YAML files, not by users directly. Hidden from `herd --help` by default (use `herd --help-all` to see them). Internal commands require `HERD_RUNNER=true` and exit with an error if run outside that environment. This variable is set in the workflow YAML files installed by `herd init`.

| Command | Purpose |
|---------|---------|
| `herd worker exec` | Execute a task from an issue |
| `herd integrator consolidate` | Merge worker branch into batch branch |
| `herd integrator advance` | Check tier completion, dispatch next tier |
| `herd integrator review` | Run agent review on batch PR |
| `herd monitor patrol` | Check for stale/failed work |

## herd init

Set up a repository for HerdOS. Installs workflow files, creates labels, and configures the project. Runners are set up separately via Docker (see [runners.md](../02-github/03-runners.md)).

```
herd init [flags]

Flags:
  --skip-labels     Don't create labels (useful if they already exist)
  --skip-workflows  Don't install workflow files
```

**What it does:**

1. Creates `.herdos.yml` with default settings (if not present)
2. Creates `.herd/` runtime directory and adds it to `.gitignore`
3. Creates `herd/*` labels in the repository
4. Installs `.github/workflows/herd-worker.yml`
5. Installs `.github/workflows/herd-monitor.yml`
6. Installs `.github/workflows/herd-integrator.yml`
7. Prompts to set up agent credentials (OAuth token or API key) as a repository secret

**Example:**

```bash
$ cd my-project
$ herd init
✓ Created .herdos.yml
✓ Created 8 labels (herd/status:ready, herd/type:feature, ...)
✓ Installed herd-worker.yml
✓ Installed herd-monitor.yml
✓ Installed herd-integrator.yml
Set up agent credentials:
  1. Run: claude setup-token (recommended, uses your subscription)
  2. Store the token as CLAUDE_CODE_OAUTH_TOKEN in repo secrets
  → https://github.com/org/repo/settings/secrets/actions

Setup complete! Next steps:
  Set up runners     See docs/runners.md for Docker setup
  herd plan          Start a planning session
  herd plan "..."    Start a planning session with an initial prompt
  herd status        Check system status
```

## herd plan

Decompose a feature into issues. Always launches an interactive agent session.

```
herd plan [description] [flags]

Arguments:
  description       Optional. If provided, sent as the first message to the agent.
                    If omitted, the agent asks what to build.

Flags:
  --no-dispatch     Don't dispatch workers after creating issues (default: dispatch Tier 0)
  --batch <name>    Name for the batch (default: derived from description)
  --dry-run         Show what would be created without creating anything
```

**Example** (with initial prompt):

```bash
$ herd plan "I want to add user authentication"

Starting planning session...

Agent: A few questions:
  1. What auth method — session-based, JWT, or OAuth?
  2. Do you need registration, or just login?
  3. Are there existing user models or is this greenfield?

User: JWT, both login and registration, greenfield

Agent: Got it. Here's my proposed decomposition:

  1. Add auth dependencies (bcrypt, jsonwebtoken)
     Scope: package.json — Complexity: low

  2. Create User model with password hashing
     Scope: src/models/user.ts — Complexity: medium

  3. Create auth middleware for JWT validation
     Scope: src/middleware/auth.ts — Complexity: medium

  4. Create login and register endpoints
     Scope: src/routes/auth.ts — Complexity: medium
     Depends on: #2, #3

  5. Add auth integration tests
     Scope: tests/auth.test.ts — Complexity: medium
     Depends on: #4

Create batch "Add JWT authentication" with 5 issues? [y/n/edit] y

Plan saved. Exit this session to create issues and dispatch workers.
(Press Ctrl+C or type /exit)

$ # After exiting:
✓ Created milestone #5: Add JWT authentication
✓ Created 5 issues (#42-#46)
✓ Created batch branch: herd/batch/5-add-jwt-authentication
Dispatching Tier 0 (3 issues with no dependencies):
  #42 Add auth dependencies              ✓ triggered
  #43 Create User model                  ✓ triggered
  #44 Create auth middleware              ✓ triggered
  (2 issues blocked, will dispatch when Tier 0 completes)
```

If the user selects `edit`, the plan opens in `$EDITOR` as YAML for modification. The CLI re-parses and validates the edited plan before creating issues. The format matches the agent's output structure (task titles, descriptions, acceptance criteria, scopes, dependencies).

## herd dispatch

Manually trigger workers. Dispatch is idempotent — if the issue is already `herd/status:in-progress`, the command warns and skips it. Normally not needed since `herd plan` dispatches Tier 0 automatically and the Integrator advances tiers. Useful for re-dispatching failed issues or catching up after `herd plan --no-dispatch`.

Running `herd dispatch` with no arguments and no flags prints usage help.

```
herd dispatch <issue-number> [flags]
herd dispatch [flags]

Arguments:
  issue-number      Dispatch a single issue (must belong to a batch)

Flags:
  --batch <number>  Dispatch all ready and failed issues in a batch (milestone)
  --all             Dispatch all ready and failed issues (across all batches)
  --ignore-limit    Dispatch even if max_concurrent would be exceeded
  --timeout <min>   Override worker timeout (default from config)
  --dry-run         Show what would be dispatched
```

**Concurrency:** `--batch` and `--all` respect `workers.max_concurrent` (which is global across all batches, not per-batch). If 3 workers are already running and `max_concurrent` is 5, at most 2 more are dispatched. Remaining issues are skipped with a message — they'll be picked up by the Integrator's `advance` step or the next `dispatch` call. Use `--ignore-limit` to override.

**Failed issues:** `--batch` and `--all` include `herd/status:failed` issues in addition to `herd/status:ready`. This makes it easy to retry after transient failures (e.g., rate limits). Failed issues are re-labeled `herd/status:ready` before dispatch.

**Example:**

```bash
# Re-dispatch a single failed issue
$ herd dispatch 42
Dispatching worker for #42: Add CSS custom properties
Batch: Add dark mode support (milestone #7)
✓ Workflow triggered (run ID: 12345678)

# Dispatch all ready and failed issues in a batch
$ herd dispatch --batch 7
Batch: Add dark mode support (milestone #7)

Ready:
  #44 Add theme persistence             ✓ dispatching
Failed (retrying):
  #42 Add CSS custom properties          ✓ dispatching
Blocked:
  #45 Add dark mode tests               ◌ blocked by #43
In progress:
  #43 Create theme toggle               ⟳ worker active

Dispatched 2 workers. 1 still blocked.

# Dispatch all ready and failed issues across all batches
$ herd dispatch --all
Dispatching 3 of 5 dispatchable issues (max_concurrent: 5, 2 running):
  #44 Add theme persistence             ✓ triggered
  #48 Add dark mode toggle              ✓ triggered
  #49 Fix pagination (retry)            ✓ triggered
  ⚠ 2 issues skipped (at capacity)
```

## herd status

Show the current state of HerdOS in this repository.

```
herd status [flags]

Flags:
  --batch <number>  Show status for a specific batch
  --runners          Show runner status
  --json             Output as JSON
  --watch            Refresh every 10 seconds
```

**Example:**

```bash
$ herd status

Batches:
  #5  Auth system       4/5 done    1 worker active
  #7  Dark mode         1/4 done    1 worker active, 1 blocked

Workers:
  Run 12345678  #43 Create theme toggle    running (12m)  https://github.com/org/repo/actions/runs/12345678
  Run 12345679  #11 Add JWT validation     running (5m)   https://github.com/org/repo/actions/runs/12345679

Batch PRs:
  PR #50  [herd] Auth system             ⟳ agent review running
  PR #48  [herd] Dark mode               ✓ merged 10m ago

$ herd status --runners
RUNNER              STATUS    LABELS          BUSY
herd-worker-1       online    herd-worker     running #43
herd-worker-2       online    herd-worker     running #11
herd-worker-3       online    herd-worker     idle
```

## herd batch

Manage batches (GitHub Milestones).

```
herd batch list [flags]
herd batch show <number> [flags]
herd batch cancel <number> [flags]

Subcommands:
  list      List active batches
  show      Show batch details
  cancel    Cancel a batch (stop workers, close milestone, delete branch)

Flags (cancel):
  --force          Skip confirmation prompt

Flags (list):
  --all            Include completed batches
  --json           Output as JSON

Flags (show):
  --json           Output as JSON
```

**Example:**

```bash
$ herd batch list
  #5  Auth system       4/5 done    landed
  #7  Dark mode         2/4 done    in progress

$ herd batch show 7
Batch: Add dark mode support (#7)
Progress: 2/4 (50%)

  ✓  #42 Add CSS custom properties      consolidated
  ✓  #43 Create theme toggle            consolidated
  ○  #44 Add theme persistence           herd/status:ready
  ○  #45 Add dark mode tests              herd/status:ready (was blocked by #43)

Batch PR: PR #48 [herd] Dark mode — merged

$ herd batch cancel 7
Batch: Add dark mode support (#7)
WARNING: This will:
  - Cancel 1 active workflow run
  - Label 2 remaining issues as herd/status:failed
  - Close milestone #7
  - Delete branch herd/batch/7-add-dark-mode-support

Continue? [type "yes" to confirm] yes

Cancelling batch: Add dark mode support (#7)
  ✓ Cancelled 1 active workflow run
  ✓ Labeled 2 issues as herd/status:failed
  ✓ Closed milestone #7
  ✓ Deleted branch herd/batch/7-add-dark-mode-support
```

## herd worker

Commands used by the worker GitHub Action. These are not typically run by users directly — they're invoked by `herd-worker.yml`.

```
herd worker exec <issue-number>

Subcommands:
  exec      Execute a task from an issue using the configured agent
```

### herd worker exec

Handles the full worker lifecycle: reads the issue, labels it `herd/status:in-progress`, creates a worker branch, invokes the configured agent in headless mode (the agent commits as it works), pushes the branch, and labels the issue `herd/status:done` or `herd/status:failed`. The agent provider, binary, and model are determined from `.herdos.yml`.

```bash
# Invoked by herd-worker.yml:
$ herd worker exec 42
Reading issue #42: "Add user authentication middleware"
Agent: claude (from .herdos.yml)
Branch: herd/worker/42-add-user-authentication-middleware
Executing...
... agent runs, commits as it works ...
Pushing herd/worker/42-add-user-authentication-middleware
Done. 4 commits, 6 files modified.
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--timeout` | from config | Override timeout (minutes) |

## herd integrator

Commands used by the Integrator GitHub Action. These are not typically run by users directly — they're invoked by `herd-integrator.yml`.

```
herd integrator consolidate --run-id <run-id>
herd integrator advance --run-id <run-id>
herd integrator review --run-id <run-id>

Subcommands:
  consolidate   Merge a completed worker branch into the batch branch
  advance       Check tier completion, dispatch next tier or open batch PR
  review        Run agent review on the batch PR
```

### herd integrator consolidate

Determines the worker branch from the completed Action run, merges it into the batch branch, and pushes.

### herd integrator advance

Checks if all workers in the current tier are done. If so, updates blocked issues to `herd/status:ready` and dispatches the next tier. If all tiers are complete, opens the batch PR against `main`.

### herd integrator review

Dispatches the configured agent to review the batch PR diff. The agent checks acceptance criteria, looks for bugs and security issues, and posts a review on the PR. If issues are found and `review_max_fix_cycles` hasn't been exceeded, dispatches fix workers.

## herd monitor

Commands used by the Monitor GitHub Action. These are not typically run by users directly — they're invoked by `herd-monitor.yml`.

```
herd monitor patrol

Subcommands:
  patrol    Check for stale, failed, or stuck work and take corrective action
```

### herd monitor patrol

Runs a single patrol cycle: checks for stale issues (dispatched but no worker output after `stale_threshold_minutes`), failed Action runs, and stuck batch PRs. Can re-dispatch failed work (if `auto_redispatch` is enabled) or comment on issues to escalate. Safe to run repeatedly — actions are idempotent.

## herd runner

Query runner status from GitHub. Runners are set up via Docker (see [runners.md](../02-github/03-runners.md)), not managed by the CLI.

```
herd runner list

Subcommands:
  list      List registered runners and their status
```

**Example:**

```bash
$ herd runner list
RUNNER              STATUS    LABELS          BUSY
herd-worker-1       online    herd-worker     running #43
herd-worker-2       online    herd-worker     idle
herd-worker-3       offline   herd-worker     —
```

## herd config

View and edit HerdOS configuration.

```
herd config [key] [value]
herd config list
herd config edit

Subcommands:
  list      Show all config values
  edit      Open .herdos.yml in $EDITOR
  (none)    With one argument, print the current value. With two arguments, set the value.
```

**Example:**

```bash
$ herd config list
platform.provider: github
platform.owner: my-org
platform.repo: my-project
agent.provider: claude
agent.binary: (not set)
agent.model: (not set)
workers.max_concurrent: 3
workers.runner_label: herd-worker
workers.timeout_minutes: 30
integrator.strategy: squash
integrator.on_conflict: notify
integrator.max_conflict_resolution_attempts: 2
integrator.require_ci: true
integrator.review: true
integrator.review_max_fix_cycles: 3
integrator.ci_max_fix_cycles: 2
monitor.patrol_interval_minutes: 15
monitor.stale_threshold_minutes: 30
monitor.max_pr_age_hours: 24
monitor.auto_redispatch: true
monitor.max_redispatch_attempts: 3
monitor.notify_on_failure: true
monitor.notify_users: []
pull_requests.auto_merge: false

$ herd config workers.max_concurrent 5
Updated workers.max_concurrent: 3 → 5
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid arguments |
| 3 | GitHub API error (auth, rate limit, not found) |
| 4 | Configuration error (.herdos.yml missing or invalid) |
