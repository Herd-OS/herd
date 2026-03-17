# GitHub Integration Design

HerdOS treats GitHub as its database, event bus, and execution platform. All state lives in Issues, PRs, and Actions -- no local database, no custom server. This document consolidates the design for how HerdOS uses GitHub's primitives.

## 1. Issues as Work Items

Every task a worker executes is defined by a GitHub Issue. Issues carry structured metadata in YAML front matter and human-readable sections that become the agent's prompt.

### Label Taxonomy

All labels use the `herd/` prefix to avoid collisions. Created automatically by `herd init`.

**Status labels** (mutually exclusive -- an issue has exactly one at any time):

| Label | Description |
|-------|-------------|
| `herd/status:ready` | Ready for a worker to pick up |
| `herd/status:in-progress` | A worker is actively executing |
| `herd/status:done` | Worker completed, branch ready for consolidation |
| `herd/status:failed` | Worker failed -- needs re-dispatch or manual fix |
| `herd/status:blocked` | Waiting for a dependency to complete |

**Type labels:**

| Label | Description |
|-------|-------------|
| `herd/type:feature` | New functionality (set by Planner) |
| `herd/type:bugfix` | Bug fix (set by Planner) |
| `herd/type:fix` | Auto-generated fix from agent review or conflict resolution |
| `herd/type:manual` | Requires human action -- not dispatched to workers |

### Issue Body Format

Issues use YAML front matter followed by structured markdown sections. The front matter carries machine-readable metadata (schema version, batch number, dependency list, file scope, complexity estimate, optional runner label override). Integrator-created issues add fields for fix cycle tracking, batch PR reference, and conflict resolution context.

The body sections serve distinct purposes:

- **Task** -- what to build (concise summary)
- **Implementation Details** -- how to build it, with exact file paths, function signatures, and references to existing code. Makes the issue self-contained so the worker never needs to read other issues.
- **Conventions** -- project-specific patterns discovered by the Planner during codebase exploration
- **Context from Dependencies** -- information from upstream issues inlined by the Planner, so the worker has full context without traversing the dependency graph
- **Acceptance Criteria** -- concrete, verifiable checks
- **Files to Modify** -- explicit list of files to create or edit

Workers parse the front matter for metadata and pass the human-readable sections directly to the agent as the prompt. Manual issues (created by users) are also supported -- YAML front matter is optional, and without it the full body becomes the prompt.

### Lifecycle State Machine

```
                    +-------------------------+
                    |                         |
                    v                         |
+----------+   +----------+   +-----------------+   +--------+
| blocked  |-->|  ready   |-->|  in-progress    |-->|  done  |
+----------+   +----------+   +-----------------+   +--------+
                    ^                |
                    |                |
                    |                v
                    |          +----------+
                    +----------| failed   |
                   (re-dispatch)+----------+
```

### Transition Table

| From | To | Trigger |
|------|----|---------|
| (created) | `ready` | Issue created with no unresolved dependencies |
| (created) | `blocked` | Issue created with unmet `depends_on` |
| `blocked` | `ready` | Integrator advances tier; Monitor as safety net |
| `ready` | `in-progress` | Worker dispatched |
| `in-progress` | `done` | Worker completes, pushes worker branch |
| `in-progress` | `failed` | Worker fails, times out, or cannot complete |
| `failed` | `ready` | Re-dispatched by Monitor or user |
| `ready`/`blocked`/`in-progress`/`done` | `failed` | Batch cancelled |
| `done` | (closed) | Batch PR merged |

## 2. Actions Workflows

HerdOS ships three workflow files, installed by `herd init` into `.github/workflows/`. All follow a thin-YAML philosophy: the workflow does checkout and calls a single `herd` CLI command. All logic lives in the Go binary, making it portable across platforms.

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `herd-worker.yml` | `workflow_dispatch` | Executes a task from an issue. Receives issue number, batch branch, timeout, and runner label as inputs. Checks out the batch branch (or main), runs `herd worker exec`, which handles reading the issue, labeling, branching, invoking the agent, pushing, and updating status. |
| `herd-monitor.yml` | `schedule` (cron, every 15 min) + `workflow_dispatch` | Health patrol. Runs `herd monitor patrol` to detect stuck/failed work, re-dispatch if configured, comment on issues, and unblock stragglers. Does not need an agent -- only makes API calls. |
| `herd-integrator.yml` | `workflow_run` (worker completed) + `check_suite` (CI completed) + `issues` (closed) + `pull_request_review` (submitted) + `pull_request` (closed) | Consolidates worker branches into the batch branch, checks tier completion, dispatches next tier, opens the batch PR when all tiers are done, detects CI failures and dispatches fix workers, runs agent review, and merges after human approval. |

