package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/google/uuid"
	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/mutationguard"
	"github.com/herd-os/herd/internal/controlplane/mutations"
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

	mutationStatusPreDispatch = mutations.PhaseIntentRecorded
	mutationStatusDispatching = mutations.PhaseCallStarted
	mutationStatusUnknown     = mutations.PhaseRepairRequired
)

type DispatchRequest struct {
	RepoID            int64
	GitHubRepoID      int64
	Owner             string
	Repo              string
	InstallationID    int64
	Kind              JobKind
	WorkflowFile      string
	Ref               string
	BatchNumber       int
	IssueNumber       int
	PRNumber          int
	BatchBranch       string
	BaseSHA           string
	HeadSHA           string
	ExpectedHeadSHA   string
	Mode              string
	RunnerLabel       string
	TimeoutMinutes    int
	ControlPlaneURL   string
	Reason            string
	ReviewPrompt      string
	ManualReview      bool
	ManualDispatchKey string
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
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
}

type MutationRecorder interface {
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
}

type MutationReader interface {
	GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error)
}

type MutationStarter interface {
	TryStartGitHubMutationAttempt(ctx context.Context, idempotencyKey string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error)
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
	if err := d.requireMutationStore(); err != nil {
		return DispatchResult{}, err
	}

	idempotencyKey, jobID, now, created, err := d.recordIntent(ctx, req)
	if err != nil {
		return DispatchResult{}, err
	}
	if !created {
		return d.duplicateResult(ctx, req, idempotencyKey)
	}

	inputs, err := WorkflowInputs(req, jobID)
	if err != nil {
		return DispatchResult{}, err
	}
	return d.dispatchWithJob(ctx, req, idempotencyKey, jobID, inputs, now, true)
}

// RecordIntent persists the pre-call workflow dispatch boundary without
// invoking GitHub. Callers that must perform other durable state transitions
// before the workflow_dispatch API call use this to ensure redelivery can
// recover from labels or other local state that say "started" before the
// external workflow is actually dispatched.
func (d Dispatcher) RecordIntent(ctx context.Context, req DispatchRequest) error {
	if err := validateRequest(req); err != nil {
		return err
	}
	if d.Store == nil {
		return fmt.Errorf("dispatch store is required")
	}
	if err := d.requireMutationStore(); err != nil {
		return err
	}
	_, _, _, _, err := d.recordIntent(ctx, req)
	return err
}

func (d Dispatcher) recordIntent(ctx context.Context, req DispatchRequest) (string, string, time.Time, bool, error) {
	idempotencyKey := IdempotencyKey(req)
	jobID := "job_" + uuid.NewString()
	now := time.Now().UTC()
	keyMetadataValues := map[string]any{
		"job_id":         jobID,
		"repo_id":        req.RepoID,
		"github_repo_id": req.GitHubRepoID,
		"job_kind":       req.Kind,
		"workflow_file":  req.WorkflowFile,
		"ref":            req.Ref,
		"batch_number":   req.BatchNumber,
		"issue_number":   req.IssueNumber,
		"pr_number":      req.PRNumber,
		"batch_branch":   req.BatchBranch,
		"head_sha":       req.HeadSHA,
	}
	if req.ReviewPrompt != "" {
		keyMetadataValues["review_prompt"] = req.ReviewPrompt
	}
	if req.ManualDispatchKey != "" {
		keyMetadataValues["manual_dispatch_key"] = req.ManualDispatchKey
	}
	keyMetadata, err := json.Marshal(keyMetadataValues)
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("marshal idempotency metadata: %w", err)
	}
	created, err := d.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       idempotencyKey,
		Scope:     "workflow_dispatch",
		Status:    mutations.PhaseIntentRecorded,
		Metadata:  keyMetadata,
		CreatedAt: now,
	})
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("acquire dispatch idempotency key: %w", err)
	}
	return idempotencyKey, jobID, now, created, nil
}

