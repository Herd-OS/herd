# Runner Setup

HerdOS workers run as GitHub Actions on self-hosted runners. Self-hosted runners are required because workers need an AI agent (Claude Code or OpenCode) installed, and because GitHub-hosted runners don't support `workflow_dispatch` chaining with custom tools.

`herd init` generates all the files you need: `Dockerfile.herd_runner`, `docker-compose.herd.yml`, and `.env.herd.example`.

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

Also add as an org or repo secret named `CLAUDE_CODE_OAUTH_TOKEN`.

#### Option 2: API key

Pay-per-token via https://console.anthropic.com/.

Add to `.env`:
```
ANTHROPIC_API_KEY=sk-ant-...
```

Also add as an org or repo secret named `ANTHROPIC_API_KEY`.

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

Also add it as an org or repo secret with the same name so the GitHub Actions worker workflow can surface it. This API-key path does not use `CLAUDE_CODE_OAUTH_TOKEN`.

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

Also add the same secret name as an org or repo secret so the GitHub Actions worker workflow can surface it. The worker workflow forwards **both** `OPENAI_API_KEY` and `CODEX_API_KEY`, so configure whichever one you use as a secret.

> `.env` is auto-gitignored by `herd init` — credentials won't be committed.

> `.env` is for Docker runners (container registration and agent auth). Org/repo secrets are for GitHub Actions workflows. If you use Docker runners, you need both.

#### Subscription auth (opt-in)

API-key auth (above) remains the documented default. If you'd rather drive Codex from a ChatGPT subscription instead of paying per token, herd supports three opt-in subscription paths. The mechanics mirror OpenAI's own [CI/CD auth guide](https://developers.openai.com/codex/auth/ci-cd-auth).

In every subscription path, set `agent.provider: codex` and `agent.model: gpt-5-codex` in `.herdos.yml`.

##### Path A — ChatGPT Enterprise (`CODEX_ACCESS_TOKEN`)

For ChatGPT Enterprise workspaces:

1. A workspace admin enables Codex access tokens.
2. A user mints an agent-identity JWT.
3. Set that JWT as `CODEX_ACCESS_TOKEN` **both** in the runner environment (`.env`) **and** as a repo/org GitHub Actions secret.

This is the cleanest headless path: there is no refresh dance, and it is multi-replica safe because there is no per-replica state to keep in sync.

##### Path B — Personal subscription (Plus/Pro/Team/Edu), single replica

For a personal ChatGPT subscription on a single runner:

1. Run `codex login` on a trusted machine (or `codex login --device-auth` when the machine has no browser).
2. Base64-encode the resulting credentials:

   ```bash
   base64 -w0 ~/.codex/auth.json
   ```

3. Set the encoded value as `CODEX_AUTH_JSON` in the runner `.env`.

On the first worker run, herd seeds the docker-volume-backed `~/.codex` from `CODEX_AUTH_JSON`; thereafter Codex refreshes the OAuth token in place inside that volume. A **docker volume on `~/.codex` is REQUIRED** — without it, rotated tokens are lost on every container restart and the chain breaks. The background keepalive (see below) keeps idle chains warm so they don't expire between batches.

##### Path C — Personal subscription, N parallel replicas

Two flavors, based on your tolerance for OpenAI's "one `auth.json` per runner" warning. Pick one; don't mix them.

> **Rate-limit caveat (applies to both flavors)**: ChatGPT Pro/Team rate limits are **per account**, so N replicas do **not** multiply your LLM-call throughput. Extra replicas help only when tasks are wall-clock-bound on non-LLM work (builds, tests, I/O), not when they're bottlenecked on model calls.

###### Path C-shared (recommended for personal use)

One `codex login`, one `auth.json`, **all N replicas share the same docker volume**. Less setup, but OpenAI warns against it (the warning is about refresh-token races; in practice the failure is rare, automatic, and self-healing — see below).

1. Log in **once**:
   ```bash
   codex login
   ```
2. Encode and set a **single** env var (NOT `_1`/`_2`/…):
   ```bash
   echo "CODEX_AUTH_JSON=$(base64 -w0 ~/.codex/auth.json)" >> .env
   ```
3. Set `agent.codex_replicas: 1` in `.herdos.yml` so herd init generates a single worker service.
4. Scale the worker at runtime via docker-compose:
   ```bash
   docker compose -f docker-compose.herd.yml up -d --scale worker=N
   ```
   All N containers mount the **same** `codex-auth` volume and share the same `auth.json`.

> **Concurrency cap caveat**: with `agent.codex_replicas: 1`, herd's validation enforces `workers.max_concurrent <= 1`. To dispatch more than one worker at a time against the shared volume, override the validation by setting `workers.max_concurrent: N` after `herd init` (validation will reject the config; you'll need to invoke herd commands with the higher value via `.herdos.yml` edits that you re-apply after each `herd init`). A first-class `agent.codex_shared_volume: true` config field is a future enhancement.

