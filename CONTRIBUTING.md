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
