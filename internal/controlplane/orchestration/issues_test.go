package orchestration

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureTaskIssue_CreateUpdateAndDeduplicate(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		req        TaskIssueRequest
		wantCreate bool
		wantUpdate bool
	}{
		{
			name: "create issue",
			req: TaskIssueRequest{
				BatchNumber: 9,
				Title:       "Task",
				Body:        "---\nherd:\n  version: 1\n  batch: 9\n---\n\n## Task\nDo it\n",
				Labels:      []string{issues.StatusReady},
				Milestone:   9,
			},
			wantCreate: true,
		},
		{
			name: "update issue",
			req: TaskIssueRequest{
				BatchNumber: 9,
				IssueNumber: 3,
				Title:       "Updated task",
				Body:        "---\nherd:\n  version: 1\n  batch: 9\n---\n\n## Task\nDo it better\n",
				Labels:      []string{issues.StatusBlocked},
				Milestone:   9,
			},
			wantUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			fake.issues.next = 2
			fake.issues.items[3] = &platform.Issue{Number: 3, Title: "Old"}
			svc := newTestService(fake, newFakeStore(), nil)

			got, err := svc.EnsureTaskIssue(ctx, tt.req)
			require.NoError(t, err)
			require.NotNil(t, got)

			again, err := svc.EnsureTaskIssue(ctx, tt.req)
			require.NoError(t, err)
			assert.Equal(t, got.Number, again.Number)
			if tt.wantCreate {
				assert.Len(t, fake.issues.created, 1)
				assert.Equal(t, "Task", fake.issues.created[0].Title)
			}
			if tt.wantUpdate {
				assert.Empty(t, fake.issues.created)
				assert.Equal(t, "Updated task", fake.issues.items[3].Title)
				assert.Contains(t, fake.issues.added[3], issues.StatusBlocked)
			}
		})
	}
}

