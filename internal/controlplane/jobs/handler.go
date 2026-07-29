package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/mutationguard"
	mutationspkg "github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
)

const maxResultPayloadBytes = 1 << 20

type Store interface {
	GetJob(ctx context.Context, jobID string) (store.Job, error)
	RecordJobResult(ctx context.Context, r store.JobResult) (created bool, err error)
	GetJobResult(ctx context.Context, jobID string, idempotencyKey string) (store.JobResult, error)
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
}

type MutationRecorder interface {
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
}

type MutationStarter interface {
	TryStartGitHubMutationAttempt(ctx context.Context, idempotencyKey string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error)
}

type MutationReader interface {
	GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error)
}

type JobStatusUpdater interface {
	UpdateJobStatus(ctx context.Context, jobID string, status string, metadata json.RawMessage, updatedAt time.Time) error
}

type WorkflowRunBinder interface {
	BindJobWorkflowRunID(ctx context.Context, jobID string, runID string, updatedAt time.Time) (store.Job, bool, error)
}

type ReadTokenSource interface {
	InstallationTokenWithPermissions(ctx context.Context, installationID int64, repositoryIDs []int64, permissions gh.InstallationPermissions) (appauth.InstallationToken, error)
}

type PatchApplier interface {
	Prepare(ctx context.Context, req artifacts.ApplyRequest) (artifacts.PreparedApply, error)
}

type ReviewProcessor interface {
	PrepareSubmitReviewResult(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult) (review.PreparedReviewResultSubmission, error)
}

type ReviewResultRepairer interface {
	RepairSubmittedReviewResult(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult) (bool, error)
}

type defaultPatchApplier struct{}

func (defaultPatchApplier) Prepare(ctx context.Context, req artifacts.ApplyRequest) (artifacts.PreparedApply, error) {
	return artifacts.Prepare(ctx, req)
}

type Handler struct {
	store           Store
	validator       OIDCValidator
	audience        string
	now             func() time.Time
	artifactStore   artifacts.Store
	patchApplier    PatchApplier
	appTokenSource  appauth.TokenSource
	appLogin        string
	appEmail        string
	tempDir         string
	reviewProcessor ReviewProcessor
}

type HandlerOptions struct {
	Store           Store
	Validator       OIDCValidator
	Audience        string
	Now             func() time.Time
	ArtifactStore   artifacts.Store
	PatchApplier    PatchApplier
	AppTokenSource  appauth.TokenSource
	AppLogin        string
	AppEmail        string
	TempDir         string
	ReviewProcessor ReviewProcessor
}

func NewHandler(opts HandlerOptions) http.Handler {
	audience := strings.TrimSpace(opts.Audience)
	if audience == "" {
		audience = controlplane.DefaultOIDCAudience
	}
	validator := opts.Validator
	if validator == nil {
		validator = NewJWKSValidator(audience)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	patchApplier := opts.PatchApplier
	if patchApplier == nil && opts.ArtifactStore != nil {
		patchApplier = defaultPatchApplier{}
	}
	return Handler{
		store:           opts.Store,
		validator:       validator,
		audience:        audience,
		now:             now,
		artifactStore:   opts.ArtifactStore,
		patchApplier:    patchApplier,
		appTokenSource:  opts.AppTokenSource,
		appLogin:        opts.AppLogin,
		appEmail:        opts.AppEmail,
		tempDir:         opts.TempDir,
		reviewProcessor: opts.ReviewProcessor,
	}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job result storage is not configured"})
		return
	}
	if h.validator == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "OIDC validator is not configured"})
		return
	}

	pathJobID := strings.TrimSpace(r.PathValue("job_id"))
	if pathJobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job_id path parameter is required"})
		return
	}
	if r.Method == http.MethodGet {
		h.serveReadToken(w, r, pathJobID)
		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxResultPayloadBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "result payload is invalid"})
		return
	}
	result, err := ParseResultPayload(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	envelope := result.Envelope()
	if envelope.JobID != pathJobID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path job_id does not match result job_id"})
		return
	}

	job, err := h.store.GetJob(r.Context(), pathJobID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup job"})
		return
	}
	if err := validateResultAgainstJob(result, job); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	expected, err := StrictExpectedIdentityFromJob(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if envelope.Repository != expected.Repository {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "result repository does not match job"})
		return
	}

	token, err := BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	claims, err := h.validator.Validate(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "validate OIDC token"})
		return
	}
	if err := ValidateOIDCClaims(claims, expected, OIDCOptions{Audience: h.audience, Now: h.now}); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	idempotencyKey := ResultIdempotencyKey(result, payload)
	callbackKey := "job_result:" + idempotencyKey
	shouldProcess, err := h.acquireResultCallback(r.Context(), callbackKey, envelope.JobID, idempotencyKey)
	if err != nil {
		if errors.Is(err, errResultCallbackRepairRequired) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "acquire job result idempotency"})
		return
	}
	if !shouldProcess {
		if err := h.repairResultAcceptance(r.Context(), result, job, idempotencyKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "repair result acceptance"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":          "accepted",
			"created":         false,
			"job_id":          envelope.JobID,
			"kind":            envelope.Kind,
			"idempotency_key": idempotencyKey,
		})
		return
	}

	acceptedWorkerKey, err := h.acquireWorkerResultAcceptance(r.Context(), result, job, idempotencyKey)
	if err != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, err.Error())
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	var applyMetadata map[string]any
	patchReplayed, replayMetadata, replayErr := h.replayCompletedWorkerPatch(r.Context(), result, job)
	if replayErr != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, replayErr.Error())
		writeJSON(w, http.StatusConflict, map[string]string{"error": replayErr.Error()})
		return
	}
	applyMetadata = replayMetadata
	patchArtifact, validationMetadata, applyErr := h.validateWorkerPatch(r.Context(), result, job, patchReplayed)
	if validationMetadata != nil && !patchReplayed {
		applyMetadata = validationMetadata
	}
	if applyErr != nil {
		if workerPatchConfigurationError(applyErr) {
			_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, applyErr.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": applyErr.Error()})
			return
		}
		if transientPatchValidationError(applyErr) {
			_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, mutationspkg.PhaseFailedPreCall+":"+applyErr.Error())
			writeJSON(w, http.StatusConflict, map[string]string{"error": applyErr.Error()})
			return
		}
		metadata, metadataErr := resultMetadata(payload, claims, applyMetadata)
		if metadataErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build job result metadata"})
			return
		}
		_, err = h.store.RecordJobResult(r.Context(), store.JobResult{
			JobID:          envelope.JobID,
			IdempotencyKey: idempotencyKey,
			Status:         StatusFailure,
			ResultRef:      ResultPayloadHash(payload),
			Metadata:       metadata,
			CreatedAt:      h.now(),
		})
		if err != nil {
			_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, mutationspkg.PhaseFailedPreCall+":"+err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record rejected job result"})
			return
		}
		if err := h.store.CompleteIdempotencyKey(r.Context(), callbackKey, idempotencyKey); err != nil {
			_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete rejected job result idempotency"})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": applyErr.Error()})
		return
	}
	if !patchReplayed {
		if applyErr := h.processWorkerPatch(r.Context(), result, job, patchArtifact, applyMetadata); applyErr != nil {
			message := applyErr.Error()
			var preCallErr mutationspkg.PreCallError
			if errors.As(applyErr, &preCallErr) {
				message = mutationspkg.PhaseFailedPreCall + ":" + message
			}
			_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, message)
			writeJSON(w, http.StatusConflict, map[string]string{"error": applyErr.Error()})
			return
		}
	}
	acceptedReviewKey, err := h.acquireReviewResultAcceptance(r.Context(), result, job, idempotencyKey, payload, claims, applyMetadata)
	if err != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, err.Error())
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err := h.processReviewResult(r.Context(), result, job); err != nil {
		message := err.Error()
		var preCallErr mutationspkg.PreCallError
		if errors.As(err, &preCallErr) {
			message = mutationspkg.PhaseFailedPreCall + ":" + message
		}
		_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, message)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "process review result"})
		return
	}
	metadata, err := resultMetadata(payload, claims, applyMetadata)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build job result metadata"})
		return
	}
	created, err := h.store.RecordJobResult(r.Context(), store.JobResult{
		JobID:          envelope.JobID,
		IdempotencyKey: idempotencyKey,
		Status:         result.StatusValue(),
		ResultRef:      ResultPayloadHash(payload),
		Metadata:       metadata,
		CreatedAt:      h.now(),
	})
	if err != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, mutationspkg.PhaseFailedPreCall+":"+err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record job result"})
		return
	}
	if acceptedReviewKey != "" {
		if err := h.store.CompleteIdempotencyKey(r.Context(), acceptedReviewKey, idempotencyKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete review result acceptance"})
			return
		}
	}
	if acceptedWorkerKey != "" {
		if err := h.store.CompleteIdempotencyKey(r.Context(), acceptedWorkerKey, idempotencyKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete worker result acceptance"})
			return
		}
	}
	if err := h.store.CompleteIdempotencyKey(r.Context(), callbackKey, idempotencyKey); err != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), callbackKey, err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete job result idempotency"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":          "accepted",
		"created":         created,
		"job_id":          envelope.JobID,
		"kind":            envelope.Kind,
		"idempotency_key": idempotencyKey,
	})
}

