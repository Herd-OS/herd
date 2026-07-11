package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/google/uuid"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/store"
)

type JobKind string

const (
	JobKindWorker             JobKind = "worker"
	JobKindIntegrator         JobKind = "integrator"
	JobKindMonitor            JobKind = "monitor"
	JobKindReview             JobKind = "review"
	JobKindReviewFix          JobKind = "review-fix"
	JobKindCIFix              JobKind = "ci-fix"
	JobKindConflictResolution JobKind = "conflict-resolution"
)

type DispatchRequest struct {
	RepoID          int64
	Owner           string
	Repo            string
	InstallationID  int64
	Kind            JobKind
	WorkflowFile    string
	Ref             string
	BatchNumber     int
	IssueNumber     int
	PRNumber        int
	BatchBranch     string
	HeadSHA         string
	ExpectedHeadSHA string
	RunnerLabel     string
	TimeoutMinutes  int
	ControlPlaneURL string
	Reason          string
}

type DispatchResult struct {
	JobID         string
	WorkflowRunID int64
	URL           string
	Created       bool
}

type Store interface {
	CreateJob(ctx context.Context, j store.Job) error
	GetJob(ctx context.Context, jobID string) (store.Job, error)
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
}

type MutationRecorder interface {
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
}

type GitHubMutationResult struct {
	Status      string
	Response    json.RawMessage
	Error       string
	CompletedAt time.Time
}

type WorkflowClient interface {
	DispatchWorkflow(ctx context.Context, installationID int64, owner, repo, workflowFile, ref string, inputs map[string]string) error
}

type Dispatcher struct {
	Store  Store
	GitHub WorkflowClient
}

func (d Dispatcher) Dispatch(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	if err := validateRequest(req); err != nil {
		return DispatchResult{}, err
	}
	if d.Store == nil {
		return DispatchResult{}, fmt.Errorf("dispatch store is required")
	}
	if d.GitHub == nil {
		return DispatchResult{}, fmt.Errorf("dispatch GitHub client is required")
	}

	idempotencyKey := IdempotencyKey(req)
	jobID := "job_" + uuid.NewString()
	now := time.Now().UTC()
	keyMetadata, err := json.Marshal(map[string]any{
		"job_id":       jobID,
		"repo_id":      req.RepoID,
		"job_kind":     req.Kind,
		"batch_number": req.BatchNumber,
		"issue_number": req.IssueNumber,
		"pr_number":    req.PRNumber,
		"head_sha":     req.HeadSHA,
	})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("marshal idempotency metadata: %w", err)
	}
	created, err := d.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       idempotencyKey,
		Scope:     "workflow_dispatch",
		Status:    "started",
		Metadata:  keyMetadata,
		CreatedAt: now,
	})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("acquire dispatch idempotency key: %w", err)
	}
	if !created {
		return d.duplicateResult(ctx, idempotencyKey)
	}

	inputs, err := WorkflowInputs(req, jobID)
	if err != nil {
		return DispatchResult{}, err
	}
	jobMetadata, err := json.Marshal(map[string]any{
		"kind":              req.Kind,
		"workflow_file":     req.WorkflowFile,
		"ref":               req.Ref,
		"batch_number":      req.BatchNumber,
		"issue_number":      req.IssueNumber,
		"pr_number":         req.PRNumber,
		"batch_branch":      req.BatchBranch,
		"expected_head_sha": req.ExpectedHeadSHA,
		"runner_label":      req.RunnerLabel,
		"timeout_minutes":   req.TimeoutMinutes,
		"reason":            req.Reason,
		"idempotency_key":   idempotencyKey,
	})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("marshal job metadata: %w", err)
	}
	if err := d.Store.CreateJob(ctx, store.Job{
		JobID:          jobID,
		RepositoryID:   req.RepoID,
		InstallationID: req.InstallationID,
		PRNumber:       req.PRNumber,
		HeadSHA:        req.HeadSHA,
		Status:         "dispatching",
		WorkerBranch:   req.BatchBranch,
		Metadata:       jobMetadata,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		return DispatchResult{}, fmt.Errorf("create dispatch job: %w", err)
	}

	if recorder, ok := d.Store.(MutationRecorder); ok {
		requestJSON, marshalErr := json.Marshal(map[string]any{
			"owner":         req.Owner,
			"repo":          req.Repo,
			"workflow_file": req.WorkflowFile,
			"ref":           req.Ref,
			"inputs":        inputs,
		})
		if marshalErr != nil {
			return DispatchResult{}, fmt.Errorf("marshal mutation request: %w", marshalErr)
		}
		if err := recorder.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
			IdempotencyKey: idempotencyKey,
			RepositoryID:   req.RepoID,
			MutationType:   "workflow_dispatch",
			Status:         "started",
			Request:        requestJSON,
			CreatedAt:      now,
		}); err != nil {
			return DispatchResult{}, fmt.Errorf("record workflow dispatch mutation attempt: %w", err)
		}
	}

	if err := d.GitHub.DispatchWorkflow(ctx, req.InstallationID, req.Owner, req.Repo, req.WorkflowFile, req.Ref, inputs); err != nil {
		d.recordMutationResult(ctx, idempotencyKey, GitHubMutationResult{
			Status:      "failed",
			Error:       err.Error(),
			CompletedAt: time.Now().UTC(),
		})
		return DispatchResult{}, fmt.Errorf("dispatch workflow: %w", err)
	}

	result := DispatchResult{
		JobID:   jobID,
		URL:     workflowURL(req.Owner, req.Repo),
		Created: true,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("marshal dispatch result: %w", err)
	}
	if err := d.Store.CompleteIdempotencyKey(ctx, idempotencyKey, string(resultJSON)); err != nil {
		return DispatchResult{}, fmt.Errorf("complete dispatch idempotency key: %w", err)
	}
	d.recordMutationResult(ctx, idempotencyKey, GitHubMutationResult{
		Status:      "completed",
		Response:    json.RawMessage(resultJSON),
		CompletedAt: time.Now().UTC(),
	})
	return result, nil
}

