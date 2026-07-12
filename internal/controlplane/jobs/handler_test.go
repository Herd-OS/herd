package jobs

import (
	"archive/zip"
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

	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
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
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837", Metadata: json.RawMessage(`{"ref":"refs/heads/herd/worker/837","workflow_file":"worker.yml","workflow_run_id":"12345"}`)}
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

func TestHandlerDuplicateCallbacksAreIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	patch := []byte{}
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
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

func TestHandlerDuplicateWorkerCallbackUsesStableIdentityAcrossJSONFormatting(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, HeadSHA: "head", BaseSHA: "base", WorkerBranch: "herd/worker/837"}
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
		Metadata:       json.RawMessage(`{"workflow_run_url":"https://example.test/run"}`),
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
	assert.Equal(t, review.Repository{ID: 7, InstallationID: 9, Owner: "acme", Name: "widgets", ReviewEnabled: true}, processor.calls[0].repo)
	assert.Equal(t, 42, processor.calls[0].result.PRNumber)
	assert.Equal(t, "head", processor.calls[0].result.HeadSHA)
	assert.Equal(t, "https://example.test/run", processor.calls[0].result.TargetURL)
	require.Len(t, st.results, 1)
	assert.Equal(t, StatusApproved, st.results[0].Status)
}

func TestHandlerDuplicateReviewCallbackUsesStableIdentityAcrossJSONFormatting(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := newResultStore()
	st.jobs["job-1"] = store.Job{JobID: "job-1", RepositoryID: 7, InstallationID: 9, PRNumber: 42, HeadSHA: "head"}
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
		Metadata:       json.RawMessage(`{"integrator":{"review":false}}`),
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
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", WorkerBranch: "herd/worker/837", Metadata: json.RawMessage(`{"repository":"acme/widgets","ref":"refs/heads/herd/worker/837","workflow_file":"worker.yml","workflow_run_id":"12345"}`)}
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
		Metadata:       json.RawMessage(`{"requester_name":"Mona","requester_email":"mona@example.com"}`),
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
	require.Len(t, st.mutationCompletions, 1)
	assert.Equal(t, "completed", st.mutationCompletions[0].status)
	assert.Contains(t, string(st.mutationCompletions[0].response), strings.Repeat("a", 40))
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
		Metadata:       json.RawMessage(`{"requester_name":"Mona","requester_email":"mona@example.com"}`),
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
	require.Len(t, st.mutationCompletions, 1)
	assert.Contains(t, string(st.mutationCompletions[0].response), strings.Repeat("b", 40))
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
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", WorkerBranch: "herd/worker/837"}
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
	st.jobs["job-1"] = store.Job{JobID: "job-1", HeadSHA: "head", WorkerBranch: "herd/worker/837"}
	handler := NewHandler(HandlerOptions{Store: st, Validator: errOIDCValidator{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, resultRequest("job-1", validWorkerPayload("job-1", "head")))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, st.results)
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
	assert.Contains(t, second.Body.String(), `"created":true`)
	assert.Contains(t, third.Body.String(), `"created":false`)
	assert.Len(t, applier.requests, 2)
	assert.Len(t, st.results, 1)
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

func TestHandlerRetryAfterRecordJobResultFailureDoesNotReapplyPatch(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := artifacts.BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)
	applier := &recordingPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("f", 40)}}
	st := newResultStore()
	st.recordJobResultErrs = []error{assert.AnError, nil}
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

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
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
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
	assert.Equal(t, "completed", st.idem[patchApplyKeyForTest(t, payload, job)].Status)
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("a", 40))
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

	require.Equal(t, http.StatusConflict, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "unknown outcome")
	assert.Len(t, applier.requests, 1)
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
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Len(t, applier.requests, 1)
	assert.Len(t, st.results, 1)
	require.Equal(t, "completed", st.idem[patchKey].Status)
	assert.Contains(t, st.idem[patchKey].ResultRef, strings.Repeat("a", 40))
	assertJobResultCommitSHA(t, st.results[0], strings.Repeat("a", 40))
	attempt, err := st.GetGitHubMutationAttempt(context.Background(), patchKey)
	require.NoError(t, err)
	assert.Equal(t, "completed", attempt.Status)
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

func TestHandlerRetriesReviewResultAfterProcessorFailure(t *testing.T) {
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
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, resultRequest("job-1", payload))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	require.Equal(t, http.StatusAccepted, third.Code)
	assert.Contains(t, second.Body.String(), `"created":true`)
	assert.Contains(t, third.Body.String(), `"created":false`)
	assert.Len(t, processor.calls, 2)
	assert.Len(t, st.results, 1)
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

func withDefaultJobRepositoryMetadata(raw json.RawMessage) json.RawMessage {
	metadata := map[string]any{"repository": "acme/widgets"}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &metadata)
		metadata["repository"] = "acme/widgets"
	}
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
	record.Status = "failed"
	record.ResultRef = errorMessage
	record.CompletedAt = &now
	s.idem[key] = record
	return nil
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
	return nil
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
	calls []reviewProcessorCall
	errs  []error
}

type reviewProcessorCall struct {
	repo   review.Repository
	result review.ReviewCompletedResult
}

func (p *capturingReviewProcessor) SubmitReviewResult(_ context.Context, repo review.Repository, result review.ReviewCompletedResult) error {
	p.calls = append(p.calls, reviewProcessorCall{repo: repo, result: result})
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return err
	}
	return nil
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

type fakeAppTokenSource struct{}

func (fakeAppTokenSource) InstallationToken(context.Context, int64) (appauth.InstallationToken, error) {
	return appauth.InstallationToken{Token: "token"}, nil
}

func (a fixedPatchApplier) Apply(context.Context, artifacts.ApplyRequest) (artifacts.ApplyResult, error) {
	return a.result, a.err
}

type recordingPatchApplier struct {
	requests []artifacts.ApplyRequest
	result   artifacts.ApplyResult
	err      error
	errs     []error
}

func (a *recordingPatchApplier) Apply(_ context.Context, req artifacts.ApplyRequest) (artifacts.ApplyResult, error) {
	a.requests = append(a.requests, req)
	if len(a.errs) > 0 {
		err := a.errs[0]
		a.errs = a.errs[1:]
		return a.result, err
	}
	return a.result, a.err
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

func validReviewPayload() string {
	return `{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":106,"pr_number":42,"head_sha":"head","status":"approved","summary":"review summary"}`
}
