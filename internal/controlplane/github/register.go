package github

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	ghapi "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	cpclient "github.com/herd-os/herd/internal/controlplane/client"
	"github.com/herd-os/herd/internal/controlplane/store"
	"golang.org/x/oauth2"
)

const (
	defaultBootstrapTokenTTL = 24 * time.Hour
	bootstrapTokenBytes      = 32
)

var (
	ErrRepoUnauthorized     = errors.New("repository requires admin access")
	ErrAppInstallation      = errors.New("GitHub App is not installed for this repository")
	ErrAppInstallationMatch = errors.New("GitHub App installation does not match requested repository")
)

type RegistrationStore interface {
	UpsertInstallation(ctx context.Context, i store.Installation) error
	UpsertRepository(ctx context.Context, r store.Repository) (store.Repository, error)
	CreateRegistrationAttempt(ctx context.Context, a store.RegistrationAttempt) error
	CreateRunnerBootstrapToken(ctx context.Context, t store.RunnerBootstrapToken) error
}

type SetupRepository struct {
	ID             int64
	Owner          string
	Name           string
	FullName       string
	DefaultBranch  string
	Private        bool
	Admin          bool
	InstallationID int64
	AccountLogin   string
	AccountID      int64
	AccountType    string
}

type SetupVerifier interface {
	VerifySetupRepository(ctx context.Context, setupToken string, owner string, name string) (SetupRepository, error)
}

type AppInstallationVerifier interface {
	VerifyAppAccess(ctx context.Context, installationID int64, owner string, name string) error
}

type RegisterHandler struct {
	store           RegistrationStore
	setupVerifier   SetupVerifier
	appVerifier     AppInstallationVerifier
	appLogin        string
	controlPlaneURL string
	now             func() time.Time
}

type RegisterHandlerOptions struct {
	Store           RegistrationStore
	SetupVerifier   SetupVerifier
	AppVerifier     AppInstallationVerifier
	AppLogin        string
	ControlPlaneURL string
	Now             func() time.Time
}

func NewRegisterHandler(opts RegisterHandlerOptions) http.Handler {
	h := RegisterHandler{
		store:           opts.Store,
		setupVerifier:   opts.SetupVerifier,
		appVerifier:     opts.AppVerifier,
		appLogin:        normalizeAppLogin(opts.AppLogin),
		controlPlaneURL: strings.TrimSpace(opts.ControlPlaneURL),
		now:             opts.Now,
	}
	if h.now == nil {
		h.now = func() time.Time { return time.Now().UTC() }
	}
	return h
}

func NewDefaultRegisterHandler(store RegistrationStore, appCfg appauth.AppConfig, appLogin string, controlPlaneURL string) (http.Handler, error) {
	tokenSource, _, err := appauth.NewGitHubTokenSource(appCfg)
	if err != nil {
		return nil, err
	}
	return NewRegisterHandler(RegisterHandlerOptions{
		Store:           store,
		SetupVerifier:   githubSetupVerifier{},
		AppVerifier:     githubAppVerifier{source: tokenSource},
		AppLogin:        appLogin,
		ControlPlaneURL: controlPlaneURL,
	}), nil
}

