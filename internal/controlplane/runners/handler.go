package runners

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/store"
	ghplatform "github.com/herd-os/herd/internal/platform/github"
)

const (
	maxPayloadBytes          = 1 << 20
	idempotencyScope         = "runner-registration-token"
	idempotencyTTL           = time.Hour
	idempotencyStatusStarted = "started"
	idempotencyStatusDone    = "completed"
	failedResultPrefix       = "registration_token:"
)

type RegistrationTokenRequest struct {
	Repository     string   `json:"repository"`
	Owner          string   `json:"owner"`
	Name           string   `json:"name"`
	RunnerName     string   `json:"runner_name"`
	RunnerLabels   []string `json:"runner_labels"`
	BootstrapToken string   `json:"bootstrap_token"`
	RequestNonce   string   `json:"request_nonce"`
}

type RegistrationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Store interface {
	GetRepository(ctx context.Context, owner string, name string) (store.Repository, error)
	GetRunnerBootstrapTokenByHash(ctx context.Context, tokenHash string) (store.RunnerBootstrapToken, error)
	MarkRunnerBootstrapTokenUsed(ctx context.Context, tokenID int64, usedAt time.Time) error
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
}

type TokenMinter interface {
	CreateRegistrationToken(ctx context.Context, installationID int64, owner string, repo string) (RegistrationTokenResponse, error)
}

type RegistrationTokenHandler struct {
	store  Store
	minter TokenMinter
	now    func() time.Time
}

type HandlerOptions struct {
	Store  Store
	Minter TokenMinter
	Now    func() time.Time
}

func NewRegistrationTokenHandler(opts HandlerOptions) http.Handler {
	h := RegistrationTokenHandler{
		store:  opts.Store,
		minter: opts.Minter,
		now:    opts.Now,
	}
	if h.now == nil {
		h.now = func() time.Time { return time.Now().UTC() }
	}
	return h
}

func NewDefaultRegistrationTokenHandler(s Store, cfg appauth.AppConfig) (http.Handler, error) {
	source, _, err := appauth.NewGitHubTokenSource(cfg)
	if err != nil {
		return nil, err
	}
	return NewRegistrationTokenHandler(HandlerOptions{
		Store:  s,
		Minter: AppInstallationMinter{Source: source},
	}), nil
}

func (h RegistrationTokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "runner registration storage is not configured"})
		return
	}
	if h.minter == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "runner registration GitHub App auth is not configured"})
		return
	}

	req, ok := h.decodeRequest(w, r)
	if !ok {
		return
	}
	repo, token, ok := h.validateBootstrap(w, r.Context(), req)
	if !ok {
		return
	}

	idempotencyKey := registrationIDKey(repo.ID, token.ID, req.RequestNonce)
	result, replayed, ok := h.acquireOrReplay(w, r.Context(), idempotencyKey, repo.ID, req, token)
	if !ok {
		return
	}
	if replayed {
		writeJSON(w, http.StatusOK, result)
		return
	}

	result, err := h.minter.CreateRegistrationToken(r.Context(), repo.InstallationID, repo.Owner, repo.Name)
	if err != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), idempotencyKey, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "create GitHub runner registration token"})
		return
	}
	if strings.TrimSpace(result.Token) == "" || result.ExpiresAt.IsZero() {
		_ = h.store.FailIdempotencyKey(r.Context(), idempotencyKey, "empty registration token response")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "create GitHub runner registration token: empty response"})
		return
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record runner registration token result"})
		return
	}
	usedAt := h.now()
	if err := h.store.CompleteIdempotencyKey(r.Context(), idempotencyKey, string(resultJSON)); err != nil {
		_ = h.store.FailIdempotencyKey(r.Context(), idempotencyKey, failedResultPrefix+string(resultJSON))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "complete runner registration idempotency"})
		return
	}
	if err := h.store.MarkRunnerBootstrapTokenUsed(r.Context(), token.ID, usedAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record runner bootstrap token use"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h RegistrationTokenHandler) decodeRequest(w http.ResponseWriter, r *http.Request) (RegistrationTokenRequest, bool) {
	var req RegistrationTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPayloadBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON runner registration request"})
		return RegistrationTokenRequest{}, false
	}
	req.Owner = strings.TrimSpace(req.Owner)
	req.Name = strings.TrimSpace(req.Name)
	req.Repository = strings.TrimSpace(req.Repository)
	req.RunnerName = strings.TrimSpace(req.RunnerName)
	req.BootstrapToken = strings.TrimSpace(req.BootstrapToken)
	req.RequestNonce = strings.TrimSpace(req.RequestNonce)
	for i := range req.RunnerLabels {
		req.RunnerLabels[i] = strings.TrimSpace(req.RunnerLabels[i])
	}
	if req.Owner == "" || req.Name == "" {
		parts := strings.Split(req.Repository, "/")
		if len(parts) == 2 {
			req.Owner = strings.TrimSpace(parts[0])
			req.Name = strings.TrimSpace(parts[1])
		}
	}
	if req.Owner == "" || req.Name == "" || req.RunnerName == "" || req.BootstrapToken == "" || req.RequestNonce == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner, name, runner_name, bootstrap_token, and request_nonce are required"})
		return RegistrationTokenRequest{}, false
	}
	return req, true
}

