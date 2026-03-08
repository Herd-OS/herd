# Vision

## The Problem

Managing multiple AI coding agents is hard. As tools like Claude Code, Codex, and others become capable enough to handle real engineering tasks autonomously, a new problem emerges: how do you coordinate many of them working on the same codebase simultaneously?

Today's approach is ad-hoc. Developers open multiple terminal tabs, manually assign tasks, check back periodically, and hope nothing conflicts. This doesn't scale. The moment you have three or more agents working in parallel, you need:

- **Work decomposition** — breaking a feature into tasks agents can execute independently
- **Conflict management** — handling merge conflicts when agents touch overlapping code
- **Progress tracking** — knowing what's done, what's stuck, what failed
- **Health monitoring** — detecting and recovering from stalled or broken agents
- **Delivery coordination** — landing a set of related changes together

These are orchestration problems. They've been solved before — in CI/CD, in container orchestration, in distributed systems. But nobody has solved them specifically for AI coding agents in a way that's lightweight and practical.

## The Insight

GitHub already solves most of these problems. Issues track work. Actions execute jobs. Pull requests manage code review and merging. Milestones group related work into deliverable batches. The infrastructure exists — it just needs a thin orchestration layer on top.

Instead of building a custom orchestration runtime (with its own storage, its own UI, its own job queue), HerdOS uses GitHub as the orchestration backbone. The local CLI is just the entry point.

## What HerdOS Is

HerdOS is a GitHub-native orchestration platform for managing multiple agentic development systems. It consists of:

1. **A CLI tool** (`herd`) that runs locally — your interface to plan work, dispatch agents, and monitor progress
2. **GitHub Issues** as the work tracking layer — structured with labels and conventions so agents can read and update them
3. **GitHub Actions** as the execution layer — workers run agents in headless mode on self-hosted runners
4. **A set of reusable workflows** that handle merging, health monitoring, and delivery tracking

The user describes what they want. HerdOS breaks it into issues, dispatches workers, monitors progress, handles merges, and reports when everything lands.

## Positioning

HerdOS is the spiritual successor to [Gastown](https://github.com/steveyegge/gastown), carrying forward its proven ideas about role decomposition, batch-based delivery, and autonomous workers. What makes HerdOS different from other orchestration approaches:

- **Lightweight** — no custom database, no local daemons, no persistent processes. The CLI is the only local component.
- **GitHub-native** — uses Issues for tracking, Actions for execution, and Milestones for batches. No new infrastructure to deploy or maintain.
- **Accessible** — everything is visible in the GitHub web UI. Monitor progress from anywhere with a browser.

For the full history of what we learned from Gastown and how it shaped HerdOS, see [gastown-lessons.md](04-gastown-lessons.md).

## Target Users

- **Solo developers** using AI coding agents who want to parallelize their work — dispatch multiple agents while they focus on architecture or review
- **Small teams** coordinating AI agents across a shared codebase
- **Anyone already on GitHub** who wants agent orchestration without new infrastructure

## Non-Goals

- **Not a full operating system.** The "OS" in HerdOS is aspirational (like "AgentOS" or "ArchonOS"), not literal. It's an orchestration platform.
- **Not a replacement for GitHub.** HerdOS is a layer on top of GitHub, not a competitor. If GitHub adds native agent orchestration, HerdOS adapts or becomes unnecessary.
- **Not agent-specific.** Ships with Claude Code support first, with more agents (Codex, Cursor, Gemini CLI, OpenCode) coming soon. The architecture supports any agent that can read a task description and produce code changes on a branch.
- **Not enterprise-first.** Start simple, for individuals. Complexity comes later.
- **Not a hosted service.** HerdOS runs locally and uses your GitHub account. No SaaS, no vendor lock-in beyond GitHub itself.
