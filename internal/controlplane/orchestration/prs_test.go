package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/herd-os/herd/internal/controlplane/mutations"
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

func TestOpenBatchPR_UpdatesCompletedDuplicateWhenBodyChanges(t *testing.T) {
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
	req.Body = "updated body"
	second, err := svc.OpenBatchPR(ctx, req)

	require.NoError(t, err)
	assert.Equal(t, first.Number, second.Number)
	assert.Equal(t, "updated body", second.Body)
	assert.Equal(t, 1, fake.prs.next)
	assert.Equal(t, 1, fake.prs.updated)
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
			name: "delete rejects missing expected head",
			setup: func(p *fakePlatform) {
				p.repo.branches["herd/worker/1-task"] = "actual"
			},
			req: BranchOperationRequest{
				OperationKind: "delete",
				BranchName:    "herd/worker/1-task",
			},
			wantErr: "expected head SHA",
		},
		{
			name: "delete rejects mismatched expected head",
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

func TestApplyBranchOperationCreateIdentityIncludesFromSHA(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	svc := newTestService(fake, newFakeStore(), nil)
	first := BranchOperationRequest{OperationKind: "create", BranchName: "herd/worker/1-task", FromSHA: "base-a"}
	second := BranchOperationRequest{OperationKind: "create", BranchName: "herd/worker/1-task", FromSHA: "base-b"}

	require.NoError(t, svc.ApplyBranchOperation(ctx, first))
	err := svc.ApplyBranchOperation(ctx, second)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected create source")
	assert.Equal(t, "base-a", fake.repo.branches[first.BranchName])
}

func TestApplyBranchOperationUpdateIdentityIncludesNewSHA(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/worker/1-task"] = "old"
	svc := newTestService(fake, newFakeStore(), nil)
	first := BranchOperationRequest{OperationKind: "update", BranchName: "herd/worker/1-task", ExpectedHeadSHA: "old", NewSHA: "new-a"}
	second := BranchOperationRequest{OperationKind: "update", BranchName: "herd/worker/1-task", ExpectedHeadSHA: "old", NewSHA: "new-b"}

	require.NoError(t, svc.ApplyBranchOperation(ctx, first))
	err := svc.ApplyBranchOperation(ctx, second)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "head mismatch")
	assert.Equal(t, "new-a", fake.repo.branches[first.BranchName])
}

func TestApplyBranchOperationCreateRejectsExistingWrongSHARecovery(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/worker/1-task"] = "stale"
	svc := newTestService(fake, newFakeStore(), nil)

	err := svc.ApplyBranchOperation(ctx, BranchOperationRequest{
		OperationKind: "create",
		BranchName:    "herd/worker/1-task",
		FromSHA:       "fresh",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected create source fresh")
	assert.Equal(t, "stale", fake.repo.branches["herd/worker/1-task"])
}

func TestApplyBranchOperationCreateTransientLookupFailsPreCallUntilRedelivery(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branchErrs = []error{errors.New("github unavailable"), nil}
	st := newFakeStore()
	svc := newTestService(fake, st, nil)
	req := BranchOperationRequest{
		OperationKind: "create",
		BranchName:    "herd/worker/1-task",
		FromSHA:       "fresh",
	}

	firstErr := svc.ApplyBranchOperation(ctx, req)
	require.Error(t, firstErr)
	assert.Contains(t, firstErr.Error(), "github unavailable")
	assert.Empty(t, fake.repo.branches)

	secondErr := svc.ApplyBranchOperation(ctx, req)
	require.NoError(t, secondErr)
	assert.Equal(t, "fresh", fake.repo.branches["herd/worker/1-task"])
}

func TestApplyBranchOperationGuardsHeadAtMutationBoundary(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		req       BranchOperationRequest
		advance   func(*fakeRepoService)
		wantValue string
	}{
		{
			name: "update rejects branch advanced after preflight",
			req: BranchOperationRequest{
				OperationKind:   "update",
				BranchName:      "herd/worker/1-task",
				ExpectedHeadSHA: "old",
				NewSHA:          "new",
			},
			advance: func(repo *fakeRepoService) {
				repo.beforeUpdate = func(name string) {
					repo.branches[name] = "advanced"
				}
			},
			wantValue: "advanced",
		},
		{
			name: "delete rejects branch advanced after preflight",
			req: BranchOperationRequest{
				OperationKind:   "delete",
				BranchName:      "herd/worker/1-task",
				ExpectedHeadSHA: "old",
			},
			advance: func(repo *fakeRepoService) {
				repo.beforeDelete = func(name string) {
					repo.branches[name] = "advanced"
				}
			},
			wantValue: "advanced",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			fake.repo.branches["herd/worker/1-task"] = "old"
			tt.advance(fake.repo)
			svc := newTestService(fake, newFakeStore(), nil)

			err := svc.ApplyBranchOperation(ctx, tt.req)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "head mismatch")
			assert.ErrorIs(t, err, platform.ErrRefUpdateConflict)
			assert.Equal(t, tt.wantValue, fake.repo.branches["herd/worker/1-task"])
			assert.Empty(t, fake.repo.updated)
			assert.Empty(t, fake.repo.deleted)
		})
	}
}

