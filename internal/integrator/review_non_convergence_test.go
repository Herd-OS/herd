package integrator

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReviewHistoryCycle_WithMarkerAggregationAndFixIssues(t *testing.T) {
	marker, err := buildReviewResultMarker(newReviewResultMarker(849, 111, "abc123", reviewResultStatusChangesRequested, 4, 20, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	comment := &platform.Comment{
		AuthorLogin: "herd[bot]",
		Body: strings.Join([]string{
			"🔍 **HerdOS Agent Review** (cycle 3 of 5)",
			"",
			"Found 18 issues:",
			"",
			"**HIGH** (fix worker dispatched → #951):",
			"- internal/controlplane/dispatch/worker.go: durable mutation lacks idempotency before started workflow retry",
			"",
			"**MEDIUM**:",
			"1. **[MEDIUM]**: internal/controlplane/commands/review.go: post-call side-effect records unknown repair state",
			"",
			"## Review Aggregation",
			"- Raw findings before dedupe: 23",
			"- Findings after dedupe: 20",
			"- Stale PR-state findings ignored: 2",
			"",
			"## Diff Coverage",
			"- Reviewed 4 chunks",
			"",
			"created strategy-level fix issue #952",
			marker,
		}, "\n"),
	}

	cycle, ok := parseReviewHistoryCycle(comment, 849, 111, "abc123")
	require.True(t, ok)
	assert.Equal(t, 4, cycle.Cycle, "marker cycle wins over visible text")
	assert.Equal(t, "abc123", cycle.HeadSHA)
	assert.Equal(t, reviewResultStatusChangesRequested, cycle.Status)
	assert.Equal(t, 23, cycle.RawFindingsBeforeDedupe)
	assert.Equal(t, 20, cycle.FindingsAfterDedupe)
	assert.Equal(t, 2, cycle.StalePRStateFindingsIgnored)
	assert.Equal(t, 20, cycle.PostedFindingsCount)
	assert.Equal(t, []int{951, 952}, cycle.FixIssueNumbers)
	assert.Contains(t, cycle.ChunkCoverageSummary, "## Review Aggregation")
	assert.Contains(t, cycle.FindingsBySeverity["HIGH"], "internal/controlplane/dispatch/worker.go: durable mutation lacks idempotency before started workflow retry")
	assert.Contains(t, cycle.FindingsBySeverity["MEDIUM"], "internal/controlplane/commands/review.go: post-call side-effect records unknown repair state")
}

func TestParseReviewHistoryCycle_OlderCommentWithoutMarkerUsesPartialData(t *testing.T) {
	comment := &platform.Comment{
		AuthorLogin: "trusted-human",
		Body: strings.Join([]string{
			"🔍 **HerdOS Agent Review** (cycle 2 of 5)",
			"",
			"Found 2 issues:",
			"",
			"- **HIGH** internal/controlplane/dispatch/queue.go: pre-call side-effect starts workflow twice",
			"- **LOW** docs/review/history.md: wording is unclear",
			"",
			"fix #951",
		}, "\n"),
	}

	cycle, ok := parseReviewHistoryCycle(comment, 849, 111, "abc123", "trusted-human")
	require.True(t, ok)
	assert.Equal(t, 2, cycle.Cycle)
	assert.Empty(t, cycle.HeadSHA)
	assert.Equal(t, 2, cycle.PostedFindingsCount)
	assert.Equal(t, []int{951}, cycle.FixIssueNumbers)
	assert.Len(t, cycle.FindingsBySeverity["HIGH"], 1)
	assert.Len(t, cycle.FindingsBySeverity["LOW"], 1)
}

func TestParseReviewHistoryCycle_AcceptsOlderMarkerHeadSHA(t *testing.T) {
	marker, err := buildReviewResultMarker(newReviewResultMarker(849, 111, "older-head", reviewResultStatusChangesRequested, 34, 14, time.Now()))
	require.NoError(t, err)
	comment := &platform.Comment{
		AuthorLogin: "herd[bot]",
		Body: strings.Join([]string{
			"🔍 **HerdOS Agent Review** (cycle 34 of 100)",
			"",
			"Found 14 issues:",
			"",
			"- **HIGH** internal/controlplane/dispatch/history.go: durable mutation lacks idempotency",
			marker,
		}, "\n"),
	}

	cycle, ok := parseReviewHistoryCycle(comment, 849, 111, "latest-head")
	require.True(t, ok)
	assert.Equal(t, 34, cycle.Cycle)
	assert.Equal(t, "older-head", cycle.HeadSHA)
	assert.Equal(t, 14, cycle.PostedFindingsCount)
	assert.Contains(t, cycle.FindingsBySeverity["HIGH"], "internal/controlplane/dispatch/history.go: durable mutation lacks idempotency")
}

func TestParseReviewHistoryCycle_RejectsUntrustedOrMismatchedMarker(t *testing.T) {
	marker, err := buildReviewResultMarker(newReviewResultMarker(850, 111, "abc123", reviewResultStatusChangesRequested, 1, 1, time.Now()))
	require.NoError(t, err)

	tests := []struct {
		name    string
		comment *platform.Comment
	}{
		{
			name:    "untrusted",
			comment: &platform.Comment{AuthorLogin: "stranger", Body: "Found 1 issue:\n- **HIGH** bug"},
		},
		{
			name:    "wrong pr marker",
			comment: &platform.Comment{AuthorLogin: "herd[bot]", Body: marker},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseReviewHistoryCycle(tt.comment, 849, 111, "abc123")
			assert.False(t, ok)
		})
	}
}

func TestCollectReviewHistoryFromComments_WindowsAndAttachesFixIssues(t *testing.T) {
	comments := []*platform.Comment{
		reviewHistoryComment(t, "head", 1, 4, "internal/controlplane/dispatch/a.go: idempotency mutation", 901),
		reviewHistoryComment(t, "head", 2, 5, "internal/controlplane/dispatch/b.go: idempotency mutation", 902),
		reviewHistoryComment(t, "old", 3, 6, "internal/controlplane/dispatch/c.go: idempotency mutation", 903),
	}
	allIssues := []*platform.Issue{
		reviewFixIssue(901, 1, issues.StatusDone, []string{"internal/controlplane/dispatch/a.go"}, "Validation passed"),
		reviewFixIssue(902, 2, issues.StatusDone, []string{"internal/controlplane/dispatch/b.go"}, "Validation passed"),
		reviewFixIssue(903, 3, issues.StatusInProgress, []string{"internal/controlplane/dispatch/c.go"}, "Worker Report"),
	}

	cycles := collectReviewHistoryFromComments(comments, allIssues, 849, 111, "head", 2)
	require.Len(t, cycles, 2)
	assert.Equal(t, []int{2, 3}, []int{cycles[0].Cycle, cycles[1].Cycle})
	assert.Equal(t, []int{902}, cycles[0].FixIssueNumbers)
	assert.Equal(t, issues.StatusDone, cycles[0].FixIssues[0].StatusLabel)
	assert.Equal(t, issues.StatusInProgress, cycles[1].FixIssues[0].StatusLabel)
	assert.True(t, cycles[0].FixIssues[0].WorkerReport)
	assert.Contains(t, cycles[0].FixIssues[0].FilesSummary, "internal/controlplane/dispatch/b.go")
}

func TestAttachReviewFixIssueHistory_FiltersReviewFixes(t *testing.T) {
	cycles := []reviewHistoryCycle{{Cycle: 1, FixIssueNumbers: []int{100}}, {Cycle: 2}}
	allIssues := []*platform.Issue{
		reviewFixIssue(100, 1, issues.StatusDone, []string{"internal/controlplane/dispatch/a.go"}, "## Summary\nDone\n\nValidation success"),
		reviewFixIssue(101, 2, issues.StatusReady, []string{"internal/controlplane/commands/b.go"}, ""),
		reviewFixIssue(103, 1, issues.StatusDone, []string{"internal/controlplane/dispatch/extra.go"}, ""),
		{
			Number: 102,
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n  batch: 111\n  type: fix\n  batch_pr: 849\n  ci_fix_cycle: 1\n---\n\n## Task\nCI",
		},
	}

	got := attachReviewFixIssueHistory(cycles, allIssues)
	require.Len(t, got[0].FixIssues, 1)
	require.Len(t, got[1].FixIssues, 1)
	assert.Equal(t, 100, got[0].FixIssues[0].Number)
	assert.Equal(t, 101, got[1].FixIssues[0].Number)
	assert.Equal(t, []int{101}, got[1].FixIssueNumbers)
}

func TestPackageClusterFromFinding(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"cmd service", "cmd/herd-service/main.go: bug", "cmd/herd-service"},
		{"internal jobs", "internal/controlplane/jobs/review.go: bug", "internal/controlplane/jobs"},
		{"internal review", "`internal/controlplane/review/non_convergence.go`: bug", "internal/controlplane/review"},
		{"internal orchestration", "internal/controlplane/orchestration/runner.go: bug", "internal/controlplane/orchestration"},
		{"internal git", "internal/git/repo.go: bug", "internal/git"},
		{"docs", "docs/review.md: bug", "docs"},
		{"specs", "specs/api.md: bug", "specs"},
		{"fraction", "1/9", ""},
		{"fraction two", "2/9", ""},
		{"chunk label", "Chunk 1/9", ""},
		{"diff coverage", "Diff Coverage", ""},
		{"review aggregation", "Review Aggregation", ""},
		{"files reviewed", "Files reviewed", ""},
		{"local git source", "Source: local-git", ""},
		{"empty", "no path here", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, packageClusterFromFinding(tt.in))
		})
	}
}

