# Project Structure

Repository layout and module boundaries for the `herd` CLI.

## Repo Layout

```
herd/
├── docs/
│   ├── specs/                    # These specification documents
│   ├── examples/                 # Example .herdos.yml configs
│   │   ├── README.md             # Index of example configs
│   │   ├── solo-dev.yml          # Solo developer setup
│   │   ├── small-team.yml        # Small team setup
│   │   └── ci-heavy.yml          # CI-heavy repo setup
│   ├── getting-started.md        # Getting started guide
│   ├── installation.md           # Installation guide (Homebrew, binary, source)
│   ├── configuration.md          # Configuration reference
│   └── runners.md                # Runner setup and troubleshooting
│
├── cmd/
│   └── herd/
│       └── main.go               # Entry point
│
├── internal/
│   ├── cli/                      # CLI command definitions
│   │   ├── root.go               # Root command, global flags
│   │   ├── errors.go             # Error handling helpers
│   │   ├── init.go               # herd init
│   │   ├── plan.go               # herd plan
│   │   ├── dispatch.go           # herd dispatch
│   │   ├── status.go             # herd status
│   │   ├── batch.go              # herd batch (subcommands)
│   │   ├── worker.go             # herd worker (used by Actions)
│   │   ├── integrator.go         # herd integrator (used by Actions)
│   │   ├── monitor.go            # herd monitor (used by Actions)
│   │   ├── runner.go             # herd runner (list)
│   │   ├── config.go             # herd config
│   │   ├── workflows.go          # Embedded workflow file helpers
│   │   ├── workflows/            # GitHub Actions workflow templates (embedded)
│   │   │   ├── herd-worker.yml   # Worker workflow (installed by herd init)
│   │   │   ├── herd-integrator.yml # Integrator workflow
│   │   │   └── herd-monitor.yml  # Monitor workflow
│   │   └── runner/               # Runner setup files (embedded)
│   │       ├── embed.go          # Embed directive for runner files
│   │       ├── Dockerfile.runner # Base image for Docker-based runners
│   │       ├── entrypoint.sh     # Runner entrypoint script
│   │       ├── docker-compose.herd.yml.tmpl # Compose template (owner/repo)
│   │       └── .env.example      # Environment variable template
│   │
│   ├── agent/                    # Agent abstraction
│   │   ├── agent.go              # Agent interface and types
│   │   └── claude/               # Claude Code implementation
│   │       └── plan.go           # Planning via Claude Code
│   │
│   ├── planner/                  # Planning logic
│   │   └── planner.go            # Orchestrates planning flow
│   │
│   ├── worker/                   # Worker execution logic
│   │   ├── worker.go             # Worker lifecycle (read issue, branch, invoke agent, push)
│   │   └── doc.go                # Package documentation
│   │
│   ├── integrator/               # Integration logic
│   │   ├── integrator.go         # Consolidation, tier advancement, batch PR
│   │   ├── merge.go              # Branch merging logic
│   │   ├── review.go             # Agent review dispatch and result handling
│   │   └── ci.go                 # CI failure handling and fix cycles
│   │
│   ├── monitor/                  # Monitor logic
│   │   └── patrol.go             # Patrol cycle, stale detection, escalation
│   │
│   ├── dag/                      # DAG and tier logic (shared by dispatch, plan, integrator)
│   │   └── dag.go                # DAG construction, topological sort, tier assignment, cycle detection
│   │
│   ├── git/                      # Git CLI wrapper
│   │   └── git.go                # Checkout, branch, merge, rebase, push, force-push
│   │
│   ├── platform/                 # Platform abstraction
│   │   ├── platform.go           # Interface definitions
│   │   ├── types.go              # Platform-agnostic types
│   │   └── github/               # GitHub implementation
│   │       ├── client.go         # GitHub API client wrapper
│   │       ├── issues.go         # Issue operations
│   │       ├── pullrequests.go   # PR operations
│   │       ├── workflows.go      # Action dispatch
│   │       ├── labels.go         # Label management
│   │       ├── milestones.go     # Milestone (batch) operations
│   │       ├── runners.go        # Runner queries
│   │       └── checks.go         # CI check status queries
│   │
│   ├── config/                   # Configuration
│   │   ├── config.go             # Config struct and loading
│   │   ├── defaults.go           # Default values
│   │   └── validate.go           # Validation rules
│   │
│   ├── issues/                   # Issue management logic
│   │   ├── template.go           # Issue body template generation
│   │   ├── parser.go             # Parse issue body (YAML front matter)
│   │   ├── labels.go             # Label constants and helpers
│   │   └── lifecycle.go          # State machine transitions
│   │
│   └── display/                  # Terminal output formatting
│       ├── table.go              # Table rendering
│       ├── status.go             # Status display and symbols
│       └── colors.go             # Color and style helpers
│
├── tests/
│   └── e2e/                     # End-to-end tests (real GitHub, real agent)
│       └── e2e_test.go
│
├── scripts/
│   └── update-homebrew.sh        # Updates Homebrew tap formula after release
│
├── .golangci.yml                 # golangci-lint configuration
├── go.mod
├── go.sum
├── Makefile
├── .herdos.yml                   # Dogfooding: HerdOS manages itself
├── .herd/                        # Role instruction files (committed) + state (gitignored)
│   ├── planner.md                # Planner role instructions
│   ├── worker.md                 # Worker role instructions
│   └── integrator.md             # Integrator role instructions
└── .github/
    └── workflows/
        ├── ci.yml                # CI for the herd CLI itself
        └── release.yml           # Build and publish binaries
```

