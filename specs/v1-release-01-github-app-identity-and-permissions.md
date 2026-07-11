# v1 Release: Hosted GitHub App Identity and Permissions

## Purpose

Make HerdOS operate as a GitHub-native bot identity through an official hosted HerdOS GitHub App, while keeping all code execution on the user's own GitHub Actions self-hosted runners.

This replaces the current PAT-centered model where Herd jobs act as the human user's GitHub account. The v1 target is:

- HerdOS hosts the official GitHub App and owns the App private key.
- Users install the HerdOS GitHub App on selected repositories.
- Users still run their own self-hosted GitHub Actions runners.
- The hosted HerdOS control plane owns GitHub-visible side effects.
- Workers execute agent/code/test work and report structured results back to the control plane.

## Why This Matters Before v1

The current GitHub auth model causes two major product problems:

1. Herd-created commits and PRs are attributed to the human PAT owner. If the user dispatching Herd is also the PR creator, GitHub prevents them from reviewing or approving the PR normally.
2. Herd review is not a required merge signal. A PR can be merged while Herd review is still running or while a review/fix cycle is active.

The hosted App model fixes this by making `herd-os[bot]` the GitHub actor for PRs, comments, reviews, statuses, labels, workflow dispatch, and merges.

## Product Direction

Do not require users to create their own GitHub Apps for the normal v1 path. That is poor UX and makes users manage private keys, permissions, and installation IDs.

Also support a fully self-hosted deployment mode for users who do not want to use HerdOS-operated servers. Self-hosted users should run the same Go control-plane API service themselves, backed by their own Postgres database and their own GitHub App registration. Consumer repos then point their Herd configuration/workflows at that self-hosted control-plane URL.

Do not build a broad installation-token broker as the target architecture. Passing general-purpose GitHub App installation tokens into worker jobs weakens the trust boundary and creates a temporary architecture that would be hard to reason about later.

Instead, build a hosted HerdOS control-plane API that can perform GitHub mutations itself. GitHub App identity is the first v1 integration surface, but the service should be designed as the durable API foundation for future hosted Herd capabilities, including cloud `herd plan`, cloud-created task issues, and hosted orchestration that still hands execution to user-owned or future Herd-owned runners. Workers should have the minimum GitHub access necessary to do repository work and should report structured outcomes to the control plane for GitHub-visible actions.

## Non-Goals

- Do not host worker execution.
- Do not host agent execution.
- Do not run AI model calls in the hosted service.
- Do not host repository tests, builds, Docker jobs, or local repo work.
- Do not implement cloud `herd plan`, cloud issue creation, hosted workers, or hosted agent execution in this spec.
- Do not require users to create a GitHub App for the normal path.
- Do not require users to store the HerdOS App private key in their repos.
- Do not implement line-level review comments in this spec; that is covered by `v1-release-02-line-level-review-findings.md`.
- Do not mutate branch protection automatically in this spec.
- Do not preserve PAT-based production Herd orchestration. Local developer-only auth paths can remain only when they are clearly separated from worker/integrator/monitor job auth.
- Do not preserve `/herd` slash-comment commands as a production command interface.

## Target User Setup

The desired default hosted user flow is:

1. User installs the official HerdOS GitHub App on selected repositories.
2. User installs and authenticates GitHub CLI with `gh auth login`.
3. User runs `herd init`.
4. `herd init` verifies that the official App is installed on the repo and writes workflows/runner config.
5. User starts self-hosted runners as they do today.
6. User configures agent credentials in runner `.env` or auth volumes.
7. Herd jobs run on the user's runners, but GitHub-visible actions come from `herd-os[bot]`.

The user should not need to:

- create a GitHub App,
- generate a private key,
- add an App private key to Actions secrets,
- find an App installation ID,
- create a PAT for Herd workflow dispatch,
- create a PAT for self-hosted runner registration,
- understand GitHub App token minting.

The desired self-hosted user flow is:

1. User deploys the HerdOS control-plane API service.
2. User provisions Postgres for the service.
3. User creates a GitHub App for their organization/user account.
4. User configures that App's webhook URL to point at their control-plane service.
5. User configures the service with the App ID, private key, webhook secret, public URL, and database URL.
6. User installs their App on selected repositories.
7. User installs and authenticates GitHub CLI with `gh auth login`.
8. User runs `herd init` and points the repo at the self-hosted control-plane URL.
9. User starts self-hosted runners and configures agent credentials as usual.

Self-hosted mode is more work, but it preserves the open-source "host everything yourself" path without forcing that complexity onto the default hosted onboarding flow.

## Repository Registration During `herd init`

Removing production PAT auth must not make repository registration unauthenticated.

During `herd init`, the CLI must prove to the control plane that the human is allowed to enable Herd for the target repository before the service issues runner bootstrap credentials or writes service-side repo state.

Use GitHub CLI as the required v1 setup-only human authorization path:

1. `herd init` discovers the repository owner/name and selected control-plane URL.
2. The CLI verifies that the GitHub App is installed on the repository.
3. The CLI shells out to `gh auth token` to obtain a local human GitHub credential.
4. The CLI sends the repository identity and the setup credential to the control plane over TLS.
5. The control plane validates the credential with GitHub, verifies the human has sufficient repository authority to configure Herd, verifies the App installation can access the repository, then discards the credential.
6. The control plane records the repository as enabled and returns repo-scoped runner bootstrap configuration.

Requirements:

- `gh` is a hard setup-time dependency for `herd init` in this v1 flow.
- `herd init` must detect when `gh` is missing, unauthenticated, authenticated to the wrong host, or lacks required scopes/permissions, then print an actionable install/login/remediation message.
- `gh` must not be required inside generated GitHub Actions worker jobs, runner containers, integrator jobs, monitor jobs, or the hosted service container.
- This setup credential is allowed only for `herd init` repository registration and diagnostics.
- The service must never persist the human GitHub credential.
- The generated worker, integrator, monitor, and runner paths must not receive this credential.
- If setup authorization fails, `herd init` must stop with an actionable error.
- The minimum human repository permission for setup is `admin` or the GitHub API equivalent permission that can manage repository settings, secrets, workflows, and App installation-dependent configuration.