func TestExtractReviewFindingsBySeverityStopsBeforeCoverageMetadata(t *testing.T) {
	body := strings.Join([]string{
		"🔍 **HerdOS Agent Review** (cycle 64 of 100)",
		"",
		"**HIGH**:",
		"- internal/controlplane/jobs/a.go: durable mutation lacks idempotency before workflow retry",
		"- internal/controlplane/jobs/b.go: durable mutation lacks idempotency before workflow retry",
		"- internal/controlplane/jobs/c.go: durable mutation lacks idempotency before workflow retry",
		"",
		"## Diff Coverage",
		"- Chunk 1/9",
		"- 2/9",
		"- Chunks reviewed: 9/9",
		"",
		"## Review Aggregation",
		"- Files reviewed: internal/controlplane/jobs/a.go",
		"- Source: local-git",
	}, "\n")

	findings := extractReviewFindingsBySeverity(body)
	require.Len(t, findings["HIGH"], 3)
	for _, noisy := range []string{"1/9", "2/9", "Chunk 1/9", "Chunks reviewed: 9/9", "Diff Coverage", "Review Aggregation", "Files reviewed", "Source: local-git"} {
		assert.NotContains(t, strings.Join(findings["HIGH"], "\n"), noisy)
	}

	cluster := buildReviewConvergenceCluster([]reviewHistoryCycle{{Cycle: 64, FindingsBySeverity: findings}})
	assert.Contains(t, cluster.PackageClusters, "internal/controlplane/jobs")
	for _, noisy := range []string{"1/9", "2/9", "Chunk 1/9", "Chunks reviewed: 9/9"} {
		assert.NotContains(t, cluster.PackageClusters, noisy)
		assert.NotContains(t, cluster.Summary, noisy)
	}
}

