# Spec: Codex provider ChatGPT subscription auth

Depends on `codex-provider-api-key.md`: the API-key codex provider must exist before this spec lands on top of it.

Add opt-in support for ChatGPT subscription authentication to the Codex provider, so users with ChatGPT Plus / Pro / Team / Edu / Enterprise subscriptions can drive herd workers without a per-token-billed `OPENAI_API_KEY`. The API-key path remains the documented default; subscription auth is opt-in via env vars.

OpenAI documents a CI/CD subscription flow at https://developers.openai.com/codex/auth/ci-cd-auth. The subscription path in this spec implements that flow on top of herd's runner architecture.

## Why this design works despite OAuth refresh-token rotation

OpenAI's OAuth refresh tokens rotate on use: each successful refresh returns a new `access_token` AND a new `refresh_token`, and the previous `refresh_token` is invalidated server-side. A naive "base64 the auth.json, ship it as a one-time seed into ephemeral containers" approach would fail at the first refresh because subsequent containers seeded from the same env var would receive an invalidated `refresh_token`.

Three facts make this tractable for herd:

1. **Runner containers are long-lived.** A herd runner container hosts the GitHub Actions self-hosted runner agent and is started with `docker run --restart unless-stopped` (or its docker-compose equivalent). The runner agent forks child processes per job; HOME is shared across all jobs in that container. `~/.codex/auth.json` written by Codex during one worker invocation persists naturally for the next invocation in the same container.

2. **A docker volume on `~/.codex/` survives container restarts.** Image updates, host reboots, manual restarts all preserve the rotated state.

3. **Multiple OAuth sessions per ChatGPT account are allowed.** A user can run `codex login` multiple times with `CODEX_HOME=~/codex-replica-N` to mint N independent credential pairs from a single account. Each has its own refresh chain. This is how a single Pro subscription scales to N parallel replicas — N independent seeds, N independent volumes, no cross-replica refresh-token races.

The "8 days" figure in OpenAI's docs ("if `last_refresh` is older than about 8 days, Codex refreshes the token bundle before the run continues") is a forced-refresh trigger that Codex applies on its end, NOT a chain-expiry deadline imposed by OpenAI's server. The refresh uses the current `refresh_token` to swap for a new bundle. As long as each refresh succeeds and the rotated state persists, the chain runs indefinitely.

The chain breaks reactively, not preventively — when something server-side invalidates it (password change, manual logout-everywhere, undocumented OpenAI session-expiry policy). Recovery is a one-time re-seed.

A background keepalive (described below) periodically triggers Codex's refresh logic so the chain stays warm during long idle periods (runner up but no workers dispatched).

## Three subscription paths

Three distinct paths ship in v1. Users pick whichever matches their subscription tier.

### Path 1: Personal subscription (Plus / Pro / Team / Edu)

Mechanism: portable `auth.json` per replica.

1. User runs `codex login` once per replica on a trusted local machine. For multi-replica setups, each invocation uses a distinct `CODEX_HOME` so the resulting `auth.json` files don't clobber each other.
2. Each `auth.json` is base64-encoded and assigned to an env var: `CODEX_AUTH_JSON` for single-replica, or `CODEX_AUTH_JSON_1`, `CODEX_AUTH_JSON_2`, ..., `CODEX_AUTH_JSON_N` for multi-replica.
3. herd's provisioning seeds each replica's docker-volume-backed `~/.codex/` from its env var on first start, then leaves the volume to Codex.
4. Codex refreshes inside each replica's volume; rotated tokens persist there.
5. herd's keepalive (per replica) triggers periodic refreshes so the chain doesn't age out during idle stretches.

Direct quotes from the OpenAI docs:

> "Codex loads the local auth cache from `auth.json`" — "Create `auth.json` once on a trusted machine... Put that file on the runner."

> "if `last_refresh` is older than about 8 days, Codex refreshes the token bundle before the run continues" — "after a successful refresh, Codex writes the new tokens... back to `auth.json`."

> "Use one `auth.json` per runner or per serialized workflow stream. Do not share the same file across concurrent jobs or multiple machines."

The last quote is why N replicas require N independent seeds: not "one machine, max one OAuth session," but "one OAuth session, max one concurrent consumer."

