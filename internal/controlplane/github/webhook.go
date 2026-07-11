package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
)

const (
	signaturePrefix = "sha256="
	maxPayloadBytes = 1 << 20
)

var (
	ErrMissingSignature   = errors.New("missing X-Hub-Signature-256")
	ErrMalformedSignature = errors.New("malformed X-Hub-Signature-256")
	ErrInvalidSignature   = errors.New("invalid X-Hub-Signature-256")
)

type Store interface {
	RecordWebhookDelivery(ctx context.Context, d store.WebhookDelivery) (created bool, err error)
	UpsertInstallation(ctx context.Context, i store.Installation) error
	UpsertRepository(ctx context.Context, r store.Repository) (store.Repository, error)
}

type Handler struct {
	secret string
	store  Store
	logger *log.Logger
}

func NewHandler(secret string, store Store, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	return Handler{
		secret: secret,
		store:  store,
		logger: logger,
	}
}

func VerifySignature(secret string, payload []byte, signatureHeader string) error {
	if strings.TrimSpace(signatureHeader) == "" {
		return ErrMissingSignature
	}
	if !strings.HasPrefix(signatureHeader, signaturePrefix) {
		return ErrMalformedSignature
	}

	signatureHex := strings.TrimPrefix(signatureHeader, signaturePrefix)
	signature, err := hex.DecodeString(signatureHex)
	if err != nil || len(signature) != sha256.Size {
		return ErrMalformedSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	if !hmac.Equal(signature, expected) {
		return ErrInvalidSignature
	}
	return nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "webhook storage is not configured",
		})
		return
	}
	if strings.TrimSpace(h.secret) == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "webhook secret is not configured",
		})
		return
	}

	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing X-GitHub-Delivery header",
		})
		return
	}

	eventName := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	if eventName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing X-GitHub-Event header",
		})
		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPayloadBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "webhook payload is invalid",
		})
		return
	}

	if err := VerifySignature(h.secret, payload, r.Header.Get("X-Hub-Signature-256")); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrMissingSignature) || errors.Is(err, ErrMalformedSignature) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	action := PayloadAction(payload)
	created, err := h.store.RecordWebhookDelivery(r.Context(), store.WebhookDelivery{
		DeliveryID:  deliveryID,
		Event:       eventName,
		Action:      action,
		PayloadHash: payloadHash(payload),
		Status:      "accepted",
		Metadata:    mustJSON(map[string]string{"event": eventName, "action": action}),
		ReceivedAt:  time.Now().UTC(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "record webhook delivery: storage unavailable",
		})
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate"})
		return
	}

	event, err := ParseEvent(eventName, payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "parse webhook payload: unsupported payload shape",
		})
		return
	}
	if event == nil {
		h.logger.Printf("accepted unsupported GitHub webhook event delivery=%s event=%s action=%s", deliveryID, eventName, action)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
		return
	}

	if err := h.processEvent(r.Context(), event); err != nil {
		h.logger.Printf("process GitHub webhook delivery=%s event=%s action=%s: %v", deliveryID, eventName, action, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "process webhook event: storage unavailable",
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h Handler) processEvent(ctx context.Context, event Event) error {
	switch e := event.(type) {
	case InstallationEvent:
		return h.processInstallation(ctx, e)
	case InstallationRepositoriesEvent:
		return h.processInstallationRepositories(ctx, e)
	default:
		return nil
	}
}

func (h Handler) processInstallation(ctx context.Context, e InstallationEvent) error {
	if err := h.store.UpsertInstallation(ctx, store.Installation{
		ID:           e.InstallationID,
		AccountLogin: e.AccountLogin,
		AccountID:    e.AccountID,
		TargetType:   firstNonEmpty(e.TargetType, e.AccountType),
		Permissions:  rawMessageOrEmpty(e.Permissions),
		Events:       e.Events,
		CreatedAt:    parseGitHubTime(e.InstallationCreatedAt),
		UpdatedAt:    parseGitHubTime(e.InstallationUpdatedAt),
	}); err != nil {
		return fmt.Errorf("upsert installation: %w", err)
	}

	for _, repo := range e.Repositories {
		if err := h.upsertRepository(ctx, e.InstallationID, repo, "selected", map[string]any{
			"installation_action":  e.Action,
			"repository_selection": e.RepositorySelection,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h Handler) processInstallationRepositories(ctx context.Context, e InstallationRepositoriesEvent) error {
	if err := h.store.UpsertInstallation(ctx, store.Installation{
		ID:           e.InstallationID,
		AccountLogin: e.AccountLogin,
		AccountID:    e.AccountID,
		TargetType:   e.AccountType,
		Permissions:  json.RawMessage(`{}`),
		UpdatedAt:    time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("upsert installation: %w", err)
	}

	for _, repo := range e.RepositoriesAdded {
		if err := h.upsertRepository(ctx, e.InstallationID, repo, "selected", map[string]any{
			"installation_repositories_action": e.Action,
			"repository_selection":             e.RepositorySelection,
		}); err != nil {
			return err
		}
	}
	for _, repo := range e.RepositoriesRemoved {
		if err := h.upsertRepository(ctx, e.InstallationID, repo, "removed", map[string]any{
			"installation_repositories_action": e.Action,
			"repository_selection":             e.RepositorySelection,
			"removed":                          true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h Handler) upsertRepository(ctx context.Context, installationID int64, repo Repository, selectionState string, metadata map[string]any) error {
	if repo.Owner == "" || repo.Name == "" {
		return fmt.Errorf("upsert repository: missing owner or name")
	}
	metadata["selection_state"] = selectionState
	metadata["full_name"] = repo.FullName
	if _, err := h.store.UpsertRepository(ctx, store.Repository{
		GitHubID:       repo.ID,
		InstallationID: installationID,
		Owner:          repo.Owner,
		Name:           repo.Name,
		DefaultBranch:  repo.DefaultBranch,
		Private:        repo.Private,
		RegisteredAt:   time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		Metadata:       mustJSON(metadata),
	}); err != nil {
		return fmt.Errorf("upsert repository: %w", err)
	}
	return nil
}

func payloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func rawMessageOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

func parseGitHubTime(value string) time.Time {
	if value == "" {
		return time.Now().UTC()
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