func TestEnsureTaskIssue_RejectsMissingMilestone(t *testing.T) {
	svc := newTestService(newFakePlatform(), newFakeStore(), nil)

	_, err := svc.EnsureTaskIssue(context.Background(), TaskIssueRequest{Title: "Task"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "milestone")
}

func TestEnsureTaskIssue_CreateAllowsSameTitleWithDifferentBody(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	svc := newTestService(fake, newFakeStore(), nil)

	first, err := svc.EnsureTaskIssue(ctx, TaskIssueRequest{
		BatchNumber: 9,
		Title:       "Task",
		Body:        "first body",
		Milestone:   9,
	})
	require.NoError(t, err)
	second, err := svc.EnsureTaskIssue(ctx, TaskIssueRequest{
		BatchNumber: 9,
		Title:       "Task",
		Body:        "second body",
		Milestone:   9,
	})
	require.NoError(t, err)

	assert.NotEqual(t, first.Number, second.Number)
	assert.Len(t, fake.issues.created, 2)
}

func TestEnsureTaskIssue_DeduplicatesReorderedLabels(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.issues.items[3] = &platform.Issue{Number: 3, Title: "Old"}
	svc := newTestService(fake, newFakeStore(), nil)
	create := TaskIssueRequest{
		BatchNumber: 9,
		Title:       "Task",
		Body:        "body",
		Labels:      []string{issues.StatusReady, issues.TypeFeature},
		Milestone:   9,
	}

	first, err := svc.EnsureTaskIssue(ctx, create)
	require.NoError(t, err)
	create.Labels = []string{issues.TypeFeature, "", " " + issues.StatusReady + " "}
	second, err := svc.EnsureTaskIssue(ctx, create)
	require.NoError(t, err)

	assert.Equal(t, first.Number, second.Number)
	assert.Len(t, fake.issues.created, 1)

	update := TaskIssueRequest{
		BatchNumber: 9,
		IssueNumber: 3,
		Title:       "Updated task",
		Body:        "updated body",
		Labels:      []string{issues.StatusBlocked, issues.TypeFeature},
		Milestone:   9,
	}
	_, err = svc.EnsureTaskIssue(ctx, update)
	require.NoError(t, err)
	update.Labels = []string{issues.TypeFeature, issues.StatusBlocked}
	_, err = svc.EnsureTaskIssue(ctx, update)
	require.NoError(t, err)

	assert.Equal(t, []string{issues.StatusBlocked, issues.TypeFeature}, fake.issues.added[3])
}

func TestEnsureTaskIssue_UpdateAllowsChangedContent(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.issues.items[3] = &platform.Issue{Number: 3, Title: "Old", Body: "old"}
	svc := newTestService(fake, newFakeStore(), nil)

	_, err := svc.EnsureTaskIssue(ctx, TaskIssueRequest{
		BatchNumber: 9,
		IssueNumber: 3,
		Title:       "Task",
		Body:        "first body",
		Milestone:   9,
	})
	require.NoError(t, err)
	_, err = svc.EnsureTaskIssue(ctx, TaskIssueRequest{
		BatchNumber: 9,
		IssueNumber: 3,
		Title:       "Task",
		Body:        "second body",
		Labels:      []string{issues.StatusBlocked},
		Milestone:   9,
	})
	require.NoError(t, err)

	assert.Equal(t, "second body", fake.issues.items[3].Body)
	assert.Contains(t, fake.issues.added[3], issues.StatusBlocked)
}

func TestEnsureTaskIssueUpdateReconcilesStatusLabels(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.issues.items[3] = &platform.Issue{
		Number: 3,
		Title:  "Old",
		Labels: []string{issues.StatusReady, issues.TypeFeature},
	}
	st := newFakeStore()
	svc := newTestService(fake, st, nil)

	_, err := svc.EnsureTaskIssue(ctx, TaskIssueRequest{
		BatchNumber: 9,
		IssueNumber: 3,
		Title:       "Task",
		Body:        "body",
		Labels:      []string{issues.StatusBlocked, issues.TypeFeature},
		Milestone:   9,
	})

	require.NoError(t, err)
	assert.Contains(t, fake.issues.removed[3], issues.StatusReady)
	assert.Contains(t, fake.issues.added[3], issues.StatusBlocked)
	var durableRemove bool
	for _, key := range st.keys {
		if key.Scope == "issue_label_remove" && key.Status == mutationStatusCompleted {
			durableRemove = true
		}
	}
	assert.True(t, durableRemove)
}

func TestEnsureReviewFixIssueAndDispatchAreIdempotentByFingerprint(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	dispatcher := &fakeDispatcher{}
	svc := newTestService(fake, st, dispatcher)
	repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	result := review.ReviewCompletedResult{
		BatchNumber: 9,
		PRNumber:    42,
		BatchBranch: "herd/batch/9-demo",
		HeadSHA:     "head",
		FixCycle:    1,
	}
	finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}

	firstIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.NoError(t, err)
	assert.True(t, created)
	firstDispatch, err := svc.DispatchReviewFixWorker(ctx, repo, result, firstIssue)
	require.NoError(t, err)
	secondIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.NoError(t, err)
	assert.False(t, created)
	secondDispatch, err := svc.DispatchReviewFixWorker(ctx, repo, result, secondIssue)
	require.NoError(t, err)

	assert.Equal(t, firstIssue, secondIssue)
	assert.True(t, firstDispatch)
	assert.False(t, secondDispatch)
	assert.Len(t, fake.issues.created, 1)
	assert.Len(t, dispatcher.requests, 1)
	assert.Equal(t, int64(123), dispatcher.requests[0].RepoID)
	assert.Equal(t, int64(456), dispatcher.requests[0].InstallationID)
	assert.Equal(t, "owner", dispatcher.requests[0].Owner)
	assert.Equal(t, "repo", dispatcher.requests[0].Repo)
	assert.Equal(t, "head", dispatcher.requests[0].ExpectedHeadSHA)
}