**Rate-limit caveat**: per-account rate limits cap throughput regardless of how many sessions are fanned out. N replicas don't multiply effective request rate; they distribute it across N worker processes. Useful when tasks are wall-clock-bound on non-LLM work (git, tests, file I/O); not useful when tasks are LLM-call-bound.

### Path 2: ChatGPT Enterprise (admin-minted access token)

Mechanism: long-lived bearer token via `CODEX_ACCESS_TOKEN`.

Enterprise admins enable Codex access tokens for the workspace. Permitted members mint an agent-identity JWT. The user sets `CODEX_ACCESS_TOKEN=<jwt>` in the runner env. No refresh dance, no concurrency caveat, no per-replica state.

The env var is verified in source (`codex-rs/login/src/auth/manager.rs:469`, consumed at `manager.rs:766`). It's resolved AFTER `CODEX_API_KEY` and the ephemeral store but BEFORE persistent `auth.json` storage, so setting `CODEX_ACCESS_TOKEN` overrides any seeded `auth.json`.

### Path 3: Device code (`codex login --device-auth`)

NOT a separate herd code path. This is just an alternative way for users on Path 1 to obtain `auth.json` when their trusted machine has no browser: `codex login --device-auth` prints a code and URL; the user visits the URL on a browser-equipped device, completes auth, and Codex writes `auth.json` as usual. From there, the Path 1 mechanism (base64 → env var → seed) takes over.

## File structure inside the runner container

### `~/.codex/auth.json` (verified shape — `codex-rs/login/src/auth/storage.rs:32`)

Complete schema from the Rust `AuthDotJson` struct:

```json
{
  "auth_mode": "chatgpt" | "apikey" | null,
  "OPENAI_API_KEY": "string" | null,
  "tokens": {
    "id_token": "<JWT>",
    "access_token": "<JWT>",
    "refresh_token": "<string>",
    "account_id": "<string>" | null
  } | null,
  "last_refresh": "2026-06-03T19:24:00Z" | null,
  "agent_identity": "<JWT>" | null
}
```

Notes:

- Field names are case-sensitive: `auth_mode` (lowercase), `OPENAI_API_KEY` (uppercase — the JSON key literally is `OPENAI_API_KEY`).
- `last_refresh` is serialized as a chrono `DateTime<Utc>` — ISO 8601 with timezone.
- File permissions: Codex writes the file at `0o600` (`storage.rs:147`). herd's provisioning code must match.
- For the subscription path, `auth_mode = "chatgpt"` + the `tokens` block is what gets read. The `OPENAI_API_KEY` field is independent of the OAuth flow and is only populated by `login_with_api_key`.

### `~/.codex/.herd-seed` (herd-written, never read by Codex)

Verbatim base64 string of the `CODEX_AUTH_JSON` env var at the time of last seed. Used by herd's provisioning logic to detect when the user has updated the env var and a re-seed is needed.

Permissions: `0o600`. Contents: the raw base64 string. Comparison is byte-equality.

### `~/.codex/config.toml` (optional, defense-in-depth)

`cli_auth_credentials_store` defaults to `File` mode in production builds (verified at `codex-rs/config/src/types.rs:89-99`, `#[default]` annotation on the `File` variant). So an absent `config.toml` resolves to file-backed credentials. herd writes:

```toml
cli_auth_credentials_store = "file"
```

Written by `provisionCodexAuth` only when the file is absent (no-clobber).

## herd integration

### Env vars (runner-side)

- `CODEX_AUTH_JSON` — single-replica seed: base64 of `auth.json`.
- `CODEX_AUTH_JSON_1`, `CODEX_AUTH_JSON_2`, ..., `CODEX_AUTH_JSON_N` — multi-replica per-replica seeds.
- `CODEX_ACCESS_TOKEN` — Enterprise; overrides any seeded `auth.json`.
- `CODEX_HOME` — optional; defaults to `~/.codex`.
- `HERD_CODEX_KEEPALIVE_INTERVAL` — optional duration string (e.g. `144h`); defaults to 6 days.

No `CODEX_AUTH_FORCE_SEED` env var. The diff-based provisioning makes it unnecessary: updating `CODEX_AUTH_JSON` is itself the signal to re-seed.

