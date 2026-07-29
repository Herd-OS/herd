# Service Operations

`herd-service` is the HerdOS control-plane API. The official hosted service runs
at `https://api.herd-os.com`; self-hosted operators can run the same service
with their own GitHub App and Postgres database.

## Image

Release publishing produces the service image at:

```text
ghcr.io/herd-os/herd-service
```

Use a version tag for production when available. `latest` is acceptable for
local smoke tests, but production deployments should pin a release tag.

## Required Environment

| Variable | Required | Notes |
|----------|----------|-------|
| `HERD_GITHUB_APP_ID` | Production | Numeric GitHub App ID. |
| `HERD_GITHUB_APP_PRIVATE_KEY` | Production | App private key PEM. Escaped PEM in an env var is supported by the deployment environment. |
| `HERD_WEBHOOK_SECRET` | Production | GitHub App webhook secret. |
| `HERD_PUBLIC_URL` | Production | Public base URL, e.g. `https://api.herd-os.com` or `https://herd.example.com`. |
| `HERD_DATABASE_URL` | Production | Postgres DSN. |
| `HERD_ENV` | No | Defaults to `production`; set `development` for local Compose. |
| `HERD_GITHUB_APP_LOGIN` | No | Defaults to `herd-os`; override for self-hosted Apps. |
| `HERD_RECONCILER_ENABLED` | No | Boolean for background reconciliation. |
| `HERD_RECONCILER_INTERVAL` | No | Go duration when reconciliation is enabled. |

Production values are supplied as environment variables to the service
container. Do not bake secrets into the image.

## Local Docker Compose Development

The repository root `docker-compose.yml` starts the service with local Postgres:

```bash
touch .env
docker compose up --build
```

The compose file sets:

```text
HERD_ENV=development
HERD_DATABASE_URL=postgres://herd:herd@postgres:5432/herd?sslmode=disable
```

Add local GitHub App values to `.env` when testing real webhooks:

```bash
HERD_GITHUB_APP_ID=...
HERD_GITHUB_APP_PRIVATE_KEY=...
HERD_WEBHOOK_SECRET=...
HERD_PUBLIC_URL=https://<tunnel-host>
```

Development mode relaxes production-required env validation, but real webhook
and App flows still need valid App credentials.

## Webhook Tunnel

For local webhook testing, expose port 8080 with a tunnel and set the GitHub App
webhook URL to:

```text
https://<tunnel-host>/webhooks/github
```

Then set:

```bash
HERD_PUBLIC_URL=https://<tunnel-host>
HERD_WEBHOOK_SECRET=<same value configured on the App>
```

Restart `docker compose up` after changing `.env`.

## Migrations

The control-plane Postgres store uses embedded migrations under
`internal/controlplane/store/migrations/`. The store validates that embedded
migrations have been applied unless it is explicitly constructed with the
development/test migration option.

When operating production, apply migrations deliberately before starting a new
service version, then start the container against the migrated
`HERD_DATABASE_URL`. Local development and tests can use the migration helper
path that applies embedded migrations to the Compose Postgres database.

## Logs and Health

The service logs to stdout/stderr with the `herd-service:` prefix. Docker Compose
logs are available with:

```bash
docker compose logs -f service
```

Health endpoints:

```text
GET /healthz
GET /readyz
```

Use `/healthz` for process liveness and `/readyz` for database-backed readiness.

## Reset Local Postgres

To remove local service state and start over:

```bash
docker compose down -v
docker compose up --build
```

`down -v` deletes the `herd-postgres-data` volume declared by the root compose
file. Do not use it against production data.

## Self-Hosted Repository Setup

Self-hosted users create their own GitHub App with the same repository
permissions as the hosted App, configure its webhook to
`https://herd.example.com/webhooks/github`, run `herd-service`, then initialize
consumer repositories with:

```bash
herd init --control-plane-url https://herd.example.com --app-login <your-app-login>
```

Generated workers receive `HERD_CONTROL_PLANE_URL=https://herd.example.com` and
a repo-scoped `HERD_RUNNER_BOOTSTRAP_TOKEN`. Agent credentials still live only
in your runner environment.
