package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchReadyWorkers_UsesServiceDispatcher(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[99] = &platform.PullRequest{Number: 99}
	fake.repo.branches["herd/batch/7-demo"] = "batch-head"
	disp := &fakeDispatcher{}
	svc := newTestService(fake, newFakeStore(), disp)
	allIssues := []*platform.Issue{
		{
			Number: 1,
			Title:  "Task",
			Labels: []string{issues.StatusReady},
			Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n",
		},
	}

	count, err := svc.DispatchReadyWorkers(ctx, DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1},
		AllIssues:   allIssues,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.Len(t, disp.requests, 1)
	assert.Equal(t, cpdispatch.JobKindWorker, disp.requests[0].Kind)
	assert.Equal(t, int64(123), disp.requests[0].RepoID)
	assert.Equal(t, "herd-worker.yml", disp.requests[0].WorkflowFile)
	assert.Equal(t, "batch-head", disp.requests[0].BaseSHA)
	assert.Equal(t, "batch-head", disp.requests[0].HeadSHA)
	assert.Equal(t, "batch-head", disp.requests[0].ExpectedHeadSHA)
	assert.Contains(t, fake.issues.removed[1], issues.StatusReady)
	assert.Contains(t, fake.issues.added[1], issues.StatusInProgress)
}

func TestRecordWorkerCallback_ClassifiesStaleAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := Service{Store: store, Clock: fixedClock}

	tests := []struct {
		name      string
		req       WorkerCallbackRequest
		wantStale bool
	}{
		{
			name: "current head",
			req: WorkerCallbackRequest{
				JobID:           "job-1",
				IdempotencyKey:  "callback-1",
				Status:          "success",
				ExpectedHeadSHA: "abc",
				ActualHeadSHA:   "abc",
			},
		},
		{
			name: "stale head",
			req: WorkerCallbackRequest{
				JobID:           "job-2",
				IdempotencyKey:  "callback-2",
				Status:          "success",
				ExpectedHeadSHA: "abc",
				ActualHeadSHA:   "def",
			},
			wantStale: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := svc.RecordWorkerCallback(ctx, tt.req)
			require.NoError(t, err)
			assert.True(t, result.Created)
			assert.Equal(t, tt.wantStale, result.Stale)

			duplicate, err := svc.RecordWorkerCallback(ctx, tt.req)
			require.NoError(t, err)
			assert.False(t, duplicate.Created)
			assert.Equal(t, tt.wantStale, duplicate.Stale)
		})
	}
}

func TestDispatchReadyWorkers_RedeliveryRepairsLabelsAfterDispatchBeforeInProgressFailure(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/batch/7-demo"] = "batch-head-a"
	disp := &fakeDispatcher{}
	st := newFakeStore()
	st.completeErrs[issueStatusTransitionKey(123, 1, issues.StatusReady, "remove", "job-1")] = []error{errors.New("crash after ready removal")}
	svc := newTestService(fake, st, disp)
	readyIssue := &platform.Issue{
		Number: 1,
		Title:  "Task",
		Labels: []string{issues.StatusReady},
		Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n",
	}
	req := DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1},
		AllIssues:   []*platform.Issue{readyIssue},
	}

	count, dispatchErr := svc.DispatchReadyWorkers(ctx, req)
	require.Error(t, dispatchErr)
	assert.Equal(t, 0, count)
	assert.Len(t, disp.requests, 1)
	assert.Contains(t, fake.issues.removed[1], issues.StatusReady)
	assert.Empty(t, fake.issues.added[1])
	st.keys[cpdispatch.IdempotencyKey(disp.requests[0])] = store.IdempotencyKey{
		Key:       cpdispatch.IdempotencyKey(disp.requests[0]),
		Scope:     "workflow_dispatch",
		Status:    "completed",
		Metadata:  workflowDispatchMetadata(t, disp.requests[0]),
		CreatedAt: fixedClock(),
	}
	fake.repo.branches["herd/batch/7-demo"] = "batch-head-b"

	req.AllIssues = []*platform.Issue{{
		Number: 1,
		Title:  "Task",
		Labels: nil,
		Body:   readyIssue.Body,
	}}
	count, dispatchErr = svc.DispatchReadyWorkers(ctx, req)

	require.NoError(t, dispatchErr)
	assert.Equal(t, 0, count)
	assert.Len(t, disp.requests, 1)
	assert.Contains(t, fake.issues.removed[1], issues.StatusReady)
	assert.Contains(t, fake.issues.added[1], issues.StatusInProgress)
}

