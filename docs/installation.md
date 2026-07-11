# Installation

## Homebrew (macOS and Linux)

```bash
brew install herd-os/tap/herd
```

## Arch Linux (AUR)

```bash
yay -S herd-bin   # or: paru -S herd-bin
```

`herd-bin` installs the pre-built linux-amd64 / linux-arm64 binary from the latest GitHub Release and tracks tagged releases automatically. Manual install (no AUR helper):

```bash
git clone https://aur.archlinux.org/herd-bin.git
cd herd-bin
makepkg -si
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

### Upgrade notifications

`herd plan` and `herd init --check` perform a best-effort version check at start-up and print a one-line warning when a newer release is published. The check queries `https://api.github.com/repos/Herd-OS/herd/releases/latest` with a 3-second timeout; if it fails (no network, GitHub unreachable, non-200 response, etc.) nothing is printed and the command continues normally — the warning never blocks, and a missing warning is not a failure.

Example warning:

```
⚠ A newer herd version is available: v0.6.0 (you are running v0.5.3). See https://github.com/Herd-OS/herd/releases/latest
```

When you see the warning, follow the [release page](https://github.com/Herd-OS/herd/releases/latest) and the steps below to upgrade.

The check is skipped by design for development builds (`herd --version` reports `dev`) and for pre-release tags (anything containing `-`, e.g. `v0.5.3-rc.1`), so contributors and rc testers are not nagged about a "newer" stable release.

### Update the binary

```bash
# Homebrew
brew upgrade herd-os/tap/herd

# AUR (Arch Linux)
yay -Syu herd-bin

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

You can preview what would change first with `herd init --check`, which re-renders every managed file and exits 1 if any are out of date — see [Runner Setup → Checking for updates](runners.md#10-checking-for-updates) for details.

`herd init` creates a `herd/init-<version>` branch, commits the updated files, pushes, and opens a PR. Review and merge the PR to apply the changes. Configuration (`.herdos.yml`) and role instructions (`.herd/*.md`) are never overwritten.

`Dockerfile.herd_runner` is also user-owned and not overwritten, but `herd init` will auto-migrate its `FROM` line if it still references the pre-GHCR local base — `FROM herd-runner-base[:tag]` is rewritten to `FROM ghcr.io/herd-os/herd-runner-base:<herd-version>` while everything else in the file is left byte-identical. So the per-project upgrade flow is: upgrade the binary → `herd init` → merge the PR → redeploy runner containers, with no manual Dockerfile edit required. See [Runner Setup → Migrating from the local base image](runners.md#migrating-from-the-local-base-image) for the full migration behavior.

### Update runner containers

Runner containers automatically download the latest herd binary on startup. Just restart them — for Docker Compose:

```bash
docker compose -f docker-compose.herd.yml restart
```

Or, if you started the runners with [direct `docker run`](runners.md#running-runners-directly-with-docker-run):

```bash
docker restart herd-worker-1 herd-worker-2 herd-worker-3
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
- **Docker** — required only for `agent.exec: docker` (running `herd plan` inside the runner image). Not needed for the default `local` execution. Also used for self-hosted runners (see [Runner Setup](runners.md))
- **GitHub CLI** (`gh`) — required on the machine where you run `herd init`; run `gh auth login -h github.com` first. `gh` is a setup-time dependency only and is not required in worker/runtime containers.
- **GitHub account** — with administrator access to the target repository for setup
- **HerdOS GitHub App** — install the official `@herd-os` App on the target repository before `herd init`
- **Self-hosted runners** — for worker execution. See [Runner Setup](runners.md) for Docker-based runner configuration

Production Herd orchestration no longer uses user-created GitHub Personal Access
Tokens or a `HERD_GITHUB_TOKEN` secret. The GitHub App and HerdOS control plane
own GitHub mutations; runner containers use `HERD_RUNNER_BOOTSTRAP_TOKEN` to
obtain short-lived runner registration tokens.

## Release process (maintainers)

This section describes what happens when a maintainer cuts a release of `herd` itself. End users do not need to read it — it is for maintainers of the `Herd-OS/herd` repository.

For day-to-day edits to workflow files between releases, see [`../CONTRIBUTING.md`](../CONTRIBUTING.md). Templates under `internal/cli/workflows/*.tmpl` are the source of truth; CI runs `herd init --check` and rejects PRs where the rendered `.github/workflows/herd-*.yml` files have drifted from their templates.

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
- Publishes the control-plane service image as `ghcr.io/herd-os/herd-service`.
- Updates the `herd-os/homebrew-tap` formula via `scripts/update-homebrew.sh` (only if `HERD_GITHUB_TOKEN` is set).
- Updates the `herd-bin` AUR package via `scripts/update-aur.sh` (only if `AUR_SSH_PRIVATE_KEY` is set). The step runs inside an `archlinux:base-devel` container because `makepkg --printsrcinfo` is required to regenerate `.SRCINFO` and is not available on Ubuntu runners.

### `self-init` job

After `release` succeeds, `self-init` regenerates the herd-managed files in this repository so the templates committed to `main` always match the version that was just released.

The job:

1. Checks out `main` with `fetch-depth: 0` (full history needed for the PR push).
2. Downloads the just-built `herd-linux-amd64` artifact (avoiding a race against GitHub Release publication).
3. Sets `HERD_VERSION=${{ github.ref_name }}` and runs `./herd init --skip-labels` against the herd repo itself.
4. `herd init` regenerates the workflow files, entrypoint, `docker-compose.herd.yml`, and `.env.herd.example` (the user-owned `Dockerfile.herd_runner` is created once and never overwritten). If anything changed, it commits to branch `herd/init-<tag>`, pushes, and opens a PR titled **`Update HerdOS to <tag>`**.
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

This maintainer release job has its own publishing token choice. It is not part
of end-user HerdOS repository setup and is not used for production Herd
orchestration in user repositories.

For the GitHub token, the job uses:

```yaml
token: ${{ secrets.HERD_GITHUB_TOKEN || secrets.GITHUB_TOKEN }}
```

For this maintainer-only release workflow, `HERD_GITHUB_TOKEN` may be a token
with `contents` and `pull-requests` write so self-init PRs trigger downstream
workflows. The fallback to `GITHUB_TOKEN` keeps the job functional in forks or
environments where the secret is not configured.

### AUR (herd-bin) setup

The `Update AUR (herd-bin)` step in the release job pushes a new `PKGBUILD` + `.SRCINFO` to `ssh://aur@aur.archlinux.org/herd-bin.git` on every tag. It is gated on the `AUR_SSH_PRIVATE_KEY` secret being set; if the secret is absent, the step is skipped and the release completes normally without an AUR update.

**One-time setup (only needed once per maintainer / per repo):**

1. Create an AUR account at https://aur.archlinux.org/register.
2. Generate a dedicated SSH key (no passphrase if it will be stored as a GitHub Actions secret):

   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/aur_ed25519 -N "" -C "aur-herd-bin"
   ```

3. Add the **public** key (`~/.ssh/aur_ed25519.pub`) to your AUR account at https://aur.archlinux.org/account/ → "SSH Public Key".
4. On a local Arch box, clone the empty AUR repo, copy in a valid PKGBUILD, regenerate `.SRCINFO`, validate, and do the **initial push** (the workflow can only update an existing AUR repo, not create one):

   ```bash
   # Optional but recommended SSH config entry — keeps your GitHub keys from being offered first
   cat >> ~/.ssh/config << 'EOF'
   Host aur.archlinux.org
     HostName aur.archlinux.org
     User aur
     IdentityFile ~/.ssh/aur_ed25519
     IdentitiesOnly yes
   EOF

   git clone ssh://aur@aur.archlinux.org/herd-bin.git
   cd herd-bin
   # Copy a known-good PKGBUILD in (e.g. from a tagged release of this repo).
   # Then:
   makepkg --printsrcinfo > .SRCINFO
   makepkg -si          # local install — confirms the package works end-to-end
   git add PKGBUILD .SRCINFO
   git commit -m "Initial import (herd-bin v<X.Y.Z>)"
   git push
   ```

5. Add the **private** key as a GitHub Actions secret named `AUR_SSH_PRIVATE_KEY` (the entire contents of `~/.ssh/aur_ed25519`, including the `-----BEGIN OPENSSH PRIVATE KEY-----` header). From then on, every tag push triggers the workflow to regenerate the PKGBUILD with the new version + checksums, regenerate `.SRCINFO` via `makepkg --printsrcinfo`, and push to AUR as `herd-os[bot]`.

**Updating the PKGBUILD template:** the AUR PKGBUILD is generated inline by `scripts/update-aur.sh`. Edits to dependencies, optdepends, or the `package()` function go there. The script must remain runnable both inside the CI container and on a maintainer's local Arch box (it detects `EUID==0` and drops to a `builder` user for `makepkg`).

**Skipping AUR on a release:** unset or rename the `AUR_SSH_PRIVATE_KEY` secret temporarily. The release will still publish binaries and update Homebrew.
