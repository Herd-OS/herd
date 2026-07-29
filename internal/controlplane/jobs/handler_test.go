package jobs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
	mutationspkg "github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerAcceptsAndStoresResult(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	patch := []byte{}
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837", Metadata: validJobMetadata()}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   fixedPatchApplier{},
		AppTokenSource: fakeAppTokenSource{},
	})

	payload := validWorkerPayload("job-1", "head")
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	req := resultRequest("job-1", payload)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"accepted","created":true,"job_id":"job-1","kind":"worker_completed","idempotency_key":"`+resultKey+`"}`, rec.Body.String())
	require.Len(t, st.results, 1)
	result := st.results[0]
	assert.Equal(t, "job-1", result.JobID)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, ResultPayloadHash([]byte(validWorkerPayload("job-1", "head"))), result.ResultRef)
}

func validJobMetadata() json.RawMessage {
	return json.RawMessage(`{"repository":"acme/widgets","ref":"refs/heads/herd/worker/837","workflow_file":"worker.yml","workflow_run_id":"12345"}`)
}

func validReviewJobMetadata() json.RawMessage {
	return json.RawMessage(`{"kind":"review","repository":"acme/widgets","github_repository_id":3003,"ref":"refs/heads/herd/worker/837","workflow_file":"herd-review.yml","workflow_run_id":"12345","workflow_run_url":"https://example.test/run"}`)
}

func validJobMetadataWith(extra map[string]any) json.RawMessage {
	metadata := map[string]any{
		"repository":      "acme/widgets",
		"ref":             "refs/heads/herd/worker/837",
		"workflow_file":   "worker.yml",
		"workflow_run_id": "12345",
	}
	for key, value := range extra {
		metadata[key] = value
	}
	out, _ := json.Marshal(metadata)
	return out
}

func TestHandlerDuplicateCallbacksAreIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	patch := []byte{}
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837", Metadata: validJobMetadata()}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   fixedPatchApplier{},
		AppTokenSource: fakeAppTokenSource{},
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

func TestHandlerPostCallUnknownCallbackRequiresDurableJobResult(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		seedResult    bool
		wantCode      int
		wantCreated   string
		wantResultLen int
	}{
		{name: "call started without result conflicts", status: mutationspkg.PhaseCallStarted, wantCode: http.StatusConflict, wantResultLen: 0},
		{name: "repair required without result conflicts", status: mutationspkg.PhaseRepairRequired, wantCode: http.StatusConflict, wantResultLen: 0},
		{name: "legacy started without result conflicts", status: mutationspkg.LegacyStarted, wantCode: http.StatusConflict, wantResultLen: 0},
		{name: "call started with stored result replays", status: mutationspkg.PhaseCallStarted, seedResult: true, wantCode: http.StatusAccepted, wantCreated: `"created":false`, wantResultLen: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
			st := newResultStore()
			patch := []byte{}
			metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
			st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837", Metadata: validJobMetadata()}
			payload := validWorkerPayload("job-1", "head")
			resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
			callbackKey := "job_result:" + resultKey
			st.idem[callbackKey] = store.IdempotencyKey{Key: callbackKey, Scope: "job_result_callback", Status: tt.status, ResultRef: resultKey, CreatedAt: now}
			if tt.seedResult {
				st.results = append(st.results, store.JobResult{JobID: "job-1", IdempotencyKey: resultKey, Status: StatusSuccess, ResultRef: ResultPayloadHash([]byte(payload)), CreatedAt: now})
				st.seen["job-1\x00"+resultKey] = struct{}{}
			}
			handler := NewHandler(HandlerOptions{
				Store:          st,
				Validator:      fixedOIDCValidator(validClaims(now)),
				Audience:       "herd-control-plane",
				Now:            func() time.Time { return now },
				ArtifactStore:  artifactMap(t, metadata, patch),
				PatchApplier:   fixedPatchApplier{},
				AppTokenSource: fakeAppTokenSource{},
			})

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, resultRequest("job-1", payload))

			require.Equal(t, tt.wantCode, rec.Code)
			if tt.wantCreated != "" {
				assert.Contains(t, rec.Body.String(), tt.wantCreated)
			} else {
				assert.Contains(t, rec.Body.String(), "repair required")
			}
			assert.Len(t, st.results, tt.wantResultLen)
		})
	}
}

func TestHandlerDuplicateWorkerCallbackUsesStableIdentityAcrossJSONFormatting(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837", Metadata: validJobMetadata()}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	firstPayload := validWorkerPayload("job-1", "head")
	secondPayload := `{
		"status":"success",
		"patch_artifact":"patches/job.diff",
		"expected_head_sha":"head",
		"base_sha":"base",
		"target_branch":"herd/worker/837",
		"issue_number":837,
		"batch_number":106,
		"job_id":"job-1",
		"repository":"acme/widgets",
		"kind":"worker_completed",
		"version":1
	}`

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", firstPayload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", secondPayload))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Contains(t, second.Body.String(), `"created":false`)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
}

func TestHandlerProcessesReviewCompletedResult(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
		Metadata:       validReviewJobMetadata(),
	}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validReviewClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validReviewPayload()))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, processor.calls, 1)
	assert.Equal(t, review.Repository{ID: 7, InstallationID: 9, Owner: "acme", Name: "widgets", ReviewEnabled: true}, processor.calls[0].repo)
	assert.Equal(t, 42, processor.calls[0].result.PRNumber)
	assert.Equal(t, "head", processor.calls[0].result.HeadSHA)
	assert.Equal(t, "https://example.test/run", processor.calls[0].result.TargetURL)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusApproved, st.results[0].Status)
}

func TestHandlerMintsHostedReviewReadToken(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	source := &fakeAppTokenSource{token: appauth.InstallationToken{
		Token:       "ghs_read_token",
		ExpiresAt:   now.Add(time.Hour),
		Permissions: map[string]string{"contents": "read", "pull_requests": "read"},
	}}
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
		WorkerBranch:   "herd/worker/837",
		Metadata:       validReviewJobMetadata(),
	}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validReviewClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: source,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/review-read-token", nil)
	req.SetPathValue("job_id", "job-1")
	req.Header.Set("Authorization", "Bearer oidc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(9), source.installationID)
	assert.Equal(t, []int64{3003}, source.repositoryIDs)
	require.NotNil(t, source.permissions)
	assert.Equal(t, "read", source.permissions.GetContents())
	assert.Equal(t, "read", source.permissions.GetPullRequests())
	assert.Equal(t, "read", source.permissions.GetIssues())
	assert.JSONEq(t, `{"token":"ghs_read_token","expires_at":"2026-07-11T13:00:00Z","permissions":{"contents":"read","pull_requests":"read"}}`, rec.Body.String())
}

func TestHandlerBindsHostedReviewReadTokenRunIDOnFirstExchange(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	source := &fakeAppTokenSource{token: appauth.InstallationToken{
		Token:     "ghs_read_token",
		ExpiresAt: now.Add(time.Hour),
	}}
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
		WorkerBranch:   "herd/worker/837",
		Metadata:       json.RawMessage(`{"kind":"review","repository":"acme/widgets","github_repository_id":3003,"ref":"refs/heads/herd/worker/837","workflow_file":"herd-review.yml"}`),
	}
	claims := validReviewClaims(now)
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(claims),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: source,
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, readTokenRequest())

	require.Equal(t, http.StatusOK, first.Code)
	updated, err := st.GetJob(context.Background(), "job-1")
	require.NoError(t, err)
	assert.Contains(t, string(updated.Metadata), `"workflow_run_id":"12345"`)

	otherClaims := claims
	otherClaims.RunID = "67890"
	secondHandler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(otherClaims),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: source,
	})
	second := httptest.NewRecorder()
	secondHandler.ServeHTTP(second, readTokenRequest())

	require.Equal(t, http.StatusUnauthorized, second.Code)
	assert.Contains(t, second.Body.String(), "run ID")
	assert.Equal(t, int64(9), source.installationID)
	assert.Equal(t, []int64{3003}, source.repositoryIDs)
}

func TestHandlerConcurrentHostedReviewReadTokenRunIDBindMintsOnce(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	source := &concurrentReadTokenSource{token: appauth.InstallationToken{
		Token:     "ghs_read_token",
		ExpiresAt: now.Add(time.Hour),
	}}
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
		WorkerBranch:   "herd/worker/837",
		Metadata:       json.RawMessage(`{"kind":"review","repository":"acme/widgets","github_repository_id":3003,"ref":"refs/heads/herd/worker/837","workflow_file":"herd-review.yml"}`),
	}
	firstClaims := validReviewClaims(now)
	secondClaims := firstClaims
	secondClaims.RunID = "67890"
	first := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(firstClaims),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: source,
	})
	second := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(secondClaims),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: source,
	})
	recorders := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		recorders[0] = httptest.NewRecorder()
		first.ServeHTTP(recorders[0], readTokenRequest())
	}()
	go func() {
		defer wg.Done()
		recorders[1] = httptest.NewRecorder()
		second.ServeHTTP(recorders[1], readTokenRequest())
	}()
	wg.Wait()

	codes := []int{recorders[0].Code, recorders[1].Code}
	assert.Contains(t, codes, http.StatusOK)
	assert.Contains(t, []int{http.StatusConflict, http.StatusUnauthorized}, nonOKStatus(codes))
	updated, err := st.GetJob(context.Background(), "job-1")
	require.NoError(t, err)
	assert.Contains(t, string(updated.Metadata), `"workflow_run_id":`)
	assert.Equal(t, 1, source.mintCount)
}

func nonOKStatus(codes []int) int {
	for _, code := range codes {
		if code != http.StatusOK {
			return code
		}
	}
	return 0
}

func TestHandlerRejectsHostedReviewReadTokenWithoutRepositoryScope(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
		WorkerBranch:   "herd/worker/837",
		Metadata:       json.RawMessage(`{"kind":"review","repository":"acme/widgets","ref":"refs/heads/herd/worker/837","workflow_file":"herd-review.yml","workflow_run_id":"12345"}`),
	}
	source := &fakeAppTokenSource{}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validReviewClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: source,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/review-read-token", nil)
	req.SetPathValue("job_id", "job-1")
	req.Header.Set("Authorization", "Bearer oidc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "repository ID")
	assert.Zero(t, source.installationID)
	assert.Empty(t, source.repositoryIDs)
}

func readTokenRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/review-read-token", nil)
	req.SetPathValue("job_id", "job-1")
	req.Header.Set("Authorization", "Bearer oidc")
	return req
}

func TestHandlerRejectsHostedReviewReadTokenWithoutEligibleJob(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", InstallationID: 9, Metadata: validReviewJobMetadata()}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		AppTokenSource: &fakeAppTokenSource{},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/review-read-token", nil)
	req.SetPathValue("job_id", "job-1")
	req.Header.Set("Authorization", "Bearer oidc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "not eligible")
}

func TestHandlerRejectsHostedReviewReadTokenForNonReviewJobs(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{name: "worker job", metadata: validJobMetadataWith(map[string]any{"kind": "worker"})},
		{name: "integrator job", metadata: validJobMetadataWith(map[string]any{"kind": "integrator"})},
		{name: "monitor job", metadata: validJobMetadataWith(map[string]any{"kind": "monitor"})},
		{name: "review kind wrong workflow", metadata: validJobMetadataWith(map[string]any{"kind": "review"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
			st := newResultStore()
			source := &fakeAppTokenSource{}
			st.jobs["job-1"] = store.Job{
				JobID:          "job-1",
				RepositoryID:   7,
				InstallationID: 9,
				PRNumber:       42,
				HeadSHA:        "head",
				WorkerBranch:   "herd/worker/837",
				Metadata:       tt.metadata,
			}
			handler := NewHandler(HandlerOptions{
				Store:          st,
				Validator:      fixedOIDCValidator(validClaims(now)),
				Audience:       "herd-control-plane",
				Now:            func() time.Time { return now },
				AppTokenSource: source,
			})

			req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/review-read-token", nil)
			req.SetPathValue("job_id", "job-1")
			req.Header.Set("Authorization", "Bearer oidc")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusConflict, rec.Code)
			assert.Zero(t, source.installationID)
			assert.Empty(t, source.repositoryIDs)
		})
	}
}

func TestHandlerDuplicateReviewCallbackUsesStableIdentityAcrossJSONFormatting(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head", Metadata: validJobMetadata()}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	secondPayload := `{
		"summary":"review summary",
		"status":"approved",
		"head_sha":"head",
		"pr_number":42,
		"batch_number":106,
		"job_id":"job-1",
		"repository":"acme/widgets",
		"kind":"review_completed",
		"version":1
	}`

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", validReviewPayload()))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", secondPayload))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Contains(t, second.Body.String(), `"created":false`)
	assert.Len(t, processor.calls, 1)
	assert.Len(t, st.results, 1)
}

func TestHandlerRejectsChangedReviewResultAfterAcceptance(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head", Metadata: validJobMetadata()}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	changedPayload := strings.Replace(validReviewPayload(), `"summary":"review summary"`, `"summary":"corrected review summary"`, 1)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", validReviewPayload()))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", changedPayload))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "conflicting review result")
	require.Len(t, processor.calls, 1)
	assert.Equal(t, "review summary", processor.calls[0].result.Summary)
	assert.Len(t, st.results, 1)
}

func TestHandlerStartedCallbackDoesNotProcessAgain(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head", Metadata: validJobMetadata()}
	payload := validReviewPayload()
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	st.idem["job_result:"+resultKey] = store.IdempotencyKey{
		Key:       "job_result:" + resultKey,
		Scope:     "job_result_callback",
		Status:    "started",
		ResultRef: resultKey,
		Metadata:  json.RawMessage(`{"job_id":"job-1"}`),
		CreatedAt: now,
	}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "repair required")
	assert.Empty(t, processor.calls)
	assert.Empty(t, st.results)
}

func TestHandlerFailedCallbackDoesNotProcessAgain(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	payload := validReviewPayload()
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	st.idem["job_result:"+resultKey] = store.IdempotencyKey{
		Key:       "job_result:" + resultKey,
		Scope:     "job_result_callback",
		Status:    "failed",
		ResultRef: "previous failure",
		Metadata:  json.RawMessage(`{"job_id":"job-1"}`),
		CreatedAt: now,
	}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "repair required")
	assert.Empty(t, processor.calls)
	assert.Empty(t, st.results)
}

func TestHandlerRejectsReviewCompletedPRNumberMismatch(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
	}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	payload := strings.Replace(validReviewPayload(), `"pr_number":42`, `"pr_number":99`, 1)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "pr_number")
	assert.Empty(t, processor.calls)
	assert.Empty(t, st.results)
}

func TestHandlerPassesDisabledReviewMetadata(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		PRNumber:       42,
		HeadSHA:        "head",
		Metadata:       validJobMetadataWith(map[string]any{"integrator": map[string]any{"review": false}}),
	}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validReviewPayload()))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, processor.calls, 1)
	assert.False(t, processor.calls[0].repo.ReviewEnabled)
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
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "new", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{Store: st, Validator: fixedOIDCValidator(validClaims(time.Now().Add(time.Hour)))})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "old")))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Empty(t, st.results)
}

func TestHandlerRejectsResultRepositoryDifferentFromJobRepository(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837", Metadata: json.RawMessage(`{"repository":"acme/widgets","ref":"refs/heads/herd/worker/837","workflow_file":"worker.yml","workflow_run_id":"12345"}`)}
	claims := validClaims(now)
	claims.Repository = "evil/fork"
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedOIDCValidator(claims),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
	})
	payload := `{"version":1,"kind":"worker_completed","repository":"evil/fork","job_id":"job-1","batch_number":106,"issue_number":837,"target_branch":"herd/worker/837","base_sha":"base","expected_head_sha":"head","patch_artifact":"patches/job.diff","status":"success"}`

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "result repository does not match job")
	assert.Empty(t, st.results)
	assert.Empty(t, st.mutationAttempts)
}

func TestHandlerRejectsWorkerSuccessWhenPatchDependenciesMissing(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		opts          HandlerOptions
		directHandler bool
		want          string
	}{
		{name: "artifact store", opts: HandlerOptions{PatchApplier: fixedPatchApplier{}, AppTokenSource: fakeAppTokenSource{}}, want: "artifact store"},
		{name: "patch applier", opts: HandlerOptions{ArtifactStore: artifactStore{}, AppTokenSource: fakeAppTokenSource{}}, directHandler: true, want: "patch applier"},
		{name: "app token source", opts: HandlerOptions{ArtifactStore: artifactStore{}, PatchApplier: fixedPatchApplier{}}, want: "App token source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newResultStore()
			st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
			tt.opts.Store = st
			tt.opts.Validator = fixedOIDCValidator(validClaims(now))
			tt.opts.Now = func() time.Time { return now }
			handler := NewHandler(tt.opts)
			if tt.directHandler {
				handler = Handler{
					store:          tt.opts.Store,
					validator:      tt.opts.Validator,
					audience:       controlplane.DefaultOIDCAudience,
					now:            tt.opts.Now,
					artifactStore:  tt.opts.ArtifactStore,
					appTokenSource: tt.opts.AppTokenSource,
				}
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

			require.Equal(t, http.StatusInternalServerError, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.want)
			assert.Empty(t, st.results)
			assert.Empty(t, st.mutationAttempts)
		})
	}
}

func TestHandlerRejectsWorkerResultTargetBranchMismatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedOIDCValidator(validClaims(now)),
		Now:       func() time.Time { return now },
	})
	payload := `{"version":1,"kind":"worker_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"issue_number":837,"target_branch":"main","base_sha":"base","expected_head_sha":"head","patch_artifact":"patches/job.diff","status":"success"}`

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "target_branch")
	assert.Empty(t, st.results)
	assert.Empty(t, st.mutationAttempts)
}

