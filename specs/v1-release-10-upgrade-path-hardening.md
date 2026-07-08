# Upgrade Path Hardening

## Purpose

Make upgrades between HerdOS versions predictable and low-risk.

## Why This Matters Before v1

Herd owns generated workflows and runner files. v1 users should be able to upgrade the binary, run `herd init`, review the generated PR, and redeploy runners without surprises.

## Initial Scope

- Verify `herd init --check` reliability.
- Document generated-file drift and remediation.
- Document GitHub App migration from PAT-based installs.
- Verify runner image pin and publish behavior.
- Verify old generated files migrate cleanly.
- Ensure upgrade PRs explain what changed.

## Open Questions

- Should `herd init` produce a migration summary?
- Should App migration be interactive or purely documented?
- How many pre-v1 versions need explicit migration support?
