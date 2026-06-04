# Spec: Codex provider (API-key auth)

Add OpenAI's official Codex CLI (`@openai/codex`) as a peer agent provider next to `claude` and `opencode` in herd. This spec covers only the API-key authentication path (`OPENAI_API_KEY` → `CODEX_API_KEY`). The subscription path (ChatGPT Plus/Pro/Team/Edu/Enterprise) is covered by `codex-provider-chatgpt-subscription.md` and depends on this spec.

## Scope

In scope:

- New agent package `internal/agent/codex/` with `Execute`, `Plan`, `Review`, `Discuss` methods.
- Factory registration so `agent.provider: codex` resolves to the new package.
- Config validation accepts `"codex"` in the agent provider enum.
- Install `@openai/codex` in `images/base/Dockerfile` at a pinned version.
- Docs updates for the new provider (configuration.md, runners.md).
- Tests covering each method, headless flag selection, output parsing, and the structured-output path.

Out of scope:

- Any subscription auth (covered by the sibling spec).
- Custom sandbox tuning beyond what's required for headless worker invocations.
- The Codex GitHub Action (`openai/codex-action`) — separate effort.

## Codex CLI surface area

All flags and behaviors below are verified against the published source:

- `github.com/openai/codex` (Rust source under `codex-rs/exec/src/cli.rs` and `codex-rs/utils/cli/src/shared_options.rs`).
- Official docs at `developers.openai.com/codex/auth` and `developers.openai.com/codex/noninteractive`.
- npm package `@openai/codex` (wrapper that delegates to the platform-specific Rust binary, e.g. `@openai/codex-linux-x64`).

### Subcommand and headless invocation

`codex exec [OPTIONS] [PROMPT]` is the non-interactive mode. From the docs: "Non-interactive mode lets you run Codex from scripts (for example, continuous integration (CI) jobs) without opening the interactive TUI. You invoke it with `codex exec`."

Interactive TUI (used by `Discuss`) is `codex` with no subcommand.

### Headless flag set used by herd

| Flag | Purpose | Notes |
|------|---------|-------|
| `--sandbox workspace-write` | Allow file edits in cwd without approval prompts | Replaces the deprecated `--full-auto`. The runner container is the outer sandbox; `workspace-write` constrains edits to the worker checkout. |
| `--skip-git-repo-check` | Run outside a git repo | Defensive; workers always run in a checked-out batch branch, but tests / dev runs may not. |
| `--ephemeral` | Don't persist session rollout files | Worker containers are throwaway; no point writing session state. |
| `--ignore-user-config` | Skip `$CODEX_HOME/config.toml` | Hermetic invocation; herd controls the config that matters. |
| `--json` | Emit JSONL events to stdout | Used when herd needs to observe events (tool calls, token usage). Optional. |
| `--output-last-message <FILE>` (`-o`) | Write final agent message to a file | Used by `Execute` to capture the response without parsing JSONL. |
| `--output-schema <FILE>` | Constrain final response to a JSON Schema | Used by `Plan` and `Review` to get structured output natively. |
| `--model <ID>` (`-m`) | Model selection (global arg per `cli.rs`) | Mapped from `agent.model` in `.herdos.yml`. |
| `-c key=value` | Generic config override | Used for reasoning effort: `-c model_reasoning_effort=high`. Accepts `minimal | low | medium | high`. |

`--full-auto` is a "compatibility trap" (per `cli.rs:43-50` — `hide = true`, conflicts with `dangerously_bypass_approvals_and_sandbox`). It still parses but emits a deprecation warning. Use `--sandbox workspace-write` exclusively.

### Sandbox modes

- `read-only` (default for `codex exec`): agent cannot edit files, no approval prompts because nothing destructive is possible.
- `workspace-write`: edits allowed in cwd, no approval prompts.
- `danger-full-access`: full host access, no sandbox.
- `dangerously-bypass-approvals-and-sandbox` (top-level arg): fully off.

For workers running inside a herd-runner container, `workspace-write` is the correct level.

### Output formats

**Default (no `--json`)**: progress streams to stderr; only the final agent message prints to stdout. Pipeable as text.

**`--json`**: JSONL events to stdout. Event types observed in the docs:
- `thread.started` — session opening
- `turn.started` / `turn.completed` / `turn.failed` — per turn lifecycle (`turn.completed` includes `usage`)
- `item.completed` — content items (`agent_message` text, reasoning, command executions, file changes)

