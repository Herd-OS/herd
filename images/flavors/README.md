# Runner image flavors

First-party, single-language runner images layered on `herd-runner-base`.
Published to `ghcr.io/herd-os/herd-runner-<flavor>` on every herd release,
multi-arch (linux/amd64, linux/arm64), pinned to the matching base version.

## Scoping decision (deliberate)

Flavors are intentionally small and single-language: node, ruby, python, go.
We do NOT ship combination images (e.g. ruby+node). Multi-toolchain setups are
the user's job: extend `Dockerfile.herd_runner` with additional `RUN apt-get
install` lines (or change the FROM flavor). Each flavor tracks the current
stable of its language; pinning a specific language version is also a user
extension via `Dockerfile.herd_runner`.

## Flavors

| Image | Contents |
|-------|----------|
| herd-runner-base | OS + GitHub Actions runner + Node 22 + git/gh/curl/jq |
| herd-runner-node | base + current Node LTS |
| herd-runner-ruby | base + current stable Ruby + bundler |
| herd-runner-python | base + current stable Python 3 + pip + venv |
| herd-runner-go | base + current stable Go |
