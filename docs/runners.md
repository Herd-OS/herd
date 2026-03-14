# Runner Setup

HerdOS workers run as GitHub Actions on self-hosted runners. Self-hosted runners are required because workers need an AI agent (Claude Code) installed, and because GitHub-hosted runners don't support `workflow_dispatch` chaining with custom tools.

`herd init` generates all the files you need: `Dockerfile.runner`, `entrypoint.sh`, `docker-compose.herd.yml`, and `.env.example`.

## Quick Setup

```bash
herd init                    # generates runner files + config
cp .env.example .env         # copy the env template
# fill in .env (see sections below)
docker compose -f docker-compose.herd.yml up -d
```

That's it. Three runners start by default (configurable in `docker-compose.herd.yml`).

## 1. GitHub Token

You need a Personal Access Token (PAT) for runner registration and API operations.

### Fine-grained token (recommended)

1. Go to **Settings → Developer settings → Fine-grained tokens → Generate new token**
   (https://github.com/settings/tokens?type=beta)
2. Set a name (e.g., `herd-runner`) and expiration
3. Under **Repository access**, select **Only select repositories** → pick your HerdOS repos
4. Under **Permissions**, enable:
   - **Actions**: Read and write
   - **Administration**: Read and write (runner self-registration)
   - **Contents**: Read and write
   - **Issues**: Read and write
   - **Pull requests**: Read and write
   - **Metadata**: Read (auto-selected)
5. Generate and copy the token

### Classic token (simpler)

1. Go to **Settings → Developer settings → Tokens (classic) → Generate new token**
   (https://github.com/settings/tokens)
2. Select the `repo` scope
3. Generate and copy the token

### Where to use it

Add the token in two places:

| Location | Variable | Purpose |
|----------|----------|---------|
| `.env` file | `GITHUB_TOKEN=ghp_...` | Docker runner registration |
| Org/repo secrets | `HERD_GITHUB_TOKEN` | Workflow dispatch between roles |

The same token works for both. `HERD_GITHUB_TOKEN` is needed because GitHub's automatic `GITHUB_TOKEN` cannot trigger `workflow_dispatch` events (anti-recursion protection). Without it, HerdOS runs but Monitor cannot redispatch failed workers and the Integrator cannot dispatch next-tier workers.

## 2. Agent Authentication

Choose one:

### Option 1: OAuth token (recommended)

Uses your Claude Pro/Max subscription — no per-token cost.

```bash
claude setup-token
# Copy the output token
```

Add to `.env`:
```
CLAUDE_CODE_OAUTH_TOKEN=your-token-here
```

Also add as an org or repo secret named `CLAUDE_CODE_OAUTH_TOKEN`.

### Option 2: API key

Pay-per-token via https://console.anthropic.com/.

Add to `.env`:
```
ANTHROPIC_API_KEY=sk-ant-...
```

Also add as an org or repo secret named `ANTHROPIC_API_KEY`.

> `.env` is auto-gitignored by `herd init` — credentials won't be committed.

> `.env` is for Docker runners (container registration and agent auth). Org/repo secrets are for GitHub Actions workflows. If you use Docker runners, you need both.

## 3. GitHub Actions Settings

### Organization level

Go to **https://github.com/organizations/{org}/settings/actions**.

- [x] **Actions permissions**: "Allow all actions" (or allowlist `actions/checkout`, `softprops/action-gh-release`, `golangci/golangci-lint-action`)
- [x] **Workflow permissions**: Select **"Read and write permissions"**
- [x] **"Allow GitHub Actions to create and approve pull requests"**: **Check this box** — the Integrator creates batch PRs and the agent review may approve them
- [x] **Self-hosted runners**: "All repositories" or select your HerdOS repos

### Repository level

Go to **https://github.com/{org}/{repo}/settings/actions**.

Verify settings are inherited from org and not overridden to be more restrictive. Organization settings act as a ceiling — if disabled at org level, it cannot be enabled at repo level.

> **Most common issue**: The "Allow GitHub Actions to create and approve pull requests" checkbox is off by default. If the Integrator gets 403 errors when creating PRs, this is why.

## 4. Secrets Summary

Configure at **org level** (recommended for multi-repo) or **repo level**:

| Secret/Variable | Type | Required | Purpose |
|----------------|------|----------|---------|
| `HERD_GITHUB_TOKEN` | Secret | Yes | PAT for workflow dispatch, releases, cross-repo ops |
| `CLAUDE_CODE_OAUTH_TOKEN` | Secret | One of these | Agent auth — Pro/Max subscription |
| `ANTHROPIC_API_KEY` | Secret | One of these | Agent auth — pay-per-token |
| `HERD_RUNNER_LABEL` | Variable | No | Override default runner label (default: `herd-worker`) |

**Org secrets**: https://github.com/organizations/{org}/settings/secrets/actions — set visibility to "All repositories".

**Repo secrets**: https://github.com/{org}/{repo}/settings/secrets/actions

## 5. What's in the Docker Image

The generated `Dockerfile.runner` builds an Ubuntu 24.04 image with:

- **GitHub Actions runner** (v2.332.0)
- **Herd CLI** (installed from source via `go install`)
- **Claude Code** (installed via npm)
- **Tools**: curl, jq, git, gh, Node.js

The `entrypoint.sh` script handles runner lifecycle:
1. Removes stale config from previous runs (ephemeral runners leave `.runner` behind on restart)
2. Registers with GitHub using a short-lived registration token
3. Starts the runner in ephemeral mode (picks up one job, then deregisters)
4. On SIGTERM/SIGINT, deregisters cleanly

The `docker-compose.herd.yml` runs the worker service with `restart: always`, so after each job completes the container restarts and re-registers for the next job.

## 6. Scaling

### Docker Compose

Scale the worker service:

```bash
# Start with 5 runners
docker compose -f docker-compose.herd.yml up -d --scale worker=5

# Or edit docker-compose.herd.yml:
# deploy:
#   replicas: 5
```

### Concurrency control

`workers.max_concurrent` in `.herdos.yml` controls how many workers HerdOS dispatches simultaneously. This is independent of how many runners you have — if you have 5 runners but `max_concurrent: 3`, only 3 will be active at once.

### Runner labels

`workers.runner_label` in `.herdos.yml` must match the `RUNNER_LABELS` environment variable in `docker-compose.herd.yml`. Default is `herd-worker`. Use different labels to route heavy tasks to specific runners (e.g., `herd-gpu`).

## 7. Cloud Runners

You can run on cloud VMs instead of Docker. Requirements:

1. Install the [GitHub Actions runner](https://github.com/actions/runner)
2. Install Claude Code: `npm install -g @anthropic-ai/claude-code`
3. Install Herd CLI: `go install github.com/herd-os/herd/cmd/herd@latest`
4. Register the runner with the `herd-worker` label
5. Set `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` in the runner's environment

See [GitHub's self-hosted runner docs](https://docs.github.com/en/actions/hosting-your-own-runners) for detailed setup.

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| Runner not picking up jobs | Label mismatch | Ensure `RUNNER_LABELS` matches `workers.runner_label` in `.herdos.yml` |
| Runner exits after one job | Expected | Ephemeral mode — `docker-compose` restarts it automatically |
| "Must not run with sudo" | Running as root | The Dockerfile creates a non-root `runner` user — don't override `USER` |
| Agent not found | Not installed | Ensure Claude Code is in the Docker image (`npm install -g @anthropic-ai/claude-code`) |
| 403 on PR creation | Org setting | Enable "Allow GitHub Actions to create and approve pull requests" in org settings |
| 403 on listing PRs | Missing permission | Ensure `pull-requests: write` is in workflow permissions |
| Dispatch succeeds but no run appears | Missing secret | Add `HERD_GITHUB_TOKEN` as org/repo secret (see section 1) |
| Token permission errors | Insufficient scope | Fine-grained: needs Administration read/write. Classic: needs `repo` scope |
| Auth errors in worker | Missing credentials | Verify `.env` has `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` |
