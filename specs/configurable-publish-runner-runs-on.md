# Configurable `runs-on` for `herd-publish-runner.yml`

## Goal

Let `.herdos.yml` override the `runs-on:` value in the herd-managed
`herd-publish-runner.yml` workflow. Default stays `ubuntu-latest` so
existing consumers see zero behavior change. Users on private repos,
internal/air-gapped registries, or compliance regimes that forbid
GitHub-hosted runners can switch the publish workflow to a self-hosted
runner without forking the template.

## Non-goals (deliberate)

This spec is narrowly about the publish workflow only. Don't:

- Generalize to other herd-managed workflows (`herd-worker.yml`,
  `herd-integrator.yml`, `herd-monitor.yml`). They already key off
  `workers.runner_label` / consumer label conventions.
- Generate a separate `Dockerfile.herd_publisher` or a second
  docker-compose service for a Docker-capable publisher runner.
  Setting up such a host is the user's responsibility; this spec just
  unblocks routing the workflow there.
- Change `release.yml` in the `herd-os/herd` repo itself. That
  workflow is consumer-irrelevant.
- Cross-arch native builds. The workflow keeps `--platform
  linux/amd64,linux/arm64` and the runner still emulates the
  non-native arch via QEMU.

## YAML shape

New top-level section in `.herdos.yml`:

```yaml
image_publish:
  # Where the herd-publish-runner.yml workflow runs. Defaults to
  # ["ubuntu-latest"]. Override with a self-hosted label set when
  # building on your own runner — see docs/runners.md for prerequisites
  # (Docker + buildx + registry creds + QEMU for cross-arch).
  runs_on: ["ubuntu-latest"]
```

### Backward-compat shape (no `image_publish` block)

```yaml
# .herdos.yml with no image_publish: block
```

