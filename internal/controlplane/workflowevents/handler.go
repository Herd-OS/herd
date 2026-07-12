package workflowevents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/store"
)

const (
	maxPayloadBytes = 1 << 20

	KindIntegratorEvent = "integrator_event"
	KindMonitorEvent    = "monitor_event"
)

type Store interface {
	GetRepository(ctx context.Context, owner string, name string) (store.Repository, error)
	RecordCommand(ctx context.Context, c store.CommandRecord) (created bool, err error)
	GetCommandRecord(ctx context.Context, repoID int64, commentID int64, commandKey string) (store.CommandRecord, error)
	UpdateCommandStatus(ctx context.Context, repoID int64, commentID int64, commandKey string, status string, metadata json.RawMessage) error
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
}

type Processor interface {
	ProcessWorkflowEvent(ctx context.Context, repo store.Repository, event Event) error
}

type HandlerOptions struct {
	Store     Store
	Validator jobs.OIDCValidator
	Audience  string
	Now       func() time.Time
	Processor Processor
}

type Event struct {
	Version     int             `json:"version"`
	Kind        string          `json:"kind"`
	Repository  string          `json:"repository"`
	EventName   string          `json:"event_name"`
	Action      string          `json:"action"`
	BatchNumber int             `json:"batch_number,omitempty"`
	IssueNumber int             `json:"issue_number,omitempty"`
	PRNumber    int             `json:"pr_number,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"`
	ReviewState string          `json:"review_state,omitempty"`
	Merged      *bool           `json:"merged,omitempty"`
	WorkflowRun json.RawMessage `json:"workflow_run,omitempty"`
	CheckRun    json.RawMessage `json:"check_run,omitempty"`
}

type Handler struct {
	store     Store
	validator jobs.OIDCValidator
	audience  string
	now       func() time.Time
	processor Processor
}