func (h RegistrationTokenHandler) validateBootstrap(w http.ResponseWriter, ctx context.Context, req RegistrationTokenRequest) (store.Repository, store.RunnerBootstrapToken, bool) {
	repo, err := h.store.GetRepository(ctx, req.Owner, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository is not registered"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup repository registration"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}

	token, err := h.store.GetRunnerBootstrapTokenByHash(ctx, HashBootstrapToken(req.BootstrapToken))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid runner bootstrap token"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup runner bootstrap token"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}
	if token.RepositoryID != repo.ID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "runner bootstrap token is not valid for this repository"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}
	if token.RevokedAt != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "runner bootstrap token has been revoked"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}
	if !token.ExpiresAt.IsZero() && !h.now().Before(token.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "runner bootstrap token has expired"})
		return store.Repository{}, store.RunnerBootstrapToken{}, false
	}
	return repo, token, true
}

func (h RegistrationTokenHandler) acquireOrReplay(w http.ResponseWriter, ctx context.Context, key string, repoID int64, req RegistrationTokenRequest, token store.RunnerBootstrapToken) (RegistrationTokenResponse, bool, bool) {
	expiresAt := h.now().Add(idempotencyTTL)
	metadata, err := runnerRequestMetadata(repoID, req, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "build runner registration idempotency metadata"})
		return RegistrationTokenResponse{}, false, false
	}
	created, err := h.store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     idempotencyScope,
		Status:    idempotencyStatusStarted,
		ExpiresAt: &expiresAt,
		Metadata:  metadata,
		CreatedAt: h.now(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "acquire runner registration idempotency"})
		return RegistrationTokenResponse{}, false, false
	}
	if created {
		return RegistrationTokenResponse{}, false, true
	}
	record, err := h.store.GetIdempotencyKey(ctx, key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup runner registration idempotency"})
		return RegistrationTokenResponse{}, false, false
	}
	if !sameRegistrationRequest(record.Metadata, metadata) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "runner registration nonce was already used with different runner metadata"})
		return RegistrationTokenResponse{}, false, false
	}
	if record.Status == "failed" {
		if resultJSON, ok := strings.CutPrefix(record.ResultRef, failedResultPrefix); ok {
			result, ok := h.replayRegistrationResult(w, ctx, resultJSON, token)
			return result, ok, ok
		}
		return RegistrationTokenResponse{}, false, true
	}
	if record.Status != idempotencyStatusDone {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "runner registration request outcome is unknown; retry with a new nonce after the current request expires or reconciliation completes"})
		return RegistrationTokenResponse{}, false, false
	}
	result, ok := h.replayRegistrationResult(w, ctx, record.ResultRef, token)
	return result, ok, ok
}

func (h RegistrationTokenHandler) replayRegistrationResult(w http.ResponseWriter, ctx context.Context, raw string, token store.RunnerBootstrapToken) (RegistrationTokenResponse, bool) {
	var result RegistrationTokenResponse
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decode runner registration idempotency result"})
		return RegistrationTokenResponse{}, false
	}
	if result.ExpiresAt.IsZero() || !h.now().Before(result.ExpiresAt) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "stored runner registration token has expired; retry with a new nonce"})
		return RegistrationTokenResponse{}, false
	}
	if token.UsedAt == nil {
		if err := h.store.MarkRunnerBootstrapTokenUsed(ctx, token.ID, h.now()); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record runner bootstrap token use"})
			return RegistrationTokenResponse{}, false
		}
	}
	return result, true
}

func runnerRequestMetadata(repoID int64, req RegistrationTokenRequest, token store.RunnerBootstrapToken) ([]byte, error) {
	return json.Marshal(struct {
		RepositoryID     int64    `json:"repository_id"`
		RunnerName       string   `json:"runner_name"`
		RunnerLabels     []string `json:"runner_labels"`
		BootstrapTokenID int64    `json:"bootstrap_token_id"`
		RequestNonce     string   `json:"request_nonce"`
	}{
		RepositoryID:     repoID,
		RunnerName:       strings.TrimSpace(req.RunnerName),
		RunnerLabels:     req.RunnerLabels,
		BootstrapTokenID: token.ID,
		RequestNonce:     strings.TrimSpace(req.RequestNonce),
	})
}

func sameRegistrationRequest(existing json.RawMessage, current []byte) bool {
	return strings.TrimSpace(string(existing)) == strings.TrimSpace(string(current))
}

func registrationIDKey(repoID int64, tokenID int64, nonce string) string {
	payload, _ := json.Marshal(struct {
		RepositoryID     int64  `json:"repository_id"`
		BootstrapTokenID int64  `json:"bootstrap_token_id"`
		RequestNonce     string `json:"request_nonce"`
	}{
		RepositoryID:     repoID,
		BootstrapTokenID: tokenID,
		RequestNonce:     strings.TrimSpace(nonce),
	})
	return idempotencyScope + ":" + HashBootstrapToken(string(payload))
}

type AppInstallationMinter struct {
	Source appauth.TokenSource
}

func (m AppInstallationMinter) CreateRegistrationToken(ctx context.Context, installationID int64, owner string, repo string) (RegistrationTokenResponse, error) {
	client, _, err := appauth.NewInstallationClient(ctx, m.Source, installationID)
	if err != nil {
		return RegistrationTokenResponse{}, err
	}
	token, err := ghplatform.CreateRunnerRegistrationToken(ctx, client, owner, repo)
	if err != nil {
		return RegistrationTokenResponse{}, err
	}
	return RegistrationTokenResponse{
		Token:     token.Token,
		ExpiresAt: token.ExpiresAt,
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

var _ TokenMinter = AppInstallationMinter{}
