package integrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/platform"
)

const reviewLockMarkerPrefix = "<!-- herd:review-lock "
const reviewLockMarkerSuffix = " -->"
const reviewLockExpiry = 2 * time.Hour

type reviewLockState struct {
	PRNumber    int       `json:"pr_number"`
	BatchNumber int       `json:"batch_number"`
	RunID       int64     `json:"run_id,omitempty"`
	Owner       string    `json:"owner"`
	AcquiredAt  time.Time `json:"acquired_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type reviewLockHandle struct {
	commentID int64
	branch    string
	state     reviewLockState
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
		if err := issueSvc.DeleteComment(ctx, c.ID); err != nil && !isNotFoundLikeError(err) {
			return nil, false, fmt.Errorf("deleting stale review lock comment %d: %w", c.ID, err)
		}
		if err := repoSvc.DeleteBranch(ctx, lockBranch); err != nil && !isNotFoundLikeError(err) {
			return nil, false, fmt.Errorf("deleting stale review lock branch %s: %w", lockBranch, err)
		}
	}

	if err := repoSvc.CreateBranch(ctx, lockBranch, lockFromSHA); err != nil {
		if isAlreadyExistsLikeError(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("creating review lock branch %s: %w", lockBranch, err)
	}

	state := reviewLockState{
		PRNumber:    prNumber,
		BatchNumber: batchNumber,
		RunID:       runID,
		Owner:       reviewLockOwner(batchNumber, runID),
		AcquiredAt:  now.UTC(),
		ExpiresAt:   now.Add(reviewLockExpiry).UTC(),
	}
	body, err := buildReviewLockComment(state)
	if err != nil {
		return nil, false, err
	}
	commentID, err := issueSvc.AddCommentReturningID(ctx, prNumber, body)
	if err != nil {
		_ = repoSvc.DeleteBranch(ctx, lockBranch)
		return nil, false, fmt.Errorf("creating review lock comment for PR #%d: %w", prNumber, err)
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
	if h.branch == "" {
		return nil
	}
	if err := repoSvc.DeleteBranch(ctx, h.branch); err != nil {
		if isNotFoundLikeError(err) {
			return nil
		}
		return fmt.Errorf("deleting review lock branch %s: %w", h.branch, err)
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