What can go wrong with sharing one `auth.json`:

- Codex refreshes the OAuth bundle when the access token is within 5 minutes of expiry (access tokens last ~1 hour), or when `last_refresh` is older than ~8 days. If two replica containers hit OpenAI's token endpoint in the same 5-minute window with the same `refresh_token`, one wins (gets `refresh_token_v2`) and the other gets a "refresh token already used" error.
- The losing worker is in a self-healing state: on its next invocation, it reads the updated `auth.json` and finds `refresh_token_v2`, or its `access_token` is still fresh (refreshes fire 5 minutes before expiry, so the cached token usually has slack) and no refresh is needed at all.
- Net effect: occasional auth blips that auto-recover on the next worker. For typical batch herd usage this is rare and forgiving; for sustained 24/7 high-concurrency workloads, Path C-strict is safer.
- Keepalive in shared-volume mode: every container's entrypoint spawns its own `herd codex keepalive-loop`. They all observe the same on-disk `last_refresh` and most will skip — so N near-simultaneous keepalives in practice means at most one refresh per cadence. Acceptable, but if you want to be strict you can set `HERD_CODEX_KEEPALIVE_INTERVAL=8760h` (1 year — effectively disable) on N-1 of the replicas.

###### Path C-strict (OpenAI's recommendation)

Each replica gets its own independent login → its own `auth.json` → its own docker volume. No race risk. Setup is more involved.

