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

	"github.com/herd-os/herd/internal/cli"
	"github.com/herd-os/herd/internal/controlplane/artifacts"
	"github.com/herd-os/herd/internal/controlplane/commands"
	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/controlplane/workflowevents"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildServiceDependenciesProductionWiresCommandDispatcher(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()

	deps, err := buildServiceDependenciesWithOptions(cfg, st, log.New(io.Discard, "", 0), productionDependencyOptions{
		WorkflowEventProcessor: fixedWorkflowProcessor{},
	})

	require.NoError(t, err)
	require.NotNil(t, deps.IssueCommentCommandHandler)
}

func TestBuildServiceDependenciesProductionRequiresWorkflowProcessor(t *testing.T) {
	cfg := validProductionServiceConfig(t)
	st := store.NewMemoryStore()

	deps, err := buildServiceDependencies(cfg, st, log.New(io.Discard, "", 0))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "production workflow event processor is not configured")
	assert.Empty(t, deps)
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
		want string
	}{
		{name: "review", kind: cpdispatch.JobKindReview, want: "herd-review.yml"},
		{name: "review fix", kind: cpdispatch.JobKindReviewFix, want: "herd-worker.yml"},
		{name: "ci fix", kind: cpdispatch.JobKindCIFix, want: "herd-worker.yml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowFile := commandWorkflowFile(tt.kind)

			assert.Equal(t, tt.want, workflowFile)
			assert.Contains(t, managed, workflowFile)
		})
	}
}

func TestCommandTargetFromPullRequest(t *testing.T) {
	tests := []struct {
		name      string
		kind      commands.CommandKind
		issue     int
		head      string
		wantBatch int
		wantIssue int
		wantErr   string
	}{
		{
			name:      "review without batch milestone uses PR number context",
			kind:      commands.CommandReview,
			head:      "feature-branch",
			wantBatch: 42,
			wantIssue: 42,
		},
		{
			name:      "review with batch branch uses batch number",
			kind:      commands.CommandReview,
			head:      "herd/batch/849-hosted-app",
			wantBatch: 849,
			wantIssue: 42,
		},
		{
			name:      "fix with tracking issue uses durable issue number",
			kind:      commands.CommandFix,
			issue:     101,
			head:      "feature-branch",
			wantBatch: 42,
			wantIssue: 101,
		},
		{
			name:    "fix without tracking issue is rejected",
			kind:    commands.CommandFix,
			head:    "feature-branch",
			wantErr: "durable fix issue number",
		},
		{
			name:    "fix-ci without tracking issue is rejected",
			kind:    commands.CommandFixCI,
			head:    "herd/batch/42-command-surface",
			wantErr: "durable fix issue number",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := commandTargetFromPullRequest(commands.DispatchCommand{
				IssueNumber: tt.issue,
				PRNumber:    42,
				Command:     commands.ParsedCommand{Kind: tt.kind},
			}, &platform.PullRequest{
				Head:    tt.head,
				HeadSHA: "head-sha",
				Base:    "main",
				BaseSHA: "base-sha",
			})

			if tt.wantErr != "" {
				require.NoError(t, err)
				err = validateCommandTarget(commands.DispatchCommand{
					IssueNumber: tt.issue,
					PRNumber:    42,
					Command:     commands.ParsedCommand{Kind: tt.kind},
				}, target)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantBatch, target.BatchNumber)
			assert.Equal(t, tt.wantIssue, target.IssueNumber)
			assert.Equal(t, tt.head, target.Ref)
			assert.Equal(t, tt.head, target.BatchBranch)
			assert.Equal(t, "head-sha", target.BaseSHA)
			assert.Equal(t, "head-sha", target.HeadSHA)
		})
	}
}

