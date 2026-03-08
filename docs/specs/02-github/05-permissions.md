# Permissions

Security model for HerdOS: who can trigger what, and how to keep self-hosted runners safe.

## Access Control

### Who Can Dispatch Workers

Only users with **write access** to the repository can trigger `workflow_dispatch` events. This is enforced by GitHub, not by HerdOS.

| Role | Can dispatch? | Can create issues? | Can merge PRs? |
|------|--------------|-------------------|----------------|
| Read | No | No | No |
| Triage | No | Yes (but can't label) | No |
| Write | Yes | Yes | Yes (if no branch protection) |
| Maintain | Yes | Yes | Yes |
| Admin | Yes | Yes | Yes |

For most setups, **write access** is the minimum required to use HerdOS.

### Branch Protection

Batch PRs should go through branch protection rules. Recommended settings:

```
Branch: main
├── Require pull request reviews: 1 (human reviews batch PRs — default)
│   or
├── Require pull request reviews: 0 (auto-merge enabled)
├── Require status checks: yes
│   └── Required checks: CI, lint, test
├── Require branches to be up to date: yes
└── Restrict who can push: nobody (all changes via PR)
```

**Decision point:** whether to require human review on batch PRs.

- **Review required (default):** a human reviews the consolidated batch PR before it merges. The reviewer sees the complete feature as one diff. This is the recommended starting point.
- **No review required:** fully autonomous with `auto_merge: true` in `.herdos.yml`. Workers land code without human oversight. Best for trusted codebases with strong CI.

A middle ground: require review only for batch PRs touching certain paths (e.g., security-sensitive code, configuration).

### Workflow Permissions

GitHub Actions `GITHUB_TOKEN` permissions should be scoped per workflow:

```yaml
# Worker
permissions:
  contents: write      # Push branches, create commits
  issues: write        # Update labels
  actions: write       # Trigger Monitor on failure

# Integrator (needs actions: write to dispatch next-tier and fix workers)
permissions:
  contents: write
  issues: write        # Update labels, post comments, create fix/resolution issues
  pull-requests: write # Create and manage batch PRs, post reviews
  actions: write       # Dispatch workflow_dispatch events for next tier / fix workers

# Monitor
permissions:
  contents: read       # Read branch and run state
  issues: write        # Update labels, post comments
  actions: write       # Check run status + dispatch workers (auto_redispatch)
```

These are set in the workflow YAML files installed by `herd init`.

### Runner Management Permissions

The Docker runner image registers itself with GitHub on startup using a registration token derived from the `GITHUB_TOKEN` passed as an environment variable. Creating a registration token requires the API call `POST /repos/{owner}/{repo}/actions/runners/registration-token`, which needs **admin access** to the repository — the token must come from a user with the Admin role or a personal access token with `administration: write` scope.

This is a one-time setup operation, not a runtime workflow permission.

## Secrets

### Agent Credentials

Workers and the Integrator need credentials to run the configured agent. Set the secret that matches your agent provider:

| Secret | Agent | Cost |
|--------|-------|------|
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude Code | Included in Pro/Max subscription |
| `ANTHROPIC_API_KEY` | Claude Code | Pay per token |
| `OPENAI_API_KEY` | Codex | Pay per token |
| `GEMINI_API_KEY` | Gemini CLI | Free tier available, or pay per token |

Cursor and OpenCode credential requirements will be documented when their integrations ship.

For Claude Code, the OAuth token is recommended — it shares your existing subscription with workers at no extra cost. See [actions.md](02-actions.md) for details.

On self-hosted runners where the agent is already authenticated locally, no secrets may be needed.

### Automatic Secrets

| Secret | Provided By | Purpose |
|--------|-------------|---------|
| `GITHUB_TOKEN` | GitHub Actions | GitHub API access within workflows |

## Self-Hosted Runner Security

### Private Repositories (recommended)

Self-hosted runners on private repos are reasonably safe:
- Only collaborators can trigger workflows
- Workflow files are reviewed via PR (if branch protection is on)
- `workflow_dispatch` requires write access

### Public Repositories (caution)

Self-hosted runners on public repos are risky:
- Fork PRs can trigger workflows (if configured to do so)
- Malicious PRs could execute arbitrary code on your runner
- Secrets may be exposed to untrusted code

**Recommendations for public repos:**
1. Do NOT use self-hosted runners for public repos
2. Use GitHub-hosted runners instead
3. If you must use self-hosted runners:
   - Disable `pull_request_target` triggers from forks
   - Run the runner in a container or VM with no access to host resources
   - Use a dedicated machine (not your development laptop)
   - Rotate agent credentials if compromised

### Runner Isolation

Each worker Action run gets a fresh workspace via `actions/checkout`. Workers cannot access other runners' workspaces. However, self-hosted runners share the host OS — a malicious workflow could access host files or network.

For maximum isolation:
- Run each runner in a container (Docker, Podman)
- Use ephemeral runners that are destroyed after each job

## Rate Limits

GitHub API has rate limits that affect HerdOS:

| Limit | Value | Impact |
|-------|-------|--------|
| REST API (authenticated) | 5,000 req/hour | CLI operations, Monitor patrols |
| Actions workflow triggers | 500 dispatches/10 min | Worker dispatch rate |
| Actions concurrent jobs | Varies by plan | Max simultaneous workers |

For typical HerdOS usage (5-20 issues per planning session), these limits are not a concern. Large-scale usage (100+ concurrent workers) would need rate limit awareness.

## Audit Trail

All HerdOS operations are traceable through GitHub's existing audit mechanisms:

- **Issue history:** who created, labeled, commented
- **Action logs:** full stdout/stderr of worker runs
- **PR history:** commits, reviews, merge events
- **Action run logs:** workflow inputs, outputs, and timing

No additional audit infrastructure is needed.
