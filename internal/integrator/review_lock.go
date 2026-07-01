package integrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	state     reviewLockState
}

func acquireReviewLock(ctx context.Context, issueSvc platform.IssueService, prNumber int, batchNumber int, runID int64, now time.Time) (*reviewLockHandle, bool, error) {
	comments, err := issueSvc.ListComments(ctx, prNumber)
	if err != nil {
		return nil, false, fmt.Errorf("listing review lock comments for PR #%d: %w", prNumber, err)
	}
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
		return nil, false, fmt.Errorf("creating review lock comment for PR #%d: %w", prNumber, err)
	}

	comments, err = issueSvc.ListComments(ctx, prNumber)
	if err != nil {
		_ = issueSvc.DeleteComment(ctx, commentID)
		return nil, false, fmt.Errorf("listing review lock comments after create for PR #%d: %w", prNumber, err)
	}
	active := activeReviewLocks(comments, prNumber, now)
	if len(active) == 0 || active[0].commentID != commentID {
		if err := issueSvc.DeleteComment(ctx, commentID); err != nil && !isNotFoundLikeError(err) {
			return nil, false, fmt.Errorf("deleting duplicate review lock comment %d: %w", commentID, err)
		}
		return nil, false, nil
	}

	return &reviewLockHandle{commentID: commentID, state: state}, true, nil
}

func releaseReviewLock(ctx context.Context, issueSvc platform.IssueService, h *reviewLockHandle) error {
	if h == nil || h.commentID == 0 {
		return nil
	}
	if err := issueSvc.DeleteComment(ctx, h.commentID); err != nil {
		if isNotFoundLikeError(err) {
			return nil
		}
		return fmt.Errorf("deleting review lock comment %d: %w", h.commentID, err)
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

func activeReviewLocks(comments []*platform.Comment, prNumber int, now time.Time) []*reviewLockHandle {
	var active []*reviewLockHandle
	for _, c := range comments {
		state, ok := parseReviewLockComment(c.Body)
		if !ok || state.PRNumber != prNumber || !state.ExpiresAt.After(now) {
			continue
		}
		active = append(active, &reviewLockHandle{commentID: c.ID, state: state})
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].state.AcquiredAt.Equal(active[j].state.AcquiredAt) {
			return active[i].commentID < active[j].commentID
		}
		return active[i].state.AcquiredAt.Before(active[j].state.AcquiredAt)
	})
	return active
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