func TestDispatchReadyWorkersEmptyStatusRecoversBeforeResolvingMissingBatchBranch(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branchErrs = []error{errors.New("branch deleted")}
	disp := &fakeDispatcher{}
	st := newFakeStore()
	svc := newTestService(fake, st, disp)
	dispatchReq := cpdispatch.DispatchRequest{
		RepoID:          svc.Repo.ID,
		GitHubRepoID:    svc.Repo.GitHubID,
		Owner:           svc.Repo.Owner,
		Repo:            svc.Repo.Name,
		InstallationID:  svc.Repo.InstallationID,
		Kind:            cpdispatch.JobKindWorker,
		WorkflowFile:    "herd-worker.yml",
		Ref:             "main",
		BatchNumber:     7,
		IssueNumber:     1,
		BatchBranch:     "herd/batch/7-demo",
		BaseSHA:         "recovered-head",
		HeadSHA:         "recovered-head",
		ExpectedHeadSHA: "recovered-head",
	}
	st.keys["workflow-dispatch-completed"] = store.IdempotencyKey{
		Key:       "workflow-dispatch-completed",
		Scope:     "workflow_dispatch",
		Status:    "completed",
		Metadata:  workflowDispatchMetadata(t, dispatchReq),
		CreatedAt: fixedClock(),
	}

	count, err := svc.DispatchReadyWorkers(ctx, DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1},
		AllIssues: []*platform.Issue{{
			Number: 1,
			Title:  "Task",
			Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n",
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.Empty(t, disp.requests)
	assert.Empty(t, fake.repo.branchLookups)
	assert.Equal(t, []string{issues.StatusInProgress}, fake.issues.added[1])
}

func TestRecoverWorkflowDispatchIntentChoosesLatestCompletedMatchingDispatch(t *testing.T) {
	st := newFakeStore()
	svc := newTestService(newFakePlatform(), st, &fakeDispatcher{})
	req := cpdispatch.DispatchRequest{
		RepoID:       svc.Repo.ID,
		Kind:         cpdispatch.JobKindWorker,
		WorkflowFile: "herd-worker.yml",
		Ref:          "main",
		BatchNumber:  7,
		IssueNumber:  1,
		BatchBranch:  "herd/batch/7-demo",
		HeadSHA:      "current-head",
	}
	oldReq := req
	oldReq.HeadSHA = "old-head"
	newReq := req
	newReq.HeadSHA = "new-head"
	st.keys["old"] = store.IdempotencyKey{Key: "old", Scope: "workflow_dispatch", Status: "completed", Metadata: workflowDispatchMetadata(t, oldReq), CreatedAt: fixedClock().Add(-time.Hour)}
	st.keys["new"] = store.IdempotencyKey{Key: "new", Scope: "workflow_dispatch", Status: "completed", Metadata: workflowDispatchMetadata(t, newReq), CreatedAt: fixedClock()}
	st.keys["failed"] = store.IdempotencyKey{Key: "failed", Scope: "workflow_dispatch", Status: "failed_pre_call", Metadata: workflowDispatchMetadata(t, req), CreatedAt: fixedClock().Add(time.Hour)}

	recovered, ok := svc.recoverWorkflowDispatchIntent(context.Background(), req)

	require.True(t, ok)
	assert.Equal(t, "new-head", recovered.Request.HeadSHA)
	assert.Equal(t, "new-head", recovered.Request.BaseSHA)
	assert.Equal(t, "new-head", recovered.Request.ExpectedHeadSHA)
}

func TestRecoverWorkflowDispatchIntentDoesNotRecoverCallStartedWithoutProof(t *testing.T) {
	st := newFakeStore()
	svc := newTestService(newFakePlatform(), st, &fakeDispatcher{})
	req := cpdispatch.DispatchRequest{
		RepoID:       svc.Repo.ID,
		Kind:         cpdispatch.JobKindWorker,
		WorkflowFile: "herd-worker.yml",
		Ref:          "main",
		BatchNumber:  7,
		IssueNumber:  1,
		BatchBranch:  "herd/batch/7-demo",
		HeadSHA:      "current-head",
	}
	st.keys["started"] = store.IdempotencyKey{Key: "started", Scope: "workflow_dispatch", Status: "call_started", Metadata: workflowDispatchMetadata(t, req), CreatedAt: fixedClock()}

	_, ok := svc.recoverWorkflowDispatchIntent(context.Background(), req)

	assert.False(t, ok)
}

func TestRecoverWorkflowDispatchIntentRefusesAmbiguousDispatches(t *testing.T) {
	st := newFakeStore()
	svc := newTestService(newFakePlatform(), st, &fakeDispatcher{})
	req := cpdispatch.DispatchRequest{
		RepoID:       svc.Repo.ID,
		Kind:         cpdispatch.JobKindWorker,
		WorkflowFile: "herd-worker.yml",
		Ref:          "main",
		BatchNumber:  7,
		IssueNumber:  1,
		BatchBranch:  "herd/batch/7-demo",
		HeadSHA:      "current-head",
	}
	first := req
	first.HeadSHA = "head-a"
	second := req
	second.HeadSHA = "head-b"
	st.keys["first"] = store.IdempotencyKey{Key: "first", Scope: "workflow_dispatch", Status: "completed", Metadata: workflowDispatchMetadata(t, first), CreatedAt: fixedClock()}
	st.keys["second"] = store.IdempotencyKey{Key: "second", Scope: "workflow_dispatch", Status: "completed", Metadata: workflowDispatchMetadata(t, second), CreatedAt: fixedClock()}

	_, ok := svc.recoverWorkflowDispatchIntent(context.Background(), req)

	assert.False(t, ok)
}

func TestDispatchReadyWorkersReadyIssueIgnoresOldCompletedDispatchAtStaleHead(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/batch/7-demo"] = "new-head"
	disp := &fakeDispatcher{}
	st := newFakeStore()
	svc := newTestService(fake, st, disp)
	oldReq := cpdispatch.DispatchRequest{
		RepoID:       svc.Repo.ID,
		Kind:         cpdispatch.JobKindWorker,
		WorkflowFile: "herd-worker.yml",
		Ref:          "main",
		BatchNumber:  7,
		IssueNumber:  1,
		BatchBranch:  "herd/batch/7-demo",
		HeadSHA:      "old-head",
	}
	st.keys["old"] = store.IdempotencyKey{Key: "old", Scope: "workflow_dispatch", Status: "completed", Metadata: workflowDispatchMetadata(t, oldReq), CreatedAt: fixedClock()}

	count, err := svc.DispatchReadyWorkers(ctx, DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1},
		AllIssues: []*platform.Issue{{
			Number: 1,
			Labels: []string{issues.StatusReady},
			Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n",
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.Len(t, disp.requests, 1)
	assert.Equal(t, "new-head", disp.requests[0].HeadSHA)
	assert.Equal(t, "new-head", disp.requests[0].ExpectedHeadSHA)
}

func TestDispatchReadyWorkersCallStartedDispatchDoesNotMarkInProgressOrRedispatch(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/batch/7-demo"] = "batch-head"
	disp := &fakeDispatcher{err: errors.New("workflow dispatch \"started\" is already in progress")}
	st := newFakeStore()
	svc := newTestService(fake, st, disp)
	req := DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1},
		AllIssues: []*platform.Issue{{
			Number: 1,
			Title:  "Task",
			Labels: []string{issues.StatusReady},
			Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n",
		}},
	}
	dispatchReq := cpdispatch.DispatchRequest{
		RepoID:       svc.Repo.ID,
		Kind:         cpdispatch.JobKindWorker,
		WorkflowFile: "herd-worker.yml",
		Ref:          "main",
		BatchNumber:  7,
		IssueNumber:  1,
		BatchBranch:  "herd/batch/7-demo",
		HeadSHA:      "batch-head",
	}
	st.keys[cpdispatch.IdempotencyKey(dispatchReq)] = store.IdempotencyKey{Key: cpdispatch.IdempotencyKey(dispatchReq), Scope: "workflow_dispatch", Status: "call_started", Metadata: workflowDispatchMetadata(t, dispatchReq), CreatedAt: fixedClock()}

	count, err := svc.DispatchReadyWorkers(ctx, req)

	require.Error(t, err)
	assert.Equal(t, 0, count)
	assert.Len(t, disp.requests, 1)
	assert.Empty(t, fake.issues.removed[1])
	assert.Empty(t, fake.issues.added[1])
}

func TestDispatchReadyWorkersRepairsReadyIssueWhenHeadAdvancesAfterDispatchBeforeStatusTransition(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/batch/7-demo"] = "batch-head-1"
	disp := &fakeDispatcher{}
	st := newFakeStore()
	st.completeErrs[issueStatusTransitionKey(123, 1, issues.StatusReady, "remove", "job-1")] = []error{errors.New("crash after workflow dispatch")}
	svc := newTestService(fake, st, disp)
	req := DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1},
		AllIssues: []*platform.Issue{{
			Number: 1,
			Title:  "Task",
			Labels: []string{issues.StatusReady},
			Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDo it\n",
		}},
	}

	firstCount, firstErr := svc.DispatchReadyWorkers(ctx, req)
	require.Error(t, firstErr)
	assert.Equal(t, 0, firstCount)
	require.Len(t, disp.requests, 1)
	st.keys[cpdispatch.IdempotencyKey(disp.requests[0])] = store.IdempotencyKey{
		Key:       cpdispatch.IdempotencyKey(disp.requests[0]),
		Scope:     "workflow_dispatch",
		Status:    "completed",
		Metadata:  workflowDispatchMetadata(t, disp.requests[0]),
		CreatedAt: fixedClock(),
	}
	st.jobs["job-1"] = store.Job{JobID: "job-1", Status: "dispatching"}
	fake.repo.branches["herd/batch/7-demo"] = "batch-head-2"
	secondCount, secondErr := svc.DispatchReadyWorkers(ctx, req)

	require.NoError(t, secondErr)
	assert.Equal(t, 0, secondCount)
	assert.Len(t, disp.requests, 1)
	assert.Equal(t, []string{issues.StatusReady}, fake.issues.removed[1])
	assert.Equal(t, []string{issues.StatusInProgress}, fake.issues.added[1])
}

