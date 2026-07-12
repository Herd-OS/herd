package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/artifacts"
	cpclient "github.com/herd-os/herd/internal/controlplane/client"
	"github.com/herd-os/herd/internal/controlplane/commands"
	"github.com/herd-os/herd/internal/controlplane/dispatch"
	cpgithub "github.com/herd-os/herd/internal/controlplane/github"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/runners"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostedAppFlowWithIdempotencyAndMigrationRejections(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st := store.NewMemoryStore()
	gh := &hostedAppGitHub{headSHA: "head-current"}
	workflows := &hostedAppWorkflowClient{}
	patcher := &hostedAppPatchApplier{result: artifacts.ApplyResult{CommitSHA: strings.Repeat("a", 40)}}
	setupVerifier := hostedAppSetupVerifier{repo: cpgithub.SetupRepository{
		ID:             9001,
		Owner:          "octo-org",
		Name:           "herd",
		FullName:       "octo-org/herd",
		DefaultBranch:  "main",
		Private:        true,
		Admin:          true,
		InstallationID: 42,
		AccountLogin:   "octo-org",
		AccountID:      100,
		AccountType:    "Organization",
	}}
	registerRoute := cpgithub.NewRegisterHandler(cpgithub.RegisterHandlerOptions{
		Store:           st,
		SetupVerifier:   setupVerifier,
		AppVerifier:     hostedAppInstallationVerifier{},
		AppLogin:        "herd-os",
		ControlPlaneURL: "https://control.herd.test",
		Now:             func() time.Time { return now },
	})
	minter := &hostedAppRunnerMinter{response: runners.RegistrationTokenResponse{
		Token:     "runner-registration-token",
		ExpiresAt: now.Add(time.Hour),
	}}
	runnerRoute := runners.NewRegistrationTokenHandler(runners.HandlerOptions{
		Store:  st,
		Minter: minter,
		Now:    func() time.Time { return now },
	})
	dispatcher := dispatch.Dispatcher{Store: st, GitHub: workflows}
	reviewService := review.ReviewService{
		Status: review.StatusService{
			Store:  st,
			GitHub: gh,
			Now:    func() time.Time { return now },
		},
		GitHub:     gh,
		Mutations:  st,
		Locks:      st,
		Dispatcher: dispatcher,
		Now:        func() time.Time { return now },
	}
	commandDispatcher := hostedAppCommandDispatcher{
		review: reviewService,
		head:   func() string { return gh.headSHA },
	}
	commandHandler := commands.Handler{
		AppLogin:   "herd-os",
		Store:      st,
		GitHub:     gh,
		Dispatcher: commandDispatcher,
	}
	jobResultsRoute := jobs.NewHandler(jobs.HandlerOptions{
		Store: st,
		Validator: hostedAppOIDCValidator{claims: jobs.OIDCClaims{
			Issuer:     jobs.GitHubActionsIssuer,
			Audience:   []string{"herd-control-plane"},
			Repository: "octo-org/herd",
			Ref:        "refs/heads/main",
			Workflow:   "herd-review.yml",
			ExpiresAt:  now.Add(time.Hour),
		}},
		Audience:        "herd-control-plane",
		Now:             func() time.Time { return now },
		ArtifactStore:   hostedAppArtifacts{},
		PatchApplier:    patcher,
		AppLogin:        "herd-os[bot]",
		AppEmail:        "herd-os[bot]@users.noreply.github.com",
		ReviewProcessor: reviewService,
	})
	handler, err := NewServer(Config{
		Env:           "development",
		WebhookSecret: "webhook-secret",
		AppLogin:      "herd-os",
		OIDCAudience:  "herd-control-plane",
	}, Dependencies{
		Logger:                       log.New(io.Discard, "", 0),
		Store:                        st,
		RegisterRepositoryRoute:      registerRoute,
		RunnerRegistrationTokenRoute: runnerRoute,
		JobResultsRoute:              jobResultsRoute,
		IssueCommentCommandHandler:   commandHandler,
	})
	require.NoError(t, err)

	registerResp := postJSON[cpclient.RegisterRepositoryResponse](t, handler, "/api/v1/github/repositories/register", cpclient.RegisterRepositoryRequest{
		Owner:      "octo-org",
		Name:       "herd",
		SetupToken: "gho_setup",
		AppLogin:   "herd-os",
	}, http.StatusCreated)
	assert.Equal(t, int64(1), registerResp.RepositoryID)
	assert.Equal(t, int64(42), registerResp.InstallationID)
	require.NotEmpty(t, registerResp.RunnerBootstrapToken)

	runnerReq := runners.RegistrationTokenRequest{
		Repository:     "octo-org/herd",
		RunnerName:     "runner-1",
		RunnerLabels:   []string{"herd-worker"},
		BootstrapToken: registerResp.RunnerBootstrapToken,
		RequestNonce:   "nonce-1",
	}
	runnerResp := postJSON[runners.RegistrationTokenResponse](t, handler, "/api/v1/runners/registration-token", runnerReq, http.StatusOK)
	replayedRunnerResp := postJSON[runners.RegistrationTokenResponse](t, handler, "/api/v1/runners/registration-token", runnerReq, http.StatusOK)
	assert.Equal(t, runnerResp, replayedRunnerResp)
	assert.Equal(t, "runner-registration-token", runnerResp.Token)
	assert.Equal(t, 1, minter.calls)

	sendIssueCommentWebhook(t, handler, "delivery-review", "@herd-os review", 123)
	sendIssueCommentWebhook(t, handler, "delivery-review", "@herd-os review", 123)
	sendIssueCommentWebhook(t, handler, "delivery-review-command-replay", "@herd-os review", 123)
	_, err = commandHandler.HandleIssueComment(ctx, commands.IssueComment{
		Action:            "created",
		Owner:             "octo-org",
		Repo:              "herd",
		IssueNumber:       7,
		PullRequestURL:    "https://api.github.com/repos/octo-org/herd/pulls/7",
		CommentID:         123,
		CommentBody:       "@herd-os review",
		CommentAuthorType: "User",
		SenderLogin:       "mona",
		AuthorAssociation: "OWNER",
	})
	require.NoError(t, err)
	require.Len(t, gh.issueComments, 1)
	assert.Contains(t, gh.issueComments[0], "Acknowledged `@herd-os review`.")
	require.Len(t, workflows.dispatches, 1)
	assert.Equal(t, "herd-review.yml", workflows.dispatches[0].workflowFile)
	reviewJobID := workflows.dispatches[0].inputs["job_id"]
	require.NotEmpty(t, reviewJobID)
	require.Len(t, gh.statuses, 1)
	assert.Equal(t, "pending", gh.statuses[0].State)

	reviewPayload := map[string]any{
		"version":      1,
		"kind":         jobs.KindReviewCompleted,
		"repository":   "octo-org/herd",
		"job_id":       reviewJobID,
		"batch_number": 106,
		"pr_number":    7,
		"head_sha":     "head-current",
		"status":       jobs.StatusApproved,
		"summary":      "approved by fake reviewer",
	}
	firstReviewCallback := postJobResult(t, handler, reviewJobID, reviewPayload, http.StatusAccepted)
	secondReviewCallback := postJobResult(t, handler, reviewJobID, reviewPayload, http.StatusAccepted)
	assert.True(t, firstReviewCallback["created"].(bool))
	assert.False(t, secondReviewCallback["created"].(bool))
	require.Len(t, gh.reviews, 1)
	assert.Equal(t, platform.ReviewApprove, gh.reviews[0].event)
	assert.Equal(t, "head-current", gh.reviews[0].commitID)
	require.Len(t, gh.statuses, 2)
	assert.Equal(t, "success", gh.statuses[1].State)

	staleReviewPayload := cloneMap(reviewPayload)
	staleReviewPayload["head_sha"] = "old-head"
	_ = postJobResult(t, handler, reviewJobID, staleReviewPayload, http.StatusConflict)
	assert.Len(t, gh.reviews, 1)
	assert.Len(t, gh.statuses, 2)

	sendIssueCommentWebhook(t, handler, "delivery-legacy", "/herd review", 456)
	_, err = commandHandler.HandleIssueComment(ctx, commands.IssueComment{
		Action:            "created",
		Owner:             "octo-org",
		Repo:              "herd",
		IssueNumber:       7,
		PullRequestURL:    "https://api.github.com/repos/octo-org/herd/pulls/7",
		CommentID:         456,
		CommentBody:       "/herd review",
		CommentAuthorType: "User",
		SenderLogin:       "mona",
		AuthorAssociation: "OWNER",
	})
	require.NoError(t, err)
	require.Len(t, gh.issueComments, 2)
	assert.Contains(t, gh.issueComments[1], "@herd-os <command>")
	assert.Len(t, workflows.dispatches, 1, "legacy slash command must not dispatch production work")

	workerJobID := "job-worker-1"
	require.NoError(t, st.CreateJob(ctx, store.Job{
		JobID:          workerJobID,
		RepositoryID:   registerResp.RepositoryID,
		InstallationID: registerResp.InstallationID,
		HeadSHA:        "worker-head",
		BaseSHA:        "base",
		Status:         "dispatched",
		WorkerBranch:   "herd/worker/845",
		Metadata:       json.RawMessage(`{"requester_name":"Mona","requester_email":"mona@example.com"}`),
		CreatedAt:      now,
		UpdatedAt:      now,
	}))
	workerPayload := map[string]any{
		"version":           1,
		"kind":              jobs.KindWorkerCompleted,
		"repository":        "octo-org/herd",
		"job_id":            workerJobID,
		"batch_number":      106,
		"issue_number":      845,
		"target_branch":     "herd/worker/845",
		"base_sha":          "base",
		"expected_head_sha": "worker-head",
		"patch_artifact":    "worker-branch",
		"status":            jobs.StatusSuccess,
	}
	workerCallback := postJobResult(t, handler, workerJobID, workerPayload, http.StatusAccepted)
	assert.True(t, workerCallback["created"].(bool))
	require.Len(t, patcher.requests, 1)
	assert.Equal(t, "herd-os[bot]", patcher.requests[0].Identity.Name)
	assert.Equal(t, "herd-os[bot]@users.noreply.github.com", patcher.requests[0].Identity.Email)
	assert.Equal(t, "herd/worker/845", patcher.requests[0].TargetBranch)

	staleWorkerPayload := cloneMap(workerPayload)
	staleWorkerPayload["base_sha"] = "old-base"
	_ = postJobResult(t, handler, workerJobID, staleWorkerPayload, http.StatusConflict)
	assert.Len(t, patcher.requests, 1, "stale worker base must be rejected before patch application")
}

