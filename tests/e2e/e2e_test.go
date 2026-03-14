//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	ghplatform "github.com/herd-os/herd/internal/platform/github"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIfNoCredentials skips the test if required env vars are missing.
func skipIfNoCredentials(t *testing.T) {
	t.Helper()
	if os.Getenv("HERD_TEST_OWNER") == "" || os.Getenv("HERD_TEST_REPO") == "" {
		t.Skip("HERD_TEST_OWNER and HERD_TEST_REPO must be set")
	}
	if os.Getenv("GITHUB_TOKEN") == "" && os.Getenv("GH_TOKEN") == "" {
		t.Skip("GITHUB_TOKEN or GH_TOKEN must be set")
	}
}

// newTestPlatform creates a real GitHub client for the test repo.
func newTestPlatform(t *testing.T) platform.Platform {
	t.Helper()
	owner := os.Getenv("HERD_TEST_OWNER")
	repo := os.Getenv("HERD_TEST_REPO")
	p, err := ghplatform.New(owner, repo)
	require.NoError(t, err, "failed to create GitHub client")
	return p
}

// herdBin builds and returns the path to the herd binary.
func herdBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herd")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/herd")
	cmd.Dir = findModuleRoot(t)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building herd: %s", string(out))
	return bin
}

// findModuleRoot walks up from the test file to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// setupHerdWorkdir creates a temp directory with .herdos.yml pointing at the test repo.
func setupHerdWorkdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	owner := os.Getenv("HERD_TEST_OWNER")
	repo := os.Getenv("HERD_TEST_REPO")

	config := fmt.Sprintf(`platform:
  owner: %s
  repo: %s

workers:
  max_concurrent: 3
  runner_label: herd-worker
  timeout_minutes: 30
`, owner, repo)

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herdos.yml"), []byte(config), 0644))
	return dir
}

// runHerd runs the herd binary with the given args in the given workdir.
func runHerd(t *testing.T, bin, workdir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "herd %v failed: %s", args, string(out))
	return string(out)
}

// waitForIssueLabel polls until the issue has the expected label or timeout is reached.
func waitForIssueLabel(t *testing.T, p platform.Platform, issueNumber int, expectedLabel string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		issue, err := p.Issues().Get(ctx, issueNumber)
		require.NoError(t, err)

		for _, label := range issue.Labels {
			if label == expectedLabel {
				return
			}
		}
		time.Sleep(10 * time.Second)
	}
	t.Fatalf("issue #%d did not get label %q within %v", issueNumber, expectedLabel, timeout)
}

// waitForWorkflowCompletion polls until the workflow run completes or timeout is reached.
func waitForWorkflowCompletion(t *testing.T, p platform.Platform, runID int64, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		run, err := p.Workflows().GetRun(ctx, runID)
		require.NoError(t, err)

		if run.Status == "completed" {
			return
		}
		time.Sleep(10 * time.Second)
	}
	t.Fatalf("workflow run %d did not complete within %v", runID, timeout)
}

// findRecentRun polls ListRuns to find a run created after the given time.
func findRecentRun(t *testing.T, p platform.Platform, workflowFile string, after time.Time, timeout time.Duration) *platform.Run {
	t.Helper()
	ctx := context.Background()

	workflowID, err := p.Workflows().GetWorkflow(ctx, workflowFile)
	require.NoError(t, err)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		runs, err := p.Workflows().ListRuns(ctx, platform.RunFilters{WorkflowID: workflowID})
		if err != nil {
			continue
		}
		for _, run := range runs {
			if run.CreatedAt.After(after) {
				return run
			}
		}
	}
	t.Fatalf("could not find workflow run for %s within %v", workflowFile, timeout)
	return nil
}

