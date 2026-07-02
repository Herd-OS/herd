package integrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustReviewResultMarker(t *testing.T, marker reviewResultMarker) string {
	t.Helper()
	body, err := buildReviewResultMarker(marker)
	require.NoError(t, err)
	return body
}

func TestParseReviewResultMarker(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	valid := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusApproved, 2, 0, now)
	validBody := mustReviewResultMarker(t, valid)

	tests := []struct {
		name       string
		body       string
		want       bool
		wantStatus string
	}{
		{name: "valid marker", body: validBody, want: true, wantStatus: reviewResultStatusApproved},
		{name: "valid changes requested marker", body: mustReviewResultMarker(t, newReviewResultMarker(50, 1, "head-sha", reviewResultStatusChangesRequested, 2, 3, now)), want: true, wantStatus: reviewResultStatusChangesRequested},
		{name: "valid max cycles marker", body: mustReviewResultMarker(t, newReviewResultMarker(50, 1, "head-sha", reviewResultStatusMaxCyclesHit, 3, 4, now)), want: true, wantStatus: reviewResultStatusMaxCyclesHit},
		{name: "malformed JSON", body: reviewResultMarkerPrefix + `{"version":` + reviewResultMarkerSuffix, want: false},
		{name: "missing suffix", body: reviewResultMarkerPrefix + `{}`, want: false},
		{name: "wrong version", body: mustReviewResultMarker(t, reviewResultMarker{Version: 2, PRNumber: 50, BatchNumber: 1, HeadSHA: "head-sha", Status: reviewResultStatusApproved, CreatedAt: now}), want: false},
		{name: "zero PR number", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, BatchNumber: 1, HeadSHA: "head-sha", Status: reviewResultStatusApproved, CreatedAt: now}), want: false},
		{name: "zero batch number", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, PRNumber: 50, HeadSHA: "head-sha", Status: reviewResultStatusApproved, CreatedAt: now}), want: false},
		{name: "empty head SHA", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, PRNumber: 50, BatchNumber: 1, Status: reviewResultStatusApproved, CreatedAt: now}), want: false},
		{name: "empty status", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, PRNumber: 50, BatchNumber: 1, HeadSHA: "head-sha", CreatedAt: now}), want: false},
		{name: "unknown status", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, PRNumber: 50, BatchNumber: 1, HeadSHA: "head-sha", Status: "approved_with_notes", CreatedAt: now}), want: false},
		{name: "whitespace polluted status", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, PRNumber: 50, BatchNumber: 1, HeadSHA: "head-sha", Status: reviewResultStatusApproved + " ", CreatedAt: now}), want: false},
		{name: "zero created_at", body: mustReviewResultMarker(t, reviewResultMarker{Version: 1, PRNumber: 50, BatchNumber: 1, HeadSHA: "head-sha", Status: reviewResultStatusApproved}), want: false},
		{name: "marker embedded after visible comment text", body: "Visible text\n\n" + validBody, want: true, wantStatus: reviewResultStatusApproved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseReviewResultMarker(tt.body)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Equal(t, valid.PRNumber, got.PRNumber)
				assert.Equal(t, valid.BatchNumber, got.BatchNumber)
				assert.Equal(t, valid.HeadSHA, got.HeadSHA)
				assert.Equal(t, tt.wantStatus, got.Status)
			}
		})
	}
}

func TestLatestReviewResultMarker(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	oldMatch := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusApproved, 1, 0, base)
	newMatch := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusChangesRequested, 2, 3, base.Add(time.Hour))

	comments := []*platform.Comment{
		{AuthorLogin: "github-actions[bot]", Body: reviewResultMarkerPrefix + `{"version":` + reviewResultMarkerSuffix},
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, newReviewResultMarker(51, 1, "head-sha", reviewResultStatusChangesRequested, 1, 1, base.Add(2*time.Hour)))},
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, newReviewResultMarker(50, 2, "head-sha", reviewResultStatusChangesRequested, 1, 1, base.Add(3*time.Hour)))},
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, newReviewResultMarker(50, 1, "other-sha", reviewResultStatusChangesRequested, 1, 1, base.Add(4*time.Hour)))},
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, oldMatch)},
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, newMatch)},
	}

	got, ok := latestReviewResultMarker(comments, 50, 1, "head-sha")

	require.True(t, ok)
	assert.Equal(t, reviewResultStatusChangesRequested, got.Status)
	assert.Equal(t, 3, got.FindingsCount)
	assert.Equal(t, newMatch.CreatedAt, got.CreatedAt)
}

func TestLatestReviewResultMarker_IgnoresNewerInvalidStatus(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	oldValid := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusApproved, 1, 0, base)
	newInvalid := reviewResultMarker{
		Version:       1,
		PRNumber:      50,
		BatchNumber:   1,
		HeadSHA:       "head-sha",
		Status:        "approved ",
		Cycle:         2,
		FindingsCount: 0,
		CreatedAt:     base.Add(time.Hour),
	}

	comments := []*platform.Comment{
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, oldValid)},
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, newInvalid)},
	}

	got, ok := latestReviewResultMarker(comments, 50, 1, "head-sha")

	require.True(t, ok)
	assert.Equal(t, reviewResultStatusApproved, got.Status)
	assert.Equal(t, oldValid.CreatedAt, got.CreatedAt)
}

