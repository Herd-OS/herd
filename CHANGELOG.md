# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Published GHCR runner base image at `ghcr.io/herd-os/herd-runner-base` — public, multi-arch (linux/amd64, linux/arm64), version-pinned to the herd release.
- `herd image build` and `herd image publish` commands to build and push a customized runner image to `ghcr.io/<owner>/<repo>-herd-runner`.
- `.github/workflows/herd-publish-runner.yml` auto-publish workflow that builds and pushes the consumer runner image on changes to `Dockerfile.herd_runner` (gated on `HERD_ENABLED`, requires `packages: write`).

### Changed

- `herd init` no longer generates `Dockerfile.herd_runner_base`; an existing one is removed and the base service is dropped from `docker-compose.herd.yml`.
- `Dockerfile.herd_runner` now uses `FROM ghcr.io/herd-os/herd-runner-base:<version>` (pulled from GHCR) instead of the locally built `herd-runner-base`.