### Secrets Management

Agent credentials are stored as repository secrets. The workflows pass all supported agent secrets; the CLI uses whichever matches the configured provider. Only one needs to be set. For Claude Code, the OAuth token is recommended as it shares an existing Pro/Max subscription at no extra per-token cost. On self-hosted runners where the agent is already authenticated locally, no agent secrets may be needed.

`GITHUB_TOKEN` is provided automatically by GitHub Actions with permissions scoped to the repository.

## 3. Event Architecture

HerdOS uses GitHub's event system instead of polling. Events chain together to drive the full lifecycle from dispatch through merge.

```
USER ACTION           GITHUB EVENT              HERDOS RESPONSE
-----------           ------------              ---------------

herd dispatch #42  -> workflow_dispatch       -> Worker starts
                                                      |
Worker completes   -> workflow_run.completed  -> Integrator consolidates
                                                      |
Tier complete      -> (integrator logic)      -> Dispatch next tier
                                                      |
Manual task closed -> issues.closed           -> Integrator advances + reviews
                                                      |
All tiers done     -> (integrator logic)      -> Batch PR opened
                                                      |
CI fails on batch  -> check_suite.completed   -> Integrator dispatches fix worker
                                                      |
Agent review       -> (integrator logic)      -> Approve or fix cycle
                                                      |
Human approves PR  -> pull_request_review     -> Integrator merges (if CI passes)
                                                      |
Batch PR merged    -> pull_request.closed     -> Issues closed, cleanup
                                                      |
Cron fires         -> schedule                -> Monitor patrols
Worker fails       -> workflow_dispatch       -> Monitor patrols (immediate)
Comment posted     -> issue_comment.created   -> handle-comment parses and executes
```

### Event Types

- **workflow_dispatch** -- primary dispatch mechanism. Only users with write access can trigger it (enforced by GitHub). The `ref` parameter points to the branch containing the workflow YAML, not the branch the worker checks out.
- **workflow_run** -- triggers the Integrator when a worker completes (success or failure).
- **check_suite** -- triggers the Integrator when CI completes on a batch branch. If CI failed, the Integrator re-runs checks once (transient failure filter), then dispatches fix workers up to `ci_max_fix_cycles`. Note: `check_suite` events may not fire for external CI providers (e.g., Cloudflare Pages). The Monitor patrol serves as a fallback, checking CI status on open batch PRs every 15 minutes. CI status detection checks both GitHub's commit status API and the check runs API to support external providers.
- **issues** -- triggers the Integrator when an issue is closed. Used for manual task completion — the Integrator advances the tier and runs agent review if all tiers are done.
- **pull_request_review** -- triggers the Integrator to merge batch PRs after human approval + CI pass.
- **pull_request** -- triggers cleanup when a batch PR is merged (branch deletion, milestone closure).
- **schedule** -- triggers Monitor patrol. GitHub may delay or skip scheduled runs under load; the Monitor is stateless and catches up on the next patrol.
- **issue_comment** -- triggers the Integrator's `handle-comment` job when a comment starting with `/herd ` is posted. The workflow validates the commenter's permissions, parses the command, and executes it. This is the entry point for both user-initiated commands and Monitor-posted commands (retry, fix-ci).

All workflows require the `HERD_ENABLED` repository variable to be set to `true`. This prevents workflow storms when `herd init` pushes workflow files before runners are configured. All `${{ }}` expressions in `run:` blocks are passed through environment variables to prevent shell injection.

The checkout action in all workflows uses `HERD_GITHUB_TOKEN` (falling back to `GITHUB_TOKEN`) to configure git credentials for pushes. This is required for workers that create workflow files, which need the `workflows` permission.

