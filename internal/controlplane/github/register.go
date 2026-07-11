package github

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/client"
	"github.com/herd-os/herd/internal/controlplane/store"
	"golang.org/x/oauth2"
)

const defaultBootstrapTokenTTL = 24 * time.Hour

type RegistrationStore interface {
	UpsertInstallation(ctx context.Context, i store.Installation) error
	UpsertRepository(ctx context.Context, r store.Repository) (store.Repository, error)
	CreateRegistrationAttempt(ctx context.Context, a store.RegistrationAttempt) error
	CreateRunnerBootstrapToken(ctx context.Context, t store.RunnerBootstrapToken) error
}

type SetupVerifier interface {
	VerifySetupToken(ctx context.Context, setupToken, owner, name string) (SetupRepository, error)
}

type AppInstallationVerifier interface {
	VerifyAppInstallation(ctx context.Context, installationID int64, owner, name string) error
}

type SetupRepository struct {
	GitHubID       int64
	InstallationID int64
	Owner          string
	Name           string
	DefaultBranch  string
	Private        bool
	Admin          bool
}

type GitHubSetupVerifier struct{}

func (v GitHubSetupVerifier) VerifySetupToken(ctx context.Context, setupToken, owner, name string) (SetupRepository, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: setupToken})
	ghClient := gh.NewClient(oauth2.NewClient(ctx, ts))

	repo, _, err := ghClient.Repositories.Get(ctx, owner, name)
	if err != nil {
		return SetupRepository{}, err
	}
	installation, _, err := ghClient.Apps.FindRepositoryInstallation(ctx, owner, name)
	if err != nil {
		return SetupRepository{}, err
	}
	perms := repo.GetPermissions()
	return SetupRepository{
		GitHubID:       repo.GetID(),
		InstallationID: installation.GetID(),
		Owner:          owner,
		Name:           name,
		DefaultBranch:  repo.GetDefaultBranch(),
		Private:        repo.GetPrivate(),
		Admin:          perms["admin"] || perms["maintain"],
	}, nil
}

type GitHubAppVerifier struct {
	TokenSource appauth.TokenSource
}

func (v GitHubAppVerifier) VerifyAppInstallation(ctx context.Context, installationID int64, owner, name string) error {
	client, _, err := appauth.NewInstallationClient(ctx, v.TokenSource, installationID)
	if err != nil {
		return err
	}
	if _, _, err := client.Repositories.Get(ctx, owner, name); err != nil {
		return err
	}
	return nil
}

type Registrar struct {
	Store       RegistrationStore
	Setup       SetupVerifier
	App         AppInstallationVerifier
	AppLogin    string
	ControlURL  string
	Now         func() time.Time
	TokenSource func() (string, error)
}

func (r Registrar) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	in, err := decodeRequest(req)
	resp, status := r.Register(req.Context(), in, err)
	writeJSON(w, status, resp)
}

