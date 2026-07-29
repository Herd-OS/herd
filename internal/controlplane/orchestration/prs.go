package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

// OpenBatchPRRequest describes an idempotent batch PR creation/update.
type OpenBatchPRRequest struct {
	BatchNumber int
	Title       string
	Body        string
	Head        string
	Base        string
}

// OpenBatchPR opens a batch PR idempotently using repo_id + batch_number.
// Existing open PRs for the batch head are updated instead of duplicated.
func (s Service) OpenBatchPR(ctx context.Context, req OpenBatchPRRequest) (*platform.PullRequest, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if req.BatchNumber <= 0 {
		return nil, fmt.Errorf("batch number is required")
	}
	if req.Head == "" || req.Base == "" {
		return nil, fmt.Errorf("head and base branches are required")
	}
	key := idempotencyKey("batch-pr", "repo", s.Repo.ID, "batch", req.BatchNumber)
	var existing []*platform.PullRequest
	resultRef, err := s.withIdempotencyPhased(ctx, key, "pull_request_create", func() error {
		var err error
		existing, err = s.Platform.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: req.Head})
		return err
	}, nil, func() (string, error) {
		if len(existing) > 0 {
			title := req.Title
			body := req.Body
			updated, err := s.Platform.PullRequests().Update(ctx, existing[0].Number, &title, &body)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("pr:%d", updated.Number), nil
		}
		created, err := s.Platform.PullRequests().Create(ctx, req.Title, req.Body, req.Head, req.Base)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("pr:%d", created.Number), nil
	})
	if err != nil {
		return nil, err
	}
	prNumber, ok := parsePRResult(resultRef)
	if !ok {
		return nil, fmt.Errorf("invalid PR result ref %q", resultRef)
	}
	pr, err := s.Platform.PullRequests().Get(ctx, prNumber)
	if err != nil {
		return nil, err
	}
	if pr.Head != "" && pr.Head != req.Head {
		return nil, fmt.Errorf("batch PR #%d head mismatch: expected %s, got %s", pr.Number, req.Head, pr.Head)
	}
	if pr.Title != req.Title || pr.Body != req.Body {
		title := req.Title
		body := req.Body
		updateKey := idempotencyKey("batch-pr-update", "repo", s.Repo.ID, "batch", req.BatchNumber, "pr", pr.Number, batchPRContentFingerprint(req.Title, req.Body))
		if err := s.mutate(ctx, updateKey, "pull_request_update", func() (string, error) {
			updated, err := s.Platform.PullRequests().Update(ctx, pr.Number, &title, &body)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("pr:%d", updated.Number), nil
		}); err != nil {
			return nil, err
		}
		return s.Platform.PullRequests().Get(ctx, pr.Number)
	}
	return pr, nil
}

