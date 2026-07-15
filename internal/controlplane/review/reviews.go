package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	mutationspkg "github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
)

var ErrReviewSubmissionInProgress = errors.New("review submission is already in progress")

const (
	ResultStatusApproved         = "approved"
	ResultStatusChangesRequested = "changes_requested"
	ResultStatusFailure          = "failure"
	ResultStatusTimedOut         = "timed_out"
	ResultStatusUnparseable      = "unparseable"
	ResultStatusMaxCyclesHit     = "max_cycles_hit"
)

type ReviewCompletedResult struct {
	Repository  string
	JobID       string
	BatchNumber int
	PRNumber    int
	BatchBranch string
	HeadSHA     string
	Status      string
	Summary     string
	TargetURL   string
	FixCycle    int
	Findings    []Finding
}

type Finding struct {
	Fingerprint string
	Severity    string
	Description string
}

type DispatchReviewRequest struct {
	BatchNumber     int
	PRNumber        int
	BatchBranch     string
	HeadSHA         string
	WorkflowFile    string
	Ref             string
	RunnerLabel     string
	TimeoutMinutes  int
	ControlPlaneURL string
	Reason          string
	LockTTL         time.Duration
}

type ReviewDispatchResult struct {
	Locked     bool
	Dispatched bool
	JobID      string
	TargetURL  string
}

type PullRequestClient interface {
	GetPullRequest(ctx context.Context, installationID int64, owner, repo string, number int) (*platform.PullRequest, error)
	CreateReviewForCommit(ctx context.Context, installationID int64, owner, repo string, number int, body string, event platform.ReviewEvent, commitID string) error
	AddPullRequestComment(ctx context.Context, installationID int64, owner, repo string, number int, body string) error
}

type ReviewLookupClient interface {
	FindReviewForCommit(ctx context.Context, installationID int64, owner, repo string, number int, body string, event platform.ReviewEvent, commitID string) (bool, error)
}

type LockStore interface {
	AcquireReviewLock(ctx context.Context, lock store.ReviewLock) (created bool, err error)
	ReleaseReviewLock(ctx context.Context, repoID int64, prNumber int, headSHA string, holder string, releasedAt time.Time) error
}

type Dispatcher interface {
	Dispatch(ctx context.Context, req cpdispatch.DispatchRequest) (cpdispatch.DispatchResult, error)
}

type FixCoordinator interface {
	EnsureReviewFixIssue(ctx context.Context, repo Repository, result ReviewCompletedResult, finding Finding) (int, bool, error)
	DispatchReviewFixWorker(ctx context.Context, repo Repository, result ReviewCompletedResult, issueNumber int) (bool, error)
}

type ReviewService struct {
	Status     StatusService
	GitHub     PullRequestClient
	Mutations  ReviewMutationStore
	Locks      LockStore
	Dispatcher Dispatcher
	Fixes      FixCoordinator
	Now        func() time.Time
}

type ReviewMutationStore interface {
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
	GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error)
}

func (s ReviewService) MarkReviewPending(ctx context.Context, repo Repository, prNumber int, headSHA string, description, targetURL string) error {
	return s.Status.SetHerdReviewStatus(ctx, repo, prNumber, headSHA, ReviewStatusPending, description, targetURL)
}