Example final events:
```json
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"..."}}
{"type":"turn.completed","usage":{"input_tokens":24763,"output_tokens":122}}
```

**`--output-last-message <FILE>`**: just the final agent message (text only). Simpler than parsing JSONL for the common Execute case.

**`--output-schema <FILE>`**: Codex constrains its final response to a JSON Schema. Use this for `Plan` and `Review` instead of prompt-engineering the agent to write JSON to a path.

> TODO(verify): the JSON Schema dialect `--output-schema` accepts (draft-07 vs 2020-12). Not specified in the source. The OpenAI Responses API canonical is JSON Schema 2020-12 with the constrained subset (`additionalProperties: false` at every object level, all properties required, etc.). Test with a representative plan schema during implementation; iterate.

## Auth

Codex's main auth resolution (verified in `codex-rs/login/src/auth/manager.rs:467-490, 738-770`) reads three env vars, in this precedence order:

1. `CODEX_API_KEY` — highest precedence; used directly as the API key when set.
2. Ephemeral in-memory store — for in-process ChatGPT tokens (not used by herd directly).
3. `CODEX_ACCESS_TOKEN` — Enterprise agent-identity JWT (covered by the subscription spec).
4. Persistent storage (`$CODEX_HOME/auth.json` per `cli_auth_credentials_store_mode`).

**`OPENAI_API_KEY` is NOT in Codex's main auth path.** It exists in source (`read_openai_api_key_from_env`) but is only consumed by `realtime_conversation.rs` (voice/RT API, irrelevant to herd). Setting `OPENAI_API_KEY` alone will leave Codex unauthenticated.

### Required behavior in herd's codex agent package

- When spawning the `codex` binary, the codex agent constructs the child env by:
  1. Inheriting the parent env.
  2. If `CODEX_API_KEY` is empty AND `OPENAI_API_KEY` is set, set `CODEX_API_KEY=$OPENAI_API_KEY` in the child env.
  3. Leave existing `CODEX_API_KEY` alone (user-explicit setting wins).
- This mapping is purely runtime; herd does not modify the parent process env.
- `internal/cli/exec_docker.go` `passEnv` slice must include BOTH `CODEX_API_KEY` and `OPENAI_API_KEY` (and the existing entries). Explicit setters of either env var get it surfaced to the container.
- `internal/cli/workflows/herd-worker.yml.tmpl` env block must include both.

## Code structure

New package layout:

```
internal/agent/codex/
├── codex.go              // CodexAgent struct, NewAgent constructor, env mapping helpers
├── execute.go            // Execute method
├── plan.go               // Plan method
├── review.go             // Review method
├── discuss.go            // Discuss method (interactive TUI, stdio passthrough)
├── schemas/
│   ├── plan.json         // JSON Schema for Plan output (embedded via embed.FS)
│   └── review.json       // JSON Schema for Review output (embedded via embed.FS)
├── codex_test.go         // shared fixture helpers + provider-level tests
├── execute_test.go
├── plan_test.go
├── review_test.go
└── discuss_test.go
```

Other files:

- `internal/agent/factory/factory.go` — add `case "codex": return codex.NewAgent(cfg.Binary, cfg.Model)`.
- `internal/config/validate.go` — add `"codex"` to the agent.provider allowed list.
- `images/base/Dockerfile` — add `&& npm install -g --no-audit --no-fund @openai/codex@<pinned-version>` to the existing agent install RUN block.
- `internal/cli/exec_docker.go` — add `CODEX_API_KEY` to `passEnv`.
- `internal/cli/workflows/herd-worker.yml.tmpl` — add `CODEX_API_KEY` to the env block.
- `docs/configuration.md` — document `codex` as a provider, document the model field semantics (bare IDs like `gpt-5-codex`).
- `docs/runners.md` — short Codex provider section: set `OPENAI_API_KEY` (or `CODEX_API_KEY`); herd maps automatically.

> TODO(verify): the pinned version. Use `npm view @openai/codex version` at implementation time to pick the current release. Mark with a `TODO(verify)` comment next to the install line.

## Invocation patterns per method

### Execute

```sh
codex exec \
  --sandbox workspace-write \
  --skip-git-repo-check \
  --ephemeral \
  --ignore-user-config \
  --model <agent.model> \
  -c model_reasoning_effort=<derived-or-medium> \
  --output-last-message /tmp/codex-out.txt \
  "<task prompt>"
```

