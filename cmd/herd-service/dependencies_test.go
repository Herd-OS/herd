package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/commands"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/controlplane/workflowevents"
	"github.com/herd-os/herd/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildServiceDependenciesProductionRejectsUnsafeDefaults(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()

	deps, err := buildServiceDependenciesWithOptions(cfg, st, log.New(io.Discard, "", 0), productionDependencyOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow event processor")
	assert.Empty(t, deps)
}

func TestBuildServiceDependenciesProductionRejectsMissingArtifactStore(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()

	deps, err := buildServiceDependenciesWithOptions(cfg, st, log.New(io.Discard, "", 0), productionDependencyOptions{
		WorkflowEventProcessor: fixedWorkflowProcessor{},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact store")
	assert.Empty(t, deps)
}

func TestBuildServiceDependenciesProductionRegistersRealRoutes(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()
	deps, err := buildServiceDependenciesWithOptions(cfg, st, log.New(io.Discard, "", 0), productionDependencyOptions{
		OIDCValidator:          fixedOIDCValidator{},
		WorkflowEventProcessor: fixedWorkflowProcessor{},
		ArtifactStore:          emptyArtifactStore{},
	})
	require.NoError(t, err)
	require.NotNil(t, deps.IssueCommentCommandHandler)
	require.NotNil(t, deps.JobResultsRoute)
	require.NotNil(t, deps.WorkflowEventsRoute)
	require.NotNil(t, deps.RunnerRegistrationTokenRoute)
	require.NotNil(t, deps.RegisterRepositoryRoute)

	handler, err := service.NewServer(cfg, deps)
	require.NoError(t, err)

	jobReq := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/results", strings.NewReader(`{`))
	jobReq.Header.Set("Authorization", "Bearer oidc")
	jobResp := httptest.NewRecorder()
	handler.ServeHTTP(jobResp, jobReq)
	assert.Equal(t, http.StatusBadRequest, jobResp.Code)
	assert.Contains(t, jobResp.Body.String(), "malformed JSON result payload")

	eventReq := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-events", strings.NewReader(`{`))
	eventReq.Header.Set("Authorization", "Bearer oidc")
	eventResp := httptest.NewRecorder()
	handler.ServeHTTP(eventResp, eventReq)
	assert.Equal(t, http.StatusBadRequest, eventResp.Code)
	assert.Contains(t, eventResp.Body.String(), "invalid workflow event payload")
}

func TestProductionCommandDispatcherRejectsSyntheticDefaults(t *testing.T) {
	err := productionCommandDispatcher{}.DispatchCommand(context.Background(), commands.DispatchCommand{
		RepositoryID:   7,
		InstallationID: 9,
		Owner:          "octo",
		Repo:           "repo",
		IssueNumber:    849,
		PRNumber:       849,
		Command:        commands.ParsedCommand{Kind: commands.CommandReview},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "durable batch/ref/head context")
	assert.NotContains(t, err.Error(), "batch 1")
}

func validProductionServiceConfig(t *testing.T) service.Config {
	t.Helper()
	return service.Config{
		GitHubAppID:         123,
		GitHubAppPrivateKey: string(testPrivateKeyPEM(t)),
		WebhookSecret:       "webhook-secret",
		PublicURL:           "https://control.example.test",
		DatabaseURL:         "postgres://example",
		Env:                 "production",
		AppLogin:            "herd-os",
		OIDCAudience:        "herd-control-plane",
	}
}

func testPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

type emptyArtifactStore struct{}

func (emptyArtifactStore) OpenArtifact(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

type fixedOIDCValidator struct{}

func (fixedOIDCValidator) Validate(context.Context, string) (jobs.OIDCClaims, error) {
	return jobs.OIDCClaims{
		Issuer:     jobs.GitHubActionsIssuer,
		Audience:   []string{"herd-control-plane"},
		Repository: "octo/herd",
		Ref:        "refs/heads/main",
		Workflow:   ".github/workflows/herd-integrator.yml",
		ExpiresAt:  time.Now().Add(time.Hour),
	}, nil
}

var _ artifacts.Store = emptyArtifactStore{}

type fixedWorkflowProcessor struct{}

func (fixedWorkflowProcessor) ProcessWorkflowEvent(context.Context, store.Repository, workflowevents.Event) error {
	return nil
}
