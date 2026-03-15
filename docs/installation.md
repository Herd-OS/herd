# Installation

## Homebrew (macOS and Linux)

```bash
brew install herd-os/tap/herd
```

## Binary Download

Download the latest release for your platform from the [GitHub Releases page](https://github.com/Herd-OS/herd/releases/latest).

```bash
# Linux (amd64)
curl -L https://github.com/Herd-OS/herd/releases/latest/download/herd-linux-amd64 -o herd

# Linux (arm64)
curl -L https://github.com/Herd-OS/herd/releases/latest/download/herd-linux-arm64 -o herd

# macOS (Apple Silicon)
curl -L https://github.com/Herd-OS/herd/releases/latest/download/herd-darwin-arm64 -o herd

# macOS (Intel)
curl -L https://github.com/Herd-OS/herd/releases/latest/download/herd-darwin-amd64 -o herd
```

Verify the checksum (optional but recommended):

```bash
curl -L https://github.com/Herd-OS/herd/releases/latest/download/checksums.txt -o checksums.txt
sha256sum herd
# Compare the output against the matching line in checksums.txt
```

Then install:

```bash
chmod +x herd
sudo mv herd /usr/local/bin/
```

## From Source

Requires Go 1.26 or later.

```bash
git clone https://github.com/Herd-OS/herd.git
cd herd
make build
```

The binary is built to `bin/herd`. Add it to your `PATH` or move it to a directory already in your `PATH`:

```bash
sudo cp bin/herd /usr/local/bin/
```

## Updating

### Update the binary

```bash
# Homebrew
brew upgrade herd-os/tap/herd

# Binary download — same as installation, replace the existing binary

# From source
git pull && make build && sudo cp bin/herd /usr/local/bin/
```

### Update project workflows

After updating the binary, re-run `herd init` in each repository that uses HerdOS. This updates the workflow files and runner infrastructure to match the new version.

```bash
cd /path/to/your/repo
herd init
```

`herd init` creates a `herd/init-<version>` branch, commits the updated files, pushes, and opens a PR. Review and merge the PR to apply the changes. Configuration (`.herdos.yml`) and role instructions (`.herd/*.md`) are never overwritten.

### Update runner containers

Runner containers automatically download the latest herd binary on startup. Just restart them:

```bash
docker compose -f docker-compose.herd.yml restart
```

To pin a specific version, set `HERD_VERSION` in `.env`:

```bash
HERD_VERSION=v0.1.0-rc.2
```

## Verify Installation

```bash
herd --version
```

## Prerequisites

- **Git** — Herd operates on git repositories
- **GitHub CLI** (`gh`) — optional, used as fallback for label creation during `herd init`
- **GitHub account** — with write access to the target repository
- **Self-hosted runners** — for worker execution. See [Runner Setup](runners.md) for Docker-based runner configuration
- **GitHub Personal Access Token** — for runner registration and workflow dispatch. See [Runner Setup](runners.md) for details
