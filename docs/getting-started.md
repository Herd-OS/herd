# Getting Started

## Initialize a Repository

Navigate to a git repository with a GitHub remote and run:

```bash
herd init
```

This will:

1. **Create `.herdos.yml`** — the configuration file with sensible defaults, auto-detecting your GitHub owner and repo from the git remote
2. **Create `.herd/` directory** — with empty role instruction files (`planner.md`, `worker.md`, `integrator.md`) for customizing agent behavior per role
3. **Create GitHub labels** — the `herd/*` label taxonomy used to track issue status and type
4. **Install workflow files** — GitHub Actions workflows for workers, integrator, and monitor in `.github/workflows/`
5. **Create runner files** — `Dockerfile.herd_runner_base`, `Dockerfile.herd_runner`, `entrypoint.herd.sh`, `docker-compose.herd.yml`, and `.env.herd.example` for self-hosted runner setup
6. **Commit and open a PR** — creates a `herd/init-<version>` branch, commits all generated files, pushes, and opens a PR. Review and merge the PR to apply the changes.

### Enable Workflows

Workflows are installed but inactive until you enable them. After setting up runners (see [Runner Setup](runners.md)), set the `HERD_ENABLED` repository variable:

```bash
gh variable set HERD_ENABLED --body true --repo <owner>/<repo>
```

This prevents a workflow storm if runners are online before the system is fully configured.

### Skipping Steps

```bash
herd init --skip-labels       # Don't create GitHub labels
herd init --skip-workflows    # Don't install workflow files
```

## Configuration

View all configuration:

```bash
herd config list
```

Get a specific value:

```bash
herd config get workers.max_concurrent
```

Set a value:

```bash
herd config set workers.max_concurrent 5
herd config set platform.owner my-org
herd config set pull_requests.auto_merge true
```

Open the config file in your editor:

```bash
herd config edit
```

See [configuration.md](configuration.md) for all available options.

## Role Instruction Files

Customize how each HerdOS role behaves in your project by editing files in `.herd/`:

| File | Purpose |
|------|---------|
| `.herd/planner.md` | Extra instructions for the Planner (e.g., "always include testing requirements") |
| `.herd/worker.md` | Extra instructions for Workers (e.g., "use table-driven tests", "follow project coding standards") |
| `.herd/integrator.md` | Extra instructions for the Integrator's agent review (e.g., "be strict about error handling") |

These files are created empty by `herd init`. Add your project-specific instructions and commit them — they're shared across your team.

## Planning Work

Decompose a feature into tasks with an interactive agent session:

```bash
herd plan "Add user authentication"
```

### Pre-Flight Git Checks

Before launching the planning session, `herd plan` runs pre-flight git checks to ensure your local repository is in a clean, up-to-date state:

1. **Fetches from origin** — always runs; a fetch failure is non-fatal.
2. **Branch check** — if you're on a branch other than the default branch, prompts: *"You're on branch 'foo'. Switch to '\<default\>' and pull latest? [Y/n]"*
3. **Behind remote check** — if your local branch is behind the remote, prompts: *"Local is N commits behind origin/\<branch\>. Pull latest? [Y/n]"*
4. **Dirty working tree warning** — if you have uncommitted changes, shows: *"Working tree has uncommitted changes. The planner will see your local state."*

If you accept a prompt to switch branches or pull but have a dirty working tree, the command exits with an error asking you to stash or commit first.

To bypass all pre-flight checks:

```bash
herd plan --skip-preflight "Add user authentication"
```

The planner automatically reads the repository structure, README, tech stack manifest, recent git history, and active batches to give the agent context about your project. The agent asks clarifying questions, then produces a decomposition with tasks, dependencies, and tier assignments. You can confirm, reject, or edit the plan in `$EDITOR`. When you exit the session, herd automatically creates issues and dispatches Tier 0 workers — no extra commands needed.

To plan without auto-dispatching Tier 0:

```bash
herd plan --no-dispatch "Add user authentication"
```

Preview what would be created:

```bash
herd plan --dry-run "Add user authentication"
```

If issue creation fails after planning (e.g., duplicate milestone), the plan file is preserved and the exact `--from-file` command is printed. Use it to retry without re-running the agent session:

```bash
herd plan --from-file .herd/state/1234567890.json
```

## Dispatching Workers

After planning, Tier 0 tasks are dispatched automatically. To manually dispatch:

```bash
# Dispatch a single issue
herd dispatch 42

# Dispatch all ready issues in a batch
herd dispatch --batch 5

# Dispatch across all batches
herd dispatch --all

# Override concurrency limit
herd dispatch --batch 5 --ignore-limit
```

## Monitoring Progress

```bash
# Overview of all batches and active workers
herd status

# Detailed view of a specific batch
herd status --batch 5

# Auto-refreshing dashboard
herd status --watch

# Machine-readable output
herd status --json

# Runner status
herd status --runners
```

## Managing Batches

```bash
# List active batches
herd batch list

# Show detailed issue status for a batch
herd batch show 5

# Cancel a batch (stops workers, labels issues as failed, closes milestone)
herd batch cancel 5
```