func TestHandlerRejectsPatchForDifferentRepositoryAndRecordsFailure(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/other", "job-1", "base", "head", "patch.diff", patch)
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   fixedPatchApplier{},
		AppTokenSource: fakeAppTokenSource{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusFailure, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), "patch repository does not match result repository")
	assert.Empty(t, st.mutationCompletions)
}

func TestHandlerRecordsFailureWhenPatchArtifactMissingFromBundle(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "missing.patch", patch)
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore{"worker-branch": workerBranchMetadataOnlyArtifact(t, metadata)},
		PatchApplier:   fixedPatchApplier{},
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := `{"version":1,"kind":"worker_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"issue_number":837,"target_branch":"herd/worker/837","base_sha":"base","expected_head_sha":"head","patch_artifact":"worker-branch","status":"success"}`

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusFailure, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), "missing from artifact bundle")
	assert.Empty(t, st.mutationCompletions)
}

func TestHandlerRetriesTerminalPatchValidationWhenFailureResultCannotBeRecorded(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.recordJobResultErrs = []error{assert.AnError, nil}
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/other", "job-1", "base", "head", "patch.diff", patch)
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   fixedPatchApplier{},
		AppTokenSource: fakeAppTokenSource{},
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", validWorkerPayload("job-1", "head")))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	assert.Contains(t, first.Body.String(), "record rejected job result")
	require.Equal(t, http.StatusConflict, second.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusFailure, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), "patch repository does not match result repository")
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
		WorkerBranch:   "herd/worker/837",
		Metadata:       validJobMetadataWith(map[string]any{"requester_name": "Mona", "requester_email": "mona@example.com"}),
	}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	applier := fixedPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
		AppLogin:       "herd-os[bot]",
		AppEmail:       "herd@example.com",
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusSuccess, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), artifacts.FormatGitDiffBinary)
	require.Len(t, st.mutationCompletions, 2)
	assert.Equal(t, "call_started", st.mutationCompletions[0].status)
	assert.Equal(t, "completed", st.mutationCompletions[1].status)
	assert.Contains(t, string(st.mutationCompletions[1].response), strings.Repeat("a", 40))
}

