# Runners

Workers execute on GitHub Actions self-hosted runners. In v1.0, HerdOS provides a Docker image as the supported way to run self-hosted runners. Users who prefer bare-metal can set up GitHub Actions runners manually.

## Runner Types

| Type | Cost | Setup | Best For |
|------|------|-------|----------|
| Docker (your machine) | Free | `docker compose up` | Solo developers |
| Docker (cloud VM) | VM cost | Same image, remote host | Teams, always-on |
| GitHub-hosted | Actions minutes | None | Quick start (if agent CLI available) |
| Manual bare-metal | Free | User manages setup | Advanced users |

## Docker Runner

HerdOS provides a minimal base Dockerfile for running self-hosted runners in containers. It includes only the essentials — users extend it with their project's toolchain and agent.

### Base Image Contents

- GitHub Actions runner binary (configured with `--ephemeral`)
- `herd` CLI
- `git`
- `gh` CLI

No agent CLI, no programming languages, no build tools. The image is intentionally minimal so users can extend it for their stack.

### Entrypoint Lifecycle

The Docker image's entrypoint script manages the full runner lifecycle:

1. **Boot** — calls the GitHub API to create a registration token, then runs the Actions runner's `config.sh` to register with the repository. The runner is configured with `--ephemeral` so it exits after completing one job.
2. **Idle** — the runner long-polls GitHub for work. If no jobs are queued, it waits.
3. **Job** — picks up a job, executes it, then exits (ephemeral mode).
4. **Restart** — Docker's `restart: always` policy restarts the container. The next boot starts with a clean filesystem — no leftover files, no stale state from the previous job.
5. **Shutdown** — on `SIGTERM`/`SIGINT` (e.g., `docker compose down`), the entrypoint traps the signal and runs `config.sh remove` to deregister the runner from GitHub before exiting. No ghost runners left behind.

This means every job runs in a clean environment. The tradeoff is a few seconds of overhead per job for container restart and re-registration.

### Entrypoint Script

```bash
#!/bin/bash
# entrypoint.sh — included in the base image

# Deregister on shutdown
cleanup() {
  echo "Removing runner..."
  ./config.sh remove --token "$(get_token)"
  exit 0
}
trap cleanup SIGTERM SIGINT

# Get a registration token from the GitHub API
get_token() {
  curl -s -X POST \
    -H "Authorization: token ${GITHUB_TOKEN}" \
    "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/actions/runners/registration-token" \
    | jq -r .token
}

# Parse REPO_URL into owner/name
REPO_OWNER=$(echo "$REPO_URL" | sed -E 's|.*/([^/]+)/([^/]+)$|\1|')
REPO_NAME=$(echo "$REPO_URL" | sed -E 's|.*/([^/]+)/([^/]+)$|\2|')

# Register as an ephemeral runner
./config.sh \
  --url "$REPO_URL" \
  --token "$(get_token)" \
  --name "${RUNNER_NAME:-$(hostname)}" \
  --labels "${RUNNER_LABELS:-herd-worker}" \
  --ephemeral \
  --unattended

# Start the runner (run.sh is part of the GitHub Actions runner binary —
# it listens for jobs and executes them; blocks until job completes, then exits)
exec ./run.sh
```

### Base Dockerfile

The base image published at `ghcr.io/herd-os/github-runner` is built for both `linux/amd64` and `linux/arm64` (multi-arch). Docker automatically pulls the right architecture — ARM64 on Apple Silicon Macs, AMD64 on Intel/AMD machines.

```dockerfile
FROM ubuntu:22.04

ARG RUNNER_VERSION=2.319.1
ARG HERD_VERSION=1.0.0
ARG TARGETARCH

# Dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl jq ca-certificates git gh \
    && rm -rf /var/lib/apt/lists/*

# Map Docker's TARGETARCH to GitHub's naming convention
# TARGETARCH is set automatically by Docker buildx (amd64, arm64)
RUN ARCH=$(echo "$TARGETARCH" | sed 's/amd64/x64/' | sed 's/arm64/arm64/') \
    && mkdir /runner && cd /runner \
    && curl -sL "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${ARCH}-${RUNNER_VERSION}.tar.gz" \
    | tar xz

# Herd CLI
RUN curl -sL "https://github.com/herd-os/herd/releases/download/v${HERD_VERSION}/herd-linux-${TARGETARCH}" \
    -o /usr/local/bin/herd && chmod +x /usr/local/bin/herd

WORKDIR /runner
COPY entrypoint.sh .
RUN chmod +x entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

The image is built and pushed via CI:

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  -t ghcr.io/herd-os/github-runner:latest \
  -t ghcr.io/herd-os/github-runner:v1.0.0 \
  --push .
```

### Usage

```dockerfile
# Dockerfile.runner (in your project)
FROM ghcr.io/herd-os/github-runner:latest

# Add your agent
RUN npm install -g @anthropic-ai/claude-code

# Add your project's toolchain
RUN apt-get update && apt-get install -y golang-go
```

