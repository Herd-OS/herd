# Security Posture

## Purpose

Document HerdOS's security model bluntly and practically before v1.

## Why This Matters Before v1

HerdOS runs AI agents with repository write access, GitHub API permissions, and provider credentials. Users need a clear understanding of the trust boundary and the risks they are accepting.

## Initial Scope

Document:

- What the runner container can access.
- Where GitHub and provider credentials live.
- Why provider auth belongs in runner `.env`, not worker workflow secrets.
- Required GitHub App permissions.
- Risks of host bind mounts.
- Public repository warnings for subscription auth.
- Recommended runner isolation.
- Secret exposure and log redaction expectations.

## Open Questions

- Should security posture live in `docs/security.md` or design docs?
- Should `herd doctor` check for known risky configurations?
- What is the minimum supported hardening story for teams?