func TestEnsureReviewFixIssueRejectsMismatchedRepository(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		repo review.Repository
	}{
		{name: "id", repo: review.Repository{ID: 999, InstallationID: 456, Owner: "owner", Name: "repo"}},
		{name: "installation", repo: review.Repository{ID: 123, InstallationID: 999, Owner: "owner", Name: "repo"}},
		{name: "owner", repo: review.Repository{ID: 123, InstallationID: 456, Owner: "other", Name: "repo"}},
		{name: "name", repo: review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			st := newFakeStore()
			svc := newTestService(fake, st, nil)
			result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, HeadSHA: "head"}
			finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}

			issueNumber, created, err := svc.EnsureReviewFixIssue(ctx, tt.repo, result, finding)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not match service repository")
			assert.Zero(t, issueNumber)
			assert.False(t, created)
			assert.Empty(t, fake.issues.created)
			assert.Empty(t, st.keys)
		})
	}
}

func TestDispatchReviewFixWorkerRejectsMismatchedRepository(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	dispatcher := &fakeDispatcher{}
	svc := newTestService(fake, st, dispatcher)
	repo := review.Repository{ID: 999, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	result := review.ReviewCompletedResult{
		BatchNumber: 9,
		PRNumber:    42,
		BatchBranch: "herd/batch/9-demo",
		HeadSHA:     "head",
	}

	dispatched, err := svc.DispatchReviewFixWorker(ctx, repo, result, 10)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match service repository")
	assert.False(t, dispatched)
	assert.Empty(t, dispatcher.requests)
}

func TestEnsureReviewFixIssuePreCallIdempotencyIsRetryable(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, nil)
	repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "head", FixCycle: 1}
	finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
	key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "review_fix_issue_create", Status: mutationStatusIntentRecorded}

	issueNumber, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)

	require.NoError(t, err)
	assert.Equal(t, 1, issueNumber)
	assert.True(t, created)
	assert.Len(t, fake.issues.created, 1)
}

func TestEnsureReviewFixIssuePreCallMutationFailureRetriesAndDispatchesOnce(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	dispatcher := &fakeDispatcher{}
	svc := newTestService(fake, st, dispatcher)
	repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "head", FixCycle: 1}
	finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
	reviewFixKey := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
	st.recordMutationErrs = map[string][]error{reviewFixKey: {assert.AnError, nil}}

	firstIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "record mutation attempt")
	assert.Zero(t, firstIssue)
	assert.False(t, created)
	assert.Empty(t, fake.issues.created)
	assert.Equal(t, mutationStatusFailedPreCall, st.keys[reviewFixKey].Status)

	secondIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.NoError(t, err)
	dispatched, err := svc.DispatchReviewFixWorker(ctx, repo, result, secondIssue)
	require.NoError(t, err)

	assert.True(t, created)
	assert.True(t, dispatched)
	assert.Equal(t, 1, secondIssue)
	assert.Len(t, fake.issues.created, 1)
	assert.Len(t, dispatcher.requests, 1)
	assert.Equal(t, mutationStatusCompleted, st.keys[reviewFixKey].Status)
	assert.Equal(t, "issue:1", st.keys[reviewFixKey].ResultRef)
	assert.Equal(t, mutationStatusCompleted, st.mutations[reviewFixKey].Status)
}

func TestEnsureReviewFixIssueCreatedIntentWithoutMutationAttemptCreatesOnce(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, nil)
	repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "head", FixCycle: 1}
	finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
	key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "review_fix_issue_create", Status: mutationStatusIntentRecorded}

	firstIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.NoError(t, err)
	secondIssue, createdAgain, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.NoError(t, err)

	assert.True(t, created)
	assert.False(t, createdAgain)
	assert.Equal(t, firstIssue, secondIssue)
	assert.Len(t, fake.issues.created, 1)
	assert.Equal(t, mutationStatusCompleted, st.keys[key].Status)
	assert.Equal(t, "issue:1", st.keys[key].ResultRef)
}