Read final message from `/tmp/codex-out.txt`. Parse exit code; non-zero is an error.

### Plan

```sh
codex exec \
  --sandbox workspace-write \
  --skip-git-repo-check \
  --ephemeral \
  --ignore-user-config \
  --model <agent.model> \
  --output-schema /tmp/plan-schema.json \
  --output-last-message /tmp/plan.json \
  "<planner prompt>"
```

Write `plan-schema.json` from the embedded `schemas/plan.json`. Read `plan.json` after invocation; unmarshal into `agent.Plan`.

### Review

Same shape as Plan, with `schemas/review.json` and unmarshaling into `agent.ReviewResult`.

### Discuss

Interactive TUI:

```sh
codex --model <agent.model>
```

Wire stdin/stdout/stderr directly to the user's terminal. No output parsing.

## Plan schema

Schema must match the Go struct in `internal/agent/agent.go`:

```go
type Plan struct {
    BatchName string        `json:"batch_name"`
    Tasks     []PlannedTask `json:"tasks"`
}
type PlannedTask struct {
    Title                   string   `json:"title"`
    Description             string   `json:"description"`
    ImplementationDetails   string   `json:"implementation_details"`
    AcceptanceCriteria      []string `json:"acceptance_criteria"`
    Scope                   []string `json:"scope"`
    Conventions             []string `json:"conventions"`
    ContextFromDependencies []string `json:"context_from_dependencies"`
    Complexity              string   `json:"complexity"`  // "low", "medium", "high"
    Type                    string   `json:"type"`        // "feature", "bugfix"
    RunnerLabel             string   `json:"runner_label"`
    DependsOn               []int    `json:"depends_on"`
    Manual                  bool     `json:"manual"`
}
```

Translate this to a JSON Schema 2020-12 document with strict-mode constraints (`additionalProperties: false`, all properties required at every level — OpenAI structured output requirements). Place at `internal/agent/codex/schemas/plan.json` and embed via `embed.FS`.

## Review schema

Match the Go struct:

```go
type ReviewResult struct {
    Approved bool            `json:"approved"`
    Findings []ReviewFinding `json:"findings"`
    Comments []string        `json:"comments"`  // Deprecated; backfilled from Findings by herd
    Summary  string          `json:"summary"`
}
type ReviewFinding struct {
    Severity    string `json:"severity"`     // "HIGH", "MEDIUM", "LOW" (uppercase)
    Description string `json:"description"`
}
```

The schema should NOT require `comments` — Codex emits findings only; the herd parsing layer fills `comments` from `findings[].description` for legacy callers.

## Reasoning effort

Codex has no dedicated `--reasoning-effort` flag. Reasoning effort is set via the generic config override:

```
-c model_reasoning_effort=high
```

Accepted values per source (`codex-rs/config/src/config_toml.rs:337` + edit_tests.rs fixtures): `minimal`, `low`, `medium`, `high`.

Add a herd config field `agent.codex_reasoning_effort` (string, accepts the four values above; defaults to `medium`). The codex agent package plumbs it through to the `-c` flag on every invocation.

> TODO(verify): the exact tier names accepted (`minimal` is in test fixtures but the production registry may differ). Confirm by running `codex exec -c model_reasoning_effort=<X>` against each candidate during implementation.

## Model IDs

Codex accepts bare model IDs (e.g. `gpt-5-codex`, `gpt-5.2`, `gpt-5.1`), not provider-prefixed forms like `openai/gpt-5`. Set `.herdos.yml` `agent.model` to the bare form.

Test fixtures in the Codex source reference `gpt-5.2`, `gpt-5.4`, `gpt-5.1-codex-max`.

> TODO(verify): the full model registry Codex accepts. Not in the sparse source checkout. Run `codex exec --model <candidate>` against the major model IDs during implementation and document the verified list in `docs/configuration.md`.

## Tests