### Config field: `agent.codex_replicas`

In `internal/config/config.go`, add to the `Agent` struct:

```go
CodexReplicas int `yaml:"codex_replicas"`  // default 1; >1 generates N runner replicas
```

Validation in `internal/config/validate.go`:

- `agent.codex_replicas` must be ≥ 1.
- When > 1, only the Codex provider is supported. Other providers warn if set.
- `workers.max_concurrent` must be ≤ `agent.codex_replicas` when `agent.provider: codex` AND any `CODEX_AUTH_JSON*` env var is set (subscription mode). Otherwise multiple workers contend for one replica's seed.

### Provisioning helper (`internal/agent/codex/auth.go`)

Called at Execute/Plan/Review/Discuss time, gated by `sync.Once` to run once per process:

```go
func provisionCodexAuth() error {
    envSeed := strings.TrimSpace(os.Getenv("CODEX_AUTH_JSON"))
    if envSeed == "" {
        return nil  // no subscription auth requested
    }

    codexHome, err := resolveCodexHome()  // $CODEX_HOME or $HOME/.codex
    if err != nil { return err }

    seedFile := filepath.Join(codexHome, ".herd-seed")
    authFile := filepath.Join(codexHome, "auth.json")
    cfgFile  := filepath.Join(codexHome, "config.toml")

    existingSeed, err := os.ReadFile(seedFile)
    if err == nil && bytes.Equal(existingSeed, []byte(envSeed)) {
        // env unchanged since last seed; leave auth.json alone
        return ensureConfigToml(cfgFile)
    }

    // env changed (or first time): re-seed
    decoded, err := base64.StdEncoding.DecodeString(envSeed)
    if err != nil {
        return fmt.Errorf("CODEX_AUTH_JSON is not valid base64: %w", err)
    }
    if err := os.MkdirAll(codexHome, 0o700); err != nil { return err }
    if err := os.WriteFile(authFile, decoded, 0o600); err != nil { return err }
    if err := os.WriteFile(seedFile, []byte(envSeed), 0o600); err != nil { return err }
    return ensureConfigToml(cfgFile)
}

func ensureConfigToml(path string) error {
    if _, err := os.Stat(path); err == nil { return nil }  // exists, leave alone
    return os.WriteFile(path, []byte(`cli_auth_credentials_store = "file"`+"\n"), 0o600)
}
```

Lifecycle cases handled:

1. **First start, empty volume**: no `.herd-seed`, env populated → re-seed, write both files.
2. **Restart, same env**: `.herd-seed` matches env → no-op, Codex keeps using its rotated `auth.json`.
3. **User updated `CODEX_AUTH_JSON`**: `.herd-seed` mismatch → overwrite `auth.json` and `.herd-seed`.
4. **User unset `CODEX_AUTH_JSON`**: env empty → no-op; whatever's in the volume stays.

### Keepalive (`internal/cli/codex_keepalive.go`)

A `herd codex keepalive-loop` subcommand spawned by `entrypoint.herd.sh` when subscription mode is detected. Triggers Codex's own refresh logic periodically so an idle chain doesn't age out.

In `images/base/entrypoint.herd.sh`, after the runner agent registration and before `exec ./run.sh`:

```sh
# Keep the Codex OAuth chain warm if subscription auth is configured.
# Skipped for Enterprise (CODEX_ACCESS_TOKEN only — no refresh needed) and
# API-key (no expiry) setups.
if env | grep -q '^CODEX_AUTH_JSON'; then
  /opt/herd/bin/herd codex keepalive-loop \
    >>/var/log/herd-codex-keepalive.log 2>&1 &
fi
```

Subcommand implementation:

