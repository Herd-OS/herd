# Cost, Token, and Time Visibility

## Purpose

Expose enough execution metrics for users to understand how much work Herd is doing and where time is being spent.

## Why This Matters Before v1

Herd runs potentially expensive agents in parallel. Even when exact token costs are provider-specific or unavailable, users need visibility into duration, attempts, retries, and fix cycles.

## Initial Scope

Track and display:

- Worker duration.
- Attempts per issue.
- Review cycles used.
- CI fix cycles used.
- Timeout count.
- Provider used per role.
- Available token/cost metadata when a provider exposes it.

## Open Questions

- Where should metrics be stored: comments, progress files, branch metadata, or GitHub checks?
- Should `herd status --json` expose these metrics first?
- Which providers expose reliable token/cost data?
