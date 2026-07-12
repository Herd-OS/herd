package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ghapi "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type registerFakeStore struct {
	installations []store.Installation
	repositories  map[string]store.Repository
	attempts      []store.RegistrationAttempt
	tokens        []store.RunnerBootstrapToken
	err           error
}

func newRegisterFakeStore() *registerFakeStore {
	return &registerFakeStore{repositories: map[string]store.Repository{}}
}

func (s *registerFakeStore) UpsertInstallation(_ context.Context, i store.Installation) error {
	if s.err != nil {
		return s.err
	}
	s.installations = append(s.installations, i)
	return nil
}

func (s *registerFakeStore) UpsertRepository(_ context.Context, r store.Repository) (store.Repository, error) {
	if s.err != nil {
		return store.Repository{}, s.err
	}
	if r.ID == 0 {
		r.ID = 1234
	}
	s.repositories[r.Owner+"/"+r.Name] = r
	return r, nil
}

func (s *registerFakeStore) CreateRegistrationAttempt(_ context.Context, a store.RegistrationAttempt) error {
	s.attempts = append(s.attempts, a)
	return nil
}

func (s *registerFakeStore) CreateRunnerBootstrapToken(_ context.Context, tok store.RunnerBootstrapToken) error {
	if s.err != nil {
		return s.err
	}
	s.tokens = append(s.tokens, tok)
	return nil
}

type fakeSetupVerifier struct {
	repo SetupRepository
	err  error
	got  string
}

func (v *fakeSetupVerifier) VerifySetupRepository(_ context.Context, setupToken string, _ string, _ string) (SetupRepository, error) {
	v.got = setupToken
	return v.repo, v.err
}

type fakeAppVerifier struct {
	err            error
	installationID int64
}

func (v *fakeAppVerifier) VerifyAppAccess(_ context.Context, installationID int64, _ string, _ string) error {
	v.installationID = installationID
	return v.err
}

