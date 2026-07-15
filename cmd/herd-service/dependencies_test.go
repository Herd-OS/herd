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

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/cli"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/commands"
	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/controlplane/workflowevents"
	"github.com/herd-os/herd/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildServiceDependenciesProductionWiresCommandDispatcher(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()

	deps, err := buildServiceDependencies(cfg, st, log.New(io.Discard, "", 0))

	require.NoError(t, err)
	require.NotNil(t, deps.IssueCommentCommandHandler)
}

func TestBuildServiceDependenciesProductionUsesDefaultWorkflowProcessorAndArtifactStore(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()

	deps, err := buildServiceDependencies(cfg, st, log.New(io.Discard, "", 0))

	require.NoError(t, err)
	require.NotNil(t, deps.WorkflowEventProcessor)
	require.NotNil(t, deps.JobResultsRoute)
	require.NotNil(t, deps.WorkflowEventsRoute)
	require.NotNil(t, deps.RunnerRegistrationTokenRoute)
	require.NotNil(t, deps.RegisterRepositoryRoute)
}

func TestBuildServiceDependenciesProductionRegistersRealRoutes(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()
	deps, err := buildServiceDependenciesWithOptions(cfg, st, log.New(io.Discard, "", 0), productionDependencyOptions{
		OIDCValidator:          fixedOIDCValidator{},
		CommandDispatcher:      fixedCommandDispatcher{},
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

func TestProductionCommandDispatcherRequiresRealAppContextWithoutSyntheticDefaults(t *testing.T) {
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
	assert.Contains(t, err.Error(), "GitHub App token source")
	assert.NotContains(t, err.Error(), "durable batch/ref/head context")
	assert.NotContains(t, err.Error(), "batch 1")
}

func TestCommandWorkflowFileIsManagedWorkflow(t *testing.T) {
	managed := cli.WorkflowFiles()
	tests := []struct {
		name string
		kind cpdispatch.JobKind
	}{
		{name: "review", kind: cpdispatch.JobKindReview},
		{name: "review fix", kind: cpdispatch.JobKindReviewFix},
		{name: "ci fix", kind: cpdispatch.JobKindCIFix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowFile := commandWorkflowFile(tt.kind)

			assert.Equal(t, "herd-worker.yml", workflowFile)
			assert.Contains(t, managed, workflowFile)
			assert.NotContains(t, managed, "herd-review.yml")
		})
	}
}

func TestCommandTargetFromPullRequest(t *testing.T) {
	tests := []struct {
		name      string
		kind      cpdispatch.JobKind
		milestone *gh.Milestone
		wantBatch int
		wantErr   string
	}{
		{
			name:      "review without batch milestone uses PR number context",
			kind:      cpdispatch.JobKindReview,
			wantBatch: 42,
		},
		{
			name:      "review with batch milestone uses milestone",
			kind:      cpdispatch.JobKindReview,
			milestone: &gh.Milestone{Number: gh.Ptr(849)},
			wantBatch: 849,
		},
		{
			name:    "fix still requires batch milestone",
			kind:    cpdispatch.JobKindReviewFix,
			wantErr: "durable batch milestone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := commandTargetFromPullRequest(commands.DispatchCommand{
				IssueNumber: 0,
				PRNumber:    42,
			}, tt.kind, &gh.PullRequest{
				Head: &gh.PullRequestBranch{
					Ref: gh.Ptr("feature-branch"),
					SHA: gh.Ptr("head-sha"),
				},
				Base: &gh.PullRequestBranch{
					Ref: gh.Ptr("main"),
					SHA: gh.Ptr("base-sha"),
				},
				Milestone: tt.milestone,
			})

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantBatch, target.BatchNumber)
			assert.Equal(t, 42, target.IssueNumber)
			assert.Equal(t, "feature-branch", target.Ref)
			assert.Equal(t, "feature-branch", target.BatchBranch)
			assert.Equal(t, "head-sha", target.BaseSHA)
			assert.Equal(t, "head-sha", target.HeadSHA)
		})
	}
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

type fixedCommandDispatcher struct{}

func (fixedCommandDispatcher) DispatchCommand(context.Context, commands.DispatchCommand) error {
	return nil
}
