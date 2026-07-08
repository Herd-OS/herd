# v1 Release Notes and Known Limitations

## Purpose

Prepare the final `v1.0.0` release notes and known limitations after the pre-v1 readiness specs are implemented or explicitly deferred.

## Why This Matters Before v1

The release notes are the public contract for v1. They should not be drafted too early, but the work should remain visible as a release gate so the final tag does not ship without a clear upgrade story and support boundary.

## Initial Scope

- Draft final `v1.0.0` release notes.
- Summarize the v1 value proposition.
- List supported platforms:
  - GitHub
  - single-repository orchestration
  - self-hosted GitHub Actions runners
- List supported providers:
  - Claude Code
  - OpenCode
  - Codex
  - Pi
- Document GitHub App installation and required permissions.
- Document migration from pre-v1 PAT-based installs to App-based installs.
- Document runner setup, runner image, and upgrade expectations.
- Document security caveats and link to the security posture doc.
- Document known limitations.

## Known Limitations to Include

- GitHub only.
- Single-repository batches only.
- Self-hosted runners required for worker execution.
- No GitLab, Gitea, or Forgejo support yet.
- No cross-repository batches yet.
- No hosted HerdOS service.
- Public repository/self-hosted runner risks.

## Open Questions

- Should release notes live only on GitHub Releases, or also in `CHANGELOG.md`?
- Should v1 include a migration guide page separate from release notes?
- Which pre-v1 versions need explicit upgrade notes?
