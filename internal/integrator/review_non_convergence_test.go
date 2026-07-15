package integrator

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
		{"internal dispatch", "internal/controlplane/dispatch/worker.go: bug", "internal/controlplane/dispatch"},
		{"internal commands", "`internal/controlplane/commands/review.go`: bug", "internal/controlplane/commands"},
		{"sibling package", "web/src/review/history.ts: bug", "web/src/review"},
		{"empty", "no path here", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, packageClusterFromFinding(tt.in))
		})
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

func TestBuildStrategyFixIssueTitle(t *testing.T) {
	tests := []struct {
		name    string
		cycle   int
		cluster reviewConvergenceCluster
		want    string
	}{
		{
			name:    "package cluster wins",
			cycle:   6,
			cluster: reviewConvergenceCluster{PackageClusters: []string{"internal/controlplane/dispatch"}, RootCauseTerms: []string{"idempotency"}},
			want:    "Review strategy fix (cycle 6): internal/controlplane/dispatch",
		},
		{
			name:    "root cause fallback",
			cycle:   7,
			cluster: reviewConvergenceCluster{RootCauseTerms: []string{"idempotency"}},
			want:    "Review strategy fix (cycle 7): idempotency",
		},
		{
			name:    "default fallback",
			cycle:   8,
			cluster: reviewConvergenceCluster{},
			want:    "Review strategy fix (cycle 8): non-converging review loop",
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
	assert.Contains(t, parsed.ImplementationDetails, "durable mutation/idempotency boundary")
	assert.Contains(t, parsed.ImplementationDetails, "shared state transitions")
	assert.Contains(t, parsed.ImplementationDetails, "repeated dispatch, retry, and unknown-state repair paths")
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
	assert.Contains(t, parsed.Context, "Dominant root-cause terms: idempotency, retry")
	assert.Contains(t, parsed.Context, "Rationale: finding trend is increasing or flat after completed fix cycles")

	fingerprint, ok := parseReviewNonConvergenceFingerprint(body)
	require.True(t, ok)
	assert.Equal(t, analysis.Cluster.Fingerprint, fingerprint)
	assert.Contains(t, body, `"version":1`)
	assert.Contains(t, body, `"batch_pr":849`)
	assert.NotContains(t, body, "internal/controlplane/dispatch/worker.go: durable mutation lacks idempotency")
}

func TestBuildReviewNonConvergencePRComment(t *testing.T) {
	comment := buildReviewNonConvergencePRComment(reviewStrategyAnalysisFixture(), 954)

	assert.True(t, strings.HasPrefix(comment, "⚠️ **Herd review is not converging**"))
	assert.Contains(t, comment, "Cycles analyzed: 3, 4, 5")
	assert.Contains(t, comment, "Finding count trend: 14, 20, 20")
	assert.Contains(t, comment, "Fix issues considered: #951, #952, #953")
	assert.Contains(t, comment, "Dominant package clusters: internal/controlplane/commands, internal/controlplane/dispatch")
	assert.Contains(t, comment, "Dominant root-cause terms: idempotency, retry")
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
		Decision:             reviewDecisionEscalateToArchitectureFix,
		Confidence:           0.86,
		Rationale:            "finding trend is increasing or flat after completed fix cycles",
		Cycles:               []reviewHistoryCycle{{Cycle: 3}, {Cycle: 4}, {Cycle: 5}},
		TrendCounts:          []int{14, 20, 20},
		CompletedFixIssues:   []int{951, 952},
		InProgressFixIssues:  []int{953},
		LatestFindingCount:   20,
		EarliestFindingCount: 14,
		Cluster: reviewConvergenceCluster{
			PackageClusters: []string{"internal/controlplane/commands", "internal/controlplane/dispatch"},
			RootCauseTerms:  []string{"idempotency", "retry"},
			Fingerprint:     "fp-match",
			Summary:         "packages: internal/controlplane/commands, internal/controlplane/dispatch; root causes: idempotency, retry",
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
