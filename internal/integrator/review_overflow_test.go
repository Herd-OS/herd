package integrator

import (
	"context"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stripOverflowFraming removes the "_Part N of M..._" prefix and the
// "_Continued from issue body..._" header from an overflow comment so
// the remaining content can be concatenated with the truncated issue
// body to reproduce the original input.
func stripOverflowFraming(c string) string {
	if strings.HasPrefix(c, "_Part ") {
		if i := strings.Index(c, "_\n\n"); i >= 0 {
			c = c[i+len("_\n\n"):]
		}
	}
	const overflowMarker = "_Continued from issue body"
	if strings.HasPrefix(c, overflowMarker) {
		if end := strings.Index(c, "._\n\n"); end >= 0 {
			c = c[end+len("._\n\n"):]
		}
	}
	return c
}

func TestCreateFixIssue_PostsOverflowComment(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	// Build a synthetic ~100000-char finding description with newlines
	// so the truncation boundary search has clean breaks to land on.
	const lineFragment = "line of test content for overflow handling\n"
	var sb strings.Builder
	for sb.Len() < 100000 {
		sb.WriteString(lineFragment)
	}
	bigDescription := sb.String()

	var (
		createdCount  int
		capturedTitle string
		capturedBody  string
		nextNum       = 500
	)
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdCount++
			capturedTitle = title
			capturedBody = body
			n := nextNum
			nextNum++
			return &platform.Issue{Number: n, Title: title, Body: body}, nil
		},
	}

	wf := &mockWorkflowService{
		runs: map[int64]*platform.Run{
			100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows:  wf,
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: bigDescription},
			},
			Comments: []string{"big finding"},
		},
	}

	dir, g := initTestRepo(t)
	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Exactly one Issue was created.
	require.Equal(t, 1, createdCount, "expected exactly one fix issue to be created")
	require.NotEmpty(t, capturedTitle)

	// Created issue body length <= 65000 and contains the truncation marker.
	assert.LessOrEqual(t, len(capturedBody), 65000, "truncated body should fit GitHub limit")
	assert.Contains(t, capturedBody, "⚠️ Body truncated", "truncated body should carry marker")

	// At least one AddComment was made on that issue's number.
	createdIssueNum := nextNum - 1
	comments := issueSvc.comments[createdIssueNum]
	require.NotEmpty(t, comments, "expected overflow comment(s) on created fix issue")

	// Reconstruct original content: strip marker from body, strip framing from
	// comments, concatenate. The original description must appear inside the
	// reconstructed content.
	markerIdx := strings.Index(capturedBody, "⚠️ Body truncated")
	require.GreaterOrEqual(t, markerIdx, 0)
	// Strip back to before the marker's leading "\n\n---\n_" framing so we
	// don't leave structural noise in the body half.
	bodyHead := capturedBody[:markerIdx]
	if i := strings.LastIndex(bodyHead, "\n\n---\n_"); i >= 0 {
		bodyHead = bodyHead[:i]
	}

	var rebuilt strings.Builder
	rebuilt.WriteString(bodyHead)
	for _, c := range comments {
		rebuilt.WriteString(stripOverflowFraming(c))
	}
	assert.Contains(t, rebuilt.String(), bigDescription, "reconstructed body+comments should contain original description")
}

func TestCreateFixIssue_ShortBodyNoOverflowComment(t *testing.T) {
	issueSvc := newMockIssueService()
	issueSvc.getResult[42] = &platform.Issue{
		Number: 42, Title: "Test",
		Labels:    []string{issues.StatusDone},
		Milestone: &platform.Milestone{Number: 1, Title: "Batch"},
	}
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n"},
	}

	var (
		createdCount int
		nextNum      = 700
	)
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdCount++
			n := nextNum
			nextNum++
			return &platform.Issue{Number: n, Title: title, Body: body}, nil
		},
	}

	mock := &mockPlatform{
		issues: mockCreate,
		prs: &mockPRService{
			listResult: []*platform.PullRequest{{Number: 50, Title: "[herd] Batch"}},
		},
		workflows: &mockWorkflowService{
			runs: map[int64]*platform.Run{
				100: {ID: 100, Conclusion: "success", Inputs: map[string]string{"issue_number": "42"}},
			},
		},
		repo:       &mockRepoService{defaultBranch: "main", branchExists: map[string]bool{"herd/batch/1-batch": true}},
		milestones: &mockMilestoneService{},
	}

	ag := &mockReviewAgent{
		reviewResult: &agent.ReviewResult{
			Approved: false,
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "small finding that fits comfortably"},
			},
			Comments: []string{"small finding"},
		},
	}

	dir, g := initTestRepo(t)
	_, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{Review: true, ReviewMaxFixCycles: 3},
	}, ReviewParams{RunID: 100, RepoRoot: dir})
	require.NoError(t, err)
	require.Equal(t, 1, createdCount)

	createdIssueNum := nextNum - 1
	assert.Empty(t, issueSvc.comments[createdIssueNum], "no overflow comments expected for short body")
}
