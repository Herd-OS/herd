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