Resolves to `runs_on: ["ubuntu-latest"]` from `Default()`. Workflow renders identically to today. No drift on `herd init --check` after upgrade (see [Template rendering](#template-rendering) below).

### Self-hosted example

```yaml
image_publish:
  runs_on: ["self-hosted", "<your-publisher-label>"]
```

`runs_on` is a free-form list of GitHub Actions runner labels. The
specific label you put after `self-hosted` is **yours to pick** —
whatever string you registered your publisher host with (via the
runner's `--labels` flag at registration time). `herd-publisher` is a
reasonable convention used in this spec's examples below to avoid
clashing with the `herd-worker` label, but herd does not require it.
For a multi-label match, supply all labels: `["self-hosted", "linux",
"x64", "my-publisher"]` is fine.

Labels are free-form strings, but duplicate labels in
`image_publish.runs_on` are treated as a configuration error because
they do not add matching power and usually indicate a copy/paste
mistake.

Note: unlike `workers.runner_label`, there is no separate "label"
field here. The worker block uses one — `workers.runner_label` — as
the source of truth for both the workflow's `runs-on` AND the
runner's `--labels` at registration, because herd manages worker
registration via `entrypoint.herd.sh`. The publisher runner is
user-provided infrastructure (deliberately out of scope, see
[Non-goals](#non-goals-deliberate)), so there's nothing on the
registration side for a derived field to wire into. The list form of
`runs_on` is strictly more expressive — it covers single-label
hosted defaults, single-label self-hosted, and arbitrary multi-label
selectors — so we don't need both.

## Implementation outline

### 1. Config types — `internal/config/config.go`

Add a new top-level struct and field on `Config`:

```go
type Config struct {
    Version      int          `yaml:"version"`
    Platform     Platform     `yaml:"platform"`
    Agent        Agent        `yaml:"agent"`
    Workers      Workers      `yaml:"workers"`
    Integrator   Integrator   `yaml:"integrator"`
    Monitor      Monitor      `yaml:"monitor"`
    PullRequests PullRequests `yaml:"pull_requests"`
    ImagePublish ImagePublish `yaml:"image_publish"`   // <-- new
}

type ImagePublish struct {
    RunsOn []string `yaml:"runs_on"`
}
```

Section is structured (not a flat field) to leave room for future
publish-workflow knobs (e.g. additional platforms, image tag
overrides) without re-rooting the config.

### 2. Defaults — `internal/config/defaults.go`

```go
ImagePublish: ImagePublish{
    RunsOn: []string{"ubuntu-latest"},
},
```

### 3. Validation — `internal/config/validate.go`

```go
if len(cfg.ImagePublish.RunsOn) == 0 {
    ve.Errors = append(ve.Errors, "image_publish.runs_on must contain at least one runner label")
}
for i, label := range cfg.ImagePublish.RunsOn {
    if strings.TrimSpace(label) == "" {
        ve.Errors = append(ve.Errors,
            fmt.Sprintf("image_publish.runs_on[%d] must be a non-empty label", i))
    }
}
seen := map[string]int{}
for i, label := range cfg.ImagePublish.RunsOn {
    if first, ok := seen[label]; ok {
        ve.Errors = append(ve.Errors,
            fmt.Sprintf("image_publish.runs_on[%d] duplicates image_publish.runs_on[%d] (%q)", i, first, label))
    }
    seen[label] = i
}
```

No enum check on the values themselves — `runs-on` labels are
free-form in GitHub Actions and we can't enumerate them.

### 4. Template — `internal/cli/workflows/herd-publish-runner.yml.tmpl`

Today:

```yaml
jobs:
  publish:
    if: vars.HERD_ENABLED == 'true'
    runs-on: ubuntu-latest
```

New, with shape-aware rendering to avoid drift on the default case:

```yaml
jobs:
  publish:
    if: vars.HERD_ENABLED == 'true'
    runs-on: {{ if eq (len .ImagePublish.RunsOn) 1 }}{{ index .ImagePublish.RunsOn 0 }}{{ else }}[{{ range $i, $r := .ImagePublish.RunsOn }}{{ if $i }}, {{ end }}"{{ $r }}"{{ end }}]{{ end }}
```

- Single label (`["ubuntu-latest"]`) renders as `runs-on: ubuntu-latest` — **byte-identical to today's template, so no drift on first `herd init --check` after upgrade**.
- Multi-label (any list whose `len > 1` — for example `["self-hosted", "<any-label>"]` or `["self-hosted", "linux", "x64", "<any-label>"]`) renders as a YAML flow list with quoted entries, e.g. `runs-on: ["self-hosted", "herd-publisher"]`, preserving order.

Current `internal/cli/workflows.go` executes templated workflows
directly against `*config.Config`, so the template should read
`.ImagePublish.RunsOn` unless the implementation deliberately introduces
a broader workflow template data struct. Do not reference a nonexistent
`.ImagePublishRunsOn` field.

Because GitHub Actions runner labels are free-form strings, the
implementation must YAML-quote multi-label entries safely, including
escaping quotes and backslashes if they appear. Use a small template
helper instead of raw string interpolation if needed. Tests must cover
labels that require quoting, such as `linux x64` and `gpu:large`.

### 5. CLI surfacing — `internal/cli/config.go`

`flattenConfig` already uses `formatStringSlice` for `workers.extra_env`
and `monitor.notify_users`. Reuse the same pattern:

```go
kvs = append(kvs, keyValue{"image_publish.runs_on", formatStringSlice(cfg.ImagePublish.RunsOn)})
```

`herd config image_publish.runs_on` reads the value. `herd config
image_publish.runs_on <value>` to **write** is **not** supported —
slices fall through to "cannot set via CLI (use 'herd config edit')"
in the existing `setConfigValue` reflection switch, matching how
`workers.extra_env` and `monitor.notify_users` already behave. Document
this in the help text for the field; do not add slice-write support in
this PR.

### 6. Migration & drift

- Existing `.herdos.yml` files without an `image_publish` block: load
  applies the default `["ubuntu-latest"]`, template renders identically
  to today. `herd init --check` clean after upgrade.
- Existing `.github/workflows/herd-publish-runner.yml` files generated
  by the pre-spec template: same text (`runs-on: ubuntu-latest`).
  `herd init --check` clean.
- Users opting in to self-hosted: add `image_publish.runs_on:` to
  `.herdos.yml`, run `herd init`, merge the PR. Workflow file changes
  to list form.

## Documentation

### `docs/runners.md`

Add a new subsection under the existing **6. Runner images** section
titled **Publishing on a self-hosted runner** (or similar — pick a
heading consistent with neighboring h3/h4). Cover:

- The default (`ubuntu-latest`) and when to consider switching.
- Prerequisites a self-hosted publisher must satisfy:
  - Docker daemon + `docker buildx` accessible to the runner user.
  - QEMU emulation registered (`docker run --rm --privileged tonistiigi/binfmt --install all` once per host) if you keep the default
    `--platform linux/amd64,linux/arm64`. Alternatively, the user
    edits `Dockerfile.herd_runner`'s publish job locally to drop one
    arch — but doing so is out of this spec's scope; mention it as
    "if you're on a single-arch host, you can edit the workflow's
    `--platform` flag and accept the drift, or drop the platform you
    don't need."
  - GHCR write credentials (the workflow uses `secrets.GITHUB_TOKEN`,
    which is already provisioned by Actions on self-hosted runners).
  - A distinct runner label — herd does not prescribe one; pick
    anything that won't collide with `workers.runner_label` (default
    `herd-worker`). `herd-publisher` is a suggested convention used
    in the example below, but `publisher`, `image-builder`, or any
    other identifier you register the host with is equally valid.
    **Do not co-locate this on a `herd-worker`-labeled runner** —
    image builds consume capacity for several minutes per run and
    would block worker dispatch.
- Example `.herdos.yml` snippet (using `herd-publisher` as the
  suggested convention — substitute your own label below):

  ```yaml
  image_publish:
    runs_on: ["self-hosted", "herd-publisher"]
  ```
- One-paragraph reminder that the publish workflow only runs on
  `workflow_dispatch` or on pushes to `Dockerfile.herd_runner` on
  `main`, so build cadence is bursty rather than continuous. The
  publisher host can be sized for occasional image builds, but it must
  be available when Herd upgrade PRs merge and trigger the push path.

### `docs/configuration.md`

Add a row to the existing config table:

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `image_publish.runs_on` | string list | `["ubuntu-latest"]` | Runner labels for the `herd-publish-runner.yml` workflow. Override with `["self-hosted", "<label>"]` to build on your own host. See [Publishing on a self-hosted runner](runners.md#publishing-on-a-self-hosted-runner) for prerequisites. Not settable via `herd config <key> <value>` — edit `.herdos.yml` directly or use `herd config edit`. |

### `CHANGELOG.md`

Under `### Added`:

> New `image_publish.runs_on` config field controls where the
> `herd-publish-runner.yml` workflow runs (defaults to
> `["ubuntu-latest"]`). Override with `["self-hosted", "<label>"]` to
> build the customized runner image on your own host — useful for
> private-repo cost control, internal registries, or compliance
> environments that disallow GitHub-hosted runners. The host needs
> Docker + buildx + QEMU (for cross-arch) and a dedicated runner label
> separate from the `herd-worker` pool. See
> [docs/runners.md#publishing-on-a-self-hosted-runner](docs/runners.md).
> Defaults preserve byte-identical output, so existing repos see no
> drift on `herd init --check` after upgrading.

## Tests

### `internal/config/`

- `defaults_test.go` / `config_test.go`: assert `Default().ImagePublish.RunsOn == []string{"ubuntu-latest"}`.
- `validate_test.go`: new test function `TestValidate_ImagePublishRunsOn` with the matrix:
  - default value → valid
  - empty list → "must contain at least one runner label" error
  - list with one empty string → "must be a non-empty label" error
  - list with one whitespace-only string → same error
  - duplicate labels such as `["self-hosted", "self-hosted"]` → duplicate error
  - explicit `["ubuntu-latest"]` → valid
  - explicit `["self-hosted", "herd-publisher"]` → valid
  - explicit label that requires YAML quoting, e.g. `["self-hosted", "linux x64", "gpu:large"]` → valid
- `config_test.go`: new test `TestLoadImagePublishBlock` exercising
  the yaml roundtrip with an explicit block.

### `internal/cli/workflows/` (template tests)

- `publish_runner_workflow_test.go` (existing): extend
  `TestPublishRunnerWorkflow_Rendered` with subtests covering:
  - **single-label form**: config has default `["ubuntu-latest"]` → rendered file contains the literal `runs-on: ubuntu-latest` (scalar form, no brackets, no quotes).
  - **multi-label form**: config has `["self-hosted", "herd-publisher"]` → rendered file contains `runs-on: ["self-hosted", "herd-publisher"]`.
  - **quoted-label form**: config has `["self-hosted", "linux x64", "gpu:large"]` → rendered YAML is valid and preserves the exact labels when parsed.
  - **byte-identical default guard**: default config renders byte-for-byte equal to the currently committed `herd-publish-runner.yml` template output except for the template escaping already handled by `RenderWorkflow`; this is stronger than checking for substrings.
  - **regression guard**: assert the existing `workflow_dispatch:` trigger, `push:` trigger for `Dockerfile.herd_runner` on `main`, `packages: write` permission, and `if: vars.HERD_ENABLED == 'true'` gate are all still present (the existing assertions cover most of this — keep them).

### `internal/cli/` (CLI surfacing)

- `config_test.go`: extend `TestFlattenConfig` to assert
  `image_publish.runs_on` appears with the formatted slice value
  `[ubuntu-latest]` for the default.
- `TestGetConfigValueAgentRoleOverrides` (existing matrix) — sibling
  test that `herd config image_publish.runs_on` returns the formatted
  value.

## Verification (manual, post-merge)

1. Fresh `herd init` on a sandbox repo, no `image_publish:` block —
   rendered `.github/workflows/herd-publish-runner.yml` is byte-identical
   to the pre-spec version. `herd init --check` clean.
2. Add `image_publish.runs_on: ["self-hosted", "herd-publisher"]` to
   `.herdos.yml`, re-run `herd init` — workflow file shows
   `runs-on: ["self-hosted", "herd-publisher"]`. Drift detected on the
   first run, clean after the herd-init PR merges.
3. `herd config image_publish.runs_on` returns the formatted value.
4. `herd config image_publish.runs_on '["x"]'` returns the "cannot set
   via CLI" error pointing the user at `herd config edit`.
5. Optional end-to-end: actually dispatch the workflow against a real
   self-hosted runner with Docker + buildx and confirm it publishes
   the image. Out of CI's reach but worth a single maintainer pass.

## Out of scope (explicit)

- A `Dockerfile.herd_publisher` template that bakes Docker + buildx
  into a publisher-runner image. Users opting in build their own host.
- A second `docker-compose.herd.yml` service for the publisher.
  Mention as "you'll likely want to keep this separate from your
  worker compose file" but don't generate it.
- Native multi-arch builds (i.e. matrix across arm64 + amd64 native
  hosts). Single-host multi-arch via QEMU is preserved as the default
  shape.
- A `herd init` flag to set up a publisher runner. Manual setup is
  fine for a power-user knob.
- Configurability of other workflows' `runs-on`. They already accept
  the consumer-defined worker label via existing config; nothing new
  is required there.

## Spec cleanup

This file is planning input, not durable product documentation. The
final documentation task must delete
`specs/configurable-publish-runner-runs-on.md` after the implementation
is covered in `docs/` and `CHANGELOG.md`.
