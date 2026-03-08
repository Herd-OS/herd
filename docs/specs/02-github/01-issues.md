# Issues

GitHub Issues are HerdOS's work tracking layer. Every task a worker executes is defined by an issue. This document specifies the label taxonomy, issue body format, and lifecycle state machine.

## Label Taxonomy

All HerdOS labels use the `herd/` prefix to avoid collisions with existing repository labels. Labels are created automatically by `herd init`.

### Status Labels (mutually exclusive)

| Label | Color | Description (shown on hover in GitHub) |
|-------|-------|---------|
| `herd/status:ready` | `#0E8A16` green | Ready for a worker to pick up |
| `herd/status:in-progress` | `#FBCA04` yellow | A worker is actively executing this task |
| `herd/status:done` | `#6F42C1` purple | Worker completed, branch ready for consolidation |
| `herd/status:failed` | `#D93F0B` red | Worker failed — needs re-dispatch or manual fix |
| `herd/status:blocked` | `#C5DEF5` light blue | Waiting for a dependency to complete |

An issue has exactly one status label at any time. The CLI and Actions enforce this — setting a new status removes the old one.

### Type Labels

| Label | Color | Description (shown on hover in GitHub) |
|-------|-------|---------|
| `herd/type:feature` | `#1D76DB` blue | New functionality (set by Planner) |
| `herd/type:bugfix` | `#D93F0B` red | Bug fix (set by Planner) |
| `herd/type:fix` | `#E99695` salmon | Auto-generated fix from agent review or conflict resolution |

Label descriptions are set by `herd init` via `LabelService.Create` and are visible on hover in the GitHub UI.

## Issue Body Format

Issues created by HerdOS use a structured body with YAML front matter:

```markdown
---
herd:
  version: 1
  batch: 7
  depends_on: [42, 43]
  scope:
    - src/components/ThemeToggle.tsx
    - src/styles/theme.css
  estimated_complexity: medium
---

## Task

Create a theme toggle component that switches between light and dark mode.

## Acceptance Criteria

- [ ] Component renders a toggle button
- [ ] Clicking toggles between `data-theme="light"` and `data-theme="dark"` on `<html>`
- [ ] Current theme is persisted to localStorage
- [ ] Component is exported from the components index

## Context

This is part of the dark mode feature. The CSS custom properties are already defined
(see issue #42). This component consumes them.

## Files to Modify

- `src/components/ThemeToggle.tsx` (create)
- `src/components/index.ts` (add export)
```

### Front Matter Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | number | yes | Schema version (currently `1`) |
| `batch` | number | no | Milestone number this issue belongs to |
| `depends_on` | number[] | no | Issue numbers that must close before this is ready |
| `scope` | string[] | no | Files the worker should focus on |
| `estimated_complexity` | string | no | `low`, `medium`, `high` — hint for worker timeout |
| `runner_label` | string | no | Runner label override for this issue (e.g., `herd-gpu`). Defaults to `workers.runner_label` from config. |

**Integrator-generated fields** (only present on issues created by the Integrator, not the Planner):

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | no | `fix` — distinguishes Integrator-created issues from Planner-created ones |
| `fix_cycle` | number | no | Which review cycle spawned this fix (1 = first review, 2 = second) |
| `batch_pr` | number | no | PR number of the batch PR being reviewed |
| `conflict_resolution` | boolean | no | `true` if this is a conflict-resolution issue |
| `conflicting_branches` | string[] | no | Branch names that conflicted (for conflict-resolution issues) |

Workers parse the front matter to understand their task. The human-readable sections (Task, Acceptance Criteria, Context) are passed directly to the agent as the prompt.

## Lifecycle State Machine

```
                    ┌─────────────────────────┐
                    │                         │
                    ▼                         │
┌──────────┐   ┌──────────┐   ┌─────────────────┐   ┌────────┐
│ blocked   │──▶│  ready    │──▶│  in-progress     │──▶│  done   │
└──────────┘   └──────────┘   └─────────────────┘   └────────┘
                    ▲                │
                    │                │
                    │                ▼
                    │          ┌──────────┐
                    └──────────│  failed   │
                    (re-dispatch)└──────────┘
```

### Transitions

| From | To | Trigger |
|------|----|---------|
| (created) | `ready` | Planner/Integrator creates issue with no unresolved dependencies |
| (created) | `blocked` | Planner creates issue with unmet `depends_on` |
| `blocked` | `ready` | Integrator (`herd integrator advance`) unblocks next tier; Monitor as safety net |
| `ready` | `in-progress` | Worker dispatched (`herd dispatch` or Integrator advance) |
| `in-progress` | `done` | Worker completes task, pushes worker branch |
| `in-progress` | `failed` | Worker fails, times out, or can't complete |
| `failed` | `ready` | Re-dispatched by Monitor or user |
| `ready` | `failed` | Batch cancelled (`herd batch cancel`) |
| `blocked` | `failed` | Batch cancelled (`herd batch cancel`) |
| `in-progress` | `failed` | Batch cancelled (`herd batch cancel`) |
| `done` | `failed` | Batch cancelled (`herd batch cancel`) |
| `done` | (closed) | Batch PR merged (GitHub auto-closes via `Closes #N`) |

## Manual Issues

Users can create HerdOS-compatible issues manually. The issue must have a `herd/status:ready` label and belong to a batch (milestone) — `herd dispatch` requires a batch context for branch naming and tier tracking. The YAML front matter is optional — without it, the worker uses the full issue body as its prompt.

## Query Patterns

Common GitHub API queries HerdOS uses:

```
# All ready issues
label:herd/status:ready

# All in-progress issues for a batch
label:herd/status:in-progress milestone:"Dark mode support"

# All failed issues
label:herd/status:failed
```