func TestDispatchReadyWorkersFailedAttemptAtOldHeadDispatchesNewGeneration(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/batch/7-demo"] = "new-head"
	disp := &fakeDispatcher{}
	st := newFakeStore()
	svc := newTestService(fake, st, disp)
	oldReq := cpdispatch.DispatchRequest{
		RepoID: svc.Repo.ID, Kind: cpdispatch.JobKindWorker, WorkflowFile: "herd-worker.yml",
		Ref: "main", BatchNumber: 7, IssueNumber: 1, BatchBranch: "herd/batch/7-demo",
		HeadSHA: "old-head",
	}
	st.keys["old-dispatch"] = store.IdempotencyKey{
		Key: "old-dispatch", Scope: "workflow_dispatch", Status: mutations.PhaseCompleted,
		Metadata: workflowDispatchMetadata(t, oldReq), CreatedAt: fixedClock(),
	}
	st.keys[issueStatusTransitionKey(svc.Repo.ID, 1, issues.StatusReady, "remove", "job-1")] = store.IdempotencyKey{
		Key: "old-ready-removal", Scope: "issue_label_remove", Status: mutations.PhaseCompleted,
	}
	st.jobs["job-1"] = store.Job{JobID: "job-1", Status: "failed", HeadSHA: "old-head"}

	count, err := svc.DispatchReadyWorkers(ctx, DispatchReadyWorkersRequest{
		BatchNumber: 7, BatchBranch: "herd/batch/7-demo", TierIssues: []int{1},
		AllIssues: []*platform.Issue{{
			Number: 1, Labels: []string{issues.StatusReady},
			Body: "---\nherd:\n  version: 1\n---\n\n## Task\nRetry it\n",
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, count)
	require.Len(t, disp.requests, 1)
	assert.Equal(t, "new-head", disp.requests[0].HeadSHA)
}

func TestDispatchAttemptActive(t *testing.T) {
	tests := []struct {
		name   string
		jobID  string
		status string
		want   bool
	}{
		{name: "dispatching", jobID: "active", status: "dispatching", want: true},
		{name: "blank status remains active", jobID: "blank", want: true},
		{name: "failed", jobID: "failed", status: "failed"},
		{name: "completed", jobID: "completed", status: "completed"},
		{name: "missing job", jobID: "missing"},
		{name: "missing identity"},
	}
	st := newFakeStore()
	for _, tt := range tests {
		if tt.jobID != "" && tt.name != "missing job" {
			st.jobs[tt.jobID] = store.Job{JobID: tt.jobID, Status: tt.status}
		}
	}
	svc := newTestService(newFakePlatform(), st, &fakeDispatcher{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, svc.dispatchAttemptActive(context.Background(), tt.jobID))
		})
	}
}

func workflowDispatchMetadata(t *testing.T, req cpdispatch.DispatchRequest) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"repo_id":       req.RepoID,
		"job_kind":      req.Kind,
		"workflow_file": req.WorkflowFile,
		"ref":           req.Ref,
		"batch_number":  req.BatchNumber,
		"issue_number":  req.IssueNumber,
		"batch_branch":  req.BatchBranch,
		"head_sha":      req.HeadSHA,
		"job_id":        "job-1",
	})
	require.NoError(t, err)
	return data
}

