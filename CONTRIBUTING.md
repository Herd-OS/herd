# Contributing to Herd

Thanks for your interest in contributing. This document covers project-
specific contribution guidelines. For general setup, see
[docs/getting-started.md](docs/getting-started.md).

## Workflow files

The files at `.github/workflows/herd-*.yml` are regenerated from templates
in `internal/cli/workflows/*.tmpl` whenever `herd init` runs (including the
automated self-init on every release). To change a workflow, edit the
TEMPLATE, not the rendered file:

1. Edit `internal/cli/workflows/herd-<name>.yml.tmpl`
2. Run `go build -o ./herd ./cmd/herd && ./herd init --skip-labels` to
   regenerate `.github/workflows/herd-<name>.yml`
3. Commit both files together

CI runs `herd init --check` and will reject PRs that have drifted files.

## Releasing the runner images

The `Release` workflow (`.github/workflows/release.yml`) builds and pushes the
public runner images (`ghcr.io/herd-os/herd-runner-{base,node,ruby,python,go}`)
on every `v*` tag. GHCR creates newly pushed packages as **private** by default,
and the workflow has no permission to change package visibility. After the
**first** release that publishes a given package, a maintainer must make it
public once: go to https://github.com/orgs/herd-os/packages, open each
`herd-runner-*` package → **Package settings** → **Change visibility** →
**Public**. Until that is done, `docker pull ghcr.io/herd-os/herd-runner-base:latest`
from an unauthenticated client will fail. Subsequent releases inherit the public
setting, so this is a one-time step per package.
