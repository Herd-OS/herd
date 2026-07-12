package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchReadyWorkers_UsesServiceDispatcher(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
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

func TestEnsureTaskIssueStartedIdempotencyDoesNotCreateDuplicate(t *testing.T) {
	ctx := context.Background()
	fake := newFakePlatform()
	st := newFakeStore()
	svc := newTestService(fake, st, &fakeDispatcher{})
	key := idempotencyKey("task-issue", "repo", svc.Repo.ID, "batch", 4, "create", "Task")
	st.keys[key] = store.IdempotencyKey{Key: key, Scope: "issue_create", Status: "started", CreatedAt: fixedClock()}

	issue, err := svc.EnsureTaskIssue(ctx, TaskIssueRequest{
		BatchNumber: 4,
		Title:       "Task",
		Body:        "body",
		Labels:      []string{issues.StatusReady},
		Milestone:   4,
	})

	require.Error(t, err)
	assert.Nil(t, issue)
	assert.Contains(t, err.Error(), "without a completed result")
	assert.Empty(t, fake.issues.created)
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
	keys         map[string]store.IdempotencyKey
	mutations    map[string]store.GitHubMutationAttempt
	results      map[string]store.JobResult
	completeErrs map[string][]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		keys:      map[string]store.IdempotencyKey{},
		mutations: map[string]store.GitHubMutationAttempt{},
		results:   map[string]store.JobResult{},
	}
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
	record.Status = "failed"
	s.keys[key] = record
	return nil
}

func (s *fakeStore) RecordGitHubMutationAttempt(_ context.Context, a store.GitHubMutationAttempt) error {
	s.mutations[a.IdempotencyKey] = a
	return nil
}

func (s *fakeStore) CompleteGitHubMutationAttempt(_ context.Context, key string, status string, response json.RawMessage, errMsg string, completedAt time.Time) error {
	attempt := s.mutations[key]
	attempt.Status = status
	attempt.Response = response
	attempt.Error = errMsg
	attempt.CompletedAt = &completedAt
	s.mutations[key] = attempt
	return nil
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
	items      map[int]*platform.Issue
	listResult []*platform.Issue
	created    []*platform.Issue
	comments   map[int][]string
	added      map[int][]string
	removed    map[int][]string
	next       int
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
	if s.comments == nil {
		s.comments = map[int][]string{}
	}
	s.comments[number] = append(s.comments[number], body)
	return nil
}

func (s *fakeIssueService) AddCommentReturningID(ctx context.Context, number int, body string) (int64, error) {
	return 1, s.AddComment(ctx, number, body)
}

func (s *fakeIssueService) UpdateComment(context.Context, int64, string) error { return nil }
func (s *fakeIssueService) DeleteComment(context.Context, int64) error         { return nil }
func (s *fakeIssueService) ListComments(context.Context, int) ([]*platform.Comment, error) {
	return nil, nil
}
func (s *fakeIssueService) CreateCommentReaction(context.Context, int64, string) error { return nil }

type fakePRService struct {
	items   map[int]*platform.PullRequest
	created *platform.PullRequest
	updated int
	merged  int
	next    int
}

func (s *fakePRService) Create(_ context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	s.next++
	pr := &platform.PullRequest{Number: s.next, Title: title, Body: body, Head: head, Base: base, State: "open"}
	s.items[pr.Number] = pr
	s.created = pr
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
	deleted       []string
	updated       []string
}

func (s *fakeRepoService) GetInfo(context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: s.defaultBranch}, nil
}
func (s *fakeRepoService) GetDefaultBranch(context.Context) (string, error) {
	return s.defaultBranch, nil
}
func (s *fakeRepoService) CreateBranch(_ context.Context, name, fromSHA string) error {
	s.branches[name] = fromSHA
	return nil
}
func (s *fakeRepoService) DeleteBranch(_ context.Context, name string) error {
	delete(s.branches, name)
	s.deleted = append(s.deleted, name)
	return nil
}
func (s *fakeRepoService) GetBranchSHA(_ context.Context, name string) (string, error) {
	sha, ok := s.branches[name]
	if !ok {
		return "", fmt.Errorf("branch not found")
	}
	return sha, nil
}
func (s *fakeRepoService) UpdateBranchToCommit(_ context.Context, name, sha string, _ bool) error {
	s.branches[name] = sha
	s.updated = append(s.updated, name)
	return nil
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