func TestDispatchReadyWorkers_SkipsIneligibleIssuesBeforeResolvingBatchHead(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.repo.branches["herd/batch/7-demo"] = "batch-head"
	fake.repo.branchErrs = []error{nil, errors.New("extra branch lookup should not run")}
	disp := &fakeDispatcher{}
	svc := newTestService(fake, newFakeStore(), disp)

	count, err := svc.DispatchReadyWorkers(ctx, DispatchReadyWorkersRequest{
		BatchNumber: 7,
		BatchBranch: "herd/batch/7-demo",
		TierIssues:  []int{1, 2, 3},
		AllIssues: []*platform.Issue{
			{Number: 1, Title: "In progress", Labels: []string{issues.StatusInProgress}},
			{Number: 2, Title: "Done", Labels: []string{issues.StatusDone}},
			{Number: 3, Title: "Ready", Labels: []string{issues.StatusReady}},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, []string{"herd/batch/7-demo"}, fake.repo.branchLookups)
	require.Len(t, disp.requests, 1)
	assert.Equal(t, 3, disp.requests[0].IssueNumber)
}

func TestAdvanceBatch_OpensPRWhenAllTiersComplete(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.milestones.items[4] = &platform.Milestone{Number: 4, Title: "Demo"}
	fake.issues.listResult = []*platform.Issue{
		{
			Number: 11,
			Title:  "Done",
			State:  "open",
			Labels: []string{issues.StatusDone},
			Body:   "---\nherd:\n  version: 1\n---\n\n## Task\nDone\n",
		},
	}
	svc := newTestService(fake, newFakeStore(), &fakeDispatcher{})

	result, err := svc.AdvanceBatch(ctx, 4, nil)

	require.NoError(t, err)
	assert.True(t, result.AllComplete)
	assert.Equal(t, 1, result.BatchPRNumber)
	require.NotNil(t, fake.prs.created)
	assert.Equal(t, "herd/batch/4-demo", fake.prs.created.Head)
}

func TestOpenBatchPRStartedIdempotencyDoesNotCreateDuplicate(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "pull_request_create", Status: "started", CreatedAt: fixedClock()}

	pr, err := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.Error(t, err)
	assert.Nil(t, pr)
	assert.Contains(t, err.Error(), "without a completed result")
	assert.Nil(t, fake.prs.created)
}

func TestOpenBatchPRGenericFailedIdempotencyWithoutMutationAttemptDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "pull_request_create", Status: mutations.LegacyFailed, CreatedAt: fixedClock()}

	pr, err := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.Error(t, err)
	assert.Nil(t, pr)
	assert.Contains(t, err.Error(), "retry after reconciliation")
	assert.Nil(t, fake.prs.created)
}

func TestOpenBatchPRExplicitFailedPreCallWithoutMutationAttemptRetries(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "pull_request_create", Status: mutations.PhaseFailedPreCall, CreatedAt: fixedClock()}

	pr, err := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, 1, pr.Number)
	assert.Equal(t, 1, len(fake.prs.createCalls))
}

func TestOpenBatchPRCompletedIdempotencyRepairsStartedMutationAttempt(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[99] = &platform.PullRequest{Number: 99}
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "pull_request_create", Status: "completed", ResultRef: "pr:99", CreatedAt: fixedClock()}
	st.mutations[key] = store.GitHubMutationAttempt{IdempotencyKey: key, RepositoryID: svc.Repo.ID, MutationType: "pull_request_create", Status: "started", CreatedAt: fixedClock()}

	pr, err := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, 99, pr.Number)
	assert.Nil(t, fake.prs.created)
	assert.Equal(t, "completed", st.mutations[key].Status)
	assert.Contains(t, string(st.mutations[key].Response), "pr:99")
}

func TestOpenBatchPRRetryRepairsIdempotencyFromCompletedMutationAttempt(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[1] = &platform.PullRequest{Number: 1}
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.completeErrs = map[string][]error{key: {errors.New("database down"), nil}}

	first, firstErr := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})
	second, secondErr := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.Error(t, firstErr)
	assert.Nil(t, first)
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, 1, second.Number)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "pr:1", st.keys[key].ResultRef)
	assert.Equal(t, 1, len(fake.prs.createCalls))
}

func TestOpenBatchPRRetriesAfterMutationAttemptRecordFailure(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.recordMutationErrs = map[string][]error{key: {errors.New("database down"), nil}}

	first, firstErr := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})
	second, secondErr := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.Error(t, firstErr)
	assert.Nil(t, first)
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, 1, second.Number)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "pr:1", st.keys[key].ResultRef)
	assert.Equal(t, 1, len(fake.prs.createCalls))
}

func TestOpenBatchPRRetryRepairsAfterMutationAttemptCompletionFailure(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.completeMutationErrs = map[string][]error{key: {errors.New("database down"), nil}}

	first, firstErr := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})
	second, secondErr := svc.OpenBatchPR(ctx, OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	})

	require.Error(t, firstErr)
	assert.Nil(t, first)
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, 1, second.Number)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "pr:1", st.keys[key].ResultRef)
	assert.Equal(t, "completed", st.mutations[key].Status)
	assert.Contains(t, string(st.mutations[key].Response), "pr:1")
	assert.Equal(t, 1, len(fake.prs.createCalls))
}

func TestOpenBatchPRRedeliveryRepairsCreateWhenDurableCompletionFails(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.completeMutationErrs[key] = []error{errors.New("mutation store down"), nil}
	st.completeErrs[key] = []error{errors.New("idempotency store down"), nil}
	req := OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] Demo",
		Body:        "body",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	}

	first, firstErr := svc.OpenBatchPR(ctx, req)
	second, secondErr := svc.OpenBatchPR(ctx, req)

	require.Error(t, firstErr)
	assert.Nil(t, first)
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, 1, second.Number)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "pr:1", st.keys[key].ResultRef)
	assert.Equal(t, mutations.PhaseCompleted, st.mutations[key].Status)
	assert.Equal(t, 1, len(fake.prs.createCalls))
}

