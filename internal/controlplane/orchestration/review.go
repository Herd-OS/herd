package orchestration

import (
	"context"
	"fmt"
	"strings"

	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/issues"
)

// EnsureReviewFixIssue creates one fix issue per review finding fingerprint.
func (s Service) EnsureReviewFixIssue(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult, finding review.Finding) (int, bool, error) {
	if err := s.validate(); err != nil {
		return 0, false, err
	}
	if err := validateReviewFix(result, finding); err != nil {
		return 0, false, err
	}
	nextCycle := result.FixCycle + 1
	title := fmt.Sprintf("Review fix: %s", finding.Fingerprint)
	body := issues.RenderBody(issues.IssueBody{
		FrontMatter: issues.FrontMatter{
			Version:  1,
			Batch:    result.BatchNumber,
			Type:     "fix",
			FixCycle: nextCycle,
			BatchPR:  result.PRNumber,
		},
		Task:    fmt.Sprintf("Fix review finding `%s`.\n\nSeverity: %s\n\n%s\n", finding.Fingerprint, finding.Severity, finding.Description),
		Context: fmt.Sprintf("Found during Herd Review of PR #%d at head %s.", result.PRNumber, result.HeadSHA),
	})
	req := TaskIssueRequest{
		BatchNumber: result.BatchNumber,
		Title:       title,
		Body:        body,
		Labels:      []string{issues.TypeFix, issues.StatusInProgress},
		Milestone:   result.BatchNumber,
	}
	key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
	created, err := s.Store.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "review_fix_issue_create",
		Status:    mutationStatusStarted,
		CreatedAt: s.now(),
	})
	if err != nil {
		return 0, false, fmt.Errorf("acquire review fix issue idempotency key: %w", err)
	}
	if !created {
		record, err := s.Store.GetIdempotencyKey(ctx, key)
		if err != nil {
			return 0, false, err
		}
		issueNumber, ok := parseIssueResult(record.ResultRef)
		if !ok {
			return 0, false, fmt.Errorf("invalid review fix issue result ref %q", record.ResultRef)
		}
		return issueNumber, false, nil
	}
	resultRef, err := s.withAcquiredIdempotency(ctx, key, "review_fix_issue_create", func() (string, error) {
		issue, err := s.EnsureTaskIssue(ctx, req)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("issue:%d", issue.Number), nil
	})
	if err != nil {
		return 0, false, err
	}
	issueNumber, ok := parseIssueResult(resultRef)
	if !ok {
		return 0, false, fmt.Errorf("invalid review fix issue result ref %q", resultRef)
	}
	return issueNumber, true, nil
}

// DispatchReviewFixWorker dispatches a fix worker for a review fix issue.
func (s Service) DispatchReviewFixWorker(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult, issueNumber int) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	if s.Dispatcher == nil {
		return false, fmt.Errorf("dispatcher is required")
	}
	if issueNumber <= 0 {
		return false, fmt.Errorf("issue number is required")
	}
	batchBranch := strings.TrimSpace(result.BatchBranch)
	if batchBranch == "" {
		return false, fmt.Errorf("batch branch is required")
	}
	dispatch, err := s.Dispatcher.Dispatch(ctx, cpdispatch.DispatchRequest{
		RepoID:          repo.ID,
		Owner:           repo.Owner,
		Repo:            repo.Name,
		InstallationID:  repo.InstallationID,
		Kind:            cpdispatch.JobKindReviewFix,
		WorkflowFile:    "herd-worker.yml",
		Ref:             firstNonEmpty(repo.DefaultBranch, s.Repo.DefaultBranch, "main"),
		BatchNumber:     result.BatchNumber,
		IssueNumber:     issueNumber,
		PRNumber:        result.PRNumber,
		BatchBranch:     batchBranch,
		HeadSHA:         result.HeadSHA,
		ExpectedHeadSHA: result.HeadSHA,
		Reason:          "herd review finding " + result.HeadSHA,
	})
	if err != nil {
		return false, err
	}
	return dispatch.Created, nil
}

func validateReviewFix(result review.ReviewCompletedResult, finding review.Finding) error {
	if result.BatchNumber <= 0 {
		return fmt.Errorf("batch number is required")
	}
	if result.PRNumber <= 0 {
		return fmt.Errorf("PR number is required")
	}
	if strings.TrimSpace(result.HeadSHA) == "" {
		return fmt.Errorf("head SHA is required")
	}
	if strings.TrimSpace(finding.Fingerprint) == "" {
		return fmt.Errorf("finding fingerprint is required")
	}
	if strings.TrimSpace(finding.Description) == "" {
		return fmt.Errorf("finding description is required")
	}
	return nil
}
