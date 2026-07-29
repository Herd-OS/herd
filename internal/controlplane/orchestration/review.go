package orchestration

import (
	"context"
	"fmt"
	"strings"

	cpdispatch "github.com/herd-os/herd/internal/controlplane/dispatch"
	"github.com/herd-os/herd/internal/controlplane/review"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// EnsureReviewFixIssue creates one fix issue per review finding fingerprint.
func (s Service) EnsureReviewFixIssue(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult, finding review.Finding) (int, bool, error) {
	if err := s.validate(); err != nil {
		return 0, false, err
	}
	if err := s.validateReviewRepository(repo); err != nil {
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
	key := idempotencyKey("review-fix-issue", "repo", repo.ID, "pr", result.PRNumber, "head", result.HeadSHA, "finding", finding.Fingerprint)
	body, overflow := issues.TruncateIssueBody(body)
	body = bodyWithMarker(body, reviewFixIssueCreateMarker(key))
	req := reviewFixIssueRequest{
		BatchNumber: result.BatchNumber,
		Title:       title,
		Body:        body,
		Overflow:    overflow,
		Labels:      []string{issues.TypeFix, issues.StatusInProgress},
		Milestone:   result.BatchNumber,
	}
	created := false
	resultRef, err := s.withIdempotencyRepair(ctx, key, "review_fix_issue_create", func() (string, bool, error) {
		return s.repairReviewFixIssue(ctx, req, key)
	}, func() (string, error) {
		createdIssue, err := s.Platform.Issues().Create(ctx, req.Title, req.Body, req.Labels, &req.Milestone)
		if err != nil {
			return "", err
		}
		created = true
		return fmt.Sprintf("issue:%d", createdIssue.Number), nil
	})
	if err != nil {
		return 0, false, err
	}
	issueNumber, ok := parseIssueResult(resultRef)
	if !ok {
		return 0, false, fmt.Errorf("invalid review fix issue result ref %q", resultRef)
	}
	if err := s.ensureOverflowComments(ctx, issueNumber, "review-fix-create", req.Overflow); err != nil {
		return 0, false, err
	}
	return issueNumber, created, nil
}

type reviewFixIssueRequest struct {
	BatchNumber int
	Title       string
	Body        string
	Overflow    string
	Labels      []string
	Milestone   int
}

func (s Service) repairReviewFixIssue(ctx context.Context, req reviewFixIssueRequest, key string) (string, bool, error) {
	issuesFound, err := s.Platform.Issues().List(ctx, platformIssueFilters(req.BatchNumber))
	if err != nil {
		return "", false, fmt.Errorf("list review fix issues for recovery: %w", err)
	}
	for _, issue := range issuesFound {
		if issue == nil || issue.Title != req.Title {
			continue
		}
		if !strings.Contains(issue.Body, reviewFixIssueCreateMarker(key)) {
			continue
		}
		return fmt.Sprintf("issue:%d", issue.Number), true, nil
	}
	return "", false, nil
}

func platformIssueFilters(batchNumber int) platform.IssueFilters {
	return platform.IssueFilters{State: "all", Milestone: &batchNumber}
}

func reviewFixIssueCreateMarker(key string) string {
	return "herd:review-fix-issue " + key
}

// DispatchReviewFixWorker dispatches a fix worker for a review fix issue.
func (s Service) DispatchReviewFixWorker(ctx context.Context, repo review.Repository, result review.ReviewCompletedResult, issueNumber int) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	if err := s.validateReviewRepository(repo); err != nil {
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

func (s Service) validateReviewRepository(repo review.Repository) error {
	if repo.ID != s.Repo.ID ||
		repo.InstallationID != s.Repo.InstallationID ||
		!strings.EqualFold(strings.TrimSpace(repo.Owner), strings.TrimSpace(s.Repo.Owner)) ||
		!strings.EqualFold(strings.TrimSpace(repo.Name), strings.TrimSpace(s.Repo.Name)) {
		return fmt.Errorf("review repository %d %s/%s installation %d does not match service repository %d %s/%s installation %d",
			repo.ID, repo.Owner, repo.Name, repo.InstallationID,
			s.Repo.ID, s.Repo.Owner, s.Repo.Name, s.Repo.InstallationID)
	}
	return nil
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
