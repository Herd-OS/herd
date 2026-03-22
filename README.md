<p align="center">
  <img src="assets/logo.png" width="260" alt="Herd">
</p>

<h3 align="center">Herd your agents.</h3>

<p align="center">
  GitHub-native orchestration for AI coding agents.<br>
  One command. Parallel workers. One reviewed PR.
</p>

<p align="center">
  <a href="docs/getting-started.md">Getting Started</a> ·
  <a href="docs/installation.md">Install</a> ·
  <a href="docs/design/">Design Docs</a>
</p>

---

```
herd plan "Add user authentication"
```

That's it. Herd decomposes your feature into tasks, dispatches AI agents
to work them in parallel on self-hosted runners, consolidates the
results, runs an automated review, and opens a single PR — ready for
you to merge.

You walk away after `herd plan`. You come back to a reviewed PR.

---

## How it works

```mermaid
graph TD
    A[herd plan 'Add auth'] --> B

    subgraph B[Tier 0 — parallel]
        W1[Worker #42<br>auth model]
        W2[Worker #43<br>login route]
    end

    B --> C[Integrator consolidates]

    subgraph D[Tier 1 — depends on Tier 0]
        W3[Worker #44<br>auth middleware]
    end

    C --> D
    D --> E[Integrator consolidates]
    E --> F[Batch PR opened]
    F --> G[Agent review]
    G --> H[You merge]
    F --> I[You close without merging]
    I --> J[Non-done issues cancelled, cleanup]
```

Workers run your configured agent (Claude Code, Codex, Cursor) in
headless mode. If one fails, the Monitor detects it and retries
automatically. The system is self-healing.

## Quick start

```bash
brew install herd-os/tap/herd

cd /path/to/your/repo
herd init
herd plan "Add dark mode support"
```

See the [full setup guide](docs/getting-started.md) for runner
configuration and options.

## Why "Herd"?

Anyone who's tried to coordinate multiple AI agents knows the feeling —
it's like herding cats. Each one is powerful on its own, but getting them
to work together without stepping on each other? That's the hard part.

Herd tames the chaos.

## Documentation

| | |
|---|---|
| [Installation](docs/installation.md) | Homebrew, binary, from source |
| [Getting Started](docs/getting-started.md) | First run walkthrough |
| [Runner Setup](docs/runners.md) | Self-hosted runner configuration |
| [Configuration](docs/configuration.md) | `.herdos.yml` reference |
| [Design Docs](docs/design/) | Architecture and design decisions |

## Status

In active development. The core system is functional end-to-end:
planning, dispatch, parallel worker execution, tier-based DAG
scheduling, conflict resolution, automated agent review with fix cycles,
and health monitoring.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
