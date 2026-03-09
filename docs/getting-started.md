# Getting Started

## Initialize a Repository

Navigate to a git repository with a GitHub remote and run:

```bash
herd init
```

This will:

1. **Create `.herdos.yml`** — the configuration file with sensible defaults, auto-detecting your GitHub owner and repo from the git remote
2. **Create `.herd/` directory** — with empty role instruction files (`planner.md`, `worker.md`, `integrator.md`, `monitor.md`) for customizing agent behavior per role
3. **Create GitHub labels** — the `herd/*` label taxonomy used to track issue status and type
4. **Install workflow files** — GitHub Actions workflows for workers, integrator, and monitor in `.github/workflows/`

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
| `.herd/integrator.md` | Extra instructions for the Integrator |
| `.herd/monitor.md` | Extra instructions for the Monitor |

These files are created empty by `herd init`. Add your project-specific instructions and commit them — they're shared across your team.

## Planning Work

Decompose a feature into tasks with an interactive agent session:

```bash
herd plan "Add user authentication"
```

The agent asks clarifying questions, then produces a decomposition with tasks, dependencies, and tier assignments. You can confirm, reject, or edit the plan in `$EDITOR`.

To plan without auto-dispatching Tier 0:

```bash
herd plan --no-dispatch "Add user authentication"
```

Preview what would be created:

```bash
herd plan --dry-run "Add user authentication"
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

2. **Integrator consolidates** — When a worker completes, the Integrator merges its branch into the batch branch (`herd/batch/<number>-<slug>`) and deletes the worker branch. If a merge conflict is detected, the behavior depends on `integrator.on_conflict`: with `notify` (default), a comment is posted for manual resolution; with `dispatch-resolver`, a conflict-resolution worker is automatically dispatched.

3. **Integrator advances** — After consolidation, the Integrator checks if the current tier is complete. If so, it unblocks and dispatches the next tier. When all tiers are done, it rebases the batch branch onto `main` and opens a single batch PR.

4. **Agent review** — If `integrator.review` is enabled, an agent reviews the batch PR diff against all acceptance criteria. If issues are found, the Integrator creates fix issues and dispatches fix workers. This cycle repeats up to `review_max_fix_cycles` times.

5. **Monitor patrols** — A cron-triggered Action detects stale workers (in-progress with no active run), failed issues (auto-redispatches with exponential backoff), and stuck PRs (open longer than `max_pr_age_hours`). It escalates to `notify_users` when retries are exhausted.

6. **You review and merge** — The batch PR arrives with a summary table of all tasks and their tiers. If `pull_requests.auto_merge` is true and the agent review passed, it merges automatically.

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

### Re-triggering Review

When a human submits a review on the batch PR, the Integrator's `re-review` job runs automatically, invoking the agent for a fresh review against the current diff. This allows you to push manual fixes and have the agent re-evaluate.