func TestHandlerDuplicateSuccessDoesNotFetchExpiredPatchArtifact(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		HeadSHA:        "head",
		BaseSHA:        "base",
		WorkerBranch:   "herd/worker/837",
	}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	artifactStore := &flakyArtifactStore{store: artifactMap(t, metadata, patch)}
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore,
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
		AppLogin:       "herd-os[bot]",
		AppEmail:       "herd@example.com",
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	require.Equal(t, http.StatusAccepted, first.Code)

	artifactStore.errs = []error{errors.New("artifact expired")}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Contains(t, second.Body.String(), `"created":false`)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
	assert.Len(t, artifactStore.errs, 1, "duplicate should return before artifact fetch")
}

func TestHandlerRejectsChangedWorkerPatchArtifactAfterAcceptance(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		HeadSHA:        "head",
		BaseSHA:        "base",
		WorkerBranch:   "herd/worker/837",
	}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	firstMetadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	secondMetadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "alternate.diff", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore{"patches/job.diff": patchMetadataArtifact(t, firstMetadata), "patch.diff": patch, "patches/alternate.diff": patchMetadataArtifact(t, secondMetadata), "alternate.diff": patch},
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
		AppLogin:       "herd-os[bot]",
		AppEmail:       "herd@example.com",
	})
	secondPayload := strings.Replace(validWorkerPayload("job-1", "head"), `"patch_artifact":"patches/job.diff"`, `"patch_artifact":"patches/alternate.diff"`, 1)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", validWorkerPayload("job-1", "head")))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", secondPayload))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "conflicting worker result")
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
}

func TestHandlerAppliesBundledWorkerBranchArtifactAndRecordsCommitSHA(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{
		JobID:          "job-1",
		RepositoryID:   7,
		InstallationID: 9,
		HeadSHA:        "head",
		BaseSHA:        "base",
		WorkerBranch:   "herd/worker/837",
		Metadata:       validJobMetadataWith(map[string]any{"requester_name": "Mona", "requester_email": "mona@example.com"}),
	}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "herd-worker.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("b", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore{"worker-branch": workerBranchArtifact(t, metadata, patch)},
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	payload := `{"version":1,"kind":"worker_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"issue_number":837,"target_branch":"herd/worker/837","base_sha":"base","expected_head_sha":"head","patch_artifact":"worker-branch","status":"success"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, applier.requests, 1)
	assert.Equal(t, patch, applier.requests[0].Artifact.Data)
	assert.Equal(t, "herd-worker.patch", applier.requests[0].Artifact.Metadata.ArtifactName)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusSuccess, st.results[0].Status)
	require.Len(t, st.mutationCompletions, 2)
	assert.Equal(t, "call_started", st.mutationCompletions[0].status)
	assert.Equal(t, "completed", st.mutationCompletions[1].status)
	assert.Contains(t, string(st.mutationCompletions[1].response), strings.Repeat("b", 40))
}

func TestHandlerIgnoresPatchArtifactOnFailureResult(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "herd-worker.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("c", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore{"worker-branch": workerBranchArtifact(t, metadata, patch)},
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	payload := `{"version":1,"kind":"worker_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"issue_number":837,"target_branch":"herd/worker/837","base_sha":"base","expected_head_sha":"head","patch_artifact":"worker-branch","status":"failure"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, applier.requests)
	assert.Empty(t, st.mutationAttempts)
	assert.Empty(t, st.mutationCompletions)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusFailure, st.results[0].Status)
	assert.NotContains(t, string(st.results[0].Metadata), "patch_apply")
	assert.NotContains(t, string(st.results[0].Metadata), strings.Repeat("c", 40))
}