## Module Boundaries

```
cmd/herd ──▶ internal/cli ──▶ internal/planner ──▶ internal/agent
                  │                 │
                  │                 ├──────▶ internal/issues
                  │                 │
                  ├──────▶ internal/worker ──────▶ internal/agent
                  │              ├──────▶ internal/platform
                  │              └──────▶ internal/git
                  │
                  ├──────▶ internal/integrator ──▶ internal/agent
                  │              ├──────▶ internal/platform
                  │              ├──────▶ internal/git
                  │              └──────▶ internal/dag
                  │
                  ├──────▶ internal/monitor ──▶ internal/platform
                  │
                  ├──────▶ internal/platform
                  │              │
                  │              ▼
                  │       internal/platform/github
                  │
                  ├──────▶ internal/issues
                  ├──────▶ internal/dag
                  ├──────▶ internal/git
                  ├──────▶ internal/config
                  │
                  └──────▶ internal/display
```

### Dependency Rules

- `cmd/herd` only imports `internal/cli`
- `internal/cli` imports everything else and injects config into each component
- `internal/planner` imports `internal/agent` (to run planning sessions) and `internal/issues` (for templates). Returns a plan — the CLI creates issues via `internal/platform`
- `internal/worker` imports `internal/agent` (to execute tasks), `internal/platform` (to read issues, update labels), `internal/git` (to branch and push)
- `internal/integrator` imports `internal/agent` (for reviews), `internal/platform` (to open PRs, dispatch workers, create fix issues), `internal/git` (to merge, rebase, push), `internal/dag` (for tier logic)
- `internal/monitor` imports `internal/platform` (to query issues and runs, post comments, dispatch workers)
- `internal/dag` imports `internal/platform` (types only — `Issue`, `Milestone`)
- `internal/git` has no internal dependencies (wraps `git` CLI)
- `internal/agent/claude` implements `internal/agent` interfaces
- `internal/platform/github` implements `internal/platform` interfaces
- `internal/config` has no internal dependencies
- `internal/display` has no internal dependencies
- `internal/issues` has no internal dependencies (pure logic)

Config is not imported directly by worker, integrator, or monitor — `internal/cli` loads config and passes relevant values as struct fields or function parameters. This keeps config at the edge.

No circular dependencies. The platform interface is the key boundary — nothing above it knows about GitHub specifically.

## Key Files

### cmd/herd/main.go
Entry point. Initializes the root Cobra command, loads config, creates the platform client, and runs.

### internal/platform/platform.go
The `Platform` interface. All platform interactions go through this. See [abstraction-layers.md](../03-cli/03-abstraction-layers.md).

