package reconciler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunOnceRepairsRecoverableWork(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := seedReconcilerStore(t, now)
	state := &fakeState{
		prs: map[int]platform.PullRequest{
			10: {Number: 10, State: "open", HeadSHA: "review-sha"},
			11: {Number: 11, State: "open", HeadSHA: "new-sha"},
		},
		statuses: map[string]platform.CommitStatus{},
	}
	commands := &fakeCommandRequeuer{}
	r := &Reconciler{
		Store:    st,
		State:    state,
		Commands: commands,
		Now:      func() time.Time { return now },
		Config: Config{
			JobTimeout:       time.Hour,
			CommandTimeout:   time.Minute,
			ReviewStaleAfter: time.Minute,
			CallbackTimeout:  time.Minute,
			Limit:            50,
		},
	}

	report, err := r.RunOnce(ctx)

	require.NoError(t, err)
	counts := report.CountsByClassification()
	assert.Equal(t, 2, counts[ClassificationFailedSurfaced])
	assert.Equal(t, 3, counts[ClassificationSafeToRetry])
	assert.Equal(t, 1, counts[ClassificationComplete])
	assert.Equal(t, 1, counts[ClassificationStaleAbandoned])

	failedJob, err := st.GetJob(ctx, "job-timeout")
	require.NoError(t, err)
	assert.Equal(t, "failed", failedJob.Status)
	assert.Contains(t, string(failedJob.Metadata), "job timed out before callback")

	completedJob, err := st.GetJob(ctx, "job-complete")
	require.NoError(t, err)
	assert.Equal(t, "dispatching", completedJob.Status)

	require.Len(t, commands.items, 1)
	assert.Equal(t, int64(101), commands.items[0].Command.CommentID)

	reviewState, err := st.GetReviewState(ctx, 1, 10, "review-sha")
	require.NoError(t, err)
	assert.Equal(t, "pending", reviewState.Status)
	assert.Equal(t, "pending", state.repairs[0].State)
	assert.Equal(t, review.HerdReviewContext, state.repairs[0].Context)

	staleReview, err := st.GetReviewState(ctx, 1, 11, "old-sha")
	require.NoError(t, err)
	assert.Equal(t, "abandoned", staleReview.Status)

	attempts, err := st.ListStartedGitHubMutationAttempts(ctx, now, 10)
	require.NoError(t, err)
	assert.Empty(t, attempts)
	mutationKey, err := st.GetIdempotencyKey(ctx, "patch_apply:stuck")
	require.NoError(t, err)
	assert.Equal(t, "failed", mutationKey.Status)
}

func TestRunOnceDoesNotRepeatRecoveredSideEffects(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := seedReconcilerStore(t, now)
	state := &fakeState{
		prs: map[int]platform.PullRequest{
			10: {Number: 10, State: "open", HeadSHA: "review-sha"},
			11: {Number: 11, State: "open", HeadSHA: "new-sha"},
		},
		statuses: map[string]platform.CommitStatus{},
	}
	commands := &fakeCommandRequeuer{}
	r := &Reconciler{
		Store:    st,
		State:    state,
		Commands: commands,
		Now:      func() time.Time { return now },
		Config: Config{
			JobTimeout:       time.Hour,
			CommandTimeout:   time.Minute,
			ReviewStaleAfter: time.Minute,
			CallbackTimeout:  time.Minute,
			Limit:            50,
		},
	}

	_, err := r.RunOnce(ctx)
	require.NoError(t, err)
	state.statuses["review-sha"] = state.repairs[0]
	_, err = r.RunOnce(ctx)

	require.NoError(t, err)
	assert.Len(t, commands.items, 1)
	assert.Len(t, state.repairs, 1)
}

func TestDuplicateDispatchIsNotRepeated(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := store.NewMemoryStore()
	repo := seedRepo(t, st, ctx)
	created, err := st.RecordCommand(ctx, store.CommandRecord{
		RepositoryID: repo.ID,
		CommentID:    200,
		CommandKey:   "review",
		CommandName:  "review",
		Actor:        "octo",
		Status:       "acknowledged",
		CreatedAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, created)
	created, err = st.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       "repo:1:comment:200:command:review",
		Scope:     "issue_comment_command",
		Status:    "completed",
		ResultRef: "dispatch:job-1",
		CreatedAt: now.Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, created)
	commands := &fakeCommandRequeuer{}
	r := &Reconciler{Store: st, Commands: commands, Now: func() time.Time { return now }, Config: Config{CommandTimeout: time.Minute}}

	report, err := r.RunOnce(ctx)

	require.NoError(t, err)
	assert.Empty(t, commands.items)
	assert.Equal(t, 1, report.CountsByClassification()[ClassificationComplete])
}