func (s ReviewService) DispatchReview(ctx context.Context, repo Repository, req DispatchReviewRequest) (ReviewDispatchResult, error) {
	if !repo.ReviewEnabled {
		return ReviewDispatchResult{}, nil
	}
	if s.Locks == nil {
		return ReviewDispatchResult{}, fmt.Errorf("review lock store is required")
	}
	if s.Dispatcher == nil {
		return ReviewDispatchResult{}, fmt.Errorf("review dispatcher is required")
	}
	if err := validateReviewDispatch(repo, req); err != nil {
		return ReviewDispatchResult{}, err
	}
	now := s.now()
	ttl := req.LockTTL
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	holder := reviewLockHolder(repo.ID, req.PRNumber, req.HeadSHA)
	locked, err := s.Locks.AcquireReviewLock(ctx, store.ReviewLock{
		RepositoryID: repo.ID,
		PRNumber:     req.PRNumber,
		HeadSHA:      req.HeadSHA,
		Holder:       holder,
		ExpiresAt:    now.Add(ttl),
		AcquiredAt:   now,
	})
	if err != nil {
		return ReviewDispatchResult{}, fmt.Errorf("acquire review lock: %w", err)
	}
	if !locked {
		return ReviewDispatchResult{Locked: false}, nil
	}
	if err := s.MarkReviewPending(ctx, repo, req.PRNumber, req.HeadSHA, "Herd Review is running on a self-hosted worker", ""); err != nil {
		_ = s.Locks.ReleaseReviewLock(ctx, repo.ID, req.PRNumber, req.HeadSHA, holder, s.now())
		return ReviewDispatchResult{}, err
	}
	workflowFile := strings.TrimSpace(req.WorkflowFile)
	if workflowFile == "" {
		workflowFile = "herd-review.yml"
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = firstNonEmpty(repo.DefaultBranch, "main")
	}
	dispatched, err := s.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:          repo.ID,
		Owner:           repo.Owner,
		Repo:            repo.Name,
		InstallationID:  repo.InstallationID,
		Kind:            cpdispatch.JobKindReview,
		WorkflowFile:    workflowFile,
		Ref:             ref,
		BatchNumber:     req.BatchNumber,
		PRNumber:        req.PRNumber,
		BatchBranch:     req.BatchBranch,
		HeadSHA:         req.HeadSHA,
		ExpectedHeadSHA: req.HeadSHA,
		RunnerLabel:     req.RunnerLabel,
		TimeoutMinutes:  req.TimeoutMinutes,
		ControlPlaneURL: req.ControlPlaneURL,
		Reason:          req.Reason,
	})
	if err != nil {
		_ = s.Locks.ReleaseReviewLock(ctx, repo.ID, req.PRNumber, req.HeadSHA, holder, s.now())
		_ = s.Status.SetHerdReviewStatus(ctx, repo, req.PRNumber, req.HeadSHA, ReviewStatusFailure, "Herd Review workflow dispatch failed", "")
		return ReviewDispatchResult{}, fmt.Errorf("dispatch review workflow: %w", err)
	}
	if !dispatched.Created {
		_ = s.Locks.ReleaseReviewLock(ctx, repo.ID, req.PRNumber, req.HeadSHA, holder, s.now())
	}
	return ReviewDispatchResult{Locked: true, Dispatched: dispatched.Created, JobID: dispatched.JobID, TargetURL: dispatched.URL}, nil
}

func (s ReviewService) SubmitReviewResult(ctx context.Context, repo Repository, result ReviewCompletedResult) error {
	if !repo.ReviewEnabled {
		return nil
	}
	if s.GitHub == nil {
		return fmt.Errorf("review GitHub client is required")
	}
	if err := validateReviewResult(result); err != nil {
		return err
	}
	current, err := s.GitHub.GetPullRequest(ctx, repo.InstallationID, repo.Owner, repo.Name, result.PRNumber)
	if err != nil {
		return fmt.Errorf("get pull request head before Herd Review submission: %w", err)
	}
	if current.HeadSHA != "" && current.HeadSHA != result.HeadSHA {
		return s.Status.SetHerdReviewStatus(ctx, repo, result.PRNumber, current.HeadSHA, ReviewStatusPending, "Herd Review pending for the latest PR head", targetURL(result, current.URL))
	}
	defer s.releaseReviewLock(ctx, repo, result.PRNumber, result.HeadSHA)

	if result.Status == ResultStatusChangesRequested && repo.ReviewFixEnabled && s.Fixes != nil {
		return s.handleChangesWithFixes(ctx, repo, result, current)
	}

	event, state, description := reviewEventAndStatus(result)
	if event != "" {
		if err := s.submitPRReviewOnce(ctx, repo, result, event); err != nil {
			if errors.Is(err, ErrReviewSubmissionInProgress) {
				return err
			}
			statusErr := s.Status.SetHerdReviewStatus(ctx, repo, result.PRNumber, result.HeadSHA, ReviewStatusFailure, "Herd Review could not submit a PR review", targetURL(result, current.URL))
			commentErr := s.GitHub.AddPullRequestComment(ctx, repo.InstallationID, repo.Owner, repo.Name, result.PRNumber, reviewSubmissionFailureComment(err))
			if statusErr != nil {
				return statusErr
			}
			if commentErr != nil {
				return commentErr
			}
			return nil
		}
	}
	return s.Status.SetHerdReviewStatus(ctx, repo, result.PRNumber, result.HeadSHA, state, description, targetURL(result, current.URL))
}