func TestProductionResolveConflictsCommand(t *testing.T) {
	originalSleep := resolveConflictsSleep
	resolveConflictsSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { resolveConflictsSleep = originalSleep })

	tests := []struct {
		name             string
		prs              []*platform.PullRequest
		existingIssues   []*platform.Issue
		wantCreatedIssue bool
		wantDispatch     bool
		wantComment      string
		wantGets         int
	}{
		{
			name:             "conflicting PR creates resolver issue and dispatches",
			prs:              []*platform.PullRequest{batchPR("DIRTY", false, false)},
			wantCreatedIssue: true,
			wantDispatch:     true,
			wantComment:      "Created conflict-resolution issue #100",
			wantGets:         1,
		},
		{
			name:        "clean PR no-ops",
			prs:         []*platform.PullRequest{batchPR("CLEAN", true, true)},
			wantComment: "not currently conflicting",
			wantGets:    1,
		},
		{
			name:        "blocked PR no-ops",
			prs:         []*platform.PullRequest{batchPR("BLOCKED", false, false)},
			wantComment: "not currently conflicting",
			wantGets:    1,
		},
		{
			name: "unknown mergeability retries then warns",
			prs: []*platform.PullRequest{
				batchPR("UNKNOWN", false, false),
				batchPR("UNKNOWN", false, false),
				batchPR("UNKNOWN", false, false),
				batchPR("UNKNOWN", false, false),
			},
			wantComment: "could not determine",
			wantGets:    4,
		},
		{
			name: "duplicate active resolver issue no-ops",
			prs:  []*platform.PullRequest{batchPR("CONFLICTING", false, false)},
			existingIssues: []*platform.Issue{{
				Number: 55,
				Body: integrator.BuildConflictResolutionIssueBody(integrator.ConflictResolutionIssueParams{
					Kind:         integrator.ConflictResolutionKindPRBase,
					Milestone:    &platform.Milestone{Number: 7, Title: "Command Surface"},
					BatchPR:      7,
					PRHeadBranch: "herd/batch/7-command-surface",
					PRHeadSHA:    "head-sha",
					BaseBranch:   "main",
					BaseSHA:      "base-sha",
				}),
				Labels: []string{issues.TypeFix, issues.StatusInProgress},
			}},
			wantComment: "already active",
			wantGets:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakeCommandPlatform(tt.prs)
			p.issues.listed = tt.existingIssues
			workflow := &recordingWorkflowClient{}
			d := productionCommandDispatcher{
				Dispatcher:      cpdispatch.Dispatcher{Store: store.NewMemoryStore(), GitHub: workflow},
				ControlPlaneURL: "https://control.example.test",
				DefaultRunner:   "herd-worker",
				TimeoutMinutes:  30,
				PlatformFactory: func(context.Context, commands.DispatchCommand) (platform.Platform, error) {
					return p, nil
				},
			}

			err := d.DispatchCommand(context.Background(), resolveConflictsCommand())

			require.NoError(t, err)
			assert.Equal(t, tt.wantGets, p.prs.gets)
			assert.Contains(t, strings.Join(p.prs.comments, "\n"), tt.wantComment)
			assert.Equal(t, tt.wantCreatedIssue, len(p.issues.created) == 1)
			assert.Equal(t, tt.wantDispatch, len(workflow.dispatches) == 1)
			if tt.wantDispatch {
				require.Len(t, p.issues.created, 1)
				assert.Contains(t, p.issues.created[0].Body, "PR #7 cannot be merged cleanly")
				assert.NotContains(t, p.issues.created[0].Body, "full PR conversation")
				assert.Equal(t, "herd/batch/7-command-surface", workflow.dispatches[0].ref)
				assert.Equal(t, "100", workflow.dispatches[0].inputs["issue_number"])
				assert.Equal(t, "7", workflow.dispatches[0].inputs["pr_number"])
			}
		})
	}
}

func TestProductionDispatchCommandDispatchesReadyIssue(t *testing.T) {
	p := newFakeCommandPlatform([]*platform.PullRequest{})
	p.issues.byNumber[42] = &platform.Issue{
		Number:    42,
		Title:     "Do work",
		Labels:    []string{issues.StatusReady},
		Milestone: &platform.Milestone{Number: 7, Title: "Command Surface"},
	}
	p.repo.branchSHAs["herd/batch/7-command-surface"] = "batch-sha"
	workflow := &recordingWorkflowClient{}
	d := productionCommandDispatcher{
		Dispatcher:      cpdispatch.Dispatcher{Store: store.NewMemoryStore(), GitHub: workflow},
		ControlPlaneURL: "https://control.example.test",
		DefaultRunner:   "herd-worker",
		TimeoutMinutes:  30,
		PlatformFactory: func(context.Context, commands.DispatchCommand) (platform.Platform, error) {
			return p, nil
		},
	}

	cmd := resolveConflictsCommand()
	cmd.IssueNumber = 7
	cmd.PRNumber = 7
	cmd.Command = commands.ParsedCommand{Kind: commands.CommandDispatch, Args: []string{"42"}}
	err := d.DispatchCommand(context.Background(), cmd)

	require.NoError(t, err)
	require.Len(t, workflow.dispatches, 1)
	assert.Equal(t, "herd-worker.yml", workflow.dispatches[0].workflowFile)
	assert.Equal(t, "main", workflow.dispatches[0].ref)
	assert.Equal(t, "42", workflow.dispatches[0].inputs["issue_number"])
	assert.Equal(t, "herd/batch/7-command-surface", workflow.dispatches[0].inputs["batch_branch"])
	assert.Contains(t, p.issues.byNumber[42].Labels, issues.StatusInProgress)
	assert.NotContains(t, p.issues.byNumber[42].Labels, issues.StatusReady)
	assert.Contains(t, strings.Join(p.issues.comments[7], "\n"), "Dispatched worker for issue #42")
}