func TestHandlerAcceptsEmptyPatchArtifactWithoutApplying(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patch := []byte{}
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	applier := fixedPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Audience:       "herd-control-plane",
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusSuccess, st.results[0].Status)
	assert.Contains(t, string(st.results[0].Metadata), `"empty":true`)
	require.Len(t, st.mutationCompletions, 1)
	assert.Equal(t, "completed", st.mutationCompletions[0].status)
	assert.Contains(t, string(st.mutationCompletions[0].response), `"empty":true`)
}

func TestHandlerRejectsMissingBearerToken(t *testing.T) {
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
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
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{Store: st, Validator: errOIDCValidator{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, st.results)
}

func TestHandlerRejectsCallbackWhenHostedOIDCIdentityMetadataMissing(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		metadata json.RawMessage
		wantErr  string
	}{
		{name: "missing ref", metadata: json.RawMessage(`{"repository":"acme/widgets","workflow_file":"worker.yml","workflow_run_id":"12345"}`), wantErr: "ref"},
		{name: "missing workflow", metadata: json.RawMessage(`{"repository":"acme/widgets","ref":"refs/heads/herd/worker/837","workflow_run_id":"12345"}`), wantErr: "workflow"},
		{name: "missing run ID", metadata: json.RawMessage(`{"repository":"acme/widgets","ref":"refs/heads/herd/worker/837","workflow_file":"worker.yml"}`), wantErr: "workflow_run_id"},
		{name: "malformed metadata", metadata: json.RawMessage(`{`), wantErr: "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newResultStore()
			st.jobs["job-1"] = store.Job{
				JobID:        "job-1",
				RepositoryID: 7,
				HeadSHA:      "head",
				BaseSHA:      "base",
				WorkerBranch: "herd/worker/837",
				Metadata:     tt.metadata,
			}
			artifactStore := &flakyArtifactStore{errs: []error{errors.New("artifact should not be fetched")}}
			handler := NewHandler(HandlerOptions{
				Store:         st,
				Validator:     fixedOIDCValidator(validClaims(now)),
				Audience:      "herd-control-plane",
				Now:           func() time.Time { return now },
				ArtifactStore: artifactStore,
				PatchApplier:  fixedPatchApplier{},
			})

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

			require.Equal(t, http.StatusConflict, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantErr)
			assert.Empty(t, st.results)
			assert.Len(t, artifactStore.errs, 1)
		})
	}
}

func TestHandlerDuplicateWorkerPatchAppliesOnce(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("d", 40)}}
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Contains(t, first.Body.String(), `"created":true`)
	assert.Contains(t, second.Body.String(), `"created":false`)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
}

func TestHandlerTransientPatchArtifactValidationRetryRecordsSuccess(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("b", 40)}}
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  &flakyArtifactStore{store: artifactMap(t, metadata, patch), errs: []error{fmt.Errorf("artifact unavailable")}},
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	require.Equal(t, http.StatusConflict, first.Code)
	assert.Empty(t, st.results)

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, st.results, 1)
	assert.Equal(t, StatusSuccess, st.results[0].Status)
	assert.Len(t, applier.requests, 1)
	assert.Contains(t, string(st.results[0].Metadata), strings.Repeat("b", 40))
}

func TestHandlerRetriesWorkerPatchAfterApplyFailure(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{
		result: artifacts.ApplyResult{CommitSHA: strings.Repeat("e", 40)},
		errs:   []error{assert.AnError, nil},
	}
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	require.Equal(t, http.StatusAccepted, third.Code)
	assert.Len(t, applier.requests, 2)
	assert.Equal(t, 1, applier.pushes)
	assert.Len(t, st.results, 1)
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("e", 40))
}

func TestHandlerDoesNotRetryWorkerPatchAfterPushFailure(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{
		result:  artifacts.ApplyResult{CommitSHA: strings.Repeat("e", 40)},
		pushErr: assert.AnError,
	}
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "repair required")
	assert.Len(t, applier.requests, 1)
	assert.Equal(t, 1, applier.pushes)
	assert.Empty(t, st.results)
}

func TestHandlerRetryAfterPatchMutationAttemptRecordFailureRecordsBeforeApply(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("9", 40)}}
	st := newResultStore()
	st.recordMutationErrs = []error{assert.AnError, nil}
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, first.Code)
	assert.Contains(t, first.Body.String(), "record patch mutation attempt")
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.mutationAttempts, 1)
	assert.Len(t, st.results, 1)
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("9", 40))
}

func TestHandlerIntentRecordedPatchApplyWithoutMutationAttemptRecordsBeforeApply(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("7", 40)}}
	st := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patchKey := patchApplyKeyForTest(t, payload, job)
	st.idem[patchKey] = store.IdempotencyKey{
		Key:       patchKey,
		Scope:     "patch_apply",
		Status:    mutationspkg.PhaseIntentRecorded,
		ResultRef: "job-1",
		CreatedAt: now,
	}
	st.jobs["job-1"] = job
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.mutationAttempts, 1)
	assert.Equal(t, mutationspkg.PhaseCompleted, st.mutationAttempts[0].Status)
	require.Len(t, st.mutationCompletions, 2)
	assert.Equal(t, mutationspkg.PhaseCallStarted, st.mutationCompletions[0].status)
	assert.Equal(t, mutationspkg.PhaseCompleted, st.mutationCompletions[1].status)
	require.Len(t, st.results, 1)
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("7", 40))
}

func TestHandlerRetryAfterRecordJobResultFailureDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("f", 40)}}
	st := newResultStore()
	st.recordJobResultErrs = []error{assert.AnError, nil}
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	artifactStore := &flakyArtifactStore{store: artifactMap(t, metadata, patch), errs: []error{nil, nil, fmt.Errorf("artifact expired")}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore,
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})
	payload := validWorkerPayload("job-1", "head")

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, artifactStore.errs, 1, "redelivery should replay the completed patch apply without fetching the expired artifact")
	assert.Len(t, st.results, 1)
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("f", 40))
}

func TestHandlerRetryAfterPatchApplyCompletionFailureDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	st := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	st.completeIdemErrs = map[string][]error{patchApplyKeyForTest(t, payload, job): {assert.AnError, nil}}
	st.jobs["job-1"] = job
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "repair required")
	assert.Len(t, applier.requests, 1)
	assert.Empty(t, st.results)
	assert.Equal(t, "intent_recorded", st.idem[patchApplyKeyForTest(t, payload, job)].Status)
}

func TestHandlerRetryAfterPatchApplyCompletionFailureWithoutMutationReaderDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	inner := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	inner.completeIdemErrs = map[string][]error{patchApplyKeyForTest(t, payload, job): {assert.AnError}}
	inner.jobs["job-1"] = job
	st := mutationRecorderOnlyResultStore{inner: inner}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "repair required")
	assert.Empty(t, applier.requests)
	assert.Empty(t, inner.results)
}

func TestHandlerRejectsWorkerSuccessWithoutMutationTrackingBeforeArtifactFetch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	inner := newResultStore()
	inner.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	artifactStore := &flakyArtifactStore{store: artifactMap(t, metadata, patch), errs: []error{fmt.Errorf("artifact should not be fetched")}}
	handler := NewHandler(HandlerOptions{
		Store:          noMutationResultStore{inner: inner},
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore,
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "mutation recorder is not configured")
	assert.Len(t, artifactStore.errs, 1, "the queued artifact error should remain unused when artifacts are not fetched")
	assert.Empty(t, applier.requests)
	assert.Empty(t, inner.results)
}