```yaml
# docker-compose.yml
services:
  worker:
    build:
      dockerfile: Dockerfile.runner
    restart: always
    environment:
      - GITHUB_TOKEN
      - REPO_URL
      - ANTHROPIC_API_KEY
    deploy:
      replicas: 3
```

```bash
# Start 3 runners
docker compose up -d

# Check logs
docker compose logs -f worker
```

Each replica registers as an independent ephemeral runner. Three replicas = three concurrent workers. After each job, the container restarts with a clean filesystem and re-registers.

### Runner Labels

Labels route workflow jobs to specific runners.

| Label | Purpose |
|-------|---------|
| `herd-worker` | General-purpose worker runner (default) |
| `herd-gpu` | Runner with GPU for ML/AI tasks |
| `herd-heavy` | Runner with more resources for complex tasks |
| `self-hosted` | Applied automatically by GitHub |

Configure the default label in `.herdos.yml`:

```yaml
workers:
  runner_label: "herd-worker"
```

Individual issues can override this with the `runner_label` front matter field. For example, a task that needs GPU hardware would set `runner_label: "herd-gpu"` in its issue. The Planner sets this during decomposition when the task clearly requires specific hardware.

The runner label is set via the `RUNNER_LABELS` environment variable. To run runners with different labels, use separate services in Docker Compose:

```yaml
services:
  worker:
    build:
      dockerfile: Dockerfile.runner
    restart: always
    environment:
      - GITHUB_TOKEN
      - REPO_URL
      - RUNNER_LABELS=herd-worker
      - ANTHROPIC_API_KEY
    deploy:
      replicas: 3

  gpu-worker:
    build:
      dockerfile: Dockerfile.runner.gpu
    restart: always
    environment:
      - GITHUB_TOKEN
      - REPO_URL
      - RUNNER_LABELS=herd-gpu
      - ANTHROPIC_API_KEY
    deploy:
      replicas: 1
      resources:
        reservations:
          devices:
            - capabilities: [gpu]
```

### Managing Runners

```bash
# Start runners
docker compose up -d

# Scale up/down
docker compose up -d --scale worker=5

# Stop runners (deregisters from GitHub)
docker compose down

# View logs
docker compose logs -f worker
```

### Resource Considerations

- Each worker needs ~2 GB RAM for the agent
- CPU is less of a constraint (workers are mostly I/O-bound waiting for API responses)
- Disk: each worker checks out the full repo on every job (ephemeral — clean slate each time)

### Cloud Deployment

The same Docker image works on any cloud provider:

1. Provision a VM (e.g., 4 vCPU, 8 GB RAM for 3 concurrent workers)
2. Install Docker
3. Copy your `Dockerfile.runner` and `docker-compose.yml`
4. Run `docker compose up -d`

### Cost Comparison

| Setup | Monthly Cost | Concurrency |
|-------|-------------|-------------|
| Docker on your laptop | $0 | 1-3 workers |
| Docker on cloud VM (e2-standard-4) | ~$100/mo | 3 concurrent workers |
| GitHub-hosted (Linux) | ~$0.008/min | Limited by plan |

Self-hosted runners don't consume GitHub Actions minutes. You pay only for the machine they run on (which is $0 for your own laptop).

## Runner Status

Check runner status from the CLI:

```bash
$ herd status --runners
RUNNER              STATUS    LABELS          BUSY
herd-worker-1       online    herd-worker     idle
herd-worker-2       online    herd-worker     running #42
herd-worker-3       offline   herd-worker     -
```

`herd runner list` queries the GitHub API (`GET /repos/{owner}/{repo}/actions/runners`) to show all registered runners and their current state.

## Manual Bare-Metal Setup

For users who prefer not to use Docker, you can set up GitHub Actions runners manually following [GitHub's documentation](https://docs.github.com/en/actions/hosting-your-own-runners). Ensure the runner has:

- The configured agent CLI installed and authenticated
- `git` and `herd` CLI installed
- The `herd-worker` label (or your configured label)
- Network access to GitHub (HTTPS)

HerdOS doesn't manage bare-metal runner lifecycle in v1.0. Service management (systemd, launchd, etc.) is the user's responsibility.

## Security Considerations

Self-hosted runners execute arbitrary code from workflow files. This is safe when:
- The repo is private and trusted
- Only repo collaborators can trigger workflows
- `workflow_dispatch` is restricted to users with write access

For public repos, self-hosted runners are a security risk. See [permissions.md](05-permissions.md) for details.

## Runner for Monitor

The Monitor Action doesn't need an agent — it only makes GitHub API calls via the `herd` CLI. By default it uses the same self-hosted runners as workers (since `herd` is already installed there). It could also run on GitHub-hosted runners if a step is added to install the `herd` binary first.

```yaml
# In herd-monitor.yml (default: reuse self-hosted runners)
jobs:
  patrol:
    runs-on: [self-hosted, herd-worker]
```
