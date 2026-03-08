<p align="center">
  <img src="assets/logo.png" width="260" alt="Herd">
</p>

# Herd

*Herd your agents.*

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

## Quick Start

```bash
# Build and install (requires Go 1.26+)
git clone https://github.com/Herd-OS/herd.git
cd herd && make build
sudo cp bin/herd /usr/local/bin/

# Initialize a repository
cd /path/to/your/repo
herd init

# View and customize configuration
herd config list
herd config set workers.max_concurrent 5
```

See [docs/getting-started.md](docs/getting-started.md) for the full setup guide.

## Available Commands

| Command | Description |
|---------|-------------|
| `herd init` | Set up a repo for HerdOS (config, labels, workflows) |
| `herd config list\|get\|set\|edit` | View and manage configuration |

More commands (`plan`, `dispatch`, `status`, `batch`) are coming soon.

## Documentation

- [Installation](docs/installation.md)
- [Getting Started](docs/getting-started.md)
- [Configuration](docs/configuration.md)
- [Design Specs](docs/specs/) (internal)

## Why a Cat?

Anyone who's tried to coordinate multiple AI agents knows the feeling — it's like herding cats. Each agent is powerful on its own, but getting them to work together without stepping on each other? That's the hard part. Herd tames the chaos.

## Status

In active development. Foundation is complete — core orchestration commands are next.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