func (h RegisterHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "repository registration storage is not configured"})
		return
	}
	if h.setupVerifier == nil || h.appVerifier == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "repository registration GitHub verification is not configured"})
		return
	}

	var req cpclient.RegisterRepositoryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPayloadBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON registration request"})
		return
	}
	req.Owner = strings.TrimSpace(req.Owner)
	req.Name = strings.TrimSpace(req.Name)
	req.Repository = strings.TrimSpace(req.Repository)
	if req.Owner == "" || req.Name == "" {
		parts := strings.Split(req.Repository, "/")
		if len(parts) == 2 {
			req.Owner = strings.TrimSpace(parts[0])
			req.Name = strings.TrimSpace(parts[1])
		}
	}
	if req.Owner == "" || req.Name == "" || strings.TrimSpace(req.SetupToken) == "" {
		h.recordAttempt(r.Context(), 0, 0, req.Owner, req.Name, "rejected", "missing owner, name, or setup_token")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner, name, and setup_token are required"})
		return
	}
	if requestedApp := normalizeAppLogin(req.AppLogin); requestedApp != "" && h.appLogin != "" && requestedApp != h.appLogin {
		h.recordAttempt(r.Context(), 0, 0, req.Owner, req.Name, "rejected", "requested app_login does not match this Herd control plane")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "app_login does not match this Herd control plane; install the configured Herd GitHub App and retry `herd init`"})
		return
	}

	setupRepo, err := h.setupVerifier.VerifySetupRepository(r.Context(), req.SetupToken, req.Owner, req.Name)
	if err != nil {
		_, msg := setupVerificationErrorResponse(err)
		h.recordAttempt(r.Context(), 0, 0, req.Owner, req.Name, "rejected", sanitizeRegistrationError(msg, req.SetupToken))
		status, msg := setupVerificationErrorResponse(err)
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if !setupRepo.Admin {
		h.recordAttempt(r.Context(), setupRepo.ID, setupRepo.InstallationID, req.Owner, req.Name, "rejected", ErrRepoUnauthorized.Error())
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "repository registration requires admin access; run `gh auth login -h github.com` as a repo admin and retry"})
		return
	}
	if setupRepo.InstallationID == 0 {
		h.recordAttempt(r.Context(), setupRepo.ID, 0, req.Owner, req.Name, "rejected", ErrAppInstallation.Error())
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Herd GitHub App is not installed for this repository; install the App for the repository and retry `herd init`"})
		return
	}
	if err := h.appVerifier.VerifyAppAccess(r.Context(), setupRepo.InstallationID, req.Owner, req.Name); err != nil {
		if !errors.Is(err, ErrAppInstallationMatch) {
			h.recordAttempt(r.Context(), setupRepo.ID, setupRepo.InstallationID, req.Owner, req.Name, "rejected", "verify GitHub App installation access: GitHub unavailable")
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "verify GitHub App installation access: GitHub unavailable, retry repository registration"})
			return
		}
		h.recordAttempt(r.Context(), setupRepo.ID, setupRepo.InstallationID, req.Owner, req.Name, "rejected", ErrAppInstallationMatch.Error())
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Herd GitHub App installation cannot access this repository; update the App installation repository selection and retry `herd init`"})
		return
	}

	now := h.now()
	if err := h.store.UpsertInstallation(r.Context(), store.Installation{
		ID:           setupRepo.InstallationID,
		AccountLogin: setupRepo.AccountLogin,
		AccountID:    setupRepo.AccountID,
		TargetType:   setupRepo.AccountType,
		Permissions:  json.RawMessage(`{}`),
		UpdatedAt:    now,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store GitHub App installation: storage unavailable"})
		return
	}
	repo, err := h.store.UpsertRepository(r.Context(), store.Repository{
		GitHubID:       setupRepo.ID,
		InstallationID: setupRepo.InstallationID,
		Owner:          req.Owner,
		Name:           req.Name,
		DefaultBranch:  setupRepo.DefaultBranch,
		Private:        setupRepo.Private,
		RegisteredAt:   now,
		UpdatedAt:      now,
		Metadata:       mustJSON(map[string]any{"full_name": setupRepo.FullName, "registered_by": "setup"}),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store repository registration: storage unavailable"})
		return
	}
	plainToken, tokenHash, err := newBootstrapToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "generate runner bootstrap token"})
		return
	}
	if err := h.store.CreateRunnerBootstrapToken(r.Context(), store.RunnerBootstrapToken{
		RepositoryID: repo.ID,
		TokenHash:    tokenHash,
		CreatedAt:    now,
		ExpiresAt:    now.Add(defaultBootstrapTokenTTL),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store runner bootstrap token: storage unavailable"})
		return
	}
	h.recordAttempt(r.Context(), repo.ID, setupRepo.InstallationID, req.Owner, req.Name, "registered", "")

	writeJSON(w, http.StatusCreated, cpclient.RegisterRepositoryResponse{
		RepositoryID:         repo.ID,
		InstallationID:       setupRepo.InstallationID,
		RunnerBootstrapToken: plainToken,
		ControlPlaneURL:      h.controlPlaneURL,
	})
}

func setupVerificationErrorResponse(err error) (int, string) {
	if errors.Is(err, ErrRepoUnauthorized) {
		return http.StatusForbidden, "repository registration requires a GitHub account with admin access; run `gh auth login -h github.com` as a repo admin and retry"
	}
	var rateLimitErr *ghapi.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return http.StatusBadGateway, "verify GitHub setup credential: GitHub rate limit reached, retry repository registration"
	}
	var abuseErr *ghapi.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		return http.StatusBadGateway, "verify GitHub setup credential: GitHub rate limit reached, retry repository registration"
	}
	var ghErr *ghapi.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		switch ghErr.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return ghErr.Response.StatusCode, "verify GitHub setup credential: run `gh auth login -h github.com` with an account that has admin access to the repository"
		default:
			if ghErr.Response.StatusCode == http.StatusTooManyRequests || ghErr.Response.StatusCode >= 500 {
				return http.StatusBadGateway, "verify GitHub setup credential: GitHub unavailable, retry repository registration"
			}
		}
	}
	return http.StatusBadGateway, "verify GitHub setup credential: GitHub unavailable, retry repository registration"
}

