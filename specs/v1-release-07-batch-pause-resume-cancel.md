# Batch Pause, Resume, and Cancel

## Purpose

Give users explicit control when a batch should stop spending agent time or temporarily stop advancing.

## Why This Matters Before v1

Herd can dispatch multiple workers and fix cycles automatically. Users need a clear way to pause automation, resume it, or cancel the batch without relying on ad hoc labels or workflow cancellation.

## Initial Scope

- Clarify current cancel behavior.
- Add or document pause/resume semantics.
- Prevent new worker/review/CI-fix dispatch while paused.
- Allow in-flight work to finish or define explicit cancellation behavior.
- Surface paused state in status/dashboard/PR comments.

## Open Questions

- Should pause stop only new dispatches or also cancel in-flight runs?
- Should review and CI fix loops have separate pause controls?
- Which command forms should exist for App mentions and slash compatibility?
