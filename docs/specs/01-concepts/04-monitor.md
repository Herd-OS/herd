# Monitor

The Monitor is HerdOS's health monitoring system. It's a scheduled GitHub Action that periodically audits the state of the system and takes corrective action when things go wrong.

## Responsibilities

1. **Detect stale work** — issues labeled `herd/status:in-progress` with no corresponding active Action run or PR
2. **Detect failed workers** — Action runs that failed without updating the issue
3. **Detect stuck PRs** — PRs open for too long without merging
4. **Re-dispatch failed work** — automatically retry failed tasks (if configured)
5. **Escalate** — notify the user when problems can't be auto-resolved

## How It Works

The Monitor runs as a GitHub Action triggered by cron schedule or on-demand via `workflow_dispatch` (e.g., when a worker fails and needs immediate attention):

```yaml
on:
  schedule:
    - cron: '*/15 * * * *'  # Every 15 minutes (configurable)
  workflow_dispatch:         # Triggered by workers on failure
```

Each patrol cycle:

```
Monitor Action starts
        │
        ▼
Query: all issues with herd/* labels
        │
        ▼
No active issues? → exit early (nothing to monitor)
        │
        ▼
For each in-progress issue:
  ├── Check: is there an active Action run?
  │   └── No → Mark stale, re-dispatch or escalate
  ├── Check: has the Action run completed?
  │   ├── Success but issue still open → Check for PR
  │   └── Failure → Re-dispatch or escalate
  └── Check: has the worker been running too long?
      └── Yes (> timeout_minutes) → Cancel and re-dispatch
        │
        ▼
For each open batch PR:
  ├── Check: has it been open > max_pr_age?
  │   └── Yes → Comment once asking for review/merge (skip if already commented)
  └── Check: is CI failing?
      └── Yes → Check for open `herd/type:fix` issues in the milestone
           ├── Fix issues exist → fix cycle in progress, skip (Integrator handles it)
           └── No fix issues → Comment on batch PR with details (once per CI state change)
        │
        ▼
Done — patrol complete
```

## Patrol Frequency

The default patrol interval is 15 minutes. This balances responsiveness with GitHub API rate limits.

| Interval | Use Case |
|----------|----------|
| 5 min | Active development, many workers |
| 15 min | Default, most scenarios |
| 30 min | Low-priority background work |
| 60 min | Overnight batch jobs |

Configure in `.herdos.yml`:

```yaml
monitor:
  patrol_interval_minutes: 15
  stale_threshold_minutes: 30
  max_pr_age_hours: 24
  auto_redispatch: true
  max_redispatch_attempts: 3
  notify_on_failure: true
  notify_users: ["jfturcot"]       # GitHub usernames to @mention on escalation
```

## Escalation

When the Monitor can't resolve a problem automatically, it escalates:

1. **Comment on the issue** with diagnostic information (Action run URL, error logs, time elapsed) and `@mention` the users listed in `monitor.notify_users`
2. **Label the issue** `herd/status:failed`

The Monitor doesn't send Slack messages, emails, or other notifications directly. It uses GitHub's native notification system — if you're watching the repo, you'll get notified when issues are commented on.

## Exponential Backoff

For issues that repeatedly fail, the Monitor uses exponential backoff before re-dispatching:

- 1st failure: re-dispatch immediately
- 2nd failure: wait 15 minutes
- 3rd failure: wait 1 hour
- After `max_redispatch_attempts`: label `herd/status:failed`, don't retry

This prevents burning Actions minutes on tasks that are fundamentally broken.

The Monitor determines the failure count by querying the GitHub Actions API for all completed runs of the worker workflow filtered by the issue number. The count of runs with `conclusion: "failure"` for a given issue is the failure count. No state is stored — each patrol cycle recomputes it from the run history.

**Backoff enforcement:** Since the Monitor is stateless, it enforces wait times by comparing the timestamp of the most recent failed run against the required backoff delay. If less time has elapsed than the required wait, the Monitor skips re-dispatch for that issue. With a 15-minute patrol interval, the natural spacing handles the 15-minute backoff; for the 1-hour wait, the Monitor will skip ~3 patrol cycles before re-dispatching.
