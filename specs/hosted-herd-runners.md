# Hosted Herd Runners

## Purpose

Capture the future product idea of running Herd as a fully hosted service, including GitHub App control plane and optional hosted runner execution.

This is not v1 scope. The v1 direction remains a hosted HerdOS GitHub App/control plane with user-owned GitHub Actions self-hosted runners. This spec exists so we do not lose the hosted-runner idea while designing the v1 control plane.

## Product Motivation

User-owned runners are powerful, but they add operational burden:

- installing Docker,
- starting and monitoring runner containers,
- keeping runner images updated,
- managing runner host capacity,
- configuring auth volumes,
- debugging runner connectivity,
- managing project-specific Docker overrides.

A fully hosted Herd execution backend could make onboarding much simpler:

1. Install the HerdOS GitHub App.
2. Configure agent credentials/secrets.
3. Let Herd run the work.

That is a much better user experience for small teams and less infrastructure-heavy projects.

## Execution Backend Model

Long term, Herd should treat execution as a backend choice:

```text
execution_backend:
  type: github-actions-self-hosted
```

Potential future backends:

```text
github-actions-self-hosted   # user-owned runners, v1 default
herd-hosted-linux            # simple managed Linux workers
herd-hosted-custom-image     # managed workers using project-specific images
```

The orchestration/control-plane design should avoid assuming that all execution is permanently tied to user-owned GitHub Actions runners.

## Hosted Levels

### Level 1: Hosted Control Plane, User-Owned Runners

This is the v1 direction.

Herd hosts:

- GitHub App,
- webhooks,
- command routing,
- orchestration state,
- GitHub-visible mutations,
- review blocking status.

Users host:

- GitHub Actions self-hosted runners,
- Docker runner environment,
- project-specific dependencies,
- agent credentials and auth volumes.

This solves identity and GitHub-native UX without Herd taking responsibility for arbitrary build environments.

### Level 2: Hosted Generic Linux Runners

Herd provides a standard Linux execution environment for simple projects.

Good fit:

- Go projects,
- Node projects,
- Python projects,
- docs-only repos,
- simple unit-test suites,
- projects with no external services or only common package managers.

Poor fit:

- apps requiring custom system packages,
- Docker Compose integration tests,
- private package registries,
- browser/system dependencies,
- large native builds,
- databases/services with project-specific setup.

This could be an optional convenience tier with clear limitations.

### Level 3: Hosted Custom Runner Images

Herd builds and runs project-specific runner images.

Users provide one or more of:

- `Dockerfile.herd_runner`,
- a base image reference,
- build secrets,
- cache settings,
- service definitions,
- environment variables/secrets,
- possibly a compose-like service file.

This is the most powerful hosted model and the closest equivalent to the current self-hosted runner customization story.

## Main Challenge: Project Customization

The hard part is preserving the flexibility users currently get from:

- `Dockerfile.herd_runner`,
- `docker-compose.herd.override.yml`,
- `.env`,
- named auth volumes,
- project-specific tools and services.

Hosted runners would need answers for:

- language runtime installation,
- system package installation,
- private dependency credentials,
- private Git dependencies,
- custom CLIs,
- browser dependencies,
- databases and service containers,
- Docker-in-Docker or Compose-like workloads,
- persistent caches,
- agent auth persistence,
- per-repo secrets,
- network controls,
- artifact/log retention.

## Security and Isolation Concerns

Hosted execution means Herd runs untrusted user code. That changes the risk profile substantially.

The hosted runner platform would need:

- strong tenant isolation,
- per-job ephemeral filesystems,
- secrets redaction,
- network egress controls,
- resource limits,
- CPU/memory/time quotas,
- cache isolation,
- build secret isolation,
- abuse detection,
- safe log/artifact handling,
- incident response procedures,
- billing and usage controls.

This is closer to building hosted CI infrastructure than simply hosting a GitHub App.

## Possible Architecture

The hosted control plane schedules work onto an execution backend.

```text
GitHub App Webhook
        |
        v
Herd Control Plane
        |
        +--> github-actions-self-hosted backend
        |
        +--> herd-hosted-linux backend
        |
        +--> herd-hosted-custom-image backend
```

Each backend should expose a common internal contract:

- start job,
- cancel job,
- stream logs,
- collect result,
- collect patch/branch/artifacts,
- report environment metadata,
- enforce timeout,
- enforce resource limits.

## Open Questions

- Should hosted runners execute inside GitHub Actions larger runners, cloud VMs, Firecracker/microVMs, Kubernetes jobs, or another isolation layer?
- Should custom images be built by Herd or supplied by users through a registry?
- How should Herd handle `docker compose` projects in hosted mode?
- Can hosted execution reuse the same `Dockerfile.herd_runner` contract as self-hosted execution?
- How should agent auth work in hosted mode, especially subscription auth volumes?
- What secrets model is acceptable for private package registries and app credentials?
- What cache model is safe and useful?
- Should hosted runners be opt-in per repo, per batch, or per task?
- How should usage/billing be measured: wall time, CPU/memory, token cost, storage, or all of these?
- Should hosted generic runners be offered before hosted custom images?

## Suggested Future Path

Do not block v1 on hosted runners.

After the hosted GitHub App/control plane exists, add an execution backend abstraction. Keep `github-actions-self-hosted` as the default backend, then experiment with a constrained `herd-hosted-linux` backend for simple repositories.

Only add `herd-hosted-custom-image` after the control plane, result reporting, secrets model, and isolation story are mature.
