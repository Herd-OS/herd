# Actions

HerdOS ships three reusable GitHub Actions workflow files. These are installed into your repository by `herd init` and handle worker execution, health monitoring, and integration.

## Workflow Files

All workflows live in `.github/workflows/` and are prefixed with `herd-`:

| File | Trigger | Purpose |
|------|---------|---------|
| `herd-worker.yml` | `workflow_dispatch` | Execute a task from an issue |
| `herd-monitor.yml` | `schedule` (cron) | Patrol for stuck/failed work |
| `herd-integrator.yml` | `workflow_run` | Consolidate worker branches, open batch PR, agent review |

## herd-worker.yml

The main worker workflow. Triggered by `workflow_dispatch` with the issue number as input.

```yaml
name: HerdOS Worker
on:
  workflow_dispatch:
    inputs:
      issue_number:
        description: 'Issue number to work on'
        required: true
        type: number
      batch_branch:
        description: 'Batch branch to base work on (empty = main)'
        required: false
        type: string
        default: ''
      timeout_minutes:
        description: 'Max execution time'
        required: false
        type: number
        default: 30
      runner_label:
        description: 'Runner label override (default from config)'
        required: false
        type: string
        default: 'herd-worker'

permissions:
  contents: write      # Push worker branches
  issues: write        # Update labels
  actions: write       # Trigger Monitor on failure

jobs:
  execute:
    runs-on: [self-hosted, '${{ inputs.runner_label }}']
    timeout-minutes: ${{ inputs.timeout_minutes }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          ref: ${{ inputs.batch_branch || github.event.repository.default_branch }}
          fetch-depth: 0

      - name: Execute task
        env:
          HERD_RUNNER: "true"
          # Platform credentials (for reading issues, updating labels)
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          # Agent credentials — only one needed, matching .herdos.yml provider.
          # On self-hosted runners with agent already authenticated, none needed.
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          CLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
          GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}
        run: |
          herd worker exec ${{ inputs.issue_number }}
```

**Notes:**
- The `runs-on` label defaults to `herd-worker` (from config) but can be overridden per-issue via the `runner_label` front matter field. The runner with the matching label must exist.
- `GITHUB_TOKEN` is needed for the CLI to read issues, update labels, and open PRs via the Platform interface. This keeps all platform interactions in the Go binary, making it portable to GitLab/Gitea.
- Agent credentials must be stored as repository secrets (see Secrets Management below)
- `herd worker exec` handles everything: reads the issue, labels it in-progress, creates the worker branch, invokes the agent in headless mode, and pushes. The agent commits as it works. On success, labels the issue `herd/status:done`. On failure, labels `herd/status:failed` and triggers the Monitor via `workflow_dispatch` for immediate escalation (instead of waiting for the next cron patrol).
- The YAML is intentionally thin — checkout and a single CLI command. All logic lives in the `herd` binary so it works across platforms without changing workflow files.

## herd-monitor.yml

Health patrol workflow. Runs on a cron schedule and on-demand via `workflow_dispatch` (triggered by workers on failure).

```yaml
name: HerdOS Monitor
on:
  schedule:
    - cron: '*/15 * * * *'
  workflow_dispatch: # Allow manual trigger

permissions:
  contents: read
  issues: write
  actions: write       # read run status + dispatch workers (auto_redispatch)

jobs:
  patrol:
    runs-on: [self-hosted, herd-worker]
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Patrol
        env:
          HERD_RUNNER: "true"
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          herd monitor patrol
```

The Monitor doesn't need an agent — it only makes GitHub API calls via the `herd` CLI. It can run on any runner where the `herd` binary is installed.

## herd-integrator.yml

Integration, review, and merge management workflow. Triggers when a worker completes or when a pull request review is submitted. On worker completion: consolidates the worker's branch into the batch branch, checks tier completion, and dispatches the next tier. When all tiers are done, opens the batch PR and runs an agent review on it. On PR review: if a human approves a `[herd]` batch PR and CI passes, merges it.