- **Per-method tests**: fake `codex` binary that records arguments + emits scripted output. Verify flags, working dir, env mapping (`CODEX_API_KEY` populated from `OPENAI_API_KEY` when missing; explicit `CODEX_API_KEY` preserved).
- **Output parsing tests**: feed canned JSONL through the JSON output parser; verify final-message extraction.
- **Structured output tests**: feed canned schema-conforming JSON through the parser; verify mapping to `*agent.Plan` and `*agent.ReviewResult`.
- **Discuss**: spawn + stdio passthrough; verify args and that stdin/stdout/stderr are wired.
- **Factory test**: `cfg.Agent.Provider = "codex"` resolves to `*codex.CodexAgent`.
- **Config validation**: `agent.provider: codex` passes validate; `agent.provider: codeX` (case mismatch) fails. `agent.codex_reasoning_effort` accepts valid values, rejects invalid.
- **Dockerfile regression**: existing `TestDockerfile_BakesAgentNpmPackages` extended to include `@openai/codex` at the pinned version.
- **`OPENAI_API_KEY` → `CODEX_API_KEY` mapping**: a test that sets only `OPENAI_API_KEY` in the test env and asserts the child process's env (via the fake codex binary's recorded env) contains `CODEX_API_KEY` set to the same value. A second test sets both and asserts the explicit `CODEX_API_KEY` wins.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run`, `gofmt -l` all pass.
- `agent.provider: codex` with `agent.model: gpt-5-codex` and `OPENAI_API_KEY` set runs `Execute` end-to-end and returns the agent's final message.
- Same configuration runs `Plan` against a small prompt and returns a non-empty `*agent.Plan` with structured output.
- Same configuration runs `Review` against a small diff and returns a `*agent.ReviewResult` with at least the approved/findings/summary fields populated.
- Running with `CODEX_API_KEY` instead of `OPENAI_API_KEY` works identically.
- `images/base/Dockerfile` installs `@openai/codex` at the pinned version; the existing `@anthropic-ai/claude-code`, `opencode-ai` installs are unchanged.
- `herd init --check` passes (workflow templates and runner files in sync with templates).

## Documentation updates required

The implementation is incomplete without these docs landing alongside the code:

- `docs/configuration.md`: under the `agent.provider` table, add `codex` as an accepted value with a one-line description. Add a sub-section documenting `agent.model` for the Codex provider — bare model IDs (e.g. `gpt-5-codex`), not provider-prefixed forms. Add a sub-section for `agent.codex_reasoning_effort` (`minimal | low | medium | high`, default `medium`), noting that it maps to `-c model_reasoning_effort=<value>` on every Codex invocation.
- `docs/runners.md`: add a "Codex provider (`agent.provider: codex`)" section under the existing provider sections. Document the API-key path: set `OPENAI_API_KEY` (herd auto-maps to `CODEX_API_KEY` at invocation) or set `CODEX_API_KEY` directly. Mirror the structure and depth of the existing Claude provider section.
- `docs/installation.md`: no change.
- `CHANGELOG.md`: add an entry under "Added" describing the new Codex provider, API-key auth, and the model/effort config fields.

## Final cleanup

After the acceptance criteria are met and all PR review is complete, delete this spec file (`specs/codex-provider-api-key.md`) as the final task in the batch. The spec served its purpose as a planning artifact; the docs and code are the durable record.

## Notes that apply across both Codex specs

- The codex provider package (`internal/agent/codex/`) is the home for ALL codex-specific runtime logic: agent methods (Execute/Plan/Review/Discuss), `provisionCodexAuth` (subscription seeding, no-op in API-key mode), `runKeepaliveLoop` (subscription chain warmup, no-op in API-key mode).
- The `provisionCodexAuth` and keepalive logic detailed in the subscription spec do not fire in API-key-only configurations. The entrypoint's subscription-mode detection (`env | grep -q '^CODEX_AUTH_JSON'`) returns false when no subscription auth is set, so no background goroutine is spawned and there is no overhead.

## References

- Codex repository: https://github.com/openai/codex (key files: `codex-rs/exec/src/cli.rs`, `codex-rs/utils/cli/src/shared_options.rs`, `codex-rs/login/src/auth/manager.rs`)
- npm package: `@openai/codex` (wrapper around platform-specific binaries `@openai/codex-{linux,darwin,win32}-{x64,arm64}`)
- Auth docs: https://developers.openai.com/codex/auth
- Non-interactive docs: https://developers.openai.com/codex/noninteractive
- Config docs: https://developers.openai.com/codex/config
- Sandbox docs: https://developers.openai.com/codex/sandbox

## Related spec

- `specs/codex-provider-chatgpt-subscription.md` — opt-in subscription auth on top of this provider.
