package workflowevents

import (
	"bytes"
	"context"
	"encoding/json"
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
	if err := jobs.ValidateOIDCClaims(claims, jobs.ExpectedOIDCIdentity{
		Repository: event.Repository,
	}, jobs.OIDCOptions{Audience: h.audience, Now: h.now}); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	owner, name, ok := strings.Cut(event.Repository, "/")
	if !ok || owner == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repository must be owner/name"})
		return
	}
	repo, err := h.store.GetRepository(r.Context(), owner, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
		return
	}
	metadata, err := eventMetadata(payload, claims)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build workflow event metadata"})
		return
	}
	created, err := h.store.RecordCommand(r.Context(), store.CommandRecord{
		RepositoryID: repo.ID,
		CommentID:    syntheticCommentID(event),
		CommandKey:   event.Kind + ":" + event.Action,
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
	if created && h.processor != nil {
		if err := h.processor.ProcessWorkflowEvent(r.Context(), repo, event); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "process workflow event"})
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "accepted",
		"created": created,
		"kind":    event.Kind,
		"action":  event.Action,
	})
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

func syntheticCommentID(event Event) int64 {
	key := event.Kind + ":" + event.Action + ":" + event.EventName + ":" + fmt.Sprint(event.BatchNumber) + ":" + fmt.Sprint(event.IssueNumber) + ":" + fmt.Sprint(event.PRNumber)
	var out int64
	for _, b := range []byte(key) {
		out = out*31 + int64(b)
	}
	if out < 0 {
		out = -out
	}
	if out == 0 {
		out = 1
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
