package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/client"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRegistrationStore struct {
	installations []store.Installation
	repositories  []store.Repository
	attempts      []store.RegistrationAttempt
	tokens        []store.RunnerBootstrapToken
}

func (s *fakeRegistrationStore) UpsertInstallation(_ context.Context, i store.Installation) error {
	s.installations = append(s.installations, i)
	return nil
}

func (s *fakeRegistrationStore) UpsertRepository(_ context.Context, r store.Repository) (store.Repository, error) {
	r.ID = 99
	s.repositories = append(s.repositories, r)
	return r, nil
}

func (s *fakeRegistrationStore) CreateRegistrationAttempt(_ context.Context, a store.RegistrationAttempt) error {
	s.attempts = append(s.attempts, a)
	return nil
}

func (s *fakeRegistrationStore) CreateRunnerBootstrapToken(_ context.Context, t store.RunnerBootstrapToken) error {
	s.tokens = append(s.tokens, t)
	return nil
}

type fakeSetupVerifier struct {
	repo SetupRepository
	err  error
	got  string
}

func (v *fakeSetupVerifier) VerifySetupToken(_ context.Context, setupToken, _, _ string) (SetupRepository, error) {
	v.got = setupToken
	return v.repo, v.err
}

type fakeAppVerifier struct {
	err            error
	installationID int64
}

func (v *fakeAppVerifier) VerifyAppInstallation(_ context.Context, installationID int64, _, _ string) error {
	v.installationID = installationID
	return v.err
}

func TestRegistrarRegistersAdminRepositoryAndStoresHashedBootstrapToken(t *testing.T) {
	st := &fakeRegistrationStore{}
	setup := &fakeSetupVerifier{repo: SetupRepository{
		GitHubID:       123,
		InstallationID: 456,
		Owner:          "octo",
		Name:           "repo",
		DefaultBranch:  "main",
		Admin:          true,
	}}
	app := &fakeAppVerifier{}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	registrar := Registrar{
		Store:       st,
		Setup:       setup,
		App:         app,
		AppLogin:    "herd-os",
		ControlURL:  "https://cp.example.com",
		Now:         func() time.Time { return now },
		TokenSource: func() (string, error) { return "bootstrap-token", nil },
	}

	body, status := registrar.Register(context.Background(), client.RegisterRepositoryRequest{
		Repository: "octo/repo",
		SetupToken: "setup-token",
	}, nil)

	require.Equal(t, http.StatusOK, status)
	resp, ok := body.(client.RegisterRepositoryResponse)
	require.True(t, ok)
	assert.Equal(t, int64(99), resp.RepositoryID)
	assert.Equal(t, int64(456), resp.InstallationID)
	assert.Equal(t, "bootstrap-token", resp.RunnerBootstrapToken)
	assert.Equal(t, "https://cp.example.com", resp.ControlPlaneURL)
	assert.Equal(t, "setup-token", setup.got)
	require.Len(t, st.repositories, 1)
	assert.Equal(t, int64(123), st.repositories[0].GitHubID)
	require.Len(t, st.tokens, 1)
	assert.Equal(t, int64(99), st.tokens[0].RepositoryID)
	assert.Equal(t, hashBootstrapToken("bootstrap-token"), st.tokens[0].TokenHash)
	assert.NotEqual(t, "bootstrap-token", st.tokens[0].TokenHash)
	require.Len(t, st.attempts, 1)
	assert.Equal(t, "registered", st.attempts[0].Status)
	assert.NotContains(t, st.attempts[0].Error, "setup-token")
}

func TestRegistrarRejectsInsufficientPermissionWithoutPersistingToken(t *testing.T) {
	st := &fakeRegistrationStore{}
	registrar := Registrar{
		Store: st,
		Setup: &fakeSetupVerifier{repo: SetupRepository{
			GitHubID:       123,
			InstallationID: 456,
			Admin:          false,
		}},
		App:      &fakeAppVerifier{},
		AppLogin: "herd-os",
	}

	body, status := registrar.Register(context.Background(), client.RegisterRepositoryRequest{
		Owner:      "octo",
		Name:       "repo",
		SetupToken: "setup-token",
	}, nil)

	require.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body.(map[string]string)["error"], "admin access")
	assert.Empty(t, st.tokens)
	require.Len(t, st.attempts, 1)
	assert.NotContains(t, st.attempts[0].Error, "setup-token")
}

func TestRegistrarRejectsAppInstallationMismatch(t *testing.T) {
	st := &fakeRegistrationStore{}
	registrar := Registrar{
		Store: st,
		Setup: &fakeSetupVerifier{repo: SetupRepository{
			GitHubID:       123,
			InstallationID: 456,
			Admin:          true,
		}},
		App:      &fakeAppVerifier{err: errors.New("not found")},
		AppLogin: "herd-os",
	}

	body, status := registrar.Register(context.Background(), client.RegisterRepositoryRequest{
		Owner:      "octo",
		Name:       "repo",
		SetupToken: "setup-token",
	}, nil)

	require.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body.(map[string]string)["error"], "install or grant repository access")
	assert.Empty(t, st.tokens)
	require.Len(t, st.attempts, 1)
	assert.Equal(t, "rejected", st.attempts[0].Status)
}

func TestRegistrarValidationErrorsAreActionableJSON(t *testing.T) {
	registrar := Registrar{Store: &fakeRegistrationStore{}, Setup: &fakeSetupVerifier{}, App: &fakeAppVerifier{}}

	tests := []struct {
		name       string
		req        client.RegisterRepositoryRequest
		wantStatus int
		wantError  string
	}{
		{
			name:       "missing repo",
			req:        client.RegisterRepositoryRequest{SetupToken: "token"},
			wantStatus: http.StatusBadRequest,
			wantError:  "repository owner and name are required",
		},
		{
			name:       "missing setup token",
			req:        client.RegisterRepositoryRequest{Repository: "octo/repo"},
			wantStatus: http.StatusBadRequest,
			wantError:  "gh auth login -h github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, status := registrar.Register(context.Background(), tt.req, nil)
			require.Equal(t, tt.wantStatus, status)
			assert.Contains(t, body.(map[string]string)["error"], tt.wantError)
		})
	}
}
