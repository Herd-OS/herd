package mutationguard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunMutationBoundaryConverges(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*fakeBoundaryStore)
		mutateErr  error
		wantCalls  int
		wantErr    string
		wantResult string
		wantStatus string
		repair     func() (string, bool, error)
		wantReplay bool
	}{
		{
			name:       "records starts and completes one visible mutation",
			wantCalls:  1,
			wantResult: "issue:1",
			wantStatus: mutations.PhaseCompleted,
		},
		{
			name: "replays completed mutation without repeating call",
			setup: func(st *fakeBoundaryStore) {
				st.attempts["k"] = store.GitHubMutationAttempt{IdempotencyKey: "k", Status: mutations.PhaseCompleted, Response: json.RawMessage(`{"result_ref":"issue:1"}`)}
			},
			wantResult: "issue:1",
			wantStatus: mutations.PhaseCompleted,
			wantReplay: true,
		},
		{
			name: "retries failed pre-call mutation",
			setup: func(st *fakeBoundaryStore) {
				st.attempts["k"] = store.GitHubMutationAttempt{IdempotencyKey: "k", Status: mutations.PhaseFailedPreCall}
			},
			wantCalls:  1,
			wantResult: "issue:1",
			wantStatus: mutations.PhaseCompleted,
		},
		{
			name: "does not repeat unknown post-call mutation",
			setup: func(st *fakeBoundaryStore) {
				st.attempts["k"] = store.GitHubMutationAttempt{
					IdempotencyKey: "k",
					Status:         mutations.PhaseCallStarted,
					Error:          "github response was lost",
				}
			},
			wantErr:    "repair required before retry",
			wantStatus: mutations.PhaseRepairRequired,
		},
		{
			name: "repairs unknown post-call mutation without repeating call",
			setup: func(st *fakeBoundaryStore) {
				st.attempts["k"] = store.GitHubMutationAttempt{IdempotencyKey: "k", Status: mutations.PhaseRepairRequired}
			},
			repair: func() (string, bool, error) {
				return "issue:7", true, nil
			},
			wantResult: "issue:7",
			wantStatus: mutations.PhaseCompleted,
			wantReplay: true,
		},
		{
			name:       "failed API call becomes post-call unknown",
			mutateErr:  errors.New("github timeout"),
			wantCalls:  1,
			wantErr:    "github timeout",
			wantStatus: mutations.PhasePostCallUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeBoundaryStore()
			if tt.setup != nil {
				tt.setup(st)
			}
			calls := 0

			result, err := Run(context.Background(), st, RunRequest{
				Key:          "k",
				RepositoryID: 1,
				MutationType: "issue_create",
				Mutate: func() (string, error) {
					calls++
					return "issue:1", tt.mutateErr
				},
				Repair: tt.repair,
				Now:    fixedBoundaryTime,
			})

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, calls)
			assert.Equal(t, tt.wantResult, result.ResultRef)
			assert.Equal(t, tt.wantReplay, result.Replayed)
			assert.Equal(t, tt.wantStatus, st.attempts["k"].Status)
			if tt.wantStatus == mutations.PhaseRepairRequired {
				assert.Equal(t, "github response was lost", st.attempts["k"].Error)
				assert.Equal(t, mutations.PhaseRepairRequired, st.idem["k"].Status)
				assert.Equal(t, "github response was lost", st.idem["k"].ResultRef)
			}
		})
	}
}

