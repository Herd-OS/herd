<p align="center">
  <img src="assets/logo.png" width="360" alt="Herd">
</p>

# Herd

GitHub-native orchestration for agentic development systems.

## The Problem

AI coding agents like Claude Code, Codex, and Cursor are powerful — but managing multiple agents working on the same codebase is hard. You need to decompose work, dispatch tasks, handle failures, resolve conflicts, and merge everything cleanly. Doing this manually doesn't scale.

## The Solution

Herd turns GitHub into your orchestration layer. Work is tracked as Issues, executed by Actions on self-hosted runners, and landed as a single reviewed PR. No local database, no daemon, no polling — just a CLI and GitHub.

## How It Works

```
herd plan "Add user authentication"
```

1. **Plan** — An interactive agent session decomposes your feature into tasks with dependencies
2. **Dispatch** — Tasks are created as GitHub Issues and dispatched as Actions to self-hosted runners
3. **Execute** — Workers run your configured agent in headless mode, each on its own branch
4. **Review** — When all tasks complete, an agent reviews the consolidated batch PR
5. **Merge** — You review the PR and merge. One PR per feature, clean history.

Workers execute in parallel where possible, tier by tier. If a worker fails, the Monitor detects it and retries automatically. The system is self-healing — you can walk away after `herd plan` and come back to a reviewed PR.

## Status

In development. Design specs are available in [`docs/specs/`](docs/specs/).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