func TestRootCauseTermsFromFinding(t *testing.T) {
	got := rootCauseTermsFromFinding("internal/controlplane/dispatch/worker.go: durable mutation breaks idempotency; pre-call side effect started workflow before unknown repair. Suggested fix: mention retry")
	assert.ElementsMatch(t, []string{"dispatch", "durable", "idempotency", "mutation", "pre-call", "repair", "side effect", "started", "unknown", "workflow"}, got)
	assert.NotContains(t, got, "retry", "boilerplate after Suggested fix should be ignored")
}

func TestBuildReviewConvergenceClusterAndFingerprint(t *testing.T) {
	cycles := []reviewHistoryCycle{
		reviewHistoryCycleWithFinding(1, 10, "internal/controlplane/dispatch/a.go: durable mutation lacks idempotency"),
		reviewHistoryCycleWithFinding(2, 10, "internal/controlplane/dispatch/b.go: durable mutation lacks idempotency"),
		reviewHistoryCycleWithFinding(3, 10, "internal/controlplane/commands/c.go: durable mutation lacks idempotency"),
		reviewHistoryCycleWithFinding(4, 10, "internal/controlplane/commands/d.go: durable mutation lacks idempotency"),
	}

	cluster := buildReviewConvergenceCluster(cycles)
	assert.Contains(t, cluster.PackageClusters, "internal/controlplane/dispatch")
	assert.Contains(t, cluster.PackageClusters, "internal/controlplane/commands")
	assert.Contains(t, cluster.RootCauseTerms, "durable")
	assert.Contains(t, cluster.RootCauseTerms, "idempotency")
	assert.NotEmpty(t, cluster.Fingerprint)
	assert.Equal(t, cluster.Fingerprint, reviewNonConvergenceFingerprint(cluster))
	assert.Contains(t, cluster.Summary, "packages:")
}

func TestAnalyzeReviewConvergence_EscalatesForIncreasingPR849StyleTrend(t *testing.T) {
	counts := []int{14, 20, 21, 24, 28}
	var cycles []reviewHistoryCycle
	for i, count := range counts {
		cycle := reviewHistoryCycleWithFinding(i+1, count, fmt.Sprintf("internal/controlplane/dispatch/file%d.go: durable mutation lacks idempotency before started workflow retry", i))
		cycle.FixIssues = []reviewHistoryFixIssue{{Number: 950 + i, StatusLabel: issues.StatusDone, WorkerReport: true, ValidationStatus: "success"}}
		cycles = append(cycles, cycle)
	}

	analysis := analyzeReviewConvergence(cycles, 3)
	assert.Equal(t, reviewDecisionEscalateToArchitectureFix, analysis.Decision)
	assert.Equal(t, counts, analysis.TrendCounts)
	assert.Equal(t, 14, analysis.EarliestFindingCount)
	assert.Equal(t, 28, analysis.LatestFindingCount)
	assert.Equal(t, []int{950, 951, 952, 953, 954}, analysis.CompletedFixIssues)
	assert.Contains(t, analysis.Cluster.PackageClusters, "internal/controlplane/dispatch")
	assert.Contains(t, analysis.Rationale, "increasing or flat")
}

func TestAnalyzeReviewConvergence_ContinuesForDecreasingTrend(t *testing.T) {
	counts := []int{20, 12, 5, 1}
	var cycles []reviewHistoryCycle
	for i, count := range counts {
		cycle := reviewHistoryCycleWithFinding(i+1, count, "internal/controlplane/dispatch/file.go: durable mutation lacks idempotency")
		cycle.FixIssues = []reviewHistoryFixIssue{{Number: 900 + i, StatusLabel: issues.StatusDone}}
		cycles = append(cycles, cycle)
	}

	analysis := analyzeReviewConvergence(cycles, 2)
	assert.Equal(t, reviewDecisionContinueFixLoop, analysis.Decision)
	assert.Equal(t, counts, analysis.TrendCounts)
	assert.Contains(t, analysis.Rationale, "below non-convergence threshold")
}

func TestAnalyzeReviewConvergence_MinCompletedCyclesAndLatestInProgress(t *testing.T) {
	tests := []struct {
		name      string
		cycles    []reviewHistoryCycle
		wantIssue []int
		wantText  string
	}{
		{
			name: "fewer than min completed cycles",
			cycles: []reviewHistoryCycle{
				{Cycle: 1, FindingsAfterDedupe: 14, FindingsBySeverity: map[string][]string{"HIGH": {"internal/controlplane/dispatch/a.go: durable mutation lacks idempotency"}}, FixIssues: []reviewHistoryFixIssue{{Number: 1, StatusLabel: issues.StatusDone}}},
				{Cycle: 2, FindingsAfterDedupe: 20, FindingsBySeverity: map[string][]string{"HIGH": {"internal/controlplane/dispatch/b.go: durable mutation lacks idempotency"}}},
			},
			wantText: "need at least 2",
		},
		{
			name: "latest in progress",
			cycles: []reviewHistoryCycle{
				{Cycle: 1, FindingsAfterDedupe: 14, FindingsBySeverity: map[string][]string{"HIGH": {"internal/controlplane/dispatch/a.go: durable mutation lacks idempotency"}}, FixIssues: []reviewHistoryFixIssue{{Number: 1, StatusLabel: issues.StatusDone}}},
				{Cycle: 2, FindingsAfterDedupe: 20, FindingsBySeverity: map[string][]string{"HIGH": {"internal/controlplane/dispatch/b.go: durable mutation lacks idempotency"}}, FixIssues: []reviewHistoryFixIssue{{Number: 2, StatusLabel: issues.StatusReady}}},
			},
			wantIssue: []int{2},
			wantText:  "synthesis is deferred",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis := analyzeReviewConvergence(tt.cycles, 2)
			assert.Equal(t, reviewDecisionContinueFixLoop, analysis.Decision)
			assert.Equal(t, tt.wantIssue, analysis.InProgressFixIssues)
			assert.Contains(t, analysis.Rationale, tt.wantText)
		})
	}
}