func TestRunMutationBoundaryPreflightFailureIsRetryable(t *testing.T) {
	st := newFakeBoundaryStore()

	_, err := Run(context.Background(), st, RunRequest{
		Key:          "k",
		RepositoryID: 1,
		MutationType: "workflow_dispatch",
		Preflight: func() error {
			return errors.New("marshal inputs")
		},
		Mutate: func() (string, error) {
			return "unused", nil
		},
		Now: fixedBoundaryTime,
	})
	require.Error(t, err)
	var preCallErr mutations.PreCallError
	require.ErrorAs(t, err, &preCallErr)
	assert.Equal(t, mutations.PhaseFailedPreCall, st.attempts["k"].Status)
	require.Equal(t, mutations.PhaseFailedPreCall, st.idem["k"].Status)
	assert.Equal(t, "marshal inputs", st.idem["k"].ResultRef)

	calls := 0
	result, err := Run(context.Background(), st, RunRequest{
		Key:          "k",
		RepositoryID: 1,
		MutationType: "workflow_dispatch",
		Mutate: func() (string, error) {
			calls++
			return "job:1", nil
		},
		Now: fixedBoundaryTime,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "job:1", result.ResultRef)
	assert.Equal(t, mutations.PhaseCompleted, st.attempts["k"].Status)
}

func TestRunMutationBoundaryStartCommitWithErrorFailsClosed(t *testing.T) {
	st := newFakeBoundaryStore()
	st.startErr = errors.New("database response lost")
	st.persistStartOnError = true
	calls := 0

	_, err := Run(context.Background(), st, RunRequest{
		Key:          "k",
		RepositoryID: 1,
		MutationType: "workflow_dispatch",
		Mutate: func() (string, error) {
			calls++
			return "job:1", nil
		},
		Now: fixedBoundaryTime,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "repair required")
	assert.Equal(t, 0, calls)
	assert.Equal(t, mutations.PhaseRepairRequired, st.attempts["k"].Status)

	_, err = Run(context.Background(), st, RunRequest{
		Key:          "k",
		RepositoryID: 1,
		MutationType: "workflow_dispatch",
		Mutate: func() (string, error) {
			calls++
			return "job:1", nil
		},
		Now: fixedBoundaryTime,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "repair required")
	assert.Equal(t, 0, calls)
	assert.Equal(t, mutations.PhaseRepairRequired, st.attempts["k"].Status)
}

func fixedBoundaryTime() time.Time {
	return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
}

type fakeBoundaryStore struct {
	attempts            map[string]store.GitHubMutationAttempt
	idem                map[string]store.IdempotencyKey
	startErr            error
	persistStartOnError bool
}

func newFakeBoundaryStore() *fakeBoundaryStore {
	return &fakeBoundaryStore{
		attempts: map[string]store.GitHubMutationAttempt{},
		idem:     map[string]store.IdempotencyKey{"k": {Key: "k", Status: mutations.PhaseIntentRecorded}},
	}
}

func (s *fakeBoundaryStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	record := s.idem[key]
	record.Status = mutations.PhaseCompleted
	record.ResultRef = resultRef
	s.idem[key] = record
	return nil
}

func (s *fakeBoundaryStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	record := s.idem[key]
	record.Status, record.ResultRef = fakeIdempotencyFailureStatus(errorMessage)
	s.idem[key] = record
	return nil
}

func (s *fakeBoundaryStore) RecordGitHubMutationAttempt(_ context.Context, a store.GitHubMutationAttempt) error {
	if _, ok := s.attempts[a.IdempotencyKey]; ok {
		return store.ErrAlreadyExists
	}
	s.attempts[a.IdempotencyKey] = a
	return nil
}

func (s *fakeBoundaryStore) GetGitHubMutationAttempt(_ context.Context, key string) (store.GitHubMutationAttempt, error) {
	attempt, ok := s.attempts[key]
	if !ok {
		return store.GitHubMutationAttempt{}, store.ErrNotFound
	}
	return attempt, nil
}

func (s *fakeBoundaryStore) CompleteGitHubMutationAttempt(_ context.Context, key string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	attempt, ok := s.attempts[key]
	if !ok {
		return store.ErrNotFound
	}
	attempt.Status = status
	attempt.Response = response
	attempt.Error = errorMessage
	attempt.CompletedAt = &completedAt
	s.attempts[key] = attempt
	return nil
}

func (s *fakeBoundaryStore) TryStartGitHubMutationAttempt(_ context.Context, key string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error) {
	attempt, ok := s.attempts[key]
	if !ok {
		return store.GitHubMutationStartResult{}, store.ErrNotFound
	}
	for _, status := range allowedStatuses {
		if attempt.Status == status {
			if s.startErr != nil {
				if s.persistStartOnError {
					attempt.Status = mutations.PhaseCallStarted
					attempt.CompletedAt = &completedAt
					s.attempts[key] = attempt
				}
				return store.GitHubMutationStartResult{}, s.startErr
			}
			attempt.Status = mutations.PhaseCallStarted
			attempt.CompletedAt = &completedAt
			s.attempts[key] = attempt
			return store.GitHubMutationStartResult{Started: true, Attempt: attempt}, nil
		}
	}
	return store.GitHubMutationStartResult{Attempt: attempt}, nil
}

func fakeIdempotencyFailureStatus(errorMessage string) (string, string) {
	for _, phase := range []string{mutations.PhaseFailedPreCall, mutations.PhasePostCallUnknown, mutations.PhaseRepairRequired} {
		prefix := phase + ":"
		if errorMessage == phase {
			return phase, ""
		}
		if strings.HasPrefix(errorMessage, prefix) {
			return phase, strings.TrimSpace(strings.TrimPrefix(errorMessage, prefix))
		}
	}
	return mutations.LegacyFailed, errorMessage
}