func postJSON[T any](t *testing.T, handler http.Handler, path string, body any, wantStatus int) T {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	var out T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func postJobResult(t *testing.T, handler http.Handler, jobID string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID+"/results", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer oidc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func sendIssueCommentWebhook(t *testing.T, handler http.Handler, deliveryID string, body string, commentID int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"action":       "created",
		"installation": map[string]any{"id": 42},
		"repository": map[string]any{
			"id":             9001,
			"name":           "herd",
			"owner":          map[string]any{"login": "octo-org"},
			"default_branch": "main",
		},
		"issue": map[string]any{
			"number":       7,
			"pull_request": map[string]any{"url": "https://api.github.com/repos/octo-org/herd/pulls/7"},
		},
		"comment": map[string]any{
			"id":                 commentID,
			"body":               body,
			"author_association": "OWNER",
			"user":               map[string]any{"login": "mona", "type": "User"},
		},
		"sender": map[string]any{"login": "mona", "type": "User"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-GitHub-Event", cpgithub.EventIssueComment)
	req.Header.Set("X-Hub-Signature-256", signHostedAppPayload("webhook-secret", payload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
}

func signHostedAppPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type hostedAppSetupVerifier struct {
	repo cpgithub.SetupRepository
}

func (v hostedAppSetupVerifier) VerifySetupRepository(_ context.Context, setupToken string, owner string, name string) (cpgithub.SetupRepository, error) {
	if setupToken != "gho_setup" || owner != v.repo.Owner || name != v.repo.Name {
		return cpgithub.SetupRepository{}, cpgithub.ErrRepoUnauthorized
	}
	return v.repo, nil
}

type hostedAppInstallationVerifier struct{}

func (hostedAppInstallationVerifier) VerifyAppAccess(context.Context, int64, string, string) error {
	return nil
}

type hostedAppRunnerMinter struct {
	calls    int
	response runners.RegistrationTokenResponse
}

func (m *hostedAppRunnerMinter) CreateRegistrationToken(context.Context, int64, string, string) (runners.RegistrationTokenResponse, error) {
	m.calls++
	return m.response, nil
}

type hostedAppCommandDispatcher struct {
	review review.ReviewService
	head   func() string
}

func (d hostedAppCommandDispatcher) DispatchCommand(ctx context.Context, cmd commands.DispatchCommand) error {
	if cmd.Command.Kind != commands.CommandReview {
		return nil
	}
	_, err := d.review.DispatchReview(ctx, review.Repository{
		ID:             cmd.RepositoryID,
		InstallationID: cmd.InstallationID,
		Owner:          cmd.Owner,
		Name:           cmd.Repo,
		DefaultBranch:  "main",
		ReviewEnabled:  true,
	}, review.DispatchReviewRequest{
		BatchNumber:     106,
		PRNumber:        cmd.PRNumber,
		BatchBranch:     "herd/batch/106",
		HeadSHA:         d.head(),
		WorkflowFile:    "herd-review.yml",
		Ref:             "main",
		RunnerLabel:     "herd-worker",
		TimeoutMinutes:  30,
		ControlPlaneURL: "https://control.herd.test",
		Reason:          "manual review command",
	})
	return err
}

type hostedAppGitHub struct {
	mu            sync.Mutex
	headSHA       string
	issueComments []string
	statuses      []platform.CommitStatus
	reviews       []hostedAppReview
}

type hostedAppReview struct {
	event    platform.ReviewEvent
	commitID string
	body     string
}

func (g *hostedAppGitHub) AddIssueComment(_ context.Context, _, _ string, _ int, body string) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.issueComments = append(g.issueComments, body)
	return int64(len(g.issueComments)), nil
}

func (g *hostedAppGitHub) CreateCommitStatus(_ context.Context, _ int64, _, _, _ string, status platform.CommitStatus) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.statuses = append(g.statuses, status)
	return nil
}

func (g *hostedAppGitHub) GetPullRequest(context.Context, int64, string, string, int) (*platform.PullRequest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return &platform.PullRequest{Number: 7, HeadSHA: g.headSHA, URL: "https://github.com/octo-org/herd/pull/7"}, nil
}

func (g *hostedAppGitHub) CreateReviewForCommit(_ context.Context, _ int64, _, _ string, _ int, body string, event platform.ReviewEvent, commitID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reviews = append(g.reviews, hostedAppReview{event: event, commitID: commitID, body: body})
	return nil
}

func (g *hostedAppGitHub) AddPullRequestComment(context.Context, int64, string, string, int, string) error {
	return nil
}

type hostedAppWorkflowClient struct {
	dispatches []hostedAppWorkflowDispatch
}

type hostedAppWorkflowDispatch struct {
	installationID int64
	owner          string
	repo           string
	workflowFile   string
	ref            string
	inputs         map[string]string
}

func (c *hostedAppWorkflowClient) DispatchWorkflow(_ context.Context, installationID int64, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	c.dispatches = append(c.dispatches, hostedAppWorkflowDispatch{
		installationID: installationID,
		owner:          owner,
		repo:           repo,
		workflowFile:   workflowFile,
		ref:            ref,
		inputs:         inputs,
	})
	return nil
}

type hostedAppOIDCValidator struct {
	claims jobs.OIDCClaims
}

func (v hostedAppOIDCValidator) Validate(context.Context, string) (jobs.OIDCClaims, error) {
	return v.claims, nil
}

type hostedAppArtifacts map[string][]byte

func (a hostedAppArtifacts) OpenArtifact(_ context.Context, name string) (io.ReadCloser, error) {
	if len(a) == 0 {
		patch := []byte("diff --git a/file.txt b/file.txt\n")
		metadata := artifacts.BuildMetadata("octo-org/herd", "job-worker-1", "base", "worker-head", "patch.diff", patch)
		metadataBytes, _ := json.Marshal(metadata)
		a = hostedAppArtifacts{
			"worker-branch": metadataBytes,
			"patch.diff":    patch,
		}
	}
	data, ok := a[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type hostedAppPatchApplier struct {
	requests []artifacts.ApplyRequest
	result   artifacts.ApplyResult
}

func (a *hostedAppPatchApplier) Apply(_ context.Context, req artifacts.ApplyRequest) (artifacts.ApplyResult, error) {
	a.requests = append(a.requests, req)
	return a.result, nil
}
