# Getting Started

## Initialize a Repository

Navigate to a git repository with a GitHub remote and run:

```bash
herd init
```

This will:

1. **Create `.herdos.yml`** — the configuration file with sensible defaults, auto-detecting your GitHub owner and repo from the git remote
2. **Create `.herd/` directory** — with empty role instruction files (`planner.md`, `worker.md`, `integrator.md`, `monitor.md`) for customizing agent behavior per role
3. **Create GitHub labels** — the `herd/*` label taxonomy used to track issue status and type
4. **Install workflow files** — GitHub Actions workflows for workers, integrator, and monitor in `.github/workflows/`

### Skipping Steps

```bash
herd init --skip-labels       # Don't create GitHub labels
herd init --skip-workflows    # Don't install workflow files
```

## Configuration

View all configuration:

```bash
herd config list
```

Get a specific value:

```bash
herd config get workers.max_concurrent
```

Set a value:

```bash
herd config set workers.max_concurrent 5
herd config set platform.owner my-org
herd config set pull_requests.auto_merge true
```

Open the config file in your editor:

```bash
herd config edit
```

See [configuration.md](configuration.md) for all available options.

## Role Instruction Files

Customize how each HerdOS role behaves in your project by editing files in `.herd/`:

| File | Purpose |
|------|---------|
| `.herd/planner.md` | Extra instructions for the Planner (e.g., "always include testing requirements") |
| `.herd/worker.md` | Extra instructions for Workers (e.g., "use table-driven tests", "follow project coding standards") |
| `.herd/integrator.md` | Extra instructions for the Integrator |
| `.herd/monitor.md` | Extra instructions for the Monitor |

These files are created empty by `herd init`. Add your project-specific instructions and commit them — they're shared across your team.

## Next Steps

Once initialized, you're ready to plan and dispatch work. These features are coming in the next milestone:

- `herd plan` — decompose a feature into tasks
- `herd dispatch` — send tasks to workers
- `herd status` — monitor progress