func TestE2E_FullWorkflow(t *testing.T) {
	skipIfNoCredentials(t)
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY must be set for full workflow test")
	}

	ctx := context.Background()
	p := newTestPlatform(t)
	bin := herdBin(t)
	workdir := setupHerdWorkdir(t)
	testName := t.Name()

	// Create milestone (simulating planner output)
	ms, err := p.Milestones().Create(ctx, fmt.Sprintf("e2e-%s-%d", testName, time.Now().Unix()), "E2E test milestone", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Close all issues in the milestone (including any created by the integrator)
		cleanupMilestoneIssues(t, p, ms.Number)
		_, _ = p.Milestones().Update(ctx, ms.Number, platform.MilestoneUpdate{State: strPtr("closed")})
	})

	// Create 2 independent issues (simulating planner output)
	issueBody := fmt.Sprintf(`---
herd:
  version: 1
  batch: %d
  scope:
    - e2e-test.txt
  estimated_complexity: low
---

## Task

Create a file called e2e-test-%s.txt with the content "hello from e2e".

## Acceptance Criteria

- [ ] File e2e-test-%s.txt exists with content "hello from e2e"

## Files to Modify

- e2e-test-%s.txt
`, ms.Number, testName, testName, testName)

	issue1, err := p.Issues().Create(ctx,
		fmt.Sprintf("[e2e] Create test file 1 (%s)", testName),
		issueBody,
		[]string{issues.StatusReady, issues.TypeFeature},
		&ms.Number,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = p.Issues().Update(ctx, issue1.Number, platform.IssueUpdate{State: strPtr("closed")})
	})

	issue2, err := p.Issues().Create(ctx,
		fmt.Sprintf("[e2e] Create test file 2 (%s)", testName),
		issueBody,
		[]string{issues.StatusReady, issues.TypeFeature},
		&ms.Number,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = p.Issues().Update(ctx, issue2.Number, platform.IssueUpdate{State: strPtr("closed")})
	})

	// Wait for GitHub API consistency — issues must be visible in milestone-filtered list
	waitForMilestoneIssueCount(t, p, ms.Number, 2, 30*time.Second)

	// Dispatch via real herd CLI (creates batch branch, labels issues, triggers workflows)
	before := time.Now().Add(-5 * time.Second)
	out := runHerd(t, bin, workdir, "dispatch", "--batch", fmt.Sprintf("%d", ms.Number), "--ignore-limit")
	t.Logf("herd dispatch output: %s", out)

	// Find the dispatched runs
	run1 := findRecentRun(t, p, "herd-worker.yml", before, 2*time.Minute)
	t.Cleanup(func() { _ = p.Workflows().CancelRun(ctx, run1.ID) })

	// Wait for workers to complete (generous timeout for real Actions)
	waitForWorkflowCompletion(t, p, run1.ID, 10*time.Minute)

	// Verify issues labeled done
	waitForIssueLabel(t, p, issue1.Number, issues.StatusDone, 2*time.Minute)
	waitForIssueLabel(t, p, issue2.Number, issues.StatusDone, 2*time.Minute)

	// Verify batch PR opened (integrator runs after worker completes, so poll for it)
	batchBranch := fmt.Sprintf("herd/batch/%d-", ms.Number)
	batchPR := waitForBatchPR(t, p, batchBranch, 3*time.Minute)
	t.Cleanup(func() { closePR(t, batchPR.Number) })
}

func TestE2E_WorkerFailure(t *testing.T) {
	t.Skip("Skipped: AI agents are too creative to reliably fail on impossible tasks — needs a different failure mechanism")
	skipIfNoCredentials(t)
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY must be set")
	}

	ctx := context.Background()
	p := newTestPlatform(t)
	bin := herdBin(t)
	workdir := setupHerdWorkdir(t)
	testName := t.Name()

	// Create milestone
	ms, err := p.Milestones().Create(ctx, fmt.Sprintf("e2e-%s-%d", testName, time.Now().Unix()), "E2E failure test", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = p.Milestones().Update(ctx, ms.Number, platform.MilestoneUpdate{State: strPtr("closed")})
	})

	// Create an issue with an impossible task
	issueBody := fmt.Sprintf(`---
herd:
  version: 1
  batch: %d
  scope:
    - nonexistent/impossible/path.go
  estimated_complexity: low
---

## Task

Refactor the function FooBarBazQux42 in nonexistent/impossible/path.go to use generics.
This function does not exist and neither does the file. The worker should fail.

## Acceptance Criteria

- [ ] FooBarBazQux42 uses generics

## Files to Modify

- nonexistent/impossible/path.go
`, ms.Number)

	issue, err := p.Issues().Create(ctx,
		fmt.Sprintf("[e2e] Impossible task (%s)", testName),
		issueBody,
		[]string{issues.StatusReady, issues.TypeFeature},
		&ms.Number,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = p.Issues().Update(ctx, issue.Number, platform.IssueUpdate{State: strPtr("closed")})
	})

	// Dispatch via real herd CLI
	before := time.Now().Add(-5 * time.Second)
	out := runHerd(t, bin, workdir, "dispatch", fmt.Sprintf("%d", issue.Number))
	t.Logf("herd dispatch output: %s", out)

	// Find the dispatched run
	run := findRecentRun(t, p, "herd-worker.yml", before, 2*time.Minute)
	t.Cleanup(func() { _ = p.Workflows().CancelRun(ctx, run.ID) })

	// Wait for worker to complete (it should fail)
	waitForWorkflowCompletion(t, p, run.ID, 10*time.Minute)

	// Verify issue labeled failed
	waitForIssueLabel(t, p, issue.Number, issues.StatusFailed, 2*time.Minute)
}