func (s ReviewService) submitPRReviewOnce(ctx context.Context, repo Repository, result ReviewCompletedResult, event platform.ReviewEvent) error {
	key := reviewSubmissionKey(repo, result, event)
	if s.Mutations == nil {
		return fmt.Errorf("review submission mutation store is required")
	}
	request, err := json.Marshal(map[string]any{
		"repository": repo.Owner + "/" + repo.Name,
		"pr_number":  result.PRNumber,
		"head_sha":   result.HeadSHA,
		"status":     result.Status,
		"event":      event,
		"job_id":     result.JobID,
	})
	if err != nil {
		return fmt.Errorf("marshal review submission request: %w", err)
	}
	created, err := s.Mutations.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "review_submission",
		Status:    "started",
		Metadata:  request,
		CreatedAt: s.now(),
	})
	if err != nil {
		return fmt.Errorf("acquire review submission idempotency: %w", err)
	}
	if !created {
		record, err := s.Mutations.GetIdempotencyKey(ctx, key)
		if err != nil {
			return fmt.Errorf("get review submission idempotency: %w", err)
		}
		if record.Status == "completed" {
			return nil
		}
		if repaired, repairErr := s.repairCompletedReviewSubmission(ctx, key); repaired || repairErr != nil {
			return repairErr
		}
		if repaired, repairErr := s.repairStartedReviewSubmission(ctx, key, repo, result, event); repaired || repairErr != nil {
			return repairErr
		}
		if record.Status == "started" {
			if attempt, attemptErr := s.Mutations.GetGitHubMutationAttempt(ctx, key); attemptErr == nil && mutationspkg.IsPreCallRetryable(attempt.Status) {
				// Safe redelivery: the review submission GitHub call had not started.
			} else if attemptErr != nil && !errors.Is(attemptErr, store.ErrNotFound) {
				return fmt.Errorf("get review submission mutation attempt: %w", attemptErr)
			} else {
				return fmt.Errorf("%w: %s", ErrReviewSubmissionInProgress, key)
			}
		}
		if record.Status == "failed" {
			if attempt, attemptErr := s.Mutations.GetGitHubMutationAttempt(ctx, key); attemptErr != nil && !errors.Is(attemptErr, store.ErrNotFound) {
				return fmt.Errorf("get failed review submission mutation attempt: %w", attemptErr)
			} else if attemptErr == nil && !mutationspkg.IsPreCallRetryable(attempt.Status) && !mutationspkg.IsCompleted(attempt.Status) {
				return fmt.Errorf("%w: %s has unknown outcome after failed mutation attempt", ErrReviewSubmissionInProgress, key)
			} else if errors.Is(attemptErr, store.ErrNotFound) {
				if err := s.Mutations.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
					IdempotencyKey: key,
					RepositoryID:   repo.ID,
					MutationType:   "review_submission",
					Status:         mutationspkg.PhaseIntentRecorded,
					Request:        request,
					CreatedAt:      s.now(),
				}); err != nil {
					if errors.Is(err, store.ErrAlreadyExists) {
						return fmt.Errorf("%w: %s", ErrReviewSubmissionInProgress, key)
					}
					return fmt.Errorf("record retry review submission mutation attempt: %w", err)
				}
			}
		}
	} else if err := s.Mutations.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
		IdempotencyKey: key,
		RepositoryID:   repo.ID,
		MutationType:   "review_submission",
		Status:         mutationspkg.PhaseIntentRecorded,
		Request:        request,
		CreatedAt:      s.now(),
	}); err != nil {
		_ = s.Mutations.FailIdempotencyKey(ctx, key, err.Error())
		if errors.Is(err, store.ErrAlreadyExists) {
			return fmt.Errorf("%w: %s", ErrReviewSubmissionInProgress, key)
		}
		return fmt.Errorf("record review submission mutation attempt: %w", err)
	}
	if err := s.Mutations.CompleteGitHubMutationAttempt(ctx, key, mutationspkg.PhaseCallStarted, nil, "", s.now()); err != nil {
		_ = s.Mutations.CompleteGitHubMutationAttempt(ctx, key, mutationspkg.PhaseFailedPreCall, nil, err.Error(), s.now())
		_ = s.Mutations.FailIdempotencyKey(ctx, key, err.Error())
		return fmt.Errorf("mark review submission mutation call started: %w", err)
	}
	if err := s.GitHub.CreateReviewForCommit(ctx, repo.InstallationID, repo.Owner, repo.Name, result.PRNumber, reviewBody(result), event, result.HeadSHA); err != nil {
		_ = s.Mutations.CompleteGitHubMutationAttempt(ctx, key, mutationspkg.PhaseRepairRequired, nil, err.Error(), s.now())
		_ = s.Mutations.FailIdempotencyKey(ctx, key, err.Error())
		return err
	}
	response, _ := json.Marshal(map[string]any{"submitted": true, "event": event, "head_sha": result.HeadSHA})
	if err := s.Mutations.CompleteGitHubMutationAttempt(ctx, key, mutationspkg.PhaseCompleted, response, "", s.now()); err != nil {
		return fmt.Errorf("complete review submission mutation attempt: %w", err)
	}
	if err := s.Mutations.CompleteIdempotencyKey(ctx, key, string(response)); err != nil {
		return fmt.Errorf("complete review submission idempotency: %w", err)
	}
	return nil
}