```go
func runKeepaliveLoop(ctx context.Context) {
    interval := 6 * 24 * time.Hour  // 6-day cadence, 2-day buffer before 8-day
    if v := os.Getenv("HERD_CODEX_KEEPALIVE_INTERVAL"); v != "" {
        if d, err := time.ParseDuration(v); err == nil && d > 0 {
            interval = d
        }
    }

    for {
        select {
        case <-ctx.Done():
            return
        case <-time.After(interval):
        }

        authFile := filepath.Join(codexHome(), "auth.json")
        data, err := os.ReadFile(authFile)
        if err != nil { continue }  // no auth yet

        var auth struct {
            AuthMode    *string    `json:"auth_mode"`
            LastRefresh *time.Time `json:"last_refresh"`
        }
        if err := json.Unmarshal(data, &auth); err != nil { continue }

        if auth.AuthMode == nil || *auth.AuthMode != "chatgpt" {
            continue  // not subscription mode; nothing to refresh
        }

        if auth.LastRefresh != nil && time.Since(*auth.LastRefresh) < interval - time.Hour {
            continue  // a worker beat us to it; chain is fresh
        }

        // Trigger Codex's own refresh by invoking a near-noop exec.
        // Codex's auth path handles the refresh transparently.
        // Cost: ~50 tokens; effectively $0 on a subscription.
        cmd := exec.CommandContext(ctx, "codex", "exec",
            "--ephemeral", "--skip-git-repo-check",
            "Reply with the single character 'k' and stop.")
        cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
        _ = cmd.Run()
    }
}
```

Why this design:

- The `codex exec` call triggers Codex's built-in refresh; herd does not reimplement the OAuth refresh protocol. This avoids tracking the Codex OAuth client ID, racing with Codex's own refresh logic, or duplicating retry/error handling.
- A goroutine inside the herd binary keeps the entrypoint simple (no cron daemon to add to the image, no platform-specific cron syntax).
- Each multi-replica container has its own keepalive loop on its own volume; no cross-replica coordination required.
- Graceful shutdown via context cancellation when the container stops.

Multi-replica edge cases:

- N replicas → N independent keepalives.
- Worker fires during a keepalive's sleep → worker triggers refresh normally; keepalive's next wake sees fresh `last_refresh` and skips. Convergent.
- Worker and keepalive race → Codex's file locking serializes refreshes; the losing party waits briefly.
- `codex exec` itself fails → logged, loop continues.

### Docker-exec env passthrough (`internal/cli/exec_docker.go`)

Update `passEnv` slice to include:

- `CODEX_API_KEY`
- `CODEX_AUTH_JSON`
- `CODEX_AUTH_JSON_1`, `CODEX_AUTH_JSON_2`, ..., `CODEX_AUTH_JSON_N` (up to a reasonable cap; e.g. 16)
- `CODEX_ACCESS_TOKEN`
- `CODEX_HOME`
- `HERD_CODEX_KEEPALIVE_INTERVAL`

### Worker workflow (`internal/cli/workflows/herd-worker.yml.tmpl`)

Add the env block entries that surface subscription secrets via GitHub Actions:

```yaml
CODEX_AUTH_JSON: ${{ secrets.CODEX_AUTH_JSON }}
CODEX_AUTH_JSON_1: ${{ secrets.CODEX_AUTH_JSON_1 }}
# ... up to CODEX_AUTH_JSON_N
CODEX_ACCESS_TOKEN: ${{ secrets.CODEX_ACCESS_TOKEN }}
```

> TODO(verify): mechanism for the worker workflow to know which replica it's running on. Likely via `RUNNER_NAME` or the runner-side env that distinguishes `herd-worker-1` from `herd-worker-2`. Each replica should see only ITS slot's secret to avoid leakage; alternative is to surface all of them and have the runner-side entrypoint pick. Confirm during implementation.

### docker-compose generation

`herd init` regenerates `docker-compose.herd.yml` based on `agent.codex_replicas`.

For N=1 (default), the existing single-service shape:

```yaml
services:
  herd-worker:
    image: ghcr.io/herd-os/herd-runner-base:latest
    environment:
      - CODEX_AUTH_JSON=${CODEX_AUTH_JSON:-}
      - CODEX_ACCESS_TOKEN=${CODEX_ACCESS_TOKEN:-}
      # ...other env...
    volumes:
      - codex-auth:/home/runner/.codex
volumes:
  codex-auth:
```

For N>1, generate N service blocks plus N named volumes:

