package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
