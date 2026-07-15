package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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
			svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: newFakeReviewMutationStore()}

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
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: newFakeReviewMutationStore()}

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
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: newFakeReviewMutationStore()}

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
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: newFakeReviewMutationStore()}

	err := svc.SubmitReviewResult(context.Background(), testRepo(true), reviewResult(ResultStatusApproved, "head"))

	require.NoError(t, err)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "failure", statusGH.statuses[0].status.State)
	require.Len(t, gh.comments, 1)
	assert.Contains(t, gh.comments[0], "could not submit")
	assert.Contains(t, gh.comments[0], "secondary rate limit")
}

func TestSubmitReviewResultFailureCommentRedactsSensitiveError(t *testing.T) {
	gh := &fakeReviewGitHub{
		pr:        &platform.PullRequest{Number: 42, HeadSHA: "head"},
		reviewErr: errors.New("request failed with token github_pat_1234567890abcdef and ghp_1234567890abcdef"),
	}
	statusGH := &fakeStatusGitHub{}
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: newFakeReviewMutationStore()}

	err := svc.SubmitReviewResult(context.Background(), testRepo(true), reviewResult(ResultStatusApproved, "head"))

	require.NoError(t, err)
	require.Len(t, gh.comments, 1)
	assert.Contains(t, gh.comments[0], "[REDACTED]")
	assert.NotContains(t, gh.comments[0], "github_pat_1234567890abcdef")
	assert.NotContains(t, gh.comments[0], "ghp_1234567890abcdef")
}

func TestSubmitReviewResultRetryAfterStatusFailureDoesNotDuplicateReview(t *testing.T) {
	gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "head", URL: "https://github.test/pr/42"}}
	statusGH := &fakeStatusGitHub{errs: []error{errors.New("status down"), nil}}
	mutations := newFakeReviewMutationStore()
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: mutations}
	result := reviewResult(ResultStatusApproved, "head")

	firstErr := svc.SubmitReviewResult(context.Background(), testRepo(true), result)
	secondErr := svc.SubmitReviewResult(context.Background(), testRepo(true), result)

	require.Error(t, firstErr)
	require.Error(t, secondErr)
	assert.Contains(t, secondErr.Error(), "repair required")
	assert.Len(t, gh.reviews, 1)
	assert.Empty(t, statusGH.statuses)
	for _, record := range mutations.idem {
		if record.Scope == "review_submission" {
			assert.Equal(t, "completed", record.Status)
		}
	}
}

func TestSubmitPRReviewOnceStartedRecordDoesNotCreateReview(t *testing.T) {
	gh := &fakeReviewGitHub{}
	mutations := newFakeReviewMutationStore()
	svc := ReviewService{GitHub: gh, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")
	key := reviewSubmissionKey(repo, result, platform.ReviewApprove)
	mutations.idem[key] = store.IdempotencyKey{Key: key, Scope: "review_submission", Status: "started"}

	err := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in progress")
	assert.Empty(t, gh.reviews)
}

func TestSubmitPRReviewOnceRequiresMutationStore(t *testing.T) {
	gh := &fakeReviewGitHub{}
	svc := ReviewService{GitHub: gh}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")

	err := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "review submission mutation store is required")
	assert.Empty(t, gh.reviews)
}

func TestSubmitPRReviewOnceFailedRecordWithoutMutationRecordsAttemptBeforeReview(t *testing.T) {
	gh := &fakeReviewGitHub{}
	mutations := newFakeReviewMutationStore()
	svc := ReviewService{GitHub: gh, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")
	key := reviewSubmissionKey(repo, result, platform.ReviewApprove)
	mutations.idem[key] = store.IdempotencyKey{Key: key, Scope: "review_submission", Status: "failed"}

	err := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)

	require.NoError(t, err)
	assert.Len(t, gh.reviews, 1)
	attempt, err := mutations.GetGitHubMutationAttempt(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "completed", attempt.Status)
}

func TestSubmitPRReviewOnceFailedUnknownOutcomeDoesNotCreateDuplicateReview(t *testing.T) {
	gh := &fakeReviewGitHub{}
	mutations := newFakeReviewMutationStore()
	mutations.completeMutationErrs = []error{nil, errors.New("database down")}
	svc := ReviewService{GitHub: gh, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")
	key := reviewSubmissionKey(repo, result, platform.ReviewApprove)

	firstErr := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)
	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "complete review submission mutation attempt")
	require.Len(t, gh.reviews, 1)
	require.NoError(t, mutations.CompleteGitHubMutationAttempt(context.Background(), key, "failed", nil, "reconciled unknown outcome", time.Now().UTC()))
	require.NoError(t, mutations.FailIdempotencyKey(context.Background(), key, "reconciled unknown outcome"))

	secondErr := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)

	require.NoError(t, secondErr)
	assert.Len(t, gh.reviews, 1)
}