This preserves the product decision to remove human PATs from production Herd orchestration while still making repo onboarding secure.

## Target Architecture

Add a hosted HerdOS control-plane API service, implemented in Go. The GitHub App integration should be one module of the service, not the top-level service boundary.

Use this command shape:

- `cmd/herd-service` for the HTTP service entrypoint.
- `internal/service/...` for service setup, routing, middleware, configuration, API versioning, and lifecycle.
- `internal/appauth/...` for GitHub App JWT and installation-token helpers.
- `internal/controlplane/...` for command routing, job records, worker result ingestion, and service-side orchestration.
- `internal/controlplane/github/...` or a similarly scoped package for GitHub-specific webhook handling and GitHub side-effect orchestration.
- Reuse existing `internal/platform/github` client primitives where possible, but avoid forcing service-specific auth concerns into the generic platform interface prematurely.

The exact internal package names can differ if the existing codebase suggests better names, but the service binary should be `cmd/herd-service` and service code must stay clearly separated from CLI-only code.

### Responsibilities

The hosted service owns:

- GitHub App webhook handling.
- App installation/repository authorization.
- self-hosted runner registration-token brokering.
- command acknowledgement when events require one.
- workflow dispatch into the user's repository.
- GitHub comments/reactions.
- GitHub PR creation/update/merge.
- GitHub PR review submission.
- `Herd Review` commit status management.
- labels/milestones/issues that represent public Herd state.
- validation that worker result submissions belong to an installed repo and expected job.

The user's self-hosted GitHub Actions workers own:

- checking out code,
- running the selected agent,
- running every AI/model-backed operation, including worker implementation, fix jobs, plan jobs that exist in v1 workflows, and Herd Review analysis,
- editing files,
- running validation/tests,
- producing patch/bundle artifacts as designed below,
- reporting structured results to the hosted service.

The hosted service must not call Codex, Claude, OpenCode, Pi, or any other AI model provider in this v1 design. For Herd Review specifically, the service dispatches the review workflow, sets `Herd Review` pending, validates the review worker callback, stores the result, submits the App-authored PR review/status/comment, and coordinates any fix cycle. The review analysis itself must run on the user's self-hosted GitHub Actions worker.

### API Namespacing and Future Expansion

Do not design the hosted API as if it will only ever serve GitHub App webhooks.

Use explicit API namespaces and versioning so future hosted capabilities can be added without breaking v1 clients:

- `/webhooks/github` for GitHub webhook ingress, because GitHub requires a stable webhook URL and this route is provider-specific.
- `/api/v1/github/...` for GitHub repository setup and App-installation-specific operations.
- `/api/v1/runners/...` for runner bootstrap and runner-registration-token exchange.
- `/api/v1/jobs/...` for worker, integrator, review, and future hosted-plan job callbacks/results.
- `/api/v1/control-plane/...` or another clearly internal namespace for future service-to-service orchestration APIs.

The first implementation does not need to build cloud planning, cloud task issue creation, or hosted workers, but it must avoid route names and package boundaries that make those features look like GitHub-only special cases. Future examples that should fit naturally into the API shape:

- `POST /api/v1/plans` to request a hosted/cloud `herd plan`.
- `POST /api/v1/repositories/{repo_id}/issues` to create a task issue that will be picked up by cloud planning or normal Herd orchestration.
- `POST /api/v1/jobs/{job_id}/results` for both GitHub Actions workers and future cloud workers.
- `GET /api/v1/jobs/{job_id}` for job status polling or diagnostics.

These future routes are examples, not part of this implementation's acceptance criteria. The v1 requirement is that the current hosted GitHub App work uses a namespaced API design that can expand cleanly.

## GitHub App Permissions

Document the exact permissions required by the official App. Start from least privilege and only add permissions with a specific use case.

Expected v1 permissions:

| Permission | Access | Use |
|------------|--------|-----|
| Metadata | read | Required by GitHub Apps |
| Contents | read/write | Read repo metadata, create/update branches, commit or merge changes if service-owned Git operations are used |
| Issues | read/write | Create/update task issues, labels on issues, comments on issues |
| Pull requests | read/write | Create/update PRs, submit reviews, read PR state |
| Actions | read/write | Dispatch workflows and inspect workflow state where needed |
| Commit statuses | read/write | Set the required `Herd Review` status |
| Checks | read | Read CI check state for merge/review diagnostics; do not request write in v1 because `Herd Review` uses commit statuses |
| Administration | read/write | Create/remove repository self-hosted runner registration tokens and inspect runner state |

GitHub requires repository `Administration` write permission for an App installation token to create repository self-hosted runner registration tokens. Herd should use this permission only for runner registration/removal and runner diagnostics. Do not use it to mutate branch protection in this spec. Branch protection setup should be documented for users instead.

## GitHub Identity Rules

All production Herd GitHub-visible actions should be performed as the App identity:

- PR creation.
- PR updates.
- PR comments.
- PR reviews.
- issue comments.
- labels/milestones/issues.
- workflow dispatch.
- merge actions.
- `Herd Review` status updates.
- branch pushes where service-owned Git operations are implemented.

Human attribution remains important, but it must not make the human the GitHub actor.

Service-created commits must be authored/committed by the App identity and preserve the human's idea/architecture attribution using commit trailers, for example:

```text
Co-authored-by: Jane Doe <jane@example.com>
```

The exact trailer format for the human should be documented and tested. PR bodies should also identify the human requester/architect in a non-author field such as `Requested-by:` or `Human requester:`. The important requirement is that the commit author/committer, PR author, and GitHub API actor are the App, so the human can review/approve the PR.