func TestAnalyzeReviewConvergence_DefersForLatestFixBearingCycleBeforeCurrentReview(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantIssues []int
	}{
		{
			name:       "ready latest fix before appended current review",
			status:     issues.StatusReady,
			wantIssues: []int{954},
		},
		{
			name:       "in-progress latest fix before appended current review",
			status:     issues.StatusInProgress,
			wantIssues: []int{954},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counts := []int{14, 20, 21, 24, 28}
			cycles := make([]reviewHistoryCycle, 0, len(counts)+1)
			for i, count := range counts {
				cycle := reviewHistoryCycleWithFinding(i+1, count, fmt.Sprintf("internal/controlplane/dispatch/file%d.go: durable mutation lacks idempotency before started workflow retry", i))
				cycle.FixIssues = []reviewHistoryFixIssue{{Number: 950 + i, StatusLabel: issues.StatusDone, WorkerReport: true, ValidationStatus: "success"}}
				cycles = append(cycles, cycle)
			}
			cycles[len(cycles)-1].FixIssues[0] = reviewHistoryFixIssue{Number: 954, StatusLabel: tt.status}
			cycles = append(cycles, reviewHistoryCycleWithFinding(6, 28, "internal/controlplane/dispatch/current.go: durable mutation lacks idempotency before started workflow retry"))

			analysis := analyzeReviewConvergence(cycles, 3)

			assert.Equal(t, reviewDecisionContinueFixLoop, analysis.Decision)
			assert.Equal(t, tt.wantIssues, analysis.InProgressFixIssues)
			assert.Equal(t, []int{950, 951, 952, 953}, analysis.CompletedFixIssues)
			assert.Contains(t, analysis.Rationale, "synthesis is deferred")
		})
	}
}

func TestLatestFixBearingReviewCycle(t *testing.T) {
	tests := []struct {
		name      string
		cycles    []reviewHistoryCycle
		wantCycle int
		wantOK    bool
	}{
		{
			name: "empty cycles",
		},
		{
			name: "no fix-bearing cycles",
			cycles: []reviewHistoryCycle{
				{Cycle: 1},
				{Cycle: 2},
			},
		},
		{
			name: "latest fix-bearing cycle ignores appended current review",
			cycles: []reviewHistoryCycle{
				{Cycle: 3, FixIssues: []reviewHistoryFixIssue{{Number: 103}}},
				{Cycle: 4},
			},
			wantCycle: 3,
			wantOK:    true,
		},
		{
			name: "highest fix-bearing cycle wins",
			cycles: []reviewHistoryCycle{
				{Cycle: 3, FixIssues: []reviewHistoryFixIssue{{Number: 103}}},
				{Cycle: 4},
				{Cycle: 5, FixIssues: []reviewHistoryFixIssue{{Number: 105}}},
			},
			wantCycle: 5,
			wantOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCycle, gotOK := latestFixBearingReviewCycle(tt.cycles)
			assert.Equal(t, tt.wantCycle, gotCycle)
			assert.Equal(t, tt.wantOK, gotOK)
		})
	}
}

func TestBuildStrategyFixIssueTitle(t *testing.T) {
	tests := []struct {
		name    string
		cycle   int
		cluster reviewConvergenceCluster
		want    string
	}{
		{
			name:    "durable mutation title",
			cycle:   65,
			cluster: reviewConvergenceCluster{PackageClusters: []string{"internal/controlplane/review"}, RootCauseTerms: []string{"durable", "mutation"}},
			want:    "Review strategy fix (cycle 65): durable mutation boundary gaps",
		},
		{
			name:    "hosted app package plus terms",
			cycle:   65,
			cluster: reviewConvergenceCluster{PackageClusters: []string{"cmd/herd-hosted-app"}, RootCauseTerms: []string{"idempotency", "repair"}},
			want:    "Review strategy fix (cycle 65): hosted App idempotency repair",
		},
		{
			name:    "control plane retry title",
			cycle:   65,
			cluster: reviewConvergenceCluster{PackageClusters: []string{"internal/controlplane/jobs"}, RootCauseTerms: []string{"retry", "workflow"}},
			want:    "Review strategy fix (cycle 65): repeated control-plane retry failures",
		},
		{
			name:    "default fallback",
			cycle:   65,
			cluster: reviewConvergenceCluster{PackageClusters: []string{"1/9", "Chunk 1/9"}, RootCauseTerms: []string{"Diff Coverage"}},
			want:    "Review strategy fix (cycle 65): repeated review findings",
		},
		{
			name:    "long title is truncated",
			cycle:   9,
			cluster: reviewConvergenceCluster{PackageClusters: []string{strings.Repeat("a", 140)}},
			want:    "Review strategy fix (cycle 9): " + strings.Repeat("a", 86) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildStrategyFixIssueTitle(tt.cycle, tt.cluster)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len([]rune(got)), 120)
		})
	}
}

