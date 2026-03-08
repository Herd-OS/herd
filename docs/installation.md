# Installation

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

## Verify Installation

```bash
herd --version
```

## Prerequisites

- **Git** — Herd operates on git repositories
- **GitHub CLI** (`gh`) — optional, used as fallback for label creation during `herd init`
- **GitHub account** — with write access to the target repository