func (d Dispatcher) dispatchWithJob(ctx context.Context, req DispatchRequest, idempotencyKey string, jobID string, inputs map[string]string, now time.Time, createJob bool) (DispatchResult, error) {
	if completed, result, err := d.completedDispatchMutation(ctx, idempotencyKey); completed || err != nil {
		if result.JobID == "" {
			result.JobID = jobID
		}
		result.Created = false
		return result, err
	}
	jobMetadataValues := map[string]any{
		"kind":              req.Kind,
		"workflow_file":     req.WorkflowFile,
		"ref":               req.Ref,
		"batch_number":      req.BatchNumber,
		"issue_number":      req.IssueNumber,
		"pr_number":         req.PRNumber,
		"batch_branch":      req.BatchBranch,
		"base_sha":          dispatchBaseSHA(req),
		"head_sha":          req.HeadSHA,
		"repository":        req.Owner + "/" + req.Repo,
		"github_repo_id":    req.GitHubRepoID,
		"owner":             req.Owner,
		"repo":              req.Repo,
		"expected_head_sha": req.ExpectedHeadSHA,
		"mode":              req.Mode,
		"runner_label":      req.RunnerLabel,
		"timeout_minutes":   req.TimeoutMinutes,
		"reason":            req.Reason,
		"idempotency_key":   idempotencyKey,
	}
	if req.ReviewPrompt != "" {
		jobMetadataValues["review_prompt"] = req.ReviewPrompt
	}
	if req.ManualDispatchKey != "" {
		jobMetadataValues["manual_dispatch_key"] = req.ManualDispatchKey
	}
	jobMetadata, err := json.Marshal(jobMetadataValues)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("marshal job metadata: %w", err)
	}
	if createJob {
		if err := d.Store.CreateJob(ctx, store.Job{
			JobID:          jobID,
			RepositoryID:   req.RepoID,
			InstallationID: req.InstallationID,
			PRNumber:       req.PRNumber,
			BaseSHA:        dispatchBaseSHA(req),
			HeadSHA:        req.HeadSHA,
			Status:         "dispatching",
			WorkerBranch:   req.BatchBranch,
			Metadata:       jobMetadata,
			CreatedAt:      now,
			UpdatedAt:      now,
		}); err != nil {
			_ = d.Store.FailIdempotencyKey(ctx, idempotencyKey, mutations.PhaseFailedPreCall+":"+err.Error())
			return DispatchResult{}, fmt.Errorf("create dispatch job: %w", err)
		}
	}

	requestJSON, err := workflowDispatchRequest(req, inputs)
	if err != nil {
		_ = d.Store.FailIdempotencyKey(ctx, idempotencyKey, mutations.PhaseFailedPreCall+":"+err.Error())
		return DispatchResult{}, err
	}
	if d.GitHub == nil {
		_ = d.Store.FailIdempotencyKey(ctx, idempotencyKey, mutations.PhaseFailedPreCall+":dispatch GitHub client is required")
		return DispatchResult{}, fmt.Errorf("dispatch GitHub client is required")
	}
	mutationStore, ok := d.Store.(mutationguard.Store)
	if !ok {
		return DispatchResult{}, fmt.Errorf("workflow dispatch mutation store is required")
	}
	expected := DispatchResult{
		JobID:   jobID,
		URL:     workflowURL(req.Owner, req.Repo),
		Created: true,
	}
	resultJSON, err := json.Marshal(expected)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("marshal dispatch result: %w", err)
	}
	run, err := mutationguard.Run(ctx, mutationStore, mutationguard.RunRequest{
		Key:          idempotencyKey,
		RepositoryID: req.RepoID,
		MutationType: "workflow_dispatch",
		Request:      requestJSON,
		ResultRef:    workflowDispatchResultRef,
		Response: func(string) json.RawMessage {
			return json.RawMessage(resultJSON)
		},
		Accepted: func(_ string, response json.RawMessage) string {
			return "dispatch_accepted:" + string(response)
		},
		Mutate: func() (string, error) {
			if err := d.GitHub.DispatchWorkflow(ctx, req.InstallationID, req.Owner, req.Repo, req.WorkflowFile, req.Ref, inputs); err != nil {
				var preCallErr PreCallError
				if errors.As(err, &preCallErr) {
					return "", err
				}
				return "", fmt.Errorf("dispatch workflow outcome is unknown after GitHub call: %w", err)
			}
			return string(resultJSON), nil
		},
		Now: time.Now,
	})
	if err != nil {
		return DispatchResult{}, wrapWorkflowDispatchMutationError(err)
	}
	var result DispatchResult
	if err := json.Unmarshal([]byte(run.ResultRef), &result); err != nil {
		return DispatchResult{}, fmt.Errorf("decode dispatch result: %w", err)
	}
	if run.Replayed {
		result.Created = false
	}
	return result, nil
}

type PreCallError = mutations.PreCallError

func workflowDispatchRequest(req DispatchRequest, inputs map[string]string) (json.RawMessage, error) {
	requestJSON, marshalErr := json.Marshal(map[string]any{
		"owner":         req.Owner,
		"repo":          req.Repo,
		"workflow_file": req.WorkflowFile,
		"ref":           req.Ref,
		"inputs":        inputs,
	})
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal mutation request: %w", marshalErr)
	}
	return requestJSON, nil
}