func TestApplyBranchOperationRepairsPostCallUnknownBranchMutations(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		req        BranchOperationRequest
		setup      func(*fakeRepoService)
		wantBranch string
	}{
		{
			name: "create repaired from branch at source sha",
			req: BranchOperationRequest{
				OperationKind: "create",
				BranchName:    "herd/worker/1-task",
				FromSHA:       "base",
			},
			wantBranch: "base",
		},
		{
			name: "update repaired from branch at new sha",
			req: BranchOperationRequest{
				OperationKind:   "update",
				BranchName:      "herd/worker/1-task",
				ExpectedHeadSHA: "old",
				NewSHA:          "new",
			},
			setup: func(repo *fakeRepoService) {
				repo.branches["herd/worker/1-task"] = "old"
			},
			wantBranch: "new",
		},
		{
			name: "delete repaired from missing branch",
			req: BranchOperationRequest{
				OperationKind:   "delete",
				BranchName:      "herd/worker/1-task",
				ExpectedHeadSHA: "old",
			},
			setup: func(repo *fakeRepoService) {
				repo.branches["herd/worker/1-task"] = "old"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			if tt.setup != nil {
				tt.setup(fake.repo)
			}
			st := newFakeStore()
			svc := newTestService(fake, st, nil)
			identitySHA := tt.req.ExpectedHeadSHA
			if tt.req.OperationKind == "create" {
				identitySHA = tt.req.FromSHA
			}
			keyParts := []any{"branch", "repo", svc.Repo.ID, tt.req.BranchName, identitySHA, tt.req.OperationKind}
			if tt.req.OperationKind == "update" {
				keyParts = append(keyParts, tt.req.NewSHA)
			}
			key := idempotencyKey(keyParts...)
			st.completeMutationErrs[key] = []error{assert.AnError}
			st.completeErrs[key] = []error{assert.AnError}

			firstErr := svc.ApplyBranchOperation(ctx, tt.req)
			require.Error(t, firstErr)
			require.NoError(t, svc.ApplyBranchOperation(ctx, tt.req))

			assert.Len(t, fake.repo.created, boolToInt(tt.req.OperationKind == "create"))
			assert.Len(t, fake.repo.updated, boolToInt(tt.req.OperationKind == "update"))
			assert.Len(t, fake.repo.deleted, boolToInt(tt.req.OperationKind == "delete"))
			if tt.wantBranch == "" {
				assert.NotContains(t, fake.repo.branches, tt.req.BranchName)
			} else {
				assert.Equal(t, tt.wantBranch, fake.repo.branches[tt.req.BranchName])
			}
			assert.Equal(t, mutations.PhaseCompleted, st.mutations[key].Status)
			assert.Equal(t, "completed", st.keys[key].Status)
		})
	}
}

