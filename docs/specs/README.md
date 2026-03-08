# HerdOS Specifications

Design documents for HerdOS — a GitHub-native orchestration platform for managing multiple agentic development systems.

## Reading Order

Start with the overview, then read sections in order. Files within each section are numbered by suggested reading order.

### 00 — Overview

| # | Document | Description | Status |
|---|----------|-------------|--------|
| 1 | [vision.md](00-overview/01-vision.md) | Problem statement, positioning, non-goals | Draft |
| 2 | [architecture.md](00-overview/02-architecture.md) | High-level system architecture with diagrams | Draft |
| 3 | [glossary.md](00-overview/03-glossary.md) | Terms and naming conventions | Draft |
| 4 | [gastown-lessons.md](00-overview/04-gastown-lessons.md) | What we learned from Gastown, what we kept/replaced | Draft |

### 01 — Concepts

| # | Document | Description | Status |
|---|----------|-------------|--------|
| 1 | [planner.md](01-concepts/01-planner.md) | The local planner/orchestrator | Draft |
| 2 | [workers.md](01-concepts/02-workers.md) | GitHub Actions workers | Draft |
| 3 | [integrator.md](01-concepts/03-integrator.md) | Integration, agent review, and merge management | Draft |
| 4 | [monitor.md](01-concepts/04-monitor.md) | Health monitoring and stale work detection | Draft |
| 5 | [batches.md](01-concepts/05-batches.md) | Work grouping and delivery tracking | Draft |
| 6 | [workflows.md](01-concepts/06-workflows.md) | End-to-end work flow: dispatch → execute → land | Draft |

### 02 — GitHub Integration

| # | Document | Description | Status |
|---|----------|-------------|--------|
| 1 | [issues.md](02-github/01-issues.md) | Issue conventions: labels, body format, lifecycle | Draft |
| 2 | [actions.md](02-github/02-actions.md) | Reusable workflow templates (worker, monitor, integrator) | Draft |
| 3 | [runners.md](02-github/03-runners.md) | Self-hosted runner setup and scaling | Draft |
| 4 | [events.md](02-github/04-events.md) | Event-driven architecture: triggers and event types | Draft |
| 5 | [permissions.md](02-github/05-permissions.md) | Security model and access control | Draft |
| 6 | [github-app.md](02-github/06-github-app.md) | GitHub App: bot identity, attribution, auth | Draft |

### 03 — CLI

| # | Document | Description | Status |
|---|----------|-------------|--------|
| 1 | [commands.md](03-cli/01-commands.md) | Full CLI command reference | Draft |
| 2 | [configuration.md](03-cli/02-configuration.md) | Config format, init process, repo setup | Draft |
| 3 | [abstraction-layers.md](03-cli/03-abstraction-layers.md) | Agent and Platform abstraction interfaces | Draft |

### 04 — Implementation

| # | Document | Description | Status |
|---|----------|-------------|--------|
| 1 | [roadmap.md](04-implementation/01-roadmap.md) | v1.0 → v2 → future scope | Draft |
| 2 | [project-structure.md](04-implementation/02-project-structure.md) | Repo layout and module boundaries | Draft |
| 3 | [testing.md](04-implementation/03-testing.md) | Testing strategy | Draft |

## Status Legend

- **Draft** — Initial writing, open for major changes
- **Review** — Content stable, collecting feedback
- **Accepted** — Approved, guides implementation
