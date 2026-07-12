package review

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetHerdReviewStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		repo       Repository
		state      ReviewStatusState
		wantStatus bool
		wantErr    string
	}{
		{name: "pending on dispatch", repo: testRepo(true), state: ReviewStatusPending, wantStatus: true},
		{name: "success", repo: testRepo(true), state: ReviewStatusSuccess, wantStatus: true},
		{name: "failure", repo: testRepo(true), state: ReviewStatusFailure, wantStatus: true},
		{name: "review disabled", repo: testRepo(false), state: ReviewStatusPending},
		{name: "invalid state", repo: testRepo(true), state: "neutral", wantErr: "unsupported Herd Review status state"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStatusStore{}
			gh := &fakeStatusGitHub{}
			svc := StatusService{Store: st, GitHub: gh, Now: func() time.Time { return now }}

			err := svc.SetHerdReviewStatus(ctx, tt.repo, 42, "head-sha", tt.state, "desc", "https://example.test/run")

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if !tt.wantStatus {
				assert.Empty(t, gh.statuses)
				assert.Empty(t, st.states)
				return
			}
			require.Len(t, gh.statuses, 1)
			assert.Equal(t, int64(99), gh.statuses[0].installationID)
			assert.Equal(t, "octo", gh.statuses[0].owner)
			assert.Equal(t, "widgets", gh.statuses[0].repo)
			assert.Equal(t, "head-sha", gh.statuses[0].sha)
			assert.Equal(t, string(tt.state), gh.statuses[0].status.State)
			assert.Equal(t, HerdReviewContext, gh.statuses[0].status.Context)
			assert.Equal(t, "https://example.test/run", gh.statuses[0].status.TargetURL)
			require.Len(t, st.states, 1)
			assert.Equal(t, string(tt.state), st.states[0].Status)
			assert.Equal(t, now, st.states[0].UpdatedAt)
			assert.Contains(t, string(st.states[0].Metadata), "herd_review_status:7:42:head-sha:Herd Review")
		})
	}
}

func TestSetHerdReviewStatusAllowsNewHeadPendingAfterSuccess(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	require.NoError(t, svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "old-head", ReviewStatusSuccess, "approved", ""))
	require.NoError(t, svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "new-head", ReviewStatusPending, "new commit", ""))

	require.Len(t, gh.statuses, 2)
	assert.Equal(t, "old-head", gh.statuses[0].sha)
	assert.Equal(t, "success", gh.statuses[0].status.State)
	assert.Equal(t, "new-head", gh.statuses[1].sha)
	assert.Equal(t, "pending", gh.statuses[1].status.State)
}

func TestSetHerdReviewStatusRequiresIdempotencyStore(t *testing.T) {
	ctx := context.Background()
	st := &stateOnlyStatusStore{}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	err := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "review status idempotency store is required")
	assert.Empty(t, gh.statuses)
	assert.Empty(t, st.states)
}

func TestSetHerdReviewStatusRequiresMutationStore(t *testing.T) {
	ctx := context.Background()
	st := &idempotencyOnlyStatusStore{idem: map[string]store.IdempotencyKey{}}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	err := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "review status mutation store is required")
	assert.Empty(t, gh.statuses)
	assert.Empty(t, st.states)
}

func TestSetHerdReviewStatusRetryAfterStateFailureDoesNotDuplicateGitHubStatus(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{errs: []error{errors.New("database down"), nil}}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	firstErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")
	secondErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "record Herd Review state")
	require.NoError(t, secondErr)
	assert.Len(t, gh.statuses, 1)
	require.Len(t, st.states, 1)
	assert.Equal(t, "success", st.states[0].Status)
}

func TestSetHerdReviewStatusRetryAfterCompleteIdempotencyFailureRepairsFromMutation(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{completeErrs: []error{errors.New("database down"), nil}}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	firstErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")
	secondErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "complete Herd Review status idempotency")
	require.NoError(t, secondErr)
	assert.Len(t, gh.statuses, 1)
	require.Len(t, st.states, 1)
	assert.Equal(t, "success", st.states[0].Status)
}

func TestSetHerdReviewStatusReturnsMutationCompletionFailure(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{mutationCompleteErrs: []error{errors.New("database down")}}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	err := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete Herd Review status mutation attempt")
	assert.Len(t, gh.statuses, 1)
	assert.Empty(t, st.states)
	require.Len(t, st.mutationAttempts, 1)
	assert.Equal(t, "started", st.mutationAttempts[0].Status)
}

func TestSetHerdReviewStatusRetryAfterMutationCompletionFailureRepairsStartedAttempt(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{mutationCompleteErrs: []error{errors.New("database down"), nil}}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	firstErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")
	secondErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, firstErr)
	require.NoError(t, secondErr)
	assert.Len(t, gh.statuses, 1)
	require.Len(t, st.mutationAttempts, 1)
	assert.Equal(t, "completed", st.mutationAttempts[0].Status)
	require.Len(t, st.states, 1)
	assert.Equal(t, "success", st.states[0].Status)
}

