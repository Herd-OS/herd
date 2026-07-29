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

const batchLockExpiry = 2 * time.Hour
const batchLockMaxAttempts = 6

type BatchLockState struct {
	Kind           string     `json:"kind"`
	Version        int        `json:"version"`
	Status         string     `json:"status"`
	LockID         string     `json:"lock_id,omitempty"`
	BatchNumber    int        `json:"batch_number"`
	BatchBranch    string     `json:"batch_branch,omitempty"`
	RunID          int64      `json:"run_id,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	AcquiredAt     *time.Time `json:"acquired_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ReleasedLockID string     `json:"released_lock_id,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
}

type BatchLockHandle struct {
	branch string
	state  BatchLockState
}

func BatchLockBranch(batchNumber int) string {
	return fmt.Sprintf("herd/locks/batch/%d", batchNumber)
}

func AcquireBatchLock(ctx context.Context, repoSvc platform.RepositoryService, batchNumber int, batchBranch string, runID int64, now time.Time) (*BatchLockHandle, bool, error) {
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return nil, false, fmt.Errorf("repository service does not support append-only batch locks")
	}
	lockBranch := BatchLockBranch(batchNumber)
	if err := ensureBatchLockBranch(ctx, repoSvc, repo, lockBranch, batchNumber, batchBranch, now); err != nil {
		return nil, false, err
	}

	for attempt := 0; attempt < batchLockMaxAttempts; attempt++ {
		headSHA, state, stateOK, err := readBatchLockHead(ctx, repoSvc, repo, lockBranch)
		if err != nil {
			return nil, false, err
		}
		if !stateOK || state.BatchNumber != batchNumber {
			return nil, false, fmt.Errorf("batch lock branch %s has malformed or mismatched state", lockBranch)
		}
		if IsBatchLockActive(state, now) {
			return nil, false, nil
		}

		lockID, err := newBatchLockToken()
		if err != nil {
			return nil, false, err
		}
		lockedState := lockedBatchLockState(batchNumber, batchBranch, runID, lockID, now)
		message, err := buildBatchLockCommitMessage(lockedState)
		if err != nil {
			return nil, false, err
		}
		commitSHA, err := repo.CreateCommit(ctx, headSHA, message)
		if err != nil {
			return nil, false, fmt.Errorf("creating batch lock marker commit for %s: %w", lockBranch, err)
		}
		if err := repo.UpdateBranchToCommit(ctx, lockBranch, commitSHA, false); err != nil {
			if platform.IsRefUpdateConflict(err) {
				continue
			}
			return nil, false, fmt.Errorf("updating batch lock branch %s: %w", lockBranch, err)
		}
		return &BatchLockHandle{branch: lockBranch, state: lockedState}, true, nil
	}
	return nil, false, fmt.Errorf("acquiring batch lock %s: exceeded retry attempts", lockBranch)
}

func ReleaseBatchLock(ctx context.Context, repoSvc platform.RepositoryService, h *BatchLockHandle) error {
	if h == nil || h.branch == "" || h.state.LockID == "" {
		return nil
	}
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return fmt.Errorf("repository service does not support append-only batch locks")
	}
	for attempt := 0; attempt < batchLockMaxAttempts; attempt++ {
		headSHA, state, stateOK, err := readBatchLockHead(ctx, repoSvc, repo, h.branch)
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
		unlockedState := BatchLockState{
			Kind:           "herd-batch-lock",
			Version:        1,
			Status:         "unlocked",
			BatchNumber:    state.BatchNumber,
			BatchBranch:    state.BatchBranch,
			ReleasedLockID: h.state.LockID,
			ReleasedAt:     &releasedAt,
		}
		message, err := buildBatchLockCommitMessage(unlockedState)
		if err != nil {
			return err
		}
		commitSHA, err := repo.CreateCommit(ctx, headSHA, message)
		if err != nil {
			return fmt.Errorf("creating batch unlock marker commit for %s: %w", h.branch, err)
		}
		if err := repo.UpdateBranchToCommit(ctx, h.branch, commitSHA, false); err != nil {
			if platform.IsRefUpdateConflict(err) {
				continue
			}
			return fmt.Errorf("updating batch lock branch %s: %w", h.branch, err)
		}
		return nil
	}
	return fmt.Errorf("releasing batch lock %s: exceeded retry attempts", h.branch)
}

func DescribeBatchLock(ctx context.Context, repoSvc platform.RepositoryService, batchNumber int) (BatchLockState, bool, error) {
	repo, ok := repoSvc.(reviewLockRepository)
	if !ok {
		return BatchLockState{}, false, fmt.Errorf("repository service does not support append-only batch locks")
	}
	_, state, stateOK, err := readBatchLockHead(ctx, repoSvc, repo, BatchLockBranch(batchNumber))
	if err != nil {
		if isNotFoundLikeError(err) {
			return BatchLockState{}, false, nil
		}
		return BatchLockState{}, false, err
	}
	return state, stateOK, nil
}

func IsBatchLockActive(state BatchLockState, now time.Time) bool {
	if state.Status != "locked" {
		return false
	}
	if state.ExpiresAt == nil {
		return true
	}
	return state.ExpiresAt.After(now)
}

func timeNowUTC() time.Time {
	return time.Now().UTC()
}

func releaseBatchLockDeferred(p platform.Platform, handle *BatchLockHandle, batchNumber int) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ReleaseBatchLock(releaseCtx, p.Repository(), handle); err != nil {
		fmt.Printf("Warning: failed to release batch lock for batch #%d: %s\n", batchNumber, err)
	}
}

