package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
)

const maxResultPayloadBytes = 1 << 20

type Store interface {
	GetJob(ctx context.Context, jobID string) (store.Job, error)
	RecordJobResult(ctx context.Context, r store.JobResult) (created bool, err error)
}

type MutationRecorder interface {
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
}

type PatchApplier interface {
	Apply(ctx context.Context, req artifacts.ApplyRequest) (artifacts.ApplyResult, error)
}

type ReviewProcessor interface {
	SubmitReviewResult(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult) error
}

type defaultPatchApplier struct{}

func (defaultPatchApplier) Apply(ctx context.Context, req artifacts.ApplyRequest) (artifacts.ApplyResult, error) {
	return artifacts.Apply(ctx, req)
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
	expected := ExpectedIdentityFromJob(job, envelope.Repository)
	if err := ValidateOIDCClaims(claims, expected, OIDCOptions{Audience: h.audience, Now: h.now}); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	applyMetadata, applyErr := h.processWorkerPatch(r.Context(), result, job, payload)
	if applyErr != nil {
		metadata, metadataErr := resultMetadata(payload, claims, applyMetadata)
		if metadataErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build job result metadata"})
			return
		}
		_, _ = h.store.RecordJobResult(r.Context(), store.JobResult{
			JobID:          envelope.JobID,
			IdempotencyKey: ResultIdempotencyKey(result, payload),
			Status:         StatusFailure,
			ResultRef:      ResultPayloadHash(payload),
			Metadata:       metadata,
			CreatedAt:      h.now(),
		})
		writeJSON(w, http.StatusConflict, map[string]string{"error": applyErr.Error()})
		return
	}

	metadata, err := resultMetadata(payload, claims, applyMetadata)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build job result metadata"})
		return
	}
	if err := h.processReviewResult(r.Context(), result, job); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "process review result"})
		return
	}
	idempotencyKey := ResultIdempotencyKey(result, payload)
	created, err := h.store.RecordJobResult(r.Context(), store.JobResult{
		JobID:          envelope.JobID,
		IdempotencyKey: idempotencyKey,
		Status:         result.StatusValue(),
		ResultRef:      ResultPayloadHash(payload),
		Metadata:       metadata,
		CreatedAt:      h.now(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record job result"})
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

func (h Handler) processReviewResult(ctx context.Context, result Result, job store.Job) error {
	reviewResult, ok := result.(ReviewCompletedResult)
	if !ok || h.reviewProcessor == nil {
		return nil
	}
	repo := reviewRepositoryFromJob(job, reviewResult.Repository)
	targetURL := firstMetadataString(metadataMap(job.Metadata), "workflow_run_url", "run_url", "target_url", "pr_url")
	return h.reviewProcessor.SubmitReviewResult(ctx, repo, review.ReviewCompletedResult{
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
	})
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
	if worker, ok := result.(WorkerCompletedResult); ok && job.BaseSHA != "" && worker.BaseSHA != "" && job.BaseSHA != worker.BaseSHA {
		return fmt.Errorf("stale base SHA: expected %s, got %s", job.BaseSHA, worker.BaseSHA)
	}
	return nil
}

func (h Handler) processWorkerPatch(ctx context.Context, result Result, job store.Job, payload []byte) (map[string]any, error) {
	worker, ok := result.(WorkerCompletedResult)
	if !ok || worker.Status != StatusSuccess || h.artifactStore == nil {
		return nil, nil
	}
	idempotencyKey := "patch_apply:" + ResultPayloadHash(payload)
	metadata := map[string]any{
		"patch_artifact": worker.PatchArtifact,
		"mutation_key":   idempotencyKey,
	}
	if recorder, ok := h.store.(MutationRecorder); ok {
		request, err := json.Marshal(map[string]any{
			"repository":        worker.Repository,
			"job_id":            worker.JobID,
			"target_branch":     worker.TargetBranch,
			"base_sha":          worker.BaseSHA,
			"expected_head_sha": worker.ExpectedHeadSHA,
			"patch_artifact":    worker.PatchArtifact,
		})
		if err != nil {
			return metadata, fmt.Errorf("marshal patch mutation request: %w", err)
		}
		if err := recorder.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
			IdempotencyKey: idempotencyKey,
			RepositoryID:   job.RepositoryID,
			MutationType:   "patch_apply",
			Status:         "started",
			Request:        request,
			CreatedAt:      h.now(),
		}); err != nil {
			return metadata, fmt.Errorf("record patch mutation attempt: %w", err)
		}
	}
	artifact, err := artifacts.Validate(ctx, h.artifactStore, artifacts.ValidationRequest{
		Repository:       worker.Repository,
		JobID:            worker.JobID,
		BaseSHA:          worker.BaseSHA,
		ExpectedHeadSHA:  worker.ExpectedHeadSHA,
		MetadataArtifact: worker.PatchArtifact,
	})
	if err != nil {
		h.completePatchMutation(ctx, idempotencyKey, "failed", nil, err)
		metadata["error"] = err.Error()
		return metadata, err
	}
	metadata["format"] = artifact.Metadata.Format
	metadata["sha256"] = artifact.Metadata.SHA256
	if h.patchApplier == nil {
		return metadata, nil
	}
	applyResult, err := h.patchApplier.Apply(ctx, artifacts.ApplyRequest{
		Repository:      worker.Repository,
		CloneURL:        "https://github.com/" + worker.Repository + ".git",
		InstallationID:  job.InstallationID,
		TargetBranch:    worker.TargetBranch,
		BaseSHA:         worker.BaseSHA,
		ExpectedHeadSHA: worker.ExpectedHeadSHA,
		Artifact:        artifact,
		Identity:        artifacts.DefaultIdentity(h.appLogin, h.appEmail),
		Human:           humanAttribution(job.Metadata),
		TokenSource:     h.appTokenSource,
		TempDir:         h.tempDir,
		Now:             h.now,
	})
	if err != nil {
		h.completePatchMutation(ctx, idempotencyKey, "failed", nil, err)
		metadata["error"] = err.Error()
		return metadata, err
	}
	response, err := json.Marshal(applyResult)
	if err != nil {
		return metadata, fmt.Errorf("marshal patch apply result: %w", err)
	}
	h.completePatchMutation(ctx, idempotencyKey, "completed", response, nil)
	metadata["commit_sha"] = applyResult.CommitSHA
	return metadata, nil
}

func (h Handler) completePatchMutation(ctx context.Context, key, status string, response json.RawMessage, resultErr error) {
	recorder, ok := h.store.(MutationRecorder)
	if !ok {
		return
	}
	errorMessage := ""
	if resultErr != nil {
		errorMessage = resultErr.Error()
	}
	_ = recorder.CompleteGitHubMutationAttempt(ctx, key, status, response, errorMessage, h.now())
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