## Runner Registration Without PATs

Current runner containers use a GitHub token to request short-lived self-hosted runner registration tokens. Removing PAT-based production auth means this path must move behind the control plane too.

Add a runner bootstrap flow:

1. `herd init` registers the repository with the control plane after verifying the GitHub App installation.
2. The control plane issues a repo-scoped runner bootstrap credential.
3. `herd init` writes that credential into the generated runner `.env` file. For the official hosted path, `HERD_CONTROL_PLANE_URL` may be omitted because Herd defaults to the cloud API. For self-hosted mode, `herd init` writes the override explicitly, for example:

   ```text
   HERD_CONTROL_PLANE_URL=https://herd.example.com
   HERD_RUNNER_BOOTSTRAP_TOKEN=...
   ```

4. On startup, the runner container calls the control plane with the repo identity and bootstrap token.
5. The control plane validates the bootstrap token, mints a GitHub self-hosted runner registration token using the App installation, and returns only that short-lived runner registration token.
6. The runner registers with GitHub Actions using the short-lived registration token.

Requirements:

- The bootstrap token is a Herd service credential, not a GitHub PAT.
- It must be scoped to one repository or installation/repository pair.
- It must be revocable from the control plane.
- It must not grant GitHub API access directly.
- It must be rotatable by re-running `herd init` or a documented command.
- The control plane must record runner bootstrap token creation, rotation, revocation, and last use in Postgres.
- The runner entrypoint must resolve the control-plane URL by using `HERD_CONTROL_PLANE_URL` when set and `https://api.herd-os.com` when unset. It must fail loudly if the resolved URL is invalid, if self-hosted mode was requested without a URL, or if the bootstrap token is missing.
- Existing docs that tell users to put a GitHub PAT in runner `.env` must be replaced.

This keeps the user-owned runner model while removing the last normal-user need for a GitHub PAT.

## Worker-to-Control-Plane Contract

Add a structured way for workflow jobs to report results back to the hosted service.

The service must validate:

- repository owner/name,
- GitHub App installation,
- workflow run ID / job identity,
- batch number,
- issue number where applicable,
- PR number where applicable,
- head branch/head SHA,
- expected job kind,
- request authenticity.

Use GitHub Actions OIDC for authenticating worker result submissions. Generated workflows must request `id-token: write` permission for jobs that call the control plane. The worker requests an OIDC token with a Herd-controlled audience and sends it to the Herd service; the service validates issuer, audience, repository, ref, workflow, run ID, and expiration before accepting the result.

If OIDC hits a concrete blocker, stop and document the blocker before replacing it. Do not silently introduce a broad long-lived shared secret.

Result payloads must be structured JSON with a version field. Examples:

```json
{
  "version": 1,
  "kind": "worker-completed",
  "repository": "owner/repo",
  "job_id": "job_123",
  "batch_number": 42,
  "issue_number": 123,
  "target_branch": "herd/batch/42-title",
  "base_sha": "abc123",
  "expected_head_sha": "abc123",
  "patch_artifact": "worker-123.patch",
  "status": "success"
}
```

```json
{
  "version": 1,
  "kind": "review-completed",
  "repository": "owner/repo",
  "batch_number": 42,
  "pr_number": 99,
  "head_sha": "abc123",
  "status": "approved"
}
```

Design exact payloads during implementation and test versioned parsing.

## Code Change Transport

Use patch/artifact upload as the v1 code-change transport.

Workers should not push branches to GitHub as the normal path. Workers should:

1. Check out the expected base SHA.
2. Run the agent and validation locally.
3. Produce a patch/bundle artifact for file changes.
4. Submit structured metadata to the control plane, including base SHA, patch artifact reference, validation result, and intended target branch.

The hosted service must:

1. Validate the worker callback.
2. Fetch the patch/bundle artifact.
3. Verify the patch was produced against the expected base SHA.
4. Apply the patch in a controlled service-side git workspace.
5. Commit as the App identity with human attribution trailers.
6. Push the expected branch.
7. Record the resulting commit SHA in Postgres.

The patch/bundle format must support file additions, deletions, renames, mode changes, and binary files where feasible. If plain text patches are insufficient, use `git bundle`, archive plus metadata, or another git-native artifact format.

If patch application fails because the target branch changed, the service must mark the job stale/conflicted and dispatch or surface the appropriate existing Herd conflict-resolution path.

Do not hand every worker a broad App installation token and consider the identity work complete.

## Workflow Dispatch

The hosted service must dispatch workflows using App installation tokens generated server-side.

Existing generated workflows should be updated so they no longer require `HERD_GITHUB_TOKEN` as the production dispatch mechanism. The service becomes the dispatch actor.

Current workflows to update for orchestration auth:

- `herd-worker.yml`
- `herd-integrator.yml`
- `herd-monitor.yml`

`herd-publish-runner.yml` is not a Herd orchestration workflow. It may continue to use normal GitHub workflow tokens for GHCR unless implementation discovers a direct dependency on Herd orchestration auth.

The service must dispatch workflow runs with enough inputs for the job to execute and report back:

- repo owner/name,
- service job ID,
- batch number,
- issue number,
- batch branch,
- PR number where applicable,
- service callback URL or identifier,
- any job-specific expected head SHA.

## Command Handling

Use `@herd-os` mentions as the supported v1 command syntax.

The hosted service must receive `issue_comment` webhooks and handle comments that mention the installed App account, for example:

```text
@herd-os plan
@herd-os fix
@herd-os review
@herd-os fix-ci
```

The exact public App login should be configurable in service/config tests so the implementation is not hardcoded to a name that might differ from the final GitHub App slug. The official hosted docs should use `@herd-os` unless the final App username differs.

The service must acknowledge quickly as `herd-os[bot]`, validate commenter permissions, and dispatch the correct workflow.