func TestE2E_BatchCancel(t *testing.T) {
	skipIfNoCredentials(t)

	ctx := context.Background()
	p := newTestPlatform(t)
	bin := herdBin(t)
	workdir := setupHerdWorkdir(t)
	testName := t.Name()

	// Create milestone
	ms, err := p.Milestones().Create(ctx, fmt.Sprintf("e2e-%s-%d", testName, time.Now().Unix()), "E2E cancel test", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = p.Milestones().Update(ctx, ms.Number, platform.MilestoneUpdate{State: strPtr("closed")})
	})

	// Create issue
	issueBody := fmt.Sprintf(`---
herd:
  version: 1
  batch: %d
  scope:
    - cancel-test.txt
  estimated_complexity: low
---

## Task

Create cancel-test.txt. This task will be cancelled before completion.

## Acceptance Criteria

- [ ] cancel-test.txt exists

## Files to Modify

- cancel-test.txt
`, ms.Number)

	issue, err := p.Issues().Create(ctx,
		fmt.Sprintf("[e2e] Cancel test (%s)", testName),
		issueBody,
		[]string{issues.StatusReady, issues.TypeFeature},
		&ms.Number,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = p.Issues().Update(ctx, issue.Number, platform.IssueUpdate{State: strPtr("closed")})
	})

	// Dispatch via real herd CLI
	before := time.Now().Add(-5 * time.Second)
	out := runHerd(t, bin, workdir, "dispatch", fmt.Sprintf("%d", issue.Number))
	t.Logf("herd dispatch output: %s", out)

	// Find the dispatched run
	run := findRecentRun(t, p, "herd-worker.yml", before, 2*time.Minute)

	// Cancel immediately — may fail if already completed
	err = p.Workflows().CancelRun(ctx, run.ID)
	if err != nil {
		t.Logf("cancel returned error (run may have already completed): %v", err)
	}

	// Wait for run to finish
	waitForWorkflowCompletion(t, p, run.ID, 2*time.Minute)

	// Verify the run ended
	finalRun, err := p.Workflows().GetRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, []string{"cancelled", "success", "failure"}, finalRun.Conclusion,
		"expected run to be cancelled or completed, got %q", finalRun.Conclusion)
}

// waitForMilestoneIssueCount polls until the milestone has at least the expected number of open issues.
func waitForMilestoneIssueCount(t *testing.T, p platform.Platform, milestoneNumber, expectedCount int, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		issues, err := p.Issues().List(ctx, platform.IssueFilters{
			State:     "open",
			Milestone: &milestoneNumber,
		})
		if err == nil && len(issues) >= expectedCount {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("milestone #%d did not have %d issues within %v", milestoneNumber, expectedCount, timeout)
}

// waitForBatchPR polls until a PR whose head branch starts with branchPrefix appears.
func waitForBatchPR(t *testing.T, p platform.Platform, branchPrefix string, timeout time.Duration) *platform.PullRequest {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open"})
		if err == nil {
			for _, pr := range prs {
				if len(pr.Head) >= len(branchPrefix) && pr.Head[:len(branchPrefix)] == branchPrefix {
					return pr
				}
			}
		}
		time.Sleep(10 * time.Second)
	}
	t.Fatalf("no batch PR with branch prefix %q appeared within %v", branchPrefix, timeout)
	return nil
}

// cleanupMilestoneIssues closes all open issues in a milestone.
func cleanupMilestoneIssues(t *testing.T, p platform.Platform, milestoneNumber int) {
	t.Helper()
	ctx := context.Background()
	issues, err := p.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &milestoneNumber,
	})
	if err != nil {
		return
	}
	for _, iss := range issues {
		_, _ = p.Issues().Update(ctx, iss.Number, platform.IssueUpdate{State: strPtr("closed")})
	}
}

// closePR closes a PR via gh CLI (the platform interface lacks a state update method).
func closePR(t *testing.T, number int) {
	t.Helper()
	owner := os.Getenv("HERD_TEST_OWNER")
	repo := os.Getenv("HERD_TEST_REPO")
	cmd := exec.Command("gh", "pr", "close", fmt.Sprintf("%d", number),
		"--repo", fmt.Sprintf("%s/%s", owner, repo), "--delete-branch")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("closing PR #%d: %v: %s", number, err, string(out))
	}
}

func strPtr(s string) *string {
	return &s
}