func TestProductionFixCommandsCreateTrackingIssueAndDispatchCreatedIssue(t *testing.T) {
	tests := []struct {
		name          string
		kind          commands.CommandKind
		args          []string
		wantBody      []string
		wantCIFix     bool
		wantFixCycle  bool
		wantIssueType string
	}{
		{
			name:          "fix",
			kind:          commands.CommandFix,
			args:          []string{"update", "auth", "error", "handling"},
			wantBody:      []string{"update auth error handling", "via `@herd-os fix`", "batch_pr: 849", "fix_cycle: 1"},
			wantFixCycle:  true,
			wantIssueType: issues.TypeFix,
		},
		{
			name:          "fix-ci",
			kind:          commands.CommandFixCI,
			args:          []string{"failing", "tests", "mention", "missing", "env", "var"},
			wantBody:      []string{"failing tests mention missing env var", "via `@herd-os fix-ci`", "batch_pr: 849", "ci_fix_cycle: 1"},
			wantCIFix:     true,
			wantIssueType: issues.TypeFix,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakeCommandPlatform([]*platform.PullRequest{{
				Number:  849,
				Head:    "herd/batch/106-hosted-app",
				Base:    "main",
				HeadSHA: "head-sha",
				BaseSHA: "base-sha",
			}})
			p.ms.milestones[106] = &platform.Milestone{Number: 106, Title: "Hosted App"}
			workflow := &recordingWorkflowClient{}
			d := productionCommandDispatcher{
				Dispatcher:      cpdispatch.Dispatcher{Store: store.NewMemoryStore(), GitHub: workflow},
				ControlPlaneURL: "https://control.example.test",
				DefaultRunner:   "herd-worker",
				TimeoutMinutes:  30,
				PlatformFactory: func(context.Context, commands.DispatchCommand) (platform.Platform, error) {
					return p, nil
				},
			}

			err := d.DispatchCommand(context.Background(), commands.DispatchCommand{
				RepositoryID:   42,
				InstallationID: 77,
				Owner:          "octo",
				Repo:           "herd",
				IssueNumber:    0,
				PRNumber:       849,
				CommentID:      123,
				Actor:          "maintainer",
				Command:        commands.ParsedCommand{Kind: tt.kind, Args: tt.args},
			})

			require.NoError(t, err)
			require.Len(t, p.issues.created, 1)
			created := p.issues.created[0]
			require.NotEqual(t, 849, created.Number)
			assert.Equal(t, 100, created.Number)
			assert.Equal(t, &platform.Milestone{Number: 106}, created.Milestone)
			assert.Contains(t, created.Labels, tt.wantIssueType)
			assert.Contains(t, created.Labels, issues.StatusInProgress)
			for _, want := range tt.wantBody {
				assert.Contains(t, created.Body, want)
			}
			parsed, parseErr := issues.ParseBody(created.Body)
			require.NoError(t, parseErr)
			assert.Equal(t, "fix", parsed.FrontMatter.Type)
			assert.Equal(t, tt.wantFixCycle, parsed.FrontMatter.FixCycle > 0)
			assert.Equal(t, tt.wantCIFix, parsed.FrontMatter.CIFixCycle > 0)

			require.Len(t, workflow.dispatches, 1)
			assert.Equal(t, "herd-worker.yml", workflow.dispatches[0].workflowFile)
			assert.Equal(t, "herd/batch/106-hosted-app", workflow.dispatches[0].ref)
			assert.Equal(t, "100", workflow.dispatches[0].inputs["issue_number"])
			assert.Equal(t, "849", workflow.dispatches[0].inputs["pr_number"])
		})
	}
}