func TestOpenBatchPRRedeliveryRepairsExistingUpdateWhenDurableCompletionFails(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	fake.prs.items[42] = &platform.PullRequest{
		Number: 42,
		Title:  "[herd] Old",
		Body:   "old",
		State:  "open",
		Head:   "herd/batch/4-demo",
		Base:   "main",
	}
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("batch-pr", "repo", svc.Repo.ID, "batch", 4)
	st.completeMutationErrs[key] = []error{errors.New("mutation store down"), nil}
	st.completeErrs[key] = []error{errors.New("idempotency store down"), nil}
	req := OpenBatchPRRequest{
		BatchNumber: 4,
		Title:       "[herd] New",
		Body:        "new",
		Head:        "herd/batch/4-demo",
		Base:        "main",
	}

	first, firstErr := svc.OpenBatchPR(ctx, req)
	second, secondErr := svc.OpenBatchPR(ctx, req)

	require.Error(t, firstErr)
	assert.Nil(t, first)
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, 42, second.Number)
	assert.Equal(t, "[herd] New", second.Title)
	assert.Equal(t, "new", second.Body)
	assert.Nil(t, fake.prs.created)
	assert.Equal(t, 1, fake.prs.updated)
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "pr:42", st.keys[key].ResultRef)
	assert.Equal(t, mutations.PhaseCompleted, st.mutations[key].Status)
}

func TestEnsureTaskIssueStartedIdempotencyDoesNotCreateDuplicate(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	req := TaskIssueRequest{
		BatchNumber: 4,
		Title:       "Task",
		Body:        "body",
		Labels:      []string{issues.StatusReady},
		Milestone:   4,
	}
	body, overflow := issues.TruncateIssueBody(req.Body)
	key := idempotencyKey("task-issue", "repo", svc.Repo.ID, "batch", 4, "create", "Task", taskIssueFingerprint(req, body, overflow))
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "issue_create", Status: "started", CreatedAt: fixedClock()}

	issue, err := svc.EnsureTaskIssue(ctx, req)

	require.Error(t, err)
	assert.Nil(t, issue)
	assert.Contains(t, err.Error(), "without a completed result")
	assert.Empty(t, fake.issues.created)
}

func TestEnsureTaskIssueRepairsCreatedIssueAfterCompletionFailures(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	req := TaskIssueRequest{
		BatchNumber: 4,
		Title:       "Task",
		Body:        "body",
		Labels:      []string{issues.StatusReady},
		Milestone:   4,
	}
	body, overflow := issues.TruncateIssueBody(req.Body)
	key := idempotencyKey("task-issue", "repo", svc.Repo.ID, "batch", 4, "create", "Task", taskIssueFingerprint(req, body, overflow))
	st.completeMutationErrs[key] = []error{assert.AnError}
	st.completeErrs[key] = []error{assert.AnError}

	first, firstErr := svc.EnsureTaskIssue(ctx, req)
	second, secondErr := svc.EnsureTaskIssue(ctx, req)

	require.Error(t, firstErr)
	assert.Nil(t, first)
	require.NoError(t, secondErr)
	require.NotNil(t, second)
	assert.Equal(t, 1, second.Number)
	assert.Len(t, fake.issues.created, 1)
	assert.Contains(t, fake.issues.created[0].Body, taskIssueCreateMarker(key))
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "issue:1", st.keys[key].ResultRef)
	assert.Equal(t, "completed", st.mutations[key].Status)
}

func TestEnsureOverflowCommentsRepairsCommentAfterCompletionFailures(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("task-issue-overflow-comment", "repo", svc.Repo.ID, "issue", 9, "create", 0, taskIssueTextFingerprint("overflow"))
	st.completeMutationErrs[key] = []error{assert.AnError}
	st.completeErrs[key] = []error{assert.AnError}

	firstErr := svc.ensureOverflowComments(ctx, 9, "create", "overflow")
	secondErr := svc.ensureOverflowComments(ctx, 9, "create", "overflow")

	require.Error(t, firstErr)
	require.NoError(t, secondErr)
	assert.Len(t, fake.issues.comments[9], 1)
	assert.Contains(t, fake.issues.comments[9][0].Body, taskIssueOverflowCommentMarker(key))
	assert.Equal(t, "completed", st.keys[key].Status)
	assert.Equal(t, "issue_comment:1", st.keys[key].ResultRef)
	assert.Equal(t, "completed", st.mutations[key].Status)
}

func TestMutateVoidLabelCompletedIdempotencyDoesNotCallGitHubAgain(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("issue-label", svc.Repo.ID, 11, issues.StatusReady, "remove")
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "issue_label_remove", Status: "completed", CreatedAt: fixedClock()}

	err := svc.mutate(ctx, key, "issue_label_remove", func() (string, error) {
		return "", fake.issues.RemoveLabels(ctx, 11, []string{issues.StatusReady})
	})

	require.NoError(t, err)
	assert.Empty(t, fake.issues.removed)
}

func newTestService(p *fakePlatform, st *fakeStore, dispatcher Dispatcher) Service {
	return Service{
		Repo: store.Repository{
			ID:             123,
			InstallationID: 456,
			Owner:          "owner",
			Name:           "repo",
			DefaultBranch:  "main",
		},
		Platform:   p,
		Store:      st,
		Dispatcher: dispatcher,
		Clock:      fixedClock,
	}
}

func fixedClock() time.Time {
	return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
}