func TestSubmitPRReviewOnceRetryAfterMutationCompletionFailureRepairsStartedAttempt(t *testing.T) {
	gh := &fakeReviewGitHub{}
	mutations := newFakeReviewMutationStore()
	mutations.completeMutationErrs = []error{errors.New("database down"), nil}
	svc := ReviewService{GitHub: gh, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")
	key := reviewSubmissionKey(repo, result, platform.ReviewApprove)

	firstErr := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)
	secondErr := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)

	require.Error(t, firstErr)
	require.NoError(t, secondErr)
	assert.Len(t, gh.reviews, 1)
	record, err := mutations.GetIdempotencyKey(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "completed", record.Status)
	attempt, err := mutations.GetGitHubMutationAttempt(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "completed", attempt.Status)
}

func TestSubmitPRReviewOnceRetriesAfterMutationAttemptRecordFailure(t *testing.T) {
	gh := &fakeReviewGitHub{}
	mutations := newFakeReviewMutationStore()
	mutations.recordMutationErrs = []error{errors.New("database down"), nil}
	svc := ReviewService{GitHub: gh, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")
	key := reviewSubmissionKey(repo, result, platform.ReviewApprove)

	firstErr := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)
	secondErr := svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)

	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "record review submission mutation attempt")
	require.NoError(t, secondErr)
	assert.Len(t, gh.reviews, 1)
	record, err := mutations.GetIdempotencyKey(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "completed", record.Status)
	attempt, err := mutations.GetGitHubMutationAttempt(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "completed", attempt.Status)
}

func TestSubmitReviewResultStartedSubmissionReturnsRetryableError(t *testing.T) {
	gh := &fakeReviewGitHub{pr: &platform.PullRequest{Number: 42, HeadSHA: "head", URL: "https://github.test/pr/42"}}
	statusGH := &fakeStatusGitHub{}
	mutations := newFakeReviewMutationStore()
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")
	key := reviewSubmissionKey(repo, result, platform.ReviewApprove)
	mutations.idem[key] = store.IdempotencyKey{Key: key, Scope: "review_submission", Status: "started"}

	err := svc.SubmitReviewResult(context.Background(), repo, result)

	require.ErrorIs(t, err, ErrReviewSubmissionInProgress)
	assert.Empty(t, gh.reviews)
	assert.Empty(t, statusGH.statuses)
}

func TestSubmitPRReviewOnceConcurrentDuplicateCreatesOneReview(t *testing.T) {
	gh := &fakeReviewGitHub{}
	mutations := newFakeReviewMutationStore()
	svc := ReviewService{GitHub: gh, Mutations: mutations}
	repo := testRepo(true)
	result := reviewResult(ResultStatusApproved, "head")

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.submitPRReviewOnce(context.Background(), repo, result, platform.ReviewApprove)
		}()
	}
	wg.Wait()
	close(errs)

	var inProgress int
	for err := range errs {
		if err != nil {
			assert.Contains(t, err.Error(), "already in progress")
			inProgress++
		}
	}
	assert.LessOrEqual(t, inProgress, 1)
	assert.Len(t, gh.reviews, 1)
}