func (h Handler) acquireWorkerResultAcceptance(ctx context.Context, result Result, job store.Job, resultKey string) (string, error) {
	worker, ok := result.(WorkerCompletedResult)
	if !ok || worker.Status != StatusSuccess {
		return "", nil
	}
	// Worker success is accepted once per durable job/head/branch boundary.
	// Payload-specific callback keys still replay exact redeliveries, but a
	// changed artifact name must not create a second GitHub-visible patch push.
	key := workerResultAcceptanceKey(job)
	created, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "worker_result_acceptance",
		Status:    mutationspkg.PhaseIntentRecorded,
		ResultRef: resultKey,
		CreatedAt: h.now(),
	})
	if err != nil {
		return "", fmt.Errorf("acquire worker result acceptance: %w", err)
	}
	if created {
		return key, nil
	}
	record, err := h.store.GetIdempotencyKey(ctx, key)
	if err != nil {
		return "", fmt.Errorf("get worker result acceptance: %w", err)
	}
	if record.ResultRef == resultKey {
		if mutationspkg.IsPreCallRetryable(record.Status) {
			return key, nil
		}
		return "", nil
	}
	if record.Status == mutationspkg.PhaseCompleted || record.Status == "completed" {
		return "", fmt.Errorf("conflicting worker result already accepted for job %q", job.JobID)
	}
	if mutationspkg.IsPostCallUnknown(record.Status) {
		return "", fmt.Errorf("worker result acceptance for job %q is pending repair", job.JobID)
	}
	return "", fmt.Errorf("conflicting worker result is already pending for job %q", job.JobID)
}

func (h Handler) acquireReviewResultAcceptance(ctx context.Context, result Result, job store.Job, resultKey string, payload []byte, claims OIDCClaims, applyMetadata map[string]any) (string, error) {
	if _, ok := result.(ReviewCompletedResult); !ok {
		return "", nil
	}
	metadata, err := reviewResultAcceptanceMetadata(result, job, resultKey, payload, claims, applyMetadata)
	if err != nil {
		return "", err
	}
	key := reviewResultAcceptanceKey(job)
	created, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "review_result_acceptance",
		Status:    mutationspkg.PhaseIntentRecorded,
		ResultRef: resultKey,
		Metadata:  metadata,
		CreatedAt: h.now(),
	})
	if err != nil {
		return "", fmt.Errorf("acquire review result acceptance: %w", err)
	}
	if created {
		return key, nil
	}
	record, err := h.store.GetIdempotencyKey(ctx, key)
	if err != nil {
		return "", fmt.Errorf("get review result acceptance: %w", err)
	}
	if record.ResultRef == resultKey {
		if mutationspkg.IsPreCallRetryable(record.Status) {
			return key, nil
		}
		return "", nil
	}
	if repaired, err := h.repairPriorReviewResultAcceptance(ctx, key, record, job); err != nil {
		return "", err
	} else if repaired {
		return "", fmt.Errorf("conflicting review result already accepted for job %q", job.JobID)
	}
	if record.Status == mutationspkg.PhaseCompleted || record.Status == "completed" {
		return "", fmt.Errorf("conflicting review result already accepted for job %q", job.JobID)
	}
	if mutationspkg.IsPostCallUnknown(record.Status) {
		return "", fmt.Errorf("review result acceptance for job %q is pending repair", job.JobID)
	}
	return "", fmt.Errorf("conflicting review result is already pending for job %q", job.JobID)
}