func TestBuildStrategyFixIssueBody(t *testing.T) {
	analysis := reviewStrategyAnalysisFixture()
	title := buildStrategyFixIssueTitle(6, analysis.Cluster)
	body := buildStrategyFixIssueBody(
		&platform.Milestone{Number: 111, Title: "Batch"},
		&platform.PullRequest{Number: 849, Title: "[herd] Batch"},
		6,
		analysis,
	)

	parsed, err := issues.ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, 1, parsed.FrontMatter.Version)
	assert.Equal(t, 111, parsed.FrontMatter.Batch)
	assert.Equal(t, "fix", parsed.FrontMatter.Type)
	assert.Equal(t, 6, parsed.FrontMatter.FixCycle)
	assert.Equal(t, 849, parsed.FrontMatter.BatchPR)
	assert.Equal(t, []string{"internal/controlplane/commands", "internal/controlplane/dispatch"}, parsed.FrontMatter.Scope)

	assert.Contains(t, parsed.Task, "shared architecture/design problem")
	assert.Contains(t, parsed.Task, "Do not process each endpoint-level finding independently")
	assert.Contains(t, parsed.ImplementationDetails, "## Repeated Pattern")
	assert.Contains(t, parsed.ImplementationDetails, "## Representative Findings")
	assert.Contains(t, parsed.ImplementationDetails, "## Prior Fix Attempts")
	assert.Contains(t, parsed.ImplementationDetails, "## Strategy Guidance")
	assert.Contains(t, parsed.ImplementationDetails, "internal/controlplane/commands, internal/controlplane/dispatch")
	assert.Contains(t, parsed.ImplementationDetails, "durable GitHub-visible mutation boundaries")
	assert.Contains(t, parsed.ImplementationDetails, "pre-call, call-started, post-call-unknown, completed, failed-pre-call, and repair-required states")
	assert.Contains(t, parsed.ImplementationDetails, "comments, issues, or workflow dispatches")
	assert.Contains(t, parsed.ImplementationDetails, "Cycle 5 HIGH: internal/controlplane/dispatch/worker.go: retry can duplicate workflow dispatches after post-call unknown state")
	assert.Contains(t, parsed.ImplementationDetails, "#951 (cycle 3): herd/status:done; success; worker report present; files: internal/controlplane/commands/review.go")
	assert.Contains(t, parsed.ImplementationDetails, "#952 (cycle 4): herd/status:done; success; worker report present; files: internal/controlplane/dispatch/worker.go")
	assert.Contains(t, parsed.ImplementationDetails, "Fix the shared invariant or state transition")
	assert.Contains(t, parsed.Criteria, "Architecture-level abstraction or invariant is documented in code.")
	assert.Contains(t, parsed.Criteria, "Clustered packages are migrated to the strategy or explicitly justified if a package is left unchanged.")
	assert.Contains(t, parsed.Criteria, "Idempotency and repair behavior is covered by regression tests.")
	assert.Contains(t, parsed.Criteria, "No duplicate endpoint-level loop behavior is introduced.")
	assert.Contains(t, parsed.Criteria, "Relevant package tests are run and reported.")
	assert.Contains(t, parsed.Context, "Cycles analyzed: 3, 4, 5")
	assert.Contains(t, parsed.Context, "Finding count trend: 14, 20, 20")
	assert.Contains(t, parsed.Context, "Completed fix issues: #951, #952")
	assert.Contains(t, parsed.Context, "In-progress fix issues: #953")
	assert.Contains(t, parsed.Context, "Dominant package clusters: internal/controlplane/commands, internal/controlplane/dispatch")
	assert.Contains(t, parsed.Context, "Dominant root-cause terms: durable, idempotency, mutation, retry")
	assert.Contains(t, parsed.Context, "Rationale: finding trend is increasing or flat after completed fix cycles")

	fingerprint, ok := parseReviewNonConvergenceFingerprint(body)
	require.True(t, ok)
	assert.Equal(t, analysis.Cluster.Fingerprint, fingerprint)
	assert.Contains(t, body, `"version":1`)
	assert.Contains(t, body, `"batch_pr":849`)
	for _, noisy := range []string{"1/9", "Chunk 1/9", "Diff Coverage", "Review Aggregation", "Files reviewed", "Source: local-git"} {
		assert.NotContains(t, body, noisy)
		assert.NotContains(t, title, noisy)
		assert.NotContains(t, parsed.Context, noisy)
	}
}

func TestSynthesizedReviewStrategyFingerprintNormalizesRootCauseAndSymptoms(t *testing.T) {
	a := highConfidenceReviewSynthesisResult()
	b := highConfidenceReviewSynthesisResult()
	b.RootCauseTitle = "  dispatch: IDEMPOTENCY boundary is split across review paths!!! "
	b.RecurringSymptoms = []agent.ReviewSynthesisSymptom{
		{
			Description:   "Unknown state repair paths update labels before converging on one dispatch outcome",
			Cycles:        []int{39, 38, 36, 38},
			AffectedFiles: []string{"internal/controlplane/dispatch/review.go", "internal/controlplane/dispatch/repair.go", "internal/controlplane/dispatch/repair.go"},
		},
		{
			Description:   "Started workflow retry can dispatch twice before the durable record is repaired",
			Cycles:        []int{37, 35, 39},
			AffectedFiles: []string{"internal/controlplane/dispatch/review.go", "internal/controlplane/dispatch/retry.go"},
		},
	}

	got := synthesizedReviewStrategyFingerprint(a)
	assert.Len(t, got, 12)
	assert.Equal(t, got, synthesizedReviewStrategyFingerprint(b))

	changed := highConfidenceReviewSynthesisResult()
	changed.RecurringSymptoms[0].AffectedFiles = []string{"internal/other/path.go"}
	assert.NotEqual(t, got, synthesizedReviewStrategyFingerprint(changed))
}

