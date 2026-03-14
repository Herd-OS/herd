# Testing Strategy

## Test Layers

```
┌─────────────────────────────────────────────────┐
│  E2E Tests                                       │
│  Real GitHub repo, real Actions, real Claude      │
│  Slow, expensive, run manually or on release      │
├─────────────────────────────────────────────────┤
│  Integration Tests                               │
│  Real GitHub API (test repo), mocked Claude       │
│  Medium speed, run in CI on PRs                   │
├─────────────────────────────────────────────────┤
│  Unit Tests                                      │
│  No external calls, all mocked                    │
│  Fast, run on every commit                        │
└─────────────────────────────────────────────────┘
```

## Unit Tests

Test internal logic without any external calls. Everything is mocked.

### What to Test

**Config parsing and validation:**
- Valid `.herdos.yml` parses correctly
- Invalid config produces clear errors
- Environment variable overrides work
- Default values applied when fields are missing
- Config migration: version 1 → 2 applies field renames and new defaults
- Config migration: outdated version prompts user, refuses to run without migrating

**Issue template generation:**
- YAML front matter is well-formed
- Dependencies are serialized correctly
- Acceptance criteria are formatted
- Body template matches expected format

**Issue body parsing:**
- YAML front matter extracted correctly
- Dependencies parsed
- Scope parsed
- Body without front matter handled gracefully

**Label helpers:**
- Status transition validation (ready → in-progress: OK, done → ready: error)
- Label name constants match expected format

**Plan parsing:**
- Planner output parsed into structured tasks
- Dependencies extracted
- Malformed output handled with clear error

**DAG and tier assignment:**
- Tasks with no dependencies → all Tier 0
- Linear chain A→B→C → Tiers 0, 1, 2
- Diamond graph (A→B, A→C, B→D, C→D) → correct tier assignment
- Circular dependency detected → error with cycle description
- Single-issue batch (one issue in milestone) → Tier 0

**Internal command guardrail:**
- `worker exec`, `integrator *`, `monitor patrol` refuse to run without `HERD_RUNNER=true`
- Commands exit with clear error message when guardrail fails

**Worker logic:**
- No-op detection: worker recognizes acceptance criteria already met, labels done without creating branch
- Worker labels issue `herd/status:failed` and triggers Monitor on failure

**Display formatting:**
- Table rendering with various widths
- Status icons and colors
- Progress calculations

### How to Test

```go
func TestParseIssueBody(t *testing.T) {
    body := `---
herd:
  version: 1
  depends_on: [42, 43]
  scope: ["src/auth.ts"]
---

## Task
Add authentication middleware.
`
    issue, err := issues.ParseBody(body)
    require.NoError(t, err)
    assert.Equal(t, []int{42, 43}, issue.DependsOn)
    assert.Equal(t, []string{"src/auth.ts"}, issue.Scope)
}
```

Platform interfaces are mocked using standard Go patterns:

```go
type MockIssueService struct {
    CreateFunc func(ctx context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error)
    Created    []*platform.Issue // records all created issues
}

func (m *MockIssueService) Create(ctx context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
    if m.CreateFunc != nil {
        return m.CreateFunc(ctx, title, body, labels, milestone)
    }
    issue := &platform.Issue{Number: len(m.Created) + 1, Title: title, Body: body, Labels: labels}
    m.Created = append(m.Created, issue)
    return issue, nil
}
```

## Integration Tests

Test against the real GitHub API using a dedicated test repository.

### Setup

- A test repo (e.g., `herd-os/herd-test`) with HerdOS labels and workflows pre-configured
- A GitHub token with write access, stored as a CI secret
- Tests create real issues, trigger real workflows, and clean up after

### What to Test

**Issue lifecycle:**
- Create issue with labels → verify via API
- Update labels (status transitions) → verify
- Add to milestone → verify

**Dispatch:**
- Trigger `workflow_dispatch` → verify run appears
- Verify worker picks up the issue (may need to wait)

