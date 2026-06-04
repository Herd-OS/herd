# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Codex provider (`agent.provider: codex`) — shells out to the OpenAI Codex CLI with API-key auth. `OPENAI_API_KEY` is auto-mapped to `CODEX_API_KEY` at invocation time when `CODEX_API_KEY` is unset; an explicit `CODEX_API_KEY` always wins. The worker workflow and docker exec forward both secrets. `agent.model` takes a **bare** model ID (e.g. `gpt-5-codex`, `gpt-5.2`), not a provider-prefixed form. New `agent.codex_reasoning_effort` config field (`minimal` | `low` | `medium` | `high`, default `medium`) maps to `-c model_reasoning_effort=<value>` on every Codex invocation.
- Codex ChatGPT-subscription auth (opt-in alternative to API-key auth). Personal subscriptions (Plus/Pro/Team/Edu) authenticate via a base64-encoded `CODEX_AUTH_JSON` (single replica) or `CODEX_AUTH_JSON_1` … `CODEX_AUTH_JSON_N` (multi-replica); ChatGPT Enterprise authenticates via an agent-identity JWT in `CODEX_ACCESS_TOKEN`. New `agent.codex_replicas` config field (integer, default `1`) generates per-replica `docker-compose.herd.yml` services (`herd-worker-1` … `herd-worker-N`) and named volumes (`codex-auth-1` … `codex-auth-N`); `workers.max_concurrent` must be `<= agent.codex_replicas` when subscription auth is in use. Provisioning seeds the docker-volume-backed `~/.codex` from the base64 env var, records a `.herd-seed` marker, and re-seeds automatically when the env value changes (no force-seed flag). A `herd codex keepalive-loop` background daemon (spawned by the runner entrypoint when any `CODEX_AUTH_JSON*` is set) keeps idle OAuth chains warm via a near-noop `codex exec` on a default 6-day cadence, tunable via `HERD_CODEX_KEEPALIVE_INTERVAL`.
- Published GHCR runner base image at `ghcr.io/herd-os/herd-runner-base` — public, multi-arch (linux/amd64, linux/arm64), version-pinned to the herd release.
- `herd image build` and `herd image publish` commands to build and push a customized runner image to `ghcr.io/<owner>/<repo>-herd-runner`.
- `.github/workflows/herd-publish-runner.yml` auto-publish workflow that builds and pushes the consumer runner image on changes to `Dockerfile.herd_runner` (gated on `HERD_ENABLED`, requires `packages: write`).

### Changed

- `herd init` no longer generates `Dockerfile.herd_runner_base`; an existing one is removed and the base service is dropped from `docker-compose.herd.yml`.
- `Dockerfile.herd_runner` now uses `FROM ghcr.io/herd-os/herd-runner-base:<version>` (pulled from GHCR) instead of the locally built `herd-runner-base`.
- `herd init` no longer generates `entrypoint.herd.sh` in consumer repos — the entrypoint is now baked into the published base image `ghcr.io/herd-os/herd-runner-base`. A leftover copy from an older init is removed automatically on the next `herd init`.

### Removed

- OpenCode subscription authentication paths (ChatGPT/Codex via `opencode-openai-codex-auth` and Anthropic via the `opencode-claude-auth` OAuth bridge). The OpenCode provider now supports only plain API-key auth (`ANTHROPIC_API_KEY` for `anthropic/*` models, `OPENAI_API_KEY` for `openai/*` models). The `claude` provider's `CLAUDE_CODE_OAUTH_TOKEN` auth is unchanged.
