# Provider Parity Tests

## Purpose

Define the minimum behavior every supported agent provider must satisfy before v1.

## Why This Matters Before v1

HerdOS now intends to support Claude Code, OpenCode, Codex, and Pi. The provider interface is abstracted, but users will expect all supported providers to work across the same core Herd roles.

## Initial Scope

For each supported provider, verify:

- Planning interaction works.
- Worker execution works.
- Review JSON output works.
- Timeout and process cleanup works.
- Auth failure diagnostics are understandable.
- Large prompt and large diff handling are safe.

## Open Questions

- Which checks can be pure unit tests with fake binaries?
- Which checks require optional integration tests?
- How should provider-specific capabilities be documented without weakening the common contract?