func TestEnsureReviewFixIssuePostCallUnknownRequiresRepair(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "call started", status: mutationStatusCallStarted},
		{name: "repair required", status: mutationStatusRepairRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fake := newFakePlatform()
			st := newFakeStore()
			svc := newTestService(fake, st, nil)
			repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
			result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "head", FixCycle: 1}
			finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
			key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
			st.keys[key] = store.IdempotencyKey{Key: key, Scope: "review_fix_issue_create", Status: tt.status}
			st.mutations[key] = store.GitHubMutationAttempt{IdempotencyKey: key, MutationType: "review_fix_issue_create", Status: tt.status}

			issueNumber, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "retry after reconciliation")
			assert.Zero(t, issueNumber)
			assert.False(t, created)
			assert.Empty(t, fake.issues.created)
		})
	}
}

func TestEnsureReviewFixIssuePostCallUnknownRepairsByMarkerWithoutDuplicate(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "call started", status: mutationStatusCallStarted},
		{name: "repair required", status: mutationStatusRepairRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fake := newFakePlatform()
			st := newFakeStore()
			svc := newTestService(fake, st, nil)
			repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
			result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "head", FixCycle: 1}
			finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
			key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
			st.keys[key] = store.IdempotencyKey{Key: key, Scope: "review_fix_issue_create", Status: tt.status}
			st.mutations[key] = store.GitHubMutationAttempt{IdempotencyKey: key, MutationType: "review_fix_issue_create", Status: tt.status}
			fake.issues.listResult = []*platform.Issue{
				{Number: 12, Title: "Review fix: fp-1", Body: "same fingerprint\n\n<!-- " + reviewFixIssueCreateMarker(key) + " -->"},
			}

			issueNumber, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)

			require.NoError(t, err)
			assert.Equal(t, 12, issueNumber)
			assert.False(t, created)
			assert.Empty(t, fake.issues.created)
			assert.Equal(t, mutationStatusCompleted, st.keys[key].Status)
			assert.Equal(t, "issue:12", st.keys[key].ResultRef)
			assert.Equal(t, mutationStatusCompleted, st.mutations[key].Status)
		})
	}
}

func TestEnsureReviewFixIssueRecoversAfterOuterCompletionFailure(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, nil)
	repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	result := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "head", FixCycle: 1}
	finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
	key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
	st.completeErrs = map[string][]error{key: {assert.AnError, nil}}

	firstIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete idempotency key")
	assert.Zero(t, firstIssue)
	assert.False(t, created)

	secondIssue, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, 1, secondIssue)
	assert.Len(t, fake.issues.created, 1)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "issue:1", st.keys[key].ResultRef)
}

func TestEnsureReviewFixIssueRecoveryRequiresMatchingOperationMarker(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, nil)
	repo := review.Repository{ID: 123, InstallationID: 456, Owner: "owner", Name: "repo", DefaultBranch: "main"}
	current := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 42, BatchBranch: "herd/batch/9-demo", HeadSHA: "current-head", FixCycle: 1}
	stale := review.ReviewCompletedResult{BatchNumber: 9, PRNumber: 41, BatchBranch: "herd/batch/9-demo", HeadSHA: "stale-head", FixCycle: 1}
	finding := review.Finding{Fingerprint: "fp-1", Severity: "high", Description: "fix it"}
	currentKey := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", current.PRNumber, "head", current.HeadSHA, "finding", finding.Fingerprint)
	staleKey := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", stale.PRNumber, "head", stale.HeadSHA, "finding", finding.Fingerprint)
	st.keys[currentKey] = store.IdempotencyKey{Key: currentKey, Scope: "review_fix_issue_create", Status: mutationStatusCallStarted}
	title := "Review fix: " + finding.Fingerprint
	fake.issues.listResult = []*platform.Issue{
		{Number: 11, Title: title, Body: "same fingerprint\n\n<!-- " + reviewFixIssueCreateMarker(staleKey) + " -->"},
		{Number: 12, Title: title, Body: "same fingerprint\n\n<!-- " + reviewFixIssueCreateMarker(currentKey) + " -->"},
	}

	issueNumber, created, err := svc.EnsureReviewFixIssue(ctx, repo, current, finding)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, 12, issueNumber)
	assert.Empty(t, fake.issues.created)
	assert.Equal(t, "completed", st.keys[currentKey].Status)
	assert.Equal(t, "issue:12", st.keys[currentKey].ResultRef)
}