func (r Registrar) Register(ctx context.Context, in client.RegisterRepositoryRequest, decodeErr error) (any, int) {
	if decodeErr != nil {
		return errorBody("invalid JSON request body"), http.StatusBadRequest
	}
	owner, name, err := normalizeRepository(in)
	if err != nil {
		return errorBody(err.Error()), http.StatusBadRequest
	}
	if strings.TrimSpace(in.SetupToken) == "" {
		return errorBody("setup_token is required; run `gh auth login -h github.com` before `herd init`"), http.StatusBadRequest
	}
	if r.Store == nil || r.Setup == nil || r.App == nil {
		return errorBody("repository registration is not configured on this Herd control plane"), http.StatusServiceUnavailable
	}
	appLogin := strings.TrimSpace(in.AppLogin)
	if appLogin == "" {
		appLogin = strings.TrimSpace(r.AppLogin)
	}
	if appLogin == "" {
		appLogin = "herd-os"
	}

	repo, err := r.Setup.VerifySetupToken(ctx, in.SetupToken, owner, name)
	if err != nil {
		_ = r.recordAttempt(ctx, owner, name, 0, 0, "rejected", err.Error())
		return errorBody("GitHub setup authorization failed; run `gh auth login -h github.com` with an account that has admin access to " + owner + "/" + name), http.StatusUnauthorized
	}
	if !repo.Admin {
		msg := "GitHub setup credential does not have admin access to " + owner + "/" + name + "; use an admin account or update repository permissions"
		_ = r.recordAttempt(ctx, owner, name, repo.GitHubID, repo.InstallationID, "rejected", msg)
		return errorBody(msg), http.StatusForbidden
	}
	if repo.InstallationID == 0 {
		msg := "GitHub App @" + strings.TrimPrefix(appLogin, "@") + " is not installed on " + owner + "/" + name + "; install the App and retry `herd init`"
		_ = r.recordAttempt(ctx, owner, name, repo.GitHubID, 0, "rejected", msg)
		return errorBody(msg), http.StatusForbidden
	}
	if err := r.App.VerifyAppInstallation(ctx, repo.InstallationID, owner, name); err != nil {
		msg := "GitHub App @" + strings.TrimPrefix(appLogin, "@") + " cannot access " + owner + "/" + name + "; install or grant repository access to the App and retry `herd init`"
		_ = r.recordAttempt(ctx, owner, name, repo.GitHubID, repo.InstallationID, "rejected", msg)
		return errorBody(msg), http.StatusForbidden
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if repo.DefaultBranch == "" {
		repo.DefaultBranch = "main"
	}
	if err := r.Store.UpsertInstallation(ctx, store.Installation{
		ID:           repo.InstallationID,
		AccountLogin: owner,
		TargetType:   "Repository",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		return errorBody("storing GitHub App installation failed"), http.StatusInternalServerError
	}
	stored, err := r.Store.UpsertRepository(ctx, store.Repository{
		GitHubID:       repo.GitHubID,
		InstallationID: repo.InstallationID,
		Owner:          owner,
		Name:           name,
		DefaultBranch:  repo.DefaultBranch,
		Private:        repo.Private,
		RegisteredAt:   now,
		UpdatedAt:      now,
	})
	if err != nil {
		return errorBody("storing repository registration failed"), http.StatusInternalServerError
	}
	token, err := r.newBootstrapToken()
	if err != nil {
		return errorBody("generating runner bootstrap token failed"), http.StatusInternalServerError
	}
	tokenHash := hashBootstrapToken(token)
	if err := r.Store.CreateRunnerBootstrapToken(ctx, store.RunnerBootstrapToken{
		RepositoryID: stored.ID,
		TokenHash:    tokenHash,
		CreatedAt:    now,
		ExpiresAt:    now.Add(defaultBootstrapTokenTTL),
	}); err != nil {
		return errorBody("storing runner bootstrap token failed"), http.StatusInternalServerError
	}
	_ = r.recordAttempt(ctx, owner, name, stored.ID, repo.InstallationID, "registered", "")

	return client.RegisterRepositoryResponse{
		RepositoryID:         stored.ID,
		InstallationID:       repo.InstallationID,
		RunnerBootstrapToken: token,
		ControlPlaneURL:      r.ControlURL,
	}, http.StatusOK
}

func decodeRequest(req *http.Request) (client.RegisterRepositoryRequest, error) {
	var in client.RegisterRepositoryRequest
	err := json.NewDecoder(req.Body).Decode(&in)
	return in, err
}

func normalizeRepository(in client.RegisterRepositoryRequest) (string, string, error) {
	owner := strings.TrimSpace(in.Owner)
	name := strings.TrimSpace(in.Name)
	if owner == "" || name == "" {
		repo := strings.Trim(strings.TrimSpace(in.Repository), "/")
		parts := strings.Split(repo, "/")
		if len(parts) == 2 {
			if owner == "" {
				owner = parts[0]
			}
			if name == "" {
				name = parts[1]
			}
		}
	}
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("repository owner and name are required")
	}
	return owner, name, nil
}

func (r Registrar) recordAttempt(ctx context.Context, owner, name string, repoID, installationID int64, status, msg string) error {
	if r.Store == nil {
		return nil
	}
	return r.Store.CreateRegistrationAttempt(ctx, store.RegistrationAttempt{
		RepositoryID:   repoID,
		InstallationID: installationID,
		Owner:          owner,
		Name:           name,
		Status:         status,
		Error:          msg,
		CreatedAt:      time.Now().UTC(),
	})
}

func (r Registrar) newBootstrapToken() (string, error) {
	if r.TokenSource != nil {
		return r.TokenSource()
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "hrb_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashBootstrapToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
