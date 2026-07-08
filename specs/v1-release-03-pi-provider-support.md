# Pi Provider Support

## Purpose

Add Pi as a first-class HerdOS agent provider for v1.

## Why This Matters Before v1

Pi appears to fit HerdOS's existing provider abstraction well because it documents print/JSON, RPC, and SDK modes. If provider support is already abstracted, adding Pi should be small enough to include in the v1 provider set instead of leaving it as vague future work.

## Initial Scope

- Add `agent.provider: pi` config validation and defaults.
- Add a Pi provider implementation behind the existing agent interface.
- Support the required Herd roles:
  - planning
  - worker execution
  - agent review
  - interactive discussion where applicable
- Wire default binary resolution.
- Pass prompts through a headless/non-interactive mode.
- Parse structured review output consistently with other providers.
- Add auth/environment documentation.
- Add unit tests with fake Pi binaries for command construction, stdin/stdout handling, and error paths.

## Open Questions

- Which Pi CLI mode is best for each Herd role: print/JSON, RPC, or SDK?
- What environment variables or login state does Pi require in runner containers?
- Does Pi expose token/cost metadata that Herd can surface later?
- Does Pi need provider-specific timeout or process cleanup behavior?
