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

You can preview what would change first with `herd init --check`, which re-renders every managed file and exits 1 if any are out of date — see [Runner Setup → Checking for updates](runners.md#8-checking-for-updates) for details.

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

## Release process (maintainers)

This section describes what happens when a maintainer cuts a release of `herd` itself. End users do not need to read it — it is for maintainers of the `Herd-OS/herd` repository.

### Triggering a release

Pushing a tag of the form `vX.Y.Z` triggers `.github/workflows/release.yml`:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The workflow has two jobs that run sequentially: `release`, then `self-init`.

### `release` job

Builds binaries for all supported platforms, generates `checksums.txt`, and:

- Uploads the linux-amd64 binary as a workflow artifact (`herd-linux-amd64`, retained for 1 day) so the follow-up `self-init` job can use the exact bits that were just built without racing the GitHub Release publish.
- Publishes a GitHub Release via [`softprops/action-gh-release`](https://github.com/softprops/action-gh-release) with all platform binaries plus `checksums.txt` attached.
- Updates the `herd-os/homebrew-tap` formula via `scripts/update-homebrew.sh` (only if `HERD_GITHUB_TOKEN` is set).

### `self-init` job

After `release` succeeds, `self-init` regenerates the herd-managed files in this repository so the templates committed to `main` always match the version that was just released.

The job:

1. Checks out `main` with `fetch-depth: 0` (full history needed for the PR push).
2. Downloads the just-built `herd-linux-amd64` artifact (avoiding a race against GitHub Release publication).
3. Sets `HERD_VERSION=${{ github.ref_name }}` and runs `./herd init --skip-labels` against the herd repo itself.
4. `herd init` regenerates the workflow files, runner Dockerfiles, entrypoint, `docker-compose.herd.yml`, and `.env.herd.example`. If anything changed, it commits to branch `herd/init-<tag>`, pushes, and opens a PR titled **`Update HerdOS to <tag>`**.
5. If no herd-managed files changed in this release, no commit, no branch, and no PR are produced. The workflow logs:

   ```
   No herd-managed files changed in this release; skipping self-init PR.
   ```

   and exits 0.

The end-to-end behavior of `herd init --skip-labels` is covered by `TestRunInitSkipLabelsEndToEnd` and `TestRunInitSkipLabelsIdempotent` in `internal/cli/init_test.go`.

### Maintainer follow-up

After each release, check whether a `Update HerdOS to <tag>` PR was opened against `Herd-OS/herd`. If so, review the diff and merge it — this keeps the templates that ship with `herd init` in sync with the released CLI. If no PR appears, the release did not change any herd-managed file and no action is required.

### Permissions and token

The `self-init` job needs `contents: write` and `pull-requests: write` permissions (already declared on the job).

For the GitHub token, the job uses:

```yaml
token: ${{ secrets.HERD_GITHUB_TOKEN || secrets.GITHUB_TOKEN }}
```

Prefer `HERD_GITHUB_TOKEN` (a PAT with `contents` and `pull-requests` write) when available — PRs created by the default `GITHUB_TOKEN` do not trigger downstream workflows, which can hide CI failures on the self-init PR. The fallback to `GITHUB_TOKEN` keeps the job functional in forks or environments where the secret is not configured.