func TestProductionFixCommandDuplicateDoesNotCreateSecondIssueOrDispatch(t *testing.T) {
	p := newFakeCommandPlatform([]*platform.PullRequest{{
		Number:  849,
		Head:    "herd/batch/106-hosted-app",
		Base:    "main",
		HeadSHA: "head-sha",
		BaseSHA: "base-sha",
	}})
	p.ms.milestones[106] = &platform.Milestone{Number: 106, Title: "Hosted App"}
	workflow := &recordingWorkflowClient{}
	d := productionCommandDispatcher{
		Dispatcher:      cpdispatch.Dispatcher{Store: store.NewMemoryStore(), GitHub: workflow},
		ControlPlaneURL: "https://control.example.test",
		DefaultRunner:   "herd-worker",
		TimeoutMinutes:  30,
		PlatformFactory: func(context.Context, commands.DispatchCommand) (platform.Platform, error) {
			return p, nil
		},
	}
	cmd := commands.DispatchCommand{
		RepositoryID:   42,
		InstallationID: 77,
		Owner:          "octo",
		Repo:           "herd",
		PRNumber:       849,
		CommentID:      123,
		Actor:          "maintainer",
		Command:        commands.ParsedCommand{Kind: commands.CommandFix, Args: []string{"update", "auth", "error", "handling"}},
	}

	err := d.DispatchCommand(context.Background(), cmd)
	require.NoError(t, err)
	err = d.DispatchCommand(context.Background(), cmd)
	require.NoError(t, err)

	assert.Len(t, p.issues.created, 1)
	assert.Len(t, workflow.dispatches, 1)
	assert.Equal(t, "100", workflow.dispatches[0].inputs["issue_number"])
}

