package orchestration

import (
	"context"
	"fmt"
	"testing"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenBatchPR_IdempotentByRepoAndBatch(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	svc := newTestService(fake, newFakeStore(), nil)
	req := OpenBatchPRRequest{
		BatchNumber: 5,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/5-demo",
		Base:        "main",
	}

	first, err := svc.OpenBatchPR(ctx, req)
	require.NoError(t, err)
	second, err := svc.OpenBatchPR(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, first.Number, second.Number)
	assert.Equal(t, 1, fake.prs.next)
}

func TestOpenBatchPR_UpdatesExistingHeadInsteadOfDuplicating(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[42] = &platform.PullRequest{
		Number: 42,
		Title:  "[herd] Old",
		Body:   "old",
		State:  "open",
		Head:   "herd/batch/5-demo",
		Base:   "main",
	}
	svc := newTestService(fake, newFakeStore(), nil)

	pr, err := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 5,
		Title:       "[herd] New",
		Body:        "new",
		Head:        "herd/batch/5-demo",
		Base:        "main",
	})

	require.NoError(t, err)
	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "[herd] New", fake.prs.items[42].Title)
	assert.Nil(t, fake.prs.created)
}

func TestApplyBranchOperation_IdempotencyAndHeadGuard(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		setup   func(*fakePlatform)
		req     BranchOperationRequest
		wantErr string
	}{
		{
			name: "create once",
			req: BranchOperationRequest{
				OperationKind: "create",
				BranchName:    "herd/worker/1-task",
				FromSHA:       "base",
			},
		},
		{
			name: "delete requires expected head",
			setup: func(p *fakePlatform) {
				p.repo.branches["herd/worker/1-task"] = "actual"
			},
			req: BranchOperationRequest{
				OperationKind:   "delete",
				BranchName:      "herd/worker/1-task",
				ExpectedHeadSHA: "expected",
			},
			wantErr: "head mismatch",
		},
		{
			name: "update with expected head",
			setup: func(p *fakePlatform) {
				p.repo.branches["herd/worker/1-task"] = "old"
			},
			req: BranchOperationRequest{
				OperationKind:   "update",
				BranchName:      "herd/worker/1-task",
				ExpectedHeadSHA: "old",
				NewSHA:          "new",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			if tt.setup != nil {
				tt.setup(fake)
			}
			svc := newTestService(fake, newFakeStore(), nil)

			err := svc.ApplyBranchOperation(ctx, tt.req)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, svc.ApplyBranchOperation(ctx, tt.req))
			if tt.req.OperationKind == "update" {
				assert.Equal(t, "new", fake.repo.branches[tt.req.BranchName])
				assert.Len(t, fake.repo.updated, 1)
			}
		})
	}
}

func TestMergePR_RequiresExpectedHeadAndSuccessfulStatus(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		sha     string
		status  string
		wantErr string
	}{
		{name: "merges matching successful head", sha: "head", status: "success"},
		{name: "rejects stale head", sha: "stale", status: "success", wantErr: "head mismatch"},
		{name: "rejects failing CI", sha: "head", status: "failure", wantErr: "CI status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			fake.prs.items[8] = &platform.PullRequest{Number: 8, Title: "[herd] Demo", State: "open", Head: "herd/batch/8-demo"}
			fake.repo.branches["herd/batch/8-demo"] = tt.sha
			fake.checks.status = tt.status
			svc := newTestService(fake, newFakeStore(), nil)

			result, err := svc.MergePR(ctx, MergePRRequest{PRNumber: 8, ExpectedHeadSHA: "head", RequireCI: true})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.True(t, result.Merged)
			assert.Equal(t, 8, fake.prs.merged)
		})
	}
}

func TestCleanupClosedBatchPR_ClosesIssuesMilestoneAndDeletesBranch(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[9] = &platform.PullRequest{Number: 9, Title: "[herd] Demo", State: "closed", Head: "herd/batch/9-demo"}
	fake.issues.listResult = []*platform.Issue{
		{Number: 1, Labels: []string{issues.StatusReady}},
		{Number: 2, Labels: []string{issues.StatusDone}},
	}
	fake.issues.items[1] = fake.issues.listResult[0]
	fake.issues.items[2] = fake.issues.listResult[1]
	fake.milestones.items[9] = &platform.Milestone{Number: 9, Title: "Demo"}
	fake.repo.branches["herd/batch/9-demo"] = "head"
	svc := newTestService(fake, newFakeStore(), nil)

	err := svc.CleanupClosedBatchPR(ctx, 9, false)

	require.NoError(t, err)
	assert.Equal(t, "closed", fake.issues.items[1].State)
	assert.Equal(t, "closed", fake.issues.items[2].State)
	assert.Contains(t, fake.issues.added[1], issues.StatusCancelled)
	assert.Equal(t, []int{9}, fake.milestones.closed)
	assert.Contains(t, fake.repo.deleted, "herd/batch/9-demo")
}

func TestCleanupClosedBatchPR_ReturnsIssueCleanupErrorAndDeletesBranch(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[9] = &platform.PullRequest{Number: 9, Title: "[herd] Demo", State: "closed", Head: "herd/batch/9-demo"}
	fake.issues.listResult = []*platform.Issue{{Number: 1, Labels: []string{issues.StatusReady}}}
	fake.issues.items[1] = fake.issues.listResult[0]
	fake.issues.updateErr = fmt.Errorf("github unavailable")
	fake.milestones.items[9] = &platform.Milestone{Number: 9, Title: "Demo"}
	fake.repo.branches["herd/batch/9-demo"] = "head"
	svc := newTestService(fake, newFakeStore(), nil)

	err := svc.CleanupClosedBatchPR(ctx, 9, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "close issue 1")
	assert.Contains(t, fake.repo.deleted, "herd/batch/9-demo")
}