```yaml
services:
  herd-worker-1:
    image: ghcr.io/herd-os/herd-runner-base:latest
    environment:
      - CODEX_AUTH_JSON=${CODEX_AUTH_JSON_1:-}
      - CODEX_ACCESS_TOKEN=${CODEX_ACCESS_TOKEN:-}
      # ...
    volumes:
      - codex-auth-1:/home/runner/.codex
  herd-worker-2:
    image: ghcr.io/herd-os/herd-runner-base:latest
    environment:
      - CODEX_AUTH_JSON=${CODEX_AUTH_JSON_2:-}
      # ...
    volumes:
      - codex-auth-2:/home/runner/.codex
  # ...
volumes:
  codex-auth-1:
  codex-auth-2:
  # ...
```

Each replica is a separately-registered self-hosted runner with a distinct GitHub Actions runner name. Each has its own credential AND its own persistent volume.

`internal/cli/runner/docker-compose.herd.yml.tmpl` takes a `Replicas int` field in its template data and generates the service/volume blocks accordingly.

### `.env.herd.example` documentation

Both root and `internal/cli/runner/.env.herd.example` get the Codex subscription block:

```bash
# Codex CLI subscription auth (opt-in alternative to OPENAI_API_KEY).
#
# Personal subscription (Plus/Pro/Team/Edu): base64-encoded auth.json from
# `codex login`. One env var per replica. Each replica needs an INDEPENDENT
# `codex login` invocation (multiple OAuth sessions per ChatGPT account are
# allowed).
#
# Single replica:
# CODEX_AUTH_JSON=
#
# Multi-replica (set agent.codex_replicas: N in .herdos.yml):
# CODEX_AUTH_JSON_1=
# CODEX_AUTH_JSON_2=
# CODEX_AUTH_JSON_3=
#
# Enterprise admin-minted access token (no refresh dance, no concurrency
# caveat — recommended if you have ChatGPT Enterprise):
# CODEX_ACCESS_TOKEN=
#
# Optional: override keepalive interval (defaults to 6 days)
# HERD_CODEX_KEEPALIVE_INTERVAL=144h
```

## User workflow

### Path A: ChatGPT Enterprise

1. Admin enables Codex access tokens for the workspace in the ChatGPT Enterprise admin console.
2. User mints an agent identity JWT via the admin console flow.
3. Set `CODEX_ACCESS_TOKEN=<jwt>` in the runner `.env` and as a repo/org secret.
4. Set `.herdos.yml`:
   ```yaml
   agent:
     provider: codex
     model: gpt-5-codex
   ```
5. Done. Multi-replica works without per-replica setup (no refresh = no race).

### Path B: Personal subscription (Plus / Pro / Team / Edu), single replica

1. On a trusted local machine:
   ```bash
   codex login
   # Interactive browser OAuth dance.
   # If no browser available: codex login --device-auth
   ```
2. Encode:
   ```bash
   base64 -w0 ~/.codex/auth.json
   ```
3. Set the value as `CODEX_AUTH_JSON` in the runner `.env` and as a repo/org secret.
4. Set `.herdos.yml`:
   ```yaml
   agent:
     provider: codex
     model: gpt-5-codex
   ```
5. `herd init` to regenerate `docker-compose.herd.yml` with the volume mount and env passthrough.
6. Start the runner. First worker invocation seeds `auth.json` from the env var; subsequent invocations use the persisted file. Refresh happens in place, indefinitely. Background keepalive prevents idle chains from aging out.

### Path C: Personal subscription, N parallel replicas

Same as Path B with these differences:

1. Run `codex login` N times with `CODEX_HOME` per invocation:
   ```bash
   for i in 1 2 3; do
     CODEX_HOME=~/codex-replica-$i codex login
   done
   ```
2. Encode each:
   ```bash
   for i in 1 2 3; do
     echo "CODEX_AUTH_JSON_$i=$(base64 -w0 ~/codex-replica-$i/auth.json)"
   done >> .env
   ```
3. Set `agent.codex_replicas: 3` in `.herdos.yml`.
4. `herd init` regenerates `docker-compose.herd.yml` with three services and three volumes.
5. Start the runners. Each replica seeds its own volume from its own env var.

Throughput: ChatGPT Pro/Team rate limits are per-account. N replicas don't multiply LLM-call throughput; they help only when tasks are wall-clock-bound on non-LLM work.

