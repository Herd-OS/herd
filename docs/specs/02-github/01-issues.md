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
| `herd/type:manual` | `#BFD4F2` light blue | Requires human action — not dispatched to workers |

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
The component should be a React functional component using the existing
design system button styles.

## Implementation Details

Create `src/components/ThemeToggle.tsx`:

- React functional component using `useState` and `useEffect`
- Read initial theme from `localStorage.getItem('theme')`, default to `'light'`
- On toggle, set `document.documentElement.dataset.theme` to `'light'` or `'dark'`
- Persist to `localStorage.setItem('theme', newTheme)` on every toggle
- Use the existing `<Button>` component from `src/components/Button.tsx` for the toggle
  (already exported, accepts `onClick` and `children` props)
- Display a sun icon (☀️) when dark mode is active, moon icon (🌙) when light mode is active

Add the export to `src/components/index.ts`:

    export { ThemeToggle } from './ThemeToggle';

### Conventions

- Follow the existing component pattern in src/components/ (functional components,
  named exports, no default exports)
- CSS custom properties for theming are already defined in src/styles/theme.css
  (issue #42 added `--bg-color`, `--text-color`, `--border-color` for both
  `[data-theme="light"]` and `[data-theme="dark"]` selectors)

### Context from Dependencies

- Issue #42 (Add CSS custom properties) defined these custom properties:
  `--bg-color`, `--text-color`, `--border-color`, `--accent-color` on both
  `[data-theme="light"]` and `[data-theme="dark"]` selectors in
  `src/styles/theme.css`. This component sets the `data-theme` attribute
  that activates them.
- Issue #43 (Create theme context) created `src/context/ThemeContext.tsx`
  which exports `useTheme()` hook returning `{ theme, toggleTheme }`.
  Use this hook instead of managing state locally.

## Acceptance Criteria

- [ ] Component renders a toggle button using the existing Button component
- [ ] Clicking toggles `data-theme` attribute on `<html>` between "light" and "dark"
- [ ] Current theme is persisted to localStorage under key "theme"
- [ ] Initial render reads from localStorage (or defaults to "light")
- [ ] Component is exported from src/components/index.ts
- [ ] Uses the useTheme() hook from ThemeContext (created by issue #43)

## Files to Modify

- `src/components/ThemeToggle.tsx` (create)
- `src/components/index.ts` (add export)
```

The key sections are:

- **Task** — what to build (concise summary)
- **Implementation Details** — how to build it: exact file paths, function signatures, algorithms, data structures, which existing code to use. This section makes the issue self-contained.
- **Conventions** — project-specific patterns the worker must follow (discovered by the Planner during its exploration of the codebase)
- **Context from Dependencies** — information from issues this task depends on, inlined so the worker doesn't need to read other issues. The Planner knows what each dependency produces — it encodes that knowledge here.
- **Acceptance Criteria** — concrete, verifiable checks
- **Files to Modify** — explicit list of files to create or edit

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
| `done` | (closed) | Batch PR merged (Integrator closes issues via API) |

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
