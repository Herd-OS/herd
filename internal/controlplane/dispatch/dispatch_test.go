package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatcherDispatchCreatesJobAndDispatchesWorkflow(t *testing.T) {
	st := newFakeStore()
	gh := &fakeWorkflowClient{}
	req := validRequest()

	result, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.NotEmpty(t, result.JobID)
	assert.Equal(t, "https://github.com/octo/herd/actions", result.URL)
	require.Len(t, gh.calls, 1)
	assert.Equal(t, int64(101), gh.calls[0].installationID)
	assert.Equal(t, "herd-worker.yml", gh.calls[0].workflowFile)
	assert.Equal(t, "main", gh.calls[0].ref)
	assert.Equal(t, result.JobID, gh.calls[0].inputs["job_id"])
	assert.Equal(t, "55", gh.calls[0].inputs["issue_number"])
	require.Len(t, st.jobs, 1)
	assert.Equal(t, "dispatching", st.jobs[result.JobID].Status)
	require.Len(t, st.mutationAttempts, 1)
	assert.Equal(t, "completed", st.mutationAttempts[0].Status)
	assert.Empty(t, st.mutationAttempts[0].Error)
}

func TestDispatcherDuplicateDispatchIsIdempotent(t *testing.T) {
	st := newFakeStore()
	gh := &fakeWorkflowClient{}
	req := validRequest()

	first, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)
	require.NoError(t, err)
	second, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, first.Created)
	assert.False(t, second.Created)
	assert.Equal(t, first.JobID, second.JobID)
	assert.Equal(t, first.URL, second.URL)
	assert.Len(t, gh.calls, 1)
	assert.Len(t, st.jobs, 1)
	assert.Len(t, st.idempotencyKeys, 1)
}

func TestDispatcherDuplicateInProgressUsesIdempotencyMetadata(t *testing.T) {
	st := newFakeStore()
	req := validRequest()
	key := IdempotencyKey(req)
	st.idempotencyKeys[key] = store.IdempotencyKey{
		Key:      key,
		Scope:    "workflow_dispatch",
		Status:   "started",
		Metadata: json.RawMessage(`{"job_id":"job-existing"}`),
	}
	st.jobs["job-existing"] = store.Job{JobID: "job-existing"}

	result, err := Dispatcher{Store: st, GitHub: &fakeWorkflowClient{}}.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.False(t, result.Created)
	assert.Equal(t, "job-existing", result.JobID)
}

func TestDispatcherValidationRejectsMissingAndStaleHeadSHA(t *testing.T) {
	tests := []struct {
		name string
		req  DispatchRequest
		want string
	}{
		{
			name: "review missing head",
			req: func() DispatchRequest {
				r := validRequest()
				r.Kind = JobKindReview
				r.PRNumber = 7
				r.IssueNumber = 0
				r.HeadSHA = ""
				return r
			}(),
			want: "head SHA is required",
		},
		{
			name: "stale expected head",
			req: func() DispatchRequest {
				r := validRequest()
				r.Kind = JobKindReview
				r.PRNumber = 7
				r.IssueNumber = 0
				r.ExpectedHeadSHA = "old"
				return r
			}(),
			want: "stale dispatch head SHA",
		},
		{
			name: "review missing pr",
			req: func() DispatchRequest {
				r := validRequest()
				r.Kind = JobKindReview
				r.PRNumber = 0
				return r
			}(),
			want: "PR number is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Dispatcher{Store: newFakeStore(), GitHub: &fakeWorkflowClient{}}.Dispatch(context.Background(), tt.req)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestDispatcherRecordsGitHubDispatchError(t *testing.T) {
	st := newFakeStore()
	gh := &fakeWorkflowClient{err: errors.New("github down")}

	_, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), validRequest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch workflow")
	require.Len(t, st.jobs, 1)
	require.Len(t, st.mutationAttempts, 1)
	assert.Equal(t, "failed", st.mutationAttempts[0].Status)
	assert.Contains(t, st.mutationAttempts[0].Error, "github down")
	for _, key := range st.idempotencyKeys {
		assert.Equal(t, "failed", key.Status)
	}
}

func TestDispatcherRetriesAfterWorkflowDispatchFailure(t *testing.T) {
	st := newFakeStore()
	gh := &fakeWorkflowClient{errors: []error{errors.New("github down"), nil}}
	req := validRequest()

	_, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)
	require.Error(t, err)
	result, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Len(t, gh.calls, 1)
	assert.Len(t, st.jobs, 1)
	record := st.idempotencyKeys[IdempotencyKey(req)]
	assert.Equal(t, "completed", record.Status)
	assert.Contains(t, record.ResultRef, result.JobID)
}

func TestDispatcherRetriesAfterCreateJobFailure(t *testing.T) {
	st := newFakeStore()
	st.createJobErrs = []error{errors.New("database down"), nil}
	gh := &fakeWorkflowClient{}
	req := validRequest()

	_, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)
	require.Error(t, err)
	result, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Len(t, gh.calls, 1)
	assert.Len(t, st.jobs, 1)
	assert.Equal(t, "completed", st.idempotencyKeys[IdempotencyKey(req)].Status)
}

