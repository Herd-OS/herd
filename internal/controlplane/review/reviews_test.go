package review

import (
	"context"
	"errors"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmitReviewResultSetsStatusesAndReviews(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		wantEvent platform.ReviewEvent
		wantState string
	}{
		{name: "approved latest SHA", status: ResultStatusApproved, wantEvent: platform.ReviewApprove, wantState: "success"},
		{name: "blocking findings", status: ResultStatusChangesRequested, wantEvent: platform.ReviewRequestChanges, wantState: "failure"},
		{name: "unparseable", status: ResultStatusUnparseable, wantState: "failure"},
		{name: "timeout", status: ResultStatusTimedOut, wantState: "failure"},
		{name: "max cycles", status: ResultStatusMaxCyclesHit, wantState: "failure"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "head", URL: "https://github.test/pr/42"}}
			statusGH := &fakeStatusGitHub{}
			svc := ReviewService{GitHub: gh, Status: StatusService{GitHub: statusGH}}

			err := svc.SubmitReviewResult(context.Background(), testRepo(true), reviewResult(tt.status, "head"))

			require.NoError(t, err)
			if tt.wantEvent != "" {
				require.Len(t, gh.reviews, 1)
				assert.Equal(t, tt.wantEvent, gh.reviews[0].event)
				assert.Equal(t, "head", gh.reviews[0].commitID)
			} else {
				assert.Empty(t, gh.reviews)
			}
			require.Len(t, statusGH.statuses, 1)
			assert.Equal(t, tt.wantState, statusGH.statuses[0].status.State)
			assert.Equal(t, "https://example.test/run", statusGH.statuses[0].status.TargetURL)
		})
	}
}

func TestSubmitReviewResultStaleApprovedCallbackCannotMarkNewerHeadSuccess(t *testing.T) {
	gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "new-head", URL: "https://github.test/pr/42"}}
	statusGH := &fakeStatusGitHub{}
	svc := ReviewService{GitHub: gh, Status: StatusService{GitHub: statusGH}}

	err := svc.SubmitReviewResult(context.Background(), testRepo(true), reviewResult(ResultStatusApproved, "old-head"))

	require.NoError(t, err)
	assert.Empty(t, gh.reviews)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "new-head", statusGH.statuses[0].sha)
	assert.Equal(t, "pending", statusGH.statuses[0].status.State)
}

func TestSubmitReviewResultDisabledReviewDoesNothing(t *testing.T) {
	gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "head"}}
	statusGH := &fakeStatusGitHub{}
	svc := ReviewService{GitHub: gh, Status: StatusService{GitHub: statusGH}}

	err := svc.SubmitReviewResult(context.Background(), testRepo(false), reviewResult(ResultStatusApproved, "head"))

	require.NoError(t, err)
	assert.Empty(t, gh.reviews)
	assert.Empty(t, statusGH.statuses)
}

func TestSubmitReviewResultFailureStillSetsStatusAndComment(t *testing.T) {
	gh := &fakeReviewGitHub{
		pr:        &platform.PullRequest{Number: 42, HeadSHA: "head"},
		reviewErr: errors.New("secondary rate limit"),
	}
	statusGH := &fakeStatusGitHub{}
	svc := ReviewService{GitHub: gh, Status: StatusService{GitHub: statusGH}}

	err := svc.SubmitReviewResult(context.Background(), testRepo(true), reviewResult(ResultStatusApproved, "head"))

	require.NoError(t, err)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "failure", statusGH.statuses[0].status.State)
	require.Len(t, gh.comments, 1)
	assert.Contains(t, gh.comments[0], "could not submit")
	assert.Contains(t, gh.comments[0], "secondary rate limit")
}

type fakeReviewGitHub struct {
	pr        *platform.PullRequest
	reviewErr error
	reviews   []capturedReview
	comments  []string
}

type capturedReview struct {
	event    platform.ReviewEvent
	commitID string
}

func (g *fakeReviewGitHub) GetPullRequest(_ context.Context, _ int64, _, _ string, _ int) (*platform.PullRequest, error) {
	return g.pr, nil
}

func (g *fakeReviewGitHub) CreateReviewForCommit(_ context.Context, _ int64, _, _ string, _ int, _ string, event platform.ReviewEvent, commitID string) error {
	if g.reviewErr != nil {
		return g.reviewErr
	}
	g.reviews = append(g.reviews, capturedReview{event: event, commitID: commitID})
	return nil
}

func (g *fakeReviewGitHub) AddPullRequestComment(_ context.Context, _ int64, _, _ string, _ int, body string) error {
	g.comments = append(g.comments, body)
	return nil
}

func reviewResult(status, headSHA string) ReviewCompletedResult {
	return ReviewCompletedResult{
		Repository:  "octo/widgets",
		JobID:       "job-1",
		BatchNumber: 1,
		PRNumber:    42,
		HeadSHA:     headSHA,
		Status:      status,
		Summary:     "summary",
		TargetURL:   "https://example.test/run",
	}
}
