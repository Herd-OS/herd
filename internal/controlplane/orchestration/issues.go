package orchestration

import (
	"context"
	"fmt"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// TaskIssueRequest describes a service-owned task issue mutation.
type TaskIssueRequest struct {
	BatchNumber int
	IssueNumber int
	Title       string
	Body        string
	Labels      []string
	Milestone   int
}

// EnsureTaskIssue creates or updates a batch task issue with idempotency and
// mutation audit records. Bodies are truncated using the shared issue helpers
// so existing front matter remains parseable.
func (s Service) EnsureTaskIssue(ctx context.Context, req TaskIssueRequest) (*platform.Issue, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if req.Title == "" {
		return nil, fmt.Errorf("issue title is required")
	}
	if req.Milestone <= 0 {
		return nil, fmt.Errorf("milestone is required")
	}
	body, overflow := issues.TruncateIssueBody(req.Body)
	if req.IssueNumber > 0 {
		return s.updateTaskIssue(ctx, req, body, overflow)
	}
	return s.createTaskIssue(ctx, req, body, overflow)
}

func (s Service) createTaskIssue(ctx context.Context, req TaskIssueRequest, body, overflow string) (*platform.Issue, error) {
	key := idempotencyKey("task-issue", "repo", s.Repo.ID, "batch", req.BatchNumber, "create", req.Title)
	resultRef, err := s.withIdempotency(ctx, key, "issue_create", func() (string, error) {
		created, err := s.Platform.Issues().Create(ctx, req.Title, body, req.Labels, &req.Milestone)
		if err != nil {
			return "", err
		}
		for _, comment := range issues.SplitOverflowComments(overflow) {
			if err := s.Platform.Issues().AddComment(ctx, created.Number, comment); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("issue:%d", created.Number), nil
	})
	if err != nil {
		return nil, err
	}
	issueNumber, ok := parseIssueResult(resultRef)
	if !ok {
		return nil, fmt.Errorf("invalid issue result ref %q", resultRef)
	}
	return s.Platform.Issues().Get(ctx, issueNumber)
}

func (s Service) updateTaskIssue(ctx context.Context, req TaskIssueRequest, body, overflow string) (*platform.Issue, error) {
	key := idempotencyKey("task-issue", "repo", s.Repo.ID, "batch", req.BatchNumber, "issue", req.IssueNumber, "update")
	resultRef, err := s.withIdempotency(ctx, key, "issue_update", func() (string, error) {
		title := req.Title
		milestone := req.Milestone
		updated, err := s.Platform.Issues().Update(ctx, req.IssueNumber, platform.IssueUpdate{
			Title:     &title,
			Body:      &body,
			Milestone: &milestone,
		})
		if err != nil {
			return "", err
		}
		if len(req.Labels) > 0 {
			if err := s.Platform.Issues().AddLabels(ctx, req.IssueNumber, req.Labels); err != nil {
				return "", err
			}
		}
		for _, comment := range issues.SplitOverflowComments(overflow) {
			if err := s.Platform.Issues().AddComment(ctx, req.IssueNumber, comment); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("issue:%d", updated.Number), nil
	})
	if err != nil {
		return nil, err
	}
	issueNumber, ok := parseIssueResult(resultRef)
	if !ok {
		return nil, fmt.Errorf("invalid issue result ref %q", resultRef)
	}
	return s.Platform.Issues().Get(ctx, issueNumber)
}

func parseIssueResult(ref string) (int, bool) {
	var number int
	if _, err := fmt.Sscanf(ref, "issue:%d", &number); err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}