func seedReconcilerStore(t *testing.T, now time.Time) *store.MemoryStore {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore()
	repo := seedRepo(t, st, ctx)
	require.NoError(t, st.CreateJob(ctx, store.Job{JobID: "job-timeout", RepositoryID: repo.ID, InstallationID: repo.InstallationID, PRNumber: 1, HeadSHA: "sha", Status: "dispatching", UpdatedAt: now.Add(-2 * time.Hour)}))
	require.NoError(t, st.CreateJob(ctx, store.Job{JobID: "job-complete", RepositoryID: repo.ID, InstallationID: repo.InstallationID, PRNumber: 2, HeadSHA: "sha2", Status: "dispatching", UpdatedAt: now.Add(-2 * time.Hour)}))
	_, err := st.RecordJobResult(ctx, store.JobResult{JobID: "job-complete", IdempotencyKey: "result-1", Status: "success"})
	require.NoError(t, err)

	_, err = st.RecordCommand(ctx, store.CommandRecord{RepositoryID: repo.ID, CommentID: 101, CommandKey: "review", CommandName: "review", Actor: "octo", Status: "acknowledged", CreatedAt: now.Add(-time.Hour)})
	require.NoError(t, err)
	_, err = st.AcquireIdempotencyKey(ctx, store.IdempotencyKey{Key: "repo:1:comment:101:command:review", Scope: "issue_comment_command", Status: "started", CreatedAt: now.Add(-time.Hour)})
	require.NoError(t, err)

	require.NoError(t, st.SetReviewState(ctx, store.ReviewState{RepositoryID: repo.ID, PRNumber: 10, HeadSHA: "review-sha", Status: "pending", Metadata: json.RawMessage(`{"target_url":"https://runs/1"}`), UpdatedAt: now.Add(-time.Hour)}))
	require.NoError(t, st.SetReviewState(ctx, store.ReviewState{RepositoryID: repo.ID, PRNumber: 11, HeadSHA: "old-sha", Status: "pending", UpdatedAt: now.Add(-time.Hour)}))
	_, err = st.AcquireIdempotencyKey(ctx, store.IdempotencyKey{Key: "patch_apply:stuck", Scope: "patch_apply", Status: "started", CreatedAt: now.Add(-time.Hour)})
	require.NoError(t, err)
	require.NoError(t, st.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{IdempotencyKey: "patch_apply:stuck", MutationType: "patch_apply", Status: "started", CreatedAt: now.Add(-time.Hour)}))
	return st
}

func seedRepo(t *testing.T, st *store.MemoryStore, ctx context.Context) store.Repository {
	t.Helper()
	require.NoError(t, st.UpsertInstallation(ctx, store.Installation{ID: 99, AccountLogin: "octo", AccountID: 1, TargetType: "Organization"}))
	repo, err := st.UpsertRepository(ctx, store.Repository{GitHubID: 123, InstallationID: 99, Owner: "octo", Name: "herd", DefaultBranch: "main"})
	require.NoError(t, err)
	return repo
}

type fakeState struct {
	prs      map[int]platform.PullRequest
	statuses map[string]platform.CommitStatus
	repairs  []platform.CommitStatus
}

func (s *fakeState) GetPullRequest(_ context.Context, _ store.Repository, prNumber int) (platform.PullRequest, error) {
	return s.prs[prNumber], nil
}

func (s *fakeState) GetHerdReviewStatus(_ context.Context, _ store.Repository, headSHA string) (platform.CommitStatus, bool, error) {
	status, ok := s.statuses[headSHA]
	return status, ok, nil
}

func (s *fakeState) EnsureHerdReviewStatus(_ context.Context, _ store.Repository, _ int, _ string, status platform.CommitStatus) error {
	s.repairs = append(s.repairs, status)
	return nil
}

type fakeCommandRequeuer struct {
	items []store.ReconcileCommand
}

func (q *fakeCommandRequeuer) RequeueCommand(_ context.Context, item store.ReconcileCommand) error {
	q.items = append(q.items, item)
	return nil
}