func workflowDispatchResultRef(response json.RawMessage) string {
	var result DispatchResult
	if len(response) == 0 || json.Unmarshal(response, &result) != nil || result.JobID == "" {
		return ""
	}
	return string(response)
}

func wrapWorkflowDispatchMutationError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "record mutation attempt:"):
		return fmt.Errorf("record workflow dispatch mutation attempt: %w", err)
	case strings.Contains(msg, "complete mutation attempt:") && strings.Contains(msg, "complete idempotency key after mutation attempt failure:"):
		return fmt.Errorf("complete workflow dispatch mutation attempt after GitHub accepted dispatch: %w", err)
	case strings.Contains(msg, "complete idempotency key:"):
		return fmt.Errorf("complete dispatch idempotency key: %w", err)
	default:
		return err
	}
}

func (d Dispatcher) duplicateResult(ctx context.Context, req DispatchRequest, idempotencyKey string) (DispatchResult, error) {
	record, err := d.Store.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("get dispatch idempotency key: %w", err)
	}
	if record.Status == "completed" && record.ResultRef != "" {
		var result DispatchResult
		if err := json.Unmarshal([]byte(record.ResultRef), &result); err == nil && result.JobID != "" {
			_ = d.repairCompletedMutationAttempt(ctx, idempotencyKey, json.RawMessage(record.ResultRef))
			result.Created = false
			return result, nil
		}
	}
	if record.Status == "failed" {
		if resultJSON, ok := strings.CutPrefix(record.ResultRef, "dispatch_accepted:"); ok {
			var result DispatchResult
			if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
				return DispatchResult{}, fmt.Errorf("decode accepted workflow dispatch result: %w", err)
			}
			if err := d.completeMutationResult(ctx, idempotencyKey, GitHubMutationResult{
				Status:      "completed",
				Response:    json.RawMessage(resultJSON),
				CompletedAt: time.Now().UTC(),
			}); err != nil {
				return DispatchResult{}, err
			}
			if err := d.Store.CompleteIdempotencyKey(ctx, idempotencyKey, resultJSON); err != nil {
				return DispatchResult{}, fmt.Errorf("repair accepted workflow dispatch idempotency key: %w", err)
			}
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
	if errors.Is(err, store.ErrNotFound) && mutations.IsPreCallRetryableRecord(record.Status, record.ResultRef) {
		inputs, inputErr := WorkflowInputs(req, metadata.JobID)
		if inputErr != nil {
			return DispatchResult{}, inputErr
		}
		return d.dispatchWithJob(ctx, req, idempotencyKey, metadata.JobID, inputs, time.Now().UTC(), true)
	}
	if err != nil {
		return DispatchResult{}, fmt.Errorf("get existing dispatch job: %w", err)
	}
	if record.Status == "failed" {
		if completed, result, recoverErr := d.completedDispatchMutation(ctx, idempotencyKey); recoverErr != nil {
			return DispatchResult{}, recoverErr
		} else if completed {
			if result.JobID == "" {
				result.JobID = job.JobID
			}
			result.Created = false
			return result, nil
		}
	}
	if mutations.IsPreCallRetryableRecord(record.Status, record.ResultRef) {
		inputs, inputErr := WorkflowInputs(req, metadata.JobID)
		if inputErr != nil {
			return DispatchResult{}, inputErr
		}
		return d.dispatchWithJob(ctx, req, idempotencyKey, metadata.JobID, inputs, time.Now().UTC(), false)
	}
	if mutations.Normalize(record.Status) == mutations.PhaseCallStarted {
		preCall, preCallErr := d.preDispatchMutation(ctx, idempotencyKey)
		if preCallErr != nil {
			return DispatchResult{}, preCallErr
		}
		if preCall {
			inputs, inputErr := WorkflowInputs(req, metadata.JobID)
			if inputErr != nil {
				return DispatchResult{}, inputErr
			}
			return d.dispatchWithJob(ctx, req, idempotencyKey, metadata.JobID, inputs, time.Now().UTC(), false)
		}
	}
	if completed, result, recoverErr := d.completedDispatchMutation(ctx, idempotencyKey); recoverErr != nil {
		return DispatchResult{}, recoverErr
	} else if completed {
		if result.JobID == "" {
			result.JobID = job.JobID
		}
		result.Created = false
		return result, nil
	}
	return DispatchResult{}, fmt.Errorf("workflow dispatch %q is already in progress", idempotencyKey)
}