func TestHandlerRetryAfterPatchMutationCompletionFailureDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	st := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	patchKey := patchApplyKeyForTest(t, payload, job)
	st.mutationCompleteErrs = []error{assert.AnError}
	st.jobs["job-1"] = job
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusConflict, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "repair required")
	assert.Len(t, applier.requests, 1)
	assert.Empty(t, st.results)
	require.Equal(t, "completed", st.idem[patchKey].Status)
	attempt, err := st.GetGitHubMutationAttempt(context.Background(), patchKey)
	require.NoError(t, err)
	assert.Equal(t, mutationspkg.PhaseCallStarted, attempt.Status)
}

func TestHandlerRetryAfterCompleteCallbackFailureDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	st := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	st.completeIdemErrs = map[string][]error{"job_result:" + ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload)): []error{assert.AnError, nil}}
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("a", 40))
}

func TestHandlerRetryAfterWorkerAcceptanceCompletionFailureDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	st := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	st.jobs["job-1"] = job
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	acceptanceKey := workerResultAcceptanceKey(job)
	st.completeIdemErrs = map[string][]error{acceptanceKey: {assert.AnError, nil}}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactMap(t, metadata, patch),
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
	assert.Equal(t, "completed", st.idem[acceptanceKey].Status)
	assert.Equal(t, "completed", st.idem["job_result:"+resultKey].Status)
}

func TestHandlerRepairsDanglingWorkerAcceptanceOnCompletedCallbackRedelivery(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	payload := validWorkerPayload("job-1", "head")
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 99, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
	st.jobs["job-1"] = job
	acceptanceKey := workerResultAcceptanceKey(job)
	st.idem["job_result:"+resultKey] = store.IdempotencyKey{Key: "job_result:" + resultKey, Scope: "job_result_callback", Status: mutationspkg.PhaseCompleted, ResultRef: resultKey, CreatedAt: now}
	st.idem[acceptanceKey] = store.IdempotencyKey{Key: acceptanceKey, Scope: "worker_result_acceptance", Status: mutationspkg.PhaseIntentRecorded, ResultRef: resultKey, CreatedAt: now}
	applier := &recordingPatchApplier{}
	handler := NewHandler(HandlerOptions{
		Store:          st,
		Validator:      fixedOIDCValidator(validClaims(now)),
		Now:            func() time.Time { return now },
		ArtifactStore:  artifactStore{},
		PatchApplier:   applier,
		AppTokenSource: fakeAppTokenSource{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, applier.requests)
	assert.Empty(t, st.results)
	assert.Equal(t, "completed", st.idem[acceptanceKey].Status)
}

func assertJobResultCommitSHA(t *testing.T, result store.JobResult, want string) {
	t.Helper()
	var metadata struct {
		PatchApply struct {
			CommitSHA string `json:"commit_sha"`
		} `json:"patch_apply"`
	}
	require.NoError(t, json.Unmarshal(result.Metadata, &metadata))
	assert.Equal(t, want, metadata.PatchApply.CommitSHA)
}

type mutationRecorderOnlyResultStore struct {
	inner *resultStore
}

func (s mutationRecorderOnlyResultStore) GetJob(ctx context.Context, jobID string) (store.Job, error) {
	return s.inner.GetJob(ctx, jobID)
}

func (s mutationRecorderOnlyResultStore) RecordJobResult(ctx context.Context, r store.JobResult) (bool, error) {
	return s.inner.RecordJobResult(ctx, r)
}

func (s mutationRecorderOnlyResultStore) GetJobResult(ctx context.Context, jobID string, idempotencyKey string) (store.JobResult, error) {
	return s.inner.GetJobResult(ctx, jobID, idempotencyKey)
}

func (s mutationRecorderOnlyResultStore) AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (bool, error) {
	return s.inner.AcquireIdempotencyKey(ctx, key)
}

func (s mutationRecorderOnlyResultStore) GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error) {
	return s.inner.GetIdempotencyKey(ctx, key)
}

func (s mutationRecorderOnlyResultStore) CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error {
	return s.inner.CompleteIdempotencyKey(ctx, key, resultRef)
}

func (s mutationRecorderOnlyResultStore) FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error {
	return s.inner.FailIdempotencyKey(ctx, key, errorMessage)
}

func (s mutationRecorderOnlyResultStore) RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error {
	return s.inner.RecordGitHubMutationAttempt(ctx, a)
}

func (s mutationRecorderOnlyResultStore) CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	return s.inner.CompleteGitHubMutationAttempt(ctx, idempotencyKey, status, response, errorMessage, completedAt)
}

func (s mutationRecorderOnlyResultStore) TryStartGitHubMutationAttempt(ctx context.Context, idempotencyKey string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error) {
	return s.inner.TryStartGitHubMutationAttempt(ctx, idempotencyKey, allowedStatuses, completedAt)
}

type noMutationResultStore struct {
	inner *resultStore
}

func (s noMutationResultStore) GetJob(ctx context.Context, jobID string) (store.Job, error) {
	return s.inner.GetJob(ctx, jobID)
}

func (s noMutationResultStore) RecordJobResult(ctx context.Context, r store.JobResult) (bool, error) {
	return s.inner.RecordJobResult(ctx, r)
}

func (s noMutationResultStore) GetJobResult(ctx context.Context, jobID string, idempotencyKey string) (store.JobResult, error) {
	return s.inner.GetJobResult(ctx, jobID, idempotencyKey)
}

func (s noMutationResultStore) AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (bool, error) {
	return s.inner.AcquireIdempotencyKey(ctx, key)
}

func (s noMutationResultStore) GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error) {
	return s.inner.GetIdempotencyKey(ctx, key)
}

func (s noMutationResultStore) CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error {
	return s.inner.CompleteIdempotencyKey(ctx, key, resultRef)
}

func (s noMutationResultStore) FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error {
	return s.inner.FailIdempotencyKey(ctx, key, errorMessage)
}

func TestHandlerDoesNotRetryReviewResultAfterProcessorFailure(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	processor := &capturingReviewProcessor{errs: []error{assert.AnError, nil}}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	payload := validReviewPayload()

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "repair required")
	assert.Len(t, processor.calls, 1)
	require.Len(t, st.mutationCompletions, 2)
	assert.Equal(t, mutationspkg.PhaseCallStarted, st.mutationCompletions[0].status)
	assert.Equal(t, mutationspkg.PhaseRepairRequired, st.mutationCompletions[1].status)
	assert.Empty(t, st.results)
}

func TestHandlerRetriesReviewResultAfterProcessorPreCallFailure(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	processor := &capturingReviewProcessor{prepareErrs: []error{mutationspkg.PreCallError{Op: "create client", Err: assert.AnError}, nil}}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	payload := validReviewPayload()

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, processor.calls, 2)
	require.Len(t, st.mutationCompletions, 3)
	assert.Equal(t, mutationspkg.PhaseFailedPreCall, st.mutationCompletions[0].status)
	assert.Equal(t, mutationspkg.PhaseCallStarted, st.mutationCompletions[1].status)
	assert.Equal(t, mutationspkg.PhaseCompleted, st.mutationCompletions[2].status)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusApproved, st.results[0].Status)
}

func TestAcquireResultCallbackRetriesLegacyFailedPreCallMarker(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		status        string
		resultRef     string
		wantProcess   bool
		wantErrSubstr string
	}{
		{name: "legacy failed pre call marker retries", status: mutationspkg.LegacyFailed, resultRef: mutationspkg.PhaseFailedPreCall + ":temporary validation failure", wantProcess: true},
		{name: "generic failed does not retry", status: mutationspkg.LegacyFailed, resultRef: "github-visible result unknown", wantProcess: false, wantErrSubstr: "repair required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newResultStore()
			callbackKey := "job_result:callback"
			st.idem[callbackKey] = store.IdempotencyKey{
				Key:       callbackKey,
				Scope:     "job_result_callback",
				Status:    tt.status,
				ResultRef: tt.resultRef,
				CreatedAt: now,
			}
			handler := Handler{store: st, now: func() time.Time { return now }}

			shouldProcess, err := handler.acquireResultCallback(context.Background(), callbackKey, "job-1", "result-1")

			if tt.wantErrSubstr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSubstr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantProcess, shouldProcess)
		})
	}
}

