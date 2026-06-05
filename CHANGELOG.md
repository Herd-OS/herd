# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Runtime-configurable runner UID/GID via `RUNNER_UID` and `RUNNER_GID` env vars. The base image entrypoint now starts as root, optionally remaps the in-container `runner` user to a caller-specified UID/GID (recursive chown of `/home/runner`, `/runner`, `/opt/herd` happens once, on the first start with a new UID), then drops privileges via `gosu` before invoking the GitHub Actions runner. Lets operators match host UID/GID for bind mounts (e.g. TrueNAS SCALE's `apps` user at 568:568) without rebuilding the image — set `RUNNER_UID=568` / `RUNNER_GID=568` in `.env` and restart. Defaults remain `1000:1000`, so existing setups are unchanged. **Migration:** to opt in, ensure `Dockerfile.herd_runner` does **not** end with `USER runner` (older `herd init`-generated wrappers added this); the entrypoint detects a non-root start and skips the remap entirely to preserve backward compatibility. `RUNNER_UID=0` / `RUNNER_GID=0` are rejected — the GitHub Actions runner refuses to run as root.
- Codex provider (`agent.provider: codex`) — shells out to the OpenAI Codex CLI with API-key auth. `OPENAI_API_KEY` is auto-mapped to `CODEX_API_KEY` at invocation time when `CODEX_API_KEY` is unset; an explicit `CODEX_API_KEY` always wins. The worker workflow and docker exec forward both secrets. `agent.model` takes a **bare** model ID (e.g. `gpt-5-codex`, `gpt-5.2`), not a provider-prefixed form. New `agent.codex_reasoning_effort` config field (`minimal` | `low` | `medium` | `high`, default `medium`) maps to `-c model_reasoning_effort=<value>` on every Codex invocation.
- Codex ChatGPT-subscription auth (opt-in alternative to API-key auth). Personal subscriptions (Plus/Pro/Team/Edu) authenticate via a single base64-encoded `CODEX_AUTH_JSON`, with parallelism via `docker compose up -d --scale worker=N` (all worker containers share one `codex-auth` docker volume); ChatGPT Enterprise authenticates via an agent-identity JWT in `CODEX_ACCESS_TOKEN`. Provisioning seeds the docker-volume-backed `~/.codex` from the base64 env var, records a `.herd-seed` marker, and re-seeds automatically when the env value changes (no force-seed flag). A `herd codex keepalive-loop` background daemon (spawned by the runner entrypoint when `CODEX_AUTH_JSON` is set) keeps idle OAuth chains warm via a near-noop `codex exec` on a default 6-day cadence, tunable via `HERD_CODEX_KEEPALIVE_INTERVAL`.
- Published GHCR runner base image at `ghcr.io/herd-os/herd-runner-base` — public, multi-arch (linux/amd64, linux/arm64), version-pinned to the herd release.
- `herd image build` and `herd image publish` commands to build and push a customized runner image to `ghcr.io/<owner>/<repo>-herd-runner`.
- `.github/workflows/herd-publish-runner.yml` auto-publish workflow that builds and pushes the consumer runner image on changes to `Dockerfile.herd_runner` (gated on `HERD_ENABLED`, requires `packages: write`).

### Changed

- `herd init` no longer generates `Dockerfile.herd_runner_base`; an existing one is removed and the base service is dropped from `docker-compose.herd.yml`.
- `Dockerfile.herd_runner` now uses `FROM ghcr.io/herd-os/herd-runner-base:<version>` (pulled from GHCR) instead of the locally built `herd-runner-base`.
- `herd init` no longer generates `entrypoint.herd.sh` in consumer repos — the entrypoint is now baked into the published base image `ghcr.io/herd-os/herd-runner-base`. A leftover copy from an older init is removed automatically on the next `herd init`.
- AI provider auth env vars (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`, `CODEX_ACCESS_TOKEN`, `GEMINI_API_KEY`) are no longer surfaced from GitHub Actions secrets in the worker workflow template — they live only in the runner's `.env` (injected by `docker-compose`), matching the architecturally-recommended setup. A workflow `env:` block overrides container env unconditionally, so an unset secret would clobber the real `.env` value at the step level; removing the secrets path eliminates that footgun (generalizing the `CODEX_AUTH_JSON` protection from #694). **Migration:** users who previously configured these values **only** as GitHub Actions secrets (without `.env`) must add the same values to the runner's `.env` for workers to authenticate. `HERD_GITHUB_TOKEN` and `workers.extra_env` secrets are unaffected.

### Removed

- Multi-replica Codex ChatGPT-subscription auth (`agent.codex_replicas`, `CODEX_AUTH_JSON_1`..`N` env vars, per-replica `docker-compose.herd.yml` services and volumes). Subscription users now run a single `codex login`, set `CODEX_AUTH_JSON`, and scale parallelism via `docker compose up -d --scale worker=N` — all worker containers share a single `codex-auth` docker volume. The single-`CODEX_AUTH_JSON` + `CODEX_ACCESS_TOKEN` (Enterprise) + API-key paths are unchanged.
- OpenCode subscription authentication paths (ChatGPT/Codex via `opencode-openai-codex-auth` and Anthropic via the `opencode-claude-auth` OAuth bridge). The OpenCode provider now supports only plain API-key auth (`ANTHROPIC_API_KEY` for `anthropic/*` models, `OPENAI_API_KEY` for `openai/*` models). The `claude` provider's `CLAUDE_CODE_OAUTH_TOKEN` auth is unchanged.