type reviewAcceptanceMetadata struct {
	ResultKey         string          `json:"result_key"`
	MutationKey       string          `json:"mutation_key"`
	Status            string          `json:"status"`
	PayloadHash       string          `json:"payload_hash"`
	JobResultMetadata json.RawMessage `json:"job_result_metadata"`
	Submission        json.RawMessage `json:"submission"`
}

func reviewResultAcceptanceMetadata(result Result, job store.Job, resultKey string, payload []byte, claims OIDCClaims, applyMetadata map[string]any) (json.RawMessage, error) {
	jobResultMetadata, err := resultMetadata(payload, claims, applyMetadata)
	if err != nil {
		return nil, fmt.Errorf("build review acceptance metadata: %w", err)
	}
	submission, err := reviewResultSubmissionMetadata(result, job)
	if err != nil {
		return nil, fmt.Errorf("build review acceptance submission metadata: %w", err)
	}
	metadata, err := json.Marshal(reviewAcceptanceMetadata{
		ResultKey:         resultKey,
		MutationKey:       reviewResultMutationKey(result, job),
		Status:            result.StatusValue(),
		PayloadHash:       ResultPayloadHash(payload),
		JobResultMetadata: jobResultMetadata,
		Submission:        submission,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal review acceptance metadata: %w", err)
	}
	return metadata, nil
}

func reviewResultSubmissionMetadata(result Result, job store.Job) (json.RawMessage, error) {
	reviewResult, ok := result.(ReviewCompletedResult)
	if !ok {
		return nil, nil
	}
	submission := reviewResultSubmission(reviewResult, job)
	return json.Marshal(submission)
}

func (h Handler) repairPriorReviewResultAcceptance(ctx context.Context, acceptanceKey string, record store.IdempotencyKey, job store.Job) (bool, error) {
	var metadata reviewAcceptanceMetadata
	if len(record.Metadata) == 0 || json.Unmarshal(record.Metadata, &metadata) != nil {
		return false, nil
	}
	if strings.TrimSpace(metadata.ResultKey) == "" || strings.TrimSpace(metadata.MutationKey) == "" {
		return false, nil
	}
	if !h.reviewResultProcessCompleted(ctx, metadata.MutationKey) {
		repaired, err := h.repairAcceptedReviewResultMutation(ctx, metadata, job)
		if err != nil || !repaired {
			return false, err
		}
	}
	if err := h.recordRepairedReviewResult(ctx, metadata, job); err != nil {
		return false, err
	}
	if err := h.store.CompleteIdempotencyKey(ctx, acceptanceKey, metadata.ResultKey); err != nil {
		return false, fmt.Errorf("complete repaired review result acceptance: %w", err)
	}
	return true, nil
}

func (h Handler) repairAcceptedReviewResultMutation(ctx context.Context, metadata reviewAcceptanceMetadata, job store.Job) (bool, error) {
	if h.reviewProcessor == nil {
		return false, nil
	}
	mutationStore, ok := h.store.(mutationguard.Store)
	if !ok {
		return false, nil
	}
	var submission review.ReviewCompletedResult
	if len(metadata.Submission) == 0 || json.Unmarshal(metadata.Submission, &submission) != nil {
		return false, nil
	}
	if strings.TrimSpace(submission.Repository) == "" {
		return false, nil
	}
	repo := reviewRepositoryFromJob(job, submission.Repository)
	request := metadata.Submission
	_, err := mutationguard.Run(ctx, mutationStore, mutationguard.RunRequest{
		Key:          metadata.MutationKey,
		RepositoryID: job.RepositoryID,
		MutationType: "review_result_process",
		Request:      request,
		ResultRef: func(raw json.RawMessage) string {
			if len(raw) == 0 {
				return ""
			}
			return "review_result:processed"
		},
		Response: func(string) json.RawMessage { return request },
		Mutate: func() (string, error) {
			return "", fmt.Errorf("prior review result mutation must be repaired before retry")
		},
		Repair: func() (string, bool, error) {
			repairer, ok := h.reviewProcessor.(ReviewResultRepairer)
			if !ok {
				return "", false, nil
			}
			repaired, err := repairer.RepairSubmittedReviewResult(ctx, repo, submission)
			if err != nil || !repaired {
				return "", repaired, err
			}
			return "review_result:processed", true, nil
		},
		Now: h.now,
	})
	if err != nil {
		return false, err
	}
	return h.reviewResultProcessCompleted(ctx, metadata.MutationKey), nil
}

func (h Handler) recordRepairedReviewResult(ctx context.Context, metadata reviewAcceptanceMetadata, job store.Job) error {
	if len(metadata.JobResultMetadata) > 0 && strings.TrimSpace(metadata.Status) != "" && strings.TrimSpace(metadata.PayloadHash) != "" {
		if _, err := h.store.RecordJobResult(ctx, store.JobResult{
			JobID:          job.JobID,
			IdempotencyKey: metadata.ResultKey,
			Status:         metadata.Status,
			ResultRef:      metadata.PayloadHash,
			Metadata:       metadata.JobResultMetadata,
			CreatedAt:      h.now(),
		}); err != nil {
			return fmt.Errorf("record repaired review job result: %w", err)
		}
	}
	return nil
}

func (h Handler) reviewResultProcessCompleted(ctx context.Context, mutationKey string) bool {
	record, err := h.store.GetIdempotencyKey(ctx, mutationKey)
	if err == nil && mutationspkg.IsCompleted(record.Status) {
		return true
	}
	reader, ok := h.store.(MutationReader)
	if !ok {
		return false
	}
	attempt, err := reader.GetGitHubMutationAttempt(ctx, mutationKey)
	if err != nil || !mutationspkg.IsCompleted(attempt.Status) {
		return false
	}
	_ = h.store.CompleteIdempotencyKey(ctx, mutationKey, "review_result:processed")
	return true
}

func (h Handler) repairResultAcceptance(ctx context.Context, result Result, job store.Job, resultKey string) error {
	if _, ok := result.(ReviewCompletedResult); ok {
		if err := h.repairResultAcceptanceKey(ctx, reviewResultAcceptanceKey(job), resultKey); err != nil {
			return err
		}
	}
	if worker, ok := result.(WorkerCompletedResult); ok && worker.Status == StatusSuccess {
		if err := h.repairResultAcceptanceKey(ctx, workerResultAcceptanceKey(job), resultKey); err != nil {
			return err
		}
	}
	return nil
}

func (h Handler) repairResultAcceptanceKey(ctx context.Context, key string, resultKey string) error {
	record, err := h.store.GetIdempotencyKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.ResultRef != resultKey || !mutationspkg.IsPreCallRetryable(record.Status) {
		return nil
	}
	return h.store.CompleteIdempotencyKey(ctx, key, resultKey)
}

type ReviewReadTokenResponse struct {
	Token       string            `json:"token"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

func (h Handler) serveReadToken(w http.ResponseWriter, r *http.Request, jobID string) {
	if h.appTokenSource == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "GitHub App token source is not configured"})
		return
	}
	source, ok := h.appTokenSource.(ReadTokenSource)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "GitHub App read token source is not configured"})
		return
	}
	job, err := h.store.GetJob(r.Context(), jobID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup job"})
		return
	}
	if job.InstallationID == 0 || job.PRNumber <= 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is not eligible for hosted review read token"})
		return
	}
	if err := validateHostedReviewReadTokenJob(job); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	expected, err := ReadTokenExpectedIdentityFromJob(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	token, err := BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	claims, err := h.validator.Validate(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "validate OIDC token"})
		return
	}
	if err := ValidateOIDCClaims(claims, expected, OIDCOptions{Audience: h.audience, Now: h.now}); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if expected.RunID == "" {
		var bindErr error
		job, expected, bindErr = h.bindReadTokenRunID(r.Context(), job, claims.RunID)
		if bindErr != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": bindErr.Error()})
			return
		}
	}
	read := "read"
	githubRepositoryID := hostedReviewGitHubRepositoryID(job)
	if githubRepositoryID == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job GitHub repository ID is missing"})
		return
	}
	minted, err := source.InstallationTokenWithPermissions(r.Context(), job.InstallationID, []int64{githubRepositoryID}, gh.InstallationPermissions{
		Actions:      &read,
		Checks:       &read,
		Contents:     &read,
		Issues:       &read,
		Metadata:     &read,
		PullRequests: &read,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mint GitHub read token"})
		return
	}
	if strings.TrimSpace(minted.Token) == "" || minted.ExpiresAt.IsZero() || !h.now().Before(minted.ExpiresAt) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "minted GitHub read token is invalid"})
		return
	}
	writeJSON(w, http.StatusOK, ReviewReadTokenResponse{
		Token:       minted.Token,
		ExpiresAt:   minted.ExpiresAt,
		Permissions: minted.Permissions,
	})
}

func (h Handler) bindReadTokenRunID(ctx context.Context, job store.Job, runID string) (store.Job, ExpectedOIDCIdentity, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return store.Job{}, ExpectedOIDCIdentity{}, fmt.Errorf("OIDC run ID is required")
	}
	binder, ok := h.store.(WorkflowRunBinder)
	if !ok {
		return store.Job{}, ExpectedOIDCIdentity{}, fmt.Errorf("job OIDC run ID binder is not configured")
	}
	bound, ok, err := binder.BindJobWorkflowRunID(ctx, job.JobID, runID, h.now())
	if err != nil {
		return store.Job{}, ExpectedOIDCIdentity{}, fmt.Errorf("bind job OIDC run ID: %w", err)
	}
	if !ok {
		return store.Job{}, ExpectedOIDCIdentity{}, fmt.Errorf("OIDC run ID conflicts with job workflow_run_id")
	}
	expected, err := StrictExpectedIdentityFromJob(bound)
	if err != nil {
		return store.Job{}, ExpectedOIDCIdentity{}, err
	}
	return bound, expected, nil
}

func validateHostedReviewReadTokenJob(job store.Job) error {
	metadata := metadataMap(job.Metadata)
	if firstMetadataString(metadata, "kind", "job_kind") != "review" {
		return fmt.Errorf("job is not a hosted review job")
	}
	workflow := strings.TrimSpace(firstMetadataString(metadata, "workflow_file", "workflow"))
	if workflow != "herd-review.yml" && workflow != ".github/workflows/herd-review.yml" {
		return fmt.Errorf("job is not from the hosted review workflow")
	}
	return nil
}

func hostedReviewGitHubRepositoryID(job store.Job) int64 {
	metadata := metadataMap(job.Metadata)
	for _, key := range []string{"github_repository_id", "github_repo_id", "repository_github_id"} {
		switch v := metadata[key].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case json.Number:
			id, _ := v.Int64()
			return id
		case string:
			id, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return id
		}
	}
	return 0
}

func (h Handler) acquireResultCallback(ctx context.Context, callbackKey, jobID, resultKey string) (bool, error) {
	created, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       callbackKey,
		Scope:     "job_result_callback",
		Status:    mutationspkg.PhaseIntentRecorded,
		ResultRef: resultKey,
		Metadata:  json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, jobID)),
		CreatedAt: h.now(),
	})
	if err != nil {
		return false, err
	}
	if created {
		return true, nil
	}
	record, err := h.store.GetIdempotencyKey(ctx, callbackKey)
	if err != nil {
		return false, err
	}
	status := mutationspkg.Normalize(record.Status)
	switch {
	case status == mutationspkg.PhaseCompleted:
		return false, nil
	case mutationspkg.IsPreCallRetryableRecord(record.Status, record.ResultRef):
		return true, nil
	case mutationspkg.IsPostCallUnknown(status):
		if _, err := h.store.GetJobResult(ctx, jobID, resultKey); err == nil {
			if err := h.store.CompleteIdempotencyKey(ctx, callbackKey, resultKey); err != nil {
				return false, fmt.Errorf("repair completed job result callback: %w", err)
			}
			return false, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
		return false, fmt.Errorf("%w: job result callback %q is %s with no durable job result", errResultCallbackRepairRequired, callbackKey, status)
	default:
		return false, fmt.Errorf("job result callback %q has unknown status %q", callbackKey, record.Status)
	}
}

var errResultCallbackRepairRequired = errors.New("job result callback repair required")

func (h Handler) processReviewResult(ctx context.Context, result Result, job store.Job) error {
	reviewResult, ok := result.(ReviewCompletedResult)
	if !ok {
		return nil
	}
	if h.reviewProcessor == nil {
		return fmt.Errorf("review result processor is not configured")
	}
	mutationStore, ok := h.store.(mutationguard.Store)
	if !ok {
		return fmt.Errorf("review result mutation store is not configured")
	}
	mutationKey := reviewResultMutationKey(result, job)
	if _, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       mutationKey,
		Scope:     "review_result_process",
		Status:    mutationspkg.PhaseIntentRecorded,
		CreatedAt: h.now(),
	}); err != nil {
		return fmt.Errorf("acquire review result mutation idempotency key: %w", err)
	}
	repo := reviewRepositoryFromJob(job, reviewResult.Repository)
	submission := reviewResultSubmission(reviewResult, job)
	request, _ := json.Marshal(submission)
	var prepared review.PreparedReviewResultSubmission
	_, err := mutationguard.Run(ctx, mutationStore, mutationguard.RunRequest{
		Key:          mutationKey,
		RepositoryID: job.RepositoryID,
		MutationType: "review_result_process",
		Request:      request,
		ResultRef: func(raw json.RawMessage) string {
			if len(raw) == 0 {
				return ""
			}
			return "review_result:processed"
		},
		Response: func(string) json.RawMessage { return request },
		Preflight: func() error {
			var err error
			prepared, err = h.reviewProcessor.PrepareSubmitReviewResult(ctx, repo, submission)
			return err
		},
		Mutate: func() (string, error) {
			if prepared == nil {
				return "", mutationspkg.PreCallError{Op: "prepare review result submission", Err: fmt.Errorf("prepared review result submission is missing")}
			}
			if err := prepared.Submit(ctx); err != nil {
				return "", err
			}
			return "review_result:processed", nil
		},
		Repair: func() (string, bool, error) {
			repairer, ok := h.reviewProcessor.(ReviewResultRepairer)
			if !ok {
				return "", false, nil
			}
			repaired, err := repairer.RepairSubmittedReviewResult(ctx, repo, submission)
			if err != nil || !repaired {
				return "", repaired, err
			}
			return "review_result:processed", true, nil
		},
		Now: h.now,
	})
	return err
}

func reviewResultSubmission(reviewResult ReviewCompletedResult, job store.Job) review.ReviewCompletedResult {
	targetURL := firstMetadataString(metadataMap(job.Metadata), "workflow_run_url", "run_url", "target_url", "pr_url")
	return review.ReviewCompletedResult{
		Repository:  reviewResult.Repository,
		JobID:       reviewResult.JobID,
		BatchNumber: reviewResult.BatchNumber,
		PRNumber:    reviewResult.PRNumber,
		BatchBranch: job.WorkerBranch,
		HeadSHA:     reviewResult.HeadSHA,
		Status:      reviewResult.Status,
		Summary:     reviewResult.Summary,
		TargetURL:   targetURL,
		FixCycle:    reviewResult.FixCycle,
		Findings:    reviewFindings(reviewResult.Findings),
	}
}

func reviewResultMutationKey(result Result, job store.Job) string {
	parts := []string{
		job.JobID,
		fmt.Sprint(job.RepositoryID),
		fmt.Sprint(job.PRNumber),
		job.HeadSHA,
		result.StatusValue(),
	}
	if reviewResult, ok := result.(ReviewCompletedResult); ok {
		parts = append(parts, reviewResult.Repository, fmt.Sprint(reviewResult.BatchNumber), fmt.Sprint(reviewResult.PRNumber), reviewResult.HeadSHA, fmt.Sprint(reviewResult.FixCycle), canonicalReviewOutputIdentity(reviewResult))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "review_result:" + hex.EncodeToString(sum[:])
}

func reviewResultAcceptanceKey(job store.Job) string {
	parts := []string{
		job.JobID,
		fmt.Sprint(job.RepositoryID),
		fmt.Sprint(job.PRNumber),
		job.HeadSHA,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "review_result_acceptance:" + hex.EncodeToString(sum[:])
}

func workerResultAcceptanceKey(job store.Job) string {
	parts := []string{
		job.JobID,
		fmt.Sprint(job.RepositoryID),
		job.WorkerBranch,
		job.BaseSHA,
		job.HeadSHA,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "worker_result_acceptance:" + hex.EncodeToString(sum[:])
}

func reviewRepositoryFromJob(job store.Job, fullName string) review.Repository {
	owner, name, _ := strings.Cut(fullName, "/")
	enabled := true
	fixEnabled := false
	maxFixCycles := 0
	fixSeverity := ""
	metadata := metadataMap(job.Metadata)
	if v, ok := metadata["integrator_review"].(bool); ok {
		enabled = v
	}
	if v, ok := metadata["review_enabled"].(bool); ok {
		enabled = v
	}
	if integrator, ok := metadata["integrator"].(map[string]any); ok {
		if v, ok := integrator["review"].(bool); ok {
			enabled = v
		}
		if v, ok := integrator["review_fix_enabled"].(bool); ok {
			fixEnabled = v
		}
		if v, ok := integrator["review_max_fix_cycles"].(float64); ok {
			maxFixCycles = int(v)
			fixEnabled = true
		}
		if v, ok := integrator["review_fix_severity"].(string); ok {
			fixSeverity = v
			fixEnabled = true
		}
	}
	if v, ok := metadata["review_fix_enabled"].(bool); ok {
		fixEnabled = v
	}
	if v, ok := metadata["review_max_fix_cycles"].(float64); ok {
		maxFixCycles = int(v)
		fixEnabled = true
	}
	if v, ok := metadata["review_fix_severity"].(string); ok {
		fixSeverity = v
		fixEnabled = true
	}
	return review.Repository{
		ID:                 job.RepositoryID,
		InstallationID:     job.InstallationID,
		Owner:              owner,
		Name:               name,
		ReviewEnabled:      enabled,
		ReviewFixEnabled:   fixEnabled,
		ReviewMaxFixCycles: maxFixCycles,
		ReviewFixSeverity:  fixSeverity,
	}
}

func reviewFindings(findings []ReviewFinding) []review.Finding {
	out := make([]review.Finding, 0, len(findings))
	for _, finding := range findings {
		out = append(out, review.Finding{
			Fingerprint: finding.Fingerprint,
			Severity:    finding.Severity,
			Description: finding.Description,
		})
	}
	return out
}

func validateResultAgainstJob(result Result, job store.Job) error {
	if job.JobID != "" && result.Envelope().JobID != job.JobID {
		return fmt.Errorf("result job_id does not match job")
	}
	head := result.ResultHeadSHA()
	if job.HeadSHA != "" && head != "" && job.HeadSHA != head {
		return fmt.Errorf("stale head SHA: expected %s, got %s", job.HeadSHA, head)
	}
	if review, ok := result.(ReviewCompletedResult); ok {
		if job.PRNumber == 0 {
			return fmt.Errorf("job PR number is missing")
		}
		if strings.TrimSpace(job.HeadSHA) == "" {
			return fmt.Errorf("job head SHA is missing")
		}
		if review.HeadSHA != job.HeadSHA {
			return fmt.Errorf("stale head SHA: expected %s, got %s", job.HeadSHA, review.HeadSHA)
		}
		if review.PRNumber != job.PRNumber {
			return fmt.Errorf("result pr_number does not match job: expected %d, got %d", job.PRNumber, review.PRNumber)
		}
	}
	if worker, ok := result.(WorkerCompletedResult); ok {
		if job.BaseSHA != "" && worker.BaseSHA != "" && job.BaseSHA != worker.BaseSHA {
			return fmt.Errorf("stale base SHA: expected %s, got %s", job.BaseSHA, worker.BaseSHA)
		}
		if worker.Status == StatusSuccess {
			if strings.TrimSpace(job.BaseSHA) == "" {
				return fmt.Errorf("job base SHA is missing")
			}
			if strings.TrimSpace(job.HeadSHA) == "" {
				return fmt.Errorf("job head SHA is missing")
			}
			if strings.TrimSpace(job.WorkerBranch) == "" {
				return fmt.Errorf("job worker branch is missing")
			}
			if worker.BaseSHA != job.BaseSHA {
				return fmt.Errorf("stale base SHA: expected %s, got %s", job.BaseSHA, worker.BaseSHA)
			}
			if worker.ExpectedHeadSHA != job.HeadSHA {
				return fmt.Errorf("stale expected head SHA: expected %s, got %s", job.HeadSHA, worker.ExpectedHeadSHA)
			}
			if strings.TrimSpace(worker.TargetBranch) != job.WorkerBranch {
				return fmt.Errorf("result target_branch does not match job worker branch")
			}
		}
	}
	return nil
}

func (h Handler) validateWorkerPatch(ctx context.Context, result Result, job store.Job, alreadyApplied bool) (*artifacts.ValidatedArtifact, map[string]any, error) {
	worker, ok := result.(WorkerCompletedResult)
	if !ok || worker.Status != StatusSuccess {
		return nil, nil, nil
	}
	metadata := map[string]any{
		"patch_artifact": worker.PatchArtifact,
	}
	if h.artifactStore == nil {
		return nil, nil, fmt.Errorf("worker patch artifact store is not configured")
	}
	if h.patchApplier == nil {
		return nil, nil, fmt.Errorf("worker patch applier is not configured")
	}
	if h.appTokenSource == nil {
		return nil, nil, fmt.Errorf("worker patch GitHub App token source is not configured")
	}
	if _, ok := h.store.(MutationRecorder); !ok {
		return nil, nil, fmt.Errorf("worker patch mutation recorder is not configured")
	}
	if _, ok := h.store.(MutationReader); !ok {
		return nil, nil, fmt.Errorf("worker patch mutation reader is not configured")
	}
	if alreadyApplied {
		return nil, metadata, nil
	}
	artifactCtx := artifacts.ContextWithWorkflowRunArtifactRepository(ctx, worker.Repository, job.InstallationID, metadataWorkflowRunID(job.Metadata))
	artifact, err := artifacts.Validate(artifactCtx, h.artifactStore, artifacts.ValidationRequest{
		Repository:       worker.Repository,
		JobID:            worker.JobID,
		BaseSHA:          worker.BaseSHA,
		ExpectedHeadSHA:  worker.ExpectedHeadSHA,
		MetadataArtifact: worker.PatchArtifact,
	})
	if err != nil {
		metadata["error"] = err.Error()
		return nil, metadata, err
	}
	metadata["format"] = artifact.Metadata.Format
	metadata["sha256"] = artifact.Metadata.SHA256
	if len(artifact.Data) == 0 {
		metadata["empty"] = true
	}
	return &artifact, metadata, nil
}

func (h Handler) replayCompletedWorkerPatch(ctx context.Context, result Result, job store.Job) (bool, map[string]any, error) {
	worker, ok := result.(WorkerCompletedResult)
	if !ok || worker.Status != StatusSuccess {
		return false, nil, nil
	}
	metadata := map[string]any{
		"patch_artifact": worker.PatchArtifact,
	}
	if _, ok := h.store.(MutationReader); !ok {
		return false, metadata, nil
	}
	idempotencyKey := PatchApplyIdempotencyKey(worker, job)
	record, err := h.store.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, metadata, fmt.Errorf("get patch apply idempotency: %w", err)
	}
	if err == nil && record.Status == "completed" {
		completed, response, err := h.repairCompletedPatchApply(ctx, idempotencyKey, json.RawMessage(record.ResultRef))
		if err != nil {
			return false, metadata, err
		}
		if completed {
			mergePatchApplyMetadata(metadata, response)
			return true, metadata, nil
		}
		mergePatchApplyMetadata(metadata, json.RawMessage(record.ResultRef))
		return true, metadata, nil
	}
	completed, response, err := h.repairCompletedPatchApply(ctx, idempotencyKey, nil)
	if err != nil {
		return false, metadata, err
	}
	if completed {
		mergePatchApplyMetadata(metadata, response)
		return true, metadata, nil
	}
	return false, metadata, nil
}

func metadataWorkflowRunID(metadata json.RawMessage) int64 {
	values := metadataMap(metadata)
	for _, key := range []string{"workflow_run_id", "run_id", "github_run_id"} {
		switch v := values[key].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case json.Number:
			id, _ := v.Int64()
			return id
		case string:
			id, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return id
		}
	}
	return 0
}

func transientPatchValidationError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "unavailable")
}

func workerPatchConfigurationError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not configured")
}

func (h Handler) processWorkerPatch(ctx context.Context, result Result, job store.Job, artifact *artifacts.ValidatedArtifact, metadata map[string]any) error {
	worker, ok := result.(WorkerCompletedResult)
	if !ok || worker.Status != StatusSuccess || artifact == nil {
		return nil
	}
	idempotencyKey := PatchApplyIdempotencyKey(worker, job)
	shouldApply, applyResponse, err := h.acquirePatchApply(ctx, idempotencyKey, worker, job)
	if err != nil {
		return err
	}
	if !shouldApply {
		mergePatchApplyMetadata(metadata, applyResponse)
		return nil
	}
	response := json.RawMessage(`{"empty":true}`)
	if len(artifact.Data) == 0 {
		if err := h.completePatchMutation(ctx, idempotencyKey, mutationspkg.PhaseCompleted, response, nil); err != nil {
			_ = h.store.FailIdempotencyKey(ctx, idempotencyKey, err.Error())
			return err
		}
		if err := h.completePatchApply(ctx, idempotencyKey, response); err != nil {
			return err
		}
		return nil
	}
	prepared, err := h.patchApplier.Prepare(ctx, artifacts.ApplyRequest{
		Repository:      worker.Repository,
		CloneURL:        "https://github.com/" + worker.Repository + ".git",
		InstallationID:  job.InstallationID,
		TargetBranch:    worker.TargetBranch,
		BaseSHA:         worker.BaseSHA,
		ExpectedHeadSHA: worker.ExpectedHeadSHA,
		Artifact:        *artifact,
		Identity:        artifacts.DefaultIdentity(h.appLogin, h.appEmail),
		Human:           humanAttribution(job.Metadata),
		TokenSource:     h.appTokenSource,
		TempDir:         h.tempDir,
		Now:             h.now,
	})
	if err != nil {
		_ = h.completePatchMutation(ctx, idempotencyKey, mutationspkg.PhaseFailedPreCall, nil, err)
		_ = h.store.FailIdempotencyKey(ctx, idempotencyKey, mutationspkg.PhaseFailedPreCall+":"+err.Error())
		return mutationspkg.PreCallError{Op: "prepare patch apply", Err: err}
	}
	defer prepared.Cleanup()
	if err := h.markPatchMutationCallStarted(ctx, idempotencyKey); err != nil {
		_ = h.store.FailIdempotencyKey(ctx, idempotencyKey, err.Error())
		return err
	}
	applyResult, err := prepared.Push()
	if err != nil {
		_ = h.completePatchMutation(ctx, idempotencyKey, mutationspkg.PhaseRepairRequired, nil, err)
		_ = h.store.FailIdempotencyKey(ctx, idempotencyKey, err.Error())
		return err
	}
	if metadata != nil {
		metadata["commit_sha"] = applyResult.CommitSHA
	}
	response, err = json.Marshal(applyResult)
	if err != nil {
		return fmt.Errorf("marshal patch apply result: %w", err)
	}
	if err := h.completePatchMutation(ctx, idempotencyKey, mutationspkg.PhaseCompleted, response, nil); err != nil {
		if idemErr := h.completePatchApply(ctx, idempotencyKey, response); idemErr != nil {
			return fmt.Errorf("%w; complete patch apply idempotency after mutation completion failure: %v", err, idemErr)
		}
		return err
	}
	if err := h.completePatchApply(ctx, idempotencyKey, response); err != nil {
		return err
	}
	return nil
}

func PatchApplyIdempotencyKey(worker WorkerCompletedResult, job store.Job) string {
	parts := []string{
		worker.Repository,
		worker.JobID,
		worker.TargetBranch,
		job.BaseSHA,
		job.HeadSHA,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "patch_apply:" + hex.EncodeToString(sum[:])
}

func (h Handler) acquirePatchApply(ctx context.Context, idempotencyKey string, worker WorkerCompletedResult, job store.Job) (bool, json.RawMessage, error) {
	metadata, err := json.Marshal(map[string]any{
		"repository":        worker.Repository,
		"job_id":            worker.JobID,
		"target_branch":     worker.TargetBranch,
		"base_sha":          worker.BaseSHA,
		"expected_head_sha": worker.ExpectedHeadSHA,
		"patch_artifact":    worker.PatchArtifact,
	})
	if err != nil {
		return false, nil, fmt.Errorf("marshal patch apply metadata: %w", err)
	}
	created, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       idempotencyKey,
		Scope:     "patch_apply",
		Status:    mutationspkg.PhaseIntentRecorded,
		ResultRef: worker.JobID,
		Metadata:  metadata,
		CreatedAt: h.now(),
	})
	if err != nil {
		return false, nil, fmt.Errorf("acquire patch apply idempotency: %w", err)
	}
	if created {
		return true, nil, h.recordPatchMutationAttempt(ctx, idempotencyKey, job.RepositoryID, metadata)
	}
	record, err := h.store.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return false, nil, fmt.Errorf("get patch apply idempotency: %w", err)
	}
	if record.Status == "completed" {
		if completed, response, err := h.repairCompletedPatchApply(ctx, idempotencyKey, json.RawMessage(record.ResultRef)); completed || err != nil {
			return false, response, err
		}
		return false, json.RawMessage(record.ResultRef), nil
	}
	if completed, response, err := h.repairCompletedPatchApply(ctx, idempotencyKey, nil); completed || err != nil {
		return false, response, err
	}
	reader, ok := h.store.(MutationReader)
	if !ok {
		return false, nil, fmt.Errorf("patch apply %q has unknown outcome and mutation repair is not configured", idempotencyKey)
	}
	attempt, err := reader.GetGitHubMutationAttempt(ctx, idempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		if err := h.recordPatchMutationAttempt(ctx, idempotencyKey, job.RepositoryID, metadata); err != nil {
			return false, nil, err
		}
		return true, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("get patch mutation attempt: %w", err)
	}
	if mutationspkg.IsPostCallUnknown(attempt.Status) {
		return false, nil, fmt.Errorf("patch apply %q has unknown outcome after started mutation attempt", idempotencyKey)
	}
	return true, nil, nil
}

func (h Handler) repairCompletedPatchApply(ctx context.Context, idempotencyKey string, fallbackResponse json.RawMessage) (bool, json.RawMessage, error) {
	reader, ok := h.store.(MutationReader)
	if !ok {
		return false, nil, nil
	}
	attempt, err := reader.GetGitHubMutationAttempt(ctx, idempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("get patch mutation attempt: %w", err)
	}
	response := attempt.Response
	if mutationspkg.IsPostCallUnknown(attempt.Status) {
		if len(fallbackResponse) == 0 {
			return false, nil, nil
		}
		response = fallbackResponse
		if err := h.completePatchMutation(ctx, idempotencyKey, mutationspkg.PhaseCompleted, response, nil); err != nil {
			return false, nil, err
		}
	} else if !mutationspkg.IsCompleted(attempt.Status) {
		return false, nil, nil
	}
	if len(response) == 0 {
		response = json.RawMessage(`{"recovered":true}`)
	}
	if err := h.completePatchApply(ctx, idempotencyKey, response); err != nil {
		return false, nil, err
	}
	return true, response, nil
}

func mergePatchApplyMetadata(metadata map[string]any, response json.RawMessage) {
	if metadata == nil || len(response) == 0 {
		return
	}
	var applyResult artifacts.ApplyResult
	if err := json.Unmarshal(response, &applyResult); err == nil && strings.TrimSpace(applyResult.CommitSHA) != "" {
		metadata["commit_sha"] = applyResult.CommitSHA
		return
	}
	var body map[string]any
	if err := json.Unmarshal(response, &body); err != nil {
		return
	}
	if commit, ok := body["commit_sha"].(string); ok && strings.TrimSpace(commit) != "" {
		metadata["commit_sha"] = commit
	}
}

func (h Handler) recordPatchMutationAttempt(ctx context.Context, idempotencyKey string, repositoryID int64, request json.RawMessage) error {
	if recorder, ok := h.store.(MutationRecorder); ok {
		if err := recorder.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
			IdempotencyKey: idempotencyKey,
			RepositoryID:   repositoryID,
			MutationType:   "patch_apply",
			Status:         mutationspkg.PhaseIntentRecorded,
			Request:        request,
			CreatedAt:      h.now(),
		}); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				return fmt.Errorf("patch mutation already in progress: %w", err)
			}
			return mutationspkg.PreCallError{Op: "record patch mutation attempt", Err: err}
		}
	}
	return nil
}

func (h Handler) markPatchMutationCallStarted(ctx context.Context, idempotencyKey string) error {
	recorder, ok := h.store.(MutationRecorder)
	if !ok {
		return fmt.Errorf("patch mutation recorder is not configured")
	}
	starter, ok := h.store.(MutationStarter)
	if !ok {
		return fmt.Errorf("patch mutation starter is not configured")
	}
	start, err := starter.TryStartGitHubMutationAttempt(ctx, idempotencyKey, []string{mutationspkg.PhaseIntentRecorded, mutationspkg.PhaseFailedPreCall}, h.now())
	if err != nil {
		_ = recorder.CompleteGitHubMutationAttempt(ctx, idempotencyKey, mutationspkg.PhaseFailedPreCall, nil, err.Error(), h.now())
		return fmt.Errorf("mark patch mutation call started: %w", err)
	}
	if !start.Started {
		return fmt.Errorf("patch mutation %q is %s; repair required before retry", idempotencyKey, start.Attempt.Status)
	}
	return nil
}

func (h Handler) completePatchApply(ctx context.Context, idempotencyKey string, response json.RawMessage) error {
	if err := h.store.CompleteIdempotencyKey(ctx, idempotencyKey, string(response)); err != nil {
		return fmt.Errorf("complete patch apply idempotency: %w", err)
	}
	return nil
}

func (h Handler) completePatchMutation(ctx context.Context, key, status string, response json.RawMessage, resultErr error) error {
	recorder, ok := h.store.(MutationRecorder)
	if !ok {
		return nil
	}
	errorMessage := ""
	if resultErr != nil {
		errorMessage = resultErr.Error()
	}
	if err := recorder.CompleteGitHubMutationAttempt(ctx, key, status, response, errorMessage, h.now()); err != nil {
		return fmt.Errorf("complete patch mutation attempt: %w", err)
	}
	return nil
}

func humanAttribution(raw json.RawMessage) artifacts.HumanAttribution {
	metadata := metadataMap(raw)
	return artifacts.HumanAttribution{
		Name:  firstMetadataString(metadata, "requester_name", "actor_name", "sender_name"),
		Email: firstMetadataString(metadata, "requester_email", "actor_email", "sender_email"),
	}
}

func metadataMap(raw json.RawMessage) map[string]any {
	var metadata map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &metadata) != nil {
		return map[string]any{}
	}
	return metadata
}

func resultMetadata(payload []byte, claims OIDCClaims, extra map[string]any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	body := map[string]any{
		"payload": raw,
		"oidc": map[string]any{
			"repository": claims.Repository,
			"ref":        claims.Ref,
			"workflow":   claims.Workflow,
			"run_id":     claims.RunID,
			"expires_at": claims.ExpiresAt,
		},
	}
	if extra != nil {
		body["patch_apply"] = extra
	}
	metadata, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return metadata, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