func TestEvaluateReviewSynthesisSafetyGates(t *testing.T) {
	base := highConfidenceReviewSynthesisResult()
	analysis := reviewConvergenceAnalysis{CompletedFixIssues: []int{951, 952}}
	tests := []struct {
		name   string
		result func() *agent.ReviewSynthesisResult
		want   string
	}{
		{name: "nil result", result: func() *agent.ReviewSynthesisResult { return nil }, want: "nil result"},
		{name: "no escalation", result: func() *agent.ReviewSynthesisResult { r := *base; r.ShouldEscalate = false; return &r }, want: "not to escalate"},
		{name: "low confidence", result: func() *agent.ReviewSynthesisResult { r := *base; r.Confidence = 0.70; return &r }, want: "below threshold"},
		{name: "insufficient symptoms", result: func() *agent.ReviewSynthesisResult {
			r := *base
			r.RecurringSymptoms = r.RecurringSymptoms[:1]
			return &r
		}, want: "need at least 2"},
		{name: "missing cycles and attempts", result: func() *agent.ReviewSynthesisResult {
			r := *base
			r.RecurringSymptoms = []agent.ReviewSynthesisSymptom{
				{Description: "one", Cycles: []int{39}, AffectedFiles: []string{"a.go"}},
				{Description: "two", Cycles: []int{39}, AffectedFiles: []string{"b.go"}},
			}
			return &r
		}, want: "do not span two cycles"},
		{name: "missing title", result: func() *agent.ReviewSynthesisResult { r := *base; r.RootCauseTitle = " "; return &r }, want: "title is empty"},
		{name: "missing criteria", result: func() *agent.ReviewSynthesisResult { r := *base; r.AcceptanceCriteria = []string{" ", ""}; return &r }, want: "criteria are empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := evaluateReviewSynthesis(tt.result(), 0.75, func() reviewConvergenceAnalysis {
				if tt.name == "missing cycles and attempts" {
					return reviewConvergenceAnalysis{CompletedFixIssues: []int{951}}
				}
				return analysis
			}())
			assert.Equal(t, reviewSynthesisDecisionFallback, got)
			assert.Contains(t, reason, tt.want)
		})
	}

	got, reason := evaluateReviewSynthesis(base, 0.75, reviewConvergenceAnalysis{CompletedFixIssues: []int{951}})
	assert.Equal(t, reviewSynthesisDecisionEscalate, got)
	assert.Contains(t, reason, "passed")
}

func TestBuildSynthesizedStrategyFixIssueBody(t *testing.T) {
	result := highConfidenceReviewSynthesisResult()
	input := agent.ReviewSynthesisInput{
		PRNumber:             849,
		BatchNumber:          111,
		HeadSHA:              "head-123",
		HeadRef:              "herd/batch/111-batch",
		CurrentPRMetadata:    "Head SHA: head-123",
		AffectedFiles:        []string{"internal/controlplane/dispatch/review.go"},
		RecentReviewComments: []string{"review comment source context"},
		Cycles: []agent.ReviewSynthesisCycle{
			{Cycle: 38, Status: "changes_requested", FindingsAfterDedupe: 28, FixIssueNumbers: []int{955}, AffectedFiles: []string{"internal/controlplane/dispatch/retry.go"}},
			{Cycle: 39, Status: "changes_requested", FindingsAfterDedupe: 28, AffectedFiles: []string{"internal/controlplane/dispatch/review.go"}},
		},
		CompletedFixIssues: []agent.ReviewSynthesisFixIssue{{Number: 955, StatusLabel: issues.StatusDone}},
	}
	fingerprint := synthesizedReviewStrategyFingerprint(result)

	body := buildSynthesizedStrategyFixIssueBody(&platform.Milestone{Number: 111}, &platform.PullRequest{Number: 849}, 40, result, input, fingerprint)

	parsed, err := issues.ParseBody(body)
	require.NoError(t, err)
	assert.Equal(t, 111, parsed.FrontMatter.Batch)
	assert.Equal(t, "fix", parsed.FrontMatter.Type)
	assert.Equal(t, 40, parsed.FrontMatter.FixCycle)
	assert.Equal(t, 849, parsed.FrontMatter.BatchPR)
	assert.ElementsMatch(t, []string{
		"internal/controlplane/dispatch/repair.go",
		"internal/controlplane/dispatch/retry.go",
		"internal/controlplane/dispatch/review.go",
	}, parsed.FrontMatter.Scope)
	assert.Contains(t, parsed.Task, "synthesized architectural/root-cause fix")
	assert.Contains(t, parsed.Task, "not a normal individual review finding")
	assert.Contains(t, parsed.ImplementationDetails, "Root cause title:")
	assert.Contains(t, parsed.ImplementationDetails, "Recurring symptoms:")
	assert.Contains(t, parsed.ImplementationDetails, "Why previous individual fixes did not converge:")
	assert.Contains(t, parsed.ImplementationDetails, "Proposed strategy:")
	assert.Contains(t, parsed.ImplementationDetails, "Acceptance criteria:")
	assert.Contains(t, parsed.ImplementationDetails, "Non-goals:")
	assert.Contains(t, parsed.ImplementationDetails, "Source review result comments/context:")
	assert.Equal(t, result.AcceptanceCriteria, parsed.Criteria)
	marker, ok := parseReviewNonConvergenceFingerprintMarker(body)
	require.True(t, ok)
	assert.Equal(t, fingerprint, marker.Fingerprint)
	assert.Equal(t, "head-123", marker.HeadSHA)
}

func TestBuildReviewNonConvergencePRComment(t *testing.T) {
	comment := buildReviewNonConvergencePRComment(reviewStrategyAnalysisFixture(), 954)

	assert.True(t, strings.HasPrefix(comment, "⚠️ **Herd review is not converging**"))
	assert.Contains(t, comment, "Cycles analyzed: 3, 4, 5")
	assert.Contains(t, comment, "Finding count trend: 14, 20, 20")
	assert.Contains(t, comment, "Fix issues considered: #951, #952, #953")
	assert.Contains(t, comment, "Dominant package clusters: internal/controlplane/commands, internal/controlplane/dispatch")
	assert.Contains(t, comment, "Dominant root-cause terms: durable, idempotency, mutation, retry")
	for _, noisy := range []string{"1/9", "Chunk 1/9", "Diff Coverage", "Review Aggregation", "Files reviewed", "Source: local-git"} {
		assert.NotContains(t, comment, noisy)
	}
	assert.Contains(t, comment, "Escalation reason: finding trend is increasing or flat after completed fix cycles")
	assert.Contains(t, comment, "Strategy fix issue: #954")
	assert.NotContains(t, comment, "/herd fix")
}