1. Create a `CODEX_HOME` directory for each replica **before** logging in (Codex refuses to run if `CODEX_HOME` doesn't exist):
   ```bash
   for i in 1 2 3; do
     mkdir -p ~/codex-replica-$i
     CODEX_HOME=~/codex-replica-$i codex login
   done
   ```

   > **Browser-session caveat**: OpenAI's OAuth server deduplicates within a single browser session — running `codex login` N times against the same logged-in browser returns the **same** token each time. You must use a **fresh cookie jar per login**: either a separate incognito/private-window session per replica (close + reopen between each `codex login`), or N pre-created browser profiles (Chrome/Firefox profiles each have their own cookies). Without this you'll get N identical seeds and replicas 2..N will race on the same refresh token.

2. Base64-encode each `auth.json` into `CODEX_AUTH_JSON_1` … `CODEX_AUTH_JSON_N`. Note: `CODEX_HOME` points at the codex config dir directly (no internal `.codex` subdir), so the file is at `~/codex-replica-$i/auth.json`:
   ```bash
   for i in 1 2 3; do
     echo "CODEX_AUTH_JSON_$i=$(base64 -w0 ~/codex-replica-$i/auth.json)"
   done >> .env
   ```

3. Set `agent.codex_replicas: N` in `.herdos.yml`.
4. Run `herd init` to regenerate `docker-compose.herd.yml` with N worker services (`herd-worker-1` … `herd-worker-N`) and N named volumes (`codex-auth-1` … `codex-auth-N`).

##### Secret-routing rule (important)

`CODEX_AUTH_JSON` / `CODEX_AUTH_JSON_N` must be set in the **runner container environment** (via the per-replica `docker-compose` service), **NOT** as GitHub Actions secrets surfaced in the worker workflow. The worker workflow only surfaces `CODEX_ACCESS_TOKEN`, because an Actions `env:` block unconditionally overrides container env — surfacing `CODEX_AUTH_JSON*` there would clobber the per-replica `auth.json` value that `docker-compose` injects. Only `CODEX_ACCESS_TOKEN` (Enterprise) and the API-key vars belong in GitHub secrets.

##### Recovery runbook

The OAuth chain can break server-side — a password change, "log out everywhere", or an OpenAI session expiry will invalidate it. When that happens herd surfaces an auth error from the failing worker. To recover:

1. Re-run `codex login` (N times if multi-replica).
2. Update `CODEX_AUTH_JSON` / `CODEX_AUTH_JSON_N` with the new base64 value(s).
3. Restart the runner(s).

Provisioning detects the `.herd-seed` mismatch on the next agent use and re-seeds the volume automatically — no force-seed flag exists or is needed.

##### Keepalive

When any `CODEX_AUTH_JSON*` env var is set, the runner entrypoint spawns a `herd codex keepalive-loop` goroutine. It periodically triggers Codex's own refresh via a near-noop `codex exec`, keeping idle OAuth chains warm so they don't lapse between batches. The default cadence is **6 days** (a ~2-day buffer before Codex's ~8-day forced refresh) and is tunable via `HERD_CODEX_KEEPALIVE_INTERVAL`, a Go duration string (e.g. `144h`). Logs go to `/var/log/herd-codex-keepalive.log`. The keepalive is **not** spawned for API-key-only or Enterprise-only (`CODEX_ACCESS_TOKEN`) setups, which need no refresh.

##### OpenAI security warnings

OpenAI mandates both of these for subscription-auth CI/CD use:

> Treat ~/.codex/auth.json like a password... Do not use this workflow for public or open-source repositories.

> Use one auth.json per runner or per serialized workflow stream. Do not share the same file across concurrent jobs or multiple machines.

herd's multi-replica design satisfies the second: each replica has its own seed **and** its own docker volume, so no `auth.json` is shared across concurrent jobs.

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
| `CLAUDE_CODE_OAUTH_TOKEN` | Secret | Claude provider (one of these) | Claude provider auth — Pro/Max subscription |
| `ANTHROPIC_API_KEY` | Secret | Claude or OpenCode (anthropic models) — one of these | Agent auth — pay-per-token Anthropic API key |
| `OPENAI_API_KEY` | Secret | OpenCode with `openai/...` model, or Codex | OpenCode provider auth for OpenAI models; for Codex, auto-mapped to `CODEX_API_KEY` when that is unset |
| `CODEX_API_KEY` | Secret | Codex (optional — explicit alternative to `OPENAI_API_KEY`) | Codex provider auth; an explicit value always wins over the `OPENAI_API_KEY` mapping |
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
USER runner
```

The base image runs as the non-root `runner` user, so switch to `root` to install packages and switch back when done.

The **Herd CLI** is not baked into the image — the entrypoint script that ships inside the published base image (`ghcr.io/herd-os/herd-runner-base`) downloads it at container startup. This ensures runners always use the latest version without rebuilding. Set `HERD_VERSION` in `.env` to pin a specific version.

The base image's entrypoint script handles runner lifecycle:
1. Downloads the herd binary (latest or pinned version)
2. Installs both supported agent CLIs via npm at container startup: Claude Code (`@anthropic-ai/claude-code`) and OpenCode (`opencode-ai`). Both are present in every runner regardless of which `agent.provider` the repo selects, so switching providers does not require rebuilding the image.
3. Removes stale config from previous runs (ephemeral runners leave `.runner` behind on restart)
4. Registers with GitHub using a short-lived registration token
5. Starts the runner in ephemeral mode (picks up one job, then deregisters)
6. On SIGTERM/SIGINT, deregisters cleanly

The `docker-compose.herd.yml` runs the worker service with `restart: always`, so after each job completes the container restarts and re-registers for the next job.

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

This is also automated. `herd init` installs `.github/workflows/herd-publish-runner.yml`, which builds and pushes the multi-arch consumer image (`ghcr.io/<owner>/<repo>-herd-runner:latest`) on every push to `main` that touches `Dockerfile.herd_runner`, or on manual `workflow_dispatch`. The job is gated on the `HERD_ENABLED` variable being `true` and requires `packages: write` permission (the default `GITHUB_TOKEN` is used to authenticate to GHCR).

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

### Concurrency control

`workers.max_concurrent` in `.herdos.yml` controls how many workers HerdOS dispatches simultaneously. This is independent of how many runners you have — if you have 5 runners but `max_concurrent: 3`, only 3 will be active at once.

### Runner labels

`workers.runner_label` in `.herdos.yml` must match the `RUNNER_LABELS` environment variable in `docker-compose.herd.yml`. Default is `herd-worker`. Use different labels to route heavy tasks to specific runners (e.g., `herd-gpu`).

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
| Runner exits after one job | Expected | Ephemeral mode — `docker-compose` restarts it automatically |
| "Must not run with sudo" | Running as root | The Dockerfile creates a non-root `runner` user — don't override `USER` |
| Agent not found | Not installed | Ensure the configured agent CLI is in the Docker image — the base image installs both (`npm install -g @anthropic-ai/claude-code` and `npm install -g opencode-ai`) |
| 403 on PR creation | Org setting | Enable "Allow GitHub Actions to create and approve pull requests" in org settings |
| 403 on listing PRs | Missing permission | Ensure `pull-requests: write` is in workflow permissions |
| Dispatch succeeds but no run appears | Missing secret | Add `HERD_GITHUB_TOKEN` as org/repo secret (see section 1) |
| Token permission errors | Insufficient scope | Fine-grained: needs Administration read/write. Classic: needs `repo` scope |
| Integrator crashes checking CI | Missing Statuses permission | Add **Statuses: Read** to fine-grained PAT, or set `require_ci: false` in `.herdos.yml` |
| Auth errors in worker | Missing credentials | Verify `.env` has the right key for the configured provider — Claude: `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`; OpenCode: `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` matching `agent.model`; Codex: `OPENAI_API_KEY` or `CODEX_API_KEY` |