func TestSetHerdReviewStatusRetryAfterGitHubFailureReusesFailedMutationAttempt(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{}
	gh := &fakeStatusGitHub{errs: []error{errors.New("github down"), nil}}
	svc := StatusService{Store: st, GitHub: gh}

	firstErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")
	secondErr := svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "head-sha", ReviewStatusSuccess, "approved", "https://example.test/run")

	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "github down")
	require.NoError(t, secondErr)
	assert.Len(t, gh.statuses, 1)
	require.Len(t, st.mutationAttempts, 1)
	assert.Equal(t, "completed", st.mutationAttempts[0].Status)
	require.Len(t, st.states, 1)
	assert.Equal(t, "success", st.states[0].Status)
}

type fakeStatusStore struct {
	states               []store.ReviewState
	errs                 []error
	completeErrs         []error
	mutationCompleteErrs []error
	idem                 map[string]store.IdempotencyKey
	mutationAttempts     []store.GitHubMutationAttempt
}

type stateOnlyStatusStore struct {
	states []store.ReviewState
}

type idempotencyOnlyStatusStore struct {
	states []store.ReviewState
	idem   map[string]store.IdempotencyKey
}

func (s *stateOnlyStatusStore) SetReviewState(_ context.Context, state store.ReviewState) error {
	s.states = append(s.states, state)
	return nil
}

func (s *idempotencyOnlyStatusStore) SetReviewState(_ context.Context, state store.ReviewState) error {
	s.states = append(s.states, state)
	return nil
}

func (s *idempotencyOnlyStatusStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	if _, ok := s.idem[key.Key]; ok {
		return false, nil
	}
	s.idem[key.Key] = key
	return true, nil
}

func (s *idempotencyOnlyStatusStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	record, ok := s.idem[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *idempotencyOnlyStatusStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	record, ok := s.idem[key]
	if !ok {
		return store.ErrNotFound
	}
	record.Status = "completed"
	record.ResultRef = resultRef
	s.idem[key] = record
	return nil
}

func (s *idempotencyOnlyStatusStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	record, ok := s.idem[key]
	if !ok {
		return store.ErrNotFound
	}
	record.Status = "failed"
	record.ResultRef = errorMessage
	s.idem[key] = record
	return nil
}

func (s *fakeStatusStore) SetReviewState(_ context.Context, state store.ReviewState) error {
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return err
		}
	}
	s.states = append(s.states, state)
	return nil
}

func (s *fakeStatusStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	if s.idem == nil {
		s.idem = map[string]store.IdempotencyKey{}
	}
	if _, ok := s.idem[key.Key]; ok {
		return false, nil
	}
	s.idem[key.Key] = key
	return true, nil
}

func (s *fakeStatusStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	record, ok := s.idem[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *fakeStatusStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	if len(s.completeErrs) > 0 {
		err := s.completeErrs[0]
		s.completeErrs = s.completeErrs[1:]
		if err != nil {
			return err
		}
	}
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

func (s *fakeStatusStore) RecordGitHubMutationAttempt(_ context.Context, attempt store.GitHubMutationAttempt) error {
	for _, existing := range s.mutationAttempts {
		if existing.IdempotencyKey == attempt.IdempotencyKey {
			return store.ErrAlreadyExists
		}
	}
	s.mutationAttempts = append(s.mutationAttempts, attempt)
	return nil
}

func (s *fakeStatusStore) CompleteGitHubMutationAttempt(_ context.Context, key string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	if len(s.mutationCompleteErrs) > 0 {
		err := s.mutationCompleteErrs[0]
		s.mutationCompleteErrs = s.mutationCompleteErrs[1:]
		if err != nil {
			return err
		}
	}
	for i := range s.mutationAttempts {
		if s.mutationAttempts[i].IdempotencyKey == key {
			s.mutationAttempts[i].Status = status
			s.mutationAttempts[i].Response = response
			s.mutationAttempts[i].Error = errorMessage
			s.mutationAttempts[i].CompletedAt = &completedAt
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *fakeStatusStore) GetGitHubMutationAttempt(_ context.Context, key string) (store.GitHubMutationAttempt, error) {
	for _, attempt := range s.mutationAttempts {
		if attempt.IdempotencyKey == key {
			return attempt, nil
		}
	}
	return store.GitHubMutationAttempt{}, store.ErrNotFound
}

func (s *fakeStatusStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
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

type fakeStatusGitHub struct {
	statuses []capturedStatus
	err      error
	errs     []error
}

type capturedStatus struct {
	installationID int64
	owner          string
	repo           string
	sha            string
	status         platform.CommitStatus
}

func (g *fakeStatusGitHub) CreateCommitStatus(_ context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) error {
	if len(g.errs) > 0 {
		err := g.errs[0]
		g.errs = g.errs[1:]
		if err != nil {
			return err
		}
	}
	if g.err != nil {
		return g.err
	}
	g.statuses = append(g.statuses, capturedStatus{installationID: installationID, owner: owner, repo: repo, sha: sha, status: status})
	return nil
}

func (g *fakeStatusGitHub) FindCommitStatus(_ context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) (bool, error) {
	for _, existing := range g.statuses {
		if existing.installationID == installationID &&
			existing.owner == owner &&
			existing.repo == repo &&
			existing.sha == sha &&
			existing.status == status {
			return true, nil
		}
	}
	return false, nil
}

func testRepo(enabled bool) Repository {
	return Repository{ID: 7, InstallationID: 99, Owner: "octo", Name: "widgets", ReviewEnabled: enabled}
}
