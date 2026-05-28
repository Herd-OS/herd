package integrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findingsTestCfg returns a config with enough capacity for any dispatch in
// these tests.
func findingsTestCfg() *config.Config {
	return &config.Config{Workers: config.Workers{MaxConcurrent: 10, TimeoutMinutes: 30, RunnerLabel: "herd-worker"}}
}

// newFindingsMocks builds the mock platform used by the dispatch-findings
// tests: empty active-run lists so capacity is available, and a default
// branch resolver that returns "main".
func newFindingsMocks(issueSvc platform.IssueService) (*mockPlatform, *mockWorkflowService) {
	wf := &mockWorkflowService{
		listResult: []*platform.Run{},
	}
	mock := &mockPlatform{
		issues:    issueSvc,
		prs:       &mockPRService{},
		workflows: wf,
		repo:      &mockRepoService{defaultBranch: "main"},
	}
	return mock, wf
}

func TestDispatchReadyIssues_InjectsManualFindings(t *testing.T) {
	const depNum = 100
	const dependentNum = 300

	depBody := "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nThe manual investigation.\n"
	dependentBody := fmt.Sprintf(
		"---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [%d]\n---\n\n## Task\nDo dependent\n",
		depNum,
	)

	issueSvc := newMockIssueService()
	issueSvc.getResult[depNum] = &platform.Issue{
		Number: depNum,
		Title:  "Manual investigation",
		State:  "closed",
		Labels: []string{issues.TypeManual},
		Body:   depBody,
	}
	issueSvc.getResult[dependentNum] = &platform.Issue{
		Number: dependentNum,
		Title:  "Dependent",
		Labels: []string{issues.StatusReady},
		Body:   dependentBody,
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{ID: 1, AuthorLogin: "human", Body: "Key finding: the bug is in foo.go."},
	}

	mock, wf := newFindingsMocks(issueSvc)

	allIssues := []*platform.Issue{
		issueSvc.getResult[depNum],
		issueSvc.getResult[dependentNum],
	}

	dispatched, err := dispatchReadyIssues(
		context.Background(), mock, findingsTestCfg(),
		[]int{dependentNum}, allIssues, "herd/batch/1-batch",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, dispatched)

	update, ok := issueSvc.updatedIssues[dependentNum]
	require.True(t, ok, "dependent issue body should have been updated")
	require.NotNil(t, update.Body)
	assert.Contains(t, *update.Body, fmt.Sprintf("herd:injected-findings:%d", depNum))
	assert.Contains(t, *update.Body, "Key finding: the bug is in foo.go.")

	// Worker dispatch should have happened for the dependent issue.
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, fmt.Sprintf("%d", dependentNum), wf.dispatched[0]["issue_number"])
}

func TestDispatchReadyIssues_SkipsNonManualDeps(t *testing.T) {
	const depNum = 101
	const dependentNum = 301

	depBody := "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nAuto task body.\n"
	dependentBody := fmt.Sprintf(
		"---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [%d]\n---\n\n## Task\nDo dependent\n",
		depNum,
	)

	issueSvc := newMockIssueService()
	// Non-manual (e.g. typical worker) dep that is done.
	issueSvc.getResult[depNum] = &platform.Issue{
		Number: depNum,
		Title:  "Auto task",
		Labels: []string{issues.StatusDone, issues.TypeFeature},
		Body:   depBody,
	}
	issueSvc.getResult[dependentNum] = &platform.Issue{
		Number: dependentNum,
		Title:  "Dependent",
		Labels: []string{issues.StatusReady},
		Body:   dependentBody,
	}
	// A human-looking comment exists, but the dep isn't manual so it must be
	// ignored.
	issueSvc.listCommentsResult = []*platform.Comment{
		{ID: 1, AuthorLogin: "human", Body: "Not findings — this dep isn't manual."},
	}

	mock, wf := newFindingsMocks(issueSvc)

	allIssues := []*platform.Issue{
		issueSvc.getResult[depNum],
		issueSvc.getResult[dependentNum],
	}

	dispatched, err := dispatchReadyIssues(
		context.Background(), mock, findingsTestCfg(),
		[]int{dependentNum}, allIssues, "herd/batch/1-batch",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, dispatched)

	// No body update for the dependent — non-manual deps must not be scanned.
	if update, ok := issueSvc.updatedIssues[dependentNum]; ok {
		assert.Nil(t, update.Body, "dependent body must not be updated when dep is non-manual")
	}
	// Workflow dispatch should still proceed.
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, fmt.Sprintf("%d", dependentNum), wf.dispatched[0]["issue_number"])
}

func TestDispatchReadyIssues_NoFindingsNoInjection(t *testing.T) {
	const depNum = 102
	const dependentNum = 302

	// Manual dep body is only frontmatter (no human content after stripping).
	depBody := "---\nherd:\n  version: 1\n  batch: 1\n---\n"
	dependentBody := fmt.Sprintf(
		"---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [%d]\n---\n\n## Task\nDo dependent\n",
		depNum,
	)

	issueSvc := newMockIssueService()
	issueSvc.getResult[depNum] = &platform.Issue{
		Number: depNum,
		Title:  "Manual",
		State:  "closed",
		Labels: []string{issues.TypeManual},
		Body:   depBody,
	}
	issueSvc.getResult[dependentNum] = &platform.Issue{
		Number: dependentNum,
		Title:  "Dependent",
		Labels: []string{issues.StatusReady},
		Body:   dependentBody,
	}
	// Bot/automated comments only — must be filtered out by extractFindings.
	issueSvc.listCommentsResult = []*platform.Comment{
		{ID: 1, AuthorLogin: "github-actions[bot]", Body: "Just a bot comment."},
		{ID: 2, AuthorLogin: "human", Body: "👋 **Manual task** required, here is what to do..."},
	}

	mock, wf := newFindingsMocks(issueSvc)

	allIssues := []*platform.Issue{
		issueSvc.getResult[depNum],
		issueSvc.getResult[dependentNum],
	}

	dispatched, err := dispatchReadyIssues(
		context.Background(), mock, findingsTestCfg(),
		[]int{dependentNum}, allIssues, "herd/batch/1-batch",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, dispatched)

	if update, ok := issueSvc.updatedIssues[dependentNum]; ok {
		assert.Nil(t, update.Body, "no human findings → dependent body must not be updated")
	}
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, fmt.Sprintf("%d", dependentNum), wf.dispatched[0]["issue_number"])
}