## What Happens After Dispatch

Once workers are dispatched, the system runs autonomously via GitHub Actions:

1. **Workers execute** — Each worker reads its assigned issue, runs your agent in headless mode on a self-hosted runner, and pushes changes to a worker branch (`herd/worker/<number>-<slug>`). If no changes are needed, the worker marks the issue as done without pushing.

2. **Integrator consolidates** — When a worker completes, the Integrator merges its branch into the batch branch (`herd/batch/<number>-<slug>`) and deletes the worker branch. If a merge conflict is detected, the behavior depends on `integrator.on_conflict`: with `dispatch-resolver` (default), a conflict-resolution worker is automatically dispatched; with `notify`, a comment is posted for manual resolution instead.

3. **Integrator advances** — After consolidation, the Integrator checks if the current tier is complete. If so, it unblocks and dispatches the next tier. When all tiers are done, it rebases the batch branch onto `main` and opens a single batch PR.

4. **Agent review** — If `integrator.review` is enabled, an agent reviews the batch PR diff against all acceptance criteria. If issues are found, the Integrator creates fix issues and dispatches fix workers. This cycle repeats up to `review_max_fix_cycles` times.

5. **CI failure detection** — When CI completes on the batch branch, a `check_suite` event triggers the Integrator. If CI failed, it re-runs checks once (transient failure filter), then dispatches fix workers up to `ci_max_fix_cycles`.

6. **Monitor patrols** — A cron-triggered Action detects stale workers (in-progress with no active run), failed issues (auto-redispatches with exponential backoff), CI failures on batch PRs, and stuck PRs (open longer than `max_pr_age_hours`). It escalates to `notify_users` when retries are exhausted.

7. **You review and merge** — The batch PR arrives with a summary table of all tasks and their tiers. If `pull_requests.auto_merge` is true and the agent review passed, it merges automatically.

### Role Instruction Files

Customize agent behavior for each role by editing files in `.herd/`:

- **`.herd/worker.md`** — Appended to the worker's system prompt (e.g., "use table-driven tests", "follow project coding standards")
- **`.herd/integrator.md`** — Appended to the integrator's review prompt (e.g., "be strict about error handling")

These are loaded automatically when the respective role runs.

### Failure Handling

- **Worker failure** — The issue is labeled `herd/status:failed` and the Monitor is triggered immediately for fast escalation
- **Tier stuck** — If any issue in a tier fails, the tier is stuck and the next tier won't be dispatched until the failure is resolved (manually or by the Monitor's auto-redispatch)
- **Merge conflict** — When `on_conflict: dispatch-resolver`, the Integrator creates a conflict-resolution issue and dispatches a worker to resolve it. The number of attempts is limited by `max_conflict_resolution_attempts`. When `on_conflict: notify`, a comment is posted on the issue for manual resolution.
- **Review safety valve** — If a single agent review finds more than 10 issues, fix workers are not created (to prevent runaway invocations). The PR is flagged for manual intervention.

### Manual Tasks

Some tasks in a batch may be labeled `herd/type:manual` — these require human action (e.g., infrastructure setup, external service configuration). They appear in `herd status` with a 👤 icon. To complete a manual task, close the issue on GitHub. The Integrator detects the close event and advances the tier if all tasks are now complete.

### Re-triggering Review

When a human submits a review on the batch PR, the Integrator's `re-review` job runs automatically, invoking the agent for a fresh review against the current diff. This allows you to push manual fixes and have the agent re-evaluate.

## Comment Commands

You can interact with HerdOS by posting `/herd` commands as comments on issues and PRs. Commands are available to repository owners, members, and collaborators.

When you post a command, HerdOS reacts with 👀 to acknowledge it, executes the command, and posts the result as a reply.

### Available Commands

| Command | Where | Description |
|---------|-------|-------------|
| `/herd fix-ci` | Batch PR | Checks CI status and dispatches fix workers if failing |
| `/herd fix-ci <hint>` | Batch PR | Same as above, with context passed to the fix worker |
| `/herd retry <issue-number>` | Any issue/PR | Re-dispatches a failed issue |
| `/herd review` | Batch PR | Triggers agent review of the batch PR |
| `/herd review <focus area>` | Batch PR | Same as above, with extra review instructions |
| `/herd fix <description>` | Batch PR | Creates a fix issue and dispatches a worker. The full PR comment thread is included in the fix issue so the worker has context of prior iterations. The reviewer automatically recognizes these fixes and will not flag them as acceptance criteria violations. |

### Examples

```
/herd fix-ci the Node version file is missing from the Docker image
/herd retry 42
/herd review focus on error handling in the auth module
/herd fix add missing error check in auth.go line 42
```

Quotes around the prompt text are optional. You can paste error logs, JSON snippets, or any text directly after the command — everything after the command name (including subsequent lines) is treated as the prompt. The quoted format (`/herd fix "description"`) is also still supported.

The Monitor also uses comment commands internally — it posts `/herd retry <N>` and `/herd fix-ci` comments instead of dispatching directly, keeping all command execution flowing through a single handler.