func NewHandler(opts HandlerOptions) http.Handler {
	audience := strings.TrimSpace(opts.Audience)
	if audience == "" {
		audience = controlplane.DefaultOIDCAudience
	}
	validator := opts.Validator
	if validator == nil {
		validator = jobs.NewJWKSValidator(audience)
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
		processor: opts.Processor,
	}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "workflow event storage is not configured"})
		return
	}
	if h.validator == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "OIDC validator is not configured"})
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPayloadBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workflow event payload is invalid"})
		return
	}
	event, err := Parse(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	token, err := jobs.BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	claims, err := h.validator.Validate(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "validate OIDC token"})
		return
	}
	expected, err := expectedOIDCIdentity(event)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	expected.Repository = event.Repository
	if err := jobs.ValidateOIDCClaims(claims, expected, jobs.OIDCOptions{Audience: h.audience, Now: h.now}); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if h.processor == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "workflow event processor is not configured"})
		return
	}

	owner, name, ok := strings.Cut(event.Repository, "/")
	if !ok || owner == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repository must be owner/name"})
		return
	}
	repo, err := h.store.GetRepository(r.Context(), owner, name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup repository"})
		return
	}
	metadata, err := eventMetadata(payload, claims)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build workflow event metadata"})
		return
	}
	processKey := "workflow_event:" + workflowEventCommandKey(event, payload, claims)
	shouldProcess, err := h.acquireWorkflowEvent(r.Context(), processKey, metadata)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "acquire workflow event idempotency"})
		return
	}
	if !shouldProcess {
		commandKey := workflowEventCommandKey(event, payload, claims)
		commentID := workflowEventCommentID(event, payload, claims)
		if record, recordErr := h.store.GetCommandRecord(r.Context(), repo.ID, commentID, commandKey); recordErr == nil && record.Status != "processed" {
			if err := h.markWorkflowEventProcessed(r.Context(), repo.ID, commentID, commandKey, metadata); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark workflow event processed"})
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":  "accepted",
			"created": false,
			"kind":    event.Kind,
			"action":  event.Action,
		})
		return
	}
	created, err := h.store.RecordCommand(r.Context(), store.CommandRecord{
		RepositoryID: repo.ID,
		CommentID:    workflowEventCommentID(event, payload, claims),
		CommandKey:   workflowEventCommandKey(event, payload, claims),
		CommandName:  event.Kind,
		Actor:        "github-actions",
		Status:       "acknowledged",
		Metadata:     metadata,
		CreatedAt:    h.now(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record workflow event"})
		return
	}
	commandKey := workflowEventCommandKey(event, payload, claims)
	commentID := workflowEventCommentID(event, payload, claims)
	if !created && h.workflowEventProcessedOrRepairable(r.Context(), repo.ID, commentID, commandKey) {
		if err := h.markWorkflowEventProcessed(r.Context(), repo.ID, commentID, commandKey, metadata); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark workflow event processed"})
			return
		}
		if err := h.store.CompleteIdempotencyKey(r.Context(), processKey, commandKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete workflow event idempotency"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":  "accepted",
			"created": false,
			"kind":    event.Kind,
			"action":  event.Action,
		})
		return
	}
	if !created && h.workflowEventProcessingUnknown(r.Context(), repo.ID, commentID, commandKey) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "workflow event processing outcome is unknown; retry after reconciliation"})
		return
	}
	if err := h.markWorkflowEventProcessing(r.Context(), repo.ID, commentID, commandKey, metadata); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark workflow event processing"})
		return
	}
	if h.processor != nil {
		if err := h.processor.ProcessWorkflowEvent(r.Context(), repo, event); err != nil {
			_ = h.store.UpdateCommandStatus(r.Context(), repo.ID, commentID, commandKey, "acknowledged", metadata)
			_ = h.store.FailIdempotencyKey(r.Context(), processKey, err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "process workflow event"})
			return
		}
	}
	if err := h.markWorkflowEventProcessed(r.Context(), repo.ID, commentID, commandKey, metadata); err != nil {
		if repairErr := h.markWorkflowEventProcessedPending(r.Context(), repo.ID, commentID, commandKey, metadata); repairErr == nil {
			_ = h.store.CompleteIdempotencyKey(r.Context(), processKey, commandKey)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark workflow event processed"})
		return
	}
	if err := h.store.CompleteIdempotencyKey(r.Context(), processKey, commandKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete workflow event idempotency"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "accepted",
		"created": created,
		"kind":    event.Kind,
		"action":  event.Action,
	})
}

func expectedOIDCIdentity(event Event) (jobs.ExpectedOIDCIdentity, error) {
	switch event.Kind {
	case KindIntegratorEvent:
		return jobs.ExpectedOIDCIdentity{Workflow: ".github/workflows/herd-integrator.yml"}, nil
	case KindMonitorEvent:
		return jobs.ExpectedOIDCIdentity{Workflow: ".github/workflows/herd-monitor.yml"}, nil
	default:
		return jobs.ExpectedOIDCIdentity{}, fmt.Errorf("unsupported workflow event kind %q", event.Kind)
	}
}

func (h Handler) acquireWorkflowEvent(ctx context.Context, key string, metadata json.RawMessage) (bool, error) {
	created, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "workflow_event",
		Status:    "started",
		Metadata:  metadata,
		CreatedAt: h.now(),
	})
	if err != nil {
		return false, err
	}
	if created {
		return true, nil
	}
	record, err := h.store.GetIdempotencyKey(ctx, key)
	if err != nil {
		return false, err
	}
	return record.Status != "completed", nil
}

func (h Handler) workflowEventProcessedOrRepairable(ctx context.Context, repoID int64, commentID int64, commandKey string) bool {
	record, err := h.store.GetCommandRecord(ctx, repoID, commentID, commandKey)
	return err == nil && (record.Status == "processed" || record.Status == "processed_pending")
}

