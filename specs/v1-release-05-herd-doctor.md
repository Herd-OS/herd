# Herd Doctor

## Purpose

Add a broad `herd doctor` command that diagnoses the full HerdOS installation, not only provider-specific state.

## Why This Matters Before v1

HerdOS depends on GitHub auth, the GitHub App, generated workflows, runner labels, provider credentials, Docker runner config, and CI workflow names. Users need one command that explains whether the system is ready and what to fix next.

## Initial Scope

Check:

- Herd binary version and generated-file drift.
- GitHub App installation and permissions.
- GitHub auth fallback state.
- Required repo variables and secrets.
- Runner availability and labels.
- Provider binary and auth health.
- Docker Compose runner configuration.
- CI workflow configuration.

## Open Questions

- Should `herd doctor` be local-only or also inspect remote runner state?
- Should it have `--json` output for support/debug automation?
- Should provider-specific doctors be subchecks or separate commands?