func TestHandlerRetryAfterReviewAcceptanceCompletionFailureDoesNotResubmit(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	st.jobs["job-1"] = job
	payload := validReviewPayload()
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	acceptanceKey := reviewResultAcceptanceKey(job)
	st.completeIdemErrs = map[string][]error{acceptanceKey: {assert.AnError, nil}}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, processor.calls, 1)
	assert.Len(t, st.results, 1)
	assert.Equal(t, "completed", st.idem[acceptanceKey].Status)
	assert.Equal(t, "completed", st.idem["job_result:"+resultKey].Status)
}

func TestHandlerChangedReviewResultRepairsPriorAcceptedMutationWithoutResubmit(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.recordJobResultErrs = []error{assert.AnError, nil}
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	st.jobs["job-1"] = job
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	firstPayload := validReviewPayload()
	changedPayload := `{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"pr_number":42,"head_sha":"head","status":"approved","summary":"changed review summary"}`
	firstResultKey := ResultIdempotencyKey(parsedResultPayload(t, firstPayload), []byte(firstPayload))
	acceptanceKey := reviewResultAcceptanceKey(job)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", firstPayload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", changedPayload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Len(t, processor.calls, 1)
	require.Len(t, st.results, 1)
	assert.Equal(t, firstResultKey, st.results[0].IdempotencyKey)
	assert.Equal(t, StatusApproved, st.results[0].Status)
	assert.Equal(t, ResultPayloadHash([]byte(firstPayload)), st.results[0].ResultRef)
	assert.Equal(t, "completed", st.idem[acceptanceKey].Status)
	assert.Equal(t, firstResultKey, st.idem[acceptanceKey].ResultRef)
}

func TestHandlerChangedReviewResultRepairsPriorPostCallUnknownMutationWithoutResubmit(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	st.jobs["job-1"] = job
	firstPayload := validReviewPayload()
	changedPayload := `{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"pr_number":42,"head_sha":"head","status":"approved","summary":"changed review summary"}`
	firstResult := mustParseReviewPayload(t, firstPayload)
	firstResultKey := ResultIdempotencyKey(firstResult, []byte(firstPayload))
	mutationKey := reviewResultMutationKey(firstResult, job)
	acceptanceKey := reviewResultAcceptanceKey(job)
	st.mutationCompleteErrs = []error{errors.New("mutation store down"), nil}
	st.completeIdemErrs = map[string][]error{mutationKey: {errors.New("idempotency store down"), nil}}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", firstPayload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", changedPayload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "conflicting review result already accepted")
	assert.Len(t, processor.calls, 1)
	require.Len(t, processor.repairs, 1)
	assert.Equal(t, "review summary", processor.repairs[0].result.Summary)
	require.Len(t, st.results, 1)
	assert.Equal(t, firstResultKey, st.results[0].IdempotencyKey)
	assert.Equal(t, ResultPayloadHash([]byte(firstPayload)), st.results[0].ResultRef)
	assert.Equal(t, mutationspkg.PhaseCompleted, st.idem[acceptanceKey].Status)
	assert.Equal(t, firstResultKey, st.idem[acceptanceKey].ResultRef)
	attempt, err := st.GetGitHubMutationAttempt(context.Background(), mutationKey)
	require.NoError(t, err)
	assert.Equal(t, mutationspkg.PhaseCompleted, attempt.Status)
	assert.Equal(t, mutationspkg.PhaseCompleted, st.idem[mutationKey].Status)
}

func TestHandlerRepairsDanglingReviewAcceptanceOnCompletedCallbackRedelivery(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	payload := validReviewPayload()
	resultKey := ResultIdempotencyKey(parsedResultPayload(t, payload), []byte(payload))
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	st.jobs["job-1"] = job
	acceptanceKey := reviewResultAcceptanceKey(job)
	st.idem["job_result:"+resultKey] = store.IdempotencyKey{Key: "job_result:" + resultKey, Scope: "job_result_callback", Status: mutationspkg.PhaseCompleted, ResultRef: resultKey, CreatedAt: now}
	st.idem[acceptanceKey] = store.IdempotencyKey{Key: acceptanceKey, Scope: "review_result_acceptance", Status: mutationspkg.PhaseIntentRecorded, ResultRef: resultKey, CreatedAt: now}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", payload))

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, processor.calls)
	assert.Empty(t, st.results)
	assert.Equal(t, "completed", st.idem[acceptanceKey].Status)
}

func TestHandlerConcurrentDuplicateReviewResultsSubmitOnce(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	processor := newBlockingReviewProcessor()
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	payload := validReviewPayload()

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, resultRequest("job-1", payload))
		firstDone <- rec
	}()
	require.Eventually(t, func() bool { return processor.CallCount() == 1 }, time.Second, 10*time.Millisecond)

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))
	processor.Release()
	first := <-firstDone

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusInternalServerError, second.Code)
	assert.Contains(t, second.Body.String(), "process review result")
	assert.Equal(t, 1, processor.CallCount())
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusApproved, st.results[0].Status)
}

func TestHandlerReviewResultCompletionPersistenceFailureDoesNotResubmit(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	st.mutationCompleteErrs = []error{errors.New("mutation store down")}
	processor := &capturingReviewProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:           st,
		Validator:       fixedOIDCValidator(validClaims(now)),
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ReviewProcessor: processor,
	})
	payload := validReviewPayload()

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, resultRequest("job-1", payload))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "repair required")
	assert.Len(t, processor.calls, 1)
	assert.Empty(t, st.results)
	key := reviewResultMutationKey(mustParseReviewPayload(t, payload), st.jobs["job-1"])
	require.Contains(t, st.idem, key)
	assert.Equal(t, "completed", st.idem[key].Status)
	assert.Equal(t, "review_result:processed", st.idem[key].ResultRef)
}

func TestProcessReviewResultRepairsAfterMutationAndIdempotencyCompletionFailures(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	job := store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	result := mustParseReviewPayload(t, validReviewPayload())
	key := reviewResultMutationKey(result, job)
	st.mutationCompleteErrs = []error{errors.New("mutation store down")}
	st.completeIdemErrs = map[string][]error{key: {errors.New("idempotency store down"), nil}}
	processor := &capturingReviewProcessor{}
	handler := Handler{
		store:           st,
		now:             func() time.Time { return now },
		reviewProcessor: processor,
	}

	firstErr := handler.processReviewResult(context.Background(), result, job)
	secondErr := handler.processReviewResult(context.Background(), result, job)

	require.Error(t, firstErr)
	require.NoError(t, secondErr)
	assert.Len(t, processor.calls, 1)
	assert.Len(t, processor.repairs, 1)
	require.Contains(t, st.idem, key)
	assert.Equal(t, mutationspkg.PhaseCompleted, st.idem[key].Status)
	assert.Equal(t, "review_result:processed", st.idem[key].ResultRef)
	attempt, err := st.GetGitHubMutationAttempt(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, mutationspkg.PhaseCompleted, attempt.Status)
}

func TestHandlerRejectsReviewResultWhenProcessorMissing(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedOIDCValidator(validClaims(now)),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validReviewPayload()))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "process review result")
	assert.Empty(t, st.results)
}

type resultStore struct {
	mu      sync.Mutex
	jobs    map[string]store.Job
	results []store.JobResult
	seen    map[string]struct{}
	idem    map[string]store.IdempotencyKey

	mutationAttempts     []store.GitHubMutationAttempt
	mutationCompletions  []mutationCompletion
	mutationCompleteErrs []error
	recordJobResultErrs  []error
	completeIdemErrs     map[string][]error
	recordMutationErrs   []error
}

