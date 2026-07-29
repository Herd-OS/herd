package integrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustBatchLockCommitMessage(t *testing.T, state BatchLockState) string {
	t.Helper()
	msg, err := buildBatchLockCommitMessage(state)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(msg), &BatchLockState{}), "lock commit message must be valid JSON")
	assert.NotContains(t, msg, "//")
	assert.NotContains(t, msg, ",}")
	return msg
}

func TestAcquireBatchLock_Table(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	batchBranch := "herd/batch/1-batch"
	lockBranch := BatchLockBranch(1)
	expiredAt := now.Add(-time.Minute)
	acquiredAt := now.Add(-batchLockExpiry - time.Minute)
	activeState := lockedBatchLockState(1, batchBranch, 100, "active-lock", now.Add(-time.Minute))
	expiredState := BatchLockState{
		Kind:        "herd-batch-lock",
		Version:     1,
		Status:      "locked",
		LockID:      "expired-lock",
		BatchNumber: 1,
		BatchBranch: batchBranch,
		Owner:       "old-owner",
		AcquiredAt:  &acquiredAt,
		ExpiresAt:   &expiredAt,
	}

	tests := []struct {
		name       string
		setup      func(*mockRepoService)
		wantErr    bool
		wantAcquire bool
		wantHead    func(*testing.T, *mockRepoService)
	}{
		{
			name:        "first acquisition creates branch and locks",
			wantAcquire: true,
			wantHead: func(t *testing.T, repo *mockRepoService) {
				state := requireBatchLockHead(t, repo, 1)
				assert.Equal(t, "locked", state.Status)
				assert.Equal(t, int64(101), state.RunID)
				assert.Equal(t, batchBranch, state.BatchBranch)
			},
		},
		{
			name: "active same-batch lock blocks",
			setup: func(repo *mockRepoService) {
				repo.branchExists[lockBranch] = true
				repo.branchSHAs[lockBranch] = "active-sha"
				repo.commitMessages["active-sha"] = mustBatchLockCommitMessage(t, activeState)
			},
			wantAcquire: false,
			wantHead: func(t *testing.T, repo *mockRepoService) {
				assert.Equal(t, "active-sha", repo.branchSHAs[lockBranch])
			},
		},
		{
			name: "expired lock is reclaimed",
			setup: func(repo *mockRepoService) {
				repo.branchExists[lockBranch] = true
				repo.branchSHAs[lockBranch] = "expired-sha"
				repo.commitMessages["expired-sha"] = mustBatchLockCommitMessage(t, expiredState)
			},
			wantAcquire: true,
			wantHead: func(t *testing.T, repo *mockRepoService) {
				state := requireBatchLockHead(t, repo, 1)
				assert.Equal(t, "locked", state.Status)
				assert.NotEqual(t, "expired-lock", state.LockID)
				assert.Equal(t, "expired-sha", repo.commitParents[repo.branchSHAs[lockBranch]])
			},
		},
		{
			name: "ref update conflict retries",
			setup: func(repo *mockRepoService) {
				releasedAt := now.Add(-time.Minute)
				unlocked := BatchLockState{Kind: "herd-batch-lock", Version: 1, Status: "unlocked", BatchNumber: 1, BatchBranch: batchBranch, ReleasedAt: &releasedAt}
				repo.branchExists[lockBranch] = true
				repo.branchSHAs[lockBranch] = "unlocked-sha"
				repo.commitMessages["unlocked-sha"] = mustBatchLockCommitMessage(t, unlocked)
				repo.updateConflicts = 1
			},
			wantAcquire: true,
			wantHead: func(t *testing.T, repo *mockRepoService) {
				state := requireBatchLockHead(t, repo, 1)
				assert.Equal(t, "locked", state.Status)
				assert.GreaterOrEqual(t, repo.markerCommitSeq, 2)
			},
		},
		{
			name: "malformed lock state fails closed",
			setup: func(repo *mockRepoService) {
				repo.branchExists[lockBranch] = true
				repo.branchSHAs[lockBranch] = "bad-sha"
				repo.commitMessages["bad-sha"] = `{"kind":"herd-batch-lock","version":1,"status":"locked","batch_number":1`
			},
			wantErr: true,
			wantHead: func(t *testing.T, repo *mockRepoService) {
				assert.Equal(t, "bad-sha", repo.branchSHAs[lockBranch])
			},
		},
		{
			name: "lock branch creation race reads existing branch",
			setup: func(repo *mockRepoService) {
				releasedAt := now.Add(-time.Minute)
				unlocked := BatchLockState{Kind: "herd-batch-lock", Version: 1, Status: "unlocked", BatchNumber: 1, BatchBranch: batchBranch, ReleasedAt: &releasedAt}
				repo.onGetBranchSHA = func(name string) {
					if name == batchBranch && !repo.branchExists[lockBranch] {
						repo.branchExists[lockBranch] = true
						repo.branchSHAs[lockBranch] = "existing-sha"
						repo.commitMessages["existing-sha"] = mustBatchLockCommitMessage(t, unlocked)
					}
				}
			},
			wantAcquire: true,
			wantHead: func(t *testing.T, repo *mockRepoService) {
				state := requireBatchLockHead(t, repo, 1)
				assert.Equal(t, "locked", state.Status)
				assert.Equal(t, "existing-sha", repo.commitParents[repo.branchSHAs[lockBranch]])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newBatchLockTestRepo(batchBranch)
			if tt.setup != nil {
				tt.setup(repo)
			}
			handle, acquired, err := AcquireBatchLock(context.Background(), repo, 1, batchBranch, 101, now)
			if tt.wantErr {
				require.Error(t, err)
				assert.False(t, acquired)
				assert.Nil(t, handle)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantAcquire, acquired)
				if tt.wantAcquire {
					require.NotNil(t, handle)
					assert.Equal(t, lockBranch, handle.branch)
				}
			}
			if tt.wantHead != nil {
				tt.wantHead(t, repo)
			}
		})
	}
}