Remove `/herd` command support as part of this v1 breaking change. Do not keep `/herd` as a compatibility alias.

Authorization must validate the human commenter, not merely trust that the App received the webhook.

Allowed users remain:

- `OWNER`
- `MEMBER`
- `COLLABORATOR`

Bot/internal comments must not gain arbitrary command authority unless they match a narrowly defined internal path.

Mention command parsing requirements:

- Only comments that explicitly mention the App login should be considered commands.
- Edited comments must be idempotent and must not dispatch duplicate work for an already-handled command/head SHA.
- Bot-authored comments should not trigger public command handling.
- `/herd ...` comments should be ignored or receive a migration response telling the user to use `@herd-os ...`; they must not dispatch work.
- Command idempotency should key on the source comment ID, normalized command kind, repository, target issue/PR, and relevant head SHA.

## Review Blocking: `Herd Review` Commit Status

Add a required commit status named:

```text
Herd Review
```

Use commit statuses first, not check runs, unless implementation reveals a hard blocker. Statuses are simpler and branch protection can require them.

Status behavior:

| State | Meaning |
|-------|---------|
| `pending` | Herd review is queued/running, a review fix cycle is active, or the current head SHA has not been approved yet |
| `success` | The latest PR head SHA has a successful Herd review result |
| `failure` | Herd review found blocking findings, failed, timed out, produced unparseable output, hit max fix cycles, or requires a fix cycle |

Rules:

- Herd Review analysis runs on self-hosted GitHub Actions workers, not inside the hosted service.
- Status is scoped to PR head SHA.
- A new commit resets `Herd Review` to `pending` until reviewed.
- Stale review results must not set success on a newer head SHA.
- Fix cycles keep or set status to `pending` until the fix lands and review reruns.
- Review failure modes must set `failure` with a clear description and target URL when a useful target exists.
- If review is disabled by config, Herd should not set `Herd Review`. Docs must tell users to require `Herd Review` in branch protection only when Herd review is enabled for that repo.

Document branch protection setup:

- Users should require the `Herd Review` status before merging protected branches.
- Herd should not mutate branch protection in this spec.

## PR Review Submission

When Herd review is enabled:

- Approved review should submit an App-authored approving PR review.
- Blocking findings should submit an App-authored "changes requested" review and set `Herd Review` failure. If GitHub rejects the review submission for a specific platform reason, the service must still set `Herd Review` failure and post an explicit PR comment explaining the review-submission failure.
- Regardless of PR review semantics, the `Herd Review` commit status is the authoritative branch-protection signal.

Line-level review comments are intentionally deferred to `v1-release-02-line-level-review-findings.md`.

## Breaking Migration From PAT-Based Installs

Existing installs currently use:

- runner `.env` `GITHUB_TOKEN`,
- Actions secret `HERD_GITHUB_TOKEN`,
- workflow `GITHUB_TOKEN` fallback.

The new implementation should intentionally remove PAT-based production Herd orchestration instead of preserving it as a compatibility path.

This is an acceptable breaking change for the v1 release path. It simplifies the code, avoids identity ambiguity, and forces existing users to migrate away from human-user GitHub auth.

The new implementation should:

- remove `HERD_GITHUB_TOKEN` as the production workflow dispatch/auth mechanism,
- remove generated workflow fallback to `secrets.GITHUB_TOKEN` for Herd orchestration side effects,
- remove docs that tell users to create a PAT for Herd jobs,
- make hosted/self-hosted App control-plane auth the required path for production Herd jobs,
- fail loudly if the App is not installed or the service cannot authorize the repository,
- fail loudly if workflows have an invalid control-plane URL override or if self-hosted mode is selected without a control-plane URL,
- never silently fall back to human PAT auth.

`herd init` should detect and explain:

- App not installed on this repo,
- service unavailable,
- repository not authorized for this installation,
- branch protection missing required `Herd Review` status,
- old PAT-only setup still present and no longer supported.

Local developer commands may still use `gh auth token` or a developer PAT when they are explicitly local-only and not performing production Herd orchestration. Keep any such remaining local auth paths clearly separated from worker/integrator/monitor job auth.

## Service Deployment Configuration

Add service configuration through environment variables or a config file suitable for hosted deployment.

Expected service config:

```text
HERD_GITHUB_APP_ID
HERD_GITHUB_APP_PRIVATE_KEY
HERD_WEBHOOK_SECRET
HERD_PUBLIC_URL
HERD_DATABASE_URL
HERD_ENV
```

Use these explicit env names. Do not support a second service env naming scheme.

The first implementation must expose these HTTP surfaces:

| Endpoint | Purpose |
|----------|---------|
| `POST /webhooks/github` | Receive GitHub App webhooks |
| `POST /api/v1/github/repositories/register` | Register a repository during `herd init` after setup-only human authorization |
| `POST /api/v1/runners/registration-token` | Exchange a repo-scoped Herd runner bootstrap token for a short-lived GitHub runner registration token |
| `POST /api/v1/jobs/{job_id}/results` | Receive authenticated worker/integrator/review result callbacks |
| `GET /healthz` | Liveness check for containers and deployment platforms |
| `GET /readyz` | Readiness check that verifies required config and Postgres connectivity |

Use these route names for the first implementation. The API must remain versioned and namespaced, with separate webhook, GitHub repository registration, runner bootstrap, job result callback, liveness, and readiness surfaces.

The same service binary must support two deployment modes:

| Mode | App owner | Service owner | Postgres owner | Typical URL |
|------|-----------|---------------|----------------|-------------|
| Official hosted | HerdOS | HerdOS | HerdOS | `https://api.herd-os.com` or equivalent |
| Self-hosted | User/org | User/org | User/org | `https://herd.example.com` |

Consumer repositories may override the control-plane URL with:

```text
HERD_CONTROL_PLANE_URL
```

