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
	assert.Equal(t, "head", dispatcher.requests[0].ExpectedHeadSHA)
}

func TestEnsureReviewFixIssueStartedOrFailedIdempotencyIsRepairable(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "started", status: mutationStatusStarted},
		{name: "failed", status: mutationStatusFailed},
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

			issueNumber, created, err := svc.EnsureReviewFixIssue(ctx, repo, result, finding)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "retry after reconciliation")
			assert.Zero(t, issueNumber)
			assert.False(t, created)
			assert.Empty(t, fake.issues.created)
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