Issues auto-close via GitHub's native "Closes #N" references in the batch PR description. Dependency unblocking is handled by the Integrator's tier advancement logic, with the Monitor as a safety net.

### Infinite Loop Prevention

Several automated feedback loops exist. Each has explicit termination:

| Loop | Terminates Because | Config Cap |
|------|--------------------|------------|
| Tier advancement | DAG is finite | -- |
| Agent review -> fix -> re-review | `review_max_fix_cycles` | Default: 3 |
| Monitor re-dispatch | `max_redispatch_attempts` | Default: 3 |
| Conflict resolution | `max_conflict_resolution_attempts` | Default: 2, then notify |
| CI failure after consolidation | `ci_max_fix_cycles` | Default: 2 |

Additional safeguards: actions performed with `GITHUB_TOKEN` do not trigger further workflow runs (GitHub's built-in loop prevention), label filters restrict reactions to `herd/`-prefixed labels, guard clauses check for bot actors, and all state changes are idempotent.

### Concurrency and Race Conditions

Multiple workers can complete near-simultaneously, triggering concurrent Integrator runs.

**Concurrent consolidation:** If two merges into the batch branch race, the second push is rejected (non-fast-forward). The consolidate command handles this by pulling, retrying, and pushing again.

**Double-dispatch prevention:** The `advance` command uses issue labels as an atomic guard -- it sets `in-progress` before dispatching, and skips any issue already `in-progress`. Label transitions via the GitHub API are atomic, so only one advance call can transition a given issue.

**General rule:** All operations are idempotent. Labeling an already-labeled issue, dispatching an already-in-progress issue, and merging an already-merged branch are all detected and skipped.

## 4. Runners

Workers execute on GitHub Actions self-hosted runners. HerdOS provides a Docker image as the primary deployment method.

### Runner Types

| Type | Cost | Setup | Best For |
|------|------|-------|----------|
| Docker (your machine) | Free | `docker compose up` | Solo developers |
| Docker (cloud VM) | VM cost (~$100/mo for 3 workers) | Same image, remote host | Teams, always-on |
| GitHub-hosted | Actions minutes (~$0.008/min) | None | Quick start |
| Manual bare-metal | Free | User manages setup | Advanced users |

Self-hosted runners do not consume GitHub Actions minutes.

### Docker Runner Concept

The base image is intentionally minimal: GitHub Actions runner binary (configured with `--ephemeral`), `herd` CLI, `git`, and `gh` CLI. No agent, no programming languages, no build tools. Users extend it with their project's toolchain.

**Ephemeral lifecycle:**

1. **Boot** -- creates a registration token via GitHub API, registers with `--ephemeral`
2. **Idle** -- long-polls GitHub for work
3. **Job** -- executes one job, then exits
4. **Restart** -- Docker `restart: always` brings a clean container. No leftover files or stale state.
5. **Shutdown** -- traps SIGTERM/SIGINT, deregisters the runner from GitHub before exiting. No ghost runners.

Every job runs in a clean environment at the cost of a few seconds overhead for container restart and re-registration. Multiple replicas run as independent ephemeral runners (3 replicas = 3 concurrent workers).

### Runner Labels

Labels route jobs to runners with specific capabilities.

| Label | Purpose |
|-------|---------|
| `herd-worker` | General-purpose worker (default) |
| `herd-gpu` | Runner with GPU for ML/AI tasks |
| `herd-heavy` | Runner with more resources |
| `self-hosted` | Applied automatically by GitHub |

The default label is configured in `.herdos.yml`. Individual issues can override it via the `runner_label` front matter field, set by the Planner when a task requires specific hardware.

### Resource Considerations

Each worker needs approximately 2 GB RAM for the agent. CPU is less of a constraint (workers are mostly I/O-bound waiting for API responses). Disk usage is per-job since ephemeral runners check out the full repo each time.

## 5. Permissions and Security

### Access Control

GitHub enforces the security boundary: only users with write access can trigger `workflow_dispatch`.

| Role | Can Dispatch | Can Create Issues | Can Merge PRs |
|------|-------------|-------------------|---------------|
| Read | No | No | No |
| Triage | No | Yes (cannot label) | No |
| Write | Yes | Yes | Yes (if no branch protection) |
| Maintain | Yes | Yes | Yes |
| Admin | Yes | Yes | Yes |

Write access is the minimum required to use HerdOS.

### Branch Protection Recommendations

Batch PRs should go through branch protection. Two modes:

- **Review required (default):** A human reviews the consolidated batch PR. The reviewer sees the complete feature as one diff. Recommended starting point.
- **No review required:** Fully autonomous with `auto_merge: true`. Best for trusted codebases with strong CI.

A middle ground: require review only for PRs touching specific paths (security-sensitive code, configuration).

### Workflow Permissions

Each workflow's `GITHUB_TOKEN` is scoped to the minimum required:

- **Worker:** contents write (push branches), issues write (update labels), actions write (trigger Monitor on failure)
- **Integrator:** contents write, issues write, pull-requests write (create/manage batch PRs), actions write (dispatch next tier and fix workers)
- **Monitor:** contents read, issues write, actions write (check run status, dispatch workers)

Runner registration requires admin access to the repository (for the registration token API call). This is a one-time setup operation.

### Runner Security

**Private repos (recommended):** Self-hosted runners are reasonably safe. Only collaborators can trigger workflows, workflow files are reviewed via PR, and `workflow_dispatch` requires write access.

**Public repos (caution):** Self-hosted runners are a security risk. Fork PRs can trigger workflows, potentially executing arbitrary code on your runner and exposing secrets. Recommendations: use GitHub-hosted runners instead, disable fork PR triggers, run in containers with no host access, use a dedicated machine, and rotate credentials if compromised.

For maximum isolation, run each runner in a container with ephemeral mode so it is destroyed after each job.

### Comment Command Permissions

Comment commands (`/herd`) are restricted to users with `OWNER`, `MEMBER`, or `COLLABORATOR` association on the repository, plus bot accounts. This is enforced by the CLI, not by GitHub's native permissions. The `author_association` field from the webhook payload is used for validation.

### Rate Limits and Audit

GitHub API limits (5,000 req/hour REST, 500 dispatches/10 min) are not a concern for typical usage (5-20 issues per session). All operations are traceable through GitHub's existing audit mechanisms: issue history, action logs, PR history, and workflow run logs. No additional audit infrastructure is needed.

## 6. GitHub App

### Why a GitHub App

1. **Bot identity** -- commits show the HerdOS logo via the `herd-os[bot]` account, giving clear visual attribution in the commit history
2. **Fine-grained permissions** -- scoped to exactly what HerdOS needs, with no personal token required for workflow operations
3. **No seat cost** -- GitHub Apps do not consume a seat in your organization
4. **Installation-level auth** -- tokens are scoped to the repos where the App is installed, limiting blast radius

### Commit Co-Authorship

Worker commits use the dispatching user as author and HerdOS as co-author via the `Co-authored-by` git trailer. The user's git identity (name and email) is captured at dispatch time from `git config` and passed to the worker. GitHub renders the App's avatar alongside the user's in the commit history -- the same mechanism used by Dependabot and Renovate. This can be disabled with `pull_requests.co_author: false`.

### Two Authentication Modes

**Mode 1: Personal Token (default).** Users authenticate with their own token. The App is only used for commit attribution. This is the default for `herd init`.

**Mode 2: App Installation Token (recommended for teams).** The GitHub App generates installation tokens for API calls, removing the dependency on personal tokens and giving the organization control over permissions. `herd init` detects whether the App is installed and configures accordingly; if not installed, it prints an installation link and falls back to personal token auth.

The App also solves a branch protection constraint: since the Integrator (via the App) opens the batch PR, the human user is not the PR author and can approve their own work. The App must be added to the branch protection bypass list to merge PRs.

### Future Plans

The official `herd-os` GitHub App is maintained by the HerdOS project. Users install it rather than creating their own. For enterprise or self-hosted deployments, organizations can create a custom App with the same permissions. The App handles GitHub API auth and bot identity only -- it does not replace agent credentials, which remain separate.