### Re-seeding (when the chain breaks)

The chain breaks server-side (password change, manual logout-everywhere, OpenAI session-expiry policy). herd surfaces an auth error from the failing worker.

1. User runs `codex login` locally (N times if multi-replica).
2. Updates `CODEX_AUTH_JSON` / `CODEX_AUTH_JSON_N` env vars with the new base64.
3. Restarts the runner(s).
4. Provisioning detects the `.herd-seed` mismatch on first agent use → overwrites `auth.json` with the new seed.

## Documentation updates required

The implementation is incomplete without these docs landing alongside the code:

- `docs/runners.md`: under the existing Codex provider section (added by the API-key spec), add a "Subscription (opt-in)" sub-section with three sub-subsections:
  - **Path A: ChatGPT Enterprise** — the `CODEX_ACCESS_TOKEN` setup. Note this is the cleanest headless path (no refresh dance, multi-replica safe).
  - **Path B: Personal subscription (single replica)** — the `codex login` + base64 + `CODEX_AUTH_JSON` flow. Document the docker-volume requirement and the keepalive.
  - **Path C: Personal subscription (N parallel replicas)** — the N-times-`codex login` pattern, `agent.codex_replicas: N`, the rate-limit caveat.
  - **Recovery runbook**: how to re-seed when the chain breaks (update env var + restart; no force-seed flag needed).
  - **Keepalive description**: how it works, default cadence, how to tune via `HERD_CODEX_KEEPALIVE_INTERVAL`, where logs go.
  - Include both OpenAI-mandated warnings: "Treat `~/.codex/auth.json` like a password... Do not use this workflow for public or open-source repositories." and "Use one `auth.json` per runner or per serialized workflow stream."
- `docs/configuration.md`: document `agent.codex_replicas` (integer, default 1). Note that values > 1 generate per-replica services in `docker-compose.herd.yml` and require N independent `CODEX_AUTH_JSON_<i>` env vars.
- `docs/installation.md`: no change.
- `CHANGELOG.md`: add an entry under "Added" describing the Codex subscription auth paths (Plus/Pro/Team/Edu via `CODEX_AUTH_JSON`, Enterprise via `CODEX_ACCESS_TOKEN`), the `agent.codex_replicas` config field, and the keepalive mechanism.

## Final cleanup

After the acceptance criteria are met and all PR review is complete, delete this spec file (`specs/codex-provider-chatgpt-subscription.md`) as the final task in the batch. The spec served its purpose as a planning artifact; the docs and code are the durable record.

## ToS considerations

The subscription path is OpenAI-documented for CI/CD use. The docs caveat it as a non-default ("API keys are still the recommended option for most CI/CD jobs") but don't restrict the pattern. Warnings to surface in herd's docs:

- "Treat `~/.codex/auth.json` like a password... Do not use this workflow for public or open-source repositories."
- "Use one `auth.json` per runner or per serialized workflow stream. Do not share the same file across concurrent jobs or multiple machines." (herd's multi-replica setup satisfies this because each replica has its own seed AND its own volume.)

## Tests

- `TestProvisionCodexAuth_NoEnvNoOp` — `CODEX_AUTH_JSON` empty → no files written.
- `TestProvisionCodexAuth_FirstSeed` — empty volume + env populated → writes `auth.json` (0600), `.herd-seed` (0600), `config.toml`.
- `TestProvisionCodexAuth_RestartUnchangedEnv` — `.herd-seed` matches env → `auth.json` unchanged.
- `TestProvisionCodexAuth_DetectsSeedChange` — pre-existing `auth.json` + `.herd-seed` with stale content, env has new content → both files overwritten with new content.
- `TestProvisionCodexAuth_InvalidBase64` — env var contains junk → returns base64 decode error; no files written.
- `TestProvisionCodexAuth_HonorsCodexHome` — `CODEX_HOME` env var respected as the base path.
- `TestProvisionCodexAuth_ConfigTomlNoClobber` — existing `config.toml` preserved; missing one is created with `cli_auth_credentials_store = "file"`.
- `TestProvisionCodexAuth_PermsAreCorrect` — `auth.json` is 0600, `.herd-seed` is 0600, the codex home dir is 0700.
- `TestValidate_CodexReplicasMinimum` — `agent.codex_replicas < 1` fails validation.
- `TestValidate_MaxConcurrentBoundByReplicas` — `workers.max_concurrent > agent.codex_replicas` is rejected when in subscription mode (`CODEX_AUTH_JSON*` env var present).
- `TestRenderDockerCompose_SingleReplica` — N=1 produces a single service + single volume.
- `TestRenderDockerCompose_MultiReplica` — N=3 produces three services + three volumes + three distinct env var refs.
- `TestKeepalive_SkipsWhenAuthJsonMissing` — no `auth.json` on disk → loop continues without invoking `codex`.
- `TestKeepalive_SkipsWhenAuthModeApiKey` — `auth.json` has `auth_mode: "apikey"` → loop skips refresh.
- `TestKeepalive_SkipsWhenLastRefreshFresh` — `last_refresh` within the interval → loop skips.
- `TestKeepalive_TriggersWhenLastRefreshStale` — `last_refresh` older than interval → loop invokes `codex exec`.
- `TestKeepalive_ExitsOnContextCancel` — context cancellation exits cleanly.
- `TestKeepalive_IntervalOverride` — `HERD_CODEX_KEEPALIVE_INTERVAL=1m` honored; invalid values fall back to default.
- `TestEntrypoint_SpawnsKeepaliveWhenSubscriptionEnvSet` — textual check on `entrypoint.herd.sh`: the `env | grep -q '^CODEX_AUTH_JSON'` block exists and points at `/opt/herd/bin/herd codex keepalive-loop`.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run`, `gofmt -l` all pass.
- With `agent.provider: codex` and only `CODEX_ACCESS_TOKEN` set, a worker authenticates and runs Execute end-to-end against an Anthropic model.
- With `agent.provider: codex` and only `CODEX_AUTH_JSON` set (no `OPENAI_API_KEY`, no `CODEX_ACCESS_TOKEN`), a worker authenticates and runs Execute end-to-end on a fresh volume. Restarting the container reuses the persisted `auth.json` without re-seeding.
- Updating `CODEX_AUTH_JSON` to a new value and restarting the runner re-seeds the volume automatically (no force-seed flag).
- With `agent.codex_replicas: 3` and three distinct `CODEX_AUTH_JSON_1/2/3` env vars, `herd init` generates three service blocks + three named volumes in `docker-compose.herd.yml`, each replica registers as a distinct runner, each is independently authenticated.
- The entrypoint spawns the keepalive only when at least one `CODEX_AUTH_JSON*` env var is set. API-key-only and Enterprise-only setups do not spawn the loop.
- Validation rejects `agent.provider: codex` + `CODEX_AUTH_JSON` set + `workers.max_concurrent > agent.codex_replicas`.
- `herd init --check` passes (no template drift).

## TODO(verify) — confirm during implementation

- **Concrete chain-break failure shape**: when the refresh chain breaks server-side, what does Codex output? Is the exit code distinguishable from a transient failure? Affects the worker-report wording.
- **Per-account rate limit behavior**: characterize Pro/Team rate limit behavior — do 429s appear that the worker can retry, or are they hard failures? Affects retry logic and worker-report wording.
- **Worker workflow per-replica secret routing**: how the worker workflow knows which `CODEX_AUTH_JSON_N` to surface for a specific replica's runner. Likely via `RUNNER_NAME` or runner-label conventions.
- **Multi-OAuth-session limits per ChatGPT account**: empirically, multiple sessions per account are allowed (web + mobile + desktop simultaneously). Confirm that programmatic `codex login` invocations from a single machine produce distinct, non-invalidating sessions.

## References

- OpenAI Codex CI/CD auth: https://developers.openai.com/codex/auth/ci-cd-auth
- General auth docs: https://developers.openai.com/codex/auth
- Non-interactive mode: https://developers.openai.com/codex/noninteractive
- Codex repository: https://github.com/openai/codex (specifically `codex-rs/login/` for auth, `codex-rs/config/` for config types, `codex-rs/exec/src/cli.rs` for CLI surface)

## Related spec

- `specs/codex-provider-api-key.md` — the prerequisite API-key Codex provider.
