package review

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/store"
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

func TestDispatchReviewSetsPendingAndSuppressesDuplicateWithLock(t *testing.T) {
	locks := newFakeLockStore()
	dispatcher := &fakeReviewDispatcher{}
	statusGH := &fakeStatusGitHub{}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	svc := ReviewService{
		Status:     StatusService{GitHub: statusGH, Now: func() time.Time { return now }},
		Locks:      locks,
		Dispatcher: dispatcher,
		Now:        func() time.Time { return now },
	}
	req := DispatchReviewRequest{
		BatchNumber: 7,
		PRNumber:    42,
		BatchBranch: "herd/batch/7-demo",
		HeadSHA:     "head",
		RunnerLabel: "self-hosted",
	}
	repo := testRepo(true)
	repo.DefaultBranch = "main"

	first, err := svc.DispatchReview(context.Background(), repo, req)
	require.NoError(t, err)
	second, err := svc.DispatchReview(context.Background(), repo, req)
	require.NoError(t, err)

	assert.True(t, first.Locked)
	assert.True(t, first.Dispatched)
	assert.False(t, second.Locked)
	assert.False(t, second.Dispatched)
	require.Len(t, dispatcher.requests, 1)
	assert.Equal(t, cpdispatch.JobKindReview, dispatcher.requests[0].Kind)
	assert.Equal(t, "head", dispatcher.requests[0].ExpectedHeadSHA)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "pending", statusGH.statuses[0].status.State)
}

func TestSubmitReviewResultChangesRequestedWithFixesKeepsPendingAndDispatchesFixes(t *testing.T) {
	gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "head", URL: "https://github.test/pr/42"}}
	statusGH := &fakeStatusGitHub{}
	locks := newFakeLockStore()
	fixes := &fakeFixCoordinator{}
	repo := testRepo(true)
	repo.ReviewFixEnabled = true
	repo.ReviewMaxFixCycles = 3
	repo.ReviewFixSeverity = "medium"
	result := reviewResult(ResultStatusChangesRequested, "head")
	result.FixCycle = 1
	result.BatchBranch = "herd/batch/1-demo"
	result.Findings = []Finding{
		{Fingerprint: "high-1", Severity: "high", Description: "fix high"},
		{Fingerprint: "low-1", Severity: "low", Description: "skip low"},
	}
	svc := ReviewService{GitHub: gh, Status: StatusService{GitHub: statusGH}, Locks: locks, Fixes: fixes}

	err := svc.SubmitReviewResult(context.Background(), repo, result)

	require.NoError(t, err)
	assert.Empty(t, gh.reviews)
	require.Len(t, fixes.ensureCalls, 1)
	assert.Equal(t, "high-1", fixes.ensureCalls[0].Fingerprint)
	assert.Equal(t, []int{101}, fixes.dispatched)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "pending", statusGH.statuses[0].status.State)
}

func TestSubmitReviewResultMaxFixCyclesSetsFailureWithoutDispatch(t *testing.T) {
	gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "head", URL: "https://github.test/pr/42"}}
	statusGH := &fakeStatusGitHub{}
	fixes := &fakeFixCoordinator{}
	repo := testRepo(true)
	repo.ReviewFixEnabled = true
	repo.ReviewMaxFixCycles = 2
	result := reviewResult(ResultStatusChangesRequested, "head")
	result.FixCycle = 2
	result.Findings = []Finding{{Fingerprint: "high-1", Severity: "high", Description: "fix high"}}
	svc := ReviewService{GitHub: gh, Status: StatusService{GitHub: statusGH}, Fixes: fixes}

	err := svc.SubmitReviewResult(context.Background(), repo, result)

	require.NoError(t, err)
	assert.Empty(t, fixes.ensureCalls)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "failure", statusGH.statuses[0].status.State)
	assert.Contains(t, statusGH.statuses[0].status.Description, "maximum fix cycles")
}

type fakeReviewGitHub struct {
	pr        *platform.PullRequest
	reviewErr error
	reviews   []capturedReview
	comments  []string
}

type fakeLockStore struct {
	locks map[string]store.ReviewLock
}

func newFakeLockStore() *fakeLockStore {
	return &fakeLockStore{locks: map[string]store.ReviewLock{}}
}

func (s *fakeLockStore) AcquireReviewLock(_ context.Context, lock store.ReviewLock) (bool, error) {
	key := lockKey(lock.RepositoryID, lock.PRNumber, lock.HeadSHA)
	if active, ok := s.locks[key]; ok && active.ExpiresAt.After(lock.AcquiredAt) {
		return false, nil
	}
	s.locks[key] = lock
	return true, nil
}

func (s *fakeLockStore) ReleaseReviewLock(_ context.Context, repoID int64, prNumber int, headSHA string, holder string, _ time.Time) error {
	key := lockKey(repoID, prNumber, headSHA)
	active, ok := s.locks[key]
	if !ok || active.Holder != holder {
		return store.ErrNotFound
	}
	delete(s.locks, key)
	return nil
}

func lockKey(repoID int64, prNumber int, headSHA string) string {
	return fmt.Sprintf("%d/%d/%s", repoID, prNumber, headSHA)
}

type fakeReviewDispatcher struct {
	requests []cpdispatch.DispatchRequest
}

func (d *fakeReviewDispatcher) Dispatch(_ context.Context, req cpdispatch.DispatchRequest) (cpdispatch.DispatchResult, error) {
	d.requests = append(d.requests, req)
	return cpdispatch.DispatchResult{JobID: "job-1", URL: "https://github.test/actions", Created: true}, nil
}

type fakeFixCoordinator struct {
	ensureCalls []Finding
	dispatched  []int
}

func (f *fakeFixCoordinator) EnsureReviewFixIssue(_ context.Context, _ Repository, _ ReviewCompletedResult, finding Finding) (int, bool, error) {
	f.ensureCalls = append(f.ensureCalls, finding)
	return 100 + len(f.ensureCalls), true, nil
}

func (f *fakeFixCoordinator) DispatchReviewFixWorker(_ context.Context, _ Repository, _ ReviewCompletedResult, issueNumber int) (bool, error) {
	f.dispatched = append(f.dispatched, issueNumber)
	return true, nil
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