func TestDispatchReviewSetsPendingAndSuppressesDuplicateWithLock(t *testing.T) {
	locks := newFakeLockStore()
	dispatcher := &fakeReviewDispatcher{}
	statusGH := &fakeStatusGitHub{}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	svc := ReviewService{
		Status:     StatusService{Store: &fakeStatusStore{}, GitHub: statusGH, Now: func() time.Time { return now }},
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

func TestDispatchReviewDispatchFailureSurfacesFailureStatusError(t *testing.T) {
	locks := newFakeLockStore()
	dispatcher := &fakeReviewDispatcher{err: errors.New("dispatch down")}
	statusGH := &fakeStatusGitHub{err: errors.New("status down")}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	svc := ReviewService{
		Status:     StatusService{Store: &fakeStatusStore{}, GitHub: statusGH, Now: func() time.Time { return now }},
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

	_, err := svc.DispatchReview(context.Background(), testRepo(true), req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch review workflow failed and record failure status")
	assert.Empty(t, statusGH.statuses)
	assert.Empty(t, locks.locks)
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
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Locks: locks, Fixes: fixes, Mutations: newFakeReviewMutationStore()}

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
	svc := ReviewService{GitHub: gh, Status: StatusService{Store: &fakeStatusStore{}, GitHub: statusGH}, Fixes: fixes, Mutations: newFakeReviewMutationStore()}

	err := svc.SubmitReviewResult(context.Background(), repo, result)

	require.NoError(t, err)
	assert.Empty(t, fixes.ensureCalls)
	require.Len(t, statusGH.statuses, 1)
	assert.Equal(t, "failure", statusGH.statuses[0].status.State)
	assert.Contains(t, statusGH.statuses[0].status.Description, "maximum fix cycles")
}

type fakeReviewGitHub struct {
	mu        sync.Mutex
	pr        *platform.PullRequest
	reviewErr error
	reviews   []capturedReview
	comments  []string
}

type fakeReviewMutationStore struct {
	mu                   sync.Mutex
	idem                 map[string]store.IdempotencyKey
	mutations            map[string]store.GitHubMutationAttempt
	recordMutationErrs   []error
	completeMutationErrs []error
}

func newFakeReviewMutationStore() *fakeReviewMutationStore {
	return &fakeReviewMutationStore{
		idem:      map[string]store.IdempotencyKey{},
		mutations: map[string]store.GitHubMutationAttempt{},
	}
}

func (s *fakeReviewMutationStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idem[key.Key]; ok {
		return false, nil
	}
	s.idem[key.Key] = key
	return true, nil
}

func (s *fakeReviewMutationStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idem[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *fakeReviewMutationStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idem[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "completed"
	record.ResultRef = resultRef
	record.CompletedAt = &now
	s.idem[key] = record
	return nil
}

func (s *fakeReviewMutationStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idem[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "failed"
	record.ResultRef = errorMessage
	record.CompletedAt = &now
	s.idem[key] = record
	return nil
}

func (s *fakeReviewMutationStore) RecordGitHubMutationAttempt(_ context.Context, a store.GitHubMutationAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recordMutationErrs) > 0 {
		err := s.recordMutationErrs[0]
		s.recordMutationErrs = s.recordMutationErrs[1:]
		if err != nil {
			return err
		}
	}
	if _, ok := s.mutations[a.IdempotencyKey]; ok {
		return store.ErrAlreadyExists
	}
	s.mutations[a.IdempotencyKey] = a
	return nil
}

func (s *fakeReviewMutationStore) CompleteGitHubMutationAttempt(_ context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.completeMutationErrs) > 0 {
		err := s.completeMutationErrs[0]
		s.completeMutationErrs = s.completeMutationErrs[1:]
		if err != nil {
			return err
		}
	}
	attempt, ok := s.mutations[idempotencyKey]
	if !ok {
		return store.ErrNotFound
	}
	attempt.Status = status
	attempt.Response = response
	attempt.Error = errorMessage
	attempt.CompletedAt = &completedAt
	s.mutations[idempotencyKey] = attempt
	return nil
}

func (s *fakeReviewMutationStore) GetGitHubMutationAttempt(_ context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.mutations[idempotencyKey]
	if !ok {
		return store.GitHubMutationAttempt{}, store.ErrNotFound
	}
	return attempt, nil
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
	err      error
}

func (d *fakeReviewDispatcher) Dispatch(_ context.Context, req cpdispatch.DispatchRequest) (cpdispatch.DispatchResult, error) {
	d.requests = append(d.requests, req)
	if d.err != nil {
		return cpdispatch.DispatchResult{}, d.err
	}
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
	body     string
	event    platform.ReviewEvent
	commitID string
}

func (g *fakeReviewGitHub) GetPullRequest(_ context.Context, _ int64, _, _ string, _ int) (*platform.PullRequest, error) {
	return g.pr, nil
}

func (g *fakeReviewGitHub) CreateReviewForCommit(_ context.Context, _ int64, _, _ string, _ int, body string, event platform.ReviewEvent, commitID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reviewErr != nil {
		return g.reviewErr
	}
	g.reviews = append(g.reviews, capturedReview{body: strings.TrimSpace(body), event: event, commitID: commitID})
	return nil
}

func (g *fakeReviewGitHub) FindReviewForCommit(_ context.Context, _ int64, _, _ string, _ int, body string, event platform.ReviewEvent, commitID string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, review := range g.reviews {
		if review.body == strings.TrimSpace(body) && review.event == event && review.commitID == commitID {
			return true, nil
		}
	}
	return false, nil
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