func TestLatestReviewResultMarker_IgnoresUntrustedHumanComment(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	humanMarker := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusApproved, 1, 0, base)
	botMarker := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusChangesRequested, 2, 1, base.Add(-time.Hour))

	comments := []*platform.Comment{
		{AuthorLogin: "github-actions[bot]", Body: mustReviewResultMarker(t, botMarker)},
		{AuthorLogin: "alice", AuthorAssociation: "MEMBER", Body: mustReviewResultMarker(t, humanMarker)},
	}

	got, ok := latestReviewResultMarker(comments, 50, 1, "head-sha")

	require.True(t, ok)
	assert.Equal(t, reviewResultStatusChangesRequested, got.Status)
	assert.Equal(t, botMarker.CreatedAt, got.CreatedAt)
}

func TestLatestReviewResultMarker_TrustsBotComment(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	marker := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusApproved, 1, 0, base)

	got, ok := latestReviewResultMarker([]*platform.Comment{
		{AuthorLogin: "herd-os[bot]", Body: mustReviewResultMarker(t, marker)},
	}, 50, 1, "head-sha")

	require.True(t, ok)
	assert.Equal(t, reviewResultStatusApproved, got.Status)
	assert.Equal(t, marker.CreatedAt, got.CreatedAt)
}

func TestIsTrustedReviewResultMarkerComment(t *testing.T) {
	tests := []struct {
		name    string
		comment *platform.Comment
		want    bool
	}{
		{name: "nil comment", comment: nil, want: false},
		{name: "empty author", comment: &platform.Comment{}, want: false},
		{name: "human member", comment: &platform.Comment{AuthorLogin: "alice", AuthorAssociation: "MEMBER"}, want: false},
		{name: "trusted bot", comment: &platform.Comment{AuthorLogin: "github-actions[bot]", AuthorAssociation: "NONE"}, want: true},
		{name: "herd bot", comment: &platform.Comment{AuthorLogin: "herd-os[bot]", AuthorAssociation: "MEMBER"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTrustedReviewResultMarkerComment(tt.comment))
		})
	}
}

func TestReview_ApprovedCommentContainsReviewResultMarker(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	prSvc := &mockCapturingPRService{mockPRService: &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
	}}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "head-approved"},
	}
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{
		reviewResult: &agent.ReviewResult{Approved: true, Summary: "LGTM"},
	}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.True(t, result.Approved)
	require.NotEmpty(t, prSvc.comments)
	marker, ok := parseReviewResultMarker(prSvc.comments[0])
	require.True(t, ok)
	assert.Contains(t, prSvc.comments[0], reviewResultMarkerPrefix)
	assert.Equal(t, reviewResultStatusApproved, marker.Status)
	assert.Equal(t, "head-approved", marker.HeadSHA)
	assert.Equal(t, 50, marker.PRNumber)
	assert.Equal(t, 1, marker.BatchNumber)
	assert.Equal(t, 0, marker.FindingsCount)
}

func TestReview_DispatchedFindingsCommentContainsChangesRequestedMarker(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			return &platform.Issue{Number: 100, Title: title, Body: body}, nil
		},
	}
	prSvc := &mockCapturingPRService{mockPRService: &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
	}}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "head-findings"},
	}
	mock := newReviewLockTestPlatform(mockCreate)
	mock.prs = prSvc
	mock.repo = repoSvc

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Bug found"},
			},
		},
	}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
		Workers:    config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.False(t, result.Approved)
	require.NotEmpty(t, prSvc.comments)
	findingsComment := ""
	for _, comment := range prSvc.comments {
		if strings.HasPrefix(comment, "🔍") {
			findingsComment = comment
			break
		}
	}
	require.NotEmpty(t, findingsComment)
	marker, ok := parseReviewResultMarker(findingsComment)
	require.True(t, ok)
	assert.Equal(t, reviewResultStatusChangesRequested, marker.Status)
	assert.Equal(t, "head-findings", marker.HeadSHA)
	assert.Equal(t, 1, marker.FindingsCount)
	assert.Equal(t, 1, marker.Cycle)
}

func TestReview_MaxCycleCommentContainsReviewResultMarker(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
		{Number: 60, Labels: []string{issues.StatusDone}, Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nFix it\n"},
	}
	prSvc := &mockCapturingPRService{mockPRService: &mockPRService{
		getResult: map[int]*platform.PullRequest{
			50: {Number: 50, Title: "[herd] Batch", Head: "herd/batch/1-batch", Base: "main"},
		},
	}}
	repoSvc := &mockRepoService{
		defaultBranch: "main",
		branchExists:  map[string]bool{"herd/batch/1-batch": true},
		branchSHAs:    map[string]string{"herd/batch/1-batch": "head-max-cycle"},
	}
	mock := newReviewLockTestPlatform(issueSvc)
	mock.prs = prSvc
	mock.repo = repoSvc
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
		{Number: 60, Labels: []string{issues.StatusDone}, Body: "---\nherd:\n  version: 1\n  batch: 1\n  type: fix\n  fix_cycle: 3\n---\n\n## Task\nFix it\n"},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Still broken"},
			},
		},
	}, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.True(t, result.MaxCyclesHit)
	require.Len(t, prSvc.comments, 1)
	marker, ok := parseReviewResultMarker(prSvc.comments[0])
	require.True(t, ok)
	assert.Equal(t, reviewResultStatusMaxCyclesHit, marker.Status)
	assert.Equal(t, "head-max-cycle", marker.HeadSHA)
	assert.Equal(t, 1, marker.FindingsCount)
	assert.Equal(t, 3, marker.Cycle)
}

func TestAppendReviewResultMarkerSpacing(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	marker := newReviewResultMarker(50, 1, "head-sha", reviewResultStatusApproved, 0, 0, now)

	withText, err := appendReviewResultMarker("Visible text\n\n", marker)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(withText, "Visible text\n\n"+reviewResultMarkerPrefix))

	withoutText, err := appendReviewResultMarker(" \n\t", marker)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(withoutText, reviewResultMarkerPrefix))
}
