# Unsupported OpenCode Google OAuth Plugin Research

## Purpose

Record research about third-party OpenCode plugins that claim to route Google/Gemini/Antigravity subscription access through OpenCode.

This is explicitly **not** a supported HerdOS feature plan. The goal is to preserve the technical findings and product boundary so Herd does not accidentally endorse, document, install, or maintain this path.

## Support Boundary

HerdOS should not:

- Ship or install Google subscription bypass plugins.
- Document those plugins in user-facing setup guides.
- Present Google subscription access through OpenCode as supported.
- Vendor or fork a plugin to maintain this behavior.
- Add official configuration examples for this path.

If a user privately customizes their runner image or compose override, that is outside HerdOS's supported surface area.

## Why This Is Unsupported

Current research shows that Google subscription access through OpenCode relies on unofficial plugins that mimic or bridge Antigravity/Gemini/Code Assist flows rather than using a normal supported Google API billing path.

The most mature plugin found explicitly warns that using Antigravity proxies violates Google's Terms of Service and that users have reported account bans or shadow-bans. HerdOS should not create the impression that this is an endorsed or safe integration.

## Plugins Observed

### `opencode-antigravity-auth`

Repository:

- https://github.com/NoeFabris/opencode-antigravity-auth

Observed characteristics:

- Published on npm as `opencode-antigravity-auth`.
- Claims Google OAuth against Antigravity.
- Claims access to Gemini and Claude models through Google credentials.
- Supports multi-account rotation, quota handling, file locking, and config-directory overrides.
- Stores sensitive state under `~/.config/opencode`, including `antigravity-accounts.json`.
- Supports `OPENCODE_CONFIG_DIR` for custom config location.
- Explicitly warns that the approach violates Google Terms of Service and may result in account bans or shadow-bans.

Technical note:

- If privately tested in a runner, it likely needs a persistent config mount such as `/home/runner/.config/opencode`, not only OpenCode's native data auth path.
- This should remain user-owned runner customization, not Herd-managed behavior.

### `opencode-gemini-oauth`

Repositories:

- https://github.com/simonfr/opencode-gemini-oauth
- https://github.com/Marquinho/opencode-gemini-oauth

Observed characteristics:

- Smaller projects than `opencode-antigravity-auth`.
- Claim Google OAuth or Gemini subscription-style auth for OpenCode.
- One implementation appears to use OpenCode's plugin auth APIs and may persist through OpenCode's native auth store.
- Another stores separate OAuth account state under an OpenCode config path.
- Less evidence of real-world use and hardening.

Technical note:

- These are not preferable to `opencode-antigravity-auth` for private experiments because they appear less mature.
- They are still unsupported for HerdOS.

### `opencode-antigravity-plugin`

Repository:

- https://github.com/yohi/opencode-antigravity-plugin

Observed characteristics:

- Bridges OpenCode to the Google Antigravity SDK through an OpenAI-compatible local HTTP shape.
- Live mode still requires a `GEMINI_API_KEY`.
- Does not solve subscription-style auth.

## OpenCode Plugin Risk

OpenCode plugin auth behavior has had issues where multiple plugins registering auth for the same provider can overwrite or hide each other. A Google OAuth plugin can silently disappear from `opencode auth login` if another plugin registers Google auth first.

This makes plugin-based Google subscription auth a poor fit for Herd's managed runner defaults. Herd workers need predictable, diagnosable auth behavior.

## Private Experiment Shape

For maintainers who choose to test this privately, the plausible technical shape is:

- Use a custom private `Dockerfile.herd_runner`.
- Install the plugin in that image or through user-owned config.
- Mount persistent OpenCode data and config paths.
- Set `OPENCODE_CONFIG_DIR` to the mounted config path if the plugin supports it.
- Authenticate inside the running worker container as the `runner` user.
- Verify auth survives container restart.
- Treat failure, bans, quota changes, or plugin breakage as outside HerdOS support.

Do not convert this into public docs or generated defaults.

## Relationship To Supported Work

This research should stay separate from `14-opencode-subscription-auth-volume.md`.

Supported OpenCode work can still add generic auth/config persistence that helps legitimate OpenCode subscription paths such as ChatGPT Plus/Pro, GitHub Copilot, and other officially supported providers. That generic persistence should not mention or promote Google subscription plugins.

## Acceptance Criteria

- HerdOS does not implement this as a supported provider path.
- HerdOS does not document these plugins in public user guides.
- HerdOS maintainers understand that generic OpenCode config-volume support may make private experiments possible without Herd endorsing them.
- The final implementation, if any, deletes this research spec only if the unsupported boundary is captured somewhere durable for maintainers.