func batchPRContentFingerprint(title, body string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

// BranchOperationRequest describes an idempotent branch mutation.
type BranchOperationRequest struct {
	OperationKind   string
	BranchName      string
	FromSHA         string
	NewSHA          string
	ExpectedHeadSHA string
	Force           bool
}

type branchUpdater interface {
	UpdateBranchToCommit(ctx context.Context, name, sha string, force bool) error
}

// ApplyBranchOperation performs create/update/delete keyed by repo, branch,
// guarded head/source SHA, and operation kind.
func (s Service) ApplyBranchOperation(ctx context.Context, req BranchOperationRequest) error {
	if err := s.validate(); err != nil {
		return err
	}
	if req.BranchName == "" {
		return fmt.Errorf("branch name is required")
	}
	if req.OperationKind == "" {
		return fmt.Errorf("operation kind is required")
	}
	if (req.OperationKind == "delete" || req.OperationKind == "update") && req.ExpectedHeadSHA == "" {
		return fmt.Errorf("expected head SHA is required for branch %s", req.OperationKind)
	}
	identitySHA := req.ExpectedHeadSHA
	if req.OperationKind == "create" {
		identitySHA = req.FromSHA
	}
	key := idempotencyKey("branch", "repo", s.Repo.ID, req.BranchName, identitySHA, req.OperationKind)
	preflight := func() error {
		if req.OperationKind == "create" {
			current, err := s.Platform.Repository().GetBranchSHA(ctx, req.BranchName)
			if err != nil {
				if platform.IsNotFound(err) {
					return nil
				}
				return mutations.PreCallError{Err: err}
			}
			if current != req.FromSHA {
				return fmt.Errorf("branch %s already exists at %s, expected create source %s", req.BranchName, current, req.FromSHA)
			}
			return nil
		}
		if req.ExpectedHeadSHA != "" {
			current, err := s.Platform.Repository().GetBranchSHA(ctx, req.BranchName)
			if err != nil && req.OperationKind != "create" {
				return err
			}
			if err == nil && current != req.ExpectedHeadSHA {
				return fmt.Errorf("branch %s head mismatch: expected %s, got %s", req.BranchName, req.ExpectedHeadSHA, current)
			}
		}
		return nil
	}
	_, err := s.withIdempotencyPhased(ctx, key, "branch_"+req.OperationKind, preflight, nil, func() (string, error) {
		switch req.OperationKind {
		case "create":
			if req.FromSHA == "" {
				return "", fmt.Errorf("from SHA is required for branch create")
			}
			current, err := s.Platform.Repository().GetBranchSHA(ctx, req.BranchName)
			if err == nil {
				if current != req.FromSHA {
					return "", fmt.Errorf("branch %s already exists at %s, expected create source %s", req.BranchName, current, req.FromSHA)
				}
				return "branch:" + req.BranchName, nil
			}
			if !platform.IsNotFound(err) {
				return "", mutations.PreCallError{Err: err}
			}
			return "branch:" + req.BranchName, s.Platform.Repository().CreateBranch(ctx, req.BranchName, req.FromSHA)
		case "update":
			if req.NewSHA == "" {
				return "", fmt.Errorf("new SHA is required for branch update")
			}
			updater, ok := s.Platform.Repository().(branchUpdater)
			if !ok {
				return "", fmt.Errorf("repository client does not support branch update")
			}
			return "branch:" + req.BranchName, updater.UpdateBranchToCommit(ctx, req.BranchName, req.NewSHA, req.Force)
		case "delete":
			return "branch:" + req.BranchName, s.Platform.Repository().DeleteBranch(ctx, req.BranchName)
		default:
			return "", fmt.Errorf("unsupported branch operation %q", req.OperationKind)
		}
	})
	return err
}

// MergePRRequest describes a guarded merge.
type MergePRRequest struct {
	PRNumber        int
	ExpectedHeadSHA string
	Method          platform.MergeMethod
	RequireCI       bool
}

// MergePR verifies PR state, branch head, and optional CI status before merge.
func (s Service) MergePR(ctx context.Context, req MergePRRequest) (*platform.MergeResult, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if req.PRNumber <= 0 {
		return nil, fmt.Errorf("PR number is required")
	}
	if req.ExpectedHeadSHA == "" {
		return nil, fmt.Errorf("expected head SHA is required")
	}
	key := idempotencyKey("merge", "repo", s.Repo.ID, "pr", req.PRNumber, "head", req.ExpectedHeadSHA)
	var method platform.MergeMethod
	resultRef, err := s.withIdempotencyPhased(ctx, key, "pull_request_merge", func() error {
		pr, err := s.Platform.PullRequests().Get(ctx, req.PRNumber)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			return fmt.Errorf("PR #%d is %s", req.PRNumber, pr.State)
		}
		current, err := s.Platform.Repository().GetBranchSHA(ctx, pr.Head)
		if err != nil {
			return err
		}
		if current != req.ExpectedHeadSHA {
			return fmt.Errorf("PR #%d head mismatch: expected %s, got %s", req.PRNumber, req.ExpectedHeadSHA, current)
		}
		if req.RequireCI {
			status, err := s.Platform.Checks().GetCombinedStatus(ctx, req.ExpectedHeadSHA)
			if err != nil {
				return err
			}
			if status != "success" {
				return fmt.Errorf("PR #%d CI status is %s", req.PRNumber, status)
			}
		}
		method = req.Method
		if method == "" {
			method = platform.MergeMethodMerge
		}
		return nil
	}, nil, func() (string, error) {
		merged, err := s.Platform.PullRequests().Merge(ctx, req.PRNumber, method)
		if err != nil {
			return "", err
		}
		return "merge:" + merged.SHA, nil
	})
	if err != nil {
		return nil, err
	}
	return &platform.MergeResult{SHA: strings.TrimPrefix(resultRef, "merge:"), Merged: true}, nil
}