If `HERD_CONTROL_PLANE_URL` is missing, the CLI, generated workflows, runner entrypoint, integrator, and monitor must default to the official hosted HerdOS API:

```text
https://api.herd-os.com
```

Generated workflows should use the resolved control-plane URL when reporting worker results or when interacting with the control plane.

For the official hosted path, `herd init` should not require users to configure `HERD_CONTROL_PLANE_URL`. For self-hosted mode, `herd init` should accept a flag or config value that points to the user's control-plane URL and should persist that override into generated config/workflows where needed.

### Configuration Defaults

Prefer defaults for consumer-repo setup when a default is unambiguous and safe. Do not default secrets, private keys, database URLs, or self-hosted service ownership settings.

Consumer-side defaults:

| Config | Default when missing | Notes |
|--------|----------------------|-------|
| `HERD_CONTROL_PLANE_URL` | `https://api.herd-os.com` | Official hosted API. Self-hosters override this explicitly. |
| GitHub App slug/identity | official HerdOS App | `herd init` should assume the official App for hosted mode and only require custom App configuration for self-hosted mode. |
| Worker callback OIDC audience | `herd-control-plane` | Generated workflows and service validation must use the same default unless self-hosted config overrides it. |
| API version prefix | `/api/v1` | Clients should use v1 routes unless explicitly configured otherwise by a future migration. |

`herd init` rendering rule:

- Do not write active config entries for values that are merely using defaults.
- For generated human-edited config files, either omit defaulted values entirely or include them only as commented-out examples.
- For generated machine-owned workflow files, prefer omitting defaulted env vars and resolving defaults in code. Only render an env var when the user selected a non-default value or when the value is a generated credential that has no safe default.
- For self-hosted mode, render the explicit self-hosted overrides because they are not defaults.
- For official hosted mode, do not render active `HERD_CONTROL_PLANE_URL=https://api.herd-os.com`; the absence of `HERD_CONTROL_PLANE_URL` should be the normal hosted setup.

Consumer-side values that must not default:

| Config | Reason |
|--------|--------|
| `HERD_RUNNER_BOOTSTRAP_TOKEN` | Repo-scoped credential issued by the control plane; must be generated and rotated deliberately. |
| Agent credentials/auth volumes | Provider-specific secrets; must remain user-controlled. |
| Self-hosted control-plane URL | Required only when the user opts into self-hosted mode; missing value should fail loudly in that mode. |

Service-operator values that must remain explicit:

| Config | Reason |
|--------|--------|
| `HERD_GITHUB_APP_ID` | Identifies the App registration. |
| `HERD_GITHUB_APP_PRIVATE_KEY` | Secret key material. |
| `HERD_WEBHOOK_SECRET` | Secret used to verify GitHub webhooks. |
| `HERD_DATABASE_URL` | Deployment-specific Postgres location and credentials. |
| `HERD_PUBLIC_URL` | Public callback/webhook base URL; self-hosted operators must set this to the externally reachable service URL. |

`HERD_ENV` may default to `production` for the service binary when unset. Local Docker Compose should set `HERD_ENV=development` explicitly so development behavior is never inferred accidentally.

## Service Container Image and Publishing

The hosted control-plane service must be packaged as a Docker image and published to GitHub Packages / GHCR.

Expected image:

```text
ghcr.io/herd-os/herd-service:<version>
```

or another clearly named GHCR image if the repository naming convention suggests it.

Requirements:

- Add a Dockerfile for the Go service.
- Build a minimal production image.
- Run the service as a non-root user.
- Include only the compiled service binary and required runtime assets.
- Do not bake secrets into the image.
- Publish version tags when Herd releases.
- Publish `latest` only if that matches the repository's existing release policy.
- Document image configuration through environment variables.
- Ensure self-hosted users can run the published image directly.

Add or update release automation so GHCR publishing is part of the service release path. If the existing runner-base publishing workflow can be reused conceptually, keep the service image workflow separate enough that runner-base changes and service image changes remain understandable.

The root `docker-compose.yml` for local development should support both:

- building the service image from the local checkout, and
- optionally running the published GHCR image for self-hosted smoke testing.

## Postgres State Store

The hosted service must use Postgres for durable control-plane state.

Do not implement the production service as a stateless webhook handler. Stateless handling is not enough for review locks, callback validation, webhook idempotency, setup diagnostics, or reliable `Herd Review` status transitions.

GitHub remains the public source of truth for repository artifacts: issues, PRs, comments, reviews, branches, and statuses. Postgres is the source of truth for Herd's orchestration/control-plane state: what was received, dispatched, expected, accepted, rejected, retried, or blocked.

The first implementation should add migrations and a small storage layer for at least:

- App installations,
- repositories enabled for Herd,
- repository registration attempts and authorized setup user metadata,
- repository setup/config state,
- runner bootstrap credentials and usage records,
- webhook delivery idempotency records,
- dispatched job records,
- expected workflow run/job callbacks,
- review status state,
- review locks,
- command records and acknowledgements,
- idempotency keys,
- service-side GitHub mutation attempts or audit events.

Use SQL migrations checked into the repository. The service must fail startup in production mode when required migrations have not been applied, unless the implementation includes an explicit migration-on-start mode documented for operators.

Use database constraints where possible for correctness:

- unique GitHub webhook delivery IDs,
- one active review lock per PR/head where applicable,
- unique command handling by comment ID,
- expected worker callback identity by dispatched job,
- monotonic or guarded state transitions for review status.

In-memory fake storage is acceptable only for pure control-plane orchestration unit tests that do not validate SQL behavior. Storage tests that cover migrations, constraints, transactions, idempotency, locks, and state transitions must run against a real Postgres instance through Docker Compose, `testcontainers-go`, or an equivalent CI-friendly Postgres test harness. Do not use SQLite or an in-memory SQL substitute for Postgres correctness tests.

## Local Development With Docker Compose