### internal/agent/agent.go
The `Agent` interface. Abstracts the AI coding tool (Claude Code, Codex, Cursor, Gemini CLI, OpenCode). See [abstraction-layers.md](../03-cli/03-abstraction-layers.md).

### internal/planner/planner.go
Orchestrates the planning flow. Calls the Agent interface to launch an interactive planning session (with an optional initial prompt), then returns a structured plan (list of tasks with dependencies). The Planner returns a plan — the CLI command (`internal/cli/plan.go`) uses `internal/platform` and `internal/issues` to create GitHub Issues from it.

### internal/issues/template.go
Generates issue bodies from plan tasks. Produces the YAML front matter and markdown body format defined in [issues.md](../02-github/01-issues.md).

### internal/config/config.go
Loads `.herdos.yml`, applies environment variable overrides, validates, and provides the config to all other modules.

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/spf13/cobra` | CLI framework (same as `gh`, `kubectl`) |
| `github.com/google/go-github/v68` | GitHub REST API client |
| `golang.org/x/oauth2` | GitHub token auth |
| `gopkg.in/yaml.v3` | `.herdos.yml` parsing |
| `github.com/charmbracelet/lipgloss` | Terminal styling and colors |
| `github.com/stretchr/testify` | Test assertions |

## Distribution

### Binary releases

The `release.yml` workflow builds binaries for all platforms on version tags. Users download the binary for their platform from the GitHub Releases page.

### Homebrew

A Homebrew tap at `herd-os/homebrew-tap` provides the formula. The tap repo contains a single formula that points to the GitHub Release binaries:

```ruby
class Herd < Formula
  desc "GitHub-native orchestration for agentic development"
  homepage "https://github.com/herd-os/herd_os"
  version "1.0.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/herd-os/herd_os/releases/download/v1.0.0/herd-darwin-arm64"
      sha256 "..."
    else
      url "https://github.com/herd-os/herd_os/releases/download/v1.0.0/herd-darwin-amd64"
      sha256 "..."
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/herd-os/herd_os/releases/download/v1.0.0/herd-linux-arm64"
      sha256 "..."
    else
      url "https://github.com/herd-os/herd_os/releases/download/v1.0.0/herd-linux-amd64"
      sha256 "..."
    end
  end

  def install
    bin.install stable.url.split("/").last => "herd"
  end

  test do
    assert_match "herd version", shell_output("#{bin}/herd --version")
  end
end
```

Installation:

```bash
brew install herd-os/tap/herd
```

The formula is updated automatically by the release workflow — after uploading binaries, it computes SHA256 checksums and pushes an updated formula to the tap repo.

## Release Workflow

`.github/workflows/release.yml` — triggered on version tags, builds binaries for all platforms and publishes a GitHub Release:

```yaml
name: Release
on:
  push:
    tags: ['v*']

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: 'go.mod'

      - name: Build binaries
        run: |
          VERSION=${GITHUB_REF#refs/tags/}
          make release VERSION=$VERSION

      - name: Generate checksums
        run: cd bin && sha256sum herd-* > checksums.txt

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          files: |
            bin/herd-linux-amd64
            bin/herd-linux-arm64
            bin/herd-darwin-amd64
            bin/herd-darwin-arm64
            bin/herd-windows-amd64.exe
            bin/checksums.txt

      - name: Update Homebrew tap
        env:
          TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
        run: |
          VERSION=${GITHUB_REF#refs/tags/}
          ./scripts/update-homebrew.sh $VERSION bin/checksums.txt
```

To cut a release:

```bash
git tag v1.0.0
git push origin v1.0.0
# → GitHub Actions builds binaries and creates the release
```

## Build

```makefile
VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS = -ldflags="-X main.version=$(VERSION)"

# Build for current platform
build:
	go build $(LDFLAGS) -o bin/herd ./cmd/herd

# Build for all platforms
release:
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o bin/herd-linux-amd64 ./cmd/herd
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o bin/herd-linux-arm64 ./cmd/herd
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o bin/herd-darwin-amd64 ./cmd/herd
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o bin/herd-darwin-arm64 ./cmd/herd
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/herd-windows-amd64.exe ./cmd/herd

# Run tests
test:
	go test ./...

# Lint
lint:
	golangci-lint run
```
