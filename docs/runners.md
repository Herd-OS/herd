# Runner Setup

HerdOS workers run as GitHub Actions on self-hosted runners. Self-hosted runners are required because workers need an AI agent (Claude Code or OpenCode) installed, and because GitHub-hosted runners don't support `workflow_dispatch` chaining with custom tools.

`herd init` generates all the files you need: `Dockerfile.herd_runner`, `docker-compose.herd.yml`, and `.env.herd.example`.

## Deployment options

There are two supported ways to run the runner containers. Both produce identical runners — same image, same env vars, same volume requirements — so pick by lifecycle and tooling preference:

- **Docker Compose** (covered in [Quick Setup](#quick-setup) below) — the simplest path for getting started, local development, and most single-machine setups. `herd init` generates `docker-compose.herd.yml` for you.
- **Direct `docker run`** (covered in [Running runners directly with `docker run`](#running-runners-directly-with-docker-run) below) — useful when you want to manage container lifecycle some other way (systemd, a NAS appliance's container runtime, a private orchestrator, etc.).

Subsequent sections assume Docker Compose for command examples, but every step has a `docker run` equivalent — credentials in `.env`, named volume for `~/.codex`, restart policy on the container.

## Quick Setup

```bash
herd init                    # generates runner files + config + PR
# merge the PR created by herd init
cp .env.herd.example .env         # copy the env template
# fill in .env (see sections below)
docker compose -f docker-compose.herd.yml build
docker compose -f docker-compose.herd.yml up -d
# enable workflows after runners are online:
gh variable set HERD_ENABLED --body true --repo <owner>/<repo>
```

Three runners start by default (configurable in `docker-compose.herd.yml`). Workflows are inactive until `HERD_ENABLED` is set — this prevents a workflow storm from queued events firing before runners are ready.

Prefer `docker run` directly? See [Running runners directly with `docker run`](#running-runners-directly-with-docker-run) for the equivalent commands.

## Running runners directly with `docker run`

If you'd rather not use Docker Compose — common reason: you're running on a NAS appliance, behind systemd, or under a private orchestrator — the same `Dockerfile.herd_runner` runs identically with plain `docker run`. The image, env vars, and volume requirements are the same as the Compose path; you're just supplying them to the Docker CLI directly.

```bash
# 1. Build the customized runner image (equivalent to `docker compose build`):
docker build -t herd-runner -f Dockerfile.herd_runner .

# 2. Run one container (equivalent to one replica of the compose `worker` service):
docker run -d \
  --name herd-worker-1 \
  --restart unless-stopped \
  --env-file .env \
  -v codex-auth:/home/runner/.codex \
  herd-runner

# 3. Run N parallel containers (equivalent to `up -d --scale worker=N`):
for i in 1 2 3; do
  docker run -d \
    --name herd-worker-$i \
    --restart unless-stopped \
    --env-file .env \
    -v codex-auth:/home/runner/.codex \
    herd-runner
done

# 4. Stop / remove a container:
docker stop herd-worker-1 && docker rm herd-worker-1

# 5. Restart (e.g., to pick up a fresh herd binary after a release):
docker restart herd-worker-1
```

Notes:

- **`--env-file .env`** reads the same file Docker Compose does, so the credential setup is identical between the two paths. AI provider auth, `GITHUB_TOKEN`, and the Codex subscription vars all live in `.env` either way (see [Agent Authentication](#2-agent-authentication) below).
- **`-v codex-auth:/home/runner/.codex`** uses a Docker named volume to persist Codex subscription state across container restarts. If you're not using the Codex subscription auth path, the volume is still harmless to mount and is created lazily by Docker on first use.
- **`--restart unless-stopped`** mirrors the `restart: always` policy in the generated Compose file. After each ephemeral job, the runner exits and Docker restarts it; the entrypoint re-registers with GitHub for the next job.
- **Container names matter** when scaling: each runner needs a unique name (which becomes its registered name on GitHub). The `--name herd-worker-$i` pattern above gives you stable, predictable names.

`Dockerfile.herd_runner` is the same file `herd init` generated for Docker Compose — no separate "direct mode" Dockerfile to maintain. Updates to it (e.g., extra `RUN apt-get install` lines for project-specific tools) take effect on the next `docker build`.

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
   - **Commit statuses**: Read and write
   - **Contents**: Read and write
   - **Issues**: Read and write
   - **Pull requests**: Read and write
   - **Workflows**: Read and write
   - **Metadata**: Read-only (auto-selected)
5. Generate and copy the token

### Classic token (simpler)

1. Go to **Settings → Developer settings → Tokens (classic) → Generate new token**
   (https://github.com/settings/tokens)
2. Select the `repo` and `workflow` scopes
3. Generate and copy the token

### Where to use it

Add the token in two places:

| Location | Variable | Purpose |
|----------|----------|---------|
| `.env` file | `GITHUB_TOKEN=ghp_...` | Docker runner registration |
| Org/repo secrets | `HERD_GITHUB_TOKEN` | Workflow dispatch between roles |

The same token works for both. `HERD_GITHUB_TOKEN` is needed because GitHub's automatic `GITHUB_TOKEN` cannot trigger `workflow_dispatch` events (anti-recursion protection). Without it, HerdOS runs but Monitor cannot redispatch failed workers and the Integrator cannot dispatch next-tier workers.

## 2. Agent Authentication

Authentication depends on which `agent.provider` is configured in `.herdos.yml` (see [configuration.md](configuration.md#agent-providers)).

> **AI provider auth env vars (Claude, OpenCode, Codex, Gemini) belong only in the runner's `.env`, not in GitHub Actions secrets.** The worker workflow's `env:` block would override container env unconditionally — surfacing these from secrets either no-ops or shadow-overrides your `.env` value at the step level. The runner container reads them from `.env` at startup (via `docker-compose`'s automatic `env_file` or `docker run --env-file .env`); the workflow inherits them from the runner process.

### Claude provider (`agent.provider: claude`)

Choose one:

#### Option 1: OAuth token (recommended)

Uses your Claude Pro/Max subscription — no per-token cost.

```bash
claude setup-token
# Copy the output token
```

Add to `.env`:
```
CLAUDE_CODE_OAUTH_TOKEN=your-token-here
```

#### Option 2: API key

Pay-per-token via https://console.anthropic.com/.

Add to `.env`:
```
ANTHROPIC_API_KEY=sk-ant-...
```

### OpenCode provider (`agent.provider: opencode`)

#### API key

OpenCode authenticates against whichever LLM provider the configured `agent.model` resolves to. Set the matching provider API key in the runner environment:

| `agent.model` prefix | Required env var |
|----------------------|------------------|
| `anthropic/...` (e.g. `anthropic/claude-sonnet-4`) | `ANTHROPIC_API_KEY` |
| `openai/...` (e.g. `openai/gpt-5`) | `OPENAI_API_KEY` |

Add the relevant key to `.env`:
```
ANTHROPIC_API_KEY=sk-ant-...
# or
OPENAI_API_KEY=sk-...
```

This API-key path does not use `CLAUDE_CODE_OAUTH_TOKEN`.

### Codex provider (`agent.provider: codex`)

The Codex provider shells out to the OpenAI Codex CLI. API-key auth is the documented default; ChatGPT-subscription auth is available as an opt-in (see [Subscription auth (opt-in)](#subscription-auth-opt-in) below).

#### API key

Codex itself reads `CODEX_API_KEY`. herd supports two ways to provide it:

- **Set `OPENAI_API_KEY`** — herd auto-maps it to `CODEX_API_KEY` at invocation time **when `CODEX_API_KEY` is unset**. This is convenient if you already have `OPENAI_API_KEY` in the environment (e.g. shared with the `opencode` provider).
- **Set `CODEX_API_KEY` directly** — an explicit `CODEX_API_KEY` always wins; it is never overwritten by the `OPENAI_API_KEY` mapping.

Add the key to `.env`:
```
OPENAI_API_KEY=sk-...
# or, to set it explicitly:
CODEX_API_KEY=sk-...
```

> `.env` is auto-gitignored by `herd init` — credentials won't be committed.

> `.env` holds the Docker runner's agent credentials and is read at container startup. The only GitHub Actions secret needed for auth is `HERD_GITHUB_TOKEN` (workflow dispatch); AI provider keys are not secrets — see the principle box above.

##### Auth precedence

Codex resolves credentials in this order: `CODEX_API_KEY` > ephemeral key > `CODEX_ACCESS_TOKEN` > `~/.codex/auth.json` (ChatGPT subscription).

herd's `OPENAI_API_KEY` -> `CODEX_API_KEY` convenience mapping is **skipped when a subscription `auth.json` is present** (under `$CODEX_HOME`, or `~/.codex` when `CODEX_HOME` is unset). This prevents a stray `OPENAI_API_KEY` in your shell from silently overriding your ChatGPT subscription and billing you per-token. Pure API-key users (no `auth.json`) keep the convenience mapping. An explicit `CODEX_API_KEY` always wins, with or without `auth.json`.

##### Diagnostics

`herd codex doctor` checks local Codex CLI presence, `auth.json` shape, env auth state, effective auth path, provider/model sanity, and stale `CODEX_AUTH_JSON`; it is local-only and does not inspect remote runner hosts.

```bash
herd codex doctor
```

#### Subscription auth (opt-in)

API-key auth (above) remains the documented default. If you'd rather drive Codex from a ChatGPT subscription instead of paying per token, herd supports two opt-in subscription paths. The mechanics mirror OpenAI's own [CI/CD auth guide](https://developers.openai.com/codex/auth/ci-cd-auth).

In every subscription path, set `agent.provider: codex` and `agent.model: gpt-5-codex` in `.herdos.yml`.

Codex workers run inside the runner container. When `agent.codex_sandbox` is unset, HerdOS defaults worker, integrator, and review runs to `danger-full-access`, because Codex's bubblewrap-based sandboxes usually cannot create namespaces inside container hosts. Set `codex_sandbox` only when you want an explicit base or per-role override. For local planning, an unset sandbox stays empty so Codex uses its own `workspace-write` default; `agent.exec: docker` planning uses the same container default as workers.

##### Path A — ChatGPT Enterprise (`CODEX_ACCESS_TOKEN`)

For ChatGPT Enterprise workspaces:

1. A workspace admin enables Codex access tokens.
2. A user mints an agent-identity JWT.
3. Set that JWT as `CODEX_ACCESS_TOKEN` in the runner environment (`.env`).

This is the cleanest headless path: there is no refresh dance and no per-runner auth state to keep in sync, so it scales to any number of workers without coordination.

##### Path B — Personal subscription (Plus/Pro/Team/Edu)

For a personal ChatGPT subscription (Plus/Pro/Team/Edu), authenticate **directly against the already-running worker container**. The CLI writes `~/.codex/auth.json` into the persistent `codex-auth` docker volume (mounted at `/home/runner/.codex`), which is the **source of truth** for subscription auth:

```bash
sudo docker exec -it -u runner <worker-container> codex login --device-auth
```

Complete the device-auth flow in your browser when prompted. On success, the Codex CLI writes `~/.codex/auth.json` into the `codex-auth` volume; thereafter Codex refreshes the OAuth token in place inside that volume. Because all worker replicas share the one `codex-auth` volume — `herd init` renders a single volume mounted at `/home/runner/.codex` for exactly this reason — a **single login covers every worker**. The background keepalive (see [Keepalive](#keepalive) below) keeps the idle chain warm so it doesn't expire between batches.

> **Rate-limit caveat**: ChatGPT Plus/Pro/Team rate limits are **per account**. Running additional worker replicas does **not** multiply your LLM-call throughput — they all share the one `auth.json` and the one account. Extra workers help only when tasks are wall-clock-bound on non-LLM work (builds, tests, I/O), not when they're bottlenecked on model calls.

###### No running worker yet?

If no worker container is up to exec into, run the login one-shot directly against the runner-base image with the `codex-auth` volume mounted, then let the container exit:

```bash
docker run --rm -it \
  -u "$(grep -E '^RUNNER_UID=' .env | cut -d= -f2-)" \
  -v <project>_codex-auth:/home/runner/.codex \
  ghcr.io/herd-os/herd-runner-base:latest \
  codex login --device-auth
```

The `-u` value must match the `RUNNER_UID` in your `.env` (default `1001`) so the written `auth.json` is owned by the same UID the running workers use. A mismatch causes a silent "no auth" fallback on the worker side — e.g. logging in as the base image's default `runner` (UID 1001) when your workers run with `RUNNER_UID=568` (the TrueNAS SCALE pattern) writes an `auth.json` the 568-running workers can't read. The `docker exec` primary path above doesn't have this problem because the running worker container has already chowned the volume. If `RUNNER_UID` is unset in `.env`, the shell expansion yields an empty `-u` (a docker error) — use the literal default `-u 1001` instead.

The volume name follows docker compose's `<project>_codex-auth` convention — confirm the exact name with `docker volume ls` before running. The written `auth.json` persists in the volume and is picked up by workers on their next start.

##### Recovery runbook

The OAuth chain can break server-side — a password change, "log out everywhere", or an OpenAI session expiry will invalidate it. When that happens herd surfaces an auth error from the failing worker. To recover, write a fresh `auth.json` into the volume and restart:

1. Re-run the login against a running worker (or use the `docker run` fallback above if none is up):

   ```bash
   sudo docker exec -it -u runner <worker-container> codex login --device-auth
   ```

2. Restart the runner(s).

The refreshed `auth.json` in the `codex-auth` volume is the source of truth — once it's rewritten, every worker picks it up.

##### Keepalive

When `~/.codex/auth.json` is present in the `codex-auth` volume, the runner entrypoint spawns a `herd codex keepalive-loop` goroutine. It periodically triggers Codex's own refresh via a near-noop `codex exec`, keeping idle OAuth chains warm so they don't lapse between batches. The default cadence is **6 days** (a ~2-day buffer before Codex's ~8-day forced refresh) and is tunable via `HERD_CODEX_KEEPALIVE_INTERVAL`, a Go duration string (e.g. `144h`). Logs go to `/opt/herd/herd-codex-keepalive.log` (owned by the runner user; survives the `RUNNER_UID` remap). The keepalive is **not** spawned for API-key-only or Enterprise-only (`CODEX_ACCESS_TOKEN`) setups, which need no refresh.

##### OpenAI security warnings

OpenAI mandates both of these for subscription-auth CI/CD use:

> Treat ~/.codex/auth.json like a password... Do not use this workflow for public or open-source repositories.

> Use one auth.json per runner or per serialized workflow stream. Do not share the same file across concurrent jobs or multiple machines.

All worker replicas share the single `codex-auth` volume and therefore the one `auth.json`, which is what the second warning cautions against. herd makes this safe in practice through Codex's self-healing refresh behavior: refreshes fire ~5 minutes before the ~1-hour access token expires, so a refresh-token race fails at most one worker at a time, and that worker recovers on its next invocation by reading the rotated tokens already in the shared `auth.json`. For sustained 24/7 high-concurrency workloads where you want to honor the warning strictly, use ChatGPT Enterprise (`CODEX_ACCESS_TOKEN`, Path A), which has no shared per-runner auth state.

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
| `HERD_ENABLED` | Variable | Yes | Activates workflows — set to `true` after runners are online |
| `HERD_RUNNER_LABEL` | Variable | No | Override default runner label (default: `herd-worker`) |

**Org secrets**: https://github.com/organizations/{org}/settings/secrets/actions — set visibility to "All repositories".

**Repo secrets**: https://github.com/{org}/{repo}/settings/secrets/actions

## 5. What's in the Docker Image

The base image is **published** at `ghcr.io/herd-os/herd-runner-base` — a public, multi-arch (linux/amd64, linux/arm64) image that provides the GitHub Actions runner and base tools (Node 22, git, gh, curl, jq). `herd init` no longer generates a local `Dockerfile.herd_runner_base`; the base is pulled from GHCR instead. (If a `Dockerfile.herd_runner_base` is left over from an older init, re-running `herd init` removes it — see [Migrating from the local base image](#migrating-from-the-local-base-image).)

`herd init` generates a single user-owned Dockerfile:

- **`Dockerfile.herd_runner`** — user-owned, created once by `herd init`, never overwritten. Its first line is `FROM ghcr.io/herd-os/herd-runner-base:<herd-version>` (see [Runner images](#6-runner-images)). Add project-specific tools (languages, a database client, linters, etc.) below the `FROM` line.

For example, to add a Postgres client on top of the base image:

```dockerfile
FROM ghcr.io/herd-os/herd-runner-base:v1.4.2
USER root
RUN apt-get update && apt-get install -y postgresql-client && rm -rf /var/lib/apt/lists/*
```

The base image's entrypoint starts as root, optionally remaps the runner UID/GID (see [Matching host UID/GID for bind mounts](#matching-host-uidgid-for-bind-mounts) below), then drops to the `runner` user via `gosu` before invoking the GitHub Actions runner. Do **not** end your `Dockerfile.herd_runner` with `USER runner` — that opts out of the remap path. The entrypoint detects whether the container started as root and falls back to the legacy non-root path if not, so existing wrappers continue to work, but they lose the `RUNNER_UID` override.

The **Herd CLI** is not baked into the image — the entrypoint script that ships inside the published base image (`ghcr.io/herd-os/herd-runner-base`) downloads it at container startup. This ensures runners always use the latest version without rebuilding. Set `HERD_VERSION` in `.env` to pin a specific version.

The base image's entrypoint script handles runner lifecycle:
1. Downloads the herd binary (latest or pinned version)
2. Installs both supported agent CLIs via npm at container startup: Claude Code (`@anthropic-ai/claude-code`) and OpenCode (`opencode-ai`). Both are present in every runner regardless of which `agent.provider` the repo selects, so switching providers does not require rebuilding the image.
3. Removes stale config from previous runs (ephemeral runners leave `.runner` behind on restart)
4. Registers with GitHub using a short-lived registration token
5. Starts the runner in ephemeral mode (picks up one job, then deregisters)
6. On SIGTERM/SIGINT, deregisters cleanly

The runner container is started with a restart-always policy (`restart: always` in `docker-compose.herd.yml`, or `--restart unless-stopped` if you're running with [direct `docker run`](#running-runners-directly-with-docker-run)) so after each job completes the container restarts and re-registers for the next job.

### Project-specific overrides

`docker-compose.herd.yml` is regenerated by `herd init`. To add project-specific configuration (build args, extra volumes, environment variables), create a `docker-compose.herd.override.yml`:

```yaml
services:
  worker:
    build:
      args:
        BUNDLE_RUBYGEMS__PKG__GITHUB__COM: ${BUNDLE_RUBYGEMS__PKG__GITHUB__COM}
    environment:
      - EXTRA_VAR=${EXTRA_VAR:-}
```

`herd init` automatically merges the override into `docker-compose.herd.yml`. The override file is never overwritten.

### Matching host UID/GID for bind mounts

By default the in-container `runner` user is UID/GID **1001:1001**, baked into the base image at build time (the next free UID on `ubuntu:24.04`, which reserves 1000 for its default `ubuntu` user). If your host expects a different owner — for example, TrueNAS SCALE runs Custom Apps as the `apps` user (568:568), and the TrueNAS UI creates bind-mount directories owned by 568:568 — set `RUNNER_UID` and `RUNNER_GID` in `.env`:

```bash
RUNNER_UID=568
RUNNER_GID=568
```

The entrypoint reads these on container start. If they differ from the build-time defaults, it `usermod`/`groupmod`s the `runner` user, recursively `chown`s `/home/runner`, `/runner`, and `/opt/herd`, then drops privileges via `gosu` before invoking the GitHub Actions runner. Same image, no rebuild, no per-host fork. The first-startup `chown -R` is one-time per UID change; restarts with the same UID skip it.

Caveats:

- **Do not set `RUNNER_UID=0` or `RUNNER_GID=0`.** The GitHub Actions runner refuses to run as root and the entrypoint rejects 0 to fail loudly.
- **`Dockerfile.herd_runner` must end as root.** If your wrapper ends with `USER runner` (older `herd init` versions added this), the container starts non-root and the entrypoint skips the remap entirely. Remove the trailing `USER runner` line to opt in.
- **Codex auth volume.** When you change `RUNNER_UID`, the existing `codex-auth` volume contents are chowned to the new UID on the next start. No re-seeding needed.

## 6. Runner images

The base image is a first-party runner image published to `ghcr.io/herd-os/` on every herd release. It is **public** (no `docker login` needed to pull) and **multi-arch** (linux/amd64, linux/arm64).

| Image | Contents |
|-------|----------|
| `herd-runner-base` | OS + GitHub Actions runner + Node 22 + git/gh/curl/jq |

`herd init` injects the base image into the `FROM` line of `Dockerfile.herd_runner` as `ghcr.io/herd-os/herd-runner-base:<herd-version>`. Add any additional tools or language toolchains by extending `Dockerfile.herd_runner` with `RUN apt-get install …` lines below the `FROM` line.

### Version pinning

The base image is pinned to the herd version that generated `Dockerfile.herd_runner` (e.g. `:v1.4.2`). The pin is refreshed when you re-run `herd init` from a newer herd binary: if the existing `FROM` line still references the legacy local base (`FROM herd-runner-base[:tag]`), it is auto-migrated to the version-pinned `ghcr.io/herd-os/herd-runner-base:<herd-version>` reference (see [Migrating from the local base image](#migrating-from-the-local-base-image)). `FROM` lines that already point at `ghcr.io` or use a custom base are left untouched, so re-running `herd init` will not bump an already-published `ghcr.io` reference for you — refresh those by editing `Dockerfile.herd_runner` yourself. Dev builds (an empty or `dev` version) pin to `:latest`, because a `:dev` tag does not exist in GHCR and would fail to pull.

### Building and publishing your runner image

Once you've customized `Dockerfile.herd_runner`, you can build and publish the resulting image to GHCR under your own repository:

```bash
herd image build      # docker build -f Dockerfile.herd_runner -t ghcr.io/<owner>/<repo>-herd-runner:<tag> .
docker login ghcr.io  # required before publishing
herd image publish    # docker push ghcr.io/<owner>/<repo>-herd-runner:<tag>
```

The owner and repo are detected from your git remote and lower-cased; the tag defaults to the herd version (`latest` for dev builds) and can be overridden with `--tag`.

This is also automated. `herd init` installs `.github/workflows/herd-publish-runner.yml`, which builds and pushes the multi-arch consumer image (`ghcr.io/<owner>/<repo>-herd-runner:latest`) on two triggers:

- **`push` to `main` that touches `Dockerfile.herd_runner`** — automatic. This is the path that picks up a new base-image version after merging an `Update HerdOS to <tag>` PR (which bumps the wrapper's `FROM ghcr.io/herd-os/herd-runner-base:<version>` line). Without it, workers would continue running with stale baked-in agent CLIs and project-specific tools until someone manually triggered a rebuild.
- **`workflow_dispatch`** — manual escape hatch. `gh workflow run herd-publish-runner.yml --ref main` from your shell, or the **Run workflow** button in the Actions tab. Useful for re-publishing after editing the wrapper without going through a PR.

The job is gated on the `HERD_ENABLED` variable being `true` and requires `packages: write` permission (the default `GITHUB_TOKEN` is used to authenticate to GHCR).

### Publishing on a self-hosted runner

By default, the publish workflow runs on GitHub-hosted runners:

```yaml
image_publish:
  runs_on: ["ubuntu-latest"]
```

Switch this to a self-hosted publisher when private-repo Actions minutes are expensive, the build needs access to an internal or air-gapped registry, or compliance policy disallows GitHub-hosted runners.

Self-hosted publisher prerequisites:

- Docker daemon access for the runner user.
- `docker buildx` available to the runner user.
- QEMU emulation registered once per host if you keep the default multi-arch build (`--platform linux/amd64,linux/arm64`):

  ```bash
  docker run --rm --privileged tonistiigi/binfmt --install all
  ```

- GHCR write credentials through the workflow's `secrets.GITHUB_TOKEN`; the generated workflow uses that token for `docker login ghcr.io`.
- A distinct runner label that does not collide with `workers.runner_label` (default `herd-worker`).

Do not co-locate publish builds on a `herd-worker`-labeled runner. Image builds can consume the host for several minutes, which blocks worker dispatch while the publisher job is running.

```yaml
image_publish:
  runs_on: ["self-hosted", "herd-publisher"]
```

`herd-publisher` is only a suggested convention. You can choose any label registered on the host, and multi-label selectors such as `["self-hosted", "linux", "x64", "my-publisher"]` are allowed.

The publish workflow runs only on `workflow_dispatch` or pushes to `Dockerfile.herd_runner` on `main`, so the publisher host can be sized for bursty image builds. It still must be available when Herd upgrade PRs merge, because those PRs update the runner image pin and trigger a rebuild after merge.

If you only need one architecture, you can edit the workflow's `--platform` flag and drop the architecture you do not need. That is a managed-file edit, so accept the resulting drift from Herd's generated workflow.

### Migrating from the local base image

Older versions of `herd init` generated a local two-layer build: a herd-managed `Dockerfile.herd_runner_base` plus a `Dockerfile.herd_runner` that did `FROM herd-runner-base`, with a separate base service in `docker-compose.herd.yml`. The base image is now published to GHCR, so:

- Re-running `herd init` **removes** the obsolete `Dockerfile.herd_runner_base` and drops the base service from `docker-compose.herd.yml` (the worker now builds directly from `Dockerfile.herd_runner`).
- `herd init` **auto-migrates** the `FROM` line in an existing `Dockerfile.herd_runner`. If a `FROM` line references the legacy local base (`FROM herd-runner-base` or `FROM herd-runner-base:<tag>`), that single line is rewritten to `FROM ghcr.io/herd-os/herd-runner-base:<herd-version>` and the rest of the file is left byte-identical (your customizations below the `FROM` line are preserved). Any trailing tokens on the matched `FROM` line — a multi-stage `AS <stage>` alias or a trailing `# comment` — are kept. Note that the matched `FROM` line itself is normalized: leading whitespace is dropped and internal whitespace between the tokens collapses to single spaces. `FROM` lines that already point at `ghcr.io` or use a custom base are left untouched. On migration, `herd init` prints `Migrated Dockerfile.herd_runner FROM line to <base-image>`; otherwise it prints `Dockerfile.herd_runner already exists (not overwritten)`.
- The base image is **public**, so no `docker login` is needed to pull it at build time.

This means the per-project upgrade from the old local-base model collapses to:

1. Upgrade the `herd` binary (`brew upgrade herd-os/tap/herd`, or replace the binary — see [installation.md](installation.md)).
2. Re-run `herd init` in the repository — the legacy `FROM` line is rewritten automatically.
3. Merge the PR that `herd init` opens.
4. Redeploy the runner containers (`docker compose -f docker-compose.herd.yml up -d --build`).

No manual edit of `Dockerfile.herd_runner` is required.

## 7. Scaling

### Docker Compose

Scale the worker service:

```bash
# Start with 5 runners
docker compose -f docker-compose.herd.yml up -d --scale worker=5

# Or edit docker-compose.herd.yml:
# deploy:
#   replicas: 5
```

### Direct `docker run`

Start N containers with unique names — each becomes a separately registered runner:

```bash
for i in 1 2 3 4 5; do
  docker run -d \
    --name herd-worker-$i \
    --restart unless-stopped \
    --env-file .env \
    -v codex-auth:/home/runner/.codex \
    herd-runner
done
```

See [Running runners directly with `docker run`](#running-runners-directly-with-docker-run) for the full command shape and notes.

### Concurrency control

`workers.max_concurrent` in `.herdos.yml` controls how many workers HerdOS dispatches simultaneously. This is independent of how many runners you have — if you have 5 runners but `max_concurrent: 3`, only 3 will be active at once.

### Runner labels

`workers.runner_label` in `.herdos.yml` must match the `RUNNER_LABELS` environment variable on the runner container (set in `docker-compose.herd.yml`, or via `-e RUNNER_LABELS=…` / `.env` for direct `docker run`). Default is `herd-worker`. Use different labels to route heavy tasks to specific runners (e.g., `herd-gpu`).

### Worker timeout sizing

`workers.timeout_minutes` configures the outer GitHub Actions job timeout for
worker jobs. HerdOS runs the inner agent command with a shorter deadline so it
can stop the agent, checkpoint timed-out work, run validation when applicable,
post diagnostics, and push resumable state before GitHub Actions cancels the
job.

Avoid very low `workers.timeout_minutes` values for complex Codex tasks, Rails
apps, or repositories with long build/test cycles. Low values reduce the cleanup
reserve and make it more likely that GitHub Actions cancels the job before HerdOS
can checkpoint and report useful state. Prefer increasing the worker timeout for
test-heavy repos instead of relying on Monitor retries as the normal path.

On Unix-like self-hosted runners, HerdOS starts agent commands in a process group
and terminates that group where possible when the inner timeout fires. This helps
clean up wrapper-spawned descendants such as Codex's native child process. On
Windows runners, HerdOS terminates the direct child process.

## 8. Cloud Runners

You can run on cloud VMs instead of Docker. Requirements:

1. Install the [GitHub Actions runner](https://github.com/actions/runner)
2. Install both agent CLIs (matches the Docker base image, so either `agent.provider` works):
   - Claude Code: `npm install -g @anthropic-ai/claude-code`
   - OpenCode: `npm install -g opencode-ai`
3. Install Herd CLI: `go install github.com/herd-os/herd/cmd/herd@latest`
4. Register the runner with the `herd-worker` label
5. Set the agent credentials in the runner's environment:
   - For `agent.provider: claude` — `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`
   - For `agent.provider: opencode` — the provider API key for the configured model (e.g. `ANTHROPIC_API_KEY` for `anthropic/...` models, `OPENAI_API_KEY` for `openai/...` models)
   - For `agent.provider: codex` — `OPENAI_API_KEY` (auto-mapped to `CODEX_API_KEY` when unset) or `CODEX_API_KEY` directly

See [GitHub's self-hosted runner docs](https://docs.github.com/en/actions/hosting-your-own-runners) for detailed setup.

## 9. Checking for updates

`herd init` lays down a set of managed files — workflow YAMLs in `.github/workflows/` (including `herd-publish-runner.yml`), `docker-compose.herd.yml`, and `.env.herd.example`. Newer versions of the `herd` binary may render different content for those files, so a repository that was initialized against an older version can drift from the current templates over time.

`herd init --check` (with `--dry-run` as an alias) re-renders every managed file, compares the result against what's on disk, and prints a per-file summary: `✓` for files that match and `✗ <path> (would change)` followed by up to 5 lines of diff preview for files that differ. After the per-file output, it prints a final line of the form `N files would be modified, M unchanged`. The command exits 0 when nothing would change and 1 when any drift is detected.

`Dockerfile.herd_runner` is user-owned — it is created once by `herd init` and never overwritten — so check mode only verifies that it exists and does not compare its content. No labels, workflows, secrets, or git operations are touched in check mode; it is purely read-only.

```
herd init --check
```

This makes it easy to wire into CI as a guard against forgetting to re-run `herd init` after upgrading the binary:

```
- name: Verify HerdOS files are current
  run: herd init --check
```

The `Herd-OS/herd` repository itself wires this step into its own CI to catch drift between the embedded workflow templates and the rendered `.github/workflows/herd-*.yml` files — see [`../CONTRIBUTING.md`](../CONTRIBUTING.md) for the template-first edit workflow contributors follow.

For day-to-day use, `herd plan` also prints a one-line warning when drift is detected, so you'll be nudged to re-run `herd init` without having to remember to check explicitly.

Both `herd plan` and `herd init --check` additionally perform a best-effort lookup against `api.github.com` and warn when a newer herd release is published. The check is bounded by a 3-second timeout and never blocks; it is intentionally skipped for dev builds and pre-release tags. See [Installation → Upgrade notifications](installation.md#upgrade-notifications) for details.

## Private dependencies and extra secrets

Worker runners often need to install private dependencies — Bundler from GitHub Packages, npm from a private registry, pip from a private index, cargo from a private Maven mirror, and so on. The package managers usually read credentials from environment variables (e.g. `BUNDLE_RUBYGEMS__PKG__GITHUB__COM`, `NPM_TOKEN`, `PIP_INDEX_URL`).

Use `workers.extra_env` in `.herdos.yml` to surface those credentials into the worker job:

```yaml
workers:
  extra_env:
    - BUNDLE_RUBYGEMS__PKG__GITHUB__COM
    - NPM_TOKEN
```

For each name listed, `herd init` renders one line into the `env:` block of the worker workflow's `Execute task` step:

```yaml
BUNDLE_RUBYGEMS__PKG__GITHUB__COM: ${{ secrets.BUNDLE_RUBYGEMS__PKG__GITHUB__COM }}
```

The named secrets must be configured as GitHub repository or organization secrets. herd reads from `secrets.<NAME>` at workflow runtime; it does not create or manage the secrets themselves. Add them under **Settings → Secrets and variables → Actions** at the repo or org level before listing them in `extra_env`.

This passthrough applies only to the worker workflow. The integrator and monitor workflows are not affected.

After editing `extra_env`, re-run `herd init` to regenerate `.github/workflows/herd-worker.yml`.

See [configuration.md](configuration.md) for the full field reference.

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| Runner not picking up jobs | Label mismatch | Ensure `RUNNER_LABELS` matches `workers.runner_label` in `.herdos.yml` |
| Runner exits after one job | Expected | Ephemeral mode — Docker's restart policy (`restart: always` in Compose, `--restart unless-stopped` for direct `docker run`) brings it back automatically |
| "Must not run with sudo" | Running as root | The Dockerfile creates a non-root `runner` user — don't override `USER` |
| Agent not found | Not installed | Ensure the configured agent CLI is in the Docker image — the base image installs both (`npm install -g @anthropic-ai/claude-code` and `npm install -g opencode-ai`) |
| 403 on PR creation | Org setting | Enable "Allow GitHub Actions to create and approve pull requests" in org settings |
| 403 on listing PRs | Missing permission | Ensure `pull-requests: write` is in workflow permissions |
| Dispatch succeeds but no run appears | Missing secret | Add `HERD_GITHUB_TOKEN` as org/repo secret (see section 1) |
| Token permission errors | Insufficient scope | Fine-grained: needs Administration read/write. Classic: needs `repo` scope |
| Integrator crashes checking CI | Missing CI read permission | Add **Actions: Read**, **Checks: Read**, and **Statuses: Read** to the fine-grained PAT, or set `require_ci: false` in `.herdos.yml`. CI diagnostics are best-effort and depend on the permissions GitHub grants to the workflow/token. |
| Auth errors in worker | Missing credentials | Verify `.env` has the right key for the configured provider — Claude: `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`; OpenCode: `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` matching `agent.model`; Codex: `OPENAI_API_KEY` or `CODEX_API_KEY` |