func newResultStore() *resultStore {
	return &resultStore{
		jobs: map[string]store.Job{},
		seen: map[string]struct{}{},
		idem: map[string]store.IdempotencyKey{},
	}
}

func (s *resultStore) GetJob(_ context.Context, jobID string) (store.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return store.Job{}, store.ErrNotFound
	}
	job.Metadata = withDefaultJobRepositoryMetadata(job.Metadata)
	return job, nil
}

func (s *resultStore) UpdateJobStatus(_ context.Context, jobID string, status string, metadata json.RawMessage, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return store.ErrNotFound
	}
	job.Status = status
	job.Metadata = metadata
	job.UpdatedAt = updatedAt
	s.jobs[jobID] = job
	return nil
}

func (s *resultStore) BindJobWorkflowRunID(_ context.Context, jobID string, runID string, updatedAt time.Time) (store.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return store.Job{}, false, store.ErrNotFound
	}
	metadata := metadataMap(job.Metadata)
	existing := firstMetadataString(metadata, "workflow_run_id")
	if existing != "" && existing != runID {
		return job, false, nil
	}
	metadata["workflow_run_id"] = runID
	out, err := json.Marshal(metadata)
	if err != nil {
		return store.Job{}, false, err
	}
	job.Metadata = out
	job.UpdatedAt = updatedAt
	s.jobs[jobID] = job
	return job, true, nil
}

func withDefaultJobRepositoryMetadata(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return validJobMetadata()
	}
	metadata := map[string]any{}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return raw
	}
	metadata["repository"] = "acme/widgets"
	out, _ := json.Marshal(metadata)
	return out
}

func (s *resultStore) RecordJobResult(_ context.Context, result store.JobResult) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recordJobResultErrs) > 0 {
		err := s.recordJobResultErrs[0]
		s.recordJobResultErrs = s.recordJobResultErrs[1:]
		if err != nil {
			return false, err
		}
	}
	key := result.JobID + "\x00" + result.IdempotencyKey
	if _, ok := s.seen[key]; ok {
		return false, nil
	}
	s.seen[key] = struct{}{}
	s.results = append(s.results, result)
	return true, nil
}

func (s *resultStore) GetJobResult(_ context.Context, jobID string, idempotencyKey string) (store.JobResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, result := range s.results {
		if result.JobID == jobID && result.IdempotencyKey == idempotencyKey {
			return result, nil
		}
	}
	return store.JobResult{}, store.ErrNotFound
}

func (s *resultStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idem[key.Key]; ok {
		return false, nil
	}
	s.idem[key.Key] = key
	return true, nil
}

func (s *resultStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idem[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *resultStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.completeIdemErrs[key]) > 0 {
		err := s.completeIdemErrs[key][0]
		s.completeIdemErrs[key] = s.completeIdemErrs[key][1:]
		if err != nil {
			return err
		}
	}
	record, ok := s.idem[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "completed"
	record.ResultRef = resultRef
	record.CompletedAt = &now
	s.idem[key] = record
	return nil
}

func (s *resultStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idem[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status, record.ResultRef = normalizeFailedIdempotencyForTest(errorMessage)
	record.CompletedAt = &now
	s.idem[key] = record
	return nil
}

func normalizeFailedIdempotencyForTest(errorMessage string) (string, string) {
	if rest, ok := strings.CutPrefix(errorMessage, mutationspkg.PhaseFailedPreCall+":"); ok {
		return mutationspkg.PhaseFailedPreCall, rest
	}
	if rest, ok := strings.CutPrefix(errorMessage, mutationspkg.PhaseRepairRequired+":"); ok {
		return mutationspkg.PhaseRepairRequired, rest
	}
	return mutationspkg.LegacyFailed, errorMessage
}

func (s *resultStore) RecordGitHubMutationAttempt(_ context.Context, attempt store.GitHubMutationAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recordMutationErrs) > 0 {
		err := s.recordMutationErrs[0]
		s.recordMutationErrs = s.recordMutationErrs[1:]
		if err != nil {
			return err
		}
	}
	for _, existing := range s.mutationAttempts {
		if existing.IdempotencyKey == attempt.IdempotencyKey {
			return store.ErrAlreadyExists
		}
	}
	s.mutationAttempts = append(s.mutationAttempts, attempt)
	return nil
}

func (s *resultStore) CompleteGitHubMutationAttempt(_ context.Context, key string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mutationCompleteErrs) > 0 {
		err := s.mutationCompleteErrs[0]
		s.mutationCompleteErrs = s.mutationCompleteErrs[1:]
		if err != nil {
			return err
		}
	}
	s.mutationCompletions = append(s.mutationCompletions, mutationCompletion{
		key:          key,
		status:       status,
		response:     response,
		errorMessage: errorMessage,
		completedAt:  completedAt,
	})
	for i, attempt := range s.mutationAttempts {
		if attempt.IdempotencyKey == key {
			attempt.Status = status
			attempt.Response = response
			attempt.Error = errorMessage
			attempt.CompletedAt = &completedAt
			s.mutationAttempts[i] = attempt
			break
		}
	}
	return nil
}

func (s *resultStore) TryStartGitHubMutationAttempt(_ context.Context, key string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, attempt := range s.mutationAttempts {
		if attempt.IdempotencyKey == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return store.GitHubMutationStartResult{}, store.ErrNotFound
	}
	attempt := s.mutationAttempts[idx]
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
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	attempt.Status = "call_started"
	attempt.Response = json.RawMessage(`{}`)
	attempt.Error = ""
	attempt.CompletedAt = &completedAt
	s.mutationAttempts[idx] = attempt
	s.mutationCompletions = append(s.mutationCompletions, mutationCompletion{
		key:         key,
		status:      "call_started",
		response:    json.RawMessage(`{}`),
		completedAt: completedAt,
	})
	return store.GitHubMutationStartResult{Started: true, Attempt: attempt}, nil
}

func (s *resultStore) GetGitHubMutationAttempt(_ context.Context, key string) (store.GitHubMutationAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.mutationCompletions) - 1; i >= 0; i-- {
		completion := s.mutationCompletions[i]
		if completion.key == key {
			return store.GitHubMutationAttempt{
				IdempotencyKey: key,
				Status:         completion.status,
				Response:       completion.response,
				Error:          completion.errorMessage,
				CompletedAt:    &completion.completedAt,
			}, nil
		}
	}
	for _, attempt := range s.mutationAttempts {
		if attempt.IdempotencyKey == key {
			return attempt, nil
		}
	}
	return store.GitHubMutationAttempt{}, store.ErrNotFound
}

type mutationCompletion struct {
	key          string
	status       string
	response     json.RawMessage
	errorMessage string
	completedAt  time.Time
}

type capturingReviewProcessor struct {
	calls       []reviewProcessorCall
	repairs     []reviewProcessorCall
	prepareErrs []error
	errs        []error
	repairErrs  []error
}

type reviewProcessorCall struct {
	repo   review.Repository
	result review.ReviewCompletedResult
}