func (d Dispatcher) preDispatchMutation(ctx context.Context, idempotencyKey string) (bool, error) {
	reader, ok := d.Store.(MutationReader)
	if !ok {
		return false, fmt.Errorf("workflow dispatch mutation reader is required")
	}
	attempt, err := reader.GetGitHubMutationAttempt(ctx, idempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get workflow dispatch mutation attempt: %w", err)
	}
	return mutations.IsPreCallRetryable(attempt.Status), nil
}

func (d Dispatcher) repairCompletedMutationAttempt(ctx context.Context, idempotencyKey string, resultJSON json.RawMessage) error {
	reader, ok := d.Store.(MutationReader)
	if !ok {
		return fmt.Errorf("workflow dispatch mutation reader is required")
	}
	attempt, err := reader.GetGitHubMutationAttempt(ctx, idempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if mutations.IsCompleted(attempt.Status) {
		return nil
	}
	return d.completeMutationResult(ctx, idempotencyKey, GitHubMutationResult{
		Status:      mutations.PhaseCompleted,
		Response:    resultJSON,
		CompletedAt: time.Now().UTC(),
	})
}

func (d Dispatcher) completedDispatchMutation(ctx context.Context, idempotencyKey string) (bool, DispatchResult, error) {
	reader, ok := d.Store.(MutationReader)
	if !ok {
		return false, DispatchResult{}, fmt.Errorf("workflow dispatch mutation reader is required")
	}
	attempt, err := reader.GetGitHubMutationAttempt(ctx, idempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		return false, DispatchResult{}, nil
	}
	if err != nil {
		return false, DispatchResult{}, fmt.Errorf("get workflow dispatch mutation attempt: %w", err)
	}
	if mutations.IsPreCallRetryable(attempt.Status) {
		return false, DispatchResult{}, nil
	}
	if mutations.IsPostCallUnknown(attempt.Status) {
		return false, DispatchResult{}, fmt.Errorf("workflow dispatch %q is already in progress; outcome is unknown after GitHub accepted dispatch; repair required", idempotencyKey)
	}
	if !mutations.IsCompleted(attempt.Status) {
		return false, DispatchResult{}, nil
	}
	var result DispatchResult
	if len(attempt.Response) > 0 {
		_ = json.Unmarshal(attempt.Response, &result)
	}
	resultJSON := attempt.Response
	if len(resultJSON) == 0 || result.JobID == "" {
		resultJSON = json.RawMessage(`{"created":false}`)
	}
	if err := d.Store.CompleteIdempotencyKey(ctx, idempotencyKey, string(resultJSON)); err != nil {
		return false, DispatchResult{}, fmt.Errorf("repair dispatch idempotency key: %w", err)
	}
	return true, result, nil
}

func (d Dispatcher) completeMutationResult(ctx context.Context, idempotencyKey string, result GitHubMutationResult) error {
	recorder, ok := d.Store.(MutationRecorder)
	if !ok {
		return fmt.Errorf("workflow dispatch mutation recorder is required")
	}
	if err := recorder.CompleteGitHubMutationAttempt(ctx, idempotencyKey, result.Status, result.Response, result.Error, result.CompletedAt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("complete workflow dispatch mutation attempt: %w", err)
	}
	return nil
}

func (d Dispatcher) requireMutationStore() error {
	if _, ok := d.Store.(MutationRecorder); !ok {
		return fmt.Errorf("workflow dispatch mutation recorder is required")
	}
	if _, ok := d.Store.(MutationReader); !ok {
		return fmt.Errorf("workflow dispatch mutation reader is required")
	}
	if _, ok := d.Store.(MutationStarter); !ok {
		return fmt.Errorf("workflow dispatch mutation starter is required")
	}
	return nil
}

func dispatchBaseSHA(req DispatchRequest) string {
	if strings.TrimSpace(req.BaseSHA) != "" {
		return req.BaseSHA
	}
	return req.HeadSHA
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
	if req.Kind == JobKindWorker {
		if strings.TrimSpace(req.BaseSHA) == "" {
			return fmt.Errorf("base SHA is required for %s dispatch", req.Kind)
		}
		if strings.TrimSpace(req.HeadSHA) == "" {
			return fmt.Errorf("head SHA is required for %s dispatch", req.Kind)
		}
		if strings.TrimSpace(req.ExpectedHeadSHA) == "" {
			return fmt.Errorf("expected head SHA is required for %s dispatch", req.Kind)
		}
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
	}
	if req.ManualDispatchKey != "" {
		parts = append(parts, "manual", req.ManualDispatchKey)
	} else {
		parts = append(parts, "head", req.HeadSHA)
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
		return PreCallError{Op: "create installation GitHub client", Err: err}
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