func TestReleaseBatchLock(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	batchBranch := "herd/batch/1-batch"
	repo := newBatchLockTestRepo(batchBranch)
	handle, acquired, err := AcquireBatchLock(context.Background(), repo, 1, batchBranch, 101, now)
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, ReleaseBatchLock(context.Background(), repo, handle))
	state := requireBatchLockHead(t, repo, 1)
	assert.Equal(t, "unlocked", state.Status)
	assert.Equal(t, handle.state.LockID, state.ReleasedLockID)

	next, acquired, err := AcquireBatchLock(context.Background(), repo, 1, batchBranch, 102, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, next)
	assert.NotEqual(t, handle.state.LockID, next.state.LockID)
}

func TestReleaseBatchLockOnlyUnlocksMatchingLockID(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	batchBranch := "herd/batch/1-batch"
	repo := newBatchLockTestRepo(batchBranch)
	active := lockedBatchLockState(1, batchBranch, 200, "new-lock", now)
	lockBranch := BatchLockBranch(1)
	repo.branchExists[lockBranch] = true
	repo.branchSHAs[lockBranch] = "active-sha"
	repo.commitMessages["active-sha"] = mustBatchLockCommitMessage(t, active)

	oldHandle := &BatchLockHandle{branch: lockBranch, state: lockedBatchLockState(1, batchBranch, 100, "old-lock", now)}
	require.NoError(t, ReleaseBatchLock(context.Background(), repo, oldHandle))
	state := requireBatchLockHead(t, repo, 1)
	assert.Equal(t, "locked", state.Status)
	assert.Equal(t, "new-lock", state.LockID)
}

func TestBatchLocksAreIndependentByBatchNumber(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	repo := newBatchLockTestRepo("herd/batch/1-batch")
	repo.branchExists["herd/batch/2-batch"] = true
	repo.branchSHAs["herd/batch/2-batch"] = "batch-2-sha"

	first, acquired, err := AcquireBatchLock(context.Background(), repo, 1, "herd/batch/1-batch", 101, now)
	require.NoError(t, err)
	require.True(t, acquired)
	second, acquired, err := AcquireBatchLock(context.Background(), repo, 2, "herd/batch/2-batch", 201, now)
	require.NoError(t, err)
	require.True(t, acquired)

	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.NotEqual(t, first.branch, second.branch)
	assert.True(t, repo.branchExists[BatchLockBranch(1)])
	assert.True(t, repo.branchExists[BatchLockBranch(2)])
}

func TestAcquireBatchLockFastForwardConflictSeesWinnerAndSkips(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	batchBranch := "herd/batch/1-batch"
	lockBranch := BatchLockBranch(1)
	repo := newBatchLockTestRepo(batchBranch)
	releasedAt := now.Add(-time.Minute)
	unlocked := BatchLockState{Kind: "herd-batch-lock", Version: 1, Status: "unlocked", BatchNumber: 1, BatchBranch: batchBranch, ReleasedAt: &releasedAt}
	repo.branchExists[lockBranch] = true
	repo.branchSHAs[lockBranch] = "unlocked-sha"
	repo.commitMessages["unlocked-sha"] = mustBatchLockCommitMessage(t, unlocked)
	repo.onUpdateBranch = func(name, sha string) {
		if name != lockBranch || repo.branchSHAs[lockBranch] != "unlocked-sha" {
			return
		}
		winnerState := lockedBatchLockState(1, batchBranch, 999, "winner-lock", now)
		winnerSHA, createErr := repo.CreateCommit(context.Background(), "unlocked-sha", mustBatchLockCommitMessage(t, winnerState))
		require.NoError(t, createErr)
		repo.branchSHAs[lockBranch] = winnerSHA
		_ = sha
	}

	handle, acquired, err := AcquireBatchLock(context.Background(), repo, 1, batchBranch, 101, now)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, handle)
	state := requireBatchLockHead(t, repo, 1)
	assert.Equal(t, "winner-lock", state.LockID)
}

func TestBatchLockCommitMessageIsJSONOnly(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	msg := mustBatchLockCommitMessage(t, lockedBatchLockState(1, "herd/batch/1-batch", 101, "lock", now))
	assert.True(t, strings.HasPrefix(msg, "{"))
	assert.True(t, strings.HasSuffix(msg, "}"))
	assert.NotContains(t, msg, "\n")
}

func requireBatchLockHead(t *testing.T, repo *mockRepoService, batchNumber int) BatchLockState {
	t.Helper()
	sha := repo.branchSHAs[BatchLockBranch(batchNumber)]
	require.NotEmpty(t, sha)
	state, ok := parseBatchLockCommitMessage(repo.commitMessages[sha])
	require.True(t, ok, "head %s message must parse: %q", sha, repo.commitMessages[sha])
	return state
}

func newBatchLockTestRepo(batchBranch string) *mockRepoService {
	return &mockRepoService{
		defaultBranch:   "main",
		branchExists:    map[string]bool{batchBranch: true},
		branchSHAs:      map[string]string{batchBranch: fmt.Sprintf("%s-sha", batchBranch)},
		commitMessages:  make(map[string]string),
		commitParents:   make(map[string]string),
		markerCommitSeq: 0,
	}
}

var _ platform.RepositoryService = (*mockRepoService)(nil)