The hosted service stack must be runnable locally with Docker Compose.

Add or update a root-level `docker-compose.yml` for the hosted HerdOS service development environment. This compose file is separate from generated consumer-repo `docker-compose.herd.yml` runner files.

The local compose stack should include:

- the HerdOS hosted App/control-plane service,
- Postgres,
- database migration execution or a documented migration command,
- any required lightweight supporting services.

The service container should define health checks against `/healthz` or `/readyz` where Docker Compose supports it.

The local setup should make the common development loop straightforward:

```bash
docker compose up --build
```

Expected local environment inputs:

```text
HERD_GITHUB_APP_ID
HERD_GITHUB_APP_PRIVATE_KEY
HERD_WEBHOOK_SECRET
HERD_PUBLIC_URL
HERD_DATABASE_URL
HERD_ENV=development
```

The compose setup should support loading these from a local env file, for example `.env`, while ensuring real secrets are gitignored.

Document the local webhook development flow:

- how to expose the local service to GitHub, for example with a tunnel,
- what webhook URL to configure on a development GitHub App,
- how to point a test repository's Herd config/workflows at the local service URL,
- how to run migrations,
- how to inspect logs,
- how to reset local Postgres state.

Do not confuse this compose file with the consumer runner compose file. `docker-compose.yml` is for HerdOS service development; `docker-compose.herd.yml` remains the generated file used by Herd users to run self-hosted workers.

## Webhook Handling

Implement webhook verification:

- Verify `X-Hub-Signature-256` with `HERD_WEBHOOK_SECRET`.
- Reject invalid signatures.
- Deduplicate webhook deliveries by `X-GitHub-Delivery`.
- Parse event type from `X-GitHub-Event`.
- Return 2xx quickly after durable acceptance. If the event cannot be verified or durably accepted, return an appropriate non-2xx response so GitHub can retry or surface delivery failure.

Initial events to support:

- `installation`
- `installation_repositories`
- `issue_comment`
- `pull_request`
- `pull_request_review`
- `workflow_run`

Only implement the event handlers needed for this spec. Stub unsupported events safely with logs and 2xx responses after verification.

## Webhook Outage Recovery and Reconciliation

The hosted service must not rely on GitHub webhook delivery as the only recovery mechanism.

GitHub retries failed webhook deliveries, and maintainers can manually redeliver failed App webhooks from GitHub's App settings, but Herd should treat webhooks as an event source rather than a durable queue it controls.

When the service is healthy, webhook handling should follow this shape:

1. Verify the webhook signature.
2. Deduplicate by GitHub delivery ID.
3. Persist the event or normalized work item to Postgres.
4. Return 2xx quickly after durable acceptance.
5. Process the event asynchronously.

If the service is down for a few minutes:

- GitHub may retry the delivery later.
- Some user commands may be delayed.
- Worker callbacks may fail and need to retry.
- Some events may still require service-side reconciliation if retries are exhausted or not delivered.

Add a hosted-service reconciler/monitor loop that can repair missed or partially processed events. It should use Postgres state plus GitHub state to recover safely.

Initial reconciliation responsibilities:

- Find dispatched workflow jobs that have not reported back before their timeout.
- Re-check open Herd batch PRs and active review state.
- Recompute or repair `Herd Review` status for open PRs when current state is missing/stale.
- Requeue review or command work when a durable record exists but processing did not complete.
- Detect stuck worker callbacks and surface actionable errors.
- Detect comments/commands that were acknowledged but never dispatched.
- Detect failed webhook deliveries when possible through GitHub APIs or operational logs.

Worker result callbacks should retry submission to the control plane with bounded exponential backoff. If all callback attempts fail, the worker should leave enough workflow logs/artifacts for the service or a human to diagnose and re-drive the job.

All reconciliation work must be idempotent. It should be safe to run after every deploy and on a periodic timer.

## Idempotency Requirements

Every hosted-service side effect must have an explicit idempotency key. This applies to normal webhook processing and to outage recovery/reconciliation.

Webhook delivery dedupe by `X-GitHub-Delivery` is necessary but not sufficient. A single GitHub event can cause multiple service side effects, and reconciliation may need to re-drive those side effects later. Each side effect must be safe to retry without creating duplicates or applying stale state.

Required idempotency keys:

| Operation | Idempotency key |
|-----------|-----------------|
| Webhook delivery acceptance | GitHub delivery ID |
| Command handling | `repo_id + source_comment_id + command_kind` |
| Acknowledgement comment | `repo_id + source_comment_id + ack_kind` |
| Workflow dispatch | `repo_id + job_kind + batch_number + issue_number/pr_number + head_sha` |
| Runner registration token request | `repo_id + runner_name + runner_labels + bootstrap_token_id + request_nonce` |
| Worker callback acceptance | `job_id + callback_kind + result_sha/attempt` |
| Review lock | `repo_id + pr_number + head_sha` with at most one active lock |
| `Herd Review` status update | `repo_id + pr_number + head_sha + status_context` |
| Batch PR creation | `repo_id + batch_number` |
| Fix issue creation | `repo_id + pr_number + head_sha + finding_fingerprint` |
| Branch operation | `repo_id + branch_name + expected_head_sha + operation_kind` |
| Merge attempt | `repo_id + pr_number + expected_head_sha` |

Use Postgres unique constraints where practical. Application-level checks alone are not enough for operations that can race between webhook processing, reconciliation, manual redelivery, and worker callbacks.

Before performing a GitHub mutation, the service should:

1. Acquire or create the idempotency record in Postgres.
2. Check whether the desired GitHub state already exists.
3. Check whether the requested operation is stale for the current repo/PR/head SHA.
4. Perform the mutation only if still needed.
5. Record the GitHub object ID, URL, status, or failure details.

During outage recovery, the reconciler must not blindly repeat side effects. It should use idempotency records plus current GitHub state to classify work as:

- already complete,
- still needed,
- stale and should be abandoned,
- failed and should be surfaced,
- safe to retry.

Examples:

- If `@herd-os review` is redelivered, only one review workflow should be dispatched for the same PR head SHA.
- If a review result callback is submitted twice, only the first accepted result should advance state.
- If a new commit lands while review was running, the old review result must not set `Herd Review` success on the new head SHA.
- If PR creation is retried after an outage, the service should find the existing batch PR instead of creating another one.
- If a fix issue was already created for a finding fingerprint, reconciliation should not create a duplicate issue.
- If merge is retried, it must verify the PR head SHA and required statuses before attempting merge again.

## Existing Code Paths To Audit

The implementation must audit and migrate GitHub-visible actions currently performed in CLI/workflow jobs.

Key areas:

- `internal/cli/workflows/*.tmpl`
- `internal/cli/init.go`
- `internal/cli/runner/*`
- `internal/cli/integrator.go`
- `internal/cli/monitor.go`
- `internal/cli/worker.go`
- `images/base/entrypoint.herd.sh`
- `internal/commands/*`
- `internal/integrator/*`
- `internal/monitor/*`
- `internal/worker/*`
- `internal/platform/github/*`
- docs in `docs/runners.md`, `docs/getting-started.md`, `docs/design/github-integration.md`

For each GitHub mutation, verify that it moves to the hosted service or is explicitly local-only developer behavior. Worker, integrator, monitor, and generated workflow code must not retain production GitHub mutations that depend on human PAT auth.

## Tests

Add focused unit tests for:

- GitHub webhook signature verification.
- Invalid webhook signature rejection.
- Duplicate delivery idempotency.
- Installation/repository authorization.
- `herd init` setup-only human authorization and credential discard behavior.
- `herd init` fails with actionable errors when `gh` is missing or unauthenticated.
- `@herd-os` mention command parsing and permission validation using commenter association.
- `/herd` slash-command comments are ignored or receive a migration response and do not dispatch work.
- Workflow dispatch request construction.
- Runner bootstrap token creation, rotation, revocation, and registration-token exchange.
- Worker result callback authentication and repository/run validation.
- Worker result payload parsing and version checks.
- Patch/bundle metadata validation.
- Patch/bundle stale-base rejection.
- Patch/bundle application success for additions, modifications, deletions, renames, mode changes, and binary-safe artifacts where supported.
- `Herd Review` status state transitions.
- New head SHA resets review status from success to pending.
- Stale review result cannot mark newer head SHA successful.
- Review failure sets failure with a useful description.
- Production Herd jobs fail when they encounter old PAT-only auth configuration.
- Generated workflows no longer require PAT-based dispatch.
- Generated workflows request `id-token: write` and only the minimum GitHub permissions required for local execution.
- CLI, runner, and generated workflow control-plane URL resolution defaults to `https://api.herd-os.com` when `HERD_CONTROL_PLANE_URL` is unset.
- Self-hosted control-plane URL overrides are persisted and used when configured.
- `herd init` does not write active config entries for defaulted values; defaults are omitted or shown only as commented examples in human-edited config.
- Service configuration validation for required environment variables.
- `/healthz` and `/readyz` behavior.
- Docker image build metadata or smoke test hooks where existing test patterns support it.

Add integration-style tests with fake GitHub clients for:

- App receives `@herd-os review`, acknowledges, dispatches review workflow, sets `Herd Review` pending.
- Review result approved for current SHA sets status success.
- Review result changes requested sets status failure or pending according to fix-cycle behavior.
- Herd Review dispatch runs analysis in a GitHub Actions worker and the hosted service only processes the callback/result.
- Worker completion result with a valid patch/bundle leads the service to commit as the App identity, push the expected branch, and record the resulting commit SHA.
- Worker completion result for a stale base SHA is rejected or marked conflicted without pushing.
- Duplicate webhook delivery, duplicate command handling, duplicate workflow dispatch, and duplicate worker callback do not create duplicate GitHub side effects.
- Old PAT-only workflow configuration is rejected with an actionable migration error.
- Local Docker Compose starts the service with Postgres and exposes health/readiness endpoints, using fake credentials or fakes where needed.

Do not require real GitHub network calls in normal CI tests.

## Documentation Updates

Update durable docs only after implementation exists.

Docs should explain:

- install the official HerdOS GitHub App,
- install GitHub CLI and run `gh auth login` before `herd init`,
- run `herd init`,
- run self-hosted workers,
- explain runner bootstrap credentials and why they are not GitHub PATs,
- configure agent credentials,
- enable required branch protection status `Herd Review`,
- what HerdOS hosts and what users still host,
- why production Herd jobs no longer use PATs,
- how to migrate from PAT-based installs,
- how to use `@herd-os` mention commands,
- that `/herd` slash-comment commands are no longer supported.

Do not document `/herd` slash-comment commands as supported after this implementation.

## Acceptance Criteria

