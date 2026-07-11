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

	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/store"
)

const maxResultPayloadBytes = 1 << 20

type Store interface {
	GetJob(ctx context.Context, jobID string) (store.Job, error)
	RecordJobResult(ctx context.Context, r store.JobResult) (created bool, err error)
}

type Handler struct {
	store     Store
	validator OIDCValidator
	audience  string
	now       func() time.Time
}

type HandlerOptions struct {
	Store     Store
	Validator OIDCValidator
	Audience  string
	Now       func() time.Time
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
	return Handler{
		store:     opts.Store,
		validator: validator,
		audience:  audience,
		now:       now,
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

	metadata, err := resultMetadata(payload, claims)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build job result metadata"})
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

func validateResultAgainstJob(result Result, job store.Job) error {
	if job.JobID != "" && result.Envelope().JobID != job.JobID {
		return fmt.Errorf("result job_id does not match job")
	}
	head := result.ResultHeadSHA()
	if job.HeadSHA != "" && head != "" && job.HeadSHA != head {
		return fmt.Errorf("stale head SHA: expected %s, got %s", job.HeadSHA, head)
	}
	return nil
}

func resultMetadata(payload []byte, claims OIDCClaims) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(map[string]any{
		"payload": raw,
		"oidc": map[string]any{
			"repository": claims.Repository,
			"ref":        claims.Ref,
			"workflow":   claims.Workflow,
			"run_id":     claims.RunID,
			"expires_at": claims.ExpiresAt,
		},
	})
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