func TestApplyBranchOperationValidatesOperationBeforeMutationAttempt(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		invalid   BranchOperationRequest
		corrected BranchOperationRequest
		wantErr   string
	}{
		{
			name:      "create without from sha",
			invalid:   BranchOperationRequest{OperationKind: "create", BranchName: "herd/worker/1-task"},
			corrected: BranchOperationRequest{OperationKind: "create", BranchName: "herd/worker/1-task", FromSHA: "base"},
			wantErr:   "from SHA is required",
		},
		{
			name:      "update without new sha",
			invalid:   BranchOperationRequest{OperationKind: "update", BranchName: "herd/worker/1-task", ExpectedHeadSHA: "old"},
			corrected: BranchOperationRequest{OperationKind: "update", BranchName: "herd/worker/1-task", ExpectedHeadSHA: "old", NewSHA: "new"},
			wantErr:   "new SHA is required",
		},
		{
			name:      "unsupported operation kind",
			invalid:   BranchOperationRequest{OperationKind: "rename", BranchName: "herd/worker/1-task"},
			corrected: BranchOperationRequest{OperationKind: "delete", BranchName: "herd/worker/1-task", ExpectedHeadSHA: "old"},
			wantErr:   "unsupported branch operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakePlatform()
			if tt.corrected.OperationKind != "create" {
				fake.repo.branches["herd/worker/1-task"] = "old"
			}
			st := newFakeStore()
			svc := newTestService(fake, st, nil)

			err := svc.ApplyBranchOperation(ctx, tt.invalid)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, st.mutations)

			err = svc.ApplyBranchOperation(ctx, tt.corrected)
			require.NoError(t, err)
			assert.NotEmpty(t, st.mutations)
			for _, attempt := range st.mutations {
				assert.NotEqual(t, mutations.PhaseCallStarted, attempt.Status)
				assert.NotEqual(t, mutations.PhaseRepairRequired, attempt.Status)
			}
		})
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
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

func TestMergePR_RedeliveryRepairsMergedPRAfterCompletionFailures(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[8] = &platform.PullRequest{
		Number:  8,
		Title:   "[herd] Demo",
		State:   "open",
		Head:    "herd/batch/8-demo",
		HeadSHA: "head",
	}
	fake.repo.branches["herd/batch/8-demo"] = "head"
	fake.checks.status = "success"
	st := newFakeStore()
	key := idempotencyKey("merge", "repo", int64(123), "pr", 8, "head", "head")
	st.completeMutationErrs[key] = []error{errors.New("mutation store down")}
	st.completeErrs[key] = []error{errors.New("idempotency store down")}
	svc := newTestService(fake, st, nil)

	first, err := svc.MergePR(ctx, MergePRRequest{PRNumber: 8, ExpectedHeadSHA: "head", RequireCI: true})
	require.Error(t, err)
	assert.Nil(t, first)
	assert.Equal(t, 8, fake.prs.merged)

	fake.prs.merged = 0
	second, err := svc.MergePR(ctx, MergePRRequest{PRNumber: 8, ExpectedHeadSHA: "head", RequireCI: true})

	require.NoError(t, err)
	require.NotNil(t, second)
	assert.True(t, second.Merged)
	assert.Equal(t, "merge-sha", second.SHA)
	assert.Zero(t, fake.prs.merged)
	attempt, err := st.GetGitHubMutationAttempt(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, mutations.PhaseCompleted, attempt.Status)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "merge:merge-sha", st.keys[key].ResultRef)
}

func TestCleanupClosedBatchPR_ClosesIssuesMilestoneAndDeletesBranch(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[9] = &platform.PullRequest{Number: 9, Title: "[herd] Demo", State: "closed", Head: "herd/batch/9-demo", HeadSHA: "head"}
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
	fake.prs.items[9] = &platform.PullRequest{Number: 9, Title: "[herd] Demo", State: "closed", Head: "herd/batch/9-demo", HeadSHA: "head"}
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

func TestCleanupClosedBatchPR_DoesNotDeleteBranchWhenHeadSHAChanged(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[9] = &platform.PullRequest{Number: 9, Title: "[herd] Demo", State: "closed", Head: "herd/batch/9-demo", HeadSHA: "closed-head"}
	fake.milestones.items[9] = &platform.Milestone{Number: 9, Title: "Demo"}
	fake.repo.branches["herd/batch/9-demo"] = "new-head"
	svc := newTestService(fake, newFakeStore(), nil)

	err := svc.CleanupClosedBatchPR(ctx, 9, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "head mismatch")
	assert.NotContains(t, fake.repo.deleted, "herd/batch/9-demo")
}
