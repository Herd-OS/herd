# Recovery UX

## Purpose

Make every non-self-healing Herd failure explain what happened, whether Herd will retry, and what the user should do next.

## Why This Matters Before v1

HerdOS is valuable because it self-heals. When it cannot, unclear comments or logs make users distrust the system. The failure path needs to be as designed as the happy path.

## Initial Scope

Audit user-facing failure messages from:

- Worker failures.
- Integrator failures.
- Review failures.
- CI fix failures.
- GitHub App permission failures.
- Runner/auth failures.
- Timeout and retry exhaustion.

Each message should include:

- What failed.
- Whether Herd will retry automatically.
- The next user action.
- Relevant issue, PR, run, or branch links when available.

## Open Questions

- Should failure messages use a shared template library?
- Should errors be grouped into stable categories for docs and support?
- Which failures should page/mention configured users?
