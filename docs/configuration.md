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

integrator:
  strategy: "squash"             # squash | rebase | merge
  on_conflict: "notify"          # notify | dispatch-resolver
  max_conflict_resolution_attempts: 2
  require_ci: true
  review: true                   # agent reviews batch PRs before merge
  review_max_fix_cycles: 3       # max fix-and-re-review cycles
  review_strictness: "standard"  # standard | strict | lenient
  ci_max_fix_cycles: 2           # max CI-failure fix cycles (0 = notify-only)

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
