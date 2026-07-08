# Concurrency Model Audit

## Purpose

Audit every event path that can dispatch work, review a PR, create fix issues, or push branches.

## Why This Matters Before v1

Recent dogfooding found duplicate reviews and overlapping fix risks. GitHub Actions, App webhooks, workflow events, and manual commands can fire concurrently. Herd needs an explicit concurrency model for v1.

## Initial Scope

Audit trigger pairs involving:

- GitHub App webhooks.
- `issue_comment`.
- `workflow_run`.
- `check_run`.
- `pull_request_review`.
- `pull_request`.
- `issues`.
- Manual workflow dispatch.
- Scheduled monitor patrol.

Verify that races cannot create duplicate workers, duplicate reviews, duplicate fix issues, or conflicting pushes.

## Open Questions

- Which protections belong in GitHub Actions concurrency groups?
- Which protections require application-level locks?
- Which operations must be idempotent instead of merely serialized?
