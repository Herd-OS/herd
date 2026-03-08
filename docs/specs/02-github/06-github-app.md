# GitHub App

HerdOS uses a GitHub App for bot identity and authentication. The App provides a `herd-os[bot]` account that appears as co-author on worker commits, and can be used for API authentication with fine-grained permissions.

## Why a GitHub App

- **Bot identity**: commits show the HerdOS logo via the `herd-os[bot]` account
- **Fine-grained permissions**: scoped to exactly what HerdOS needs, no personal token required
- **No seat cost**: GitHub Apps don't consume a seat in your organization
- **Installation-level auth**: tokens are scoped to the repos where the App is installed

## Commit Attribution

Worker commits use the dispatching user as author and HerdOS as co-author:

```
Add CSS custom properties for theming (#42)

Co-authored-by: herd-os[bot] <ID+herd-os[bot]@users.noreply.github.com>
```

The numeric ID is the App's user ID, discoverable via `gh api /users/herd-os[bot]` once the App is created. This behavior can be disabled with `pull_requests.co_author: false` in `.herdos.yml`. GitHub renders the App's avatar in the commit's author list — this is the same mechanism used by Dependabot, Renovate, and other GitHub Apps.

The dispatching user's git identity (name and email) is captured at dispatch time from `git config` and passed to the worker workflow as inputs. The worker sets `GIT_AUTHOR_NAME` and `GIT_AUTHOR_EMAIL` accordingly, and instructs the agent to include the `Co-authored-by` trailer in every commit message.

## App Permissions

The GitHub App needs these permissions:

| Permission | Access | Purpose |
|-----------|--------|---------|
| Contents | Read & Write | Push branches, create commits |
| Issues | Read & Write | Create/update issues, manage labels |
| Pull requests | Read & Write | Create batch PRs, post reviews |
| Actions | Read & Write | Trigger workflow_dispatch events |
| Metadata | Read | Required by GitHub for all Apps |

These match the per-workflow `GITHUB_TOKEN` permissions in [permissions.md](05-permissions.md) but are configured once on the App instead of per-workflow.

## Authentication Flow

Two authentication modes, depending on setup:

### Mode 1: Personal Token (simple, default)

Users authenticate with their own `GITHUB_TOKEN` or `gh auth token`. The App is only used for commit attribution (co-author trailer). This is the default for `herd init`.

### Mode 2: App Installation Token (recommended for teams)

The GitHub App generates installation tokens for API calls. This removes the dependency on personal tokens and gives the organization control over HerdOS's permissions.

```
1. Org admin installs the HerdOS GitHub App on the repo
2. herd init detects the App installation
3. Workflows use the App's installation token instead of GITHUB_TOKEN
4. Commits are co-authored by herd-os[bot]
```

## Branch Protection

The GitHub App must be able to merge PRs after human approval (or without approval when `auto_merge: true`). This requires the App to be added to the branch protection rule's **bypass list** in the repository settings.

This also solves a key workflow constraint: since the Integrator (via the App) opens the batch PR, the human user is not the PR author and can approve their own work. If a human opened the PR instead, branch protection rules requiring approval would block them from approving it.

## Setup

### Creating the App

The HerdOS project maintains the official `herd-os` GitHub App. Users install it on their repos — they don't create their own App.

For self-hosted or enterprise deployments, organizations can create their own GitHub App with the permissions listed above.

### Installation

```bash
# herd init detects if the App is installed and configures accordingly
herd init
```

If the App is not installed, `herd init` prints a link to install it and falls back to personal token authentication.

## Relationship to Other Auth

| Auth Method | Used For | When |
|------------|----------|------|
| Personal token (`GITHUB_TOKEN` / `gh auth token`) | CLI operations, dispatch | Always (user runs CLI locally) |
| App installation token | Workflow API calls | When App is installed (optional) |
| Agent credentials (`CLAUDE_CODE_OAUTH_TOKEN`, etc.) | Running the AI agent | Always (workers need agent access) |

The GitHub App does not replace agent credentials — those are separate. The App handles GitHub API auth and bot identity only.