func TestProductionFixCommandRecoversCreatedIssueAfterCompletionFailure(t *testing.T) {
	p := newFakeCommandPlatform([]*platform.PullRequest{{
		Number:  849,
		Head:    "herd/batch/106-hosted-app",
		Base:    "main",
		HeadSHA: "head-sha",
		BaseSHA: "base-sha",
	}})
	p.ms.milestones[106] = &platform.Milestone{Number: 106, Title: "Hosted App"}
	workflow := &recordingWorkflowClient{}
	st := store.NewMemoryStore()
	d := productionCommandDispatcher{
		Dispatcher:      cpdispatch.Dispatcher{Store: st, GitHub: workflow},
		ControlPlaneURL: "https://control.example.test",
		DefaultRunner:   "herd-worker",
		TimeoutMinutes:  30,
		PlatformFactory: func(context.Context, commands.DispatchCommand) (platform.Platform, error) {
			return p, nil
		},
	}
	cmd := commands.DispatchCommand{
		RepositoryID:   42,
		InstallationID: 77,
		Owner:          "octo",
		Repo:           "herd",
		PRNumber:       849,
		CommentID:      123,
		Actor:          "maintainer",
		Command:        commands.ParsedCommand{Kind: commands.CommandFix, Args: []string{"update", "auth", "error", "handling"}},
	}

	key := commandFixIssueKey(cmd)
	_, err := st.AcquireIdempotencyKey(context.Background(), store.IdempotencyKey{Key: key, Scope: "command_fix_issue_create", Status: mutations.PhaseIntentRecorded, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	p.issues.created = append(p.issues.created, &platform.Issue{
		Number:    100,
		Title:     "Fix: update auth error handling",
		Body:      injectCommandFixIssueMarker(issues.RenderBody(issues.IssueBody{FrontMatter: issues.FrontMatter{Version: 1, Batch: 106, Type: "fix", FixCycle: 1, BatchPR: 849}, Task: "update auth error handling"}), cmd),
		Labels:    []string{issues.TypeFix, issues.StatusInProgress},
		Milestone: &platform.Milestone{Number: 106},
	})
	p.issues.byNumber[100] = p.issues.created[0]

	err = d.DispatchCommand(context.Background(), cmd)

	require.NoError(t, err)
	assert.Len(t, p.issues.created, 1)
	require.Len(t, workflow.dispatches, 1)
	assert.Equal(t, "100", workflow.dispatches[0].inputs["issue_number"])
	assert.NotEqual(t, "849", workflow.dispatches[0].inputs["issue_number"])
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
		Issuer:      jobs.GitHubActionsIssuer,
		Audience:    []string{"herd-control-plane"},
		Repository:  "octo/herd",
		Ref:         "refs/heads/main",
		Workflow:    ".github/workflows/herd-integrator.yml",
		WorkflowRef: "octo/herd/.github/workflows/herd-integrator.yml@refs/heads/main",
		ExpiresAt:   time.Now().Add(time.Hour),
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

func batchPR(state string, mergeableKnown bool, mergeable bool) *platform.PullRequest {
	return &platform.PullRequest{
		Number:           7,
		Head:             "herd/batch/7-command-surface",
		Base:             "main",
		HeadSHA:          "head-sha",
		BaseSHA:          "base-sha",
		MergeStateStatus: state,
		MergeableKnown:   mergeableKnown,
		Mergeable:        mergeable,
	}
}

func resolveConflictsCommand() commands.DispatchCommand {
	return commands.DispatchCommand{
		RepositoryID:   42,
		InstallationID: 77,
		Owner:          "octo",
		Repo:           "herd",
		IssueNumber:    7,
		PRNumber:       7,
		CommentID:      123,
		Actor:          "maintainer",
		Command: commands.ParsedCommand{
			Kind: commands.CommandResolveConflicts,
			Args: []string{"keep", "generated", "files"},
		},
	}
}

type recordingWorkflowClient struct {
	dispatches []recordedWorkflowDispatch
}

type recordedWorkflowDispatch struct {
	installationID int64
	owner          string
	repo           string
	workflowFile   string
	ref            string
	inputs         map[string]string
}

func (c *recordingWorkflowClient) DispatchWorkflow(_ context.Context, installationID int64, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	copied := map[string]string{}
	for k, v := range inputs {
		copied[k] = v
	}
	c.dispatches = append(c.dispatches, recordedWorkflowDispatch{
		installationID: installationID,
		owner:          owner,
		repo:           repo,
		workflowFile:   workflowFile,
		ref:            ref,
		inputs:         copied,
	})
	return nil
}

type fakeCommandPlatform struct {
	issues *fakeCommandIssueService
	prs    *fakeCommandPRService
	repo   *fakeCommandRepoService
	ms     *fakeCommandMilestoneService
}

func newFakeCommandPlatform(prs []*platform.PullRequest) *fakeCommandPlatform {
	return &fakeCommandPlatform{
		issues: &fakeCommandIssueService{byNumber: map[int]*platform.Issue{}},
		prs:    &fakeCommandPRService{prs: prs},
		repo: &fakeCommandRepoService{
			defaultBranch: "main",
			branchSHAs:    map[string]string{"main": "main-sha"},
		},
		ms: &fakeCommandMilestoneService{milestones: map[int]*platform.Milestone{
			7: {Number: 7, Title: "Command Surface"},
		}},
	}
}

func (p *fakeCommandPlatform) Issues() platform.IssueService             { return p.issues }
func (p *fakeCommandPlatform) PullRequests() platform.PullRequestService { return p.prs }
func (p *fakeCommandPlatform) Workflows() platform.WorkflowService       { return nil }
func (p *fakeCommandPlatform) Labels() platform.LabelService             { return nil }
func (p *fakeCommandPlatform) Milestones() platform.MilestoneService     { return p.ms }
func (p *fakeCommandPlatform) Runners() platform.RunnerService           { return nil }
func (p *fakeCommandPlatform) Repository() platform.RepositoryService    { return p.repo }
func (p *fakeCommandPlatform) Checks() platform.CheckService             { return nil }

type fakeCommandPRService struct {
	prs      []*platform.PullRequest
	gets     int
	comments []string
}

func (s *fakeCommandPRService) Get(context.Context, int) (*platform.PullRequest, error) {
	if len(s.prs) == 0 {
		return nil, store.ErrNotFound
	}
	idx := s.gets
	if idx >= len(s.prs) {
		idx = len(s.prs) - 1
	}
	s.gets++
	return s.prs[idx], nil
}
func (s *fakeCommandPRService) AddComment(_ context.Context, _ int, body string) error {
	s.comments = append(s.comments, body)
	return nil
}
func (s *fakeCommandPRService) Create(context.Context, string, string, string, string) (*platform.PullRequest, error) {
	return nil, nil
}
func (s *fakeCommandPRService) List(context.Context, platform.PRFilters) ([]*platform.PullRequest, error) {
	return nil, nil
}
func (s *fakeCommandPRService) Update(context.Context, int, *string, *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (s *fakeCommandPRService) Merge(context.Context, int, platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, nil
}
func (s *fakeCommandPRService) UpdateBranch(context.Context, int) error { return nil }
func (s *fakeCommandPRService) CreateReview(context.Context, int, string, platform.ReviewEvent) error {
	return nil
}
func (s *fakeCommandPRService) ListReviewComments(context.Context, int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (s *fakeCommandPRService) ListFiles(context.Context, int) ([]*platform.PullRequestFile, error) {
	return nil, nil
}
func (s *fakeCommandPRService) GetDiff(context.Context, int) (string, error) { return "", nil }
func (s *fakeCommandPRService) Close(context.Context, int) error             { return nil }

type fakeCommandIssueService struct {
	byNumber map[int]*platform.Issue
	listed   []*platform.Issue
	created  []*platform.Issue
	comments map[int][]string
}

func (s *fakeCommandIssueService) Create(_ context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	issue := &platform.Issue{Number: 100 + len(s.created), Title: title, Body: body, Labels: append([]string(nil), labels...)}
	if milestone != nil {
		issue.Milestone = &platform.Milestone{Number: *milestone}
	}
	s.created = append(s.created, issue)
	if s.byNumber == nil {
		s.byNumber = map[int]*platform.Issue{}
	}
	s.byNumber[issue.Number] = issue
	return issue, nil
}
func (s *fakeCommandIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	issue, ok := s.byNumber[number]
	if !ok {
		return nil, store.ErrNotFound
	}
	return issue, nil
}
func (s *fakeCommandIssueService) List(context.Context, platform.IssueFilters) ([]*platform.Issue, error) {
	out := append([]*platform.Issue(nil), s.listed...)
	out = append(out, s.created...)
	return out, nil
}
func (s *fakeCommandIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	issue, ok := s.byNumber[number]
	if !ok {
		return store.ErrNotFound
	}
	for _, label := range labels {
		if !issues.HasLabel(issue.Labels, label) {
			issue.Labels = append(issue.Labels, label)
		}
	}
	return nil
}
func (s *fakeCommandIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	issue, ok := s.byNumber[number]
	if !ok {
		return store.ErrNotFound
	}
	remove := map[string]bool{}
	for _, label := range labels {
		remove[label] = true
	}
	kept := issue.Labels[:0]
	for _, label := range issue.Labels {
		if !remove[label] {
			kept = append(kept, label)
		}
	}
	issue.Labels = kept
	return nil
}
func (s *fakeCommandIssueService) AddComment(_ context.Context, number int, body string) error {
	if s.comments == nil {
		s.comments = map[int][]string{}
	}
	s.comments[number] = append(s.comments[number], body)
	return nil
}
func (s *fakeCommandIssueService) AddCommentReturningID(ctx context.Context, number int, body string) (int64, error) {
	return 1, s.AddComment(ctx, number, body)
}
func (s *fakeCommandIssueService) Update(context.Context, int, platform.IssueUpdate) (*platform.Issue, error) {
	return nil, nil
}
func (s *fakeCommandIssueService) UpdateComment(context.Context, int64, string) error { return nil }
func (s *fakeCommandIssueService) DeleteComment(context.Context, int64) error         { return nil }
func (s *fakeCommandIssueService) ListComments(context.Context, int) ([]*platform.Comment, error) {
	return nil, nil
}
func (s *fakeCommandIssueService) CreateCommentReaction(context.Context, int64, string) error {
	return nil
}

type fakeCommandMilestoneService struct {
	milestones map[int]*platform.Milestone
}

func (s *fakeCommandMilestoneService) Get(_ context.Context, number int) (*platform.Milestone, error) {
	ms, ok := s.milestones[number]
	if !ok {
		return nil, store.ErrNotFound
	}
	return ms, nil
}
func (s *fakeCommandMilestoneService) Create(context.Context, string, string, *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (s *fakeCommandMilestoneService) List(context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}
func (s *fakeCommandMilestoneService) Update(context.Context, int, platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

type fakeCommandRepoService struct {
	defaultBranch string
	branchSHAs    map[string]string
}

func (s *fakeCommandRepoService) GetDefaultBranch(context.Context) (string, error) {
	return s.defaultBranch, nil
}
func (s *fakeCommandRepoService) GetBranchSHA(_ context.Context, name string) (string, error) {
	sha, ok := s.branchSHAs[name]
	if !ok {
		return "", store.ErrNotFound
	}
	return sha, nil
}
func (s *fakeCommandRepoService) GetInfo(context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: s.defaultBranch}, nil
}
func (s *fakeCommandRepoService) CreateBranch(context.Context, string, string) error { return nil }
func (s *fakeCommandRepoService) DeleteBranch(context.Context, string) error         { return nil }
func (s *fakeCommandRepoService) CreateBranchWithCommit(context.Context, string, string, string) (string, error) {
	return "", nil
}