func TestDispatchReadyIssues_Idempotent(t *testing.T) {
	const depNum = 103
	const dependentNum = 303

	depBody := "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nManual content.\n"
	// Dependent body already contains the injected-findings marker for the dep,
	// so injectFindings is a no-op and no Update should occur.
	dependentBody := fmt.Sprintf(
		"---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [%d]\n---\n\n## Task\nDo dependent\n\n<!-- herd:injected-findings:%d -->\n## Context from #%d (manual task)\n\npreviously injected.\n<!-- /herd:injected-findings:%d -->\n",
		depNum, depNum, depNum, depNum,
	)

	issueSvc := newMockIssueService()
	issueSvc.getResult[depNum] = &platform.Issue{
		Number: depNum,
		Title:  "Manual",
		State:  "closed",
		Labels: []string{issues.TypeManual},
		Body:   depBody,
	}
	issueSvc.getResult[dependentNum] = &platform.Issue{
		Number: dependentNum,
		Title:  "Dependent",
		Labels: []string{issues.StatusReady},
		Body:   dependentBody,
	}
	issueSvc.listCommentsResult = []*platform.Comment{
		{ID: 1, AuthorLogin: "human", Body: "More content, but should not be re-injected."},
	}

	mock, wf := newFindingsMocks(issueSvc)

	allIssues := []*platform.Issue{
		issueSvc.getResult[depNum],
		issueSvc.getResult[dependentNum],
	}

	dispatched, err := dispatchReadyIssues(
		context.Background(), mock, findingsTestCfg(),
		[]int{dependentNum}, allIssues, "herd/batch/1-batch",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, dispatched)

	// No Update because the marker is already present in the body.
	if update, ok := issueSvc.updatedIssues[dependentNum]; ok {
		assert.Nil(t, update.Body, "body already contains the marker; no Update should be issued")
	}
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, fmt.Sprintf("%d", dependentNum), wf.dispatched[0]["issue_number"])
}

func TestDispatchReadyIssues_ManyDepsStayUnderBodyLimit(t *testing.T) {
	const dependentNum = 400

	// 10 manual deps; perDepFindingsCap is 8KB so the body would explode past
	// MaxIssueBodyChars (65000) if uncapped. The helper must skip the
	// overflow injections.
	depNums := []int{500, 501, 502, 503, 504, 505, 506, 507, 508, 509}

	// Build a "big" findings body for each dep — ~8KB of unique content so it
	// hits the per-dep cap.
	bigContent := strings.Repeat("findings line — useful info from manual investigation.\n", 200) // ~10KB raw
	// Each dep body includes its number so injected blocks remain distinct.
	mkDepBody := func(n int) string {
		return fmt.Sprintf("---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nManual #%d\n\n%s", n, bigContent)
	}

	depsOnYAML := ""
	for i, n := range depNums {
		if i > 0 {
			depsOnYAML += ", "
		}
		depsOnYAML += fmt.Sprintf("%d", n)
	}
	dependentBody := fmt.Sprintf(
		"---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [%s]\n---\n\n## Task\nDo dependent\n",
		depsOnYAML,
	)

	issueSvc := newMockIssueService()
	var allIssues []*platform.Issue
	for _, n := range depNums {
		iss := &platform.Issue{
			Number: n,
			Title:  fmt.Sprintf("Manual %d", n),
			State:  "closed",
			Labels: []string{issues.TypeManual},
			Body:   mkDepBody(n),
		}
		issueSvc.getResult[n] = iss
		allIssues = append(allIssues, iss)
	}
	dependent := &platform.Issue{
		Number: dependentNum,
		Title:  "Dependent",
		Labels: []string{issues.StatusReady},
		Body:   dependentBody,
	}
	issueSvc.getResult[dependentNum] = dependent
	allIssues = append(allIssues, dependent)

	// No comments needed — the dep body itself supplies the bulky findings.
	issueSvc.listCommentsResult = nil

	mock, wf := newFindingsMocks(issueSvc)

	dispatched, err := dispatchReadyIssues(
		context.Background(), mock, findingsTestCfg(),
		[]int{dependentNum}, allIssues, "herd/batch/1-batch",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, dispatched)

	update, ok := issueSvc.updatedIssues[dependentNum]
	require.True(t, ok, "at least one dep should have been injected")
	require.NotNil(t, update.Body)
	assert.LessOrEqual(t, len(*update.Body), issues.MaxIssueBodyChars,
		"persisted body must never exceed the issue body limit")

	// Dispatch must still succeed for the dependent issue.
	require.Len(t, wf.dispatched, 1)
	assert.Equal(t, fmt.Sprintf("%d", dependentNum), wf.dispatched[0]["issue_number"])
}
