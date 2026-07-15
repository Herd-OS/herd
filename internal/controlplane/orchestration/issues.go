package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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
	key := idempotencyKey("task-issue", "repo", s.Repo.ID, "batch", req.BatchNumber, "create", req.Title, taskIssueFingerprint(req, body, overflow))
	resultRef, err := s.withIdempotency(ctx, key, "issue_create", func() (string, error) {
		created, err := s.Platform.Issues().Create(ctx, req.Title, body, req.Labels, &req.Milestone)
		if err != nil {
			return "", err
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
	if err := s.ensureOverflowComments(ctx, issueNumber, "create", overflow); err != nil {
		return nil, err
	}
	return s.Platform.Issues().Get(ctx, issueNumber)
}

func (s Service) updateTaskIssue(ctx context.Context, req TaskIssueRequest, body, overflow string) (*platform.Issue, error) {
	key := idempotencyKey("task-issue", "repo", s.Repo.ID, "batch", req.BatchNumber, "issue", req.IssueNumber, "update", taskIssueFingerprint(req, body, overflow))
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
		return fmt.Sprintf("issue:%d", updated.Number), nil
	})
	if err != nil {
		return nil, err
	}
	issueNumber, ok := parseIssueResult(resultRef)
	if !ok {
		return nil, fmt.Errorf("invalid issue result ref %q", resultRef)
	}
	if len(req.Labels) > 0 {
		if err := s.mutate(ctx, idempotencyKey("task-issue-labels", "repo", s.Repo.ID, "issue", issueNumber, taskIssueLabelsFingerprint(req.Labels)), "issue_label_add", func() (string, error) {
			return "", s.Platform.Issues().AddLabels(ctx, issueNumber, req.Labels)
		}); err != nil {
			return nil, err
		}
	}
	if err := s.ensureOverflowComments(ctx, issueNumber, "update", overflow); err != nil {
		return nil, err
	}
	return s.Platform.Issues().Get(ctx, issueNumber)
}

func (s Service) ensureOverflowComments(ctx context.Context, issueNumber int, phase string, overflow string) error {
	for idx, comment := range issues.SplitOverflowComments(overflow) {
		comment := comment
		key := idempotencyKey("task-issue-overflow-comment", "repo", s.Repo.ID, "issue", issueNumber, phase, idx, taskIssueTextFingerprint(comment))
		if err := s.mutate(ctx, key, "issue_comment_create", func() (string, error) {
			return "", s.Platform.Issues().AddComment(ctx, issueNumber, comment)
		}); err != nil {
			return err
		}
	}
	return nil
}

func taskIssueFingerprint(req TaskIssueRequest, body, overflow string) string {
	labels := normalizedTaskIssueLabels(req.Labels)
	payload, _ := json.Marshal(struct {
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Overflow  string   `json:"overflow"`
		Labels    []string `json:"labels"`
		Milestone int      `json:"milestone"`
	}{
		Title:     req.Title,
		Body:      body,
		Overflow:  overflow,
		Labels:    labels,
		Milestone: req.Milestone,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func normalizedTaskIssueLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label != "" {
			out = append(out, label)
		}
	}
	slices.Sort(out)
	return out
}

func taskIssueLabelsFingerprint(labels []string) string {
	payload, _ := json.Marshal(normalizedTaskIssueLabels(labels))
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func taskIssueTextFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func parseIssueResult(ref string) (int, bool) {
	var number int
	if _, err := fmt.Sscanf(ref, "issue:%d", &number); err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}