- A repo can use the official hosted HerdOS GitHub App without storing an App private key or human PAT in repo secrets for production Herd job orchestration.
- `herd init` securely registers repositories through setup-only GitHub CLI human authorization without persisting human GitHub credentials.
- `gh` is documented as a setup-time dependency and is not required in worker/runtime containers.
- Herd-created PRs, comments, reviews, statuses, and workflow dispatches are performed by `herd-os[bot]` or the installed App identity.
- The human who requested the work can review/approve Herd-created PRs.
- `Herd Review` commit status blocks merging when review is pending, failed, stale, or in a fix cycle.
- The `Herd Review` status becomes success only for the latest approved PR head SHA.
- `@herd-os` mention commands are the supported GitHub command interface.
- `/herd` slash-comment commands no longer dispatch work.
- GitHub App webhook signatures are verified.
- Worker result submissions are authenticated and validated.
- Herd Review and every AI/model-backed operation run on self-hosted GitHub Actions workers, not in the hosted service.
- Worker code changes are transported through authenticated patch/bundle artifacts and applied by the service as the App identity.
- The service does not issue broad GitHub installation tokens to workers as the normal architecture.
- Production Herd jobs fail loudly instead of falling back to human PAT auth.
- The service is packaged as `cmd/herd-service`, can run locally with Docker Compose and Postgres, and can be published as a GHCR image.
- The service exposes liveness and readiness endpoints.
- The service API is versioned and namespaced so GitHub App operations, runner operations, job callbacks, and future hosted/cloud capabilities are not collapsed into one GitHub-only API surface.
- `HERD_CONTROL_PLANE_URL` is optional for official hosted installs and defaults to `https://api.herd-os.com`; self-hosted installs can override it explicitly.
- `herd init` omits active default-valued config entries, or writes them only as commented examples in human-edited files.
- Webhook processing, command handling, workflow dispatch, worker callbacks, review locks, branch pushes, and merge attempts are idempotent.
- Branch protection setup is documented but not auto-mutated.
- Tests cover auth, webhook, dispatch, callback, patch/bundle application, idempotency, and review-status state transitions.
- Public docs no longer claim `/herd` slash-comment command support.
- The final implementation deletes this spec file after durable docs are updated.

## Implementation Decisions For Herd Plan

Use these decisions when decomposing the work. Do not turn them back into open-ended research questions unless implementation uncovers a hard blocker.

### Service Binary

Build the hosted control plane as a separate Go binary:

```text
cmd/herd-service
```

Do not implement the hosted service as a `herd` CLI subcommand. The service has a different lifecycle, deployment target, Docker image, configuration surface, and dependency profile than the user-facing CLI.

Shared packages can live under `internal/service`, `internal/controlplane`, `internal/appauth`, or similarly clear names chosen during implementation.

### Storage

Add a new Postgres-backed storage layer and migrations for hosted service state.

Do not try to reuse ad hoc GitHub comments/branches as the only durable service state. GitHub remains the public artifact source of truth; Postgres owns orchestration state.

Tests can use an in-memory fake storage implementation only for pure control-plane orchestration tests that do not validate SQL behavior. Storage correctness tests must use real Postgres, matching the Postgres State Store section above. Production code must require Postgres.

### Code Change Transport

Use patch/artifact upload as the target v1 code-change transport.

Workers should not receive broad GitHub App installation tokens. Workers should produce a structured result plus a patch/bundle/artifact containing the code changes. The hosted service validates the worker callback, applies the patch/bundle, commits as the App identity, and pushes the expected branch.

If patch/bundle transport hits a concrete blocker, stop and report the blocker instead of silently implementing a branch-push fallback.

### GitHub Mutations That Must Move To The Service

Move these GitHub-visible actions to the hosted service in this spec:

- workflow dispatch for Herd jobs,
- command acknowledgement comments/reactions,
- issue comments created by Herd,
- PR comments created by Herd,
- PR creation and updates for batch PRs,
- PR review submissions,
- `Herd Review` commit status updates,
- issue/PR labels and milestones,
- fix issue creation,
- merge actions,
- branch pushes that apply worker patches/bundles.

After this work, worker/integrator/monitor jobs should not perform those mutations directly with human PAT auth.

### Command Interface

Use `@herd-os` mention commands as the v1 GitHub command interface.

Do not preserve `/herd` slash-comment commands as a production command path. Existing `/herd ...` comments should not dispatch work after this implementation. They may be ignored or receive a migration response telling the user to use `@herd-os ...`.

Command parsing, authorization, acknowledgement, dispatch, and idempotency must live in one service path so command handling does not split across old slash-command and new mention-command flows.

### Consumer Defaults

Default official hosted consumer configuration in code instead of eagerly writing active config values.

Do not render active default-valued settings from `herd init` unless the value is a generated credential or the user selected a non-default option. In particular, hosted-mode generated config should not write active `HERD_CONTROL_PLANE_URL=https://api.herd-os.com`; absence of `HERD_CONTROL_PLANE_URL` should mean "use the official hosted API."

Self-hosted overrides should be rendered explicitly because they are not defaults.

### GitHub Actions Workflows

Generated workflows should become execution jobs that:

- are dispatched by the hosted service through `workflow_dispatch`,
- no longer use `issue_comment`, `pull_request`, `pull_request_review`, `workflow_run`, or `check_run` triggers for Herd orchestration,
- check out the repository using the minimum GitHub-provided permissions needed for read access and local execution,
- run the appropriate Herd CLI role, agent work, and any AI/model-backed review analysis,
- produce structured result payloads,
- produce patch/bundle artifacts when code changes are made,
- authenticate callbacks to the control plane with GitHub Actions OIDC,
- submit results to the resolved control-plane URL, defaulting to `https://api.herd-os.com` unless `HERD_CONTROL_PLANE_URL` is set,
- retry callback submission with bounded exponential backoff,
- fail loudly if the resolved control-plane URL is invalid or self-hosted mode was selected without a URL.

Generated workflows should no longer require `HERD_GITHUB_TOKEN` or a human PAT for Herd orchestration.

Generated workflow job permissions should be minimal. Start with:

```yaml
permissions:
  contents: read
  id-token: write
```

Add any additional job permission only when a specific remaining workflow operation requires it, and document why that operation has not moved to the hosted service.

### Deployment Documentation

Add service-operator documentation separate from user-facing runner docs.

Service-operator docs should cover:

- running the service locally with `docker compose`,
- required environment variables,
- Postgres migrations,
- GitHub App creation for self-hosted mode,
- webhook secret setup,
- public URL/tunnel setup,
- GHCR image usage,
- logs and health checks,
- backup/restore expectations for Postgres,
- production deployment assumptions.

User-facing docs should remain simpler and focus on installing the official App, running `herd init`, starting workers, and setting required branch protection.