func TestAppWorkflowClientPropagatesTokenSourceError(t *testing.T) {
	client := NewAppWorkflowClient(fakeTokenSource{err: errors.New("mint failed")})

	err := client.DispatchWorkflow(context.Background(), 101, "octo", "herd", "herd-worker.yml", "main", map[string]string{"job_id": "job-1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint failed")
}

func TestDispatcherDoesNotReadEnvironmentTokens(t *testing.T) {
	t.Setenv("HERD_GITHUB_TOKEN", "gho_human")
	t.Setenv("GITHUB_TOKEN", "gho_actions")
	t.Setenv("GH_TOKEN", "gho_cli")
	st := newFakeStore()
	gh := &fakeWorkflowClient{}

	_, err := Dispatcher{Store: st, GitHub: gh}.Dispatch(context.Background(), validRequest())

	require.NoError(t, err)
	require.Len(t, gh.calls, 1)
	assert.NotContains(t, gh.calls[0].inputs, "token")
}

func validRequest() DispatchRequest {
	return DispatchRequest{
		RepoID:          42,
		Owner:           "octo",
		Repo:            "herd",
		InstallationID:  101,
		Kind:            JobKindWorker,
		WorkflowFile:    "herd-worker.yml",
		Ref:             "main",
		BatchNumber:     12,
		IssueNumber:     55,
		BatchBranch:     "herd/batch/12",
		HeadSHA:         "abc123",
		ExpectedHeadSHA: "abc123",
		RunnerLabel:     "herd-worker",
		TimeoutMinutes:  30,
		Reason:          "test",
	}
}

type fakeStore struct {
	jobs             map[string]store.Job
	idempotencyKeys  map[string]store.IdempotencyKey
	mutationAttempts []store.GitHubMutationAttempt
	createJobErrs    []error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		jobs:            map[string]store.Job{},
		idempotencyKeys: map[string]store.IdempotencyKey{},
	}
}

func (s *fakeStore) CreateJob(_ context.Context, j store.Job) error {
	if len(s.createJobErrs) > 0 {
		err := s.createJobErrs[0]
		s.createJobErrs = s.createJobErrs[1:]
		if err != nil {
			return err
		}
	}
	s.jobs[j.JobID] = j
	return nil
}

func (s *fakeStore) GetJob(_ context.Context, jobID string) (store.Job, error) {
	job, ok := s.jobs[jobID]
	if !ok {
		return store.Job{}, store.ErrNotFound
	}
	return job, nil
}

func (s *fakeStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	if _, ok := s.idempotencyKeys[key.Key]; ok {
		return false, nil
	}
	s.idempotencyKeys[key.Key] = key
	return true, nil
}

func (s *fakeStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *fakeStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "completed"
	record.ResultRef = resultRef
	record.CompletedAt = &now
	s.idempotencyKeys[key] = record
	return nil
}

func (s *fakeStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "failed"
	record.ResultRef = errorMessage
	record.CompletedAt = &now
	s.idempotencyKeys[key] = record
	return nil
}

func (s *fakeStore) RecordGitHubMutationAttempt(_ context.Context, a store.GitHubMutationAttempt) error {
	for _, existing := range s.mutationAttempts {
		if existing.IdempotencyKey == a.IdempotencyKey {
			return errors.New("duplicate mutation idempotency key")
		}
	}
	s.mutationAttempts = append(s.mutationAttempts, a)
	return nil
}

func (s *fakeStore) CompleteGitHubMutationAttempt(_ context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	for i := range s.mutationAttempts {
		if s.mutationAttempts[i].IdempotencyKey == idempotencyKey {
			s.mutationAttempts[i].Status = status
			s.mutationAttempts[i].Response = response
			s.mutationAttempts[i].Error = errorMessage
			s.mutationAttempts[i].CompletedAt = &completedAt
			return nil
		}
	}
	return store.ErrNotFound
}

type workflowCall struct {
	installationID int64
	owner          string
	repo           string
	workflowFile   string
	ref            string
	inputs         map[string]string
}

type fakeWorkflowClient struct {
	err    error
	errors []error
	calls  []workflowCall
}

func (c *fakeWorkflowClient) DispatchWorkflow(_ context.Context, installationID int64, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	if len(c.errors) > 0 {
		err := c.errors[0]
		c.errors = c.errors[1:]
		if err != nil {
			return err
		}
	}
	if c.err != nil {
		return c.err
	}
	copied := make(map[string]string, len(inputs))
	for k, v := range inputs {
		copied[k] = v
	}
	c.calls = append(c.calls, workflowCall{
		installationID: installationID,
		owner:          owner,
		repo:           repo,
		workflowFile:   workflowFile,
		ref:            ref,
		inputs:         copied,
	})
	return nil
}

type fakeTokenSource struct {
	err error
}

func (s fakeTokenSource) InstallationToken(context.Context, int64) (appauth.InstallationToken, error) {
	return appauth.InstallationToken{}, s.err
}