```yaml
name: HerdOS Integrator
on:
  workflow_run:
    workflows: ["HerdOS Worker"]
    types: [completed]
  pull_request_review:
    types: [submitted]        # Merge batch PRs after human approval

permissions:
  contents: write
  issues: write
  pull-requests: write
  actions: write

jobs:
  integrate:
    runs-on: [self-hosted, herd-worker]
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Consolidate, advance, and review
        env:
          HERD_RUNNER: "true"
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          CLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
          GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}
        run: |
          # 1. Merge completed worker branch into batch branch
          herd integrator consolidate --run-id ${{ github.event.workflow_run.id }}

          # 2. Check tier completion, dispatch next tier or open batch PR
          herd integrator advance --run-id ${{ github.event.workflow_run.id }}

          # 3. If batch PR was opened, run agent review
          herd integrator review --run-id ${{ github.event.workflow_run.id }}
```

**Notes:**
- Triggers on both successful and failed worker runs. `herd integrator consolidate` only merges successful workers; for failed runs, it updates issue state (labels `herd/status:failed` if the worker didn't) and exits 0 so `advance` always runs to check tier status.
- `herd integrator advance` is a no-op if the tier isn't complete yet — safe to call after every worker.
- `herd integrator review` is a no-op if the batch PR wasn't opened — it only runs when all tiers are done.
- Agent credentials are only needed for the review step. The consolidate and advance steps only use `GITHUB_TOKEN`.
- Same thin-YAML philosophy as the worker: all logic in the `herd` binary, portable across platforms.

## Secrets Management

### Agent authentication

Workers and the Integrator need credentials to run the configured agent. The workflow passes all supported agent secrets — the `herd` CLI uses whichever matches the configured provider in `.herdos.yml`. Only one needs to be set.

| Secret | Agent | Setup |
|--------|-------|-------|
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude Code | Run `claude setup-token`, store as secret (uses Pro/Max subscription) |
| `ANTHROPIC_API_KEY` | Claude Code | API key from Anthropic console (pay per token) |
| `OPENAI_API_KEY` | Codex | API key from OpenAI console |
| `GEMINI_API_KEY` | Gemini CLI | API key from Google AI Studio |

Cursor and OpenCode credential requirements will be documented when their integrations ship. On self-hosted runners, these agents may use their own local credential stores.

For Claude Code, the OAuth token is recommended — it uses your existing Pro or Max subscription, so worker runs don't incur additional per-token costs.

For self-hosted runners where the agent CLI is already authenticated locally, no secrets may be needed — the CLI uses its existing credential store.

### Platform authentication

| Secret | Provided By | Purpose |
|--------|-------------|---------|
| `GITHUB_TOKEN` | GitHub Actions (automatic) | GitHub API access within workflows |

`GITHUB_TOKEN` is automatically available in Actions with permissions scoped to the repository. For cross-repo operations (future), a Personal Access Token or GitHub App token would be needed.

## Input/Output Contract

### Worker Inputs
- `issue_number` (required): The GitHub Issue to execute
- `batch_branch` (optional): The batch branch to base work on (defaults to `main`)
- `timeout_minutes` (optional, default 30): Max execution time
- `runner_label` (optional): Runner label override (defaults to `workers.runner_label` from config). Set per-issue via the `runner_label` front matter field (e.g., `herd-gpu` for tasks needing GPU hardware).

### Worker Outputs (via branch/issue state)
- Worker branch created: `herd/worker/<issue>-<slug>` (if changes were needed; no branch for no-op workers)
- Issue labeled `herd/status:done` or `herd/status:failed`
- On failure: triggers Monitor via `workflow_dispatch` for immediate escalation (Monitor comments with diagnostics from Action run logs)
- Workers do NOT open PRs — the Integrator handles consolidation and the batch PR
- Workers do NOT comment on issues — the Monitor handles all notification and escalation

### Integrator Outputs
- Worker branches merged into batch branch
- Next tier dispatched (when current tier completes)
- Batch PR opened against `main` (when all tiers complete)
- Agent review posted on the batch PR
- Fix workers dispatched (if agent review finds issues)

### Monitor Outputs
- Re-dispatched workflow runs (if auto-redispatch enabled)
- Comments on stale/failed issues
- Label updates on newly unblocked issues