func ensureBatchLockBranch(ctx context.Context, repoSvc platform.RepositoryService, repo reviewLockRepository, branch string, batchNumber int, batchBranch string, now time.Time) error {
	parentSHA, err := repoSvc.GetBranchSHA(ctx, batchBranch)
	if err != nil {
		return fmt.Errorf("getting batch branch SHA for lock branch %s: %w", branch, err)
	}
	releasedAt := now.UTC()
	initialState := BatchLockState{
		Kind:        "herd-batch-lock",
		Version:     1,
		Status:      "unlocked",
		BatchNumber: batchNumber,
		BatchBranch: batchBranch,
		ReleasedAt:  &releasedAt,
	}
	message, err := buildBatchLockCommitMessage(initialState)
	if err != nil {
		return err
	}
	if _, err := repo.CreateBranchWithCommit(ctx, branch, parentSHA, message); err != nil {
		if isAlreadyExistsLikeError(err) {
			return nil
		}
		return fmt.Errorf("creating batch lock branch %s: %w", branch, err)
	}
	_, err = repoSvc.GetBranchSHA(ctx, branch)
	if err != nil {
		return fmt.Errorf("validating batch lock branch %s: %w", branch, err)
	}
	return nil
}

func readBatchLockHead(ctx context.Context, repoSvc platform.RepositoryService, repo reviewLockRepository, branch string) (string, BatchLockState, bool, error) {
	headSHA, err := repoSvc.GetBranchSHA(ctx, branch)
	if err != nil {
		return "", BatchLockState{}, false, fmt.Errorf("getting batch lock branch %s: %w", branch, err)
	}
	message, err := repo.GetCommitMessage(ctx, headSHA)
	if err != nil {
		return "", BatchLockState{}, false, fmt.Errorf("getting batch lock commit %s: %w", headSHA, err)
	}
	state, ok := parseBatchLockCommitMessage(message)
	return headSHA, state, ok, nil
}

func lockedBatchLockState(batchNumber int, batchBranch string, runID int64, lockID string, now time.Time) BatchLockState {
	acquiredAt := now.UTC()
	expiresAt := now.Add(batchLockExpiry).UTC()
	return BatchLockState{
		Kind:        "herd-batch-lock",
		Version:     1,
		Status:      "locked",
		LockID:      lockID,
		BatchNumber: batchNumber,
		BatchBranch: batchBranch,
		RunID:       runID,
		Owner:       batchLockOwner(batchNumber, runID),
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiresAt,
	}
}

func parseBatchLockCommitMessage(message string) (BatchLockState, bool) {
	var state BatchLockState
	if err := json.Unmarshal([]byte(strings.TrimSpace(message)), &state); err != nil {
		return BatchLockState{}, false
	}
	if state.Kind != "herd-batch-lock" || state.Version != 1 {
		return BatchLockState{}, false
	}
	switch state.Status {
	case "locked":
		if state.LockID == "" || state.BatchNumber <= 0 {
			return BatchLockState{}, false
		}
	case "unlocked":
		if state.BatchNumber <= 0 {
			return BatchLockState{}, false
		}
	default:
		return BatchLockState{}, false
	}
	return state, true
}

func buildBatchLockCommitMessage(state BatchLockState) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshaling batch lock state: %w", err)
	}
	return string(data), nil
}

func batchLockOwner(batchNumber int, runID int64) string {
	if runID > 0 {
		return fmt.Sprintf("batch-%d-run-%d", batchNumber, runID)
	}
	return fmt.Sprintf("batch-%d", batchNumber)
}

func newBatchLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating batch lock token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func logActiveBatchLock(ctx context.Context, repoSvc platform.RepositoryService, batchNumber int, batchBranch string, runID int64) {
	state, ok, err := DescribeBatchLock(ctx, repoSvc, batchNumber)
	if err != nil {
		fmt.Printf("Warning: failed to describe active batch lock for batch #%d: %s\n", batchNumber, err)
	}
	if ok {
		fmt.Printf("Integrator batch lock active; skipping batch=%d branch=%s run_id=%d owner=%s acquired_at=%s expires_at=%s lock_branch=%s\n",
			batchNumber, batchBranch, runID, state.Owner, formatReviewLockTime(state.AcquiredAt), formatReviewLockTime(state.ExpiresAt), BatchLockBranch(batchNumber))
		return
	}
	fmt.Printf("Integrator batch lock active; skipping batch=%d branch=%s run_id=%d lock_branch=%s\n",
		batchNumber, batchBranch, runID, BatchLockBranch(batchNumber))
}

func batchLockSkipReason(ctx context.Context, repoSvc platform.RepositoryService, batchNumber int, batchBranch string, runID int64) string {
	state, ok, err := DescribeBatchLock(ctx, repoSvc, batchNumber)
	if err == nil && ok {
		return fmt.Sprintf("Integrator batch lock active; skipping batch=%d branch=%s run_id=%d owner=%s acquired_at=%s expires_at=%s lock_branch=%s",
			batchNumber, batchBranch, runID, state.Owner, formatReviewLockTime(state.AcquiredAt), formatReviewLockTime(state.ExpiresAt), BatchLockBranch(batchNumber))
	}
	return fmt.Sprintf("Integrator batch lock active; skipping batch=%d branch=%s run_id=%d lock_branch=%s",
		batchNumber, batchBranch, runID, BatchLockBranch(batchNumber))
}