func (d Dispatcher) duplicateResult(ctx context.Context, idempotencyKey string) (DispatchResult, error) {
	record, err := d.Store.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("get dispatch idempotency key: %w", err)
	}
	if record.ResultRef != "" {
		var result DispatchResult
		if err := json.Unmarshal([]byte(record.ResultRef), &result); err == nil && result.JobID != "" {
			result.Created = false
			return result, nil
		}
	}
	var metadata struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
		return DispatchResult{}, fmt.Errorf("decode dispatch idempotency metadata: %w", err)
	}
	if metadata.JobID == "" {
		return DispatchResult{}, fmt.Errorf("dispatch idempotency record is missing job_id")
	}
	job, err := d.Store.GetJob(ctx, metadata.JobID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("get existing dispatch job: %w", err)
	}
	return DispatchResult{JobID: job.JobID, Created: false}, nil
}

func (d Dispatcher) recordMutationResult(ctx context.Context, idempotencyKey string, result GitHubMutationResult) {
	recorder, ok := d.Store.(MutationRecorder)
	if !ok {
		return
	}
	_ = recorder.CompleteGitHubMutationAttempt(ctx, idempotencyKey, result.Status, result.Response, result.Error, result.CompletedAt)
}

func validateRequest(req DispatchRequest) error {
	if req.RepoID == 0 {
		return fmt.Errorf("repo ID is required")
	}
	if req.InstallationID == 0 {
		return fmt.Errorf("installation ID is required")
	}
	if req.Owner == "" {
		return fmt.Errorf("repository owner is required")
	}
	if req.Repo == "" {
		return fmt.Errorf("repository name is required")
	}
	if req.Kind == "" {
		return fmt.Errorf("job kind is required")
	}
	if !validJobKind(req.Kind) {
		return fmt.Errorf("unsupported job kind %q", req.Kind)
	}
	if req.WorkflowFile == "" {
		return fmt.Errorf("workflow file is required")
	}
	if req.Ref == "" {
		return fmt.Errorf("workflow ref is required")
	}
	if req.BatchNumber <= 0 {
		return fmt.Errorf("batch number is required")
	}
	if headRequired(req.Kind) && req.HeadSHA == "" {
		return fmt.Errorf("head SHA is required for %s dispatch", req.Kind)
	}
	if req.ExpectedHeadSHA != "" && req.HeadSHA != "" && req.ExpectedHeadSHA != req.HeadSHA {
		return fmt.Errorf("stale dispatch head SHA: expected %s, got %s", req.ExpectedHeadSHA, req.HeadSHA)
	}
	if prRequired(req.Kind) && req.PRNumber <= 0 {
		return fmt.Errorf("PR number is required for %s dispatch", req.Kind)
	}
	return nil
}

func validJobKind(kind JobKind) bool {
	switch kind {
	case JobKindWorker, JobKindIntegrator, JobKindMonitor, JobKindReview, JobKindReviewFix, JobKindCIFix, JobKindConflictResolution:
		return true
	default:
		return false
	}
}

func headRequired(kind JobKind) bool {
	switch kind {
	case JobKindReview, JobKindReviewFix, JobKindCIFix:
		return true
	default:
		return false
	}
}

func prRequired(kind JobKind) bool {
	switch kind {
	case JobKindReview, JobKindReviewFix, JobKindCIFix:
		return true
	default:
		return false
	}
}

func IdempotencyKey(req DispatchRequest) string {
	issueOrPR := req.IssueNumber
	if issueOrPR == 0 {
		issueOrPR = req.PRNumber
	}
	parts := []string{
		"repo", strconv.FormatInt(req.RepoID, 10),
		"job", string(req.Kind),
		"batch", strconv.Itoa(req.BatchNumber),
		"target", strconv.Itoa(issueOrPR),
		"head", req.HeadSHA,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))
	return "workflow_dispatch:" + hex.EncodeToString(sum[:])
}

func workflowURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s/actions", owner, repo)
}

type InstallationClientFactory func(ctx context.Context, installationID int64) (*gh.Client, error)

type AppWorkflowClient struct {
	NewClient InstallationClientFactory
}

func NewAppWorkflowClient(source appauth.TokenSource) AppWorkflowClient {
	return AppWorkflowClient{
		NewClient: func(ctx context.Context, id int64) (*gh.Client, error) {
			client, _, err := appauth.NewInstallationClient(ctx, source, id)
			return client, err
		},
	}
}

func (c AppWorkflowClient) DispatchWorkflow(ctx context.Context, installationID int64, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	if c.NewClient == nil {
		return fmt.Errorf("installation GitHub client factory is required")
	}
	client, err := c.NewClient(ctx, installationID)
	if err != nil {
		return err
	}
	return dispatchWithClient(ctx, client, owner, repo, workflowFile, ref, inputs)
}

func dispatchWithClient(ctx context.Context, client *gh.Client, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	if client == nil {
		return fmt.Errorf("GitHub client is required")
	}
	ghInputs := make(map[string]any, len(inputs))
	for k, v := range inputs {
		ghInputs[k] = v
	}
	_, err := client.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner, repo, workflowFile, gh.CreateWorkflowDispatchEventRequest{
		Ref:    ref,
		Inputs: ghInputs,
	})
	if err != nil {
		return fmt.Errorf("creating workflow dispatch event: %w", err)
	}
	return nil
}