func (h RegisterHandler) recordAttempt(ctx context.Context, repositoryID int64, installationID int64, owner string, name string, status string, msg string) {
	if h.store == nil {
		return
	}
	_ = h.store.CreateRegistrationAttempt(ctx, store.RegistrationAttempt{
		RepositoryID:   repositoryID,
		InstallationID: installationID,
		Owner:          owner,
		Name:           name,
		Status:         status,
		Error:          msg,
		Metadata:       json.RawMessage(`{}`),
		CreatedAt:      h.now(),
	})
}

func sanitizeRegistrationError(msg string, setupToken string) string {
	msg = strings.TrimSpace(msg)
	token := strings.TrimSpace(setupToken)
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[REDACTED]")
	}
	for _, prefix := range []string{"ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_"} {
		for {
			idx := strings.Index(msg, prefix)
			if idx < 0 {
				break
			}
			end := idx + len(prefix)
			for end < len(msg) {
				c := msg[end]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
					end++
					continue
				}
				break
			}
			msg = msg[:idx] + "[REDACTED]" + msg[end:]
		}
	}
	return msg
}

type githubSetupVerifier struct{}

func (githubSetupVerifier) VerifySetupRepository(ctx context.Context, setupToken string, owner string, name string) (SetupRepository, error) {
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: setupToken}))
	client := ghapi.NewClient(httpClient)
	if _, _, err := client.Users.Get(ctx, ""); err != nil {
		return SetupRepository{}, fmt.Errorf("verify authenticated GitHub user: %w", err)
	}
	repo, _, err := client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return SetupRepository{}, fmt.Errorf("verify repository access: %w", err)
	}
	admin := repo.GetPermissions()["admin"]
	if !admin {
		return SetupRepository{}, ErrRepoUnauthorized
	}
	installation, _, err := client.Apps.FindRepositoryInstallation(ctx, owner, name)
	if err != nil {
		if githubRateLimitError(err) {
			return SetupRepository{}, fmt.Errorf("find repository installation: %w", err)
		}
		if githubStatusCode(err) == http.StatusNotFound || githubStatusCode(err) == http.StatusForbidden {
			return SetupRepository{}, ErrAppInstallation
		}
		return SetupRepository{}, fmt.Errorf("find repository installation: %w", err)
	}
	out := SetupRepository{
		ID:             repo.GetID(),
		Owner:          owner,
		Name:           name,
		FullName:       repo.GetFullName(),
		DefaultBranch:  repo.GetDefaultBranch(),
		Private:        repo.GetPrivate(),
		Admin:          true,
		InstallationID: installation.GetID(),
	}
	if installation.GetAccount() != nil {
		out.AccountLogin = installation.GetAccount().GetLogin()
		out.AccountID = installation.GetAccount().GetID()
		out.AccountType = installation.GetAccount().GetType()
	}
	return out, nil
}

type githubAppVerifier struct {
	source appauth.TokenSource
}

func (v githubAppVerifier) VerifyAppAccess(ctx context.Context, installationID int64, owner string, name string) error {
	client, _, err := appauth.NewInstallationClient(ctx, v.source, installationID)
	if err != nil {
		return err
	}
	repo, _, err := client.Repositories.Get(ctx, owner, name)
	if err != nil {
		if githubRateLimitError(err) {
			return fmt.Errorf("verify installation repository access: %w", err)
		}
		if githubStatusCode(err) == http.StatusNotFound || githubStatusCode(err) == http.StatusForbidden {
			return ErrAppInstallationMatch
		}
		return fmt.Errorf("verify installation repository access: %w", err)
	}
	if !strings.EqualFold(repo.GetOwner().GetLogin(), owner) || !strings.EqualFold(repo.GetName(), name) {
		return ErrAppInstallationMatch
	}
	return nil
}

func githubStatusCode(err error) int {
	var ghErr *ghapi.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		return ghErr.Response.StatusCode
	}
	return 0
}

func githubRateLimitError(err error) bool {
	var rateLimitErr *ghapi.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return true
	}
	var abuseErr *ghapi.AbuseRateLimitError
	return errors.As(err, &abuseErr)
}

func newBootstrapToken() (plain string, hash string, err error) {
	raw := make([]byte, bootstrapTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plain = "hrb_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plain))
	return plain, hex.EncodeToString(sum[:]), nil
}

func normalizeAppLogin(login string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(login)), "@")
}
