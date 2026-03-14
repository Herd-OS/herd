# Roadmap

## v1.0 — Full Release

A complete, self-healing orchestration system for a single repository.

### CLI Commands

- `herd init` — set up repo with labels, workflows, config
- `herd plan` — Planner decomposes work into issues (interactive agent session)
- `herd dispatch` — manually trigger workers for a batch (`--batch`) or all dispatchable issues (`--all`)
- `herd dispatch --batch` — dispatch all ready and failed issues in a batch
- `herd status` — show issues, workers, batch PRs, runners
- `herd batch list/show/cancel` — batch management
- `herd runner list` — check runner status
- `herd worker exec` — worker lifecycle (used by Actions)
- `herd integrator consolidate/advance/review` — integration pipeline (used by Actions)
- `herd monitor patrol` — health monitoring (used by Actions)
- `herd config` — view/edit configuration

### GitHub Actions Workflows

- `herd-worker.yml` — run agent on an issue
- `herd-integrator.yml` — consolidate, advance tiers, agent review
- `herd-monitor.yml` — health patrol (cron-scheduled + on-demand via worker failure)

### Core Features

- GitHub App for bot identity (`herd-os[bot]`) and commit co-authorship
- Role instruction files (`.herd/planner.md`, `.herd/worker.md`, `.herd/integrator.md`) — each ships with the milestone that implements its role
- GitHub Issues with `herd/*` labels as work tracking layer
- DAG-based tier execution (parallel within tier, sequential between tiers)
- Batch branch consolidation with single batch PR per batch
- Agent review with fix-worker dispatch cycle
- Auto-rebase of batch branch onto latest main before opening PR
- Monitor: auto-redispatch of failed workers, stale work detection, exponential backoff
- Human review by default, auto-merge available via `pull_requests.auto_merge: true`
- Conflict-resolution worker dispatch (`on_conflict: dispatch-resolver`)
- Configuration migration tooling (schema version upgrades)

### Distribution

- Binary releases for Linux, macOS, Windows
- Homebrew formula (`brew install herd-os/tap/herd`)
- User-facing documentation:
  - `README.md` — project overview, installation, quickstart (plan → dispatch → merge in 5 minutes)
  - `docs/quickstart.md` — step-by-step guide: install herd, init a repo, plan a feature, dispatch, review PR
  - `docs/configuration.md` — all `.herdos.yml` options with examples
  - `docs/runners.md` — self-hosted runner setup and scaling guide
  - `docs/examples.md` — example workflows: planning with design mockups, multi-tier features, bug fixes, CI failure recovery
  - Example `.herdos.yml` files for common setups (solo dev, small team, CI-heavy repo)

### Documentation Process

Each milestone ends with a documentation pass:

1. **User-facing docs** (`docs/`) — create or update guides covering the features shipped in that milestone. These are the public docs that may be published to a website. Written for end users, not contributors.
2. **README.md** — update to reflect current capabilities. Not historical — just what's available now: installation, quickstart, feature summary.

Documentation is not deferred to a final milestone. It ships with the code it describes.

The `docs/specs/` directory remains internal design documents for planning and is not user-facing.

### Testing

- Unit tests for all core logic (config, issues, DAG, labels, display)
- Integration tests against real GitHub API
- E2E tests: plan → dispatch → worker → consolidate → review → merge

### Limitations

- Single-repo only (no cross-repo batches)
- GitHub only (Platform interface exists but only GitHub is implemented)
- Claude Code only (Agent interface exists but only Claude Code is implemented)

### Success Criteria

A user can:
1. Run `herd init` in a repo and start Docker runners
2. Run `herd plan "Add feature X"`, get issues created, and have Tier 0 workers dispatch automatically
3. Watch workers execute tasks tier by tier (Integrator advances tiers automatically)
4. See the batch PR with agent review, review the complete feature, and merge it
5. If a worker fails, the Monitor re-dispatches it automatically

## v2 — Multi-Agent and Automation

### Additions

- Additional agent implementations (Codex, Cursor, Gemini CLI, OpenCode)

### Success Criteria

Users can choose their preferred agent.

## v3 — Multi-Platform

### Additions

- GitLab implementation of the Platform interface
- Gitea/Forgejo implementation of the Platform interface
- Platform auto-detection from Git remote URL

### Success Criteria

Same `herd` CLI works against GitHub, GitLab, and Gitea repositories.

## v4 — Multi-Repo and Formulas

### Additions

- Cross-repo batches (track work across multiple repos)
- Formula system (reusable work decomposition templates)
- Worker templates (customizable worker behavior per task type)
- Performance metrics (worker success rate, average completion time)

### Formulas

A formula is a reusable template for decomposing a type of work:

```yaml
# formulas/add-api-endpoint.yml
name: Add API Endpoint
description: Standard pattern for adding a REST endpoint
tasks:
  - title: "Create {resource} model"
    scope: ["src/models/"]
    type: feature
  - title: "Create {resource} route handlers"
    scope: ["src/routes/"]
    type: feature
    depends_on: [0]
  - title: "Add {resource} validation"
    scope: ["src/middleware/"]
    type: feature
    depends_on: [0]
  - title: "Add {resource} tests"
    scope: ["tests/"]
    type: feature
    depends_on: [1, 2]
```

```bash
$ herd plan --formula add-api-endpoint --vars resource=User
```

## Future

### Federation

Coordinating HerdOS instances across organizations. A central registry of batches that span multiple repos and orgs.

### Marketplace

Shareable formulas and worker templates. Community-contributed patterns for common tasks (add auth, add CI, refactor to TypeScript, etc.).

### Model A/B Testing

Run different AI models on similar tasks and compare outcomes. Track success rate, completion time, and code quality per model.