func (s ReviewService) repairCompletedReviewSubmission(ctx context.Context, key string) (bool, error) {
	attempt, err := s.Mutations.GetGitHubMutationAttempt(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get review submission mutation attempt: %w", err)
	}
	if !mutationspkg.IsCompleted(attempt.Status) {
		return false, nil
	}
	response := attempt.Response
	if len(response) == 0 {
		response = json.RawMessage(`{"submitted":true}`)
	}
	if err := s.Mutations.CompleteIdempotencyKey(ctx, key, string(response)); err != nil {
		return false, fmt.Errorf("repair review submission idempotency: %w", err)
	}
	return true, nil
}

func (s ReviewService) repairStartedReviewSubmission(ctx context.Context, key string, repo Repository, result ReviewCompletedResult, event platform.ReviewEvent) (bool, error) {
	attempt, err := s.Mutations.GetGitHubMutationAttempt(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get review submission mutation attempt: %w", err)
	}
	if !mutationspkg.IsPostCallUnknown(attempt.Status) {
		return false, nil
	}
	lookup, ok := s.GitHub.(ReviewLookupClient)
	if !ok {
		return false, nil
	}
	found, err := lookup.FindReviewForCommit(ctx, repo.InstallationID, repo.Owner, repo.Name, result.PRNumber, reviewBody(result), event, result.HeadSHA)
	if err != nil {
		return false, fmt.Errorf("repair review submission lookup: %w", err)
	}
	if !found {
		return false, nil
	}
	response, _ := json.Marshal(map[string]any{"submitted": true, "event": event, "head_sha": result.HeadSHA, "repaired": true})
	if err := s.Mutations.CompleteGitHubMutationAttempt(ctx, key, mutationspkg.PhaseCompleted, response, "", s.now()); err != nil {
		return true, fmt.Errorf("repair review submission mutation attempt: %w", err)
	}
	if err := s.Mutations.CompleteIdempotencyKey(ctx, key, string(response)); err != nil {
		return true, fmt.Errorf("repair review submission idempotency: %w", err)
	}
	return true, nil
}

func (s ReviewService) handleChangesWithFixes(ctx context.Context, repo Repository, result ReviewCompletedResult, current *platform.PullRequest) error {
	if repo.ReviewMaxFixCycles > 0 && result.FixCycle >= repo.ReviewMaxFixCycles {
		return s.Status.SetHerdReviewStatus(ctx, repo, result.PRNumber, result.HeadSHA, ReviewStatusFailure, "Herd Review reached the maximum fix cycles", targetURL(result, current.URL))
	}
	findings := actionableFindings(result.Findings, repo.ReviewFixSeverity)
	if len(findings) == 0 {
		return s.Status.SetHerdReviewStatus(ctx, repo, result.PRNumber, result.HeadSHA, ReviewStatusFailure, "Herd Review requested changes but returned no actionable fix findings", targetURL(result, current.URL))
	}
	for _, finding := range findings {
		issueNumber, _, err := s.Fixes.EnsureReviewFixIssue(ctx, repo, result, finding)
		if err != nil {
			return fmt.Errorf("ensure review fix issue: %w", err)
		}
		if _, err := s.Fixes.DispatchReviewFixWorker(ctx, repo, result, issueNumber); err != nil {
			return fmt.Errorf("dispatch review fix worker: %w", err)
		}
	}
	return s.Status.SetHerdReviewStatus(ctx, repo, result.PRNumber, result.HeadSHA, ReviewStatusPending, "Herd Review requested changes; fix workers are running", targetURL(result, current.URL))
}

