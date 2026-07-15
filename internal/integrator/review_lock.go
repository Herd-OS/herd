package integrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/platform"
)

const reviewLockMarkerPrefix = "<!-- herd:review-lock "
const reviewLockMarkerSuffix = " -->"
const reviewLockExpiry = 2 * time.Hour
const reviewLockMaxAttempts = 6

type reviewLockState struct {
	Kind           string     `json:"kind"`
	Version        int        `json:"version"`
	Status         string     `json:"status"`
	LockID         string     `json:"lock_id,omitempty"`
	PRNumber       int        `json:"pr_number"`
	BatchNumber    int        `json:"batch_number"`
	RunID          int64      `json:"run_id,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	BatchBranchSHA string     `json:"batch_branch_sha,omitempty"`
	AcquiredAt     *time.Time `json:"acquired_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ReleasedLockID string     `json:"released_lock_id,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`

	// BranchSHA is retained only so legacy diagnostic comments can still be
	// parsed and filtered from review context.
	BranchSHA string `json:"branch_sha,omitempty"`
}

type reviewLockHandle struct {
	branch string
	state  reviewLockState
}

type reviewLockRepository interface {
	CreateBranchWithCommit(ctx context.Context, name, parentSHA, message string) (string, error)
	CreateCommit(ctx context.Context, parentSHA, message string) (string, error)
	GetCommitMessage(ctx context.Context, sha string) (string, error)
	UpdateBranchToCommit(ctx context.Context, name, sha string, force bool) error
}

func acquireReviewLock(ctx context.Context, _ platform.IssueService, repoSvc platform.RepositoryService, prNumber int, batchNumber int, runID int64, lockFromSHA string, now time.Time) (*reviewLockHandle, bool, error) {
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return nil, false, fmt.Errorf("repository service does not support append-only review locks")
	}
	lockBranch := reviewLockBranch(prNumber)
	if err := ensureReviewLockBranch(ctx, repoSvc, repo, lockBranch, prNumber, batchNumber, lockFromSHA, now); err != nil {
		return nil, false, err
	}

	for attempt := 0; attempt < reviewLockMaxAttempts; attempt++ {
		headSHA, state, stateOK, err := readReviewLockHead(ctx, repoSvc, repo, lockBranch)
		if err != nil {
			return nil, false, err
		}
		if !stateOK || state.PRNumber != prNumber {
			return nil, false, nil
		}
		if reviewLockBlocksCurrentHead(state, lockFromSHA, now) {
			return nil, false, nil
		}

		lockID, err := newReviewLockToken()
		if err != nil {
			return nil, false, err
		}
		lockedState := lockedReviewLockState(prNumber, batchNumber, runID, lockFromSHA, lockID, now)
		message, err := buildReviewLockCommitMessage(lockedState)
		if err != nil {
			return nil, false, err
		}
		commitSHA, err := repo.CreateCommit(ctx, headSHA, message)
		if err != nil {
			return nil, false, fmt.Errorf("creating review lock marker commit for %s: %w", lockBranch, err)
		}
		if err := repo.UpdateBranchToCommit(ctx, lockBranch, commitSHA, false); err != nil {
			if platform.IsRefUpdateConflict(err) {
				continue
			}
			return nil, false, fmt.Errorf("updating review lock branch %s: %w", lockBranch, err)
		}
		return &reviewLockHandle{branch: lockBranch, state: lockedState}, true, nil
	}
	return nil, false, nil
}

func reviewLockBlocksCurrentHead(state reviewLockState, currentHeadSHA string, now time.Time) bool {
	if state.Status != "locked" {
		return false
	}
	if state.ExpiresAt != nil && !state.ExpiresAt.After(now) {
		return false
	}
	if state.ExpiresAt == nil {
		return true
	}
	if state.BatchBranchSHA == "" {
		return true
	}
	return state.BatchBranchSHA == currentHeadSHA
}

func releaseReviewLock(ctx context.Context, _ platform.IssueService, repoSvc platform.RepositoryService, h *reviewLockHandle) error {
	if h == nil || h.branch == "" || h.state.LockID == "" {
		return nil
	}
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return fmt.Errorf("repository service does not support append-only review locks")
	}
	for attempt := 0; attempt < reviewLockMaxAttempts; attempt++ {
		headSHA, state, stateOK, err := readReviewLockHead(ctx, repoSvc, repo, h.branch)
		if err != nil {
			if isNotFoundLikeError(err) {
				return nil
			}
			return err
		}
		if !stateOK || state.Status != "locked" || state.LockID != h.state.LockID {
			return nil
		}

		releasedAt := time.Now().UTC()
		unlockedState := reviewLockState{
			Kind:           "herd-review-lock",
			Version:        1,
			Status:         "unlocked",
			PRNumber:       state.PRNumber,
			BatchNumber:    state.BatchNumber,
			ReleasedLockID: h.state.LockID,
			ReleasedAt:     &releasedAt,
		}
		message, err := buildReviewLockCommitMessage(unlockedState)
		if err != nil {
			return err
		}
		commitSHA, err := repo.CreateCommit(ctx, headSHA, message)
		if err != nil {
			return fmt.Errorf("creating review unlock marker commit for %s: %w", h.branch, err)
		}
		if err := repo.UpdateBranchToCommit(ctx, h.branch, commitSHA, false); err != nil {
			if platform.IsRefUpdateConflict(err) {
				continue
			}
			return fmt.Errorf("updating review lock branch %s: %w", h.branch, err)
		}
		return nil
	}
	return nil
}

func describeReviewLock(ctx context.Context, repoSvc platform.RepositoryService, prNumber int) (reviewLockState, bool, error) {
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return reviewLockState{}, false, fmt.Errorf("repository service does not support append-only review locks")
	}
	_, state, stateOK, err := readReviewLockHead(ctx, repoSvc, repo, reviewLockBranch(prNumber))
	if err != nil {
		if isNotFoundLikeError(err) {
			return reviewLockState{}, false, nil
		}
		return reviewLockState{}, false, err
	}
	return state, stateOK, nil
}

func ensureReviewLockBranch(ctx context.Context, repoSvc platform.RepositoryService, repo reviewLockRepository, branch string, prNumber int, batchNumber int, lockFromSHA string, now time.Time) error {
	releasedAt := now.UTC()
	initialState := reviewLockState{
		Kind:        "herd-review-lock",
		Version:     1,
		Status:      "unlocked",
		PRNumber:    prNumber,
		BatchNumber: batchNumber,
		ReleasedAt:  &releasedAt,
	}
	message, err := buildReviewLockCommitMessage(initialState)
	if err != nil {
		return err
	}
	if _, err := repo.CreateBranchWithCommit(ctx, branch, lockFromSHA, message); err != nil {
		if isAlreadyExistsLikeError(err) {
			return nil
		}
		return fmt.Errorf("creating review lock branch %s: %w", branch, err)
	}
	_, err = repoSvc.GetBranchSHA(ctx, branch)
	if err != nil {
		return fmt.Errorf("validating review lock branch %s: %w", branch, err)
	}
	return nil
}

func readReviewLockHead(ctx context.Context, repoSvc platform.RepositoryService, repo reviewLockRepository, branch string) (string, reviewLockState, bool, error) {
	headSHA, err := repoSvc.GetBranchSHA(ctx, branch)
	if err != nil {
		return "", reviewLockState{}, false, fmt.Errorf("getting review lock branch %s: %w", branch, err)
	}
	message, err := repo.GetCommitMessage(ctx, headSHA)
	if err != nil {
		return "", reviewLockState{}, false, fmt.Errorf("getting review lock commit %s: %w", headSHA, err)
	}
	state, ok := parseReviewLockCommitMessage(message)
	return headSHA, state, ok, nil
}

func lockedReviewLockState(prNumber int, batchNumber int, runID int64, batchBranchSHA string, lockID string, now time.Time) reviewLockState {
	acquiredAt := now.UTC()
	expiresAt := now.Add(reviewLockExpiry).UTC()
	return reviewLockState{
		Kind:           "herd-review-lock",
		Version:        1,
		Status:         "locked",
		LockID:         lockID,
		PRNumber:       prNumber,
		BatchNumber:    batchNumber,
		RunID:          runID,
		Owner:          reviewLockOwner(batchNumber, runID),
		BatchBranchSHA: batchBranchSHA,
		AcquiredAt:     &acquiredAt,
		ExpiresAt:      &expiresAt,
	}
}

func parseReviewLockCommitMessage(message string) (reviewLockState, bool) {
	var state reviewLockState
	if err := json.Unmarshal([]byte(strings.TrimSpace(message)), &state); err != nil {
		return parseLegacyReviewLockCommitMessage(message)
	}
	if state.Kind != "herd-review-lock" || state.Version != 1 {
		return reviewLockState{}, false
	}
	switch state.Status {
	case "locked":
		if state.LockID == "" || state.PRNumber == 0 {
			return reviewLockState{}, false
		}
	case "unlocked":
		if state.PRNumber == 0 {
			return reviewLockState{}, false
		}
	default:
		return reviewLockState{}, false
	}
	return state, true
}

func parseLegacyReviewLockCommitMessage(message string) (reviewLockState, bool) {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "Herd review lock" {
		return reviewLockState{}, false
	}

	fields := make(map[string]string)
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			fields[key] = value
		}
	}

	prNumber, ok := parsePositiveReviewLockInt(fields["pr"])
	if !ok {
		return reviewLockState{}, false
	}
	acquiredAt, ok := parseReviewLockTime(fields["acquired_at"])
	if !ok {
		return reviewLockState{}, false
	}
	expiresAt := acquiredAt.Add(reviewLockExpiry).UTC()

	batchNumber, _ := parsePositiveReviewLockInt(fields["batch"])
	return reviewLockState{
		Kind:        "herd-review-lock",
		Version:     1,
		Status:      "locked",
		LockID:      fields["token"],
		PRNumber:    prNumber,
		BatchNumber: batchNumber,
		Owner:       fields["owner"],
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiresAt,
	}, true
}

func parsePositiveReviewLockInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func parseReviewLockTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
	}
	for _, format := range formats {
		if parsed, err := time.Parse(format, raw); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func buildReviewLockCommitMessage(state reviewLockState) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshaling review lock state: %w", err)
	}
	return string(data), nil
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

func newReviewLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating review lock token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
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
	return strings.Contains(msg, "409") ||
		strings.Contains(msg, "422") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "reference already exists")
}
