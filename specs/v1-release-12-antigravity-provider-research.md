# Antigravity Provider Research

## Purpose

Track whether Antigravity CLI can become a future HerdOS agent provider once its headless/non-interactive behavior is reliable enough for orchestrated worker execution.

## Why This Is Not v1 Scope

Antigravity CLI exposes a nominal print/headless mode through `agy --print` / `agy -p`, but current issue reports show that this mode is not yet a stable provider contract for HerdOS.

The key upstream issue to watch is:

- https://github.com/google-antigravity/antigravity-cli/issues/76

That issue reports `agy --print` completing model calls while failing to produce reliable stdout in non-TTY contexts such as subprocesses, pipes, redirects, CI jobs, and orchestrators. Later comments suggest some Windows stdout behavior improved around `agy 1.0.15`, but reports still mention hangs when stdin is attached, unreliable `--print-timeout`, silent server-side failures, and macOS cases that exit successfully with empty output.

Until this contract is stable, Antigravity should not be listed as a v1 supported provider.

## Required Provider Contract

Before HerdOS can support Antigravity as a normal provider, `agy` must support:

- Deterministic stdout or JSON output in non-TTY subprocess mode.
- Reliable nonzero exit codes on auth, quota, server, and tool failures.
- Useful stderr diagnostics on failure.
- Safe behavior when stdin is closed or redirected.
- A timeout behavior that does not rely solely on the CLI's internal timeout.
- No dependency on scraping internal sqlite/transcript files as the primary output channel.
- No required PTY wrapper unless Herd explicitly chooses to own and test that wrapper.

## Research Plan

When revisiting this:

1. Re-test the latest `agy` release on Linux, macOS, and Windows.
2. Verify a minimal non-TTY subprocess call:

   ```bash
   agy --print "Reply with exactly: PONG" < /dev/null
   ```

3. Verify the same call with stdout and stderr captured by a parent process.
4. Verify that stdin is closed or `DEVNULL` in Herd's process invocation.
5. Verify auth failures produce nonzero exit codes or parseable stderr.
6. Verify quota/server failures produce nonzero exit codes or parseable stderr.
7. Verify Herd's external process timeout can terminate the full process tree.
8. Verify subscription/user-login persistence in a container using the auth-volume experiment below.
9. Decide whether an experimental provider flag is appropriate before full support.

## Auth-Volume Experiment

Antigravity CLI's README says the CLI authenticates through the system keyring and falls back to Google Sign-In when no active session exists. Issue reports also show Antigravity state under `~/.gemini/antigravity-cli/`, including conversation/cache files.

Before assuming HerdOS can support subscription-style Antigravity auth, verify whether a Codex-style persistent auth volume works:

1. Add a named Docker volume mounted at the Antigravity config path:

   ```yaml
   volumes:
     - antigravity-auth:/home/runner/.gemini/antigravity-cli
   ```

2. Start a worker container with `agy` installed.
3. Run Antigravity login/auth inside the running container as the `runner` user.
4. Complete browser/device auth from the host.
5. Stop and recreate the worker container.
6. Run a non-interactive smoke test:

   ```bash
   agy --print "Reply with exactly: PONG" < /dev/null
   ```

7. Confirm the command succeeds without re-authenticating.
8. Inspect whether any credential lives outside `~/.gemini/antigravity-cli`.

Critical unknown: if the actual refresh token is stored only in an OS keyring, mounting `~/.gemini/antigravity-cli` may not be enough. Some CLIs fall back to file-backed auth in headless Linux containers; others require a keyring service. HerdOS should not claim subscription auth works until this is tested in the runner image.

## Possible Herd Integration Shape

If the CLI becomes reliable enough:

- Add `agent.provider: antigravity`.
- Default binary: `agy`.
- Use `agy --print <prompt>` or a future JSON/output-file mode.
- Always close stdin unless a documented mode requires prompt input through stdin.
- If subscription auth works, mount a persistent `antigravity-auth` volume similarly to Codex's `codex-auth` volume.
- If a keyring service is required, document it clearly or defer subscription auth until a runner-safe auth path exists.
- Treat empty stdout with exit 0 as provider failure, not success.
- Add `herd doctor` checks for the exact headless contract.
- Add `herd doctor` checks that confirm persisted Antigravity auth survives container restart.
- Add provider parity tests with a fake `agy` binary and optional integration tests for real `agy`.

## Open Questions

- Will Antigravity add `--format json`, `--stream-json`, or `--output <path>`?
- Will `--print-timeout` become reliable enough, or should Herd always rely on its own timeout only?
- Will failures become machine-readable?
- Can Antigravity run safely in the published runner image without desktop-app assumptions?
- Is Antigravity subscription auth file-backed enough for Docker volumes, or does it require an OS keyring service?
- Does the auth state survive container restart when only `~/.gemini/antigravity-cli` is mounted?
- Does the CLI expose token/cost metadata that Herd can surface later?