// CleanupClosedBatchPR closes milestone issues and deletes the batch branch
// after a batch PR closes.
func (s Service) CleanupClosedBatchPR(ctx context.Context, prNumber int, merged bool) error {
	if err := s.validate(); err != nil {
		return err
	}
	pr, err := s.Platform.PullRequests().Get(ctx, prNumber)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(pr.Title, "[herd]") || pr.State != "closed" {
		return nil
	}
	msNumber, err := ParseBatchBranchMilestone(pr.Head)
	if err != nil {
		return nil
	}
	openIssues, err := s.Platform.Issues().List(ctx, platform.IssueFilters{State: "open", Milestone: &msNumber})
	if err != nil {
		return err
	}
	closed := "closed"
	var cleanupErrs []error
	for _, iss := range openIssues {
		if !merged && !issues.HasLabel(iss.Labels, issues.StatusDone) {
			if status := issues.StatusLabel(iss.Labels); status != "" {
				key := idempotencyKey("batch-cleanup", "repo", s.Repo.ID, "batch", msNumber, "issue", iss.Number, "label", status, "remove")
				if err := s.mutate(ctx, key, "batch_cleanup_issue_label_remove", func() (string, error) {
					return "", s.Platform.Issues().RemoveLabels(ctx, iss.Number, []string{status})
				}); err != nil {
					cleanupErrs = append(cleanupErrs, fmt.Errorf("remove issue %d status label: %w", iss.Number, err))
				}
			}
			key := idempotencyKey("batch-cleanup", "repo", s.Repo.ID, "batch", msNumber, "issue", iss.Number, "label", issues.StatusCancelled, "add")
			if err := s.mutate(ctx, key, "batch_cleanup_issue_label_add", func() (string, error) {
				return "", s.Platform.Issues().AddLabels(ctx, iss.Number, []string{issues.StatusCancelled})
			}); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("add issue %d cancelled label: %w", iss.Number, err))
			}
		}
		key := idempotencyKey("batch-cleanup", "repo", s.Repo.ID, "batch", msNumber, "issue", iss.Number, "close")
		if err := s.mutate(ctx, key, "batch_cleanup_issue_close", func() (string, error) {
			_, err := s.Platform.Issues().Update(ctx, iss.Number, platform.IssueUpdate{State: &closed})
			return "", err
		}); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("close issue %d: %w", iss.Number, err))
		}
	}
	key := idempotencyKey("batch-cleanup", "repo", s.Repo.ID, "batch", msNumber, "milestone", "close")
	if err := s.mutate(ctx, key, "batch_cleanup_milestone_close", func() (string, error) {
		_, err := s.Platform.Milestones().Update(ctx, msNumber, platform.MilestoneUpdate{State: &closed})
		return "", err
	}); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("close milestone %d: %w", msNumber, err))
	}
	if strings.TrimSpace(pr.HeadSHA) == "" {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("closed PR #%d head SHA is required for branch cleanup", prNumber))
		return errors.Join(cleanupErrs...)
	}
	if err := s.ApplyBranchOperation(ctx, BranchOperationRequest{
		OperationKind:   "delete",
		BranchName:      pr.Head,
		ExpectedHeadSHA: pr.HeadSHA,
	}); err != nil {
		cleanupErrs = append(cleanupErrs, err)
	}
	return errors.Join(cleanupErrs...)
}

// MergeApprovedBatchPR is the hosted-service equivalent of local approved PR
// merging and cleanup.
func (s Service) MergeApprovedBatchPR(ctx context.Context, prNumber int, expectedHeadSHA string, cfg *config.Config) (*platform.MergeResult, error) {
	method := platform.MergeMethodMerge
	requireCI := false
	if cfg != nil {
		method = platform.MergeMethod(cfg.Integrator.Strategy)
		requireCI = cfg.Integrator.RequireCI
	}
	result, err := s.MergePR(ctx, MergePRRequest{PRNumber: prNumber, ExpectedHeadSHA: expectedHeadSHA, Method: method, RequireCI: requireCI})
	if err != nil {
		return nil, err
	}
	if err := s.CleanupClosedBatchPR(ctx, prNumber, true); err != nil {
		return nil, err
	}
	return result, nil
}

func ParseBatchBranchMilestone(branch string) (int, error) {
	parts := strings.TrimPrefix(branch, "herd/batch/")
	if parts == branch {
		return 0, fmt.Errorf("not a batch branch: %s", branch)
	}
	idx := strings.Index(parts, "-")
	if idx < 0 {
		return 0, fmt.Errorf("invalid batch branch format: %s", branch)
	}
	var number int
	if _, err := fmt.Sscanf(parts[:idx], "%d", &number); err != nil {
		return 0, err
	}
	return number, nil
}

func parsePRResult(ref string) (int, bool) {
	var number int
	if _, err := fmt.Sscanf(ref, "pr:%d", &number); err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}