func (s ReviewService) releaseReviewLock(ctx context.Context, repo Repository, prNumber int, headSHA string) {
	if s.Locks == nil {
		return
	}
	_ = s.Locks.ReleaseReviewLock(ctx, repo.ID, prNumber, headSHA, reviewLockHolder(repo.ID, prNumber, headSHA), s.now())
}

func validateReviewResult(result ReviewCompletedResult) error {
	if result.PRNumber <= 0 {
		return fmt.Errorf("PR number is required")
	}
	if strings.TrimSpace(result.HeadSHA) == "" {
		return fmt.Errorf("head SHA is required")
	}
	if strings.TrimSpace(result.Summary) == "" {
		return fmt.Errorf("review summary is required")
	}
	switch result.Status {
	case ResultStatusApproved, ResultStatusChangesRequested, ResultStatusFailure, ResultStatusTimedOut, ResultStatusUnparseable, ResultStatusMaxCyclesHit:
		return nil
	default:
		return fmt.Errorf("unsupported review result status %q", result.Status)
	}
}

func validateReviewDispatch(repo Repository, req DispatchReviewRequest) error {
	if err := validateStatusInput(repo, req.PRNumber, req.HeadSHA, ReviewStatusPending); err != nil {
		return err
	}
	if req.BatchNumber <= 0 {
		return fmt.Errorf("batch number is required")
	}
	return nil
}

func reviewEventAndStatus(result ReviewCompletedResult) (platform.ReviewEvent, ReviewStatusState, string) {
	switch result.Status {
	case ResultStatusApproved:
		return platform.ReviewApprove, ReviewStatusSuccess, "Herd Review approved this PR head"
	case ResultStatusChangesRequested:
		return platform.ReviewRequestChanges, ReviewStatusFailure, "Herd Review requested changes"
	case ResultStatusTimedOut:
		return "", ReviewStatusFailure, "Herd Review timed out"
	case ResultStatusUnparseable:
		return "", ReviewStatusFailure, "Herd Review returned an unparseable result"
	case ResultStatusMaxCyclesHit:
		return "", ReviewStatusFailure, "Herd Review reached the maximum fix cycles"
	default:
		return "", ReviewStatusFailure, "Herd Review failed"
	}
}

func reviewBody(result ReviewCompletedResult) string {
	body := strings.TrimSpace(result.Summary)
	if body == "" {
		body = "Herd Review completed."
	}
	return body
}

func reviewSubmissionKey(repo Repository, result ReviewCompletedResult, event platform.ReviewEvent) string {
	return fmt.Sprintf("review_submission:%d:%d:%s:%s:%s:%s", repo.ID, result.PRNumber, result.HeadSHA, result.Status, event, result.JobID)
}

func reviewSubmissionFailureComment(err error) string {
	return "Herd Review could not submit an App-authored pull request review. The Herd Review commit status has been set to failure.\n\nError: " + err.Error()
}

func targetURL(result ReviewCompletedResult, prURL string) string {
	if strings.TrimSpace(result.TargetURL) != "" {
		return strings.TrimSpace(result.TargetURL)
	}
	return strings.TrimSpace(prURL)
}

func (s ReviewService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func reviewLockHolder(repoID int64, prNumber int, headSHA string) string {
	return fmt.Sprintf("herd-review:%d:%d:%s", repoID, prNumber, headSHA)
}

func actionableFindings(findings []Finding, minSeverity string) []Finding {
	min := severityRank(minSeverity)
	if min == 0 {
		min = severityRank("medium")
	}
	out := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		if strings.TrimSpace(finding.Fingerprint) == "" || strings.TrimSpace(finding.Description) == "" {
			continue
		}
		if severityRank(finding.Severity) >= min {
			out = append(out, finding)
		}
	}
	return out
}

func severityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