func TestReviewNonConvergenceFingerprintRoundTrip(t *testing.T) {
	body := appendReviewNonConvergenceFingerprint(reviewStrategyIssueBody(849), "abc123finger")
	got, ok := parseReviewNonConvergenceFingerprint(body)
	require.True(t, ok)
	assert.Equal(t, "abc123finger", got)
	assert.Contains(t, body, reviewNonConvergenceFingerprintMarkerPrefix)
	assert.Contains(t, body, reviewNonConvergenceFingerprintMarkerSuffix)

	tests := []struct {
		name string
		body string
	}{
		{name: "missing marker", body: "plain body"},
		{name: "malformed json", body: reviewNonConvergenceFingerprintMarkerPrefix + "{broken" + reviewNonConvergenceFingerprintMarkerSuffix},
		{name: "empty fingerprint", body: reviewNonConvergenceFingerprintMarkerPrefix + `{"version":1,"batch_pr":849,"fingerprint":""}` + reviewNonConvergenceFingerprintMarkerSuffix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseReviewNonConvergenceFingerprint(tt.body)
			assert.False(t, ok)
			assert.Empty(t, got)
		})
	}
}

func TestFindDuplicateStrategyFixIssue(t *testing.T) {
	matchingBody := appendReviewNonConvergenceFingerprint(reviewStrategyIssueBody(849), "fp-match")
	tests := []struct {
		name       string
		issues     []*platform.Issue
		wantNumber int
		wantOK     bool
	}{
		{
			name: "open matching strategy issue",
			issues: []*platform.Issue{
				reviewStrategyIssue(101, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, matchingBody),
			},
			wantNumber: 101,
			wantOK:     true,
		},
		{
			name: "open in-progress matching strategy issue",
			issues: []*platform.Issue{
				reviewStrategyIssue(102, "open", []string{issues.ReviewNonConverging, issues.StatusInProgress}, matchingBody),
			},
			wantNumber: 102,
			wantOK:     true,
		},
		{
			name: "open unlabeled matching strategy issue",
			issues: []*platform.Issue{
				reviewStrategyIssue(103, "open", []string{issues.ReviewNonConverging}, matchingBody),
			},
			wantNumber: 103,
			wantOK:     true,
		},
		{
			name: "closed ready issue ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(104, "closed", []string{issues.ReviewNonConverging, issues.StatusReady}, matchingBody),
			},
		},
		{
			name: "closed in-progress issue ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(105, "closed", []string{issues.ReviewNonConverging, issues.StatusInProgress}, matchingBody),
			},
		},
		{
			name: "closed done issue ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(106, "closed", []string{issues.ReviewNonConverging, issues.StatusDone}, matchingBody),
			},
		},
		{
			name: "closed cancelled issue ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(107, "closed", []string{issues.ReviewNonConverging, issues.StatusCancelled}, matchingBody),
			},
		},
		{
			name: "wrong label ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(108, "open", []string{issues.StatusReady}, matchingBody),
			},
		},
		{
			name: "wrong title ignored",
			issues: []*platform.Issue{
				{Number: 109, State: "open", Title: "Review fixes (cycle 6)", Labels: []string{issues.ReviewNonConverging, issues.StatusReady}, Body: matchingBody},
			},
		},
		{
			name: "wrong batch pr ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(110, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, appendReviewNonConvergenceFingerprint(reviewStrategyIssueBody(850), "fp-match")),
			},
		},
		{
			name: "wrong fingerprint ignored",
			issues: []*platform.Issue{
				reviewStrategyIssue(111, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, appendReviewNonConvergenceFingerprint(reviewStrategyIssueBody(849), "fp-other")),
			},
		},
		{
			name: "fallback body contains fingerprint when marker parse fails",
			issues: []*platform.Issue{
				reviewStrategyIssue(112, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, reviewStrategyIssueBody(849)+"\n"+reviewNonConvergenceFingerprintMarkerPrefix+"broken fp-match"),
			},
			wantNumber: 112,
			wantOK:     true,
		},
		{
			name: "returns first matching active issue",
			issues: []*platform.Issue{
				reviewStrategyIssue(113, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, matchingBody),
				reviewStrategyIssue(114, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, matchingBody),
			},
			wantNumber: 113,
			wantOK:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := findDuplicateStrategyFixIssue(tt.issues, 849, "fp-match")
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantNumber, got.Number)
			} else {
				assert.Nil(t, got)
			}
		})
	}

	got, ok := findDuplicateStrategyFixIssue([]*platform.Issue{
		reviewStrategyIssue(112, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, matchingBody),
	}, 849, "")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestFindDuplicateSynthesizedStrategyFixIssue(t *testing.T) {
	bodyFor := func(pr int, fingerprint, head string) string {
		return appendReviewNonConvergenceFingerprintWithHeadSHA(reviewStrategyIssueBody(pr), fingerprint, head)
	}
	tests := []struct {
		name       string
		issue      *platform.Issue
		head       string
		wantNumber int
		wantOK     bool
	}{
		{
			name:       "open ready matching fingerprint suppresses",
			issue:      reviewStrategyIssue(201, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, bodyFor(849, "fp-match", "old-head")),
			head:       "new-head",
			wantNumber: 201,
			wantOK:     true,
		},
		{
			name:       "open in-progress matching fingerprint suppresses",
			issue:      reviewStrategyIssue(202, "open", []string{issues.ReviewNonConverging, issues.StatusInProgress}, bodyFor(849, "fp-match", "old-head")),
			head:       "new-head",
			wantNumber: 202,
			wantOK:     true,
		},
		{
			name:       "done same head suppresses",
			issue:      reviewStrategyIssue(203, "closed", []string{issues.ReviewNonConverging, issues.StatusDone}, bodyFor(849, "fp-match", "same-head")),
			head:       "same-head",
			wantNumber: 203,
			wantOK:     true,
		},
		{
			name:  "done older head can be superseded",
			issue: reviewStrategyIssue(204, "closed", []string{issues.ReviewNonConverging, issues.StatusDone}, bodyFor(849, "fp-match", "old-head")),
			head:  "new-head",
		},
		{
			name:  "wrong batch pr ignored",
			issue: reviewStrategyIssue(205, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, bodyFor(850, "fp-match", "new-head")),
			head:  "new-head",
		},
		{
			name:  "wrong fingerprint ignored",
			issue: reviewStrategyIssue(206, "open", []string{issues.ReviewNonConverging, issues.StatusReady}, bodyFor(849, "fp-other", "new-head")),
			head:  "new-head",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := findDuplicateSynthesizedStrategyFixIssue([]*platform.Issue{tt.issue}, 849, "fp-match", tt.head)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantNumber, got.Number)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func reviewHistoryComment(t *testing.T, head string, cycle, count int, finding string, fixIssue int) *platform.Comment {
	t.Helper()
	marker, err := buildReviewResultMarker(newReviewResultMarker(849, 111, head, reviewResultStatusChangesRequested, cycle, count, time.Date(2026, 7, 15, 12, cycle, 0, 0, time.UTC)))
	require.NoError(t, err)
	return &platform.Comment{
		AuthorLogin: "herd[bot]",
		Body: fmt.Sprintf("🔍 **HerdOS Agent Review** (cycle %d of 5)\n\nFound %d issues:\n\n**HIGH** (fix worker dispatched → #%d):\n- %s\n\n%s",
			cycle, count, fixIssue, finding, marker),
	}
}

func reviewFixIssue(number, cycle int, status string, files []string, report string) *platform.Issue {
	body := issues.RenderBody(issues.IssueBody{
		FrontMatter:   issues.FrontMatter{Version: 1, Batch: 111, Type: "fix", BatchPR: 849, FixCycle: cycle},
		Task:          "Fix review findings.",
		FilesToModify: files,
	})
	if report != "" {
		body += "\n## Worker Report\n\n" + report + "\n"
	}
	return &platform.Issue{Number: number, Labels: []string{status}, Body: body}
}

func reviewHistoryCycleWithFinding(cycle, count int, finding string) reviewHistoryCycle {
	return reviewHistoryCycle{
		Cycle:               cycle,
		FindingsAfterDedupe: count,
		PostedFindingsCount: count,
		FindingsBySeverity:  map[string][]string{"HIGH": {finding}},
	}
}

func reviewStrategyAnalysisFixture() reviewConvergenceAnalysis {
	return reviewConvergenceAnalysis{
		Decision:   reviewDecisionEscalateToArchitectureFix,
		Confidence: 0.86,
		Rationale:  "finding trend is increasing or flat after completed fix cycles",
		Cycles: []reviewHistoryCycle{
			{
				Cycle: 3,
				FindingsBySeverity: map[string][]string{
					"HIGH": {
						"internal/controlplane/commands/review.go: durable mutation lacks idempotency before retry",
						"Chunk 1/9",
					},
				},
				FixIssues: []reviewHistoryFixIssue{{
					Number:           951,
					StatusLabel:      issues.StatusDone,
					WorkerReport:     true,
					ValidationStatus: "success",
					FilesSummary:     []string{"internal/controlplane/commands/review.go", "1/9"},
				}},
			},
			{
				Cycle: 4,
				FindingsBySeverity: map[string][]string{
					"HIGH": {"internal/controlplane/dispatch/worker.go: mutation boundary allows duplicate issue comments"},
				},
				FixIssues: []reviewHistoryFixIssue{{
					Number:           952,
					StatusLabel:      issues.StatusDone,
					WorkerReport:     true,
					ValidationStatus: "success",
					FilesSummary:     []string{"internal/controlplane/dispatch/worker.go", "Source: local-git"},
				}},
			},
			{
				Cycle: 5,
				FindingsBySeverity: map[string][]string{
					"HIGH": {
						"internal/controlplane/dispatch/worker.go: retry can duplicate workflow dispatches after post-call unknown state",
						"Diff Coverage",
					},
				},
				FixIssues: []reviewHistoryFixIssue{{Number: 953, StatusLabel: issues.StatusInProgress}},
			},
		},
		TrendCounts:          []int{14, 20, 20},
		CompletedFixIssues:   []int{951, 952},
		InProgressFixIssues:  []int{953},
		LatestFindingCount:   20,
		EarliestFindingCount: 14,
		Cluster: reviewConvergenceCluster{
			PackageClusters: []string{"internal/controlplane/commands", "internal/controlplane/dispatch", "Chunk 1/9"},
			RootCauseTerms:  []string{"durable", "idempotency", "mutation", "retry", "Diff Coverage"},
			Fingerprint:     "fp-match",
			Summary:         "packages: internal/controlplane/commands, internal/controlplane/dispatch; root causes: durable, idempotency, mutation, retry",
		},
	}
}

func reviewStrategyIssueBody(batchPR int) string {
	return issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{Version: 1, Batch: 111, Type: "fix", BatchPR: batchPR, FixCycle: 6},
		Task:        "Fix the strategy.",
	})
}

func reviewStrategyIssue(number int, state string, labels []string, body string) *platform.Issue {
	return &platform.Issue{
		Number: number,
		State:  state,
		Title:  "Review strategy fix (cycle 6): internal/controlplane/dispatch",
		Labels: labels,
		Body:   body,
	}
}
