package integrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/platform"
)

const reviewLockMarkerPrefix = "<!-- herd:review-lock "
const reviewLockMarkerSuffix = " -->"
const reviewLockExpiry = 2 * time.Hour
const reviewLockOrphanBranchGrace = 10 * time.Minute

type reviewLockState struct {
	PRNumber    int       `json:"pr_number"`
	BatchNumber int       `json:"batch_number"`
	RunID       int64     `json:"run_id,omitempty"`
	Owner       string    `json:"owner"`
	BranchSHA   string    `json:"branch_sha,omitempty"`
	AcquiredAt  time.Time `json:"acquired_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type reviewLockHandle struct {
	commentID int64
	branch    string
	state     reviewLockState
}

type reviewLockCommitRepository interface {
	CreateBranchWithCommit(ctx context.Context, name, parentSHA, message string) (string, error)
}

type reviewLockCompareDeleteRepository interface {
	DeleteBranchIfSHA(ctx context.Context, name, expectedSHA string) (bool, error)
}

func acquireReviewLock(ctx context.Context, issueSvc platform.IssueService, repoSvc platform.RepositoryService, prNumber int, batchNumber int, runID int64, lockFromSHA string, now time.Time) (*reviewLockHandle, bool, error) {
	comments, err := issueSvc.ListComments(ctx, prNumber)
	if err != nil {
		return nil, false, fmt.Errorf("listing review lock comments for PR #%d: %w", prNumber, err)
	}
	lockBranch := reviewLockBranch(prNumber)
	for _, c := range comments {
		state, ok := parseReviewLockComment(c.Body)
		if !ok || state.PRNumber != prNumber {
			continue
		}
		if state.ExpiresAt.After(now) {
			return nil, false, nil
		}
		if state.BranchSHA == "" {
			if err := issueSvc.DeleteComment(ctx, c.ID); err != nil && !isNotFoundLikeError(err) {
				return nil, false, fmt.Errorf("deleting stale review lock comment %d: %w", c.ID, err)
			}
			continue
		}
		if ok, err := deleteReviewLockBranchIfCurrent(ctx, repoSvc, lockBranch, state.BranchSHA); err != nil {
			return nil, false, fmt.Errorf("deleting stale review lock branch %s: %w", lockBranch, err)
		} else if !ok {
			return nil, false, nil
		}
		if err := issueSvc.DeleteComment(ctx, c.ID); err != nil && !isNotFoundLikeError(err) {
			return nil, false, fmt.Errorf("deleting stale review lock comment %d: %w", c.ID, err)
		}
	}

	owner := reviewLockOwner(batchNumber, runID)
	lockToken, err := newReviewLockToken()
	if err != nil {
		return nil, false, err
	}
	lockCommitMessage := fmt.Sprintf("Herd review lock\n\npr: %d\nbatch: %d\nowner: %s\nacquired_at: %s\ntoken: %s\n", prNumber, batchNumber, owner, now.UTC().Format(time.RFC3339Nano), lockToken)
	lockSHA, err := createReviewLockBranch(ctx, repoSvc, lockBranch, lockFromSHA, lockCommitMessage)
	if err != nil {
		if isAlreadyExistsLikeError(err) {
			if err := createOrphanedReviewLockBranchComment(ctx, issueSvc, repoSvc, prNumber, batchNumber, lockBranch, now); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("creating review lock branch %s: %w", lockBranch, err)
	}

	state := reviewLockState{
		PRNumber:    prNumber,
		BatchNumber: batchNumber,
		RunID:       runID,
		Owner:       owner,
		BranchSHA:   lockSHA,
		AcquiredAt:  now.UTC(),
		ExpiresAt:   now.Add(reviewLockExpiry).UTC(),
	}
	body, err := buildReviewLockComment(state)
	if err != nil {
		return nil, false, err
	}
	commentID, err := issueSvc.AddCommentReturningID(ctx, prNumber, body)
	if err != nil {
		_, _ = deleteReviewLockBranchIfCurrent(ctx, repoSvc, lockBranch, lockSHA)
		return nil, false, fmt.Errorf("creating review lock comment for PR #%d: %w", prNumber, err)
	}
	currentSHA, err := repoSvc.GetBranchSHA(ctx, lockBranch)
	if err != nil {
		_ = issueSvc.DeleteComment(ctx, commentID)
		if isNotFoundLikeError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("validating review lock branch %s: %w", lockBranch, err)
	}
	if currentSHA != lockSHA {
		_ = issueSvc.DeleteComment(ctx, commentID)
		return nil, false, nil
	}
	return &reviewLockHandle{commentID: commentID, branch: lockBranch, state: state}, true, nil
}

func releaseReviewLock(ctx context.Context, issueSvc platform.IssueService, repoSvc platform.RepositoryService, h *reviewLockHandle) error {
	if h == nil {
		return nil
	}
	if h.commentID != 0 {
		if err := issueSvc.DeleteComment(ctx, h.commentID); err != nil {
			if !isNotFoundLikeError(err) {
				return fmt.Errorf("deleting review lock comment %d: %w", h.commentID, err)
			}
		}
	}
	if h.branch != "" {
		if _, err := deleteReviewLockBranchIfCurrent(ctx, repoSvc, h.branch, h.state.BranchSHA); err != nil {
			return fmt.Errorf("deleting review lock branch %s: %w", h.branch, err)
		}
	}
	if h.state.BranchSHA != "" && h.state.PRNumber != 0 {
		if err := deleteReviewLockCommentsForBranchSHA(ctx, issueSvc, h.state.PRNumber, h.state.BranchSHA, h.commentID); err != nil {
			return err
		}
	}
	return nil
}

func deleteReviewLockCommentsForBranchSHA(ctx context.Context, issueSvc platform.IssueService, prNumber int, branchSHA string, skipCommentID int64) error {
	comments, err := issueSvc.ListComments(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("listing review lock comments for PR #%d: %w", prNumber, err)
	}
	for _, c := range comments {
		if c.ID == skipCommentID {
			continue
		}
		state, ok := parseReviewLockComment(c.Body)
		if !ok || state.PRNumber != prNumber || state.BranchSHA != branchSHA {
			continue
		}
		if err := issueSvc.DeleteComment(ctx, c.ID); err != nil && !isNotFoundLikeError(err) {
			return fmt.Errorf("deleting review lock comment %d: %w", c.ID, err)
		}
	}
	return nil
}

func parseReviewLockComment(body string) (reviewLockState, bool) {
	start := strings.Index(body, reviewLockMarkerPrefix)
	if start < 0 {
		return reviewLockState{}, false
	}
	start += len(reviewLockMarkerPrefix)
	end := strings.Index(body[start:], reviewLockMarkerSuffix)
	if end < 0 {
		return reviewLockState{}, false
	}
	raw := body[start : start+end]
	var state reviewLockState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return reviewLockState{}, false
	}
	return state, true
}

func buildReviewLockComment(state reviewLockState) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshaling review lock state: %w", err)
	}
	return reviewLockMarkerPrefix + string(data) + reviewLockMarkerSuffix, nil
}

func reviewLockOwner(batchNumber int, runID int64) string {
	if runID > 0 {
		return fmt.Sprintf("batch-%d-run-%d", batchNumber, runID)
	}
	return fmt.Sprintf("batch-%d", batchNumber)
}

func reviewLockBranch(prNumber int) string {
	return fmt.Sprintf("herd/review-lock/pr-%d", prNumber)
}

func createReviewLockBranch(ctx context.Context, repoSvc platform.RepositoryService, branch string, lockFromSHA string, message string) (string, error) {
	if repo, ok := repoSvc.(reviewLockCommitRepository); ok {
		return repo.CreateBranchWithCommit(ctx, branch, lockFromSHA, message)
	}
	return "", fmt.Errorf("repository service does not support review lock marker commits")
}

func newReviewLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating review lock token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func createOrphanedReviewLockBranchComment(ctx context.Context, issueSvc platform.IssueService, repoSvc platform.RepositoryService, prNumber int, batchNumber int, lockBranch string, now time.Time) error {
	branchSHA, err := repoSvc.GetBranchSHA(ctx, lockBranch)
	if err != nil {
		if isNotFoundLikeError(err) {
			return nil
		}
		return fmt.Errorf("getting orphaned review lock branch SHA for %s: %w", lockBranch, err)
	}
	body, err := buildReviewLockComment(reviewLockState{
		PRNumber:    prNumber,
		BatchNumber: batchNumber,
		Owner:       "orphaned-branch",
		BranchSHA:   branchSHA,
		AcquiredAt:  now.UTC(),
		ExpiresAt:   now.Add(reviewLockOrphanBranchGrace).UTC(),
	})
	if err != nil {
		return err
	}
	if _, err := issueSvc.AddCommentReturningID(ctx, prNumber, body); err != nil {
		return fmt.Errorf("creating orphaned review lock branch comment for PR #%d: %w", prNumber, err)
	}
	currentSHA, err := repoSvc.GetBranchSHA(ctx, lockBranch)
	if err != nil {
		if isNotFoundLikeError(err) {
			_ = deleteReviewLockCommentsForBranchSHA(ctx, issueSvc, prNumber, branchSHA, 0)
			return nil
		}
		return fmt.Errorf("validating orphaned review lock branch %s: %w", lockBranch, err)
	}
	if currentSHA != branchSHA {
		_ = deleteReviewLockCommentsForBranchSHA(ctx, issueSvc, prNumber, branchSHA, 0)
	}
	return nil
}

func deleteReviewLockBranchIfCurrent(ctx context.Context, repoSvc platform.RepositoryService, branch string, expectedSHA string) (bool, error) {
	if expectedSHA == "" {
		if _, err := repoSvc.GetBranchSHA(ctx, branch); err != nil {
			if isNotFoundLikeError(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	}
	repo, ok := repoSvc.(reviewLockCompareDeleteRepository)
	if !ok {
		return false, fmt.Errorf("repository service does not support leased review lock branch deletion")
	}
	return repo.DeleteBranchIfSHA(ctx, branch, expectedSHA)
}

func filterReviewLockComments(comments []*platform.Comment) []*platform.Comment {
	filtered := comments[:0]
	for _, c := range comments {
		if _, ok := parseReviewLockComment(c.Body); ok {
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered
}

func isNotFoundLikeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "404") || strings.Contains(msg, "not found")
}

func isAlreadyExistsLikeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "422") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "reference already exists")
}
