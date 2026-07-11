package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerAcceptsAndStoresResult(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", Metadata: json.RawMessage(`{"ref":"refs/heads/herd/worker/837","workflow_file":"worker.yml","workflow_run_id":"12345"}`)}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedOIDCValidator(validClaims(now)),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
	})

	req := resultRequest("job-1", validWorkerPayload("job-1", "head"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"accepted","created":true,"job_id":"job-1","kind":"worker_completed","idempotency_key":"worker_completed:`+ResultPayloadHash([]byte(validWorkerPayload("job-1", "head")))+`"}`, rec.Body.String())
	require.Len(t, st.results, 1)
	result := st.results[0]
	assert.Equal(t, "job-1", result.JobID)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, ResultPayloadHash([]byte(validWorkerPayload("job-1", "head"))), result.ResultRef)
}

func TestHandlerDuplicateCallbacksAreIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head"}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedOIDCValidator(validClaims(now)),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", validWorkerPayload("job-1", "head")))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Contains(t, second.Body.String(), `"created":false`)
	assert.Len(t, st.results, 1)
}

func TestHandlerRejectsMismatchedPathAndBodyJobID(t *testing.T) {
	st := newResultStore()
	handler := NewHandler(HandlerOptions{Store: st, Validator: fixedOIDCValidator(validClaims(time.Now().Add(time.Hour)))})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("path-job", validWorkerPayload("body-job", "head")))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, st.results)
}

func TestHandlerRejectsStaleHeadSHA(t *testing.T) {
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "new"}
	handler := NewHandler(HandlerOptions{Store: st, Validator: fixedOIDCValidator(validClaims(time.Now().Add(time.Hour)))})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "old")))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Empty(t, st.results)
}

func TestHandlerRejectsPatchForDifferentRepositoryAndRecordsFailure(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base"}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/other", "job-1", "base", "head", "patch.diff", patch)
	handler := NewHandler(HandlerOptions{
		Store:         st,
		Validator:     fixedOIDCValidator(validClaims(now)),
		Audience:      "herd-control-plane",
		Now:           func() time.Time { return now },
		ArtifactStore: artifactMap(t, metadata, patch),
		PatchApplier:  fixedPatchApplier{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusFailure, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), "patch repository does not match result repository")
	require.Len(t, st.mutationCompletions, 1)
	assert.Equal(t, "failed", st.mutationCompletions[0].status)
}

func TestHandlerAppliesValidPatchArtifactAndRecordsCommitSHA(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		HeadSHA:        "head",
		BaseSHA:        "base",
		Metadata:       json.RawMessage(`{"requester_name":"Mona","requester_email":"mona@example.com"}`),
	}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	applier := fixedPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:         st,
		Validator:     fixedOIDCValidator(validClaims(now)),
		Audience:      "herd-control-plane",
		Now:           func() time.Time { return now },
		ArtifactStore: artifactMap(t, metadata, patch),
		PatchApplier:  applier,
		AppLogin:      "herd-os[bot]",
		AppEmail:      "herd@example.com",
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusSuccess, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), strings.Repeat("a", 40))
	require.Len(t, st.mutationCompletions, 1)
	assert.Equal(t, "completed", st.mutationCompletions[0].status)
	assert.Contains(t, string(st.mutationCompletions[0].response), strings.Repeat("a", 40))
}

func TestHandlerRejectsMissingBearerToken(t *testing.T) {
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head"}
	handler := NewHandler(HandlerOptions{Store: st, Validator: fixedOIDCValidator(validClaims(time.Now().Add(time.Hour)))})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/results", strings.NewReader(validWorkerPayload("job-1", "head")))
	req.SetPathValue("job_id", "job-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, st.results)
}

func TestHandlerRejectsOIDCValidatorFailure(t *testing.T) {
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head"}
	handler := NewHandler(HandlerOptions{Store: st, Validator: errOIDCValidator{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, st.results)
}

type resultStore struct {
	mu      sync.Mutex
	jobs    map[string]store.Job
	results []store.JobResult
	seen    map[string]struct{}

	mutationAttempts    []store.GitHubMutationAttempt
	mutationCompletions []mutationCompletion
}

func newResultStore() *resultStore {
	return &resultStore{
		jobs: map[string]store.Job{},
		seen: map[string]struct{}{},
	}
}

func (s *resultStore) GetJob(_ context.Context, jobID string) (store.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return store.Job{}, store.ErrNotFound
	}
	return job, nil
}

func (s *resultStore) RecordJobResult(_ context.Context, result store.JobResult) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := result.JobID + "\x00" + result.IdempotencyKey
	if _, ok := s.seen[key]; ok {
		return false, nil
	}
	s.seen[key] = struct{}{}
	s.results = append(s.results, result)
	return true, nil
}

func (s *resultStore) RecordGitHubMutationAttempt(_ context.Context, attempt store.GitHubMutationAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutationAttempts = append(s.mutationAttempts, attempt)
	return nil
}

func (s *resultStore) CompleteGitHubMutationAttempt(_ context.Context, key string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutationCompletions = append(s.mutationCompletions, mutationCompletion{
		key:          key,
		status:       status,
		response:     response,
		errorMessage: errorMessage,
		completedAt:  completedAt,
	})
	return nil
}

type mutationCompletion struct {
	key          string
	status       string
	response     json.RawMessage
	errorMessage string
	completedAt  time.Time
}

type artifactStore map[string][]byte

func artifactMap(t *testing.T, metadata artifacts.PatchMetadata, patch []byte) artifactStore {
	t.Helper()
	metadataBytes, err := json.Marshal(metadata)
	require.NoError(t, err)
	return artifactStore{
		"patches/job.diff":    metadataBytes,
		metadata.ArtifactName: patch,
	}
}

func (s artifactStore) OpenArtifact(_ context.Context, name string) (io.ReadCloser, error) {
	data, ok := s[name]
	if !ok {
		return nil, fmt.Errorf("missing artifact")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type fixedPatchApplier struct {
	result artifacts.ApplyResult
	err    error
}

func (a fixedPatchApplier) Apply(context.Context, artifacts.ApplyRequest) (artifacts.ApplyResult, error) {
	return a.result, a.err
}

type fixedOIDCValidator OIDCClaims

func (v fixedOIDCValidator) Validate(context.Context, string) (OIDCClaims, error) {
	return OIDCClaims(v), nil
}

type errOIDCValidator struct{}

func (errOIDCValidator) Validate(context.Context, string) (OIDCClaims, error) {
	return OIDCClaims{}, assert.AnError
}

func validClaims(now time.Time) OIDCClaims {
	return OIDCClaims{
		Issuer:     GitHubActionsIssuer,
		Audience:   []string{"herd-control-plane"},
		Repository: "acme/widgets",
		Ref:        "refs/heads/herd/worker/837",
		Workflow:   "worker.yml",
		RunID:      "12345",
		ExpiresAt:  now.Add(time.Hour),
	}
}

func resultRequest(jobID string, payload string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID+"/results", strings.NewReader(payload))
	req.SetPathValue("job_id", jobID)
	req.Header.Set("Authorization", "Bearer token")
	return req
}

func validWorkerPayload(jobID string, headSHA string) string {
	return `{"version":1,"kind":"worker_completed","repository":"acme/widgets","job_id":"` + jobID + `","batch_number":106,"issue_number":837,"target_branch":"herd/worker/837","base_sha":"base","expected_head_sha":"` + headSHA + `","patch_artifact":"patches/job.diff","status":"success"}`
}