**Integrator:**
- `herd integrator consolidate` → verify worker branch merged into batch branch
- `herd integrator advance` → verify tier completion detection, next tier dispatch
- `herd integrator advance` with failed issue → verify tier stays stuck, no dispatch
- `herd integrator review` → verify agent review posted on batch PR
- Fix-worker cycle: agent review finds issues → verify fix workers dispatched → re-review

**Batch cancellation:**
- `herd batch cancel` → verify workflow runs cancelled, issues labeled failed, milestone closed, branch deleted

**Conflict handling:**
- Two workers modify same file → verify Integrator detects conflict and applies configured strategy

**Label management:**
- `herd init` creates all expected labels
- Re-running `herd init` is idempotent (no duplicate labels)

**Milestone management:**
- Create milestone → verify
- Add issues to milestone → verify progress

### How to Run

```bash
# Requires GITHUB_TOKEN with repo access
HERD_TEST_REPO=herd-os/herd-test go test ./internal/platform/github/... -tags=integration
```

Integration tests are guarded by a build tag so they don't run by default:

```go
//go:build integration

func TestCreateIssue_Real(t *testing.T) {
    // Uses real GitHub API
}
```

### Cleanup

Integration tests should clean up after themselves:
- Close and delete test issues
- Delete test milestones
- Cancel triggered workflow runs

Use `t.Cleanup()` to ensure cleanup runs even on failure.

## E2E Tests

Test the full workflow: plan → dispatch → worker → consolidate → batch PR → merge.

### What to Test

**Happy path:**
1. Create test issues and milestone via GitHub API (simulates plan output)
2. Run `herd dispatch --batch <milestone>` in the test repo
3. Verify batch branch created and Tier 0 dispatched
4. Wait for worker Action to complete
5. Verify worker branch merged into batch branch
6. Verify issue labeled `herd/status:done`
7. Verify batch PR opened against main
8. Verify agent review posted on batch PR
9. Merge batch PR
10. Verify issues closed

**No-op worker:**
1. Create an issue whose acceptance criteria are already satisfied by the codebase
2. Dispatch worker
3. Verify worker labels issue `herd/status:done` without creating a worker branch
4. Verify Integrator handles missing branch gracefully (tier advances normally)

**Failure recovery:**
1. Create an issue with an impossible task
2. Dispatch worker
3. Verify worker fails gracefully (issue labeled `herd/status:failed`)
4. Verify Monitor comments on issue with diagnostics

### How to Run

E2E tests are expensive (they consume Actions minutes and Claude API credits). Run them:
- Manually before releases
- In CI on the `main` branch (not on every PR)
- With a timeout to prevent runaway costs

```bash
HERD_TEST_REPO=herd-os/herd-test \
CLAUDE_CODE_OAUTH_TOKEN=... \
go test ./tests/e2e/... -tags=e2e -timeout=30m
```

## CI Configuration

```yaml
# .github/workflows/ci.yml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build
        run: go build ./...
      - name: Test
        run: go test ./... -count=1 -race
      - name: Vet
        run: go vet ./...

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: golangci/golangci-lint-action@v6
        with:
          version: latest

  integration:
    runs-on: ubuntu-latest
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Run integration tests
        run: go test ./internal/platform/github/... -tags=integration -count=1 -v
        env:
          GITHUB_TOKEN: ${{ secrets.HERD_GITHUB_TOKEN }}
          HERD_TEST_OWNER: ${{ vars.HERD_TEST_OWNER }}
          HERD_TEST_REPO: ${{ vars.HERD_TEST_REPO }}
```

## Test Repo Setup

The test repo should be:
- Private (to avoid abuse)
- Pre-configured with HerdOS labels and workflows
- Have a self-hosted runner available (for E2E tests)
- Have branch protection disabled (to allow test PR merges)
- Be cleaned periodically (close stale test issues)
