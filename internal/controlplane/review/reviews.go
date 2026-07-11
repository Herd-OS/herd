package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/platform"
)

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
	HeadSHA     string
	Status      string
	Summary     string
	TargetURL   string
}

type PullRequestClient interface {
	GetPullRequest(ctx context.Context, installationID int64, owner, repo string, number int) (*platform.PullRequest, error)
	CreateReviewForCommit(ctx context.Context, installationID int64, owner, repo string, number int, body string, event platform.ReviewEvent, commitID string) error
	AddPullRequestComment(ctx context.Context, installationID int64, owner, repo string, number int, body string) error
}

type ReviewService struct {
	Status StatusService
	GitHub PullRequestClient
}

func (s ReviewService) MarkReviewPending(ctx context.Context, repo Repository, prNumber int, headSHA string, description, targetURL string) error {
	return s.Status.SetHerdReviewStatus(ctx, repo, prNumber, headSHA, ReviewStatusPending, description, targetURL)
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

	event, state, description := reviewEventAndStatus(result)
	if event != "" {
		if err := s.GitHub.CreateReviewForCommit(ctx, repo.InstallationID, repo.Owner, repo.Name, result.PRNumber, reviewBody(result), event, result.HeadSHA); err != nil {
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

func reviewSubmissionFailureComment(err error) string {
	return "Herd Review could not submit an App-authored pull request review. The Herd Review commit status has been set to failure.\n\nError: " + err.Error()
}

func targetURL(result ReviewCompletedResult, prURL string) string {
	if strings.TrimSpace(result.TargetURL) != "" {
		return strings.TrimSpace(result.TargetURL)
	}
	return strings.TrimSpace(prURL)
}