func (h Handler) workflowEventProcessingUnknown(ctx context.Context, repoID int64, commentID int64, commandKey string) bool {
	record, err := h.store.GetCommandRecord(ctx, repoID, commentID, commandKey)
	return err == nil && record.Status == "processing"
}

func (h Handler) markWorkflowEventProcessing(ctx context.Context, repoID int64, commentID int64, commandKey string, metadata json.RawMessage) error {
	return h.store.UpdateCommandStatus(ctx, repoID, commentID, commandKey, "processing", metadata)
}

func (h Handler) markWorkflowEventProcessed(ctx context.Context, repoID int64, commentID int64, commandKey string, metadata json.RawMessage) error {
	return h.store.UpdateCommandStatus(ctx, repoID, commentID, commandKey, "processed", metadata)
}

func (h Handler) markWorkflowEventProcessedPending(ctx context.Context, repoID int64, commentID int64, commandKey string, metadata json.RawMessage) error {
	return h.store.UpdateCommandStatus(ctx, repoID, commentID, commandKey, "processed_pending", metadata)
}

func Parse(payload []byte) (Event, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return Event{}, fmt.Errorf("workflow event payload is empty")
	}
	var event Event
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("invalid workflow event payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Event{}, fmt.Errorf("invalid workflow event payload: multiple JSON values")
	}
	if err := validate(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func validate(event Event) error {
	if event.Version != 1 {
		return fmt.Errorf("unsupported workflow event version %d", event.Version)
	}
	if event.Kind != KindIntegratorEvent && event.Kind != KindMonitorEvent {
		return fmt.Errorf("unsupported workflow event kind %q", event.Kind)
	}
	if strings.TrimSpace(event.Repository) == "" {
		return fmt.Errorf("repository is required")
	}
	if strings.TrimSpace(event.EventName) == "" {
		return fmt.Errorf("event_name is required")
	}
	if strings.TrimSpace(event.Action) == "" {
		return fmt.Errorf("action is required")
	}
	return nil
}

func eventMetadata(payload []byte, claims jobs.OIDCClaims) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"payload": raw,
		"oidc": map[string]any{
			"repository": claims.Repository,
			"ref":        claims.Ref,
			"workflow":   claims.Workflow,
			"run_id":     claims.RunID,
			"expires_at": claims.ExpiresAt,
		},
	})
}

func workflowEventCommentID(event Event, payload []byte, claims jobs.OIDCClaims) int64 {
	sum := sha256.Sum256([]byte(event.Kind + ":" + event.Action + ":" + event.EventName + ":" + workflowEventIdentity(event, payload, claims)))
	id := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if id == 0 {
		return 1
	}
	return id
}

func workflowEventCommandKey(event Event, payload []byte, claims jobs.OIDCClaims) string {
	return event.Kind + ":" + event.Action + ":" + workflowEventIdentity(event, payload, claims)
}

func workflowEventIdentity(event Event, payload []byte, claims jobs.OIDCClaims) string {
	if id := workflowEventSourceID(event); id != "" {
		return event.EventName + ":" + id
	}
	if strings.TrimSpace(claims.RunID) != "" {
		return event.EventName + ":run:" + strings.TrimSpace(claims.RunID)
	}
	sum := sha256.Sum256(payload)
	return event.EventName + ":payload:" + hex.EncodeToString(sum[:])
}

func workflowEventSourceID(event Event) string {
	if id := rawObjectID(event.WorkflowRun); id != "" {
		return "workflow_run:" + id
	}
	if id := rawObjectID(event.CheckRun); id != "" {
		return "check_run:" + id
	}
	return ""
}

func rawObjectID(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var object struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	id := bytes.TrimSpace(object.ID)
	if len(id) == 0 || bytes.Equal(id, []byte("null")) {
		return ""
	}
	var asString string
	if err := json.Unmarshal(id, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return ""
	}
	return strings.TrimSpace(number.String())
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
