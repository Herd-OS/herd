# Hosted Control Plane

HerdOS has two execution boundaries:

| Boundary | Hosted HerdOS provides | You provide |
|----------|------------------------|-------------|
| Control plane | Official GitHub App `@herd-os`, webhook receiver, GitHub API mutations, runner registration-token minting, job callback handling, Herd Review status/review/comment updates | Repository access by installing the App |
| Execution | Dispatch and coordination through GitHub Actions | Self-hosted runner containers, project toolchains, source checkout, tests, and AI agent credentials |

The production hosted API is:

```text
https://api.herd-os.com
```

The official App webhook target is:

```text
https://api.herd-os.com/webhooks/github
```

## Setup Flow

1. Install the official HerdOS GitHub App on selected repositories.
2. Install GitHub CLI locally and run:

   ```bash
   gh auth login -h github.com
   ```

3. Run `herd init` in the repository.
4. Merge the initialization PR.
5. Start your self-hosted runner containers with the generated `.env`.
6. Enable workflows:

   ```bash
   gh variable set HERD_ENABLED --body true --repo <owner>/<repo>
   ```

`gh` is only needed where you run `herd init`. It is not required in worker or
runtime containers.

## Why Production Jobs No Longer Use PATs

Older HerdOS installs used user-created Personal Access Tokens for workflow
dispatch, runner registration, and GitHub mutations. That made production jobs
act as a human account and forced users to manage broad, long-lived credentials.

The hosted App model replaces that with installation-scoped GitHub App auth.
Workers use `HERD_RUNNER_BOOTSTRAP_TOKEN`, a Herd service credential, to ask the
control plane for short-lived runner registration tokens. GitHub-visible actions
are attributed to `herd-os[bot]`. Agent credentials remain in your runner
environment and are never hosted by HerdOS.

## Branch Protection

Branch protection remains user-configured. HerdOS does not create or mutate
branch protection rules.

When `integrator.review` is enabled and you want Herd Review to block merges,
require the commit status named exactly `Herd Review` on protected branches. Do
not require that status for repositories where Herd Review is disabled.

## Commands

Mention commands use the App login:

```text
@herd-os review
@herd-os fix <description>
@herd-os fix-ci
@herd-os retry
@herd-os retry <issue-number>
@herd-os integrate
@herd-os dispatch
@herd-os dispatch <issue-number>
```

`/herd` slash-comment commands are no longer supported and should not be used in
new workflows or docs.

## Migrating From PAT-Based Installs

1. Upgrade the `herd` binary.
2. Install the `@herd-os` GitHub App on the repository.
3. Run `gh auth login -h github.com` locally with an admin-capable account.
4. Re-run `herd init` and merge the generated PR.
5. Confirm `.env` contains `HERD_RUNNER_BOOTSTRAP_TOKEN`.
6. Remove obsolete production orchestration credentials:

   ```text
   HERD_GITHUB_TOKEN
   GITHUB_TOKEN in runner .env
   any Herd-specific human PAT used only for dispatch or runner registration
   ```

7. Restart runner containers.
8. If Herd Review should block merges, require the `Herd Review` status in
   branch protection.

Keep unrelated secrets that your project needs, such as package registry tokens
listed under `workers.extra_env`.

## Self-Hosted Override

Most users should omit `HERD_CONTROL_PLANE_URL` and use the hosted default. If
you operate your own control plane, initialize repositories with:

```bash
herd init --control-plane-url https://herd.example.com
```

The generated runner environment includes:

```bash
HERD_CONTROL_PLANE_URL=https://herd.example.com
HERD_RUNNER_BOOTSTRAP_TOKEN=hrb_...
```

See [service.md](service.md) for self-hosted service operation.