func TestRegisterHandlerValidRegistration(t *testing.T) {
	st := newRegisterFakeStore()
	setup := &fakeSetupVerifier{repo: validSetupRepository()}
	app := &fakeAppVerifier{}
	handler := NewRegisterHandler(RegisterHandlerOptions{
		Store:           st,
		SetupVerifier:   setup,
		AppVerifier:     app,
		AppLogin:        "herd-os",
		ControlPlaneURL: "https://api.herd-os.com",
		Now:             func() time.Time { return time.Unix(100, 0).UTC() },
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, registerRequest(`{"repository":"octo/herd","owner":"octo","name":"herd","setup_token":"gho_human","app_login":"@herd-os"}`))

	require.Equal(t, http.StatusCreated, rec.Code)
	var resp struct {
		RepositoryID         int64  `json:"repository_id"`
		InstallationID       int64  `json:"installation_id"`
		RunnerBootstrapToken string `json:"runner_bootstrap_token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, int64(1234), resp.RepositoryID)
	assert.Equal(t, int64(42), resp.InstallationID)
	assert.NotEmpty(t, resp.RunnerBootstrapToken)
	assert.Equal(t, "gho_human", setup.got)
	assert.Equal(t, int64(42), app.installationID)
	require.Len(t, st.tokens, 1)
	assert.Equal(t, int64(1234), st.tokens[0].RepositoryID)
	assert.NotEqual(t, resp.RunnerBootstrapToken, st.tokens[0].TokenHash)
	assert.NotContains(t, st.tokens[0].TokenHash, "gho_human")
	require.Len(t, st.attempts, 1)
	assert.Equal(t, "registered", st.attempts[0].Status)
	for _, attempt := range st.attempts {
		assert.NotContains(t, attempt.Error, "gho_human")
		assert.NotContains(t, string(attempt.Metadata), "gho_human")
	}
}

func TestRegisterHandlerFailures(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		setup      *fakeSetupVerifier
		app        *fakeAppVerifier
		wantStatus int
		wantError  string
	}{
		{
			name:       "missing setup token",
			body:       `{"owner":"octo","name":"herd"}`,
			setup:      &fakeSetupVerifier{repo: validSetupRepository()},
			app:        &fakeAppVerifier{},
			wantStatus: http.StatusBadRequest,
			wantError:  "setup_token",
		},
		{
			name:       "insufficient permission rejected",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human"}`,
			setup:      &fakeSetupVerifier{repo: SetupRepository{ID: 99, Admin: false, InstallationID: 42}},
			app:        &fakeAppVerifier{},
			wantStatus: http.StatusForbidden,
			wantError:  "admin access",
		},
		{
			name:       "setup verifier unauthorized",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human"}`,
			setup:      &fakeSetupVerifier{err: ErrRepoUnauthorized},
			app:        &fakeAppVerifier{},
			wantStatus: http.StatusForbidden,
			wantError:  "gh auth login",
		},
		{
			name:       "app not installed",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human"}`,
			setup:      &fakeSetupVerifier{repo: SetupRepository{ID: 99, Admin: true}},
			app:        &fakeAppVerifier{},
			wantStatus: http.StatusConflict,
			wantError:  "GitHub App is not installed",
		},
		{
			name:       "app installation mismatch",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human"}`,
			setup:      &fakeSetupVerifier{repo: validSetupRepository()},
			app:        &fakeAppVerifier{err: ErrAppInstallationMatch},
			wantStatus: http.StatusConflict,
			wantError:  "App installation",
		},
		{
			name:       "app verifier transient failure",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human"}`,
			setup:      &fakeSetupVerifier{repo: validSetupRepository()},
			app:        &fakeAppVerifier{err: errors.New("github 500")},
			wantStatus: http.StatusBadGateway,
			wantError:  "GitHub unavailable",
		},
		{
			name:       "wrong app login",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human","app_login":"other-app"}`,
			setup:      &fakeSetupVerifier{repo: validSetupRepository()},
			app:        &fakeAppVerifier{},
			wantStatus: http.StatusBadRequest,
			wantError:  "app_login",
		},
		{
			name:       "github unavailable",
			body:       `{"owner":"octo","name":"herd","setup_token":"gho_human"}`,
			setup:      &fakeSetupVerifier{err: errors.New("github unavailable")},
			app:        &fakeAppVerifier{},
			wantStatus: http.StatusBadGateway,
			wantError:  "GitHub unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newRegisterFakeStore()
			handler := NewRegisterHandler(RegisterHandlerOptions{
				Store:         st,
				SetupVerifier: tt.setup,
				AppVerifier:   tt.app,
				AppLogin:      "herd-os",
			})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, registerRequest(tt.body))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantError)
			assert.Empty(t, st.tokens)
			assert.NotContains(t, rec.Body.String(), "gho_human")
		})
	}
}

func TestRegisterHandlerSetupVerifierTransientFailureDoesNotSuggestGhAuthLogin(t *testing.T) {
	st := newRegisterFakeStore()
	handler := NewRegisterHandler(RegisterHandlerOptions{
		Store:         st,
		SetupVerifier: &fakeSetupVerifier{err: errors.New("github 502")},
		AppVerifier:   &fakeAppVerifier{},
		AppLogin:      "herd-os",
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, registerRequest(`{"owner":"octo","name":"herd","setup_token":"gho_human"}`))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "retry repository registration")
	assert.NotContains(t, rec.Body.String(), "gh auth login")
	assert.Empty(t, st.tokens)
}

func TestSetupVerificationErrorResponseRateLimitsAreRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "core rate limit",
			err:  &ghapi.RateLimitError{Rate: ghapi.Rate{Limit: 1}},
		},
		{
			name: "abuse rate limit",
			err:  &ghapi.AbuseRateLimitError{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := setupVerificationErrorResponse(tt.err)

			assert.Equal(t, http.StatusBadGateway, status)
			assert.Contains(t, msg, "rate limit")
			assert.NotContains(t, msg, "gh auth login")
		})
	}
}

func registerRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v1/github/repositories/register", bytes.NewBufferString(body))
}

func validSetupRepository() SetupRepository {
	return SetupRepository{
		ID:             99,
		Owner:          "octo",
		Name:           "herd",
		FullName:       "octo/herd",
		DefaultBranch:  "main",
		Private:        true,
		Admin:          true,
		InstallationID: 42,
		AccountLogin:   "octo",
		AccountID:      100,
		AccountType:    "Organization",
	}
}