type fakeStore struct {
	keys                 map[string]store.IdempotencyKey
	mutations            map[string]store.GitHubMutationAttempt
	results              map[string]store.JobResult
	completeErrs         map[string][]error
	recordMutationErrs   map[string][]error
	completeMutationErrs map[string][]error
	jobs                 map[string]store.Job
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		keys:                 map[string]store.IdempotencyKey{},
		mutations:            map[string]store.GitHubMutationAttempt{},
		results:              map[string]store.JobResult{},
		completeErrs:         map[string][]error{},
		recordMutationErrs:   map[string][]error{},
		completeMutationErrs: map[string][]error{},
		jobs:                 map[string]store.Job{},
	}
}

func (s *fakeStore) GetJob(_ context.Context, jobID string) (store.Job, error) {
	job, ok := s.jobs[jobID]
	if !ok {
		return store.Job{}, store.ErrNotFound
	}
	return job, nil
}

func (s *fakeStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	if _, ok := s.keys[key.Key]; ok {
		return false, nil
	}
	s.keys[key.Key] = key
	return true, nil
}

func (s *fakeStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	record, ok := s.keys[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *fakeStore) ListIdempotencyKeys(_ context.Context, scope string, limit int) ([]store.IdempotencyKey, error) {
	var out []store.IdempotencyKey
	for _, record := range s.keys {
		if limit > 0 && len(out) >= limit {
			break
		}
		if record.Scope != scope {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *fakeStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	if len(s.completeErrs[key]) > 0 {
		err := s.completeErrs[key][0]
		s.completeErrs[key] = s.completeErrs[key][1:]
		if err != nil {
			return err
		}
	}
	record, ok := s.keys[key]
	if !ok {
		return store.ErrNotFound
	}
	record.ResultRef = resultRef
	record.Status = "completed"
	s.keys[key] = record
	return nil
}

func (s *fakeStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	record, ok := s.keys[key]
	if !ok {
		return store.ErrNotFound
	}
	record.ResultRef = errorMessage
	if rest, ok := strings.CutPrefix(errorMessage, mutations.PhaseFailedPreCall+":"); ok {
		record.Status = mutations.PhaseFailedPreCall
		record.ResultRef = rest
	} else if rest, ok := strings.CutPrefix(errorMessage, mutations.PhaseRepairRequired+":"); ok {
		record.Status = mutations.PhaseRepairRequired
		record.ResultRef = rest
	} else if errorMessage == mutations.PhaseFailedPreCall || errorMessage == mutations.PhaseRepairRequired {
		record.Status = errorMessage
		record.ResultRef = ""
	} else {
		record.Status = mutations.LegacyFailed
	}
	s.keys[key] = record
	return nil
}

func (s *fakeStore) RecordGitHubMutationAttempt(_ context.Context, a store.GitHubMutationAttempt) error {
	if len(s.recordMutationErrs[a.IdempotencyKey]) > 0 {
		err := s.recordMutationErrs[a.IdempotencyKey][0]
		s.recordMutationErrs[a.IdempotencyKey] = s.recordMutationErrs[a.IdempotencyKey][1:]
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

func (s *fakeStore) GetGitHubMutationAttempt(_ context.Context, key string) (store.GitHubMutationAttempt, error) {
	attempt, ok := s.mutations[key]
	if !ok {
		return store.GitHubMutationAttempt{}, store.ErrNotFound
	}
	return attempt, nil
}

func (s *fakeStore) CompleteGitHubMutationAttempt(_ context.Context, key string, status string, response json.RawMessage, errMsg string, completedAt time.Time) error {
	if len(s.completeMutationErrs[key]) > 0 {
		err := s.completeMutationErrs[key][0]
		s.completeMutationErrs[key] = s.completeMutationErrs[key][1:]
		if err != nil {
			return err
		}
	}
	attempt := s.mutations[key]
	attempt.Status = status
	attempt.Response = response
	attempt.Error = errMsg
	attempt.CompletedAt = &completedAt
	s.mutations[key] = attempt
	return nil
}

func (s *fakeStore) TryStartGitHubMutationAttempt(_ context.Context, key string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error) {
	attempt, ok := s.mutations[key]
	if !ok {
		return store.GitHubMutationStartResult{}, store.ErrNotFound
	}
	allowed := false
	for _, status := range allowedStatuses {
		if attempt.Status == status {
			allowed = true
			break
		}
	}
	if !allowed {
		return store.GitHubMutationStartResult{Attempt: attempt}, nil
	}
	attempt.Status = "call_started"
	attempt.Response = json.RawMessage(`{}`)
	attempt.Error = ""
	attempt.CompletedAt = &completedAt
	s.mutations[key] = attempt
	return store.GitHubMutationStartResult{Started: true, Attempt: attempt}, nil
}

func (s *fakeStore) RecordJobResult(_ context.Context, r store.JobResult) (bool, error) {
	key := r.JobID + "/" + r.IdempotencyKey
	if _, ok := s.results[key]; ok {
		return false, nil
	}
	s.results[key] = r
	return true, nil
}

type fakeDispatcher struct {
	requests []cpdispatch.DispatchRequest
	err      error
	seen     map[string]string
}

func (d *fakeDispatcher) Dispatch(_ context.Context, req cpdispatch.DispatchRequest) (cpdispatch.DispatchResult, error) {
	if d.seen == nil {
		d.seen = map[string]string{}
	}
	key := cpdispatch.IdempotencyKey(req)
	if jobID, ok := d.seen[key]; ok {
		return cpdispatch.DispatchResult{JobID: jobID, Created: false}, nil
	}
	d.requests = append(d.requests, req)
	if d.err != nil {
		return cpdispatch.DispatchResult{}, d.err
	}
	jobID := fmt.Sprintf("job-%d", len(d.requests))
	d.seen[key] = jobID
	return cpdispatch.DispatchResult{JobID: jobID, Created: true}, nil
}

type fakePlatform struct {
	issues     *fakeIssueService
	prs        *fakePRService
	workflows  *fakeWorkflowService
	labels     *fakeLabelService
	milestones *fakeMilestoneService
	runners    *fakeRunnerService
	repo       *fakeRepoService
	checks     *fakeCheckService
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		issues:     &fakeIssueService{items: map[int]*platform.Issue{}, added: map[int][]string{}, removed: map[int][]string{}},
		prs:        &fakePRService{items: map[int]*platform.PullRequest{}},
		workflows:  &fakeWorkflowService{},
		labels:     &fakeLabelService{},
		milestones: &fakeMilestoneService{items: map[int]*platform.Milestone{}},
		runners:    &fakeRunnerService{},
		repo:       &fakeRepoService{branches: map[string]string{}, defaultBranch: "main"},
		checks:     &fakeCheckService{status: "success"},
	}
}

func (p *fakePlatform) Issues() platform.IssueService             { return p.issues }
func (p *fakePlatform) PullRequests() platform.PullRequestService { return p.prs }
func (p *fakePlatform) Workflows() platform.WorkflowService       { return p.workflows }
func (p *fakePlatform) Labels() platform.LabelService             { return p.labels }
func (p *fakePlatform) Milestones() platform.MilestoneService     { return p.milestones }
func (p *fakePlatform) Runners() platform.RunnerService           { return p.runners }
func (p *fakePlatform) Repository() platform.RepositoryService    { return p.repo }
func (p *fakePlatform) Checks() platform.CheckService             { return p.checks }

type fakeIssueService struct {
	items       map[int]*platform.Issue
	listResult  []*platform.Issue
	created     []*platform.Issue
	comments    map[int][]*platform.Comment
	added       map[int][]string
	removed     map[int][]string
	updateErr   error
	next        int
	nextComment int64
}

func (s *fakeIssueService) Create(_ context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	s.next++
	if s.next == 0 {
		s.next = 1
	}
	iss := &platform.Issue{Number: s.next, Title: title, Body: body, Labels: append([]string(nil), labels...)}
	if milestone != nil {
		iss.Milestone = &platform.Milestone{Number: *milestone}
	}
	s.items[iss.Number] = iss
	s.created = append(s.created, iss)
	return iss, nil
}

func (s *fakeIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	if iss, ok := s.items[number]; ok {
		return iss, nil
	}
	for _, iss := range s.listResult {
		if iss.Number == number {
			return iss, nil
		}
	}
	return nil, fmt.Errorf("issue not found")
}

func (s *fakeIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	out := append([]*platform.Issue(nil), s.listResult...)
	for _, issue := range s.items {
		out = append(out, issue)
	}
	return out, nil
}

func (s *fakeIssueService) Update(_ context.Context, number int, changes platform.IssueUpdate) (*platform.Issue, error) {
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	iss, ok := s.items[number]
	if !ok {
		iss = &platform.Issue{Number: number}
		s.items[number] = iss
	}
	if changes.Title != nil {
		iss.Title = *changes.Title
	}
	if changes.Body != nil {
		iss.Body = *changes.Body
	}
	if changes.State != nil {
		iss.State = *changes.State
	}
	return iss, nil
}

func (s *fakeIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	s.added[number] = append(s.added[number], labels...)
	return nil
}

func (s *fakeIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	s.removed[number] = append(s.removed[number], labels...)
	return nil
}

func (s *fakeIssueService) AddComment(_ context.Context, number int, body string) error {
	_, err := s.AddCommentReturningID(context.Background(), number, body)
	return err
}

func (s *fakeIssueService) AddCommentReturningID(ctx context.Context, number int, body string) (int64, error) {
	if s.comments == nil {
		s.comments = map[int][]*platform.Comment{}
	}
	s.nextComment++
	if s.nextComment == 0 {
		s.nextComment = 1
	}
	comment := &platform.Comment{ID: s.nextComment, Body: body}
	s.comments[number] = append(s.comments[number], comment)
	return comment.ID, nil
}

func (s *fakeIssueService) UpdateComment(context.Context, int64, string) error { return nil }
func (s *fakeIssueService) DeleteComment(context.Context, int64) error         { return nil }
func (s *fakeIssueService) ListComments(_ context.Context, number int) ([]*platform.Comment, error) {
	return append([]*platform.Comment(nil), s.comments[number]...), nil
}
func (s *fakeIssueService) CreateCommentReaction(context.Context, int64, string) error { return nil }

type fakePRService struct {
	items       map[int]*platform.PullRequest
	created     *platform.PullRequest
	createCalls []*platform.PullRequest
	updated     int
	merged      int
	next        int
}

func (s *fakePRService) Create(_ context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	s.next++
	pr := &platform.PullRequest{Number: s.next, Title: title, Body: body, Head: head, Base: base, State: "open"}
	s.items[pr.Number] = pr
	s.created = pr
	s.createCalls = append(s.createCalls, pr)
	return pr, nil
}

func (s *fakePRService) Get(_ context.Context, number int) (*platform.PullRequest, error) {
	pr, ok := s.items[number]
	if !ok {
		return nil, fmt.Errorf("PR not found")
	}
	return pr, nil
}

func (s *fakePRService) List(_ context.Context, filters platform.PRFilters) ([]*platform.PullRequest, error) {
	var out []*platform.PullRequest
	for _, pr := range s.items {
		if filters.State != "" && pr.State != filters.State {
			continue
		}
		if filters.Head != "" && pr.Head != filters.Head {
			continue
		}
		out = append(out, pr)
	}
	return out, nil
}

func (s *fakePRService) Update(_ context.Context, number int, title, body *string) (*platform.PullRequest, error) {
	pr, err := s.Get(context.Background(), number)
	if err != nil {
		return nil, err
	}
	if title != nil {
		pr.Title = *title
	}
	if body != nil {
		pr.Body = *body
	}
	s.updated++
	return pr, nil
}

func (s *fakePRService) Merge(_ context.Context, number int, method platform.MergeMethod) (*platform.MergeResult, error) {
	pr, err := s.Get(context.Background(), number)
	if err != nil {
		return nil, err
	}
	pr.State = "closed"
	pr.Merged = true
	pr.MergeCommitSHA = "merge-sha"
	s.merged = number
	return &platform.MergeResult{SHA: "merge-sha", Merged: true, Message: string(method)}, nil
}

func (s *fakePRService) UpdateBranch(context.Context, int) error { return nil }
func (s *fakePRService) CreateReview(context.Context, int, string, platform.ReviewEvent) error {
	return nil
}
func (s *fakePRService) AddComment(context.Context, int, string) error { return nil }
func (s *fakePRService) ListReviewComments(context.Context, int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (s *fakePRService) ListFiles(context.Context, int) ([]*platform.PullRequestFile, error) {
	return nil, nil
}
func (s *fakePRService) GetDiff(context.Context, int) (string, error) { return "", nil }
func (s *fakePRService) Close(context.Context, int) error             { return nil }

type fakeRepoService struct {
	branches      map[string]string
	defaultBranch string
	created       []string
	deleted       []string
	updated       []string
	branchErrs    []error
	branchLookups []string
	beforeUpdate  func(name string)
	beforeDelete  func(name string)
}

func (s *fakeRepoService) GetInfo(context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: s.defaultBranch}, nil
}
func (s *fakeRepoService) GetDefaultBranch(context.Context) (string, error) {
	return s.defaultBranch, nil
}
func (s *fakeRepoService) CreateBranch(_ context.Context, name, fromSHA string) error {
	s.branches[name] = fromSHA
	s.created = append(s.created, name)
	return nil
}
func (s *fakeRepoService) DeleteBranch(_ context.Context, name string) error {
	delete(s.branches, name)
	s.deleted = append(s.deleted, name)
	return nil
}
func (s *fakeRepoService) DeleteBranchIfHead(ctx context.Context, name, expectedHeadSHA string) error {
	if s.beforeDelete != nil {
		s.beforeDelete(name)
	}
	current, err := s.GetBranchSHA(ctx, name)
	if err != nil {
		return err
	}
	if current != expectedHeadSHA {
		return fmt.Errorf("deleting branch %s: head mismatch: expected %s, got %s: %w", name, expectedHeadSHA, current, platform.ErrRefUpdateConflict)
	}
	return s.DeleteBranch(ctx, name)
}
func (s *fakeRepoService) GetBranchSHA(_ context.Context, name string) (string, error) {
	s.branchLookups = append(s.branchLookups, name)
	if len(s.branchErrs) > 0 {
		err := s.branchErrs[0]
		s.branchErrs = s.branchErrs[1:]
		if err != nil {
			return "", err
		}
	}
	sha, ok := s.branches[name]
	if !ok {
		return "", platform.ErrNotFound
	}
	return sha, nil
}
func (s *fakeRepoService) UpdateBranchToCommit(_ context.Context, name, sha string, _ bool) error {
	s.branches[name] = sha
	s.updated = append(s.updated, name)
	return nil
}
func (s *fakeRepoService) UpdateBranchToCommitIfHead(ctx context.Context, name, sha, expectedHeadSHA string, force bool) error {
	if s.beforeUpdate != nil {
		s.beforeUpdate(name)
	}
	current, err := s.GetBranchSHA(ctx, name)
	if err != nil {
		return err
	}
	if current != expectedHeadSHA {
		return fmt.Errorf("updating branch %s: head mismatch: expected %s, got %s: %w", name, expectedHeadSHA, current, platform.ErrRefUpdateConflict)
	}
	return s.UpdateBranchToCommit(ctx, name, sha, force)
}

type fakeMilestoneService struct {
	items  map[int]*platform.Milestone
	closed []int
}

func (s *fakeMilestoneService) Create(context.Context, string, string, *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (s *fakeMilestoneService) Get(_ context.Context, number int) (*platform.Milestone, error) {
	ms, ok := s.items[number]
	if !ok {
		return nil, fmt.Errorf("milestone not found")
	}
	return ms, nil
}
func (s *fakeMilestoneService) List(context.Context) ([]*platform.Milestone, error) { return nil, nil }
func (s *fakeMilestoneService) Update(_ context.Context, number int, changes platform.MilestoneUpdate) (*platform.Milestone, error) {
	ms := s.items[number]
	if ms == nil {
		ms = &platform.Milestone{Number: number}
		s.items[number] = ms
	}
	if changes.State != nil {
		ms.State = *changes.State
		if *changes.State == "closed" {
			s.closed = append(s.closed, number)
		}
	}
	return ms, nil
}

type fakeCheckService struct{ status string }

func (s *fakeCheckService) GetCombinedStatus(context.Context, string) (string, error) {
	return s.status, nil
}
func (s *fakeCheckService) RerunFailedChecks(context.Context, string) error { return nil }

type fakeWorkflowService struct{}

func (s *fakeWorkflowService) GetWorkflow(context.Context, string) (int64, error) { return 0, nil }
func (s *fakeWorkflowService) Dispatch(context.Context, string, string, map[string]string) (*platform.Run, error) {
	return nil, nil
}
func (s *fakeWorkflowService) GetRun(context.Context, int64) (*platform.Run, error) { return nil, nil }
func (s *fakeWorkflowService) GetRunDiagnostics(context.Context, int64) (*platform.WorkflowRunDiagnostics, error) {
	return nil, nil
}
func (s *fakeWorkflowService) ListRuns(context.Context, platform.RunFilters) ([]*platform.Run, error) {
	return nil, nil
}
func (s *fakeWorkflowService) CancelRun(context.Context, int64) error { return nil }

type fakeLabelService struct{}

func (s *fakeLabelService) Create(context.Context, string, string, string) error { return nil }
func (s *fakeLabelService) List(context.Context) ([]*platform.Label, error)      { return nil, nil }
func (s *fakeLabelService) Delete(context.Context, string) error                 { return nil }

type fakeRunnerService struct{}

func (s *fakeRunnerService) List(context.Context) ([]*platform.Runner, error) { return nil, nil }
func (s *fakeRunnerService) Get(context.Context, int64) (*platform.Runner, error) {
	return nil, nil
}