func (p *capturingReviewProcessor) PrepareSubmitReviewResult(_ context.Context, repo review.Repository, result review.ReviewCompletedResult) (review.PreparedReviewResultSubmission, error) {
	p.calls = append(p.calls, reviewProcessorCall{repo: repo, result: result})
	if len(p.prepareErrs) > 0 {
		err := p.prepareErrs[0]
		p.prepareErrs = p.prepareErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return reviewSubmissionFunc(func(context.Context) error {
		if len(p.errs) > 0 {
			err := p.errs[0]
			p.errs = p.errs[1:]
			return err
		}
		return nil
	}), nil
}

func (p *capturingReviewProcessor) RepairSubmittedReviewResult(_ context.Context, repo review.Repository, result review.ReviewCompletedResult) (bool, error) {
	p.repairs = append(p.repairs, reviewProcessorCall{repo: repo, result: result})
	if len(p.repairErrs) > 0 {
		err := p.repairErrs[0]
		p.repairErrs = p.repairErrs[1:]
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

type reviewSubmissionFunc func(context.Context) error

func (f reviewSubmissionFunc) Submit(ctx context.Context) error {
	return f(ctx)
}

func (p *capturingReviewProcessor) SubmitReviewResult(_ context.Context, repo review.Repository, result review.ReviewCompletedResult) error {
	_, err := p.PrepareSubmitReviewResult(context.Background(), repo, result)
	if err != nil {
		return err
	}
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return err
	}
	return nil
}

type blockingReviewProcessor struct {
	mu       sync.Mutex
	calls    int
	entered  chan struct{}
	released chan struct{}
}

func newBlockingReviewProcessor() *blockingReviewProcessor {
	return &blockingReviewProcessor{
		entered:  make(chan struct{}),
		released: make(chan struct{}),
	}
}

func (p *blockingReviewProcessor) PrepareSubmitReviewResult(context.Context, review.Repository, review.ReviewCompletedResult) (review.PreparedReviewResultSubmission, error) {
	return reviewSubmissionFunc(p.submit), nil
}

func (p *blockingReviewProcessor) submit(context.Context) error {
	p.mu.Lock()
	p.calls++
	if p.calls == 1 {
		close(p.entered)
	}
	p.mu.Unlock()
	<-p.released
	return nil
}

func (p *blockingReviewProcessor) SubmitReviewResult(ctx context.Context, _ review.Repository, _ review.ReviewCompletedResult) error {
	return p.submit(ctx)
}

func (p *blockingReviewProcessor) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *blockingReviewProcessor) Release() {
	close(p.released)
}

type artifactStore map[string][]byte

func artifactMap(t *testing.T, metadata artifacts.PatchMetadata, patch []byte) artifactStore {
	t.Helper()
	return artifactStore{
		"patches/job.diff":    patchMetadataArtifact(t, metadata),
		metadata.ArtifactName: patch,
	}
}

func patchMetadataArtifact(t *testing.T, metadata artifacts.PatchMetadata) []byte {
	t.Helper()
	metadataBytes, err := json.Marshal(metadata)
	require.NoError(t, err)
	return metadataBytes
}

func (s artifactStore) OpenArtifact(_ context.Context, name string) (io.ReadCloser, error) {
	data, ok := s[name]
	if !ok {
		return nil, fmt.Errorf("missing artifact")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type flakyArtifactStore struct {
	store artifactStore
	errs  []error
}

func (s *flakyArtifactStore) OpenArtifact(ctx context.Context, name string) (io.ReadCloser, error) {
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return s.store.OpenArtifact(ctx, name)
}

type fixedPatchApplier struct {
	result artifacts.ApplyResult
	err    error
}

type fakeAppTokenSource struct {
	token          appauth.InstallationToken
	installationID int64
	repositoryIDs  []int64
	permissions    *gh.InstallationPermissions
}

func (s fakeAppTokenSource) InstallationToken(context.Context, int64) (appauth.InstallationToken, error) {
	if strings.TrimSpace(s.token.Token) != "" {
		return s.token, nil
	}
	return appauth.InstallationToken{Token: "token"}, nil
}

func (s *fakeAppTokenSource) InstallationTokenWithPermissions(_ context.Context, installationID int64, repositoryIDs []int64, permissions gh.InstallationPermissions) (appauth.InstallationToken, error) {
	s.installationID = installationID
	s.repositoryIDs = append([]int64(nil), repositoryIDs...)
	s.permissions = &permissions
	if strings.TrimSpace(s.token.Token) != "" {
		return s.token, nil
	}
	return appauth.InstallationToken{Token: "token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type concurrentReadTokenSource struct {
	mu        sync.Mutex
	token     appauth.InstallationToken
	mintCount int
}

func (s *concurrentReadTokenSource) InstallationToken(context.Context, int64) (appauth.InstallationToken, error) {
	if strings.TrimSpace(s.token.Token) != "" {
		return s.token, nil
	}
	return appauth.InstallationToken{Token: "token"}, nil
}

func (s *concurrentReadTokenSource) InstallationTokenWithPermissions(context.Context, int64, []int64, gh.InstallationPermissions) (appauth.InstallationToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mintCount++
	if strings.TrimSpace(s.token.Token) != "" {
		return s.token, nil
	}
	return appauth.InstallationToken{Token: "token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (a fixedPatchApplier) Prepare(context.Context, artifacts.ApplyRequest) (artifacts.PreparedApply, error) {
	if a.err != nil {
		return artifacts.PreparedApply{}, a.err
	}
	return artifacts.NewPreparedApply(a.result.CommitSHA, func() error { return nil }, nil), nil
}

type recordingPatchApplier struct {
	requests []artifacts.ApplyRequest
	result   artifacts.ApplyResult
	err      error
	errs     []error
	pushErr  error
	pushErrs []error
	pushes   int
}

func (a *recordingPatchApplier) Prepare(_ context.Context, req artifacts.ApplyRequest) (artifacts.PreparedApply, error) {
	a.requests = append(a.requests, req)
	if len(a.errs) > 0 {
		err := a.errs[0]
		a.errs = a.errs[1:]
		if err != nil {
			return artifacts.PreparedApply{}, err
		}
	}
	if a.err != nil {
		return artifacts.PreparedApply{}, a.err
	}
	return artifacts.NewPreparedApply(a.result.CommitSHA, func() error {
		a.pushes++
		if len(a.pushErrs) > 0 {
			err := a.pushErrs[0]
			a.pushErrs = a.pushErrs[1:]
			return err
		}
		return a.pushErr
	}, nil), nil
}

func workerBranchArtifact(t *testing.T, metadata artifacts.PatchMetadata, patch []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string][]byte{
		"herd-worker-metadata.json": mustJSON(t, metadata),
		metadata.ArtifactName:       patch,
	}
	for name, data := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func workerBranchMetadataOnlyArtifact(t *testing.T, metadata artifacts.PatchMetadata) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("herd-worker-metadata.json")
	require.NoError(t, err)
	_, err = w.Write(mustJSON(t, metadata))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
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
		Issuer:      GitHubActionsIssuer,
		Audience:    []string{"herd-control-plane"},
		Repository:  "acme/widgets",
		Ref:         "refs/heads/herd/worker/837",
		Workflow:    "worker.yml",
		WorkflowRef: "acme/widgets/.github/workflows/worker.yml@refs/heads/herd/worker/837",
		RunID:       "12345",
		ExpiresAt:   now.Add(time.Hour),
	}
}

func validReviewClaims(now time.Time) OIDCClaims {
	claims := validClaims(now)
	claims.Workflow = "herd-review.yml"
	claims.WorkflowRef = "acme/widgets/.github/workflows/herd-review.yml@refs/heads/herd/worker/837"
	return claims
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

func parsedResultPayload(t *testing.T, payload string) Result {
	t.Helper()
	result, err := ParseResultPayload([]byte(payload))
	require.NoError(t, err)
	return result
}

func patchApplyKeyForTest(t *testing.T, payload string, job store.Job) string {
	t.Helper()
	result := parsedResultPayload(t, payload)
	worker, ok := result.(WorkerCompletedResult)
	require.True(t, ok)
	return PatchApplyIdempotencyKey(worker, job)
}

func mustParseReviewPayload(t *testing.T, payload string) ReviewCompletedResult {
	t.Helper()
	result := parsedResultPayload(t, payload)
	reviewResult, ok := result.(ReviewCompletedResult)
	require.True(t, ok)
	return reviewResult
}

func validReviewPayload() string {
	return `{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"pr_number":42,"head_sha":"head","status":"approved","summary":"review summary"}`
}
