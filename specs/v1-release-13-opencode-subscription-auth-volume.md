# OpenCode Subscription Auth Volume

## Purpose

Revisit OpenCode subscription authentication now that OpenCode has first-class credential storage and documented subscription auth paths.

Herd previously removed OpenCode subscription auth bridges and documented OpenCode as API-key-only. That removal still made sense for the old bridge-based implementation, but current upstream OpenCode supports durable credentials in `~/.local/share/opencode/auth.json`. Herd should support this native auth path through a persistent Docker volume, similarly to the Codex `codex-auth` volume.

## Background

OpenCode's current docs say provider credentials configured through `/connect` or `opencode auth login` are stored in:

```text
~/.local/share/opencode/auth.json
```

OpenCode's current source also supports reading auth from `OPENCODE_AUTH_CONTENT`, but that should not be Herd's primary subscription path because refreshes are written back to `auth.json`. If Herd injects stale JSON through an environment variable, a worker process can keep reading old credentials even after OpenCode refreshes the file.

The durable runner shape should therefore be:

- Mount a persistent volume at `/home/runner/.local/share/opencode`.
- Run OpenCode login inside the already-running worker container as the runner user.
- Let OpenCode own refresh/writeback to `auth.json`.
- Keep environment-variable auth only for API-key users.

## Important Boundary

This work should not claim Google subscription support through OpenCode.

Current research shows:

- OpenCode documents subscription-style auth for ChatGPT Plus/Pro, GitHub Copilot, and GitLab Duo.
- OpenCode's Google provider uses API-key style auth through `GOOGLE_GENERATIVE_AI_API_KEY` / Google API credentials.
- The OpenCode issue about adding Antigravity/Google subscription support was closed after noting that the SDK path is separate billing/API-key based.

Google personal-account or Code Assist subscription auth should stay in the Antigravity/Gemini provider research track, not this OpenCode support track.

## Proposed Runner Changes

Update generated runner compose output to include an OpenCode auth volume:

```yaml
services:
  worker:
    volumes:
      - codex-auth:/home/runner/.codex
      - opencode-auth:/home/runner/.local/share/opencode

volumes:
  codex-auth:
  opencode-auth:
```

The mount should be harmless for API-key-only OpenCode users. Docker will create it lazily, and OpenCode will only populate it when a user logs in.

If direct `docker run` examples are documented, include:

```bash
-v opencode-auth:/home/runner/.local/share/opencode
```

## Login Workflow

Document the primary login flow against a running worker:

```bash
docker exec -it -u runner <worker-container> opencode auth login
```

Document provider-specific examples where useful:

```bash
docker exec -it -u runner <worker-container> \
  opencode auth login --provider openai --method "ChatGPT Pro/Plus (headless)"
```

```bash
docker exec -it -u runner <worker-container> \
  opencode auth login --provider github-copilot
```

The docs should tell users to restart workers after first login if a running job has already failed due to missing auth. Otherwise the next OpenCode invocation should read the volume-backed file naturally.

## Auth Precedence

Define and document Herd's intended OpenCode auth precedence:

1. Explicit provider environment variables used by OpenCode or the provider SDK, such as `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `GOOGLE_GENERATIVE_AI_API_KEY`.
2. OpenCode's volume-backed `~/.local/share/opencode/auth.json`.
3. No auth, which should fail with a clear error from OpenCode/Herd.

Do not set `OPENCODE_AUTH_CONTENT` automatically from Herd.

If a project sets both an API key and a subscription credential for the same provider, Herd should document that OpenCode/provider behavior may prefer the env key. If feasible, add `herd doctor` diagnostics that call this out explicitly.

## Doctor Support

Add or extend provider diagnostics so users can quickly see whether OpenCode subscription auth is usable inside the runner.

The diagnostic should check:

- OpenCode binary exists.
- Configured `agent.provider` is `opencode` when relevant.
- Configured `agent.model` is in provider/model form.
- Provider prefix inferred from `agent.model`, such as `openai`, `github-copilot`, `anthropic`, or `google`.
- `~/.local/share/opencode/auth.json` exists and is readable.
- The auth file has an entry for the inferred provider when subscription auth is expected.
- Relevant API-key environment variables are present when API-key auth is expected.
- An API-key environment variable appears to shadow a volume-backed subscription credential.
- The OpenCode auth volume is mounted in the generated compose file.

The output should avoid printing secret values.

## Tests

Add tests for:

- Generated `docker-compose.herd.yml` includes exactly one `opencode-auth` volume and one mount.
- Compose override merge still works with the new volume.
- Direct Docker documentation examples include the OpenCode auth mount.
- Default rendering remains stable except for the intentional volume addition.
- OpenCode docs mention subscription login without claiming Google subscription support.
- Doctor detects absent auth file.
- Doctor detects unreadable or invalid `auth.json`.
- Doctor detects a provider entry matching `agent.model`.
- Doctor warns when `OPENAI_API_KEY` shadows an OpenCode `openai` OAuth credential.
- Doctor does not print token, key, refresh, or access values from `auth.json`.

Use fake auth files in tests. Do not require real OpenCode network auth in unit tests.

## Acceptance Criteria

- Herd-generated runner compose mounts `opencode-auth` at `/home/runner/.local/share/opencode`.
- Docs explain how to authenticate OpenCode subscriptions inside a running worker container.
- Docs clearly state that OpenCode Google/Gemini remains API-key based and does not provide Google subscription support.
- Herd does not inject `OPENCODE_AUTH_CONTENT` for subscription auth.
- Users can diagnose missing or shadowed OpenCode auth without inspecting Docker volumes manually.
- Existing API-key OpenCode users continue working.
- The final implementation deletes this spec file after updating durable docs.

## Open Questions

- Should `herd doctor` grow provider-specific subcommands such as `herd opencode doctor`, or should this be folded into the broader pre-v1 `herd doctor` work?
- Should Herd add an OpenCode keepalive similar to Codex, or is OpenCode's refresh behavior reliable enough without it?
- Should Herd warn when multiple workers share one OpenCode subscription auth file, or is this safe enough for OpenCode's supported subscription providers?
- Should the generated compose file always mount `opencode-auth`, or should it be conditional on `agent.provider: opencode`?
